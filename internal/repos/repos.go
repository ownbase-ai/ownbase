// Package repos manages the on-Base bare repos that back user services —
// one bare repo per service under /opt/ownbase/repos/, keyed by service name.
//
// Every service declares an external git URL (repo:); OwnBase keeps a
// read-only `git clone --bare --mirror` of it locally, refreshed on demand
// (FetchRef) when ownbase.yaml pins a ref that is not yet present locally.
//
// Every repo is backed up locally (see internal/backup), so a Base is
// self-contained: the external git host is only consulted when a new ref is
// requested that hasn't been fetched yet.
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
// for name when it is not already present locally. This is the on-demand
// fetch triggered when a ref: is pinned to a value that has not yet been
// pulled from the external source. A no-op when externalURL or ref is empty,
// or when the ref is already present locally. See EnsureRepo for adminUser.
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
	if isBareRepo(repoPath) {
		return nil
	}
	if externalURL == "" {
		return fmt.Errorf("ensure repo %s: no repo URL — every service must declare repo:", repoPath)
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
	if hasRefAt(repoPath, ref) {
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
