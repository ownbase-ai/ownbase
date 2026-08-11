// Package repos manages the on-Base bare repos that back user services —
// one bare repo per service under /opt/ownbase/repos/, keyed by service name.
//
// Every service declares an external git URL (repo:); OwnBase keeps a
// read-only `git clone --bare --mirror` of it locally. FetchRef always
// refreshes branch and tag names from upstream; only full commit SHAs
// short-circuit when already present (immutable objects).
//
// Every repo is backed up locally (see internal/backup). The external git
// host is consulted on every reconcile for mutable refs so a poisoned
// mirror or force-push cannot stick forever.
//
// Repos are created by the daemon, which runs as root (see install.sh's
// systemd unit) — EnsureRepo/EnsureRepos chown each repo to the configured
// admin user (internal/fsowner) so file ownership stays consistent.
package repos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ownbase/ownbase/internal/fsowner"
	"github.com/ownbase/ownbase/internal/gitssh"
	"github.com/ownbase/ownbase/internal/schema"
)

// DefaultReposDir is the root directory containing one bare repo per service.
const DefaultReposDir = "/opt/ownbase/repos"

// RepoPath returns the on-disk path of the bare repo for the given service
// name. Each service's local bare clone lives at
// /opt/ownbase/repos/<service-name>.
func RepoPath(name string) string {
	return filepath.Join(DefaultReposDir, name)
}

// HasRef reports whether ref (branch, tag, or commit SHA) exists in the
// local bare repo for name. An empty ref reports whether the repo itself
// exists — an empty ref means "whatever HEAD resolves to", which is only
// meaningful once the repo is present.
func HasRef(name, ref string) bool {
	return hasRefAt(RepoPath(name), ref)
}

// EnsureRepo makes sure a read-only bare clone exists at RepoPath(name),
// performing an initial `git clone --bare --mirror` from externalURL (the
// service's repo: URL) when it does not yet exist locally. Idempotent: a
// no-op when the repo already exists. The clone uses the managed SSH identity
// (see internal/gitssh) for private repos.
//
// adminUser, when non-empty, is chowned onto the repo (see internal/fsowner)
// so file ownership stays consistent with the admin account; the daemon that
// creates the repo runs as root. Pass "" to skip this step (e.g. a local
// dev/test build with no installer run).
func EnsureRepo(name, externalURL, adminUser string) error {
	return ensureRepoAtWithOwner(RepoPath(name), externalURL, adminUser)
}

// FetchRef fetches the given ref from externalURL into the local bare repo
// for name. Full commit SHAs skip the network when already local; branches
// and tags always refresh. A no-op when externalURL or ref is empty.
// See EnsureRepo for adminUser.
func FetchRef(name, externalURL, ref, adminUser string) error {
	if externalURL == "" || ref == "" {
		return nil
	}
	if err := EnsureRepo(name, externalURL, adminUser); err != nil {
		return err
	}
	return fetchRefAt(RepoPath(name), externalURL, ref)
}

// EnsureRepos ensures a local bare clone exists for every service declared in
// cfg, cloning each service's repo: from its external URL on first sight and
// fetching any pinned ref: that is not yet available locally. Each error is
// collected and returned rather than aborting early, so a problem with one
// service's repo does not block the others from being ensured. Callers
// should log the returned errors as non-fatal — the next reconcile tick (or
// timer backstop) retries. See EnsureRepo for adminUser.
func EnsureRepos(cfg *schema.OwnbaseConfig, adminUser string) []error {
	if cfg == nil {
		return nil
	}
	var errs []error
	for name, svc := range cfg.Services {
		externalURL := svc.Repo
		if externalURL == "" {
			continue // invalid service decl; schema.Validate already rejects this
		}
		// The local bare clone is keyed by the service name (see
		// compiler.build) so it is collision-free even when two services
		// share the same upstream URL.
		if err := EnsureRepo(name, externalURL, adminUser); err != nil {
			errs = append(errs, fmt.Errorf("service %q: ensure repo: %w", name, err))
			continue
		}
		if svc.Ref != "" {
			if err := FetchRef(name, externalURL, svc.Ref, adminUser); err != nil {
				errs = append(errs, fmt.Errorf("service %q: fetch ref %q: %w", name, svc.Ref, err))
			}
		}
	}
	return errs
}

// ---------------------------------------------------------------------------
// path-based implementations, kept separate from the name-based public API
// so tests can exercise real git behavior against temp directories instead
// of the fixed /opt/ownbase/repos production path.
// ---------------------------------------------------------------------------

func isBareRepo(path string) bool {
	_, err := os.Stat(filepath.Join(path, "HEAD"))
	return err == nil
}

func hasRefAt(repoPath, ref string) bool {
	if ref == "" {
		return isBareRepo(repoPath)
	}
	if !isBareRepo(repoPath) {
		return false
	}
	err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Run()
	return err == nil
}

func ensureRepoAt(repoPath, externalURL string) error {
	if externalURL == "" {
		return fmt.Errorf("ensure repo %s: no repo URL — every service must declare repo:", repoPath)
	}
	if isBareRepo(repoPath) {
		// Reuse the existing clone only when its origin still matches repo:.
		// After `service update --repo` (or forking + repointing) the URL
		// changes; a stale clone would keep serving the previous upstream's
		// refs, so re-clone from scratch on mismatch.
		if originURLAt(repoPath) == externalURL {
			return nil
		}
		if err := os.RemoveAll(repoPath); err != nil {
			return fmt.Errorf("remove stale clone %s: %w", repoPath, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", repoPath, err)
	}

	cmd := exec.Command("git", "clone", "--bare", "--mirror", externalURL, repoPath)
	cmd.Env = gitssh.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone --bare --mirror %s -> %s: %w\n%s", externalURL, repoPath, err, out)
	}
	return nil
}

// originURLAt returns the configured origin remote URL of the bare repo at
// repoPath, or "" when it has no origin (e.g. an old push-to-Base source repo
// created with `git init --bare`). Used to detect when a local clone must be
// re-created because the service's repo: URL changed.
func originURLAt(repoPath string) string {
	out, err := exec.Command("git", "-C", repoPath, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ensureRepoAtWithOwner is ensureRepoAt plus a chown of the repo to
// adminUser (see internal/fsowner). Re-applied on every call, not just on
// first creation: cheap, and self-heals if the admin user changed or a
// previous chown failed. A no-op chown when adminUser is empty.
func ensureRepoAtWithOwner(repoPath, externalURL, adminUser string) error {
	if err := ensureRepoAt(repoPath, externalURL); err != nil {
		return err
	}
	if err := fsowner.Chown(repoPath, adminUser); err != nil {
		return fmt.Errorf("chown %s to %q: %w", repoPath, adminUser, err)
	}
	return nil
}

func fetchRefAt(repoPath, externalURL, ref string) error {
	// Full commit SHAs are immutable: if the object is already local, skip
	// the network round-trip. Branches and tags move, so always refresh them
	// — otherwise a poisoned bare mirror (or a force-push upstream) would
	// stick forever because hasRefAt short-circuits on the stale name.
	if isFullCommitSHA(ref) && hasRefAt(repoPath, ref) {
		return nil
	}
	cmd := exec.Command("git", "-C", repoPath, "fetch", externalURL, "+refs/*:refs/*", "--prune")
	cmd.Env = gitssh.Env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch %s: %w\n%s", externalURL, err, out)
	}
	return nil
}

// isFullCommitSHA reports whether ref looks like a full git object id
// (40-char SHA-1 or 64-char SHA-256 hex). Abbreviated SHAs and branch/tag
// names return false.
func isFullCommitSHA(ref string) bool {
	n := len(ref)
	if n != 40 && n != 64 {
		return false
	}
	for i := 0; i < n; i++ {
		c := ref[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
