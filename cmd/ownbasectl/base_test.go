package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestIsReleaseBuild(t *testing.T) {
	orig := version
	t.Cleanup(func() { version = orig })

	cases := []struct {
		v    string
		want bool
	}{
		{"dev", false},
		{"v0.3.3-dev", false},
		{"0.3.4-dev", false},
		{"v0.3.3-27-g97150bb", false},
		{"v0.3.3-27-g97150bb-dirty", false},
		{"v0.3.3-rc.1", false},
		{"v0.3.3", true},
		{"v1.0.0", true},
		{"v10.2.3", true},
		{"1.0.0", false},
		{"", false},
		{"latest", false},
	}
	for _, c := range cases {
		version = c.v
		if got := isReleaseBuild(); got != c.want {
			t.Errorf("isReleaseBuild() with version=%q = %v, want %v", c.v, got, c.want)
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
	startTestAgent(t)

	if err := registerProfile("mybase", "192.168.1.10", "ubuntu", 22, "tok123", true); err != nil {
		t.Fatalf("registerProfile: %v", err)
	}

	p, err := loadProfile("mybase")
	if err != nil {
		t.Fatalf("loadProfile: %v", err)
	}
	if p.Host != "192.168.1.10" || p.Token != "tok123" {
		t.Errorf("unexpected profile: %+v", p)
	}
	if !p.KnownLocalVM() {
		t.Error("expected LocalVM=true for a profile registered via the VM path")
	}
}

func TestRegisterProfile_PersistsSSHPort(t *testing.T) {
	startTestAgent(t)

	if err := registerProfile("first", "1.1.1.1", "ubuntu", 2222, "tok1", false); err != nil {
		t.Fatalf("registerProfile first: %v", err)
	}

	p, err := loadProfile("first")
	if err != nil {
		t.Fatalf("loadProfile: %v", err)
	}
	if p.SSHPort != 2222 {
		t.Errorf("SSHPort: got %d, want 2222", p.SSHPort)
	}
}

// A Base registered after keygen must keep the owner key that keygen stored:
// replacing it would lock the operator out of the machine that authorized it.
func TestRegisterProfile_KeepsOwnerKey(t *testing.T) {
	startTestAgent(t)

	if err := runKeygen("mybase", "", true); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	before, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if before.PublicKeyLine() == "" {
		t.Fatal("keygen stored no owner key")
	}

	if err := registerProfile("mybase", "203.0.113.10", "root", 22, "tok", false); err != nil {
		t.Fatalf("registerProfile: %v", err)
	}
	after, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if after.PublicKeyLine() != before.PublicKeyLine() {
		t.Error("registering a Base replaced its owner key")
	}
}

func TestCheckupFindings_AllClear(t *testing.T) {
	body := []byte(`{
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
	findings := checkupFindings("mybase", body)
	if len(findings) != 0 {
		t.Errorf("expected no findings, got %+v", findings)
	}
}

// Unfixed CVEs alone must not raise a finding — there is nothing a person
// can finish. They live as a reading on the Security tab.
func TestCheckupFindings_UnfixedCVEsAreNotFindings(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": true,
			"exposure": {"available": true, "firewall_active": true, "unexpected_count": 0},
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
				"host": {
					"critical": 11, "high": 115,
					"fixable_critical": 0, "fixable_high": 0
				}
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 0 {
		t.Errorf("unfixed CVEs must not raise a finding, got %+v", findings)
	}
}

// Banned IPs are fail2ban working, not a to-do.
func TestCheckupFindings_BannedIPsAreNotFindings(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": true,
			"exposure": {"available": true, "firewall_active": true, "unexpected_count": 0},
			"access": {"available": true, "banned_ips": ["1.2.3.4", "5.6.7.8"]},
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
				"host": {"critical": 0, "high": 0}
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 0 {
		t.Errorf("banned IPs must not raise a finding, got %+v", findings)
	}
}

func TestCheckupFindings_FlagsIssues(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": false,
			"exposure": {"available": true, "firewall_active": false, "unexpected_count": 2},
			"access": {"available": true, "banned_ips": ["1.2.3.4"]},
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
				"host": {"critical": 1, "high": 2, "fixable_critical": 1, "fixable_high": 0},
				"images": [
					{
						"service": "ownbase-core-caddy",
						"image": "docker.io/library/caddy:2-alpine",
						"summary": {"critical": 1, "high": 0, "fixable_critical": 1, "fixable_high": 0}
					},
					{
						"service": "ownbase-crm",
						"image": "localhost/crm:abc",
						"scan_failed": true,
						"scan_error": "image not found"
					}
				]
			},
			"drift_count": 3,
			"reboot_required": true
		},
		"updates": {"drift": [{"service": "crm", "up_to_date": false}, {"service": "worker", "up_to_date": true}]}
	}`)
	findings := checkupFindings("mybase", body)

	// Expected:
	// 1. backups not configured → form
	// 2. firewall not active
	// 3. unexpected ports
	// 4. reboot required
	// 5. host fixable CVEs
	// 6. caddy image fixable CVEs → self-update
	// 7. crm scan failed
	// 8. drift
	// 9. crm behind → deploy form
	// (banned IPs deliberately absent)
	if len(findings) != 9 {
		t.Fatalf("expected 9 findings, got %d: %+v", len(findings), findings)
	}

	want := []struct {
		summarySubstr string
		kind          string
		run           string
		tab           string
		form          string
	}{
		{"Backups not configured", actionForm, "", "", "backup-setup"},
		{"Firewall (UFW) is not active", actionOpen, "", "security", ""},
		{"unexpected internet-reachable port", actionOpen, "", "security", ""},
		{"Host reboot required", actionRun, "security reboot", "", ""},
		{"host CVE(s) have a patch available", actionRun, "security fix", "", ""},
		{"CVE(s) with a patch in core image", actionRun, "self-update", "", ""},
		{"CVE scan failed for service \"ownbase-crm\"", actionOpen, "", "security", ""},
		{"runtime file(s) drifted", actionOpen, "", "security", ""},
		{"behind its source repo", actionForm, "", "", "deploy"},
	}
	for _, w := range want {
		var found *checkupFinding
		for i := range findings {
			if strings.Contains(findings[i].Summary, w.summarySubstr) {
				found = &findings[i]
				break
			}
		}
		if found == nil {
			t.Errorf("missing finding containing %q", w.summarySubstr)
			continue
		}
		if found.Action.Kind != w.kind {
			t.Errorf("%q: kind = %q, want %q", w.summarySubstr, found.Action.Kind, w.kind)
		}
		if w.run != "" && found.Action.Run != w.run {
			t.Errorf("%q: run = %q, want %q", w.summarySubstr, found.Action.Run, w.run)
		}
		if w.tab != "" && found.Action.Tab != w.tab {
			t.Errorf("%q: tab = %q, want %q", w.summarySubstr, found.Action.Tab, w.tab)
		}
		if w.form != "" && found.Action.Form != w.form {
			t.Errorf("%q: form = %q, want %q", w.summarySubstr, found.Action.Form, w.form)
		}
		if found.Action.Label == "" {
			t.Errorf("%q: empty label", w.summarySubstr)
		}
		if found.Fix == "" {
			t.Errorf("%q: empty fix string", w.summarySubstr)
		}
	}
}

// TestCheckupFindings_BackupConfiguredButNotYetVerified covers the case
// Bugbot flagged: a Base with backups already configured and snapshots
// running, just waiting on the periodic verify-restore drill, must not be
// told to re-run `backup setup` — that would misleadingly suggest
// re-doing something that is already working.
func TestCheckupFindings_BackupConfiguredButNotYetVerified(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": false,
			"last_backup": "2026-07-04T00:27:18Z",
			"exposure": {"available": true, "firewall_active": true, "unexpected_count": 0},
			"access": {"available": true, "banned_ips": []},
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "` + time.Now().UTC().Format(time.RFC3339) + `",
				"host": {"critical": 0, "high": 0}
			},
			"drift_count": 0
		},
		"updates": {"drift": [{"service": "crm", "up_to_date": true}]}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if strings.Contains(findings[0].Fix, "backup setup") {
		t.Errorf("fix should not suggest re-running setup when backups are already configured, got %+v", findings[0])
	}
	if findings[0].Action.Kind != actionOpen || findings[0].Action.Tab != "backups" {
		t.Errorf("expected open→backups, got %+v", findings[0].Action)
	}
}

// TestCheckupFindings_NoSecuritySection_StillScansUpdates covers the case
// Bugbot flagged: a status payload without a "security" key (e.g. from an
// older agent build) must not skip the unrelated updates.drift scan.
func TestCheckupFindings_NoSecuritySection_StillScansUpdates(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"updates": {"drift": [{"service": "crm", "up_to_date": false}]}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (update drift) despite missing security section, got %d: %+v", len(findings), findings)
	}
	if findings[0].Action.Form != "deploy" {
		t.Errorf("expected deploy form finding, got %+v", findings[0])
	}
}

func TestCheckupFindings_StaleScan(t *testing.T) {
	stale := time.Now().UTC().Add(-50 * time.Hour).Format(time.RFC3339)
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": true,
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "` + stale + `",
				"host": {"critical": 0, "high": 0}
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 stale-scan finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Action.Kind != actionRun || findings[0].Action.Run != "security scan" {
		t.Errorf("expected run→security scan, got %+v", findings[0].Action)
	}
}

// A stale scan that still lists fixable host CVEs must raise both findings —
// rescan and apply patches. Treating stale as exclusive of available used to
// hide Apply patches while per-image findings still fired.
func TestCheckupFindings_StaleScanStillSurfacesFixableHostCVEs(t *testing.T) {
	stale := time.Now().UTC().Add(-50 * time.Hour).Format(time.RFC3339)
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": true,
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "` + stale + `",
				"host": {
					"critical": 2, "high": 1,
					"fixable_critical": 2, "fixable_high": 0
				}
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 2 {
		t.Fatalf("expected stale + fixable findings, got %d: %+v", len(findings), findings)
	}
	var sawStale, sawFixable bool
	for _, f := range findings {
		if f.Action.Run == "security scan" {
			sawStale = true
		}
		if f.Action.Run == "security fix" {
			sawFixable = true
		}
	}
	if !sawStale || !sawFixable {
		t.Errorf("want both security scan and security fix actions, got %+v", findings)
	}
}

func TestCheckupFindings_ScanUnavailable(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": true,
			"vulns": {
				"available": false,
				"trivy_installed": true,
				"host_scan_error": "trivy timed out"
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 unavailable-scan finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Action.Run != "security scan" {
		t.Errorf("expected security scan action, got %+v", findings[0].Action)
	}
}

func TestCheckupFindings_TrivyMissing(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": true,
			"vulns": {
				"available": false,
				"trivy_installed": false
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 missing-scanner finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Action.Run != "security install-scanner" {
		t.Errorf("expected install-scanner action, got %+v", findings[0].Action)
	}
}

func TestScanIsStale(t *testing.T) {
	if scanIsStale(time.Now().UTC().Format(time.RFC3339)) {
		t.Error("fresh scan must not be stale")
	}
	if !scanIsStale(time.Now().UTC().Add(-49 * time.Hour).Format(time.RFC3339)) {
		t.Error("49h-old scan must be stale")
	}
	if scanIsStale("") {
		t.Error("empty timestamp must not be stale")
	}
	if scanIsStale("not-a-time") {
		t.Error("unparseable timestamp must not be stale")
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

func TestCheckupFindings_ConfigNotSetUp(t *testing.T) {
	// Deliberately omit "config" — a fresh Base after the wizard.
	body := []byte(`{
		"security": {
			"backup_restorable": true,
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "2026-07-31T12:00:00Z",
				"host": {"critical": 0, "high": 0}
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 config finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Action.Form != "config-setup" {
		t.Errorf("expected config-setup form, got %+v", findings[0].Action)
	}
}

func TestCheckupFindings_BackupRequiresConfig(t *testing.T) {
	// No config key — backup setup cannot finish, so it must not appear.
	body := []byte(`{
		"security": {
			"backup_restorable": false,
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "2026-07-31T12:00:00Z",
				"host": {"critical": 0, "high": 0}
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	for _, f := range findings {
		if f.Action.Form == "backup-setup" {
			t.Fatalf("backup-setup must not appear without config, got %+v", findings)
		}
	}
	if len(findings) != 1 || findings[0].Action.Form != "config-setup" {
		t.Fatalf("expected only config-setup, got %+v", findings)
	}
}

func TestCheckupFindings_CoreLocalImageUsesUpgrade(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"version": "v0.4.0",
		"security": {
			"backup_restorable": true,
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "2026-07-31T12:00:00Z",
				"host": {"critical": 0, "high": 0},
				"images": [{
					"service": "ownbase-core-caddy",
					"image": "localhost/ownbase-core-caddy:local",
					"summary": {"critical": 0, "high": 2, "fixable_critical": 0, "fixable_high": 2}
				}]
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Action.Run != "upgrade --apply" {
		t.Errorf("local caddy image should offer upgrade --apply, got %+v", findings[0].Action)
	}
}

// A daemon that reports version can rebuild even while still running a
// registry Caddy image (first local build pending). Must not stuck-loop on
// self-update.
func TestCheckupFindings_CoreRegistryImageWithVersionUsesUpgrade(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"version": "v0.4.0",
		"security": {
			"backup_restorable": true,
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "2026-07-31T12:00:00Z",
				"host": {"critical": 0, "high": 0},
				"images": [{
					"service": "ownbase-core-caddy",
					"image": "docker.io/library/caddy:2.11.4-alpine",
					"summary": {"critical": 0, "high": 2, "fixable_critical": 0, "fixable_high": 2}
				}]
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Action.Run != "upgrade --apply" {
		t.Errorf("versioned daemon on registry caddy should offer upgrade --apply, got %+v", findings[0].Action)
	}
}

func TestCheckupFindings_CoreOldDaemonNeedsSelfUpdate(t *testing.T) {
	// No status.version — pre-migration daemon.
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": true,
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "2026-07-31T12:00:00Z",
				"host": {"critical": 0, "high": 0},
				"images": [{
					"service": "ownbase-core-caddy",
					"image": "docker.io/library/caddy:2-alpine",
					"summary": {"critical": 0, "high": 2, "fixable_critical": 0, "fixable_high": 2}
				}]
			}
		}
	}`)
	findings := checkupFindings("mybase", body)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	if findings[0].Action.Run != "self-update" {
		t.Errorf("old daemon should offer self-update, got %+v", findings[0].Action)
	}
}

func TestCheckupFindings_ImageCVESkippedWhenUpToDate(t *testing.T) {
	body := []byte(`{
		"config": {"repo_url": "git@example.com/x.git"},
		"security": {
			"backup_restorable": true,
			"vulns": {
				"available": true,
				"trivy_installed": true,
				"scanned_at": "2026-07-31T12:00:00Z",
				"host": {"critical": 0, "high": 0},
				"images": [{
					"service": "ownbase-crm",
					"image": "localhost/crm:abc",
					"summary": {"critical": 1, "high": 0, "fixable_critical": 1, "fixable_high": 0}
				}]
			}
		},
		"updates": {"drift": [{"service": "crm", "ref": "abc", "up_to_date": true, "newest_tag": "v1.0.0"}]}
	}`)
	findings := checkupFindings("mybase", body)
	for _, f := range findings {
		if strings.Contains(f.Summary, "crm") || f.Action.Form == "deploy" {
			t.Fatalf("up_to_date service must not get a deploy finding: %+v", findings)
		}
	}
}

func TestGitVerbSkipsFlagsWithValues(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-C", "/tmp/x", "push", "origin", "main"}, "push"},
		{[]string{"clone", "url", "dir"}, "clone"},
		{[]string{"-c", "user.name=x", "commit", "-m", "hi"}, "commit"},
		{[]string{"--git-dir=/tmp/g", "status"}, "status"},
	}
	for _, c := range cases {
		if got := gitVerb(c.args); got != c.want {
			t.Errorf("gitVerb(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}
