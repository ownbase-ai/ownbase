// Package serverconfig manages the ownbasectl client configuration file.
//
// The config file lives at ~/.ownbase/config and holds named server profiles.
// Each profile describes how to reach one Base: SSH host, user, key, and the
// cached Bearer token for the agent API.
//
// File format (YAML):
//
//	servers:
//	  mybase:
//	    host: mybase.example.com
//	    ssh_user: ubuntu
//	    ssh_key: ~/.ssh/id_ed25519
//	    api_port: 7070
//	    token: abc123...
package serverconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultConfigDir is the directory containing the ownbasectl config file.
	DefaultConfigDir = ".ownbase"
	// DefaultConfigFile is the name of the config file within DefaultConfigDir.
	DefaultConfigFile = "config"

	// DefaultSSHUser is used when ssh_user is not set in the profile.
	DefaultSSHUser = "ubuntu"
	// DefaultSSHKey is used when ssh_key is not set in the profile.
	DefaultSSHKey = "~/.ssh/id_ed25519"
	// DefaultAPIPort is the port the agent status API listens on.
	DefaultAPIPort = 7070
)

// ServerProfile holds connection details for one Base.
type ServerProfile struct {
	// Host is the SSH hostname or IP address of the Base.
	Host string `yaml:"host"`
	// SSHUser is the login user for SSH. Defaults to "ubuntu".
	SSHUser string `yaml:"ssh_user,omitempty"`
	// SSHKey is the path to the SSH private key file. Supports ~ expansion.
	// Defaults to ~/.ssh/id_ed25519.
	SSHKey string `yaml:"ssh_key,omitempty"`
	// APIPort is the port the agent API listens on (default 7070).
	APIPort int `yaml:"api_port,omitempty"`
	// SSHPort is the SSH server port (default 22). Set when the Base runs SSH
	// on a non-standard port.
	SSHPort int `yaml:"ssh_port,omitempty"`
	// Token is the Bearer token cached after first connect. Empty means the
	// profile has not been authenticated yet.
	Token string `yaml:"token,omitempty"`
	// LocalVM marks a profile as backed by a local Multipass VM (created by
	// `create` with no --remote), as opposed to a remote server
	// (`--remote`). Used by `delete`/`list` to decide whether it is safe to
	// also touch a Multipass VM of the same name.
	//
	// A pointer so it can represent three states, not two: nil means
	// unknown — profiles registered before this field existed leave it
	// unset, and must still be treated as "might be a local VM" (fall back
	// to checking Multipass) so those older Bases can still be torn down
	// with `delete`. Only an explicit false (set by `create --remote`)
	// means "known remote", which must never trigger a Multipass lookup —
	// that is what protects against destroying a coincidentally
	// same-named local VM.
	LocalVM *bool `yaml:"local_vm,omitempty"`

	// DevTLSDomain is the base domain (e.g. "mybase.test") for which
	// `ownbasectl create` generated a mkcert wildcard certificate and wrote
	// an /etc/hosts block. Empty means this Base does not use dev-TLS —
	// either it was created with --no-dev-tls, is a --remote Base, or
	// mkcert was unavailable at create time and dev-TLS was skipped.
	// `ownbasectl vm start/restart`, `dev-tls sync`, and `delete` all use
	// this field to know whether there is an /etc/hosts block to maintain
	// or remove for this Base.
	DevTLSDomain string `yaml:"dev_tls_domain,omitempty"`
}

// KnownLocalVM reports whether the profile is definitely backed by a local
// Multipass VM (LocalVM explicitly true).
func (p ServerProfile) KnownLocalVM() bool {
	return p.LocalVM != nil && *p.LocalVM
}

// KnownRemote reports whether the profile is definitely a remote server
// (LocalVM explicitly false). A profile with LocalVM unset (nil) — e.g. a
// legacy profile registered before this field existed — is NOT considered
// known-remote, so callers fall back to checking Multipass directly instead
// of assuming it is safe to skip.
func (p ServerProfile) KnownRemote() bool {
	return p.LocalVM != nil && !*p.LocalVM
}

// EffectiveSSHUser returns the resolved ssh_user, applying the default.
func (p ServerProfile) EffectiveSSHUser() string {
	if p.SSHUser != "" {
		return p.SSHUser
	}
	return DefaultSSHUser
}

// EffectiveSSHKey returns the resolved ssh_key path with ~ expanded.
func (p ServerProfile) EffectiveSSHKey() string {
	key := p.SSHKey
	if key == "" {
		key = DefaultSSHKey
	}
	return expandTilde(key)
}

// EffectiveAPIPort returns the resolved api_port, applying the default.
func (p ServerProfile) EffectiveAPIPort() int {
	if p.APIPort > 0 {
		return p.APIPort
	}
	return DefaultAPIPort
}

// EffectiveSSHPort returns the resolved ssh_port, applying the default (22).
func (p ServerProfile) EffectiveSSHPort() int {
	if p.SSHPort > 0 {
		return p.SSHPort
	}
	return 22
}

// Config is the top-level ownbasectl configuration.
type Config struct {
	// Servers is the map of named Base profiles.
	Servers map[string]ServerProfile `yaml:"servers,omitempty"`
}

// ProfileFor returns the named profile. name must be non-empty — every
// command that targets a Base takes its name as a required positional
// argument, so there is no "default Base" to fall back to.
func (c *Config) ProfileFor(name string) (ServerProfile, error) {
	if name == "" {
		return ServerProfile{}, errors.New("no Base name given")
	}
	p, ok := c.Servers[name]
	if !ok {
		return ServerProfile{}, fmt.Errorf("Base %q not found; run: ownbasectl list", name)
	}
	return p, nil
}

// DefaultConfigPath returns the canonical path to the config file.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, DefaultConfigDir, DefaultConfigFile), nil
}

// Load reads the config file at path. If the file does not exist, an empty
// Config is returned (no error). Returns an error for other I/O or parse
// failures.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{Servers: make(map[string]ServerProfile)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Servers == nil {
		cfg.Servers = make(map[string]ServerProfile)
	}
	return &cfg, nil
}

// Save writes cfg to path, creating the parent directory (mode 0700) if
// needed. The file is written with mode 0600 since it contains tokens.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// expandTilde replaces a leading ~ with the user's home directory.
func expandTilde(path string) string {
	if len(path) == 0 || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
