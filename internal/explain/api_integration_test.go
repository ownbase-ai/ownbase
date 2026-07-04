//go:build integration

package explain_test

// Tier-2 integration tests for the M15 remote access API.
// Run with: go test -tags=integration ./internal/explain/... -v -run TestAPI_Integration
//
// These tests exercise the full secrets round-trip path without any git
// involvement. Secrets are stored at a conventional path on the filesystem.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/explain"
	"github.com/ownbase/ownbase/internal/secrets"
)

func TestAPI_Integration_SecretsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatal(err)
	}

	keyPath := filepath.Join(dir, "age-key.age")
	id, err := secrets.GenerateAndSave(keyPath)
	if err != nil {
		t.Fatalf("generate age key: %v", err)
	}

	// Create an initial encrypted secrets file for myapp.
	initial := map[string]string{"SECRET_A": "val_a"}
	ct, err := secrets.EncryptSecrets(id.Recipient(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "myapp.yaml.age"), ct, 0o600); err != nil {
		t.Fatal(err)
	}

	// Wire the server.
	const tok = "integration-token"
	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler(tok))
	mux.Handle("/health", srv.Handler(tok))
	explain.MountAPI(mux, explain.APIConfig{
		SecretsDir: secretsDir,
		AgeKeyPath: keyPath,
		StatusSrv:  srv,
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	authed := func(method, path, body string) *http.Response {
		var req *http.Request
		if body != "" {
			req, _ = http.NewRequest(method, ts.URL+path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req, _ = http.NewRequest(method, ts.URL+path, nil)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	// 1. List initial secrets.
	r := authed(http.MethodGet, "/secrets/myapp", "")
	var list map[string]any
	json.NewDecoder(r.Body).Decode(&list)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("list: status %d", r.StatusCode)
	}
	keys, _ := list["keys"].([]any)
	if len(keys) != 1 || keys[0] != "SECRET_A" {
		t.Fatalf("initial keys = %v, want [SECRET_A]", keys)
	}

	// 2. Get the value.
	r2 := authed(http.MethodGet, "/secrets/myapp/SECRET_A", "")
	var getResp map[string]string
	json.NewDecoder(r2.Body).Decode(&getResp)
	r2.Body.Close()
	if getResp["value"] != "val_a" {
		t.Errorf("value = %q, want val_a", getResp["value"])
	}

	// 3. Set a new key and update an existing one.
	r3 := authed(http.MethodPost, "/secrets/myapp", `{"SECRET_B":"val_b","SECRET_A":"updated_a"}`)
	r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Fatalf("set: status %d", r3.StatusCode)
	}

	// 4. Verify the merge.
	r4 := authed(http.MethodGet, "/secrets/myapp", "")
	var list2 map[string]any
	json.NewDecoder(r4.Body).Decode(&list2)
	r4.Body.Close()
	keys2, _ := list2["keys"].([]any)
	if len(keys2) != 2 {
		t.Fatalf("after set: keys = %v, want [SECRET_A SECRET_B]", keys2)
	}

	r5 := authed(http.MethodGet, "/secrets/myapp/SECRET_A", "")
	var get2 map[string]string
	json.NewDecoder(r5.Body).Decode(&get2)
	r5.Body.Close()
	if get2["value"] != "updated_a" {
		t.Errorf("SECRET_A = %q, want updated_a", get2["value"])
	}

	// 5. Delete SECRET_B.
	r6 := authed(http.MethodDelete, "/secrets/myapp/SECRET_B", "")
	r6.Body.Close()
	if r6.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", r6.StatusCode)
	}

	// 6. Confirm deletion.
	r7 := authed(http.MethodGet, "/secrets/myapp", "")
	var list3 map[string]any
	json.NewDecoder(r7.Body).Decode(&list3)
	r7.Body.Close()
	keys3, _ := list3["keys"].([]any)
	if len(keys3) != 1 || keys3[0] != "SECRET_A" {
		t.Fatalf("after delete: keys = %v, want [SECRET_A]", keys3)
	}

	// 7. List a service with no file — should return empty, not error.
	r8 := authed(http.MethodGet, "/secrets/nonexistent", "")
	var list4 map[string]any
	json.NewDecoder(r8.Body).Decode(&list4)
	r8.Body.Close()
	if r8.StatusCode != http.StatusOK {
		t.Fatalf("list nonexistent: status %d", r8.StatusCode)
	}
	keys4, _ := list4["keys"].([]any)
	if len(keys4) != 0 {
		t.Errorf("nonexistent keys = %v, want empty", keys4)
	}
}

func TestAPI_Integration_TokenReset(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "api-token")
	if err := os.WriteFile(tokenFile, []byte("initial-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	srv := explain.NewStatusServer()
	mux := http.NewServeMux()
	mux.Handle("/status", srv.Handler("initial-token"))
	mux.Handle("/health", srv.Handler("initial-token"))
	explain.MountAPI(mux, explain.APIConfig{
		APITokenPath: tokenFile,
		StatusSrv:    srv,
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Reset the token.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/token/reset", nil)
	req.Header.Set("Authorization", "Bearer initial-token")
	resp, _ := http.DefaultClient.Do(req)
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	newToken := got["token"]

	if newToken == "" || newToken == "initial-token" {
		t.Fatalf("token not rotated: %q", newToken)
	}

	// Old token no longer works.
	req2, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req2.Header.Set("Authorization", "Bearer initial-token")
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("old token still accepted: %d", resp2.StatusCode)
	}

	// New token works.
	req3, _ := http.NewRequest(http.MethodGet, ts.URL+"/status", nil)
	req3.Header.Set("Authorization", "Bearer "+newToken)
	resp3, _ := http.DefaultClient.Do(req3)
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("new token rejected: %d", resp3.StatusCode)
	}
}
