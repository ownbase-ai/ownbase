package githost

// forgejo.go implements the integration between the on-Base bare repo and
// Forgejo (the hosted Git UX). The bare repo stays authoritative; Forgejo is
// a downstream mirror that the agent syncs to after each successful reconcile.
//
// Data flow:
//
//	agent reconciles → SyncToForgejo (best-effort push to Forgejo remote)
//	user creates PR in Forgejo → agent polls API (M7) → merges into bare repo
//	bare repo post-receive hook → SIGUSR1 → agent reconciles
//
// If Forgejo is stopped or removed, the bare-repo commit loop (M4a) continues
// unaffected. Forgejo is never in the critical path of reconcile.

import (
	"bytes"
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const (
	// ForgejoRemoteName is the git remote name added to the bare repo.
	ForgejoRemoteName = "forgejo"

	// DefaultForgejoAdminUser is the initial admin username created during setup.
	DefaultForgejoAdminUser = "ownbased"

	// DefaultForgejoRepoName is the repository created in Forgejo that mirrors
	// the bare repo.
	DefaultForgejoRepoName = "ownbase"
)

// ForgejoConfig holds the connection parameters for a Forgejo instance.
type ForgejoConfig struct {
	// BaseURL is the Forgejo HTTP base URL (e.g. "http://localhost:3000").
	BaseURL string
	// AdminToken is a Forgejo API token with full access.
	AdminToken string
	// RepoOwner is the Forgejo user or org that owns the mirror repo.
	RepoOwner string
	// RepoName is the name of the mirror repo (default: DefaultForgejoRepoName).
	RepoName string
}

// AddForgejoRemote adds (or updates) the Forgejo push remote in the bare repo.
// The bare repo gets a "forgejo" remote pointing at Forgejo's HTTP clone URL.
// Authentication is configured via a per-remote git extraHeader stored in the
// repo's local config — the token never appears in the remote URL (M14).
//
// This is idempotent: calling it again with the same URL is a no-op.
func AddForgejoRemote(repoPath string, cfg ForgejoConfig) error {
	repoName := cfg.RepoName
	if repoName == "" {
		repoName = DefaultForgejoRepoName
	}
	// Plain URL: token is passed via git config, not the URL.
	cloneURL := fmt.Sprintf("%s/%s/%s.git", cfg.BaseURL, cfg.RepoOwner, repoName)

	// Remove existing remote (if any) so we can set a fresh URL.
	_ = exec.Command("git", "-C", repoPath, "remote", "remove", ForgejoRemoteName).Run()

	out, err := exec.Command("git", "-C", repoPath, "remote", "add",
		ForgejoRemoteName, cloneURL).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git remote add forgejo: %w\n%s", err, out)
	}

	if cfg.AdminToken != "" {
		// Store auth as a URL-scoped extraHeader in the repo's local git config.
		// This means the token never appears in the remote URL or in `git remote -v`.
		configOut, err := exec.Command("git", "-C", repoPath,
			"config", "--local",
			"http."+cfg.BaseURL+"/.extraHeader",
			"Authorization: token "+cfg.AdminToken,
		).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git config extraHeader: %w\n%s", err, configOut)
		}
	}
	return nil
}

// SyncToForgejo pushes all refs from the bare repo to the Forgejo remote.
// This is called after each successful reconcile to keep Forgejo up-to-date.
//
// SyncToForgejo is best-effort: errors are logged but do not fail the
// reconcile. Forgejo being down must never block the core reconcile loop.
func SyncToForgejo(repoPath string) error {
	out, err := exec.Command("git", "-C", repoPath,
		"push", ForgejoRemoteName, "--all", "--force").CombinedOutput()
	if err != nil {
		return fmt.Errorf("sync to forgejo: git push: %w\n%s", err, out)
	}
	return nil
}

// SyncBareRepoFromForgejo fetches the latest commits from the Forgejo `base`
// repo into the local bare repo. This is the inverse of SyncToForgejo and is
// called at the start of each reconcile so that user pushes to Forgejo (the
// front-door git host) are reflected in the bare repo before UpdateCheckout.
//
// Authentication is passed via GIT_CONFIG_COUNT/KEY/VALUE env vars so the
// token never appears in the URL or in /proc/<pid>/cmdline (M14).
//
// Idempotent and non-fatal on an empty Forgejo repo.
func SyncBareRepoFromForgejo(repoPath, baseURL, token, owner, repoName string) error {
	if repoName == "" {
		repoName = DefaultForgejoRepoName
	}

	// Plain fetch URL — no token embedded.
	fetchURL := strings.TrimSuffix(baseURL, "/") + "/" + owner + "/" + repoName + ".git"

	// Fetch main:main with a 30-second timeout so git doesn't hang waiting
	// on a port-forwarding proxy that accepted the TCP connection but isn't
	// yet forwarding traffic to the container.
	//
	// git fetch spawns git-remote-http(s) as a subprocess. exec.CommandContext
	// only kills the direct child when the context expires; the spawned helper
	// keeps running. We use Setpgid to put git in its own process group and
	// kill the entire group from a watcher goroutine so that git-remote-http
	// is also terminated when the deadline fires.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.Command("git", "-C", repoPath,
		"fetch", fetchURL, "refs/heads/main:refs/heads/main",
		"--update-head-ok",
	)
	// Pass auth via env so the token never appears in argv or in the fetch URL.
	if token != "" {
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: token "+token,
		)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Watcher goroutine: kill the entire process group when the deadline fires.
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			// Negative PID targets the process group, killing git-remote-http too.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch forgejo→bare: %w\n%s", err, scrubGitToken(string(out), token))
	}
	return nil
}

// scrubGitToken removes all occurrences of token from s, and also strips the
// "user:token@" form that git may embed in error output. Safe to call when
// token is empty.
func scrubGitToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<redacted>")
}

// WaitForForgejo polls the Forgejo readiness endpoint until it responds 200
// or the timeout expires. Used after starting the Forgejo container to wait
// for it to be healthy before calling the API.
//
// containerName is the Podman container name used as a fallback: when the
// host-port URL (baseURL) is not yet reachable (netavark DNAT rules set up
// asynchronously), the container IP is tried directly. Pass the exact
// container name that was started (e.g. "ownbase-core-forgejo" for
// production, "ownbase-forgejo-integtest" for integration tests) so the
// fallback polls the correct container and not a coincidentally-running
// Forgejo instance.
//
// The endpoint used is /api/healthz (Forgejo 7+).
func WaitForForgejo(baseURL, containerName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	healthURL := baseURL + "/api/healthz"
	client := &http.Client{Timeout: 3 * time.Second}

	for time.Now().Before(deadline) {
		// Try the main URL first.
		resp, err := client.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		// Fallback: try the container IP directly (bypasses port forwarding).
		if ip := forgejoContainerIP(containerName); ip != "" {
			if resp2, err2 := client.Get("http://" + ip + ":3000/api/healthz"); err2 == nil {
				resp2.Body.Close()
				if resp2.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("forgejo at %s did not become healthy within %s", baseURL, timeout)
}

// forgejoContainerIP returns the IP address of the named Podman container.
// Returns "" if the container is not running or podman is unavailable.
func forgejoContainerIP(containerName string) string {
	out, err := exec.Command(
		"podman", "inspect", "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}",
		containerName,
	).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// CreateForgejoAdmin creates the initial admin user in Forgejo by running the
// forgejo CLI inside the container. This is called once during initial setup.
//
// containerName is the Podman container name (e.g. "ownbase-forgejo").
//
// The forgejo CLI must run as the "git" user inside the container (it refuses
// to run as root). Use --user git in the podman exec invocation.
func CreateForgejoAdmin(containerName, username, password, email string) error {
	out, err := exec.Command(
		"podman", "exec", "--user", "git", containerName,
		"forgejo", "admin", "user", "create",
		"--admin",
		"--username", username,
		"--password", password,
		"--email", email,
		"--must-change-password=false",
	).CombinedOutput()
	if err != nil {
		// "user already exists" is not an error for idempotency.
		if bytes.Contains(out, []byte("already exists")) {
			return nil
		}
		return fmt.Errorf("create forgejo admin: %w\n%s", err, out)
	}
	return nil
}

// GenerateForgejoToken creates an API token for the admin user and returns it.
// Used during initial setup so the agent can make API calls.
//
// The forgejo CLI must run as the "git" user (see CreateForgejoAdmin).
//
// Scopes follow Forgejo 1.19+ fine-grained token model:
//   - write:user — create repos under the user account
//   - write:repository — push, webhooks
//   - write:issue — create/merge PRs (M7)
//   - read:organization — read org membership
func GenerateForgejoToken(containerName, username, tokenName string) (string, error) {
	out, err := exec.Command(
		"podman", "exec", "--user", "git", containerName,
		"forgejo", "admin", "user", "generate-access-token",
		"--username", username,
		"--token-name", tokenName,
		"--scopes", "write:user,write:repository,write:issue,read:organization,write:admin",
		"--raw",
	).Output()
	if err != nil {
		return "", fmt.Errorf("generate forgejo token: %w", err)
	}
	return string(bytes.TrimSpace(out)), nil
}

// CreateForgejoRepo creates the mirror repository in Forgejo via the API.
// Returns the HTTP clone URL of the created repo. Idempotent: if the repo
// already exists, returns its URL without error.
func CreateForgejoRepo(cfg ForgejoConfig) (string, error) {
	repoName := cfg.RepoName
	if repoName == "" {
		repoName = DefaultForgejoRepoName
	}

	// auto_init: true lets Forgejo create an initial commit and set HEAD
	// on the default branch. Without this, Forgejo's branch metadata
	// is not populated until after the first push, causing the /branches
	// API to return null even when refs are present on disk.
	body, _ := json.Marshal(map[string]any{
		"name":           repoName,
		"description":    "OwnBase authoritative configuration repository",
		"private":        true,
		"auto_init":      true,
		"default_branch": "main",
	})

	url := fmt.Sprintf("%s/api/v1/user/repos", cfg.BaseURL)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create forgejo repo: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+cfg.AdminToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("create forgejo repo: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// 201 = created, 409 = already exists (both OK).
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		return "", fmt.Errorf("create forgejo repo: unexpected status %d: %s",
			resp.StatusCode, respBody)
	}

	cloneURL := fmt.Sprintf("%s/%s/%s.git",
		cfg.BaseURL, cfg.RepoOwner, repoName)
	return cloneURL, nil
}

// SetupForgejo performs the full initial Forgejo setup:
//  1. Wait for Forgejo to be healthy.
//  2. Create the admin user (via podman exec forgejo admin).
//  3. Generate an API token.
//  4. Create the mirror repository.
//  5. Add Forgejo as a push remote in the bare repo.
//  6. Push all refs from the bare repo to Forgejo.
//
// SetupForgejo is idempotent: calling it on an already-set-up instance
// re-confirms the remote and attempts a push.
func SetupForgejo(
	repoPath string,
	containerName string,
	baseURL string,
	adminPassword string,
) error {
	const timeout = 2 * time.Minute
	if err := WaitForForgejo(baseURL, containerName, timeout); err != nil {
		return fmt.Errorf("setup forgejo: wait: %w", err)
	}

	if err := CreateForgejoAdmin(containerName, DefaultForgejoAdminUser, adminPassword,
		"agent@ownbase.local"); err != nil {
		return fmt.Errorf("setup forgejo: create admin: %w", err)
	}

	token, err := GenerateForgejoToken(containerName, DefaultForgejoAdminUser, "ownbased")
	if err != nil {
		return fmt.Errorf("setup forgejo: generate token: %w", err)
	}

	cfg := ForgejoConfig{
		BaseURL:    baseURL,
		AdminToken: token,
		RepoOwner:  DefaultForgejoAdminUser,
		RepoName:   DefaultForgejoRepoName,
	}

	if _, err := CreateForgejoRepo(cfg); err != nil {
		return fmt.Errorf("setup forgejo: create repo: %w", err)
	}

	if err := AddForgejoRemote(repoPath, cfg); err != nil {
		return fmt.Errorf("setup forgejo: add remote: %w", err)
	}

	if err := SyncToForgejo(repoPath); err != nil {
		return fmt.Errorf("setup forgejo: initial sync: %w", err)
	}

	return nil
}

// ConfigureForgejoWebhook registers a push webhook on the Forgejo repo so
// that pushes to the hosted repo notify the OwnBase daemon. The daemon uses this
// to re-read the authoritative bare repo and trigger reconcile, giving users
// the experience of "push to Forgejo → reconcile happens".
//
// webhookURL is typically "http://localhost:<agent-port>/api/v1/hook/push".
// Idempotent: existing webhooks with the same URL are not duplicated.
func ConfigureForgejoWebhook(cfg ForgejoConfig, webhookURL string) error {
	repoName := cfg.RepoName
	if repoName == "" {
		repoName = DefaultForgejoRepoName
	}

	// List existing webhooks to avoid duplicates.
	existing, err := forgejoListWebhooks(cfg, repoName)
	if err == nil {
		for _, hookURL := range existing {
			if hookURL == webhookURL {
				return nil // already configured
			}
		}
	}

	body, _ := json.Marshal(map[string]any{
		"type":   "gitea",
		"active": true,
		"events": []string{"push"},
		"config": map[string]string{
			"url":          webhookURL,
			"content_type": "json",
		},
	})

	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/hooks", cfg.BaseURL, cfg.RepoOwner, repoName)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("configure webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+cfg.AdminToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("configure webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("configure webhook: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// SeedBaseRepo creates or updates the ownbase.yaml file in the Forgejo repo
// with the provided content. Used during initial bootstrap to give the user
// a commented template to start from.
//
// Idempotent: if ownbase.yaml already has user content (non-template), this
// is a no-op to avoid overwriting the user's configuration.
func SeedBaseRepo(cfg ForgejoConfig, ownbaseYAML string) error {
	repoName := cfg.RepoName
	if repoName == "" {
		repoName = DefaultForgejoRepoName
	}

	// Check if ownbase.yaml already exists and has non-template content.
	sha, _ := forgejoGetFileContent(cfg, repoName, "ownbase.yaml", "main")
	// If the file already exists, skip seeding to preserve user content.
	if sha != "" {
		return nil
	}

	payload := map[string]any{
		"message": "init: seed ownbase.yaml template",
		"content": encodeBase64Content([]byte(ownbaseYAML)),
		"branch":  "main",
	}
	body, _ := json.Marshal(payload)

	apiPath := fmt.Sprintf("/api/v1/repos/%s/%s/contents/ownbase.yaml", cfg.RepoOwner, repoName)
	url := cfg.BaseURL + apiPath
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("seed base repo: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+cfg.AdminToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("seed base repo: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("seed base repo: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// autoInitReadmeMarker identifies the stub README that Forgejo's auto_init
// creates from the repo description. Only that stub is ever replaced.
const autoInitReadmeMarker = "OwnBase authoritative configuration repository"

// SeedRepoReadme replaces the auto-init stub README.md in the config repo
// with the full operating guide. Idempotent and conservative:
//   - README.md missing            → create it.
//   - README.md is the auto stub   → replace it.
//   - README.md already seeded, or edited by the user → no-op.
func SeedRepoReadme(cfg ForgejoConfig, readme string) error {
	repoName := cfg.RepoName
	if repoName == "" {
		repoName = DefaultForgejoRepoName
	}

	sha, existing, err := forgejoGetFileWithContent(cfg, repoName, "README.md", "main")
	if err != nil {
		return fmt.Errorf("seed readme: read existing: %w", err)
	}
	if sha != "" {
		isStub := len(existing) < 200 && strings.Contains(existing, autoInitReadmeMarker)
		if !isStub {
			return nil // already seeded or user-edited — never overwrite
		}
	}

	payload := map[string]any{
		"message": "init: seed repo operating guide (README.md)",
		"content": encodeBase64Content([]byte(readme)),
		"branch":  "main",
	}
	method := "POST"
	if sha != "" {
		method = "PUT" // update requires the current blob SHA
		payload["sha"] = sha
	}
	body, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/README.md", cfg.BaseURL, cfg.RepoOwner, repoName)
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("seed readme: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+cfg.AdminToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("seed readme: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("seed readme: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// forgejoGetFileWithContent returns the blob SHA and decoded content of a file
// in the repo, or ("", "", nil) when the file does not exist.
func forgejoGetFileWithContent(cfg ForgejoConfig, repoName, path, ref string) (string, string, error) {
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s?ref=%s",
		cfg.BaseURL, cfg.RepoOwner, repoName, path, ref)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "token "+cfg.AdminToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", "", nil
	}
	var result struct {
		SHA     string `json:"sha"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", err
	}
	// Forgejo wraps base64 at 60 chars with newlines; strip them before decoding.
	cleaned := strings.ReplaceAll(result.Content, "\n", "")
	decoded, err := b64.StdEncoding.DecodeString(cleaned)
	if err != nil {
		return result.SHA, "", nil // content undecodable — treat as unknown, not stub
	}
	return result.SHA, string(decoded), nil
}

func forgejoListWebhooks(cfg ForgejoConfig, repoName string) ([]string, error) {
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/hooks",
		cfg.BaseURL, cfg.RepoOwner, repoName)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+cfg.AdminToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var hooks []struct {
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&hooks); err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(hooks))
	for _, h := range hooks {
		urls = append(urls, h.Config.URL)
	}
	return urls, nil
}

func forgejoGetFileContent(cfg ForgejoConfig, repoName, path, ref string) (string, error) {
	url := fmt.Sprintf("%s/api/v1/repos/%s/%s/contents/%s?ref=%s",
		cfg.BaseURL, cfg.RepoOwner, repoName, path, ref)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "token "+cfg.AdminToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	var result struct {
		SHA string `json:"sha"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result.SHA, nil
}

// encodeBase64Content encodes data to base64 for Forgejo file content API.
func encodeBase64Content(data []byte) string {
	return b64.StdEncoding.EncodeToString(data)
}

// CreateForgejoMirror creates a pull-mirror in Forgejo for an external git URL.
// The mirror is created under the given owner with the path mirrors/<repoName>.
// Forgejo will periodically pull from the external URL and update the local repo.
//
// interval is the sync interval (e.g. "8h0m0s"). Idempotent: if a repo with
// the given name already exists under mirrors/, this is a no-op.
func CreateForgejoMirror(cfg ForgejoConfig, externalURL, repoName, interval string) error {
	// Attempt to GET the repo first; if it exists, skip creation.
	// repoName is the full desired repo name (e.g. "mirrors-postgres");
	// no additional prefix is added here — the caller provides the final name.
	getURL := fmt.Sprintf("%s/api/v1/repos/%s/%s", cfg.BaseURL, cfg.RepoOwner, repoName)
	req, _ := http.NewRequest("GET", getURL, nil)
	req.Header.Set("Authorization", "token "+cfg.AdminToken)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil // mirror already exists
		}
	}

	if interval == "" {
		interval = "8h0m0s"
	}
	body, _ := json.Marshal(map[string]any{
		"clone_addr":      externalURL,
		"mirror":          true,
		"mirror_interval": interval,
		"repo_name":       repoName,
		"repo_owner":      cfg.RepoOwner,
		"private":         false,
		"auth_token":      "",
		"wiki":            false,
		"pull_requests":   false,
		"releases":        false,
		"issues":          false,
	})

	apiURL := fmt.Sprintf("%s/api/v1/repos/migrate", cfg.BaseURL)
	req2, err := http.NewRequest("POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create forgejo mirror %s: build request: %w", repoName, err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "token "+cfg.AdminToken)

	// Use a long timeout: Forgejo performs the initial git clone synchronously
	// before returning. Large repos (e.g. docker-library/postgres) take 1–3 min.
	migrateClient := &http.Client{Timeout: 5 * time.Minute}
	resp2, err := migrateClient.Do(req2)
	if err != nil {
		return fmt.Errorf("create forgejo mirror %s: %w", repoName, err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusCreated && resp2.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp2.Body)
		return fmt.Errorf("create forgejo mirror %s: status %d: %s", repoName, resp2.StatusCode, b)
	}
	return nil
}

// MirrorForgejoRepoName derives the Forgejo repo name for an external mirror URL.
// Uses the same basename convention as compiler.MirrorForgejoPath but returns
// just the repo name (without the "mirrors/" owner prefix used as the path).
// The repo is created under cfg.RepoOwner with name "mirrors-<basename>".
func MirrorForgejoRepoName(externalURL string) string {
	u := externalURL
	for _, suffix := range []string{"/"} {
		u = trimRight(u, suffix)
	}
	u = trimSuffix(u, ".git")
	if idx := indexOf(u, ":"); idx >= 0 && !containsSlice(u[:idx], "/") {
		u = u[idx+1:]
	}
	if idx := lastIndexOf(u, "/"); idx >= 0 {
		u = u[idx+1:]
	}
	return "mirrors-" + u
}

// Small string helpers to avoid importing strings just for this.
func trimRight(s, cutset string) string {
	for len(s) > 0 && len(cutset) > 0 && s[len(s)-1] == cutset[0] {
		s = s[:len(s)-1]
	}
	return s
}

func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func lastIndexOf(s, substr string) int {
	for i := len(s) - len(substr); i >= 0; i-- {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func containsSlice(s, substr string) bool {
	return indexOf(s, substr) >= 0
}

// UpdateForgejoUserPassword sets the password for a Forgejo user via the
// admin API. Idempotent: calling it again with the same password is harmless.
func UpdateForgejoUserPassword(cfg ForgejoConfig, username, password string) error {
	body, _ := json.Marshal(map[string]any{
		"login_name":           username,
		"source_id":            0,
		"password":             password,
		"must_change_password": false,
	})
	url := fmt.Sprintf("%s/api/v1/admin/users/%s", cfg.BaseURL, username)
	req, err := http.NewRequest("PATCH", url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("update forgejo password: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "token "+cfg.AdminToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("update forgejo password: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update forgejo password: status %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// RegisterForgejoSSHKey registers an SSH public key for the authenticated user.
// Idempotent: if a key with identical content already exists the call is skipped.
func RegisterForgejoSSHKey(cfg ForgejoConfig, pubKey, title string) error {
	// List existing keys to check for duplicates.
	listURL := fmt.Sprintf("%s/api/v1/user/keys", cfg.BaseURL)
	req, _ := http.NewRequest("GET", listURL, nil)
	req.Header.Set("Authorization", "token "+cfg.AdminToken)
	client := &http.Client{Timeout: 10 * time.Second}
	if resp, err := client.Do(req); err == nil {
		defer resp.Body.Close()
		var keys []struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&keys); err == nil {
			trimmed := strings.TrimSpace(pubKey)
			for _, k := range keys {
				if strings.TrimSpace(k.Key) == trimmed {
					return nil // already registered
				}
			}
		}
	}

	if title == "" {
		title = "owner"
	}
	body, _ := json.Marshal(map[string]any{
		"key":       pubKey,
		"read_only": false,
		"title":     title,
	})
	req2, err := http.NewRequest("POST", listURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("register forgejo ssh key: build request: %w", err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "token "+cfg.AdminToken)

	resp2, err := client.Do(req2)
	if err != nil {
		return fmt.Errorf("register forgejo ssh key: %w", err)
	}
	defer resp2.Body.Close()
	respBody, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusCreated {
		return fmt.Errorf("register forgejo ssh key: status %d: %s", resp2.StatusCode, respBody)
	}
	return nil
}
