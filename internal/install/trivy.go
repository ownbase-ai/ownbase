package install

// trivy.go installs the trivy vulnerability scanner from the official Aqua
// Security apt repository. Trivy is scanning infrastructure, not runtime
// infrastructure — its installation failure is non-fatal: PassZero records
// the result in HardeningReport.Trivy but does not abort the pass.

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// trivySourcesPath is the apt sources.list.d entry for the trivy repo.
const trivySourcesPath = "/etc/apt/sources.list.d/trivy.list"

// trivyKeyringsPath is where the signed apt key is stored.
const trivyKeyringsPath = "/usr/share/keyrings/trivy.gpg"

// trivySourcesContent is the apt source line for the official trivy repo.
// "generic" works across all Ubuntu/Debian releases.
const trivySourcesContent = "deb [signed-by=/usr/share/keyrings/trivy.gpg] https://aquasecurity.github.io/trivy-repo/deb generic main\n"

// podmanSocketUnit is the systemd unit that listens on the rootful Podman API
// socket (/run/podman/podman.sock). Trivy is the only thing in OwnBase that
// talks to it — see internal/vulnscan — and Ubuntu's podman package ships the
// unit disabled, so installing podman alone leaves every image scan failing
// with "no podman socket found" even though podman itself works fine via the
// CLI.
const podmanSocketUnit = "podman.socket"

// ensureTrivy installs trivy if it is not already present, and enables the
// Podman API socket it needs to actually scan images. Returns a StepStatus —
// never returns an error to PassZero so a transient network failure or
// missing curl/gpg does not block the daemon from starting.
func ensureTrivy(ctx context.Context, cfg PassZeroConfig) StepStatus {
	if s := checkTrivyState(ctx); s.Done {
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would install trivy (apt) and enable podman.socket"}
	}

	if !cmdExists("trivy") {
		// Add the apt signing key (requires curl and gpg, both present on Ubuntu).
		if _, err := run(ctx, "curl", "-fsSL",
			"https://aquasecurity.github.io/trivy-repo/deb/public.key",
			"--output", "/tmp/trivy-key.asc",
		); err != nil {
			return StepStatus{Err: fmt.Errorf("trivy: download apt key: %w", err)}
		}
		if _, err := run(ctx, "gpg",
			"--dearmor", "--batch", "--yes",
			"-o", trivyKeyringsPath,
			"/tmp/trivy-key.asc",
		); err != nil {
			return StepStatus{Err: fmt.Errorf("trivy: gpg dearmor key: %w", err)}
		}

		// Write sources.list entry (idempotent).
		if err := writeTrivySourcesList(); err != nil {
			return StepStatus{Err: fmt.Errorf("trivy: write sources.list: %w", err)}
		}

		// Refresh apt indices so trivy is available to install.
		if _, err := run(ctx, "apt-get", "update", "-q"); err != nil {
			return StepStatus{Err: fmt.Errorf("trivy: apt-get update: %w", err)}
		}

		if _, err := apt(ctx, "trivy", false); err != nil {
			return StepStatus{Err: fmt.Errorf("trivy: install: %w", err)}
		}
	}

	if !podmanSocketActive(ctx) {
		if _, err := run(ctx, "systemctl", "enable", "--now", podmanSocketUnit); err != nil {
			return StepStatus{Err: fmt.Errorf("trivy: enable %s: %w", podmanSocketUnit, err)}
		}
	}

	return checkTrivyState(ctx)
}

// checkTrivyState returns the current trivy installation status without making
// any changes. Used by PassZero (idempotency guard) and CheckHardeningState.
//
// "Done" requires the Podman socket too, not just the trivy binary: a trivy
// that cannot reach /run/podman/podman.sock cannot scan anything, so reporting
// it as satisfied would hide exactly the failure mode this step exists to
// prevent.
func checkTrivyState(ctx context.Context) StepStatus {
	if !cmdExists("trivy") {
		return StepStatus{Done: false, Detail: "trivy not installed"}
	}
	out, err := run(ctx, "trivy", "--version")
	if err != nil {
		return StepStatus{Done: false, Detail: "trivy --version failed: " + err.Error()}
	}
	if !podmanSocketActive(ctx) {
		return StepStatus{Done: false, Detail: podmanSocketUnit + " is not active — image scans will fail"}
	}
	// trivy --version prints e.g. "Version: 0.57.1\n..."; keep just the first line.
	version := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	return StepStatus{Done: true, AlreadyOK: true, Detail: version}
}

// podmanSocketActive reports whether the rootful Podman API socket is up.
func podmanSocketActive(ctx context.Context) bool {
	out, err := run(ctx, "systemctl", "is-active", podmanSocketUnit)
	return err == nil && strings.TrimSpace(out) == "active"
}

// writeTrivySourcesList writes the trivy apt source entry. Idempotent: skips
// the write when the file already has the expected content.
func writeTrivySourcesList() error {
	existing, _ := os.ReadFile(trivySourcesPath)
	if string(existing) == trivySourcesContent {
		return nil
	}
	return os.WriteFile(trivySourcesPath, []byte(trivySourcesContent), 0o644)
}
