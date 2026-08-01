package main

// list.go implements `ownbasectl list` and `ownbasectl delete` — enumerating
// configured Bases (vault profiles + local Multipass VMs) and tearing down a
// local VM together with its profile and owner key.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/vmhost"
)

func newListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Show configured Bases (profiles + local VMs)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBaseList(jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "print the Bases as JSON")
	return cmd
}

// listedBase is one row of `list`. It carries no secrets — no token, no key —
// so it is safe to render anywhere, including the desktop app.
type listedBase struct {
	Name string `json:"name"`
	// Host is empty for a Base that has an owner key but no machine yet.
	Host string `json:"host,omitempty"`
	// Kind is "remote", "vm", "key-only", or "unregistered-vm".
	Kind string `json:"kind"`
	// VMState is the Multipass state for a local VM, when one was found.
	VMState string `json:"vm_state,omitempty"`
	// Registered reports whether this Base has a vault profile. A local VM
	// with no profile is listed so it can be adopted or cleaned up.
	Registered bool   `json:"registered"`
	SSHUser    string `json:"ssh_user,omitempty"`
	SSHPort    int    `json:"ssh_port,omitempty"`
	// HasToken and HasKey say whether the credential exists without
	// revealing it, which is what the app needs to show setup progress.
	HasToken      bool   `json:"has_token"`
	HasKey        bool   `json:"has_key"`
	ConfigRepoURL string `json:"config_repo_url,omitempty"`
	ConfigRef     string `json:"config_ref,omitempty"`
}

func runBaseList(jsonOut bool) error {
	bases, vmErr, err := collectBases()
	if err != nil {
		return err
	}

	if jsonOut {
		// A warning must not end up in stdout next to the JSON document.
		if vmErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not list Multipass VMs: %v\n", vmErr)
		}
		if bases == nil {
			// Same contract as `sessions list --json` and `checkup --json`:
			// no Bases is `[]`, never `null`, so callers never need a nil
			// check before ranging over the result.
			bases = []listedBase{}
		}
		return printJSON(bases)
	}

	if len(bases) == 0 {
		fmt.Println("No Bases configured yet.")
		fmt.Println("  Run: ownbasectl create <name>")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tHOST\tKIND")
	for _, b := range bases {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", b.Name, listHostLabel(b), listKindLabel(b))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if vmErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list Multipass VMs: %v\n", vmErr)
	}
	return nil
}

// collectBases merges vault profiles with local Multipass VMs. The VM lookup
// failing is reported separately from a real error, because a machine without
// Multipass installed should still be able to list its remote Bases.
func collectBases() (bases []listedBase, vmErr error, err error) {
	names, err := listBases()
	if err != nil {
		return nil, nil, err
	}

	vms, vmErr := vmhost.New().List(context.Background())
	vmState := make(map[string]string, len(vms))
	for _, v := range vms {
		vmState[v.Name] = v.State
	}

	sort.Strings(names)
	for _, n := range names {
		p, perr := loadProfile(n)
		if perr != nil {
			return nil, vmErr, perr
		}
		b := listedBase{
			Name:          n,
			Host:          p.Host,
			Registered:    true,
			SSHUser:       p.EffectiveSSHUser(),
			SSHPort:       p.EffectiveSSHPort(),
			HasToken:      p.Token != "",
			HasKey:        p.PublicKeyLine() != "",
			ConfigRepoURL: p.ConfigRepoURL,
			ConfigRef:     p.ConfigRef,
		}
		// A profile always claims its name: drop any same-named Multipass VM
		// from vmState so we never emit a second "unregistered-vm" row. That
		// used to happen for key-only profiles (keygen done, create not), and
		// the desktop sidebar keys rows by name.
		if state, ok := vmState[n]; ok {
			b.VMState = state
			delete(vmState, n)
		}
		// Trust the profile's own record of what it is when it is known —
		// the same field delete checks — rather than inferring it from
		// whether a same-named Multipass VM happens to exist. A profile
		// known to be remote is never mislabeled just because an
		// unrelated leftover VM shares its name.
		switch {
		case p.Host == "":
			// keygen has run but create has not: the vault holds an owner
			// key waiting for a machine. If Multipass already has the VM
			// (create died mid-flight), surface it as a local VM rather
			// than a bare key-only row with a twin unregistered entry.
			if b.VMState != "" {
				b.Kind = "vm"
			} else {
				b.Kind = "key-only"
			}
		case p.KnownRemote():
			b.Kind = "remote"
		case b.VMState != "" || p.KnownLocalVM():
			b.Kind = "vm"
		default:
			b.Kind = "remote"
		}
		bases = append(bases, b)
	}

	// Local VMs that exist but were never registered as a Base profile
	// (e.g. `multipass launch` run by hand, or a profile that was removed).
	unregistered := make([]string, 0, len(vmState))
	for n := range vmState {
		unregistered = append(unregistered, n)
	}
	sort.Strings(unregistered)
	for _, n := range unregistered {
		bases = append(bases, listedBase{Name: n, Kind: "unregistered-vm", VMState: vmState[n]})
	}
	return bases, vmErr, nil
}

func listHostLabel(b listedBase) string {
	switch {
	case b.Host != "":
		return b.Host
	case b.Kind == "unregistered-vm":
		return "(unregistered)"
	default:
		return "(not created yet)"
	}
}

func listKindLabel(b listedBase) string {
	switch b.Kind {
	case "key-only":
		return "owner key only"
	case "vm":
		if b.VMState != "" {
			return "local VM (" + b.VMState + ")"
		}
		return "local VM (not found in Multipass)"
	case "unregistered-vm":
		return "local VM (" + b.VMState + ")"
	default:
		return "remote server"
	}
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
	// Skip the Multipass lookup only when the profile is *known* to be
	// remote (LocalVM explicitly false, set by create --remote) — that is
	// what protects a coincidentally same-named local VM from being
	// destroyed by mistake. Every other case still checks Multipass,
	// including no profile at all (an unregistered VM, as `list` can show).
	profile, err := loadProfile(name)
	hasProfile := err == nil
	if err != nil && !isMissingBase(err) {
		return err
	}
	skipVMLookup := hasProfile && profile.KnownRemote()

	prompt := fmt.Sprintf("Delete Base %q? This destroys the local VM (if any — all its data is lost) and removes its profile and owner key from your vault.", name)
	if keepVM {
		prompt = fmt.Sprintf("Remove the profile and owner key for %q from your vault (the VM/server itself is left running)?", name)
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

	if hasProfile {
		if err := deleteProfile(name); err != nil {
			return err
		}
		fmt.Printf("Removed profile and owner key %q from the vault.\n", name)
	}

	fmt.Printf("Base %q deleted.\n", name)
	return nil
}
