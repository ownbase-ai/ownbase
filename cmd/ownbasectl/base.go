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
)

// defaultOwnerSSHKey reads the caller's default SSH public key, matching the
// convention used throughout OwnBase for registering owner access
// (~/.ssh/id_ed25519.pub, falling back to id_rsa.pub). Returns "" if neither
// is found — callers should treat that as "no key to register" rather than
// a hard failure.
func defaultOwnerSSHKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, name := range []string{"id_ed25519.pub", "id_rsa.pub"} {
		path := filepath.Join(home, ".ssh", name)
		if data, err := os.ReadFile(path); err == nil {
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
