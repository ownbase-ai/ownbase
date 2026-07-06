package main

// list.go implements `ownbasectl list` and `ownbasectl delete` — enumerating
// configured Bases (profiles + local Multipass VMs) and tearing down a
// local VM together with its profile.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/serverconfig"
	"github.com/ownbase/ownbase/internal/vmhost"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show configured Bases (profiles + local VMs)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBaseList()
		},
	}
}

func runBaseList() error {
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("locate config: %w", err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	vms, vmErr := vmhost.New().List(context.Background())
	vmState := make(map[string]string, len(vms))
	for _, v := range vms {
		vmState[v.Name] = v.State
	}

	if len(cfg.Servers) == 0 && len(vms) == 0 {
		fmt.Println("No Bases configured yet.")
		fmt.Println("  Run: ownbasectl create <name>")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tHOST\tKIND")

	names := make([]string, 0, len(cfg.Servers))
	for n := range cfg.Servers {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		p := cfg.Servers[n]
		// Trust the profile's own record of what it is when it is known —
		// the same field delete checks — rather than inferring it from
		// whether a same-named Multipass VM happens to exist. A profile
		// known to be remote is never mislabeled just because an
		// unrelated leftover VM shares its name. A legacy profile that
		// predates LocalVM (neither known-local nor known-remote) falls
		// back to the old "does a VM exist with this name" heuristic.
		kind := "remote server"
		if !p.KnownRemote() {
			if state, ok := vmState[n]; ok {
				kind = "local VM (" + state + ")"
				delete(vmState, n) // seen — don't list again below
			} else if p.KnownLocalVM() {
				kind = "local VM (not found in Multipass)"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", n, p.Host, kind)
	}

	// Local VMs that exist but were never registered as a Base profile
	// (e.g. `multipass launch` run by hand, or a profile that was removed).
	unregistered := make([]string, 0, len(vmState))
	for n := range vmState {
		unregistered = append(unregistered, n)
	}
	sort.Strings(unregistered)
	for _, n := range unregistered {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", n, "(unregistered)", "local VM ("+vmState[n]+")")
	}

	if err := tw.Flush(); err != nil {
		return err
	}
	if vmErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list Multipass VMs: %v\n", vmErr)
	}
	return nil
}

func newDeleteCmd() *cobra.Command {
	var (
		keepVM    bool
		assumeYes bool
	)
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Tear down a local VM and its profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBaseDelete(args[0], keepVM, assumeYes)
		},
	}
	cmd.Flags().BoolVar(&keepVM, "keep-vm", false, "remove the profile but leave the local VM running")
	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

func runBaseDelete(name string, keepVM, assumeYes bool) error {
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("locate config: %w", err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Skip the Multipass lookup only when the profile is *known* to be
	// remote (LocalVM explicitly false, set by create --remote) — that is
	// what protects a coincidentally same-named local VM from being
	// destroyed by mistake. Every other case still checks Multipass: no
	// profile at all (an unregistered VM, as `list` can show), a profile
	// explicitly marked local, and a legacy profile that predates LocalVM
	// (unset — must not be assumed remote, or older Bases could never
	// have their VM cleaned up via delete).
	profile, hasProfile := cfg.Servers[name]
	skipVMLookup := hasProfile && profile.KnownRemote()

	prompt := fmt.Sprintf("Delete Base %q? This destroys the local VM (if any — all its data is lost) and removes the profile from ~/.ownbase/config.", name)
	if keepVM {
		prompt = fmt.Sprintf("Remove the profile for %q from ~/.ownbase/config (the VM/server itself is left running)?", name)
	}
	if !confirm(prompt, assumeYes) {
		return errAborted
	}

	if !keepVM && !skipVMLookup {
		m := vmhost.New()
		ctx := context.Background()
		exists, err := m.Exists(ctx, name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not check for a local VM named %q: %v\n", name, err)
		} else if exists {
			fmt.Printf("Deleting local VM %q ...\n", name)
			if err := m.Delete(ctx, name); err != nil {
				return fmt.Errorf("delete VM %q: %w", name, err)
			}
		}
	}

	// Clean up any dev-TLS /etc/hosts block for this Base. removeHostsBlock
	// is idempotent — a safe no-op (no sudo prompt) when this Base never had
	// a block, so it is fine to call unconditionally whenever the VM itself
	// is being torn down.
	if !keepVM {
		if err := removeHostsBlock(name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove /etc/hosts entry for %q: %v\n", name, err)
		}
	}

	if hasProfile {
		delete(cfg.Servers, name)
		if err := serverconfig.Save(cfgPath, cfg); err != nil {
			return fmt.Errorf("save config: %w", err)
		}
		fmt.Printf("Removed profile %q.\n", name)
	}

	fmt.Printf("Base %q deleted.\n", name)
	return nil
}
