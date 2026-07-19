package gitssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommand_NoManagedIdentity(t *testing.T) {
	dir := t.TempDir() // empty — no key, no config
	if got := Command(dir); got != "" {
		t.Errorf("Command(empty dir) = %q, want \"\" (fall back to system ssh)", got)
	}
}

func TestCommand_SingleKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, KeyName)
	if err := os.WriteFile(keyPath, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Command(dir)
	if !strings.Contains(got, "-i "+keyPath) {
		t.Errorf("Command = %q, want it to reference the managed key", got)
	}
	if !strings.Contains(got, "IdentitiesOnly=yes") {
		t.Errorf("Command = %q, want IdentitiesOnly=yes", got)
	}
	// No known_hosts yet → accept-new.
	if !strings.Contains(got, "StrictHostKeyChecking=accept-new") {
		t.Errorf("Command = %q, want accept-new when no known_hosts", got)
	}

	// With known_hosts present, it should be referenced instead.
	kh := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(kh, []byte("github.com ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = Command(dir)
	if !strings.Contains(got, "UserKnownHostsFile="+kh) {
		t.Errorf("Command = %q, want it to reference known_hosts", got)
	}
	if strings.Contains(got, "accept-new") {
		t.Errorf("Command = %q, should not use accept-new when known_hosts exists", got)
	}
}

func TestCommand_ConfigTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	// Both a key and a config file exist; config wins.
	if err := os.WriteFile(filepath.Join(dir, KeyName), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config")
	if err := os.WriteFile(cfgPath, []byte("Host github.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Command(dir)
	if !strings.Contains(got, "-F "+cfgPath) {
		t.Errorf("Command = %q, want it to use ssh -F <config>", got)
	}
}

func TestEnvFor_SetsGitSSHCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, KeyName), []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := EnvFor(dir)
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			found = true
		}
	}
	if !found {
		t.Error("EnvFor should set GIT_SSH_COMMAND when a managed key exists")
	}
}

func TestEnsureKey_GeneratesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	pub, err := EnsureKey(dir)
	if err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Errorf("public key = %q, want an ssh-ed25519 key", pub)
	}
	if _, err := os.Stat(filepath.Join(dir, KeyName)); err != nil {
		t.Errorf("private key not created: %v", err)
	}

	// Idempotent: a second call keeps the same key.
	pub2, err := EnsureKey(dir)
	if err != nil {
		t.Fatalf("EnsureKey (second): %v", err)
	}
	if pub != pub2 {
		t.Error("EnsureKey regenerated the key on a second call")
	}
}

func TestPublicKey_MissingReturnsEmpty(t *testing.T) {
	pub, err := PublicKey(t.TempDir())
	if err != nil {
		t.Fatalf("PublicKey(missing): %v", err)
	}
	if pub != "" {
		t.Errorf("PublicKey(missing) = %q, want empty", pub)
	}
}
