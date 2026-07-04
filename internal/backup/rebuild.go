package backup

// rebuild.go implements the rebuild path from the Reconstruction Model:
//
//	current = restore(backups)
//	running = reconcile(compile(repo, secrets), current)
//
// This is the acid test made executable: given only the repo, the secrets,
// and the latest verified backup — all artifacts the customer physically owns
// — a working Base can be reconstructed on a fresh machine.
//
// The rebuild command is exposed via the agent's --rebuild flag. It:
//  1. Calls install.PassZero (same path as the M5 installer).
//  2. Restores the latest backup snapshot to the production data paths.
//  3. Bootstraps the bare repo (M4a).
//  4. Runs the reconcile loop once (M3).
//
// Because rebuild reuses the exact same code paths as install + reconcile,
// there is no "rebuild-specific" logic to test separately — the tests for
// each component already cover it.

import (
	"context"
	"fmt"
	"os"
)

// RebuildConfig parameterises the rebuild command.
type RebuildConfig struct {
	BackupConfig Config

	// RestoreTarget is where backup data is restored before the reconcile.
	// After restore, the agent moves data from RestoreTarget to its production
	// locations. Default: RestoreTarget == production paths (restore in-place).
	RestoreTarget string

	// DryRun previews the rebuild without making changes.
	DryRun bool

	// ForceRebuild skips the Restorable=true guard and restores even when
	// the latest backup has not been verified. Use when a verified restore
	// drill is not possible but the operator deliberately accepts the risk.
	// The --force-rebuild agent flag sets this.
	ForceRebuild bool
}

// Rebuild executes the full reconstruction path:
//  1. Restore the latest snapshot to RestoreTarget (or in-place if empty).
//  2. Report what was restored.
//
// The caller is responsible for then invoking install.PassZero (M5) and the
// reconcile loop (M3) — Rebuild only handles the restore step so the
// integration seam is clear.
func Rebuild(ctx context.Context, cfg RebuildConfig) error {
	c := cfg.BackupConfig.withDefaults()
	if err := c.Validate(); err != nil {
		return fmt.Errorf("rebuild: %w", err)
	}

	// Find the latest verified snapshot. Only enforce Restorable=true when a
	// status file is actually present *and* readable *and* says the backup
	// was not verified — that is a real signal (e.g. re-running rebuild on
	// a machine whose last verify drill failed). Two other cases must not
	// block the rebuild:
	//   - no status file at all: the normal `ownbasectl restore` case on a
	//     genuinely fresh machine, where the verified-restore history lives
	//     on the machine being reconstructed, not in the backup repo.
	//   - a status file that exists but fails to parse (corrupt/unreadable):
	//     we cannot trust it either way, so it must not be treated as an
	//     authoritative "not verified" the way a valid Restorable=false
	//     would be.
	_, statErr := os.Stat(c.StatusPath)
	statusExistsAndReadable := statErr == nil
	status, loadErr := LoadStatus(c.StatusPath)
	if statusExistsAndReadable && loadErr == nil &&
		!status.Restorable && !cfg.DryRun && !cfg.ForceRebuild {
		return fmt.Errorf("rebuild: latest backup has not been verified (Restorable=false); " +
			"run a verified restore drill first or use --force-rebuild to override")
	}

	snapshotID := status.LatestSnapshot
	if snapshotID == "" && cfg.DryRun {
		// A dry-run preview does not need to actually contact the repo.
		snapshotID = "(latest)"
	} else if snapshotID == "" {
		// Fall back to asking restic for the latest.
		var err error
		snapshotID, err = latestSnapshotID(ctx, c)
		if err != nil {
			return fmt.Errorf("rebuild: find latest snapshot: %w", err)
		}
	}

	target := cfg.RestoreTarget
	if target == "" {
		target = "/" // restore in-place to original paths
	}

	fmt.Fprintf(os.Stderr, "rebuild: restoring snapshot %s to %s ...\n", snapshotID, target)

	if cfg.DryRun {
		fmt.Fprintln(os.Stderr, "rebuild: dry-run — would restore snapshot "+snapshotID)
		return nil
	}

	if err := restoreSnapshot(ctx, c, snapshotID, target); err != nil {
		return fmt.Errorf("rebuild: restore: %w", err)
	}

	fmt.Fprintf(os.Stderr, "rebuild: snapshot %s restored to %s\n", snapshotID, target)
	fmt.Fprintln(os.Stderr, "rebuild: next step — run reconcile loop (handled by agent)")
	return nil
}
