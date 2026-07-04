package install

// harden.go implements the host hardening steps that run as part of PassZero:
//
//   - UFW firewall (deny all inbound; allow SSH, HTTP, HTTPS)
//   - Automatic security updates via unattended-upgrades
//   - fail2ban for SSH brute-force protection
//   - Git SSH multiplexing (AuthorizedKeysCommand shims + sshd drop-in)
//   - Verification that database ports (5432, 3306) are not publicly reachable
//
// Every function is idempotent: it checks the current state first and skips
// the action if the condition is already satisfied. This is what makes
// PassZero resumable — restart after a failure, and only the incomplete
// steps run.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ownbase/ownbase/internal/secwatch"
)

// ---------------------------------------------------------------------------
// Firewall (UFW)
// ---------------------------------------------------------------------------

// ensureFirewall configures UFW to:
//   - Deny all inbound by default.
//   - Allow SSH (configurable port), HTTP (80), HTTPS (443).
//   - Enable UFW if not already enabled.
//
// Outbound is unrestricted (containers need to pull images, DNS, etc.).
func ensureFirewall(ctx context.Context, cfg PassZeroConfig) StepStatus {
	s := checkFirewallState(ctx)
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
		{"ufw", "allow", "80/tcp", "comment", "HTTP"},
		{"ufw", "allow", "443/tcp", "comment", "HTTPS"},
		// Allow forwarding of packets that netavark has DNAT'd to container IPs.
		// UFW's default deny-routed policy otherwise drops them after DNAT.
		// Scoped to specific ports rather than the whole subnet so a container
		// that accidentally publishes on 0.0.0.0 doesn't become externally
		// reachable without an explicit UFW INPUT rule.
		{"ufw", "route", "allow", "proto", "tcp", "from", "any", "to", "10.88.0.0/12", "port", "80", "comment", "Caddy HTTP forwarding"},
		{"ufw", "route", "allow", "proto", "tcp", "from", "any", "to", "10.88.0.0/12", "port", "443", "comment", "Caddy HTTPS forwarding"},
		// Allow containers to reach the aardvark-dns server (bound to Podman
		// bridge gateway IPs). Podman uses 10.88.0.0/12 and 10.89.0.0/16 for
		// container subnets; their DNS queries target the gateway IPs on UDP/53.
		// Without these rules UFW's INPUT DROP policy silently discards the
		// queries, breaking inter-container hostname resolution.
		{"ufw", "allow", "proto", "udp", "from", "10.88.0.0/12", "to", "10.88.0.0/12", "port", "53", "comment", "Podman DNS (10.88.0.0/12)"},
		{"ufw", "allow", "proto", "udp", "from", "10.89.0.0/16", "to", "10.89.0.0/16", "port", "53", "comment", "Podman DNS (10.89.0.0/16)"},
	}
	// When ForgejoPort > 0, Forgejo is accessed directly (no domain configured).
	// Open the port and allow container forwarding for it.
	// When ForgejoPort == 0, a domain is configured and Caddy proxies Forgejo on
	// 443 — the direct port must NOT be opened so external traffic is TLS-only.
	if cfg.ForgejoPort > 0 {
		cmds = append(cmds,
			[]string{"ufw", "allow", fmt.Sprintf("%d/tcp", cfg.ForgejoPort), "comment", "Forgejo"},
			[]string{"ufw", "route", "allow", "proto", "tcp", "from", "any", "to", "10.88.0.0/12", "port", fmt.Sprintf("%d", cfg.ForgejoPort), "comment", "Forgejo forwarding"},
		)
	}
	cmds = append(cmds, []string{"ufw", "--force", "enable"})
	for _, args := range cmds {
		if _, err := run(ctx, args[0], args[1:]...); err != nil {
			return StepStatus{Err: fmt.Errorf("ufw %s: %w", args[1], err)}
		}
	}
	return StepStatus{Done: true, Detail: fmt.Sprintf("UFW enabled: SSH(%d), 80, 443", cfg.SSHPort)}
}

// checkFirewallState returns whether UFW is active without making changes.
func checkFirewallState(ctx context.Context) StepStatus {
	if !cmdExists("ufw") {
		return StepStatus{Done: false, Detail: "ufw not installed"}
	}
	out, err := run(ctx, "ufw", "status")
	if err != nil {
		return StepStatus{Done: false, Detail: "ufw status failed: " + err.Error()}
	}
	if strings.Contains(out, "Status: active") {
		return StepStatus{Done: true, AlreadyOK: true, Detail: "UFW active"}
	}
	return StepStatus{Done: false, Detail: "UFW installed but not active"}
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
// Git SSH multiplexing (AuthorizedKeysCommand)
// ---------------------------------------------------------------------------

// GitSSH shim paths — written by ensureGitSSH and referenced by the sshd
// drop-in. Defined as package-level constants so Tier-1 tests can assert
// expected content without magic strings.
const (
	gitSSHKeysScript = "/usr/local/bin/forgejo-keys"
	gitSSHServScript = "/usr/local/bin/forgejo-serv"
	gitSSHSshdConf   = "/etc/ssh/sshd_config.d/50-ownbase-git.conf"
	gitSSHSudoers    = "/etc/sudoers.d/ownbase-git-ssh"
)

// forgejoKeysContent is the key-lookup shim. sshd calls it as:
//
//	AuthorizedKeysCommand /usr/local/bin/forgejo-keys %u %t %k
//
// %u = SSH username ("git"), %t = key type (e.g. ssh-ed25519), %k = base64 key.
// It asks the Forgejo container for the authorized_keys line, then rewrites the
// embedded command= to use our host-side forgejo-serv wrapper (since
// "forgejo serv" only exists inside the container).
//
// The shim runs as the "git" system user (AuthorizedKeysCommandUser git).
// rootful Podman requires root to exec into containers, so we use a targeted
// sudoers rule (gitSSHSudoers) granting git NOPASSWD access to exactly these
// two podman exec calls.
const forgejoKeysContent = `#!/bin/bash
# OwnBase git SSH key lookup — do not edit; managed by ownbased.
# sshd calls this as: forgejo-keys <ssh-user> <key-type> <base64-key>
#
# Forgejo outputs: command="<binary> [flags] serv key-N",...
# We rewrite the command= to point at our host-side forgejo-serv wrapper,
# preserving the key-N token that Forgejo uses to identify the session.
sudo -n podman exec --user git ownbase-core-forgejo \
  forgejo keys --username "$1" --type "$2" --content "$3" \
  | sed 's|command="[^"]*serv \(key-[^"]*\)"|command="/usr/local/bin/forgejo-serv \1"|'
`

// forgejoServContent is the git-session proxy. The sshd command= rewritten by
// forgejo-keys points here; it proxies the git wire protocol into the container
// via `podman exec -i` (interactive stdin required for the git protocol).
// --user git ensures Forgejo runs as its expected user, not root.
// -e SSH_ORIGINAL_COMMAND passes the git operation (git-upload-pack etc.) into
// the container; the sudoers env_keep preserves this var through sudo.
const forgejoServContent = `#!/bin/bash
# OwnBase git SSH session proxy — do not edit; managed by ownbased.
exec sudo -n podman exec -i -e SSH_ORIGINAL_COMMAND --user git ownbase-core-forgejo forgejo serv "$@"
`

// gitSSHSudoersContent grants the "git" system user passwordless access to
// exactly two podman exec calls — the key-lookup and the git session proxy.
// The commands are pinned to --user git and the specific container, so this
// cannot be used to exec into other containers or as other users.
//
// env_keep preserves SSH_ORIGINAL_COMMAND through sudo so forgejo-serv can
// pass it into the container via podman exec -e SSH_ORIGINAL_COMMAND.
// Without it, forgejo serv has no way to know which git operation was requested.
const gitSSHSudoersContent = `# OwnBase git SSH — do not edit; managed by ownbased.
# Allows the git system user to exec into the Forgejo container for key
# lookup and git session proxying without a password prompt.
Defaults:git env_keep += "SSH_ORIGINAL_COMMAND"
git ALL=(root) NOPASSWD: /usr/bin/podman exec --user git ownbase-core-forgejo forgejo keys *
git ALL=(root) NOPASSWD: /usr/bin/podman exec -i -e SSH_ORIGINAL_COMMAND --user git ownbase-core-forgejo forgejo serv *
`

// gitSSHSshdConfContent configures sshd to use the key-lookup shim for the
// synthetic "git" user. AuthorizedKeysCommandUser must be an unprivileged
// system account (we use the "git" account created by ensureGitSSH).
// Tokens: %u=username(git), %t=key-type, %k=base64-key — matches forgejo keys flags.
const gitSSHSshdConfContent = `# OwnBase git SSH multiplexing — do not edit; managed by ownbased.
# Allows git@<host>:owner/repo.git to work on port 22, sharing sshd with
# admin SSH, without giving the git account a real shell or authorized_keys.
Match User git
    AuthorizedKeysCommand /usr/local/bin/forgejo-keys %u %t %k
    AuthorizedKeysCommandUser git
    PasswordAuthentication no
`

// ensureGitSSH sets up AuthorizedKeysCommand SSH multiplexing so that
// git@<host>:owner/repo.git works on port 22 alongside admin SSH.
//
// Steps (all idempotent):
//  1. Create the "git" system user (no shell, no home).
//  2. Write /usr/local/bin/forgejo-keys (key-lookup shim).
//  3. Write /usr/local/bin/forgejo-serv (git-session proxy).
//  4. Write /etc/ssh/sshd_config.d/50-ownbase-git.conf.
//  5. Validate the sshd config and reload sshd.
func ensureGitSSH(ctx context.Context, cfg PassZeroConfig) StepStatus {
	if s := checkGitSSHState(ctx); s.Done {
		// Even when already configured, ensure the privilege separation
		// directory exists — it lives in /run which is cleared on every reboot.
		// sshd -t (and sshd itself) require it at startup. Best-effort: if the
		// mkdir fails, sshd will report the real error when it starts.
		if !cfg.DryRun {
			_ = os.MkdirAll("/run/sshd", 0o755)
		}
		return s
	}
	if cfg.DryRun {
		return StepStatus{Done: false, Detail: "would configure git SSH multiplexing"}
	}

	// 1. Create git system user (idempotent — useradd exits 9 if user exists).
	//
	// Shell must be /bin/bash (not nologin): sshd executes forced commands as
	// "$SHELL -c <command>"; nologin prints "This account is currently not
	// available." and exits, which corrupts the git wire protocol.
	// Interactive login is still blocked: all keys go through AuthorizedKeysCommand
	// (Forgejo DB lookup only) and every matched key has command= forced.
	//
	// Home dir /var/lib/git with --create-home lets sshd chdir successfully;
	// a missing home dir also leaks an error message into the git protocol.
	if _, err := run(ctx, "id", "git"); err != nil {
		if _, err := run(ctx, "useradd",
			"--system",
			"--create-home",
			"--home-dir", "/var/lib/git",
			"--shell", "/bin/bash",
			"git",
		); err != nil {
			return StepStatus{Err: fmt.Errorf("create git user: %w", err)}
		}
	}

	// 2 & 3. Write shim scripts.
	for _, f := range []struct {
		path    string
		content string
	}{
		{gitSSHKeysScript, forgejoKeysContent},
		{gitSSHServScript, forgejoServContent},
	} {
		if err := writeExecutable(f.path, f.content); err != nil {
			return StepStatus{Err: err}
		}
	}

	// 4. Write targeted sudoers rule so the git user can exec into the container.
	if err := os.MkdirAll("/etc/sudoers.d", 0o750); err != nil {
		return StepStatus{Err: fmt.Errorf("mkdir sudoers.d: %w", err)}
	}
	// sudoers files must be mode 0440 (readable only by root).
	existing, _ := os.ReadFile(gitSSHSudoers)
	if !bytes.Equal(existing, []byte(gitSSHSudoersContent)) {
		if err := os.WriteFile(gitSSHSudoers, []byte(gitSSHSudoersContent), 0o440); err != nil {
			return StepStatus{Err: fmt.Errorf("write %s: %w", gitSSHSudoers, err)}
		}
	}
	// Validate the sudoers file.
	if out, err := run(ctx, "visudo", "-c", "-f", gitSSHSudoers); err != nil {
		return StepStatus{Err: fmt.Errorf("sudoers file invalid: %w\n%s", err, out)}
	}

	// 5. Write sshd drop-in.
	if err := os.MkdirAll("/etc/ssh/sshd_config.d", 0o755); err != nil {
		return StepStatus{Err: fmt.Errorf("mkdir sshd_config.d: %w", err)}
	}
	if err := os.WriteFile(gitSSHSshdConf, []byte(gitSSHSshdConfContent), 0o644); err != nil {
		return StepStatus{Err: fmt.Errorf("write %s: %w", gitSSHSshdConf, err)}
	}

	// 6. Validate sshd config then reload.
	// Ensure the privilege separation directory exists before sshd -t.
	// In production sshd creates this on startup; on a fresh install or in
	// CI it may not yet exist.
	_ = os.MkdirAll("/run/sshd", 0o755) // best-effort; sshd -t reports the real error if it fails
	if out, err := run(ctx, "sshd", "-t"); err != nil {
		return StepStatus{Err: fmt.Errorf("sshd config invalid: %w\n%s", err, out)}
	}
	// Reload only when sshd is already active. On a fresh install the daemon
	// will read the updated config on first start, so skipping the reload is
	// safe. This also prevents failures in CI environments where no sshd
	// service unit is running.
	sshdActive := false
	for _, svc := range []string{"ssh", "sshd"} {
		if out, _ := run(ctx, "systemctl", "is-active", svc); strings.TrimSpace(out) == "active" {
			sshdActive = true
			break
		}
	}
	if sshdActive {
		if _, err := run(ctx, "systemctl", "reload", "ssh"); err != nil {
			// "ssh" is the service name on Ubuntu; try "sshd" as fallback.
			if _, err2 := run(ctx, "systemctl", "reload", "sshd"); err2 != nil {
				return StepStatus{Err: fmt.Errorf("reload sshd: %w", err)}
			}
		}
	}

	return StepStatus{Done: true, Detail: "git SSH multiplexing configured (AuthorizedKeysCommand)"}
}

// checkGitSSHState reports whether the sshd drop-in, shims, and sudoers rule
// are in place without making changes. Done=true when all four files exist with
// the expected content.
func checkGitSSHState(_ context.Context) StepStatus {
	for _, f := range []struct {
		path    string
		content string
	}{
		{gitSSHKeysScript, forgejoKeysContent},
		{gitSSHServScript, forgejoServContent},
		{gitSSHSudoers, gitSSHSudoersContent},
		{gitSSHSshdConf, gitSSHSshdConfContent},
	} {
		data, err := os.ReadFile(f.path)
		if err != nil || !bytes.Equal(data, []byte(f.content)) {
			return StepStatus{Done: false, Detail: f.path + " absent or stale"}
		}
	}
	return StepStatus{Done: true, AlreadyOK: true, Detail: "git SSH multiplexing already configured"}
}

// writeExecutable writes content to path with mode 0755 (owner read/write/exec,
// group and others read/exec). Idempotent: skips the write when existing
// content matches.
func writeExecutable(path, content string) error {
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, []byte(content)) {
		return nil // already correct
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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
