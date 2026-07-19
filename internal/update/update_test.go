package update_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/update"
)

// ---------------------------------------------------------------------------
// HighestVersionTag
// ---------------------------------------------------------------------------

func TestHighestVersionTag_BasicSemver(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"two versions", []string{"v1.0.0", "v2.0.0"}, "v2.0.0"},
		{"reversed input", []string{"v2.0.0", "v1.0.0"}, "v2.0.0"},
		{"minor bump", []string{"v1.0.0", "v1.1.0", "v1.2.0"}, "v1.2.0"},
		{"patch bump", []string{"v1.0.1", "v1.0.0"}, "v1.0.1"},
		{"double-digit major", []string{"v2.0.0", "v10.0.0", "v9.0.0"}, "v10.0.0"},
		{"no v prefix", []string{"1.0.0", "2.0.0"}, "2.0.0"},
		{"single tag", []string{"v3.1.4"}, "v3.1.4"},
		{"empty slice", []string{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := update.HighestVersionTag(tc.tags)
			if got != tc.want {
				t.Errorf("HighestVersionTag(%v) = %q, want %q", tc.tags, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BumpRef — the YAML ref: edit function
// ---------------------------------------------------------------------------

func TestBumpRef_ReplacesExisting(t *testing.T) {
	yaml := `schema_version: v1
services:
  auth:
    repo: https://github.com/example/auth.git
    ref: v2.1.0
    port: 8080
`
	got, err := update.BumpRef(yaml, "auth", "v2.1.0", "v2.2.0")
	if err != nil {
		t.Fatalf("BumpRef: %v", err)
	}
	if !strings.Contains(got, "ref: v2.2.0") {
		t.Errorf("expected new ref in output:\n%s", got)
	}
	if strings.Contains(got, "ref: v2.1.0") {
		t.Errorf("old ref should be gone:\n%s", got)
	}
}

func TestBumpRef_InsertsWhenMissing(t *testing.T) {
	yaml := `schema_version: v1
services:
  auth:
    repo: https://github.com/example/auth.git
    port: 8080
  other:
    repo: https://github.com/example/other.git
`
	got, err := update.BumpRef(yaml, "auth", "", "v1.0.0")
	if err != nil {
		t.Fatalf("BumpRef: %v", err)
	}
	if !strings.Contains(got, "ref: v1.0.0") {
		t.Errorf("expected inserted ref:\n%s", got)
	}
	// "other" service should be untouched.
	if !strings.Contains(got, "repo: https://github.com/example/other.git") {
		t.Errorf("other service should be unchanged:\n%s", got)
	}
}

func TestBumpRef_InsertsWhenLastService(t *testing.T) {
	yaml := `schema_version: v1
services:
  auth:
    repo: https://github.com/example/auth.git`
	got, err := update.BumpRef(yaml, "auth", "", "v1.0.0")
	if err != nil {
		t.Fatalf("BumpRef: %v", err)
	}
	if !strings.Contains(got, "ref: v1.0.0") {
		t.Errorf("expected inserted ref for last service:\n%s", got)
	}
}

func TestBumpRef_ServiceNotFound(t *testing.T) {
	yaml := `schema_version: v1
services:
  auth:
    repo: https://github.com/example/auth.git
`
	_, err := update.BumpRef(yaml, "nonexistent", "", "v1.0.0")
	if err == nil {
		t.Error("expected error for nonexistent service, got nil")
	}
}

// ---------------------------------------------------------------------------
// BumpDigest — unchanged semantics, uses shared bumpField
// ---------------------------------------------------------------------------

func TestBumpDigest_ReplacesExisting(t *testing.T) {
	yaml := `schema_version: v1
services:
  forgejo:
    image: codeberg.org/forgejo/forgejo:10
    digest: sha256:aaabbb
    port: 3000
`
	got, err := update.BumpDigest(yaml, "forgejo", "sha256:aaabbb", "sha256:cccddd")
	if err != nil {
		t.Fatalf("BumpDigest: %v", err)
	}
	if !strings.Contains(got, "digest: sha256:cccddd") {
		t.Errorf("expected new digest in output:\n%s", got)
	}
	if strings.Contains(got, "sha256:aaabbb") {
		t.Errorf("old digest should be gone:\n%s", got)
	}
}

func TestBumpDigest_InsertsWhenMissing(t *testing.T) {
	yaml := `schema_version: v1
services:
  forgejo:
    image: codeberg.org/forgejo/forgejo:10
    port: 3000
  other:
    image: alpine:latest
`
	got, err := update.BumpDigest(yaml, "forgejo", "", "sha256:new123")
	if err != nil {
		t.Fatalf("BumpDigest: %v", err)
	}
	if !strings.Contains(got, "digest: sha256:new123") {
		t.Errorf("expected inserted digest:\n%s", got)
	}
	if !strings.Contains(got, "alpine:latest") {
		t.Errorf("other service should be unchanged:\n%s", got)
	}
}

func TestBumpDigest_ServiceNotFound(t *testing.T) {
	yaml := `schema_version: v1
services:
  forgejo:
    image: codeberg.org/forgejo/forgejo:10
`
	_, err := update.BumpDigest(yaml, "nonexistent", "", "sha256:new")
	if err == nil {
		t.Error("expected error for nonexistent service, got nil")
	}
}

// ---------------------------------------------------------------------------
// parseImageRef
// ---------------------------------------------------------------------------

func TestParseImageRef_WithRegistry(t *testing.T) {
	cases := []struct {
		input    string
		registry string
		repo     string
		tag      string
	}{
		{
			"codeberg.org/forgejo/forgejo:10",
			"codeberg.org", "forgejo/forgejo", "10",
		},
		{
			"docker.io/library/nginx:latest",
			"docker.io", "library/nginx", "latest",
		},
		{
			"ghcr.io/myorg/myapp:v1.2.3",
			"ghcr.io", "myorg/myapp", "v1.2.3",
		},
	}
	for _, tc := range cases {
		reg, repo, tag, err := update.ParseImageRef(tc.input)
		if err != nil {
			t.Errorf("ParseImageRef(%q): %v", tc.input, err)
			continue
		}
		if reg != tc.registry {
			t.Errorf("ParseImageRef(%q) registry = %q, want %q", tc.input, reg, tc.registry)
		}
		if repo != tc.repo {
			t.Errorf("ParseImageRef(%q) repo = %q, want %q", tc.input, repo, tc.repo)
		}
		if tag != tc.tag {
			t.Errorf("ParseImageRef(%q) tag = %q, want %q", tc.input, tag, tc.tag)
		}
	}
}

func TestParseImageRef_AlreadyDigested_Errors(t *testing.T) {
	_, _, _, err := update.ParseImageRef("nginx:latest@sha256:abc")
	if err == nil {
		t.Error("expected error for already-digested ref, got nil")
	}
}

// ---------------------------------------------------------------------------
// ServicesFromConfig — maps schema fields to serviceRef
// ---------------------------------------------------------------------------

func TestServicesFromConfig_KeyedByServiceName(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth":     {Repo: "https://github.com/example/auth.git", Ref: "v1.0.0"},
			"postgres": {Repo: "https://github.com/docker-library/postgres"},
		},
	}
	refs := update.ServicesFromConfig(oc)
	if len(refs) != 2 {
		t.Fatalf("want 2 service refs, got %d", len(refs))
	}
	// Each service's local bare repo is keyed by the service name.
	if refs["auth"].Source != "auth" {
		t.Errorf("auth.Source = %q, want auth", refs["auth"].Source)
	}
	if refs["postgres"].Source != "postgres" {
		t.Errorf("postgres.Source = %q, want postgres", refs["postgres"].Source)
	}
}

// ---------------------------------------------------------------------------
// ComputeDrift — local bare repo under a throwaway ReposDir
// ---------------------------------------------------------------------------

// newLocalDriftRepo creates a bare-ish repo at <reposDir>/<name> with a
// "v1.0.0"-tagged first commit on main, plus extraCommits additional commits
// and any extra tags, so tests can exercise commits-behind and newest-tag
// detection against a real local git repo instead of a fake HTTP server.
func newLocalDriftRepo(t *testing.T, reposDir, name string, extraCommits int, extraTags []string) {
	t.Helper()
	dir := filepath.Join(reposDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	writeAndCommit(t, dir, "v1.0.0 commit")
	runGit(t, dir, "tag", "v1.0.0")

	for i := 0; i < extraCommits; i++ {
		writeAndCommit(t, dir, "extra commit")
	}
	for _, tag := range extraTags {
		runGit(t, dir, "tag", tag)
	}
}

func writeAndCommit(t *testing.T, dir, msg string) {
	t.Helper()
	f := filepath.Join(dir, "file.txt")
	existing, _ := os.ReadFile(f)
	if err := os.WriteFile(f, append(existing, []byte(msg+"\n")...), 0o644); err != nil {
		t.Fatalf("write %s: %v", f, err)
	}
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-m", msg)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestComputeDrift_UpToDate(t *testing.T) {
	reposDir := t.TempDir()
	newLocalDriftRepo(t, reposDir, "auth", 0, nil)

	cfg := update.Config{ReposDir: reposDir}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Repo: "https://github.com/example/auth.git", Ref: "v1.0.0"},
		},
	})

	drift := update.ComputeDrift(t.Context(), cfg, services)
	if len(drift) != 1 {
		t.Fatalf("want 1 drift entry, got %d", len(drift))
	}
	d := drift[0]
	if d.Service != "auth" {
		t.Errorf("Service = %q, want auth", d.Service)
	}
	if d.Branch != "main" {
		t.Errorf("Branch = %q, want main", d.Branch)
	}
	if d.CommitsBehind != 0 {
		t.Errorf("CommitsBehind = %d, want 0", d.CommitsBehind)
	}
	if d.NewestTag != "v1.0.0" {
		t.Errorf("NewestTag = %q, want v1.0.0", d.NewestTag)
	}
	if !d.UpToDate {
		t.Error("UpToDate should be true")
	}
}

func TestComputeDrift_Behind(t *testing.T) {
	reposDir := t.TempDir()
	newLocalDriftRepo(t, reposDir, "auth", 5, []string{"v1.1.0"})
	pinnedRef := "v1.0.0"

	cfg := update.Config{ReposDir: reposDir}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Repo: "https://github.com/example/auth.git", Ref: pinnedRef},
		},
	})

	drift := update.ComputeDrift(t.Context(), cfg, services)
	if len(drift) != 1 {
		t.Fatalf("want 1 drift entry, got %d", len(drift))
	}
	d := drift[0]
	if d.CommitsBehind != 5 {
		t.Errorf("CommitsBehind = %d, want 5", d.CommitsBehind)
	}
	if d.NewestTag != "v1.1.0" {
		t.Errorf("NewestTag = %q, want v1.1.0", d.NewestTag)
	}
	if d.UpToDate {
		t.Error("UpToDate should be false when behind")
	}
}

func TestComputeDrift_SkipsBlankRef(t *testing.T) {
	// Services with no ref: are not included in drift (they're pending resolution).
	reposDir := t.TempDir()
	newLocalDriftRepo(t, reposDir, "auth", 0, nil)

	cfg := update.Config{ReposDir: reposDir}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Repo: "https://github.com/example/auth.git"}, // no ref
		},
	})

	drift := update.ComputeDrift(t.Context(), cfg, services)
	if len(drift) != 0 {
		t.Errorf("want 0 drift entries for blank-ref service, got %d", len(drift))
	}
}

func TestComputeDrift_CommitSHAAlwaysUpToDate(t *testing.T) {
	// A full 40-char commit SHA is already maximally pinned. Even though a
	// newer semver tag exists locally (which would never equal a raw SHA),
	// the service must not be reported as out of date.
	reposDir := t.TempDir()
	newLocalDriftRepo(t, reposDir, "auth", 5, []string{"v1.1.0"})
	sha := strings.Repeat("a", 40)

	cfg := update.Config{ReposDir: reposDir}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Repo: "https://github.com/example/auth.git", Ref: sha},
		},
	})

	drift := update.ComputeDrift(t.Context(), cfg, services)
	if len(drift) != 1 {
		t.Fatalf("want 1 drift entry, got %d", len(drift))
	}
	d := drift[0]
	if d.Ref != sha {
		t.Errorf("Ref = %q, want %q", d.Ref, sha)
	}
	if !d.UpToDate {
		t.Error("UpToDate should be true for a commit-SHA ref, regardless of newer tags")
	}
	if d.CommitsBehind != 0 {
		t.Errorf("CommitsBehind = %d, want 0 for a commit-SHA ref", d.CommitsBehind)
	}
}

func TestComputeDrift_MissingRepoIsHarmless(t *testing.T) {
	// A repo not yet cloned locally (e.g. a brand-new repo: service before
	// its first EnsureRepo) must not error — resolveDefaultBranchHead and
	// fetchLatestSourceRef both degrade to empty results.
	cfg := update.Config{ReposDir: t.TempDir()}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Repo: "https://github.com/example/auth.git", Ref: "v1.0.0"},
		},
	})
	drift := update.ComputeDrift(t.Context(), cfg, services)
	if len(drift) != 1 {
		t.Fatalf("want 1 drift entry, got %d", len(drift))
	}
	d := drift[0]
	if d.Branch != "" {
		t.Errorf("Branch = %q, want empty for a missing repo", d.Branch)
	}
	if d.CommitsBehind != 0 {
		t.Errorf("CommitsBehind = %d, want 0 for a missing repo", d.CommitsBehind)
	}
}

// ---------------------------------------------------------------------------
// Taxonomy: update.pin_ref is in the schema taxonomy
// ---------------------------------------------------------------------------

func TestTaxonomy_UpdatePinRefExists(t *testing.T) {
	a, err := schema.NewAction(schema.ActionUpdatePinRef, "auth")
	if err != nil {
		t.Fatalf("update.pin_ref not in taxonomy: %v", err)
	}
	if a.DefaultTier != schema.TierAutonomous {
		t.Errorf("update.pin_ref should be TierAutonomous, got %s", a.DefaultTier)
	}
}

func TestTaxonomy_BuildImageActionExists(t *testing.T) {
	a, err := schema.NewAction(schema.ActionBuildImage, "auth")
	if err != nil {
		t.Fatalf("build.image not in taxonomy: %v", err)
	}
	if a.DefaultTier != schema.TierAutonomous {
		t.Errorf("build.image should be TierAutonomous, got %s", a.DefaultTier)
	}
}

// ---------------------------------------------------------------------------
// BumpDigest — multi-field round trip (real-shaped YAML)
// ---------------------------------------------------------------------------

func TestBumpDigest_RoundTripWithHealthProbeAndSiblingService(t *testing.T) {
	original := `schema_version: v1
services:
  caddy-core:
    image: docker.io/library/caddy:2
    digest: sha256:old111
    port: 3000
    domain: git.example.com
    health_probe:
      http: /api/healthz
  myapp:
    repo: https://github.com/example/myapp.git
    port: 8080
`
	got, err := update.BumpDigest(original, "caddy-core", "sha256:old111", "sha256:new222")
	if err != nil {
		t.Fatalf("BumpDigest: %v", err)
	}
	if !strings.Contains(got, "digest: sha256:new222") {
		t.Errorf("new digest not in output:\n%s", got)
	}
	if !strings.Contains(got, "repo: https://github.com/example/myapp.git") {
		t.Errorf("myapp service missing from output:\n%s", got)
	}
	if !strings.Contains(got, "http: /api/healthz") {
		t.Errorf("health_probe missing from output:\n%s", got)
	}
}

func TestTaxonomy_OldUpdateActionsRemoved(t *testing.T) {
	// update.open_pr and update.merge should no longer exist.
	if _, err := schema.NewAction("update.open_pr", "auth"); err == nil {
		t.Error("update.open_pr should not be in taxonomy (removed)")
	}
	if _, err := schema.NewAction("update.merge", "auth"); err == nil {
		t.Error("update.merge should not be in taxonomy (removed)")
	}
}
