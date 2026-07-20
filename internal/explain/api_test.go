package explain_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/explain"
	"github.com/ownbase/ownbase/internal/secrets"
)

// buildAPIServer creates a StatusServer + APIConfig + httptest.Server for API
// tests. secretsDir may be empty (secrets tests will fail without it).
// ageKeyPath may be empty (token tests work without it).
func buildAPIServer(t *testing.T, secretsDir, ageKeyPath, tokenPath string) (*explain.StatusServer, *httptest.Server) {
	t.Helper()
	const tok = "test-api-token"
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	statusHandler := srv.Handler(tok)
	mux.Handle("/status", statusHandler)
	mux.Handle("/health", statusHandler)
	explain.MountAPI(mux, explain.APIConfig{
		SecretsDir:   secretsDir,
		AgeKeyPath:   ageKeyPath,
		APITokenPath: tokenPath,
		StatusSrv:    srv,
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return srv, ts
}

func authedGet(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer test-api-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func authedPost(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-api-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func authedDelete(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer test-api-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// /config
// ---------------------------------------------------------------------------

func TestAPI_ConfigGet_ReturnsContent(t *testing.T) {
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("test-api-token"))
	const yamlContent = "schema_version: v1\nservices: {}\n"
	explain.MountAPI(mux, explain.APIConfig{
		StatusSrv: srv,
		GetConfig: func() (string, error) { return yamlContent, nil },
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp := authedGet(t, ts, "/config")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != yamlContent {
		t.Errorf("body = %q, want %q", body, yamlContent)
	}
}

func TestAPI_ConfigGet_501WhenNotConfigured(t *testing.T) {
	_, ts := buildAPIServer(t, "", "", "")

	resp := authedGet(t, ts, "/config")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

func TestAPI_ConfigSet_405_WriteRemoved(t *testing.T) {
	// POST /config is no longer a write path — config mutations are made
	// client-side and applied via POST /reconcile. GET is the only method.
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("test-api-token"))
	explain.MountAPI(mux, explain.APIConfig{
		StatusSrv: srv,
		GetConfig: func() (string, error) { return "schema_version: v1\n", nil },
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp := authedPost(t, ts, "/config", `{"content":"x"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 (POST /config removed)", resp.StatusCode)
	}
}

func TestAPI_Config_401WithoutToken(t *testing.T) {
	_, ts := buildAPIServer(t, "", "", "")

	resp, err := http.Get(ts.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// /reconcile
// ---------------------------------------------------------------------------

func TestAPI_Reconcile_CallsReconcile(t *testing.T) {
	called := false
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("test-api-token"))
	explain.MountAPI(mux, explain.APIConfig{
		StatusSrv: srv,
		Reconcile: func() error { called = true; return nil },
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp := authedPost(t, ts, "/reconcile", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if !called {
		t.Error("Reconcile closure was not invoked")
	}
}

func TestAPI_Reconcile_500OnError(t *testing.T) {
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("test-api-token"))
	explain.MountAPI(mux, explain.APIConfig{
		StatusSrv: srv,
		Reconcile: func() error { return fmt.Errorf("fetch failed") },
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp := authedPost(t, ts, "/reconcile", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestAPI_Reconcile_501WhenNotConfigured(t *testing.T) {
	_, ts := buildAPIServer(t, "", "", "")

	resp := authedPost(t, ts, "/reconcile", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// /config/source
// ---------------------------------------------------------------------------

func TestAPI_ConfigSource_RecordsRepoAndRef(t *testing.T) {
	var gotURL, gotRef string
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("test-api-token"))
	explain.MountAPI(mux, explain.APIConfig{
		StatusSrv: srv,
		SetConfigSource: func(repoURL, ref string) error {
			gotURL, gotRef = repoURL, ref
			return nil
		},
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp := authedPost(t, ts, "/config/source", `{"repo_url":"git@github.com:org/config.git","ref":"main"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if gotURL != "git@github.com:org/config.git" {
		t.Errorf("repo_url = %q", gotURL)
	}
	if gotRef != "main" {
		t.Errorf("ref = %q", gotRef)
	}
}

func TestAPI_ConfigSource_400OnEmptyURL(t *testing.T) {
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("test-api-token"))
	explain.MountAPI(mux, explain.APIConfig{
		StatusSrv:       srv,
		SetConfigSource: func(string, string) error { return nil },
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp := authedPost(t, ts, "/config/source", `{"repo_url":""}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// /ssh-key
// ---------------------------------------------------------------------------

func TestAPI_SSHKey_GetReturnsPublicKey(t *testing.T) {
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("test-api-token"))
	explain.MountAPI(mux, explain.APIConfig{
		StatusSrv: srv,
		GetSSHKey: func() (string, error) { return "ssh-ed25519 AAAA...", nil },
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp := authedGet(t, ts, "/ssh-key")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["public_key"] != "ssh-ed25519 AAAA..." {
		t.Errorf("public_key = %q", got["public_key"])
	}
}

func TestAPI_SSHKey_PostEnsuresKeyAndReturnsPublicKey(t *testing.T) {
	var gotHost string
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("test-api-token"))
	explain.MountAPI(mux, explain.APIConfig{
		StatusSrv: srv,
		EnsureSSHKey: func(host string) (string, error) {
			gotHost = host
			return "ssh-ed25519 BBBB...", nil
		},
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp := authedPost(t, ts, "/ssh-key", `{"host":"github.com"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if gotHost != "github.com" {
		t.Errorf("host = %q, want github.com", gotHost)
	}
	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["public_key"] != "ssh-ed25519 BBBB..." {
		t.Errorf("public_key = %q", got["public_key"])
	}
}

func TestAPI_SSHKey_501WhenNotConfigured(t *testing.T) {
	_, ts := buildAPIServer(t, "", "", "")

	resp := authedGet(t, ts, "/ssh-key")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// /core/status
// ---------------------------------------------------------------------------

func TestAPI_CoreStatus_ReturnsPackages(t *testing.T) {
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("test-api-token"))
	explain.MountAPI(mux, explain.APIConfig{
		StatusSrv: srv,
		CoreStatus: func() []explain.CorePackageStatus {
			return []explain.CorePackageStatus{
				{Name: "Caddy", Container: "ownbase-core-caddy", Image: "docker.io/library/caddy:2-alpine", Running: false},
			}
		},
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	resp := authedGet(t, ts, "/core/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	var got struct {
		Packages []explain.CorePackageStatus `json:"packages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Packages) != 1 {
		t.Fatalf("packages = %d, want 1", len(got.Packages))
	}
	if got.Packages[0].Name != "Caddy" || got.Packages[0].Running {
		t.Errorf("package = %+v, want stopped Caddy", got.Packages[0])
	}
}

func TestAPI_CoreStatus_501WhenNotConfigured(t *testing.T) {
	_, ts := buildAPIServer(t, "", "", "")

	resp := authedGet(t, ts, "/core/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

func TestAPI_CoreStatus_401WithoutToken(t *testing.T) {
	_, ts := buildAPIServer(t, "", "", "")

	resp, err := http.Get(ts.URL + "/core/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// /token/reset
// ---------------------------------------------------------------------------

func TestAPI_TokenReset_GeneratesNewToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "api-token")
	if err := os.WriteFile(tokenFile, []byte("old-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv, ts := buildAPIServer(t, "", "", tokenFile)
	_ = srv

	resp := authedPost(t, ts, "/token/reset", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	var got map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	newToken := got["token"]
	if newToken == "" || newToken == "old-token" {
		t.Errorf("new token = %q, want a new non-empty token", newToken)
	}

	// The file should now contain the new token.
	stored, _ := os.ReadFile(tokenFile)
	if strings.TrimSpace(string(stored)) != newToken {
		t.Errorf("token file contains %q, want %q", stored, newToken)
	}
}

func TestAPI_TokenReset_HotSwapsServerToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "api-token")
	_ = os.WriteFile(tokenFile, []byte("old-token"), 0o600)

	_, ts := buildAPIServer(t, "", "", tokenFile)

	// Reset the token.
	resp := authedPost(t, ts, "/token/reset", "")
	defer resp.Body.Close()
	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	newToken := got["token"]

	// The old token should no longer work.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.Header.Set("Authorization", "Bearer test-api-token")
	resp2, _ := http.DefaultClient.Do(req)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("old token still works after reset: status = %d", resp2.StatusCode)
	}

	// The new token should work.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req2.Header.Set("Authorization", "Bearer "+newToken)
	resp3, _ := http.DefaultClient.Do(req2)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("new token rejected after reset: status = %d", resp3.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// SetToken hot-swap (serve.go)
// ---------------------------------------------------------------------------

func TestStatusServer_SetToken_HotSwap(t *testing.T) {
	srv := explain.NewStatusServer()
	ts := httptest.NewServer(srv.Handler("initial-token"))
	defer ts.Close()

	// Initial token works.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req.Header.Set("Authorization", "Bearer initial-token")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("initial token rejected: %d", resp.StatusCode)
	}

	// Hot-swap to a new token.
	srv.SetToken("new-token")

	// Old token must no longer work.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req2.Header.Set("Authorization", "Bearer initial-token")
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("old token still accepted after SetToken: %d", resp2.StatusCode)
	}

	// New token must work.
	req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req3.Header.Set("Authorization", "Bearer new-token")
	resp3, _ := http.DefaultClient.Do(req3)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("new token rejected after SetToken: %d", resp3.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// /secrets — requires age key; only run when fixtures available
// ---------------------------------------------------------------------------

// buildSecretsFixture creates a SecretsDir with an age key and an initial
// encrypted secrets file for service "crm". Returns secretsDir and keyPath.
// No git repo is needed — secrets live outside git.
func buildSecretsFixture(t *testing.T) (secretsDir, keyPath string) {
	t.Helper()
	dir := t.TempDir()

	// Generate an age key.
	keyDir := filepath.Join(dir, "age")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(keyDir, "key.age")
	id, err := secrets.GenerateAndSave(keyPath)
	if err != nil {
		t.Fatalf("generate age key: %v", err)
	}

	// Create the secrets directory and an initial encrypted file for crm.
	secretsDir = filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	initial := map[string]string{"DB_URL": "postgres://localhost/crm", "API_KEY": "key1"}
	ciphertext, err := secrets.EncryptSecrets(id.Recipient(), initial)
	if err != nil {
		t.Fatalf("encrypt initial secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "crm.yaml.age"), ciphertext, 0o600); err != nil {
		t.Fatal(err)
	}

	return secretsDir, keyPath
}

func TestAPI_SecretsList(t *testing.T) {
	secretsDir, keyPath := buildSecretsFixture(t)
	_, ts := buildAPIServer(t, secretsDir, keyPath, "")

	resp := authedGet(t, ts, "/secrets/crm")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	keys, _ := got["keys"].([]any)
	if len(keys) != 2 {
		t.Fatalf("keys = %v, want [API_KEY DB_URL]", keys)
	}
	if keys[0] != "API_KEY" || keys[1] != "DB_URL" {
		t.Errorf("keys = %v, want sorted [API_KEY DB_URL]", keys)
	}
}

func TestAPI_SecretsList_UnknownService_ReturnsEmpty(t *testing.T) {
	secretsDir, keyPath := buildSecretsFixture(t)
	_, ts := buildAPIServer(t, secretsDir, keyPath, "")

	// A service with no secrets file yet returns empty list, not 404.
	// Secrets are independent of ownbase.yaml declarations.
	resp := authedGet(t, ts, "/secrets/nosuchservice")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d (want 200): %s", resp.StatusCode, body)
	}

	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	keys, _ := got["keys"].([]any)
	if len(keys) != 0 {
		t.Errorf("keys = %v, want empty slice", keys)
	}
}

func TestAPI_SecretsGet(t *testing.T) {
	secretsDir, keyPath := buildSecretsFixture(t)
	_, ts := buildAPIServer(t, secretsDir, keyPath, "")

	resp := authedGet(t, ts, "/secrets/crm/DB_URL")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["key"] != "DB_URL" {
		t.Errorf("key = %q, want DB_URL", got["key"])
	}
	if got["value"] != "postgres://localhost/crm" {
		t.Errorf("value = %q", got["value"])
	}
}

func TestAPI_SecretsGet_MissingKey_404(t *testing.T) {
	secretsDir, keyPath := buildSecretsFixture(t)
	_, ts := buildAPIServer(t, secretsDir, keyPath, "")

	resp := authedGet(t, ts, "/secrets/crm/NONEXISTENT_KEY")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAPI_SecretsSet_MergesSecrets(t *testing.T) {
	secretsDir, keyPath := buildSecretsFixture(t)
	_, ts := buildAPIServer(t, secretsDir, keyPath, "")

	resp := authedPost(t, ts, "/secrets/crm", `{"NEW_KEY":"newval","DB_URL":"postgres://newhost/crm"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	// Verify by listing.
	listResp := authedGet(t, ts, "/secrets/crm")
	defer listResp.Body.Close()
	var listGot map[string]any
	_ = json.NewDecoder(listResp.Body).Decode(&listGot)
	keys, _ := listGot["keys"].([]any)
	// Should have API_KEY, DB_URL, NEW_KEY (3 keys).
	if len(keys) != 3 {
		t.Errorf("after set: keys = %v, want 3 keys", keys)
	}

	// Verify DB_URL was updated.
	getResp := authedGet(t, ts, "/secrets/crm/DB_URL")
	defer getResp.Body.Close()
	var getGot map[string]string
	_ = json.NewDecoder(getResp.Body).Decode(&getGot)
	if getGot["value"] != "postgres://newhost/crm" {
		t.Errorf("DB_URL = %q, want postgres://newhost/crm", getGot["value"])
	}
}

func TestAPI_SecretsSet_CreatesFileForNewService(t *testing.T) {
	secretsDir, keyPath := buildSecretsFixture(t)
	_, ts := buildAPIServer(t, secretsDir, keyPath, "")

	// Set secrets for a service that doesn't have a file yet.
	resp := authedPost(t, ts, "/secrets/newservice", `{"FOO":"bar"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	// The file should now exist.
	if _, err := os.Stat(filepath.Join(secretsDir, "newservice.yaml.age")); err != nil {
		t.Errorf("secrets file not created: %v", err)
	}

	// And the key should be retrievable.
	getResp := authedGet(t, ts, "/secrets/newservice/FOO")
	defer getResp.Body.Close()
	var got map[string]string
	_ = json.NewDecoder(getResp.Body).Decode(&got)
	if got["value"] != "bar" {
		t.Errorf("FOO = %q, want bar", got["value"])
	}
}

func TestAPI_SecretsDelete_RemovesKey(t *testing.T) {
	secretsDir, keyPath := buildSecretsFixture(t)
	_, ts := buildAPIServer(t, secretsDir, keyPath, "")

	resp := authedDelete(t, ts, "/secrets/crm/API_KEY")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}

	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["deleted"] != "API_KEY" {
		t.Errorf("deleted = %q, want API_KEY", got["deleted"])
	}

	// Verify the key is gone.
	listResp := authedGet(t, ts, "/secrets/crm")
	defer listResp.Body.Close()
	var listGot map[string]any
	_ = json.NewDecoder(listResp.Body).Decode(&listGot)
	keys, _ := listGot["keys"].([]any)
	if len(keys) != 1 {
		t.Errorf("after delete: keys = %v, want 1 key", keys)
	}
	if keys[0] != "DB_URL" {
		t.Errorf("remaining key = %v, want DB_URL", keys[0])
	}
}

func TestAPI_SecretsDelete_MissingKey_404(t *testing.T) {
	secretsDir, keyPath := buildSecretsFixture(t)
	_, ts := buildAPIServer(t, secretsDir, keyPath, "")

	resp := authedDelete(t, ts, "/secrets/crm/NONEXISTENT")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
