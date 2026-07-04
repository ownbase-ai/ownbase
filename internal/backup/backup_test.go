package backup_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/backup"
)

// Tier-1 tests: pure logic that does not require restic or a Linux host.
// Tests for Config validation, Status persistence, and the dry-run path.

// ---------------------------------------------------------------------------
// Config.Validate
// ---------------------------------------------------------------------------

func TestConfig_Validate_MissingRepository(t *testing.T) {
	cfg := backup.Config{}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing Repository, got nil")
	}
}

func TestConfig_Validate_OK(t *testing.T) {
	cfg := backup.Config{Repository: "/tmp/test-repo"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Status persistence (LoadStatus / SaveStatus)
// ---------------------------------------------------------------------------

func TestStatus_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup-status.json")

	want := backup.Status{
		LastBackup:     time.Now().UTC().Truncate(time.Second),
		LastVerified:   time.Now().UTC().Truncate(time.Second),
		Restorable:     true,
		LatestSnapshot: "abc123",
		LastError:      "",
	}

	if err := backup.SaveStatus(path, want); err != nil {
		t.Fatalf("SaveStatus: %v", err)
	}

	got, err := backup.LoadStatus(path)
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}

	if !got.LastBackup.Equal(want.LastBackup) {
		t.Errorf("LastBackup: got %v, want %v", got.LastBackup, want.LastBackup)
	}
	if got.Restorable != want.Restorable {
		t.Errorf("Restorable: got %v, want %v", got.Restorable, want.Restorable)
	}
	if got.LatestSnapshot != want.LatestSnapshot {
		t.Errorf("LatestSnapshot: got %q, want %q", got.LatestSnapshot, want.LatestSnapshot)
	}
}

func TestStatus_LoadMissing_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	s, err := backup.LoadStatus(filepath.Join(dir, "no-such-file.json"))
	if err != nil {
		t.Fatalf("LoadStatus: %v", err)
	}
	if s.Restorable {
		t.Error("missing status should return Restorable=false")
	}
}

func TestStatus_SaveAtomic(t *testing.T) {
	// SaveStatus uses write-then-rename; no .tmp file should survive.
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	if err := backup.SaveStatus(path, backup.Status{Restorable: true}); err != nil {
		t.Fatalf("SaveStatus: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "status.json.tmp" {
			t.Error("tmp file should not exist after SaveStatus")
		}
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 file in dir, got %d: %v", len(entries), entries)
	}
}

func TestStatus_JSONFormat(t *testing.T) {
	// The JSON must use snake_case keys so the briefing (M8) can parse it.
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	ts := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	_ = backup.SaveStatus(path, backup.Status{
		LastBackup:     ts,
		Restorable:     true,
		LatestSnapshot: "snap1",
	})

	data, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}
	for _, key := range []string{"restorable", "latest_snapshot", "last_backup"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected key %q in JSON, not found\n%s", key, data)
		}
	}
}

// ---------------------------------------------------------------------------
// Restorable: false by default, only true after verify
// ---------------------------------------------------------------------------

func TestStatus_Restorable_DefaultFalse(t *testing.T) {
	dir := t.TempDir()
	s, _ := backup.LoadStatus(filepath.Join(dir, "missing.json"))
	if s.Restorable {
		t.Error("Restorable should be false when status file does not exist")
	}
}

// ---------------------------------------------------------------------------
// Run dry-run — no restic required
// ---------------------------------------------------------------------------

func TestRun_DryRun_NoResticRequired(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := backup.Config{
		Repository: filepath.Join(dir, "repo"),
		StatusPath: filepath.Join(dir, "status.json"),
		DryRun:     true,
	}

	s, err := backup.Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	// Dry-run returns a placeholder snapshot ID.
	if s.LatestSnapshot == "" {
		t.Error("dry-run should return a non-empty LatestSnapshot")
	}
}

// ---------------------------------------------------------------------------
// Rebuild dry-run
// ---------------------------------------------------------------------------

func TestRebuild_DryRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Write a status with Restorable=true and a snapshot ID.
	statusPath := filepath.Join(dir, "status.json")
	_ = backup.SaveStatus(statusPath, backup.Status{
		Restorable:     true,
		LatestSnapshot: "abc123",
	})

	err := backup.Rebuild(ctx, backup.RebuildConfig{
		BackupConfig: backup.Config{
			Repository: filepath.Join(dir, "repo"),
			StatusPath: statusPath,
		},
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("Rebuild dry-run: %v", err)
	}
}

func TestRebuild_NotRestorable_Fails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "status.json")

	// Status exists but Restorable=false.
	_ = backup.SaveStatus(statusPath, backup.Status{Restorable: false})

	err := backup.Rebuild(ctx, backup.RebuildConfig{
		BackupConfig: backup.Config{
			Repository: filepath.Join(dir, "repo"),
			StatusPath: statusPath,
		},
		DryRun: false, // real mode — should fail on Restorable=false
	})
	if err == nil {
		t.Error("expected error when Restorable=false, got nil")
	}
}

// TestRebuild_FreshMachine_NoStatusFile_Succeeds covers the normal `base
// restore` case: a brand-new machine with no local backup-status.json at
// all (the verified-restore history lives on the machine being rebuilt, not
// in the backup repo). The Restorable guard must not fire here, or every
// documented restore would require --force-rebuild.
func TestRebuild_FreshMachine_NoStatusFile_Succeeds(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "status.json") // never created — fresh machine

	err := backup.Rebuild(ctx, backup.RebuildConfig{
		BackupConfig: backup.Config{
			Repository: filepath.Join(dir, "repo"),
			StatusPath: statusPath,
		},
		DryRun: true, // avoid needing a real restic repo to preview
	})
	if err != nil {
		t.Fatalf("Rebuild on fresh machine (no status file) should not require --force-rebuild: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Audit emission (M12)
// ---------------------------------------------------------------------------

func TestRun_DryRun_EmitsAuditRecord(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	al := &authz.MemAuditLog{}
	cfg := backup.Config{
		Repository: filepath.Join(dir, "repo"),
		StatusPath: filepath.Join(dir, "status.json"),
		DryRun:     true,
		AuditLog:   al,
	}

	if _, err := backup.Run(ctx, cfg); err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}

	if len(al.Records) != 1 {
		t.Fatalf("expected 1 audit record, got %d: %+v", len(al.Records), al.Records)
	}
	r := al.Records[0]
	if r.Action != "backup.run" {
		t.Errorf("audit record Action = %q, want %q", r.Action, "backup.run")
	}
	if r.Outcome != authz.OutcomeApplied {
		t.Errorf("audit record Outcome = %q, want %q", r.Outcome, authz.OutcomeApplied)
	}
}

func TestRun_NoAuditLog_DoesNotPanic(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := backup.Config{
		Repository: filepath.Join(dir, "repo"),
		StatusPath: filepath.Join(dir, "status.json"),
		DryRun:     true,
		// AuditLog intentionally nil — must not panic.
	}
	if _, err := backup.Run(ctx, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Credentials field
// ---------------------------------------------------------------------------

func TestConfig_Credentials_DryRun(t *testing.T) {
	// Verify that Config.Credentials is accepted and does not break dry-run.
	ctx := context.Background()
	dir := t.TempDir()
	cfg := backup.Config{
		Repository: filepath.Join(dir, "repo"),
		StatusPath: filepath.Join(dir, "status.json"),
		DryRun:     true,
		Credentials: map[string]string{
			"AWS_ACCESS_KEY_ID":     "AKIAIOSFODNN7EXAMPLE",
			"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"RESTIC_PASSWORD":       "hunter2",
		},
	}
	if _, err := backup.Run(ctx, cfg); err != nil {
		t.Fatalf("Run with Credentials in dry-run: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	if backup.DefaultRetentionDays != 30 {
		t.Errorf("DefaultRetentionDays = %d, want 30", backup.DefaultRetentionDays)
	}
	if backup.DefaultBackupInterval != time.Hour {
		t.Errorf("DefaultBackupInterval = %v, want 1h", backup.DefaultBackupInterval)
	}
	if backup.DefaultVerifyInterval != 24*time.Hour {
		t.Errorf("DefaultVerifyInterval = %v, want 24h", backup.DefaultVerifyInterval)
	}
}
