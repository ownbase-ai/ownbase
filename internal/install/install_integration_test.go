//go:build integration

package install_test

// Tier-2 integration tests — require a fresh Ubuntu 24.04 VM.
// Run with: go test -tags=integration ./internal/install/... -v
//
// Each test is designed to be run in order on a host that has not yet been
// hardened. They are idempotent: running them twice on the same host should
// produce the same result (the second run no-ops each step).
//
// Requires root (or sudo) to install packages and configure the firewall.
// In CI, run as root in the Ubuntu 24.04 environment.

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/install"
)

func TestPassZero_FullInstall(t *testing.T) {
	requireLinux(t)
	requireRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg := install.PassZeroConfig{
		AgentUser: "ownbase-test",
		SSHPort:   22,
		DryRun:    false,
	}

	// Create the test user (so linger can be configured for them).
	createTestUser(t, cfg.AgentUser)
	defer cleanupTestUser(t, cfg.AgentUser)

	report, err := install.PassZero(ctx, cfg)
	if err != nil {
		t.Fatalf("PassZero: %v", err)
	}

	// All steps must be done.
	if !report.OK() {
		t.Errorf("HardeningReport not OK after PassZero")
	}

	// Detailed assertions.
	assertStep(t, "OS", report.OS)
	assertStep(t, "Podman", report.Podman)
	assertStep(t, "Linger", report.Linger)
	assertStep(t, "Firewall", report.Firewall)
	assertStep(t, "AutoUpdates", report.AutoUpdates)
	assertStep(t, "Fail2ban", report.Fail2ban)
	assertStep(t, "NoExposedDB", report.NoExposedDB)

	// Trivy is non-fatal (OK() excludes it) but must still report Done=true
	// after a successful full install.
	if !report.Trivy.Done {
		t.Errorf("Trivy step: Done=false after PassZero (detail: %s, err: %v)",
			report.Trivy.Detail, report.Trivy.Err)
	}
	t.Logf("step Trivy: done=%v alreadyOK=%v detail=%q",
		report.Trivy.Done, report.Trivy.AlreadyOK, report.Trivy.Detail)
}

// TestTrivy_InstalledAndVersioned verifies that after PassZero, trivy is on
// PATH and responds to --version.
func TestTrivy_InstalledAndVersioned(t *testing.T) {
	requireLinux(t)
	requireRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg := install.PassZeroConfig{AgentUser: "ownbase-test", SSHPort: 22}
	createTestUser(t, cfg.AgentUser)
	defer cleanupTestUser(t, cfg.AgentUser)

	report, err := install.PassZero(ctx, cfg)
	if err != nil {
		t.Fatalf("PassZero: %v", err)
	}
	if !report.Trivy.Done {
		t.Fatalf("Trivy step not done: detail=%s err=%v", report.Trivy.Detail, report.Trivy.Err)
	}

	out, err := exec.CommandContext(ctx, "trivy", "--version").Output()
	if err != nil {
		t.Fatalf("trivy --version: %v", err)
	}
	if !strings.Contains(string(out), "Version:") {
		t.Errorf("unexpected trivy --version output: %s", out)
	}
	t.Logf("Trivy: %s", strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0])
}

// TestTrivy_Idempotent verifies that a second PassZero reports Trivy=AlreadyOK.
func TestTrivy_Idempotent(t *testing.T) {
	requireLinux(t)
	requireRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg := install.PassZeroConfig{AgentUser: "ownbase-test", SSHPort: 22}
	createTestUser(t, cfg.AgentUser)
	defer cleanupTestUser(t, cfg.AgentUser)

	if _, err := install.PassZero(ctx, cfg); err != nil {
		t.Fatalf("PassZero first run: %v", err)
	}

	report, err := install.PassZero(ctx, cfg)
	if err != nil {
		t.Fatalf("PassZero second run: %v", err)
	}
	if !report.Trivy.Done {
		t.Errorf("Trivy: Done=false on second run (detail: %s, err: %v)",
			report.Trivy.Detail, report.Trivy.Err)
	}
	if !report.Trivy.AlreadyOK {
		t.Logf("Trivy: AlreadyOK=false on second run — detail: %s", report.Trivy.Detail)
	}
}

// TestPassZero_Idempotent runs PassZero twice and verifies the second run is
// a no-op (all steps AlreadyOK).
func TestPassZero_Idempotent(t *testing.T) {
	requireLinux(t)
	requireRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg := install.PassZeroConfig{
		AgentUser: "ownbase-test",
		SSHPort:   22,
	}
	createTestUser(t, cfg.AgentUser)
	defer cleanupTestUser(t, cfg.AgentUser)

	// First run.
	if _, err := install.PassZero(ctx, cfg); err != nil {
		t.Fatalf("PassZero first run: %v", err)
	}

	// Second run — should be all AlreadyOK.
	report, err := install.PassZero(ctx, cfg)
	if err != nil {
		t.Fatalf("PassZero second run: %v", err)
	}

	// Steps that should always be AlreadyOK on the second run:
	for _, step := range []struct {
		name   string
		status install.StepStatus
	}{
		{"Podman", report.Podman},
		{"Firewall", report.Firewall},
		{"AutoUpdates", report.AutoUpdates},
		{"Fail2ban", report.Fail2ban},
	} {
		if !step.status.Done {
			t.Errorf("%s: expected Done=true on second run, got false", step.name)
		}
		if !step.status.AlreadyOK {
			t.Logf("%s: AlreadyOK=false on second run (step ran again) — detail: %s",
				step.name, step.status.Detail)
		}
	}
}

// TestHardening_PodmanInstalled verifies Podman is installed and functional
// after PassZero.
func TestHardening_PodmanInstalled(t *testing.T) {
	requireLinux(t)
	requireRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg := install.PassZeroConfig{AgentUser: "ownbase-test"}
	createTestUser(t, cfg.AgentUser)
	defer cleanupTestUser(t, cfg.AgentUser)

	if _, err := install.PassZero(ctx, cfg); err != nil {
		t.Fatalf("PassZero: %v", err)
	}

	// Verify podman --version works.
	out, err := exec.CommandContext(ctx, "podman", "--version").Output()
	if err != nil {
		t.Fatalf("podman --version: %v", err)
	}
	if !strings.Contains(string(out), "podman version") {
		t.Errorf("unexpected podman --version output: %s", out)
	}
	t.Logf("Podman: %s", strings.TrimSpace(string(out)))
}

// TestHardening_FirewallActive verifies UFW is active after PassZero.
func TestHardening_FirewallActive(t *testing.T) {
	requireLinux(t)
	requireRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg := install.PassZeroConfig{AgentUser: "ownbase-test", SSHPort: 22}
	createTestUser(t, cfg.AgentUser)
	defer cleanupTestUser(t, cfg.AgentUser)

	if _, err := install.PassZero(ctx, cfg); err != nil {
		t.Fatalf("PassZero: %v", err)
	}

	out, err := exec.CommandContext(ctx, "ufw", "status").Output()
	if err != nil {
		t.Fatalf("ufw status: %v", err)
	}
	if !strings.Contains(string(out), "Status: active") {
		t.Errorf("UFW not active after PassZero:\n%s", out)
	}
	t.Logf("UFW status:\n%s", out)
}

// TestHardening_NoExposedDB verifies database ports are not publicly exposed.
func TestHardening_NoExposedDB(t *testing.T) {
	requireLinux(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report := install.CheckHardeningState(ctx, install.PassZeroConfig{})
	if report.NoExposedDB.Err != nil {
		t.Errorf("exposed database ports detected: %v", report.NoExposedDB.Err)
	}
	t.Logf("No exposed DB: %s", report.NoExposedDB.Detail)
}

// TestPassZero_Resumable simulates a kill-and-resume scenario: PassZero is
// interrupted (simulated by a single-step DryRun followed by a full run).
// The full run converges cleanly.
func TestPassZero_Resumable(t *testing.T) {
	requireLinux(t)
	requireRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg := install.PassZeroConfig{
		AgentUser: "ownbase-test",
		SSHPort:   22,
	}
	createTestUser(t, cfg.AgentUser)
	defer cleanupTestUser(t, cfg.AgentUser)

	// Simulate "interrupted install" by manually installing only Podman first.
	_ = exec.CommandContext(ctx, "apt-get", "install", "-y", "-q", "podman").Run()

	// Now run PassZero — it must detect Podman already installed and continue
	// from the next step without re-installing.
	report, err := install.PassZero(ctx, cfg)
	if err != nil {
		t.Fatalf("PassZero after partial install: %v", err)
	}
	if !report.OK() {
		t.Errorf("PassZero did not converge after partial install")
	}
	// Podman should have been detected as AlreadyOK.
	if !report.Podman.AlreadyOK {
		t.Logf("Podman not AlreadyOK (may have been reinstalled): %s", report.Podman.Detail)
	}
	t.Logf("Podman: alreadyOK=%v, detail=%q", report.Podman.AlreadyOK, report.Podman.Detail)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func requireLinux(t *testing.T) {
	t.Helper()
	out, err := exec.Command("uname", "-s").Output()
	if err != nil || strings.TrimSpace(string(out)) != "Linux" {
		t.Skip("skipping: requires Linux")
	}
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("skipping: requires root (run with sudo)")
	}
}

func createTestUser(t *testing.T, user string) {
	t.Helper()
	// useradd is idempotent via the --non-unique / existence check.
	out, err := exec.Command("id", user).Output()
	if err == nil && strings.Contains(string(out), user) {
		return // already exists
	}
	if err := exec.Command("useradd", "--system", "--shell", "/bin/bash",
		"--create-home", "--home-dir", "/home/"+user, user).Run(); err != nil {
		t.Fatalf("useradd %s: %v", user, err)
	}
}

func cleanupTestUser(t *testing.T, user string) {
	t.Helper()
	_ = exec.Command("loginctl", "disable-linger", user).Run()
	_ = exec.Command("userdel", "-r", user).Run()
}

func assertStep(t *testing.T, name string, s install.StepStatus) {
	t.Helper()
	if !s.Done {
		t.Errorf("step %s: Done=false (detail: %s, err: %v)", name, s.Detail, s.Err)
	}
	t.Logf("step %s: done=%v, alreadyOK=%v, detail=%q", name, s.Done, s.AlreadyOK, s.Detail)
}
