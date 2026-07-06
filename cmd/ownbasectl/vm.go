package main

// vm.go implements `ownbasectl vm` — start/stop/restart/list/ip for the
// local Multipass VMs behind `ownbasectl create` (no --remote).
//
// Multipass assigns a VM's IPv4 address by DHCP; it is released on `stop`
// and very likely changes on the next `start`. Before this command existed,
// that meant a manual `ownbasectl adopt --host <new-ip> --token ...` dance
// after every stop/start (see docs/troubleshooting.md). `vm start`/`restart`
// now re-detect the IP, update the profile automatically, and — for
// dev-TLS Bases — rewrite the /etc/hosts block so hostnames like
// forgejo.mybase.test keep resolving without any extra step.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/serverconfig"
	"github.com/ownbase/ownbase/internal/vmhost"
)

func newVMCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Start, stop, or inspect a local Multipass VM Base",
		Long: `Manages the Multipass VM lifecycle for Bases created without --remote.
'vm start' and 'vm restart' re-detect the VM's IP (which changes after
Multipass reassigns it via DHCP), update the saved profile, and refresh
/etc/hosts for dev-TLS Bases — replacing the manual 'ownbasectl adopt
--host <new-ip>' step previously needed after every stop/start.`,
	}
	cmd.AddCommand(
		newVMStartCmd(),
		newVMStopCmd(),
		newVMRestartCmd(),
		newVMListCmd(),
		newVMIPCmd(),
	)
	return cmd
}

func newVMStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Start a stopped local VM and refresh its profile IP + /etc/hosts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVMStart(args[0])
		},
	}
}

func newVMStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a local VM (data is preserved; the VM's IP will change on next start)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVMStop(args[0])
		},
	}
}

func newVMRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart <name>",
		Short: "Stop then start a local VM, refreshing its profile IP + /etc/hosts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runVMStop(args[0]); err != nil {
				return err
			}
			return runVMStart(args[0])
		},
	}
}

func newVMListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show local-VM Bases and their current Multipass state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVMList()
		},
	}
}

func newVMIPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ip <name>",
		Short: "Print the VM's current Multipass IPv4 address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVMIP(args[0])
		},
	}
}

// loadLocalVMProfile loads the named profile and rejects a profile known to
// be a remote server (`create --remote`) — vm's whole purpose is Multipass
// lifecycle management, which is meaningless for those. A profile that is
// merely unregistered, or predates the LocalVM field, is allowed through:
// the Multipass calls below fail on their own with a clear "VM not found"
// if there truly is nothing to start/stop.
func loadLocalVMProfile(name string) (*serverconfig.Config, serverconfig.ServerProfile, bool, string, error) {
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return nil, serverconfig.ServerProfile{}, false, "", fmt.Errorf("locate config: %w", err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return nil, serverconfig.ServerProfile{}, false, "", fmt.Errorf("load config: %w", err)
	}
	profile, hasProfile := cfg.Servers[name]
	if hasProfile && profile.KnownRemote() {
		return nil, serverconfig.ServerProfile{}, false, "", fmt.Errorf(
			"Base %q is a remote server (created with --remote) — 'ownbasectl vm' only manages local Multipass VMs", name)
	}
	return cfg, profile, hasProfile, cfgPath, nil
}

func runVMStart(name string) error {
	cfg, profile, hasProfile, cfgPath, err := loadLocalVMProfile(name)
	if err != nil {
		return err
	}

	m := vmhost.New()
	ctx := context.Background()

	fmt.Printf("==> Starting local VM %q ...\n", name)
	if err := m.Start(ctx, name); err != nil {
		return fmt.Errorf("start VM %q: %w", name, err)
	}

	ip, err := waitForVMIPv4(ctx, m, name, 90*time.Second)
	if err != nil {
		return err
	}
	fmt.Printf("    VM is up at %s\n", ip)

	if !hasProfile {
		fmt.Printf("    (no profile registered for %q — run 'ownbasectl adopt' to manage it with ownbasectl)\n", name)
		return nil
	}

	ipChanged := profile.Host != ip
	profile.Host = ip
	cfg.Servers[name] = profile
	if err := serverconfig.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if ipChanged {
		fmt.Printf("    Updated profile host -> %s\n", ip)
	}

	if profile.DevTLSDomain == "" {
		return nil
	}
	hostnames, err := existingHostsForBase(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read existing /etc/hosts entries for %q: %v\n", name, err)
		return nil
	}
	if len(hostnames) == 0 {
		fmt.Fprintf(os.Stderr,
			"warning: %q uses dev-TLS but has no /etc/hosts entries to refresh — run 'ownbasectl dev-tls sync %s' once it is reachable\n",
			name, name)
		return nil
	}
	if err := writeHostsBlock(name, ip, hostnames); err != nil {
		return fmt.Errorf("refresh /etc/hosts: %w", err)
	}
	fmt.Printf("    Refreshed /etc/hosts: %s\n", strings.Join(hostnames, ", "))
	return nil
}

func runVMStop(name string) error {
	_, _, _, _, err := loadLocalVMProfile(name)
	if err != nil {
		return err
	}

	m := vmhost.New()
	ctx := context.Background()
	fmt.Printf("==> Stopping local VM %q ...\n", name)
	if err := m.Stop(ctx, name); err != nil {
		return fmt.Errorf("stop VM %q: %w", name, err)
	}
	fmt.Printf("VM %q stopped. Its IP will very likely change on the next start.\n", name)
	return nil
}

func runVMList() error {
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("locate config: %w", err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	names := make([]string, 0, len(cfg.Servers))
	for n, p := range cfg.Servers {
		if p.KnownRemote() {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		fmt.Println("No local-VM Bases registered.")
		fmt.Println("  Run: ownbasectl create <name>")
		return nil
	}

	vms, vmErr := vmhost.New().List(context.Background())
	state := make(map[string]string, len(vms))
	for _, v := range vms {
		state[v.Name] = v.State
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tMULTIPASS STATE\tPROFILE HOST")
	for _, n := range names {
		p := cfg.Servers[n]
		st, ok := state[n]
		if !ok {
			st = "(not found in Multipass)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", n, st, p.Host)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if vmErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list Multipass VMs: %v\n", vmErr)
	}
	return nil
}

func runVMIP(name string) error {
	_, profile, hasProfile, _, err := loadLocalVMProfile(name)
	if err != nil {
		return err
	}

	ip, err := vmhost.New().IPv4(context.Background(), name)
	if err != nil {
		return fmt.Errorf("get IPv4 for VM %q: %w", name, err)
	}
	fmt.Println(ip)

	if hasProfile && profile.Host != "" && profile.Host != ip {
		fmt.Fprintf(os.Stderr,
			"note: the saved profile host (%s) differs from the VM's current IP (%s) — run 'ownbasectl vm start %s' to refresh it\n",
			profile.Host, ip, name)
	}
	return nil
}

// waitForVMIPv4 polls Multipass for the named VM's IPv4 address, retrying
// for a few seconds after `multipass start` returns — the VM's network
// interface is not always ready the instant the command completes.
func waitForVMIPv4(ctx context.Context, m *vmhost.Multipass, name string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		ip, err := m.IPv4(ctx, name)
		if err == nil && ip != "" {
			return ip, nil
		}
		lastErr = err
		time.Sleep(3 * time.Second)
	}
	return "", fmt.Errorf("timed out waiting for VM %q to get an IPv4 address: %w", name, lastErr)
}
