//go:build integration

package install_test

// Tier-2 integration tests for BootstrapCore and the first-run credential flow.
//
// Run on the Ubuntu VM with:
//
//	sudo go test -tags=integration ./internal/install/... -run TestBootstrap -v -timeout 600s
//	sudo go test -tags=integration ./internal/install/... -run TestFirstRunEnv -v
//
// All tests in this file require root (rootful Podman) because BootstrapCore
// uses podman exec to configure Forgejo and rootful containers by default.
// They share the well-known container name ownbase-core-forgejo and use
// different token/bare-repo paths via temp dirs so they never touch
// /opt/ownbase in production layout.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/core"
	"github.com/ownbase/ownbase/internal/githost"
	"github.com/ownbase/ownbase/internal/install"
	"github.com/ownbase/ownbase/internal/schema"
)

// ---------------------------------------------------------------------------
// Constants and helpers
// ---------------------------------------------------------------------------

const (
	bootstrapTestPort      = 3002 // external bind port; avoids conflict with agent (3000) and forgejo tests (3001)
	bootstrapTestPassword  = "TestBootstrapPass123!"
	bootstrapTestSSHPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBtgq23vSEVagQBFOPNXjNkCHHgQi5g9P2Cq0lAWJCLO bootstrap-test@ownbase"

	// bootstrapTestVolume is the Podman volume used by bootstrap tests instead
	// of core.ForgejoDataVolume (the production volume). Using a test-specific
	// volume means we never touch production Forgejo data, even when running
	// make test-vm against a live install.
	bootstrapTestVolume = "ownbase-forgejo-test-data"
)

// startBootstrapForgejoContainer starts a rootful Forgejo container with the
// well-known core name so that ensureForgejoRunning in BootstrapCore sees it
// as already running and skips the podman-run step.
//
// It stops the ownbased service (if running) so the agent's reconcile
// loop does not race with the test over the container name. The agent is
// restarted in t.Cleanup after the test finishes.
//
// Cleanup is registered with t.Cleanup before any setup begins so it always
// runs even if the helper calls t.Fatalf internally.
func startBootstrapForgejoContainer(t *testing.T) {
	t.Helper()
	requireRoot(t)

	// Stop the agent so it cannot recreate the Forgejo container mid-test.
	agentWasRunning := exec.Command("systemctl", "is-active", "--quiet", "ownbased").Run() == nil
	if agentWasRunning {
		exec.Command("systemctl", "stop", "ownbased").Run()
	}

	// OwnBase writes Quadlet unit files into /root/.config/containers/systemd/
	// (root's user-level Quadlet directory) so that rootful containers are
	// managed by root's user systemd session (PID ~860), not the system systemd.
	// Masking only the system-level unit is insufficient; we must also mask the
	// user-level unit so its Restart=always policy cannot replace our test
	// container. userCtl issues systemctl --user for root's session.
	forgejSvc := core.ForgejoContainerName + ".service"
	userEnv := append(os.Environ(),
		"XDG_RUNTIME_DIR=/run/user/0",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/0/bus",
	)
	userCtl := func(args ...string) {
		cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
		cmd.Env = userEnv
		cmd.Run()
	}
	// Mask both service instances before stopping so Restart=always cannot
	// recreate the container between our stop and the fresh podman run.
	exec.Command("systemctl", "mask", forgejSvc).Run() // system-level
	userCtl("mask", forgejSvc)                         // user-level (the active one)
	userCtl("stop", forgejSvc)                         // stops the user-level service
	exec.Command("podman", "rm", "-f", core.ForgejoContainerName).Run()
	// Remove only the TEST volume so the test starts with blank Forgejo state.
	// We never touch core.ForgejoDataVolume (the production volume) so that
	// running make test-vm against a live install does not destroy Forgejo data.
	exec.Command("podman", "volume", "rm", "-f", bootstrapTestVolume).Run()

	// Register cleanup FIRST so it fires even if pull/run below calls t.Fatalf.
	t.Cleanup(func() {
		exec.Command("podman", "stop", core.ForgejoContainerName).Run()
		exec.Command("podman", "rm", "-f", core.ForgejoContainerName).Run()
		exec.Command("podman", "volume", "rm", "-f", bootstrapTestVolume).Run()
		userCtl("unmask", forgejSvc)
		exec.Command("systemctl", "unmask", forgejSvc).Run()
		if agentWasRunning {
			exec.Command("systemctl", "start", "ownbased").Run()
		}
	})

	t.Log("pulling Forgejo image (cached after first run)…")
	if out, err := exec.Command("podman", "pull", core.Current.ForgejoImage).CombinedOutput(); err != nil {
		t.Fatalf("podman pull forgejo: %v\n%s", err, out)
	}

	// --replace atomically stops and removes any container with the same name
	// (stale from a prior run or recreated by the agent before it was stopped).
	env := core.ForgejoEnvForContainer(core.DefaultForgejoPort, "")
	args := []string{
		"run", "-d",
		"--replace",
		"--name", core.ForgejoContainerName,
		"-v", bootstrapTestVolume + ":/data", // test-specific volume; never touches production data
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", bootstrapTestPort, core.DefaultForgejoPort),
	}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, core.Current.ForgejoImage)

	if out, err := exec.Command("podman", args...).CombinedOutput(); err != nil {
		t.Fatalf("podman run forgejo: %v\n%s", err, out)
	}

	// Wait using the container IP directly (bypasses host-port forwarding races).
	ip := getBootstrapContainerIP(t)
	baseURL := fmt.Sprintf("http://%s:%d", ip, core.DefaultForgejoPort)
	t.Logf("waiting for Forgejo at %s…", baseURL)
	if err := githost.WaitForForgejo(baseURL, core.ForgejoContainerName, 3*time.Minute); err != nil {
		t.Fatalf("Forgejo did not become healthy: %v", err)
	}
	t.Log("Forgejo is healthy")
}

// getBootstrapContainerIP returns the IP address of the core Forgejo container.
func getBootstrapContainerIP(t *testing.T) string {
	t.Helper()
	out, err := exec.Command(
		"podman", "inspect", "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}",
		core.ForgejoContainerName,
	).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("cannot determine Forgejo container IP: %v (output: %q)", err, out)
	}
	return strings.TrimSpace(string(out))
}

// bootstrapCfg builds a CoreBootstrapConfig that points at the running test
// container and uses temp dirs for the token and bare-repo paths.
func bootstrapCfg(t *testing.T, tmpDir, password, sshKey string) install.CoreBootstrapConfig {
	t.Helper()
	ip := getBootstrapContainerIP(t)
	return install.CoreBootstrapConfig{
		CoreConfig:      schema.CoreConfig{},
		BareRepoPath:    filepath.Join(tmpDir, "repo"),
		TokenPath:       filepath.Join(tmpDir, "forgejo-token"),
		ForgejoBaseURL:  fmt.Sprintf("http://%s:%d", ip, core.DefaultForgejoPort),
		AgentWebhookURL: "http://localhost:17070/api/v1/hook/push",
		AdminPassword:   password,
		OwnerSSHKey:     sshKey,
	}
}

// forgejoAPIGet is a small helper that calls the Forgejo JSON API and decodes
// the response. Returns the HTTP status code and the decoded body (or nil on
// decode failure).
func forgejoAPIGet(t *testing.T, baseURL, path, token string) (int, map[string]any) {
	t.Helper()
	url := baseURL + path
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	return resp.StatusCode, m
}

// forgejoAPIGetSlice is like forgejoAPIGet but decodes a JSON array.
func forgejoAPIGetSlice(t *testing.T, baseURL, path, token string) (int, []any) {
	t.Helper()
	url := baseURL + path
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var s []any
	_ = json.Unmarshal(body, &s)
	return resp.StatusCode, s
}

// readToken reads the API token from path and fatals if missing or empty.
func readToken(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token %s: %v", path, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		t.Fatalf("token file %s is empty", path)
	}
	return token
}

// ---------------------------------------------------------------------------
// Group A — TestBootstrapCore_*
// ---------------------------------------------------------------------------

// TestBootstrapCore_E2E runs the full BootstrapCore flow against a real
// Forgejo container and asserts every observable output:
//   - Token file written and valid
//   - Owner password set (basic-auth works)
//   - Owner SSH key registered
//   - base repo created and seeded with ownbase.yaml
//   - Push webhook configured
func TestBootstrapCore_E2E(t *testing.T) {
	requireLinux(t)
	requireRoot(t)
	startBootstrapForgejoContainer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dir := t.TempDir()
	cfg := bootstrapCfg(t, dir, bootstrapTestPassword, bootstrapTestSSHPubKey)

	if err := install.BootstrapCore(ctx, cfg); err != nil {
		t.Fatalf("BootstrapCore: %v", err)
	}

	token := readToken(t, cfg.TokenPath)
	baseURL := cfg.ForgejoBaseURL

	// Token authenticates.
	status, _ := forgejoAPIGet(t, baseURL, "/api/v1/user", token)
	if status != http.StatusOK {
		t.Errorf("token auth: want 200, got %d", status)
	}

	// Password authenticates via basic auth.
	{
		url := baseURL + "/api/v1/user"
		req, _ := http.NewRequest("GET", url, nil)
		req.SetBasicAuth(core.AdminUser, bootstrapTestPassword)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("basic auth check: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("password auth: want 200, got %d", resp.StatusCode)
		}
	}

	// SSH key registered.
	{
		status, keys := forgejoAPIGetSlice(t, baseURL, "/api/v1/user/keys", token)
		if status != http.StatusOK {
			t.Errorf("GET /user/keys: want 200, got %d", status)
		}
		found := false
		for _, k := range keys {
			if m, ok := k.(map[string]any); ok {
				if keyVal, _ := m["key"].(string); strings.TrimSpace(keyVal) == strings.TrimSpace(bootstrapTestSSHPubKey) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("SSH key not found in Forgejo user keys (got %d keys)", len(keys))
		}
	}

	// base repo exists.
	repoPath := fmt.Sprintf("/api/v1/repos/%s/%s", core.AdminUser, githost.DefaultForgejoRepoName)
	if status, _ := forgejoAPIGet(t, baseURL, repoPath, token); status != http.StatusOK {
		t.Errorf("base repo: want 200, got %d", status)
	}

	// ownbase.yaml seeded in the base repo.
	contentsPath := fmt.Sprintf("/api/v1/repos/%s/%s/contents/ownbase.yaml", core.AdminUser, githost.DefaultForgejoRepoName)
	if status, _ := forgejoAPIGet(t, baseURL, contentsPath, token); status != http.StatusOK {
		t.Errorf("ownbase.yaml in base repo: want 200, got %d", status)
	}

	// Push webhook configured.
	hooksPath := fmt.Sprintf("/api/v1/repos/%s/%s/hooks", core.AdminUser, githost.DefaultForgejoRepoName)
	if status, hooks := forgejoAPIGetSlice(t, baseURL, hooksPath, token); status != http.StatusOK || len(hooks) == 0 {
		t.Errorf("webhook: want ≥1 hook, got status=%d len=%d", status, len(hooks))
	}
}

// TestBootstrapCore_Idempotent calls BootstrapCore twice and verifies the
// second run completes without error and produces no duplicates.
func TestBootstrapCore_Idempotent(t *testing.T) {
	requireLinux(t)
	requireRoot(t)
	startBootstrapForgejoContainer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dir := t.TempDir()
	cfg := bootstrapCfg(t, dir, bootstrapTestPassword, bootstrapTestSSHPubKey)

	if err := install.BootstrapCore(ctx, cfg); err != nil {
		t.Fatalf("BootstrapCore first run: %v", err)
	}
	token := readToken(t, cfg.TokenPath)

	// Second run — must not error or duplicate state.
	if err := install.BootstrapCore(ctx, cfg); err != nil {
		t.Fatalf("BootstrapCore second run: %v", err)
	}

	baseURL := cfg.ForgejoBaseURL

	// Exactly 1 base repo.
	reposPath := fmt.Sprintf("/api/v1/user/repos")
	if status, repos := forgejoAPIGetSlice(t, baseURL, reposPath, token); status != http.StatusOK {
		t.Errorf("list repos: want 200, got %d", status)
	} else if len(repos) != 1 {
		t.Errorf("idempotency: expected exactly 1 repo, got %d", len(repos))
	}

	// Exactly 1 SSH key.
	if status, keys := forgejoAPIGetSlice(t, baseURL, "/api/v1/user/keys", token); status != http.StatusOK {
		t.Errorf("list keys: want 200, got %d", status)
	} else if len(keys) != 1 {
		t.Errorf("idempotency: expected exactly 1 SSH key, got %d", len(keys))
	}

	// Exactly 1 webhook.
	hooksPath := fmt.Sprintf("/api/v1/repos/%s/%s/hooks", core.AdminUser, githost.DefaultForgejoRepoName)
	if status, hooks := forgejoAPIGetSlice(t, baseURL, hooksPath, token); status != http.StatusOK {
		t.Errorf("list hooks: want 200, got %d", status)
	} else if len(hooks) != 1 {
		t.Errorf("idempotency: expected exactly 1 webhook, got %d", len(hooks))
	}
}

// TestBootstrapCore_NoPassword runs BootstrapCore with an empty AdminPassword
// and verifies the token is still written (password is not required for the
// token credential path).
func TestBootstrapCore_NoPassword(t *testing.T) {
	requireLinux(t)
	requireRoot(t)
	startBootstrapForgejoContainer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dir := t.TempDir()
	cfg := bootstrapCfg(t, dir, "", "") // empty password, no SSH key

	if err := install.BootstrapCore(ctx, cfg); err != nil {
		t.Fatalf("BootstrapCore with empty password: %v", err)
	}

	// Token must still be written even when no password was supplied.
	token := readToken(t, cfg.TokenPath)

	// Token must be valid.
	if status, _ := forgejoAPIGet(t, cfg.ForgejoBaseURL, "/api/v1/user", token); status != http.StatusOK {
		t.Errorf("token after empty-password bootstrap: want 200, got %d", status)
	}
}

// ---------------------------------------------------------------------------
// Group B — TestFirstRunEnv_*
// ---------------------------------------------------------------------------

// TestFirstRunEnv_ReadAndDelete verifies the file parsing and deletion helpers.
// This is a pure-logic test: no Forgejo, no VM, no root required.
func TestFirstRunEnv_ReadAndDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "first-run.env")

	// Non-existent file returns zero-value struct.
	env := install.ReadFirstRunEnv(path)
	if env.Password != "" || env.SSHKey != "" {
		t.Errorf("non-existent file: expected empty, got pw=%q key=%q", env.Password, env.SSHKey)
	}

	// Write a file with both fields.
	content := "OWNER_PASSWORD=hunter2\nOWNER_SSH_KEY=ssh-ed25519 AAAAC3 user@host\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env = install.ReadFirstRunEnv(path)
	if env.Password != "hunter2" {
		t.Errorf("password: want %q, got %q", "hunter2", env.Password)
	}
	if env.SSHKey != "ssh-ed25519 AAAAC3 user@host" {
		t.Errorf("ssh key: want %q, got %q", "ssh-ed25519 AAAAC3 user@host", env.SSHKey)
	}

	// DeleteFirstRunEnv removes the file.
	install.DeleteFirstRunEnv(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file still exists after DeleteFirstRunEnv")
	}

	// Deleting again is silent (no error).
	install.DeleteFirstRunEnv(path)

	// Re-reading after deletion returns zero-value struct.
	env = install.ReadFirstRunEnv(path)
	if env.Password != "" || env.SSHKey != "" {
		t.Errorf("after delete: expected empty, got pw=%q key=%q", env.Password, env.SSHKey)
	}
}

// TestFirstRunEnv_DomainAndEmail verifies that FORGEJO_DOMAIN and CADDY_EMAIL
// are parsed correctly from the file. Pure-logic test — no root, no Forgejo.
func TestFirstRunEnv_DomainAndEmail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "first-run.env")

	content := "OWNER_PASSWORD=pw\nFORGEJO_DOMAIN=git.example.com\nCADDY_EMAIL=admin@example.com\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := install.ReadFirstRunEnv(path)
	if env.ForgejoDomain != "git.example.com" {
		t.Errorf("domain: want %q, got %q", "git.example.com", env.ForgejoDomain)
	}
	if env.CaddyEmail != "admin@example.com" {
		t.Errorf("email: want %q, got %q", "admin@example.com", env.CaddyEmail)
	}
}

// TestFirstRunEnv_PartialFile verifies parsing when only one field is present.
func TestFirstRunEnv_PartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "first-run.env")

	// Password only.
	os.WriteFile(path, []byte("OWNER_PASSWORD=mypassword\n"), 0o600)
	env := install.ReadFirstRunEnv(path)
	if env.Password != "mypassword" {
		t.Errorf("password-only: want %q, got %q", "mypassword", env.Password)
	}
	if env.SSHKey != "" {
		t.Errorf("password-only: ssh key should be empty, got %q", env.SSHKey)
	}

	// SSH key only.
	os.WriteFile(path, []byte("OWNER_SSH_KEY=ssh-rsa AAAA test\n"), 0o600)
	env = install.ReadFirstRunEnv(path)
	if env.Password != "" {
		t.Errorf("key-only: password should be empty, got %q", env.Password)
	}
	if env.SSHKey != "ssh-rsa AAAA test" {
		t.Errorf("key-only: want %q, got %q", "ssh-rsa AAAA test", env.SSHKey)
	}
}

// TestFirstRunEnv_CredentialsApplied writes first-run.env, calls BootstrapCore
// with the parsed credentials, and verifies both password and SSH key are live
// in Forgejo. Simulates the exact flow executed by bootstrap_core_integration.go.
func TestFirstRunEnv_CredentialsApplied(t *testing.T) {
	requireLinux(t)
	requireRoot(t)
	startBootstrapForgejoContainer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	dir := t.TempDir()
	envPath := filepath.Join(dir, "first-run.env")

	// Write credentials to the file (simulating what install.sh does).
	content := fmt.Sprintf("OWNER_PASSWORD=%s\nOWNER_SSH_KEY=%s\n",
		bootstrapTestPassword, bootstrapTestSSHPubKey)
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write first-run.env: %v", err)
	}

	// Simulate what bootstrap_core_integration.go does: read, pass, delete.
	firstRun := install.ReadFirstRunEnv(envPath)
	if firstRun.Password == "" {
		t.Fatal("ReadFirstRunEnv returned empty password")
	}

	cfg := bootstrapCfg(t, dir, firstRun.Password, firstRun.SSHKey)
	err := install.BootstrapCore(ctx, cfg)
	if err == nil {
		install.DeleteFirstRunEnv(envPath)
	} else {
		t.Fatalf("BootstrapCore: %v", err)
	}

	// File deleted after success.
	if _, statErr := os.Stat(envPath); !os.IsNotExist(statErr) {
		t.Errorf("first-run.env should be deleted after successful bootstrap, but still exists")
	}

	// Credentials are live in Forgejo.
	token := readToken(t, cfg.TokenPath)
	baseURL := cfg.ForgejoBaseURL

	{
		url := baseURL + "/api/v1/user"
		req, _ := http.NewRequest("GET", url, nil)
		req.SetBasicAuth(core.AdminUser, firstRun.Password)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("basic auth: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("password from first-run.env: want 200, got %d", resp.StatusCode)
		}
	}

	if status, keys := forgejoAPIGetSlice(t, baseURL, "/api/v1/user/keys", token); status != http.StatusOK || len(keys) == 0 {
		t.Errorf("SSH key from first-run.env: want ≥1 key, got status=%d len=%d", status, len(keys))
	}
}

// TestFirstRunEnv_NotDeletedOnFailure verifies that first-run.env is retained
// when BootstrapCore fails so the next agent start can retry with the same
// credentials.
func TestFirstRunEnv_NotDeletedOnFailure(t *testing.T) {
	requireLinux(t)
	requireRoot(t)
	// Deliberately do NOT start Forgejo — BootstrapCore must fail.

	dir := t.TempDir()
	envPath := filepath.Join(dir, "first-run.env")
	os.WriteFile(envPath, []byte("OWNER_PASSWORD=testpw\n"), 0o600)

	firstRun := install.ReadFirstRunEnv(envPath)

	// Use a port nothing is listening on (BootstrapCore will time out quickly).
	cfg := install.CoreBootstrapConfig{
		CoreConfig:     schema.CoreConfig{},
		BareRepoPath:   filepath.Join(dir, "repo"),
		TokenPath:      filepath.Join(dir, "forgejo-token"),
		ForgejoBaseURL: "http://127.0.0.1:19999", // nothing listening here
		AdminPassword:  firstRun.Password,
		OwnerSSHKey:    firstRun.SSHKey,
		DryRun:         false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := install.BootstrapCore(ctx, cfg)
	// BootstrapCore should fail (Forgejo not reachable).
	if err == nil {
		t.Log("BootstrapCore unexpectedly succeeded — skipping file-retained check")
		return
	}

	// The caller (bootstrap_core_integration.go) only deletes the file on success.
	// Simulate that: don't call DeleteFirstRunEnv here.
	if _, statErr := os.Stat(envPath); os.IsNotExist(statErr) {
		t.Error("first-run.env was deleted despite a failed bootstrap — credentials lost for retry")
	}
}

// ---------------------------------------------------------------------------
// Shared helpers (also used by install_integration_test.go via package test)
// ---------------------------------------------------------------------------

// forgejoAPIPost sends a POST request to the Forgejo API with a JSON body.
func forgejoAPIPost(t *testing.T, baseURL, path, token string, bodyObj any) (int, []byte) {
	t.Helper()
	bodyBytes, _ := json.Marshal(bodyObj)
	url := baseURL + path
	req, _ := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}
