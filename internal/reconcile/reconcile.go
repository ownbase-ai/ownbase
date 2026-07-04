// Package reconcile implements the diff → plan → apply loop.
// M0.5 delivers plan+dry-run; M3 grows it into the full transactional engine.
package reconcile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/runtime"
	"github.com/ownbase/ownbase/internal/schema"
)

// PlannedAction is a single step in the reconcile plan. Every action carries
// an Action from the taxonomy and enough metadata to render a human-readable
// diff and, post-V1, evaluate governance policy.
type PlannedAction struct {
	Action schema.Action
	// Description is a human-readable summary of what this action would do.
	Description string
	// Before / After describe the state change (empty string = not applicable).
	Before string
	After  string
	// UnitFilename is the basename of the Quadlet unit file associated with
	// this action (e.g. "ownbase-auth.container", "ownbase-auth-net.network").
	// Set for service.start and service.stop actions; empty for others.
	UnitFilename string
	// UnitContent is the full text content of the unit file. Set for
	// service.start actions so the Applier can install the file without
	// an additional disk read. Empty for service.stop and service.reload.
	UnitContent string
	// CaddyfileContent is the full Caddyfile text for service.reload actions
	// targeting Caddy. Empty for all other action types.
	CaddyfileContent string
}

// Plan is an ordered list of PlannedActions to converge current → desired.
type Plan struct {
	Actions []PlannedAction
}

// IsEmpty returns true when the plan has no actions (already converged).
func (p *Plan) IsEmpty() bool { return len(p.Actions) == 0 }

// unitKind classifies a Quadlet unit filename into "container", "network",
// "volume", or "other". Only containers are started/stopped; networks and
// volumes are applied as setup steps.
func unitKind(filename string) string {
	switch {
	case strings.HasSuffix(filename, ".container"):
		return "container"
	case strings.HasSuffix(filename, ".network"):
		return "network"
	case strings.HasSuffix(filename, ".volume"):
		return "volume"
	default:
		return "other"
	}
}

// DiffOptions carries optional context that makes the diff more precise.
// Callers that do not have this information can leave fields at their zero
// values; the diff degrades gracefully (no restart actions, no Caddyfile-only
// reloads).
type DiffOptions struct {
	// CurrentCaddyfile is the Caddyfile content that is currently deployed
	// (read from runtime/Caddyfile). When it differs from desired.Caddyfile,
	// a reload action is emitted even if no containers changed.
	CurrentCaddyfile string
	// CurrentUnits maps unit filename → content for every unit that is
	// currently installed on disk (read from runtime/). When a container is
	// already running but its unit content has changed, a restart action is
	// emitted instead of a no-op.
	CurrentUnits map[string]string
	// InstalledUnits is the set of Quadlet unit file basenames that actually
	// exist in the Quadlet directory on disk (e.g. /etc/containers/systemd/).
	// When provided, the planner checks this set to detect network and volume
	// Quadlet files that are missing from disk even though the corresponding
	// Podman resource still exists (e.g. after a daemon restart that cleaned
	// up unit files without removing Podman objects). Without the unit file,
	// systemd cannot satisfy the service dependency when a container starts.
	// Leave nil to skip the on-disk presence check (dev/CI builds).
	InstalledUnits map[string]bool
}

// Diff computes the ordered plan needed to converge current state to desired.
// M3 will make this full-fidelity against real podman/systemd; here it diffs
// the set of Quadlet unit filenames.
//
// opts carries optional context for more precise diffing (Caddyfile content
// comparison, unit content comparison for restart detection). Pass a zero
// DiffOptions when this context is unavailable.
func Diff(desired compiler.RuntimeOutput, current runtime.CurrentState, opts DiffOptions) (Plan, error) {
	var actions []PlannedAction

	// Separate units by kind: networks and volumes are setup prerequisites;
	// containers are the things that start/stop.
	desiredContainers := map[string]string{} // containerName → unitFilename
	var desiredNetworks, desiredVolumes []string

	for filename := range desired.QuadletUnits {
		switch unitKind(filename) {
		case "container":
			containerName := strings.TrimSuffix(filename, ".container")
			desiredContainers[containerName] = filename
		case "network":
			desiredNetworks = append(desiredNetworks, filename)
		case "volume":
			desiredVolumes = append(desiredVolumes, filename)
		}
	}
	sort.Strings(desiredNetworks)
	sort.Strings(desiredVolumes)

	// Networks first (containers depend on them).
	for _, unitFile := range desiredNetworks {
		netName := strings.TrimSuffix(unitFile, ".network")
		// Re-create when either:
		// (a) the Podman network object doesn't exist, or
		// (b) the Quadlet unit file is missing from disk — InstalledUnits is
		//     read from the actual quadlet dir (/etc/containers/systemd/), so
		//     it accurately reflects what systemd knows about. A Podman network
		//     can outlive its unit file after a daemon restart; without the file
		//     systemd cannot satisfy "ownbase-X-net-network.service" when a
		//     dependent container starts.
		unitOnDisk := opts.InstalledUnits == nil || opts.InstalledUnits[unitFile]
		if !current.PresentNetworks[netName] || !unitOnDisk {
			a, err := schema.NewAction(schema.ActionServiceStart, netName)
			if err != nil {
				return Plan{}, fmt.Errorf("build action: %w", err)
			}
			actions = append(actions, PlannedAction{
				Action:       a,
				Description:  fmt.Sprintf("create network %q", netName),
				Before:       "absent",
				After:        "present",
				UnitFilename: unitFile,
				UnitContent:  desired.QuadletUnits[unitFile],
			})
		}
	}

	// Volumes next.
	for _, unitFile := range desiredVolumes {
		volName := strings.TrimSuffix(unitFile, ".volume")
		// Same dual check as networks above: re-create when the Podman volume
		// object is absent OR when its Quadlet unit file is missing from disk.
		unitOnDisk := opts.InstalledUnits == nil || opts.InstalledUnits[unitFile]
		if !current.PresentVolumes[volName] || !unitOnDisk {
			a, err := schema.NewAction(schema.ActionServiceStart, volName)
			if err != nil {
				return Plan{}, fmt.Errorf("build action: %w", err)
			}
			actions = append(actions, PlannedAction{
				Action:       a,
				Description:  fmt.Sprintf("create volume %q", volName),
				Before:       "absent",
				After:        "present",
				UnitFilename: unitFile,
				UnitContent:  desired.QuadletUnits[unitFile],
			})
		}
	}

	// Containers in topological order by requires: dependency graph.
	// Providers must start before consumers so that a service's capabilities
	// are healthy by the time its dependents try to connect.
	sortedDesired, err := topoSortContainers(desiredContainers, desired.QuadletUnits)
	if err != nil {
		return Plan{}, fmt.Errorf("requires: dependency cycle: %w", err)
	}

	for _, containerName := range sortedDesired {
		unitFile := desiredContainers[containerName]
		desiredContent := desired.QuadletUnits[unitFile]
		if !current.RunningContainers[containerName] {
			a, err := schema.NewAction(schema.ActionServiceStart, containerName)
			if err != nil {
				return Plan{}, fmt.Errorf("build action: %w", err)
			}
			actions = append(actions, PlannedAction{
				Action:       a,
				Description:  fmt.Sprintf("start container %q (unit: %s)", containerName, unitFile),
				Before:       "not running",
				After:        "running",
				UnitFilename: unitFile,
				UnitContent:  desiredContent,
			})
		} else if opts.CurrentUnits != nil && desiredContent != opts.CurrentUnits[unitFile] {
			// Container is running but its unit file has changed — restart to
			// pick up the new configuration (new env var, volume mount, etc.).
			a, err := schema.NewAction(schema.ActionServiceRestart, containerName)
			if err != nil {
				return Plan{}, fmt.Errorf("build action: %w", err)
			}
			actions = append(actions, PlannedAction{
				Action:       a,
				Description:  fmt.Sprintf("restart container %q (unit content changed)", containerName),
				Before:       "running (stale config)",
				After:        "running (new config)",
				UnitFilename: unitFile,
				UnitContent:  desiredContent,
			})
		}
	}

	// Containers running but not in desired → stop them.
	sortedCurrent := make([]string, 0, len(current.RunningContainers))
	for cn := range current.RunningContainers {
		sortedCurrent = append(sortedCurrent, cn)
	}
	sort.Strings(sortedCurrent)

	for _, containerName := range sortedCurrent {
		if _, wanted := desiredContainers[containerName]; !wanted {
			a, err := schema.NewAction(schema.ActionServiceStop, containerName)
			if err != nil {
				return Plan{}, fmt.Errorf("build action: %w", err)
			}
			actions = append(actions, PlannedAction{
				Action:       a,
				Description:  fmt.Sprintf("stop container %q (not in desired state)", containerName),
				Before:       "running",
				After:        "not running",
				UnitFilename: containerName + ".container",
			})
		}
	}

	// Caddy reload: emit when the Caddyfile changed (content comparison) or
	// when there are other actions and a Caddyfile exists. This ensures Caddy
	// is reloaded both when routes change without container churn and when
	// container changes imply route updates.
	//
	// Caddyfile-only reloads (no other actions) are only emitted when the
	// caller provides a CurrentCaddyfile snapshot (non-empty). An empty
	// CurrentCaddyfile means "no prior snapshot available" — we fall back to
	// the old behavior and only reload when other actions are present.
	caddyfileChanged := desired.Caddyfile != "" &&
		opts.CurrentCaddyfile != "" &&
		desired.Caddyfile != opts.CurrentCaddyfile
	if (len(actions) > 0 || caddyfileChanged) && desired.Caddyfile != "" {
		a, err := schema.NewAction(schema.ActionServiceReload, "caddy")
		if err != nil {
			return Plan{}, fmt.Errorf("build action: %w", err)
		}
		actions = append(actions, PlannedAction{
			Action:           a,
			Description:      "reload Caddy with updated Caddyfile",
			CaddyfileContent: desired.Caddyfile,
		})
	}

	return Plan{Actions: actions}, nil
}

// ---------------------------------------------------------------------------
// Dependency ordering helpers
// ---------------------------------------------------------------------------

// parseRequires extracts the comma-separated service names from the
// "# Requires=dep1,dep2" provenance comment emitted by the compiler.
// Returns nil when the comment is absent or has no names.
func parseRequires(unitContent string) []string {
	const prefix = "# Requires="
	for _, line := range strings.Split(unitContent, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			raw := strings.TrimPrefix(line, prefix)
			if raw == "" {
				return nil
			}
			parts := strings.Split(raw, ",")
			result := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					result = append(result, p)
				}
			}
			return result
		}
	}
	return nil
}

// topoSortContainers returns container names in an order where each provider
// appears before all of its consumers. desiredContainers maps
// containerName → unitFilename (e.g. "ownbase-hello" → "ownbase-hello.container").
// unitContents maps unitFilename → full content (from compiler.RuntimeOutput.QuadletUnits).
//
// The dependency graph is derived from the "# Requires=" provenance comments
// in the unit content. Service name "foo" maps to container name "ownbase-foo"
// following the ownbase- naming convention.
//
// Returns an error when a cycle is detected.
func topoSortContainers(desiredContainers map[string]string, unitContents map[string]string) ([]string, error) {
	// Build adjacency list: for each container, which containers must start first.
	// deps[x] = set of container names that x depends on.
	deps := make(map[string]map[string]bool, len(desiredContainers))
	for containerName, unitFile := range desiredContainers {
		deps[containerName] = make(map[string]bool)
		content := unitContents[unitFile]
		reqs := parseRequires(content)
		for _, svcName := range reqs {
			providerContainer := "ownbase-" + svcName
			// Only add edges for providers that are also in the desired set.
			// Unknown providers are silently skipped — schema validation
			// already ensures requires: names are valid service keys.
			if _, ok := desiredContainers[providerContainer]; ok {
				deps[containerName][providerContainer] = true
			}
		}
	}

	// Kahn's algorithm: nodes with no remaining in-edges go first.
	// in-degree of a node = number of providers it depends on.
	// adj[provider] = list of consumers that must start after provider.
	inDeg := make(map[string]int, len(desiredContainers))
	adj := make(map[string][]string, len(desiredContainers))
	for name := range desiredContainers {
		inDeg[name] = 0
		adj[name] = nil
	}
	for consumer, providers := range deps {
		for provider := range providers {
			inDeg[consumer]++
			adj[provider] = append(adj[provider], consumer)
		}
	}

	// Collect zero-in-degree nodes in sorted order for determinism.
	queue := make([]string, 0, len(desiredContainers))
	for name := range desiredContainers {
		if inDeg[name] == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue)

	result := make([]string, 0, len(desiredContainers))
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		result = append(result, node)

		// Collect newly-ready nodes in sorted order for determinism.
		var newReady []string
		for _, consumer := range adj[node] {
			inDeg[consumer]--
			if inDeg[consumer] == 0 {
				newReady = append(newReady, consumer)
			}
		}
		sort.Strings(newReady)
		queue = append(queue, newReady...)
	}

	if len(result) != len(desiredContainers) {
		// Some nodes were never dequeued — there's a cycle.
		var cycle []string
		for name := range desiredContainers {
			if inDeg[name] > 0 {
				cycle = append(cycle, name)
			}
		}
		sort.Strings(cycle)
		return nil, fmt.Errorf("containers involved in cycle: %s", strings.Join(cycle, ", "))
	}

	return result, nil
}

// ApplyDryRun walks the plan through the authorization checkpoint and prints
// each action as "would do X" without any side effects. It returns an error
// only if the checkpoint refuses an action (taxonomy violation).
//
// The authorization checkpoint is the V1 trivially-permissive gate from
// internal/authz. Every action routes through it so the seam exists from
// the start — Architecture Principle 15.
func ApplyDryRun(plan Plan, checkpoint authz.Checkpoint) error {
	for _, pa := range plan.Actions {
		if err := checkpoint.Authorize(pa.Action); err != nil {
			return fmt.Errorf("checkpoint refused %q: %w", pa.Action.Type, err)
		}
		printDryRunAction(pa)
	}
	return nil
}

func printDryRunAction(pa PlannedAction) {
	fmt.Printf("[dry-run] %-20s tier=%-10s %s\n",
		pa.Action.Type, pa.Action.DefaultTier, pa.Description)
	if pa.Before != "" || pa.After != "" {
		fmt.Printf("          before=%-15s after=%s\n", pa.Before, pa.After)
	}
}

// RenderPlanText returns a human-readable multi-line string of the plan.
func RenderPlanText(plan Plan) string {
	if plan.IsEmpty() {
		return "Plan: no changes — already converged.\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Plan: %d action(s)\n", len(plan.Actions))
	for i, pa := range plan.Actions {
		fmt.Fprintf(&b, "  %d. [%s/%s] %s\n", i+1, pa.Action.Type, pa.Action.DefaultTier, pa.Description)
		if pa.Before != "" {
			fmt.Fprintf(&b, "     before: %s → after: %s\n", pa.Before, pa.After)
		}
	}
	return b.String()
}
