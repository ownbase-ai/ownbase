package main

// base_restore.go implements `ownbasectl restore` — the reconstruction path
// (internal/backup/rebuild.go) made reachable as one command: provision a
// fresh VM or server, run the installer in rebuild mode (which restores the
// latest verified backup snapshot before the daemon's normal reconcile takes
// over), and register the resulting profile. Restic credentials are passed
// as flags/env only for this one process — never written to disk on the host.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/vault"
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
	needVault := backupRepo == "" || creds.password == ""
	vc, vaultErr := loadBackupCreds(name)
	switch {
	case vaultErr == nil:
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
	case needVault:
		// Primary path is vault-backed restore. A locked vault or missing
		// Base must not look like "you forgot --password".
		return fmt.Errorf("load backup credentials from vault: %w", vaultErr)
	}
	// vaultErr != nil && !needVault: flags are complete; vault is optional.
	if backupRepo == "" {
		return fmt.Errorf("--repo is required (not stored in the vault for %q — run 'backup setup' or pass --repo)", name)
	}
	if creds.password == "" {
		return fmt.Errorf("--password is required (not stored in the vault for %q — run 'backup setup', pass --password, or --creds-stdin)", name)
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

	// Re-assert the vault's config-source pin. The snapshot restores
	// config-source.yaml verbatim; a poisoned pointer would otherwise become
	// the rebuild's source of truth. Vault wins. Wait for the daemon API —
	// without this, provision returns before ownbased is listening and the
	// pin never lands.
	if err := reassertConfigSource(name, target.jsonOut); err != nil {
		return fmt.Errorf("re-assert config source from vault: %w", err)
	}

	if !target.jsonOut {
		fmt.Println()
		fmt.Println("Restore complete. The daemon is now reconciling from the restored")
		fmt.Println("bare repo + secrets — give it a minute, then check:")
		fmt.Printf("  ownbasectl checkup %s\n", name)
	}
	return nil
}

// reassertConfigSource waits for the daemon, then POSTs the vault profile's
// config repo so a rebuild cannot keep a compromised config-source.yaml.
// No-op when the vault has no config URL. Failure is fatal for restore.
func reassertConfigSource(name string, quiet bool) error {
	p, err := loadProfile(name)
	if err != nil {
		return err
	}
	url := strings.TrimSpace(p.ConfigRepoURL)
	if url == "" {
		return nil
	}
	ref := strings.TrimSpace(p.ConfigRef)
	if ref == "" {
		ref = vault.DefaultConfigRef
	}

	if !quiet {
		progress("==> Waiting for daemon, then re-asserting config source from vault")
	}
	if err := waitForDaemonReady(name, 10*time.Minute); err != nil {
		return err
	}

	conn, err := connectToServer(name)
	if err != nil {
		return err
	}
	defer conn.close()
	payload, _ := json.Marshal(map[string]string{"repo_url": url, "ref": ref})
	if _, err := apiCall(conn, http.MethodPost, "/config/source", payload); err != nil {
		return err
	}
	if !quiet {
		fmt.Printf("Re-asserted config source from vault: %s (%s)\n", url, ref)
	}
	return nil
}
