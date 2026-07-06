// Command ownbasectl is the OwnBase CLI.
//
// Every command that targets a Base takes its name as a required first
// argument — there is no --server flag and no default Base:
//
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
//	config    — read/replace ownbase.yaml (agent-first, non-interactive)
//	service   — add/remove/update a service in ownbase.yaml
//
// Local subcommands operate on a repo checkout and take no Base name:
//
//	compile  — compile ownbase.yaml + manifests → runtime/
//	plan     — compute the diff between desired (runtime/) and current state
//	apply    — apply the plan; --dry-run for a no-op preview
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

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
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ownbasectl",
		Short: "Set up, operate, and own your Base",
		Long: `ownbasectl is the OwnBase CLI: it provisions a Base (a user-owned
Ubuntu machine managed by the ownbased daemon), keeps its backups honest,
and gives you one command for every step of the lifecycle.

Every command that targets a Base takes its name as the first argument.

Start here:
  ownbasectl create <name>                        try OwnBase on a local VM
  ownbasectl create <name> --remote root@<host>    install on a fresh Ubuntu server`,
		Version: versionString(),
		// Runtime errors are printed once in main() in the established
		// "error: ..." style; usage is only shown for usage errors.
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	root.AddCommand(
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
		newSecretsCmd(),
		newUpgradeCmd(),
		newConfigCmd(),
		newServiceCmd(),
		newCompileCmd(),
		newPlanCmd(),
		newApplyCmd(),
		newVersionCmd(),
	)
	return root
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
	return &cobra.Command{
		Use:   "version",
		Short: "Print the ownbasectl version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("ownbasectl " + versionString())
		},
	}
}
