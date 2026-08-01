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
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

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

// testTarget points at the in-process SSH server, authenticating with an
// explicit key file rather than the credential agent.
func testTarget(keyPath string, sshPort int) tunnel.Target {
	return tunnel.Target{Host: "127.0.0.1", User: "testuser", Port: sshPort, KeyPath: keyPath}
}

// overrideHome sets HOME to a temp dir for the duration of the test so that
// the tunnel package creates a fresh ~/.ownbase/known_hosts (TOFU path).
// SSH_AUTH_SOCK is cleared too, so a developer's real ssh-agent cannot inject
// keys into the auth attempt and change what the test exercises.
func overrideHome(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("SSH_AUTH_SOCK", "")
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
	tun, err := tunnel.Open(testTarget(keyPath, sshPort), agentPort)
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

	out, err := tunnel.RunCommand(testTarget(keyPath, sshPort), "echo hello-world")
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

	out, err := tunnel.RunCommand(testTarget(keyPath, sshPort), "printf '%s' test-value")
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

	tun, err := tunnel.Open(testTarget(keyPath, sshPort), agentPort)
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

// agentSockSeq makes startTestAgent socket names unique across parallel tests.
// A pid+time stamp alone collided under go test -parallel and left Dial with
// total=0 (unix dial failed → no auth methods) on CI.
var agentSockSeq atomic.Int64

// startTestAgent serves an ssh-agent keyring on a unix socket under t.TempDir
// and tracks how many client connections are currently open / have ever been
// accepted. Used to prove Dial closes the agent FD once the handshake
// finishes (Bugbot: agent sockets never closed).
func startTestAgent(t *testing.T, priv any) (sockPath string, live, total *atomic.Int32) {
	t.Helper()
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatalf("agent add: %v", err)
	}

	// macOS caps sun_path at ~104 bytes; t.TempDir() is already too long, so
	// park the socket under /tmp with a short unique name.
	sockPath = filepath.Join(os.TempDir(), fmt.Sprintf("ob-tunn-%d-%d.sock", os.Getpid(), agentSockSeq.Add(1)))
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen agent: %v", err)
	}
	t.Cleanup(func() {
		ln.Close()
		_ = os.Remove(sockPath)
	})

	live = &atomic.Int32{}
	total = &atomic.Int32{}
	ready := make(chan struct{})
	go func() {
		close(ready)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			total.Add(1)
			live.Add(1)
			go func(c net.Conn) {
				defer live.Add(-1)
				defer c.Close()
				_ = agent.ServeAgent(keyring, c)
			}(conn)
		}
	}()
	<-ready
	return sockPath, live, total
}

// waitForLive spins until the agent reports want concurrent connections, or
// the test times out. Agent.ServeAgent only notices a client Close after a
// read returns, so give it a moment after Dial returns.
func waitForLive(t *testing.T, live *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if live.Load() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("agent live connections = %d, want %d", live.Load(), want)
}

// Dial used to leave every agent unix conn open for the process lifetime.
// After a successful handshake the agent is no longer needed (rekey does not
// re-auth), so the FD must be released — otherwise create --wait and similar
// loops exhaust the process limit.
func TestDial_ClosesAgentSocketAfterHandshake(t *testing.T) {
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	clientPub, err := ssh.NewPublicKey(edPub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}

	sockPath, live, total := startTestAgent(t, edPriv)
	sshSrv := startTestSSHServer(t, clientPub)
	_, sshPortStr, _ := net.SplitHostPort(sshSrv.addr())
	var sshPort int
	_, _ = fmt.Sscan(sshPortStr, &sshPort)

	overrideHome(t)

	// Point both AgentSocket and SSH_AUTH_SOCK at the same path — the
	// documented setup — and confirm we only open one connection, not two.
	t.Setenv("SSH_AUTH_SOCK", sockPath)

	client, err := tunnel.Dial(tunnel.Target{
		Host:        "127.0.0.1",
		User:        "testuser",
		Port:        sshPort,
		AgentSocket: sockPath,
		PublicKey:   string(ssh.MarshalAuthorizedKey(clientPub)),
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	// Signers are held only for the handshake; agent conn must already be gone
	// even while the SSH client is still open.
	waitForLive(t, live, 0)
	if n := total.Load(); n != 1 {
		t.Errorf("agent accepted %d connections, want 1 (dedup AgentSocket + SSH_AUTH_SOCK)", n)
	}
	_ = client.Close()
}

// A dial that never reaches a server still opened the agent socket to build
// auth methods; that path must clean up too, or a tight failure loop leaks.
func TestDial_ClosesAgentSocketOnDialFailure(t *testing.T) {
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sockPath, live, total := startTestAgent(t, edPriv)
	overrideHome(t)
	t.Setenv("SSH_AUTH_SOCK", "")

	// Closed port: TCP fails fast, before auth runs.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	_, err = tunnel.Dial(tunnel.Target{
		Host:        "127.0.0.1",
		User:        "testuser",
		Port:        port,
		AgentSocket: sockPath,
	})
	if err == nil {
		t.Fatal("Dial: expected connection error, got nil")
	}
	// buildAuthMethods dials the agent before TCP; if that unix dial failed we
	// never opened a connection to clean up — fail with a clearer signal than
	// "want 1".
	if n := total.Load(); n == 0 {
		t.Fatalf("agent accepted 0 connections; Dial failed before opening the agent (%v)", err)
	}
	waitForLive(t, live, 0)
	if n := total.Load(); n != 1 {
		t.Errorf("agent accepted %d connections, want 1", n)
	}
}
