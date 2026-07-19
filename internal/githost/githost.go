// Package githost manages local git concerns on a Base: the machine-wide
// "safe.directory" trust required for the daemon (root) to operate on the
// per-service bare repos under /opt/ownbase/repos (see internal/repos), plus
// a small MachineID helper.
//
// The config repo is external (see internal/configsource): there is no
// on-Base bare config repo, post-receive hook, or SIGUSR1 signalling — the
// daemon pulls the config read-only and reconciles explicitly.
package githost

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DefaultCheckoutPath is the canonical on-Base path for the working checkout
// the daemon reads ownbase.yaml from (a read-only clone of the external config
// repo — see internal/configsource).
const DefaultCheckoutPath = "/opt/ownbase/checkout"

// TrustAllRepos tells git, machine-wide, that it is safe to operate on a
// repository owned by a different user than the current process. This is
// required once repos.EnsureRepo chowns a bare repo to adminUser: the daemon
// (root) still needs to read and fetch into that repo, and git refuses by
// default to touch a repo whose owner doesn't match the calling user (the
// "dubious ownership" check, CVE-2022-24765).
//
// A blanket `safe.directory = *` is appropriate here — unlike a shared
// multi-tenant host, a Base has exactly one admin user and one root
// daemon, both already fully trusted with the whole machine. Written to
// the system-wide git config (/etc/gitconfig) so it applies regardless of
// $HOME, and to every account (root and adminUser alike).
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

// MachineID returns a stable identifier for this machine. On Linux it reads
// /etc/machine-id; on other platforms it falls back to the hostname. It
// identifies which machine the Base is running on.
func MachineID() (string, error) {
	data, err := os.ReadFile("/etc/machine-id")
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	// Fallback: hostname (macOS dev machine, CI).
	return os.Hostname()
}
