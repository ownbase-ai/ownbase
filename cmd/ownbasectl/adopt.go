package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/ownbase/ownbase/internal/agentd"
	"github.com/ownbase/ownbase/internal/tunnel"
	"github.com/ownbase/ownbase/internal/vault"
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
		Use:   "adopt <name> --host <host>",
		Short: "Register an already-installed Base (verifies SSH connectivity before saving)",
		Long: `Register a Base that was installed without ownbasectl create — for
example a server someone else provisioned. The API token is fetched over
SSH automatically (it lives at /opt/ownbase/api-token on the Base); pass
--token explicitly only if SSH access can't read that file itself.

The Base needs an owner key in your vault to reach it. Either run
'ownbasectl keygen <name> --import <file>' first with the key that machine
already authorizes, or pass --ssh-key here to do the same thing.

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
	fl.StringVar(&sshKey, "ssh-key", "", "import this existing private key file into the vault as the Base's owner key")
	fl.IntVar(&sshPort, "ssh-port", 22, "SSH port on the Base")
	fl.IntVar(&apiPort, "api-port", vault.DefaultAPIPort, "agent API port on the Base")
	fl.StringVar(&token, "token", "",
		"Bearer token for the daemon API (default: fetched over SSH from /opt/ownbase/api-token)")
	return cmd
}

// runAdopt registers an existing Base in the vault and verifies SSH
// connectivity before saving.
func runAdopt(name, host, sshUser, sshKey string, sshPort, apiPort int, token string) error {
	if host == "" {
		return fmt.Errorf("--host is required")
	}

	profile, err := loadProfile(name)
	if err != nil && !isMissingBase(err) {
		return err
	}
	profile.Host = host
	profile.SSHUser = sshUser
	profile.SSHPort = sshPort
	profile.APIPort = apiPort

	// A signer to verify with, resolved without writing anything to the vault
	// yet: a mistyped host or an unauthorized key must not cost the Base its
	// previously working profile or owner key, and nothing here is undoable
	// once it lands in the vault.
	var verifySigner ssh.Signer
	if sshKey != "" {
		priv, pub, ierr := readPrivateKeyFile(sshKey)
		if ierr != nil {
			return ierr
		}
		verifySigner, ierr = ssh.ParsePrivateKey([]byte(priv))
		if ierr != nil {
			return fmt.Errorf("parse %s: %w", sshKey, ierr)
		}
		profile.PrivateKey, profile.PublicKey = priv, pub
	}
	if profile.PublicKeyLine() == "" {
		return withExitCode(exitPreflight, fmt.Errorf(
			"no owner key in the vault for Base %q — adopt cannot reach %s without one.\n"+
				"       Import the key that machine already authorizes: ownbasectl keygen %s --import ~/.ssh/<key>",
			name, host, name))
	}

	target := tunnel.Target{Host: host, User: sshUser, Port: sshPort}
	if verifySigner != nil {
		// A freshly imported key: test it directly, in memory, rather than
		// staging it into the vault first.
		target.Signers = []ssh.Signer{verifySigner}
	} else {
		// No new key offered — the Base's existing key is already in the
		// vault and unaffected either way, so testing through the agent
		// needs no write first.
		sock, serr := agentd.SSHAgentSocketPath()
		if serr != nil {
			return serr
		}
		target.AgentSocket = sock
		target.PublicKey = profile.PublicKeyLine()
	}

	fmt.Fprintf(os.Stderr, "ownbasectl: verifying SSH connection to %s ...\n", target.Destination())
	out, err := tunnel.RunCommand(target, "hostname")
	if err != nil {
		return fmt.Errorf("SSH connection to %s failed: %w\n  Check that the host is reachable and this Base's owner key is authorized on it", host, err)
	}
	fmt.Fprintf(os.Stderr, "ownbasectl: connected to %s (hostname: %s)\n", host, out)

	profile.Token = token
	if profile.Token == "" {
		// Same fetch connectToServer already does for any profile with no
		// cached token — over the same connection just verified, so there is
		// no reason to make the operator paste in what SSH can already read.
		if fetched, ferr := tunnel.RunCommand(target,
			"sudo cat /opt/ownbase/api-token 2>/dev/null || cat /opt/ownbase/api-token 2>/dev/null"); ferr == nil && fetched != "" {
			profile.Token = fetched
			fmt.Fprintln(os.Stderr, "ownbasectl: fetched the API token from the Base.")
		} else {
			// Not a failure: connectToServer bootstraps a missing token the
			// same way on first real use, so adopt succeeding without one
			// just defers that to the next command instead of blocking here.
			fmt.Fprintln(os.Stderr, "ownbasectl: could not read the API token automatically — "+
				"'ownbasectl status' will fetch it on first use, or pass --token to set it now.")
		}
	}

	if err := putProfile(name, profile); err != nil {
		return err
	}

	fmt.Printf("Base %q adopted.\n", name)
	fmt.Printf("  Run: ownbasectl status %s\n", name)
	return nil
}
