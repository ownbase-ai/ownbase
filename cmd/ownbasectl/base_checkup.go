package main

// base_checkup.go implements `ownbasectl checkup <name>` — one aggregated
// health report combining what else is spread across `status`, `security`,
// and `updates`: intrusion/access monitor, network exposure, CVE scan
// results, service update drift, and backup health. Each finding points at
// the specific command that fixes it.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newCheckupCmd() *cobra.Command {
	var jsonOut bool
	var doVerify bool
	cmd := &cobra.Command{
		Use:   "checkup <name>",
		Short: "One health report: intrusions, exposure, CVEs, updates, backups",
		Long: `One aggregated health report combining intrusion/access monitoring,
network exposure, CVE scan results, service update drift, and backup
health — each finding paired with the exact command to fix it. Run this
regularly (weekly is reasonable).

With --verify, the verified-restore drill runs first: the Base restores its
newest snapshot into an isolated directory, checks it, and (when Postgres is
in the backup) starts a real database from it. That takes minutes and streams
its progress. Without --verify the report shows the result of the last drill
the Base ran on its own schedule, which may be up to core.backup.verify_interval
old.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBaseCheckup(args[0], jsonOut, doVerify)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON instead of a formatted report")
	cmd.Flags().BoolVar(&doVerify, "verify", false, "run the verified-restore drill now instead of reporting the last scheduled one (takes minutes)")
	return cmd
}

func runBaseCheckup(base string, jsonOut, doVerify bool) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()
	return checkup(conn, base, jsonOut, doVerify)
}

func checkup(conn *connection, base string, jsonOut, doVerify bool) error {
	// The drill is what sets Restorable, so it must finish before /status is
	// read or the report would show the previous drill's verdict. A failed
	// drill is not fatal to the checkup: the report that follows is exactly
	// what an operator wants to see next, so the failure is noted and the
	// report still prints.
	var (
		verifyErr    error
		verifyResult string
	)
	if doVerify {
		verifyResult, verifyErr = runVerifyDrill(conn, jsonOut)
		if verifyErr != nil && !jsonOut {
			fmt.Printf("\n  ⚠ %v\n", verifyErr)
		}
	}

	body, err := apiGet(conn, "/status")
	if err != nil {
		return err
	}

	if jsonOut {
		printCheckupJSON(base, body, verifyResult)
		// Same verdict as the formatted path below: a failed drill exits
		// non-zero. Its message goes to stderr rather than into the payload,
		// which is what keeps stdout a document rather than a report.
		return verifyErr
	}

	fmt.Println("╔════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║                       OwnBase Checkup                                ║")
	fmt.Println("╚════════════════════════════════════════════════════════════════════╝")
	fmt.Println()

	findings := checkupFindings(base, body)
	if len(findings) == 0 {
		fmt.Println("  ✓ All clear — no issues found.")
	} else {
		fmt.Printf("  %d finding(s) need attention:\n\n", len(findings))
		for _, f := range findings {
			fmt.Printf("  ⚠ %-42s  fix: %s\n", f.Summary, f.Fix)
		}
	}
	fmt.Println()

	// Reuse the existing detailed renderers for the sections that already
	// have one — this keeps `checkup` and `security`/`updates` in sync with
	// a single source of truth instead of duplicating formatting logic.
	if err := printBackupCheckupSection(base, body); err != nil {
		fmt.Printf("  backup status: %v\n", err)
	}
	fmt.Println()
	if err := printSecurityReport(base, body); err != nil {
		fmt.Printf("  security report: %v\n", err)
	}
	fmt.Println()
	if err := printUpdatesSummary(base, body); err != nil {
		fmt.Printf("  updates summary: %v\n", err)
	}

	return verifyErr
}

// printCheckupJSON writes one document: the findings this command decided on,
// the drill result when one ran, and the raw status the verdict came from.
//
// The findings belong in the payload because deciding what counts as a problem
// is this command's whole job. Leaving them out would force every other reader
// — the desktop app most of all — to reimplement checkupFindings and slowly
// disagree with it about whether a Base is healthy. `status --json` is still
// the bare /status body for anything that only wants the machine's own words.
func printCheckupJSON(base string, status []byte, verifyResult string) {
	doc := struct {
		Findings []checkupFinding `json:"findings"`
		Verify   json.RawMessage  `json:"verify,omitempty"`
		Status   json.RawMessage  `json:"status"`
	}{
		Findings: checkupFindings(base, status),
		Status:   json.RawMessage(status),
	}
	if v := strings.TrimSpace(verifyResult); v != "" {
		doc.Verify = json.RawMessage(v)
	}
	// findings is a list, not "maybe a list": an all-clear Base reports zero
	// findings rather than null, so a caller can count them without a nil check.
	if doc.Findings == nil {
		doc.Findings = []checkupFinding{}
	}

	combined, err := json.Marshal(doc)
	if err != nil {
		// The status half is not this CLI's to rewrite, so an unmarshalable
		// document degrades to passing it through rather than printing nothing.
		fmt.Println(strings.TrimSpace(string(status)))
		return
	}
	fmt.Println(string(combined))
}

// runVerifyDrill triggers the verified-restore drill on the Base and streams
// its progress, mirroring how `upgrade` and `security fix` consume their
// long-running endpoints.
//
// The daemon ends with a ---RESULT--- JSON trailer either way, and ---OK---
// only when every check passed. The trailer is what makes a failure
// actionable: without the per-check breakdown, "not restorable" says the
// backups cannot be proven good without saying which part is not.
//
// Returns that trailer verbatim so --json can fold it into the one document the
// caller prints, rather than printing a second one here.
func runVerifyDrill(conn *connection, jsonOut bool) (string, error) {
	verifyURL := conn.baseURL + "/backup/verify"
	req, err := http.NewRequest(http.MethodPost, verifyURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	if conn.token != "" {
		req.Header.Set("Authorization", "Bearer "+conn.token)
	}

	if !jsonOut {
		fmt.Println("Verified-restore drill")
		fmt.Println(strings.Repeat("─", 68))
	}

	// The drill restores a full snapshot and may start a Postgres container,
	// so it is bounded generously rather than by a typical request timeout.
	client := &http.Client{Timeout: 60 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("backup/verify API at %s: %w\n  Is the agent running?", verifyURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return "", fmt.Errorf("unauthorized — the cached token may be stale; remove the profile and run 'ownbasectl adopt' again")
	case http.StatusNotImplemented:
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s", strings.TrimSpace(string(body)))
	default:
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("backup/verify returned %d: %s", resp.StatusCode, body)
	}

	var gotOK bool
	var result struct {
		Passed bool `json:"passed"`
		Checks []struct {
			Name   string `json:"name"`
			Passed bool   `json:"passed"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	var resultJSON string

	scanner := bufio.NewScanner(resp.Body)
	// A check's detail can carry a long command error; the default 64 KiB
	// token limit would turn that into a truncated-looking stream.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "---OK---":
			gotOK = true
		case strings.HasPrefix(line, "---RESULT---"):
			resultJSON = strings.TrimPrefix(line, "---RESULT---")
			_ = json.Unmarshal([]byte(resultJSON), &result)
		case !jsonOut:
			fmt.Println(line)
		}
	}
	if err := scanner.Err(); err != nil {
		return resultJSON, err
	}

	if gotOK {
		if !jsonOut {
			fmt.Println("\n  ✓ Restore verified — every check passed.")
			fmt.Println()
		}
		return resultJSON, nil
	}

	// Name the failures in the error itself, so a scripted caller that only
	// keeps the error still learns which check failed.
	var failed []string
	for _, ch := range result.Checks {
		if !ch.Passed {
			failed = append(failed, fmt.Sprintf("%s (%s)", ch.Name, ch.Detail))
		}
	}
	if len(failed) > 0 {
		return resultJSON, fmt.Errorf("verified-restore drill failed: %s", strings.Join(failed, "; "))
	}
	return resultJSON, fmt.Errorf("verified-restore drill did not complete — see output above")
}

// checkupFinding is one thing worth a person's attention, paired with the
// command that addresses it. The fix is part of the finding rather than
// something the reader has to work out, which is the difference between a
// report and a to-do list.
type checkupFinding struct {
	Summary string `json:"summary"`
	Fix     string `json:"fix"`
}

// checkupFindings scans the raw status JSON for anything worth flagging at
// the top of the report, each paired with the exact command to address it.
func checkupFindings(base string, body []byte) []checkupFinding {
	var s map[string]any
	if err := json.Unmarshal(body, &s); err != nil {
		return nil
	}
	var findings []checkupFinding

	// sec may be nil (e.g. a status payload from an agent build that
	// predates the security section). All security-derived checks live in
	// this block so a missing section simply yields none of them — the
	// updates.drift scan below must still run either way, so it is
	// deliberately outside this block, not behind an early return.
	if sec, ok := s["security"].(map[string]any); ok {
		if restorable, _ := sec["backup_restorable"].(bool); !restorable {
			// Only point at `backup setup` when backups have never run
			// at all. If a snapshot already exists, backups are configured
			// and working — what's missing is just the verify-restore
			// drill, which re-running setup would not skip ahead of and
			// would misleadingly suggest is the fix.
			lastBackup, _ := sec["last_backup"].(string)
			if lastBackup == "" {
				findings = append(findings, checkupFinding{
					Summary: "Backups not configured",
					Fix:     "ownbasectl backup setup " + base,
				})
			} else {
				findings = append(findings, checkupFinding{
					Summary: "Backups not yet verified restorable",
					Fix:     "ownbasectl checkup " + base + " --verify",
				})
			}
		}

		if exp, ok := sec["exposure"].(map[string]any); ok {
			if available, _ := exp["available"].(bool); available {
				if fwActive, _ := exp["firewall_active"].(bool); !fwActive {
					findings = append(findings, checkupFinding{
						Summary: "Firewall (UFW) is not active",
						Fix:     "ownbasectl security " + base,
					})
				}
				if unexpected, _ := exp["unexpected_count"].(float64); unexpected > 0 {
					findings = append(findings, checkupFinding{
						Summary: fmt.Sprintf("%d unexpected internet-reachable port(s)", int(unexpected)),
						Fix:     "ownbasectl security " + base,
					})
				}
			}
		}

		if acc, ok := sec["access"].(map[string]any); ok {
			if available, _ := acc["available"].(bool); available {
				if bannedRaw, _ := acc["banned_ips"].([]any); len(bannedRaw) > 0 {
					findings = append(findings, checkupFinding{
						Summary: fmt.Sprintf("%d banned IP(s) from failed SSH logins", len(bannedRaw)),
						Fix:     "ownbasectl security " + base,
					})
				}
			}
		}

		if vulns, ok := sec["vulns"].(map[string]any); ok {
			if available, _ := vulns["available"].(bool); available {
				if host, ok := vulns["host"].(map[string]any); ok {
					critical, _ := host["critical"].(float64)
					fixCrit, _ := host["fixable_critical"].(float64)
					high, _ := host["high"].(float64)
					fixHigh, _ := host["fixable_high"].(float64)
					if critical+high > 0 {
						fix := "ownbasectl security fix " + base
						if int(fixCrit+fixHigh) == 0 {
							fix = "ownbasectl security " + base + "  (no fix available yet)"
						}
						findings = append(findings, checkupFinding{
							Summary: fmt.Sprintf("%d critical, %d high CVE(s) on host OS", int(critical), int(high)),
							Fix:     fix,
						})
					}
				}
			}

			// Flag any image whose trivy scan failed so operators know a
			// service is unscanned rather than clean.
			imagesRaw, _ := vulns["images"].([]any)
			for _, raw := range imagesRaw {
				img, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if failed, _ := img["scan_failed"].(bool); failed {
					svc, _ := img["service"].(string)
					findings = append(findings, checkupFinding{
						Summary: fmt.Sprintf("CVE scan failed for service %q", svc),
						Fix:     "ownbasectl security " + base + "  (see error in Vulnerability Scan section)",
					})
				}
			}
		}

		if driftCount, _ := sec["drift_count"].(float64); driftCount > 0 {
			findings = append(findings, checkupFinding{
				Summary: fmt.Sprintf("%d runtime file(s) drifted from desired state", int(driftCount)),
				Fix:     "ownbasectl plan",
			})
		}
	}

	if updates, ok := s["updates"].(map[string]any); ok {
		if drift, ok := updates["drift"].([]any); ok {
			behind := 0
			for _, raw := range drift {
				d, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if upToDate, _ := d["up_to_date"].(bool); !upToDate {
					behind++
				}
			}
			if behind > 0 {
				findings = append(findings, checkupFinding{
					Summary: fmt.Sprintf("%d service(s) behind their source repo", behind),
					Fix:     "ownbasectl updates " + base,
				})
			}
		}
	}

	return findings
}

// printBackupCheckupSection renders the compact backup-health block at the
// top of the checkup report.
func printBackupCheckupSection(base string, body []byte) error {
	var s struct {
		Security struct {
			BackupRestorable bool   `json:"backup_restorable"`
			LastBackup       string `json:"last_backup"`
			LastVerified     string `json:"last_verified"`
		} `json:"security"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return err
	}
	fmt.Println("  Backups")
	fmt.Println("  " + strings.Repeat("─", 68))
	if s.Security.LastBackup == "" {
		fmt.Printf("    Last backup:   never — run 'ownbasectl backup setup %s'\n", base)
	} else {
		fmt.Printf("    Last backup:   %s\n", shortTime(s.Security.LastBackup))
	}
	restorable := "✗ not yet verified"
	if s.Security.BackupRestorable {
		restorable = "✓ restorable"
	}
	fmt.Printf("    Restorable:    %s\n", restorable)
	if s.Security.LastVerified == "" {
		if s.Security.LastBackup != "" {
			fmt.Printf("    Last drill:    never — run 'ownbasectl checkup %s --verify'\n", base)
		}
	} else {
		fmt.Printf("    Last drill:    %s\n", shortTime(s.Security.LastVerified))
	}
	return nil
}
