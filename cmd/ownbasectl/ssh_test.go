package main

// Tier-1 tests for `ownbasectl ssh` and `ownbasectl sessions`.
//
// The recording is the feature, so these run a real command over the in-process
// SSH server (sshserver_test.go), authenticated by the credential agent, and
// then assert on what landed in ~/.ownbase/sessions: the transcript, the
// metadata, and the fact that a failing command is recorded as such.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/sshsession"
	"github.com/ownbase/ownbase/internal/vault"
)

// sshTestBase registers a Base pointing at an in-process SSH server whose only
// authorized key is the one in the vault, so a session that connects proves the
// agent signed for it.
func sshTestBase(t *testing.T, name string) {
	t.Helper()
	startTestAgent(t)
	privPEM, pubLine, clientPub := newTestOwnerKey(t)
	srv := startTestSSHServer(t, clientPub)
	putTestProfile(t, name, vault.Profile{
		Host:       "127.0.0.1",
		SSHUser:    "testuser",
		SSHPort:    srv.port(),
		PrivateKey: privPEM,
		PublicKey:  pubLine,
	})
}

func TestRunSSH_RecordsCommandOutput(t *testing.T) {
	sshTestBase(t, "mybase")

	if err := runSSH("mybase", "echo hello-from-the-base", true, false); err != nil {
		t.Fatalf("runSSH: %v", err)
	}

	sessions, err := sshsession.List("mybase")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d recorded sessions, want 1", len(sessions))
	}
	m := sessions[0]

	if m.Command != "echo hello-from-the-base" {
		t.Errorf("Command = %q", m.Command)
	}
	if m.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", m.ExitCode)
	}
	if m.Error != "" {
		t.Errorf("Error = %q, want empty", m.Error)
	}
	if m.EndedAt == nil {
		t.Error("EndedAt is nil; the session was never closed out")
	}
	if m.Invoker != "cli" {
		t.Errorf("Invoker = %q, want cli", m.Invoker)
	}

	text, err := sshsession.Transcript(m.CastPath)
	if err != nil {
		t.Fatalf("Transcript: %v", err)
	}
	if !strings.Contains(text, "hello-from-the-base") {
		t.Errorf("transcript is missing the command output: %q", text)
	}
}

// A recording can hold anything typed at a prompt, so it must not be readable
// by other accounts on the machine.
func TestRunSSH_RecordingIsOwnerReadableOnly(t *testing.T) {
	sshTestBase(t, "mybase")
	if err := runSSH("mybase", "echo hi", true, false); err != nil {
		t.Fatalf("runSSH: %v", err)
	}

	sessions, err := sshsession.List("mybase")
	if err != nil || len(sessions) != 1 {
		t.Fatalf("List: %v (%d sessions)", err, len(sessions))
	}
	for _, path := range []string{
		sessions[0].CastPath,
		strings.TrimSuffix(sessions[0].CastPath, ".cast") + ".json",
	} {
		info, serr := os.Stat(path)
		if serr != nil {
			t.Fatalf("stat %s: %v", path, serr)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has mode %o, want 600", filepath.Base(path), perm)
		}
	}
}

// A non-zero remote exit is information, not a failure of ours: it must survive
// into both the exit code and the audit trail.
func TestRunSSH_PassesThroughRemoteExitCode(t *testing.T) {
	sshTestBase(t, "mybase")

	err := runSSH("mybase", "exit 7", true, false)
	if err == nil {
		t.Fatal("expected an error for a command that exited 7")
	}
	if code := exitCodeFor(err); code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}

	sessions, lerr := sshsession.List("mybase")
	if lerr != nil || len(sessions) != 1 {
		t.Fatalf("List: %v (%d sessions)", lerr, len(sessions))
	}
	if sessions[0].ExitCode != 7 {
		t.Errorf("recorded ExitCode = %d, want 7", sessions[0].ExitCode)
	}
	if sessions[0].Error != "" {
		t.Errorf("a non-zero exit is not a session error, got %q", sessions[0].Error)
	}
}

// A session that never connected still has to leave a trace, or "nothing was
// recorded" would be indistinguishable from "nothing happened".
func TestRunSSH_RecordsAFailedConnection(t *testing.T) {
	startTestAgent(t)
	privPEM, pubLine, _ := newTestOwnerKey(t)
	putTestProfile(t, "mybase", vault.Profile{
		Host:       "127.0.0.1",
		SSHUser:    "testuser",
		SSHPort:    1, // nothing listening
		PrivateKey: privPEM,
		PublicKey:  pubLine,
	})

	if err := runSSH("mybase", "echo hi", true, false); err == nil {
		t.Fatal("expected an error connecting to a closed port")
	}

	sessions, err := sshsession.List("mybase")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d recorded sessions, want 1", len(sessions))
	}
	if sessions[0].Error == "" {
		t.Error("a failed session must record why it failed")
	}
	if sessions[0].ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 for a session that never ran", sessions[0].ExitCode)
	}
}

func TestRunSSH_InvokerLabelIsRecorded(t *testing.T) {
	sshTestBase(t, "mybase")
	t.Setenv(sshsession.InvokerEnv, "app")

	if err := runSSH("mybase", "echo hi", true, false); err != nil {
		t.Fatalf("runSSH: %v", err)
	}
	sessions, err := sshsession.List("mybase")
	if err != nil || len(sessions) != 1 {
		t.Fatalf("List: %v (%d sessions)", err, len(sessions))
	}
	if sessions[0].Invoker != "app" {
		t.Errorf("Invoker = %q, want app", sessions[0].Invoker)
	}
}

// sessions list across all Bases is what the desktop app and an agent read, so
// the JSON shape matters.
func TestSessionsList_AcrossBases(t *testing.T) {
	sshTestBase(t, "one")
	// A second Base sharing the same agent and temp HOME.
	privPEM, pubLine, clientPub := newTestOwnerKey(t)
	srv := startTestSSHServer(t, clientPub)
	putTestProfile(t, "two", vault.Profile{
		Host:       "127.0.0.1",
		SSHUser:    "testuser",
		SSHPort:    srv.port(),
		PrivateKey: privPEM,
		PublicKey:  pubLine,
	})

	for _, base := range []string{"one", "two"} {
		if err := runSSH(base, "echo "+base, true, false); err != nil {
			t.Fatalf("runSSH %s: %v", base, err)
		}
	}

	all, err := sshsession.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d sessions across Bases, want 2", len(all))
	}
	// Newest first.
	if all[0].StartedAt.Before(all[1].StartedAt) {
		t.Error("sessions are not sorted newest first")
	}

	one, err := sshsession.List("one")
	if err != nil || len(one) != 1 || one[0].Base != "one" {
		t.Errorf("List(one) = %+v, %v", one, err)
	}
}

func TestSessionsFind_UnknownID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := sshsession.Find("", "nope"); err == nil {
		t.Fatal("expected an error for an unknown session id")
	} else if !strings.Contains(err.Error(), "sessions list") {
		t.Errorf("error should point at 'sessions list', got: %v", err)
	}
}

// The metadata sidecar is written before the session runs, so a killed session
// still shows up. Verify it is valid JSON on disk rather than only in memory.
func TestSessionMetaSidecarIsValidJSON(t *testing.T) {
	sshTestBase(t, "mybase")
	if err := runSSH("mybase", "echo hi", true, false); err != nil {
		t.Fatalf("runSSH: %v", err)
	}

	dir, err := sshsession.Dir("mybase")
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	found := false
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		found = true
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		var m sshsession.Meta
		if uerr := json.Unmarshal(data, &m); uerr != nil {
			t.Errorf("%s is not valid Meta JSON: %v", e.Name(), uerr)
		}
		if m.ID == "" || m.Base != "mybase" {
			t.Errorf("%s has an incomplete Meta: %+v", e.Name(), m)
		}
	}
	if !found {
		t.Error("no metadata sidecar was written")
	}
}
