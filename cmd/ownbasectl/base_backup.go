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
	"github.com/ownbase/ownbase/internal/secrets"
	"github.com/ownbase/ownbase/internal/vault"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Configure, run, and check remote backups (setup|run|status|prune)",
	}
	cmd.AddCommand(
		newBackupSetupCmd(),
		newBackupRunCmd(),
		newBackupStatusCmd(),
		newBackupPruneCmd(),
		newBackupRekeyCmd(),
	)
	return cmd
}

// backupCredFlags are the restic repository credentials shared by
// `backup setup`, `backup prune`, and `restore`.
type backupCredFlags struct {
	password     string
	awsAccessKey string
	awsSecretKey string
	b2AccountID  string
	b2AccountKey string
	// Admin* are delete-capable cloud keys for prune (and optional vault
	// escrow). Never written to the Base's backup secret.
	adminAWSAccessKey string
	adminAWSSecretKey string
	adminB2AccountID  string
	adminB2AccountKey string
	credsStdin        bool
}

// register adds the shared credential flags to cmd.
func (f *backupCredFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.password, "password", "", "restic repository password (required unless --creds-stdin)")
	fl.StringVar(&f.awsAccessKey, "aws-access-key-id", "", "AWS_ACCESS_KEY_ID (for s3: repos; non-deleting when --append-only)")
	fl.StringVar(&f.awsSecretKey, "aws-secret-access-key", "", "AWS_SECRET_ACCESS_KEY (for s3: repos)")
	fl.StringVar(&f.b2AccountID, "b2-account-id", "", "B2_ACCOUNT_ID (for b2: repos)")
	fl.StringVar(&f.b2AccountKey, "b2-account-key", "", "B2_ACCOUNT_KEY (for b2: repos)")
	fl.BoolVar(&f.credsStdin, "creds-stdin", false, "read credentials as JSON from stdin (avoids secrets in argv)")
}

// registerAdmin adds optional delete-capable cloud-key flags used by setup
// (escrow only) and prune (transient override + optional escrow).
func (f *backupCredFlags) registerAdmin(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.adminAWSAccessKey, "admin-aws-access-key-id", "", "delete-capable AWS key for backup prune (vault escrow only; never stored on the Base)")
	fl.StringVar(&f.adminAWSSecretKey, "admin-aws-secret-access-key", "", "delete-capable AWS secret for backup prune")
	fl.StringVar(&f.adminB2AccountID, "admin-b2-account-id", "", "delete-capable B2 account id for backup prune")
	fl.StringVar(&f.adminB2AccountKey, "admin-b2-account-key", "", "delete-capable B2 account key for backup prune")
}

// resolve loads credentials from stdin when --creds-stdin is set. The JSON
// shape is {"password":"…","aws_access_key_id":"…","aws_secret_access_key":"…",
// "b2_account_id":"…","b2_account_key":"…", plus optional admin_* fields}.
// Flag values fill any field the JSON omits.
func (f *backupCredFlags) resolve() error {
	if !f.credsStdin {
		return nil
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read --creds-stdin: %w", err)
	}
	var in struct {
		Password          string `json:"password"`
		AWSAccessKey      string `json:"aws_access_key_id"`
		AWSSecretKey      string `json:"aws_secret_access_key"`
		B2AccountID       string `json:"b2_account_id"`
		B2AccountKey      string `json:"b2_account_key"`
		AdminAWSAccessKey string `json:"admin_aws_access_key_id"`
		AdminAWSSecretKey string `json:"admin_aws_secret_access_key"`
		AdminB2AccountID  string `json:"admin_b2_account_id"`
		AdminB2AccountKey string `json:"admin_b2_account_key"`
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
	if in.AdminAWSAccessKey != "" {
		f.adminAWSAccessKey = in.AdminAWSAccessKey
	}
	if in.AdminAWSSecretKey != "" {
		f.adminAWSSecretKey = in.AdminAWSSecretKey
	}
	if in.AdminB2AccountID != "" {
		f.adminB2AccountID = in.AdminB2AccountID
	}
	if in.AdminB2AccountKey != "" {
		f.adminB2AccountKey = in.AdminB2AccountKey
	}
	return nil
}

func newBackupSetupCmd() *cobra.Command {
	var (
		repo           string
		creds          backupCredFlags
		interval       string
		verifyInterval string
		appendOnly     bool
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
  ownbasectl backup setup mybase --repo s3:... --password x --append-only \
    --aws-access-key-id AKIA_APPEND --aws-secret-access-key ... \
    --admin-aws-access-key-id AKIA_ADMIN --admin-aws-secret-access-key ...
  echo '{"password":"..."}' | ownbasectl backup setup mybase --repo s3:... --creds-stdin
  ownbasectl backup setup mybase --repo s3:... --password x --dry-run --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := creds.resolve(); err != nil {
				return err
			}
			var appendOnlyPtr *bool
			if cmd.Flags().Changed("append-only") {
				v := appendOnly
				appendOnlyPtr = &v
			}
			return runBackupSetup(args[0], repo, creds, interval, verifyInterval, appendOnlyPtr, dryRun, jsonOut)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "restic repository URL (s3:..., b2:..., sftp:...) (required)")
	creds.register(cmd)
	creds.registerAdmin(cmd)
	cmd.Flags().StringVar(&interval, "interval", "", "backup snapshot cadence, e.g. 1h (default: 1h)")
	cmd.Flags().StringVar(&verifyInterval, "verify-interval", "", "verified-restore drill cadence, e.g. 24h (default: 24h)")
	cmd.Flags().BoolVar(&appendOnly, "append-only", false, "disable scheduled prune; Base cloud keys should be non-deleting; run 'backup prune' with delete-capable keys")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute the ownbase.yaml edit without writing secrets or committing")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the dry-run preview (or result) as JSON")
	return cmd
}

// runBackupSetup is the one flow used for both a local VM and a remote
// server (backups always go to a real off-machine restic destination —
// S3, B2, or SFTP — never a local host directory; see docs/decisions.md).
// appendOnly nil leaves core.backup.append_only untouched; non-nil writes it.
func runBackupSetup(base, repo string, credFlags backupCredFlags, interval, verifyInterval string, appendOnly *bool, dryRun, jsonOut bool) error {
	if repo == "" {
		return fmt.Errorf("--repo is required, e.g. --repo s3:s3.amazonaws.com/mybucket/ownbase")
	}
	if !dryRun && credFlags.password == "" {
		return fmt.Errorf("--password is required (the restic repository encryption password); or pass --creds-stdin. After setup it is also stored in your vault for restore")
	}

	edit := func(current string) (string, string, error) {
		updated := backup.SetCoreBackupConfig(current, repo, interval, verifyInterval, appendOnly)
		msg := fmt.Sprintf("chore(backup): configure backup repo %s", repo)
		if appendOnly != nil && *appendOnly {
			msg += " (append-only)"
		}
		return updated, msg, nil
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
				"append_only":    appendOnly,
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

	// Only non-admin keys go to the Base. Admin keys stay in the vault for prune.
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
	// Admin keys are escrowed here too when supplied — never POSTed above.
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
		if credFlags.adminAWSAccessKey != "" {
			p.AdminAWSAccessKeyID = credFlags.adminAWSAccessKey
		}
		if credFlags.adminAWSSecretKey != "" {
			p.AdminAWSSecretAccessKey = credFlags.adminAWSSecretKey
		}
		if credFlags.adminB2AccountID != "" {
			p.AdminB2AccountID = credFlags.adminB2AccountID
		}
		if credFlags.adminB2AccountKey != "" {
			p.AdminB2AccountKey = credFlags.adminB2AccountKey
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
	if appendOnly != nil && *appendOnly {
		fmt.Println()
		fmt.Println("Append-only mode is on: scheduled snapshots will not prune. Apply retention with:")
		fmt.Printf("  ownbasectl backup prune %s\n", base)
		fmt.Println("Use non-deleting cloud keys on the Base; keep delete-capable keys in the vault")
		fmt.Println("(--admin-aws-… / --admin-b2-…) or pass them only when pruning.")
	}
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
			BackupAppendOnly bool   `json:"backup_append_only"`
			LastBackup       string `json:"last_backup"`
			LastVerified     string `json:"last_verified"`
			LastPrune        string `json:"last_prune"`
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
	if s.Security.BackupAppendOnly {
		fmt.Println("  Mode:          append-only (scheduled snapshots do not prune)")
		if s.Security.LastPrune == "" {
			fmt.Printf("  Last prune:    never — run 'ownbasectl backup prune %s'\n", base)
		} else {
			fmt.Printf("  Last prune:    %s\n", shortTime(s.Security.LastPrune))
		}
	} else if s.Security.LastPrune != "" {
		fmt.Printf("  Last prune:    %s\n", shortTime(s.Security.LastPrune))
	}
	fmt.Println("─────────────────────────────────────────────────────────────────────────")
	return nil
}

func newBackupPruneCmd() *cobra.Command {
	var (
		creds   backupCredFlags
		escrow  bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "prune <name>",
		Short: "Run restic forget+prune (owner-driven; required under --append-only)",
		Long: `Apply the retention policy (keep-within 30d, keep-last 3) via restic
forget --prune. Under core.backup.append_only the Base holds non-deleting
cloud keys and never prunes on its own — this command sends delete-capable
credentials through the SSH tunnel for one invocation and does not store
them on the Base.

Credential resolution order for cloud keys: explicit flags / --creds-stdin,
then vault admin escrow (--admin-aws-… stored by setup or --escrow), then
the Base's stored backup secret (works when that secret is still delete-capable).
The restic password defaults from the vault escrow or the Base secret.`,
		Example: `  ownbasectl backup prune mybase
  ownbasectl backup prune mybase \
    --admin-aws-access-key-id AKIA... --admin-aws-secret-access-key ... --escrow
  echo '{"admin_aws_access_key_id":"...","admin_aws_secret_access_key":"..."}' \
    | ownbasectl backup prune mybase --creds-stdin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := creds.resolve(); err != nil {
				return err
			}
			return runBackupPrune(args[0], creds, escrow, jsonOut)
		},
	}
	// Password optional on prune — Base secret / vault usually has it.
	// register still adds --password; we rewrite its help after.
	creds.register(cmd)
	cmd.Flags().Lookup("password").Usage = "restic repository password (default: vault escrow or Base secret)"
	creds.registerAdmin(cmd)
	cmd.Flags().BoolVar(&escrow, "escrow", false, "store any admin cloud keys supplied on this run into the vault for later prune")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the prune result as JSON")
	return cmd
}

// runBackupPrune POSTs /backup/prune with merged credentials.
func runBackupPrune(base string, credFlags backupCredFlags, escrow, jsonOut bool) error {
	// Pull vault escrow first so flags can override empty fields. A locked
	// vault is fatal (connect would fail the same way). A missing Base profile
	// or empty escrow is fine — the Base secret may be enough.
	vc, vcErr := loadBackupCreds(base)
	if vcErr != nil {
		if exitCodeFor(vcErr) == exitLocked {
			return vcErr
		}
		vc = vault.BackupCredentials{}
	}

	// Cloud keys for the prune call: flags → vault admin → vault steady-state
	// (last only helps non-append-only Bases whose stored keys can still delete).
	awsID := firstNonEmpty(credFlags.adminAWSAccessKey, credFlags.awsAccessKey, vc.AdminAWSAccessKeyID, vc.AWSAccessKeyID)
	awsSecret := firstNonEmpty(credFlags.adminAWSSecretKey, credFlags.awsSecretKey, vc.AdminAWSSecretAccessKey, vc.AWSSecretAccessKey)
	b2ID := firstNonEmpty(credFlags.adminB2AccountID, credFlags.b2AccountID, vc.AdminB2AccountID, vc.B2AccountID)
	b2Key := firstNonEmpty(credFlags.adminB2AccountKey, credFlags.b2AccountKey, vc.AdminB2AccountKey, vc.B2AccountKey)
	password := firstNonEmpty(credFlags.password, vc.Password)

	if escrow {
		if err := saveProfile(base, func(p *vault.Profile) {
			if credFlags.adminAWSAccessKey != "" {
				p.AdminAWSAccessKeyID = credFlags.adminAWSAccessKey
			}
			if credFlags.adminAWSSecretKey != "" {
				p.AdminAWSSecretAccessKey = credFlags.adminAWSSecretKey
			}
			if credFlags.adminB2AccountID != "" {
				p.AdminB2AccountID = credFlags.adminB2AccountID
			}
			if credFlags.adminB2AccountKey != "" {
				p.AdminB2AccountKey = credFlags.adminB2AccountKey
			}
		}); err != nil {
			return fmt.Errorf("escrow admin credentials in vault: %w", err)
		}
	}

	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	req := map[string]string{}
	if password != "" {
		req["password"] = password
	}
	if awsID != "" {
		req["aws_access_key_id"] = awsID
	}
	if awsSecret != "" {
		req["aws_secret_access_key"] = awsSecret
	}
	if b2ID != "" {
		req["b2_account_id"] = b2ID
	}
	if b2Key != "" {
		req["b2_account_key"] = b2Key
	}
	payload, _ := json.Marshal(req)

	fmt.Println("Running backup prune ...")
	body, err := apiCallWithTimeout(conn, http.MethodPost, "/backup/prune", payload, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("prune backup: %w", err)
	}
	if jsonOut {
		fmt.Println(string(body))
		return nil
	}
	var r struct {
		LastPrune string `json:"last_prune"`
		LastError string `json:"last_error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		fmt.Println(strings.TrimSpace(string(body)))
		return nil
	}
	if r.LastError != "" {
		return fmt.Errorf("prune failed: %s", r.LastError)
	}
	if r.LastPrune != "" {
		fmt.Printf("Prune complete — last_prune %s\n", shortTime(r.LastPrune))
	} else {
		fmt.Println("Prune complete")
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

func newBackupRekeyCmd() *cobra.Command {
	var (
		newPassword string
		generate    bool
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "rekey <name>",
		Short: "Rotate the restic repository encryption password",
		Long: `Rotate RESTIC_PASSWORD on the restic repository, the Base secret, and
the vault escrow in a crash-safe two-phase flow (restic multi-key):

  1. add      — add the new password as a second key
  2. vault    — escrow the new password client-side
  3. finalize — write the Base secret, then remove every other key

Re-running after any crash converges. Prefer --generate over inventing a
password by hand.`,
		Example: `  ownbasectl backup rekey mybase --generate
  ownbasectl backup rekey mybase --new-password '…'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackupRekey(args[0], newPassword, generate, jsonOut)
		},
	}
	cmd.Flags().StringVar(&newPassword, "new-password", "", "new restic repository password")
	cmd.Flags().BoolVar(&generate, "generate", false, "generate a strong 32-character password")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

// runBackupRekey executes the three-step rotation. generate and newPassword
// are mutually exclusive; one is required.
func runBackupRekey(base, newPassword string, generate, jsonOut bool) error {
	if generate && newPassword != "" {
		return fmt.Errorf("--generate and --new-password are mutually exclusive")
	}
	if !generate && newPassword == "" {
		return fmt.Errorf("pass --new-password or --generate")
	}
	if generate {
		pw, err := secrets.GeneratePassword(32)
		if err != nil {
			return err
		}
		newPassword = pw
	}

	conn, err := connectToServer(base)
	if err != nil {
		return err
	}
	defer conn.close()

	post := func(phase string) (map[string]any, error) {
		payload, _ := json.Marshal(map[string]string{
			"phase":        phase,
			"new_password": newPassword,
		})
		fmt.Printf("==> Rekey phase %s ...\n", phase)
		body, err := apiCallWithTimeout(conn, http.MethodPost, "/backup/rekey", payload, 10*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("rekey %s: %w", phase, err)
		}
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, fmt.Errorf("parse rekey %s response: %w", phase, err)
		}
		return out, nil
	}

	addResult, err := post("add")
	if err != nil {
		return err
	}

	// Escrow before finalize so a crash after the Base secret swap still
	// leaves the new password in the vault.
	if err := saveProfile(base, func(p *vault.Profile) {
		p.ResticPassword = newPassword
	}); err != nil {
		return fmt.Errorf("store new password in vault: %w\n  Phase add succeeded — re-run rekey with the same --new-password to finish", err)
	}

	finResult, err := post("finalize")
	if err != nil {
		return fmt.Errorf("%w\n  Vault already holds the new password — re-run rekey with the same --new-password to finish", err)
	}

	fp, _ := finResult["fingerprint"].(string)
	if jsonOut {
		return printJSON(map[string]any{
			"status":      "ok",
			"fingerprint": fp,
			"add":         addResult,
			"finalize":    finResult,
			// recovery_kit surfaces the new password only when --generate
			// produced it; operators who passed --new-password already have it.
			"generated_password": func() string {
				if generate {
					return newPassword
				}
				return ""
			}(),
		})
	}

	fmt.Println()
	fmt.Println("Restic password rotated on the repository, the Base, and your vault.")
	if generate {
		fmt.Println()
		fmt.Println("Generated password (store offline — this is a root recovery secret):")
		fmt.Printf("  %s\n", newPassword)
	}
	if fp != "" {
		fmt.Printf("Fingerprint: %s\n", fp)
	}
	fmt.Printf("Confirm with: ownbasectl backup status %s\n", base)
	return nil
}
