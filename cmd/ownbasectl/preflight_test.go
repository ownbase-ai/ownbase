package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/serverconfig"
)

func TestParseOSRelease(t *testing.T) {
	const ubuntu2404 = `PRETTY_NAME="Ubuntu 24.04.1 LTS"
NAME="Ubuntu"
VERSION_ID="24.04"
VERSION="24.04.1 LTS (Noble Numbat)"
ID=ubuntu
ID_LIKE=debian
`
	id, version := parseOSRelease(ubuntu2404)
	if id != "ubuntu" || version != "24.04" {
		t.Errorf("parseOSRelease = (%q, %q), want (ubuntu, 24.04)", id, version)
	}

	id, version = parseOSRelease("ID=debian\nVERSION_ID=\"12\"\n")
	if id != "debian" || version != "12" {
		t.Errorf("parseOSRelease = (%q, %q), want (debian, 12)", id, version)
	}

	if id, _ := parseOSRelease("garbage without equals"); id != "" {
		t.Errorf("expected empty ID for unparseable input, got %q", id)
	}
}

func TestUbuntuVersionAtLeast(t *testing.T) {
	cases := []struct {
		got, want string
		ok        bool
	}{
		{"24.04", "22.04", true},
		{"22.04", "22.04", true},
		{"20.04", "22.04", false},
		{"18.04", "22.04", false},
		{"22.10", "22.04", true},
		{"22.04", "22.10", false},
		{"25.04", "22.04", true},
		// Unparseable versions are accepted rather than blocking an install
		// over a version string we could not read.
		{"", "22.04", true},
		{"noble", "22.04", true},
	}
	for _, c := range cases {
		if got := ubuntuVersionAtLeast(c.got, c.want); got != c.ok {
			t.Errorf("ubuntuVersionAtLeast(%q, %q) = %v, want %v", c.got, c.want, got, c.ok)
		}
	}
}

func TestIsSSHAuthFailure(t *testing.T) {
	authErrs := []string{
		"ssh dial: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey]",
		"ssh: no supported methods remain",
		"Permission denied (publickey)",
	}
	for _, msg := range authErrs {
		if !isSSHAuthFailure(errors.New(msg)) {
			t.Errorf("expected auth failure for %q", msg)
		}
	}

	transientErrs := []string{
		"ssh dial: dial tcp 203.0.113.9:22: connect: connection refused",
		"ssh dial: dial tcp 203.0.113.9:22: i/o timeout",
	}
	for _, msg := range transientErrs {
		if isSSHAuthFailure(errors.New(msg)) {
			t.Errorf("%q is transient and must not be treated as an auth failure", msg)
		}
	}
}

func TestCheckProfileConflict(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// No profile yet: nothing to conflict with.
	if err := checkProfileConflict("mybase", "203.0.113.10", false); err != nil {
		t.Fatalf("unexpected conflict for a new name: %v", err)
	}

	if err := registerProfile("mybase", "203.0.113.10", "root", "~/.ssh/ownbase_mybase", 22, 7070, "tok", false); err != nil {
		t.Fatal(err)
	}

	// Same host is a retry, which must keep working.
	if err := checkProfileConflict("mybase", "203.0.113.10", false); err != nil {
		t.Errorf("re-running create against the same host must be allowed: %v", err)
	}

	// A different host would orphan the original server.
	err := checkProfileConflict("mybase", "198.51.100.5", false)
	if err == nil {
		t.Fatal("expected a conflict when repointing a Base name at a different host")
	}
	if !strings.Contains(err.Error(), "203.0.113.10") {
		t.Errorf("error should name the existing host, got: %v", err)
	}
	if code := exitCodeFor(err); code != exitUsage {
		t.Errorf("conflict exit code = %d, want %d", code, exitUsage)
	}

	// ...unless the caller opted in (create --replace, or restore).
	if err := checkProfileConflict("mybase", "198.51.100.5", true); err != nil {
		t.Errorf("allowRepoint should permit the change: %v", err)
	}
}

func TestCheckLocalVMProfileConflict(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// No profile yet: nothing to conflict with.
	if err := checkLocalVMProfileConflict("mybase", false, false); err != nil {
		t.Fatalf("unexpected conflict for a new name: %v", err)
	}

	// A remote Base under the same name: launching a VM here would discard
	// the token for a server that stays running and billed.
	if err := registerProfile("mybase", "203.0.113.10", "root", "~/.ssh/ownbase_mybase", 22, 7070, "tok", false); err != nil {
		t.Fatal(err)
	}
	err := checkLocalVMProfileConflict("mybase", false, false)
	if err == nil {
		t.Fatal("expected a conflict when a local VM would replace a remote Base")
	}
	if !strings.Contains(err.Error(), "203.0.113.10") {
		t.Errorf("error should name the existing host, got: %v", err)
	}
	if code := exitCodeFor(err); code != exitUsage {
		t.Errorf("conflict exit code = %d, want %d", code, exitUsage)
	}

	// A same-named Multipass VM can exist next to a remote profile by
	// coincidence, so it is not evidence that the profile describes it.
	if err := checkLocalVMProfileConflict("mybase", true, false); err == nil {
		t.Error("a coincidentally same-named VM must not bypass the guard on a remote profile")
	}

	// Opting in (create --replace, or restore) proceeds.
	if err := checkLocalVMProfileConflict("mybase", false, true); err != nil {
		t.Errorf("allowRepoint should permit the change: %v", err)
	}

	// Re-creating a VM whose profile says local is a normal retry, whether
	// or not the VM is still around.
	if err := registerProfile("vmbase", "192.168.64.5", "ubuntu", "~/.ssh/id_ed25519", 22, 7070, "tok", true); err != nil {
		t.Fatal(err)
	}
	for _, vmExists := range []bool{false, true} {
		if err := checkLocalVMProfileConflict("vmbase", vmExists, false); err != nil {
			t.Errorf("re-creating a known local VM (vmExists=%v) must be allowed: %v", vmExists, err)
		}
	}

	// A legacy profile predating local_vm could be either, so Multipass
	// breaks the tie — the same fallback `delete` uses.
	writeLegacyProfile(t, "legacy", "192.168.64.9")
	if err := checkLocalVMProfileConflict("legacy", true, false); err != nil {
		t.Errorf("a legacy profile with its VM present must still be re-creatable: %v", err)
	}
	if err := checkLocalVMProfileConflict("legacy", false, false); err == nil {
		t.Error("a legacy profile with no VM to vouch for it must be refused")
	}
}

// writeLegacyProfile registers a profile with local_vm unset, as versions
// before that field existed wrote them. registerProfile cannot produce one.
func writeLegacyProfile(t *testing.T, name, host string) {
	t.Helper()
	cfgPath, err := serverconfig.DefaultConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := serverconfig.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]serverconfig.ServerProfile{}
	}
	cfg.Servers[name] = serverconfig.ServerProfile{Host: host, Token: "tok"}
	if err := serverconfig.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureOwnerKey(t *testing.T) {
	// A local VM on a machine with no SSH key at all: generate one, so the
	// installer has a public key to authorize and later tunnels work.
	t.Run("local VM generates a missing key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		f := &baseTargetFlags{}
		key, err := f.ensureOwnerKey("mybase")
		if err != nil {
			t.Fatalf("local create must not require a pre-existing key: %v", err)
		}
		if key != filepath.Join("~", ".ssh", "ownbase_mybase") {
			t.Errorf("key path = %q, want the per-Base keygen path", key)
		}
		if !fileExists(expandKeyPath(key)) {
			t.Error("resolved key does not exist on disk")
		}
		// The installer authorizes whatever this returns; empty means the
		// VM boots with no way in.
		if ownerPublicKey(key) == "" {
			t.Error("generated key yields no authorized_keys line")
		}
	})

	// Re-running must reuse the key, or the second create locks us out of
	// the machine the first one authorized.
	t.Run("existing key is reused", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		f := &baseTargetFlags{}
		first, err := f.ensureOwnerKey("mybase")
		if err != nil {
			t.Fatal(err)
		}
		want := ownerPublicKey(first)
		if _, err := f.ensureOwnerKey("mybase"); err != nil {
			t.Fatal(err)
		}
		if got := ownerPublicKey(first); got != want {
			t.Error("re-running regenerated the key instead of reusing it")
		}
	})

	// A remote key must be authorized by the provider before the server
	// boots, so generating one here would be useless.
	t.Run("remote refuses with a keygen pointer", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		f := &baseTargetFlags{remoteHost: "root@203.0.113.10"}
		_, err := f.ensureOwnerKey("mybase")
		if err == nil {
			t.Fatal("expected a refusal when no key exists for a remote target")
		}
		if !strings.Contains(err.Error(), "keygen mybase") {
			t.Errorf("error should point at keygen, got: %v", err)
		}
		if code := exitCodeFor(err); code != exitPreflight {
			t.Errorf("exit code = %d, want %d (nothing was changed)", code, exitPreflight)
		}
	})

	// An explicit --ssh-key that does not exist is a typo, not an invitation
	// to substitute a different key.
	t.Run("explicit missing key is an error", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		f := &baseTargetFlags{sshKey: "/nonexistent/key"}
		if _, err := f.ensureOwnerKey("mybase"); err == nil {
			t.Fatal("expected a refusal for a missing --ssh-key")
		}
	})
}

func TestExitCodeFor(t *testing.T) {
	if got := exitCodeFor(errors.New("plain")); got != exitError {
		t.Errorf("untagged error = %d, want %d", got, exitError)
	}
	if got := exitCodeFor(withExitCode(exitPreflight, errors.New("nope"))); got != exitPreflight {
		t.Errorf("tagged error = %d, want %d", got, exitPreflight)
	}
	// Tags must survive wrapping, since callers add context with %w.
	wrapped := withExitCode(exitInstall, errors.New("inner"))
	if got := exitCodeFor(errWrap("outer: %w", wrapped)); got != exitInstall {
		t.Errorf("wrapped tagged error = %d, want %d", got, exitInstall)
	}
	if withExitCode(exitUsage, nil) != nil {
		t.Error("withExitCode(nil) must stay nil")
	}
}

func errWrap(format string, err error) error {
	return &wrapErr{msg: format, err: err}
}

type wrapErr struct {
	msg string
	err error
}

func (w *wrapErr) Error() string { return w.msg + ": " + w.err.Error() }
func (w *wrapErr) Unwrap() error { return w.err }
