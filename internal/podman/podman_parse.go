// Package podman — pure provenance parsing, no build-tag constraint.
// These functions parse the compiler-emitted comments from Quadlet unit
// content. They carry no runtime dependency so they compile on any host.
package podman

import (
	"fmt"
	"path/filepath"
	"strings"
)

// QuadletUserDir is the XDG path for rootless Quadlet unit files, relative
// to the user home directory.
const QuadletUserDir = ".config/containers/systemd"

// SystemQuadletDir is the system-level Quadlet unit directory used when the
// agent runs as root. Root has no user D-Bus session in a non-login service
// context, so units must be managed by the system systemctl manager from here.
const SystemQuadletDir = "/etc/containers/systemd"

// injectedSecretsMarker is emitted on its own comment line directly above the
// apply-time EnvironmentFile= directive that injectSecrets adds. It lets
// StripInjectedSecrets remove the injected block again so the reconcile diff can
// compare the on-disk unit against the compiler's (secret-free) output. Without
// it, the on-disk unit would always differ from desired and every reconcile
// tick would needlessly restart the container. Lives here (untagged) so both
// the integration daemon build and the default build can reference it.
const injectedSecretsMarker = "# ownbase:injected-secrets (apply-time; not from ownbase.yaml)"

// StripInjectedSecrets removes the apply-time secrets block that
// insertEnvironmentFile adds (the injectedSecretsMarker comment plus the
// EnvironmentFile= directive immediately below it), yielding content equivalent
// to the compiler's output.
//
// The reconcile loop reads installed units from the live Quadlet directory to
// detect config drift and decide when to restart. Because the injected
// EnvironmentFile directive is deliberately absent from the compiler's view of
// the unit (secrets never live in ownbase.yaml or the config repo), callers must
// strip it before comparing against desired, or every reconcile tick would see a
// spurious "unit content changed" and restart the container forever.
func StripInjectedSecrets(unitContent string) string {
	if !strings.Contains(unitContent, injectedSecretsMarker) {
		return unitContent
	}
	lines := strings.Split(unitContent, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		if lines[i] == injectedSecretsMarker {
			// Drop the marker and the EnvironmentFile= directive it guards.
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "EnvironmentFile=") {
				i++
			}
			continue
		}
		out = append(out, lines[i])
	}
	return strings.Join(out, "\n")
}

// quadletDirFor returns the directory where Quadlet unit files belong for the
// process owner. Root uses the system-level path so units are managed by the
// system manager (no user session bus required); non-root uses the XDG user
// path under home. An empty home falls back to /root for safety.
func quadletDirFor(isRoot bool, home string) string {
	if isRoot {
		return SystemQuadletDir
	}
	if home == "" {
		return filepath.Join("/root", QuadletUserDir)
	}
	return filepath.Join(home, QuadletUserDir)
}

// systemctlArgs returns the argument list (after the "systemctl" program name)
// targeting the correct service manager: the system manager when root, the
// per-user manager (--user) otherwise. This mirrors the root/non-root split
// used by the core bootstrap path so user-service apply and core bootstrap
// agree on which systemd manager owns the units.
func systemctlArgs(isRoot bool, args ...string) []string {
	if isRoot {
		return args
	}
	return append([]string{"--user"}, args...)
}

// parseBuildProvenance extracts the build-instruction comments from a Quadlet
// unit file. These comments are emitted by the compiler for source-built
// services and consumed by the integration Applier to drive the build step.
//
//	# BuildSource=services/auth
//	# BuildRef=v2.1.0
//	# BuildDockerfile=Dockerfile.prod   (optional)
//	# BuildContext=backend              (optional)
//
// Returns empty strings when the unit is image-bundled (no BuildSource comment).
func parseBuildProvenance(unitContent string) (source, ref, dockerfile, buildCtx string) {
	for _, line := range strings.Split(unitContent, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "# BuildSource="):
			source = strings.TrimPrefix(line, "# BuildSource=")
		case strings.HasPrefix(line, "# BuildRef="):
			ref = strings.TrimPrefix(line, "# BuildRef=")
		case strings.HasPrefix(line, "# BuildDockerfile="):
			dockerfile = strings.TrimPrefix(line, "# BuildDockerfile=")
		case strings.HasPrefix(line, "# BuildContext="):
			buildCtx = strings.TrimPrefix(line, "# BuildContext=")
		}
	}
	return
}

// parseStartProvenance extracts the runtime-injection comments from a Quadlet
// unit file. These are consumed during the apply step (M11) to poll the
// health endpoint after start and gate success (HealthProbeHTTP).
//
//	# HealthProbeHTTP=/health
//
// Returns an empty string when the directive is absent.
// Secrets are discovered by convention (/opt/ownbase/secrets/<service>.yaml.age)
// and are not encoded in the unit file.
func parseStartProvenance(unitContent string) (healthProbeURL string) {
	for _, line := range strings.Split(unitContent, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# HealthProbeHTTP=") {
			healthProbeURL = strings.TrimPrefix(line, "# HealthProbeHTTP=")
		}
	}
	return
}

// parseQuadletComment extracts the value from a "# Key=value" provenance
// comment in a Quadlet unit file. Returns empty string when the comment is
// absent. This is the single generic extractor; typed helpers delegate to it.
func parseQuadletComment(key, unitContent string) string {
	prefix := "# " + key + "="
	for _, line := range strings.Split(unitContent, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

// parsePublishPort extracts the host port number from the first
// "PublishPort=127.0.0.1:<port>:<containerPort>" line in a Quadlet unit file.
// Returns 0 when no such line is found or the port cannot be parsed.
func parsePublishPort(unitContent string) int {
	const prefix = "PublishPort="
	for _, line := range strings.Split(unitContent, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		// Format: PublishPort=[ip:]hostPort:containerPort
		rest := strings.TrimPrefix(line, prefix)
		// Strip optional IP prefix (e.g. "127.0.0.1:")
		if idx := strings.LastIndex(rest, ":"); idx >= 0 {
			// hostPort is the second-to-last segment; split on ":" from right
			parts := strings.Split(rest, ":")
			if len(parts) >= 2 {
				// host port is parts[len-2], container port is parts[len-1]
				hostPortStr := parts[len(parts)-2]
				var port int
				if _, err := fmt.Sscanf(hostPortStr, "%d", &port); err == nil && port > 0 {
					return port
				}
			}
		}
	}
	return 0
}
