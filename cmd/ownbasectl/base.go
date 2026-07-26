package main

// base.go holds small helpers shared by the Base lifecycle commands
// (create, adopt, restore, list, delete) — the Go-driven replacement for
// the old Makefile/shell setup path (testing/smoke-install.sh,
// make connect-vm). The local VM path (Multipass, via internal/vmhost) and
// the remote server path share as much orchestration as possible so that
// "set up a Base" is one mental model regardless of where it runs.

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"

	"github.com/ownbase/ownbase/internal/serverconfig"
)

// ownerKeyPath returns the conventional location of the per-Base owner key
// that `ownbasectl keygen <name>` writes. One key per Base means retiring a
// Base revokes exactly one credential.
func ownerKeyPath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".ssh", "ownbase_"+name), nil
}

// resolveOwnerKey picks the private key ownbasectl uses to reach the Base
// named name, in precedence order: an explicit --ssh-key, then the per-Base
// key from `keygen`, then the user's default key. The returned path keeps its
// ~ prefix so it stays portable when written into ~/.ownbase/config; callers
// that need a real path expand it via ServerProfile.EffectiveSSHKey.
func resolveOwnerKey(name, flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if path, err := ownerKeyPath(name); err == nil && fileExists(path) {
		return filepath.Join("~", ".ssh", "ownbase_"+name)
	}
	return serverconfig.DefaultSSHKey
}

// expandKeyPath resolves a possibly ~-prefixed key path to a real filesystem
// path.
func expandKeyPath(path string) string {
	return serverconfig.ServerProfile{SSHKey: path}.EffectiveSSHKey()
}

// ownerPublicKey returns the authorized_keys line for the private key at
// privKeyPath — the key ownbasectl will actually authenticate with. It is
// derived from the private key itself wherever possible, so the key we
// install into the server's authorized_keys can never drift from the key we
// connect with.
//
// Falls back to the adjacent .pub file (needed when the private key is
// passphrase-protected and cannot be parsed unattended), then to the
// historical ~/.ssh/id_ed25519.pub / id_rsa.pub scan. Returns "" when no key
// can be found, which callers treat as "nothing to register" rather than a
// hard failure — SSH-agent-only setups legitimately have no key file here.
func ownerPublicKey(privKeyPath string) string {
	path := expandKeyPath(privKeyPath)

	if data, err := os.ReadFile(path); err == nil {
		if signer, perr := ssh.ParsePrivateKey(data); perr == nil {
			return authorizedKeyLine(signer.PublicKey(), filepath.Base(path))
		}
	}
	if data, err := os.ReadFile(path + ".pub"); err == nil {
		return string(trimSpaceBytes(data))
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub"} {
		if data, err := os.ReadFile(filepath.Join(home, ".ssh", name)); err == nil {
			return string(trimSpaceBytes(data))
		}
	}
	return ""
}

// trimSpaceBytes trims leading/trailing ASCII whitespace, including the
// trailing newline all SSH public key files end with.
func trimSpaceBytes(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpaceByte(b[start]) {
		start++
	}
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// findRepoRoot walks up from the current working directory looking for a
// directory that contains both go.mod and install.sh — the OwnBase repo
// root. Only the dev-build VM path needs it (to `go build ./cmd/ownbased`
// from the checkout); release builds carry everything they need embedded.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, "install.sh")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find the OwnBase repo root (go.mod + install.sh) above %s — run ownbasectl from within the cloned repo", mustGetwd())
		}
		dir = parent
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}
