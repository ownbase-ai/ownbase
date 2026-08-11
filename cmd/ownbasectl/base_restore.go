package main

// base_restore.go implements `ownbasectl restore` — the reconstruction path
// (internal/backup/rebuild.go) made reachable as one command: provision a
// fresh VM or server, run the installer in rebuild mode (which restores the
// latest verified backup snapshot before the daemon's normal reconcile takes
// over), and register the resulting profile. Restic credentials are passed
// as flags/env only for this one process — never written to disk on the host.

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newRestoreCmd() *cobra.Command {
	var (
		backupRepo   string
		creds        backupCredFlags
		forceRebuild bool
		target       baseTargetFlags
	)
	cmd := &cobra.Command{
		Use:   "restore <name> [--repo <restic-url>] [--password <pw>] [--remote <ssh-host>]",
		Short: "Reconstruct a Base from backups onto a fresh VM or server",
		Long: `Provision a fresh VM or server, run the installer in rebuild mode to
restore the age key, secrets, and latest verified snapshot from the backup
repo, then let the daemon's normal reconcile loop resume — the whole
reconstruction drill as one command.

Repo URL and credentials default to the copy stored in your vault by
'backup setup'. Flags override. Manual flags remain the escape hatch if
the vault is unavailable.`,
		Example: `  ownbasectl restore mybase
  ownbasectl restore mybase --repo s3:s3.amazonaws.com/my-bucket/ownbase --password <pw>
  echo '{"password":"..."}' | ownbasectl restore mybase --repo s3:... --creds-stdin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := creds.resolve(); err != nil {
				return err
			}
			return runBaseRestore(args[0], backupRepo, creds, forceRebuild, target)
		},
	}
	cmd.Flags().StringVar(&backupRepo, "repo", "", "restic repository URL (default: from vault)")
	creds.register(cmd)
	cmd.Flags().BoolVar(&forceRebuild, "force", false, "restore even if the latest snapshot was never verified restorable")
	target.register(cmd)
	// Rebuilding onto a fresh machine is meant to move the Base to a new
	// host, so the guard that stops `create` from silently repointing a name
	// does not apply here.
	target.repointOK = true
	return cmd
}

func runBaseRestore(name, backupRepo string, creds backupCredFlags, forceRebuild bool, target baseTargetFlags) error {
	// Flags override; missing fields fall back to the vault copy written by
	// backup setup. Manual flags remain the escape hatch if the vault is gone.
	if vc, err := loadBackupCreds(name); err == nil {
		if backupRepo == "" {
			backupRepo = vc.Repo
		}
		if creds.password == "" {
			creds.password = vc.Password
		}
		if creds.awsAccessKey == "" {
			creds.awsAccessKey = vc.AWSAccessKeyID
		}
		if creds.awsSecretKey == "" {
			creds.awsSecretKey = vc.AWSSecretAccessKey
		}
		if creds.b2AccountID == "" {
			creds.b2AccountID = vc.B2AccountID
		}
		if creds.b2AccountKey == "" {
			creds.b2AccountKey = vc.B2AccountKey
		}
	}
	if backupRepo == "" {
		return fmt.Errorf("--repo is required (or run backup setup first so the vault holds it)")
	}
	if creds.password == "" {
		return fmt.Errorf("--password is required (or run backup setup first so the vault holds it); or pass --creds-stdin")
	}

	env := map[string]string{
		"OWNBASE_REBUILD":     "1",
		"OWNBASE_BACKUP_REPO": backupRepo,
		"RESTIC_PASSWORD":     creds.password,
	}
	if forceRebuild {
		env["OWNBASE_FORCE_REBUILD"] = "1"
	}
	if creds.awsAccessKey != "" {
		env["AWS_ACCESS_KEY_ID"] = creds.awsAccessKey
	}
	if creds.awsSecretKey != "" {
		env["AWS_SECRET_ACCESS_KEY"] = creds.awsSecretKey
	}
	if creds.b2AccountID != "" {
		env["B2_ACCOUNT_ID"] = creds.b2AccountID
	}
	if creds.b2AccountKey != "" {
		env["B2_ACCOUNT_KEY"] = creds.b2AccountKey
	}

	progress("==> Restoring Base %q from %s", name, backupRepo)
	progress("    current = restore(backups); running = reconcile(compile(repo, secrets), current)\n")

	if err := target.provision(name, env); err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	if !target.jsonOut {
		fmt.Println()
		fmt.Println("Restore complete. The daemon is now reconciling from the restored")
		fmt.Println("bare repo + secrets — give it a minute, then check:")
		fmt.Printf("  ownbasectl checkup %s\n", name)
	}
	return nil
}
