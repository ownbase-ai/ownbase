package main

// vault.go implements `ownbasectl vault` and `ownbasectl agent` — the two
// commands that exist because credentials moved off the filesystem and into an
// encrypted vault held by a resident agent.
//
// `vault unlock` is the second command in ownbasectl allowed to prompt (the
// first is `tunnel`), and for the same reason: typing a master password is the
// human's "I am sitting here" signal. Everything unattended reads the password
// from stdin (--password-stdin) or $OWNBASE_VAULT_PASSWORD instead, so the
// desktop app and CI are never blocked on a TTY.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ownbase/ownbase/internal/agentd"
	"github.com/ownbase/ownbase/internal/vault"
)

// PasswordEnv lets a non-interactive caller supply the master password. The
// desktop app uses --password-stdin instead, which keeps the password out of
// the process environment where `ps` can see it.
const PasswordEnv = "OWNBASE_VAULT_PASSWORD"

func newVaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Create, unlock, and inspect the credential vault",
		Long: `Every credential ownbasectl uses to reach a Base — the host, the API
token, and the owner SSH key itself — lives in one encrypted KDBX file that
you choose the location of. Put it in iCloud, Dropbox, or any folder you
back up, and the same vault works from every machine you own.

KDBX is the KeePass format, so the file is not ours: KeePassXC and every
other KeePass client can open it. That is the point. If OwnBase disappears
tomorrow, your keys are still readable with software nobody controls.

Unlocking hands the vault to a small resident agent that keeps it in memory
and signs SSH challenges on request, so no private key is ever written to
disk and no other command needs the master password.`,
	}
	cmd.AddCommand(
		newVaultInitCmd(),
		newVaultUnlockCmd(),
		newVaultLockCmd(),
		newVaultStatusCmd(),
		newVaultPasswdCmd(),
		newVaultOpenCmd(),
		newVaultRecoveryStringCmd(),
	)
	return cmd
}

// MinMasterPasswordLen is the floor for a newly chosen master password. The
// vault consolidates every credential that reaches a Base; short passwords
// are not worth the Argon2 cost we pay.
const MinMasterPasswordLen = 12

// vaultInitFlags is shared by local-file and remote object-store init.
type vaultInitFlags struct {
	path            string
	passwordStdin   bool
	jsonOut         bool
	bucket          string
	region          string
	endpoint        string
	key             string
	accessKeyID     string
	secretAccessKey string
	pathStyle       bool
	credsStdin      bool
}

func newVaultInitCmd() *cobra.Command {
	var f vaultInitFlags
	cmd := &cobra.Command{
		Use:   "init [<path>]",
		Short: "Create a new credential vault and record where it lives",
		Long: `Creates an empty KDBX vault and remembers how to find it.

Local file (default):
  ownbasectl vault init ~/Dropbox/OwnBase

Object storage (recommended for headless recovery):
  ownbasectl vault init --bucket my-vault-bucket --region auto \
    --endpoint https://<account>.r2.cloudflarestorage.com \
    --access-key-id … --secret-access-key …

The remote form stores the vault as a single object (default key
ownbase/vault/ownbase.kdbx). After init, print and save the recovery string:

  ownbasectl vault recovery-string

Recovery needs that string plus your master password — nothing else.

init refuses to overwrite an existing vault. Pointing it at a vault that
already exists just records the location (how a second machine joins).`,
		Example: `  ownbasectl vault init ~/Dropbox/OwnBase
  ownbasectl vault init --bucket ownbase-vault --region auto \
    --endpoint https://xxx.r2.cloudflarestorage.com \
    --access-key-id AKIA… --secret-access-key …`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if f.path != "" && f.path != args[0] {
					return withExitCode(exitUsage, errors.New(
						"give the vault path either as an argument or with --path, not both"))
				}
				f.path = args[0]
			}
			return runVaultInit(f)
		},
	}
	cmd.Flags().StringVar(&f.path, "path", "", "local vault file path")
	cmd.Flags().BoolVar(&f.passwordStdin, "password-stdin", false, "read the master password from stdin instead of prompting")
	cmd.Flags().BoolVar(&f.jsonOut, "json", false, "print the result as JSON")
	cmd.Flags().StringVar(&f.bucket, "bucket", "", "S3-compatible bucket for a remote vault")
	cmd.Flags().StringVar(&f.region, "region", "", "bucket region (use auto for R2)")
	cmd.Flags().StringVar(&f.endpoint, "endpoint", "", "S3 API endpoint URL (required for R2/B2/MinIO)")
	cmd.Flags().StringVar(&f.key, "key", vault.DefaultObjectKey, "object key inside the bucket")
	cmd.Flags().StringVar(&f.accessKeyID, "access-key-id", "", "bucket access key id")
	cmd.Flags().StringVar(&f.secretAccessKey, "secret-access-key", "", "bucket secret access key")
	cmd.Flags().BoolVar(&f.pathStyle, "path-style", false, "use path-style S3 URLs (MinIO)")
	cmd.Flags().BoolVar(&f.credsStdin, "creds-stdin", false,
		"read bucket credentials as JSON from stdin (optional \"password\" field; cannot combine with --password-stdin)")
	return cmd
}

func runVaultInit(f vaultInitFlags) error {
	remote := f.bucket != "" || f.endpoint != "" || f.accessKeyID != "" || f.credsStdin
	if remote {
		return runVaultInitRemote(f)
	}
	return runVaultInitLocal(f)
}

func runVaultInitLocal(f vaultInitFlags) error {
	if strings.TrimSpace(f.path) == "" {
		return withExitCode(exitUsage, errors.New(
			"a vault path is required, e.g. ownbasectl vault init ~/Dropbox/OwnBase\n"+
				"       or use --bucket/--endpoint for a remote vault"))
	}

	// Resolve before asking for a password: existing vault = adopt, not create.
	// Do not write the locator yet — a cancelled prompt must leave the prior
	// location alone.
	recorded, err := vault.NormalizePath(f.path)
	if err != nil {
		return err
	}
	adopting := false
	if _, serr := os.Stat(recorded); serr == nil {
		adopting = true
	}

	password, err := readVaultInitPassword(f.passwordStdin, adopting)
	if err != nil {
		return err
	}
	if !adopting {
		if _, err := vault.Create(recorded, password); err != nil {
			return err
		}
	}

	c, err := agentClient()
	if err != nil {
		return err
	}
	st, err := c.Unlock(recorded, password, agentd.DefaultIdleTimeout)
	if err != nil {
		return err
	}

	loc := vault.Locator{Kind: vault.LocatorKindFile, Path: recorded}
	if err := vault.SaveLocator(loc); err != nil {
		return err
	}

	return printVaultInitResult(f.jsonOut, recorded, !adopting, "", st)
}

func runVaultInitRemote(f vaultInitFlags) error {
	// --creds-stdin consumes stdin entirely, so it cannot be combined with
	// --password-stdin. Put "password" in the creds JSON instead.
	if f.credsStdin && f.passwordStdin {
		return withExitCode(exitUsage, errors.New(
			"cannot combine --creds-stdin and --password-stdin (stdin can only be read once); include \"password\" in the creds JSON"))
	}

	var passwordFromCreds string
	if f.credsStdin {
		var err error
		passwordFromCreds, err = readVaultInitCredsStdin(&f)
		if err != nil {
			return err
		}
	}
	if f.bucket == "" || f.region == "" || f.accessKeyID == "" || f.secretAccessKey == "" {
		return withExitCode(exitUsage, errors.New(
			"remote vault init requires --bucket, --region, --access-key-id, and --secret-access-key (or --creds-stdin)"))
	}
	if f.key == "" {
		f.key = vault.DefaultObjectKey
	}

	loc := vault.Locator{
		Kind:            vault.LocatorKindS3,
		Endpoint:        f.endpoint,
		Region:          f.region,
		Bucket:          f.bucket,
		Key:             f.key,
		AccessKeyID:     f.accessKeyID,
		SecretAccessKey: f.secretAccessKey,
		PathStyle:       f.pathStyle,
	}
	store, err := loc.OpenStore()
	if err != nil {
		return err
	}

	// Adopt if the object already exists. A probe error other than NotExist
	// (network blip, auth) is ignored here — CreateStore/OpenStore will
	// surface it with a clearer message.
	adopting := false
	if _, _, gerr := store.Get(cmdContext()); gerr == nil {
		adopting = true
	}

	password := passwordFromCreds
	if password == "" {
		password, err = readVaultInitPassword(f.passwordStdin, adopting)
		if err != nil {
			return err
		}
	} else if !adopting {
		// Password came from creds JSON on a create — still enforce the floor.
		if err := validateMasterPassword(password); err != nil {
			return err
		}
	}

	// Prove the password works BEFORE writing ~/.ownbase/locator. A wrong
	// password on adopt must not displace a prior working locator.
	if adopting {
		if _, err := vault.OpenStore(store, password); err != nil {
			return err
		}
	} else {
		if _, err := vault.CreateStore(store, password); err != nil {
			return err
		}
	}

	// Encode recovery as soon as the vault exists so a later unlock failure
	// cannot swallow the one portable secret the user must save.
	recovery, err := vault.EncodeRecovery(loc)
	if err != nil {
		return err
	}

	if err := vault.SaveLocator(loc); err != nil {
		// Vault is in the bucket; still print recovery so it is not lost.
		printRecoveryFallback(f.jsonOut, recovery, err)
		return err
	}

	c, err := agentClient()
	if err != nil {
		printRecoveryFallback(f.jsonOut, recovery, err)
		return err
	}
	// Empty path → agent uses ResolveStore (the locator we just wrote).
	st, err := c.Unlock("", password, agentd.DefaultIdleTimeout)
	if err != nil {
		printRecoveryFallback(f.jsonOut, recovery, err)
		return fmt.Errorf("vault is in place at %s but unlock failed: %w\n  Recovery string (save this):\n  %s",
			loc.Location(), err, recovery)
	}

	return printVaultInitResult(f.jsonOut, loc.Location(), !adopting, recovery, st)
}

// printRecoveryFallback surfaces the recovery string when init cannot finish
// cleanly after the vault object already exists.
func printRecoveryFallback(jsonOut bool, recovery string, cause error) {
	if recovery == "" {
		return
	}
	if jsonOut {
		// Best-effort JSON line on stderr so scripted callers can scrape it
		// without confusing stdout success parsing.
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{
			"recovery_string": recovery,
			"error":           cause.Error(),
		})
		return
	}
	fmt.Fprintln(os.Stderr, "Vault object exists, but setup did not finish cleanly.")
	fmt.Fprintln(os.Stderr, "Save this recovery string — it will not be shown again unless the locator was written:")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, recovery)
	fmt.Fprintln(os.Stderr)
}

// readVaultInitCredsStdin reads bucket credentials (and optional master
// password) as one JSON object from stdin. Returns the password when present.
func readVaultInitCredsStdin(f *vaultInitFlags) (password string, err error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read creds from stdin: %w", err)
	}
	var in struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		Bucket          string `json:"bucket"`
		Region          string `json:"region"`
		Endpoint        string `json:"endpoint"`
		Key             string `json:"key"`
		Password        string `json:"password"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return "", fmt.Errorf("parse creds JSON: %w", err)
	}
	if in.AccessKeyID != "" {
		f.accessKeyID = in.AccessKeyID
	}
	if in.SecretAccessKey != "" {
		f.secretAccessKey = in.SecretAccessKey
	}
	if in.Bucket != "" {
		f.bucket = in.Bucket
	}
	if in.Region != "" {
		f.region = in.Region
	}
	if in.Endpoint != "" {
		f.endpoint = in.Endpoint
	}
	if in.Key != "" {
		f.key = in.Key
	}
	return in.Password, nil
}

func printVaultInitResult(jsonOut bool, location string, created bool, recovery string, st *agentd.Status) error {
	if jsonOut {
		out := map[string]any{
			"vault_path": location,
			"created":    created,
			"unlocked":   true,
			"status":     st,
		}
		if recovery != "" {
			out["recovery_string"] = recovery
		}
		return printJSON(out)
	}
	if !created {
		fmt.Printf("Using the existing vault at %s (%d Base(s), %d key(s)).\n", location, st.Bases, st.Keys)
		return nil
	}
	fmt.Printf("Created a vault at %s and unlocked it.\n", location)
	fmt.Println()
	if recovery != "" {
		fmt.Println("Save this recovery string somewhere durable (1Password, printed paper).")
		fmt.Println("Together with your master password it is everything you need to reopen")
		fmt.Println("the vault on a new machine:")
		fmt.Println()
		fmt.Println(recovery)
		fmt.Println()
		fmt.Println("Re-print later with: ownbasectl vault recovery-string")
		fmt.Println()
	} else {
		fmt.Println("Back this file up. It holds the only copy of the SSH keys that reach your")
		fmt.Println("Bases — lose both it and your master password and no one can get in.")
		fmt.Println()
	}
	fmt.Println("Next:")
	fmt.Println("  ownbasectl keygen <name>    create the key for your first Base")
	return nil
}

// cmdContext is a tiny hook so tests can inject a context later if needed.
func cmdContext() context.Context { return context.Background() }

// readVaultInitPassword asks for the master password. Creating a vault needs a
// new password confirmed twice, because a typo there is unrecoverable; adopting
// one that already exists needs the password it already has, which confirming
// would only get in the way of.
func readVaultInitPassword(fromStdin, adopting bool) (string, error) {
	if adopting {
		return readPassword(fromStdin, "Master password: ")
	}
	return readNewPassword(fromStdin)
}

func newVaultUnlockCmd() *cobra.Command {
	var (
		passwordStdin bool
		path          string
		idleTimeout   time.Duration
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "Unlock the vault and keep it unlocked in the agent",
		Long: `Prompts for the master password, hands the decrypted vault to the
resident agent, and returns. Every later ownbasectl command — and every SSH
connection it makes — works without the password until the vault locks again.

The agent starts itself if it is not running, and outlives the shell that
started it, so this works the same in a headless SSH session as it does with
the desktop app open.

The vault auto-locks after --idle-timeout with nothing using it. Pass
--idle-timeout 0 to disable that.`,
		Example: `  ownbasectl vault unlock
  ownbasectl vault unlock --idle-timeout 30m
  echo "$PW" | ownbasectl vault unlock --password-stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A timeout the user did not set must not overwrite whatever the
			// agent is already configured with.
			explicit := cmd.Flags().Changed("idle-timeout")
			return runVaultUnlock(path, passwordStdin, idleTimeout, explicit, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the master password from stdin instead of prompting")
	cmd.Flags().StringVar(&path, "path", "", "unlock a vault at this path instead of the recorded one")
	cmd.Flags().DurationVar(&idleTimeout, "idle-timeout", agentd.DefaultIdleTimeout, "auto-lock after this long unused (0 = never)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the resulting agent status as JSON")
	return cmd
}

func runVaultUnlock(path string, passwordStdin bool, idleTimeout time.Duration, explicitTimeout, jsonOut bool) error {
	// Empty path → agent resolves the locator (file or remote). Explicit path
	// is always a local file. Do not os.Stat a remote location.
	if path != "" {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("no vault at %s — create one with 'ownbasectl vault init %s'", path, path)
		}
	} else {
		if _, err := vault.LoadLocator(); err != nil {
			return vaultError(err)
		}
	}

	c, err := agentClient()
	if err != nil {
		return err
	}

	timeout := time.Duration(-1)
	if explicitTimeout {
		timeout = idleTimeout
	}

	// Re-prompt on a wrong password rather than exiting: a typo is the most
	// likely outcome of typing a long passphrase, and making the user re-run
	// the command teaches nothing.
	for attempt := 0; ; attempt++ {
		password, perr := readPassword(passwordStdin, "Master password: ")
		if perr != nil {
			return perr
		}
		st, uerr := c.Unlock(path, password, timeout)
		if uerr == nil {
			warnStaleRecovery()
			if jsonOut {
				return printJSON(st)
			}
			fmt.Printf("Vault unlocked: %s (%d Base(s), %d key(s)).\n", st.VaultPath, st.Bases, st.Keys)
			if st.LocksAt != nil {
				fmt.Printf("Locks automatically at %s unless something uses it.\n", st.LocksAt.Local().Format(time.Kitchen))
			}
			return nil
		}
		if !errors.Is(uerr, vault.ErrWrongPassword) || passwordStdin || attempt >= 2 || !interactive() {
			return uerr
		}
		fmt.Fprintln(os.Stderr, "Wrong master password — try again.")
	}
}

// warnStaleRecovery compares the live locator credentials to the fingerprint
// stamped when the recovery string was last printed.
func warnStaleRecovery() {
	loc, err := vault.LoadLocator()
	if err != nil || loc.Kind != vault.LocatorKindS3 {
		return
	}
	live := loc.Fingerprint()
	if loc.CredsFingerprint == "" || live == "" || loc.CredsFingerprint == live {
		return
	}
	fmt.Fprintln(os.Stderr, "ownbasectl: warning: storage credentials have changed since the recovery string was last printed.")
	fmt.Fprintln(os.Stderr, "  Re-print and save a fresh one: ownbasectl vault recovery-string")
}

func newVaultOpenCmd() *cobra.Command {
	var (
		recovery      string
		passwordStdin bool
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Configure the vault from a recovery string and unlock it",
		Long: `On a fresh machine, paste the recovery string printed at vault init
(or by 'vault recovery-string'), enter the master password, and the vault is
reachable again. Writes ~/.ownbase/locator so later unlocks need only the
password.`,
		Example: `  ownbasectl vault open --recovery 'ownbase-recovery-v1:…'
  echo "$RECOVERY" | ownbasectl vault open --recovery-stdin --password-stdin`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultOpen(recovery, passwordStdin, jsonOut)
		},
	}
	cmd.Flags().StringVar(&recovery, "recovery", "", "recovery string from vault init")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the master password from stdin")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runVaultOpen(recovery string, passwordStdin, jsonOut bool) error {
	if strings.TrimSpace(recovery) == "" {
		return withExitCode(exitUsage, errors.New("--recovery is required"))
	}
	loc, err := vault.DecodeRecovery(recovery)
	if err != nil {
		return err
	}
	password, err := readPassword(passwordStdin, "Master password: ")
	if err != nil {
		return err
	}
	// Verify the password opens the vault before recording the locator.
	store, err := loc.OpenStore()
	if err != nil {
		return err
	}
	if _, err := vault.OpenStore(store, password); err != nil {
		return err
	}
	if err := vault.SaveLocator(loc); err != nil {
		return err
	}
	c, err := agentClient()
	if err != nil {
		return err
	}
	st, err := c.Unlock("", password, agentd.DefaultIdleTimeout)
	if err != nil {
		return err
	}
	if jsonOut {
		return printJSON(st)
	}
	fmt.Printf("Vault opened and unlocked: %s (%d Base(s), %d key(s)).\n", st.VaultPath, st.Bases, st.Keys)
	return nil
}

func newVaultRecoveryStringCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "recovery-string",
		Short: "Print the recovery string for the configured vault",
		Long: `Prints the portable recovery string for the current locator. Save it
with your master password somewhere durable. Re-printing after rotating
storage credentials updates the fingerprint so unlock can warn about stale
copies.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			loc, err := vault.LoadLocator()
			if err != nil {
				return vaultError(err)
			}
			if loc.Kind != vault.LocatorKindS3 {
				return errors.New("recovery strings are for remote vaults — this vault is a local file")
			}
			s, err := vault.EncodeRecovery(loc)
			if err != nil {
				return err
			}
			// Refresh the fingerprint stamp so unlock knows this print is current.
			if err := vault.SaveLocator(loc); err != nil {
				return err
			}
			if jsonOut {
				return printJSON(map[string]string{
					"recovery_string": s,
					"location":        loc.Location(),
				})
			}
			fmt.Println(s)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print as JSON")
	return cmd
}

func newVaultLockCmd() *cobra.Command {
	var shutdown bool
	cmd := &cobra.Command{
		Use:   "lock",
		Short: "Forget the master password and every key held in memory",
		Long: `Locks the vault in the agent. The master password, the decrypted
profiles, and every SSH key are dropped; the next command needs an unlock.

The desktop app runs this when it quits if you asked it to.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := agentd.NewClient()
			if err != nil {
				return err
			}
			if shutdown {
				if err := c.Shutdown(); err != nil {
					return err
				}
				fmt.Println("Vault locked and the agent stopped.")
				return nil
			}
			if err := c.Lock(); err != nil {
				return err
			}
			fmt.Println("Vault locked.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&shutdown, "stop-agent", false, "also stop the agent process")
	return cmd
}

func newVaultStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show whether the vault is unlocked, and where it lives",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Deliberately does not start the agent: "is it running?" must be
			// answerable without changing the answer.
			c, err := agentd.NewClient()
			if err != nil {
				return err
			}
			st, err := c.Status()
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(st)
			}
			printVaultStatus(st)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the status as JSON")
	return cmd
}

func printVaultStatus(st *agentd.Status) {
	if st.Running {
		fmt.Printf("Agent:  running (pid %d)\n", st.PID)
	} else {
		fmt.Println("Agent:  not running")
	}

	switch {
	case st.VaultPath == "":
		fmt.Println("Vault:  none configured")
		fmt.Println()
		fmt.Println("Create one with: ownbasectl vault init <path>")
	case st.Unlocked:
		fmt.Printf("Vault:  unlocked — %s\n", st.VaultPath)
		fmt.Printf("Holds:  %d Base(s), %d owner key(s)\n", st.Bases, st.Keys)
		if st.LocksAt != nil {
			fmt.Printf("Locks:  %s (idle timeout %s)\n",
				st.LocksAt.Local().Format(time.RFC1123), time.Duration(st.IdleTimeoutSeconds)*time.Second)
		} else {
			fmt.Println("Locks:  never (no idle timeout)")
		}
		fmt.Printf("Signs:  %s\n", st.SSHAgentSocket)
	default:
		fmt.Printf("Vault:  locked — %s\n", st.VaultPath)
		fmt.Println()
		fmt.Println("Unlock it with: ownbasectl vault unlock")
	}
}

func newVaultPasswdCmd() *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "passwd",
		Short: "Change the vault's master password",
		Long: `Re-encrypts the vault under a new master password. The vault must be
unlocked first, so this never needs the old password twice.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := agentClient()
			if err != nil {
				return err
			}
			password, err := readNewPassword(passwordStdin)
			if err != nil {
				return err
			}
			if err := c.ChangePassword(password); err != nil {
				return vaultError(err)
			}
			fmt.Println("Master password changed.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the new password from stdin instead of prompting")
	return cmd
}

// ---------------------------------------------------------------------------
// agent
// ---------------------------------------------------------------------------

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run or stop the credential agent",
		Long: `The agent holds the unlocked vault in memory and signs SSH challenges
on request, so nothing else has to handle your master password or your
private keys.

You do not normally run this yourself: any command that needs a credential
starts it. These subcommands exist for when you want to see it, log it, or
stop it.`,
	}
	cmd.AddCommand(newAgentRunCmd(), newAgentStopCmd())
	return cmd
}

func newAgentRunCmd() *cobra.Command {
	var foreground bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the credential agent in the foreground",
		Long: `Binds ~/.ownbase/agent.sock and ~/.ownbase/ssh-agent.sock and serves
until interrupted. Starts locked: something must call 'vault unlock'.

Exits with an error if an agent is already running.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = foreground // the only mode; the flag documents intent for scripts
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return agentd.NewServer(versionString()).Serve(ctx)
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", true, "stay in the foreground (the only supported mode)")
	return cmd
}

func newAgentStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the credential agent, locking the vault",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := agentd.NewClient()
			if err != nil {
				return err
			}
			if err := c.Shutdown(); err != nil {
				return err
			}
			fmt.Println("Agent stopped.")
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// Password input
// ---------------------------------------------------------------------------

// readPassword obtains the master password, from stdin when fromStdin is set,
// then from $OWNBASE_VAULT_PASSWORD, then by prompting on the terminal.
func readPassword(fromStdin bool, prompt string) (string, error) {
	if fromStdin {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read the master password from stdin: %w", err)
		}
		pw := strings.TrimRight(string(data), "\r\n")
		if pw == "" {
			return "", errors.New("no master password on stdin")
		}
		return pw, nil
	}
	if pw := os.Getenv(PasswordEnv); pw != "" {
		return pw, nil
	}
	if !interactive() {
		return "", withExitCode(exitUsage, fmt.Errorf(
			"no terminal to prompt on — pass --password-stdin or set %s", PasswordEnv))
	}

	fmt.Fprint(os.Stderr, prompt)
	data, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read master password: %w", err)
	}
	pw := strings.TrimSpace(string(data))
	if pw == "" {
		return "", errors.New("empty master password")
	}
	return pw, nil
}

// readNewPassword reads a password that is about to become the only thing
// standing between an attacker and every key the user owns, so an interactive
// caller confirms it. A typo here is unrecoverable.
func readNewPassword(fromStdin bool) (string, error) {
	first, err := readPassword(fromStdin, "New master password: ")
	if err != nil {
		return "", err
	}
	if err := validateMasterPassword(first); err != nil {
		return "", err
	}
	if fromStdin || os.Getenv(PasswordEnv) != "" || !interactive() {
		return first, nil
	}
	again, err := readPassword(false, "Confirm master password: ")
	if err != nil {
		return "", err
	}
	if first != again {
		return "", errors.New("the two passwords do not match")
	}
	return first, nil
}

func validateMasterPassword(pw string) error {
	// Count runes so a passphrase of short words still clears the floor.
	if len([]rune(pw)) < MinMasterPasswordLen {
		return fmt.Errorf("master password must be at least %d characters (this vault holds every key that reaches your Bases)", MinMasterPasswordLen)
	}
	return nil
}

// interactive reports whether stdin is a terminal we may prompt on.
func interactive() bool { return term.IsTerminal(int(syscall.Stdin)) }
