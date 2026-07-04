package main

// testSSHServer is a minimal in-process SSH server shared across
// cmd/ownbasectl tests that need a real tunnel (connect_test.go).
// It handles direct-tcpip channel forwarding and session/exec requests.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

type testSSHServer struct {
	ln        net.Listener
	clientPub ssh.PublicKey
}

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

	srv := &testSSHServer{clientPub: clientPub}

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
			go handleSSHConn(conn, cfg)
		}
	}()
	return srv
}

func (s *testSSHServer) port() int {
	_, portStr, _ := net.SplitHostPort(s.ln.Addr().String())
	var p int
	_, _ = fmt.Sscan(portStr, &p)
	return p
}

func handleSSHConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		switch newChan.ChannelType() {
		case "direct-tcpip":
			go handleDirectTCPIP(newChan)
		case "session":
			go handleSessionChan(newChan)
		default:
			_ = newChan.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}

type directTCPIPPayload struct {
	DestHost string
	DestPort uint32
	SrcHost  string
	SrcPort  uint32
}

func handleDirectTCPIP(newChan ssh.NewChannel) {
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

func handleSessionChan(newChan ssh.NewChannel) {
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
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{uint32(exitCode)}))
		return
	}
}

// generateTestClientKey writes a fresh ED25519 private key to a temp file and
// returns the path and corresponding SSH public key.
func generateTestClientKey(t *testing.T) (keyPath string, pub ssh.PublicKey) {
	t.Helper()
	edPub, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
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
	clientPub, err := ssh.NewPublicKey(edPub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	return keyPath, clientPub
}
