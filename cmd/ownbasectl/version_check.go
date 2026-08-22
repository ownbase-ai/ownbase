package main

// version_check.go implements `ownbasectl version --check [name]`.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/release"
)

// fetchReleaseSnapshot loads the release manifest (cached). Soft-fails: an
// unreachable origin yields an empty snapshot with Err set.
//
// releaseSnapshot is the indirection tests swap to avoid the network.
var releaseSnapshot = fetchReleaseSnapshot

func fetchReleaseSnapshot(refresh bool) release.Snapshot {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return release.Fetch(ctx, release.FetchOptions{Refresh: refresh})
}

// fetchDaemonVersion GETs /version on the named Base. Empty string on failure
// so the rest of the report still prints.
func fetchDaemonVersion(base string) (string, error) {
	conn, err := connectToServer(base)
	if err != nil {
		return "", err
	}
	defer conn.close()
	body, err := apiGet(conn, "/version")
	if err != nil {
		return "", err
	}
	var resp struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode /version: %w", err)
	}
	return strings.TrimSpace(resp.Version), nil
}

func runVersionCheck(base, appVersion string, jsonOut, refresh bool) error {
	snap := releaseSnapshot(refresh)

	run := release.Running{
		CLI: version,
		App: strings.TrimSpace(appVersion),
	}
	if base != "" {
		run.Base = base
		dv, err := fetchDaemonVersion(base)
		if err != nil {
			// Still report CLI/app; surface the daemon error in text mode.
			if jsonOut {
				if snap.Err == "" {
					snap.Err = fmt.Sprintf("daemon %s: %v", base, err)
				} else {
					snap.Err = snap.Err + "; daemon " + base + ": " + err.Error()
				}
			} else {
				fmt.Printf("  ⚠ could not reach daemon on %s: %v\n\n", base, err)
			}
		} else {
			run.Daemon = dv
		}
	}

	report := release.BuildReport(run, snap)
	if jsonOut {
		if err := printJSON(report); err != nil {
			return err
		}
	} else {
		printVersionCheck(report, base)
	}
	if reportNeedsAttention(report) {
		return fmt.Errorf("one or more OwnBase components are behind — see above")
	}
	return nil
}

// reportNeedsAttention is true when any component is behind or skew is present.
// Scripts use the non-zero exit; humans already saw the report on stdout.
func reportNeedsAttention(report release.Report) bool {
	if report.Skew != nil {
		return true
	}
	for _, c := range report.Components {
		if c.Status == release.StatusBehind {
			return true
		}
	}
	return false
}

func printVersionCheck(report release.Report, base string) {
	fmt.Println("OwnBase versions")
	fmt.Println()
	for _, c := range report.Components {
		mark := statusMark(c.Status)
		line := fmt.Sprintf("  %s %-8s  %s", mark, c.Name, c.Current)
		if c.Latest != "" && c.Status != release.StatusCurrent && c.Status != release.StatusDev {
			line += fmt.Sprintf("  (latest %s)", c.Latest)
		}
		fmt.Println(line)
		if c.Guide != "" {
			fmt.Printf("           → %s\n", c.Guide)
		}
		if c.Name == release.ComponentDaemon && c.Status == release.StatusBehind && base != "" {
			fmt.Printf("           → ownbasectl self-update %s\n", base)
		}
	}
	if report.Skew != nil {
		fmt.Println()
		fmt.Printf("  ⚠ %s\n", report.Skew.Summary)
		fmt.Printf("           → %s\n", report.Skew.Guide)
	}
	if report.Manifest != nil && report.Manifest.Err != "" {
		fmt.Println()
		fmt.Printf("  note: %s\n", report.Manifest.Err)
	}
	fmt.Println()
}

func statusMark(s release.Status) string {
	switch s {
	case release.StatusCurrent:
		return "✓"
	case release.StatusBehind:
		return "⚠"
	case release.StatusAhead:
		return "↑"
	case release.StatusDev:
		return "·"
	default:
		return "?"
	}
}

// versionFindings returns checkup findings for OwnBase component staleness
// and CLI/daemon skew. snap may be empty (manifest unreachable) — that yields
// no "behind latest" findings, but skew still works offline from the two
// running versions.
//
// Ordering rule: when the CLI itself is behind the newest release, never offer
// self-update. self-update installs latest and would leave the daemon ahead of
// the still-stale CLI (whack-a-mole). Upgrade the CLI first, then the daemon.
func versionFindings(base, daemonVer string, snap release.Snapshot) []checkupFinding {
	var out []checkupFinding

	skew := release.AssessSkew(version, daemonVer, base)
	cli := release.Assess(release.ComponentCLI, version, snap.LatestOf(release.ComponentCLI))
	daemon := release.Assess(release.ComponentDaemon, daemonVer, snap.LatestOf(release.ComponentDaemon))

	cliBehindLatest := cli.Status == release.StatusBehind
	daemonNeedsUpdate := daemon.Status == release.StatusBehind ||
		(skew != nil && skew.Direction == "cli_ahead")

	// --- CLI side ---
	if cliBehindLatest {
		summary := fmt.Sprintf("ownbasectl %s is behind latest %s", cli.Current, cli.Latest)
		fix := cli.Guide
		if daemonNeedsUpdate && base != "" {
			summary += " — upgrade the CLI before updating the Base daemon"
			fix = cli.Guide + " && ownbasectl self-update " + base
		}
		out = append(out, checkupFinding{
			Summary: summary,
			Fix:     fix,
			Action: checkupAction{
				Kind:  actionManual,
				Label: "Upgrade CLI",
			},
		})
	} else if skew != nil && skew.Direction == "daemon_ahead" {
		// Daemon is newer than this CLI; no "behind latest" (or CLI is current
		// relative to a lagging manifest). Guide brew upgrade.
		out = append(out, checkupFinding{
			Summary: skew.Summary,
			Fix:     skew.Guide,
			Action: checkupAction{
				Kind:  actionManual,
				Label: "Upgrade CLI",
			},
		})
	}

	// --- Daemon side: only when CLI is not itself behind latest ---
	if cliBehindLatest {
		return out
	}

	if skew != nil && skew.Direction == "cli_ahead" {
		// Pin a concrete tag when we know one (manifest or this CLI). Avoids
		// the CDN-cached /daemon/latest/ path, which can lag the manifest.
		run := selfUpdateRun(snap, version)
		out = append(out, checkupFinding{
			Summary: skew.Summary,
			Fix:     selfUpdateFix(base, run),
			Action: checkupAction{
				Kind:    actionRun,
				Run:     run,
				Label:   "Update OwnBase",
				Confirm: "Replaces the OwnBase daemon with the latest signed release (~10s restart).",
			},
		})
		return out
	}

	if daemon.Status == release.StatusBehind {
		run := selfUpdateRun(snap, daemon.Latest)
		out = append(out, checkupFinding{
			Summary: fmt.Sprintf("OwnBase daemon %s is behind latest %s", daemon.Current, daemon.Latest),
			Fix:     selfUpdateFix(base, run),
			Action: checkupAction{
				Kind:    actionRun,
				Run:     run,
				Label:   "Update OwnBase",
				Confirm: "Replaces the OwnBase daemon with the latest signed release (~10s restart).",
			},
		})
	}

	return out
}

// selfUpdateRun is the action.Run token for installing a newer daemon.
// Prefer an explicit --version tag so the Base fetches the immutable
// …/daemon/vX.Y.Z/ object. The bare "self-update" form still hits
// /daemon/latest/, which CloudFront can serve stale after a release.
func selfUpdateRun(snap release.Snapshot, pin string) string {
	if v := strings.TrimSpace(snap.LatestOf(release.ComponentDaemon)); v != "" {
		return "self-update --version " + v
	}
	if p := strings.TrimSpace(pin); p != "" && release.IsReleaseTag(p) {
		return "self-update --version " + p
	}
	return "self-update"
}

// selfUpdateFix is the human CLI line matching selfUpdateRun.
func selfUpdateFix(base, run string) string {
	if strings.HasPrefix(run, "self-update --version ") {
		return "ownbasectl self-update " + base + " --version " + strings.TrimPrefix(run, "self-update --version ")
	}
	return "ownbasectl self-update " + base
}
