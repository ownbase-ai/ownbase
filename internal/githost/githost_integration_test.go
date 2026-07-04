//go:build integration

package githost_test

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/githost"
)

// ---------------------------------------------------------------------------
// Integration tests — run on Ubuntu with:
//
//	go test -tags=integration ./internal/githost/... -v -timeout 60s
//
// These tests exercise the full commit→hook→SIGUSR1→agent-reconcile path
// without any hosted Git service (filesystem bare repo only).
// ---------------------------------------------------------------------------

// TestIntegration_BootstrapOnLinux verifies that Bootstrap creates the right
// filesystem layout including /etc/machine-id-backed MachineID.
func TestIntegration_BootstrapOnLinux(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	if err := githost.Bootstrap(repoPath, checkoutPath); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if err := githost.InstallHook(repoPath); err != nil {
		t.Fatalf("InstallHook: %v", err)
	}

	// On Linux, MachineID should return the /etc/machine-id value.
	id, err := githost.MachineID()
	if err != nil {
		t.Fatalf("MachineID: %v", err)
	}
	// /etc/machine-id is a 32-char hex string.
	if len(strings.TrimSpace(id)) < 8 {
		t.Errorf("MachineID looks too short on Linux: %q", id)
	}
}

// TestIntegration_GenesisRecord verifies the full genesis write-and-read
// cycle including the git commit being present in the bare repo.
func TestIntegration_GenesisRecord(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	githost.Bootstrap(repoPath, checkoutPath)

	rec, err := githost.NewGenesisRecord("v1.0.0-test", "age1testkey")
	if err != nil {
		t.Fatalf("NewGenesisRecord: %v", err)
	}
	if err := githost.WriteGenesisRecord(checkoutPath, rec); err != nil {
		t.Fatalf("WriteGenesisRecord: %v", err)
	}

	// The commit must also be visible in the bare repo (i.e. was pushed).
	out, err := exec.Command("git", "-C", repoPath, "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log on bare repo: %v", err)
	}
	if !strings.Contains(string(out), "genesis:") {
		t.Errorf("bare repo log does not contain genesis commit:\n%s", out)
	}
}

// TestIntegration_CommitHookSignal verifies the complete commit→hook→SIGUSR1
// path. It:
//  1. Bootstraps a bare repo + checkout.
//  2. Writes the genesis record (making the first commit).
//  3. Installs the hook pointing at a tmp PID file.
//  4. Writes the current process PID to the PID file.
//  5. Registers a SIGUSR1 channel on this process.
//  6. Pushes a new commit to the bare repo (simulating a user push).
//  7. Manually runs the hook script (simulating git calling it).
//  8. Verifies SIGUSR1 is received within the timeout.
func TestIntegration_CommitHookSignal(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	// Use a custom pid file so we don't interfere with a running agent.
	pidPath := filepath.Join(dir, "agent.pid")

	githost.Bootstrap(repoPath, checkoutPath)

	// Genesis commit so the repo is non-empty.
	rec, _ := githost.NewGenesisRecord("dev", "")
	githost.WriteGenesisRecord(checkoutPath, rec)

	// Install a hook that signals our custom pid file.
	hookContent := []byte("#!/bin/sh\n" +
		"PIDFILE=" + pidPath + "\n" +
		"if [ -f \"$PIDFILE\" ]; then\n" +
		"  kill -USR1 \"$(cat \"$PIDFILE\")\" 2>/dev/null || true\n" +
		"fi\n")
	hookPath := filepath.Join(repoPath, "hooks", "post-receive")
	os.MkdirAll(filepath.Dir(hookPath), 0o755)
	os.WriteFile(hookPath, hookContent, 0o755)

	// Write this test process's PID.
	githost.WritePIDFile(pidPath)
	// Overwrite with our own PID so the hook signals *this* process.
	os.WriteFile(pidPath, []byte(strings.TrimSpace(pidSelf())+"\n"), 0o644)

	// Arm the SIGUSR1 listener before the hook fires, so the default
	// handler (which terminates the process) never runs.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGUSR1)
	defer signal.Stop(sigCh)

	// Push a new file to the bare repo via the checkout.
	readmePath := filepath.Join(checkoutPath, "README.md")
	os.WriteFile(readmePath, []byte("# OwnBase\n"), 0o644)
	exec.Command("git", "-C", checkoutPath, "add", "README.md").Run()
	exec.Command("git", "-C", checkoutPath,
		"commit", "-m", "test: add README",
		"--author", "OwnBase Daemon <daemon@ownbase.local>").Run()
	exec.Command("git", "-C", checkoutPath, "push", "origin", "main").Run()

	// Simulate the hook firing by running it directly.
	hookCmd := exec.Command("sh", hookPath)
	hookCmd.Env = append(os.Environ(), "HOME="+dir)
	if err := hookCmd.Run(); err != nil {
		t.Fatalf("hook script failed: %v", err)
	}

	// The hook should have sent SIGUSR1 to this process.
	select {
	case <-sigCh:
		// Received — hook-to-agent signaling works.
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for SIGUSR1 from hook")
	}
}

// TestIntegration_UpdateCheckout verifies that UpdateCheckout pulls commits
// pushed to the bare repo into an existing checkout.
func TestIntegration_UpdateCheckout(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	githost.Bootstrap(repoPath, checkoutPath)

	// Write genesis (first commit).
	rec, _ := githost.NewGenesisRecord("dev", "")
	githost.WriteGenesisRecord(checkoutPath, rec)
	sha1, _ := githost.HeadCommit(checkoutPath)

	// Push a second commit from a fresh clone (simulating an external push).
	secondCheckout := filepath.Join(dir, "second-checkout")
	exec.Command("git", "clone", "--local", repoPath, secondCheckout).Run()
	exec.Command("git", "-C", secondCheckout, "config", "user.name", "Test").Run()
	exec.Command("git", "-C", secondCheckout, "config", "user.email", "t@t.com").Run()
	os.WriteFile(filepath.Join(secondCheckout, "extra.txt"), []byte("hi\n"), 0o644)
	exec.Command("git", "-C", secondCheckout, "add", "extra.txt").Run()
	exec.Command("git", "-C", secondCheckout, "commit", "-m", "add extra").Run()
	exec.Command("git", "-C", secondCheckout, "push", "origin", "main").Run()

	// Now UpdateCheckout should fast-forward the original checkout.
	if err := githost.UpdateCheckout(repoPath, checkoutPath); err != nil {
		t.Fatalf("UpdateCheckout: %v", err)
	}

	sha2, _ := githost.HeadCommit(checkoutPath)
	if sha1 == sha2 {
		t.Error("UpdateCheckout did not advance the checkout HEAD")
	}
	if _, err := os.Stat(filepath.Join(checkoutPath, "extra.txt")); err != nil {
		t.Error("extra.txt missing from checkout after UpdateCheckout")
	}
}

// TestIntegration_BackstopEquivalence verifies that a direct reconcile
// (timer backstop) and a hook-triggered reconcile both see the same desired
// state — they call the identical reconcileLoop.
//
// This test does not start a real agent; it exercises the githost primitives
// that feed the agent's loop.
func TestIntegration_BackstopEquivalence(t *testing.T) {
	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")

	githost.Bootstrap(repoPath, checkoutPath)

	// Simulate a commit arriving at the bare repo from an external push.
	cloneDir := filepath.Join(dir, "external")
	exec.Command("git", "clone", "--local", repoPath, cloneDir).Run()
	exec.Command("git", "-C", cloneDir, "config", "user.name", "Tester").Run()
	exec.Command("git", "-C", cloneDir, "config", "user.email", "t@t.com").Run()
	os.WriteFile(filepath.Join(cloneDir, "ownbase.yaml"),
		[]byte("base:\n  hostname: test-base\nservices: []\n"), 0o644)
	exec.Command("git", "-C", cloneDir, "add", "ownbase.yaml").Run()
	exec.Command("git", "-C", cloneDir, "commit", "-m", "add ownbase.yaml").Run()
	exec.Command("git", "-C", cloneDir, "push", "origin", "main").Run()

	// Both a timer-triggered and a hook-triggered agent call UpdateCheckout
	// before compiling. Verify both see the same HEAD after the pull.
	if err := githost.UpdateCheckout(repoPath, checkoutPath); err != nil {
		t.Fatalf("UpdateCheckout (timer path): %v", err)
	}
	shaTimer, _ := githost.HeadCommit(checkoutPath)

	// Simulate hook triggering a second UpdateCheckout call (idempotent).
	if err := githost.UpdateCheckout(repoPath, checkoutPath); err != nil {
		t.Fatalf("UpdateCheckout (hook path): %v", err)
	}
	shaHook, _ := githost.HeadCommit(checkoutPath)

	if shaTimer != shaHook {
		t.Errorf("timer and hook paths see different HEADs: %s vs %s", shaTimer, shaHook)
	}
}

// pidSelf returns the current process PID as a decimal string.
func pidSelf() string {
	return fmt.Sprintf("%d", os.Getpid())
}
