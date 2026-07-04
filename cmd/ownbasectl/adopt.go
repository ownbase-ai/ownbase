package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ownbase/ownbase/internal/serverconfig"
	"github.com/ownbase/ownbase/internal/tunnel"
)

func newAdoptCmd() *cobra.Command {
	var (
		host    string
		sshUser string
		sshKey  string
		sshPort int
		apiPort int
		token   string
	)
	cmd := &cobra.Command{
		Use:   "adopt <name> --host <host> --token <token>",
		Short: "Register an already-installed Base (verifies SSH connectivity before saving)",
		Long: `Register a Base that was installed without ownbasectl create — for
example a server someone else provisioned. The token was printed at
install time and is stored at /opt/ownbase/api-token on the Base.

Bases created with 'ownbasectl create' are registered automatically;
this command is only needed to connect to an already-installed Base.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdopt(args[0], host, sshUser, sshKey, sshPort, apiPort, token)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&host, "host", "", "SSH hostname or IP address of the Base (required)")
	fl.StringVar(&sshUser, "ssh-user", "root",
		"SSH login user (remote servers are typically reached as root; local VMs created by 'create' use ubuntu)")
	fl.StringVar(&sshKey, "ssh-key", serverconfig.DefaultSSHKey, "path to SSH private key")
	fl.IntVar(&sshPort, "ssh-port", 22, "SSH port on the Base")
	fl.IntVar(&apiPort, "api-port", serverconfig.DefaultAPIPort, "agent API port on the Base")
	fl.StringVar(&token, "token", "", "Bearer token printed by install.sh (required)")
	return cmd
}

// runAdopt registers an existing Base in ~/.ownbase/config and verifies SSH
// connectivity before saving.
func runAdopt(name, host, sshUser, sshKey string, sshPort, apiPort int, token string) error {
	if host == "" {
		return fmt.Errorf("--host is required")
	}
	if token == "" {
		return fmt.Errorf("--token is required\n  The token was printed at install time; run `sudo cat /opt/ownbase/api-token` on the Base to retrieve it")
	}

	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("locate config: %w", err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	profile := serverconfig.ServerProfile{
		Host:    host,
		SSHUser: sshUser,
		SSHKey:  sshKey,
		SSHPort: sshPort,
		APIPort: apiPort,
		Token:   token,
	}

	// Verify SSH connectivity before saving.
	fmt.Fprintf(os.Stderr, "ownbasectl: verifying SSH connection to %s@%s ...\n", sshUser, host)
	out, err := tunnel.RunCommand(host, sshUser, profile.EffectiveSSHKey(), "hostname", sshPort)
	if err != nil {
		return fmt.Errorf("SSH connection to %s failed: %w\n  Check that the host is reachable and your SSH key is authorized", host, err)
	}
	fmt.Fprintf(os.Stderr, "ownbasectl: connected to %s (hostname: %s)\n", host, out)

	cfg.Servers[name] = profile
	if err := serverconfig.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Base %q adopted.\n", name)
	fmt.Printf("  Run: ownbasectl status %s\n", name)
	return nil
}
