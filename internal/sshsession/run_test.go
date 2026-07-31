package sshsession_test

// Regression test for the Bugbot finding "Failed SSH omits recording result":
// Run used to return a nil *Result whenever it failed before the remote
// session started (bad host, refused connection, ...), even though the
// failed attempt was already written to disk via rec.Finish. That meant
// `ownbasectl ssh --json` and its "session recorded as ..." notice silently
// disappeared for exactly the failures an operator most needs to see.

import (
	"net"
	"testing"

	"github.com/ownbase/ownbase/internal/sshsession"
	"github.com/ownbase/ownbase/internal/tunnel"
)

// closedPort returns a loopback port nothing is listening on, guaranteeing a
// fast "connection refused" from tunnel.Dial instead of a real network wait.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// A dial failure is the earliest possible failure in Run, and the one every
// other early-return branch (session open, stdin pipe, raw mode, PTY
// request) mirrors — if this one carries a Result, so do the rest.
func TestRun_DialFailureStillReturnsResult(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	res, err := sshsession.Run(sshsession.Options{
		Base: "testbase",
		Target: tunnel.Target{
			Host: "127.0.0.1",
			Port: closedPort(t),
			User: "testuser",
		},
	})

	if err == nil {
		t.Fatal("Run: expected a dial error, got nil")
	}
	if res == nil {
		t.Fatal("Run: got a nil Result on a dial failure — the failed attempt is on disk but the caller can no longer see it")
	}
	if res.Meta.ID == "" {
		t.Error("Result.Meta.ID is empty")
	}
	if res.Meta.CastPath == "" {
		t.Error("Result.Meta.CastPath is empty")
	}
	if res.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 (no remote command ever ran)", res.ExitCode)
	}
	if res.Meta.EndedAt == nil {
		t.Error("Meta.EndedAt is nil — Finish should have closed out the recording")
	}
}
