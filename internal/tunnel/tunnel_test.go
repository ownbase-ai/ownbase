package tunnel_test

// Tier-1 tests for the tunnel package.
//
// These tests spin up a minimal in-process SSH server to exercise Open and
// RunCommand without touching any real remote host. They run on macOS and
// Linux with no special privileges.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ownbase/ownbase/internal/tunnel"
)

// ---------------------------------------------------------------------------
// In-process SSH server helper
// ---------------------------------------------------------------------------

// testSSHServer is a minimal SSH server that handles:
//   - direct-tcpip channels (for tunnel.Open forwarding)
//   - session channels with exec requests (for tunnel.RunCommand)
type testSSHServer struct {
	ln        net.Listener
	hostKey   ssh.Signer
	clientPub ssh.PublicKey
}

// startTestSSHServer starts a local SSH server on a random loopback port.
// It authenticates clients that present clientPub. Returns the server and its
// listen address.
func startTestSSHServer(t *testing.T, clientPub ssh.PublicKey) *testSSHServer {
	t.Helper()

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	srv := &testSSHServer{hostKey: hostSigner, clientPub: clientPub}

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(clientPub.Marshal()) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unauthorized key")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv.ln = ln

	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.handleConn(conn, cfg)
		}
	}()

	return srv
}

func (s *testSSHServer) addr() string { return s.ln.Addr().String() }

func (s *testSSHServer) handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "direct-tcpip":
			go s.handleDirectTCPIP(newChan)
		case "session":
			go s.handleSession(newChan)
		default:
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported channel type")
		}
	}
}

// directTCPIPPayload matches the SSH wire format for direct-tcpip extra data.
type directTCPIPPayload struct {
	DestHost string
	DestPort uint32
	SrcHost  string
	SrcPort  uint32
}

func (s *testSSHServer) handleDirectTCPIP(newChan ssh.NewChannel) {
	var payload directTCPIPPayload
	if err := ssh.Unmarshal(newChan.ExtraData(), &payload); err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}

	destAddr := net.JoinHostPort(payload.DestHost, fmt.Sprintf("%d", payload.DestPort))
	remote, err := net.Dial("tcp", destAddr)
	if err != nil {
		_ = newChan.Reject(ssh.ConnectionFailed, err.Error())
		return
	}

	ch, reqs, err := newChan.Accept()
	if err != nil {
		remote.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	defer ch.Close()
	defer remote.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, ch); done <- struct{}{} }()
	go func() { _, _ = io.Copy(ch, remote); done <- struct{}{} }()
	<-done
}

func (s *testSSHServer) handleSession(newChan ssh.NewChannel) {
	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer ch.Close()

	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}

		// exec payload is a single SSH string.
		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)

		cmd := exec.Command("sh", "-c", payload.Command)
		cmd.Stdout = ch
		cmd.Stderr = ch

		exitCode := 0
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				exitCode = ee.ExitCode()
			} else {
				exitCode = 1
			}
		}

		exitStatus := struct{ Code uint32 }{uint32(exitCode)}
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatus))
		return
	}
}

// ---------------------------------------------------------------------------
// Client key helper
// ---------------------------------------------------------------------------

// generateClientKey creates a fresh ED25519 key pair and writes the private
// key in OpenSSH PEM format to a temp file. Returns the file path and the
// corresponding public key for the server's auth check.
func generateClientKey(t *testing.T) (keyPath string, pub ssh.PublicKey) {
	t.Helper()

	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(edPriv)
	if err != nil {
		t.Fatalf("client signer: %v", err)
	}

	block, err := ssh.MarshalPrivateKey(edPriv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	keyBytes := pem.EncodeToMemory(block)

	dir := t.TempDir()
	keyPath = filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, keyBytes, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	// We also need the SSH public key for the server.
	clientPub, err := ssh.NewPublicKey(edPub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	_ = signer
	return keyPath, clientPub
}

// overrideHome sets HOME to a temp dir for the duration of the test so that
// the tunnel package creates a fresh ~/.ownbase/known_hosts (TOFU path).
func overrideHome(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestOpen_ForwardsHTTPTraffic(t *testing.T) {
	// Start a minimal HTTP server representing the on-Base agent.
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello-from-agent")
	}))
	defer agentServer.Close()

	// Parse the agent's listen port.
	_, portStr, _ := net.SplitHostPort(agentServer.Listener.Addr().String())
	var agentPort int
	_, _ = fmt.Sscan(portStr, &agentPort)

	// Generate client key pair and start SSH server.
	keyPath, clientPub := generateClientKey(t)
	sshSrv := startTestSSHServer(t, clientPub)
	_, sshPortStr, _ := net.SplitHostPort(sshSrv.addr())
	var sshPort int
	_, _ = fmt.Sscan(sshPortStr, &sshPort)

	overrideHome(t)

	// Open a tunnel through the SSH server to the agent.
	tun, err := tunnel.Open("127.0.0.1", "testuser", keyPath, agentPort, sshPort)
	if err != nil {
		t.Fatalf("tunnel.Open: %v", err)
	}
	defer tun.Close()

	// Make an HTTP request through the tunnel.
	resp, err := http.Get("http://" + tun.LocalAddr())
	if err != nil {
		t.Fatalf("GET through tunnel: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello-from-agent" {
		t.Errorf("body = %q, want hello-from-agent", body)
	}
}

func TestRunCommand_ReturnsOutput(t *testing.T) {
	keyPath, clientPub := generateClientKey(t)
	sshSrv := startTestSSHServer(t, clientPub)
	_, sshPortStr, _ := net.SplitHostPort(sshSrv.addr())
	var sshPort int
	_, _ = fmt.Sscan(sshPortStr, &sshPort)

	overrideHome(t)

	out, err := tunnel.RunCommand("127.0.0.1", "testuser", keyPath, "echo hello-world", sshPort)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if out != "hello-world" {
		t.Errorf("output = %q, want hello-world", out)
	}
}

func TestRunCommand_MultipleWords(t *testing.T) {
	keyPath, clientPub := generateClientKey(t)
	sshSrv := startTestSSHServer(t, clientPub)
	_, sshPortStr, _ := net.SplitHostPort(sshSrv.addr())
	var sshPort int
	_, _ = fmt.Sscan(sshPortStr, &sshPort)

	overrideHome(t)

	out, err := tunnel.RunCommand("127.0.0.1", "testuser", keyPath, "printf '%s' test-value", sshPort)
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if out != "test-value" {
		t.Errorf("output = %q, want test-value", out)
	}
}

func TestOpen_LocalAddrIsLoopback(t *testing.T) {
	agentServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer agentServer.Close()
	_, portStr, _ := net.SplitHostPort(agentServer.Listener.Addr().String())
	var agentPort int
	_, _ = fmt.Sscan(portStr, &agentPort)

	keyPath, clientPub := generateClientKey(t)
	sshSrv := startTestSSHServer(t, clientPub)
	_, sshPortStr, _ := net.SplitHostPort(sshSrv.addr())
	var sshPort int
	_, _ = fmt.Sscan(sshPortStr, &sshPort)

	overrideHome(t)

	tun, err := tunnel.Open("127.0.0.1", "testuser", keyPath, agentPort, sshPort)
	if err != nil {
		t.Fatalf("tunnel.Open: %v", err)
	}
	defer tun.Close()

	host, _, err := net.SplitHostPort(tun.LocalAddr())
	if err != nil {
		t.Fatalf("parse LocalAddr: %v", err)
	}
	if host != "127.0.0.1" {
		t.Errorf("LocalAddr host = %q, want 127.0.0.1", host)
	}
}
