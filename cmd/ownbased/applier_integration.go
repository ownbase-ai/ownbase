//go:build integration

package main

import (
	"os"
	"path/filepath"

	"github.com/ownbase/ownbase/internal/podman"
	"github.com/ownbase/ownbase/internal/reconcile"
)

// newApplier returns the real PodmanApplier when compiled with -tags=integration.
// Requires Ubuntu 24.04+, Podman ≥ 4.4, and a rootless systemd session with
// linger enabled (loginctl enable-linger <user>).
func newApplier(cfg agentConfig) reconcile.Applier {
	return &podman.Applier{
		RuntimeDir:   filepath.Join(cfg.checkoutPath, "runtime"),
		ForgejoURL:   cfg.forgejoURL,
		ForgejoUser:  cfg.forgejoUser,
		ForgejoToken: cfg.forgejoToken,
		SecretsDir:   "/opt/ownbase/secrets",
	}
}

func applierMode(_ reconcile.Applier) string {
	return "integration (Podman + systemd-quadlet)"
}

// installedQuadletDir returns the directory where Quadlet unit files are
// actually installed on disk. The reconcile loop reads this directory to
// determine which unit files are present, so the planner can detect when
// a Quadlet file has been removed externally (e.g. after a daemon restart)
// while the Podman resource still exists.
func installedQuadletDir() string {
	home, _ := os.UserHomeDir()
	if os.Getuid() == 0 {
		return "/etc/containers/systemd"
	}
	return filepath.Join(home, ".config/containers/systemd")
}
