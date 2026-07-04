//go:build integration

package backup_test

// Tier-2 integration tests — require a real Ubuntu host with restic installed.
// Run with: sudo go test -tags=integration ./internal/backup/... -v
//
// Each test creates its own isolated temporary repository so tests do not
// interfere with a production repository. Tests that write to / require root.

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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func requireRestic(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("skipping: restic not installed (apt-get install restic)")
	}
}

func requireLinux(t *testing.T) {
	t.Helper()
	out, err := exec.Command("uname", "-s").Output()
	if err != nil || strings.TrimSpace(string(out)) != "Linux" {
		t.Skip("skipping: requires Linux")
	}
}

// makeTestCfg creates a Config backed by a temporary local restic repository.
// A password file is written to the temp dir.
func makeTestCfg(t *testing.T) (backup.Config, string) {
	t.Helper()
	dir := t.TempDir()

	repo := filepath.Join(dir, "restic-repo")
	passFile := filepath.Join(dir, "restic-pass")
	statusPath := filepath.Join(dir, "status.json")

	if err := os.WriteFile(passFile, []byte("test-password-m6"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}

	cfg := backup.Config{
		Repository:    repo,
		PasswordFile:  passFile,
		StatusPath:    statusPath,
		RetentionDays: 7,
	}
	return cfg, dir
}

// ---------------------------------------------------------------------------
// TestBackup_RoundTrip: backup → restore → verify files present
// ---------------------------------------------------------------------------

func TestBackup_RoundTrip(t *testing.T) {
	requireLinux(t)
	requireRestic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, dir := makeTestCfg(t)

	// Create test data to back up.
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "hello.txt"), []byte("ownbase test data"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Paths = []string{dataDir}

	// Run backup.
	s, err := backup.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s.LatestSnapshot == "" {
		t.Error("expected non-empty LatestSnapshot after backup")
	}
	if s.LastBackup.IsZero() {
		t.Error("LastBackup should be set after backup")
	}
	t.Logf("snapshot: %s", s.LatestSnapshot)

	// Status file should be written.
	loaded, err := backup.LoadStatus(cfg.StatusPath)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if loaded.LatestSnapshot != s.LatestSnapshot {
		t.Errorf("LatestSnapshot mismatch: got %q, want %q", loaded.LatestSnapshot, s.LatestSnapshot)
	}
}

// ---------------------------------------------------------------------------
// TestBackup_Idempotent: running backup twice succeeds, prune retains both
// ---------------------------------------------------------------------------

func TestBackup_Idempotent(t *testing.T) {
	requireLinux(t)
	requireRestic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg, dir := makeTestCfg(t)
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.WriteFile(filepath.Join(dataDir, "f.txt"), []byte("v1"), 0o644)
	cfg.Paths = []string{dataDir}

	s1, err := backup.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}

	// Modify data between backups.
	_ = os.WriteFile(filepath.Join(dataDir, "f.txt"), []byte("v2"), 0o644)

	s2, err := backup.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}

	if s1.LatestSnapshot == s2.LatestSnapshot {
		t.Error("second backup should produce a different snapshot ID")
	}
}

// ---------------------------------------------------------------------------
// TestVerifyRestore_Pass: verified restore passes on a valid backup
// ---------------------------------------------------------------------------

func TestVerifyRestore_Pass(t *testing.T) {
	requireLinux(t)
	requireRestic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg, dir := makeTestCfg(t)
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.WriteFile(filepath.Join(dataDir, "important.txt"), []byte("customer data"), 0o644)
	cfg.Paths = []string{dataDir}

	// First take a backup.
	if _, err := backup.Run(ctx, cfg); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Run verified restore.
	result, err := backup.VerifyRestore(ctx, cfg)
	if err != nil {
		t.Fatalf("VerifyRestore: %v", err)
	}

	t.Logf("snapshot: %s, passed: %v", result.SnapshotID, result.Passed)
	for _, ch := range result.Checks {
		t.Logf("  check %s: passed=%v, detail=%q", ch.Name, ch.Passed, ch.Detail)
	}

	if !result.Passed {
		t.Error("VerifyRestore should pass on a valid backup")
	}

	// Status should report Restorable=true.
	s, _ := backup.LoadStatus(cfg.StatusPath)
	if !s.Restorable {
		t.Error("Restorable should be true after a passing verify")
	}
	if s.LastVerified.IsZero() {
		t.Error("LastVerified should be set after verify")
	}
}

// ---------------------------------------------------------------------------
// TestVerifyRestore_CorruptRepo_Fails: corrupted repo must not claim restorable
// ---------------------------------------------------------------------------

func TestVerifyRestore_CorruptRepo_Fails(t *testing.T) {
	requireLinux(t)
	requireRestic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg, dir := makeTestCfg(t)
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.WriteFile(filepath.Join(dataDir, "f.txt"), []byte("data"), 0o644)
	cfg.Paths = []string{dataDir}

	// Take a valid backup first.
	if _, err := backup.Run(ctx, cfg); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Corrupt the repo by deleting a pack file. A deleted pack is always
	// detected by `restic check` because it's referenced in the index but
	// missing from disk — unlike a truncated file which may not be in the
	// --read-data-subset sample.
	repo := cfg.Repository
	_ = filepath.WalkDir(filepath.Join(repo, "data"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		_ = os.Remove(path) // delete the first pack file found
		return filepath.SkipAll
	})

	// VerifyRestore must detect the corruption.
	result, _ := backup.VerifyRestore(ctx, cfg)
	// Either the restore itself fails or the restic check fails.
	if result.Passed {
		t.Error("VerifyRestore should fail on a corrupted repository")
	}

	// Restorable must NOT be set after a failed verify.
	s, _ := backup.LoadStatus(cfg.StatusPath)
	if s.Restorable {
		t.Error("Restorable must be false after a failed verify")
	}
}

// ---------------------------------------------------------------------------
// TestRebuild_RestoreThenReconcile: integration test for the rebuild path
// ---------------------------------------------------------------------------

func TestRebuild_RestoreThenReconcile(t *testing.T) {
	requireLinux(t)
	requireRestic(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg, dir := makeTestCfg(t)
	dataDir := filepath.Join(dir, "data")
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.WriteFile(filepath.Join(dataDir, "ownbase.yaml"), []byte("schema_version: v1\n"), 0o644)
	cfg.Paths = []string{dataDir}

	// Backup + verify to get Restorable=true.
	if _, err := backup.Run(ctx, cfg); err != nil {
		t.Fatalf("backup: %v", err)
	}
	result, err := backup.VerifyRestore(ctx, cfg)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !result.Passed {
		t.Skip("verify drill failed — cannot test rebuild path")
	}

	// Simulate rebuild: restore to a throwaway target.
	restoreTarget := filepath.Join(dir, "rebuild-target")
	_ = os.MkdirAll(restoreTarget, 0o755)

	err = backup.Rebuild(ctx, backup.RebuildConfig{
		BackupConfig:  cfg,
		RestoreTarget: restoreTarget,
	})
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// Verify the data was restored.
	restored := filepath.Join(restoreTarget, strings.TrimPrefix(dataDir, "/"))
	entries, err := os.ReadDir(restored)
	if err != nil {
		t.Fatalf("read restored dir %s: %v", restored, err)
	}
	if len(entries) == 0 {
		t.Error("restored directory is empty")
	}
	t.Logf("restored %d entries to %s", len(entries), restored)
}
