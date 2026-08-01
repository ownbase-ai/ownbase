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
	)
	return cmd
}

func newVaultInitCmd() *cobra.Command {
	var (
		path          string
		passwordStdin bool
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "init <path>",
		Short: "Create a new credential vault and record where it lives",
		Long: `Creates an empty KDBX vault at the given path and remembers that
location in ~/.ownbase/vault, so every later command finds it. Name a
directory and ownbase.kdbx is created inside it.

Choose somewhere you already sync and back up — the vault is the only copy
of the keys that reach your Bases. A cloud-storage folder is a good default:
the file is encrypted with your master password before it is written, so the
storage provider holds ciphertext and nothing else.

init refuses to overwrite an existing vault. Pointing it at a vault file that
already exists just records the location, which is how a second machine
joins an existing vault.`,
		Example: `  ownbasectl vault init ~/Library/Mobile\ Documents/com~apple~CloudDocs/OwnBase
  ownbasectl vault init ~/Dropbox/OwnBase
  ownbasectl vault init ~/Dropbox/OwnBase --password-stdin < pw.txt`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Positional or --path; the positional form reads better and is
			// what the docs use, but --path predates it and still works.
			if len(args) == 1 {
				if path != "" && path != args[0] {
					return withExitCode(exitUsage, errors.New(
						"give the vault path either as an argument or with --path, not both"))
				}
				path = args[0]
			}
			return runVaultInit(path, passwordStdin, jsonOut)
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "where to create the vault file")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the master password from stdin instead of prompting")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the result as JSON")
	return cmd
}

func runVaultInit(path string, passwordStdin, jsonOut bool) error {
	if strings.TrimSpace(path) == "" {
		return withExitCode(exitUsage, errors.New(
			"a vault path is required, e.g. ownbasectl vault init ~/Dropbox/OwnBase"))
	}

	// Resolve the location before asking for a password: an existing vault is
	// a "record this and unlock it" case, not a "create" case, and the two ask
	// for different things (one password, or a new one confirmed twice).
	// Do not write the pointer yet — a cancelled prompt or a wrong password
	// must leave ~/.ownbase/vault pointing at whatever already worked.
	recorded, err := vault.NormalizePath(path)
	if err != nil {
		return err
	}
	adopting := false
	if _, serr := os.Stat(recorded); serr == nil {
		adopting = true
	}

	password, err := readVaultInitPassword(passwordStdin, adopting)
	if err != nil {
		return err
	}
	if !adopting {
		if _, err := vault.Create(recorded, password); err != nil {
			return err
		}
	}

	// Unlock immediately: creating a vault and then being told it is locked
	// would be a pointless second step.
	c, err := agentClient()
	if err != nil {
		return err
	}
	st, err := c.Unlock(recorded, password, agentd.DefaultIdleTimeout)
	if err != nil {
		return err
	}

	// Only now is the new location known-good. Pointing earlier would strand
	// every later command on a path the user never successfully opened.
	if _, err := vault.RecordPath(path); err != nil {
		return err
	}

	if jsonOut {
		return printJSON(map[string]any{
			"vault_path": recorded,
			"created":    !adopting,
			"unlocked":   true,
			"status":     st,
		})
	}
	if adopting {
		fmt.Printf("Using the existing vault at %s (%d Base(s), %d key(s)).\n", recorded, st.Bases, st.Keys)
		return nil
	}
	fmt.Printf("Created a vault at %s and unlocked it.\n", recorded)
	fmt.Println()
	fmt.Println("Back this file up. It holds the only copy of the SSH keys that reach your")
	fmt.Println("Bases — lose both it and your master password and no one can get in, which")
	fmt.Println("is the same property that makes it safe to keep in cloud storage.")
	fmt.Println()
	fmt.Println("Next:")
	fmt.Println("  ownbasectl keygen <name>    create the key for your first Base")
	return nil
}

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
	if path == "" {
		resolved, err := vault.ResolvePath()
		if err != nil {
			return vaultError(err)
		}
		path = resolved
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no vault at %s — create one with 'ownbasectl vault init %s'", path, path)
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

// interactive reports whether stdin is a terminal we may prompt on.
func interactive() bool { return term.IsTerminal(int(syscall.Stdin)) }
