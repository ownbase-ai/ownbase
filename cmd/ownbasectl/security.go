package main

// security.go implements the 'ownbasectl security' subcommand.
//
// ownbasectl security reads the exposure and access data from the Base's
// status API and prints a detailed security posture report: the full listener
// inventory with internet-reachability assessment, and the SSH access monitor
// summary (fail2ban state, banned IPs, recent successful logins).
//
// The security scan is a locally-computed inventory — it answers "what does
// this machine believe is internet-reachable?" using ss + ufw + the expected-
// allowlist from ownbase.yaml. It cannot see an upstream cloud firewall or a
// kernel hiding sockets; use the output as the authoritative on-machine view.

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

func newSecurityCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "security <name>",
		Short: "Full network exposure inventory + SSH access monitor + CVE scan",
		Long: `Read the exposure, access, and vulnerability data from the Base's status
API and print a detailed security posture report.

The scan is a locally-computed inventory — it answers "what does this
machine believe is internet-reachable?" using ss + ufw + the expected
allowlist. It cannot see an upstream cloud firewall.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := fetchStatusBody(args[0], 10*time.Second)
			if err != nil {
				return err
			}
			if jsonOut {
				section, err := extractStatusSection(body, "security")
				if err != nil {
					return err
				}
				fmt.Println(string(section))
				return nil
			}
			return printSecurityReport(args[0], body)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the security section of the status payload as JSON")

	cmd.AddCommand(newSecurityFixCmd(), newSecurityScanCmd())
	return cmd
}

// printSecurityReport renders a human-readable security report.
func printSecurityReport(base string, body []byte) error {
	var s map[string]any
	if err := json.Unmarshal(body, &s); err != nil {
		return fmt.Errorf("parse status JSON: %w", err)
	}

	sec, _ := s["security"].(map[string]any)
	if sec == nil {
		fmt.Println("No security data in status response.")
		return nil
	}

	fmt.Println("══════════════════════ OwnBase Security Report ══════════════════════")
	generatedAt, _ := s["generated_at"].(string)
	fmt.Printf("  Generated: %s\n", shortTime(generatedAt))
	fmt.Println()

	// ── Network Exposure ────────────────────────────────────────────────────
	fmt.Println("  Network Exposure")
	fmt.Println("  " + strings.Repeat("─", 68))
	exp, _ := sec["exposure"].(map[string]any)
	if exp == nil {
		fmt.Println("    Not available.")
	} else {
		available, _ := exp["available"].(bool)
		if !available {
			fmt.Println("    Scan not available on this platform (requires Linux with ss + ufw).")
		} else {
			fwActive, _ := exp["firewall_active"].(bool)
			unexpected, _ := exp["unexpected_count"].(float64)

			fwLabel := "✓ active"
			if !fwActive {
				fwLabel = "⚠ NOT ACTIVE — all ports may be open!"
			}
			fmt.Printf("    Firewall (UFW): %s\n", fwLabel)
			if int(unexpected) == 0 {
				fmt.Printf("    Unexpected internet-reachable ports: ✓ none\n")
			} else {
				fmt.Printf("    Unexpected internet-reachable ports: ⚠ %d — review the table below\n", int(unexpected))
			}

			listeners, _ := exp["listeners"].([]any)
			if len(listeners) > 0 {
				fmt.Println()
				fmt.Printf("    %-8s  %-6s  %-18s  %-12s  %-10s  %s\n",
					"PORT", "PROTO", "BIND", "PROCESS", "REACHABLE", "EXPECTED")
				fmt.Println("    " + strings.Repeat("─", 68))
				for _, raw := range listeners {
					l, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					port, _ := l["port"].(float64)
					proto, _ := l["proto"].(string)
					bind, _ := l["bind"].(string)
					process, _ := l["process"].(string)
					reachable, _ := l["internet_reachable"].(bool)
					expected, _ := l["expected"].(bool)

					reachStr := "loopback"
					if reachable {
						reachStr = "internet"
					}
					expStr := "✓"
					if reachable && !expected {
						expStr = "⚠ UNEXPECTED"
					}
					if !reachable {
						expStr = "—"
					}
					if len(process) > 12 {
						process = process[:12]
					}
					fmt.Printf("    %-8d  %-6s  %-18s  %-12s  %-10s  %s\n",
						int(port), proto, bind, process, reachStr, expStr)
				}
			}
		}
	}

	fmt.Println()

	// ── SSH Access Monitor ───────────────────────────────────────────────────
	fmt.Println("  SSH Access Monitor")
	fmt.Println("  " + strings.Repeat("─", 68))
	acc, _ := sec["access"].(map[string]any)
	if acc == nil {
		fmt.Println("    Not available.")
	} else {
		available, _ := acc["available"].(bool)
		if !available {
			fmt.Println("    Monitor not available (requires Linux with fail2ban + journald).")
		} else {
			f2bAvail, _ := acc["fail2ban_available"].(bool)
			f2bActive, _ := acc["fail2ban_active"].(bool)
			failed, _ := acc["failed_attempts"].(float64)
			bannedRaw, _ := acc["banned_ips"].([]any)

			var f2bLabel string
			switch {
			case !f2bAvail:
				f2bLabel = "(status unknown — fail2ban-client not reachable)"
			case f2bActive:
				f2bLabel = "✓ active"
			default:
				f2bLabel = "⚠ NOT ACTIVE"
			}
			fmt.Printf("    fail2ban:        %s\n", f2bLabel)
			failedSrc := "total in fail2ban session"
			if !f2bAvail {
				failedSrc = "from journald, last 24h"
			}
			fmt.Printf("    Failed attempts: %d (%s)\n", int(failed), failedSrc)

			if len(bannedRaw) == 0 {
				fmt.Printf("    Banned IPs:      (none)\n")
			} else {
				banned := make([]string, 0, len(bannedRaw))
				for _, b := range bannedRaw {
					if bs, ok := b.(string); ok {
						banned = append(banned, bs)
					}
				}
				fmt.Printf("    Banned IPs:      %s\n", strings.Join(banned, ", "))
			}

			logins, _ := acc["recent_logins"].([]any)
			fmt.Println()
			if len(logins) == 0 {
				fmt.Println("    Recent logins (24h): none recorded")
			} else {
				fmt.Println("    Recent successful logins (24h):")
				fmt.Printf("      %-22s  %-16s  %-20s  %s\n", "TIME", "USER", "SOURCE IP", "METHOD")
				fmt.Println("      " + strings.Repeat("─", 64))
				for _, raw := range logins {
					login, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					ts, _ := login["time"].(string)
					user, _ := login["user"].(string)
					ip, _ := login["source_ip"].(string)
					method, _ := login["method"].(string)
					fmt.Printf("      %-22s  %-16s  %-20s  %s\n",
						shortTime(ts), user, ip, method)
				}
			}
		}
	}

	fmt.Println()

	// ── Vulnerability Scan ────────────────────────────────────────────────
	fmt.Println("  Vulnerability Scan")
	fmt.Println("  " + strings.Repeat("─", 68))
	vulns, _ := sec["vulns"].(map[string]any)
	if vulns == nil {
		fmt.Println("    Not available.")
	} else {
		available, _ := vulns["available"].(bool)
		trivyInstalled, _ := vulns["trivy_installed"].(bool)
		scannedAt, _ := vulns["scanned_at"].(string)

		if !trivyInstalled {
			fmt.Println("    Scanner not available (install trivy to enable CVE scanning).")
		} else if scannedAt == "" {
			fmt.Println("    Scanner installed — first scan runs ~5 minutes after daemon start.")
		} else {
			fmt.Printf("    Last scanned: %s\n", shortTime(scannedAt))
			fmt.Println()

			// Host scan summary (only when the host scan succeeded).
			if available {
				host, _ := vulns["host"].(map[string]any)
				if host != nil {
					critical, _ := host["critical"].(float64)
					high, _ := host["high"].(float64)
					medium, _ := host["medium"].(float64)
					low, _ := host["low"].(float64)
					fixCrit, _ := host["fixable_critical"].(float64)
					fixHigh, _ := host["fixable_high"].(float64)

					hostLabel := "✓ clean"
					if int(critical) > 0 {
						hostLabel = fmt.Sprintf("⚠ %d critical, %d high, %d medium, %d low", int(critical), int(high), int(medium), int(low))
					} else if int(high) > 0 {
						hostLabel = fmt.Sprintf("%d high, %d medium, %d low", int(high), int(medium), int(low))
					} else if int(medium)+int(low) > 0 {
						hostLabel = fmt.Sprintf("%d medium, %d low", int(medium), int(low))
					}
					fmt.Printf("    Host OS packages:  %s\n", hostLabel)

					// Fixable breakdown (only when there are critical or high CVEs).
					if int(critical)+int(high) > 0 {
						fixable := int(fixCrit) + int(fixHigh)
						unfixCrit := int(critical) - int(fixCrit)
						unfixHigh := int(high) - int(fixHigh)
						if fixable > 0 {
							fmt.Printf("      Fixable now:     %d critical, %d high  — run: ownbasectl security fix %s\n",
								int(fixCrit), int(fixHigh), base)
						}
						if unfixCrit+unfixHigh > 0 {
							fmt.Printf("      No fix yet:      %d critical, %d high\n", unfixCrit, unfixHigh)
						}
					}

					// Top findings.
					topRaw, _ := host["top"].([]any)
					if len(topRaw) > 0 {
						fmt.Println()
						fmt.Printf("    %-18s  %-10s  %-20s  %-12s  %s\n", "CVE ID", "SEVERITY", "PACKAGE", "FIXED IN", "TITLE")
						fmt.Println("    " + strings.Repeat("─", 76))
						for _, raw := range topRaw {
							f, ok := raw.(map[string]any)
							if !ok {
								continue
							}
							id, _ := f["vuln_id"].(string)
							sev, _ := f["severity"].(string)
							pkg, _ := f["package"].(string)
							fixedIn, _ := f["fixed_in"].(string)
							title, _ := f["title"].(string)
							if len(title) > 30 {
								title = title[:27] + "..."
							}
							if fixedIn == "" {
								fixedIn = "—"
							}
							fmt.Printf("    %-18s  %-10s  %-20s  %-12s  %s\n", id, sev, pkg, fixedIn, title)
						}
					}
				}
			} else {
				fmt.Println("    Host OS packages:  ⚠ scan failed — check agent logs")
			}

			// Per-image summaries — shown regardless of host scan outcome so
			// container CVE data is visible even when the host scan fails.
			imagesRaw, _ := vulns["images"].([]any)
			if len(imagesRaw) > 0 {
				fmt.Println()
				fmt.Printf("    %-36s  %-10s  %-8s  %-8s  %s\n", "SERVICE / IMAGE", "CRITICAL", "HIGH", "MEDIUM", "LOW")
				fmt.Println("    " + strings.Repeat("─", 72))
				for _, raw := range imagesRaw {
					img, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					svc, _ := img["service"].(string)
					image, _ := img["image"].(string)
					label := svc
					if image != "" && image != svc {
						// Strip @sha256:... digest so we show the human-readable
						// tag (e.g. "docker.io/library/caddy:2-alpine") rather
						// than an opaque hash tail.
						if i := strings.Index(image, "@sha256:"); i != -1 {
							image = image[:i]
						}
						if len(image) > 54 {
							image = image[:51] + "…"
						}
					}

					if failed, _ := img["scan_failed"].(bool); failed {
						fmt.Printf("    %-36s  scan failed (trivy error — check agent logs)\n", label)
						continue
					}
					summary, _ := img["summary"].(map[string]any)
					if summary == nil {
						continue
					}
					c, _ := summary["critical"].(float64)
					h, _ := summary["high"].(float64)
					m, _ := summary["medium"].(float64)
					l, _ := summary["low"].(float64)
					cLabel := fmt.Sprintf("%d", int(c))
					if int(c) > 0 {
						cLabel = "⚠ " + cLabel
					} else {
						cLabel = "✓"
					}
					fmt.Printf("    %-36s  %-10s  %-8d  %-8d  %d\n", label, cLabel, int(h), int(m), int(l))
					if image != "" && image != svc {
						fmt.Printf("    %-36s\n", "  ("+image+")")
					}

					// Per-service top findings — only shown when there are
					// CRITICAL or HIGH CVEs so the table stays scannable.
					topRaw, _ := summary["top"].([]any)
					if len(topRaw) > 0 && (int(c)+int(h) > 0) {
						fmt.Printf("    %-18s  %-10s  %-20s  %-12s  %s\n", "  CVE ID", "SEVERITY", "PACKAGE", "FIXED IN", "TITLE")
						fmt.Println("    " + strings.Repeat("·", 72))
						for _, fRaw := range topRaw {
							f, ok := fRaw.(map[string]any)
							if !ok {
								continue
							}
							id, _ := f["vuln_id"].(string)
							sev, _ := f["severity"].(string)
							pkg, _ := f["package"].(string)
							fixedIn, _ := f["fixed_in"].(string)
							title, _ := f["title"].(string)
							if len(title) > 30 {
								title = title[:27] + "..."
							}
							if fixedIn == "" {
								fixedIn = "—"
							}
							fmt.Printf("    %-18s  %-10s  %-20s  %-12s  %s\n", "  "+id, sev, pkg, fixedIn, title)
						}
					}
				}
			}
		}
	}

	fmt.Println()
	fmt.Println("══════════════════════════════════════════════════════════════════════")
	fmt.Println("  Note: this inventory reflects what the machine believes is")
	fmt.Println("  internet-reachable. An upstream cloud firewall or kernel-level")
	fmt.Println("  tampering is not visible here.")
	fmt.Printf("  Use 'ownbasectl security scan %s'   to trigger an immediate rescan.\n", base)
	fmt.Printf("  Use 'ownbasectl security fix %s'    to apply available OS package patches.\n", base)
	fmt.Printf("  Use 'ownbasectl upgrade %s --apply' to pull an updated Caddy image\n", base)
	fmt.Println("    and fix image CVEs (image CVEs can only be fixed by pulling a newer")
	fmt.Println("    image from the upstream maintainer — 'security fix' only helps the host OS).")
	fmt.Printf("  Use 'ownbasectl security %s --json' for the raw status data.\n", base)
	return nil
}

func newSecurityScanCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "scan <name>",
		Short: "Trigger an immediate vulnerability rescan on the Base",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecurityScan(args[0])
		},
	}
}

// runSecurityScan triggers an immediate vulnerability scan on the Base and
// returns once the daemon confirms the scan has started. The scan runs
// asynchronously; check 'ownbasectl security' for the updated results.
func runSecurityScan(base string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()
	scanURL := conn.baseURL + "/security/scan"

	req, err := http.NewRequest(http.MethodPost, scanURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if conn.token != "" {
		req.Header.Set("Authorization", "Bearer "+conn.token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("security/scan API at %s: %w\n  Is the agent running?", scanURL, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("unauthorized — the cached token may be stale; remove the profile and run 'ownbasectl adopt' again")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("security/scan returned %d: %s", resp.StatusCode, body)
	}

	fmt.Println("Vulnerability scan started on Base.")
	fmt.Printf("Results available in a few minutes — run 'ownbasectl security %s' to check.\n", base)
	return nil
}

func newSecurityFixCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "fix <name>",
		Short: "Apply available host OS package patches on the Base (apt-get upgrade)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecurityFix(args[0])
		},
	}
}

// runSecurityFix posts to the Base's /security/fix endpoint and streams the
// upgrade output back. The daemon runs apt-get on the Base as root; this
// client only needs valid API credentials.
func runSecurityFix(base string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()
	fixURL := conn.baseURL + "/security/fix"

	req, err := http.NewRequest(http.MethodPost, fixURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if conn.token != "" {
		req.Header.Set("Authorization", "Bearer "+conn.token)
	}

	fmt.Println("About to apply host OS security patches on the Base:")
	fmt.Println("  the daemon runs 'apt-get upgrade' for packages with available fixes.")
	fmt.Println("  This can take several minutes; output streams below.")
	fmt.Println()

	// Long timeout — apt-get upgrade can take several minutes on a cold mirror.
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("security/fix API at %s: %w\n  Is the agent running?", fixURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("unauthorized — the cached token may be stale; remove the profile and run 'ownbasectl adopt' again")
	}
	if resp.StatusCode == http.StatusNotImplemented {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("security/fix returned %d: %s", resp.StatusCode, body)
	}

	fmt.Println("OwnBase Security Fix — applying OS package upgrades on Base")
	fmt.Println(strings.Repeat("─", 60))

	// Stream the response body line-by-line to stdout as the daemon produces it.
	// The daemon ends with "---OK---" on success; absence means apt-get failed.
	var gotOK bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---OK---" {
			gotOK = true
			continue
		}
		fmt.Println(line)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !gotOK {
		return fmt.Errorf("security fix failed — see output above")
	}
	return nil
}
