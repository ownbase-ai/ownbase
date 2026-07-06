package main

// create.go implements `ownbasectl create` — the Go-driven replacement for
// testing/smoke-install.sh + `make connect-vm`. One command provisions a
// Base end to end (local Multipass VM by default, or a remote server via
// --remote) and registers it in ~/.ownbase/config so every other
// ownbasectl command works immediately afterward.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	ownbase "github.com/ownbase/ownbase"
	"github.com/ownbase/ownbase/internal/serverconfig"
	"github.com/ownbase/ownbase/internal/tunnel"
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
	remoteHost    string
	sshUser       string
	sshKey        string
	sshPort       int
	cpus          int
	memoryGB      int
	diskGB        int
	forgejoDomain string
	caddyEmail    string
	noDevTLS      bool
	devDomain     string
	assumeYes     bool
}

// register adds the shared provisioning flags to cmd.
func (f *baseTargetFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.remoteHost, "remote", "", "SSH host of a fresh Ubuntu server (user@host or host; omit for a local Multipass VM)")
	fl.StringVar(&f.sshUser, "ssh-user", "root", "SSH login user for --remote (ignored for a local VM)")
	fl.StringVar(&f.sshKey, "ssh-key", serverconfig.DefaultSSHKey, "path to SSH private key for --remote")
	fl.IntVar(&f.sshPort, "ssh-port", 22, "SSH port for --remote")
	fl.IntVar(&f.cpus, "cpus", 2, "VM CPU count (local VM only)")
	fl.IntVar(&f.memoryGB, "memory", 2, "VM memory in GB (local VM only)")
	fl.IntVar(&f.diskGB, "disk", 15, "VM disk in GB (local VM only)")
	fl.StringVar(&f.forgejoDomain, "forgejo-domain", "", "public domain for the Forgejo UI (optional; implies --no-dev-tls)")
	fl.StringVar(&f.caddyEmail, "caddy-email", "", "ACME contact email for automatic TLS (used with --forgejo-domain; implies --no-dev-tls)")
	fl.BoolVar(&f.noDevTLS, "no-dev-tls", false,
		"disable local HTTPS simulation (mkcert + /etc/hosts); local VMs get dev-TLS by default, remote servers never do")
	fl.StringVar(&f.devDomain, "dev-domain", "", "base domain for dev-TLS (default \"<name>.test\"; local VM only)")
	fl.BoolVarP(&f.assumeYes, "yes", "y", false, "skip confirmation prompts (e.g. overwriting an existing local VM)")
}

// provision runs the shared create/restore path for the flag target:
// a remote server when --remote is set, a local Multipass VM otherwise.
func (f *baseTargetFlags) provision(name string, extraEnv map[string]string) error {
	if f.remoteHost != "" {
		host, user := splitUserHost(f.remoteHost, f.sshUser)
		return baseCreateRemote(name, host, user, f.sshKey, f.sshPort, f.forgejoDomain, f.caddyEmail, extraEnv)
	}
	opts := vmhost.LaunchOptions{CPUs: f.cpus, MemoryGB: f.memoryGB, DiskGB: f.diskGB}
	// dev-TLS is the default for local VMs, but an explicit --forgejo-domain
	// or --caddy-email signals the user wants the production ACME path even
	// on a local VM (e.g. testing real-domain TLS) — --no-dev-tls is not
	// needed in that case, dev-TLS just steps aside.
	devTLS := !f.noDevTLS && f.forgejoDomain == "" && f.caddyEmail == ""
	return baseCreateVM(name, opts, f.forgejoDomain, f.caddyEmail, devTLS, f.devDomain, f.assumeYes, extraEnv)
}

func newCreateCmd() *cobra.Command {
	var target baseTargetFlags
	cmd := &cobra.Command{
		Use:   "create <name> [--remote <ssh-host>]",
		Short: "Provision a new Base (local VM or remote server) and register it",
		Long: `Provision a Base end to end and register it in ~/.ownbase/config so every
other ownbasectl command works immediately afterward.

With no --remote flag, a fresh local Multipass VM is launched, and — unless
--no-dev-tls, --forgejo-domain, or --caddy-email is given — it gets real
HTTPS via a locally-trusted mkcert certificate for *.<name>.test, with
Forgejo at https://forgejo.<name>.test (see 'ownbasectl dev-tls' and
'ownbasectl vm').

With --remote, the installer runs over SSH on a fresh Ubuntu 22.04/24.04
server you already provisioned — dev-TLS never applies there; use
--forgejo-domain + --caddy-email for real ACME/Let's Encrypt TLS.`,
		Example: `  ownbasectl create mybase                          local VM, HTTPS at forgejo.mybase.test
  ownbasectl create mybase --no-dev-tls             local VM, plain HTTP on :3000
  ownbasectl create mybase --remote root@mybase.example.com \
    --forgejo-domain git.yourdomain.com --caddy-email you@example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return target.provision(args[0], nil)
		},
	}
	target.register(cmd)
	return cmd
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
//
// devTLS enables local HTTPS simulation (mkcert + /etc/hosts) for this VM;
// devDomain overrides the default "<name>.test" base domain. Both are
// no-ops when devTLS is false. mkcert being unavailable (or failing) is
// never fatal — this function falls back to plain HTTP and prints a warning,
// so `make smoke-test` and CI (which have no mkcert installed) keep working.
func baseCreateVM(name string, opts vmhost.LaunchOptions, forgejoDomain, caddyEmail string, devTLS bool, devDomain string, assumeYes bool, extraEnv map[string]string) error {
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

	fmt.Printf("==> Provisioning local VM %q (multipass) ...\n", name)
	exists, err := m.Exists(ctx, name)
	if err != nil {
		return fmt.Errorf("check for existing VM %q: %w", name, err)
	}
	if exists {
		if !confirm(fmt.Sprintf("A local VM named %q already exists and will be DELETED (all its data is lost). Continue?", name), assumeYes) {
			return errAborted
		}
		if err := m.Delete(ctx, name); err != nil {
			return fmt.Errorf("clear existing VM %q: %w", name, err)
		}
	}
	if err := m.Launch(ctx, name, opts); err != nil {
		return fmt.Errorf("launch VM %q: %w", name, err)
	}
	fmt.Println("    VM launched.")

	// Clear any /etc/hosts block a previous create for this name may have
	// left behind. The VM (and its IP) are brand new, and this run may not
	// end up enabling dev-TLS at all (--no-dev-tls, mkcert missing/failed
	// below) — without this, a stale block from an earlier dev-TLS create
	// would keep pointing hostnames at the old, now-wrong IP. Idempotent
	// no-op when there was never a block for this name.
	if err := removeHostsBlock(name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not clear a previous /etc/hosts entry for %q (%v)\n", name, err)
	}

	if repoRoot != "" {
		fmt.Println("==> Building ownbased for the VM (go build -tags=integration) ...")
		binPath, cleanup, err := buildOwnbasedBinary(repoRoot)
		if err != nil {
			return err
		}
		defer cleanup()

		fmt.Println("==> Transferring the daemon binary into the VM ...")
		if err := m.Transfer(ctx, binPath, name, "/home/ubuntu/ownbased"); err != nil {
			return fmt.Errorf("transfer ownbased binary: %w", err)
		}
		env["OWNBASE_LOCAL_BINARY"] = "/home/ubuntu/ownbased"
	}

	// dev-TLS: generate a host-trusted mkcert wildcard certificate and
	// transfer it into the VM *before* running install.sh, which stages it
	// to /opt/ownbase/dev-tls and seeds core.caddy.dev_tls: true so Caddy's
	// very first reconcile already serves HTTPS. Any failure here — mkcert
	// missing, -install failing, cert generation failing, transfer failing —
	// falls back to plain HTTP for this run rather than aborting create.
	var devTLSDomain string
	if devTLS {
		domain := devDomain
		if domain == "" {
			domain = name + ".test"
		}
		if !mkcertAvailable() {
			fmt.Fprintf(os.Stderr, "warning: dev-TLS is on by default but %s\n"+
				"         continuing with plain HTTP for %q (pass --no-dev-tls to silence this warning)\n",
				mkcertInstallHint, name)
		} else if err := mkcertEnsureInstalled(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: mkcert -install failed (%v) — continuing with plain HTTP\n", err)
		} else if certPath, keyPath, certCleanup, ok := generateDevTLSCert(domain); ok {
			defer certCleanup()
			fmt.Println("==> Transferring the dev-TLS certificate into the VM ...")
			if _, err := m.Exec(ctx, name, "mkdir", "-p", "/home/ubuntu/dev-tls"); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not create /home/ubuntu/dev-tls in the VM (%v) — continuing with plain HTTP\n", err)
			} else if err := m.Transfer(ctx, certPath, name, "/home/ubuntu/dev-tls/cert.pem"); err != nil {
				fmt.Fprintf(os.Stderr, "warning: transfer dev-TLS certificate failed (%v) — continuing with plain HTTP\n", err)
			} else if err := m.Transfer(ctx, keyPath, name, "/home/ubuntu/dev-tls/key.pem"); err != nil {
				fmt.Fprintf(os.Stderr, "warning: transfer dev-TLS key failed (%v) — continuing with plain HTTP\n", err)
			} else {
				devTLSDomain = domain
				env["OWNBASE_DEV_TLS"] = "1"
				if forgejoDomain == "" {
					forgejoDomain = "forgejo." + domain
				}
			}
		}
	}

	fmt.Println("==> Transferring the installer into the VM ...")
	scriptPath, scriptCleanup, err := writeEmbeddedInstallScript()
	if err != nil {
		return err
	}
	defer scriptCleanup()
	if err := m.Transfer(ctx, scriptPath, name, "/home/ubuntu/install.sh"); err != nil {
		return fmt.Errorf("transfer install.sh: %w", err)
	}

	fmt.Println("==> Running the installer inside the VM ...")
	if key := defaultOwnerSSHKey(); key != "" {
		env["OWNBASE_OWNER_SSH_KEY"] = key
	}
	if forgejoDomain != "" {
		env["FORGEJO_DOMAIN"] = forgejoDomain
	}
	if caddyEmail != "" {
		env["CADDY_EMAIL"] = caddyEmail
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	out, err := m.RunSudoScript(ctx, name, "/home/ubuntu/install.sh", env)
	fmt.Println(out)
	if err != nil {
		return fmt.Errorf("installer failed: %w", err)
	}

	ip, err := m.IPv4(ctx, name)
	if err != nil {
		return fmt.Errorf("get VM IP address: %w", err)
	}

	fmt.Println("==> Reading the API token from the VM ...")
	token, err := waitForVMAPIToken(ctx, m, name, 2*time.Minute)
	if err != nil {
		return err
	}

	if err := registerProfile(name, ip, serverconfig.DefaultSSHUser, serverconfig.DefaultSSHKey, 22, serverconfig.DefaultAPIPort, token, true, devTLSDomain); err != nil {
		return err
	}

	// Seed the initial /etc/hosts entry for Forgejo's dev-TLS hostname (the
	// template ownbase.yaml has no services yet). 'ownbasectl dev-tls sync'
	// picks up any domain: added to a service afterward.
	if devTLSDomain != "" && forgejoDomain != "" {
		if err := writeHostsBlock(name, ip, []string{forgejoDomain}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write /etc/hosts entry for %s (%v) — run 'ownbasectl dev-tls sync %s' manually\n",
				forgejoDomain, err, name)
		}
	}

	printBaseCreatedBanner(name, ip, devTLSDomain, forgejoDomain)
	return nil
}

// generateDevTLSCert generates a wildcard mkcert certificate for domain in a
// fresh temp directory, returning a cleanup func the caller must defer.
// Returns ok=false (after printing a warning) on any failure — callers treat
// that as "fall back to plain HTTP for this run", never as a hard error.
func generateDevTLSCert(domain string) (certPath, keyPath string, cleanup func(), ok bool) {
	dir, err := os.MkdirTemp("", "ownbase-dev-tls-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create temp dir for dev-TLS certificate (%v) — continuing with plain HTTP\n", err)
		return "", "", func() {}, false
	}
	cleanup = func() { os.RemoveAll(dir) }
	certPath, keyPath, err = mkcertGenerateWildcard(domain, dir)
	if err != nil {
		cleanup()
		fmt.Fprintf(os.Stderr, "warning: %v — continuing with plain HTTP\n", err)
		return "", "", func() {}, false
	}
	return certPath, keyPath, cleanup, true
}

// baseCreateRemote installs OwnBase on a fresh remote Ubuntu server over SSH,
// using the standard signed-binary download path: the embedded install.sh is
// uploaded and run, and it downloads + minisign-verifies the daemon release.
// A release ownbasectl pins the daemon to its own version (OWNBASE_VERSION);
// a dev build installs the latest release. extraEnv is merged into the
// installer's environment — restore uses it to pass OWNBASE_REBUILD=1 and
// restic credentials.
func baseCreateRemote(name, host, sshUser, sshKey string, sshPort int, forgejoDomain, caddyEmail string, extraEnv map[string]string) error {
	keyPath := serverconfig.ServerProfile{SSHKey: sshKey}.EffectiveSSHKey()

	fmt.Printf("==> Installing OwnBase on %s@%s ...\n", sshUser, host)
	const remoteScriptPath = "/tmp/ownbase-install.sh"
	if err := tunnel.UploadFile(host, sshUser, keyPath, sshPort, ownbase.InstallScript, remoteScriptPath, 0o755); err != nil {
		return fmt.Errorf("upload install.sh: %w", err)
	}

	env := map[string]string{"OWNBASE_DRIVEN_BY_CTL": "1"}
	if isReleaseBuild() {
		env["OWNBASE_VERSION"] = version
	}
	if key := defaultOwnerSSHKey(); key != "" {
		env["OWNBASE_OWNER_SSH_KEY"] = key
	}
	if forgejoDomain != "" {
		env["FORGEJO_DOMAIN"] = forgejoDomain
	}
	if caddyEmail != "" {
		env["CADDY_EMAIL"] = caddyEmail
	}
	for k, v := range extraEnv {
		env[k] = v
	}

	// sudo -E: install.sh requires root; -E preserves the env-var prefix
	// (FORGEJO_DOMAIN, OWNBASE_OWNER_SSH_KEY, ...) through the sudo boundary
	// so it works whether sshUser is already root or a sudo-capable user.
	out, err := tunnel.RunCommand(host, sshUser, keyPath, envPrefixedCommand(env, "sudo -E bash "+remoteScriptPath), sshPort)
	fmt.Println(out)
	if err != nil {
		return fmt.Errorf("installer failed: %w", err)
	}

	fmt.Println("==> Reading the API token from the server ...")
	token, err := tunnel.RunCommand(host, sshUser, keyPath, "sudo cat /opt/ownbase/api-token", sshPort)
	if err != nil {
		return fmt.Errorf("read API token: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("read API token: got an empty token from /opt/ownbase/api-token — installer may not have completed")
	}

	if err := registerProfile(name, host, sshUser, sshKey, sshPort, serverconfig.DefaultAPIPort, strings.TrimSpace(token), false, ""); err != nil {
		return err
	}

	printBaseCreatedBanner(name, host, "", "")
	return nil
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

// registerProfile saves (or overwrites) a Base profile in ~/.ownbase/config.
// localVM marks whether this Base is a local Multipass VM (created by
// `create` with no --remote) as opposed to a remote server (--remote) — see
// ServerProfile.LocalVM. devTLSDomain is the dev-TLS base domain if this
// create run enabled it (see ServerProfile.DevTLSDomain), or "" otherwise —
// always "" for remote servers.
func registerProfile(name, host, sshUser, sshKey string, sshPort, apiPort int, token string, localVM bool, devTLSDomain string) error {
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("locate config: %w", err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	cfg.Servers[name] = serverconfig.ServerProfile{
		Host:         host,
		SSHUser:      sshUser,
		SSHKey:       sshKey,
		SSHPort:      sshPort,
		APIPort:      apiPort,
		Token:        token,
		LocalVM:      &localVM,
		DevTLSDomain: devTLSDomain,
	}
	if err := serverconfig.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Registered %q in ~/.ownbase/config.\n", name)
	return nil
}

// printBaseCreatedBanner prints the "what's next" guidance every create run
// ends with, pointing at the next step in the lifecycle: backup setup.
// devTLSDomain/forgejoDomain are both "" unless this create run enabled
// dev-TLS, in which case the Forgejo dev-TLS URL and a reminder to sync
// hostnames after adding services are shown up front.
func printBaseCreatedBanner(name, host, devTLSDomain, forgejoDomain string) {
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Printf("  Base %q is up at %s\n", name, host)
	if devTLSDomain != "" && forgejoDomain != "" {
		fmt.Printf("  Forgejo (dev-TLS): https://%s\n", forgejoDomain)
	}
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Printf("    ownbasectl status %s          check it's healthy\n", name)
	if devTLSDomain != "" {
		fmt.Printf("    ownbasectl dev-tls sync %s    after adding a service with a domain:\n", name)
	}
	fmt.Printf("    ownbasectl backup setup %s    configure remote backups\n", name)
	fmt.Printf("    ownbasectl checkup %s         full security + update + backup report\n", name)
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
