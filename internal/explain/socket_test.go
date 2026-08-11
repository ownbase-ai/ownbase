package explain_test

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/explain"
)

func TestSocketManager_PrincipalAndStatusScope(t *testing.T) {
	// macOS sun_path is ~104 bytes; t.TempDir() paths are often longer.
	dir, err := os.MkdirTemp("/tmp", "ob-sock-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		p, ok := explain.PrincipalFromContext(r.Context())
		if !ok || p.String() != "service:opencode" {
			http.Error(w, "bad principal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/config/source", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/token/reset", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/self-update", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/secrets/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/backup/run", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	grants := authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "opencode", Scopes: []string{authz.ScopeStatusRead}},
	})
	m := explain.NewSocketManager(dir, mux, grants)
	defer m.Close()

	if err := m.Sync(map[string][]string{"opencode": {authz.ScopeStatusRead}}); err != nil {
		t.Fatal(err)
	}

	sock := explain.ServiceSocketPath(dir, "opencode")
	waitUnix(t, sock)

	client := unixHTTPClient(sock)
	resp, err := client.Get("http://localhost/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body %s", resp.StatusCode, body)
	}

	// status:read alone must not reach owner-only or ungranted routes.
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPost, "/config/source"},
		{http.MethodPost, "/token/reset"},
		{http.MethodPost, "/self-update"},
		{http.MethodGet, "/secrets/myapp"},
		{http.MethodPost, "/backup/run"},
		{http.MethodGet, "/config"},
	} {
		req, err := http.NewRequest(tc.method, "http://localhost"+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		r.Body.Close()
		if r.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", tc.method, tc.path, r.StatusCode)
		}
	}

	// Service without status:read cannot read status.
	grants2 := authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "other", Scopes: []string{"backup:run"}},
	})
	m.SetGrants(grants2)
	if err := m.Sync(map[string][]string{"other": {"backup:run"}}); err != nil {
		t.Fatal(err)
	}
	otherSock := explain.ServiceSocketPath(dir, "other")
	waitUnix(t, otherSock)
	otherClient := unixHTTPClient(otherSock)
	resp2, err := otherClient.Get("http://localhost/status")
	if err != nil {
		t.Fatalf("GET /status other: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp2.StatusCode)
	}

	// backup:run is allowed for other.
	req, err := http.NewRequest(http.MethodPost, "http://localhost/backup/run", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp3, err := otherClient.Do(req)
	if err != nil {
		t.Fatalf("POST /backup/run: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("backup/run = %d, want 200", resp3.StatusCode)
	}

	// "*" still cannot call owner-only routes.
	star := authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "star", Scopes: []string{"*"}},
	})
	m.SetGrants(star)
	if err := m.Sync(map[string][]string{"star": {"*"}}); err != nil {
		t.Fatal(err)
	}
	starSock := explain.ServiceSocketPath(dir, "star")
	waitUnix(t, starSock)
	starClient := unixHTTPClient(starSock)
	req, err = http.NewRequest(http.MethodPost, "http://localhost/config/source", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp4, err := starClient.Do(req)
	if err != nil {
		t.Fatalf("POST /config/source with *: %v", err)
	}
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusForbidden {
		t.Errorf("* on /config/source = %d, want 403", resp4.StatusCode)
	}
}

func TestSocketManager_DirectoryLayout(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ob-sock-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	m := explain.NewSocketManager(dir, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "web", Scopes: []string{authz.ScopeStatusRead}},
	}))
	defer m.Close()

	if err := m.Sync(map[string][]string{"web": {authz.ScopeStatusRead}}); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "web", "api.sock")
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("expected socket at %s: %v", sock, err)
	}
	// Directory must exist as bind-mount source even after we only Sync once.
	if st, err := os.Stat(filepath.Join(dir, "web")); err != nil || !st.IsDir() {
		t.Fatalf("service dir: %v", err)
	}
}

func waitUnix(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.Dial("unix", sock)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket not ready: %s: %v", sock, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func unixHTTPClient(sock string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Dial: func(_, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
		Timeout: 2 * time.Second,
	}
}
