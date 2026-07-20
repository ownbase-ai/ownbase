// Package schema contains the typed contracts that every other package in the
// OwnBase spine speaks. It owns OwnbaseConfig (ownbase.yaml), the action
// taxonomy, and health-probe union. Nothing here touches the network, the
// filesystem, or any runtime. Pure data + validation.
package schema

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// CurrentSchemaVersion is the only version this binary understands.
const CurrentSchemaVersion = "v1"

// RepoMode is a deprecated field kept only for parsing backward compatibility.
// It has no behavioral effect. Users should remove it from ownbase.yaml.
type RepoMode string

// OwnbaseConfig is the parsed, validated form of ownbase.yaml.
//
// Every user service is built from an external git repo (repo:) that OwnBase
// clones into a local bare repo under /opt/ownbase/repos/ and builds from the
// pinned ref:. Registry images are never a valid service source; the core
// Caddy package is managed by the installer and configured via the core:
// block.
type OwnbaseConfig struct {
	SchemaVersion string                 `yaml:"schema_version"`
	Core          CoreConfig             `yaml:"core,omitempty"`
	Services      map[string]ServiceDecl `yaml:"services,omitempty"`
}

// CoreConfig holds configuration (not versions) for OwnBase core packages.
// Caddy (the reverse proxy) is managed by the OwnBase installer and
// updater — not by ownbase.yaml. This block only configures it: set
// domains, ports, TLS email. It does not control what version runs.
type CoreConfig struct {
	Caddy  CaddyCoreConfig  `yaml:"caddy,omitempty"`
	Backup BackupCoreConfig `yaml:"backup,omitempty"`
}

// BackupCoreConfig configures the restic backup engine.
// Credentials (restic password, cloud keys) are not stored here; they live
// in the age-encrypted secret at /opt/ownbase/secrets/backup.yaml.age,
// managed via ownbasectl secrets set backup.
type BackupCoreConfig struct {
	// Repo is the restic repository URL. When empty, backups are disabled.
	// Examples:
	//   s3:s3.amazonaws.com/my-bucket/ownbase
	//   b2:bucket-name:ownbase
	//   sftp:user@host:/path/to/repo
	//   /opt/ownbase/backup  (local, dev/test)
	Repo string `yaml:"repo,omitempty"`

	// Interval is how often a backup snapshot is taken (e.g. "1h", "30m").
	// Defaults to 1h when empty.
	Interval string `yaml:"interval,omitempty"`

	// VerifyInterval is how often the verified-restore drill runs (e.g. "24h").
	// Defaults to 24h when empty.
	VerifyInterval string `yaml:"verify_interval,omitempty"`
}

// DefaultBackupInterval is the snapshot cadence used when Interval is empty.
const DefaultBackupInterval = time.Hour

// DefaultVerifyInterval is the verified-restore cadence used when VerifyInterval is empty.
const DefaultVerifyInterval = 24 * time.Hour

// Enabled returns true when a backup repository is configured.
func (b BackupCoreConfig) Enabled() bool {
	return strings.TrimSpace(b.Repo) != ""
}

// EffectiveInterval returns the parsed Interval, or DefaultBackupInterval.
// Returns an error only when Interval is set but unparseable.
func (b BackupCoreConfig) EffectiveInterval() (time.Duration, error) {
	if b.Interval == "" {
		return DefaultBackupInterval, nil
	}
	d, err := time.ParseDuration(b.Interval)
	if err != nil {
		return 0, fmt.Errorf("core.backup.interval %q: %w", b.Interval, err)
	}
	return d, nil
}

// EffectiveVerifyInterval returns the parsed VerifyInterval, or DefaultVerifyInterval.
// Returns an error only when VerifyInterval is set but unparseable.
func (b BackupCoreConfig) EffectiveVerifyInterval() (time.Duration, error) {
	if b.VerifyInterval == "" {
		return DefaultVerifyInterval, nil
	}
	d, err := time.ParseDuration(b.VerifyInterval)
	if err != nil {
		return 0, fmt.Errorf("core.backup.verify_interval %q: %w", b.VerifyInterval, err)
	}
	return d, nil
}

// CaddyCoreConfig configures the built-in reverse proxy.
type CaddyCoreConfig struct {
	// Email is used for ACME/Let's Encrypt certificate issuance.
	// Required when using public domains with automatic TLS.
	Email string `yaml:"email,omitempty"`
}

// ServiceDecl is one service instance entry in the services map.
// The map key is the instance name (e.g. "crm", "crm-staging", "worker").
//
// Repo is an external git URL that OwnBase clones into a local bare repo
// under /opt/ownbase/repos/ and builds from at the pinned Ref. Registry
// images (image:) are never a valid user service source. The core Caddy
// package is installed by the OwnBase installer and configured via the
// top-level core: block.
type ServiceDecl struct {
	// Repo is the external git URL to clone and build from, e.g.
	// "git@github.com:org/app.git" or
	// "https://github.com/docker-library/postgres". OwnBase maintains a
	// read-only local bare clone automatically at
	// /opt/ownbase/repos/<service-name>.
	Repo string `yaml:"repo,omitempty"`

	// Mode is deprecated and has no behavioral effect. It is kept here so that
	// existing ownbase.yaml files that declare mode: managed or mode: pinned
	// continue to parse without error. Remove it from your config.
	Mode RepoMode `yaml:"mode,omitempty"`

	// Ref is the branch, tag, or commit SHA to build from. It is set
	// explicitly by `ownbasectl deploy`, which resolves the requested ref to
	// a concrete commit SHA and commits it here. When empty, the build falls
	// back to the repo's default-branch HEAD (no automatic pinning).
	Ref string `yaml:"ref,omitempty"`

	// Dockerfile is the path to the Dockerfile within the repo, relative to
	// Context. Defaults to "Dockerfile". Use "Containerfile" if the repo
	// follows the Podman convention.
	Dockerfile string `yaml:"dockerfile,omitempty"`

	// Context is a subdirectory within the repo to use as the build context.
	// Useful for monorepos or versioned directories like docker-library/postgres
	// where each version lives under e.g. "17/alpine".
	// Empty means the repo root.
	Context string `yaml:"context,omitempty"`

	// Port is the primary port the container listens on. Used to generate the
	// Caddy reverse-proxy route when Domain is set.
	Port int `yaml:"port,omitempty"`

	// Domain is the public hostname for this service's primary endpoint.
	// If empty, the service has no Caddy route (internal-only).
	//
	// This is the older single-hostname form, kept working indefinitely (it
	// is folded into EffectiveDomains() as an extra entry) — new configs
	// should prefer domains: even for a single hostname, but there is no
	// need to migrate existing configs away from domain:.
	Domain string `yaml:"domain,omitempty"`

	// Domains lists every public hostname this service should be reachable
	// at. The compiler emits one Caddy route per effective domain (see
	// EffectiveDomains), all pointing at the same container/port — useful
	// for serving the same service under multiple names (e.g. a .com and a
	// .org). If empty and Domain is also empty, the service has no Caddy
	// route (internal-only).
	Domains []string `yaml:"domains,omitempty"`

	// Internal marks this service as tunnel-only: it has a domain (used as
	// the local hostname by `ownbasectl tunnel`) but no Caddy route is
	// emitted, so it is never reachable from the internet. Use this for
	// private admin UIs, dashboards, or any service that should only be
	// accessible over an authenticated SSH tunnel.
	//
	// An internal service must still declare domain: (or domains:) and
	// port: so the tunnel command can derive its local hostname and connect
	// to it. Without a domain, `ownbasectl tunnel` would have nothing to
	// route to.
	Internal bool `yaml:"internal,omitempty"`

	// Requires lists capabilities (service keys) this service depends on.
	// Each name must match a key in the services map — the compiler joins
	// this container to that provider's network.
	Requires []string `yaml:"requires,omitempty"`

	// Database is the name of the Postgres database to provision. The agent
	// creates the database and injects credentials as environment variables.
	Database string `yaml:"database,omitempty"`

	// HealthProbe configures how the agent verifies the service is up before
	// marking a reconcile step as complete.
	HealthProbe *HealthProbeDecl `yaml:"health_probe,omitempty"`

	// DataPath is the mount path for the service's persistent data volume
	// inside the container. Defaults to "/data".
	// The volume itself is always named "ownbase-<name>-data".
	// Ignored when Volumes is set.
	DataPath string `yaml:"data_path,omitempty"`

	// Volumes declares the named volumes for this service.
	// When set, DataPath is ignored by both the compiler and the backup engine.
	// When empty, a single volume "ownbase-<name>-data" is created, mounted at
	// DataPath (default "/data"), and automatically included in backups —
	// preserving the behaviour of all existing configs.
	Volumes []VolumeDecl `yaml:"volumes,omitempty"`

	// Env is a list of static environment variables to inject, in KEY=VALUE
	// format. Values appear in plaintext in ownbase.yaml; use
	// ownbasectl secrets set for sensitive values.
	Env []string `yaml:"env,omitempty"`

	// User is the UID or username to run the container process as (e.g. "1000"
	// or "appuser"). Empty means the image default. Prefer a non-root UID where
	// the image allows — OwnBase always emits DropCapability=ALL regardless.
	User string `yaml:"user,omitempty"`

	// AddCapabilities lists Linux capabilities to add back after DropCapability=ALL.
	// Only use when the service genuinely requires them (e.g. ["NET_BIND_SERVICE"]
	// for a service binding port 80/443). Leave empty for normal services.
	AddCapabilities []string `yaml:"add_capabilities,omitempty"`

	// SecurityOpt passes --security-opt flags to Podman for this container.
	// Use sparingly — each entry widens the security boundary.
	// Example: ["apparmor=unconfined"] for services (like postgres) that fork
	// child processes and require inter-process signaling, which the default
	// containers-default AppArmor profile blocks when no-new-privileges is set.
	SecurityOpt []string `yaml:"security_opt,omitempty"`
}

// VolumeDecl declares one named Podman volume for a service.
// The Podman volume name is "ownbase-<service>-<name>".
type VolumeDecl struct {
	// Name is the short name for this volume (e.g. "config", "media", "cache").
	// The Podman volume is named "ownbase-<service>-<name>".
	Name string `yaml:"name"`

	// Mount is where the volume is mounted inside the container (e.g. "/config").
	Mount string `yaml:"mount"`

	// Backup is a list of paths within this volume to include in restic snapshots,
	// relative to Mount. Use "." to back up the entire volume.
	// Examples: ["."], ["./config", "./data/db"], ["./music", "./photos"]
	// Omit (or leave empty) to exclude this volume from backups entirely.
	Backup []string `yaml:"backup,omitempty"`
}

// HealthProbeDecl describes how the agent verifies a service is healthy.
// Only HTTP probes are supported in V1.
type HealthProbeDecl struct {
	// HTTP is the path to GET on localhost:Port. The probe succeeds when the
	// server returns a 2xx status within the timeout. Example: "/-/health"
	HTTP string `yaml:"http,omitempty"`
}

// EffectiveDomains returns the deduplicated, order-preserving union of the
// older singular Domain field and Domains — every public hostname this
// service should be reachable at. Domain (if set) comes first, followed by
// each entry in Domains not already seen. An empty result means the service
// has no Caddy route (internal-only) and is never bridged by
// `ownbasectl tunnel` either (no domain means no .localhost URL).
func (s ServiceDecl) EffectiveDomains() []string {
	seen := make(map[string]bool, len(s.Domains)+1)
	var out []string
	add := func(d string) {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	add(s.Domain)
	for _, d := range s.Domains {
		add(d)
	}
	return out
}

// HasPublicDomain reports whether at least one service has one or more
// domains configured (via domain: and/or domains:) AND a port to route
// them to. A domain with no port is not routable — the compiler only
// emits a Caddy route when both are present (see
// internal/compiler.buildContainer) — so it must not count here either,
// or the firewall/Caddy would open 80/443 to the world for a service that
// has no listener to actually reach. A Base with no such service exposes
// nothing publicly but SSH — Caddy publishes no ports and the firewall
// opens no web ports; reach such services with `ownbasectl tunnel` instead.
func (c *OwnbaseConfig) HasPublicDomain() bool {
	for _, svc := range c.Services {
		if svc.Internal {
			continue
		}
		if svc.Port != 0 && len(svc.EffectiveDomains()) > 0 {
			return true
		}
	}
	return false
}

// TunnelBasePort is the first loopback port the compiler allocates to
// any port'd service's direct-to-container publish. Each eligible service
// gets one port, assigned by sorted name starting here — deliberately
// decoupled from any service's own container Port so that a service can
// declare port: 80/443 (or share a port number with another service)
// without colliding with Caddy's machine-wide bind or with each other on
// the loopback publish. Despite the name, this isn't exclusively for
// `ownbasectl tunnel`: the daemon's own HTTP health_probe (internal/podman's
// waitForContainer) also dials a service's container directly over this
// same loopback publish, including for domain-less internal services the
// tunnel never bridges — see TunnelPorts.
const TunnelBasePort = 41000

// TunnelPorts returns the deterministic loopback port assigned to each
// port'd service, keyed by service name. Eligibility here is intentionally
// broader than HasPublicDomain/what `ownbasectl tunnel` bridges: ANY service
// with a Port set gets an entry, domain or not, because two independent
// things depend on this loopback publish existing —
//  1. `ownbasectl tunnel`'s SSH bridge (domain'd services only — see
//     internal/bridge.Discover, which filters this map down).
//  2. The daemon's own startup HTTP health_probe (internal/podman's
//     waitForContainer), which needs a loopback port to dial for ANY
//     port'd service, including purely-internal ones with no domain.
//
// Narrowing this to domain'd-only (as a prior version of this function did)
// silently broke health_probe for domain-less services, since the compiler
// then emitted no PublishPort line for them to dial at all.
//
// Ports are recomputed fresh from the current config on every call — never
// persisted — which is safe because the compiler (building the Quadlet
// unit) and `ownbasectl tunnel` (parsing ownbase.yaml independently, with no
// daemon call) both compute this from the same ownbase.yaml at the moment
// they need it, so they'd always agree without coordinating — except for a
// narrow race if the config changed between the two reads, which is why
// `ownbasectl tunnel` additionally cross-checks against the Base's actually-
// applied Quadlet units rather than trusting this value alone (see
// internal/bridge.ParseActualHostPorts).
func (c *OwnbaseConfig) TunnelPorts() map[string]int {
	var names []string
	for name, svc := range c.Services {
		if svc.Port != 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	ports := make(map[string]int, len(names))
	for i, name := range names {
		ports[name] = TunnelBasePort + i
	}
	return ports
}

// Validate returns the first structural error in the config, or nil.
func (c *OwnbaseConfig) Validate() error {
	if c.SchemaVersion == "" {
		return errors.New("schema_version is required")
	}
	if c.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported schema_version %q (this binary understands %q)",
			c.SchemaVersion, CurrentSchemaVersion)
	}
	if err := c.Core.Backup.validate(); err != nil {
		return err
	}
	for name, svc := range c.Services {
		if err := svc.validate(name, c.Services); err != nil {
			return err
		}
	}
	return nil
}

// Warnings returns non-fatal issues worth surfacing to the user.
func (c *OwnbaseConfig) Warnings() []string {
	var warns []string
	for name, svc := range c.Services {
		if strings.TrimSpace(svc.Ref) == "" {
			warns = append(warns, fmt.Sprintf(
				"service %q has no ref: — the build falls back to the repo's default HEAD; run `ownbasectl deploy` to pin a specific ref", name))
		}
		if svc.Mode != "" {
			warns = append(warns, fmt.Sprintf(
				"service %q: mode: is deprecated and has no effect; remove it from ownbase.yaml", name))
		}
	}
	return warns
}

func (s ServiceDecl) validate(name string, allServices map[string]ServiceDecl) error {
	if strings.TrimSpace(s.Repo) == "" {
		return fmt.Errorf("service %q: repo is required", name)
	}
	if !isGitURL(s.Repo) {
		return fmt.Errorf("service %q: repo must be a git URL (e.g. \"git@github.com:org/app.git\" or \"https://github.com/org/repo\")", name)
	}
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("service %q: port %d is out of range", name, s.Port)
	}
	for _, cap := range s.Requires {
		if _, ok := allServices[cap]; !ok {
			return fmt.Errorf("service %q: required capability %q does not match any service key",
				name, cap)
		}
	}
	seenVolNames := make(map[string]bool)
	for i, v := range s.Volumes {
		if strings.TrimSpace(v.Name) == "" {
			return fmt.Errorf("service %q: volumes[%d]: name is required", name, i)
		}
		if strings.TrimSpace(v.Mount) == "" {
			return fmt.Errorf("service %q: volumes[%d] (%q): mount is required", name, i, v.Name)
		}
		if seenVolNames[v.Name] {
			return fmt.Errorf("service %q: duplicate volume name %q", name, v.Name)
		}
		seenVolNames[v.Name] = true
	}
	return nil
}

func (b BackupCoreConfig) validate() error {
	if !b.Enabled() {
		return nil
	}
	if _, err := b.EffectiveInterval(); err != nil {
		return err
	}
	if _, err := b.EffectiveVerifyInterval(); err != nil {
		return err
	}
	return nil
}

// isGitURL returns true when s looks like a remote git URL.
func isGitURL(s string) bool {
	return strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "git://") ||
		strings.HasPrefix(s, "ssh://") ||
		strings.HasPrefix(s, "git@")
}
