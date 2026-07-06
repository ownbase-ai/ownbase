package install

import (
	"os"
	"strings"
)

// AdminUserPath is where install.sh persists the name of the OS account
// used to SSH into this Base — root for most remote servers, ubuntu for a
// local Multipass VM (whichever account `ownbasectl create`'s --ssh-user
// actually logged in as; see install.sh's OWNBASE_ADMIN_USER).
//
// Unlike FirstRunEnvPath, this file is never deleted: the daemon needs it
// every time a new bare repo is created — not just once at bootstrap — so
// that a service added months after install still gets a pushable repo
// (see internal/fsowner, internal/githost.SetRepoOwner, internal/repos).
const AdminUserPath = "/opt/ownbase/admin-user"

// ReadAdminUser returns the admin username persisted at path, or "" when
// the file is missing (a local dev/test build with no installer run, or an
// install predating this file). Callers should treat "" as "skip the
// chown" rather than an error: a Base still functions correctly without
// it — only a direct git-over-SSH push to a repo created while it was
// missing won't work until it's set. ownbasectl config/service (API-based
// commits) always work regardless.
func ReadAdminUser(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
