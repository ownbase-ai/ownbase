package main

// base_backup.go implements `ownbasectl backup setup|run|status <name>` —
// the standard way remote backups get turned on for a Base (local VM or
// remote server alike). Credentials go through the existing secrets API
// (ownbasectl secrets set <name> backup); the repo URL and cadence are
// committed to ownbase.yaml in the external config repo client-side and
// applied via a reconcile (the same path as any other config mutation).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/backup"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Configure, run, and check remote backups (setup|run|status)",
	}
	cmd.AddCommand(
		newBackupSetupCmd(),
		newBackupRunCmd(),
		newBackupStatusCmd(),
	)
	return cmd
}

// backupCredFlags are the restic repository credentials shared by
// `backup setup` and `restore`.
type backupCredFlags struct {
	password     string
	awsAccessKey string
	awsSecretKey string
	b2AccountID  string
	b2AccountKey string
}

// register adds the shared credential flags to cmd.
func (f *backupCredFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.password, "password", "", "restic repository password (required)")
	fl.StringVar(&f.awsAccessKey, "aws-access-key-id", "", "AWS_ACCESS_KEY_ID (for s3: repos)")
	fl.StringVar(&f.awsSecretKey, "aws-secret-access-key", "", "AWS_SECRET_ACCESS_KEY (for s3: repos)")
	fl.StringVar(&f.b2AccountID, "b2-account-id", "", "B2_ACCOUNT_ID (for b2: repos)")
	fl.StringVar(&f.b2AccountKey, "b2-account-key", "", "B2_ACCOUNT_KEY (for b2: repos)")
}

func newBackupSetupCmd() *cobra.Command {
	var (
		repo           string
		creds          backupCredFlags
		interval       string
		verifyInterval string
	)
	cmd := &cobra.Command{
		Use:   "setup <name> --repo <restic-url> --password <pw>",
		Short: "Turn on remote backups for a Base and run the first snapshot",
		Example: `  ownbasectl backup setup mybase \
    --repo s3:s3.amazonaws.com/my-bucket/ownbase \
    --password <a-strong-restic-password> \
    --aws-access-key-id AKIA... --aws-secret-access-key ...`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackupSetup(args[0], repo, creds, interval, verifyInterval)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "restic repository URL (s3:..., b2:..., sftp:...) (required)")
	creds.register(cmd)
	cmd.Flags().StringVar(&interval, "interval", "", "backup snapshot cadence, e.g. 1h (default: 1h)")
	cmd.Flags().StringVar(&verifyInterval, "verify-interval", "", "verified-restore drill cadence, e.g. 24h (default: 24h)")
	return cmd
}

// runBackupSetup is the one flow used for both a local VM and a remote
// server (backups always go to a real off-machine restic destination —
// S3, B2, or SFTP — never a local host directory; see docs/decisions.md).
func runBackupSetup(base, repo string, credFlags backupCredFlags, interval, verifyInterval string) error {
	if repo == "" {
		return fmt.Errorf("--repo is required, e.g. --repo s3:s3.amazonaws.com/mybucket/ownbase")
	}
	if credFlags.password == "" {
		return fmt.Errorf("--password is required (the restic repository encryption password — save it somewhere safe, it is never recoverable from OwnBase)")
	}

	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	creds := map[string]string{"RESTIC_PASSWORD": credFlags.password}
	if credFlags.awsAccessKey != "" {
		creds["AWS_ACCESS_KEY_ID"] = credFlags.awsAccessKey
	}
	if credFlags.awsSecretKey != "" {
		creds["AWS_SECRET_ACCESS_KEY"] = credFlags.awsSecretKey
	}
	if credFlags.b2AccountID != "" {
		creds["B2_ACCOUNT_ID"] = credFlags.b2AccountID
	}
	if credFlags.b2AccountKey != "" {
		creds["B2_ACCOUNT_KEY"] = credFlags.b2AccountKey
	}

	fmt.Println("==> Storing backup credentials (encrypted at rest on the Base) ...")
	credsPayload, _ := json.Marshal(creds)
	if _, err := apiCall(conn, http.MethodPost, "/secrets/backup", credsPayload); err != nil {
		return fmt.Errorf("store backup credentials: %w", err)
	}

	fmt.Printf("==> Configuring backup repo %s ...\n", repo)
	// The repo/cadence live in ownbase.yaml (external config repo); commit
	// them client-side and reconcile — the same path as any other config
	// mutation. Credentials go through the secrets API above (never git).
	cfgErr := mutateConfig(base, func(current string) (string, string, error) {
		updated := backup.SetCoreBackupConfig(current, repo, interval, verifyInterval)
		return updated, fmt.Sprintf("chore(backup): configure backup repo %s", repo), nil
	})
	if cfgErr != nil && cfgErr != errNoConfigChange {
		return fmt.Errorf("configure backup: %w", cfgErr)
	}

	fmt.Println("==> Running the first backup now (this may take a while for large volumes) ...")
	// The reconcile triggered above pulls the config into the checkout the
	// daemon reads from. Retry briefly, but only for
	// that specific "not configured yet" race — a permanent failure (bad
	// restic credentials, unreachable repo) should surface immediately
	// rather than being retried for 30 seconds as if it might resolve itself.
	var body []byte
	deadline := time.Now().Add(30 * time.Second)
	for {
		// The daemon allows up to 10 minutes for a snapshot to complete —
		// match that here so a large first backup doesn't get cut off by
		// the client while the daemon is still working.
		body, err = apiCallWithTimeout(conn, http.MethodPost, "/backup/run", nil, 10*time.Minute)
		if err == nil || !isBackupNotConfiguredYetErr(err) || time.Now().After(deadline) {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("run first backup: %w\n  Backups are configured — the scheduler will retry automatically within a minute.", err)
	}
	printBackupRunResult(body)

	fmt.Println()
	fmt.Println("Backups are set up. The verified-restore drill runs automatically to")
	fmt.Println("prove the backup is actually restorable — check with:")
	fmt.Printf("  ownbasectl backup status %s\n", base)
	return nil
}

// isBackupNotConfiguredYetErr reports whether err is the specific
// "not reconciled yet" race from runBackupNow (cmd/ownbased/backup_scheduler.go)
// rather than a real backup failure. Only this case is worth retrying —
// bad credentials or a real restic error will not resolve on their own.
func isBackupNotConfiguredYetErr(err error) bool {
	return strings.Contains(err.Error(), "no backup repo configured")
}

func newBackupRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <name>",
		Short: "Trigger an immediate backup snapshot (\"save now\")",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackupRun(args[0])
		},
	}
}

// runBackupRun triggers an immediate backup and prints the result.
func runBackupRun(base string) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	fmt.Println("Running backup now ...")
	// Match the daemon's own 10-minute allowance for a snapshot to complete.
	body, err := apiCallWithTimeout(conn, http.MethodPost, "/backup/run", nil, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("run backup: %w", err)
	}
	printBackupRunResult(body)
	return nil
}

func newBackupStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <name>",
		Short: "Show backup health: last snapshot, restorable?, last verify drill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackupStatus(args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print raw JSON instead of a formatted summary")
	return cmd
}

// runBackupStatus prints backup health (last snapshot, restorable,
// last verify) from the Base's status API.
func runBackupStatus(base string, jsonOut bool) error {
	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	body, err := apiGet(conn, "/status")
	if err != nil {
		return err
	}
	if jsonOut {
		fmt.Println(string(body))
		return nil
	}

	var s struct {
		Security struct {
			BackupRestorable bool   `json:"backup_restorable"`
			LastBackup       string `json:"last_backup"`
			LastVerified     string `json:"last_verified"`
		} `json:"security"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return fmt.Errorf("parse status JSON: %w", err)
	}

	fmt.Println("──────────────────────────── Backup Status ────────────────────────────")
	if s.Security.LastBackup == "" {
		fmt.Printf("  Last backup:   never — run 'ownbasectl backup setup %s' first\n", base)
	} else {
		fmt.Printf("  Last backup:   %s\n", shortTime(s.Security.LastBackup))
	}
	restorable := "✗ not yet verified"
	if s.Security.BackupRestorable {
		restorable = "✓ restorable"
	}
	fmt.Printf("  Restorable:    %s\n", restorable)
	if s.Security.LastVerified == "" {
		fmt.Println("  Last verified: never")
	} else {
		fmt.Printf("  Last verified: %s\n", shortTime(s.Security.LastVerified))
	}
	fmt.Println("─────────────────────────────────────────────────────────────────────────")
	return nil
}

// printBackupRunResult renders the JSON body returned by POST /backup/run.
func printBackupRunResult(body []byte) {
	var r struct {
		LastBackup     string `json:"last_backup"`
		LatestSnapshot string `json:"latest_snapshot"`
		Restorable     bool   `json:"restorable"`
		LastError      string `json:"last_error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		fmt.Println(strings.TrimSpace(string(body)))
		return
	}
	if r.LastError != "" {
		fmt.Printf("Backup failed: %s\n", r.LastError)
		return
	}
	fmt.Printf("Backup complete — snapshot %s\n", r.LatestSnapshot)
}
