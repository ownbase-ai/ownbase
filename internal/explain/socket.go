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

// DefaultSocketDir is the host directory for per-service API socket dirs.
// Each service gets <dir>/<service>/api.sock so containers can bind-mount the
// directory (stable inode) rather than the socket file itself.
const DefaultSocketDir = "/run/ownbase/svc"

// SocketFileName is the socket basename inside each per-service directory.
const SocketFileName = "api.sock"

// SocketManager serves the daemon HTTP API on one unix socket per service
// that has ownbase_access. The socket path is the credential: whoever can
// connect is that service principal. Scopes gate every route (default-deny).
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

// Dir returns the host root for per-service socket directories.
func (m *SocketManager) Dir() string {
	if m == nil {
		return DefaultSocketDir
	}
	return m.dir
}

// ServiceDir is the host directory bind-mounted into a service container.
func ServiceDir(root, service string) string {
	return filepath.Join(root, service)
}

// ServiceSocketPath is the host path of a service's API socket file.
func ServiceSocketPath(root, service string) string {
	return filepath.Join(root, service, SocketFileName)
}

// SetGrants replaces the grant table used for scope checks.
func (m *SocketManager) SetGrants(g *authz.GrantCheckpoint) {
	m.mu.Lock()
	m.grants = g
	m.mu.Unlock()
}

// EnsureBaseDir creates the root socket directory so compilers can bind-mount
// per-service subdirs even before the first Sync.
func (m *SocketManager) EnsureBaseDir() error {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return fmt.Errorf("create socket dir %s: %w", m.dir, err)
	}
	return nil
}

// Sync brings listeners in line with the given service→scopes map.
// Services not in want are closed; new ones are opened. Per-service directories
// are created before listen so container directory binds always have a source.
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
		_ = os.Remove(ServiceSocketPath(m.dir, name))
	}

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return fmt.Errorf("create socket dir %s: %w", m.dir, err)
	}

	for name := range want {
		svcDir := ServiceDir(m.dir, name)
		if err := os.MkdirAll(svcDir, 0o755); err != nil {
			return fmt.Errorf("create service socket dir %s: %w", svcDir, err)
		}
		if _, ok := m.listeners[name]; ok {
			continue
		}
		path := ServiceSocketPath(m.dir, name)
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

// Close stops every listener and removes socket files (directories remain so
// bind-mounts stay valid until containers stop).
func (m *SocketManager) Close() {
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, ln := range m.listeners {
		_ = ln.Close()
		_ = os.Remove(ServiceSocketPath(m.dir, name))
		delete(m.listeners, name)
	}
}

func (m *SocketManager) serve(service string, ln net.Listener) {
	principal := schema.Service(service)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Inject principal before any auth check.
			r = WithPrincipal(r, principal)
			// Default-deny: every route needs an explicit grant scope.
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

	access := authz.RouteAccess(r.Method, r.URL.Path)
	if access.AlwaysOK {
		return true
	}
	// Owner-only routes refuse every service principal, including "*".
	if access.OwnerOnly || access.Scope == "" {
		return false
	}
	return authz.ScopeAllowed(scopes, access.Scope)
}
