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
	"os/exec"
	"path/filepath"
	"strconv"
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
	authMethods, cleanup, err := buildAuthMethods(t)
	if err != nil {
		return nil, fmt.Errorf("ssh auth: %w", err)
	}
	// Agent unix conns are only needed while the handshake signs; keeping them
	// open for the SSH session lifetime would leak one FD per Dial (two when
	// SSH_AUTH_SOCK also points at the OwnBase agent). Close as soon as Dial
	// returns — success or failure.
	defer cleanup()
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
//
// cleanup closes every agent unix connection opened here. The caller must
// defer it around the SSH handshake (see Dial); the methods themselves do not
// own the lifetime.
func buildAuthMethods(t Target) (methods []ssh.AuthMethod, cleanup func(), err error) {
	var conns []net.Conn
	cleanup = func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}

	if len(t.Signers) > 0 {
		methods = append(methods, ssh.PublicKeys(t.Signers...))
	}
	if t.KeyPath != "" {
		signer, err := loadPrivateKey(t.KeyPath)
		if err != nil {
			cleanup()
			return nil, func() {}, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	// Dedup so pointing SSH_AUTH_SOCK at the OwnBase agent (the documented
	// setup) does not open the same socket twice per Dial.
	seen := map[string]struct{}{}
	for _, sock := range []string{t.AgentSocket, os.Getenv("SSH_AUTH_SOCK")} {
		if sock == "" {
			continue
		}
		if _, dup := seen[sock]; dup {
			continue
		}
		seen[sock] = struct{}{}
		conn, err := net.Dial("unix", sock)
		if err != nil {
			continue
		}
		conns = append(conns, conn)
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
		cleanup()
		return nil, func() {}, errors.New("no SSH authentication available: unlock the vault with 'ownbasectl vault unlock', or pass --ssh-key")
	}
	return methods, cleanup, nil
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
//   - Unknown host (not yet in the file): every host-key algorithm the
//     server offers is recorded (via ssh-keyscan when available), not only
//     the one this handshake negotiated. That way an OpenSSH upgrade that
//     starts preferring ed25519 over rsa does not look like a MITM.
//   - Known host with matching key: accepted silently.
//   - Known host with a different key type, but every previously recorded
//     key still present on the server: missing types are appended and the
//     connection is accepted (same machine, incomplete TOFU).
//   - Known host whose recorded keys are gone from the server: rejected
//     (re-provision or MITM) — operator removes the stale known_hosts line.
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
		if !errors.As(cbErr, &keyErr) {
			return fmt.Errorf("host key check for %s: %w", hostname, cbErr)
		}

		normalized := knownhosts.Normalize(hostname)
		scanHost, scanPort := splitHostPort(hostname, remote)

		if len(keyErr.Want) == 0 {
			// Host is not in the file at all — TOFU: record every key type.
			keys := collectHostKeys(scanHost, scanPort, key)
			if err := appendHostKeys(khPath, normalized, keys); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ownbasectl: added %s to ~/.ownbase/known_hosts\n", hostname)
			return nil
		}

		// Known host, handshake offered a key we do not have on file.
		// If every key we *do* have is still on the server, this is incomplete
		// TOFU (e.g. only rsa recorded, openssh now prefers ed25519) — not a
		// re-key. Append the missing types and accept.
		scanned, scanErr := keyscanHost(scanHost, scanPort)
		if scanErr == nil && len(scanned) > 0 &&
			pubKeyInList(key, scanned) &&
			allKnownKeysStillPresent(keyErr.Want, scanned) {
			if err := appendHostKeys(khPath, normalized, scanned); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "ownbasectl: updated host keys for %s in ~/.ownbase/known_hosts\n", hostname)
			return nil
		}

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

// splitHostPort pulls the dial host and port out of the callback hostname
// (often "[ip]:22") and remote address.
func splitHostPort(hostname string, remote net.Addr) (host string, port int) {
	port = 22
	h := hostname
	if hostPart, portPart, err := net.SplitHostPort(hostname); err == nil {
		h = hostPart
		if p, err := strconv.Atoi(portPart); err == nil && p > 0 {
			port = p
		}
	} else {
		h = strings.Trim(hostname, "[]")
	}
	if remote != nil {
		if _, portPart, err := net.SplitHostPort(remote.String()); err == nil {
			if p, err := strconv.Atoi(portPart); err == nil && p > 0 {
				port = p
			}
		}
	}
	// Dial target for keyscan should be the bare host (or IP), not the
	// knownhosts-normalized form.
	return h, port
}

// collectHostKeys returns every host key the server offers, always including
// the key from this handshake. Falls back to just presented when keyscan fails.
func collectHostKeys(host string, port int, presented ssh.PublicKey) []ssh.PublicKey {
	scanned, err := keyscanHost(host, port)
	if err != nil || len(scanned) == 0 {
		return []ssh.PublicKey{presented}
	}
	if !pubKeyInList(presented, scanned) {
		scanned = append(scanned, presented)
	}
	return scanned
}

// keyscanHost runs `ssh-keyscan` for all key types. Returns nil, err when the
// binary is missing or the host does not answer — callers fall back.
func keyscanHost(host string, port int) ([]ssh.PublicKey, error) {
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	if _, err := exec.LookPath("ssh-keyscan"); err != nil {
		return nil, err
	}
	if port <= 0 {
		port = 22
	}
	// -T is connect timeout (seconds). Default keyscan tries every type.
	cmd := exec.Command("ssh-keyscan", "-T", "5", "-p", strconv.Itoa(port), host)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var keys []ssh.PublicKey
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, _, pub, _, _, err := ssh.ParseKnownHosts([]byte(line))
		if err != nil {
			continue
		}
		if !pubKeyInList(pub, keys) {
			keys = append(keys, pub)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("ssh-keyscan %s: no keys parsed", host)
	}
	return keys, nil
}

func pubKeyEqual(a, b ssh.PublicKey) bool {
	return a.Type() == b.Type() && bytes.Equal(a.Marshal(), b.Marshal())
}

func pubKeyInList(key ssh.PublicKey, list []ssh.PublicKey) bool {
	for _, k := range list {
		if pubKeyEqual(key, k) {
			return true
		}
	}
	return false
}

func allKnownKeysStillPresent(want []knownhosts.KnownKey, scanned []ssh.PublicKey) bool {
	if len(want) == 0 {
		return false
	}
	for _, w := range want {
		if w.Key == nil || !pubKeyInList(w.Key, scanned) {
			return false
		}
	}
	return true
}

// appendHostKeys writes any of keys not already present for normalized host.
func appendHostKeys(khPath, normalized string, keys []ssh.PublicKey) error {
	existing, _ := os.ReadFile(khPath)
	var have []ssh.PublicKey
	for _, line := range strings.Split(string(existing), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, hosts, pub, _, _, err := ssh.ParseKnownHosts([]byte(line))
		if err != nil {
			continue
		}
		for _, h := range hosts {
			if h == normalized || h == knownhosts.Normalize(normalized) {
				have = append(have, pub)
				break
			}
		}
	}

	f, err := os.OpenFile(khPath, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("write known_hosts: %w", err)
	}
	defer f.Close()
	for _, key := range keys {
		if pubKeyInList(key, have) {
			continue
		}
		line := knownhosts.Line([]string{normalized}, key)
		if _, err := fmt.Fprintln(f, line); err != nil {
			return fmt.Errorf("write known_hosts entry: %w", err)
		}
		have = append(have, key)
	}
	return nil
}
