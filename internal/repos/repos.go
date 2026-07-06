// Package repos manages the on-Base bare repos that back user services —
// one bare repo per service under /opt/ownbase/repos/. This replaces
// Forgejo as the source of truth for service source code:
//
//   - source: services get an empty bare repo that the user (or an agent,
//     via ownbasectl) pushes into directly over SSH — exactly like the
//     config repo at /opt/ownbase/repo.
//   - mirror: services get a `git clone --bare --mirror` of the external
//     URL, refreshed on demand (FetchRef) when ownbase.yaml pins a ref that
//     is not yet present locally.
//
// Every repo is backed up locally (see internal/backup), so a Base is
// self-contained: the external git host is only consulted when a new ref is
// requested that hasn't been fetched yet.
package repos

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/schema"
)

// DefaultReposDir is the root directory containing one bare repo per service.
const DefaultReposDir = "/opt/ownbase/repos"

// RepoPath returns the on-disk path of the bare repo for the given repo name
// (a schema.ServiceDecl.Source value, or the mirrors-<basename> name derived
// from Mirror by compiler.MirrorRepoName). name may contain slashes (e.g.
// "services/auth"), which nest naturally as subdirectories.
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

// EnsureRepo makes sure a bare repo exists at RepoPath(name). For mirror
// services (externalURL non-empty) it performs an initial
// `git clone --bare --mirror` from externalURL when the repo does not yet
// exist locally. For source services (externalURL empty) it creates an
// empty bare repo that the user pushes into directly (over SSH, exactly like
// the config repo). Idempotent: a no-op when the repo already exists.
func EnsureRepo(name, externalURL string) error {
	return ensureRepoAt(RepoPath(name), externalURL)
}

// FetchRef fetches the given ref from externalURL into the local bare repo
// for name when it is not already present locally. This is the on-demand
// fetch triggered when a ref: is pinned to a value that has not yet been
// pulled from the external source. A no-op when externalURL or ref is empty,
// or when the ref is already present locally.
func FetchRef(name, externalURL, ref string) error {
	if externalURL == "" || ref == "" {
		return nil
	}
	if err := EnsureRepo(name, externalURL); err != nil {
		return err
	}
	return fetchRefAt(RepoPath(name), externalURL, ref)
}

// EnsureRepos ensures a local bare repo exists for every service declared in
// cfg, cloning mirror: services from their external URL on first sight and
// fetching any pinned ref: that is not yet available locally. Each error is
// collected and returned rather than aborting early, so a problem with one
// service's repo does not block the others from being ensured. Callers
// should log the returned errors as non-fatal — the next reconcile tick (or
// timer backstop) retries.
func EnsureRepos(cfg *schema.OwnbaseConfig) []error {
	if cfg == nil {
		return nil
	}
	var errs []error
	for name, svc := range cfg.Services {
		repoName := svc.Source
		externalURL := ""
		if repoName == "" && svc.Mirror != "" {
			repoName = compiler.MirrorRepoName(svc.Mirror)
			externalURL = svc.Mirror
		}
		if repoName == "" {
			continue // invalid service decl; schema.Validate already rejects this
		}
		if err := EnsureRepo(repoName, externalURL); err != nil {
			errs = append(errs, fmt.Errorf("service %q: ensure repo %q: %w", name, repoName, err))
			continue
		}
		if externalURL != "" && svc.Ref != "" {
			if err := FetchRef(repoName, externalURL, svc.Ref); err != nil {
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
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", repoPath, err)
	}

	if externalURL == "" {
		out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", repoPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git init --bare %s: %w\n%s", repoPath, err, out)
		}
		return nil
	}

	out, err := exec.Command("git", "clone", "--bare", "--mirror", externalURL, repoPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone --bare --mirror %s -> %s: %w\n%s", externalURL, repoPath, err, out)
	}
	return nil
}

func fetchRefAt(repoPath, externalURL, ref string) error {
	if hasRefAt(repoPath, ref) {
		return nil
	}
	out, err := exec.Command("git", "-C", repoPath, "fetch", externalURL, "+refs/*:refs/*", "--prune").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch %s: %w\n%s", externalURL, err, out)
	}
	return nil
}
