package main

import (
	"os"
	"path/filepath"
)

// readCaddyfileSnapshot reads repoDir's runtime/Caddyfile — the snapshot a
// previous `ownbased` reconcile cycle wrote to disk — and reports whether it
// exists. Both `ownbasectl plan` and `ownbasectl apply` use this to tell
// reconcile.Diff "already converged" from "never reconciled yet"; without
// it, DiffOptions.CaddyfileSnapshotAvailable defaults to false and every
// plan forces a Caddy reload even when nothing changed (see
// reconcile.DiffOptions).
func readCaddyfileSnapshot(repoDir string) (content string, available bool) {
	raw, err := os.ReadFile(filepath.Join(repoDir, "runtime", "Caddyfile"))
	if err != nil {
		return "", false
	}
	return string(raw), true
}
