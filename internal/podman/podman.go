//go:build integration

// Package podman implements the reconcile.Applier interface using the Podman
// CLI and systemd/Quadlet. It lives in its own package to avoid an import
// cycle between internal/runtime (which reconcile imports) and
// internal/reconcile (which the applier imports for PlannedAction).
//
// Dependency graph:
//
//	internal/runtime   ← internal/reconcile ← internal/podman
//	                                         ↗
//	                   internal/schema ──────
package podman

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ownbase/ownbase/internal/core"
	"github.com/ownbase/ownbase/internal/reconcile"
	"github.com/ownbase/ownbase/internal/schema"
)

// Applier implements reconcile.Applier using the Podman CLI and
// systemd/Quadlet. It requires:
//   - Ubuntu 24.04 (or any Linux with Podman ≥ 4.4 + Quadlet generator)
//   - Root (units → /etc/containers/systemd, system manager, SIGHUP reload)
//     or a rootless user systemd session (loginctl enable-linger <user>).
//     The owner is detected at runtime; both paths use the same code.
//
// For source-built services, the Applier clones the repo at the pinned ref
// from the local Forgejo instance and runs `podman build` before starting
// the Quadlet unit. ForgejoURL and ForgejoToken are required for this path.
//
// All resources it creates carry the "ownbase-" prefix and are visible to
// runtime.QueryCurrentState. Rollback removes unit files and stops services
// so the machine returns to a known state.
type Applier struct {
	// QuadletDir is the directory where Quadlet unit files are installed.
	// Defaults to ~/.config/containers/systemd.
	QuadletDir string

	// RuntimeDir is the checkout's runtime/ tracking directory. When set,
	// applied unit files and the Caddyfile are mirrored here so the drift
	// detector sees them on the next reconcile tick.
	// Example: "/opt/ownbase/checkout/runtime"
	RuntimeDir string

	// ForgejoURL is the base URL of the on-Base Forgejo instance, used to
	// construct git clone URLs for source-built services.
	// Example: "http://localhost:3000"
	ForgejoURL string

	// ForgejoUser is the Forgejo user used as the git clone username and as
	// the default repo owner when source paths lack an explicit org prefix.
	ForgejoUser string

	// ForgejoToken is the Forgejo API token embedded in clone URLs for auth.
	// If empty, unauthenticated clones are attempted (works for public repos).
	ForgejoToken string

	// SecretsDir is the directory containing age-encrypted secrets files,
	// one per service: <SecretsDir>/<service>.yaml.age. Defaults to
	// /opt/ownbase/secrets/. The agent decrypts the file (if it exists) at
	// apply time and injects all key-value pairs as environment variables.
	SecretsDir string
}

var _ reconcile.Applier = (*Applier)(nil)

func (p *Applier) quadletDir() string {
	if p.QuadletDir != "" {
		return p.QuadletDir
	}
	home, _ := os.UserHomeDir()
	return quadletDirFor(isRoot(), home)
}

// isRoot reports whether the agent is running as root. When root, Quadlet
// units are installed to the system path and managed by the system systemctl
// manager; daemon-reload is done via SIGHUP to PID 1 to avoid the deadlock
// that "systemctl daemon-reload" hits when called from inside a service.
func isRoot() bool { return os.Getuid() == 0 }

// ApplyAction executes one planned action against the real runtime.
func (p *Applier) ApplyAction(a reconcile.PlannedAction) error {
	switch a.Action.Type {
	case schema.ActionServiceStart:
		return p.start(a)
	case schema.ActionServiceStop:
		return p.stop(a)
	case schema.ActionServiceRestart:
		return p.restart(a)
	case schema.ActionServiceReload:
		return p.reload(a)
	default:
		return fmt.Errorf("podman.Applier: unhandled action type %q", a.Action.Type)
	}
}

// RollbackAction undoes a previously applied action.
func (p *Applier) RollbackAction(a reconcile.PlannedAction) error {
	switch a.Action.Type {
	case schema.ActionServiceStart:
		return p.stopAndRemoveUnit(a.UnitFilename)
	case schema.ActionServiceStop:
		// A stopped service cannot be automatically restarted without the
		// original unit content. The next reconcile tick will correct it.
		return nil
	case schema.ActionServiceRestart:
		return nil // restart is idempotent; re-running would use same unit content
	case schema.ActionServiceReload:
		return nil // reload is idempotent; no undo needed
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// start
// ---------------------------------------------------------------------------

func (p *Applier) start(a reconcile.PlannedAction) error {
	if a.UnitFilename == "" {
		return fmt.Errorf("start %q: no unit filename in planned action", a.Action.Target)
	}
	if a.UnitContent == "" {
		return fmt.Errorf("start %q: no unit content in planned action", a.Action.Target)
	}

	// For source-built containers, build the image from the local Forgejo repo
	// before installing the Quadlet unit. Image-bundled containers (IsImageBundled
	// flag in the unit: no BuildSource comment) are pulled by Podman on start.
	if strings.HasSuffix(a.UnitFilename, ".container") {
		src, ref, dockerfile, buildCtx := parseBuildProvenance(a.UnitContent)
		if src != "" {
			if err := p.buildImage(a.Action.Target, src, ref, dockerfile, buildCtx); err != nil {
				return fmt.Errorf("build image for %s: %w", a.Action.Target, err)
			}
		}

		// M11 Tier-2 seam: secrets injection.
		// Secrets are stored at a conventional path outside git:
		//   <SecretsDir>/<service>.yaml.age
		// The service name is derived from the container name (ownbase-<service>).
		service := strings.TrimPrefix(a.Action.Target, "ownbase-")
		secretsDir := p.SecretsDir
		if secretsDir == "" {
			secretsDir = "/opt/ownbase/secrets"
		}
		secretsFile := filepath.Join(secretsDir, service+".yaml.age")
		if _, err := os.Stat(secretsFile); err == nil {
			// TODO(M11-Tier2): decrypt secretsFile and inject via Podman secrets.
			fmt.Fprintf(os.Stderr,
				"podman.Applier: WARNING: secrets injection not yet implemented for %s (M11 Tier-2)\n",
				a.Action.Target)
		}
	}

	dst := filepath.Join(p.quadletDir(), a.UnitFilename)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir quadlet dir: %w", err)
	}
	if err := os.WriteFile(dst, []byte(a.UnitContent), 0o644); err != nil {
		return fmt.Errorf("write unit %s: %w", dst, err)
	}

	// Mirror the unit file into RuntimeDir for drift tracking.
	if p.RuntimeDir != "" {
		rtDst := filepath.Join(p.RuntimeDir, a.UnitFilename)
		if err := os.MkdirAll(p.RuntimeDir, 0o755); err != nil {
			return fmt.Errorf("mkdir runtime dir: %w", err)
		}
		if err := os.WriteFile(rtDst, []byte(a.UnitContent), 0o644); err != nil {
			return fmt.Errorf("write runtime mirror %s: %w", rtDst, err)
		}
	}

	if err := reloadSystemd(); err != nil {
		return fmt.Errorf("daemon-reload after installing %s: %w", a.UnitFilename, err)
	}

	// For .network and .volume Quadlet units, explicitly restart the generated
	// systemd service so the resource (network/volume) is guaranteed to exist
	// before any container that depends on it tries to start.
	//
	// Quadlet's generator maps:
	//   foo.network → foo-network.service  (podman network create --ignore systemd-foo)
	//   foo.volume  → foo-volume.service   (podman volume  create --ignore systemd-foo)
	//
	// "restart" is used instead of "start" because these are Type=oneshot
	// RemainAfterExit=yes services: once they have run, systemd marks them
	// "active (exited)" and ignores subsequent "start" calls. If the network
	// or volume was pruned externally the service must be re-run to recreate
	// it. "restart" forces a stop→start cycle regardless of current state,
	// and is equivalent to "start" when the service has never run.
	if strings.HasSuffix(a.UnitFilename, ".network") || strings.HasSuffix(a.UnitFilename, ".volume") {
		svc := unitToService(a.UnitFilename)
		if err := systemctl("restart", svc); err != nil {
			return fmt.Errorf("restart %s: %w", svc, err)
		}
		return nil
	}
	if !strings.HasSuffix(a.UnitFilename, ".container") {
		return nil
	}

	svc := unitToService(a.UnitFilename)
	if err := systemctl("start", svc); err != nil {
		return fmt.Errorf("start %s: %w", svc, err)
	}

	return waitForContainer(a.Action.Target, a.UnitContent, 90*time.Second)
}

// ---------------------------------------------------------------------------
// stop
// ---------------------------------------------------------------------------

func (p *Applier) stop(a reconcile.PlannedAction) error {
	return p.stopAndRemoveUnit(a.UnitFilename)
}

func (p *Applier) stopAndRemoveUnit(unitFilename string) error {
	if unitFilename == "" {
		return nil
	}

	if strings.HasSuffix(unitFilename, ".container") {
		svc := unitToService(unitFilename)
		_ = systemctl("stop", svc) // best-effort; ignore "unit not found"
	}

	dst := filepath.Join(p.quadletDir(), unitFilename)
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit %s: %w", dst, err)
	}

	// Mirror the removal into RuntimeDir for drift tracking.
	if p.RuntimeDir != "" {
		rtDst := filepath.Join(p.RuntimeDir, unitFilename)
		_ = os.Remove(rtDst) // best-effort; file may not exist
	}

	return reloadSystemd()
}

// ---------------------------------------------------------------------------
// restart
// ---------------------------------------------------------------------------

// restart updates the unit file on disk with the new content, reloads systemd
// so it picks up the change, then issues a systemctl restart. This is the
// correct path for config-only changes (env vars, mounts, image tag) on an
// already-running container.
//
// If the unit has a BuildSource comment, the image is rebuilt before the
// restart so that a ref change is reflected in the new container.
func (p *Applier) restart(a reconcile.PlannedAction) error {
	if a.UnitFilename == "" {
		return fmt.Errorf("restart %q: no unit filename in planned action", a.Action.Target)
	}
	if a.UnitContent == "" {
		return fmt.Errorf("restart %q: no unit content in planned action", a.Action.Target)
	}

	// Rebuild the image if this is a source-built container. Unlike start,
	// restart is triggered by unit content changes (env vars, ref updates) so
	// we must rebuild to pick up any ref change before restarting.
	if strings.HasSuffix(a.UnitFilename, ".container") {
		src, ref, dockerfile, buildCtx := parseBuildProvenance(a.UnitContent)
		if src != "" {
			if err := p.buildImage(a.Action.Target, src, ref, dockerfile, buildCtx); err != nil {
				return fmt.Errorf("build image for %s: %w", a.Action.Target, err)
			}
		}
	}

	// Write the updated unit file.
	dst := filepath.Join(p.quadletDir(), a.UnitFilename)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir quadlet dir: %w", err)
	}
	if err := os.WriteFile(dst, []byte(a.UnitContent), 0o644); err != nil {
		return fmt.Errorf("write unit %s: %w", dst, err)
	}

	// Mirror the unit file into RuntimeDir for drift tracking.
	if p.RuntimeDir != "" {
		rtDst := filepath.Join(p.RuntimeDir, a.UnitFilename)
		if err := os.MkdirAll(p.RuntimeDir, 0o755); err != nil {
			return fmt.Errorf("mkdir runtime dir: %w", err)
		}
		if err := os.WriteFile(rtDst, []byte(a.UnitContent), 0o644); err != nil {
			return fmt.Errorf("write runtime mirror %s: %w", rtDst, err)
		}
	}

	if err := reloadSystemd(); err != nil {
		return fmt.Errorf("daemon-reload before restart of %s: %w", a.UnitFilename, err)
	}

	svc := unitToService(a.UnitFilename)
	if err := systemctl("restart", svc); err != nil {
		return fmt.Errorf("restart %s: %w", svc, err)
	}

	return waitForContainer(a.Action.Target, a.UnitContent, 90*time.Second)
}

// ---------------------------------------------------------------------------
// reload (Caddy)
// ---------------------------------------------------------------------------

func (p *Applier) reload(a reconcile.PlannedAction) error {
	if a.Action.Target != "caddy" {
		return fmt.Errorf("reload: unknown target %q", a.Action.Target)
	}

	caddyfilePath := "/opt/ownbase/runtime/Caddyfile"
	if p.RuntimeDir != "" {
		caddyfilePath = filepath.Join(p.RuntimeDir, "Caddyfile")
	}
	if a.CaddyfileContent != "" {
		if err := os.MkdirAll(filepath.Dir(caddyfilePath), 0o755); err != nil {
			return fmt.Errorf("mkdir for Caddyfile: %w", err)
		}
		if err := os.WriteFile(caddyfilePath, []byte(a.CaddyfileContent), 0o644); err != nil {
			return fmt.Errorf("write Caddyfile: %w", err)
		}
	}

	// Copy the Caddyfile into the running core Caddy container, then reload.
	// Caddy runs in a container (core.CaddyContainerName), not on the host.
	const containerCaddyfile = "/etc/caddy/Caddyfile"
	if out, err := exec.Command(
		"podman", "cp", caddyfilePath,
		core.CaddyContainerName+":"+containerCaddyfile,
	).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "caddy reload (non-fatal): cp Caddyfile: %v — %s\n", err, out)
		return nil
	}
	out, err := exec.Command(
		"podman", "exec", core.CaddyContainerName,
		"caddy", "reload", "--config", containerCaddyfile, "--adapter", "caddyfile",
	).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "caddy reload (non-fatal): %v — %s\n", err, out)
	}
	return nil
}

// ---------------------------------------------------------------------------
// buildImage: clone → checkout → podman build
// ---------------------------------------------------------------------------

// buildImage builds localhost/<containerName>:local from the source repo at
// the given ref. It clones the repo from the local Forgejo instance, checks
// out the pinned ref, and runs `podman build`.
//
// containerName is e.g. "ownbase-auth"; imageName is "localhost/ownbase-auth:local".
// source is the Forgejo repo path (e.g. "services/auth").
// ref is the branch, tag, or commit SHA; empty means the default branch.
// dockerfile is relative to the repo root; empty means "Dockerfile".
// buildCtx is a subdirectory to use as the build context; empty means root.
func (p *Applier) buildImage(containerName, source, ref, dockerfile, buildCtx string) error {
	imageName := "localhost/" + containerName + ":local"

	owner, repo := splitSourcePath(source, p.forgejoUser())
	cloneURL := buildCloneURL(p.ForgejoURL, owner, repo)

	// Clone into a temp directory.
	tmpDir, err := os.MkdirTemp("", "ownbase-build-*")
	if err != nil {
		return fmt.Errorf("mktemp for build: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := gitCloneAt(cloneURL, ref, p.ForgejoToken, tmpDir); err != nil {
		return fmt.Errorf("git clone %s@%s: %w", source, ref, err)
	}

	// Determine build context directory.
	buildDir := tmpDir
	if buildCtx != "" {
		buildDir = filepath.Join(tmpDir, buildCtx)
	}

	// Build the image.
	// --dns=1.1.1.1 ensures name resolution works in the build sandbox even
	// on hosts where the default container DNS (often the gateway) doesn't
	// forward queries. Belt-and-suspenders with /etc/containers/containers.conf.
	args := []string{"build", "--tag", imageName, "--dns=1.1.1.1"}
	if dockerfile != "" {
		// dockerfile is relative to repo root, not to buildCtx.
		args = append(args, "--file", filepath.Join(tmpDir, dockerfile))
	}
	args = append(args, buildDir)

	out, err := exec.Command("podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman build %s from %s@%s: %w\n%s",
			imageName, source, ref, err, out)
	}
	fmt.Fprintf(os.Stderr, "podman.Applier: built %s from %s@%s\n", imageName, source, ref)
	return nil
}

func (p *Applier) forgejoUser() string {
	if p.ForgejoUser != "" {
		return p.ForgejoUser
	}
	return "ownbase"
}

// gitCloneAt performs a shallow clone of cloneURL into destDir. When ref is a
// tag or branch name it uses --branch for a depth-1 clone. When ref looks like
// a commit SHA it clones without --branch and then checks out the SHA.
//
// token is passed via GIT_CONFIG_COUNT/KEY/VALUE env vars so that it never
// appears in the process argv or in git's own error/log output (M14).
func gitCloneAt(cloneURL, ref, token, destDir string) error {
	var cloneArgs []string
	isCommit := len(ref) == 40 && isHexStr(ref)

	if ref != "" && !isCommit {
		// Tag or branch: shallow clone at the named ref.
		cloneArgs = []string{"clone", "--depth=1", "--branch", ref, cloneURL, destDir}
	} else {
		// Empty ref (default branch) or commit SHA: regular shallow clone.
		cloneArgs = []string{"clone", "--depth=1", cloneURL, destDir}
	}

	cmd := exec.Command("git", cloneArgs...)
	// Pass the token via environment rather than URL so it never appears in argv.
	// GIT_CONFIG_COUNT/KEY/VALUE was introduced in git 2.31 (Ubuntu 22.04: 2.34).
	if token != "" {
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: token "+token,
		)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone: %w\n%s", err, scrubToken(string(out), token))
	}

	if isCommit {
		// Commit SHAs require a fetch + checkout after the initial clone.
		fetchCmd := exec.Command("git", "-C", destDir, "fetch", "--depth=1", "origin", ref)
		if token != "" {
			fetchCmd.Env = append(os.Environ(),
				"GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=http.extraHeader",
				"GIT_CONFIG_VALUE_0=Authorization: token "+token,
			)
		}
		fetchOut, err := fetchCmd.CombinedOutput()
		if err != nil {
			// Non-fatal: the commit may already be present in the shallow clone.
			_ = scrubToken(string(fetchOut), token)
		}
		coOut, err := exec.Command("git", "-C", destDir, "checkout", ref).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git checkout %s: %w\n%s", ref, err, coOut)
		}
	}
	return nil
}

// splitSourcePath splits a Forgejo repo path (e.g. "services/auth") into
// owner and repo. If the path has no slash, defaultOwner is used.
// Mirrors the parseSourcePath function in internal/update/detect.go; kept
// here to avoid a cross-package dependency.
func splitSourcePath(sourcePath, defaultOwner string) (owner, repo string) {
	parts := strings.SplitN(sourcePath, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return defaultOwner, sourcePath
}

func isHexStr(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// systemctl runs a systemctl command against the manager that owns the units:
// the system manager when root, the per-user manager (--user) otherwise.
func systemctl(args ...string) error {
	full := systemctlArgs(isRoot(), args...)
	cmd := exec.Command("systemctl", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w\n%s", strings.Join(full, " "), err, out)
	}
	return nil
}

// reloadSystemd reloads the systemd manager so it picks up newly written
// Quadlet units. When running as root the agent is itself a systemd service,
// and "systemctl daemon-reload" deadlocks when issued from within the calling
// service's own transaction; SIGHUP to PID 1 is the documented non-D-Bus
// equivalent and is safe from any context. The non-root path uses the user
// manager, which has no such restriction.
func reloadSystemd() error {
	if isRoot() {
		if err := syscall.Kill(1, syscall.SIGHUP); err != nil {
			return fmt.Errorf("daemon-reload (SIGHUP to PID 1): %w", err)
		}
		// Give systemd a moment to process the SIGHUP before unit start.
		time.Sleep(1 * time.Second)
		return nil
	}
	return systemctl("daemon-reload")
}

// unitToService maps a Quadlet unit filename to the systemd service name that
// Quadlet's generator emits. The conventions (derived from the podman-system-generator
// source and empirically verified on Podman 4.9.x) are:
//
//	foo.container → foo.service
//	foo.network   → foo-network.service
//	foo.volume    → foo-volume.service
func unitToService(unitFilename string) string {
	switch {
	case strings.HasSuffix(unitFilename, ".network"):
		return strings.TrimSuffix(unitFilename, ".network") + "-network.service"
	case strings.HasSuffix(unitFilename, ".volume"):
		return strings.TrimSuffix(unitFilename, ".volume") + "-volume.service"
	default:
		return strings.TrimSuffix(unitFilename, ".container") + ".service"
	}
}

// waitForContainer polls until the named container is running, then (if the
// unit content declares a HealthProbeHTTP path and a PublishPort) polls the
// health endpoint until it returns 2xx. The two-phase approach catches
// crash-loops that pass the "running" check during the brief start window.
func waitForContainer(name, unitContent string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	// Phase 1: wait for status=running.
	for time.Now().Before(deadline) {
		out, _ := exec.Command("podman", "ps",
			"--filter", "name=^"+name+"$",
			"--filter", "status=running",
			"--format", "{{.Names}}").Output()
		if strings.TrimSpace(string(out)) != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		return fmt.Errorf("container %q did not become running within %s", name, timeout)
	}

	// Phase 2: HTTP health probe (optional — only when unit declares both directives).
	healthPath := parseQuadletComment("HealthProbeHTTP", unitContent)
	if healthPath == "" {
		return nil
	}
	port := parsePublishPort(unitContent)
	if port <= 0 {
		return nil
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath)
	client := &http.Client{Timeout: 3 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url) //nolint:noctx
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("container %q health probe %s did not return 2xx within %s", name, url, timeout)
}
