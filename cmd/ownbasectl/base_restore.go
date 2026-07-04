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
		Use:   "restore <name> --repo <restic-url> --password <pw> [--remote <ssh-host>]",
		Short: "Reconstruct a Base from backups onto a fresh VM or server",
		Long: `Provision a fresh VM or server, run the installer in rebuild mode to
restore the age key, secrets, and latest verified snapshot from the backup
repo, then let the daemon's normal reconcile loop resume — the whole
reconstruction drill as one command.`,
		Example: `  ownbasectl restore mybase \
    --repo s3:s3.amazonaws.com/my-bucket/ownbase \
    --password <the-restic-password>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBaseRestore(args[0], backupRepo, creds, forceRebuild, target)
		},
	}
	cmd.Flags().StringVar(&backupRepo, "repo", "", "restic repository URL to restore from (required; same flag as 'backup setup')")
	creds.register(cmd)
	cmd.Flags().BoolVar(&forceRebuild, "force", false, "restore even if the latest snapshot was never verified restorable")
	target.register(cmd)
	return cmd
}

func runBaseRestore(name, backupRepo string, creds backupCredFlags, forceRebuild bool, target baseTargetFlags) error {
	if backupRepo == "" {
		return fmt.Errorf("--repo is required — the restic repository URL of the Base you're restoring")
	}
	if creds.password == "" {
		return fmt.Errorf("--password is required (the restic repository password)")
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

	fmt.Printf("==> Restoring Base %q from %s\n", name, backupRepo)
	fmt.Println("    current = restore(backups); running = reconcile(compile(repo, secrets), current)")
	fmt.Println()

	if err := target.provision(name, env); err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	fmt.Println()
	fmt.Println("Restore complete. The daemon is now reconciling from the restored")
	fmt.Println("bare repo + secrets — give it a minute, then check:")
	fmt.Printf("  ownbasectl checkup %s\n", name)
	return nil
}
