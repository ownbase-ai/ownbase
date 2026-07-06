// Package githost manages the on-Base bare repo, the post-receive hook, and
// the working checkout the daemon reconciles from.
//
// Layout on a running Base:
//
//	/opt/ownbase/
//	  repo/        # bare git repo — the irreducible, user-owned source of truth
//	  checkout/    # working clone the daemon reads ownbase.yaml from
//	  repos/       # one bare repo per service (see internal/repos)
//	  runtime/     # compiler output written by the daemon (never by hand)
//	  logs/        # audit log and other daemon logs
//	  daemon.pid   # daemon PID for SIGUSR1 hook signaling
//
// The bare repo at repo/ is the canonical source of truth: ownbasectl pushes
// directly to it over SSH; the post-receive hook signals the daemon, which
// pulls into checkout/ and reconciles. There is no intermediary git host —
// the filesystem repo is what reconcile can never depend on being up.
package githost

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ownbase/ownbase/internal/fsowner"
)

// DefaultRepoPath is the canonical on-Base path for the bare repo.
const DefaultRepoPath = "/opt/ownbase/repo"

// DefaultCheckoutPath is the canonical on-Base path for the working checkout.
const DefaultCheckoutPath = "/opt/ownbase/checkout"

// DefaultPIDPath is where the daemon writes its PID for hook signaling.
const DefaultPIDPath = "/opt/ownbase/daemon.pid"

// HookScript is the post-receive hook installed into the bare repo.
// It sends SIGUSR1 to the daemon PID so the daemon reconciles immediately on
// push, without polling. The timer backstop still catches drift that arrives
// without a commit.
//
// The hook runs as whoever pushed — the admin SSH user for a direct `git
// push` — while the daemon runs as root. Sending a signal requires a UID
// match or root, so a plain `kill` here would silently fail for a non-root
// admin user (e.g. "ubuntu" on a local VM). `sudo -n pkill` uses the narrow,
// single-command NOPASSWD grant installed at
// internal/install.NotifySudoersPath; for a root admin user (most remote
// installs) it just works via the stock sudoers file. If neither applies
// (sudoers grant not yet installed, or the daemon isn't running) the signal
// is silently skipped (|| true) and the timer backstop catches it instead.
const HookScript = `#!/bin/sh
# OwnBase post-receive hook — triggers the daemon reconcile loop on push.
# Never hand-edit: reinstalled by the daemon on each startup.
PIDFILE=/opt/ownbase/daemon.pid
if [ -f "$PIDFILE" ]; then
  sudo -n /usr/bin/pkill -USR1 -F "$PIDFILE" 2>/dev/null || true
fi
`

// Bootstrap initializes the bare repo and working checkout at the given paths.
// It is idempotent: calling it on an already-initialized Base is a no-op.
//
// Bootstrap does not write the genesis record — that is the caller's
// responsibility after the first reconcile produces a stable desired state
// (see WriteGenesisRecord).
func Bootstrap(repoPath, checkoutPath string) error {
	repoExists := isGitDir(repoPath)
	checkoutExists := isGitDir(checkoutPath)

	if !repoExists {
		if err := initBareRepo(repoPath); err != nil {
			return fmt.Errorf("bootstrap: init bare repo: %w", err)
		}
	}

	if !checkoutExists {
		if err := cloneLocalRepo(repoPath, checkoutPath); err != nil {
			return fmt.Errorf("bootstrap: clone checkout: %w", err)
		}
	}

	return nil
}

// SetRepoOwner grants adminUser (the SSH account ownbasectl and the operator
// use to reach this Base) write access to the bare repo at repoPath, which
// the daemon otherwise creates and owns as root (see install.sh's systemd
// unit). Without this, `git push` over SSH as adminUser fails with a
// permission error even though the repo exists. A no-op when adminUser is
// empty (see internal/install.ReadAdminUser). Safe to call on every daemon
// start — chowning an already-correctly-owned tree is a cheap no-op.
func SetRepoOwner(repoPath, adminUser string) error {
	return fsowner.Chown(repoPath, adminUser)
}

// TrustAllRepos tells git, machine-wide, that it is safe to operate on a
// repository owned by a different user than the current process. This is
// required once SetRepoOwner/repos.EnsureRepo chown a bare repo to
// adminUser: the daemon (root) still needs to read, fetch into, and be
// pushed to that repo (git push's receive-pack runs as whatever SSH user
// connected, but the daemon's own pulls/seeds/fetches run as root), and
// git refuses by default to touch a repo whose owner doesn't match the
// calling user (the "dubious ownership" check, CVE-2022-24765).
//
// A blanket `safe.directory = *` is appropriate here — unlike a shared
// multi-tenant host, a Base has exactly one admin user and one root
// daemon, both already fully trusted with the whole machine. Written to
// the system-wide git config (/etc/gitconfig) so it applies regardless of
// $HOME, and to every account (root and adminUser alike) rather than just
// whichever user happens to invoke git first.
//
// Idempotent: --replace-all overwrites any prior value instead of
// appending a duplicate. Safe to call on every daemon start.
func TrustAllRepos() error {
	out, err := exec.Command("git", "config", "--system", "--replace-all", "safe.directory", "*").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git config --system safe.directory: %w\n%s", err, out)
	}
	return nil
}

// InstallHook writes (or overwrites) the post-receive hook in the bare repo.
// The hook file is always refreshed on agent startup so it stays current even
// if a previous version had a different format.
func InstallHook(repoPath string) error {
	hooksDir := filepath.Join(repoPath, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("install hook: mkdir hooks: %w", err)
	}

	hookPath := filepath.Join(hooksDir, "post-receive")
	if err := os.WriteFile(hookPath, []byte(HookScript), 0o755); err != nil {
		return fmt.Errorf("install hook: write: %w", err)
	}
	return nil
}

// UpdateCheckout pulls the latest HEAD from the bare repo into the checkout.
// The agent calls this after the hook fires (before compile).
//
// For an empty bare repo (no commits yet), UpdateCheckout is a no-op.
func UpdateCheckout(repoPath, checkoutPath string) error {
	// Check whether the bare repo has any commits.
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		// Bare repo has no commits yet — nothing to pull.
		return nil
	}

	cmd := exec.Command("git", "-C", checkoutPath, "pull", "--ff-only", "origin")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("update checkout: git pull: %w\n%s", err, out)
	}
	return nil
}

// WritePIDFile writes the current process PID to path so the post-receive
// hook can send SIGUSR1. The file is overwritten on each agent start.
func WritePIDFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("pid file: mkdir: %w", err)
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
}

// MachineID returns a stable identifier for this machine. On Linux it reads
// /etc/machine-id; on other platforms it falls back to the hostname. The
// value is included in the genesis record to identify which machine the Base
// was bootstrapped on.
func MachineID() (string, error) {
	data, err := os.ReadFile("/etc/machine-id")
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	// Fallback: hostname (macOS dev machine, CI).
	return os.Hostname()
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

// isGitDir returns true if path looks like a git repository (bare or regular).
func isGitDir(path string) bool {
	// A bare repo has a HEAD file at its root; a regular repo has .git/HEAD.
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(path, ".git", "HEAD")); err == nil {
		return true
	}
	return false
}

func initBareRepo(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}
	out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git init --bare %s: %w\n%s", path, err, out)
	}
	// Set safe defaults for the bare repo.
	setGitConfig(path, "receive.denyNonFastForwards", "true")
	setGitConfig(path, "receive.denyDeleteCurrent", "true")
	return nil
}

func cloneLocalRepo(repoPath, checkoutPath string) error {
	if err := os.MkdirAll(filepath.Dir(checkoutPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", checkoutPath, err)
	}
	out, err := exec.Command(
		"git", "clone",
		"--local",
		"--origin", "origin",
		repoPath, checkoutPath,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s → %s: %w\n%s", repoPath, checkoutPath, err, out)
	}
	// Set identity for agent commits (genesis record, update PRs, etc.).
	setGitConfigLocal(checkoutPath, "user.name", "OwnBase Daemon")
	setGitConfigLocal(checkoutPath, "user.email", "agent@ownbase.local")
	return nil
}

func setGitConfig(repoPath, key, value string) {
	_ = exec.Command("git", "-C", repoPath, "config", key, value).Run()
}

func setGitConfigLocal(repoPath, key, value string) {
	_ = exec.Command("git", "-C", repoPath, "config", "--local", key, value).Run()
}
