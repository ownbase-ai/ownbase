package main

// Tier-1 tests for connectToServer.
//
// Error-path tests (no SSH needed) verify that a missing or half-filled profile
// returns a clear error. The happy-path tests use the in-process SSH server from
// sshserver_test.go plus a mock daemon HTTP server to exercise the full
// vault -> agent -> ssh-agent signing -> tunnel flow, including the token
// bootstrap that writes back to the vault.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/vault"
)

// ---------------------------------------------------------------------------
// Error paths (no SSH required)
// ---------------------------------------------------------------------------

func TestConnectToServer_NoNameGiven(t *testing.T) {
	startTestAgent(t)

	if _, err := connectToServer(""); err == nil {
		t.Fatal("expected error when no Base name is given, got nil")
	}
}

func TestConnectToServer_ServerNotFound(t *testing.T) {
	startTestAgent(t)
	putTestProfile(t, "prod", vault.Profile{Host: "prod.example.com", Token: "abc"})

	if _, err := connectToServer("nonexistent"); err == nil {
		t.Fatal("expected error for unknown server name, got nil")
	}
}

func TestConnectToServer_NoHostInProfile(t *testing.T) {
	startTestAgent(t)
	putTestProfile(t, "empty", vault.Profile{Token: "abc"})

	_, err := connectToServer("empty")
	if err == nil {
		t.Fatal("expected error for profile with no host, got nil")
	}
	// The useful thing to say is which command fills the gap in.
	if !strings.Contains(err.Error(), "create") {
		t.Errorf("error should point at create, got: %v", err)
	}
}

// A locked vault must produce its own exit code and a pointer at the unlock
// command, not a socket-level error an unattended caller cannot act on.
func TestConnectToServer_LockedVault(t *testing.T) {
	c := startTestAgent(t)
	putTestProfile(t, "mybase", vault.Profile{Host: "127.0.0.1", Token: "tok"})
	if err := c.Lock(); err != nil {
		t.Fatalf("lock: %v", err)
	}

	_, err := connectToServer("mybase")
	if err == nil {
		t.Fatal("expected an error with the vault locked")
	}
	if code := exitCodeFor(err); code != exitLocked {
		t.Errorf("exit code = %d, want %d", code, exitLocked)
	}
	if !strings.Contains(err.Error(), "vault unlock") {
		t.Errorf("error should point at 'vault unlock', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Happy path: full tunnel + token-from-profile flow
// ---------------------------------------------------------------------------

func TestConnectToServer_EstablishesTunnelAndReturnsConnection(t *testing.T) {
	// Start a mock daemon HTTP server.
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"schema_version": "v1"})
	}))
	defer daemon.Close()

	startTestAgent(t)

	privPEM, pubLine, clientPub := newTestOwnerKey(t)
	sshSrv := startTestSSHServer(t, clientPub)

	putTestProfile(t, "local", vault.Profile{
		Host:       "127.0.0.1",
		SSHUser:    "testuser",
		SSHPort:    sshSrv.port(),
		APIPort:    listenPort(t, daemon.Listener.Addr().String()),
		Token:      "test-token",
		PrivateKey: privPEM,
		PublicKey:  pubLine,
	})

	conn, err := connectToServer("local")
	if err != nil {
		t.Fatalf("connectToServer: %v", err)
	}
	defer conn.close()

	if conn.token != "test-token" {
		t.Errorf("token = %q, want test-token", conn.token)
	}
	if conn.baseURL == "" {
		t.Error("baseURL is empty")
	}
	if conn.tun == nil {
		t.Error("tun is nil; expected an active SSH tunnel")
	}

	body, err := apiGet(conn, "/")
	if err != nil {
		t.Fatalf("apiGet through tunnel: %v", err)
	}
	if len(body) == 0 {
		t.Error("expected non-empty response body")
	}
}

// ---------------------------------------------------------------------------
// Token bootstrap: profile has no token → fetched over SSH and saved
// ---------------------------------------------------------------------------

func TestConnectToServer_BootstrapsTokenViaSSH(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	}))
	defer daemon.Close()

	// The in-process SSH server runs commands through `sh -c`, so a stub sudo
	// earlier in PATH is enough to serve a fake api-token file.
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

	privPEM, pubLine, clientPub := newTestOwnerKey(t)
	sshSrv := startTestSSHServer(t, clientPub)

	putTestProfile(t, "local", vault.Profile{
		Host:       "127.0.0.1",
		SSHUser:    "testuser",
		SSHPort:    sshSrv.port(),
		APIPort:    listenPort(t, daemon.Listener.Addr().String()),
		PrivateKey: privPEM,
		PublicKey:  pubLine,
	})

	conn, err := connectToServer("local")
	if err != nil {
		t.Fatalf("connectToServer: %v", err)
	}
	defer conn.close()

	if conn.token != "bootstrapped-token" {
		t.Errorf("token = %q, want bootstrapped-token", conn.token)
	}

	// The token must also be persisted, so the next command does not repeat
	// the SSH round trip.
	stored, err := loadProfile("local")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Token != "bootstrapped-token" {
		t.Errorf("persisted token = %q, want bootstrapped-token", stored.Token)
	}
}

func listenPort(t *testing.T, addr string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscan(portStr, &port); err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	return port
}
