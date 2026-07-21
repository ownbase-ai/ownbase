//go:build !integration

package main

import (
	"fmt"
	"os"

	"github.com/ownbase/ownbase/internal/reconcile"
)

// realBaseMarker is a path that only exists on a real OwnBase installation.
// If it is present we are definitely on a Base, not a dev machine or CI runner.
const realBaseMarker = "/opt/ownbase"

// noopGuardApplier wraps NoopApplier with a startup-time check that fires a
// loud, unmistakable warning when the agent is running on a real Base without
// the integration build tag. A non-integration binary can never apply changes
// to real Podman/systemd; silently no-op'ing on a production machine is worse
// than refusing to start.
type noopGuardApplier struct {
	*reconcile.NoopApplier
	warned bool
}

func (g *noopGuardApplier) ApplyAction(a reconcile.PlannedAction) error {
	if !g.warned {
		if _, err := os.Stat(realBaseMarker); err == nil {
			fmt.Fprintf(os.Stderr,
				"\n"+
					"╔══════════════════════════════════════════════════════════════════╗\n"+
					"║  OWNBASE-AGENT: NOOP APPLIER ON A REAL BASE — CHANGES WILL NOT  ║\n"+
					"║  BE APPLIED. Rebuild with -tags=integration to use the real      ║\n"+
					"║  Podman/systemd applier. This binary is a dev/CI build only.     ║\n"+
					"╚══════════════════════════════════════════════════════════════════╝\n\n")
		}
		g.warned = true
	}
	return g.NoopApplier.ApplyAction(a)
}

// newApplier returns a guarded NoopApplier when compiled without -tags=integration.
// The guard warns loudly when the binary is executed on a real OwnBase machine.
func newApplier(_ agentConfig) reconcile.Applier {
	return &noopGuardApplier{NoopApplier: &reconcile.NoopApplier{}}
}

func applierMode(_ reconcile.Applier) string {
	return "noop (build with -tags=integration for real Podman/systemd apply)"
}

// installedQuadletDir returns an empty string in noop/dev builds.
// The reconcile loop treats an empty string as "no quadlet dir available"
// and skips the on-disk installed-units check.
func installedQuadletDir() string { return "" }

// installedTimerDir mirrors installedQuadletDir for the native systemd timer
// directory in noop/dev builds.
func installedTimerDir() string { return "" }
