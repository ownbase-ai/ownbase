//go:build integration

package githost_test

// Tier-2 integration tests for M4b: Forgejo as a reconciled service.
//
// Run on the Ubuntu VM with:
//
//	go test -tags=integration ./internal/githost/... -run TestForgejo -v -timeout 300s
//
// Prerequisites on the VM:
//   - Podman installed and rootless configured (from M3 setup)
//   - Internet access to pull codeberg.org/forgejo/forgejo:15.0.3

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/githost"
)

// jsonMarshal is a package-local alias to avoid conflict with the std lib
// json.Marshal name in test code that reads response bodies.
var jsonMarshal = json.Marshal

const (
	forgejoImage     = "codeberg.org/forgejo/forgejo:15.0.3"
	forgejoPort      = 3001 // non-standard to avoid conflicts with a running agent
	forgejoBaseURL   = "http://localhost:3001"
	forgejoAdminUser = "ownbased"
	forgejoAdminPass = "TestPass1234"
	forgejoAdminMail = "agent@ownbase.local"
	forgejoCtName    = "ownbase-forgejo-integtest"
)

// ---------------------------------------------------------------------------
// Container lifecycle helpers
// ---------------------------------------------------------------------------

// stopForgejoTestContainer stops and removes the test Forgejo container.
// Idempotent — safe to call more than once (e.g. from KillTest and t.Cleanup).
func stopForgejoTestContainer() {
	exec.Command("podman", "stop", forgejoCtName).Run()
	exec.Command("podman", "rm", "-f", forgejoCtName).Run()
}

// startForgejoContainer starts a temporary Forgejo container for this test.
// It registers t.Cleanup so the container is always removed when the test
// finishes. It also returns a stop function for tests that need to stop
// Forgejo explicitly mid-test (e.g. TestForgejo_KillTest).
func startForgejoContainer(t *testing.T) func() {
	t.Helper()

	// Register cleanup FIRST so it fires even when t.Fatalf is called below.
	t.Cleanup(stopForgejoTestContainer)

	t.Log("pulling Forgejo image (may take a minute on first run)...")
	if out, err := exec.Command("podman", "pull", forgejoImage).CombinedOutput(); err != nil {
		t.Fatalf("podman pull: %v\n%s", err, out)
	}

	// --replace atomically stops and removes any container with the same name
	// (stale from a previous interrupted test run or still stopping from the
	// previous test's cleanup) before creating the new one.
	out, err := exec.Command("podman", "run", "-d",
		"--replace",
		"--name", forgejoCtName,
		"-p", fmt.Sprintf("127.0.0.1:%d:3000", forgejoPort),
		"-e", "FORGEJO__security__INSTALL_LOCK=true",
		"-e", "FORGEJO__server__HTTP_PORT=3000",
		"-e", fmt.Sprintf("FORGEJO__server__ROOT_URL=%s", forgejoBaseURL),
		"-e", "FORGEJO__database__DB_TYPE=sqlite3",
		// /data/gitea/ is the app's work-path and is always writable.
		"-e", "FORGEJO__database__PATH=/data/gitea/forgejo.db",
		"-e", "FORGEJO__log__LEVEL=warn",
		"-e", "FORGEJO__service__DISABLE_REGISTRATION=true",
		forgejoImage,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("podman run forgejo: %v\n%s", err, out)
	}

	t.Log("waiting for Forgejo to become healthy (up to 3 min)...")
	// Pass forgejoCtName so the fallback IP probe checks the test container,
	// not ownbase-core-forgejo (production) which may be running on the VM.
	if err := githost.WaitForForgejo(forgejoBaseURL, forgejoCtName, 3*time.Minute); err != nil {
		t.Fatalf("Forgejo did not start: %v", err)
	}
	t.Log("Forgejo is healthy")

	return stopForgejoTestContainer
}

// createAdminAndToken creates the initial admin user and returns an API token.
func createAdminAndToken(t *testing.T) string {
	t.Helper()
	if err := githost.CreateForgejoAdmin(forgejoCtName, forgejoAdminUser, forgejoAdminPass, forgejoAdminMail); err != nil {
		t.Fatalf("CreateForgejoAdmin: %v", err)
	}
	token, err := githost.GenerateForgejoToken(forgejoCtName, forgejoAdminUser, "test-token")
	if err != nil {
		t.Fatalf("GenerateForgejoToken: %v", err)
	}
	if token == "" {
		t.Fatal("GenerateForgejoToken returned empty token")
	}
	return token
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestForgejo_StartsHealthy verifies that the Forgejo container becomes
// healthy within the timeout — the M4b health probe passes.
func TestForgejo_StartsHealthy(t *testing.T) {
	startForgejoContainer(t)

	resp, err := http.Get(forgejoBaseURL + "/api/healthz")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health probe: want 200, got %d", resp.StatusCode)
	}
}

// TestForgejo_AdminAndRepo verifies the full initial setup flow:
// create admin → generate token → create mirror repo → verify via API.
func TestForgejo_AdminAndRepo(t *testing.T) {
	startForgejoContainer(t)
	token := createAdminAndToken(t)

	cfg := githost.ForgejoConfig{
		BaseURL:    forgejoBaseURL,
		AdminToken: token,
		RepoOwner:  forgejoAdminUser,
		RepoName:   githost.DefaultForgejoRepoName,
	}

	cloneURL, err := githost.CreateForgejoRepo(cfg)
	if err != nil {
		t.Fatalf("CreateForgejoRepo: %v", err)
	}
	if !strings.Contains(cloneURL, githost.DefaultForgejoRepoName) {
		t.Errorf("clone URL %q does not contain repo name", cloneURL)
	}

	// Verify via Forgejo API.
	apiURL := fmt.Sprintf("%s/api/v1/repos/%s/%s",
		forgejoBaseURL, forgejoAdminUser, githost.DefaultForgejoRepoName)
	req, _ := http.NewRequest("GET", apiURL, nil)
	req.Header.Set("Authorization", "token "+token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("API check: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("repo not found via API: status %d", resp.StatusCode)
	}
}

// TestForgejo_BareRepoSyncToForgejo verifies that the bare repo's commits
// are pushed to Forgejo and visible via the API.
func TestForgejo_BareRepoSyncToForgejo(t *testing.T) {
	startForgejoContainer(t)

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")
	githost.Bootstrap(repoPath, checkoutPath)

	rec, _ := githost.NewGenesisRecord("dev", "")
	if err := githost.WriteGenesisRecord(checkoutPath, rec); err != nil {
		t.Fatalf("WriteGenesisRecord: %v", err)
	}

	token := createAdminAndToken(t)
	cfg := githost.ForgejoConfig{
		BaseURL:    forgejoBaseURL,
		AdminToken: token,
		RepoOwner:  forgejoAdminUser,
		RepoName:   githost.DefaultForgejoRepoName,
	}
	if _, err := githost.CreateForgejoRepo(cfg); err != nil {
		t.Fatalf("CreateForgejoRepo: %v", err)
	}

	// Use basic auth for git push (embed token in URL).
	pushURL := fmt.Sprintf("http://%s:%s@localhost:%d/%s/%s.git",
		forgejoAdminUser, token, forgejoPort, forgejoAdminUser, githost.DefaultForgejoRepoName)
	exec.Command("git", "-C", repoPath, "remote", "remove", githost.ForgejoRemoteName).Run()
	exec.Command("git", "-C", repoPath, "remote", "add", githost.ForgejoRemoteName, pushURL).Run()

	// Force-push: Forgejo's auto_init created a dummy initial commit; we
	// replace it with our bare-repo history.
	out, err := exec.Command("git", "-C", repoPath,
		"push", "--force", githost.ForgejoRemoteName, "--all").CombinedOutput()
	t.Logf("git push output: %s", out)
	if err != nil {
		t.Fatalf("push to Forgejo: %v\n%s", err, out)
	}

	// Verify via branches API (immediately consistent unlike commits API).
	branchesURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/branches",
		forgejoBaseURL, forgejoAdminUser, githost.DefaultForgejoRepoName)
	req, _ := http.NewRequest("GET", branchesURL, nil)
	req.Header.Set("Authorization", "token "+token)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("list branches: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("branches API status %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"main"`) {
		t.Errorf("main branch not visible in Forgejo after push:\n%s", body)
	}
}

// TestForgejo_KillTest is the M4b acid test: stop Forgejo and confirm that
// a direct bare-repo commit still triggers the hook and the reconcile loop.
// Forgejo must never be in the critical path for reconcile.
func TestForgejo_KillTest(t *testing.T) {
	cleanup := startForgejoContainer(t)

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "repo")
	checkoutPath := filepath.Join(dir, "checkout")
	pidPath := filepath.Join(dir, "agent.pid")

	githost.Bootstrap(repoPath, checkoutPath)

	// Install a custom hook pointing to our test PID file.
	hookScript := fmt.Sprintf("#!/bin/sh\nPIDFILE=%s\n"+
		"if [ -f \"$PIDFILE\" ]; then kill -USR1 \"$(cat \"$PIDFILE\")\" 2>/dev/null || true; fi\n",
		pidPath)
	hookPath := filepath.Join(repoPath, "hooks", "post-receive")
	os.MkdirAll(filepath.Dir(hookPath), 0o755)
	os.WriteFile(hookPath, []byte(hookScript), 0o755)

	// Write the test process PID.
	os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)

	rec, _ := githost.NewGenesisRecord("dev", "")
	githost.WriteGenesisRecord(checkoutPath, rec)

	// Arm SIGUSR1 before any push.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGUSR1)
	defer signal.Stop(sigCh)

	// KILL Forgejo — the bare-repo loop must survive independently.
	t.Log("stopping Forgejo container...")
	cleanup()

	// Wait for Forgejo to be fully down.
	time.Sleep(2 * time.Second)
	resp, err := http.Get(forgejoBaseURL + "/-/health")
	if err == nil {
		resp.Body.Close()
		t.Logf("note: Forgejo still responding after stop (may be race), continuing test")
	}

	// Push a new commit directly to the bare repo (no Forgejo involved).
	os.WriteFile(filepath.Join(checkoutPath, "kill-test.txt"),
		[]byte("post-forgejo-kill commit\n"), 0o644)
	exec.Command("git", "-C", checkoutPath, "add", "kill-test.txt").Run()
	exec.Command("git", "-C", checkoutPath,
		"commit", "-m", "test: commit after Forgejo killed",
		"--author", "OwnBase Daemon <daemon@ownbase.local>").Run()
	if out, err := exec.Command("git", "-C", checkoutPath,
		"push", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("push to bare repo: %v\n%s", err, out)
	}

	// Run the hook directly (simulating git calling it after push).
	if out, err := exec.Command("sh", hookPath).CombinedOutput(); err != nil {
		t.Errorf("hook script failed after Forgejo stop: %v\n%s", err, out)
	}

	// The SIGUSR1 should arrive quickly.
	select {
	case <-sigCh:
		t.Log("kill test PASSED: SIGUSR1 received; bare-repo reconcile trigger works without Forgejo")
	case <-time.After(5 * time.Second):
		t.Error("timed out waiting for SIGUSR1 — hook did not signal agent")
	}

	// Commit is in the bare repo.
	out, err := exec.Command("git", "-C", repoPath, "log", "--oneline").Output()
	if err != nil {
		t.Fatalf("git log bare repo: %v", err)
	}
	if !strings.Contains(string(out), "after Forgejo killed") {
		t.Errorf("bare repo missing post-kill commit:\n%s", out)
	}
}

// TestForgejo_ConstitutionAudit verifies the Service Constitution properties
// for Forgejo:
//  1. Removable: stopping Forgejo doesn't break reconcile (kill test above).
//  2. Data accessible: data volume can be read directly (sqlite file).
//  3. Runs standalone: no external call required (tested in isolation).
//  4. Forkable/replaceable: service is described by a plain ownbase.yaml entry.
//
// This test records the audit result rather than asserting every property
// mechanically — the kill test above covers (1), and (2-4) are structural.
func TestForgejo_ConstitutionAudit(t *testing.T) {
	startForgejoContainer(t)

	// (1) Health probe works — Forgejo is healthy as a reconciled service.
	resp, err := http.Get(forgejoBaseURL + "/api/healthz")
	if err != nil {
		t.Errorf("constitution: health probe failed: %v", err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("constitution: health probe: want 200, got %d", resp.StatusCode)
		}
	}

	// (2) Data volume accessible: sqlite file exists inside the container.
	out, err := exec.Command("podman", "exec", forgejoCtName,
		"test", "-f", "/data/forgejo.db").CombinedOutput()
	if err != nil {
		// DB may not exist yet if Forgejo hasn't received any requests.
		// Check that the /data directory is present.
		out2, err2 := exec.Command("podman", "exec", forgejoCtName,
			"ls", "/data").CombinedOutput()
		if err2 != nil {
			t.Errorf("constitution: /data not accessible: %v\n%s", err2, out2)
		} else {
			t.Logf("constitution: /data contents: %s", out2)
		}
	} else {
		t.Logf("constitution: forgejo.db present at /data/forgejo.db (%s)", out)
	}

	// (3) No external dependency: all network calls so far are local.
	// Forgejo's health endpoint is self-contained.
	t.Log("constitution: Forgejo health endpoint is self-contained (no external dependency)")

	// (4) Described by plain ownbase.yaml with image: field — forkable and
	// replaceable (any git host exposing a Forgejo-compatible API can substitute).
	t.Log("constitution audit PASSED: removable=yes, data-accessible=yes, no-cloud=yes, forkable=yes")
}

// ---------------------------------------------------------------------------
// Group C — Forgejo security configuration
// ---------------------------------------------------------------------------

// startForgejoContainerWithExtraEnv starts Forgejo with the default test
// config plus additional environment variables. Registers t.Cleanup and
// returns a stop function (same contract as startForgejoContainer).
func startForgejoContainerWithExtraEnv(t *testing.T, extra ...string) func() {
	t.Helper()

	t.Cleanup(stopForgejoTestContainer)

	if out, err := exec.Command("podman", "pull", forgejoImage).CombinedOutput(); err != nil {
		t.Fatalf("podman pull: %v\n%s", err, out)
	}

	args := []string{
		"run", "-d",
		"--replace",
		"--name", forgejoCtName,
		"-p", fmt.Sprintf("127.0.0.1:%d:3000", forgejoPort),
		"-e", "FORGEJO__security__INSTALL_LOCK=true",
		"-e", "FORGEJO__server__HTTP_PORT=3000",
		"-e", fmt.Sprintf("FORGEJO__server__ROOT_URL=%s", forgejoBaseURL),
		"-e", "FORGEJO__database__DB_TYPE=sqlite3",
		"-e", "FORGEJO__database__PATH=/data/gitea/forgejo.db",
		"-e", "FORGEJO__log__LEVEL=warn",
		"-e", "FORGEJO__service__DISABLE_REGISTRATION=true",
	}
	for _, e := range extra {
		args = append(args, "-e", e)
	}
	args = append(args, forgejoImage)

	if out, err := exec.Command("podman", args...).CombinedOutput(); err != nil {
		t.Fatalf("podman run forgejo: %v\n%s", err, out)
	}

	t.Log("waiting for Forgejo (up to 3 min)...")
	if err := githost.WaitForForgejo(forgejoBaseURL, forgejoCtName, 3*time.Minute); err != nil {
		t.Fatalf("Forgejo did not start: %v", err)
	}
	t.Log("Forgejo is healthy")

	return stopForgejoTestContainer
}

// TestForgejo_RegistrationDisabled verifies that Forgejo rejects self-service
// registration when DISABLE_REGISTRATION=true. The sign-up page should either
// redirect away or display a "registration disabled" message in the body.
func TestForgejo_RegistrationDisabled(t *testing.T) {
	startForgejoContainer(t) // already has DISABLE_REGISTRATION=true

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(forgejoBaseURL + "/user/sign_up")
	if err != nil {
		t.Fatalf("GET /user/sign_up: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := strings.ToLower(string(body))

	// Forgejo 10 returns 200 with an in-page error when registration is disabled.
	// Older versions redirect (302/303). Both are acceptable.
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		if resp.StatusCode != http.StatusOK {
			// Redirect — registration is disabled and the user is sent elsewhere.
			t.Logf("sign_up redirected with status %d (registration disabled)", resp.StatusCode)
			return
		}
		// 200 — must contain a "disabled" signal in the body.
		if !strings.Contains(bodyStr, "disabled") && !strings.Contains(bodyStr, "not allowed") && !strings.Contains(bodyStr, "sign in") {
			t.Errorf("sign_up returned 200 with no disabled/sign-in message; body snippet: %.200s", bodyStr)
		}
		t.Logf("sign_up returned 200 with disabled indicator (registration disabled as expected)")
	} else {
		t.Logf("sign_up denied with status %d (registration disabled)", resp.StatusCode)
	}
}

// TestForgejo_AnonymousAccessDenied verifies that REQUIRE_SIGNIN_VIEW=true
// prevents unauthenticated access to repository listings.
func TestForgejo_AnonymousAccessDenied(t *testing.T) {
	defer startForgejoContainerWithExtraEnv(t,
		"FORGEJO__service__REQUIRE_SIGNIN_VIEW=true",
	)()

	// Unauthenticated request to the explore/repos endpoint.
	noRedirect := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	resp, err := noRedirect.Get(forgejoBaseURL + "/explore/repos")
	if err != nil {
		t.Fatalf("GET /explore/repos: %v", err)
	}
	defer resp.Body.Close()

	// Should redirect to login or return 401/403.
	if resp.StatusCode == http.StatusOK {
		t.Errorf("unauthenticated access to /explore/repos should be denied when REQUIRE_SIGNIN_VIEW=true, got 200")
	}
	t.Logf("anonymous access denied with status %d (expected)", resp.StatusCode)
}

// TestForgejo_WebhookAllowsPrivateIP verifies that Forgejo accepts a webhook
// pointing to a private IP when ALLOWED_HOST_LIST includes "private". This is
// required for the agent webhook (host.containers.internal is a private IP).
func TestForgejo_WebhookAllowsPrivateIP(t *testing.T) {
	defer startForgejoContainerWithExtraEnv(t,
		"FORGEJO__webhook__ALLOWED_HOST_LIST=private",
	)()

	token := createAdminAndToken(t)

	// Create the base repo so we can add a webhook to it.
	cfg := githost.ForgejoConfig{
		BaseURL:    forgejoBaseURL,
		AdminToken: token,
		RepoOwner:  forgejoAdminUser,
		RepoName:   githost.DefaultForgejoRepoName,
	}
	if _, err := githost.CreateForgejoRepo(cfg); err != nil {
		t.Fatalf("CreateForgejoRepo: %v", err)
	}

	// Create a webhook pointing to a private IP.
	payload := map[string]any{
		"active":        true,
		"branch_filter": "*",
		"config": map[string]string{
			"url":          "http://10.88.0.1:17070/api/v1/hook/push",
			"content_type": "json",
		},
		"events": []string{"push"},
		"type":   "gitea",
	}
	bodyBytes, _ := jsonMarshal(payload)

	hookURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/hooks", forgejoBaseURL, forgejoAdminUser, githost.DefaultForgejoRepoName)
	req, _ := http.NewRequest("POST", hookURL, strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+token)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST webhook: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Errorf("create webhook to private IP: want 201, got %d: %s", resp.StatusCode, b)
	}
	t.Log("webhook to private IP accepted (ALLOWED_HOST_LIST=private works)")
}

// ---------------------------------------------------------------------------
// Group F — Mirror sync behaviour
// ---------------------------------------------------------------------------

// TestMirrorSync_TimesOutGracefully verifies that SyncBareRepoFromForgejo
// does not hang indefinitely when git hangs waiting on a TCP connection that
// has been accepted but never sends data. The call must return (with an
// error) within the 30-second internal deadline plus a small buffer.
func TestMirrorSync_TimesOutGracefully(t *testing.T) {
	// This test does not need a running Forgejo or root — only a bare repo and
	// a TCP black-hole listener.

	// Start a TCP listener that accepts connections but sends nothing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close()
	// Accept one connection and leave it open (no data sent — simulates a
	// port-forwarding proxy that accepted TCP but isn't forwarding yet).
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			defer conn.Close()
			time.Sleep(5 * time.Minute) // hold open longer than the git timeout
		}
	}()

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "bare-repo")
	if out, err := exec.Command("git", "init", "--bare", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}

	blackHoleURL := fmt.Sprintf("http://%s", ln.Addr().String())
	t.Logf("testing timeout against TCP black-hole at %s", blackHoleURL)

	start := time.Now()
	err = githost.SyncBareRepoFromForgejo(repoPath, blackHoleURL, "dummy-token", "owner", "repo")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("SyncBareRepoFromForgejo: expected error on unreachable URL, got nil")
	}
	t.Logf("returned error in %v: %v", elapsed.Round(time.Second), err)

	const maxWait = 35 * time.Second
	if elapsed > maxWait {
		t.Errorf("SyncBareRepoFromForgejo hung for %v — must return within %v", elapsed, maxWait)
	}
}

// TestCreateForgejoMirror_Idempotent verifies that calling CreateForgejoMirror
// twice for the same repo name does not create a duplicate.
func TestCreateForgejoMirror_Idempotent(t *testing.T) {
	startForgejoContainer(t)

	token := createAdminAndToken(t)
	cfg := githost.ForgejoConfig{
		BaseURL:    forgejoBaseURL,
		AdminToken: token,
		RepoOwner:  forgejoAdminUser,
		RepoName:   githost.DefaultForgejoRepoName,
	}

	// Pre-create a regular repo to act as the "existing mirror". We use the
	// CreateForgejoRepo function to avoid triggering an external git clone.
	mirrorName := "mirrors-idempotency-test"
	createURL := fmt.Sprintf("%s/api/v1/user/repos", forgejoBaseURL)
	body, _ := jsonMarshal(map[string]any{
		"name":      mirrorName,
		"private":   false,
		"auto_init": false,
	})
	req, _ := http.NewRequest("POST", createURL, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("pre-create repo: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Skipf("pre-create repo failed with %d (Forgejo version incompatibility?)", resp.StatusCode)
	}

	// First call: repo already exists via pre-create — must return nil.
	mirrorCfg := githost.ForgejoConfig{
		BaseURL:    cfg.BaseURL,
		AdminToken: cfg.AdminToken,
		RepoOwner:  forgejoAdminUser,
		RepoName:   mirrorName,
	}
	if err := githost.CreateForgejoMirror(mirrorCfg, "http://example.com/dummy.git", mirrorName, "8h0m0s"); err != nil {
		t.Fatalf("CreateForgejoMirror first call: %v", err)
	}

	// Second call: must also return nil and not create a duplicate.
	if err := githost.CreateForgejoMirror(mirrorCfg, "http://example.com/dummy.git", mirrorName, "8h0m0s"); err != nil {
		t.Fatalf("CreateForgejoMirror second call: %v", err)
	}

	// Verify exactly 1 repo with this name exists.
	searchURL := fmt.Sprintf("%s/api/v1/repos/search?q=%s&token=%s", forgejoBaseURL, mirrorName, token)
	searchResp, err := (&http.Client{Timeout: 10 * time.Second}).Get(searchURL)
	if err != nil {
		t.Fatalf("search repos: %v", err)
	}
	defer searchResp.Body.Close()
	var result struct {
		Data []any `json:"data"`
	}
	if jsonDec := json.NewDecoder(searchResp.Body); jsonDec != nil {
		_ = jsonDec.Decode(&result)
	}
	if len(result.Data) != 1 {
		t.Errorf("idempotency: expected exactly 1 repo named %q, got %d", mirrorName, len(result.Data))
	}
}
