package main

import (
	"strings"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/release"
)

func TestVersionFindings_DevCLIStillFlagsDaemonBehind(t *testing.T) {
	// Default version ldflag is "dev" — never nag about the CLI.
	snap := release.Snapshot{
		Manifest: release.Manifest{
			Schema: 1,
			Components: map[string]release.ComponentVersion{
				"cli":    {Version: "v9.9.9"},
				"daemon": {Version: "v9.9.9"},
			},
		},
	}
	got := versionFindings("mybase", "v0.1.0", snap)
	foundDaemon := false
	for _, f := range got {
		if strings.Contains(f.Summary, "daemon") && isSelfUpdateRun(f.Action.Run) {
			foundDaemon = true
			if f.Action.Run != "self-update --version v9.9.9" {
				t.Errorf("want pinned latest, got Run=%q", f.Action.Run)
			}
		}
		if strings.Contains(f.Summary, "ownbasectl") {
			t.Errorf("dev CLI must not produce CLI finding: %+v", f)
		}
	}
	if !foundDaemon {
		t.Fatalf("expected daemon-behind finding, got %+v", got)
	}
}

func TestVersionFindings_SkewCLIAhead(t *testing.T) {
	orig := version
	version = "v0.5.0"
	t.Cleanup(func() { version = orig })

	got := versionFindings("mybase", "v0.4.0", release.Snapshot{})
	if len(got) != 1 {
		t.Fatalf("got %d findings: %+v", len(got), got)
	}
	// Empty manifest → pin to this CLI's tag so /daemon/latest/ is not used.
	if got[0].Action.Run != "self-update --version v0.5.0" {
		t.Errorf("action = %+v", got[0].Action)
	}
	if !strings.Contains(got[0].Summary, "behind your CLI") {
		t.Errorf("summary = %q", got[0].Summary)
	}
}

func TestVersionFindings_SkewDaemonAhead(t *testing.T) {
	orig := version
	version = "v0.3.0"
	t.Cleanup(func() { version = orig })

	got := versionFindings("mybase", "v0.4.0", release.Snapshot{})
	if len(got) != 1 {
		t.Fatalf("got %d findings: %+v", len(got), got)
	}
	if got[0].Action.Kind != actionManual {
		t.Errorf("kind = %q, want manual", got[0].Action.Kind)
	}
}

func TestVersionFindings_CLIBehindSuppressesSelfUpdate(t *testing.T) {
	orig := version
	version = "v0.4.0"
	t.Cleanup(func() { version = orig })

	snap := release.Snapshot{
		Manifest: release.Manifest{
			Schema: 1,
			Components: map[string]release.ComponentVersion{
				"cli":    {Version: "v0.5.0"},
				"daemon": {Version: "v0.5.0"},
			},
		},
	}
	// Both at v0.4.0 — CLI-first: one finding, no self-update button.
	got := versionFindings("mybase", "v0.4.0", snap)
	if len(got) != 1 {
		t.Fatalf("got %d findings: %+v", len(got), got)
	}
	if got[0].Action.Kind != actionManual {
		t.Errorf("kind = %q, want manual", got[0].Action.Kind)
	}
	if got[0].Action.Run != "" {
		t.Errorf("must not offer self-update while CLI is behind: %+v", got[0].Action)
	}
	if !strings.Contains(got[0].Summary, "upgrade the CLI before") {
		t.Errorf("summary should name the ordering: %q", got[0].Summary)
	}
	if !strings.Contains(got[0].Fix, "self-update mybase") {
		t.Errorf("fix should chain self-update after brew: %q", got[0].Fix)
	}
}

func TestVersionFindings_CLIBehindAndCLIAheadSkew_StillCLIFirst(t *testing.T) {
	// CLI v0.4.5, daemon v0.4.0, latest v0.5.0 — CLI ahead of daemon but
	// itself behind latest. Self-update would install v0.5 and flip skew.
	orig := version
	version = "v0.4.5"
	t.Cleanup(func() { version = orig })

	snap := release.Snapshot{
		Manifest: release.Manifest{
			Schema: 1,
			Components: map[string]release.ComponentVersion{
				"cli":    {Version: "v0.5.0"},
				"daemon": {Version: "v0.5.0"},
			},
		},
	}
	got := versionFindings("mybase", "v0.4.0", snap)
	if len(got) != 1 {
		t.Fatalf("got %d findings: %+v", len(got), got)
	}
	if isSelfUpdateRun(got[0].Action.Run) {
		t.Fatalf("must not self-update while CLI is behind latest: %+v", got)
	}
}

func TestVersionFindings_CLICurrentDaemonBehind(t *testing.T) {
	orig := version
	version = "v0.5.0"
	t.Cleanup(func() { version = orig })

	snap := release.Snapshot{
		Manifest: release.Manifest{
			Schema: 1,
			Components: map[string]release.ComponentVersion{
				"cli":    {Version: "v0.5.0"},
				"daemon": {Version: "v0.5.0"},
			},
		},
	}
	// CLI current, daemon behind both CLI and latest — one self-update only.
	got := versionFindings("mybase", "v0.4.0", snap)
	if len(got) != 1 {
		t.Fatalf("got %d findings (want 1): %+v", len(got), got)
	}
	if got[0].Action.Run != "self-update --version v0.5.0" {
		t.Errorf("action = %+v", got[0].Action)
	}
}

func TestCheckupFindings_VersionBehind(t *testing.T) {
	orig := version
	version = "v0.4.0"
	t.Cleanup(func() { version = orig })

	snap := release.Snapshot{
		Manifest: release.Manifest{
			Schema: 1,
			Components: map[string]release.ComponentVersion{
				"cli":    {Version: "v0.5.0"},
				"daemon": {Version: "v0.5.0"},
			},
		},
	}
	// scanned_at must be fresh so the 48h stale-scan rule does not fire.
	body := []byte(`{
		"version": "v0.4.0",
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": true,
			"exposure": {"available": true, "firewall_active": true, "unexpected_count": 0},
			"access": {"available": true, "banned_ips": []},
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
				"host": {"critical": 0, "high": 0}
			},
			"drift_count": 0,
			"reboot_required": false
		},
		"updates": {"drift": [{"service": "crm", "up_to_date": true}]}
	}`)
	findings := checkupFindings("mybase", body, snap)
	// CLI-first: one finding covering both components.
	if len(findings) != 1 {
		t.Fatalf("expected 1 CLI-first finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Action.Kind != actionManual {
		t.Errorf("kind = %q", findings[0].Action.Kind)
	}
}

func TestReportNeedsAttention(t *testing.T) {
	if reportNeedsAttention(release.Report{}) {
		t.Error("empty report should not need attention")
	}
	if reportNeedsAttention(release.Report{
		Components: []release.Component{{Status: release.StatusCurrent}},
	}) {
		t.Error("current should not need attention")
	}
	if !reportNeedsAttention(release.Report{
		Components: []release.Component{{Status: release.StatusBehind}},
	}) {
		t.Error("behind should need attention")
	}
	if !reportNeedsAttention(release.Report{
		Skew: &release.Skew{Direction: "cli_ahead"},
	}) {
		t.Error("skew should need attention")
	}
}
