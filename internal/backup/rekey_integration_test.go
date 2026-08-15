//go:build integration

package backup_test

// Tier-2 tests for restic password rotation and owner-driven prune.
// Require restic on PATH (CI installs it). Local path repos only.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/backup"
)

// resticOpens reports whether password opens the local repository at repo.
// Uses the restic binary directly so the test does not need unexported helpers.
func resticOpens(t *testing.T, repo, password string) bool {
	t.Helper()
	cmd := exec.Command("restic", "cat", "config")
	cmd.Env = append(os.Environ(),
		"RESTIC_REPOSITORY="+repo,
		"RESTIC_PASSWORD="+password,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("restic cat config (pw opens? no): %v\n%s", err, out)
		return false
	}
	return true
}

func resticKeyCount(t *testing.T, repo, password string) int {
	t.Helper()
	cmd := exec.Command("restic", "key", "list", "--json")
	cmd.Env = append(os.Environ(),
		"RESTIC_REPOSITORY="+repo,
		"RESTIC_PASSWORD="+password,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restic key list: %v\n%s", err, out)
	}
	// Count {"id": occurrences — good enough for a small keyring.
	n := strings.Count(string(out), `"id"`)
	if n == 0 {
		// Some restic versions nest differently; fall back to non-empty lines.
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "{") {
				n++
			}
		}
	}
	return n
}

func TestRekey_AddThenFinalize(t *testing.T) {
	requireLinux(t)
	requireRestic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg, dir := makeTestCfg(t)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "f.txt"), []byte("rekey"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Paths = []string{dataDir}

	// Seed a repo with the original password.
	if _, err := backup.Run(ctx, cfg); err != nil {
		t.Fatalf("initial Run: %v", err)
	}
	oldPW := "test-password-m6"
	newPW := "replacement-password-rekey-32chars!!"

	if !resticOpens(t, cfg.Repository, oldPW) {
		t.Fatal("old password should open the repo after Run")
	}

	add, err := backup.Rekey(ctx, cfg, backup.RekeyPhaseAdd, newPW)
	if err != nil {
		t.Fatalf("Rekey add: %v", err)
	}
	if add.Fingerprint != backup.CredFingerprint(newPW) {
		t.Errorf("add fingerprint = %q, want %q", add.Fingerprint, backup.CredFingerprint(newPW))
	}
	if !resticOpens(t, cfg.Repository, oldPW) {
		t.Error("old password must still open after add")
	}
	if !resticOpens(t, cfg.Repository, newPW) {
		t.Fatal("new password must open after add")
	}
	if n := resticKeyCount(t, cfg.Repository, newPW); n < 2 {
		t.Fatalf("expected ≥2 keys after add, got %d", n)
	}

	// Finalize authenticates with the new password (daemon swaps Base secret first).
	finCfg := cfg
	finCfg.PasswordFile = ""
	finCfg.Credentials = map[string]string{"RESTIC_PASSWORD": newPW}
	fin, err := backup.Rekey(ctx, finCfg, backup.RekeyPhaseFinalize, newPW)
	if err != nil {
		t.Fatalf("Rekey finalize: %v", err)
	}
	if fin.KeysRemoved < 1 {
		t.Errorf("expected at least one key removed, got %d", fin.KeysRemoved)
	}
	if !resticOpens(t, cfg.Repository, newPW) {
		t.Error("new password must open after finalize")
	}
	if resticOpens(t, cfg.Repository, oldPW) {
		t.Error("old password must NOT open after finalize")
	}
	if n := resticKeyCount(t, cfg.Repository, newPW); n != 1 {
		t.Errorf("expected exactly 1 key after finalize, got %d", n)
	}

	// Re-running finalize is idempotent.
	fin2, err := backup.Rekey(ctx, finCfg, backup.RekeyPhaseFinalize, newPW)
	if err != nil {
		t.Fatalf("Rekey finalize again: %v", err)
	}
	if !fin2.AlreadyDone && fin2.KeysRemoved != 0 {
		t.Errorf("second finalize should be a no-op: %+v", fin2)
	}
}

func TestPrune_UpdatesLastPrune(t *testing.T) {
	requireLinux(t)
	requireRestic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg, dir := makeTestCfg(t)
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "p.txt"), []byte("prune"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Paths = []string{dataDir}
	cfg.SkipPrune = true
	cfg.RetentionDays = 1

	// Two snapshots without pruning.
	if _, err := backup.Run(ctx, cfg); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "p2.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Run(ctx, cfg); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	loaded, _ := backup.LoadStatus(cfg.StatusPath)
	if !loaded.LastPrune.IsZero() {
		t.Error("LastPrune should stay zero while SkipPrune is set")
	}

	s, err := backup.Prune(ctx, cfg)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if s.LastPrune.IsZero() {
		t.Error("LastPrune should be set after Prune")
	}
	loaded, err = backup.LoadStatus(cfg.StatusPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastPrune.IsZero() {
		t.Error("persisted LastPrune should be set")
	}
}
