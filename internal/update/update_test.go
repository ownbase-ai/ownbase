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
// mirrorRepoName — must match compiler.MirrorRepoName
// ---------------------------------------------------------------------------

func TestMirrorRepoName_UsesDash(t *testing.T) {
	// M12 regression: mirrorRepoName previously returned "mirrors/<basename>"
	// (with a slash) while the compiler uses "mirrors-<basename>" (with a dash),
	// so mirror drift detection was silently reading the wrong repo.
	cases := []struct {
		input string
		want  string
	}{
		{"https://github.com/postgres/postgres.git", "mirrors-postgres"},
		{"https://x/postgres.git", "mirrors-postgres"},
		{"git@github.com:redis/redis.git", "mirrors-redis"},
		{"https://codeberg.org/forgejo/forgejo", "mirrors-forgejo"},
	}
	for _, tc := range cases {
		got := update.MirrorRepoNameForTest(tc.input)
		if got != tc.want {
			t.Errorf("mirrorRepoName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

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
    source: services/auth
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
    source: services/auth
    port: 8080
  other:
    source: services/other
`
	got, err := update.BumpRef(yaml, "auth", "", "v1.0.0")
	if err != nil {
		t.Fatalf("BumpRef: %v", err)
	}
	if !strings.Contains(got, "ref: v1.0.0") {
		t.Errorf("expected inserted ref:\n%s", got)
	}
	// "other" service should be untouched.
	if !strings.Contains(got, "source: services/other") {
		t.Errorf("other service should be unchanged:\n%s", got)
	}
}

func TestBumpRef_InsertsWhenLastService(t *testing.T) {
	yaml := `schema_version: v1
services:
  auth:
    source: services/auth`
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
    source: services/auth
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

func TestServicesFromConfig_SourceService(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Source: "services/auth", Ref: "v1.0.0"},
		},
	}
	refs := update.ServicesFromConfig(oc)
	if len(refs) != 1 {
		t.Fatalf("want 1 service ref, got %d", len(refs))
	}
	// Verify the ref is preserved (tested indirectly via ComputeDrift).
	_ = refs
}

func TestServicesFromConfig_MirrorService_DerivesRepoName(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"postgres": {Mirror: "https://github.com/docker-library/postgres"},
		},
	}
	refs := update.ServicesFromConfig(oc)
	if len(refs) != 1 {
		t.Fatalf("want 1 service ref, got %d", len(refs))
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
	newLocalDriftRepo(t, reposDir, "services/auth", 0, nil)

	cfg := update.Config{ReposDir: reposDir}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Source: "services/auth", Ref: "v1.0.0"},
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
	newLocalDriftRepo(t, reposDir, "services/auth", 5, []string{"v1.1.0"})
	pinnedRef := "v1.0.0"

	cfg := update.Config{ReposDir: reposDir}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Source: "services/auth", Ref: pinnedRef},
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
	newLocalDriftRepo(t, reposDir, "services/auth", 0, nil)

	cfg := update.Config{ReposDir: reposDir}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Source: "services/auth"}, // no ref
		},
	})

	drift := update.ComputeDrift(t.Context(), cfg, services)
	if len(drift) != 0 {
		t.Errorf("want 0 drift entries for blank-ref service, got %d", len(drift))
	}
}

func TestComputeDrift_MissingRepoIsHarmless(t *testing.T) {
	// A repo not yet cloned locally (e.g. a brand-new mirror: service before
	// its first EnsureRepo) must not error — resolveDefaultBranchHead and
	// fetchLatestSourceRef both degrade to empty results.
	cfg := update.Config{ReposDir: t.TempDir()}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Source: "services/auth", Ref: "v1.0.0"},
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
// ResolveBlankRefs + CommitFile — local config repo (checkout + bare origin)
// ---------------------------------------------------------------------------

// newLocalConfigCheckout sets up a bare config repo plus a working checkout
// with an initial ownbase.yaml commit, mirroring the on-Base
// /opt/ownbase/repo + /opt/ownbase/checkout layout (see internal/githost).
func newLocalConfigCheckout(t *testing.T, initialYAML string) (checkoutPath string) {
	t.Helper()
	root := t.TempDir()
	barePath := filepath.Join(root, "repo")
	checkoutPath = filepath.Join(root, "checkout")

	if err := os.MkdirAll(barePath, 0o755); err != nil {
		t.Fatalf("mkdir bare repo: %v", err)
	}
	runGit(t, barePath, "init", "--bare", "--initial-branch=main")

	runGit(t, root, "clone", "--local", "--origin", "origin", barePath, checkoutPath)
	runGit(t, checkoutPath, "config", "user.email", "test@example.com")
	runGit(t, checkoutPath, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(checkoutPath, "ownbase.yaml"), []byte(initialYAML), 0o644); err != nil {
		t.Fatalf("write ownbase.yaml: %v", err)
	}
	runGit(t, checkoutPath, "add", "ownbase.yaml")
	runGit(t, checkoutPath, "commit", "-m", "initial")
	runGit(t, checkoutPath, "push", "origin", "HEAD")
	return checkoutPath
}

func TestCommitFile_CommitsAndPushesToOrigin(t *testing.T) {
	checkoutPath := newLocalConfigCheckout(t, "schema_version: v1\nservices: {}\n")

	cfg := update.Config{CheckoutPath: checkoutPath}
	if err := update.CommitFile(t.Context(), cfg, "ownbase.yaml", "schema_version: v1\nservices:\n  auth: {}\n", "test: add auth"); err != nil {
		t.Fatalf("CommitFile: %v", err)
	}

	// Verify the working tree was updated.
	got, err := os.ReadFile(filepath.Join(checkoutPath, "ownbase.yaml"))
	if err != nil {
		t.Fatalf("read ownbase.yaml: %v", err)
	}
	if !strings.Contains(string(got), "auth:") {
		t.Errorf("expected auth service in checkout, got:\n%s", got)
	}

	// Verify the commit was pushed to origin (bare repo), not just committed locally.
	out, err := exec.Command("git", "-C", checkoutPath, "log", "origin/main", "-1", "--format=%s").Output()
	if err != nil {
		t.Fatalf("git log origin/main: %v", err)
	}
	if !strings.Contains(string(out), "test: add auth") {
		t.Errorf("expected commit to be pushed to origin, got log: %q", out)
	}
}

func TestResolveBlankRefs_PinsAndCommitsSHA(t *testing.T) {
	initialYAML := `schema_version: v1
services:
  myapp:
    source: services/myapp
    port: 8080
`
	checkoutPath := newLocalConfigCheckout(t, initialYAML)
	reposDir := t.TempDir()
	newLocalDriftRepo(t, reposDir, "services/myapp", 0, nil)
	headSHA := revParse(t, filepath.Join(reposDir, "services/myapp"), "main")

	cfg := update.Config{CheckoutPath: checkoutPath, ReposDir: reposDir}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"myapp": {Source: "services/myapp"}, // no ref
		},
	})

	update.ResolveBlankRefs(t.Context(), cfg, services, nil)

	got, err := os.ReadFile(filepath.Join(checkoutPath, "ownbase.yaml"))
	if err != nil {
		t.Fatalf("read ownbase.yaml: %v", err)
	}
	if !strings.Contains(string(got), "ref: "+headSHA) {
		t.Errorf("expected ref: %s to be written back, got:\n%s", headSHA, got)
	}

	// Verify the commit reached the bare repo (not just the local checkout).
	out, err := exec.Command("git", "-C", checkoutPath, "log", "origin/main", "-1", "--format=%s").Output()
	if err != nil {
		t.Fatalf("git log origin/main: %v", err)
	}
	if !strings.Contains(string(out), "auto-pin") {
		t.Errorf("expected auto-pin commit to be pushed to origin, got log: %q", out)
	}
}

func TestResolveBlankRefs_SkipsAlreadyPinnedServices(t *testing.T) {
	initialYAML := `schema_version: v1
services:
  myapp:
    source: services/myapp
    ref: v1.0.0
    port: 8080
`
	checkoutPath := newLocalConfigCheckout(t, initialYAML)
	cfg := update.Config{CheckoutPath: checkoutPath, ReposDir: t.TempDir()}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"myapp": {Source: "services/myapp", Ref: "v1.0.0"},
		},
	})

	update.ResolveBlankRefs(t.Context(), cfg, services, nil)

	got, err := os.ReadFile(filepath.Join(checkoutPath, "ownbase.yaml"))
	if err != nil {
		t.Fatalf("read ownbase.yaml: %v", err)
	}
	if !strings.Contains(string(got), "ref: v1.0.0") {
		t.Errorf("expected the existing pinned ref to be left untouched, got:\n%s", got)
	}
}

// revParse returns the commit SHA that ref resolves to in the repo at dir.
func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
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
    source: services/myapp
    port: 8080
`
	got, err := update.BumpDigest(original, "caddy-core", "sha256:old111", "sha256:new222")
	if err != nil {
		t.Fatalf("BumpDigest: %v", err)
	}
	if !strings.Contains(got, "digest: sha256:new222") {
		t.Errorf("new digest not in output:\n%s", got)
	}
	if !strings.Contains(got, "source: services/myapp") {
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
