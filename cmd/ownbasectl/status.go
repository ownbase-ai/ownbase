package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Query the running agent's status API",
		Long: `Query the Base's daemon for a summary of services, security posture,
and recent actions, over its SSH tunnel.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := fetchStatusBody(args[0], 5*time.Second)
			if err != nil {
				return err
			}
			if jsonOut {
				fmt.Println(string(body))
				return nil
			}
			return printStatusSummary(args[0], body)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON instead of formatted summary")
	return cmd
}

// printStatusSummary renders a human-readable summary from the JSON status body.
func printStatusSummary(base string, body []byte) error {
	// Use a loose map so we don't import internal/explain in the CLI.
	var s map[string]any
	if err := json.Unmarshal(body, &s); err != nil {
		return fmt.Errorf("parse status JSON: %w", err)
	}

	fmt.Println("─────────────────────────── Base Status ───────────────────────────")

	if ts, ok := s["generated_at"].(string); ok {
		fmt.Printf("  As of:    %s\n", ts)
	}
	if ver, ok := s["schema_version"].(string); ok {
		fmt.Printf("  Schema:   %s\n", ver)
	}

	fmt.Println()
	fmt.Println("  Services:")
	if svcs, ok := s["services"].([]any); ok {
		for _, raw := range svcs {
			svc, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			name, _ := svc["name"].(string)
			running, _ := svc["running"].(bool)
			healthy, _ := svc["healthy"].(bool)
			ref, _ := svc["ref"].(string)
			image, _ := svc["image"].(string)
			domain, _ := svc["domain"].(string)

			status := "✗ stopped"
			if running {
				status = "✓ running"
			}
			if running && !healthy {
				status = "⚠ running (unhealthy)"
			}

			line := fmt.Sprintf("    %-20s  %s", name, status)
			if ref != "" {
				line += fmt.Sprintf("  [@ %s]", ref)
			} else if image != "" {
				line += fmt.Sprintf("  [image: %s]", image)
			}
			if domain != "" {
				line += fmt.Sprintf("  → https://%s", domain)
			}
			fmt.Println(line)

			if provides, ok := svc["provides"].([]any); ok && len(provides) > 0 {
				caps := make([]string, 0, len(provides))
				for _, c := range provides {
					if cs, ok := c.(string); ok {
						caps = append(caps, cs)
					}
				}
				fmt.Printf("    %20s    provides: %s\n", "", strings.Join(caps, ", "))
			}
		}
	}

	fmt.Println()
	fmt.Println("  Security:")
	if sec, ok := s["security"].(map[string]any); ok {
		backupOK, _ := sec["backup_restorable"].(bool)
		driftOK := true
		if d, ok := sec["drift_detected"].(bool); ok {
			driftOK = !d
		}
		driftCount, _ := sec["drift_count"].(float64)

		bStatus := "✗ not verified"
		if backupOK {
			bStatus = "✓ restorable"
		}
		fmt.Printf("    Backup:   %s\n", bStatus)

		dStatus := "✓ clean"
		if !driftOK {
			dStatus = fmt.Sprintf("⚠ %d file(s) drifted", int(driftCount))
		}
		fmt.Printf("    Drift:    %s\n", dStatus)

		// Network exposure verdict.
		if exp, ok := sec["exposure"].(map[string]any); ok {
			available, _ := exp["available"].(bool)
			if !available {
				fmt.Printf("    Exposure: (scan not available on this platform)\n")
			} else {
				fwActive, _ := exp["firewall_active"].(bool)
				unexpected, _ := exp["unexpected_count"].(float64)
				fwStr := "firewall active"
				if !fwActive {
					fwStr = "WARN: firewall not active"
				}
				if int(unexpected) == 0 {
					fmt.Printf("    Exposure: ✓ %s; no unexpected ports\n", fwStr)
				} else {
					fmt.Printf("    Exposure: ⚠ %s; %d unexpected internet-reachable port(s) — run 'ownbasectl security %s' for details\n",
						fwStr, int(unexpected), base)
				}
			}
		}

		// SSH access monitor verdict.
		if acc, ok := sec["access"].(map[string]any); ok {
			available, _ := acc["available"].(bool)
			if !available {
				fmt.Printf("    Access:   (monitor not available on this platform)\n")
			} else {
				f2bAvail, _ := acc["fail2ban_available"].(bool)
				f2bActive, _ := acc["fail2ban_active"].(bool)
				banned, _ := acc["banned_ips"].([]any)
				failed, _ := acc["failed_attempts"].(float64)
				var f2bStr string
				switch {
				case !f2bAvail:
					f2bStr = "fail2ban status unknown"
				case f2bActive:
					f2bStr = "✓ fail2ban active"
				default:
					f2bStr = "⚠ fail2ban not active"
				}
				fmt.Printf("    Access:   %s; %d banned IP(s); %d failed attempt(s)\n",
					f2bStr, len(banned), int(failed))
			}
		}
	}
	fmt.Printf("    (run 'ownbasectl security %s' for full exposure + access details)\n", base)

	fmt.Println()
	fmt.Println("  Recent actions:")
	if audit, ok := s["audit"].(map[string]any); ok {
		actions, _ := audit["recent_actions"].([]any)
		if len(actions) == 0 {
			fmt.Println("    (none)")
		}
		for _, raw := range actions {
			a, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ts, _ := a["time"].(string)
			action, _ := a["action"].(string)
			target, _ := a["target"].(string)
			outcome, _ := a["outcome"].(string)
			fmt.Printf("    %s  %-20s  %-30s  %s\n",
				shortTime(ts), action, target, outcome)
		}
	}

	fmt.Println("────────────────────────────────────────────────────────────────────")
	return nil
}

// shortTime formats an RFC3339 timestamp to a compact local time.
func shortTime(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	// Year 1 is the Go zero time — treat it as "not set" so a misconfigured
	// or pre-pointer-migration daemon never renders "Jan 01 0001 00:00:00".
	if t.IsZero() || t.Year() <= 1 {
		return ""
	}
	local := t.Local()
	if local.Year() != time.Now().Year() {
		return local.Format("Jan 02 2006 15:04:05")
	}
	return local.Format("Jan 02 15:04:05")
}
