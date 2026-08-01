package main

// create.go implements `ownbasectl create` — the Go-driven replacement for
// testing/smoke-install.sh + `make connect-vm`. One command provisions a
// Base end to end (local Multipass VM by default, or a remote server via
// --remote) and registers it in the vault so every other ownbasectl command
// works immediately afterward.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	ownbase "github.com/ownbase/ownbase"
	"github.com/ownbase/ownbase/internal/tunnel"
	"github.com/ownbase/ownbase/internal/vault"
	"github.com/ownbase/ownbase/internal/vmhost"
)

// isReleaseBuild reports whether this ownbasectl binary was built by the
// release pipeline (version injected via ldflags) for an actual tagged
// release. Release builds install the matching signed daemon release; dev
// builds — including `go build`/`go run` (version == "dev") and local
// `goreleaser release --snapshot` dry runs (version like "1.2.3-dev", per
// the snapshot.version_template in .goreleaser.yaml) — build the daemon from
// the checkout (local VM) or install the latest release (remote), since no
// matching daemon release exists on releases.ownbase.ai for either.
func isReleaseBuild() bool {
	return version != "dev" && !strings.HasSuffix(version, "-dev")
}

// writeEmbeddedInstallScript writes the embedded install.sh to a temp file so
// it can be transferred to the target machine. The returned cleanup func
// removes the file; callers should defer it.
func writeEmbeddedInstallScript() (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "ownbase-install-*.sh")
	if err != nil {
		return "", func() {}, fmt.Errorf("write install script: %w", err)
	}
	cleanup = func() { os.Remove(f.Name()) }
	if _, err := f.Write(ownbase.InstallScript); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write install script: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write install script: %w", err)
	}
	return f.Name(), cleanup, nil
}

// baseTargetFlags are the provisioning flags shared by `create` and
// `restore`: where the Base runs (local VM or remote server) and how to
// reach/size it.
type baseTargetFlags struct {
	remoteHost string
	sshUser    string
	sshKey     string
	sshPort    int
	cpus       int
	memoryGB   int
	diskGB     int
	caddyEmail string
	assumeYes  bool
	// replace allows `create` to repoint an existing Base name at a
	// different machine (--replace).
	replace bool
	// repointOK is set by `restore`, not by a flag: rebuilding a Base onto a
	// fresh machine is supposed to change the host its name points at, so the
	// conflict guard that protects `create` must not fire there.
	repointOK   bool
	wait        bool
	waitTimeout time.Duration
	waitForSSH  time.Duration
	jsonOut     bool
}

// register adds the shared provisioning flags to cmd.
func (f *baseTargetFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.remoteHost, "remote", "", "SSH host of a fresh Ubuntu server (user@host or host; omit for a local Multipass VM)")
	fl.StringVar(&f.sshUser, "ssh-user", "root", "SSH login user for --remote (ignored for a local VM)")
	fl.StringVar(&f.sshKey, "ssh-key", "", "import this existing private key file into the vault as the Base's owner key (default: the key from 'ownbasectl keygen <name>')")
	fl.IntVar(&f.sshPort, "ssh-port", 22, "SSH port for --remote")
	fl.IntVar(&f.cpus, "cpus", 2, "VM CPU count (local VM only)")
	fl.IntVar(&f.memoryGB, "memory", 2, "VM memory in GB (local VM only)")
	fl.IntVar(&f.diskGB, "disk", 15, "VM disk in GB (local VM only)")
	fl.StringVar(&f.caddyEmail, "caddy-email", "", "ACME contact email for automatic TLS on public domains")
	fl.BoolVarP(&f.assumeYes, "yes", "y", false, "skip confirmation prompts (e.g. overwriting an existing local VM)")
	fl.BoolVar(&f.wait, "wait", false, "block until the daemon reports healthy (host hardening finished)")
	fl.DurationVar(&f.waitTimeout, "wait-timeout", 10*time.Minute, "how long --wait blocks before giving up")
	fl.DurationVar(&f.waitForSSH, "wait-for-ssh", 5*time.Minute, "how long to wait for a freshly booted server to accept SSH")
	fl.BoolVar(&f.jsonOut, "json", false, "print the result as JSON instead of a human banner")
}

// provision runs the shared create/restore path for the flag target:
// a remote server when --remote is set, a local Multipass VM otherwise.
func (f *baseTargetFlags) provision(name string, extraEnv map[string]string) error {
	// Resolve once, here, so the key we authenticate with and the key we
	// install into the server's authorized_keys are always the same one.
	ownerKey, err := f.ensureOwnerKey(name)
	if err != nil {
		return err
	}

	if f.remoteHost != "" {
		host, user := splitUserHost(f.remoteHost, f.sshUser)
		return baseCreateRemote(name, host, user, ownerKey, extraEnv, f)
	}
	opts := vmhost.LaunchOptions{CPUs: f.cpus, MemoryGB: f.memoryGB, DiskGB: f.diskGB}
	return baseCreateVM(name, opts, ownerKey, extraEnv, f)
}

// ensureOwnerKey guarantees the vault holds an owner key for this Base before
// anything depends on it, and returns its authorized_keys line — the exact
// public half of the key every later connection will sign with.
//
// Without this, a fresh machine provisions a local VM successfully and then
// cannot be reached: the installer gets no public key to authorize, so every
// later tunnel — including --wait's health poll — fails long after the VM is
// running.
//
// The two paths diverge on who authorizes the key. A local VM is configured by
// us, so a missing key can simply be generated and `create <name>` works on a
// machine that has never used SSH. A cloud server is authorized by the
// provider at boot, from a key pasted in before the machine existed;
// generating one now would produce a key the server has never heard of, so the
// only useful move is to say so before touching anything.
func (f *baseTargetFlags) ensureOwnerKey(name string) (string, error) {
	profile, err := loadProfile(name)
	if err != nil && !isMissingBase(err) {
		return "", err
	}

	// An explicit --ssh-key is a statement of intent: import that key rather
	// than substituting one of ours. Same conflict guard as
	// `keygen --import`: a Base that already has a different key means some
	// machine out there was authorized with it, and overwriting the vault's
	// copy would leave the operator unable to reconnect to it.
	if f.sshKey != "" {
		priv, pub, ierr := readPrivateKeyFile(f.sshKey)
		if ierr != nil {
			return "", withExitCode(exitPreflight, ierr)
		}
		if line := profile.PublicKeyLine(); line != "" && !vault.SameAuthorizedKey(line, pub) {
			return "", withExitCode(exitConflict, fmt.Errorf(
				"Base %q already has a different owner key in the vault; importing would lock you out of the machine that authorized the old one.\n"+
					"       Remove the Base first with 'ownbasectl delete %s --keep-vm' if you really mean to replace its key", name, name))
		}
		profile.PrivateKey, profile.PublicKey = priv, pub
		if perr := putProfile(name, profile); perr != nil {
			return "", perr
		}
		return pub, nil
	}

	if pub := profile.PublicKeyLine(); pub != "" {
		return pub, nil
	}

	if f.remoteHost != "" {
		return "", withExitCode(exitPreflight, fmt.Errorf(
			"no owner key in the vault for Base %q, so there is nothing %s would accept.\n"+
				"       Run 'ownbasectl keygen %s', paste the printed public key into your provider's SSH key field, and rebuild the server so it boots with that key authorized",
			name, f.remoteHost, name))
	}

	priv, pub, err := vault.NewKeyPair("ownbase_" + name)
	if err != nil {
		return "", err
	}
	profile.PrivateKey, profile.PublicKey = priv, pub
	if err := putProfile(name, profile); err != nil {
		return "", err
	}
	progress("==> No owner key for this Base yet; generated one in the vault.")
	return pub, nil
}

func newCreateCmd() *cobra.Command {
	var target baseTargetFlags
	cmd := &cobra.Command{
		Use:   "create <name> [--remote <ssh-host>]",
		Short: "Provision a new Base (local VM or remote server) and register it",
		Long: `Provision a Base end to end and register it in your vault so every other
ownbasectl command works immediately afterward.

With no --remote flag, a fresh local Multipass VM is launched. With
--remote, the installer runs over SSH on a fresh Ubuntu 22.04/24.04
server — run 'ownbasectl keygen <name>' first and paste the printed key
into your provider's SSH key field when you create the machine.

A remote target is checked before anything is changed on it: create waits
for SSH to come up, then verifies sudo, the Ubuntu version, the CPU
architecture, and that the machine is big enough.

create never prompts on the --remote path, so it is safe to run
unattended. Add --wait to block until host hardening has finished, and
--json for machine-readable output.`,
		Example: `  ownbasectl create mybase
  ownbasectl keygen mybase
  ownbasectl create mybase --remote root@203.0.113.10 --wait \
    --caddy-email you@example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return target.provision(args[0], nil)
		},
	}
	target.register(cmd)
	// create-only: restore repoints a Base by design, so it has no equivalent.
	cmd.Flags().BoolVar(&target.replace, "replace", false,
		"allow an existing Base name to be repointed at a different machine")
	return cmd
}

// progress writes a status line to stderr.
//
// Progress is not the command's result. Keeping it off stdout is what lets
// --json emit a single parseable document while a human watching a two-minute
// install still sees what is happening. The result — the banner, or the JSON —
// is the only thing that goes to stdout.
func progress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// splitUserHost splits a --remote value that may be given in ssh-style
// "user@host" form (as shown in README/INSTALL, e.g.
// --remote root@mybase.example.com) into a bare host and login user.
// tunnel.RunCommand/UploadFile dial the host directly via net.JoinHostPort
// and set the SSH user separately, so a "user@host" string must never be
// passed through as the host itself — net.Dial cannot resolve it. If no
// "@" is present, the host is used as-is and fallbackUser (--ssh-user,
// default "root") applies.
func splitUserHost(remote, fallbackUser string) (host, user string) {
	if i := strings.LastIndex(remote, "@"); i != -1 {
		return remote[i+1:], remote[:i]
	}
	return remote, fallbackUser
}

// baseCreateVM provisions a fresh local Multipass VM, installs OwnBase on it,
// and registers the resulting server profile.
//
// Daemon source depends on the build:
//   - release ownbasectl: install.sh downloads the signed daemon release
//     matching ownbasectl's own version (OWNBASE_VERSION) inside the VM.
//   - dev build (go build / go run from a checkout): the daemon is built from
//     the checkout and transferred directly — no release server needed.
//
// extraEnv is merged into the installer's environment on top of the standard
// vars — restore uses it to pass OWNBASE_REBUILD=1 and restic credentials.
func baseCreateVM(name string, opts vmhost.LaunchOptions, ownerPubKey string, extraEnv map[string]string, f *baseTargetFlags) error {
	// OWNBASE_DRIVEN_BY_CTL tells install.sh that the profile registration
	// happens automatically, so its footer skips the manual `adopt` step.
	env := map[string]string{"OWNBASE_DRIVEN_BY_CTL": "1"}

	var repoRoot string
	if isReleaseBuild() {
		env["OWNBASE_VERSION"] = version
	} else {
		// Dev build: the daemon will be built from this checkout below.
		var err error
		repoRoot, err = findRepoRoot()
		if err != nil {
			return fmt.Errorf("this is a dev build of ownbasectl, which installs the daemon by building it from the OwnBase checkout: %w", err)
		}
	}

	ctx := context.Background()
	m := vmhost.New()

	progress("==> Provisioning local VM %q (multipass) ...", name)
	exists, err := m.Exists(ctx, name)
	if err != nil {
		return fmt.Errorf("check for existing VM %q: %w", name, err)
	}
	// Refuse before launching anything if this name already belongs to a
	// machine we are not about to replace — the same orphaning risk the
	// remote path guards against.
	if err := checkLocalVMProfileConflict(name, exists, f.replace || f.repointOK); err != nil {
		return err
	}
	if exists {
		if !confirm(fmt.Sprintf("A local VM named %q already exists and will be DELETED (all its data is lost). Continue?", name), f.assumeYes) {
			return errAborted
		}
		if err := m.Delete(ctx, name); err != nil {
			return fmt.Errorf("clear existing VM %q: %w", name, err)
		}
	}
	if err := m.Launch(ctx, name, opts); err != nil {
		return fmt.Errorf("launch VM %q: %w", name, err)
	}
	progress("    VM launched.")

	if repoRoot != "" {
		progress("==> Building ownbased for the VM (go build -tags=integration) ...")
		binPath, cleanup, err := buildOwnbasedBinary(repoRoot)
		if err != nil {
			return err
		}
		defer cleanup()

		progress("==> Transferring the daemon binary into the VM ...")
		if err := m.Transfer(ctx, binPath, name, "/home/ubuntu/ownbased"); err != nil {
			return fmt.Errorf("transfer ownbased binary: %w", err)
		}
		env["OWNBASE_LOCAL_BINARY"] = "/home/ubuntu/ownbased"
	}

	progress("==> Transferring the installer into the VM ...")
	scriptPath, scriptCleanup, err := writeEmbeddedInstallScript()
	if err != nil {
		return err
	}
	defer scriptCleanup()
	if err := m.Transfer(ctx, scriptPath, name, "/home/ubuntu/install.sh"); err != nil {
		return fmt.Errorf("transfer install.sh: %w", err)
	}

	progress("==> Running the installer inside the VM ...")
	if ownerPubKey != "" {
		env["OWNBASE_OWNER_SSH_KEY"] = ownerPubKey
	}
	if f.caddyEmail != "" {
		env["CADDY_EMAIL"] = f.caddyEmail
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	out, err := m.RunSudoScript(ctx, name, "/home/ubuntu/install.sh", env)
	fmt.Fprintln(os.Stderr, out)
	if err != nil {
		return withExitCode(exitInstall, fmt.Errorf("installer failed: %w", err))
	}

	ip, err := m.IPv4(ctx, name)
	if err != nil {
		return fmt.Errorf("get VM IP address: %w", err)
	}

	progress("==> Reading the API token from the VM ...")
	token, err := waitForVMAPIToken(ctx, m, name, 2*time.Minute)
	if err != nil {
		return err
	}

	if err := registerProfile(name, ip, vault.DefaultSSHUser, 22, token, true); err != nil {
		return err
	}

	return finishCreate(name, ip, vault.DefaultSSHUser, 22, f)
}

// baseCreateRemote installs OwnBase on a fresh remote Ubuntu server over SSH,
// using the standard signed-binary download path: the embedded install.sh is
// uploaded and run, and it downloads + minisign-verifies the daemon release.
// A release ownbasectl pins the daemon to its own version (OWNBASE_VERSION);
// a dev build installs the latest release. extraEnv is merged into the
// installer's environment — restore uses it to pass OWNBASE_REBUILD=1 and
// restic credentials.
func baseCreateRemote(name, host, sshUser, ownerPubKey string, extraEnv map[string]string, f *baseTargetFlags) error {
	sshPort := f.sshPort
	target, err := ownerTarget(name, host, sshUser, sshPort)
	if err != nil {
		return err
	}

	// Refuse before touching the machine, not after: repointing a name at a
	// new server would discard the old one's API token.
	if err := checkProfileConflict(name, host, f.replace || f.repointOK); err != nil {
		return err
	}
	if _, err := preflightRemote(target, f.waitForSSH); err != nil {
		return err
	}

	progress("==> Installing OwnBase on %s ...", target.Destination())
	const remoteScriptPath = "/tmp/ownbase-install.sh"
	if err := tunnel.UploadFile(target, ownbase.InstallScript, remoteScriptPath, 0o755); err != nil {
		return withExitCode(exitInstall, fmt.Errorf("upload install.sh: %w", err))
	}

	env := map[string]string{"OWNBASE_DRIVEN_BY_CTL": "1"}
	if isReleaseBuild() {
		env["OWNBASE_VERSION"] = version
	}
	if ownerPubKey != "" {
		env["OWNBASE_OWNER_SSH_KEY"] = ownerPubKey
	}
	if f.caddyEmail != "" {
		env["CADDY_EMAIL"] = f.caddyEmail
	}
	// The daemon hardens whichever port it is told about: UFW opens it and
	// fail2ban jails it. Without this, a server on a non-standard SSH port
	// gets port 22 hardened and its real port flagged as an unexpected
	// internet-reachable listener.
	if sshPort != 0 && sshPort != 22 {
		env["OWNBASE_SSH_PORT"] = strconv.Itoa(sshPort)
	}
	for k, v := range extraEnv {
		env[k] = v
	}

	// sudo -E: install.sh requires root; -E preserves the env-var prefix
	// (CADDY_EMAIL, OWNBASE_OWNER_SSH_KEY, ...) through the sudo boundary
	// so it works whether sshUser is already root or a sudo-capable user.
	out, err := tunnel.RunCommand(target, envPrefixedCommand(env, "sudo -E bash "+remoteScriptPath))
	fmt.Fprintln(os.Stderr, out)
	if err != nil {
		return withExitCode(exitInstall, fmt.Errorf("installer failed: %w", err))
	}

	progress("==> Reading the API token from the server ...")
	token, err := tunnel.RunCommand(target, "sudo cat /opt/ownbase/api-token")
	if err != nil {
		return withExitCode(exitInstall, fmt.Errorf("read API token: %w", err))
	}
	if strings.TrimSpace(token) == "" {
		return withExitCode(exitInstall, fmt.Errorf("read API token: got an empty token from /opt/ownbase/api-token — installer may not have completed"))
	}

	if err := registerProfile(name, host, sshUser, sshPort, strings.TrimSpace(token), false); err != nil {
		return err
	}

	return finishCreate(name, host, sshUser, sshPort, f)
}

// ownerTarget builds an SSH target for a Base that is not registered yet: the
// host and user come from the command line, the signing key from the vault
// entry `keygen` (or ensureOwnerKey) just wrote.
func ownerTarget(name, host, sshUser string, sshPort int) (tunnel.Target, error) {
	profile, err := loadProfile(name)
	if err != nil && !isMissingBase(err) {
		return tunnel.Target{}, err
	}
	profile.Host = host
	profile.SSHUser = sshUser
	profile.SSHPort = sshPort
	return sshTarget(profile)
}

// finishCreate handles everything after the profile is registered: the
// optional readiness wait, then either the JSON result or the human banner.
//
// Waiting matters more than it looks. install.sh returns as soon as the
// systemd unit is started, but the daemon then runs pass zero — Podman, UFW,
// fail2ban, unattended-upgrades — before it binds the API port. So a create
// that has "succeeded" is still an unhardened machine for another minute or
// two, and the API answering is exactly the signal that pass zero finished.
func finishCreate(name, host, sshUser string, sshPort int, f *baseTargetFlags) error {
	ready := false
	if f.wait {
		progress("==> Waiting for the daemon to finish hardening the host ...")
		if err := waitForDaemonReady(name, f.waitTimeout); err != nil {
			return err
		}
		ready = true
		progress("    Host hardened, daemon healthy.")
	}

	if f.jsonOut {
		return printJSON(map[string]any{
			"base":     name,
			"host":     host,
			"ssh_user": sshUser,
			"ssh_port": sshPort,
			"api_port": vault.DefaultAPIPort,
			"ready":    ready,
		})
	}
	printBaseCreatedBanner(name, host, f.wait)
	return nil
}

// waitForDaemonReady polls the Base's health endpoint through an SSH tunnel
// until it answers or timeout elapses.
func waitForDaemonReady(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		conn, err := connectToServer(name)
		if err == nil {
			_, err = apiGet(conn, "/health")
			conn.close()
			if err == nil {
				return nil
			}
		}
		lastErr = err
		if time.Now().After(deadline) {
			return withExitCode(exitNotReady, fmt.Errorf(
				"Base %q was installed but its daemon did not report healthy within %s — check 'ownbasectl status %s', or the daemon journal (journalctl -u ownbased): %w",
				name, timeout, name, lastErr))
		}
		time.Sleep(5 * time.Second)
	}
}

// buildOwnbasedBinary cross-compiles the daemon for Linux (matching the host
// CPU architecture — Multipass VMs run at the host's native architecture) in
// a fresh temp directory. The returned cleanup func removes that directory;
// callers should defer it.
func buildOwnbasedBinary(repoRoot string) (binPath string, cleanup func(), err error) {
	tmpDir, err := os.MkdirTemp("", "ownbase-build-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp build dir: %w", err)
	}
	cleanup = func() { os.RemoveAll(tmpDir) }

	binPath = filepath.Join(tmpDir, "ownbased")
	cmd := exec.Command("go", "build", "-tags=integration", "-o", binPath, "./cmd/ownbased")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+linuxArchForHost())
	out, buildErr := cmd.CombinedOutput()
	if buildErr != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("go build ./cmd/ownbased: %w\n%s", buildErr, out)
	}
	return binPath, cleanup, nil
}

// linuxArchForHost maps the host's GOARCH to the Linux GOARCH to build for.
// Multipass VMs run at the host's native CPU architecture (arm64 on Apple
// Silicon, amd64 on Intel/AMD), so this is almost always the right choice
// without needing cross-arch emulation.
func linuxArchForHost() string {
	switch runtime.GOARCH {
	case "arm64", "amd64":
		return runtime.GOARCH
	default:
		return "amd64"
	}
}

// waitForVMAPIToken polls the VM for /opt/ownbase/api-token, which install.sh
// writes synchronously before starting the service — this normally succeeds
// on the very first try, but a short retry loop absorbs any VM exec hiccup.
func waitForVMAPIToken(ctx context.Context, m *vmhost.Multipass, name string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		out, err := m.Exec(ctx, name, "sudo", "cat", "/opt/ownbase/api-token")
		if err == nil && strings.TrimSpace(out) != "" {
			return strings.TrimSpace(out), nil
		}
		if err == nil {
			lastErr = fmt.Errorf("token file is empty — installer may not have completed")
		} else {
			lastErr = err
		}
		time.Sleep(3 * time.Second)
	}
	return "", fmt.Errorf("timed out waiting for /opt/ownbase/api-token on VM %q: %w", name, lastErr)
}

// registerProfile saves (or overwrites) a Base's connection details in the
// vault, keeping whatever owner key is already stored for it. localVM marks
// whether this Base is a local Multipass VM (created by `create` with no
// --remote) as opposed to a remote server (--remote) — see Profile.LocalVM.
func registerProfile(name, host, sshUser string, sshPort int, token string, localVM bool) error {
	profile, err := loadProfile(name)
	if err != nil && !isMissingBase(err) {
		return err
	}
	profile.Host = host
	profile.SSHUser = sshUser
	profile.SSHPort = sshPort
	profile.APIPort = vault.DefaultAPIPort
	profile.Token = token
	profile.LocalVM = &localVM

	if err := putProfile(name, profile); err != nil {
		return err
	}
	progress("Registered %q in your vault.", name)
	return nil
}

// printBaseCreatedBanner prints the "what's next" guidance every create run
// ends with, pointing at the next steps in the lifecycle: backup setup and,
// since a freshly created Base has no domain configured yet (Caddy publishes
// no ports and the firewall opens only SSH — see internal/core, internal/install),
// the local HTTPS tunnel as the way to actually reach a service once it
// has a domain.
func printBaseCreatedBanner(name, host string, waited bool) {
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Printf("  Base %q is up at %s\n", name, host)
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println()
	if !waited {
		fmt.Println("  The daemon is still hardening the host in the background — this")
		fmt.Println("  takes a minute or two. Pass --wait next time to block until it's")
		fmt.Println("  done, or watch it with 'ownbasectl status'.")
		fmt.Println()
	}
	fmt.Println("  Next steps:")
	fmt.Printf("    ownbasectl status %s          check it's healthy\n", name)
	fmt.Printf("    ownbasectl backup setup %s    configure remote backups\n", name)
	fmt.Printf("    ownbasectl checkup %s         full security + update + backup report\n", name)
	fmt.Println()
	fmt.Println("  No service has a domain configured yet, so nothing but SSH is exposed.")
	fmt.Println("  Once a service has a domain, reach it locally over trusted HTTPS with:")
	fmt.Printf("    ownbasectl tunnel %s          local HTTPS tunnel (one-time sudo prompt for mkcert)\n", name)
	fmt.Println()
}

// envPrefixedCommand renders `KEY=value ... cmd` with each value single-quoted
// so values containing spaces (e.g. an SSH public key) survive shell parsing.
func envPrefixedCommand(env map[string]string, cmd string) string {
	var b strings.Builder
	for k, v := range env {
		fmt.Fprintf(&b, "%s=%s ", k, shellQuoteEnv(v))
	}
	b.WriteString(cmd)
	return b.String()
}

func shellQuoteEnv(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
