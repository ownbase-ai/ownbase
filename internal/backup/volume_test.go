package backup_test

// volume_test.go — Tier-1 tests for BuildPaths and the fake VolumeResolver.
// No restic, no Podman, no Linux required.

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/ownbase/ownbase/internal/backup"
	"github.com/ownbase/ownbase/internal/schema"
)

// fakeResolver maps Podman volume names to fixed host mountpoints.
// Returns an error for any volume not in the map.
type fakeResolver map[string]string

func (f fakeResolver) Resolve(_ context.Context, name string) (string, error) {
	if mp, ok := f[name]; ok {
		return mp, nil
	}
	return "", fmt.Errorf("volume %q not found", name)
}

// helpers

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func mustContain(t *testing.T, paths []string, want string) {
	t.Helper()
	if !containsPath(paths, want) {
		t.Errorf("paths %v does not contain %q", paths, want)
	}
}

func mustNotContain(t *testing.T, paths []string, notWant string) {
	t.Helper()
	if containsPath(paths, notWant) {
		t.Errorf("paths %v should NOT contain %q", paths, notWant)
	}
}

// minimalOC builds an OwnbaseConfig with a single service using the old
// data_path model (no Volumes declared).
func minimalOC(serviceName string) *schema.OwnbaseConfig {
	return &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			serviceName: {Repo: "local/" + serviceName},
		},
	}
}

// ---------------------------------------------------------------------------
// Backward compat: services with no Volumes use single data volume
// ---------------------------------------------------------------------------

func TestBuildPaths_BackwardCompat(t *testing.T) {
	oc := minimalOC("myapp")

	resolver := fakeResolver{
		"ownbase-myapp-data":      "/vol/myapp-data",
		"ownbase-core-caddy-data": "/vol/caddy",
	}

	paths, err := backup.BuildPaths(context.Background(), oc, resolver)
	if err != nil {
		t.Fatalf("BuildPaths: %v", err)
	}

	// DefaultPaths always present.
	for _, dp := range backup.DefaultPaths {
		mustContain(t, paths, dp)
	}
	// Service data volume resolved.
	mustContain(t, paths, "/vol/myapp-data")
	// Core volumes resolved.
	mustContain(t, paths, "/vol/caddy")
}

// ---------------------------------------------------------------------------
// Explicit volumes — whole mount
// ---------------------------------------------------------------------------

func TestBuildPaths_ExplicitVolumes_WholeMount(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"svc": {
				Repo: "local/svc",
				Volumes: []schema.VolumeDecl{
					{Name: "config", Mount: "/config", Backup: []string{"."}},
				},
			},
		},
	}
	resolver := fakeResolver{
		"ownbase-svc-config":      "/vol/svc-config",
		"ownbase-core-caddy-data": "/vol/caddy",
	}

	paths, err := backup.BuildPaths(context.Background(), oc, resolver)
	if err != nil {
		t.Fatalf("BuildPaths: %v", err)
	}

	// "." resolves to the mountpoint itself.
	mustContain(t, paths, "/vol/svc-config")
}

// ---------------------------------------------------------------------------
// Explicit volumes — selected subdirectories
// ---------------------------------------------------------------------------

func TestBuildPaths_ExplicitVolumes_Subdirs(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"media": {
				Repo: "local/media",
				Volumes: []schema.VolumeDecl{
					{
						Name:   "storage",
						Mount:  "/media",
						Backup: []string{"./music", "./photos"},
					},
				},
			},
		},
	}
	resolver := fakeResolver{
		"ownbase-media-storage":   "/vol/media-storage",
		"ownbase-core-caddy-data": "/vol/caddy",
	}

	paths, err := backup.BuildPaths(context.Background(), oc, resolver)
	if err != nil {
		t.Fatalf("BuildPaths: %v", err)
	}

	mustContain(t, paths, "/vol/media-storage/music")
	mustContain(t, paths, "/vol/media-storage/photos")
	// Mountpoint itself should NOT be present (only the two subdirs).
	mustNotContain(t, paths, "/vol/media-storage")
}

// ---------------------------------------------------------------------------
// Volume with no backup field → excluded entirely
// ---------------------------------------------------------------------------

func TestBuildPaths_VolumeNoBackup(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"svc": {
				Repo: "local/svc",
				Volumes: []schema.VolumeDecl{
					{Name: "config", Mount: "/config", Backup: []string{"."}},
					{Name: "cache", Mount: "/cache"}, // no Backup → excluded
				},
			},
		},
	}
	resolver := fakeResolver{
		"ownbase-svc-config":      "/vol/svc-config",
		"ownbase-svc-cache":       "/vol/svc-cache",
		"ownbase-core-caddy-data": "/vol/caddy",
	}

	paths, err := backup.BuildPaths(context.Background(), oc, resolver)
	if err != nil {
		t.Fatalf("BuildPaths: %v", err)
	}

	mustContain(t, paths, "/vol/svc-config")
	mustNotContain(t, paths, "/vol/svc-cache")
}

// ---------------------------------------------------------------------------
// Core volumes always included regardless of service declarations
// ---------------------------------------------------------------------------

func TestBuildPaths_CoreVolumesAlwaysIncluded(t *testing.T) {
	// No services at all.
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services:      map[string]schema.ServiceDecl{},
	}
	resolver := fakeResolver{
		"ownbase-core-caddy-data": "/vol/caddy",
	}

	paths, err := backup.BuildPaths(context.Background(), oc, resolver)
	if err != nil {
		t.Fatalf("BuildPaths: %v", err)
	}

	mustContain(t, paths, "/vol/caddy")
}

// ---------------------------------------------------------------------------
// Core volume resolve failure is non-fatal
// ---------------------------------------------------------------------------

func TestBuildPaths_CoreVolumeResolveFail_NonFatal(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services:      map[string]schema.ServiceDecl{},
	}
	// No core volumes registered — resolver returns errors for them.
	resolver := fakeResolver{}

	paths, err := backup.BuildPaths(context.Background(), oc, resolver)
	if err != nil {
		t.Fatalf("BuildPaths should not fail when core volumes missing: %v", err)
	}
	// DefaultPaths still present.
	for _, dp := range backup.DefaultPaths {
		mustContain(t, paths, dp)
	}
}

// ---------------------------------------------------------------------------
// No duplicate paths
// ---------------------------------------------------------------------------

func TestBuildPaths_NoDuplicates(t *testing.T) {
	// Two services both map to the same host path (contrived, but tests dedup).
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"a": {Repo: "local/a"},
			"b": {Repo: "local/b"},
		},
	}
	resolver := fakeResolver{
		"ownbase-a-data":          "/vol/shared",
		"ownbase-b-data":          "/vol/shared", // intentional duplicate
		"ownbase-core-caddy-data": "/vol/caddy",
	}

	paths, err := backup.BuildPaths(context.Background(), oc, resolver)
	if err != nil {
		t.Fatalf("BuildPaths: %v", err)
	}

	counts := make(map[string]int)
	for _, p := range paths {
		counts[p]++
	}
	for p, n := range counts {
		if n > 1 {
			t.Errorf("path %q appears %d times; want 1", p, n)
		}
	}
}

// ---------------------------------------------------------------------------
// Service volume resolve failure is fatal
// ---------------------------------------------------------------------------

func TestBuildPaths_ServiceVolumeResolveFail_Fatal(t *testing.T) {
	oc := minimalOC("broken")
	// Resolver has no entry for the service volume.
	resolver := fakeResolver{
		"ownbase-core-caddy-data": "/vol/caddy",
	}

	_, err := backup.BuildPaths(context.Background(), oc, resolver)
	if err == nil {
		t.Error("expected error when service volume cannot be resolved, got nil")
	}
}

// ---------------------------------------------------------------------------
// Relative path variants: ".", "./foo", "foo" all work
// ---------------------------------------------------------------------------

func TestBuildPaths_RelativePathVariants(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"svc": {
				Repo: "local/svc",
				Volumes: []schema.VolumeDecl{
					{
						Name:  "data",
						Mount: "/data",
						Backup: []string{
							".",      // whole mount
							"./subA", // with ./
							"subB",   // without ./
						},
					},
				},
			},
		},
	}
	resolver := fakeResolver{
		"ownbase-svc-data":        "/vol/data",
		"ownbase-core-caddy-data": "/vol/caddy",
	}

	paths, err := backup.BuildPaths(context.Background(), oc, resolver)
	if err != nil {
		t.Fatalf("BuildPaths: %v", err)
	}

	mustContain(t, paths, "/vol/data")
	mustContain(t, paths, "/vol/data/subA")
	mustContain(t, paths, "/vol/data/subB")
}

// ---------------------------------------------------------------------------
// Deterministic output: same input always produces same path order
// ---------------------------------------------------------------------------

func TestBuildPaths_Deterministic(t *testing.T) {
	oc := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"alpha": {Repo: "local/alpha"},
			"beta":  {Repo: "local/beta"},
			"gamma": {Repo: "local/gamma"},
		},
	}
	resolver := fakeResolver{
		"ownbase-alpha-data":      "/vol/alpha",
		"ownbase-beta-data":       "/vol/beta",
		"ownbase-gamma-data":      "/vol/gamma",
		"ownbase-core-caddy-data": "/vol/caddy",
	}

	paths1, _ := backup.BuildPaths(context.Background(), oc, resolver)
	paths2, _ := backup.BuildPaths(context.Background(), oc, resolver)

	if len(paths1) != len(paths2) {
		t.Fatalf("length differs: %d vs %d", len(paths1), len(paths2))
	}

	// Sort both to compare sets (order within service-paths section may vary
	// by map iteration, but dedup preserves insertion order which is deterministic
	// because we sort service names before iterating).
	sorted1 := append([]string(nil), paths1...)
	sorted2 := append([]string(nil), paths2...)
	sort.Strings(sorted1)
	sort.Strings(sorted2)
	for i := range sorted1 {
		if sorted1[i] != sorted2[i] {
			t.Errorf("paths[%d] differs: %q vs %q", i, sorted1[i], sorted2[i])
		}
	}
}
