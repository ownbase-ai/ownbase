// Package release fetches and caches the OwnBase release manifest so clients
// can learn the newest published versions of the CLI, desktop app, and daemon
// without downloading binaries.
//
// The manifest is published by the release workflow to
// https://releases.ownbase.ai/latest.json. It is advisory only — install and
// self-update still minisign-verify the binaries themselves. A tampered
// manifest can produce a false nag, never an unsigned binary.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ownbase/ownbase/internal/update"
	"github.com/ownbase/ownbase/internal/vault"
)

// DefaultBaseURL is the origin that hosts latest.json. Daemon binaries live
// under {BaseURL}/daemon/… (see internal/selfupdate). Override with
// OWNBASE_RELEASE_URL (forks, local fixtures) — the same env self-update and
// install.sh honor for the binary channel.
const DefaultBaseURL = "https://releases.ownbase.ai"

// BaseURLEnv overrides DefaultBaseURL for one process. Same name as
// selfupdate.OriginEnv so one knob repoints the whole release channel.
const BaseURLEnv = "OWNBASE_RELEASE_URL"

// CacheFile is the filename under ~/.ownbase/cache for the cached manifest.
const CacheFile = "release.json"

// DefaultCacheTTL is how long a cached manifest is considered fresh.
const DefaultCacheTTL = 24 * time.Hour

// SchemaVersion is the only schema this package understands.
const SchemaVersion = 1

// Component names in the manifest.
const (
	ComponentCLI    = "cli"
	ComponentApp    = "app"
	ComponentDaemon = "daemon"
)

// Status is the verdict for one component relative to the newest release.
type Status string

const (
	// StatusCurrent: running a clean release tag equal to the newest.
	StatusCurrent Status = "current"
	// StatusBehind: running a clean release tag older than the newest.
	StatusBehind Status = "behind"
	// StatusAhead: running a clean release tag newer than the newest
	// (e.g. a just-cut tag not yet reflected in a stale cache).
	StatusAhead Status = "ahead"
	// StatusDev: not a clean release build — never nag.
	StatusDev Status = "dev"
	// StatusUnknown: no usable current version, or the newest is unknown
	// (manifest missing/unreachable). Never treated as "out of date".
	StatusUnknown Status = "unknown"
)

// ComponentVersion is one entry under components in latest.json.
type ComponentVersion struct {
	Version string `json:"version"`
}

// Manifest is the document at releases.ownbase.ai/latest.json.
type Manifest struct {
	Schema     int                         `json:"schema"`
	ReleasedAt string                      `json:"released_at,omitempty"`
	Components map[string]ComponentVersion `json:"components"`
}

// Snapshot is a loaded manifest plus where it came from.
type Snapshot struct {
	Manifest Manifest  `json:"manifest"`
	Fetched  time.Time `json:"fetched"`
	// Source is "network", "cache", or "fixture".
	Source string `json:"source,omitempty"`
	// Err is a soft failure that left the snapshot empty (network down, 404).
	// Callers treat this as StatusUnknown rather than failing the command.
	Err string `json:"error,omitempty"`
}

// Component is one checked component in a version report.
type Component struct {
	Name    string `json:"name"`
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	Status  Status `json:"status"`
	// Guide is the human command to update when Status is behind.
	// Empty for current/dev/unknown and for the daemon (use self-update).
	Guide string `json:"guide,omitempty"`
}

// Report is the document printed by `ownbasectl version --check --json`.
type Report struct {
	Components []Component `json:"components"`
	// Skew is set when a Base was checked and CLI/daemon clean tags differ.
	Skew *Skew `json:"skew,omitempty"`
	// Manifest holds the snapshot used for the comparison (may be empty).
	Manifest *Snapshot `json:"manifest,omitempty"`
}

// Skew describes CLI vs daemon version mismatch on one Base.
type Skew struct {
	// Direction is "cli_ahead" (daemon needs self-update) or "daemon_ahead"
	// (CLI needs upgrading).
	Direction string `json:"direction"`
	CLI       string `json:"cli"`
	Daemon    string `json:"daemon"`
	// Guide is the command that closes the gap.
	Guide string `json:"guide"`
	// Summary is one-line prose for findings / terminal output.
	Summary string `json:"summary"`
}

// BaseURL returns the configured release origin.
func BaseURL() string {
	if v := strings.TrimSpace(os.Getenv(BaseURLEnv)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return DefaultBaseURL
}

// CachePath returns ~/.ownbase/cache/release.json.
func CachePath() (string, error) {
	return vault.StatePath(filepath.Join("cache", CacheFile))
}

// FetchOptions control Fetch.
type FetchOptions struct {
	// BaseURL overrides BaseURL(). Empty → BaseURL().
	BaseURL string
	// CachePath overrides the default cache location. Empty → CachePath().
	// Set to a temp path in tests. Set to os.DevNull-style sentinel "" with
	// DisableCache to skip disk entirely.
	CachePath string
	// DisableCache skips reading and writing the on-disk cache.
	DisableCache bool
	// Refresh forces a network fetch even when the cache is fresh.
	Refresh bool
	// TTL overrides DefaultCacheTTL. Zero → DefaultCacheTTL.
	TTL time.Duration
	// HTTPClient overrides the default client. Tests inject a fake transport.
	HTTPClient *http.Client
	// Now overrides time.Now (tests).
	Now func() time.Time
}

func (o FetchOptions) base() string {
	if o.BaseURL != "" {
		return strings.TrimRight(o.BaseURL, "/")
	}
	return BaseURL()
}

func (o FetchOptions) ttl() time.Duration {
	if o.TTL > 0 {
		return o.TTL
	}
	return DefaultCacheTTL
}

func (o FetchOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o FetchOptions) client() *http.Client {
	if o.HTTPClient != nil {
		return o.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (o FetchOptions) cachePath() (string, error) {
	if o.CachePath != "" {
		return o.CachePath, nil
	}
	return CachePath()
}

// Fetch returns a Snapshot, preferring a fresh on-disk cache. Network failure
// falls back to a stale cache when one exists; otherwise returns a Snapshot
// with Err set and empty Components — never a hard error for the caller.
func Fetch(ctx context.Context, opts FetchOptions) Snapshot {
	now := opts.now()
	if !opts.DisableCache && !opts.Refresh {
		if snap, ok := readCache(opts, now); ok {
			return snap
		}
	}

	snap, err := fetchNetwork(ctx, opts, now)
	if err == nil {
		if !opts.DisableCache {
			_ = writeCache(opts, snap)
		}
		return snap
	}

	// Soft-fail: serve stale cache if any, else empty with the error noted.
	if !opts.DisableCache {
		if stale, ok := readCache(opts, time.Time{}); ok {
			stale.Source = "cache"
			stale.Err = fmt.Sprintf("using stale cache: %v", err)
			return stale
		}
	}
	return Snapshot{
		Fetched: now,
		Source:  "network",
		Err:     err.Error(),
	}
}

func fetchNetwork(ctx context.Context, opts FetchOptions, now time.Time) (Snapshot, error) {
	url := opts.base() + "/latest.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Snapshot{}, err
	}
	resp, err := opts.client().Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Snapshot{}, fmt.Errorf("fetch %s: HTTP %d %s", url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return Snapshot{}, fmt.Errorf("decode %s: %w", url, err)
	}
	if m.Schema != 0 && m.Schema != SchemaVersion {
		return Snapshot{}, fmt.Errorf("unsupported manifest schema %d (want %d)", m.Schema, SchemaVersion)
	}
	if m.Schema == 0 {
		m.Schema = SchemaVersion
	}
	if m.Components == nil {
		m.Components = map[string]ComponentVersion{}
	}
	return Snapshot{Manifest: m, Fetched: now, Source: "network"}, nil
}

type cacheFile struct {
	Fetched  time.Time `json:"fetched"`
	Manifest Manifest  `json:"manifest"`
}

func readCache(opts FetchOptions, now time.Time) (Snapshot, bool) {
	path, err := opts.cachePath()
	if err != nil {
		return Snapshot{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, false
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return Snapshot{}, false
	}
	// now.IsZero means "accept any age" (stale fallback path).
	if !now.IsZero() && now.Sub(cf.Fetched) > opts.ttl() {
		return Snapshot{}, false
	}
	return Snapshot{
		Manifest: cf.Manifest,
		Fetched:  cf.Fetched,
		Source:   "cache",
	}, true
}

func writeCache(opts FetchOptions, snap Snapshot) error {
	path, err := opts.cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cf := cacheFile{Fetched: snap.Fetched, Manifest: snap.Manifest}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// LatestOf returns the newest version string for a component, or "".
func (s Snapshot) LatestOf(component string) string {
	if s.Manifest.Components == nil {
		return ""
	}
	return strings.TrimSpace(s.Manifest.Components[component].Version)
}

// NormalizeTag returns a clean vMAJOR.MINOR.PATCH tag, or "" when v is not
// a release tag. Accepts both "v0.5.0" (CLI/daemon ldflags) and "0.5.0"
// (Tauri stamps the app bundle without the leading v).
func NormalizeTag(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if !isCleanReleaseTag(v) {
		return ""
	}
	return v
}

// IsReleaseTag reports whether v is a clean release tag (with or without a
// leading v). Dev/snapshot/describe builds return false so they never nag.
func IsReleaseTag(v string) bool {
	return NormalizeTag(v) != ""
}

// isCleanReleaseTag is the strict form: leading v + three numeric segments.
func isCleanReleaseTag(v string) bool {
	if len(v) < 5 || v[0] != 'v' {
		return false
	}
	parts := strings.Split(v[1:], ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// Assess compares a running version against the newest for that component.
func Assess(component, current, latest string) Component {
	c := Component{
		Name:    component,
		Current: current,
		Latest:  latest,
	}
	cur := NormalizeTag(current)
	if cur == "" {
		c.Status = StatusDev
		return c
	}
	lat := NormalizeTag(latest)
	if lat == "" {
		c.Status = StatusUnknown
		return c
	}
	// Prefer the normalized forms in the report so app "0.5.0" and CLI
	// "v0.5.0" render the same way.
	c.Current = cur
	c.Latest = lat
	switch update.CompareVersionTags(cur, lat) {
	case 0:
		c.Status = StatusCurrent
	case -1:
		c.Status = StatusBehind
		c.Guide = Guide(component)
	default:
		c.Status = StatusAhead
	}
	return c
}

// Guide returns the human update command for a client-side component.
// The daemon is updated via ownbasectl self-update <base>, not a local guide.
func Guide(component string) string {
	switch component {
	case ComponentCLI:
		return "brew upgrade --cask ownbase-ai/tap/ownbasectl"
	case ComponentApp:
		return "brew upgrade --cask ownbase-ai/tap/ownbase"
	default:
		return ""
	}
}

// AssessSkew compares a release CLI against a release daemon on one Base.
// Returns nil when either side is not a clean release tag, or when equal.
func AssessSkew(cliVer, daemonVer, base string) *Skew {
	cli := NormalizeTag(cliVer)
	daemon := NormalizeTag(daemonVer)
	if cli == "" || daemon == "" {
		return nil
	}
	cmp := update.CompareVersionTags(cli, daemon)
	if cmp == 0 {
		return nil
	}
	if cmp > 0 {
		// CLI newer than daemon.
		return &Skew{
			Direction: "cli_ahead",
			CLI:       cli,
			Daemon:    daemon,
			Guide:     "ownbasectl self-update " + base,
			Summary:   fmt.Sprintf("Base daemon %s is behind your CLI %s", daemon, cli),
		}
	}
	// Daemon newer than CLI.
	return &Skew{
		Direction: "daemon_ahead",
		CLI:       cli,
		Daemon:    daemon,
		Guide:     Guide(ComponentCLI),
		Summary:   fmt.Sprintf("Your CLI %s is behind Base daemon %s", cli, daemon),
	}
}

// BuildReport assembles a full version report from known running versions and
// an optional snapshot. Missing components are omitted.
type Running struct {
	CLI    string // ownbasectl version ldflag
	App    string // optional; empty when not running under the app
	Daemon string // optional; empty when no Base was checked
	Base   string // Base name when Daemon is set (for skew guide)
}

// BuildReport produces the version --check document.
func BuildReport(run Running, snap Snapshot) Report {
	r := Report{}
	if snap.Manifest.Components != nil || snap.Err != "" {
		s := snap
		r.Manifest = &s
	}

	if run.CLI != "" {
		r.Components = append(r.Components, Assess(ComponentCLI, run.CLI, snap.LatestOf(ComponentCLI)))
	}
	if run.App != "" {
		r.Components = append(r.Components, Assess(ComponentApp, run.App, snap.LatestOf(ComponentApp)))
	}
	if run.Daemon != "" {
		r.Components = append(r.Components, Assess(ComponentDaemon, run.Daemon, snap.LatestOf(ComponentDaemon)))
		if skew := AssessSkew(run.CLI, run.Daemon, run.Base); skew != nil {
			r.Skew = skew
		}
	}
	return r
}
