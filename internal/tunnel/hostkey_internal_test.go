package tunnel

import (
	"errors"
	"strings"
	"testing"
)

// TestBuildHostKeyCallback_NoHomeFailsClosed locks the contract that a missing
// home directory refuses the connection rather than falling back to
// ssh.InsecureIgnoreHostKey. That fallback used to disable host verification
// silently for any process whose environment lacked a resolvable home.
func TestBuildHostKeyCallback_NoHomeFailsClosed(t *testing.T) {
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	userHomeDir = func() (string, error) {
		return "", errors.New("home directory unavailable")
	}

	cb, err := buildHostKeyCallback("example.com")
	if err == nil {
		t.Fatal("buildHostKeyCallback: got nil error, want failure when home is unavailable")
	}
	if cb != nil {
		t.Fatal("buildHostKeyCallback: got a callback, want nil on failure")
	}
	if !strings.Contains(err.Error(), "home directory") {
		t.Errorf("error = %q, want it to mention home directory", err)
	}
	if !strings.Contains(err.Error(), "known_hosts") {
		t.Errorf("error = %q, want it to mention known_hosts", err)
	}
}

func TestKnownHostsPath_PropagatesHomeError(t *testing.T) {
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	userHomeDir = func() (string, error) {
		return "", errors.New("no home")
	}

	if _, err := knownHostsPath(); err == nil {
		t.Fatal("knownHostsPath: got nil error, want home error")
	}
}
