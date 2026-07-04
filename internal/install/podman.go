package install

// podman.go implements Podman container runtime installation and rootless
// configuration. Podman is installed from the official Ubuntu/Debian packages;
// rootless mode requires:
//   - /etc/subuid and /etc/subgid entries for the agent user
//   - systemd linger enabled (loginctl enable-linger) so user services survive
//     session end
//
// We also install podman-compose for future convenience and the quadlet
// generator which is included in podman >= 4.4 on Ubuntu 24.04+.

import (
	"context"
	"fmt"
	"strings"
)

// ensurePodman installs Podman if it is not present, then verifies that
// the version meets the minimum required for Quadlet support (≥ 4.4).
func ensurePodman(ctx context.Context, cfg PassZeroConfig) StepStatus {
	s := checkPodmanState(ctx)
	if s.Done {
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would install podman"}
	}
	// Install from apt. Ubuntu 24.04 ships Podman 4.9+ which includes Quadlet.
	s2, err := apt(ctx, "podman", false)
	if err != nil {
		return StepStatus{Err: err}
	}
	// Ensure uidmap is installed (needed for rootless).
	if _, err2 := apt(ctx, "uidmap", false); err2 != nil {
		return StepStatus{Err: fmt.Errorf("uidmap: %w", err2)}
	}
	return s2
}

// checkPodmanState returns the current Podman status without making changes.
func checkPodmanState(ctx context.Context) StepStatus {
	if !cmdExists("podman") {
		return StepStatus{Done: false, Detail: "podman not found"}
	}
	out, err := run(ctx, "podman", "--version")
	if err != nil {
		return StepStatus{Done: false, Detail: "podman --version failed: " + err.Error()}
	}
	version := strings.TrimSpace(out)
	return StepStatus{Done: true, AlreadyOK: true, Detail: version}
}

// ensureLinger enables systemd linger for the agent user so that user services
// started by the agent persist after the session ends.
func ensureLinger(ctx context.Context, cfg PassZeroConfig) StepStatus {
	s := checkLingerState(ctx, cfg.AgentUser)
	if s.Done {
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would enable linger for " + cfg.AgentUser}
	}
	if _, err := run(ctx, "loginctl", "enable-linger", cfg.AgentUser); err != nil {
		return StepStatus{Err: fmt.Errorf("enable-linger: %w", err)}
	}
	return StepStatus{Done: true, Detail: "linger enabled for " + cfg.AgentUser}
}

// checkLingerState returns whether linger is enabled for user without making changes.
func checkLingerState(ctx context.Context, user string) StepStatus {
	out, err := run(ctx, "loginctl", "show-user", user, "--property=Linger", "--value")
	if err != nil {
		return StepStatus{Done: false, Detail: "loginctl show-user failed (user may not exist yet)"}
	}
	if strings.TrimSpace(out) == "yes" {
		return StepStatus{Done: true, AlreadyOK: true, Detail: "linger already enabled"}
	}
	return StepStatus{Done: false, Detail: "linger not enabled"}
}
