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
				// Include what we have; put the error on the daemon component
				// via an empty daemon + note in manifest err.
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
		return printJSON(report)
	}
	printVersionCheck(report, base)
	return nil
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
func versionFindings(base, daemonVer string, snap release.Snapshot) []checkupFinding {
	var out []checkupFinding

	// Skew first — works without a manifest and is the failure that bites.
	if skew := release.AssessSkew(version, daemonVer, base); skew != nil {
		f := checkupFinding{
			Summary: skew.Summary,
			Fix:     skew.Guide,
		}
		if skew.Direction == "cli_ahead" {
			f.Action = checkupAction{
				Kind:    actionRun,
				Run:     "self-update",
				Label:   "Update OwnBase",
				Confirm: "Replaces the OwnBase daemon with the latest signed release (~10s restart).",
			}
		} else {
			// CLI behind daemon — user must upgrade the local CLI/app.
			f.Action = checkupAction{
				Kind:  actionManual,
				Label: "Upgrade CLI",
			}
		}
		out = append(out, f)
	}

	// Behind-latest findings need a usable manifest.
	if snap.LatestOf(release.ComponentDaemon) == "" && snap.LatestOf(release.ComponentCLI) == "" {
		return out
	}

	// Daemon behind newest release. Skip when skew already offered self-update
	// for the same gap (cli_ahead means daemon is older than CLI, and CLI may
	// itself be current or behind — still one self-update button is enough
	// when the skew path already covers it). Prefer the explicit "behind
	// latest" message when there is no skew.
	if skew := release.AssessSkew(version, daemonVer, base); skew == nil || skew.Direction != "cli_ahead" {
		d := release.Assess(release.ComponentDaemon, daemonVer, snap.LatestOf(release.ComponentDaemon))
		if d.Status == release.StatusBehind {
			out = append(out, checkupFinding{
				Summary: fmt.Sprintf("OwnBase daemon %s is behind latest %s", d.Current, d.Latest),
				Fix:     "ownbasectl self-update " + base,
				Action: checkupAction{
					Kind:    actionRun,
					Run:     "self-update",
					Label:   "Update OwnBase",
					Confirm: "Replaces the OwnBase daemon with the latest signed release (~10s restart).",
				},
			})
		}
	}

	// CLI behind newest — manual brew guide. Skip when skew already said so.
	if skew := release.AssessSkew(version, daemonVer, base); skew == nil || skew.Direction != "daemon_ahead" {
		c := release.Assess(release.ComponentCLI, version, snap.LatestOf(release.ComponentCLI))
		if c.Status == release.StatusBehind {
			out = append(out, checkupFinding{
				Summary: fmt.Sprintf("ownbasectl %s is behind latest %s", c.Current, c.Latest),
				Fix:     c.Guide,
				Action: checkupAction{
					Kind:  actionManual,
					Label: "Upgrade CLI",
				},
			})
		}
	}

	return out
}
