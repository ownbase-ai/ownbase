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

// adoptOpts is the resolved adopt command line. The *Set fields record whether
// the operator passed the corresponding flag: an existing profile must keep
// its SSHUser/SSHPort/APIPort/Token when those flags are omitted, otherwise
// `adopt --host <new-ip>` (the documented Multipass IP-update flow) forces
// root/22 and can wipe a working token.
type adoptOpts struct {
	Host       string
	SSHUser    string
	SSHUserSet bool
	SSHKey     string
	SSHPort    int
	SSHPortSet bool
	APIPort    int
	APIPortSet bool
	Token      string
	TokenSet   bool
}

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
this command is only needed to connect to an already-installed Base.

Re-running adopt against a Base already in the vault only changes the
fields you pass. Omitting --ssh-user / --ssh-port / --api-port / --token
leaves those vault values alone — so updating a Multipass IP is just
'adopt <name> --host <new-ip>'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl := cmd.Flags()
			return runAdopt(args[0], adoptOpts{
				Host:       host,
				SSHUser:    sshUser,
				SSHUserSet: fl.Changed("ssh-user"),
				SSHKey:     sshKey,
				SSHPort:    sshPort,
				SSHPortSet: fl.Changed("ssh-port"),
				APIPort:    apiPort,
				APIPortSet: fl.Changed("api-port"),
				Token:      token,
				TokenSet:   fl.Changed("token"),
			})
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
func runAdopt(name string, opts adoptOpts) error {
	if opts.Host == "" {
		return fmt.Errorf("--host is required")
	}

	profile, err := loadProfile(name)
	existed := err == nil
	if err != nil && !isMissingBase(err) {
		return err
	}

	// Host is always required and always applied. Everything else only
	// overwrites an existing profile when the flag was passed explicitly —
	// defaults must not clobber a working ubuntu/token entry just because
	// the operator is updating the IP.
	profile.Host = opts.Host
	if !existed || opts.SSHUserSet {
		profile.SSHUser = opts.SSHUser
	}
	if !existed || opts.SSHPortSet {
		profile.SSHPort = opts.SSHPort
	}
	if !existed || opts.APIPortSet {
		profile.APIPort = opts.APIPort
	}
	// Fresh adopt with no flag defaults: cobra supplies root/22/7070 via the
	// opts values above. When those fields are still empty on a brand-new
	// profile (tests pass zero values), fall through to Effective* at dial time.

	// A signer to verify with, resolved without writing anything to the vault
	// yet: a mistyped host or an unauthorized key must not cost the Base its
	// previously working profile or owner key, and nothing here is undoable
	// once it lands in the vault.
	var verifySigner ssh.Signer
	if opts.SSHKey != "" {
		priv, pub, ierr := readPrivateKeyFile(opts.SSHKey)
		if ierr != nil {
			return ierr
		}
		verifySigner, ierr = ssh.ParsePrivateKey([]byte(priv))
		if ierr != nil {
			return fmt.Errorf("parse %s: %w", opts.SSHKey, ierr)
		}
		profile.PrivateKey, profile.PublicKey = priv, pub
	}
	if profile.PublicKeyLine() == "" {
		return withExitCode(exitPreflight, fmt.Errorf(
			"no owner key in the vault for Base %q — adopt cannot reach %s without one.\n"+
				"       Import the key that machine already authorizes: ownbasectl keygen %s --import ~/.ssh/<key>",
			name, opts.Host, name))
	}

	sshUser := profile.EffectiveSSHUser()
	sshPort := profile.EffectiveSSHPort()
	target := tunnel.Target{Host: opts.Host, User: sshUser, Port: sshPort}
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
		return fmt.Errorf("SSH connection to %s failed: %w\n  Check that the host is reachable and this Base's owner key is authorized on it", opts.Host, err)
	}
	fmt.Fprintf(os.Stderr, "ownbasectl: connected to %s (hostname: %s)\n", opts.Host, out)

	// Token: explicit --token wins; otherwise try to fetch only when we do not
	// already have one. Never assign the empty default over a cached token —
	// a failed fetch would otherwise drop API access on a host-only re-adopt.
	switch {
	case opts.TokenSet:
		profile.Token = opts.Token
	case profile.Token == "":
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

	// Config repo lives on the Base (/opt/ownbase/config-source.yaml). Copy it
	// into the profile now so list/the app show it without a later checkup,
	// and so client-side deploy/config mutations know where to commit.
	if url, ref := configFromTarget(target); url != "" {
		applyConfigSource(&profile, url, ref)
		fmt.Fprintf(os.Stderr, "ownbasectl: config repo %s (%s)\n", url, profile.EffectiveConfigRef())
	}

	if err := putProfile(name, profile); err != nil {
		return err
	}

	fmt.Printf("Base %q adopted.\n", name)
	fmt.Printf("  Run: ownbasectl status %s\n", name)
	return nil
}
