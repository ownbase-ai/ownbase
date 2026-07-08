package bridge_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/bridge"
)

// backend starts a test HTTP server that always responds with body, and
// returns its listener address ("127.0.0.1:port").
func backend(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestNewProxyHandler_DispatchesByHostHeader(t *testing.T) {
	addrA := backend(t, "response-a")
	addrB := backend(t, "response-b")

	handler, err := bridge.NewProxyHandler(map[string]string{
		"a.example.com.localhost": addrA,
		"b.example.com.localhost": addrB,
	})
	if err != nil {
		t.Fatalf("NewProxyHandler: %v", err)
	}

	for host, want := range map[string]string{
		"a.example.com.localhost": "response-a",
		"b.example.com.localhost": "response-b",
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("host %q: status = %d, want 200", host, rec.Code)
		}
		if got := rec.Body.String(); got != want {
			t.Errorf("host %q: body = %q, want %q", host, got, want)
		}
	}
}

func TestNewProxyHandler_HostWithPortStripped(t *testing.T) {
	addr := backend(t, "ok")
	handler, err := bridge.NewProxyHandler(map[string]string{
		"app.example.com.localhost": addr,
	})
	if err != nil {
		t.Fatalf("NewProxyHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "app.example.com.localhost:8443"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

func TestNewProxyHandler_UnknownHostReturns404(t *testing.T) {
	handler, err := bridge.NewProxyHandler(map[string]string{
		"known.example.com.localhost": backend(t, "ok"),
	})
	if err != nil {
		t.Fatalf("NewProxyHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "unknown.example.com.localhost"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestNewProxyHandler_ForwardsRealDomainAsHostHeader(t *testing.T) {
	var gotHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
	}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")

	handler, err := bridge.NewProxyHandler(map[string]string{
		"app.example.com.localhost": addr,
	})
	if err != nil {
		t.Fatalf("NewProxyHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "app.example.com.localhost:8443"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// The backend should see the real production domain (as Caddy would
	// send it), not the local ".localhost:8443" hostname the browser used
	// to reach the dev bridge, and not the tunnel's loopback address.
	if want := "app.example.com"; gotHost != want {
		t.Errorf("backend received Host = %q, want %q", gotHost, want)
	}
}

func TestNewProxyHandler_MultipleHostnamesSameBackend(t *testing.T) {
	addr := backend(t, "shared")
	handler, err := bridge.NewProxyHandler(map[string]string{
		"app.example.com.localhost": addr,
		"app.example.org.localhost": addr,
	})
	if err != nil {
		t.Fatalf("NewProxyHandler: %v", err)
	}

	for _, host := range []string{"app.example.com.localhost", "app.example.org.localhost"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "shared" {
			t.Errorf("host %q: status=%d body=%q, want 200/shared", host, rec.Code, rec.Body.String())
		}
	}
}
