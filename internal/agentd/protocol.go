// Package agentd is the OwnBase credential agent: a small resident process
// that holds the unlocked vault in memory so the desktop app, the CLI, and a
// coding agent can all reach a Base without any of them handling the master
// password or touching a private key on disk.
//
// It is modelled on ssh-agent, deliberately. Two unix sockets in ~/.ownbase:
//
//	agent.sock       JSON control protocol (this file) — profiles, lock state
//	ssh-agent.sock   the standard ssh-agent protocol, serving the owner keys
//
// Splitting them is what keeps private keys inside the agent. A caller asking
// for a profile gets the host, port, token, and public key, but never the
// private half: to authenticate it asks the ssh-agent socket to sign a
// challenge. So `ownbasectl status mybase` works with no key material in its
// own address space, and a coding agent driving the CLI never has anything to
// leak.
//
// The agent is started on demand (see Client.EnsureRunning) and outlives the
// process that started it, so the CLI works in a headless shell with the
// desktop app closed. It locks itself after an idle timeout, and the desktop
// app can lock it explicitly on quit.
package agentd

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/ownbase/ownbase/internal/vault"
)

// Socket file names inside ~/.ownbase.
const (
	// ControlSocketName carries the JSON protocol in this file.
	ControlSocketName = "agent.sock"
	// SSHAgentSocketName carries the standard ssh-agent protocol.
	SSHAgentSocketName = "ssh-agent.sock"
	// LogFileName collects the agent's own stderr, since a process started
	// on demand has nowhere else to complain.
	LogFileName = "agent.log"
	// ServeLockName is the flock file that serializes probe→unlink→bind in
	// Serve, so concurrent EnsureRunning callers cannot steal each other's
	// live sockets.
	ServeLockName = "agent.lock"
)

// DefaultIdleTimeout is how long an unlocked vault stays unlocked with nothing
// using it. Long enough that a day of work does not mean retyping the master
// password, short enough that an unattended laptop does not stay unlocked
// forever. Zero disables auto-locking.
const DefaultIdleTimeout = 4 * time.Hour

// Operations understood by the control socket.
const (
	OpStatus         = "status"
	OpUnlock         = "unlock"
	OpLock           = "lock"
	OpList           = "list"
	OpGet            = "get"
	OpPut            = "put"
	OpDelete         = "delete"
	OpChangePassword = "change-password"
	OpShutdown       = "shutdown"
)

// Sentinel errors the client surfaces to the CLI, which maps them to guidance
// and exit codes.
var (
	// ErrNotRunning means no agent is listening on the control socket.
	ErrNotRunning = errors.New("the OwnBase agent is not running")
	// ErrLocked means the agent is up but the vault has not been unlocked.
	ErrLocked = errors.New("the vault is locked")
)

// Request is one control-socket call. Exactly one request per connection,
// JSON-encoded, followed by one Response.
type Request struct {
	Op string `json:"op"`
	// Base names the Base for get/put/delete.
	Base string `json:"base,omitempty"`
	// Profile is the value for put.
	Profile *vault.Profile `json:"profile,omitempty"`
	// Password is the master password for unlock.
	Password string `json:"password,omitempty"`
	// NewPassword is the replacement master password for change-password.
	NewPassword string `json:"new_password,omitempty"`
	// VaultPath overrides the recorded vault location for unlock.
	VaultPath string `json:"vault_path,omitempty"`
	// IdleTimeoutSeconds sets the auto-lock timeout on unlock. Negative
	// leaves the agent's current setting; zero disables auto-locking.
	IdleTimeoutSeconds int `json:"idle_timeout_seconds,omitempty"`
}

// Response is the agent's reply.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Code is a machine-readable error kind: "locked" or "wrong-password".
	// Everything else is a plain message.
	Code string `json:"code,omitempty"`

	Status  *Status        `json:"status,omitempty"`
	Names   []string       `json:"names,omitempty"`
	Profile *vault.Profile `json:"profile,omitempty"`
}

// Error codes carried in Response.Code.
const (
	CodeLocked        = "locked"
	CodeWrongPassword = "wrong-password"
	CodeNotFound      = "not-found"
)

// Status describes the agent's current state. It carries no secrets, so it is
// safe to render in the desktop app or print with --json.
type Status struct {
	// Running is always true in a reply — a status call that reaches the
	// agent proves it. The client sets it false when nothing answered.
	Running bool `json:"running"`
	// Unlocked reports whether the vault is open.
	Unlocked bool `json:"unlocked"`
	// VaultPath is the vault file the agent has open, or the recorded
	// location while locked.
	VaultPath string `json:"vault_path,omitempty"`
	// Bases is how many Base profiles the vault holds (0 while locked).
	Bases int `json:"bases"`
	// Keys is how many owner SSH keys are loaded for signing.
	Keys int `json:"keys"`
	// UnlockedAt is when the vault was opened.
	UnlockedAt *time.Time `json:"unlocked_at,omitempty"`
	// IdleTimeoutSeconds is the auto-lock timeout; 0 means never.
	IdleTimeoutSeconds int `json:"idle_timeout_seconds"`
	// LocksAt is when the vault will auto-lock if nothing touches it.
	LocksAt *time.Time `json:"locks_at,omitempty"`
	// PID is the agent process id, so a stuck agent can be found and killed.
	PID int `json:"pid"`
	// SSHAgentSocket is the path to export as SSH_AUTH_SOCK if the user
	// wants plain `ssh` to use their owner keys.
	SSHAgentSocket string `json:"ssh_agent_socket,omitempty"`
	// Version is the agent binary's version string.
	Version string `json:"version,omitempty"`
}

// ControlSocketPath returns the control socket location.
func ControlSocketPath() (string, error) { return vault.StatePath(ControlSocketName) }

// SSHAgentSocketPath returns the ssh-agent socket location.
func SSHAgentSocketPath() (string, error) { return vault.StatePath(SSHAgentSocketName) }

// LogPath returns the agent log location.
func LogPath() (string, error) { return vault.StatePath(LogFileName) }

// writeJSON encodes v followed by a newline, so the reader can stop at a
// message boundary without closing the connection first.
func writeJSON(enc *json.Encoder, v any) error { return enc.Encode(v) }
