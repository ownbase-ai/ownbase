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

// bootstrapCore installs and starts the core Quadlet units (Forgejo + Caddy)
// then calls install.BootstrapCore to create the admin user, token, base repo,
// and webhook. Safe to call on every agent startup — every step is idempotent.
//
// This runs after pass zero and before the main reconcile loop so that core
// packages are always healthy before user services are reconciled.
func bootstrapCore(ctx context.Context, cfg agentConfig, coreCfg schema.CoreConfig) error {
	// On first install, merge domain/email from first-run.env into coreCfg so
	// Forgejo starts with the correct ROOT_URL and the seeded ownbase.yaml has
	// the domain pre-filled — all before ownbase.yaml exists on disk.
	firstRun := readFirstRunEnv()
	if firstRun.ForgejoDomain != "" && coreCfg.Forgejo.Domain == "" {
		coreCfg.Forgejo.Domain = firstRun.ForgejoDomain
	}
	if firstRun.CaddyEmail != "" && coreCfg.Caddy.Email == "" {
		coreCfg.Caddy.Email = firstRun.CaddyEmail
	}
	if firstRun.DevTLS && !coreCfg.Caddy.DevTLS {
		coreCfg.Caddy.DevTLS = true
	}

	quadletDir := agentQuadletDir()

	// 1. Generate core Quadlet units from the pinned manifest.
	coreOut := core.BuildCoreOutput(coreCfg, core.Current)

	// 2. Install core unit files to the Quadlet directory.
	if err := os.MkdirAll(quadletDir, 0o755); err != nil {
		return fmt.Errorf("bootstrap core: mkdir quadlet dir: %w", err)
	}
	for name, content := range coreOut.QuadletUnits {
		dst := filepath.Join(quadletDir, name)
		if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
			return fmt.Errorf("bootstrap core: write %s: %w", name, err)
		}
	}

	// 3. Reload systemd so it picks up the new unit files.
	//
	// Root path: "systemctl daemon-reload" deadlocks when called from inside a
	// systemd service — systemd can't reload while processing the calling
	// service's own transaction. Sending SIGHUP to PID 1 is the documented
	// non-D-Bus equivalent and works from any context.
	//
	// Non-root path: systemctl --user daemon-reload connects to the user session
	// bus, which exists because the ownbase user has linger enabled.
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

	// 4. Pre-create the shared ownbase-internal network.
	// The .network Quadlet unit written in step 2 will manage the network
	// going forward; this call is a belt-and-suspenders ensure for the
	// first run before the unit has been started.
	_ = exec.CommandContext(ctx, "podman", "network", "create", core.OwnbaseInternalNetwork).Run()

	// 5. Start each core container unit (idempotent — already-running is fine).
	// "systemctl start <other-unit>" starts a different unit and does not
	// deadlock; it is safe to call from within a service.
	for name := range coreOut.QuadletUnits {
		if !strings.HasSuffix(name, ".container") {
			continue
		}
		svc := strings.TrimSuffix(name, ".container") + ".service"
		var out []byte
		var err error
		if runningAsRoot() {
			out, err = exec.CommandContext(ctx, "systemctl", "start", svc).CombinedOutput()
		} else {
			out, err = exec.CommandContext(ctx, "systemctl", "--user", "start", svc).CombinedOutput()
		}
		if err != nil {
			// Non-fatal: log and continue. The next reconcile will retry.
			fmt.Fprintf(os.Stderr, "ownbased: bootstrap core: start %s (non-fatal): %v — %s\n",
				svc, err, strings.TrimSpace(string(out)))
		}
	}

	// 6. Run install.BootstrapCore: wait for Forgejo, create admin/token/repo/webhook.
	// This is fully idempotent — if already done, each step is a fast no-op.
	// Use the container IP directly to bypass localhost port-forwarding races.
	forgejoBaseURL := discoverForgejoURL(fmt.Sprintf("http://localhost:%d", core.DefaultForgejoPort))
	bootstrapCfg := install.CoreBootstrapConfig{
		CoreConfig:      coreCfg,
		BareRepoPath:    cfg.repoPath,
		TokenPath:       install.DefaultTokenPath,
		ForgejoBaseURL:  forgejoBaseURL,
		AgentWebhookURL: fmt.Sprintf("http://host.containers.internal:%s/api/v1/hook/push", statusPort(cfg.statusAddr)),
		AdminPassword:   firstRun.Password,
		OwnerSSHKey:     firstRun.SSHKey,
		DryRun:          cfg.dryRun,
	}
	if err := install.BootstrapCore(ctx, bootstrapCfg); err != nil {
		// Non-fatal: Forgejo may be starting up; the token may already exist.
		// The reconcile loop will work with whatever token is already on disk.
		fmt.Fprintf(os.Stderr, "ownbased: bootstrap core (non-fatal): %v\n", err)
	} else {
		// Credentials consumed — delete the one-time file.
		deleteFirstRunEnv()
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

// statusPort extracts just the port from an addr like "0.0.0.0:7070".
func statusPort(addr string) string {
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		return addr[idx+1:]
	}
	return "7070"
}

// discoverForgejoURL returns the URL that the agent should use to reach
// Forgejo. On systems where rootful Podman's port forwarding is unreliable
// (DNAT rules set up asynchronously), we bypass localhost:3000 and talk
// directly to the container IP. Falls back to cfg.forgejoURL if the
// container isn't running.
func discoverForgejoURL(defaultURL string) string {
	port := "3000"
	if defaultURL != "" {
		// Extract port from default URL if non-standard.
		if idx := strings.LastIndex(defaultURL, ":"); idx >= 0 {
			if p := defaultURL[idx+1:]; p != "3000" && len(p) > 0 {
				port = p
			}
		}
	}
	out, err := exec.Command(
		"podman", "inspect", "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}",
		core.ForgejoContainerName,
	).Output()
	if err != nil {
		return defaultURL
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		return defaultURL
	}
	return "http://" + ip + ":" + port
}

const firstRunEnvPath = install.FirstRunEnvPath

// readFirstRunEnv reads owner credentials from the one-time first-run file
// written by install.sh. Returns a zero-value FirstRunEnv if the file does
// not exist. The file is NOT deleted here — deleteFirstRunEnv does that after
// a successful bootstrap so that a failed bootstrap retries with the same values.
func readFirstRunEnv() install.FirstRunEnv {
	return install.ReadFirstRunEnv(firstRunEnvPath)
}

// deleteFirstRunEnv removes the one-time credentials file after the agent
// has successfully consumed it. Errors are silently ignored.
func deleteFirstRunEnv() {
	install.DeleteFirstRunEnv(firstRunEnvPath)
}
