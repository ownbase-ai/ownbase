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
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/backup"
	"github.com/ownbase/ownbase/internal/vault"
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
	credsStdin   bool
}

// register adds the shared credential flags to cmd.
func (f *backupCredFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.password, "password", "", "restic repository password (required unless --creds-stdin)")
	fl.StringVar(&f.awsAccessKey, "aws-access-key-id", "", "AWS_ACCESS_KEY_ID (for s3: repos)")
	fl.StringVar(&f.awsSecretKey, "aws-secret-access-key", "", "AWS_SECRET_ACCESS_KEY (for s3: repos)")
	fl.StringVar(&f.b2AccountID, "b2-account-id", "", "B2_ACCOUNT_ID (for b2: repos)")
	fl.StringVar(&f.b2AccountKey, "b2-account-key", "", "B2_ACCOUNT_KEY (for b2: repos)")
	fl.BoolVar(&f.credsStdin, "creds-stdin", false, "read credentials as JSON from stdin (avoids secrets in argv)")
}

// resolve loads credentials from stdin when --creds-stdin is set. The JSON
// shape is {"password":"…","aws_access_key_id":"…","aws_secret_access_key":"…",
// "b2_account_id":"…","b2_account_key":"…"}. Flag values fill any field
// the JSON omits.
func (f *backupCredFlags) resolve() error {
	if !f.credsStdin {
		return nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read --creds-stdin: %w", err)
	}
	var in struct {
		Password     string `json:"password"`
		AWSAccessKey string `json:"aws_access_key_id"`
		AWSSecretKey string `json:"aws_secret_access_key"`
		B2AccountID  string `json:"b2_account_id"`
		B2AccountKey string `json:"b2_account_key"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("parse --creds-stdin JSON: %w", err)
	}
	if in.Password != "" {
		f.password = in.Password
	}
	if in.AWSAccessKey != "" {
		f.awsAccessKey = in.AWSAccessKey
	}
	if in.AWSSecretKey != "" {
		f.awsSecretKey = in.AWSSecretKey
	}
	if in.B2AccountID != "" {
		f.b2AccountID = in.B2AccountID
	}
	if in.B2AccountKey != "" {
		f.b2AccountKey = in.B2AccountKey
	}
	return nil
}

func newBackupSetupCmd() *cobra.Command {
	var (
		repo           string
		creds          backupCredFlags
		interval       string
		verifyInterval string
		dryRun         bool
		jsonOut        bool
	)
	cmd := &cobra.Command{
		Use:   "setup <name> --repo <restic-url> --password <pw>",
		Short: "Turn on remote backups for a Base and run the first snapshot",
		Example: `  ownbasectl backup setup mybase \
    --repo s3:s3.amazonaws.com/my-bucket/ownbase \
    --password <a-strong-restic-password> \
    --aws-access-key-id AKIA... --aws-secret-access-key ...
  echo '{"password":"..."}' | ownbasectl backup setup mybase --repo s3:... --creds-stdin
  ownbasectl backup setup mybase --repo s3:... --password x --dry-run --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := creds.resolve(); err != nil {
				return err
			}
			return runBackupSetup(args[0], repo, creds, interval, verifyInterval, dryRun, jsonOut)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "restic repository URL (s3:..., b2:..., sftp:...) (required)")
	creds.register(cmd)
	cmd.Flags().StringVar(&interval, "interval", "", "backup snapshot cadence, e.g. 1h (default: 1h)")
	cmd.Flags().StringVar(&verifyInterval, "verify-interval", "", "verified-restore drill cadence, e.g. 24h (default: 24h)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute the ownbase.yaml edit without writing secrets or committing")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the dry-run preview (or result) as JSON")
	return cmd
}

// runBackupSetup is the one flow used for both a local VM and a remote
// server (backups always go to a real off-machine restic destination —
// S3, B2, or SFTP — never a local host directory; see docs/decisions.md).
func runBackupSetup(base, repo string, credFlags backupCredFlags, interval, verifyInterval string, dryRun, jsonOut bool) error {
	if repo == "" {
		return fmt.Errorf("--repo is required, e.g. --repo s3:s3.amazonaws.com/mybucket/ownbase")
	}
	if !dryRun && credFlags.password == "" {
		return fmt.Errorf("--password is required (the restic repository encryption password); or pass --creds-stdin. After setup it is also stored in your vault for restore")
	}

	edit := func(current string) (string, string, error) {
		updated := backup.SetCoreBackupConfig(current, repo, interval, verifyInterval)
		return updated, fmt.Sprintf("chore(backup): configure backup repo %s", repo), nil
	}

	if dryRun {
		preview, err := previewConfig(base, edit)
		if err != nil {
			return err
		}
		if jsonOut {
			return printJSON(map[string]any{
				"status":         "preview",
				"repo":           repo,
				"would_change":   preview.WouldChange,
				"commit_message": preview.CommitMessage,
				"diff":           preview.Diff,
				"current":        preview.Current,
				"proposed":       preview.Proposed,
			})
		}
		if !preview.WouldChange {
			fmt.Println("ownbase.yaml already has this backup configuration — nothing would change.")
			return nil
		}
		fmt.Printf("Would configure backup repo %s:\n\n%s\ncommit: %s\n", repo, preview.Diff, preview.CommitMessage)
		return nil
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

	// Also escrow a client-side copy in the vault so restore does not need the
	// password re-typed (and so a destroyed Base is not a circular recovery
	// problem). Flags remain the source of truth for this invocation.
	if err := saveProfile(base, func(p *vault.Profile) {
		p.BackupRepo = repo
		p.ResticPassword = credFlags.password
		if credFlags.awsAccessKey != "" {
			p.AWSAccessKeyID = credFlags.awsAccessKey
		}
		if credFlags.awsSecretKey != "" {
			p.AWSSecretAccessKey = credFlags.awsSecretKey
		}
		if credFlags.b2AccountID != "" {
			p.B2AccountID = credFlags.b2AccountID
		}
		if credFlags.b2AccountKey != "" {
			p.B2AccountKey = credFlags.b2AccountKey
		}
	}); err != nil {
		return fmt.Errorf("store backup credentials in vault: %w", err)
	}

	fmt.Printf("==> Configuring backup repo %s ...\n", repo)
	// The repo/cadence live in ownbase.yaml (external config repo); commit
	// them client-side and reconcile — the same path as any other config
	// mutation. Credentials go through the secrets API above (never git).
	cfgErr := mutateConfig(base, edit)
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
