// Package tunnel opens an SSH tunnel from a local loopback port to a remote
// port, allowing ownbasectl to reach the agent API without exposing it to the
// network. The agent always binds to 127.0.0.1:7070; this package dials the
// server via SSH and forwards traffic through the encrypted channel.
//
// Usage:
//
//	tun, err := tunnel.Open("mybase.example.com", "ubuntu", "~/.ssh/id_ed25519", 7070)
//	if err != nil { ... }
//	defer tun.Close()
//	// tun.LocalAddr() is "127.0.0.1:<random-port>"
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

// Tunnel is an active SSH port-forward. Call Close when done.
type Tunnel struct {
	sshClient *ssh.Client
	listener  net.Listener
}

// Open dials host via SSH as sshUser, authenticating with the private key at
// keyPath (supports ~ expansion). It then binds a random loopback port locally
// and forwards connections through the SSH channel to 127.0.0.1:remotePort on
// the remote host.
//
// sshPort is the SSH port to connect to (0 or 22 = standard port 22).
//
// If keyPath is empty, Open falls back to the SSH agent (SSH_AUTH_SOCK).
//
// Host key verification uses ~/.ownbase/known_hosts with TOFU on first
// connect: the host key is added and a warning is printed.
func Open(host, sshUser, keyPath string, remotePort, sshPort int) (*Tunnel, error) {
	authMethods, err := buildAuthMethods(keyPath)
	if err != nil {
		return nil, fmt.Errorf("ssh auth: %w", err)
	}

	hostKeyCallback, err := buildHostKeyCallback(host)
	if err != nil {
		return nil, fmt.Errorf("ssh known_hosts: %w", err)
	}

	cfg := &ssh.ClientConfig{
		User:            sshUser,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	}

	port := sshPort
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	sshClient, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s@%s: %w", sshUser, addr, err)
	}

	// Bind a random local loopback port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("local listener: %w", err)
	}

	tun := &Tunnel{sshClient: sshClient, listener: listener}
	go tun.serve(remotePort)
	return tun, nil
}

// LocalAddr returns the "host:port" that the tunnel is listening on locally.
// Use this to build the base URL for HTTP requests through the tunnel.
func (t *Tunnel) LocalAddr() string {
	return t.listener.Addr().String()
}

// Close tears down the local listener and the underlying SSH connection.
func (t *Tunnel) Close() error {
	// Close listener first to stop the accept loop.
	lerr := t.listener.Close()
	cerr := t.sshClient.Close()
	if lerr != nil {
		return lerr
	}
	return cerr
}

// RunCommand executes cmd on the remote host via SSH and returns its combined
// stdout+stderr output. Useful for one-off operations such as reading the
// API token file during `ownbasectl adopt`.
//
// sshPort is the SSH port to connect to (0 or 22 = standard port 22).
func RunCommand(host, sshUser, keyPath, cmd string, sshPort int) (string, error) {
	authMethods, err := buildAuthMethods(keyPath)
	if err != nil {
		return "", fmt.Errorf("ssh auth: %w", err)
	}
	hostKeyCallback, err := buildHostKeyCallback(host)
	if err != nil {
		return "", fmt.Errorf("ssh known_hosts: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            sshUser,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	}
	port := sshPort
	if port == 0 {
		port = 22
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial: %w", err)
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

// UploadFile writes data to remotePath on host via SSH, using `sudo install`
// so the destination can live in a root-owned location without a separate
// scp/sftp dependency. mode is the octal file permission (e.g. 0o755).
//
// sshPort is the SSH port to connect to (0 or 22 = standard port 22).
func UploadFile(host, sshUser, keyPath string, sshPort int, data []byte, remotePath string, mode os.FileMode) error {
	authMethods, err := buildAuthMethods(keyPath)
	if err != nil {
		return fmt.Errorf("ssh auth: %w", err)
	}
	hostKeyCallback, err := buildHostKeyCallback(host)
	if err != nil {
		return fmt.Errorf("ssh known_hosts: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            sshUser,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	}
	port := sshPort
	if port == 0 {
		port = 22
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)), cfg)
	if err != nil {
		return fmt.Errorf("ssh dial: %w", err)
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

// buildAuthMethods returns the SSH auth methods to try in order:
// 1. Private key at keyPath (if non-empty and file exists).
// 2. SSH agent via SSH_AUTH_SOCK (if the socket is available).
func buildAuthMethods(keyPath string) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if keyPath != "" {
		signer, err := loadPrivateKey(keyPath)
		if err != nil {
			return nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			agentClient := agent.NewClient(conn)
			methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))
		}
	}

	if len(methods) == 0 {
		return nil, fmt.Errorf("no SSH authentication available: provide --ssh-key or set SSH_AUTH_SOCK")
	}
	return methods, nil
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
