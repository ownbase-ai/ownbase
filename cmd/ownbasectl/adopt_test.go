package main

// Tier-1 tests for runAdopt, using the in-process SSH server from
// sshserver_test.go. The property under test throughout is the one Bugbot
// flagged: adopt must not write anything to the vault until the connectivity
// check succeeds, so a mistyped host or an unauthorized key can never cost a
// Base its previously working profile or owner key.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ownbase/ownbase/internal/vault"
)

func TestRunAdopt_Succeeds(t *testing.T) {
	startTestAgent(t)

	privPEM, _, clientPub := newTestOwnerKey(t)
	sshSrv := startTestSSHServer(t, clientPub)
	keyFile := writeKeyFile(t, privPEM)

	if err := runAdopt("mybase", "127.0.0.1", "testuser", keyFile, sshSrv.port(), 7070, "test-token"); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	p, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if p.Host != "127.0.0.1" || p.SSHUser != "testuser" || p.Token != "test-token" {
		t.Errorf("profile not stored correctly: %+v", p)
	}
	if p.PublicKeyLine() == "" {
		t.Error("adopted Base has no owner key recorded")
	}
}

// Adopting without --ssh-key must work against a key the Base already has —
// the connectivity check has to go through the agent rather than requiring
// key material the CLI process never holds.
func TestRunAdopt_WithoutSSHKeyUsesExistingVaultKey(t *testing.T) {
	startTestAgent(t)

	if err := runKeygen("mybase", "", true); err != nil {
		t.Fatal(err)
	}
	p, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(p.PublicKeyLine()))
	if err != nil {
		t.Fatalf("parse stored public key: %v", err)
	}
	sshSrv := startTestSSHServer(t, pubKey)

	if err := runAdopt("mybase", "127.0.0.1", "testuser", "", sshSrv.port(), 7070, "tok"); err != nil {
		t.Fatalf("adopt without --ssh-key: %v", err)
	}
	after, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if after.Host != "127.0.0.1" || after.Token != "tok" {
		t.Errorf("profile not updated: %+v", after)
	}
}

// The regression this file exists for: a failed connectivity check — wrong
// host, wrong port, unauthorized key — must leave a previously adopted Base
// exactly as it was, key included.
func TestRunAdopt_FailedVerificationDoesNotOverwriteExistingProfile(t *testing.T) {
	startTestAgent(t)

	privPEM, _, clientPub := newTestOwnerKey(t)
	sshSrv := startTestSSHServer(t, clientPub)
	keyFile := writeKeyFile(t, privPEM)
	if err := runAdopt("mybase", "127.0.0.1", "testuser", keyFile, sshSrv.port(), 7070, "original-token"); err != nil {
		t.Fatalf("initial adopt: %v", err)
	}
	before, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}

	// A different, otherwise-valid key, aimed at a port nothing is listening
	// on — the check must fail before any of this reaches the vault.
	otherPEM, _, _ := newTestOwnerKey(t)
	otherKeyFile := writeKeyFile(t, otherPEM)
	deadPort := closedLocalPort(t)

	err = runAdopt("mybase", "127.0.0.1", "testuser", otherKeyFile, deadPort, 7070, "attacker-token")
	if err == nil {
		t.Fatal("expected adopt to fail against an unreachable port")
	}

	after, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if after.Host != before.Host || after.Token != before.Token || after.SSHPort != before.SSHPort {
		t.Errorf("failed adopt overwrote the profile: before %+v, after %+v", before, after)
	}
	if !vault.SameAuthorizedKey(after.PublicKeyLine(), before.PublicKeyLine()) {
		t.Error("failed adopt overwrote the owner key")
	}
}

// A host that rejects the offered key must fail the same way — not just an
// unreachable port.
func TestRunAdopt_UnauthorizedKeyDoesNotOverwriteExistingProfile(t *testing.T) {
	startTestAgent(t)

	privPEM, _, clientPub := newTestOwnerKey(t)
	sshSrv := startTestSSHServer(t, clientPub)
	keyFile := writeKeyFile(t, privPEM)
	if err := runAdopt("mybase", "127.0.0.1", "testuser", keyFile, sshSrv.port(), 7070, "original-token"); err != nil {
		t.Fatalf("initial adopt: %v", err)
	}
	before, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}

	// A key this server does not authorize, aimed at the real (reachable) port.
	unauthorizedPEM, _, _ := newTestOwnerKey(t)
	unauthorizedKeyFile := writeKeyFile(t, unauthorizedPEM)

	err = runAdopt("mybase", "127.0.0.1", "testuser", unauthorizedKeyFile, sshSrv.port(), 7070, "attacker-token")
	if err == nil {
		t.Fatal("expected adopt to fail with an unauthorized key")
	}

	after, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if !vault.SameAuthorizedKey(after.PublicKeyLine(), before.PublicKeyLine()) {
		t.Error("failed adopt overwrote the owner key")
	}
	if after.Token != before.Token {
		t.Error("failed adopt overwrote the token")
	}
}

// adopt must not require the token to be pasted in when SSH can read it
// itself — the same bootstrap connectToServer already does for any profile
// with no cached token, just performed eagerly instead of on first use.
func TestRunAdopt_FetchesTokenAutomatically(t *testing.T) {
	// The in-process SSH server runs commands through `sh -c`, so a stub sudo
	// earlier in PATH serves a fake api-token file, exactly like
	// TestConnectToServer_BootstrapsTokenViaSSH does.
	tokenFile := filepath.Join(t.TempDir(), "api-token")
	if err := os.WriteFile(tokenFile, []byte("bootstrapped-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "sudo"),
		[]byte("#!/bin/sh\ncat \""+tokenFile+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	startTestAgent(t)
	privPEM, _, clientPub := newTestOwnerKey(t)
	sshSrv := startTestSSHServer(t, clientPub)
	keyFile := writeKeyFile(t, privPEM)

	if err := runAdopt("mybase", "127.0.0.1", "testuser", keyFile, sshSrv.port(), 7070, ""); err != nil {
		t.Fatalf("adopt without --token: %v", err)
	}

	p, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if p.Token != "bootstrapped-token" {
		t.Errorf("token = %q, want bootstrapped-token", p.Token)
	}
}

// When SSH cannot read the token file (no sudo, non-standard setup, whatever
// the reason), adopt must still succeed rather than force the operator to go
// find the token by hand — the first real command against this Base
// bootstraps it exactly the same way connectToServer always has.
func TestRunAdopt_SucceedsWithoutTokenWhenFetchFails(t *testing.T) {
	// A "sudo" that always fails, ahead of the real one on PATH: the fallback
	// `cat` then fails too, since /opt/ownbase/api-token genuinely does not
	// exist on the machine running this test. That gives a deterministic
	// failure without depending on whatever the real sudo on this machine
	// would do (which, run non-interactively, could otherwise hang asking for
	// a password on some systems). Everything else `hostname` and the shell
	// itself need stays on PATH, so the earlier SSH verification is unaffected.
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "sudo"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	startTestAgent(t)
	privPEM, _, clientPub := newTestOwnerKey(t)
	sshSrv := startTestSSHServer(t, clientPub)
	keyFile := writeKeyFile(t, privPEM)

	if err := runAdopt("mybase", "127.0.0.1", "testuser", keyFile, sshSrv.port(), 7070, ""); err != nil {
		t.Fatalf("adopt without --token and without a way to fetch one: %v", err)
	}

	p, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if p.Token != "" {
		t.Errorf("token = %q, want empty (no way to have fetched one)", p.Token)
	}
	if p.Host != "127.0.0.1" {
		t.Errorf("host = %q, want 127.0.0.1 — adopt must still register the Base", p.Host)
	}
}

func TestRunAdopt_NoKeyAnywhereFails(t *testing.T) {
	startTestAgent(t)

	err := runAdopt("mybase", "127.0.0.1", "testuser", "", 22, 7070, "tok")
	if err == nil {
		t.Fatal("expected an error when no key is available")
	}
	if code := exitCodeFor(err); code != exitPreflight {
		t.Errorf("exit code = %d, want %d", code, exitPreflight)
	}
}

func writeKeyFile(t *testing.T, privPEM string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, []byte(privPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// closedLocalPort returns a port nothing is listening on, by opening a
// listener and closing it before returning — connecting to it fails fast with
// "connection refused" rather than waiting out the dial timeout an
// unreachable IP would cost.
func closedLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscan(portStr, &port); err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	return port
}
