package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLooksLikeSHA(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{strings.Repeat("a", 40), true},
		{strings.Repeat("F", 40), true},
		{strings.Repeat("a", 39), false},
		{strings.Repeat("a", 41), false},
		{"v1.2.3", false},
		{"main", false},
		{strings.Repeat("g", 40), false}, // non-hex
	}
	for _, tc := range cases {
		if got := looksLikeSHA(tc.in); got != tc.want {
			t.Errorf("looksLikeSHA(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestShortSHA(t *testing.T) {
	sha := strings.Repeat("a", 40)
	if got := shortSHA(sha); got != sha[:12] {
		t.Errorf("shortSHA(sha) = %q, want %q", got, sha[:12])
	}
	if got := shortSHA("v2.0.0"); got != "v2.0.0" {
		t.Errorf("shortSHA(tag) = %q, want v2.0.0 (unchanged)", got)
	}
}

// gitInit creates a repo at dir with one commit on main tagged v1.0.0, and
// returns the HEAD commit SHA.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestResolveRemoteRef(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "--initial-branch=main")
	gitRun(t, dir, "config", "user.email", "t@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "f.txt")
	gitRun(t, dir, "commit", "-m", "initial")
	gitRun(t, dir, "tag", "v1.0.0")

	headSHA := revParseHead(t, dir)

	// Branch resolves to HEAD.
	if got, err := resolveRemoteRef(dir, "main"); err != nil || got != headSHA {
		t.Errorf("resolveRemoteRef(main) = %q, %v; want %q", got, err, headSHA)
	}
	// Lightweight tag resolves to the same commit.
	if got, err := resolveRemoteRef(dir, "v1.0.0"); err != nil || got != headSHA {
		t.Errorf("resolveRemoteRef(v1.0.0) = %q, %v; want %q", got, err, headSHA)
	}
	// A full SHA that ls-remote can't look up is accepted as-is.
	fakeSHA := strings.Repeat("a", 40)
	if got, err := resolveRemoteRef(dir, fakeSHA); err != nil || got != fakeSHA {
		t.Errorf("resolveRemoteRef(sha) = %q, %v; want %q", got, err, fakeSHA)
	}
	// An unknown non-SHA ref errors.
	if _, err := resolveRemoteRef(dir, "does-not-exist"); err == nil {
		t.Error("expected error for unknown ref")
	}
	// Empty repo URL errors.
	if _, err := resolveRemoteRef("", "main"); err == nil {
		t.Error("expected error for empty repo URL")
	}
}

func revParseHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}
