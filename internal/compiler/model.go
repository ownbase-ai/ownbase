package compiler

// RuntimeModel is the complete in-memory representation of what should run.
// The compiler produces exactly one of these; emitters render it to output formats.
type RuntimeModel struct {
	Containers []ContainerModel
	Networks   []NetworkModel
	Volumes    []VolumeModel
	Routes     []RouteModel
	// ACMEEmail is propagated from core.caddy.email when a public domain is
	// configured. When non-empty, renderCaddyfile emits a global Caddy block
	// with the email so Let's Encrypt can provision TLS certificates.
	ACMEEmail string
}

// ContainerModel represents one rootless Podman container.
type ContainerModel struct {
	// Name is the container name, e.g. "ownbase-auth".
	Name string

	// Image is the container image reference.
	// For source-built services: "localhost/ownbase-auth:local" (built from
	// the checkout before apply). For image-bundled services: the fully-
	// qualified public image.
	Image string

	// Digest is the content-addressable digest to pin the image to.
	// When non-empty, Podman pulls Image@Digest instead of Image.
	// Example: "sha256:abc123..."
	// The agent opens a PR to bump this field when a new digest is published.
	Digest string

	// IsImageBundled is true when the image is pulled directly (image: field
	// in ownbase.yaml) rather than built from source. The agent skips the
	// build step for bundled containers.
	IsImageBundled bool

	// BuildSource is the local repo path (source-built only).
	BuildSource string
	// BuildRef is the branch, tag, or commit SHA to check out.
	BuildRef string
	// BuildDockerfile is the path to the Dockerfile within the build context.
	// Defaults to "Dockerfile".
	BuildDockerfile string
	// BuildContext is the subdirectory within the repo to use as build context.
	// Empty means the repo root.
	BuildContext string

	// Internal is true when the service has domain(s) and port set but
	// must not be given a Caddy route. The service is reachable exclusively
	// via `ownbasectl tunnel`; it is never internet-facing.
	Internal bool

	// PublicDomains lists the public hostnames for Caddy routing — one Caddy
	// site block per domain, all pointing at this container's PublicPort.
	// Empty when the service has no domain, or when Internal is true.
	PublicDomains []string
	// PublicPort is the host-local port published for Caddy.
	// Zero means no public port (internal-only container).
	PublicPort int

	// DevBridgePort is the loopback host port for this container's
	// direct-to-container publish, assigned deterministically by
	// schema.OwnbaseConfig.DevBridgePorts() (see build()). Deliberately
	// decoupled from PublicPort — the container still listens on
	// PublicPort; only the host-side number differs — so that a service can
	// declare port: 80/443, or share a port number with another service,
	// without colliding with Caddy's own machine-wide bind or with another
	// service's loopback publish. Despite the name, two independent
	// consumers dial this: `ownbasectl tunnel`'s SSH bridge (domain'd services
	// only) and the daemon's own HTTP health_probe (any port'd service,
	// domain or not). Zero means no port: at all (nothing to publish).
	DevBridgePort int

	// HostPublishPorts lists ports published on ALL host interfaces
	// (PublishPort=<p>:<p>, i.e. 0.0.0.0). This is the public web entrypoint
	// and is distinct from PublicPort, which binds loopback-only for services
	// that sit behind Caddy. Currently used only by the core Caddy package,
	// which must accept external traffic on 80/443. Empty for normal services.
	HostPublishPorts []int

	// Networks is the ordered list of network names this container joins.
	Networks []string

	// VolumeMounts is the ordered list of named volumes to mount.
	VolumeMounts []VolumeMount

	// Env is a list of static KEY=VALUE environment variables.
	// Values appear in the Quadlet unit file in plaintext.
	Env []string

	// HealthProbe describes how the agent verifies the service is healthy.
	// Nil means no probe beyond waiting for the container to enter Running state.
	HealthProbe *HealthProbeModel

	// Requires is the list of service names (keys in ownbase.yaml) that
	// this container depends on. The agent starts providers before consumers
	// and gates consumer start on provider health. Mirrors the requires: field
	// in schema.ServiceDecl; kept here so the reconciler can read it from the
	// model without re-parsing the config.
	Requires []string

	// User is the UID or username to run as inside the container (e.g. "1000"
	// or "nobody"). Empty means the image default — typically root, which is
	// why non-root is opt-in via schema.ServiceDecl.user or hardcoded for
	// well-known core packages.
	User string

	// AddCapabilities is the minimal list of Linux capabilities to add back
	// after DropCapability=ALL. Empty is the safe default for most services.
	// Example: ["NET_BIND_SERVICE"] for a service that binds port 80/443.
	AddCapabilities []string

	// SecurityOpts is a list of --security-opt values passed to Podman.
	// Each entry becomes a PodmanArgs= line in the Quadlet unit.
	// Example: ["apparmor=unconfined"] for postgres which needs inter-process
	// signaling between its child daemons (checkpointer, bgwriter, etc.).
	SecurityOpts []string
}

// VolumeMount binds a named Podman volume to a path inside a container.
type VolumeMount struct {
	// VolumeName is the Podman named volume (e.g. "ownbase-core-caddy-data").
	VolumeName string
	// MountPath is the path inside the container (e.g. "/data").
	MountPath string
}

// HealthProbeModel is the compiled form of a health probe declaration.
type HealthProbeModel struct {
	// HTTPPath is the path to GET on localhost:port to verify health.
	// The probe succeeds when the server returns a 2xx status.
	HTTPPath string
}

// NetworkModel represents one Podman network.
type NetworkModel struct {
	Name string
}

// VolumeModel represents one Podman named volume.
type VolumeModel struct {
	Name string
}

// RouteModel represents one Caddy reverse-proxy route.
type RouteModel struct {
	Host     string
	Upstream string
}
