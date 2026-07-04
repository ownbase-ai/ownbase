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

// ensureTrivy installs trivy if it is not already present. Returns a
// StepStatus — never returns an error to PassZero so a transient network
// failure or missing curl/gpg does not block the daemon from starting.
func ensureTrivy(ctx context.Context, cfg PassZeroConfig) StepStatus {
	if s := checkTrivyState(ctx); s.Done {
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would install trivy (apt)"}
	}

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

	s, err := apt(ctx, "trivy", false)
	if err != nil {
		return StepStatus{Err: fmt.Errorf("trivy: install: %w", err)}
	}
	return s
}

// checkTrivyState returns the current trivy installation status without making
// any changes. Used by PassZero (idempotency guard) and CheckHardeningState.
func checkTrivyState(ctx context.Context) StepStatus {
	if !cmdExists("trivy") {
		return StepStatus{Done: false, Detail: "trivy not installed"}
	}
	out, err := run(ctx, "trivy", "--version")
	if err != nil {
		return StepStatus{Done: false, Detail: "trivy --version failed: " + err.Error()}
	}
	// trivy --version prints e.g. "Version: 0.57.1\n..."; keep just the first line.
	version := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	return StepStatus{Done: true, AlreadyOK: true, Detail: version}
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
