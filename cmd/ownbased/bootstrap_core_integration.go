//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ownbase/ownbase/internal/core"
	"github.com/ownbase/ownbase/internal/install"
	"github.com/ownbase/ownbase/internal/schema"
)

// runningAsRoot returns true when the effective UID is 0 (root).
// When true, Quadlet units go into /etc/containers/systemd/ and are managed
// with system-level systemctl (no --user), so no user D-Bus session is needed.
func runningAsRoot() bool { return os.Getuid() == 0 }

// bootstrapCore installs and starts the core Quadlet units (Caddy) as
// systemd services. Safe to call on every agent startup AND on every
// reconcile tick (see reconcileLoop) — every step is idempotent, and unit
// files are only rewritten/reloaded/restarted when their rendered content
// actually changes, so a tick that doesn't touch Core config or domain
// state is cheap (a few file reads, no daemon-reload).
//
// At startup this runs before the main reconcile loop so that the core
// package is always healthy before user services are reconciled. On later
// ticks it re-runs so that adding or removing a service's domain — which
// changes hasPublicDomain and therefore Caddy's published ports (see
// core.BuildCoreOutput) — takes effect without requiring a daemon restart.
//
// hasPublicDomain reports whether any service in ownbase.yaml currently has
// a domain configured (schema.OwnbaseConfig.HasPublicDomain) — see
// core.BuildCoreOutput for how it gates Caddy's published ports.
func bootstrapCore(ctx context.Context, cfg agentConfig, coreCfg schema.CoreConfig, hasPublicDomain bool) error {
	// On first install, merge the ACME email from first-run.env into coreCfg
	// so Caddy starts with the correct email — all before ownbase.yaml exists
	// on disk.
	firstRun := readFirstRunEnv()
	if firstRun.CaddyEmail != "" && coreCfg.Caddy.Email == "" {
		coreCfg.Caddy.Email = firstRun.CaddyEmail
	}

	quadletDir := agentQuadletDir()

	// 1. Generate core Quadlet units from the pinned manifest.
	coreOut := core.BuildCoreOutput(coreCfg, core.Current, hasPublicDomain)

	// 2. Install core unit files to the Quadlet directory, noting which ones
	// actually changed — e.g. Caddy's .container unit gains or loses its
	// 80/443 PublishPort entries when hasPublicDomain toggles. Podman
	// Quadlet bakes PublishPort into the container's create args, so an
	// already-running container must be recreated (systemctl restart, not
	// start) to pick up the change — see step 5.
	if err := os.MkdirAll(quadletDir, 0o755); err != nil {
		return fmt.Errorf("bootstrap core: mkdir quadlet dir: %w", err)
	}
	changedUnits := make(map[string]bool, len(coreOut.QuadletUnits))
	anyChanged := false
	for name, content := range coreOut.QuadletUnits {
		dst := filepath.Join(quadletDir, name)
		existing, _ := os.ReadFile(dst) // ignore error: absent means "changed" (first write)
		if string(existing) != content {
			changedUnits[name] = true
			anyChanged = true
		}
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return fmt.Errorf("bootstrap core: write %s: %w", name, err)
		}
	}

	// 3. Reload systemd so it picks up changed unit files — skipped
	// entirely when nothing changed, so a reconcile tick triggered by an
	// unrelated service update (the common case) doesn't pay the ~1s
	// SIGHUP settle delay below on every single push.
	//
	// Root path: "systemctl daemon-reload" deadlocks when called from inside a
	// systemd service — systemd can't reload while processing the calling
	// service's own transaction. Sending SIGHUP to PID 1 is the documented
	// non-D-Bus equivalent and works from any context.
	//
	// Non-root path: systemctl --user daemon-reload connects to the user session
	// bus, which exists because the ownbase user has linger enabled.
	if anyChanged {
		if runningAsRoot() {
			if err := syscall.Kill(1, syscall.SIGHUP); err != nil {
				return fmt.Errorf("bootstrap core: daemon-reload (SIGHUP): %w", err)
			}
			// Give systemd ~1 s to process the SIGHUP before starting the units.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
		} else {
			if out, err := exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
				return fmt.Errorf("bootstrap core: daemon-reload: %w\n%s", err, out)
			}
		}
	}

	// 4. Pre-create the shared ownbase-internal network.
	// The .network Quadlet unit written in step 2 will manage the network
	// going forward; this call is a belt-and-suspenders ensure for the
	// first run before the unit has been started.
	_ = exec.CommandContext(ctx, "podman", "network", "create", core.OwnbaseInternalNetwork).Run()

	// 5. Bring up each core container unit. "systemctl start" is idempotent
	// (a no-op if already running) but will NOT pick up a changed unit
	// definition on an already-running container — only "restart" does, so
	// units whose content changed (step 2) are explicitly restarted.
	for name := range coreOut.QuadletUnits {
		if !strings.HasSuffix(name, ".container") {
			continue
		}
		svc := strings.TrimSuffix(name, ".container") + ".service"
		verb := "start"
		if changedUnits[name] {
			verb = "restart"
		}
		var out []byte
		var err error
		if runningAsRoot() {
			out, err = exec.CommandContext(ctx, "systemctl", verb, svc).CombinedOutput()
		} else {
			out, err = exec.CommandContext(ctx, "systemctl", "--user", verb, svc).CombinedOutput()
		}
		if err != nil {
			// Non-fatal: log and continue. The next reconcile will retry.
			fmt.Fprintf(os.Stderr, "ownbased: bootstrap core: %s %s (non-fatal): %v — %s\n",
				verb, svc, err, strings.TrimSpace(string(out)))
		}
	}

	return nil
}

// agentQuadletDir returns the Quadlet directory for the process owner.
// Root uses the system-level path (/etc/containers/systemd) so that units are
// managed with system systemctl rather than a user D-Bus session.
func agentQuadletDir() string {
	if runningAsRoot() {
		return "/etc/containers/systemd"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/root/.config/containers/systemd"
	}
	return filepath.Join(home, ".config/containers/systemd")
}

// readFirstRunEnv reads the one-time first-run file written by install.sh
// (currently just the ACME email). Returns a zero-value FirstRunEnv if the
// file does not exist.
//
// The file is deliberately NOT deleted here: bootstrapCore now runs on
// every reconcile tick (see syncCoreForConfig), not just once at daemon
// startup, so deleting it on first use would make every subsequent call in
// the same process see an empty CaddyEmail and regenerate Caddy's Quadlet
// unit without the ACME email it was just configured with — silently
// dropping it and forcing an unnecessary restart. Deletion instead happens
// exactly once, from the one-time startup path in main.go, after the very
// first bootstrapCore call (and, for a brand-new config repo, after
// install.SeedConfigRepo has already persisted the email into ownbase.yaml
// itself — the durable source of truth every later call reads from).
func readFirstRunEnv() install.FirstRunEnv {
	return install.ReadFirstRunEnv(install.FirstRunEnvPath)
}
