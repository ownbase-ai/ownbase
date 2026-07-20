// Package gitssh centralizes the SSH identity ownbased uses for every
// external git operation — cloning/fetching the config repo (see
// internal/configsource) and each service repo (see internal/repos).
//
// Keys and known_hosts live under /opt/ownbase/ssh, are provisioned by
// `ownbasectl ssh-key`, and are included in restic backups so they survive a
// rebuild. The identity is injected into git via the GIT_SSH_COMMAND
// environment variable rather than relying on an ambient /root/.ssh, which
// would not survive a rebuild.
package gitssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultDir is where ownbased stores its managed SSH identity.
const DefaultDir = "/opt/ownbase/ssh"

// KeyName is the default private key filename within the managed dir.
const KeyName = "id_ed25519"

// Dir returns the managed SSH identity directory.
func Dir() string { return DefaultDir }

// KeyPath returns the path to the default managed private key.
func KeyPath() string { return filepath.Join(DefaultDir, KeyName) }

// PublicKeyPath returns the path to the default managed public key.
func PublicKeyPath() string { return filepath.Join(DefaultDir, KeyName+".pub") }

// KnownHostsPath returns the path to the managed known_hosts file.
func KnownHostsPath() string { return filepath.Join(DefaultDir, "known_hosts") }

// ConfigPath returns the path to the optional managed ssh config file. When
// present it takes precedence over the single-key form, enabling per-repo
// host aliases and deploy keys.
func ConfigPath() string { return filepath.Join(DefaultDir, "config") }

// Command returns the value for GIT_SSH_COMMAND given the managed identity in
// dir, or "" when no managed identity exists (git then falls back to the
// system ssh, which is correct for anonymous https:// repos). When an ssh
// config file is present it takes precedence (supports per-repo host aliases
// / deploy keys); otherwise a single managed key + known_hosts is used.
func Command(dir string) string {
	if dir == "" {
		dir = DefaultDir
	}
	configPath := filepath.Join(dir, "config")
	if fileExists(configPath) {
		return "ssh -F " + shellQuote(configPath) + " -o IdentitiesOnly=yes"
	}
	keyPath := filepath.Join(dir, KeyName)
	if !fileExists(keyPath) {
		return "" // no managed identity
	}
	parts := []string{"ssh", "-o", "IdentitiesOnly=yes", "-i", shellQuote(keyPath)}
	knownHosts := filepath.Join(dir, "known_hosts")
	if fileExists(knownHosts) {
		parts = append(parts, "-o", "UserKnownHostsFile="+shellQuote(knownHosts))
	} else {
		// No pinned hosts yet — accept-new avoids a hard failure on first
		// use while still recording the host key for later verification.
		parts = append(parts, "-o", "StrictHostKeyChecking=accept-new")
	}
	return strings.Join(parts, " ")
}

// Env returns the current process environment with GIT_SSH_COMMAND set for the
// managed identity in DefaultDir, or os.Environ() unchanged when there is no
// managed identity.
func Env() []string { return EnvFor(DefaultDir) }

// EnvFor is Env for an arbitrary identity directory (used by tests).
func EnvFor(dir string) []string {
	env := os.Environ()
	cmd := Command(dir)
	if cmd == "" {
		return env
	}
	return append(env, "GIT_SSH_COMMAND="+cmd)
}

// EnsureKey makes sure a managed ed25519 key pair exists in dir (creating dir
// with 0700 first) and returns the public key. Idempotent: an existing key is
// left untouched. dir defaults to DefaultDir when empty.
func EnsureKey(dir string) (string, error) {
	if dir == "" {
		dir = DefaultDir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	key := filepath.Join(dir, KeyName)
	if !fileExists(key) {
		cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", key, "-C", "ownbase")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("ssh-keygen: %w\n%s", err, out)
		}
	}
	return PublicKey(dir)
}

// PublicKey returns the managed public key in dir, or "" (no error) when no
// key has been generated yet. dir defaults to DefaultDir when empty.
func PublicKey(dir string) (string, error) {
	if dir == "" {
		dir = DefaultDir
	}
	data, err := os.ReadFile(filepath.Join(dir, KeyName+".pub"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// AddKnownHost appends host's SSH host keys (via ssh-keyscan) to the managed
// known_hosts file in dir, skipping entries already present. A no-op when host
// is empty. dir defaults to DefaultDir when empty.
func AddKnownHost(dir, host string) error {
	if strings.TrimSpace(host) == "" {
		return nil
	}
	if dir == "" {
		dir = DefaultDir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	scanned, err := exec.Command("ssh-keyscan", host).Output()
	if err != nil {
		return fmt.Errorf("ssh-keyscan %s: %w", host, err)
	}
	khPath := filepath.Join(dir, "known_hosts")
	existing, _ := os.ReadFile(khPath)
	seen := map[string]bool{}
	for _, ln := range strings.Split(string(existing), "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			seen[s] = true
		}
	}
	out := strings.TrimRight(string(existing), "\n")
	added := false
	for _, ln := range strings.Split(string(scanned), "\n") {
		s := strings.TrimSpace(ln)
		if s == "" || strings.HasPrefix(s, "#") || seen[s] {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += s
		seen[s] = true
		added = true
	}
	if !added && existing != nil {
		return nil
	}
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(khPath, []byte(out), 0o600)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// shellQuote wraps s in single quotes so git's sh-style GIT_SSH_COMMAND
// parsing treats it as one token. Managed paths never contain single quotes.
func shellQuote(s string) string {
	if strings.ContainsAny(s, " \t") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
