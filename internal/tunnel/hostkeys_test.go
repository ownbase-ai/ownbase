package tunnel

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestAppendHostKeys_DedupesAndRecords(t *testing.T) {
	dir := t.TempDir()
	kh := filepath.Join(dir, "known_hosts")
	pub1 := mustEd25519(t)
	pub2 := mustEd25519(t)
	normalized := knownhosts.Normalize("1.2.3.4:22")

	if err := appendHostKeys(kh, normalized, []ssh.PublicKey{pub1, pub2}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(kh)
	if err != nil {
		t.Fatal(err)
	}
	if got := countNonEmptyLines(string(before)); got != 2 {
		t.Fatalf("want 2 lines after first append, got %d:\n%s", got, before)
	}

	if err := appendHostKeys(kh, normalized, []ssh.PublicKey{pub1, pub2}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(kh)
	if string(before) != string(after) {
		t.Fatalf("dedupe failed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestAllKnownKeysStillPresent(t *testing.T) {
	a := mustEd25519(t)
	b := mustEd25519(t)
	c := mustEd25519(t)
	want := []knownhosts.KnownKey{{Key: a}, {Key: b}}
	if !allKnownKeysStillPresent(want, []ssh.PublicKey{a, b, c}) {
		t.Fatal("expected all present")
	}
	if allKnownKeysStillPresent(want, []ssh.PublicKey{a, c}) {
		t.Fatal("expected missing b to fail")
	}
	if allKnownKeysStillPresent(nil, []ssh.PublicKey{a}) {
		t.Fatal("empty want must be false")
	}
}

func TestSplitHostPort(t *testing.T) {
	host, port := splitHostPort("185.47.254.18:22", nil)
	if host != "185.47.254.18" || port != 22 {
		t.Fatalf("got %s %d", host, port)
	}
	host, port = splitHostPort("[2001:db8::1]:2222", nil)
	if host != "2001:db8::1" || port != 2222 {
		t.Fatalf("got %s %d", host, port)
	}
}

func mustEd25519(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}

func countNonEmptyLines(s string) int {
	n := 0
	for _, line := range splitLines(s) {
		if line != "" {
			n++
		}
	}
	return n
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		out = append(out, s[start:])
	}
	return out
}
