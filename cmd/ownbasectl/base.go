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

// findRepoRoot walks up from the working directory and from this binary's
// location looking for a directory that contains both go.mod and install.sh
// — the OwnBase repo root. Only the dev-build VM path needs it (to
// `go build ./cmd/ownbased` from the checkout); release builds carry
// everything they need embedded.
//
// Searching from the executable matters for the desktop app: its sidecar
// lives at desktop/src-tauri/binaries/ and is not invoked with the repo as
// cwd, but walking up from there still reaches the monorepo root.
func findRepoRoot() (string, error) {
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		starts = append(starts, filepath.Dir(exe))
	}
	seen := map[string]bool{}
	for _, start := range starts {
		if start == "" || seen[start] {
			continue
		}
		seen[start] = true
		if root, ok := walkForRepoRoot(start); ok {
			return root, nil
		}
	}
	return "", fmt.Errorf("could not find the OwnBase repo root (go.mod + install.sh) above %s — run ownbasectl from within the cloned repo, or use a release build", mustGetwd())
}

func walkForRepoRoot(start string) (string, bool) {
	dir := start
	for {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, "install.sh")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
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
