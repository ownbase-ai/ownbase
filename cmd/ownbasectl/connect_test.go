package main

// Tier-1 tests for connectToServer.
//
// Error-path tests (no SSH needed) verify that missing/invalid config returns
// clear errors. The happy-path test uses the in-process SSH server from
// sshserver_test.go together with a mock agent HTTP server to exercise the
// full Open → token-from-profile → *connection flow.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ownbase/ownbase/internal/serverconfig"
)

// ---------------------------------------------------------------------------
// Error paths (no SSH required)
// ---------------------------------------------------------------------------

func TestConnectToServer_NoNameGiven(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := connectToServer("")
	if err == nil {
		t.Fatal("expected error when no Base name is given, got nil")
	}
}

func TestConnectToServer_ServerNotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfgDir := filepath.Join(tmp, ".ownbase")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, "config"), []byte(`
servers:
  prod:
    host: prod.example.com
    token: abc
`), 0o600)

	_, err := connectToServer("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown server name, got nil")
	}
}

func TestConnectToServer_NoHostInProfile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfgDir := filepath.Join(tmp, ".ownbase")
	_ = os.MkdirAll(cfgDir, 0o700)
	_ = os.WriteFile(filepath.Join(cfgDir, "config"), []byte(`
servers:
  empty:
    token: abc
`), 0o600)

	_, err := connectToServer("empty")
	if err == nil {
		t.Fatal("expected error for profile with no host, got nil")
	}
}

// ---------------------------------------------------------------------------
// Happy path: full tunnel + token-from-profile flow
// ---------------------------------------------------------------------------

func TestConnectToServer_EstablishesTunnelAndReturnsConnection(t *testing.T) {
	// Start a mock agent HTTP server.
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"schema_version": "v1"})
	}))
	defer agentSrv.Close()

	var agentPort int
	_, agentPortStr, _ := net.SplitHostPort(agentSrv.Listener.Addr().String())
	_, _ = fmt.Sscan(agentPortStr, &agentPort)

	// Generate client key and start SSH server.
	keyPath, clientPub := generateTestClientKey(t)
	sshSrv := startTestSSHServer(t, clientPub)

	// Point HOME at a temp dir with a server profile.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfgDir := filepath.Join(tmp, ".ownbase")
	_ = os.MkdirAll(cfgDir, 0o700)

	profile := serverconfig.ServerProfile{
		Host:    "127.0.0.1",
		SSHUser: "testuser",
		SSHKey:  keyPath,
		APIPort: agentPort,
		SSHPort: sshSrv.port(),
		Token:   "test-token",
	}
	cfg := &serverconfig.Config{
		Servers: map[string]serverconfig.ServerProfile{"local": profile},
	}
	cfgPath := filepath.Join(cfgDir, "config")
	if err := serverconfig.Save(cfgPath, cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

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

	// Verify that HTTP calls through the connection reach the mock agent.
	body, err := apiGet(conn, "/")
	if err != nil {
		t.Fatalf("apiGet through tunnel: %v", err)
	}
	if len(body) == 0 {
		t.Error("expected non-empty response body")
	}
}

// ---------------------------------------------------------------------------
// Token bootstrap: profile has no token → fetched via RunCommand
// ---------------------------------------------------------------------------

func TestConnectToServer_BootstrapsTokenViaSSH(t *testing.T) {
	// The mock agent accepts any bearer token (we just need the tunnel to open).
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	}))
	defer agentSrv.Close()

	var agentPort int
	_, agentPortStr, _ := net.SplitHostPort(agentSrv.Listener.Addr().String())
	_, _ = fmt.Sscan(agentPortStr, &agentPort)

	// The SSH server will serve the token file content via exec.
	// We set up an api-token file in a temp dir and serve it with `cat`.
	tokenDir := t.TempDir()
	tokenFile := filepath.Join(tokenDir, "api-token")
	_ = os.WriteFile(tokenFile, []byte("bootstrapped-token"), 0o600)

	keyPath, clientPub := generateTestClientKey(t)
	// Override the exec command: the SSH server runs "sh -c <cmd>"; we use a
	// script that cats a local file regardless of what path is requested.
	// We achieve this by writing a wrapper script into PATH.
	binDir := t.TempDir()
	sudoScript := filepath.Join(binDir, "sudo")
	_ = os.WriteFile(sudoScript, []byte("#!/bin/sh\ncat \""+tokenFile+"\"\n"), 0o755)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	sshSrv := startTestSSHServer(t, clientPub)

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfgDir := filepath.Join(tmp, ".ownbase")
	_ = os.MkdirAll(cfgDir, 0o700)

	// Profile has NO token — connectToServer should fetch it via SSH.
	profile := serverconfig.ServerProfile{
		Host:    "127.0.0.1",
		SSHUser: "testuser",
		SSHKey:  keyPath,
		APIPort: agentPort,
		SSHPort: sshSrv.port(),
		Token:   "", // intentionally empty
	}
	cfg := &serverconfig.Config{
		Servers: map[string]serverconfig.ServerProfile{"local": profile},
	}
	cfgPath := filepath.Join(cfgDir, "config")
	_ = serverconfig.Save(cfgPath, cfg)

	conn, err := connectToServer("local")
	if err != nil {
		t.Fatalf("connectToServer: %v", err)
	}
	defer conn.close()

	if conn.token != "bootstrapped-token" {
		t.Errorf("token = %q, want bootstrapped-token", conn.token)
	}

	// The token should also be persisted in the config file.
	updated, err := serverconfig.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if updated.Servers["local"].Token != "bootstrapped-token" {
		t.Errorf("persisted token = %q, want bootstrapped-token", updated.Servers["local"].Token)
	}
}
