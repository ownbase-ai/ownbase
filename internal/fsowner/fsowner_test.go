package fsowner_test

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"

	"github.com/ownbase/ownbase/internal/fsowner"
)

func TestChown_NoopWhenUsernameEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := fsowner.Chown(dir, ""); err != nil {
		t.Fatalf("Chown with empty username should be a no-op, got %v", err)
	}
}

func TestChown_UnknownUserReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := fsowner.Chown(dir, "no-such-user-should-exist-anywhere"); err == nil {
		t.Fatal("expected an error for an unknown username")
	}
}

func TestChown_WalksEntireTree(t *testing.T) {
	// Chowning to the current user is a no-op ownership change (you always
	// own your own files) but still exercises the full lookup + walk path
	// without requiring root privileges in CI.
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot determine current user: %v", err)
	}

	dir := t.TempDir()
	nested := filepath.Join(dir, "objects", "pack")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "pack-1.pack"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := fsowner.Chown(dir, current.Username); err != nil {
		t.Fatalf("Chown: %v", err)
	}
}
