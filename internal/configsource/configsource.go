// Package configsource manages the external git repository that holds this
// Base's ownbase.yaml, and keeps the local read-only checkout in sync with it.
//
// The daemon never writes to the config repo: the operator commits changes
// client-side via ownbasectl (deploy / config set / service *), pushing with
// their own git credentials. The Base needs only READ access, provided by the
// managed SSH identity (see internal/gitssh).
//
// The config source (repo URL + ref) is recorded in a small state file at
// /opt/ownbase/config-source.yaml, written by `ownbasectl config setup`. On
// every reconcile the daemon fetches the configured ref and hard-resets the
// checkout to it, so the checkout is always a faithful mirror of the remote —
// any local edit is discarded (there is no local source of truth).
package configsource

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultStatePath is where the daemon records the config source.
const DefaultStatePath = "/opt/ownbase/config-source.yaml"

// DefaultRef is used when a source records no explicit ref.
const DefaultRef = "main"

// Source identifies the external config repository and the ref to track.
type Source struct {
	// RepoURL is the git URL of the config repo (e.g.
	// "git@github.com:org/ownbase-config.git").
	RepoURL string `yaml:"repo_url"`
	// Ref is the branch (usually) to track. Empty means DefaultRef.
	Ref string `yaml:"ref,omitempty"`
}

// Configured reports whether a config source has been set.
func (s Source) Configured() bool { return strings.TrimSpace(s.RepoURL) != "" }

// EffectiveRef returns Ref or DefaultRef when Ref is empty.
func (s Source) EffectiveRef() string {
	if strings.TrimSpace(s.Ref) == "" {
		return DefaultRef
	}
	return s.Ref
}

// Load reads the config-source state file. A missing file returns a zero
// (unconfigured) Source with no error.
func Load(path string) (Source, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Source{}, nil
	}
	if err != nil {
		return Source{}, fmt.Errorf("read config source %s: %w", path, err)
	}
	var s Source
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Source{}, fmt.Errorf("parse config source %s: %w", path, err)
	}
	return s, nil
}

// Save writes the config-source state file (mode 0644; it holds no secrets).
func Save(path string, s Source) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config-source dir: %w", err)
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal config source: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config source %s: %w", path, err)
	}
	return nil
}

// EnsureCheckout makes checkoutPath reflect src at src.EffectiveRef(). It
// clones on first use, otherwise fetches and hard-resets. gitEnv is the
// process environment including GIT_SSH_COMMAND (see internal/gitssh); pass
// nil to inherit the current environment. A no-op when src is unconfigured.
func EnsureCheckout(ctx context.Context, src Source, checkoutPath string, gitEnv []string) error {
	if !src.Configured() {
		return nil
	}
	ref := src.EffectiveRef()

	// Re-clone from scratch when there is no checkout yet, or when an existing
	// checkout points at a different origin than the configured RepoURL (e.g.
	// after `config setup` repoints the Base at a new config repo). Reusing a
	// stale checkout would silently keep syncing the old remote.
	if !isGitDir(checkoutPath) || originURL(ctx, gitEnv, checkoutPath) != src.RepoURL {
		if err := os.MkdirAll(filepath.Dir(checkoutPath), 0o755); err != nil {
			return fmt.Errorf("create checkout parent: %w", err)
		}
		// Remove any partial/empty/stale directory so `git clone` can create it.
		_ = os.RemoveAll(checkoutPath)
		if out, err := runGit(ctx, gitEnv, "", "clone", src.RepoURL, checkoutPath); err != nil {
			return fmt.Errorf("clone config repo %s: %w\n%s", src.RepoURL, err, out)
		}
	} else if out, err := runGit(ctx, gitEnv, checkoutPath, "fetch", "--prune", "origin"); err != nil {
		return fmt.Errorf("fetch config repo: %w\n%s", err, out)
	}

	// Prefer origin/<ref> (a branch); fall back to <ref> (a tag or SHA).
	target := "origin/" + ref
	if !revParseVerifies(ctx, gitEnv, checkoutPath, target) {
		target = ref
	}
	if out, err := runGit(ctx, gitEnv, checkoutPath, "reset", "--hard", target); err != nil {
		return fmt.Errorf("reset config repo to %s: %w\n%s", target, err, out)
	}
	if out, err := runGit(ctx, gitEnv, checkoutPath, "clean", "-fd"); err != nil {
		return fmt.Errorf("clean config repo: %w\n%s", err, out)
	}
	return nil
}

func runGit(ctx context.Context, env []string, dir string, args ...string) ([]byte, error) {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	if env != nil {
		cmd.Env = env
	}
	return cmd.CombinedOutput()
}

func revParseVerifies(ctx context.Context, env []string, dir, ref string) bool {
	_, err := runGit(ctx, env, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	return err == nil
}

// originURL returns the configured URL of the "origin" remote in dir, or ""
// when it cannot be determined (no remote, not a repo). Used to detect when an
// existing checkout must be re-cloned because the config repo URL changed.
func originURL(ctx context.Context, env []string, dir string) string {
	out, err := runGit(ctx, env, dir, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func isGitDir(path string) bool {
	if _, err := os.Stat(filepath.Join(path, ".git", "HEAD")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return true
	}
	return false
}
