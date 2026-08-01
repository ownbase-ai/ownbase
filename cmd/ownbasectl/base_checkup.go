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
	// Backfill the vault profile (and inject status.config for older daemons)
	// so the app does not claim "config not set up" when the Base is tracking
	// a real repo. Same path as fetchStatusBody.
	body = ensureConfigKnown(base, body)

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

// Action kinds on a checkupFinding. The CLI decides which one applies so the
// desktop app never re-implements the rule — it just switches on Kind.
const (
	// actionRun: the app (or CLI) can finish this itself. Run is the
	// ownbasectl subcommand path without the binary name or base
	// (e.g. "security fix"); the caller appends the base name.
	actionRun = "run"
	// actionOpen: there is nothing to execute — open the named tab and read.
	actionOpen = "open"
	// actionForm: the app opens a named form flow (backup setup, deploy)
	// that still ends in a CLI call. Preview=true means dry-run the edit
	// and show the diff before committing.
	actionForm = "form"
	// "manual" is also a valid kind (genuine dead-end, plain text only) but
	// no finding emits it today — every case is run/open/form. The app still
	// knows how to render it if one appears.
)

// checkupAction tells a reader (terminal or desktop app) how to address a
// finding. Kind is the discriminator; the other fields are only meaningful
// for specific kinds.
type checkupAction struct {
	// Kind is one of actionRun / actionOpen / actionForm / actionManual.
	Kind string `json:"kind"`
	// Run is the ownbasectl subcommand path for kind=run, without the binary
	// name or the base (e.g. "security fix", "self-update").
	Run string `json:"run,omitempty"`
	// Tab is the desktop app tab to open for kind=open
	// ("security" | "backups" | "updates").
	Tab string `json:"tab,omitempty"`
	// Form names the app flow for kind=form
	// ("backup-setup" | "deploy" | "config-setup").
	Form string `json:"form,omitempty"`
	// Service is the service the form targets (deploy).
	Service string `json:"service,omitempty"`
	// SuggestedRef is a default --ref for deploy (newest_tag or branch).
	SuggestedRef string `json:"suggested_ref,omitempty"`
	// Preview means the app must dry-run and show the diff before committing.
	Preview bool `json:"preview,omitempty"`
	// Label is the button text. Always set.
	Label string `json:"label"`
	// Confirm is optional prose shown before a run that has a cost (reboot).
	Confirm string `json:"confirm,omitempty"`
}

// checkupFinding is one thing worth a person's attention, paired with the
// action that addresses it. The action is part of the finding rather than
// something the reader has to work out, which is the difference between a
// report and a to-do list.
//
// Fix is the full command string kept for terminal output and older clients.
// Action is what the desktop app switches on.
type checkupFinding struct {
	Summary string        `json:"summary"`
	Fix     string        `json:"fix"`
	Action  checkupAction `json:"action"`
}

// scanStaleAfter is how old a successful CVE scan may be before the finding
// becomes "we don't know". Twice the daemon's 24h cadence — one missed tick
// is not yet a problem; two is.
const scanStaleAfter = 48 * time.Hour

// checkupFindings scans the raw status JSON for anything worth flagging at
// the top of the report. The panel's contract: only things a person can
// finish. Unfixed CVEs and "banned IPs" (fail2ban working) are readings on
// the Security tab, not to-dos.
func checkupFindings(base string, body []byte) []checkupFinding {
	var s map[string]any
	if err := json.Unmarshal(body, &s); err != nil {
		return nil
	}
	var findings []checkupFinding

	// Config source is the first thing a fresh Base needs. Without it the
	// daemon has nothing to reconcile and services cannot be declared.
	// Wizard create intentionally stops before this step — surface it as a
	// form, not a scary missing-tools message on the Security tab.
	_, hasConfig := s["config"].(map[string]any)
	if !hasConfig {
		findings = append(findings, checkupFinding{
			Summary: "Config repo not set up — nothing is declared to run yet",
			Fix:     "ownbasectl config setup " + base + " --repo <git-url> --init",
			Action: checkupAction{
				Kind:    actionForm,
				Form:    "config-setup",
				Preview: false,
				Label:   "Set up config repo",
			},
		})
	}

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
			//
			// Also require a config repo: backup setup commits core.backup
			// into ownbase.yaml, so without config the form cannot finish.
			lastBackup, _ := sec["last_backup"].(string)
			if lastBackup == "" {
				if hasConfig {
					findings = append(findings, checkupFinding{
						Summary: "Backups not configured",
						Fix:     "ownbasectl backup setup " + base + " --repo <url> --password <pw>",
						Action: checkupAction{
							Kind:    actionForm,
							Form:    "backup-setup",
							Preview: true,
							Label:   "Set up backups",
						},
					})
				}
			} else {
				findings = append(findings, checkupFinding{
					Summary: "Backups not yet verified restorable",
					Fix:     "ownbasectl checkup " + base + " --verify",
					Action: checkupAction{
						Kind:  actionOpen,
						Tab:   "backups",
						Label: "Go to Backups",
					},
				})
			}
		}

		if exp, ok := sec["exposure"].(map[string]any); ok {
			if available, _ := exp["available"].(bool); available {
				if fwActive, _ := exp["firewall_active"].(bool); !fwActive {
					findings = append(findings, checkupFinding{
						Summary: "Firewall (UFW) is not active",
						Fix:     "ownbasectl security " + base,
						Action: checkupAction{
							Kind:  actionOpen,
							Tab:   "security",
							Label: "Go to Security",
						},
					})
				}
				if unexpected, _ := exp["unexpected_count"].(float64); unexpected > 0 {
					findings = append(findings, checkupFinding{
						Summary: fmt.Sprintf("%d unexpected internet-reachable port(s)", int(unexpected)),
						Fix:     "ownbasectl security " + base,
						Action: checkupAction{
							Kind:  actionOpen,
							Tab:   "security",
							Label: "Go to Security",
						},
					})
				}
			}
		}

		// Banned IPs are fail2ban working, not a to-do. They render on the
		// Security tab; they do not raise a finding.

		if reboot, _ := sec["reboot_required"].(bool); reboot {
			findings = append(findings, checkupFinding{
				Summary: "Host reboot required for applied packages to take effect",
				Fix:     "ownbasectl security reboot " + base,
				Action: checkupAction{
					Kind:    actionRun,
					Run:     "security reboot",
					Label:   "Reboot now",
					Confirm: "Every service on this Base will stop and restart with the machine. The outage is typically 30–60 seconds.",
				},
			})
		}

		if vulns, ok := sec["vulns"].(map[string]any); ok {
			trivyInstalled, _ := vulns["trivy_installed"].(bool)
			available, _ := vulns["available"].(bool)
			scannedAt, _ := vulns["scanned_at"].(string)

			switch {
			case !trivyInstalled:
				findings = append(findings, checkupFinding{
					Summary: "CVE scanner not installed — host and services are unchecked",
					Fix:     "ownbasectl security install-scanner " + base,
					Action: checkupAction{
						Kind:  actionRun,
						Run:   "security install-scanner",
						Label: "Install scanner",
					},
				})
			case !available:
				// Distinguish "first scan still pending" (daemon waits ~5 min
				// after start so reconcile can finish) from a real failure.
				// Both are unknown-not-clean, but the copy must not imply the
				// scanner already tried and lost.
				hostScanError, _ := vulns["host_scan_error"].(string)
				summary := "CVE scan still pending — unknown, not clean"
				label := "Scan now"
				if hostScanError != "" {
					summary = "Host CVE scan failed — unknown, not clean"
					label = "Rescan"
				} else if scannedAt != "" {
					summary = "Host CVE scan has not succeeded — unknown, not clean"
					label = "Rescan"
				}
				findings = append(findings, checkupFinding{
					Summary: summary,
					Fix:     "ownbasectl security scan " + base,
					Action: checkupAction{
						Kind:  actionRun,
						Run:   "security scan",
						Label: label,
					},
				})
			default:
				// available == true. Staleness and fixable CVEs are independent:
				// a 49-hour-old scan that still lists patches is both "rescan"
				// and "apply patches". Treating stale as exclusive of available
				// used to hide Apply patches while per-image findings (below)
				// still fired from the same payload.
				if scannedAt != "" && scanIsStale(scannedAt) {
					findings = append(findings, checkupFinding{
						Summary: "CVE scan is more than 48 hours old",
						Fix:     "ownbasectl security scan " + base,
						Action: checkupAction{
							Kind:  actionRun,
							Run:   "security scan",
							Label: "Rescan",
						},
					})
				}
				// Only fixable CVEs raise a finding. Unfixed counts live on
				// the Security tab: there is nothing a person can finish.
				if host, ok := vulns["host"].(map[string]any); ok {
					fixCrit, _ := host["fixable_critical"].(float64)
					fixHigh, _ := host["fixable_high"].(float64)
					if n := int(fixCrit + fixHigh); n > 0 {
						findings = append(findings, checkupFinding{
							Summary: fmt.Sprintf("%d host CVE(s) have a patch available (%d critical, %d high)",
								n, int(fixCrit), int(fixHigh)),
							Fix: "ownbasectl security fix " + base,
							Action: checkupAction{
								Kind:  actionRun,
								Run:   "security fix",
								Label: "Apply patches",
							},
						})
					}
				}
			}

			// Per-image findings: a service with fixable CVEs, or a scan that
			// failed (unscanned ≠ clean).
			imagesRaw, _ := vulns["images"].([]any)
			for _, raw := range imagesRaw {
				img, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				svc, _ := img["service"].(string)
				if failed, _ := img["scan_failed"].(bool); failed {
					findings = append(findings, checkupFinding{
						Summary: fmt.Sprintf("CVE scan failed for service %q", svc),
						Fix:     "ownbasectl security " + base,
						Action: checkupAction{
							Kind:  actionOpen,
							Tab:   "security",
							Label: "Go to Security",
						},
					})
					continue
				}
				summary, _ := img["summary"].(map[string]any)
				if summary == nil {
					continue
				}
				fixCrit, _ := summary["fixable_critical"].(float64)
				fixHigh, _ := summary["fixable_high"].(float64)
				n := int(fixCrit + fixHigh)
				if n == 0 {
					continue
				}
				if strings.HasPrefix(svc, "ownbase-core-") {
					// Local-build daemons rebuild Caddy via upgrade --apply.
					// Registry-pinned daemons only re-pull the same digest —
					// they need self-update first to pick up the Dockerfile.
					imgRef, _ := img["image"].(string)
					if coreNeedsSelfUpdate(s, imgRef) {
						findings = append(findings, checkupFinding{
							Summary: fmt.Sprintf("%d CVE(s) with a patch in core image %q — daemon must self-update first", n, svc),
							Fix:     "ownbasectl self-update " + base,
							Action: checkupAction{
								Kind:    actionRun,
								Run:     "self-update",
								Label:   "Update OwnBase",
								Confirm: "Replaces the OwnBase daemon with the latest signed release (~10s restart). Then use Rebuild Caddy (or upgrade --apply) so the hardened image is built.",
							},
						})
					} else {
						findings = append(findings, checkupFinding{
							Summary: fmt.Sprintf("%d CVE(s) with a patch in core image %q", n, svc),
							Fix:     "ownbasectl upgrade " + base + " --apply",
							Action: checkupAction{
								Kind:    actionRun,
								Run:     "upgrade --apply",
								Label:   "Rebuild Caddy",
								Confirm: "Rebuilds the hardened Caddy image on this Base (downloads Go toolchains on first build) and restarts the proxy. Brief interruption to HTTPS.",
							},
						})
					}
					continue
				}
				// User services: deploy a newer ref only when that ref would
				// actually move the pin. If drift says up_to_date (or the
				// suggested ref matches the pinned one), deploy is a no-op and
				// the finding is not finishable — leave it off Overview.
				svcKey := strings.TrimPrefix(svc, "ownbase-")
				suggested, canDeploy := suggestedDeployRef(s, svcKey)
				if !canDeploy {
					continue
				}
				findings = append(findings, checkupFinding{
					Summary: fmt.Sprintf("%d CVE(s) with a patch in image for %q", n, svc),
					Fix:     fmt.Sprintf("ownbasectl deploy %s %s --ref %s", base, svcKey, suggested),
					Action: checkupAction{
						Kind:         actionForm,
						Form:         "deploy",
						Service:      svcKey,
						SuggestedRef: suggested,
						Preview:      true,
						Label:        "Update " + svcKey,
					},
				})
			}
		}

		if driftCount, _ := sec["drift_count"].(float64); driftCount > 0 {
			findings = append(findings, checkupFinding{
				Summary: fmt.Sprintf("%d runtime file(s) drifted from desired state", int(driftCount)),
				Fix:     "ownbasectl plan",
				Action: checkupAction{
					Kind:  actionOpen,
					Tab:   "security",
					Label: "Go to Security",
				},
			})
		}
	}

	if updates, ok := s["updates"].(map[string]any); ok {
		if drift, ok := updates["drift"].([]any); ok {
			// One finding per behind service, each with a deploy form — so
			// the operator can finish it without going to the Updates tab
			// and copying a command.
			behindCount := 0
			for _, raw := range drift {
				d, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if upToDate, _ := d["up_to_date"].(bool); upToDate {
					continue
				}
				behindCount++
				svc, _ := d["service"].(string)
				suggested := ""
				if tag, _ := d["newest_tag"].(string); tag != "" {
					suggested = tag
				} else if branch, _ := d["branch"].(string); branch != "" {
					suggested = branch
				} else {
					suggested = "main"
				}
				behind, _ := d["commits_behind"].(float64)
				findings = append(findings, checkupFinding{
					Summary: fmt.Sprintf("%s is %d commit(s) behind its source repo", svc, int(behind)),
					Fix:     fmt.Sprintf("ownbasectl deploy %s %s --ref %s", base, svc, suggested),
					Action: checkupAction{
						Kind:         actionForm,
						Form:         "deploy",
						Service:      svc,
						SuggestedRef: suggested,
						Preview:      true,
						Label:        "Update " + svc,
					},
				})
			}
			_ = behindCount
		}
	}

	return findings
}

// suggestedDeployRef picks a default --ref for a service from updates.drift.
// canDeploy is false when a deploy of that ref would be a no-op (already
// pinned / up_to_date), so callers can skip an unfinishable finding.
func suggestedDeployRef(status map[string]any, service string) (ref string, canDeploy bool) {
	updates, _ := status["updates"].(map[string]any)
	if updates == nil {
		// No drift data yet — still allow the form; deploy resolves via
		// git ls-remote and no-ops honestly if already current.
		return "main", true
	}
	drift, _ := updates["drift"].([]any)
	for _, raw := range drift {
		d, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if svc, _ := d["service"].(string); svc != service {
			continue
		}
		if upToDate, _ := d["up_to_date"].(bool); upToDate {
			return "", false
		}
		pinned, _ := d["ref"].(string)
		suggested := ""
		if tag, _ := d["newest_tag"].(string); tag != "" {
			suggested = tag
		} else if branch, _ := d["branch"].(string); branch != "" {
			suggested = branch
		} else {
			suggested = "main"
		}
		// If the suggested ref is already what is pinned (branch name or
		// matching SHA), deploy would be unchanged.
		if pinned != "" && (pinned == suggested || strings.HasPrefix(pinned, suggested)) {
			return suggested, false
		}
		return suggested, true
	}
	// Service not in drift list (blank ref, or detection never ran).
	return "main", true
}

// coreNeedsSelfUpdate is true only when the daemon is too old to rebuild
// Caddy locally. New daemons always report status.version and embed the
// hardened Dockerfile — their upgrade --apply rebuilds even if the running
// container is still a registry image (background first build pending/failed).
//
// Old daemons omit version and only know how to pull a pinned digest, so
// upgrade --apply is a no-op for CVEs; they must self-update first.
func coreNeedsSelfUpdate(status map[string]any, imageRef string) bool {
	// Already on the local hardened tag — rebuild path is live.
	if strings.Contains(imageRef, "localhost/ownbase-core-caddy") {
		return false
	}
	// version is set by every daemon that can BuildCaddyImage (including "dev").
	if ver, ok := status["version"].(string); ok && ver != "" {
		return false
	}
	// No version + still a registry (or unknown) image → migration required.
	return true
}

// scanIsStale reports whether a scanned_at timestamp is older than
// scanStaleAfter. Unparseable or empty timestamps are not stale — the
// available=false branch already covers "no successful scan".
func scanIsStale(scannedAt string) bool {
	t, err := time.Parse(time.RFC3339, scannedAt)
	if err != nil {
		// trivy / encoding may emit RFC3339Nano
		t, err = time.Parse(time.RFC3339Nano, scannedAt)
		if err != nil {
			return false
		}
	}
	return time.Since(t) > scanStaleAfter
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
