package agentd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ownbase/ownbase/internal/vault"
)

// dialTimeout bounds a connect to the local socket. It is loopback IPC, so
// anything slower than this means a wedged agent, not a slow network.
const dialTimeout = 3 * time.Second

// Client talks to the resident agent over the control socket.
type Client struct {
	socket string
}

// NewClient returns a client for the standard control socket location.
func NewClient() (*Client, error) {
	sock, err := ControlSocketPath()
	if err != nil {
		return nil, err
	}
	return &Client{socket: sock}, nil
}

// Socket returns the control socket path.
func (c *Client) Socket() string { return c.socket }

// call sends one request and returns the reply. A refused connection is
// reported as ErrNotRunning so callers can offer to start the agent rather
// than printing a socket error.
func (c *Client) call(req Request) (*Response, error) {
	conn, err := net.DialTimeout("unix", c.socket, dialTimeout)
	if err != nil {
		return nil, ErrNotRunning
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send %s to the agent: %w", req.Op, err)
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read the agent's reply to %s: %w", req.Op, err)
	}
	if !resp.OK {
		switch resp.Code {
		case CodeLocked:
			return &resp, ErrLocked
		case CodeWrongPassword:
			return &resp, vault.ErrWrongPassword
		case CodeNotFound:
			return &resp, fmt.Errorf("%w: %s", vault.ErrNotFound, resp.Error)
		}
		return &resp, errors.New(resp.Error)
	}
	return &resp, nil
}

// Status reports the agent's state. A stopped agent is not an error — it is a
// state the desktop app and `vault status` both need to render — so the
// returned Status has Running false.
func (c *Client) Status() (*Status, error) {
	resp, err := c.call(Request{Op: OpStatus})
	if errors.Is(err, ErrNotRunning) {
		st := &Status{Running: false}
		if path, perr := vault.ResolvePath(); perr == nil {
			st.VaultPath = path
		}
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	return resp.Status, nil
}

// Unlock opens the vault in the agent. vaultPath may be empty to use the
// recorded location. idleTimeout of -1 leaves the agent's current setting.
func (c *Client) Unlock(vaultPath, password string, idleTimeout time.Duration) (*Status, error) {
	secs := -1
	if idleTimeout >= 0 {
		secs = int(idleTimeout.Seconds())
	}
	resp, err := c.call(Request{
		Op:                 OpUnlock,
		VaultPath:          vaultPath,
		Password:           password,
		IdleTimeoutSeconds: secs,
	})
	if err != nil {
		return nil, err
	}
	return resp.Status, nil
}

// Lock closes the vault, dropping the master password and every key from
// memory.
func (c *Client) Lock() error {
	_, err := c.call(Request{Op: OpLock})
	if errors.Is(err, ErrNotRunning) {
		return nil // already as locked as it gets
	}
	return err
}

// Shutdown stops the agent process.
func (c *Client) Shutdown() error {
	_, err := c.call(Request{Op: OpShutdown})
	if errors.Is(err, ErrNotRunning) {
		return nil
	}
	return err
}

// List returns the configured Base names.
func (c *Client) List() ([]string, error) {
	resp, err := c.call(Request{Op: OpList})
	if err != nil {
		return nil, err
	}
	return resp.Names, nil
}

// Get returns a Base's profile with the private key removed.
func (c *Client) Get(base string) (vault.Profile, error) {
	resp, err := c.call(Request{Op: OpGet, Base: base})
	if err != nil {
		return vault.Profile{}, err
	}
	if resp.Profile == nil {
		return vault.Profile{}, fmt.Errorf("the agent returned no profile for %q", base)
	}
	return *resp.Profile, nil
}

// Put stores a Base's profile and persists the vault. Leaving PrivateKey empty
// keeps whatever key is already on record.
func (c *Client) Put(base string, p vault.Profile) error {
	_, err := c.call(Request{Op: OpPut, Base: base, Profile: &p})
	return err
}

// Delete removes a Base's profile and its owner key.
func (c *Client) Delete(base string) error {
	_, err := c.call(Request{Op: OpDelete, Base: base})
	return err
}

// ChangePassword re-encrypts the vault under a new master password.
func (c *Client) ChangePassword(newPassword string) error {
	_, err := c.call(Request{Op: OpChangePassword, NewPassword: newPassword})
	return err
}

// SSHAgentSocket returns the path of the agent's ssh-agent socket, which is
// how callers authenticate without ever holding a private key.
func (c *Client) SSHAgentSocket() (string, error) { return SSHAgentSocketPath() }

// EnsureRunning starts the agent if nothing is listening, and returns once the
// socket answers.
//
// The child is detached from this process (its own session, no controlling
// terminal, output to ~/.ownbase/agent.log) so the agent survives the shell
// that happened to trigger it. That is what makes `ownbasectl status` work the
// same whether the desktop app is open or the user is in a headless SSH
// session: whoever needs the agent first starts it, and everyone after that
// finds it already up.
func EnsureRunning(exePath string) error {
	sock, err := ControlSocketPath()
	if err != nil {
		return err
	}
	if alive, _ := probe(sock); alive {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(sock), err)
	}

	if exePath == "" {
		exePath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("locate the ownbasectl binary to start the agent: %w", err)
		}
	}

	logPath, err := LogPath()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.Command(exePath, "agent", "run")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the OwnBase agent: %w", err)
	}
	// Not waited on deliberately: the agent outlives us. Release the child
	// so it is reparented to init instead of lingering as a zombie.
	go func() { _ = cmd.Process.Release() }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if alive, _ := probe(sock); alive {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the OwnBase agent did not come up within 10s — see %s", logPath)
}

// probe reports whether an agent answers a status call on the given socket.
func probe(socket string) (bool, *Status) {
	conn, err := net.DialTimeout("unix", socket, dialTimeout)
	if err != nil {
		return false, nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	if err := json.NewEncoder(conn).Encode(Request{Op: OpStatus}); err != nil {
		return false, nil
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return false, nil
	}
	return resp.OK, resp.Status
}
