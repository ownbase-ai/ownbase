// Command ownbasectl is the OwnBase CLI.
//
// Every command that targets a Base takes its name as a required first
// argument — there is no --server flag and no default Base:
//
//	keygen    — create the SSH keypair you use to reach a Base
//	create    — provision a new Base (local Multipass VM or remote server)
//	adopt     — register a Base installed some other way
//	list      — show configured Bases
//	delete    — remove a Base's profile (and its local VM, if any)
//	restore   — reconstruct a Base from backups onto a fresh VM/server
//	status    — query the running daemon's status API
//	checkup   — one aggregated health report (intrusions, CVEs, updates, backups)
//	updates   — show how far behind each service is from its source
//	security  — network exposure + SSH access monitor + CVE scan
//	backup    — configure/run/check remote backups
//	secrets   — view and manage per-service secrets
//	upgrade   — check/apply updates to the core package (Caddy)
//	config    — set up / read / replace ownbase.yaml (agent-first, non-interactive)
//	service   — add/remove/update a service in ownbase.yaml
//	deploy    — resolve a ref to a commit, pin it, and reconcile
//	ssh-key   — provision the Base's read-only git deploy key
//	ssh       — open a recorded shell (or run one command) on a Base
//	sessions  — list and replay recorded ssh sessions
//	tunnel    — local HTTPS bridge over SSH (interactive; may prompt for sudo)
//
// Credential commands take no Base name — the vault holds every Base:
//
//	vault    — create, unlock, lock, and inspect the credential vault
//	agent    — run or stop the resident credential agent
//
// Local subcommands operate on a repo checkout and take no Base name:
//
//	compile  — compile ownbase.yaml + manifests → runtime/
//	plan     — compute the diff between desired (runtime/) and current state
//	apply    — apply the plan; --dry-run for a no-op preview
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Exit codes. An agent driving ownbasectl unattended needs to tell "you asked
// for something impossible" apart from "the server was not reachable" apart
// from "the install itself failed", because the recovery for each is
// different. Anything unclassified stays 1.
const (
	exitError     = 1 // unclassified failure
	exitUsage     = 2 // bad flags or arguments
	exitPreflight = 3 // target unreachable or unfit before any change was made
	exitInstall   = 4 // the installer ran and failed
	exitNotReady  = 5 // installed, but not healthy within the wait timeout
	// exitConflict is a refusal, not a mistake: the command was well-formed
	// and the machine was fine, but running it would have destroyed
	// something that cannot be recovered. Distinct from exitUsage because
	// the recoveries have nothing in common — argv is not wrong, so a caller
	// cannot fix this by correcting flags and retrying. It needs a decision
	// (repoint anyway with --replace, or pick a different name), which for
	// an unattended caller usually means stopping to ask a human.
	exitConflict = 6
	// exitLocked means no credential was available: the vault is locked, or
	// there is no vault yet. Distinct from every other code because the
	// recovery is neither a flag fix nor a machine problem — it needs a
	// human to type a master password (or open the app), and an unattended
	// caller should say so rather than retrying.
	exitLocked = 7
)

// exitCodeError carries a process exit code alongside an error.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// withExitCode tags err so main exits with a specific code. A nil err stays
// nil so callers can wrap unconditionally.
func withExitCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &exitCodeError{code: code, err: err}
}

// exitCodeFor returns the tagged exit code for err, defaulting to exitError.
func exitCodeFor(err error) int {
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return ec.code
	}
	return exitError
}

// Build metadata, injected at release time via
// -ldflags "-X main.version=... -X main.commit=... -X main.date=...".
// A plain `go build` leaves the defaults, which mark a dev build.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitCodeFor(err))
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ownbasectl",
		Short: "Set up, operate, and own your Base",
		Long: `ownbasectl is the OwnBase CLI: it provisions a Base (a Ubuntu
machine you own, running the ownbased daemon), keeps its backups honest,
and gives you one command for every step of the lifecycle.

Every command that targets a Base takes its name as the first argument.

Start here:
  ownbasectl vault init <path>                     one encrypted file holds
                                                   every credential you own

  ownbasectl create <name>                         try OwnBase on a local VM

  ownbasectl keygen <name>                         ...or install on a server:
  ownbasectl create <name> --remote root@<host>    paste the key, then create`,
		Version: versionString(),
		// Runtime errors are printed once in main() in the established
		// "error: ..." style; usage is only shown for usage errors.
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Flag parse failures are usage errors, not runtime failures.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return withExitCode(exitUsage, err)
	})

	root.AddCommand(
		newVaultCmd(),
		newAgentCmd(),
		newKeygenCmd(),
		newCreateCmd(),
		newAdoptCmd(),
		newListCmd(),
		newDeleteCmd(),
		newRestoreCmd(),
		newStatusCmd(),
		newCheckupCmd(),
		newUpdatesCmd(),
		newSecurityCmd(),
		newBackupCmd(),
		newDBCmd(),
		newSecretsCmd(),
		newUpgradeCmd(),
		newSelfUpdateCmd(),
		newConfigCmd(),
		newServiceCmd(),
		newDeployCmd(),
		newSSHKeyCmd(),
		newSSHCmd(),
		newSessionsCmd(),
		newTunnelCmd(),
		newCompileCmd(),
		newPlanCmd(),
		newApplyCmd(),
		newVersionCmd(),
	)
	tagArgErrorsAsUsage(root)
	return root
}

// tagArgErrorsAsUsage walks the command tree and wraps each Args validator so
// a wrong argument count exits with exitUsage rather than the generic code.
func tagArgErrorsAsUsage(cmd *cobra.Command) {
	for _, sub := range cmd.Commands() {
		if orig := sub.Args; orig != nil {
			sub.Args = func(c *cobra.Command, args []string) error {
				return withExitCode(exitUsage, orig(c, args))
			}
		}
		tagArgErrorsAsUsage(sub)
	}
}

func versionString() string {
	if version == "dev" {
		return "dev (built from source)"
	}
	return fmt.Sprintf("%s (commit %s, built %s)", version, commit, date)
}

// newVersionCmd keeps `ownbasectl version` working alongside the standard
// --version flag.
func newVersionCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the ownbasectl version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOut {
				return printJSON(map[string]string{
					"version": version,
					"commit":  commit,
					"date":    date,
					"string":  versionString(),
				})
			}
			fmt.Println("ownbasectl " + versionString())
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print version fields as JSON")
	return cmd
}
