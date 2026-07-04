package update_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
	"github.com/ownbase/ownbase/internal/update"
)

// ---------------------------------------------------------------------------
// mirrorForgejoPath — must match compiler and githost
// ---------------------------------------------------------------------------

func TestMirrorForgejoPath_UsesDash(t *testing.T) {
	// M12 regression: mirrorForgejoPath previously returned "mirrors/<basename>"
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
		got := update.MirrorForgejoPathForTest(tc.input)
		if got != tc.want {
			t.Errorf("mirrorForgejoPath(%q) = %q, want %q", tc.input, got, tc.want)
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

func TestServicesFromConfig_MirrorService_DerivesForgejoPah(t *testing.T) {
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
// ComputeDrift — fake Forgejo server
// ---------------------------------------------------------------------------

// newFakeForgejoServer builds an httptest.Server that fakes the three
// Forgejo API endpoints ComputeDrift uses:
//   - GET /api/v1/repos/{o}/{r}         → {"default_branch":"main"}
//   - GET /api/v1/repos/{o}/{r}/branches/main → {"commit":{"id":"<headSHA>"}}
//   - GET /api/v1/repos/{o}/{r}/compare/{base}...{head} → {"total_commits":<n>}
//   - GET /api/v1/repos/{o}/{r}/tags    → [{"name":"v1.1.0"}]
func newFakeForgejoServer(headSHA string, commitsBehind int, tags []string) *httptest.Server {
	mux := http.NewServeMux()

	// Repo info.
	mux.HandleFunc("/api/v1/repos/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/repos/"), "/")
		// /api/v1/repos/{o}/{r}
		if len(parts) == 2 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
			return
		}
		// /api/v1/repos/{o}/{r}/branches/{branch}
		if len(parts) == 4 && parts[2] == "branches" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"commit": map[string]string{"id": headSHA},
			})
			return
		}
		// /api/v1/repos/{o}/{r}/compare/{base}...{head}
		if len(parts) == 4 && parts[2] == "compare" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"total_commits": commitsBehind})
			return
		}
		// /api/v1/repos/{o}/{r}/tags
		if len(parts) == 3 && parts[2] == "tags" {
			tagObjs := make([]map[string]string, len(tags))
			for i, t := range tags {
				tagObjs[i] = map[string]string{"name": t}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(tagObjs)
			return
		}
		http.NotFound(w, r)
	})

	return httptest.NewServer(mux)
}

func TestComputeDrift_UpToDate(t *testing.T) {
	headSHA := "abc123def456abc123def456abc123def456abc1"
	srv := newFakeForgejoServer(headSHA, 0, []string{"v1.0.0"})
	defer srv.Close()

	cfg := update.Config{
		ForgejoURL:   srv.URL,
		ForgejoToken: "fake",
		ForgejoUser:  "ownbase",
	}
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
	headSHA := "abc123def456abc123def456abc123def456abc1"
	srv := newFakeForgejoServer(headSHA, 5, []string{"v1.1.0"})
	defer srv.Close()

	cfg := update.Config{
		ForgejoURL:   srv.URL,
		ForgejoToken: "fake",
		ForgejoUser:  "ownbase",
	}
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
	headSHA := "abc123def456abc123def456abc123def456abc1"
	srv := newFakeForgejoServer(headSHA, 0, []string{})
	defer srv.Close()

	cfg := update.Config{
		ForgejoURL:   srv.URL,
		ForgejoToken: "fake",
		ForgejoUser:  "ownbase",
	}
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

func TestComputeDrift_NoTokenReturnsEmpty(t *testing.T) {
	cfg := update.Config{ForgejoToken: ""}
	services := update.ServicesFromConfig(&schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Source: "services/auth", Ref: "v1.0.0"},
		},
	})
	drift := update.ComputeDrift(t.Context(), cfg, services)
	if len(drift) != 0 {
		t.Errorf("want 0 drift entries without token, got %d", len(drift))
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

func TestTaxonomy_OldUpdateActionsRemoved(t *testing.T) {
	// update.open_pr and update.merge should no longer exist.
	if _, err := schema.NewAction("update.open_pr", "auth"); err == nil {
		t.Error("update.open_pr should not be in taxonomy (removed)")
	}
	if _, err := schema.NewAction("update.merge", "auth"); err == nil {
		t.Error("update.merge should not be in taxonomy (removed)")
	}
}
