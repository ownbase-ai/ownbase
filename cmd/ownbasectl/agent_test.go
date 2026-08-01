package main

// agent_test.go gives the CLI tests a real credential agent.
//
// Rather than injecting a fake profile store, each test runs an actual
// agentd.Server in-process against a throwaway vault under a temp HOME. That
// keeps the production code free of test hooks, and it means the tests exercise
// the same socket protocol and ssh-agent signing path that ships.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ownbase/ownbase/internal/agentd"
	"github.com/ownbase/ownbase/internal/vault"
)

const testVaultPassword = "test-master-password"

// startTestAgent points HOME at a temp dir, creates and unlocks a vault there,
// and serves a credential agent for the duration of the test.
func startTestAgent(t *testing.T) *agentd.Client {
	t.Helper()

	// Not t.TempDir(): it names the directory after the test, and a table-
	// driven subtest name easily pushes ~/.ownbase/agent.sock past the
	// ~104-character limit the OS puts on unix socket paths.
	home, err := os.MkdirTemp("", "ob")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv(vault.PathEnv, "")
	// A developer's own ssh-agent must not be able to satisfy an auth attempt
	// the test expects to go through the vault.
	t.Setenv("SSH_AUTH_SOCK", "")

	vaultPath := filepath.Join(home, "test-vault.kdbx")
	if _, err := vault.RecordPath(vaultPath); err != nil {
		t.Fatalf("record vault path: %v", err)
	}
	if _, err := vault.Create(vaultPath, testVaultPassword); err != nil {
		t.Fatalf("create vault: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var serveErr error
	served := make(chan struct{})
	go func() {
		defer close(served)
		serveErr = agentd.NewServer("test").Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-served
	})

	c, err := agentd.NewClient()
	if err != nil {
		t.Fatalf("agent client: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, serr := c.Status()
		if serr == nil && st.Running {
			break
		}
		// Serve returning early is the interesting failure; reporting it
		// beats waiting out the deadline with a useless message.
		select {
		case <-served:
			t.Fatalf("test agent stopped before it was ready: %v", serveErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the test agent did not start within 10s (last status error: %v)", serr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := c.Unlock(vaultPath, testVaultPassword, 0); err != nil {
		t.Fatalf("unlock test vault: %v", err)
	}
	return c
}

// newTestOwnerKey generates an owner keypair the way keygen does, returning the
// vault fields plus the public key a test SSH server should authorize.
func newTestOwnerKey(t *testing.T) (privPEM, pubLine string, pub ssh.PublicKey) {
	t.Helper()
	privPEM, pubLine, err := vault.NewKeyPair("test")
	if err != nil {
		t.Fatalf("NewKeyPair: %v", err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(privPEM))
	if err != nil {
		t.Fatalf("parse generated key: %v", err)
	}
	return privPEM, pubLine, signer.PublicKey()
}

// putTestProfile stores a profile directly, failing the test on error.
func putTestProfile(t *testing.T, name string, p vault.Profile) {
	t.Helper()
	if err := putProfile(name, p); err != nil {
		t.Fatalf("putProfile(%q): %v", name, err)
	}
}
