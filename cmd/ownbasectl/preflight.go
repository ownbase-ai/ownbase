package main

// preflight.go checks a remote target before `create`/`restore` change
// anything on it.
//
// Two problems this solves. First, a freshly provisioned cloud server accepts
// SSH somewhere between ten seconds and two minutes after the provider's
// console says "running", so the first connection attempt usually loses a
// race the caller did not know it was in. Second, when the machine is wrong
// for OwnBase — not Ubuntu, no passwordless sudo, too small — the old
// behaviour was to discover that partway through the installer, leaving a
// half-configured box. Everything here runs before a single byte is uploaded.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/install"
	"github.com/ownbase/ownbase/internal/serverconfig"
	"github.com/ownbase/ownbase/internal/tunnel"
)

// Minimum machine size. The binding constraint is not idle memory: every
// service is built from source on the Base (no pre-built application images),
// and a build's transient peak is far larger than the container it produces.
// These are floors, not recommendations — see the sizing table in README.md.
//
// Both sit deliberately below the round numbers the README quotes, because a
// machine sold as "2 GB / 20 GB" never reports those figures: some RAM is
// reserved by firmware and the kernel, and a 20 GB disk loses space to the
// partition table and filesystem metadata. Checking for the advertised number
// would reject exactly the machine the docs told the user to buy.
const (
	minMemoryMB = 1800 // a nominal 2 GB machine reports ~1955 MB
	minDiskGB   = 18   // a nominal 20 GB disk reports ~19 GB usable
)

// preflightResult is what the target told us about itself.
type preflightResult struct {
	UbuntuVersion string
	Arch          string
	MemoryMB      int
	DiskGB        int
}

// preflightRemote waits for SSH to answer, then verifies the target can host a
// Base. Errors are tagged exitPreflight: nothing has been modified, so the
// caller can fix the machine and re-run.
func preflightRemote(host, sshUser, keyPath string, sshPort int, waitForSSH time.Duration) (*preflightResult, error) {
	progress("==> Checking %s@%s ...", sshUser, host)

	if err := waitForSSHReady(host, sshUser, keyPath, sshPort, waitForSSH); err != nil {
		return nil, withExitCode(exitPreflight, err)
	}

	run := func(cmd string) (string, error) {
		return tunnel.RunCommand(host, sshUser, keyPath, cmd, sshPort)
	}

	if _, err := run("sudo -n true"); err != nil {
		return nil, withExitCode(exitPreflight, fmt.Errorf(
			"%s@%s cannot run sudo without a password prompt — log in as root, or give %s passwordless sudo",
			sshUser, host, sshUser))
	}

	res := &preflightResult{}

	osRelease, err := run("cat /etc/os-release")
	if err != nil {
		return nil, withExitCode(exitPreflight, fmt.Errorf("read /etc/os-release on %s: %w", host, err))
	}
	id, version := parseOSRelease(osRelease)
	if id != "ubuntu" {
		return nil, withExitCode(exitPreflight, fmt.Errorf(
			"%s runs %q, but OwnBase requires Ubuntu %s or newer — rebuild the server with an Ubuntu 22.04 or 24.04 image",
			host, id, install.MinUbuntuVersion))
	}
	if !ubuntuVersionAtLeast(version, install.MinUbuntuVersion) {
		return nil, withExitCode(exitPreflight, fmt.Errorf(
			"%s runs Ubuntu %s, but OwnBase requires %s or newer — rebuild the server with an Ubuntu 22.04 or 24.04 image",
			host, version, install.MinUbuntuVersion))
	}
	res.UbuntuVersion = version

	arch, err := run("uname -m")
	if err != nil {
		return nil, withExitCode(exitPreflight, fmt.Errorf("read architecture of %s: %w", host, err))
	}
	arch = strings.TrimSpace(arch)
	switch arch {
	case "x86_64", "aarch64":
		res.Arch = arch
	default:
		return nil, withExitCode(exitPreflight, fmt.Errorf(
			"%s is %s, but OwnBase supports only x86_64 and aarch64", host, arch))
	}

	// Non-fatal beyond this point in the sense that a parse failure is
	// ignored, but a confidently-too-small machine is refused: it would fail
	// later during a container build, which is a far worse place to find out.
	if out, err := run("free -m | awk '/^Mem:/{print $2}'"); err == nil {
		if mb, cerr := strconv.Atoi(strings.TrimSpace(out)); cerr == nil {
			res.MemoryMB = mb
			if mb < minMemoryMB {
				return nil, withExitCode(exitPreflight, fmt.Errorf(
					"%s has %d MB of RAM, below the 2 GB floor — services are built from source on the Base, and a build peaks around 500 MB above idle",
					host, mb))
			}
		}
	}
	if out, err := run("df -BG --output=size / | tail -1"); err == nil {
		if gb, cerr := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(out), "G"))); cerr == nil {
			res.DiskGB = gb
			if gb < minDiskGB {
				return nil, withExitCode(exitPreflight, fmt.Errorf(
					"%s has a %d GB root disk, below the 20 GB floor — a fresh Base uses about 4 GB and each build toolchain caches roughly another gigabyte of layers",
					host, gb))
			}
		}
	}

	summary := fmt.Sprintf("    Ubuntu %s, %s", res.UbuntuVersion, res.Arch)
	if res.MemoryMB > 0 {
		summary += fmt.Sprintf(", %d MB RAM", res.MemoryMB)
	}
	if res.DiskGB > 0 {
		summary += fmt.Sprintf(", %d GB disk", res.DiskGB)
	}
	progress("%s", summary)
	return res, nil
}

// waitForSSHReady polls until the target accepts an SSH session or timeout
// elapses. A just-booted cloud server refuses connections for a while; that is
// expected, not an error worth surfacing until we give up.
func waitForSSHReady(host, sshUser, keyPath string, sshPort int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	announced := false

	for {
		_, err := tunnel.RunCommand(host, sshUser, keyPath, "true", sshPort)
		if err == nil {
			return nil
		}
		lastErr = err

		// An authentication failure will not fix itself by waiting, and the
		// remedy is specific enough to be worth saying outright.
		if isSSHAuthFailure(err) {
			return fmt.Errorf(
				"%s@%s rejected the key %s — make sure this key was pasted into the provider's SSH key field when the server was created: %w",
				sshUser, host, keyPath, err)
		}
		if time.Now().After(deadline) {
			break
		}
		if !announced {
			progress("    waiting for SSH on %s (up to %s) ...", host, timeout)
			announced = true
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("%s@%s did not accept SSH within %s: %w", sshUser, host, timeout, lastErr)
}

// isSSHAuthFailure distinguishes "this key is not authorized" from "the host
// is not up yet". The SSH library reports auth failures as a plain error
// string, so this matches on its stable wording.
func isSSHAuthFailure(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "unable to authenticate") ||
		strings.Contains(s, "no supported methods remain") ||
		strings.Contains(s, "permission denied")
}

// parseOSRelease pulls ID and VERSION_ID out of /etc/os-release contents.
func parseOSRelease(contents string) (id, version string) {
	for _, line := range strings.Split(contents, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		value = strings.Trim(value, `"'`)
		switch key {
		case "ID":
			id = value
		case "VERSION_ID":
			version = value
		}
	}
	return id, version
}

// ubuntuVersionAtLeast compares dotted Ubuntu release numbers ("24.04" >=
// "22.04"). An unparseable version is accepted: refusing to install because we
// could not read a version string would be worse than trying.
func ubuntuVersionAtLeast(got, want string) bool {
	gotParts := strings.Split(got, ".")
	wantParts := strings.Split(want, ".")
	if len(gotParts) < 2 || len(wantParts) < 2 {
		return true
	}
	for i := 0; i < 2; i++ {
		g, gerr := strconv.Atoi(gotParts[i])
		w, werr := strconv.Atoi(wantParts[i])
		if gerr != nil || werr != nil {
			return true
		}
		if g != w {
			return g > w
		}
	}
	return true
}

// checkProfileConflict refuses to repoint an existing Base name at a different
// machine. Overwriting the profile would discard the API token for the old
// server, orphaning it: still running, still billed, no longer reachable with
// ownbasectl. Re-running against the same host is a normal retry and passes.
func checkProfileConflict(name, host string, allowRepoint bool) error {
	if allowRepoint {
		return nil
	}
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("locate config: %w", err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	existing, ok := cfg.Servers[name]
	if !ok || existing.Host == host || existing.Host == "" {
		return nil
	}
	return withExitCode(exitUsage, fmt.Errorf(
		"Base %q already points at %s in ~/.ownbase/config; creating it at %s would discard that server's API token.\n"+
			"       Pick another name, remove the old profile with 'ownbasectl delete %s --keep-vm', or pass --replace",
		name, existing.Host, host, name))
}

// checkLocalVMProfileConflict is the same guard for the local-VM path, where
// the host is not known until after the VM boots, so there is nothing to
// compare against. The question it answers instead: would launching a VM under
// this name discard a profile for a machine that is not this VM?
//
// What settles it is the profile's own local_vm marker, not the presence of a
// VM. A same-named Multipass VM can exist alongside a remote profile purely by
// coincidence — the case ServerProfile.LocalVM's tri-state exists to guard —
// so "a VM is here" is not evidence that this profile describes it.
//
// vmExists breaks the tie for one case only: a legacy profile written before
// local_vm existed, which might be either. Falling back to Multipass there is
// what `delete` does, and it keeps `create` working for local VMs registered
// by older versions. Everything else that is not a known local VM is refused;
// being wrong that way costs one --replace flag, being wrong the other way
// orphans a paid server.
func checkLocalVMProfileConflict(name string, vmExists, allowRepoint bool) error {
	if allowRepoint {
		return nil
	}
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		return fmt.Errorf("locate config: %w", err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	existing, ok := cfg.Servers[name]
	if !ok || existing.KnownLocalVM() || existing.Host == "" {
		return nil
	}
	if !existing.KnownRemote() && vmExists {
		return nil
	}
	return withExitCode(exitUsage, fmt.Errorf(
		"Base %q already points at %s in ~/.ownbase/config; creating a local VM under that name would discard that Base's API token.\n"+
			"       Pick another name, remove the old profile with 'ownbasectl delete %s --keep-vm', or pass --replace",
		name, existing.Host, name))
}
