package backup

// verify.go implements the verified-restore drill:
//
//  1. Restore the latest snapshot into an ephemeral isolated directory.
//  2. Run integrity checks (restic check, file presence, optional Postgres).
//  3. Tear down the ephemeral directory.
//  4. Set Status.Restorable = true only on a full pass.
//
// The key property: "restorable" is demonstrated on the customer's real data,
// not claimed because backups ran. A backup that has never been verified is
// not restorable by definition.
//
// Isolation: the ephemeral directory is separate from production data.
// The verify job never touches running services.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/schema"
)

// VerifyResult is the outcome of one verified-restore drill.
type VerifyResult struct {
	// Passed is true when every integrity check succeeded.
	Passed bool

	// SnapshotID is the snapshot that was restored.
	SnapshotID string

	// VerifiedAt is when the drill completed.
	VerifiedAt time.Time

	// Checks holds the per-check outcomes.
	Checks []CheckResult

	// Err holds the error if the drill itself failed (infrastructure failure,
	// not an integrity failure — the difference matters for alerting).
	Err error
}

// CheckResult is the outcome of one integrity check.
type CheckResult struct {
	Name   string
	Passed bool
	Detail string
}

// VerifyRestore runs the full verified-restore drill and updates the Status
// file with the result. The Status.Restorable field is set to true only if
// all checks pass.
func VerifyRestore(ctx context.Context, cfg Config) (VerifyResult, error) {
	c := cfg.withDefaults()
	if err := c.Validate(); err != nil {
		return VerifyResult{}, err
	}

	// 1. Find the latest snapshot.
	snapshotID, err := latestSnapshotID(ctx, c)
	if err != nil {
		return VerifyResult{Err: err}, fmt.Errorf("verify: find snapshot: %w", err)
	}

	// 2. Create an ephemeral isolated directory.
	tmpDir, err := os.MkdirTemp("", "ownbase-verify-*")
	if err != nil {
		return VerifyResult{Err: err}, fmt.Errorf("verify: mktempdir: %w", err)
	}
	defer func() {
		// Always tear down, even on failure.
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			fmt.Fprintf(os.Stderr, "verify: cleanup %s: %v\n", tmpDir, rmErr)
		}
	}()

	// 3. Restore the snapshot.
	if err := restoreSnapshot(ctx, c, snapshotID, tmpDir); err != nil {
		return VerifyResult{SnapshotID: snapshotID, Err: err},
			fmt.Errorf("verify: restore: %w", err)
	}

	// 4. Run integrity checks.
	result := VerifyResult{
		SnapshotID: snapshotID,
		VerifiedAt: time.Now().UTC(),
	}
	result.Checks = runIntegrityChecks(ctx, c, tmpDir)

	allPassed := true
	for _, ch := range result.Checks {
		if !ch.Passed {
			allPassed = false
		}
	}
	result.Passed = allPassed

	// 5. Update status — Restorable only on a full pass.
	status, _ := LoadStatus(c.StatusPath)
	status.Restorable = allPassed
	status.LastVerified = result.VerifiedAt
	if !allPassed {
		status.LastError = fmt.Sprintf("verified restore failed at %s",
			result.VerifiedAt.Format(time.RFC3339))
	} else {
		status.LastError = ""
	}
	_ = SaveStatus(c.StatusPath, status)

	// Emit audit record.
	if c.AuditLog != nil {
		action, _ := schema.NewAction(schema.ActionRestoreVerify, "base")
		outcome := authz.OutcomeApplied
		errMsg := ""
		if !allPassed {
			outcome = authz.OutcomeError
			errMsg = status.LastError
		}
		_ = c.AuditLog.Record(action, outcome, errMsg)
	}

	return result, nil
}

// runIntegrityChecks executes all integrity checks against the restored data.
func runIntegrityChecks(ctx context.Context, cfg Config, restoreDir string) []CheckResult {
	var checks []CheckResult

	// Check 1: restic repo consistency (verifies pack data, not just the index).
	checks = append(checks, checkResticRepo(ctx, cfg))

	// Check 2: data directory presence — the restore must contain the paths
	// that were backed up.
	for _, path := range cfg.Paths {
		checks = append(checks, checkPathPresence(restoreDir, path))
	}

	// Check 3: Postgres consistency (conditional — only if a Postgres data
	// directory is found in the restore).
	if pgDir := findPostgresDataDir(restoreDir); pgDir != "" {
		checks = append(checks, checkPostgresConsistency(pgDir))
	}

	return checks
}

// checkResticRepo runs `restic check --read-data-subset=5%` against the repo.
func checkResticRepo(ctx context.Context, cfg Config) CheckResult {
	if err := checkRepo(ctx, cfg); err != nil {
		return CheckResult{
			Name:   "restic-check",
			Passed: false,
			Detail: err.Error(),
		}
	}
	return CheckResult{Name: "restic-check", Passed: true, Detail: "repository integrity OK"}
}

// checkPathPresence verifies that a backed-up path exists in the restore dir.
// restic restores to target/<original-path>, so /opt/ownbase/data restores to
// target/opt/ownbase/data.
//
// cfg.Paths always includes DefaultPaths (e.g. /opt/ownbase/data) even on a
// fresh Base with no services deployed yet, where that directory has never
// been created — restic simply skips a nonexistent source path rather than
// failing the snapshot, so a path missing from the restore is only a real
// integrity failure when it currently exists on this Base. A path that does
// not exist here either was never expected to be backed up and passes
// vacuously.
func checkPathPresence(restoreDir, originalPath string) CheckResult {
	name := "file-presence:" + originalPath
	// Strip leading slash so it joins correctly.
	rel := strings.TrimPrefix(originalPath, "/")
	restored := filepath.Join(restoreDir, rel)
	info, err := os.Stat(restored)
	if err != nil {
		if _, liveErr := os.Stat(originalPath); os.IsNotExist(liveErr) {
			return CheckResult{
				Name:   name,
				Passed: true,
				Detail: fmt.Sprintf("%s does not exist on this Base — nothing to restore", originalPath),
			}
		}
		return CheckResult{
			Name:   name,
			Passed: false,
			Detail: fmt.Sprintf("path %s not found in restore: %v", restored, err),
		}
	}
	detail := fmt.Sprintf("present (%s)", restored)
	if info.IsDir() {
		entries, _ := os.ReadDir(restored)
		detail = fmt.Sprintf("present (%d entries)", len(entries))
	}
	return CheckResult{Name: name, Passed: true, Detail: detail}
}

// findPostgresDataDir searches the restore for a Postgres data directory
// (identified by the presence of PG_VERSION). Returns empty string if not found.
func findPostgresDataDir(restoreDir string) string {
	var found string
	_ = filepath.WalkDir(restoreDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "PG_VERSION" {
			found = filepath.Dir(path)
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// checkPostgresConsistency runs pg_controldata on a restored Postgres data
// directory to verify it is not corrupted.
func checkPostgresConsistency(pgDataDir string) CheckResult {
	name := "postgres-controldata"
	out, err := exec.Command("pg_controldata", pgDataDir).CombinedOutput()
	if err != nil {
		return CheckResult{
			Name:   name,
			Passed: false,
			Detail: fmt.Sprintf("pg_controldata failed: %v\n%s", err, out),
		}
	}
	// A healthy control file contains "Database cluster state".
	if !strings.Contains(string(out), "Database cluster state") {
		return CheckResult{
			Name:   name,
			Passed: false,
			Detail: "pg_controldata output missing expected fields",
		}
	}
	// Extract state for the log.
	state := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "Database cluster state") {
			state = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			break
		}
	}
	return CheckResult{
		Name:   name,
		Passed: true,
		Detail: fmt.Sprintf("Postgres cluster state: %s", state),
	}
}
