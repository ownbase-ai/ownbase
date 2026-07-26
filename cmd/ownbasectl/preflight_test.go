package main

import (
	"errors"
	"strings"
	"testing"
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
