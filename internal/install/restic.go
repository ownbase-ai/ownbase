package install

// restic.go installs the restic backup tool from the standard Ubuntu apt
// repository — no extra signing key or sources.list entry is needed, unlike
// trivy. Installation failure is non-fatal to PassZero, matching the trivy
// pattern: a Base with no backups configured yet should still start;
// `ownbasectl backup setup`/`status` is where a missing restic binary
// should surface, not a blocked daemon startup.

import (
	"context"
	"fmt"
	"strings"
)

// ensureRestic installs restic if it is not already present. Idempotent.
func ensureRestic(ctx context.Context, cfg PassZeroConfig) StepStatus {
	if s := checkResticState(ctx); s.Done {
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would install restic (apt)"}
	}
	s, err := apt(ctx, "restic", false)
	if err != nil {
		return StepStatus{Err: fmt.Errorf("restic: install: %w", err)}
	}
	return s
}

// checkResticState returns the current restic installation status without
// making any changes. Used by PassZero (idempotency guard) and
// CheckHardeningState.
func checkResticState(ctx context.Context) StepStatus {
	if !cmdExists("restic") {
		return StepStatus{Done: false, Detail: "restic not installed"}
	}
	out, err := run(ctx, "restic", "version")
	if err != nil {
		return StepStatus{Done: false, Detail: "restic version failed: " + err.Error()}
	}
	// "restic version" prints e.g. "restic 0.16.4 compiled with go1.21.5 on linux/amd64".
	version := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	return StepStatus{Done: true, AlreadyOK: true, Detail: version}
}
