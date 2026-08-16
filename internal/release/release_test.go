package release_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/release"
)

func TestIsReleaseTag(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"v0.3.3", true},
		{"v1.0.0", true},
		{"v10.2.3", true},
		{"dev", false},
		{"v0.3.3-dev", false},
		{"v0.3.3-27-gabc", false},
		{"v0.3.3-rc.1", false},
		{"1.0.0", true}, // app stamps without leading v
		{"0.5.0", true},
		{"", false},
		{"latest", false},
	}
	for _, tc := range cases {
		if got := release.IsReleaseTag(tc.v); got != tc.want {
			t.Errorf("IsReleaseTag(%q) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

func TestAssess(t *testing.T) {
	cases := []struct {
		name              string
		current, latest   string
		wantStatus        release.Status
		wantGuideNonEmpty bool
	}{
		{"current", "v0.4.0", "v0.4.0", release.StatusCurrent, false},
		{"behind", "v0.3.0", "v0.4.0", release.StatusBehind, true},
		{"ahead", "v0.5.0", "v0.4.0", release.StatusAhead, false},
		{"dev", "dev", "v0.4.0", release.StatusDev, false},
		{"unknown latest", "v0.4.0", "", release.StatusUnknown, false},
		{"unknown current shape", "v0.4.0-rc1", "v0.4.0", release.StatusDev, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := release.Assess(release.ComponentCLI, tc.current, tc.latest)
			if c.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", c.Status, tc.wantStatus)
			}
			if tc.wantGuideNonEmpty && c.Guide == "" {
				t.Error("expected a guide command")
			}
			if !tc.wantGuideNonEmpty && c.Guide != "" {
				t.Errorf("unexpected guide %q", c.Guide)
			}
		})
	}
}

func TestAssessSkew(t *testing.T) {
	if s := release.AssessSkew("v0.5.0", "v0.4.0", "mybase"); s == nil || s.Direction != "cli_ahead" {
		t.Fatalf("cli_ahead: got %+v", s)
	} else if !strings.Contains(s.Guide, "self-update mybase") {
		t.Errorf("guide = %q", s.Guide)
	}
	if s := release.AssessSkew("v0.3.0", "v0.4.0", "mybase"); s == nil || s.Direction != "daemon_ahead" {
		t.Fatalf("daemon_ahead: got %+v", s)
	} else if !strings.Contains(s.Guide, "brew upgrade") {
		t.Errorf("guide = %q", s.Guide)
	}
	if s := release.AssessSkew("v0.4.0", "v0.4.0", "mybase"); s != nil {
		t.Errorf("equal: got %+v", s)
	}
	if s := release.AssessSkew("dev", "v0.4.0", "mybase"); s != nil {
		t.Errorf("dev cli: got %+v", s)
	}
	if s := release.AssessSkew("v0.4.0", "dev", "mybase"); s != nil {
		t.Errorf("dev daemon: got %+v", s)
	}
}

func TestBuildReport(t *testing.T) {
	snap := release.Snapshot{
		Manifest: release.Manifest{
			Schema: 1,
			Components: map[string]release.ComponentVersion{
				"cli":    {Version: "v0.5.0"},
				"app":    {Version: "v0.5.0"},
				"daemon": {Version: "v0.5.0"},
			},
		},
		Source: "fixture",
	}
	r := release.BuildReport(release.Running{
		CLI:    "v0.4.0",
		App:    "0.4.0", // app stamps without leading v — NormalizeTag accepts it
		Daemon: "v0.4.0",
		Base:   "demo",
	}, snap)
	if len(r.Components) != 3 {
		t.Fatalf("components = %d, want 3: %+v", len(r.Components), r.Components)
	}
	for _, c := range r.Components {
		if c.Status != release.StatusBehind {
			t.Errorf("%s status = %q, want behind", c.Name, c.Status)
		}
		if c.Current != "v0.4.0" {
			t.Errorf("%s current = %q, want v0.4.0", c.Name, c.Current)
		}
	}
	// No skew when CLI == daemon.
	if r.Skew != nil {
		t.Errorf("skew = %+v, want nil", r.Skew)
	}

	r2 := release.BuildReport(release.Running{
		CLI: "v0.5.0", App: "0.4.0", Daemon: "v0.4.0", Base: "demo",
	}, snap)
	if r2.Skew == nil || r2.Skew.Direction != "cli_ahead" {
		t.Errorf("skew = %+v, want cli_ahead", r2.Skew)
	}
}

func TestFetch_NetworkAndCache(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest.json" {
			http.NotFound(w, r)
			return
		}
		hits++
		_ = json.NewEncoder(w).Encode(release.Manifest{
			Schema: 1,
			Components: map[string]release.ComponentVersion{
				"cli": {Version: "v1.2.3"},
			},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	cache := filepath.Join(dir, "release.json")
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)

	opts := release.FetchOptions{
		BaseURL:   srv.URL,
		CachePath: cache,
		Now:       func() time.Time { return now },
		TTL:       time.Hour,
	}

	snap := release.Fetch(context.Background(), opts)
	if snap.Err != "" {
		t.Fatalf("fetch: %s", snap.Err)
	}
	if snap.Source != "network" || snap.LatestOf("cli") != "v1.2.3" {
		t.Fatalf("snap = %+v", snap)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}

	// Second fetch within TTL should hit cache.
	snap2 := release.Fetch(context.Background(), opts)
	if snap2.Source != "cache" || snap2.LatestOf("cli") != "v1.2.3" {
		t.Fatalf("cached = %+v", snap2)
	}
	if hits != 1 {
		t.Fatalf("hits after cache = %d, want 1", hits)
	}

	// Refresh forces network.
	opts.Refresh = true
	snap3 := release.Fetch(context.Background(), opts)
	if snap3.Source != "network" || hits != 2 {
		t.Fatalf("refresh: source=%s hits=%d", snap3.Source, hits)
	}
}

func TestFetch_NetworkFailureFallsBackToStaleCache(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "release.json")
	// Seed a cache entry.
	seed := struct {
		Fetched  time.Time        `json:"fetched"`
		Manifest release.Manifest `json:"manifest"`
	}{
		Fetched: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		Manifest: release.Manifest{
			Schema: 1,
			Components: map[string]release.ComponentVersion{
				"cli": {Version: "v9.9.9"},
			},
		},
	}
	data, _ := json.Marshal(seed)
	if err := os.WriteFile(cache, data, 0o600); err != nil {
		t.Fatal(err)
	}

	// Server always fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	snap := release.Fetch(context.Background(), release.FetchOptions{
		BaseURL:   srv.URL,
		CachePath: cache,
		Refresh:   true, // force network attempt
		Now:       func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) },
	})
	if snap.LatestOf("cli") != "v9.9.9" {
		t.Fatalf("expected stale cache, got %+v", snap)
	}
	if snap.Err == "" {
		t.Error("expected Err noting stale cache")
	}
}

func TestFetch_404EmptySnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	snap := release.Fetch(context.Background(), release.FetchOptions{
		BaseURL:      srv.URL,
		DisableCache: true,
	})
	if snap.Err == "" {
		t.Fatal("expected error on 404")
	}
	if snap.LatestOf("cli") != "" {
		t.Errorf("latest = %q, want empty", snap.LatestOf("cli"))
	}
}

func TestGuide(t *testing.T) {
	if g := release.Guide(release.ComponentCLI); !strings.Contains(g, "ownbasectl") {
		t.Errorf("cli guide = %q", g)
	}
	if g := release.Guide(release.ComponentApp); !strings.Contains(g, "ownbase") || strings.Contains(g, "ownbasectl") {
		t.Errorf("app guide = %q", g)
	}
	if g := release.Guide(release.ComponentDaemon); g != "" {
		t.Errorf("daemon guide = %q, want empty", g)
	}
}
