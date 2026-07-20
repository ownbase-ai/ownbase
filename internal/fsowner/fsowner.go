// Package fsowner grants the on-Base admin SSH user (the same account
// ownbasectl and the operator log in as — root on most remote servers,
// ubuntu on a local Multipass VM) access to the local bare service clones
// the daemon creates as root.
//
// The daemon fetches each service's repo: into a bare clone under
// /opt/ownbase/repos/<name> while running as root (see install.sh's systemd
// unit). A root-owned, non-group-writable tree is untouchable by any other
// account, including one with full sudo rights (sudo affects command
// execution, not an existing SSH session's file permissions); chowning it to
// the admin user keeps those trees inspectable over the normal SSH login.
package fsowner

import (
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// Chown recursively changes the owner of every file and directory under
// path to username. A no-op when username is empty — callers pass "" when
// no admin user is configured (e.g. a local dev/test build with no
// installer run), in which case the repo simply stays root-owned and only
// root (or the daemon's own API-based commit path) can write to it.
//
// Errors looking up the user, or a partial chown failure partway through
// the tree, are returned to the caller, which should treat them as
// non-fatal (log and retry on the next reconcile tick) — a Base still
// functions correctly without this; it only means a direct git-over-SSH
// push to that specific repo won't work until it succeeds.
func Chown(path, username string) error {
	if username == "" {
		return nil
	}
	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("lookup user %q: %w", username, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return fmt.Errorf("parse uid %q for %q: %w", u.Uid, username, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return fmt.Errorf("parse gid %q for %q: %w", u.Gid, username, err)
	}
	return filepath.WalkDir(path, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(p, uid, gid)
	})
}
