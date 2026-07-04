// Package core manages the OwnBase core packages: Forgejo (git host) and Caddy
// (reverse proxy). These are distinct from user services declared in
// ownbase.yaml — they are installed by the OwnBase installer and updated by
// ownbasectl upgrade, not by the user update loop.
//
// Separation of concerns:
//
//   - User services (source: / mirror: in ownbase.yaml) are compiled and
//     reconciled by the agent's normal path.
//   - Core packages are always present; the installer brings them up from
//     pinned image digests; the core package manifest is embedded in the binary.
//
// The core: block in ownbase.yaml configures core packages (domains, ports,
// TLS email) but never controls which version runs — that is OwnBase's job.
package core

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ownbase/ownbase/internal/compiler"
	"github.com/ownbase/ownbase/internal/schema"
)

// CoreManifest holds the pinned image references for OwnBase core packages.
// These digests are updated by the OwnBase team via ownbasectl upgrade.
// The bootstrap exception: these are the only images ever pulled from a
// registry — user services must always be built from Forgejo-hosted repos.
type CoreManifest struct {
	ForgejoImage  string // e.g. "codeberg.org/forgejo/forgejo:15.0.3"
	ForgejoDigest string // e.g. "sha256:..."
	CaddyImage    string // e.g. "docker.io/library/caddy:2.11.4-alpine"
	CaddyDigest   string // e.g. "sha256:..."
}

// Current is the pinned core manifest embedded in this binary.
// Updated with each OwnBase release via ownbasectl upgrade.
//
// Bumped 2026-07-04 to the latest stable releases at the time (Forgejo
// 15.0.3, Caddy 2.11.4-alpine). Both digests were verified with
// `skopeo inspect` and smoke-tested on the Tier-2 VM (admin CLI, token
// generation, /api/healthz, and the migrate/pull-mirror API) before pinning.
var Current = CoreManifest{
	ForgejoImage:  "codeberg.org/forgejo/forgejo:15.0.3",
	ForgejoDigest: "sha256:55bb42bec9abef5223744804f164e37d37b20df7e8b8b4807ba213ad4f071d6d",
	CaddyImage:    "docker.io/library/caddy:2.11.4-alpine",
	CaddyDigest:   "sha256:5f5c8640aae01df9654968d946d8f1a56c497f1dd5c5cda4cf95ab7c14d58648",
}

// ForgejoContainerName is the well-known name for the core Forgejo container.
const ForgejoContainerName = "ownbase-core-forgejo"

// CaddyContainerName is the well-known name for the core Caddy container.
const CaddyContainerName = "ownbase-core-caddy"

// ForgejoDataVolume is the named volume for Forgejo's persistent data.
const ForgejoDataVolume = "ownbase-core-forgejo-data"

// CaddyDataVolume is the named volume for Caddy's persistent data (certs).
const CaddyDataVolume = "ownbase-core-caddy-data"

// DefaultForgejoPort is the default host port Forgejo listens on.
const DefaultForgejoPort = 3000

// TokenPath is the well-known path where the Forgejo admin token is stored.
// Written by the installer, read by the agent on startup.
const TokenPath = "/opt/ownbase/forgejo-token"

// AdminUser is the Forgejo admin username created by the installer.
const AdminUser = "ownbase"

// AdminEmail is the Forgejo admin email used during bootstrap.
const AdminEmail = "agent@ownbase.local"

// BuildForgejoModel returns the ContainerModel for the Forgejo core package.
// The model uses IsImageBundled=true so the renderer gives it the correct
// timeout and omits build provenance comments.
func BuildForgejoModel(cfg schema.CoreConfig, m CoreManifest) compiler.ContainerModel {
	port := cfg.Forgejo.EffectivePort()
	env := forgejoEnv(port, cfg.Forgejo.Domain)

	c := compiler.ContainerModel{
		Name:           ForgejoContainerName,
		Image:          m.ForgejoImage,
		Digest:         m.ForgejoDigest,
		IsImageBundled: true,
		PublicPort:     port,
		PublicDomain:   cfg.Forgejo.Domain,
		Env:            env,
		HealthProbe:    &compiler.HealthProbeModel{HTTPPath: "/api/healthz"},
		VolumeMounts: []compiler.VolumeMount{
			{VolumeName: ForgejoDataVolume, MountPath: "/data"},
		},
		Networks: []string{},
		// Forgejo uses s6-overlay as its process supervisor. s6-svscan starts
		// as root, initialises its service directory, then uses s6-applyuidgid
		// (which calls setresuid/setresgid) to drop to the git user (UID 1000).
		// Four capabilities are needed after DropCapability=ALL:
		//   SETUID + SETGID  — s6-applyuidgid calls setresuid/setresgid
		//   CHOWN            — init scripts chown /data dirs to the git user
		//   DAC_OVERRIDE     — init runs environment-to-ini as root after git
		//                      first wrote app.ini (git:git 644); without this
		//                      the second write fails, INSTALL_LOCK stays false,
		//                      and CLI admin commands refuse to run
		// Forgejo's SSH daemon is disabled (START_SSH_SERVER=false). The host
		// sshd owns port 22; AuthorizedKeysCommand shims (forgejo-keys/serv,
		// written by ensureGitSSH) proxy git-over-SSH into this container.
		AddCapabilities: []string{"SETUID", "SETGID", "CHOWN", "DAC_OVERRIDE"},
	}
	return c
}

// OwnbaseInternalNetwork is the name of the shared internal network used by
// Caddy and other core services that need to communicate without host routing.
const OwnbaseInternalNetwork = "ownbase-internal"

// BuildCoreOutput builds the full RuntimeOutput for all core packages.
// This is a separate compile step from the user services; the outputs are
// merged by the agent before applying.
func BuildCoreOutput(cfg schema.CoreConfig, m CoreManifest) compiler.RuntimeOutput {
	forgejo := BuildForgejoModel(cfg, m)
	caddy := buildCaddyModel(cfg, m)

	out := compiler.RuntimeOutput{
		QuadletUnits: make(map[string]string),
	}

	// Shared internal network unit — required by Caddy so that Quadlet's
	// auto-generated ownbase-internal-network.service dependency is satisfied.
	out.QuadletUnits[OwnbaseInternalNetwork+".network"] = renderCoreNetwork(OwnbaseInternalNetwork)

	// Forgejo unit.
	out.QuadletUnits[ForgejoContainerName+".container"] = renderCoreContainer(forgejo)
	out.QuadletUnits[ForgejoDataVolume+".volume"] = renderCoreVolume(ForgejoDataVolume)

	// Caddy unit.
	out.QuadletUnits[CaddyContainerName+".container"] = renderCoreContainer(caddy)
	out.QuadletUnits[CaddyDataVolume+".volume"] = renderCoreVolume(CaddyDataVolume)

	return out
}

// ForgejoURL returns the local base URL for the Forgejo admin API,
// derived from the core config.
func ForgejoURL(cfg schema.CoreConfig) string {
	return fmt.Sprintf("http://localhost:%d", cfg.Forgejo.EffectivePort())
}

func buildCaddyModel(cfg schema.CoreConfig, m CoreManifest) compiler.ContainerModel {
	env := []string{}
	if cfg.Caddy.Email != "" {
		env = append(env, "ACME_EMAIL="+cfg.Caddy.Email)
	}
	return compiler.ContainerModel{
		Name:           CaddyContainerName,
		Image:          m.CaddyImage,
		Digest:         m.CaddyDigest,
		IsImageBundled: true,
		Env:            env,
		VolumeMounts: []compiler.VolumeMount{
			{VolumeName: CaddyDataVolume, MountPath: "/data"},
		},
		Networks: []string{"ownbase-internal"},
		// Caddy is the public web entrypoint: it must accept external traffic
		// on 80 (ACME HTTP-01 + redirects) and 443 (HTTPS) on all interfaces.
		HostPublishPorts: []int{80, 443},
		// Binding ports <1024 requires CAP_NET_BIND_SERVICE even for root, and
		// every container drops ALL capabilities by default. Caddy runs as the
		// image default user (root in caddy:2-alpine) and needs exactly this one
		// capability back; no User override (forcing a non-root UID would break
		// privileged-port binding and writes to the cert store under /data).
		AddCapabilities: []string{"NET_BIND_SERVICE"},
	}
}

func forgejoEnv(port int, domain string) []string {
	return ForgejoEnvForContainer(port, domain)
}

// ForgejoEnvForContainer returns the environment variables that configure the
// Forgejo container. Used by both the Quadlet renderer (core packages) and
// the legacy podman-run path (ensureForgejoRunning in bootstrap_core.go).
func ForgejoEnvForContainer(port int, domain string) []string {
	rootURL := fmt.Sprintf("http://localhost:%d", port)
	if domain != "" {
		rootURL = "https://" + domain
	}
	return []string{
		"FORGEJO__security__INSTALL_LOCK=true",
		fmt.Sprintf("FORGEJO__server__HTTP_PORT=%d", port),
		"FORGEJO__server__ROOT_URL=" + rootURL,
		"FORGEJO__database__DB_TYPE=sqlite3",
		"FORGEJO__database__PATH=/data/gitea/forgejo.db",
		"FORGEJO__log__LEVEL=warn",
		// OwnBase is a single-owner system. Disable public registration so
		// a visitor cannot create an account on the customer's git server.
		"FORGEJO__service__DISABLE_REGISTRATION=true",
		// Require sign-in to browse repos; no anonymous read access.
		"FORGEJO__service__REQUIRE_SIGNIN_VIEW=true",
		// Allow webhooks to private/loopback addresses (e.g. host.containers.internal)
		// so the agent can receive push notifications from Forgejo containers.
		"FORGEJO__webhook__ALLOWED_HOST_LIST=*",
		// Prevent Forgejo from starting its own SSH daemon. The host sshd owns
		// port 22; host-side shims (forgejo-keys / forgejo-serv, written by
		// ensureGitSSH) handle git-over-SSH via AuthorizedKeysCommand instead.
		// We set START_SSH_SERVER=false (not DISABLE_SSH=true): DISABLE_SSH also
		// kills the `forgejo serv` and `forgejo keys` CLI commands used by the
		// shims, breaking git SSH access entirely.
		// DISABLE_SSH=false is set explicitly so environment-to-ini overwrites any
		// DISABLE_SSH=true previously written to app.ini (environment-to-ini
		// merges into app.ini but does not remove keys absent from env).
		"FORGEJO__server__DISABLE_SSH=false",
		"FORGEJO__server__START_SSH_SERVER=false",
		// Tell Forgejo to advertise port 22 in clone URLs; the host sshd handles it.
		"FORGEJO__server__SSH_PORT=22",
	}
}

const coreGeneratedHeader = "# Generated by OwnBase core — do not hand-edit.\n# Core packages are managed by OwnBase, not by ownbase.yaml.\n"

func renderCoreContainer(c compiler.ContainerModel) string {
	var b strings.Builder
	b.WriteString(coreGeneratedHeader)

	b.WriteString("\n[Unit]\n")
	fmt.Fprintf(&b, "Description=OwnBase core: %s\n", c.Name)
	b.WriteString("After=network-online.target\n")

	b.WriteString("\n[Container]\n")
	if c.Digest != "" {
		fmt.Fprintf(&b, "Image=%s@%s\n", c.Image, c.Digest)
	} else {
		fmt.Fprintf(&b, "Image=%s\n", c.Image)
	}
	fmt.Fprintf(&b, "ContainerName=%s\n", c.Name)

	for _, net := range c.Networks {
		fmt.Fprintf(&b, "Network=%s.network\n", net)
	}
	for _, vm := range c.VolumeMounts {
		fmt.Fprintf(&b, "Volume=%s:%s\n", vm.VolumeName, vm.MountPath)
	}
	if c.PublicPort > 0 {
		// Bind to all interfaces when no domain is configured (direct access
		// without Caddy, dev/no-TLS setup). With a domain, Caddy proxies from
		// localhost so 127.0.0.1 is sufficient and avoids external exposure.
		if c.PublicDomain == "" {
			fmt.Fprintf(&b, "PublishPort=%d:%d\n", c.PublicPort, c.PublicPort)
		} else {
			fmt.Fprintf(&b, "PublishPort=127.0.0.1:%d:%d\n", c.PublicPort, c.PublicPort)
		}
	}
	// Public web entrypoint ports (Caddy 80/443): published on all interfaces
	// so external clients can reach the reverse proxy. UFW already allows 80
	// and 443 inbound; everything else stays loopback-only.
	for _, hp := range c.HostPublishPorts {
		fmt.Fprintf(&b, "PublishPort=%d:%d\n", hp, hp)
	}
	for _, kv := range c.Env {
		fmt.Fprintf(&b, "Environment=%s\n", kv)
	}
	if c.HealthProbe != nil && c.HealthProbe.HTTPPath != "" {
		fmt.Fprintf(&b, "# HealthProbeHTTP=%s\n", c.HealthProbe.HTTPPath)
	}

	// Security hardening.
	b.WriteString("NoNewPrivileges=true\n")
	b.WriteString("DropCapability=ALL\n")
	if c.User != "" {
		fmt.Fprintf(&b, "User=%s\n", c.User)
	}
	for _, cap := range c.AddCapabilities {
		fmt.Fprintf(&b, "AddCapability=%s\n", cap)
	}

	b.WriteString("\n[Service]\n")
	b.WriteString("Restart=always\n")
	b.WriteString("TimeoutStartSec=120\n") // core packages pull large public images

	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=default.target\n")

	return b.String()
}

func renderCoreNetwork(name string) string {
	var b strings.Builder
	b.WriteString(coreGeneratedHeader)
	b.WriteString("\n[Network]\n")
	fmt.Fprintf(&b, "# OwnBase core internal network: %s\n", name)
	return b.String()
}

func renderCoreVolume(name string) string {
	var b strings.Builder
	b.WriteString(coreGeneratedHeader)
	b.WriteString("\n[Volume]\n")
	fmt.Fprintf(&b, "# OwnBase core data volume: %s\n", name)
	return b.String()
}

// SortedUnitNames returns the core unit filenames in deterministic order.
func SortedUnitNames(out compiler.RuntimeOutput) []string {
	names := make([]string, 0, len(out.QuadletUnits))
	for k := range out.QuadletUnits {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
