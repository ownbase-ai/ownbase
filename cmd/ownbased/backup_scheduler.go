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
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/backup"
	"github.com/ownbase/ownbase/internal/explain"
	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/secrets"
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
				if _, err := backup.Run(ctx, loadBackupConfig(cfg, backupCoreCfg, auditLog)); err != nil {
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
				result, err := backup.VerifyRestore(ctx, loadBackupConfig(cfg, backupCoreCfg, auditLog))
				switch {
				case err != nil:
					fmt.Fprintf(os.Stderr, "ownbased: verify restore: %v\n", err)
				case !result.Passed:
					// Name the checks that failed. "FAILED" on its own says
					// backups are not provably restorable without saying which
					// part is not, and that answer is what decides what the
					// operator does next.
					fmt.Fprintln(os.Stderr, "ownbased: verified restore FAILED — restorable=false")
					for _, ch := range result.Checks {
						if !ch.Passed {
							fmt.Fprintf(os.Stderr, "ownbased:   failed check %s: %s\n", ch.Name, ch.Detail)
						}
					}
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

	return backup.Run(ctx, loadBackupConfig(cfg, backupCoreCfg, auditLog))
}

// pruneBackupNow runs forget+prune with optional credential overrides from
// the request. Overrides are merged into the in-memory Config only — they are
// never written to the age-encrypted backup secret or anywhere else on disk.
func pruneBackupNow(ctx context.Context, cfg agentConfig, auditLog authz.AuditLogger, req explain.BackupPruneRequest) (backup.Status, error) {
	backupCoreCfg := readCoreConfigFromDisk(cfg.checkoutPath).Backup
	if !backupCoreCfg.Enabled() {
		return backup.Status{}, fmt.Errorf("no backup repo configured — run 'ownbasectl backup setup' first")
	}

	release, err := acquireBackupSlot(ctx)
	if err != nil {
		return backup.Status{}, fmt.Errorf("waiting for in-progress backup/verify to finish: %w", err)
	}
	defer release()

	backupCfg := loadBackupConfig(cfg, backupCoreCfg, auditLog)
	backupCfg.Credentials = mergeBackupCredOverrides(backupCfg.Credentials, req)
	return backup.Prune(ctx, backupCfg)
}

// mergeBackupCredOverrides copies base and overlays non-empty request fields.
// The returned map is always a new map so the caller's original is untouched.
func mergeBackupCredOverrides(base map[string]string, req explain.BackupPruneRequest) map[string]string {
	out := make(map[string]string, len(base)+5)
	for k, v := range base {
		out[k] = v
	}
	set := func(key, val string) {
		if val != "" {
			out[key] = val
		}
	}
	set("RESTIC_PASSWORD", req.Password)
	set("AWS_ACCESS_KEY_ID", req.AWSAccessKeyID)
	set("AWS_SECRET_ACCESS_KEY", req.AWSSecretAccessKey)
	set("B2_ACCOUNT_ID", req.B2AccountID)
	set("B2_ACCOUNT_KEY", req.B2AccountKey)
	return out
}

// rekeyBackupNow runs one phase of restic password rotation. The add phase
// uses the Base's current RESTIC_PASSWORD; finalize swaps the Base secret to
// newPassword first, then removes every other restic key.
func rekeyBackupNow(ctx context.Context, cfg agentConfig, auditLog authz.AuditLogger, req explain.BackupRekeyRequest) (backup.RekeyResult, error) {
	backupCoreCfg := readCoreConfigFromDisk(cfg.checkoutPath).Backup
	if !backupCoreCfg.Enabled() {
		return backup.RekeyResult{}, fmt.Errorf("no backup repo configured — run 'ownbasectl backup setup' first")
	}
	phase := backup.RekeyPhase(req.Phase)
	if phase != backup.RekeyPhaseAdd && phase != backup.RekeyPhaseFinalize {
		return backup.RekeyResult{}, fmt.Errorf("phase must be %q or %q", backup.RekeyPhaseAdd, backup.RekeyPhaseFinalize)
	}
	if req.NewPassword == "" {
		return backup.RekeyResult{}, fmt.Errorf("new_password is required")
	}

	release, err := acquireBackupSlot(ctx)
	if err != nil {
		return backup.RekeyResult{}, fmt.Errorf("waiting for in-progress backup/verify to finish: %w", err)
	}
	defer release()

	backupCfg := loadBackupConfig(cfg, backupCoreCfg, auditLog)

	if phase == backup.RekeyPhaseFinalize {
		// Swap the Base secret before touching the keyring so a crash after
		// the write still leaves a secret that opens the repo (both passwords
		// work until old keys are removed).
		if err := writeBackupResticPassword(req.NewPassword); err != nil {
			return backup.RekeyResult{}, fmt.Errorf("update Base backup secret: %w", err)
		}
		// Reload so finalize authenticates with the new password.
		backupCfg = loadBackupConfig(cfg, backupCoreCfg, auditLog)
	}

	return backup.Rekey(ctx, backupCfg, phase, req.NewPassword)
}

// writeBackupResticPassword merges RESTIC_PASSWORD into the conventional
// age-encrypted backup secret and re-encrypts it. Other keys (AWS/B2) are
// preserved.
func writeBackupResticPassword(newPassword string) error {
	path := filepath.Join(explain.DefaultSecretsDir, "backup.yaml.age")
	custody := secrets.FileKeyCustody{}
	merged, err := secrets.IssueMap(custody, path)
	if err != nil {
		return err
	}
	if merged == nil {
		merged = map[string]string{}
	}
	merged["RESTIC_PASSWORD"] = newPassword

	id, err := custody.LoadIdentity()
	if err != nil {
		return fmt.Errorf("load age key: %w", err)
	}
	ciphertext, err := secrets.EncryptSecrets(id.Recipient(), merged)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, ciphertext, 0o600)
}

// verifyBackupNow runs the verified-restore drill synchronously, streaming
// progress to progress. Used by the /backup/verify API (ownbasectl checkup
// --verify) so an operator can prove a restore now instead of waiting up to
// verify_interval for the scheduler to do it.
//
// Like runBackupNow it waits its turn on backupBusy rather than running
// concurrently against the same restic repo.
//
// The returned error means the drill could not run. A drill that ran and
// failed a check returns a result with Passed == false and a nil error — the
// caller reports which check failed, and only an infrastructure failure is an
// error in the HTTP sense.
func verifyBackupNow(ctx context.Context, cfg agentConfig, auditLog authz.AuditLogger, progress io.Writer) (backup.VerifyResult, error) {
	backupCoreCfg := readCoreConfigFromDisk(cfg.checkoutPath).Backup
	if !backupCoreCfg.Enabled() {
		return backup.VerifyResult{}, fmt.Errorf("no backup repo configured — run 'ownbasectl backup setup' first")
	}

	if progress != nil {
		fmt.Fprintln(progress, "==> Waiting for any in-progress backup to finish")
	}
	release, err := acquireBackupSlot(ctx)
	if err != nil {
		return backup.VerifyResult{}, fmt.Errorf("waiting for in-progress backup/verify to finish: %w", err)
	}
	defer release()

	backupCfg := loadBackupConfig(cfg, backupCoreCfg, auditLog)
	backupCfg.Progress = progress
	return backup.VerifyRestore(ctx, backupCfg)
}
