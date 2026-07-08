package bridge

// quadlet.go closes a race in the tunnel port allocation: TunnelPorts()
// (internal/schema) assigns each eligible service a sorted index over the
// CURRENT ownbase.yaml, so adding, removing, or renaming any one eligible
// service shifts every alphabetically-later service's number. If
// `ownbasectl tunnel` reads ownbase.yaml immediately after such a push — before
// the daemon's reconcile has actually re-published the affected containers —
// a value it computes can diverge from what's really running, and can even
// land on a host port a *different* service's container still occupies,
// silently tunneling to the wrong backend instead of just failing loudly.
//
// The fix: read the loopback host port each service's Quadlet unit is
// ACTUALLY published on right now, straight off the Base, instead of trusting
// an independently recomputed value. This stays true to the "no daemon call"
// tunnel bridge design (see docs/decisions.md) — it's just one more file read
// over the same SSH connection already used to fetch ownbase.yaml — while
// guaranteeing the tunnel always targets exactly what's currently applied.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SystemQuadletDir is the system-level Quadlet unit directory the daemon
// writes generated .container files to (see internal/podman.SystemQuadletDir,
// duplicated here as a plain constant so this package doesn't have to import
// internal/podman — and its os/exec-heavy transitive dependencies — just for
// one path string).
const SystemQuadletDir = "/etc/containers/systemd"

// GrepPublishPortCommand is the remote shell command `ownbasectl tunnel` runs
// over SSH to read every bridged service's actually-applied PublishPort=
// line in one round trip. Always exits 0 (via "; true") even when no units
// exist yet (e.g. nothing has been reconciled since a fresh service add) —
// callers must treat empty/missing entries as "not yet known," not an error.
const GrepPublishPortCommand = `grep -H '^PublishPort=' ` + SystemQuadletDir + `/ownbase-*.container 2>/dev/null; true`

// ParseActualHostPorts parses GrepPublishPortCommand's output — lines of the
// form "<path>/ownbase-<service>.container:PublishPort=<ip>:<host>:<container>"
// (grep -H's "filename:matchline" format) — into a map of service name to
// its currently-published loopback host port. A line that doesn't match the
// expected shape (not a service unit, or the unit has no PublishPort= line —
// e.g. a port'd-but-domain-less service) is silently skipped: a service
// absent from the result means "unknown," and the caller should fall back to
// its own freshly-computed guess in that case.
func ParseActualHostPorts(grepOutput string) map[string]int {
	ports := make(map[string]int)
	for _, line := range strings.Split(grepOutput, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pathPart, rest, ok := strings.Cut(line, ":PublishPort=")
		if !ok {
			continue
		}
		base := filepath.Base(pathPart)
		if !strings.HasPrefix(base, "ownbase-") || !strings.HasSuffix(base, ".container") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(base, "ownbase-"), ".container")

		// Format: PublishPort=[ip:]hostPort:containerPort — host port is the
		// second-to-last colon-separated field, mirroring
		// internal/podman.parsePublishPort's handling of the same syntax.
		fields := strings.Split(rest, ":")
		if len(fields) < 2 {
			continue
		}
		var port int
		if _, err := fmt.Sscanf(fields[len(fields)-2], "%d", &port); err == nil && port > 0 {
			ports[name] = port
		}
	}
	return ports
}
