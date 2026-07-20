package configsource

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSource_ConfiguredAndEffectiveRef(t *testing.T) {
	if (Source{}).Configured() {
		t.Error("empty Source should not be Configured()")
	}
	if !(Source{RepoURL: "git@x:y.git"}).Configured() {
		t.Error("Source with RepoURL should be Configured()")
	}
	if got := (Source{RepoURL: "x"}).EffectiveRef(); got != DefaultRef {
		t.Errorf("EffectiveRef() = %q, want %q", got, DefaultRef)
	}
	if got := (Source{RepoURL: "x", Ref: "release"}).EffectiveRef(); got != "release" {
		t.Errorf("EffectiveRef() = %q, want release", got)
	}
}

func TestLoadSave_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config-source.yaml")

	// Missing file loads as unconfigured, no error.
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	if s.Configured() {
		t.Error("missing file should load as unconfigured")
	}

	want := Source{RepoURL: "git@github.com:org/config.git", Ref: "main"}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Errorf("round-trip = %+v, want %+v", got, want)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// newRemote creates a bare origin plus a working checkout with an initial
// ownbase.yaml commit on main, and returns the bare repo path.
func newRemote(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "origin.git")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, bare, "init", "--bare", "--initial-branch=main")
	gitRun(t, root, "clone", bare, work)
	gitRun(t, work, "config", "user.email", "t@example.com")
	gitRun(t, work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "ownbase.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "ownbase.yaml")
	gitRun(t, work, "commit", "-m", "initial")
	gitRun(t, work, "push", "origin", "main")
	return bare
}

func TestEnsureCheckout_ClonesThenFetchesAndResets(t *testing.T) {
	bare := newRemote(t, "schema_version: v1\nservices: {}\n")
	checkout := filepath.Join(t.TempDir(), "checkout")
	src := Source{RepoURL: bare, Ref: "main"}

	// First call clones.
	if err := EnsureCheckout(context.Background(), src, checkout, nil); err != nil {
		t.Fatalf("EnsureCheckout (clone): %v", err)
	}
	got, err := os.ReadFile(filepath.Join(checkout, "ownbase.yaml"))
	if err != nil {
		t.Fatalf("read checkout: %v", err)
	}
	if string(got) != "schema_version: v1\nservices: {}\n" {
		t.Errorf("checkout content = %q", got)
	}

	// Push a new commit to the remote, then EnsureCheckout must fast-forward.
	work := filepath.Join(t.TempDir(), "work2")
	gitRun(t, filepath.Dir(work), "clone", bare, work)
	gitRun(t, work, "config", "user.email", "t@example.com")
	gitRun(t, work, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(work, "ownbase.yaml"), []byte("schema_version: v1\nservices:\n  a: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "commit", "-am", "add a")
	gitRun(t, work, "push", "origin", "main")

	if err := EnsureCheckout(context.Background(), src, checkout, nil); err != nil {
		t.Fatalf("EnsureCheckout (fetch): %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(checkout, "ownbase.yaml"))
	if string(got) != "schema_version: v1\nservices:\n  a: {}\n" {
		t.Errorf("checkout after fetch = %q", got)
	}
}

func TestEnsureCheckout_UnconfiguredIsNoop(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "checkout")
	if err := EnsureCheckout(context.Background(), Source{}, checkout, nil); err != nil {
		t.Fatalf("EnsureCheckout(unconfigured): %v", err)
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Error("unconfigured source should not create a checkout")
	}
}

func TestEnsureCheckout_DiscardsLocalEdits(t *testing.T) {
	bare := newRemote(t, "schema_version: v1\nservices: {}\n")
	checkout := filepath.Join(t.TempDir(), "checkout")
	src := Source{RepoURL: bare, Ref: "main"}

	if err := EnsureCheckout(context.Background(), src, checkout, nil); err != nil {
		t.Fatalf("EnsureCheckout: %v", err)
	}
	// Tamper with the checkout — a subsequent EnsureCheckout must reset it.
	if err := os.WriteFile(filepath.Join(checkout, "ownbase.yaml"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureCheckout(context.Background(), src, checkout, nil); err != nil {
		t.Fatalf("EnsureCheckout (reset): %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(checkout, "ownbase.yaml"))
	if string(got) != "schema_version: v1\nservices: {}\n" {
		t.Errorf("local edit not discarded: %q", got)
	}
}

func TestEnsureCheckout_ReclonesWhenRepoURLChanges(t *testing.T) {
	// Simulates `config setup` repointing the Base at a different config repo:
	// an existing checkout of repo A must be replaced by repo B's content, not
	// keep syncing A's origin.
	bareA := newRemote(t, "schema_version: v1\nservices:\n  a: {}\n")
	bareB := newRemote(t, "schema_version: v1\nservices:\n  b: {}\n")
	checkout := filepath.Join(t.TempDir(), "checkout")

	if err := EnsureCheckout(context.Background(), Source{RepoURL: bareA, Ref: "main"}, checkout, nil); err != nil {
		t.Fatalf("EnsureCheckout (repo A): %v", err)
	}
	if err := EnsureCheckout(context.Background(), Source{RepoURL: bareB, Ref: "main"}, checkout, nil); err != nil {
		t.Fatalf("EnsureCheckout (repo B): %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(checkout, "ownbase.yaml"))
	if string(got) != "schema_version: v1\nservices:\n  b: {}\n" {
		t.Errorf("checkout still reflects old repo after URL change: %q", got)
	}
	if url := originURL(context.Background(), nil, checkout); url != bareB {
		t.Errorf("origin url = %q, want %q (should have re-cloned from repo B)", url, bareB)
	}
}
