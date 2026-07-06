package repos

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"
)

func TestRepoPath(t *testing.T) {
	got := RepoPath("services/auth")
	want := filepath.Join(DefaultReposDir, "services/auth")
	if got != want {
		t.Fatalf("RepoPath = %q, want %q", got, want)
	}
}

func TestEnsureRepoAt_SourceCreatesEmptyBareRepo(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "services/auth")

	if err := ensureRepoAt(repoPath, ""); err != nil {
		t.Fatalf("ensureRepoAt: %v", err)
	}
	if !isBareRepo(repoPath) {
		t.Fatalf("expected bare repo at %s", repoPath)
	}

	// Idempotent: calling again is a no-op, not an error.
	if err := ensureRepoAt(repoPath, ""); err != nil {
		t.Fatalf("ensureRepoAt (second call): %v", err)
	}
}

func TestEnsureRepoAt_MirrorClonesFromExternalURL(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(root, "external")
	initRepoWithCommit(t, external)

	repoPath := filepath.Join(root, "repos", "mirrors-external")
	if err := ensureRepoAt(repoPath, external); err != nil {
		t.Fatalf("ensureRepoAt: %v", err)
	}
	if !isBareRepo(repoPath) {
		t.Fatalf("expected bare mirror at %s", repoPath)
	}
	if !hasRefAt(repoPath, "main") {
		t.Fatalf("expected mirrored repo to have ref 'main'")
	}
}

func TestFetchRefAt_FetchesNewRefOnDemand(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(root, "external")
	initRepoWithCommit(t, external)

	repoPath := filepath.Join(root, "repos", "mirrors-external")
	if err := ensureRepoAt(repoPath, external); err != nil {
		t.Fatalf("ensureRepoAt: %v", err)
	}

	// Create a new branch in the external repo after the initial mirror.
	runGit(t, external, "branch", "feature-x")

	if hasRefAt(repoPath, "feature-x") {
		t.Fatalf("did not expect feature-x to be present before fetch")
	}
	if err := fetchRefAt(repoPath, external, "feature-x"); err != nil {
		t.Fatalf("fetchRefAt: %v", err)
	}
	if !hasRefAt(repoPath, "feature-x") {
		t.Fatalf("expected feature-x to be present after fetch")
	}
}

func TestFetchRef_NoopWhenExternalURLOrRefEmpty(t *testing.T) {
	if err := FetchRef("anything", "", "main", ""); err != nil {
		t.Fatalf("FetchRef with empty externalURL should be a no-op, got %v", err)
	}
	if err := FetchRef("anything", "https://example.com/repo.git", "", ""); err != nil {
		t.Fatalf("FetchRef with empty ref should be a no-op, got %v", err)
	}
}

func TestEnsureRepoAtWithOwner_ChownsToAdminUser(t *testing.T) {
	// Chowning to the current user is a no-op ownership change (you always
	// own your own files) but still exercises the chown call end to end
	// without requiring root privileges in CI.
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot determine current user: %v", err)
	}

	root := t.TempDir()
	repoPath := filepath.Join(root, "services/auth")

	if err := ensureRepoAtWithOwner(repoPath, "", current.Username); err != nil {
		t.Fatalf("ensureRepoAtWithOwner: %v", err)
	}
	if !isBareRepo(repoPath) {
		t.Fatalf("expected bare repo at %s", repoPath)
	}

	// Idempotent: calling again (repo already exists) still succeeds and
	// re-applies the chown.
	if err := ensureRepoAtWithOwner(repoPath, "", current.Username); err != nil {
		t.Fatalf("ensureRepoAtWithOwner (second call): %v", err)
	}
}

func TestEnsureRepoAtWithOwner_EmptyAdminUserSkipsChown(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "services/auth")

	if err := ensureRepoAtWithOwner(repoPath, "", ""); err != nil {
		t.Fatalf("ensureRepoAtWithOwner with empty adminUser should succeed, got %v", err)
	}
}

func TestEnsureRepoAtWithOwner_UnknownAdminUserReturnsError(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "services/auth")

	if err := ensureRepoAtWithOwner(repoPath, "", "no-such-user-should-exist-anywhere"); err == nil {
		t.Fatal("expected an error for an unknown admin user")
	}
}

func TestHasRefAt_FalseWhenRepoMissing(t *testing.T) {
	root := t.TempDir()
	if hasRefAt(filepath.Join(root, "nope"), "main") {
		t.Fatalf("expected false for a repo that doesn't exist")
	}
}

func TestHasRefAt_EmptyRefReflectsRepoExistence(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "svc")
	if hasRefAt(repoPath, "") {
		t.Fatalf("expected false before the repo is created")
	}
	if err := ensureRepoAt(repoPath, ""); err != nil {
		t.Fatalf("ensureRepoAt: %v", err)
	}
	if !hasRefAt(repoPath, "") {
		t.Fatalf("expected true once the repo exists")
	}
}

func initRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
