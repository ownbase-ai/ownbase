//go:build integration

package main

// bootstrap_core_integration_test.go exercises the agent-level bootstrapCore
// function end-to-end: Quadlet unit installation, SIGHUP daemon-reload, and
// systemctl start. These tests require root + Linux + systemd (run on the VM
// with "sudo go test -tags=integration").
//
// The underlying Forgejo bootstrap (admin user, token, repo, webhook) is
// already covered by internal/install/bootstrap_e2e_integration_test.go.
// Here we focus on what the agent layer adds on top: the Quadlet plumbing.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/core"
	"github.com/ownbase/ownbase/internal/schema"
)

// requireLinuxRoot skips the test when not running as root on Linux.
func requireLinuxRoot(t *testing.T) {
	t.Helper()
	if os.Getenv("GOOS") == "darwin" {
		t.Skip("requires Linux")
	}
	if !runningAsRoot() {
		t.Skip("requires root (sudo go test)")
	}
}

// TestAgentQuadletDir_RootUsesSystemPath verifies that the agent writes Quadlet
// units to /etc/containers/systemd (system-level) rather than the user path
// when running as root.
func TestAgentQuadletDir_RootUsesSystemPath(t *testing.T) {
	requireLinuxRoot(t)
	dir := agentQuadletDir()
	if dir != "/etc/containers/systemd" {
		t.Errorf("agentQuadletDir() = %q, want /etc/containers/systemd when running as root", dir)
	}
}

// TestBootstrapCore_QuadletUnitsWritten verifies that bootstrapCore writes all
// expected Quadlet unit files to /etc/containers/systemd and that each file
// has the correct content (network, forgejo, caddy).
//
// This catches the class of bugs where a unit file is missing (e.g. the
// ownbase-internal.network unit that was omitted, breaking Caddy startup).
func TestBootstrapCore_QuadletUnitsWritten(t *testing.T) {
	requireLinuxRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// bootstrapCore writes units and starts containers. Calling it here with
	// default CoreConfig exercises the exact path the production agent takes.
	// It is fully idempotent — safe to run even if containers are already up.
	err := bootstrapCore(ctx, agentConfig{
		repoPath:   "/opt/ownbase/repo",
		statusAddr: "127.0.0.1:7070",
	}, schema.CoreConfig{}, true)
	if err != nil {
		t.Fatalf("bootstrapCore: %v", err)
	}

	quadletDir := agentQuadletDir()
	coreOut := core.BuildCoreOutput(schema.CoreConfig{}, core.Current)

	for name := range coreOut.QuadletUnits {
		path := filepath.Join(quadletDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("missing Quadlet unit %q in %s: %v", name, quadletDir, err)
			continue
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			t.Errorf("Quadlet unit %q is empty", name)
		}
	}

	// Specifically verify the network unit (missing unit caused Caddy to fail).
	netUnit := filepath.Join(quadletDir, core.OwnbaseInternalNetwork+".network")
	netData, err := os.ReadFile(netUnit)
	if err != nil {
		t.Fatalf("ownbase-internal.network unit missing — Caddy will fail: %v", err)
	}
	if !strings.Contains(string(netData), "[Network]") {
		t.Errorf("network unit missing [Network] section:\n%s", netData)
	}
}

// TestBootstrapCore_ForgejoRunning verifies that after bootstrapCore the
// Forgejo container is running and healthy.
func TestBootstrapCore_ForgejoRunning(t *testing.T) {
	requireLinuxRoot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := bootstrapCore(ctx, agentConfig{
		repoPath:   "/opt/ownbase/repo",
		statusAddr: "127.0.0.1:7070",
	}, schema.CoreConfig{}, true); err != nil {
		t.Fatalf("bootstrapCore: %v", err)
	}

	// Forgejo should be healthy within 3 minutes (image already pulled).
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		url := discoverForgejoURL("http://localhost:3000")
		resp, err := http.Get(url + "/api/healthz") //nolint:noctx
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			t.Logf("Forgejo healthy at %s", url)
			return
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(3 * time.Second)
	}
	t.Error("Forgejo not healthy after 3 minutes")
}
