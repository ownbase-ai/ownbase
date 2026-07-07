package main

// base_checkup.go implements `ownbasectl checkup <name>` — one aggregated
// health report combining what else is spread across `status`, `security`,
// and `updates`: intrusion/access monitor, network exposure, CVE scan
// results, service update drift, and backup health. Each finding points at
// the specific command that fixes it.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newCheckupCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "checkup <name>",
		Short: "One health report: intrusions, exposure, CVEs, updates, backups",
		Long: `One aggregated health report combining intrusion/access monitoring,
network exposure, CVE scan results, service update drift, and backup
health — each finding paired with the exact command to fix it. Run this
regularly (weekly is reasonable).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBaseCheckup(args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON instead of a formatted report")
	return cmd
}

func runBaseCheckup(base string, jsonOut bool) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	body, err := apiGet(conn, "/status")
	if err != nil {
		return err
	}

	if jsonOut {
		fmt.Println(string(body))
		return nil
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
			fmt.Printf("  ⚠ %-42s  fix: %s\n", f.summary, f.fix)
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

	return nil
}

type checkupFinding struct {
	summary string
	fix     string
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
			// and working — what's missing is just the (periodic,
			// automatic) verify-restore drill, which re-running setup
			// would not skip ahead of and would misleadingly suggest is
			// the fix.
			lastBackup, _ := sec["last_backup"].(string)
			if lastBackup == "" {
				findings = append(findings, checkupFinding{
					summary: "Backups not configured",
					fix:     "ownbasectl backup setup " + base,
				})
			} else {
				findings = append(findings, checkupFinding{
					summary: "Backups not yet verified restorable",
					fix:     "ownbasectl backup status " + base + "  (verify-restore drill runs automatically)",
				})
			}
		}

		if exp, ok := sec["exposure"].(map[string]any); ok {
			if available, _ := exp["available"].(bool); available {
				if fwActive, _ := exp["firewall_active"].(bool); !fwActive {
					findings = append(findings, checkupFinding{
						summary: "Firewall (UFW) is not active",
						fix:     "ownbasectl security " + base,
					})
				}
				if unexpected, _ := exp["unexpected_count"].(float64); unexpected > 0 {
					findings = append(findings, checkupFinding{
						summary: fmt.Sprintf("%d unexpected internet-reachable port(s)", int(unexpected)),
						fix:     "ownbasectl security " + base,
					})
				}
			}
		}

		if acc, ok := sec["access"].(map[string]any); ok {
			if available, _ := acc["available"].(bool); available {
				if bannedRaw, _ := acc["banned_ips"].([]any); len(bannedRaw) > 0 {
					findings = append(findings, checkupFinding{
						summary: fmt.Sprintf("%d banned IP(s) from failed SSH logins", len(bannedRaw)),
						fix:     "ownbasectl security " + base,
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
							summary: fmt.Sprintf("%d critical, %d high CVE(s) on host OS", int(critical), int(high)),
							fix:     fix,
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
						summary: fmt.Sprintf("CVE scan failed for service %q", svc),
						fix:     "ownbasectl security " + base + "  (see error in Vulnerability Scan section)",
					})
				}
			}
		}

		if driftCount, _ := sec["drift_count"].(float64); driftCount > 0 {
			findings = append(findings, checkupFinding{
				summary: fmt.Sprintf("%d runtime file(s) drifted from desired state", int(driftCount)),
				fix:     "ownbasectl plan",
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
					summary: fmt.Sprintf("%d service(s) behind their source repo", behind),
					fix:     "ownbasectl updates " + base,
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
	return nil
}
