// Package install implements provider-agnostic bootstrap and host hardening.
//
// M5: the installer IS the agent's first reconcile (pass zero). The native
// shell script does the absolute minimum (verify binary, create user, enable
// service); the agent runs PassZero to bring the host to the state required
// for the M3 reconcile loop.
//
// PassZero is idempotent: each step checks whether its condition is already
// satisfied before acting. Resumability is a consequence of idempotency — kill
// the install mid-way, restart the agent, and PassZero continues from where it
// left off with no special resume logic.
package install

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DefaultAgentUser is the system user that owns all OwnBase files and runs
// the agent and containers. Never root.
const DefaultAgentUser = "ownbase"

// MinUbuntuVersion is the oldest Ubuntu release supported.
const MinUbuntuVersion = "22.04"

// PassZeroConfig tunes the hardening reconcile pass.
type PassZeroConfig struct {
	// AgentUser is the system user running the agent. Default: "ownbase".
	AgentUser string

	// SSHPort is the SSH port to allow through the firewall. Default: 22.
	SSHPort int

	// ExposeWebPorts controls whether the firewall allows inbound 80/443.
	// Should be true only when at least one service in ownbase.yaml has a
	// domain configured (schema.OwnbaseConfig.HasPublicDomain) — a
	// domain-less Base (the default state of a fresh Base) has nothing for
	// Caddy to route, so it exposes only SSH. Reach services directly with
	// `ownbasectl tunnel` instead. Default: false (safest).
	ExposeWebPorts bool

	// DryRun logs what would be done without making changes.
	DryRun bool
}

func (cfg *PassZeroConfig) withDefaults() PassZeroConfig {
	c := *cfg
	if c.AgentUser == "" {
		c.AgentUser = DefaultAgentUser
	}
	if c.SSHPort == 0 {
		c.SSHPort = 22
	}
	return c
}

// StepStatus records the outcome of one hardening step.
type StepStatus struct {
	// Done is true when the step is satisfied after PassZero (whether it ran
	// or was already satisfied before it ran).
	Done bool
	// AlreadyOK is true when the step was already satisfied — PassZero was a
	// no-op for this step.
	AlreadyOK bool
	// Detail is a human-readable message from the step (version, config note).
	Detail string
	// Err holds the error if the step failed.
	Err error
}

// HardeningReport summarises the state of each PassZero step.
// Suitable for surfacing through the explain interface (status API).
type HardeningReport struct {
	OS          StepStatus
	Podman      StepStatus
	Git         StepStatus
	Linger      StepStatus
	Firewall    StepStatus
	AutoUpdates StepStatus
	Fail2ban    StepStatus
	NoExposedDB StepStatus

	// Trivy is the vulnerability scanner installation step. Non-fatal:
	// trivy failure does not affect OK() because it is scanning
	// infrastructure, not runtime infrastructure the Base depends on.
	Trivy StepStatus

	// Restic is the backup tool installation step. Non-fatal: a Base
	// with no backup repo configured yet has nothing to back up regardless,
	// so a transient apt failure here should not block the daemon from
	// starting. `ownbasectl backup status` is where a missing restic
	// binary should surface.
	Restic StepStatus
}

// OK returns true when every required hardening step succeeded.
// Trivy is excluded — its installation failure is non-fatal.
func (r HardeningReport) OK() bool {
	for _, s := range []StepStatus{r.OS, r.Podman, r.Git, r.Linger, r.Firewall,
		r.AutoUpdates, r.Fail2ban, r.NoExposedDB} {
		if !s.Done {
			return false
		}
	}
	return true
}

// PassZero runs the host hardening reconcile pass (install pass zero).
// It must be called on every agent start; each step is idempotent and
// no-ops when already satisfied.
//
// Steps run in dependency order:
//  1. Verify Ubuntu version.
//  2. Install Podman container runtime.
//  3. Install git (repo bootstrap + service builds).
//  4. Enable systemd linger for the agent user.
//  5. Configure UFW firewall.
//  6. Enable automatic security updates.
//  7. Install and configure fail2ban.
//  8. Configure container DNS and unqualified-search-registries (needed for
//     Dockerfile builds).
//  9. Allow container→internet egress through UFW's deny-routed policy.
// 10. Verify no database ports are publicly reachable.
//
// Any step failure returns immediately. The caller (agent main) logs the
// error and exits, so systemd will restart the service and PassZero will
// resume from the beginning (each completed step re-checks quickly).
func PassZero(ctx context.Context, cfg PassZeroConfig) (HardeningReport, error) {
	c := cfg.withDefaults()
	r := HardeningReport{}

	r.OS = checkOS(ctx, c)
	if r.OS.Err != nil {
		return r, fmt.Errorf("os check: %w", r.OS.Err)
	}

	r.Podman = ensurePodman(ctx, c)
	if r.Podman.Err != nil {
		return r, fmt.Errorf("podman: %w", r.Podman.Err)
	}

	// Git is required for repo bootstrap and every service build; a minimal
	// Ubuntu image may not ship it, so install it before anything git-backed.
	r.Git = ensureGit(ctx, c)
	if r.Git.Err != nil {
		return r, fmt.Errorf("git: %w", r.Git.Err)
	}

	r.Linger = ensureLinger(ctx, c)
	if r.Linger.Err != nil {
		return r, fmt.Errorf("linger: %w", r.Linger.Err)
	}

	r.Firewall = ensureFirewall(ctx, c)
	if r.Firewall.Err != nil {
		return r, fmt.Errorf("firewall: %w", r.Firewall.Err)
	}

	r.AutoUpdates = ensureAutoUpdates(ctx, c)
	if r.AutoUpdates.Err != nil {
		return r, fmt.Errorf("auto-updates: %w", r.AutoUpdates.Err)
	}

	r.Fail2ban = ensureFail2ban(ctx, c)
	if r.Fail2ban.Err != nil {
		return r, fmt.Errorf("fail2ban: %w", r.Fail2ban.Err)
	}

	// Ensure rootful Podman containers can resolve DNS (needed for builds).
	if s := ensureContainerDNS(ctx, c); s.Err != nil {
		return r, fmt.Errorf("container-dns: %w", s.Err)
	}

	// Let Podman resolve short image names (e.g. "golang:1-alpine") in user
	// Dockerfiles — Ubuntu's stock registries.conf leaves this unset, which
	// breaks the build of nearly every public Dockerfile.
	if s := ensureUnqualifiedSearchRegistries(ctx, c); s.Err != nil {
		return r, fmt.Errorf("unqualified-search-registries: %w", s.Err)
	}

	// Allow container→internet egress through UFW's default deny-routed
	// policy; without it, builds can't download deps and services can't
	// reach external APIs. Must run after ensureFirewall (UFW active).
	if s := ensureContainerEgress(ctx, c); s.Err != nil {
		return r, fmt.Errorf("container-egress: %w", s.Err)
	}

	r.NoExposedDB = verifyNoExposedDB(ctx, c)
	if r.NoExposedDB.Err != nil {
		return r, fmt.Errorf("exposed-db check: %w", r.NoExposedDB.Err)
	}

	// Non-fatal: trivy install failure does not abort pass zero.
	r.Trivy = ensureTrivy(ctx, c)
	if r.Trivy.Err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: pass zero: trivy install failed (non-fatal): %v\n", r.Trivy.Err)
		r.Trivy.Err = nil // clear so future OK() checks aren't confused
	}

	// Non-fatal: restic install failure does not abort pass zero.
	r.Restic = ensureRestic(ctx, c)
	if r.Restic.Err != nil {
		fmt.Fprintf(os.Stderr, "ownbased: pass zero: restic install failed (non-fatal): %v\n", r.Restic.Err)
		r.Restic.Err = nil // clear so future OK() checks aren't confused
	}

	return r, nil
}

// CheckHardeningState returns the current hardening posture without making
// any changes. Used for explain (M8) and status display.
func CheckHardeningState(ctx context.Context, cfg PassZeroConfig) HardeningReport {
	c := cfg.withDefaults()
	c.DryRun = true
	r := HardeningReport{}
	r.OS = checkOS(ctx, c)
	r.Podman = checkPodmanState(ctx)
	r.Git = checkGitState(ctx)
	r.Linger = checkLingerState(ctx, c.AgentUser)
	r.Firewall = checkFirewallState(ctx, c)
	r.AutoUpdates = checkAutoUpdatesState(ctx)
	r.Fail2ban = checkFail2banState(ctx)
	r.NoExposedDB = verifyNoExposedDB(ctx, c)
	r.Trivy = checkTrivyState(ctx)
	r.Restic = checkResticState(ctx)
	return r
}

// ---------------------------------------------------------------------------
// OS check
// ---------------------------------------------------------------------------

func checkOS(ctx context.Context, cfg PassZeroConfig) StepStatus {
	out, err := run(ctx, "lsb_release", "-rs")
	if err != nil {
		// Not Ubuntu (e.g. macOS in dev) — treat as dev mode, skip Linux steps.
		return StepStatus{Done: true, AlreadyOK: true, Detail: "non-Ubuntu host (dev mode)"}
	}
	version := strings.TrimSpace(out)

	// PassZero installs packages and writes to /etc/ — requires root on Linux.
	if !cfg.DryRun && os.Getuid() != 0 {
		return StepStatus{
			Done: false,
			Err: fmt.Errorf("PassZero requires root on Linux (uid=%d); "+
				"the ownbased systemd service must run as root for pass zero", os.Getuid()),
		}
	}

	return StepStatus{
		Done:      true,
		AlreadyOK: true,
		Detail:    fmt.Sprintf("Ubuntu %s", version),
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// run executes a command and returns stdout+stderr combined.
// Returns ("", err) if the command fails.
func run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// cmdExists returns true if the named binary is in PATH.
func cmdExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// apt installs a package if it is not already installed. Idempotent.
func apt(ctx context.Context, pkg string, dryRun bool) (StepStatus, error) {
	// Check if already installed.
	if out, err := run(ctx, "dpkg-query", "-W", "-f=${Status}", pkg); err == nil &&
		strings.Contains(out, "install ok installed") {
		return StepStatus{Done: true, AlreadyOK: true, Detail: pkg + " already installed"}, nil
	}

	if dryRun {
		return StepStatus{Done: false, Detail: "would install " + pkg}, nil
	}

	if _, err := run(ctx, "apt-get", "install", "-y", "-q", pkg); err != nil {
		return StepStatus{Err: fmt.Errorf("apt-get install %s: %w", pkg, err)}, err
	}
	return StepStatus{Done: true, Detail: pkg + " installed"}, nil
}
