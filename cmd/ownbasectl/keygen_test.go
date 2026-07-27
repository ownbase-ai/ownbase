package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ownbase/ownbase/internal/vault"
)

func TestRunKeygen_StoresKeyInVault(t *testing.T) {
	startTestAgent(t)

	if err := runKeygen("mybase", "", true); err != nil {
		t.Fatalf("keygen: %v", err)
	}

	p, err := loadProfile("mybase")
	if err != nil {
		t.Fatalf("loadProfile: %v", err)
	}
	if p.PublicKeyLine() == "" {
		t.Fatal("keygen stored no public key")
	}
	// The redacted profile the CLI receives must never carry the private half:
	// that property is what lets an agent drive ownbasectl safely.
	if p.PrivateKey != "" {
		t.Error("the private key left the credential agent")
	}
	// Nor may it appear anywhere on disk.
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".ssh", "ownbase_mybase")); err == nil {
		t.Error("keygen wrote a private key file to ~/.ssh")
	}
}

// Re-running keygen must reuse the key. Regenerating it would lock the operator
// out of the machine the first key authorized.
func TestRunKeygen_Idempotent(t *testing.T) {
	startTestAgent(t)

	if err := runKeygen("mybase", "", true); err != nil {
		t.Fatalf("first keygen: %v", err)
	}
	first, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}

	if err := runKeygen("mybase", "", true); err != nil {
		t.Fatalf("second keygen: %v", err)
	}
	second, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicKeyLine() != second.PublicKeyLine() {
		t.Error("re-running keygen regenerated the key; it must be idempotent")
	}
}

// A key for one Base must not be reachable as another Base's key.
func TestRunKeygen_KeyIsPerBase(t *testing.T) {
	startTestAgent(t)

	if err := runKeygen("first", "", true); err != nil {
		t.Fatal(err)
	}
	if err := runKeygen("second", "", true); err != nil {
		t.Fatal(err)
	}
	a, err := loadProfile("first")
	if err != nil {
		t.Fatal(err)
	}
	b, err := loadProfile("second")
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicKeyLine() == b.PublicKeyLine() {
		t.Error("two Bases share one owner key; retiring one would revoke both")
	}
}

func TestRunKeygen_Import(t *testing.T) {
	startTestAgent(t)

	privPEM, _, pub := newTestOwnerKey(t)
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, []byte(privPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runKeygen("mybase", keyFile, true); err != nil {
		t.Fatalf("keygen --import: %v", err)
	}
	p, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if !strings.HasPrefix(p.PublicKeyLine(), want) {
		t.Errorf("imported key = %q, want prefix %q", p.PublicKeyLine(), want)
	}
	// Importing must not move or delete the operator's own file.
	if _, err := os.Stat(keyFile); err != nil {
		t.Errorf("the imported key file was disturbed: %v", err)
	}
}

// Importing over a Base that already has a different key is refused: the old
// key is what the running machine authorizes, and replacing it silently would
// be a lockout.
func TestRunKeygen_ImportRefusesToReplaceDifferentKey(t *testing.T) {
	startTestAgent(t)

	if err := runKeygen("mybase", "", true); err != nil {
		t.Fatal(err)
	}
	original, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}

	otherPEM, _, _ := newTestOwnerKey(t)
	keyFile := filepath.Join(t.TempDir(), "other")
	if err := os.WriteFile(keyFile, []byte(otherPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	err = runKeygen("mybase", keyFile, true)
	if err == nil {
		t.Fatal("expected keygen --import to refuse to replace an existing owner key")
	}
	if code := exitCodeFor(err); code != exitConflict {
		t.Errorf("exit code = %d, want %d", code, exitConflict)
	}
	after, lerr := loadProfile("mybase")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if after.PublicKeyLine() != original.PublicKeyLine() {
		t.Error("the existing owner key was replaced despite the refusal")
	}
}

// Importing the exact key material a Base already has must succeed even when
// the two copies are spelled differently: a key `keygen` generated carries an
// "ownbase_<name>" comment (visible to anyone who opens the vault directly in
// KeePassXC, by design), while readPrivateKeyFile — reading that same key back
// out of a file — emits no comment at all. That difference alone must not
// read as "a different key".
func TestRunKeygen_ImportMatchesKeyStoredWithDifferentComment(t *testing.T) {
	startTestAgent(t)

	// Stand in for a key `keygen` created directly: PublicKey carries a
	// comment, exactly as entryFromProfile / profileFromEntry round-trip it.
	privPEM, commentedLine, _ := newTestOwnerKey(t)
	putTestProfile(t, "mybase", vault.Profile{PrivateKey: privPEM, PublicKey: commentedLine})

	// The same key, as if copied out of the vault file by hand — no comment.
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, []byte(privPEM), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runKeygen("mybase", keyFile, true); err != nil {
		t.Fatalf("re-importing identical key material was refused: %v", err)
	}
	after, err := loadProfile("mybase")
	if err != nil {
		t.Fatal(err)
	}
	if !vault.SameAuthorizedKey(after.PublicKeyLine(), commentedLine) {
		t.Errorf("key changed across re-import: %q -> %q", commentedLine, after.PublicKeyLine())
	}
}

func TestReadPrivateKeyFile_RejectsUnparseable(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "broken")
	if err := os.WriteFile(keyFile, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nnot parseable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readPrivateKeyFile(keyFile); err == nil {
		t.Fatal("expected an error for an unparseable key")
	}
}

// The public key handed to the installer must be derived from the private key
// the agent will sign with, not from anything alongside it — a mismatch is only
// discovered later, as a lockout.
func TestOwnerPublicKeyMatchesSigningKey(t *testing.T) {
	privPEM, storedLine, pub := newTestOwnerKey(t)
	p := vault.Profile{PrivateKey: privPEM, PublicKey: "ssh-ed25519 AAAAstale wrong-key"}

	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if got := p.PublicKeyLine(); !strings.HasPrefix(got, want) {
		t.Errorf("PublicKeyLine = %q, want prefix %q", got, want)
	}
	if !strings.HasPrefix(storedLine, want) {
		t.Errorf("NewKeyPair public line = %q, want prefix %q", storedLine, want)
	}
}
