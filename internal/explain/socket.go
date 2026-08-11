package explain

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/schema"
)

// DefaultSocketDir is the host directory for per-service API sockets.
const DefaultSocketDir = "/run/ownbase/svc"

// SocketManager serves the daemon HTTP API on one unix socket per service
// that has ownbase_access. The socket path is the credential: whoever can
// connect is that service principal.
type SocketManager struct {
	dir     string
	handler http.Handler
	grants  *authz.GrantCheckpoint

	mu        sync.Mutex
	listeners map[string]net.Listener // service → listener
	cancel    context.CancelFunc
	ctx       context.Context
}

// NewSocketManager returns a manager that has not started listening yet.
// handler is typically the same mux as the TCP status API.
func NewSocketManager(dir string, handler http.Handler, grants *authz.GrantCheckpoint) *SocketManager {
	if dir == "" {
		dir = DefaultSocketDir
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &SocketManager{
		dir:       dir,
		handler:   handler,
		grants:    grants,
		listeners: make(map[string]net.Listener),
		cancel:    cancel,
		ctx:       ctx,
	}
}

// SetGrants replaces the grant table used for scope checks on status.
func (m *SocketManager) SetGrants(g *authz.GrantCheckpoint) {
	m.mu.Lock()
	m.grants = g
	m.mu.Unlock()
}

// Sync brings listeners in line with the given service→scopes map.
// Services not in want are closed; new ones are opened.
func (m *SocketManager) Sync(want map[string][]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close removed.
	for name, ln := range m.listeners {
		if _, ok := want[name]; ok {
			continue
		}
		_ = ln.Close()
		delete(m.listeners, name)
		_ = os.Remove(filepath.Join(m.dir, name+".sock"))
	}

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return fmt.Errorf("create socket dir %s: %w", m.dir, err)
	}

	for name := range want {
		if _, ok := m.listeners[name]; ok {
			continue
		}
		path := filepath.Join(m.dir, name+".sock")
		_ = os.Remove(path) // stale socket from a previous process
		ln, err := net.Listen("unix", path)
		if err != nil {
			return fmt.Errorf("listen %s: %w", path, err)
		}
		// 0666 so containers running as non-root can connect; the directory
		// and the fact that only declared services get a socket are the ACL.
		if err := os.Chmod(path, 0o666); err != nil {
			ln.Close()
			return fmt.Errorf("chmod %s: %w", path, err)
		}
		m.listeners[name] = ln
		go m.serve(name, ln)
	}
	return nil
}

// Close stops every listener.
func (m *SocketManager) Close() {
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, ln := range m.listeners {
		_ = ln.Close()
		_ = os.Remove(filepath.Join(m.dir, name+".sock"))
		delete(m.listeners, name)
	}
}

func (m *SocketManager) serve(service string, ln net.Listener) {
	principal := schema.Service(service)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Inject principal before any auth check.
			r = WithPrincipal(r, principal)
			// Gate status/config reads on declared scopes.
			if !m.socketAllowed(service, r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			m.handler.ServeHTTP(w, r)
		}),
	}
	go func() {
		<-m.ctx.Done()
		_ = srv.Close()
	}()
	_ = srv.Serve(ln)
}

func (m *SocketManager) socketAllowed(service string, r *http.Request) bool {
	m.mu.Lock()
	grants := m.grants
	m.mu.Unlock()
	if grants == nil {
		return false
	}
	scopes := grants.ScopesFor(service)
	if len(scopes) == 0 {
		return false
	}
	// Health is always ok on the socket (liveness for the service itself).
	if r.URL.Path == "/health" {
		return true
	}
	if r.URL.Path == "/status" || r.URL.Path == "/status/" {
		return authz.ScopeAllowed(scopes, authz.ScopeStatusRead) || authz.ScopeAllowed(scopes, "*")
	}
	if r.URL.Path == "/config" || r.URL.Path == "/config/" {
		return authz.ScopeAllowed(scopes, authz.ScopeConfigRead) || authz.ScopeAllowed(scopes, "*")
	}
	// Mutating routes: allow the connection through; handlers should still
	// check grants when they perform taxonomy actions. Presence of any grant
	// is enough to reach the mux; dangerous ops remain unmapped in
	// scopeForAction and will fail if a checkpoint is wired later.
	return true
}
