package configsource

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `schema_version: v1
services:
  web:
    repo: https://github.com/example/web.git
    port: 8080
`

const validYAML2 = `schema_version: v1
services:
  web:
    repo: https://github.com/example/web.git
    port: 8080
    ref: abcdef0123456789abcdef0123456789abcdef01
`

func TestPushBranch_CreatesBranchLeavesMain(t *testing.T) {
	bare := newRemote(t, validYAML)
	src := Source{RepoURL: bare, Ref: "main"}

	res, err := PushBranch(context.Background(), src, validYAML2, "test: bump ref", "ownbase/agent/test-1", nil)
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if res.Branch != "ownbase/agent/test-1" {
		t.Errorf("branch = %q", res.Branch)
	}
	if len(res.SHA) < 40 {
		t.Errorf("sha = %q", res.SHA)
	}

	// main must still hold the original content.
	work := filepath.Join(t.TempDir(), "check")
	gitRun(t, filepath.Dir(work), "clone", bare, work)
	mainYAML, err := os.ReadFile(filepath.Join(work, "ownbase.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(mainYAML) != validYAML {
		t.Errorf("main ownbase.yaml changed: %q", mainYAML)
	}

	// Branch must have the new content.
	gitRun(t, work, "fetch", "origin", res.Branch)
	gitRun(t, work, "checkout", res.Branch)
	branchYAML, err := os.ReadFile(filepath.Join(work, "ownbase.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(branchYAML) != validYAML2 {
		t.Errorf("branch content = %q", branchYAML)
	}

	// Branch tip SHA matches.
	out, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != res.SHA {
		t.Errorf("HEAD = %s, want %s", got, res.SHA)
	}
}

func TestPushBranch_RefusesTrackedRef(t *testing.T) {
	bare := newRemote(t, validYAML)
	src := Source{RepoURL: bare, Ref: "main"}
	_, err := PushBranch(context.Background(), src, validYAML2, "", "main", nil)
	if err == nil || !strings.Contains(err.Error(), "must start with") {
		t.Fatalf("expected branch prefix error, got %v", err)
	}
	_, err = PushBranch(context.Background(), src, validYAML2, "", "ownbase/agent/../main", nil)
	if err == nil {
		t.Fatal("expected invalid branch")
	}
}

func TestPushBranch_InvalidYAML(t *testing.T) {
	bare := newRemote(t, validYAML)
	src := Source{RepoURL: bare, Ref: "main"}
	_, err := PushBranch(context.Background(), src, "not: valid: config\n", "", "ownbase/agent/x", nil)
	if err == nil || !strings.Contains(err.Error(), "invalid ownbase.yaml") {
		t.Fatalf("expected invalid yaml, got %v", err)
	}
}

func TestPushBranch_NoChange(t *testing.T) {
	bare := newRemote(t, validYAML)
	src := Source{RepoURL: bare, Ref: "main"}
	_, err := PushBranch(context.Background(), src, validYAML, "", "ownbase/agent/same", nil)
	if err == nil || !strings.Contains(err.Error(), "no change") {
		t.Fatalf("expected no change, got %v", err)
	}
}

func TestNormalizeBranch(t *testing.T) {
	got, err := normalizeBranch("", "main")
	if err != nil || !strings.HasPrefix(got, BranchPrefix) {
		t.Fatalf("auto branch: %q %v", got, err)
	}
	if _, err := normalizeBranch("feature/x", "main"); err == nil {
		t.Fatal("expected prefix required")
	}
}
