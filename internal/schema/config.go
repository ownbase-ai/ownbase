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

// MaxReplicas is the upper bound on ServiceDecl.Replicas. High enough for a
// dedicated machine's concurrent workers; low enough that a typo cannot ask
// the Base to materialize hundreds of containers.
const MaxReplicas = 64

// ContainerNamePrefix is the prefix every OwnBase-managed container name
// carries (services, jobs, core). Used by naming helpers and by callers that
// classify containers by prefix alone.
const ContainerNamePrefix = "ownbase-"

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
	Jobs          map[string]JobDecl     `yaml:"jobs,omitempty"`
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

	// VerifyPostgres controls whether the drill proves Postgres recoverable by
	// restoring the backed-up pgBackRest repository into a throwaway container
	// and waiting for a real database to come up. nil means true.
	//
	// Leaving it on is the point of the drill: without it, "restorable" means
	// the files came back, which is a much weaker claim than the database came
	// back. Set false only when the CPU and minutes it costs are genuinely a
	// problem, and understand that the recovery path then goes untested until
	// the day it is needed.
	VerifyPostgres *bool `yaml:"verify_postgres,omitempty"`
}

// PostgresVerifyEnabled reports whether the drill should prove Postgres
// recoverable, defaulting to true when unset.
func (b BackupCoreConfig) PostgresVerifyEnabled() bool {
	if b.VerifyPostgres == nil {
		return true
	}
	return *b.VerifyPostgres
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

	// Database declares a Postgres database this service needs, as
	// "<provider-service>/<dbname>" — e.g. "postgres/revolve". The provider is
	// named rather than inferred, so a Base with two databases has no ambiguity
	// and nothing depends on a service happening to be called "postgres".
	//
	// On reconcile the agent creates the database if it does not exist and
	// writes a DATABASE_URL secret for this service, composed from the
	// provider's user and password. The URL is never written to ownbase.yaml.
	// The provider must also appear in Requires.
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

	// Resources caps the container's cgroup budget via Podman. Empty means
	// unlimited (Podman/systemd default). Set these on agent workers and any
	// other service that can runaway-allocate — a Base is one machine and an
	// uncapped pool can OOM the host.
	//
	//	resources:
	//	  memory: 4g
	//	  cpus: "2"
	Resources *ResourcesDecl `yaml:"resources,omitempty"`

	// GeneratedSecrets declares secret values the agent creates for this
	// service when they do not already exist. Nothing sensitive is written
	// to ownbase.yaml — only the names of the keys to fill in.
	GeneratedSecrets []GeneratedSecretDecl `yaml:"generated_secrets,omitempty"`

	// Replicas, when set, runs this service as N indexed containers named
	// ownbase-<service>-0 … ownbase-<service>-(N-1). Each replica gets its
	// own loopback publish (for health_probe and tunnel) and, by default,
	// its own copy of every declared volume (see VolumeDecl.PerReplica).
	//
	// Absent (nil) preserves the single unindexed container ownbase-<service>
	// — byte-identical to configs that predate this field. When set, even
	// replicas: 1 is indexed (ownbase-<service>-0) so scaling 1→N never
	// renames containers or orphans volumes.
	//
	// This is concurrency, not high availability: a Base is one machine.
	// Routing, session affinity, and leasing stay in the application that
	// talks to the workers — OwnBase only provides stable, addressable
	// replicas with durable per-replica storage.
	//
	// Allowed range when set: 1..MaxReplicas.
	Replicas *int `yaml:"replicas,omitempty"`

	// OwnbaseAccess lists grant scopes this service may exercise against the
	// daemon API over a private unix socket. Empty/absent means no access —
	// the service cannot reach ownbased. Non-empty causes the daemon to
	// listen on /run/ownbase/svc/<name>/api.sock and the compiler to
	// bind-mount that directory into the container at /run/ownbase/.
	//
	// Scope strings match authz grant rules, e.g.:
	//   status:read, service:web:deploy, secrets:myapp:write, backup:run, *
	OwnbaseAccess []string `yaml:"ownbase_access,omitempty"`
}

// GeneratedSecretType is the kind of value a GeneratedSecretDecl produces.
type GeneratedSecretType string

const (
	// GeneratedSecretPassword is a random high-entropy string, for things
	// like POSTGRES_PASSWORD that must exist but that nobody needs to read.
	GeneratedSecretPassword GeneratedSecretType = "password"

	// GeneratedSecretSSHEd25519 is an ed25519 SSH keypair, where the public
	// half is usually stored on the service that accepts the connection and
	// the private half on the service that makes it.
	GeneratedSecretSSHEd25519 GeneratedSecretType = "ssh-ed25519"
)

// GeneratedSecretDecl declares one secret the agent generates on first
// reconcile if it is missing, rather than making the operator produce it by
// hand and paste it in with `ownbasectl secrets set`.
//
// Generation happens on the Base, so a private key never crosses the network
// or touches the operator's disk. It is skipped entirely when every
// destination key already has a value, which makes it idempotent across
// restarts and means a rebuilt Base regenerates what it needs on its own.
//
// A destination is written as "KEY" (this service) or "service:KEY" (another
// service), so a keypair can be split across the two ends of a connection:
//
//	generated_secrets:
//	  - type: ssh-ed25519
//	    public_key: PGBACKREST_CLIENT_PUBKEY
//	    private_key: postgres:PGBACKREST_SSH_KEY_B64
//	    private_encoding: base64
type GeneratedSecretDecl struct {
	// Type selects the generator. Required.
	Type GeneratedSecretType `yaml:"type"`

	// Key is the destination for a password. Required for type: password,
	// and not used by keypair types.
	Key string `yaml:"key,omitempty"`

	// Length is the password length in characters. Defaults to
	// DefaultGeneratedPasswordLength. Only meaningful for type: password.
	Length int `yaml:"length,omitempty"`

	// PublicKey is the destination for the public half of a keypair, in
	// OpenSSH authorized_keys form. Required for keypair types.
	PublicKey string `yaml:"public_key,omitempty"`

	// PrivateKey is the destination for the private half of a keypair, in
	// OpenSSH PEM form. Required for keypair types.
	PrivateKey string `yaml:"private_key,omitempty"`

	// PrivateEncoding is how the private key is encoded before being stored:
	// "raw" (the default) for PEM as-is, or "base64" for a single-line form,
	// which some images require because a PEM cannot survive a round trip
	// through an environment variable intact.
	PrivateEncoding string `yaml:"private_encoding,omitempty"`
}

// DefaultGeneratedPasswordLength is the password length used when Length is 0.
const DefaultGeneratedPasswordLength = 32

// EffectiveLength returns Length, or DefaultGeneratedPasswordLength when unset.
func (g GeneratedSecretDecl) EffectiveLength() int {
	if g.Length <= 0 {
		return DefaultGeneratedPasswordLength
	}
	return g.Length
}

// Base64Private reports whether the private key should be base64-encoded.
func (g GeneratedSecretDecl) Base64Private() bool {
	return g.PrivateEncoding == "base64"
}

// Destinations returns every secret destination this declaration writes to,
// as (service, key) pairs. An empty service means "the service that declared
// it" — the caller substitutes its own name.
func (g GeneratedSecretDecl) Destinations() []SecretDest {
	var out []SecretDest
	add := func(spec string) {
		if strings.TrimSpace(spec) == "" {
			return
		}
		out = append(out, ParseSecretDest(spec))
	}
	switch g.Type {
	case GeneratedSecretPassword:
		add(g.Key)
	case GeneratedSecretSSHEd25519:
		add(g.PublicKey)
		add(g.PrivateKey)
	}
	return out
}

// DatabaseRef is a parsed database: declaration.
type DatabaseRef struct {
	// Service is the service that runs the Postgres holding the database.
	Service string

	// Name is the database name.
	Name string
}

// DatabaseRef parses the database: field. The second return is false when the
// field is empty; a malformed value is rejected by Validate, so callers that
// have parsed a config can treat a true result as well-formed.
func (s ServiceDecl) DatabaseRef() (DatabaseRef, bool) {
	spec := strings.TrimSpace(s.Database)
	if spec == "" {
		return DatabaseRef{}, false
	}
	provider, name, found := strings.Cut(spec, "/")
	if !found {
		return DatabaseRef{Name: strings.TrimSpace(spec)}, true
	}
	return DatabaseRef{
		Service: strings.TrimSpace(provider),
		Name:    strings.TrimSpace(name),
	}, true
}

// DatabaseURLKey is the secret key a provisioned database's connection URL is
// written to. It becomes DATABASE_URL in the service's environment through the
// normal secrets path, so the URL is in neither git nor the unit file.
const DatabaseURLKey = "DATABASE_URL"

// validateDatabase checks a database: declaration.
//
// The provider is required to appear in requires: as well. The two say
// different things — requires: is what joins the containers to a network and
// orders their startup, database: is what gets created — and a database on a
// provider this service cannot reach is not a configuration worth accepting
// silently.
func (s ServiceDecl) validateDatabase(name string, allServices map[string]ServiceDecl) error {
	ref, ok := s.DatabaseRef()
	if !ok {
		return nil
	}
	where := fmt.Sprintf("service %q: database", name)

	if ref.Service == "" {
		return fmt.Errorf("%s: %q must be written as \"<service>/<dbname>\" (e.g. \"postgres/%s\") — the service that provides the database is named explicitly, never inferred",
			where, s.Database, ref.Name)
	}
	if ref.Name == "" {
		return fmt.Errorf("%s: %q is missing a database name after the \"/\"", where, s.Database)
	}
	if ref.Service == name {
		return fmt.Errorf("%s: %q names this service as its own provider", where, s.Database)
	}
	if _, ok := allServices[ref.Service]; !ok {
		return fmt.Errorf("%s: provider %q does not match any service key", where, ref.Service)
	}
	if !isDatabaseName(ref.Name) {
		return fmt.Errorf("%s: %q is not a usable database name — use letters, digits, and underscores, starting with a letter or underscore", where, ref.Name)
	}

	for _, req := range s.Requires {
		if req == ref.Service {
			return nil
		}
	}
	return fmt.Errorf("%s: provider %q must also be listed in requires: — otherwise this container is never joined to its network and cannot reach the database",
		where, ref.Service)
}

// isDatabaseName reports whether s is a plain unquoted SQL identifier.
//
// CREATE DATABASE cannot take the name as a bound parameter, so it is
// interpolated into the statement. Restricting the name here is what keeps that
// interpolation safe, and every name anyone actually wants already fits.
func isDatabaseName(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// SecretDest is one destination for a generated secret.
type SecretDest struct {
	// Service is the service whose secrets file receives the value. Empty
	// means the service that declared the generator.
	Service string

	// Key is the secret key name (which becomes an environment variable
	// inside the container).
	Key string
}

// ParseSecretDest splits a destination spec into its service and key.
// "KEY" yields an empty Service; "service:KEY" yields both.
func ParseSecretDest(spec string) SecretDest {
	spec = strings.TrimSpace(spec)
	if idx := strings.IndexByte(spec, ':'); idx >= 0 {
		return SecretDest{
			Service: strings.TrimSpace(spec[:idx]),
			Key:     strings.TrimSpace(spec[idx+1:]),
		}
	}
	return SecretDest{Key: spec}
}

// VolumeDecl declares one named Podman volume for a service.
// The Podman volume name is "ownbase-<service>-<name>", or
// "ownbase-<service>-<name>-<i>" when the service is replicated and this
// volume is per-replica (the default).
type VolumeDecl struct {
	// Name is the short name for this volume (e.g. "config", "media", "cache").
	// The Podman volume is named "ownbase-<service>-<name>" (shared) or
	// "ownbase-<service>-<name>-<i>" (per-replica).
	Name string `yaml:"name"`

	// Mount is where the volume is mounted inside the container (e.g. "/config").
	Mount string `yaml:"mount"`

	// Backup is a list of paths within this volume to include in restic snapshots,
	// relative to Mount. Use "." to back up the entire volume.
	// Examples: ["."], ["./config", "./data/db"], ["./music", "./photos"]
	// Omit (or leave empty) to exclude this volume from backups entirely.
	// When the volume is per-replica, every replica's copy is enumerated.
	Backup []string `yaml:"backup,omitempty"`

	// PerReplica controls whether a replicated service gets one volume per
	// replica (true) or one shared volume mounted by every replica (false).
	// Nil defaults to true when the service has replicas: set, and is ignored
	// when the service is not replicated. Prefer the default for worker state
	// that assumes exclusive ownership; set false for a shared workspace or
	// cache that every replica should see.
	PerReplica *bool `yaml:"per_replica,omitempty"`
}

// HealthProbeDecl describes how the agent verifies a service is healthy.
// Only HTTP probes are supported in V1.
type HealthProbeDecl struct {
	// HTTP is the path to GET on localhost:Port. The probe succeeds when the
	// server returns a 2xx status within the timeout. Example: "/-/health"
	HTTP string `yaml:"http,omitempty"`
}

// ResourcesDecl caps a container's cgroup budget. Values are passed through
// to Podman as --memory / --cpus (same syntax Podman accepts).
type ResourcesDecl struct {
	// Memory is a Podman memory limit, e.g. "512m", "4g". Empty means no limit.
	Memory string `yaml:"memory,omitempty"`
	// CPUs is a Podman CPU quota as a count of CPUs, e.g. "1", "2", "1.5".
	// Empty means no limit. Quoted in YAML when it would otherwise be a float.
	CPUs string `yaml:"cpus,omitempty"`
}

// validate checks Memory/CPUs syntax. Both may be empty; at least one non-empty
// field is expected when the parent pointer is non-nil, but an empty block is
// tolerated (no-op) so partial edits don't fail validate.
func (r ResourcesDecl) validate(service string) error {
	if r.Memory != "" {
		if err := validateMemoryLimit(r.Memory); err != nil {
			return fmt.Errorf("service %q: resources.memory: %w", service, err)
		}
	}
	if r.CPUs != "" {
		if err := validateCPULimit(r.CPUs); err != nil {
			return fmt.Errorf("service %q: resources.cpus: %w", service, err)
		}
	}
	return nil
}

// validateMemoryLimit accepts Podman-style sizes: bare bytes digits, or a
// number with a b/k/m/g suffix (case-insensitive). No spaces. Zero is rejected
// because Podman treats --memory 0 as unlimited, which would silently undo the
// cgroup cap.
func validateMemoryLimit(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("empty")
	}
	if strings.ContainsAny(s, " \t") {
		return fmt.Errorf("%q must not contain whitespace", s)
	}
	lower := strings.ToLower(s)
	// Strip one trailing unit letter if present.
	body := lower
	if n := len(body); n > 0 {
		switch body[n-1] {
		case 'b', 'k', 'm', 'g':
			body = body[:n-1]
		}
	}
	if body == "" {
		return fmt.Errorf("%q needs a number (e.g. 512m, 4g)", s)
	}
	dot := false
	for i, c := range body {
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '.' && !dot && i > 0 && i < len(body)-1 {
			dot = true
			continue
		}
		return fmt.Errorf("%q is not a Podman memory size (use 512m, 4g, …)", s)
	}
	// Reject zero / all-zeros (0, 0g, 0.0m) — same rule as CPUs.
	trimmed := strings.TrimLeft(strings.ReplaceAll(body, ".", ""), "0")
	if trimmed == "" {
		return fmt.Errorf("%q must be > 0", s)
	}
	return nil
}

// validateCPULimit accepts a positive decimal CPU count ("1", "2", "0.5", "1.5").
func validateCPULimit(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("empty")
	}
	if strings.ContainsAny(s, " \t") {
		return fmt.Errorf("%q must not contain whitespace", s)
	}
	dot := false
	for i, c := range s {
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '.' && !dot && i > 0 && i < len(s)-1 {
			dot = true
			continue
		}
		return fmt.Errorf("%q is not a CPU count (use 1, 2, 1.5, …)", s)
	}
	// Reject zero / all-zeros.
	trimmed := strings.TrimLeft(strings.ReplaceAll(s, ".", ""), "0")
	if trimmed == "" {
		return fmt.Errorf("%q must be > 0", s)
	}
	return nil
}

// JobDecl is one scheduled job entry in the jobs map. The map key is the
// job's instance name (e.g. "nightly-ingest").
//
// A job runs Command on a systemd timer, reusing the image, networks, and
// hardening of an existing service (Service) rather than building anything
// of its own. It compiles to a oneshot Quadlet container plus a companion
// native systemd .timer — see internal/compiler.build's job handling. Unlike
// services, jobs are never started/stopped by the reconcile start/stop
// lifecycle; the timer alone drives execution (internal/reconcile.Diff
// treats a job's container specially — see isJobContainer).
type JobDecl struct {
	// Service is the key of an existing entry in the services map whose
	// built image, networks, env, and injected secrets this job reuses.
	// Required; must match a services: key.
	Service string `yaml:"service"`

	// Command overrides the image's default entrypoint/cmd, e.g.
	// ["python", "scripts/nightly_ingest.py", "--region", "mx"]. Required.
	Command []string `yaml:"command"`

	// Schedule is a systemd OnCalendar expression, e.g. "daily" or
	// "*-*-* 08:00:00 UTC". Required. See systemd.time(7) for the grammar.
	Schedule string `yaml:"schedule"`

	// Env is a list of additional static KEY=VALUE environment variables,
	// appended after the referenced service's own env: list (a key set in
	// both wins with the job's value, since Environment= directives are
	// applied in order and systemd/Podman take the last occurrence).
	Env []string `yaml:"env,omitempty"`

	// Persistent mirrors the timer's Persistent= setting: when true (the
	// default), a run that was missed while the Base was powered off fires
	// once on the next boot instead of being skipped until the next
	// scheduled time. nil means true; set false to disable catch-up runs.
	Persistent *bool `yaml:"persistent,omitempty"`
}

// EffectivePersistent returns the job's Persistent setting, defaulting to
// true when unset.
func (j JobDecl) EffectivePersistent() bool {
	if j.Persistent == nil {
		return true
	}
	return *j.Persistent
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
// any port'd container's direct-to-container publish. Assignment is
// deliberately decoupled from any service's own container Port so that a
// service can declare port: 80/443 (or share a port number with another
// service) without colliding with Caddy's machine-wide bind or with each
// other on the loopback publish. Despite the name, this isn't exclusively
// for `ownbasectl tunnel`: the daemon's own HTTP health_probe
// (internal/podman's waitForContainer) also dials a container directly over
// this same loopback publish, including for domain-less internal services
// the tunnel never bridges — see TunnelPorts.
const TunnelBasePort = 41000

// TunnelPorts returns the deterministic loopback port assigned to each
// port'd container, keyed by **container name** (e.g. "ownbase-hello" or
// "ownbase-opencode-2"). Eligibility is intentionally broader than
// HasPublicDomain/what `ownbasectl tunnel` bridges: ANY service with a Port
// set gets an entry per container, domain or not, because two independent
// things depend on this loopback publish existing —
//  1. `ownbasectl tunnel`'s SSH bridge (domain'd services only — see
//     internal/bridge.Discover, which filters this map down and uses
//     PrimaryContainerName for replicated services).
//  2. The daemon's own startup HTTP health_probe (internal/podman's
//     waitForContainer), which needs a loopback port to dial for ANY
//     port'd container, including purely-internal ones with no domain.
//
// Allocation is strided so adding replicas: to one service does not
// renumber unrelated services (which would restart them):
//
//	port(service_i, replica_j) = TunnelBasePort + i + j*N
//
// where i is the sorted index among port'd services and N is how many
// port'd services there are. Replica 0 (and every unreplicated service)
// therefore lands on exactly the same port the old one-port-per-service
// scheme assigned — configs without replicas: stay byte-identical. Extra
// replicas occupy higher bands (base+N, base+2N, …) that no unreplicated
// service uses.
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

	n := len(names)
	ports := make(map[string]int)
	for i, name := range names {
		svc := c.Services[name]
		for j, cname := range ContainerNames(name, svc.Replicas) {
			// j=0 preserves the historic one-port-per-service slot.
			ports[cname] = TunnelBasePort + i + j*n
		}
	}
	return ports
}

// IsReplicated reports whether this service declares replicas: (even 1).
// Absent replicas: is not replicated and keeps the unindexed container name.
func (s ServiceDecl) IsReplicated() bool {
	return s.Replicas != nil
}

// ReplicaCount returns how many containers this service compiles to.
// Absent replicas: yields 1 (the unindexed single container).
func (s ServiceDecl) ReplicaCount() int {
	if s.Replicas == nil {
		return 1
	}
	return *s.Replicas
}

// VolumeIsPerReplica reports whether volume v is instantiated once per
// replica. Always false when the service is not replicated. When replicated,
// nil PerReplica defaults to true.
func (s ServiceDecl) VolumeIsPerReplica(v VolumeDecl) bool {
	if !s.IsReplicated() {
		return false
	}
	if v.PerReplica == nil {
		return true
	}
	return *v.PerReplica
}

// DataPathIsPerReplica reports whether the data_path: shorthand volume is
// per-replica. Same default as VolumeIsPerReplica when Volumes is empty.
func (s ServiceDecl) DataPathIsPerReplica() bool {
	return s.IsReplicated()
}

// ContainerName returns the Podman/systemd container name for one instance
// of service. When replicas is nil the name is unindexed ("ownbase-<service>");
// when set it is "ownbase-<service>-<index>".
func ContainerName(service string, replicas *int, index int) string {
	if replicas == nil {
		return ContainerNamePrefix + service
	}
	return fmt.Sprintf("%s%s-%d", ContainerNamePrefix, service, index)
}

// ContainerNames returns every container name for a service, in index order.
func ContainerNames(service string, replicas *int) []string {
	if replicas == nil {
		return []string{ContainerNamePrefix + service}
	}
	n := *replicas
	if n < 1 {
		n = 1
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = ContainerName(service, replicas, i)
	}
	return out
}

// PrimaryContainerName is the stable single address for consumers that need
// one host (gendb DATABASE_URL, ownbasectl tunnel, pgBackRest): replica 0
// when replicated, otherwise the unindexed name.
func PrimaryContainerName(service string, replicas *int) string {
	return ContainerName(service, replicas, 0)
}

// VolumeName returns the Podman volume name for one volume instance.
// perReplica and index are ignored when the service is not using per-replica
// volumes for this entry (shared name "ownbase-<service>-<vol>").
func VolumeName(service, vol string, replicas *int, index int, perReplica bool) string {
	base := fmt.Sprintf("%s%s-%s", ContainerNamePrefix, service, vol)
	if replicas == nil || !perReplica {
		return base
	}
	return fmt.Sprintf("%s-%d", base, index)
}

// DataVolumeName is VolumeName for the data_path: shorthand volume ("data").
func DataVolumeName(service string, replicas *int, index int, perReplica bool) string {
	return VolumeName(service, "data", replicas, index, perReplica)
}

// ServiceKeyFromContainer trims the ownbase- prefix and, when the remainder
// ends in -<digits> and matches a known replicated service pattern, is not
// used for secrets lookup — callers that need the service key should prefer
// the # Service= provenance comment. This helper only strips the prefix.
func ServiceKeyFromContainer(containerName string) string {
	return strings.TrimPrefix(containerName, ContainerNamePrefix)
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
		// Job containers compile to "ownbase-job-<job-name>" (see
		// internal/compiler) so reconcile can tell them apart from
		// long-running service containers ("ownbase-<service-name>") by name
		// prefix alone. A service named "job-*" would compile to that same
		// "ownbase-job-*" prefix, get misclassified as a scheduled job, and
		// never be started by reconcile.
		if strings.HasPrefix(name, "job-") {
			return fmt.Errorf("service %q: service names may not start with %q (reserved for scheduled jobs)", name, "job-")
		}
		if err := svc.validate(name, c.Services); err != nil {
			return err
		}
	}
	if err := validateReplicaNameCollisions(c.Services); err != nil {
		return err
	}
	if err := validateDomainUniqueness(c.Services); err != nil {
		return err
	}
	for name, job := range c.Jobs {
		if err := job.validate(name, c.Services); err != nil {
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
	if s.Replicas != nil {
		n := *s.Replicas
		if n < 1 || n > MaxReplicas {
			return fmt.Errorf("service %q: replicas %d is out of range (must be 1..%d)", name, n, MaxReplicas)
		}
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
	for i, g := range s.GeneratedSecrets {
		if err := g.validate(name, i, allServices); err != nil {
			return err
		}
	}
	if s.Resources != nil {
		if err := s.Resources.validate(name); err != nil {
			return err
		}
	}
	if err := validateOwnbaseAccess(name, s.OwnbaseAccess); err != nil {
		return err
	}
	return s.validateDatabase(name, allServices)
}

// validateOwnbaseAccess checks declared grant scopes. Patterns must be
// non-empty and use only safe characters; closed-set matching happens at
// authorize time against authz.scopeForAction.
func validateOwnbaseAccess(service string, scopes []string) error {
	seen := make(map[string]bool, len(scopes))
	for i, raw := range scopes {
		s := strings.TrimSpace(raw)
		if s == "" {
			return fmt.Errorf("service %q: ownbase_access[%d]: scope is empty", service, i)
		}
		if seen[s] {
			return fmt.Errorf("service %q: ownbase_access: duplicate scope %q", service, s)
		}
		seen[s] = true
		if !isValidAccessScope(s) {
			return fmt.Errorf("service %q: ownbase_access: invalid scope %q (use e.g. status:read, service:web:deploy, secrets:myapp:write, *)", service, s)
		}
	}
	return nil
}

func isValidAccessScope(s string) bool {
	if s == "*" {
		return true
	}
	// Allow trailing wildcard: service:web:*
	if strings.HasSuffix(s, ":*") {
		s = strings.TrimSuffix(s, ":*")
		if s == "" {
			return false
		}
	} else if strings.HasSuffix(s, "*") && !strings.HasSuffix(s, ":*") {
		// bare prefix* without colon — only allow full "*"
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == ':' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return strings.Contains(s, ":") || s == "deploy" // bare "deploy" is a valid short form
}

// validateReplicaNameCollisions rejects configs where a generated replica
// container name would collide with another service's container name — e.g.
// service "web" with replicas: 2 and a service literally named "web-0" both
// compile to "ownbase-web-0". Also rejects Podman volume name collisions
// within or across services (per-replica "state" index 0 is the same string
// as a shared volume named "state-0").
func validateReplicaNameCollisions(services map[string]ServiceDecl) error {
	// claimed[containerName] = service key that owns it
	claimed := make(map[string]string)
	// volClaimed[volumeName] = "service/volDecl" that owns it
	volClaimed := make(map[string]string)
	var names []string
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := services[name]
		for _, cname := range ContainerNames(name, svc.Replicas) {
			if other, ok := claimed[cname]; ok {
				return fmt.Errorf("service %q: container name %q collides with service %q (rename one of them, or adjust replicas:)", name, cname, other)
			}
			claimed[cname] = name
		}
		for _, volName := range expandedVolumeNames(name, svc) {
			if other, ok := volClaimed[volName]; ok {
				return fmt.Errorf("service %q: volume name %q collides with %s (rename a volumes: entry or adjust per_replica/replicas)", name, volName, other)
			}
			volClaimed[volName] = name
		}
	}
	return nil
}

// validateDomainUniqueness rejects two services claiming the same hostname.
// Public services would otherwise become one Caddy reverse_proxy pool;
// tunnel-only services would fight over the same .localhost route. Matches
// ownbasectl tunnel's "each domain → exactly one service" rule.
func validateDomainUniqueness(services map[string]ServiceDecl) error {
	claimed := make(map[string]string) // domain → service key
	var names []string
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, d := range services[name].EffectiveDomains() {
			if d == "" {
				continue
			}
			if other, ok := claimed[d]; ok {
				return fmt.Errorf("domain %q is claimed by both service %q and service %q (each hostname must belong to exactly one service)", d, other, name)
			}
			claimed[d] = name
		}
	}
	return nil
}

// expandedVolumeNames returns every Podman volume name a service will create
// (shared and per-replica instances), matching the compiler/backup naming.
func expandedVolumeNames(service string, svc ServiceDecl) []string {
	var out []string
	if len(svc.Volumes) > 0 {
		for _, v := range svc.Volumes {
			if svc.VolumeIsPerReplica(v) {
				for i := 0; i < svc.ReplicaCount(); i++ {
					out = append(out, VolumeName(service, v.Name, svc.Replicas, i, true))
				}
			} else {
				out = append(out, VolumeName(service, v.Name, svc.Replicas, 0, false))
			}
		}
		return out
	}
	// data_path shorthand
	if svc.DataPathIsPerReplica() {
		for i := 0; i < svc.ReplicaCount(); i++ {
			out = append(out, DataVolumeName(service, svc.Replicas, i, true))
		}
	} else {
		out = append(out, DataVolumeName(service, svc.Replicas, 0, false))
	}
	return out
}

func (g GeneratedSecretDecl) validate(svcName string, idx int, allServices map[string]ServiceDecl) error {
	where := fmt.Sprintf("service %q: generated_secrets[%d]", svcName, idx)

	switch g.Type {
	case GeneratedSecretPassword:
		if strings.TrimSpace(g.Key) == "" {
			return fmt.Errorf("%s: key is required for type: password", where)
		}
		if g.PublicKey != "" || g.PrivateKey != "" {
			return fmt.Errorf("%s: public_key/private_key are not used by type: password (use key:)", where)
		}
	case GeneratedSecretSSHEd25519:
		if strings.TrimSpace(g.PublicKey) == "" {
			return fmt.Errorf("%s: public_key is required for type: %s", where, g.Type)
		}
		if strings.TrimSpace(g.PrivateKey) == "" {
			return fmt.Errorf("%s: private_key is required for type: %s", where, g.Type)
		}
		if g.Key != "" {
			return fmt.Errorf("%s: key is not used by type: %s (use public_key:/private_key:)", where, g.Type)
		}
	case "":
		return fmt.Errorf("%s: type is required (one of: %s, %s)",
			where, GeneratedSecretPassword, GeneratedSecretSSHEd25519)
	default:
		return fmt.Errorf("%s: unknown type %q (one of: %s, %s)",
			where, g.Type, GeneratedSecretPassword, GeneratedSecretSSHEd25519)
	}

	if enc := g.PrivateEncoding; enc != "" && enc != "raw" && enc != "base64" {
		return fmt.Errorf("%s: private_encoding %q must be \"raw\" or \"base64\"", where, enc)
	}

	// A destination naming another service must name one that exists, or the
	// generated half would be written to a secrets file nothing ever reads.
	for _, d := range g.Destinations() {
		if d.Key == "" {
			return fmt.Errorf("%s: destination is missing a key name", where)
		}
		if d.Service == "" {
			continue
		}
		if _, ok := allServices[d.Service]; !ok {
			return fmt.Errorf("%s: destination service %q does not match any service key", where, d.Service)
		}
	}
	return nil
}

func (j JobDecl) validate(name string, allServices map[string]ServiceDecl) error {
	// Job secrets are stored at the same <name>.yaml.age path convention as
	// service secrets (see internal/podman's injectSecrets). A job sharing a
	// name with a service would silently read/overwrite that service's
	// secrets file via `ownbasectl secrets set`, so the two namespaces must
	// stay disjoint.
	if _, ok := allServices[name]; ok {
		return fmt.Errorf("job %q: name collides with a service of the same name (jobs and services share the secrets-file namespace)", name)
	}
	if strings.TrimSpace(j.Service) == "" {
		return fmt.Errorf("job %q: service is required", name)
	}
	if _, ok := allServices[j.Service]; !ok {
		return fmt.Errorf("job %q: service %q does not match any service key", name, j.Service)
	}
	if len(j.Command) == 0 {
		return fmt.Errorf("job %q: command is required", name)
	}
	if strings.TrimSpace(j.Schedule) == "" {
		return fmt.Errorf("job %q: schedule is required", name)
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
