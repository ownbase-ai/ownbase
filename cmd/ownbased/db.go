package main

// db.go wires the Postgres point-in-time recovery endpoints (/db/status,
// /db/restore) to internal/backup.
//
// Both read ownbase.yaml fresh rather than caching a PGBackRest at startup, so
// a Base that gains (or renames, or removes) its Postgres service does not need
// a daemon restart for these endpoints to follow.

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/ownbase/ownbase/internal/backup"
	"github.com/ownbase/ownbase/internal/explain"
	"github.com/ownbase/ownbase/internal/schema"
)

// findPGBackRest locates the Base's Postgres and its pgBackRest repository from
// the current ownbase.yaml.
func findPGBackRest(cfg agentConfig) (backup.PGBackRest, error) {
	oc, err := schema.ParseConfigFile(filepath.Join(cfg.checkoutPath, "ownbase.yaml"))
	if err != nil {
		return backup.PGBackRest{}, fmt.Errorf("parse ownbase.yaml: %w", err)
	}
	return backup.FindPGBackRest(oc)
}

// restoreDatabase performs a point-in-time restore, validating the target
// against what the repository actually holds first.
//
// The pre-validation is the whole reason this is not a thin passthrough. A
// target past the end of the WAL archive does not fail with "no data for that
// time" — Postgres replays everything it has and aborts with "recovery ended
// before configured recovery target was reached", which reads like data loss.
// Catching it here costs one `pgbackrest info` and turns that into a sentence
// that says what to do instead.
func restoreDatabase(ctx context.Context, cfg agentConfig, progress io.Writer, req explain.DBRestoreRequest) (backup.RestoreOutcome, error) {
	pb, err := findPGBackRest(cfg)
	if err != nil {
		return backup.RestoreOutcome{}, err
	}

	opts := backup.RestoreOptions{
		Into:        backup.RestoreIntoScratch,
		ScratchPort: req.ScratchPort,
		Progress:    progress,
	}
	switch req.Into {
	case "", string(backup.RestoreIntoScratch):
	case string(backup.RestoreIntoProduction):
		opts.Into = backup.RestoreIntoProduction
	default:
		return backup.RestoreOutcome{}, fmt.Errorf("unknown restore destination %q (want %q or %q)",
			req.Into, backup.RestoreIntoScratch, backup.RestoreIntoProduction)
	}

	if req.Target != "" {
		parsed, err := backup.ParseTarget(req.Target)
		if err != nil {
			return backup.RestoreOutcome{}, err
		}
		opts.Target = parsed

		fmt.Fprintf(progress, "==> Checking %s against the repository\n", backup.FormatTarget(parsed))
		status, err := backup.QueryStatus(ctx, pb)
		if err != nil {
			// Refusing to restore because the *check* could not run would be
			// the wrong trade: the operator asked for a recovery, and the
			// restore reports its own failures clearly either way.
			fmt.Fprintf(progress, "    could not read the repository to validate the target (%v) — continuing\n", err)
		} else if err := backup.ValidateTarget(status, parsed); err != nil {
			return backup.RestoreOutcome{}, err
		}
	}

	return backup.Restore(ctx, pb, opts)
}
