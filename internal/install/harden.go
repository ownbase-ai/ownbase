package install

// harden.go implements the host hardening steps that run as part of PassZero:
//
//   - UFW firewall (deny all inbound; allow SSH, HTTP, HTTPS)
//   - Automatic security updates via unattended-upgrades
//   - fail2ban for SSH brute-force protection
//   - Verification that database ports (5432, 3306) are not publicly reachable
//
// Every function is idempotent: it checks the current state first and skips
// the action if the condition is already satisfied. This is what makes
// PassZero resumable — restart after a failure, and only the incomplete
// steps run.
//
// Git access is over the normal admin SSH port using the standard admin user
// (no dedicated "git" system user, no AuthorizedKeysCommand shim): ownbasectl
// pushes directly to the bare repo at /opt/ownbase/repo over SSH.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ownbase/ownbase/internal/secwatch"
)

// ---------------------------------------------------------------------------
// Firewall (UFW)
// ---------------------------------------------------------------------------

// ensureFirewall configures UFW to:
//   - Deny all inbound by default.
//   - Allow SSH (configurable port), and HTTP (80) + HTTPS (443) only when
//     cfg.ExposeWebPorts is true (i.e. at least one service has a domain
//     configured — see schema.OwnbaseConfig.HasPublicDomain).
//   - Enable UFW if not already enabled.
//
// A domain-less Base (the default state of a fresh Base) therefore exposes
// nothing but SSH externally: there is no Caddy route to serve on 80/443
// yet, and `ownbasectl tunnel` reaches services directly over SSH,
// bypassing Caddy entirely.
//
// Outbound is unrestricted (containers need to pull images, DNS, etc.).
func ensureFirewall(ctx context.Context, cfg PassZeroConfig) StepStatus {
	s := checkFirewallState(ctx, cfg)
	if s.Done {
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would configure UFW firewall"}
	}

	// Install ufw if missing.
	if !cmdExists("ufw") {
		if _, err := apt(ctx, "ufw", false); err != nil {
			return StepStatus{Err: err}
		}
	}

	cmds := [][]string{
		{"ufw", "--force", "reset"},
		{"ufw", "default", "deny", "incoming"},
		{"ufw", "default", "allow", "outgoing"},
		{"ufw", "allow", fmt.Sprintf("%d/tcp", cfg.SSHPort), "comment", "SSH"},
	}
	if cfg.ExposeWebPorts {
		cmds = append(cmds,
			[]string{"ufw", "allow", "80/tcp", "comment", "HTTP"},
			[]string{"ufw", "allow", "443/tcp", "comment", "HTTPS"},
			// Allow forwarding of packets that netavark has DNAT'd to container
			// IPs. UFW's default deny-routed policy otherwise drops them after
			// DNAT. Scoped to specific ports rather than the whole subnet so a
			// container that accidentally publishes on 0.0.0.0 doesn't become
			// externally reachable without an explicit UFW INPUT rule.
			[]string{"ufw", "route", "allow", "proto", "tcp", "from", "any", "to", "10.88.0.0/12", "port", "80", "comment", "Caddy HTTP forwarding"},
			[]string{"ufw", "route", "allow", "proto", "tcp", "from", "any", "to", "10.88.0.0/12", "port", "443", "comment", "Caddy HTTPS forwarding"},
		)
	}
	cmds = append(cmds,
		// Allow containers to reach the aardvark-dns server (bound to Podman
		// bridge gateway IPs). Podman uses 10.88.0.0/12 and 10.89.0.0/16 for
		// container subnets; their DNS queries target the gateway IPs on UDP/53.
		// Without these rules UFW's INPUT DROP policy silently discards the
		// queries, breaking inter-container hostname resolution.
		[]string{"ufw", "allow", "proto", "udp", "from", "10.88.0.0/12", "to", "10.88.0.0/12", "port", "53", "comment", "Podman DNS (10.88.0.0/12)"},
		[]string{"ufw", "allow", "proto", "udp", "from", "10.89.0.0/16", "to", "10.89.0.0/16", "port", "53", "comment", "Podman DNS (10.89.0.0/16)"},
	)
	cmds = append(cmds, []string{"ufw", "--force", "enable"})
	for _, args := range cmds {
		if _, err := run(ctx, args[0], args[1:]...); err != nil {
			return StepStatus{Err: fmt.Errorf("ufw %s: %w", args[1], err)}
		}
	}
	if cfg.ExposeWebPorts {
		return StepStatus{Done: true, Detail: fmt.Sprintf("UFW enabled: SSH(%d), 80, 443", cfg.SSHPort)}
	}
	return StepStatus{Done: true, Detail: fmt.Sprintf("UFW enabled: SSH(%d) only (no domain configured yet)", cfg.SSHPort)}
}

// checkFirewallState returns whether UFW is active AND its web-port rules
// (80/443) already match cfg.ExposeWebPorts, without making changes.
//
// Checking only "is UFW active" (as this used to do) means ensureFirewall
// short-circuits forever once UFW is first enabled — a Base hardened with
// ExposeWebPorts: false (the common case: a fresh Base with no domain yet)
// would never gain 80/443 later when a service gets a domain, since nothing
// re-evaluates the firewall rules against the new desired state. Comparing
// the actual rules against cfg.ExposeWebPorts makes ensureFirewall correctly
// idempotent in both directions, so callers can re-invoke it on every
// reconcile tick (see install.SyncFirewallExposure) and it will only
// reconfigure UFW when the exposure state has actually changed.
func checkFirewallState(ctx context.Context, cfg PassZeroConfig) StepStatus {
	if !cmdExists("ufw") {
		return StepStatus{Done: false, Detail: "ufw not installed"}
	}
	out, err := run(ctx, "ufw", "status")
	if err != nil {
		return StepStatus{Done: false, Detail: "ufw status failed: " + err.Error()}
	}
	if !strings.Contains(out, "Status: active") {
		return StepStatus{Done: false, Detail: "UFW installed but not active"}
	}
	if !webPortsMatchDesired(out, cfg.ExposeWebPorts) {
		return StepStatus{Done: false, Detail: "UFW active but web-port rules don't match the desired domain configuration"}
	}
	return StepStatus{Done: true, AlreadyOK: true, Detail: "UFW active"}
}

// ufwRuleAllowed reports whether `ufw status` output contains an ALLOW rule
// for the exact port/proto token (e.g. "80/tcp") as the first field of a
// line. Matching the first field only (rather than strings.Contains on the
// whole output) avoids a false positive against an unrelated rule whose
// port happens to contain the same digits as a substring (e.g. "8080/tcp"
// textually contains "80/tcp").
//
// It also requires the line's action to actually be ALLOW: a DENY, REJECT,
// or LIMIT rule for that same port/proto (e.g. left over from a previous
// manual `ufw` invocation) must not be mistaken for the port being open.
// The action column isn't always at a fixed index — an IPv6 port token like
// "80/tcp (v6)" shifts it right by one field — so this scans every
// remaining field on the line rather than checking a hardcoded position.
func ufwRuleAllowed(status, portProto string) bool {
	for _, line := range strings.Split(status, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != portProto {
			continue
		}
		for _, f := range fields[1:] {
			if f == "ALLOW" {
				return true
			}
		}
	}
	return false
}

// webPortsMatchDesired reports whether `ufw status` output's 80/tcp and
// 443/tcp rules both match exposeWebPorts — i.e. both allowed when true,
// both NOT allowed when false.
//
// Each port is compared independently rather than folding them into a
// single "are both open" bool with &&: that works for the true case (both
// must be open to match) but silently breaks the false case, where a
// partially-open state (e.g. 80 still allowed, 443 not) would AND down to
// false — matching a desired "false" and leaving 80 exposed to the world
// on a domain-less Base.
func webPortsMatchDesired(status string, exposeWebPorts bool) bool {
	port80Allowed := ufwRuleAllowed(status, "80/tcp")
	port443Allowed := ufwRuleAllowed(status, "443/tcp")
	return exposeWebPorts == port80Allowed && exposeWebPorts == port443Allowed
}

// SyncFirewallExposure re-evaluates UFW's web-port rules (80/443) against
// cfg.ExposeWebPorts and reconfigures the firewall only if they don't
// already match — a no-op fast path (a single `ufw status` call) otherwise.
//
// Unlike PassZero, which runs the full hardening pass once at daemon
// startup, this is meant to be called on every reconcile tick so that
// adding or removing a service's domain takes effect immediately, without
// requiring a daemon restart.
func SyncFirewallExposure(ctx context.Context, cfg PassZeroConfig) StepStatus {
	return ensureFirewall(ctx, cfg.withDefaults())
}

// containerEgressSubnets are the Podman subnet ranges whose forwarded
// (routed) traffic UFW must allow out. Podman assigns container networks from
// 10.88.0.0/12 (default) and 10.89.0.0/16 (ownbase capability nets).
var containerEgressSubnets = []string{"10.88.0.0/12", "10.89.0.0/16"}

func containerEgressComment(subnet string) string {
	return "Container egress (" + subnet + ")"
}

// ensureContainerEgress allows container networks to reach the internet.
//
// ensureFirewall sets UFW's default routed policy to DENY (deny (routed) /
// DEFAULT_FORWARD_POLICY=DROP) and only opens the FORWARD path for Caddy's
// DNAT'd ingress. Container→internet traffic is *forwarded* (routed) traffic,
// not host-originated, so "ufw default allow outgoing" does NOT cover it — it
// is dropped by the default deny-routed policy. That silently breaks every
// Dockerfile build that downloads dependencies (apt/apk, npm, pip/uv, plugin
// zips) and every runtime call a service makes to an external API. This step
// adds a `ufw route allow` for each Podman subnet so containers can egress.
//
// Idempotent: it checks `ufw status` for the marker comment on each subnet
// and only adds the rules that are missing, so it is safe to run on every
// daemon start (including on an already-hardened Base that predates this fix).
func ensureContainerEgress(ctx context.Context, cfg PassZeroConfig) StepStatus {
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would allow container egress forwarding"}
	}
	if !cmdExists("ufw") {
		return StepStatus{Done: true, AlreadyOK: true, Detail: "ufw not installed; skipping egress rules"}
	}
	out, err := run(ctx, "ufw", "status")
	if err != nil {
		return StepStatus{Err: fmt.Errorf("ufw status: %w", err)}
	}
	added := 0
	for _, subnet := range containerEgressSubnets {
		if strings.Contains(out, containerEgressComment(subnet)) {
			continue
		}
		if _, err := run(ctx, "ufw", "route", "allow", "from", subnet,
			"comment", containerEgressComment(subnet)); err != nil {
			return StepStatus{Err: fmt.Errorf("ufw route allow from %s: %w", subnet, err)}
		}
		added++
	}
	if added == 0 {
		return StepStatus{Done: true, AlreadyOK: true, Detail: "container egress already allowed"}
	}
	return StepStatus{Done: true, Detail: fmt.Sprintf("container egress allowed (%d subnet rule(s) added)", added)}
}

// ---------------------------------------------------------------------------
// Automatic security updates
// ---------------------------------------------------------------------------

// ensureAutoUpdates installs and enables unattended-upgrades for security-only
// automatic updates. This applies OS security patches without operator
// intervention, a key part of the "stays safe" promise.
func ensureAutoUpdates(ctx context.Context, cfg PassZeroConfig) StepStatus {
	s := checkAutoUpdatesState(ctx)
	if s.Done {
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would enable automatic security updates"}
	}

	// Install packages.
	for _, pkg := range []string{"unattended-upgrades", "apt-listchanges"} {
		if _, err := apt(ctx, pkg, false); err != nil {
			return StepStatus{Err: err}
		}
	}

	// Write the configuration.
	const autoUpgConf = `APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
`
	if _, err := writeFile(ctx, "/etc/apt/apt.conf.d/20auto-upgrades", autoUpgConf, 0o644); err != nil {
		return StepStatus{Err: fmt.Errorf("write auto-upgrades config: %w", err)}
	}

	// Enable the service.
	if _, err := run(ctx, "systemctl", "enable", "--now", "unattended-upgrades"); err != nil {
		return StepStatus{Err: fmt.Errorf("enable unattended-upgrades: %w", err)}
	}
	return StepStatus{Done: true, Detail: "unattended-upgrades enabled (security-only)"}
}

// checkAutoUpdatesState returns whether auto-updates are configured.
func checkAutoUpdatesState(ctx context.Context) StepStatus {
	out, err := run(ctx, "systemctl", "is-enabled", "unattended-upgrades")
	if err != nil || strings.TrimSpace(out) != "enabled" {
		return StepStatus{Done: false, Detail: "unattended-upgrades not enabled"}
	}
	return StepStatus{Done: true, AlreadyOK: true, Detail: "unattended-upgrades enabled"}
}

// ---------------------------------------------------------------------------
// fail2ban
// ---------------------------------------------------------------------------

// ensureFail2ban installs fail2ban with an SSH jail. fail2ban bans IPs that
// fail SSH authentication too many times, mitigating brute-force attacks.
func ensureFail2ban(ctx context.Context, cfg PassZeroConfig) StepStatus {
	s := checkFail2banState(ctx)
	if s.Done {
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would install fail2ban"}
	}

	if _, err := apt(ctx, "fail2ban", false); err != nil {
		return StepStatus{Err: err}
	}

	// Write a local jail configuration that overrides the defaults.
	jailConf := fmt.Sprintf(`[DEFAULT]
bantime  = 3600
findtime = 600
maxretry = 5

[sshd]
enabled  = true
port     = %d
maxretry = 3
`, cfg.SSHPort)

	if _, err := writeFile(ctx, "/etc/fail2ban/jail.d/ownbase-ssh.conf", jailConf, 0o644); err != nil {
		return StepStatus{Err: fmt.Errorf("write fail2ban jail config: %w", err)}
	}

	if _, err := run(ctx, "systemctl", "enable", "--now", "fail2ban"); err != nil {
		return StepStatus{Err: fmt.Errorf("enable fail2ban: %w", err)}
	}
	return StepStatus{Done: true, Detail: "fail2ban enabled with SSH jail"}
}

// checkFail2banState returns whether fail2ban is active.
func checkFail2banState(ctx context.Context) StepStatus {
	out, err := run(ctx, "systemctl", "is-active", "fail2ban")
	if err != nil || strings.TrimSpace(out) != "active" {
		return StepStatus{Done: false, Detail: "fail2ban not active"}
	}
	return StepStatus{Done: true, AlreadyOK: true, Detail: "fail2ban active"}
}

// ---------------------------------------------------------------------------
// Container DNS
// ---------------------------------------------------------------------------

// ensureContainerDNS writes /etc/containers/containers.conf to ensure that
// rootful Podman containers resolve DNS correctly. On some VMs (e.g. Multipass)
// the default gateway IP does not offer DNS service, which breaks `apk add`
// and other package-manager steps in Dockerfile builds.
//
// The file is written only if it doesn't already contain dns_servers.
func ensureContainerDNS(ctx context.Context, cfg PassZeroConfig) StepStatus {
	_ = ctx
	const confPath = "/etc/containers/containers.conf"
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would write " + confPath}
	}
	// Check if already configured.
	existing, _ := os.ReadFile(confPath)
	if strings.Contains(string(existing), "dns_servers") {
		return StepStatus{Done: true, AlreadyOK: true, Detail: "container DNS already configured"}
	}

	if err := os.MkdirAll("/etc/containers", 0o755); err != nil {
		return StepStatus{Err: fmt.Errorf("mkdir /etc/containers: %w", err)}
	}
	const conf = `[containers]
# Use public DNS resolvers. The default gateway IP assigned by many VM
# environments (e.g. Multipass 192.168.252.1) does not forward DNS queries,
# which breaks package-manager steps inside Dockerfile builds.
dns_servers = ["1.1.1.1", "8.8.8.8"]
`
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		return StepStatus{Err: fmt.Errorf("write %s: %w", confPath, err)}
	}
	return StepStatus{Done: true, Detail: "container DNS configured (1.1.1.1, 8.8.8.8)"}
}

// ---------------------------------------------------------------------------
// Unqualified-search registries
// ---------------------------------------------------------------------------

// ensureUnqualifiedSearchRegistries writes a registries.conf.d drop-in so
// that Podman resolves short image names (e.g. "golang:1-alpine", as used
// by nearly every public Dockerfile) when building user services. Ubuntu's
// stock /etc/containers/registries.conf ships with every
// unqualified-search-registries example commented out — unlike Docker,
// Podman treats an unqualified image name as unresolvable, not "assume
// Docker Hub", so without this every `podman build`/`podman pull` of a
// Dockerfile with a short FROM line fails with "short-name ... did not
// resolve to an alias". See docs/troubleshooting.md.
//
// A drop-in file (rather than editing registries.conf directly) is used so
// this survives untouched across podman package upgrades and never fights a
// user's own edits to the main file — per containers-registries.conf.d(5),
// unqualified-search-registries is a simple knob that the drop-in overrides
// outright, so setting it here is safe even if the main file also sets it.
func ensureUnqualifiedSearchRegistries(ctx context.Context, cfg PassZeroConfig) StepStatus {
	_ = ctx
	const confPath = "/etc/containers/registries.conf.d/999-ownbase-unqualified-search.conf"
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would write " + confPath}
	}
	if _, err := os.Stat(confPath); err == nil {
		return StepStatus{Done: true, AlreadyOK: true, Detail: "unqualified-search-registries already configured"}
	}

	if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
		return StepStatus{Err: fmt.Errorf("mkdir %s: %w", filepath.Dir(confPath), err)}
	}
	const conf = `# Written by OwnBase pass zero. Without this, Podman refuses to resolve
# short image names (e.g. "golang:1-alpine") in Dockerfile FROM lines —
# nearly every public Dockerfile uses them — with a
# "short-name ... did not resolve to an alias" build failure.
unqualified-search-registries = ["docker.io"]
`
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		return StepStatus{Err: fmt.Errorf("write %s: %w", confPath, err)}
	}
	return StepStatus{Done: true, Detail: "unqualified-search-registries configured (docker.io)"}
}

// dbPorts are the well-known database ports that must not be publicly
// reachable. OwnBase containers run on loopback-only networks; this check
// verifies that no process is accidentally listening on a public interface.
var dbPorts = []string{"5432", "3306", "27017", "6379"}

// verifyNoExposedDB checks that database ports are not publicly reachable.
// Returns an error (via StepStatus.Err) if any database port is listening
// on a non-loopback interface. Uses secwatch.IsPublicBind for parsing.
func verifyNoExposedDB(ctx context.Context, _ PassZeroConfig) StepStatus {
	// ss -tlnH: listening TCP sockets, no header, no process info (-H avoids
	// the header row, -n avoids resolving ports to names).
	out, err := run(ctx, "ss", "-tlnH")
	if err != nil {
		// ss may not be installed yet; skip the check gracefully.
		return StepStatus{Done: true, AlreadyOK: true,
			Detail: "skipped: ss not available"}
	}

	var exposed []string
	for _, port := range dbPorts {
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			if secwatch.IsPublicBind(fields[3], port) {
				exposed = append(exposed, port)
				break
			}
		}
	}

	if len(exposed) > 0 {
		return StepStatus{
			Done: false,
			Err: fmt.Errorf("database port(s) exposed on public interface: %s — "+
				"containers must listen on loopback only", strings.Join(exposed, ", ")),
		}
	}
	return StepStatus{Done: true, AlreadyOK: true,
		Detail: "no database ports exposed on public interfaces"}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// writeFile writes content to path using os.WriteFile. PassZero runs as root
// (see install.sh systemd unit), so direct writes to /etc/ work correctly.
func writeFile(_ context.Context, path, content string, perm os.FileMode) (string, error) {
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return "", nil
}
