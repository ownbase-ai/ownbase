// Package backup implements encrypted, deduplicated, off-machine backups using
// restic, Postgres PITR via pgBackRest, and the verified-restore drill that
// makes the "restorable" claim measurable rather than assumed. M6.
//
// # Architecture
//
// The backup loop runs as a second ticker inside the agent alongside the
// reconcile ticker. Each cycle:
//
//  1. [restic] back up all data paths → snapshot
//  2. [restic] forget + prune per retention policy
//  3. [pgBackRest] archive WAL segments (Postgres only, conditional)
//
// The verified-restore job runs on its own (slower) cadence:
//
//  1. Restore latest snapshot into an ephemeral throwaway directory
//  2. Run integrity checks (restic check --read-data-subset, file presence,
//     optional pg_controldata for Postgres consistency)
//  3. Tear down the throwaway directory
//  4. Update Status.Restorable; only report "restorable" on a pass
//
// # Destination model
//
// The restic repository URL is the single abstraction point.  For dev/test,
// use a local path (e.g. /tmp/ownbase-backup-test).  For production use any
// restic-supported backend: B2, S3, SFTP, etc.  The deferred backup node is
// a second `restic copy` from the primary repository — no structural change
// required.
//
// # Decisions locked (M6)
//
//   - restic (not kopia): encrypts-first, mature, any backend, simple CLI.
//   - pgBackRest (not wal-g): richer PITR UI; conditional on Postgres presence.
//   - Cadence: backup every 1h, verified restore every 24h, keep 30 days.
//   - V1 integrity check: restic check --read-data-subset + file presence +
//     optional pg_controldata.  Starting containers in the isolated env is M9.
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/configsource"
	"github.com/ownbase/ownbase/internal/gitssh"
	"github.com/ownbase/ownbase/internal/repos"
	"github.com/ownbase/ownbase/internal/schema"
)

// DefaultBackupInterval is how often a scheduled backup snapshot is taken.
const DefaultBackupInterval = time.Hour

// DefaultVerifyInterval is how often a verified restore drill is run.
const DefaultVerifyInterval = 24 * time.Hour

// DefaultRetentionDays is how many days of snapshots to keep.
const DefaultRetentionDays = 30

// DefaultStatusPath is where the backup status JSON is written.
const DefaultStatusPath = "/opt/ownbase/agent/backup-status.json"

// Config describes backup behaviour for one Base.
type Config struct {
	// Repository is the restic repository URL or local path.
	// Examples:
	//   /opt/ownbase/backup          (local, dev/test)
	//   s3:s3.amazonaws.com/bucket/ownbase
	//   b2:bucket-name:ownbase
	//   sftp:user@host:/path/to/repo
	Repository string

	// PasswordFile is the path to a file containing the restic repo password.
	// The secrets engine (M2) writes this file at start-up.
	// Fallback: the RESTIC_PASSWORD_FILE env var; or RESTIC_PASSWORD.
	PasswordFile string

	// Paths are the directories to include in each snapshot.
	// Assembled by backup.BuildPaths from the service declarations in
	// ownbase.yaml: DefaultPaths + resolved Podman volume mountpoints + core
	// volumes. When empty, withDefaults fills in DefaultPaths only.
	Paths []string

	// RetentionDays is how many days of daily snapshots to keep.
	// Default: DefaultRetentionDays (30).
	RetentionDays int

	// StatusPath is where the Status JSON is persisted.
	// Default: DefaultStatusPath.
	StatusPath string

	// Credentials are injected into the restic subprocess environment
	// (e.g. AWS_ACCESS_KEY_ID, RESTIC_PASSWORD). Decrypted from the
	// age-encrypted backup secret by the agent at runtime. Leave nil
	// in dev or rebuild — restic will read RESTIC_PASSWORD and AWS_*
	// from the ambient environment instead.
	Credentials map[string]string

	// DryRun makes all operations no-ops (useful for plan preview).
	DryRun bool

	// AuditLog receives one record per backup run and verify drill. When nil,
	// no audit records are emitted. Inject the production logger from the
	// agent; leave nil in tests that don't test audit emission.
	AuditLog authz.AuditLogger
}

// DefaultDataDir is the root directory of all service persistent data.
const DefaultDataDir = "/opt/ownbase/data"

// DefaultPaths is the set of directories included in every restic snapshot.
// These cover service data, the local bare clones of every service repo (the
// only copy of source code OwnBase controls without reaching the external git
// host — see internal/repos), the config-source pointer, the managed SSH
// identity used to reach external git (see internal/gitssh), age-encrypted
// secrets files, and the age private key. The config repo itself is external
// and reproducible from git, so it is not backed up here.
var DefaultPaths = []string{
	DefaultDataDir,                // /opt/ownbase/data
	repos.DefaultReposDir,         // one bare repo per service
	configsource.DefaultStatePath, // config-source pointer (repo_url + ref)
	gitssh.DefaultDir,             // managed SSH identity (keys + known_hosts)
	"/opt/ownbase/secrets",        // age-encrypted secrets files (one per service)
	"/opt/ownbase/age",            // age private key
}

func (c *Config) withDefaults() Config {
	out := *c
	if len(out.Paths) == 0 {
		out.Paths = DefaultPaths
	}
	if out.RetentionDays == 0 {
		out.RetentionDays = DefaultRetentionDays
	}
	if out.StatusPath == "" {
		out.StatusPath = DefaultStatusPath
	}
	return out
}

// Validate checks that the Config has the required fields.
func (c Config) Validate() error {
	if c.Repository == "" {
		return fmt.Errorf("backup: Repository is required")
	}
	return nil
}

// Status is the current backup posture. Persisted to disk so the briefing
// (M8) can read it without running a backup.
type Status struct {
	// LastBackup is when the most recent snapshot was taken.
	LastBackup time.Time `json:"last_backup,omitempty"`

	// LastVerified is when the most recent verified restore passed.
	LastVerified time.Time `json:"last_verified,omitempty"`

	// Restorable is true only after a verified restore drill has passed.
	// A backup that has never been verified is NOT restorable by definition.
	Restorable bool `json:"restorable"`

	// LatestSnapshot is the restic snapshot ID from the most recent backup.
	LatestSnapshot string `json:"latest_snapshot,omitempty"`

	// LastError holds the error from the most recent failed operation.
	// Empty when the last operation succeeded.
	LastError string `json:"last_error,omitempty"`
}

// Run performs one backup cycle: init repo if needed, take snapshot,
// forget+prune per retention policy, save status.
// Idempotent: safe to call repeatedly.
func Run(ctx context.Context, cfg Config) (Status, error) {
	c := cfg.withDefaults()
	if err := c.Validate(); err != nil {
		return Status{}, err
	}

	// Ensure the repository is initialized.
	if err := ensureRepoInit(ctx, c); err != nil {
		return Status{LastError: err.Error()},
			fmt.Errorf("backup: init repo: %w", err)
	}

	// Take a snapshot.
	snapshotID, err := takeSnapshot(ctx, c)
	if err != nil {
		s := Status{LastError: err.Error()}
		_ = SaveStatus(c.StatusPath, s)
		return s, fmt.Errorf("backup: snapshot: %w", err)
	}

	// Prune old snapshots per retention policy.
	if err := pruneOld(ctx, c); err != nil {
		// Non-fatal: pruning failure does not invalidate the backup.
		fmt.Fprintf(os.Stderr, "backup: prune: %v (non-fatal)\n", err)
	}

	s := Status{
		LastBackup:     time.Now().UTC(),
		LatestSnapshot: snapshotID,
	}

	// Preserve the Restorable flag from the previous status (a new backup
	// does not automatically reset it — only a new verify pass changes it).
	if prev, err := LoadStatus(c.StatusPath); err == nil {
		s.Restorable = prev.Restorable
		s.LastVerified = prev.LastVerified
	}

	if err := SaveStatus(c.StatusPath, s); err != nil {
		fmt.Fprintf(os.Stderr, "backup: save status: %v (non-fatal)\n", err)
	}

	// Emit audit record so the user-owned log is exhaustive.
	if c.AuditLog != nil {
		action, _ := schema.NewAction(schema.ActionBackupRun, "base")
		_ = c.AuditLog.Record(action, authz.OutcomeApplied, "")
	}

	return s, nil
}

// LoadStatus reads the persisted Status from disk.
// Returns a zero Status (Restorable=false) if the file does not exist.
func LoadStatus(path string) (Status, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Status{}, nil
		}
		return Status{}, fmt.Errorf("read backup status: %w", err)
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, fmt.Errorf("parse backup status: %w", err)
	}
	return s, nil
}

// SaveStatus writes Status to disk atomically (write-then-rename).
func SaveStatus(path string, s Status) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
