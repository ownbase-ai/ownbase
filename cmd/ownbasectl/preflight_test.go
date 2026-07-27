package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/vault"
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
	startTestAgent(t)

	// No profile yet: nothing to conflict with.
	if err := checkProfileConflict("mybase", "203.0.113.10", false); err != nil {
		t.Fatalf("unexpected conflict for a new name: %v", err)
	}

	// A Base that has only an owner key from keygen is not a conflict either:
	// there is no machine yet to orphan.
	if err := runKeygen("mybase", "", true); err != nil {
		t.Fatal(err)
	}
	if err := checkProfileConflict("mybase", "203.0.113.10", false); err != nil {
		t.Fatalf("a keygen-only Base must not conflict: %v", err)
	}

	if err := registerProfile("mybase", "203.0.113.10", "root", 22, "tok", false); err != nil {
		t.Fatal(err)
	}

	// Same host is a retry, which must keep working — including when it is
	// spelled differently than the profile records it.
	for _, host := range []string{"203.0.113.10", " 203.0.113.10 "} {
		if err := checkProfileConflict("mybase", host, false); err != nil {
			t.Errorf("re-running create against the same host (%q) must be allowed: %v", host, err)
		}
	}

	// A different host would orphan the original server.
	err := checkProfileConflict("mybase", "198.51.100.5", false)
	if err == nil {
		t.Fatal("expected a conflict when repointing a Base name at a different host")
	}
	if !strings.Contains(err.Error(), "203.0.113.10") {
		t.Errorf("error should name the existing host, got: %v", err)
	}
	// A refusal, not a malformed command: a caller that maps exitUsage to
	// "fix the flags and retry" must not see this.
	if code := exitCodeFor(err); code != exitConflict {
		t.Errorf("conflict exit code = %d, want %d", code, exitConflict)
	}

	// ...unless the caller opted in (create --replace, or restore).
	if err := checkProfileConflict("mybase", "198.51.100.5", true); err != nil {
		t.Errorf("allowRepoint should permit the change: %v", err)
	}
}

func TestSameHost(t *testing.T) {
	same := [][2]string{
		{"203.0.113.10", "203.0.113.10"},
		{"Example.COM", "example.com"},
		{"example.com.", "example.com"},
		{" example.com ", "example.com"},
		{"[2001:db8::1]", "2001:db8::1"},
		{"2001:DB8::0001", "2001:db8::1"},
	}
	for _, c := range same {
		if !sameHost(c[0], c[1]) {
			t.Errorf("sameHost(%q, %q) = false, want true", c[0], c[1])
		}
	}

	// A hostname and an address are not assumed to be the same machine:
	// see the note on sameHost about why DNS is not consulted.
	differ := [][2]string{
		{"203.0.113.10", "203.0.113.20"},
		{"example.com", "example.org"},
		{"example.com", "203.0.113.10"},
		{"2001:db8::1", "2001:db8::2"},
	}
	for _, c := range differ {
		if sameHost(c[0], c[1]) {
			t.Errorf("sameHost(%q, %q) = true, want false", c[0], c[1])
		}
	}
}

func TestCheckLocalVMProfileConflict(t *testing.T) {
	startTestAgent(t)

	// No profile yet: nothing to conflict with.
	if err := checkLocalVMProfileConflict("mybase", false, false); err != nil {
		t.Fatalf("unexpected conflict for a new name: %v", err)
	}

	// A remote Base under the same name: launching a VM here would discard
	// the token for a server that stays running and billed.
	if err := registerProfile("mybase", "203.0.113.10", "root", 22, "tok", false); err != nil {
		t.Fatal(err)
	}
	err := checkLocalVMProfileConflict("mybase", false, false)
	if err == nil {
		t.Fatal("expected a conflict when a local VM would replace a remote Base")
	}
	if !strings.Contains(err.Error(), "203.0.113.10") {
		t.Errorf("error should name the existing host, got: %v", err)
	}
	if code := exitCodeFor(err); code != exitConflict {
		t.Errorf("conflict exit code = %d, want %d", code, exitConflict)
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
	if err := registerProfile("vmbase", "192.168.64.5", "ubuntu", 22, "tok", true); err != nil {
		t.Fatal(err)
	}
	for _, vmExists := range []bool{false, true} {
		if err := checkLocalVMProfileConflict("vmbase", vmExists, false); err != nil {
			t.Errorf("re-creating a known local VM (vmExists=%v) must be allowed: %v", vmExists, err)
		}
	}

	// A profile whose kind is unknown (local_vm unset) could be either, so
	// Multipass breaks the tie — the same fallback `delete` uses.
	putTestProfile(t, "unknown", vault.Profile{Host: "192.168.64.9", Token: "tok"})
	if err := checkLocalVMProfileConflict("unknown", true, false); err != nil {
		t.Errorf("a profile of unknown kind with its VM present must still be re-creatable: %v", err)
	}
	if err := checkLocalVMProfileConflict("unknown", false, false); err == nil {
		t.Error("a profile of unknown kind with no VM to vouch for it must be refused")
	}
}

func TestEnsureOwnerKey(t *testing.T) {
	// A local VM on a machine with no key for this Base: generate one, so the
	// installer has a public key to authorize and later tunnels work.
	t.Run("local VM generates a missing key", func(t *testing.T) {
		startTestAgent(t)
		f := &baseTargetFlags{}
		pub, err := f.ensureOwnerKey("mybase")
		if err != nil {
			t.Fatalf("local create must not require a pre-existing key: %v", err)
		}
		// The installer authorizes whatever this returns; empty means the
		// VM boots with no way in.
		if pub == "" {
			t.Fatal("generated key yields no authorized_keys line")
		}
		stored, lerr := loadProfile("mybase")
		if lerr != nil {
			t.Fatal(lerr)
		}
		if stored.PublicKeyLine() != pub {
			t.Error("the key handed to the installer is not the key stored in the vault")
		}
	})

	// Re-running must reuse the key, or the second create locks us out of
	// the machine the first one authorized.
	t.Run("existing key is reused", func(t *testing.T) {
		startTestAgent(t)
		f := &baseTargetFlags{}
		first, err := f.ensureOwnerKey("mybase")
		if err != nil {
			t.Fatal(err)
		}
		second, err := f.ensureOwnerKey("mybase")
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Error("re-running regenerated the key instead of reusing it")
		}
	})

	// A remote key must be authorized by the provider before the server
	// boots, so generating one here would be useless.
	t.Run("remote refuses with a keygen pointer", func(t *testing.T) {
		startTestAgent(t)
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
		startTestAgent(t)
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

	// The codes only earn their keep if a caller can tell them apart.
	seen := map[int]string{}
	for name, code := range map[string]int{
		"exitError":     exitError,
		"exitUsage":     exitUsage,
		"exitPreflight": exitPreflight,
		"exitInstall":   exitInstall,
		"exitNotReady":  exitNotReady,
		"exitConflict":  exitConflict,
		"exitLocked":    exitLocked,
	} {
		if prev, dup := seen[code]; dup {
			t.Errorf("%s and %s share exit code %d", prev, name, code)
		}
		seen[code] = name
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
