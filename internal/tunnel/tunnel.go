// Package tunnel is ownbasectl's SSH transport to a Base: port forwards for
// the daemon API, one-shot commands, file uploads, and interactive shells.
//
// The daemon always binds 127.0.0.1:7070, so there is no public API to reach —
// everything goes through the encrypted SSH channel. This is a pure-Go client
// (golang.org/x/crypto/ssh), not a wrapper around the ssh binary, so its
// behaviour does not depend on the user's ~/.ssh/config.
//
// Authentication comes from a Target. In normal operation the owner key lives
// in the vault and is served by the credential agent (internal/agentd), so
// Target carries a socket path and a public key to select, not a private key:
//
//	t := tunnel.Target{
//		Host: "mybase.example.com", User: "root",
//		AgentSocket: "/Users/me/.ownbase/ssh-agent.sock",
//		PublicKey: "ssh-ed25519 AAAA... ownbase_mybase",
//	}
//	tun, err := tunnel.Open(t, 7070)
//	defer tun.Close()
//	resp, _ := http.Get("http://" + tun.LocalAddr() + "/status")
package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// dialTimeout bounds the TCP+handshake phase of reaching a Base.
const dialTimeout = 15 * time.Second

// Target describes one Base and how to authenticate to it.
type Target struct {
	// Host is the hostname or IP address. Never a "user@host" string —
	// dialing uses it verbatim.
	Host string
	// User is the SSH login user.
	User string
	// Port is the SSH port; 0 means 22.
	Port int

	// AgentSocket is a unix socket speaking the ssh-agent protocol —
	// normally the OwnBase credential agent's. Preferred, because it means
	// no private key is ever in this process.
	AgentSocket string
	// PublicKey selects which of the agent's keys to offer, as an
	// authorized_keys line. Without it every key the agent holds is tried,
	// which risks tripping a server's MaxAuthTries once a user has several
	// Bases.
	PublicKey string
	// KeyPath is an explicit private key file, for the case where the user
	// insists on one (--ssh-key) rather than the vault.
	KeyPath string
	// Signers are in-memory keys, used by tests and by the agent itself.
	Signers []ssh.Signer
}

// Addr returns the "host:port" this target dials.
func (t Target) Addr() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(t.Host, fmt.Sprintf("%d", port))
}

// Destination returns the "user@host" form, for messages that show the user an
// SSH invocation they could run themselves.
func (t Target) Destination() string { return t.User + "@" + t.Host }

// Tunnel is an active SSH port-forward. Call Close when done.
type Tunnel struct {
	sshClient *ssh.Client
	listener  net.Listener
}

// Dial opens an SSH connection to the target. Callers that need more than a
// port forward (an interactive shell, several sessions) use this directly and
// close the client themselves.
func Dial(t Target) (*ssh.Client, error) {
	authMethods, err := buildAuthMethods(t)
	if err != nil {
		return nil, fmt.Errorf("ssh auth: %w", err)
	}
	hostKeyCallback, err := buildHostKeyCallback(t.Host)
	if err != nil {
		return nil, fmt.Errorf("ssh known_hosts: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         dialTimeout,
	}
	client, err := ssh.Dial("tcp", t.Addr(), cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s@%s: %w", t.User, t.Addr(), err)
	}
	return client, nil
}

// Open dials the target and forwards a random local loopback port through the
// SSH channel to 127.0.0.1:remotePort on the Base.
//
// Host key verification uses ~/.ownbase/known_hosts with TOFU on first
// connect: the host key is added and a notice is printed.
func Open(t Target, remotePort int) (*Tunnel, error) {
	sshClient, err := Dial(t)
	if err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("local listener: %w", err)
	}

	tun := &Tunnel{sshClient: sshClient, listener: listener}
	go tun.serve(remotePort)
	return tun, nil
}

// LocalAddr returns the "host:port" the tunnel listens on locally. Use it to
// build the base URL for HTTP requests through the tunnel.
func (t *Tunnel) LocalAddr() string { return t.listener.Addr().String() }

// Close tears down the local listener and the underlying SSH connection.
func (t *Tunnel) Close() error {
	// Close the listener first to stop the accept loop.
	lerr := t.listener.Close()
	cerr := t.sshClient.Close()
	if lerr != nil {
		return lerr
	}
	return cerr
}

// RunCommand executes cmd on the Base and returns its combined stdout+stderr,
// trimmed. Used for one-off operations such as reading the API token.
func RunCommand(t Target, cmd string) (string, error) {
	client, err := Dial(t)
	if err != nil {
		return "", err
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer sess.Close()

	out, err := sess.CombinedOutput(cmd)
	return strings.TrimSpace(string(out)), err
}

// UploadFile writes data to remotePath on the Base using `sudo install`, so
// the destination can live in a root-owned location without a separate
// scp/sftp dependency. mode is the octal file permission (e.g. 0o755).
func UploadFile(t Target, data []byte, remotePath string, mode os.FileMode) error {
	client, err := Dial(t)
	if err != nil {
		return err
	}
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("ssh session: %w", err)
	}
	defer sess.Close()

	sess.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	sess.Stderr = &stderr

	cmd := fmt.Sprintf("sudo install -m %#o /dev/stdin %s", mode, remotePath)
	if err := sess.Run(cmd); err != nil {
		return fmt.Errorf("upload %s: %w\n%s", remotePath, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// serve accepts local connections and forwards each to remotePort via the SSH
// channel. Runs until the listener is closed.
func (t *Tunnel) serve(remotePort int) {
	remoteAddr := fmt.Sprintf("127.0.0.1:%d", remotePort)
	for {
		local, err := t.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go t.forward(local, remoteAddr)
	}
}

func (t *Tunnel) forward(local net.Conn, remoteAddr string) {
	defer local.Close()
	remote, err := t.sshClient.Dial("tcp", remoteAddr)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
	go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
	<-done
}

// buildAuthMethods returns the SSH auth methods to try, in order:
//  1. Signers handed in directly.
//  2. The private key at KeyPath.
//  3. The agent at AgentSocket (the OwnBase credential agent).
//  4. Whatever SSH_AUTH_SOCK points at.
func buildAuthMethods(t Target) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if len(t.Signers) > 0 {
		methods = append(methods, ssh.PublicKeys(t.Signers...))
	}
	if t.KeyPath != "" {
		signer, err := loadPrivateKey(t.KeyPath)
		if err != nil {
			return nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	for _, sock := range []string{t.AgentSocket, os.Getenv("SSH_AUTH_SOCK")} {
		if sock == "" {
			continue
		}
		conn, err := net.Dial("unix", sock)
		if err != nil {
			continue
		}
		client := agent.NewClient(conn)
		wanted := t.PublicKey
		methods = append(methods, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			signers, err := client.Signers()
			if err != nil {
				return nil, err
			}
			return selectSigners(signers, wanted), nil
		}))
	}

	if len(methods) == 0 {
		return nil, errors.New("no SSH authentication available: unlock the vault with 'ownbasectl vault unlock', or pass --ssh-key")
	}
	return methods, nil
}

// selectSigners narrows an agent's keys to the one the target names. An
// unparseable or absent wanted key falls back to offering everything, since
// refusing to try at all would be worse than one extra auth attempt.
func selectSigners(signers []ssh.Signer, wanted string) []ssh.Signer {
	if strings.TrimSpace(wanted) == "" {
		return signers
	}
	want, _, _, _, err := ssh.ParseAuthorizedKey([]byte(wanted))
	if err != nil {
		return signers
	}
	marshalled := string(want.Marshal())
	for _, s := range signers {
		if string(s.PublicKey().Marshal()) == marshalled {
			return []ssh.Signer{s}
		}
	}
	return signers
}

func loadPrivateKey(keyPath string) (ssh.Signer, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read SSH key %s: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse SSH key %s: %w", keyPath, err)
	}
	return signer, nil
}

// buildHostKeyCallback returns a host key verifier backed by
// ~/.ownbase/known_hosts with TOFU semantics:
//   - Unknown host (not yet in the file): key is appended and a notice is
//     printed. This covers both the first-ever connect and re-provisioned
//     servers that appear at a new IP address.
//   - Known host with matching key: accepted silently.
//   - Known host with a DIFFERENT key: rejected (possible MITM).
func buildHostKeyCallback(host string) (ssh.HostKeyCallback, error) {
	khPath, err := knownHostsPath()
	if err != nil {
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // fallback when home dir unavailable
	}

	if err := os.MkdirAll(filepath.Dir(khPath), 0o700); err != nil {
		return nil, fmt.Errorf("create ~/.ownbase: %w", err)
	}

	// Load existing entries (file may not exist yet — knownhosts.New fails on
	// a missing file, so we create it first if needed).
	if _, serr := os.Stat(khPath); os.IsNotExist(serr) {
		if f, cerr := os.OpenFile(khPath, os.O_CREATE, 0o600); cerr == nil {
			f.Close()
		}
	}

	strictCB, err := knownhosts.New(khPath)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", khPath, err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		cbErr := strictCB(hostname, remote, key)
		if cbErr == nil {
			return nil // known and matches
		}

		var keyErr *knownhosts.KeyError
		if errors.As(cbErr, &keyErr) && len(keyErr.Want) == 0 {
			// Host is not in the file at all — TOFU: append and accept.
			f, ferr := os.OpenFile(khPath, os.O_APPEND|os.O_WRONLY, 0o600)
			if ferr != nil {
				return fmt.Errorf("write known_hosts: %w", ferr)
			}
			defer f.Close()
			normalized := knownhosts.Normalize(hostname)
			line := knownhosts.Line([]string{normalized}, key)
			if _, werr := fmt.Fprintln(f, line); werr != nil {
				return fmt.Errorf("write known_hosts entry: %w", werr)
			}
			fmt.Fprintf(os.Stderr, "ownbasectl: added %s to ~/.ownbase/known_hosts\n", hostname)
			return nil
		}

		// Key mismatch for a known host — reject (possible MITM or re-keyed server).
		return fmt.Errorf("host key mismatch for %s — if you re-provisioned the server, remove the old entry from ~/.ownbase/known_hosts: %w", hostname, cbErr)
	}, nil
}

func knownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ownbase", "known_hosts"), nil
}
