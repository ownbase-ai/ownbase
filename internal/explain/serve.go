package explain

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// StatusServer caches the most recently gathered BaseStatus and serves it
// over HTTP. It is the Base <-> opterm API seam defined in M8.
//
// The /status endpoint requires a Bearer token when Token is non-empty.
// The /health endpoint is always public — opterm uses it for liveness checks.
//
// Callers replace the cached status by calling Update after each reconcile.
// The Bearer token can be hot-swapped at runtime via SetToken.
type StatusServer struct {
	mu     sync.RWMutex
	status *BaseStatus
	token  string // Bearer token; empty = no auth
}

// NewStatusServer returns a StatusServer with a minimal initial status.
func NewStatusServer() *StatusServer {
	return &StatusServer{
		status: &BaseStatus{
			GeneratedAt:   time.Now().UTC(),
			SchemaVersion: StatusSchemaVersion,
		},
	}
}

// Update replaces the cached status atomically. Safe for concurrent use.
// The Updates field from the previous status is preserved so that drift data
// written by SetUpdates is not lost when a vuln scan or reconcile triggers a
// full status refresh before the update ticker runs again.
func (s *StatusServer) Update(st *BaseStatus) {
	s.mu.Lock()
	st.Updates = s.status.Updates
	s.status = st
	s.mu.Unlock()
}

// Get returns the current cached status. Safe for concurrent use.
func (s *StatusServer) Get() *BaseStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// SetUpdates merges the latest drift data into the cached status atomically.
// Safe for concurrent use. Called by the agent's update-interval ticker so
// drift is available between reconcile cycles without requiring a full Gather.
func (s *StatusServer) SetUpdates(u UpdateStatus) {
	s.mu.Lock()
	s.status.Updates = u
	s.mu.Unlock()
}

// SetToken updates the Bearer token required to access /status. Passing an
// empty string disables authentication. Safe for concurrent use; takes effect
// on the next request without restarting the server.
func (s *StatusServer) SetToken(token string) {
	s.mu.Lock()
	s.token = token
	s.mu.Unlock()
}

// currentToken returns the active Bearer token. Safe for concurrent use.
func (s *StatusServer) currentToken() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// Handler returns an http.Handler for the status API. The token argument sets
// the initial Bearer token; an empty string disables authentication. The token
// can be changed at runtime via SetToken without restarting the server.
func (s *StatusServer) Handler(token string) http.Handler {
	s.SetToken(token)
	mux := http.NewServeMux()

	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if tok := s.currentToken(); tok != "" {
			if !bearerTokenValid(r.Header.Get("Authorization"), tok) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		st := s.Get()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(st); err != nil {
			// Header already sent; best effort.
			fmt.Fprintf(w, `{"error":"encode failed"}`)
		}
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"ok":true}`)
	})

	return mux
}

// ListenAndServe starts the HTTP server at addr. Blocks until the server
// returns an error (typically http.ErrServerClosed). Wrap in a goroutine and
// use an *http.Server with Shutdown for graceful teardown:
//
//	srv := &http.Server{Addr: addr, Handler: statusSrv.Handler(token)}
//	go srv.ListenAndServe()
//	// ...
//	srv.Shutdown(ctx)
func (s *StatusServer) ListenAndServe(addr, token string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(token),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// WriteTimeout is 0 (no limit) — the daemon is an internal API. Some
		// action endpoints (e.g. /security/fix) stream output from long-running
		// commands; a fixed write deadline would cut them off mid-stream.
		// ReadHeaderTimeout is sufficient to protect against slow-header attacks.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}
	return srv.ListenAndServe()
}

// bearerTokenValid checks whether the Authorization header contains a valid
// Bearer token. The "Bearer" scheme keyword is matched case-insensitively;
// the secret token itself is compared in constant time to prevent timing
// oracles. Returns false when header is empty or the token does not match.
func bearerTokenValid(header, secret string) bool {
	const prefix = "bearer "
	if len(header) <= len(prefix) {
		return false
	}
	// Only the scheme keyword is case-insensitive; the token is byte-exact.
	if !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := header[len(prefix):]
	// Constant-time comparison prevents timing oracles on the secret.
	return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}
