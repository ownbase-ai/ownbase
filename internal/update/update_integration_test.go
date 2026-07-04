//go:build integration

package update_test

// Tier-2 integration tests for M7: blank-ref resolution and drift reporting.
//
// These tests require:
//   - Ubuntu Linux (use the multipass VM; see AGENTS.md)
//   - Podman installed (rootless or root)
//
// Run on the VM:
//
//	go test -v -tags=integration -run TestUpdate ./internal/update/... -timeout 300s

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/update"
)

// requireLinuxUpdate skips the test if not running on Linux.
func requireLinuxUpdate(t *testing.T) {
	t.Helper()
	if os.Getenv("GOOS") == "darwin" {
		t.Skip("Tier-2 test: requires Linux")
	}
	out, err := exec.Command("uname", "-s").Output()
	if err != nil || strings.TrimSpace(string(out)) != "Linux" {
		t.Skip("Tier-2 test: requires Linux")
	}
}

const (
	m7ForgejoImage   = "codeberg.org/forgejo/forgejo:15.0.3"
	m7ForgejoPort    = 3002 // distinct from other tests
	m7ForgejoBaseURL = "http://localhost:3002"
	m7ForgejoUser    = "ownbase"
	m7ForgejoPass    = "TestPass1234"
	m7ForgejoMail    = "agent@ownbase.local"
	m7ForgejoCt      = "ownbase-forgejo-m7test"
)

// startM7Forgejo starts a Forgejo container for M7 tests. Returns cleanup fn.
func startM7Forgejo(t *testing.T) (baseURL, token string, cleanup func()) {
	t.Helper()

	exec.Command("podman", "rm", "-f", m7ForgejoCt).Run()

	t.Log("starting Forgejo for M7 tests...")
	out, err := exec.Command("podman", "run", "-d",
		"--name", m7ForgejoCt,
		"-p", fmt.Sprintf("127.0.0.1:%d:3000", m7ForgejoPort),
		"-e", "FORGEJO__security__INSTALL_LOCK=true",
		"-e", "FORGEJO__server__HTTP_PORT=3000",
		"-e", fmt.Sprintf("FORGEJO__server__ROOT_URL=%s", m7ForgejoBaseURL),
		"-e", "FORGEJO__database__DB_TYPE=sqlite3",
		"-e", "FORGEJO__database__PATH=/data/gitea/forgejo.db",
		"-e", "FORGEJO__log__LEVEL=warn",
		"-e", "FORGEJO__service__DISABLE_REGISTRATION=true",
		m7ForgejoImage,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("podman run forgejo: %v\n%s", err, out)
	}

	cleanup = func() {
		exec.Command("podman", "stop", m7ForgejoCt).Run()
		exec.Command("podman", "rm", "-f", m7ForgejoCt).Run()
	}

	t.Log("waiting for Forgejo health (up to 3m)...")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		resp, err := http.Get(m7ForgejoBaseURL + "/api/healthz")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
	if !forgejoHealthyM7(m7ForgejoBaseURL) {
		cleanup()
		t.Fatal("Forgejo did not become healthy within 3 min")
	}
	t.Log("Forgejo healthy")

	out2, err := exec.Command("podman", "exec", "--user", "git", m7ForgejoCt,
		"forgejo", "admin", "user", "create",
		"--username", m7ForgejoUser,
		"--password", m7ForgejoPass,
		"--email", m7ForgejoMail,
		"--admin", "--must-change-password=false",
	).CombinedOutput()
	if err != nil {
		cleanup()
		t.Fatalf("create admin user: %v\n%s", err, out2)
	}

	raw, err := exec.Command("podman", "exec", "--user", "git", m7ForgejoCt,
		"forgejo", "admin", "user", "generate-access-token",
		"--username", m7ForgejoUser, "--raw",
		"--token-name", "m7test",
		"--scopes", "write:user,write:repository,write:issue,write:organization,read:organization",
	).Output()
	if err != nil {
		cleanup()
		t.Fatalf("generate token: %v", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		cleanup()
		t.Fatal("empty token returned")
	}

	return m7ForgejoBaseURL, tok, cleanup
}

func forgejoHealthyM7(baseURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/healthz", nil)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// createForgejoRepoWithFile creates a repo and seeds it with one file.
func createForgejoRepoWithFile(t *testing.T, baseURL, token, owner, repoName, content string) {
	t.Helper()

	payload, _ := json.Marshal(map[string]any{
		"name":      repoName,
		"auto_init": true,
		"private":   false,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/user/repos", bytes.NewReader(payload))
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	resp.Body.Close()

	filePayload, _ := json.Marshal(map[string]any{
		"message": "chore: initial ownbase.yaml",
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  "main",
	})
	req2, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/ownbase.yaml", baseURL, owner, repoName),
		bytes.NewReader(filePayload))
	req2.Header.Set("Authorization", "token "+token)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := (&http.Client{Timeout: 10 * time.Second}).Do(req2)
	if err != nil {
		t.Fatalf("seed ownbase.yaml: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp2.Body)
		t.Fatalf("seed ownbase.yaml: status %d: %s", resp2.StatusCode, b)
	}
}

// createForgejoOrgAndTaggedRepo creates an org, repo, and lightweight tags.
func createForgejoOrgAndTaggedRepo(t *testing.T, baseURL, adminToken, adminUser, org, repoName string, tags []string) {
	t.Helper()

	orgPayload, _ := json.Marshal(map[string]any{"username": org, "visibility": "public"})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/orgs", bytes.NewReader(orgPayload))
	req.Header.Set("Authorization", "token "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("create org %q: %v", org, err)
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusUnprocessableEntity {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create org %q: status %d: %s", org, resp.StatusCode, b)
	}
	resp.Body.Close()

	repoPayload, _ := json.Marshal(map[string]any{"name": repoName, "auto_init": true, "private": false})
	req2, _ := http.NewRequest(http.MethodPost,
		fmt.Sprintf("%s/api/v1/orgs/%s/repos", baseURL, org), bytes.NewReader(repoPayload))
	req2.Header.Set("Authorization", "token "+adminToken)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := (&http.Client{Timeout: 10 * time.Second}).Do(req2)
	if err != nil {
		t.Fatalf("create repo %s/%s: %v", org, repoName, err)
	}
	if resp2.StatusCode != http.StatusCreated && resp2.StatusCode != http.StatusUnprocessableEntity {
		b, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		t.Fatalf("create repo %s/%s: status %d: %s", org, repoName, resp2.StatusCode, b)
	}
	resp2.Body.Close()

	for _, tag := range tags {
		// Get HEAD SHA.
		headURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/branches/main", baseURL, org, repoName)
		req3, _ := http.NewRequest(http.MethodGet, headURL, nil)
		req3.Header.Set("Authorization", "token "+adminToken)
		resp3, err := (&http.Client{Timeout: 5 * time.Second}).Do(req3)
		if err != nil {
			t.Fatalf("get HEAD for tag %s: %v", tag, err)
		}
		var brInfo struct {
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		json.NewDecoder(resp3.Body).Decode(&brInfo)
		resp3.Body.Close()
		sha := brInfo.Commit.ID

		tagPayload, _ := json.Marshal(map[string]string{"tag_name": tag, "target": sha, "message": tag})
		req4, _ := http.NewRequest(http.MethodPost,
			fmt.Sprintf("%s/api/v1/repos/%s/%s/tags", baseURL, org, repoName), bytes.NewReader(tagPayload))
		req4.Header.Set("Authorization", "token "+adminToken)
		req4.Header.Set("Content-Type", "application/json")
		resp4, err := (&http.Client{Timeout: 5 * time.Second}).Do(req4)
		if err != nil {
			t.Fatalf("create tag %s: %v", tag, err)
		}
		resp4.Body.Close()
	}
}

// ---------------------------------------------------------------------------
// TestUpdate_ComputeDrift_LiveForgejo
// ---------------------------------------------------------------------------

// TestUpdate_ComputeDrift_LiveForgejo verifies that ComputeDrift correctly
// reports that a service pinned to an old tag is behind the newest tag.
func TestUpdate_ComputeDrift_LiveForgejo(t *testing.T) {
	requireLinuxUpdate(t)

	fURL, token, cleanup := startM7Forgejo(t)
	defer cleanup()

	const appRepoOwner = "services"
	createForgejoOrgAndTaggedRepo(t, fURL, token, m7ForgejoUser, appRepoOwner, "myapp", []string{"v1.0.0", "v2.0.0"})

	cfg := update.Config{
		ForgejoURL:   fURL,
		ForgejoToken: token,
		ForgejoUser:  m7ForgejoUser,
		RepoName:     "base",
	}

	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"myapp": {Source: "services/myapp", Ref: "v1.0.0"},
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	drift := update.ComputeDrift(ctx, cfg, services)
	if len(drift) == 0 {
		t.Fatal("expected a drift entry for myapp")
	}
	d := drift[0]
	if d.Service != "myapp" {
		t.Errorf("Service = %q, want myapp", d.Service)
	}
	if d.NewestTag != "v2.0.0" {
		t.Errorf("NewestTag = %q, want v2.0.0", d.NewestTag)
	}
	if d.UpToDate {
		t.Error("UpToDate should be false (pinned to v1.0.0, newest is v2.0.0)")
	}
	t.Logf("drift: ref=%s branch=%s behind=%d newest=%s", d.Ref, d.Branch, d.CommitsBehind, d.NewestTag)
}

// ---------------------------------------------------------------------------
// TestUpdate_ResolveBlankRef_LiveForgejo
// ---------------------------------------------------------------------------

// TestUpdate_ResolveBlankRef_LiveForgejo verifies that ResolveBlankRefs
// commits the default-branch HEAD SHA to ownbase.yaml when ref: is blank.
func TestUpdate_ResolveBlankRef_LiveForgejo(t *testing.T) {
	requireLinuxUpdate(t)

	fURL, token, cleanup := startM7Forgejo(t)
	defer cleanup()

	const repoName = "base"

	initialYAML := `schema_version: v1
services:
  myapp:
    source: services/myapp
    port: 8080
`
	// Seed the service repo (needed for HEAD resolution).
	createForgejoOrgAndTaggedRepo(t, fURL, token, m7ForgejoUser, "services", "myapp", nil)
	// Seed the config repo.
	createForgejoRepoWithFile(t, fURL, token, m7ForgejoUser, repoName, initialYAML)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ownbase.yaml"), []byte(initialYAML), 0o644); err != nil {
		t.Fatalf("write ownbase.yaml: %v", err)
	}

	cfg := update.Config{
		CheckoutPath: dir,
		ForgejoURL:   fURL,
		ForgejoToken: token,
		ForgejoUser:  m7ForgejoUser,
		RepoName:     repoName,
	}

	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"myapp": {Source: "services/myapp"}, // no ref
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	update.ResolveBlankRefs(ctx, cfg, services, nil)

	// Read the local ownbase.yaml (ResolveBlankRefs commits to Forgejo AND
	// updates the local file via forgejoCommitFile, which commits to the repo
	// but does NOT write back locally — so we check Forgejo instead).
	//
	// Verify via Forgejo: the ownbase.yaml in the repo should now have ref: set.
	contentsURL := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/ownbase.yaml",
		fURL, m7ForgejoUser, repoName)
	req, _ := http.NewRequest(http.MethodGet, contentsURL, nil)
	req.Header.Set("Authorization", "token "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("get ownbase.yaml from Forgejo: %v", err)
	}
	defer resp.Body.Close()
	var fileInfo struct {
		Content string `json:"content"`
	}
	json.NewDecoder(resp.Body).Decode(&fileInfo)

	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(fileInfo.Content, "\n", ""))
	if err != nil {
		t.Fatalf("decode ownbase.yaml content: %v", err)
	}
	content := string(decoded)
	if !strings.Contains(content, "ref:") {
		t.Errorf("expected ref: to be written back, got:\n%s", content)
	}
	t.Logf("ownbase.yaml after ResolveBlankRefs:\n%s", content)
}

// ---------------------------------------------------------------------------
// TestUpdate_BumpDigest_RoundTrip (real YAML file)
// ---------------------------------------------------------------------------

func TestUpdate_BumpDigest_RoundTrip(t *testing.T) {
	requireLinuxUpdate(t)

	original := `schema_version: v1
services:
  forgejo:
    image: codeberg.org/forgejo/forgejo:10
    digest: sha256:old111
    port: 3000
    domain: git.example.com
    health_probe:
      http: /api/healthz
  myapp:
    source: services/myapp
    port: 8080
`
	got, err := update.BumpDigest(original, "forgejo", "sha256:old111", "sha256:new222")
	if err != nil {
		t.Fatalf("BumpDigest: %v", err)
	}
	if !strings.Contains(got, "digest: sha256:new222") {
		t.Errorf("new digest not in output:\n%s", got)
	}
	if !strings.Contains(got, "source: services/myapp") {
		t.Errorf("myapp service missing from output:\n%s", got)
	}
	if !strings.Contains(got, "http: /api/healthz") {
		t.Errorf("health_probe missing from output:\n%s", got)
	}
}
