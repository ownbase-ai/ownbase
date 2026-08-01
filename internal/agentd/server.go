package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ownbase/ownbase/internal/vault"
)

// Server is the resident agent. One per user; Client.EnsureRunning starts it.
type Server struct {
	version string

	mu          sync.Mutex
	vlt         *vault.Vault
	keyring     agent.Agent
	unlockedAt  time.Time
	idleTimeout time.Duration
	lastUse     time.Time

	controlLn net.Listener
	sshLn     net.Listener
}

// NewServer returns an agent with the vault locked. version is reported by
// status so a stale agent left over from an older install is visible.
func NewServer(version string) *Server {
	return &Server{
		version:     version,
		idleTimeout: DefaultIdleTimeout,
		keyring:     agent.NewKeyring(),
	}
}

// Serve binds both sockets and serves until ctx is cancelled or a shutdown
// request arrives. It refuses to start when another agent already answers on
// the control socket, and clears a socket file left behind by a crash.
//
// probe → unlink → bind is serialized with a flock so two concurrent
// EnsureRunning callers (desktop app + CLI, or two CLIs) cannot both pass the
// dead-socket check and have the loser unlink the winner's live sockets.
func (s *Server) Serve(ctx context.Context) error {
	controlPath, err := ControlSocketPath()
	if err != nil {
		return err
	}
	sshPath, err := SSHAgentSocketPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(controlPath), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(controlPath), err)
	}

	release, err := acquireServeLock()
	if err != nil {
		return err
	}
	// Hold the lock only through bind: once the sockets answer, probe alone
	// serializes later Serves. Releasing here lets a crashed-and-replaced
	// agent recover without waiting on a process that already exited.
	if alive, _ := probe(controlPath); alive {
		release()
		return fmt.Errorf("an OwnBase agent is already running (socket %s)", controlPath)
	}
	// Not alive but present: a crashed agent's socket. Removing it is safe
	// precisely because nothing answered on it — and because we hold the lock,
	// no peer is about to bind the same path.
	_ = os.Remove(controlPath)
	_ = os.Remove(sshPath)

	s.controlLn, err = listenUnix(controlPath)
	if err != nil {
		release()
		return err
	}
	defer func() {
		s.controlLn.Close()
		os.Remove(controlPath)
	}()

	s.sshLn, err = listenUnix(sshPath)
	if err != nil {
		release()
		return err
	}
	defer func() {
		s.sshLn.Close()
		os.Remove(sshPath)
	}()
	release()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go s.acceptLoop(ctx, s.controlLn, func(c net.Conn) { s.handleControl(c, cancel) })
	go s.acceptLoop(ctx, s.sshLn, s.handleSSHAgent)
	go s.autoLockLoop(ctx)

	<-ctx.Done()
	s.lockVault()
	return nil
}

// maxUnixPath is the shortest sockaddr_un.sun_path any platform we target
// allows (macOS: 104, Linux: 108), minus the NUL terminator. The kernel
// truncates silently past this, so bind fails in a way that has nothing to do
// with the actual path — worth naming.
const maxUnixPath = 103

// listenUnix binds a unix socket readable only by its owner. The socket is the
// gate to every credential in the vault, so the permission bits are the whole
// access-control story: any process running as this user may use the agent,
// nothing else may.
func listenUnix(path string) (net.Listener, error) {
	if len(path) > maxUnixPath {
		return nil, fmt.Errorf("socket path is too long for the OS (%d chars, limit %d): %s",
			len(path), maxUnixPath, path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("restrict %s: %w", path, err)
	}
	return ln, nil
}

func (s *Server) acceptLoop(ctx context.Context, ln net.Listener, handle func(net.Conn)) {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handle(conn)
	}
}

// autoLockLoop locks the vault once it has gone unused for idleTimeout.
func (s *Server) autoLockLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			expired := s.vlt != nil && s.idleTimeout > 0 &&
				time.Since(s.lastUse) > s.idleTimeout
			s.mu.Unlock()
			if expired {
				s.lockVault()
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Control protocol
// ---------------------------------------------------------------------------

func (s *Server) handleControl(conn net.Conn, shutdown func()) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	var req Request
	if err := dec.Decode(&req); err != nil {
		_ = writeJSON(enc, Response{Error: "malformed request: " + err.Error()})
		return
	}

	resp := s.dispatch(req, shutdown)
	_ = writeJSON(enc, resp)
}

func (s *Server) dispatch(req Request, shutdown func()) Response {
	switch req.Op {
	case OpStatus:
		return Response{OK: true, Status: s.status()}
	case OpUnlock:
		return s.unlock(req)
	case OpLock:
		s.lockVault()
		return Response{OK: true, Status: s.status()}
	case OpShutdown:
		defer shutdown()
		return Response{OK: true}
	case OpList:
		return s.withVault(func(v *vault.Vault) Response {
			return Response{OK: true, Names: v.Names()}
		})
	case OpGet:
		return s.withVault(func(v *vault.Vault) Response {
			p, err := v.Get(req.Base)
			if err != nil {
				code := ""
				if errors.Is(err, vault.ErrNotFound) {
					code = CodeNotFound
				}
				return Response{Error: err.Error(), Code: code}
			}
			redacted := p.Redacted()
			return Response{OK: true, Profile: &redacted}
		})
	case OpPut:
		if req.Profile == nil {
			return Response{Error: "put requires a profile"}
		}
		return s.withVault(func(v *vault.Vault) Response {
			// A put that omits the private key must not wipe the one on
			// record: every caller reads profiles redacted, so a
			// read-modify-write of an existing Base always arrives without
			// it. Only an explicit key in the request replaces it.
			p := *req.Profile
			if p.PrivateKey == "" {
				if existing, err := v.Get(req.Base); err == nil {
					p.PrivateKey = existing.PrivateKey
					if p.PublicKey == "" {
						p.PublicKey = existing.PublicKey
					}
				}
			}
			v.Put(req.Base, p)
			if err := v.Save(); err != nil {
				return Response{Error: err.Error()}
			}
			s.reloadKeyringLocked(v)
			return Response{OK: true}
		})
	case OpDelete:
		return s.withVault(func(v *vault.Vault) Response {
			v.Delete(req.Base)
			if err := v.Save(); err != nil {
				return Response{Error: err.Error()}
			}
			s.reloadKeyringLocked(v)
			return Response{OK: true}
		})
	case OpChangePassword:
		if req.NewPassword == "" {
			return Response{Error: "change-password requires a new password"}
		}
		return s.withVault(func(v *vault.Vault) Response {
			if err := v.ChangePassword(req.NewPassword); err != nil {
				return Response{Error: err.Error()}
			}
			return Response{OK: true}
		})
	default:
		return Response{Error: fmt.Sprintf("unknown operation %q", req.Op)}
	}
}

// withVault runs fn against the unlocked vault, holding the lock for the whole
// operation so two writers can never interleave a read-modify-write of the
// KDBX file.
func (s *Server) withVault(fn func(*vault.Vault) Response) Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vlt == nil {
		return Response{Error: ErrLocked.Error(), Code: CodeLocked}
	}
	s.lastUse = time.Now()
	return fn(s.vlt)
}

func (s *Server) unlock(req Request) Response {
	path := req.VaultPath
	if path == "" {
		p, err := vault.ResolvePath()
		if err != nil {
			return Response{Error: err.Error()}
		}
		path = p
	}
	v, err := vault.Open(path, req.Password)
	if err != nil {
		code := ""
		if errors.Is(err, vault.ErrWrongPassword) {
			code = CodeWrongPassword
		}
		return Response{Error: err.Error(), Code: code}
	}

	s.mu.Lock()
	s.vlt = v
	s.unlockedAt = time.Now()
	s.lastUse = s.unlockedAt
	if req.IdleTimeoutSeconds >= 0 {
		s.idleTimeout = time.Duration(req.IdleTimeoutSeconds) * time.Second
	}
	s.reloadKeyringLocked(v)
	s.mu.Unlock()

	return Response{OK: true, Status: s.status()}
}

func (s *Server) lockVault() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vlt = nil
	s.unlockedAt = time.Time{}
	s.keyring = agent.NewKeyring()
}

func (s *Server) status() *Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := &Status{
		Running:            true,
		Unlocked:           s.vlt != nil,
		IdleTimeoutSeconds: int(s.idleTimeout.Seconds()),
		PID:                os.Getpid(),
		Version:            s.version,
	}
	if sock, err := SSHAgentSocketPath(); err == nil {
		st.SSHAgentSocket = sock
	}
	if s.vlt != nil {
		st.VaultPath = s.vlt.Path()
		st.Bases = len(s.vlt.Names())
		unlockedAt := s.unlockedAt
		st.UnlockedAt = &unlockedAt
		if keys, err := s.keyring.List(); err == nil {
			st.Keys = len(keys)
		}
		if s.idleTimeout > 0 {
			locksAt := s.lastUse.Add(s.idleTimeout)
			st.LocksAt = &locksAt
		}
	} else if path, err := vault.ResolvePath(); err == nil {
		st.VaultPath = path
	}
	return st
}

// reloadKeyringLocked rebuilds the signing keyring from the vault. Caller holds
// s.mu.
func (s *Server) reloadKeyringLocked(v *vault.Vault) {
	kr := agent.NewKeyring()
	for _, name := range v.Names() {
		p, err := v.Get(name)
		if err != nil || p.PrivateKey == "" {
			continue
		}
		raw, err := ssh.ParseRawPrivateKey([]byte(p.PrivateKey))
		if err != nil {
			continue
		}
		_ = kr.Add(agent.AddedKey{PrivateKey: raw, Comment: "ownbase_" + name})
	}
	s.keyring = kr
}

// ---------------------------------------------------------------------------
// ssh-agent protocol
// ---------------------------------------------------------------------------

func (s *Server) handleSSHAgent(conn net.Conn) {
	defer conn.Close()
	_ = agent.ServeAgent(&readOnlyAgent{s: s}, conn)
}

// readOnlyAgent exposes the vault's owner keys over the ssh-agent protocol for
// signing only. Add/Remove are refused: the vault is the source of truth for
// which keys exist, and a client that could add one could also quietly swap the
// key a Base is reached with.
type readOnlyAgent struct{ s *Server }

func (a *readOnlyAgent) keyring() (agent.Agent, error) {
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	if a.s.vlt == nil {
		return nil, ErrLocked
	}
	a.s.lastUse = time.Now()
	return a.s.keyring, nil
}

func (a *readOnlyAgent) List() ([]*agent.Key, error) {
	kr, err := a.keyring()
	if err != nil {
		return nil, err
	}
	return kr.List()
}

func (a *readOnlyAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	kr, err := a.keyring()
	if err != nil {
		return nil, err
	}
	return kr.Sign(key, data)
}

func (a *readOnlyAgent) Signers() ([]ssh.Signer, error) {
	kr, err := a.keyring()
	if err != nil {
		return nil, err
	}
	return kr.Signers()
}

func (a *readOnlyAgent) Add(agent.AddedKey) error {
	return errors.New("the OwnBase agent serves only keys from the vault; add one with 'ownbasectl keygen'")
}

func (a *readOnlyAgent) Remove(ssh.PublicKey) error {
	return errors.New("remove the Base instead: 'ownbasectl delete <name>'")
}

func (a *readOnlyAgent) RemoveAll() error { return a.Remove(nil) }

func (a *readOnlyAgent) Lock([]byte) error {
	a.s.lockVault()
	return nil
}

func (a *readOnlyAgent) Unlock([]byte) error {
	return errors.New("unlock the vault with 'ownbasectl vault unlock'")
}
