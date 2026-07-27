package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ownbase/ownbase/internal/serverconfig"
)

func TestGenerateOwnerKey(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	keyPath := filepath.Join(tmpHome, ".ssh", "ownbase_mybase")
	if err := generateOwnerKey(keyPath, "ownbase_mybase"); err != nil {
		t.Fatalf("generateOwnerKey: %v", err)
	}

	priv, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read private key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatalf("generated private key does not parse: %v", err)
	}

	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("private key mode = %o, want 600", perm)
	}

	// The .pub file must describe the same key as the private half, or the
	// key installed into authorized_keys would not be the key we connect with.
	pub, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	wantPrefix := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if !strings.HasPrefix(strings.TrimSpace(string(pub)), wantPrefix) {
		t.Errorf(".pub does not match the private key\n got: %s\nwant prefix: %s", pub, wantPrefix)
	}
}

// TestGenerateOwnerKey_NeverClobbers guards the worst outcome of a bug here:
// silently replacing an owner key locks you out of the Base it reaches.
func TestGenerateOwnerKey_NeverClobbers(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	keyPath := filepath.Join(tmpHome, ".ssh", "ownbase_mybase")
	if err := generateOwnerKey(keyPath, "first"); err != nil {
		t.Fatalf("generateOwnerKey: %v", err)
	}
	original, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := generateOwnerKey(keyPath, "second"); err == nil {
		t.Error("expected generateOwnerKey to refuse to overwrite an existing key")
	}
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != string(after) {
		t.Error("existing private key was modified")
	}
}

func TestRunKeygen_Idempotent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := runKeygen("mybase", true); err != nil {
		t.Fatalf("first keygen: %v", err)
	}
	keyPath := filepath.Join(tmpHome, ".ssh", "ownbase_mybase")
	first, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := runKeygen("mybase", true); err != nil {
		t.Fatalf("second keygen: %v", err)
	}
	second, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("re-running keygen regenerated the key; it must be idempotent")
	}
}

func TestResolveOwnerKey(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if got := resolveOwnerKey("mybase", "/custom/key"); got != "/custom/key" {
		t.Errorf("explicit --ssh-key should win, got %q", got)
	}
	if got := resolveOwnerKey("mybase", ""); got != serverconfig.DefaultSSHKey {
		t.Errorf("with no per-Base key, want %q, got %q", serverconfig.DefaultSSHKey, got)
	}

	if err := runKeygen("mybase", true); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("~", ".ssh", "ownbase_mybase")
	if got := resolveOwnerKey("mybase", ""); got != want {
		t.Errorf("after keygen, want %q, got %q", want, got)
	}
	// A key for one Base must not be picked up by another.
	if got := resolveOwnerKey("otherbase", ""); got != serverconfig.DefaultSSHKey {
		t.Errorf("per-Base key leaked to another Base: got %q", got)
	}
}

// TestOwnerPublicKeyMatchesConnectingKey is the regression test for the
// mismatch this refactor removed: the public key written into the server's
// authorized_keys must be derived from the private key ownbasectl
// authenticates with, not from whatever ~/.ssh/id_ed25519.pub happens to be.
func TestOwnerPublicKeyMatchesConnectingKey(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// A decoy default key that must NOT be chosen.
	decoyPath := filepath.Join(tmpHome, ".ssh", "id_ed25519")
	if err := generateOwnerKey(decoyPath, "decoy"); err != nil {
		t.Fatal(err)
	}
	decoyPub, err := os.ReadFile(decoyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	if err := runKeygen("mybase", true); err != nil {
		t.Fatal(err)
	}
	resolved := resolveOwnerKey("mybase", "")
	got := ownerPublicKey(resolved)

	if got == strings.TrimSpace(string(decoyPub)) {
		t.Fatal("ownerPublicKey returned the default key instead of the per-Base key")
	}

	priv, err := os.ReadFile(filepath.Join(tmpHome, ".ssh", "ownbase_mybase"))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if !strings.HasPrefix(got, want) {
		t.Errorf("authorized key does not match the connecting key\n got: %s\nwant prefix: %s", got, want)
	}
}

// TestOwnerPublicKey_FallsBackToPubFile covers passphrase-protected keys,
// which cannot be parsed unattended.
func TestOwnerPublicKey_FallsBackToPubFile(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	keyPath := filepath.Join(tmpHome, ".ssh", "encrypted")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nnot parseable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath+".pub", []byte("ssh-ed25519 AAAAC3Nza-fake me@host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := ownerPublicKey(keyPath), "ssh-ed25519 AAAAC3Nza-fake me@host"; got != want {
		t.Errorf("ownerPublicKey = %q, want %q", got, want)
	}
}
