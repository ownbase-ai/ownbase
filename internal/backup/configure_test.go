package backup

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
)

// TestSetCoreBackupConfig covers the text-preserving core.backup: editor
// used by the /backup/configure API.

func TestSetCoreBackupConfig_NoCoreBlock(t *testing.T) {
	in := "schema_version: v1\nservices:\n  crm:\n    source: services/crm\n"
	out := SetCoreBackupConfig(in, "s3:s3.amazonaws.com/bucket/ownbase", "1h", "24h")

	assertParsesWithBackup(t, out, "s3:s3.amazonaws.com/bucket/ownbase", "1h", "24h")
	if !strings.Contains(out, "services:") {
		t.Errorf("expected existing services: block preserved, got:\n%s", out)
	}
}

func TestSetCoreBackupConfig_CoreBlockNoBackup(t *testing.T) {
	in := "schema_version: v1\ncore:\n  caddy:\n    email: you@example.com\nservices: {}\n"
	out := SetCoreBackupConfig(in, "/opt/ownbase/backup", "", "")

	assertParsesWithBackup(t, out, "/opt/ownbase/backup", "", "")
	if !strings.Contains(out, "email: you@example.com") {
		t.Errorf("expected caddy.email preserved, got:\n%s", out)
	}
}

func TestSetCoreBackupConfig_ExistingBackupBlock_UpdatesRepo(t *testing.T) {
	in := "schema_version: v1\ncore:\n  backup:\n    repo: /old/path\n    interval: 2h\nservices: {}\n"
	out := SetCoreBackupConfig(in, "s3:new/bucket", "", "")

	assertParsesWithBackup(t, out, "s3:new/bucket", "2h", "")
	if strings.Contains(out, "/old/path") {
		t.Errorf("expected old repo replaced, got:\n%s", out)
	}
}

func TestSetCoreBackupConfig_AddsIntervalToExistingBlock(t *testing.T) {
	in := "schema_version: v1\ncore:\n  backup:\n    repo: /data/backup\nservices: {}\n"
	out := SetCoreBackupConfig(in, "/data/backup", "30m", "12h")

	assertParsesWithBackup(t, out, "/data/backup", "30m", "12h")
}

func TestSetCoreBackupConfig_Idempotent(t *testing.T) {
	in := "schema_version: v1\nservices: {}\n"
	once := SetCoreBackupConfig(in, "/data/backup", "1h", "24h")
	twice := SetCoreBackupConfig(once, "/data/backup", "1h", "24h")
	if once != twice {
		t.Errorf("expected idempotent result, got:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

// assertParsesWithBackup parses out as ownbase.yaml and checks the resulting
// BackupCoreConfig matches.
func assertParsesWithBackup(t *testing.T, out, repo, interval, verifyInterval string) {
	t.Helper()
	cfg, err := schema.ParseConfig(strings.NewReader(out))
	if err != nil {
		t.Fatalf("output is not valid ownbase.yaml: %v\n%s", err, out)
	}
	if cfg.Core.Backup.Repo != repo {
		t.Errorf("repo = %q, want %q\noutput:\n%s", cfg.Core.Backup.Repo, repo, out)
	}
	if interval != "" && cfg.Core.Backup.Interval != interval {
		t.Errorf("interval = %q, want %q\noutput:\n%s", cfg.Core.Backup.Interval, interval, out)
	}
	if verifyInterval != "" && cfg.Core.Backup.VerifyInterval != verifyInterval {
		t.Errorf("verify_interval = %q, want %q\noutput:\n%s", cfg.Core.Backup.VerifyInterval, verifyInterval, out)
	}
}
