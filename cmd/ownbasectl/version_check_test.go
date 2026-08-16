package main

import (
	"strings"
	"testing"

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
		if strings.Contains(f.Summary, "daemon") && f.Action.Run == "self-update" {
			foundDaemon = true
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
	if got[0].Action.Run != "self-update" {
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

func TestVersionFindings_BehindLatestNoSkew(t *testing.T) {
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
	// Both at v0.4.0 — no skew, both behind latest.
	got := versionFindings("mybase", "v0.4.0", snap)
	if len(got) != 2 {
		t.Fatalf("got %d findings: %+v", len(got), got)
	}
	var sawDaemon, sawCLI bool
	for _, f := range got {
		if f.Action.Run == "self-update" {
			sawDaemon = true
		}
		if f.Action.Kind == actionManual {
			sawCLI = true
		}
	}
	if !sawDaemon || !sawCLI {
		t.Errorf("want daemon+cli findings, got %+v", got)
	}
}

func TestVersionFindings_SkewDedupesBehind(t *testing.T) {
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
		t.Fatalf("got %d findings (want 1 skew): %+v", len(got), got)
	}
	if got[0].Action.Run != "self-update" {
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
				"scanned_at": "2026-08-16T00:00:00Z",
				"host": {"critical": 0, "high": 0}
			},
			"drift_count": 0,
			"reboot_required": false
		},
		"updates": {"drift": [{"service": "crm", "up_to_date": true}]}
	}`)
	findings := checkupFindings("mybase", body, snap, "v0.4.0")
	var versionHits int
	for _, f := range findings {
		if strings.Contains(f.Summary, "behind latest") {
			versionHits++
		}
	}
	if versionHits != 2 {
		t.Fatalf("expected 2 behind-latest findings, got %d: %+v", versionHits, findings)
	}
	// All-clear body plus two version findings — nothing else.
	if len(findings) != 2 {
		t.Errorf("unexpected extra findings: %+v", findings)
	}
}
