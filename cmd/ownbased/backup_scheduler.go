package main

// backup_scheduler.go runs the backup and verified-restore-drill cadence as
// an independent goroutine, decoupled from the main reconcile select loop.
//
// This matters for the setup lifecycle (`ownbasectl backup setup`):
// core.backup.repo may not be set yet when the daemon starts (a fresh Base
// has no backups configured until the owner runs `backup setup`). By
// re-reading ownbase.yaml on every poll instead of wiring a fixed ticker at
// startup, backups activate as soon as the config commit lands — within one
// poll interval, no daemon restart required.
//
// backup.Run and backup.VerifyRestore persist their own status file
// (backup.DefaultStatusPath), which the status API already reads fresh on
// every request — so this scheduler has no need to feed results back into
// the main select loop at all.

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/backup"
	"github.com/ownbase/ownbase/internal/schema"
)

// backupSchedulerPollInterval is how often the scheduler wakes up to check
// whether a backup or verify drill is due.
const backupSchedulerPollInterval = time.Minute

// backupSchedulerInitialDelay gives the initial reconcile + bootstrap a head
// start before the first check, so ownbase.yaml has had a chance to be seeded.
const backupSchedulerInitialDelay = 30 * time.Second

// backupBusy is the single point of exclusion for every restic operation
// against the shared repo: the scheduler's backup tick, its verify-restore
// drill tick, and a manual `ownbasectl backup run`/`backup setup`
// all go through it. Restic takes its own repo lock, but two operations
// racing for it just means one fails with a confusing lock error — this
// guard means only one restic operation ever runs at a time in-process, so
// a manual run and a scheduled snapshot (or a snapshot and a verify drill)
// never collide.
var backupBusy atomic.Bool

// acquireBackupSlot blocks until no other backup/verify operation is in
// flight, then marks the slot busy. The caller must call the returned
// release func exactly once. Used by manual runs (which should wait their
// turn rather than fail); the scheduler instead uses a non-blocking
// CompareAndSwap so a busy tick is simply skipped until the next poll.
func acquireBackupSlot(ctx context.Context) (release func(), err error) {
	for {
		if backupBusy.CompareAndSwap(false, true) {
			return func() { backupBusy.Store(false) }, nil
		}
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// runBackupScheduler runs for the life of the daemon. It is always started;
// each poll is a no-op when core.backup.repo is unset.
//
// reconcileSig is the same channel /backup/configure and /backup/run write
// to, wired to trigger reconcileOnce on the main select loop. This scheduler
// signals it after a backup or verify-restore
// drill completes so the cached /status payload (which only refreshes on
// reconcile — see explain.Gather in reconcileOnce) picks up the new
// LastBackup/LastVerified/Restorable values within seconds instead of
// waiting for the next 5-minute ticker backstop. Non-blocking send: a
// reconcile already queued is enough, no need to queue a second one.
func runBackupScheduler(ctx context.Context, cfg agentConfig, auditLog authz.AuditLogger, reconcileSig chan<- struct{}) {
	timer := time.NewTimer(backupSchedulerInitialDelay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return
	}

	check := func() {
		backupCoreCfg := readCoreConfigFromDisk(cfg.checkoutPath).Backup
		if !backupCoreCfg.Enabled() {
			return
		}

		interval, err := backupCoreCfg.EffectiveInterval()
		if err != nil {
			interval = schema.DefaultBackupInterval
		}
		verifyInterval, err := backupCoreCfg.EffectiveVerifyInterval()
		if err != nil {
			verifyInterval = schema.DefaultVerifyInterval
		}

		status, _ := backup.LoadStatus(backup.DefaultStatusPath)

		// Backup and verify share backupBusy, so at most one of these two
		// launches per tick — the other, if also due, simply waits for the
		// next poll (a one-minute delay is immaterial for either cadence).
		if (status.LastBackup.IsZero() || time.Since(status.LastBackup) >= interval) &&
			backupBusy.CompareAndSwap(false, true) {
			go func() {
				defer backupBusy.Store(false)
				fmt.Fprintln(os.Stderr, "ownbased: backup: running scheduled snapshot")
				if _, err := backup.Run(ctx, loadBackupConfig(cfg, backupCoreCfg.Repo, auditLog)); err != nil {
					fmt.Fprintf(os.Stderr, "ownbased: backup: %v\n", err)
				}
				signalReconcile(reconcileSig)
			}()
			return
		}

		if (status.LastVerified.IsZero() || time.Since(status.LastVerified) >= verifyInterval) &&
			backupBusy.CompareAndSwap(false, true) {
			go func() {
				defer backupBusy.Store(false)
				fmt.Fprintln(os.Stderr, "ownbased: backup: running verified restore drill")
				result, err := backup.VerifyRestore(ctx, loadBackupConfig(cfg, backupCoreCfg.Repo, auditLog))
				switch {
				case err != nil:
					fmt.Fprintf(os.Stderr, "ownbased: verify restore: %v\n", err)
				case !result.Passed:
					fmt.Fprintln(os.Stderr, "ownbased: verified restore FAILED — restorable=false")
				default:
					fmt.Fprintln(os.Stderr, "ownbased: verified restore passed — restorable=true")
				}
				signalReconcile(reconcileSig)
			}()
		}
	}

	check()
	ticker := time.NewTicker(backupSchedulerPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			check()
		case <-ctx.Done():
			return
		}
	}
}

// signalReconcile does a non-blocking send on sig: a full channel means a
// reconcile is already queued, so a second signal would be redundant.
func signalReconcile(sig chan<- struct{}) {
	select {
	case sig <- struct{}{}:
	default:
	}
}

// runBackupNow runs a single backup cycle synchronously and returns a
// JSON-friendly summary. Used by the /backup/run API (ownbasectl base
// backup run) to give an immediate result rather than waiting for the
// scheduler's next poll. Waits for any in-flight scheduled backup or
// verify-restore drill to finish first (see backupBusy) rather than
// running concurrently against the same repo.
func runBackupNow(ctx context.Context, cfg agentConfig, auditLog authz.AuditLogger) (backup.Status, error) {
	backupCoreCfg := readCoreConfigFromDisk(cfg.checkoutPath).Backup
	if !backupCoreCfg.Enabled() {
		return backup.Status{}, fmt.Errorf("no backup repo configured — run 'ownbasectl backup setup' first")
	}

	release, err := acquireBackupSlot(ctx)
	if err != nil {
		return backup.Status{}, fmt.Errorf("waiting for in-progress backup/verify to finish: %w", err)
	}
	defer release()

	return backup.Run(ctx, loadBackupConfig(cfg, backupCoreCfg.Repo, auditLog))
}
