package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/serverconfig"
)

func TestSplitUserHost(t *testing.T) {
	cases := []struct {
		remote       string
		fallbackUser string
		wantHost     string
		wantUser     string
	}{
		{"root@mybase.example.com", "ubuntu", "mybase.example.com", "root"},
		{"mybase.example.com", "ubuntu", "mybase.example.com", "ubuntu"},
		{"192.168.1.10", "root", "192.168.1.10", "root"},
		{"deploy@192.168.1.10", "root", "192.168.1.10", "deploy"},
	}
	for _, c := range cases {
		host, user := splitUserHost(c.remote, c.fallbackUser)
		if host != c.wantHost || user != c.wantUser {
			t.Errorf("splitUserHost(%q, %q) = (%q, %q), want (%q, %q)",
				c.remote, c.fallbackUser, host, user, c.wantHost, c.wantUser)
		}
	}
}

func TestFindRepoRoot(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "install.sh"), []byte("#!/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(tmp, "cmd", "ownbasectl")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}

	got, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	// Resolve symlinks (macOS /tmp is often a symlink) before comparing.
	wantResolved, _ := filepath.EvalSymlinks(tmp)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("findRepoRoot() = %q, want %q", gotResolved, wantResolved)
	}
}

func TestFindRepoRoot_NotFound(t *testing.T) {
	tmp := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatal(err)
		}
	}()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	if _, err := findRepoRoot(); err == nil {
		t.Error("expected error when go.mod/install.sh are not found above cwd")
	}
}

func TestRegisterProfile(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := registerProfile("mybase", "192.168.1.10", "ubuntu", "~/.ssh/id_ed25519", 22, 7070, "tok123", true, "mybase.test"); err != nil {
		t.Fatalf("registerProfile: %v", err)
	}

	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p, ok := cfg.Servers["mybase"]
	if !ok {
		t.Fatal("expected profile 'mybase' to be registered")
	}
	if p.Host != "192.168.1.10" || p.Token != "tok123" {
		t.Errorf("unexpected profile: %+v", p)
	}
	if !p.KnownLocalVM() {
		t.Error("expected LocalVM=true for a profile registered via the VM path")
	}
	if p.DevTLSDomain != "mybase.test" {
		t.Errorf("DevTLSDomain = %q, want %q", p.DevTLSDomain, "mybase.test")
	}
}

func TestRegisterProfile_PersistsSSHPort(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := registerProfile("first", "1.1.1.1", "ubuntu", "", 2222, 7070, "tok1", false, ""); err != nil {
		t.Fatalf("registerProfile first: %v", err)
	}

	cfgPath, _ := serverconfig.DefaultConfigPath()
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	p, ok := cfg.Servers["first"]
	if !ok {
		t.Fatal("expected profile 'first' to be registered")
	}
	if p.SSHPort != 2222 {
		t.Errorf("SSHPort: got %d, want 2222", p.SSHPort)
	}
}

func TestCheckupFindings_AllClear(t *testing.T) {
	body := []byte(`{
		"security": {
			"backup_restorable": true,
			"exposure": {"available": true, "firewall_active": true, "unexpected_count": 0},
			"access": {"available": true, "banned_ips": []},
			"vulns": {"available": true, "host": {"critical": 0, "high": 0}},
			"drift_count": 0
		},
		"updates": {"drift": [{"service": "crm", "up_to_date": true}]}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %+v", findings)
	}
}

func TestCheckupFindings_FlagsIssues(t *testing.T) {
	body := []byte(`{
		"security": {
			"backup_restorable": false,
			"exposure": {"available": true, "firewall_active": false, "unexpected_count": 2},
			"access": {"available": true, "banned_ips": ["1.2.3.4"]},
			"vulns": {"available": true, "host": {"critical": 1, "high": 2, "fixable_critical": 1, "fixable_high": 0}},
			"drift_count": 3
		},
		"updates": {"drift": [{"service": "crm", "up_to_date": false}, {"service": "worker", "up_to_date": true}]}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 7 {
		t.Fatalf("expected 7 findings, got %d: %+v", len(findings), findings)
	}
}

// TestCheckupFindings_BackupConfiguredButNotYetVerified covers the case
// Bugbot flagged: a Base with backups already configured and snapshots
// running, just waiting on the periodic verify-restore drill, must not be
// told to re-run `backup setup` — that would misleadingly suggest
// re-doing something that is already working.
func TestCheckupFindings_BackupConfiguredButNotYetVerified(t *testing.T) {
	body := []byte(`{
		"security": {
			"backup_restorable": false,
			"last_backup": "2026-07-04T00:27:18Z",
			"exposure": {"available": true, "firewall_active": true, "unexpected_count": 0},
			"access": {"available": true, "banned_ips": []},
			"vulns": {"available": true, "host": {"critical": 0, "high": 0}},
			"drift_count": 0
		},
		"updates": {"drift": [{"service": "crm", "up_to_date": true}]}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if strings.Contains(findings[0].fix, "backup setup") {
		t.Errorf("fix should not suggest re-running setup when backups are already configured, got %+v", findings[0])
	}
}

// TestCheckupFindings_NoSecuritySection_StillScansUpdates covers the case
// Bugbot flagged: a status payload without a "security" key (e.g. from an
// older agent build) must not skip the unrelated updates.drift scan.
func TestCheckupFindings_NoSecuritySection_StillScansUpdates(t *testing.T) {
	body := []byte(`{
		"updates": {"drift": [{"service": "crm", "up_to_date": false}]}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (update drift) despite missing security section, got %d: %+v", len(findings), findings)
	}
	if !strings.Contains(findings[0].summary, "behind their source repo") {
		t.Errorf("expected an update-drift finding, got %+v", findings[0])
	}
}

func TestShellQuoteEnv(t *testing.T) {
	if got := shellQuoteEnv("ssh-ed25519 AAAA foo@bar"); got != `'ssh-ed25519 AAAA foo@bar'` {
		t.Errorf("shellQuoteEnv = %q", got)
	}
	if got := shellQuoteEnv("o'brien"); got != `'o'\''brien'` {
		t.Errorf("shellQuoteEnv = %q", got)
	}
}

func TestEnvPrefixedCommand(t *testing.T) {
	got := envPrefixedCommand(map[string]string{"FOO": "bar"}, "run-it")
	want := "FOO='bar' run-it"
	if got != want {
		t.Errorf("envPrefixedCommand = %q, want %q", got, want)
	}
}

func TestLinuxArchForHost(t *testing.T) {
	arch := linuxArchForHost()
	if arch != "amd64" && arch != "arm64" {
		t.Errorf("unexpected arch: %q", arch)
	}
}
