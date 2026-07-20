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

func TestEnsureRepoAt_EmptyURLIsError(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "auth")

	// Every service must declare repo: — there is no push-to-Base source
	// path anymore, so an empty URL must fail rather than create an empty
	// bare repo.
	if err := ensureRepoAt(repoPath, ""); err == nil {
		t.Fatal("expected an error for an empty repo URL")
	}
	if isBareRepo(repoPath) {
		t.Fatalf("no repo should have been created at %s", repoPath)
	}
}

func TestEnsureRepoAt_ClonesFromExternalURL(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(root, "external")
	initRepoWithCommit(t, external)

	repoPath := filepath.Join(root, "repos", "external")
	if err := ensureRepoAt(repoPath, external); err != nil {
		t.Fatalf("ensureRepoAt: %v", err)
	}
	if !isBareRepo(repoPath) {
		t.Fatalf("expected bare clone at %s", repoPath)
	}
	if !hasRefAt(repoPath, "main") {
		t.Fatalf("expected cloned repo to have ref 'main'")
	}
}

func TestEnsureRepoAt_ReclonesWhenExternalURLChanges(t *testing.T) {
	// Simulates `service update --repo` (or forking + repointing): an existing
	// clone of repo A must be replaced by repo B, not keep serving A's refs.
	root := t.TempDir()
	extA := filepath.Join(root, "extA")
	extB := filepath.Join(root, "extB")
	initRepoWithCommit(t, extA)
	initRepoWithCommit(t, extB)
	runGit(t, extA, "branch", "only-in-a")
	runGit(t, extB, "branch", "only-in-b")

	repoPath := filepath.Join(root, "repos", "svc")
	if err := ensureRepoAt(repoPath, extA); err != nil {
		t.Fatalf("ensureRepoAt(A): %v", err)
	}
	if !hasRefAt(repoPath, "only-in-a") {
		t.Fatal("expected repo A's branch after first clone")
	}

	// Repoint at repo B — the stale clone must be discarded and re-cloned.
	if err := ensureRepoAt(repoPath, extB); err != nil {
		t.Fatalf("ensureRepoAt(B): %v", err)
	}
	if !hasRefAt(repoPath, "only-in-b") {
		t.Fatal("expected repo B's branch after re-clone")
	}
	if hasRefAt(repoPath, "only-in-a") {
		t.Fatal("repo A's branch should be gone after repointing to repo B")
	}
	if got := originURLAt(repoPath); got != extB {
		t.Errorf("origin url = %q, want %q", got, extB)
	}
}

func TestEnsureRepoAt_ReusesCloneWhenURLUnchanged(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(root, "external")
	initRepoWithCommit(t, external)

	repoPath := filepath.Join(root, "repos", "svc")
	if err := ensureRepoAt(repoPath, external); err != nil {
		t.Fatalf("ensureRepoAt: %v", err)
	}
	// Mark the clone so we can tell whether it was re-created.
	marker := filepath.Join(repoPath, "reuse-marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureRepoAt(repoPath, external); err != nil {
		t.Fatalf("ensureRepoAt (second call): %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("clone was re-created even though the URL was unchanged")
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
	external := filepath.Join(root, "external")
	initRepoWithCommit(t, external)
	repoPath := filepath.Join(root, "auth")

	if err := ensureRepoAtWithOwner(repoPath, external, current.Username); err != nil {
		t.Fatalf("ensureRepoAtWithOwner: %v", err)
	}
	if !isBareRepo(repoPath) {
		t.Fatalf("expected bare repo at %s", repoPath)
	}

	// Idempotent: calling again (repo already exists) still succeeds and
	// re-applies the chown.
	if err := ensureRepoAtWithOwner(repoPath, external, current.Username); err != nil {
		t.Fatalf("ensureRepoAtWithOwner (second call): %v", err)
	}
}

func TestEnsureRepoAtWithOwner_EmptyAdminUserSkipsChown(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(root, "external")
	initRepoWithCommit(t, external)
	repoPath := filepath.Join(root, "auth")

	if err := ensureRepoAtWithOwner(repoPath, external, ""); err != nil {
		t.Fatalf("ensureRepoAtWithOwner with empty adminUser should succeed, got %v", err)
	}
}

func TestEnsureRepoAtWithOwner_UnknownAdminUserReturnsError(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(root, "external")
	initRepoWithCommit(t, external)
	repoPath := filepath.Join(root, "auth")

	if err := ensureRepoAtWithOwner(repoPath, external, "no-such-user-should-exist-anywhere"); err == nil {
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
	external := filepath.Join(root, "external")
	initRepoWithCommit(t, external)
	repoPath := filepath.Join(root, "svc")
	if hasRefAt(repoPath, "") {
		t.Fatalf("expected false before the repo is created")
	}
	if err := ensureRepoAt(repoPath, external); err != nil {
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
