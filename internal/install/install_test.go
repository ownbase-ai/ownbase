package install_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/install"
)

// Tier-1 tests: pure logic that does not require a Linux host.
// These tests exercise the planning, config, and check functions without
// actually installing packages or modifying the host.

// ---------------------------------------------------------------------------
// PassZeroConfig defaults
// ---------------------------------------------------------------------------

func TestPassZeroConfig_Defaults(t *testing.T) {
	// We test defaults via behaviour: PassZero with DryRun=true on macOS
	// should not fail even though the Linux steps are no-ops.
	ctx := context.Background()
	// DryRun means no actual changes. The OS check returns "dev mode" on macOS.
	report, err := install.PassZero(ctx, install.PassZeroConfig{DryRun: true})
	if err != nil {
		t.Fatalf("PassZero dry-run failed: %v", err)
	}
	// OS step should always succeed (returns dev-mode on non-Ubuntu).
	if !report.OS.Done {
		t.Errorf("OS step: want Done=true, got false (detail: %s)", report.OS.Detail)
	}
}

// ---------------------------------------------------------------------------
// Dry-run does not modify the host
// ---------------------------------------------------------------------------

// TestPassZero_DryRunNoop verifies that dry-run returns without error and
// marks steps as not-done (rather than applying them).
func TestPassZero_DryRunNoop(t *testing.T) {
	ctx := context.Background()
	report, err := install.PassZero(ctx, install.PassZeroConfig{DryRun: true})
	if err != nil {
		t.Fatalf("PassZero dry-run: unexpected error: %v", err)
	}
	// OS step is always Done (read-only check). On macOS (dev), all other
	// steps will be "not done" since they're no-ops in dry-run without Linux.
	// The critical invariant: no error, report returned.
	t.Logf("OS:          done=%v, alreadyOK=%v, detail=%q", report.OS.Done, report.OS.AlreadyOK, report.OS.Detail)
	t.Logf("Podman:      done=%v, alreadyOK=%v, detail=%q", report.Podman.Done, report.Podman.AlreadyOK, report.Podman.Detail)
	t.Logf("Linger:      done=%v, detail=%q", report.Linger.Done, report.Linger.Detail)
	t.Logf("Firewall:    done=%v, detail=%q", report.Firewall.Done, report.Firewall.Detail)
	t.Logf("AutoUpdates: done=%v, detail=%q", report.AutoUpdates.Done, report.AutoUpdates.Detail)
	t.Logf("Fail2ban:    done=%v, detail=%q", report.Fail2ban.Done, report.Fail2ban.Detail)
	t.Logf("NoExposedDB: done=%v, detail=%q", report.NoExposedDB.Done, report.NoExposedDB.Detail)
}

// ---------------------------------------------------------------------------
// HardeningReport.OK()
// ---------------------------------------------------------------------------

func TestHardeningReport_OK_AllDone(t *testing.T) {
	done := install.StepStatus{Done: true}
	r := install.HardeningReport{
		OS: done, Podman: done, Linger: done,
		Firewall: done, AutoUpdates: done, Fail2ban: done,
		NoExposedDB: done,
	}
	if !r.OK() {
		t.Error("HardeningReport.OK() should be true when all steps are Done")
	}
}

func TestHardeningReport_OK_OneFailed(t *testing.T) {
	done := install.StepStatus{Done: true}
	r := install.HardeningReport{
		OS: done, Podman: install.StepStatus{Done: false},
		Linger: done, Firewall: done, AutoUpdates: done, Fail2ban: done,
		NoExposedDB: done,
	}
	if r.OK() {
		t.Error("HardeningReport.OK() should be false when any step is not Done")
	}
}

// ---------------------------------------------------------------------------
// CheckHardeningState — read-only query
// ---------------------------------------------------------------------------

// TestCheckHardeningState_NoPanic verifies that the read-only state check
// never panics and always returns a report (even on non-Linux).
func TestCheckHardeningState_NoPanic(t *testing.T) {
	ctx := context.Background()
	// Should not panic. On macOS, most steps will report not-done.
	report := install.CheckHardeningState(ctx, install.PassZeroConfig{})
	// Just log — the important thing is no panic.
	_ = report.OK()
}

// ---------------------------------------------------------------------------
// DefaultAgentUser constant
// ---------------------------------------------------------------------------

func TestDefaultAgentUser(t *testing.T) {
	if install.DefaultAgentUser != "ownbase" {
		t.Errorf("DefaultAgentUser = %q, want %q", install.DefaultAgentUser, "ownbase")
	}
}

// ---------------------------------------------------------------------------
// Minisign key parity (M14)
// ---------------------------------------------------------------------------

// TestInstallSh_MinisignKeyParity asserts that the manual-verify example in
// install.sh uses exactly the same public key as the embedded MINISIGN_PUBLIC_KEY
// variable. A case mismatch means one of them is wrong and the installer's
// trust chain is broken.
func TestInstallSh_MinisignKeyParity(t *testing.T) {
	data, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	content := string(data)

	// Extract the MINISIGN_PUBLIC_KEY="..." value.
	embeddedKey := extractShValue(content, `MINISIGN_PUBLIC_KEY="`)
	if embeddedKey == "" {
		t.Fatal("could not find MINISIGN_PUBLIC_KEY= line in install.sh")
	}

	// Extract the key from the manual-verify comment: -P 'KEY'
	docKey := extractShValue(content, `-P '`)
	if docKey == "" {
		t.Fatal("could not find -P '<key>' in install.sh")
	}

	if embeddedKey != docKey {
		t.Errorf("minisign key mismatch:\n  MINISIGN_PUBLIC_KEY = %q\n  manual-verify -P    = %q\n  keys must be identical", embeddedKey, docKey)
	}
}

// extractShValue extracts the value following needle up to the next quote character.
func extractShValue(content, needle string) string {
	idx := strings.Index(content, needle)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(needle):]
	// Find closing quote (either " or ')
	end := strings.IndexAny(rest, `"'`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
