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

	grants := authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "opencode", Scopes: []string{authz.ScopeStatusRead}},
	})
	m := explain.NewSocketManager(dir, mux, grants)
	defer m.Close()

	if err := m.Sync(map[string][]string{"opencode": {authz.ScopeStatusRead}}); err != nil {
		t.Fatal(err)
	}

	sock := filepath.Join(dir, "opencode.sock")
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

	// Service without status:read cannot read status.
	grants2 := authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "other", Scopes: []string{"backup:run"}},
	})
	m.SetGrants(grants2)
	if err := m.Sync(map[string][]string{"other": {"backup:run"}}); err != nil {
		t.Fatal(err)
	}
	otherSock := filepath.Join(dir, "other.sock")
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
