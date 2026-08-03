package schema_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ownbase/ownbase/internal/schema"
)

// ---------------------------------------------------------------------------
// Config tests
// ---------------------------------------------------------------------------

func TestParseConfig_ValidFixtures(t *testing.T) {
	fixtures := []string{
		"../../testdata/minimal/ownbase.yaml",
		"../../testdata/valid/full-config.yaml",
		"../../testdata/valid/jobs.yaml",
		"../../testdata/valid/replicas.yaml",
	}
	for _, path := range fixtures {
		t.Run(filepath.Base(filepath.Dir(path)), func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer f.Close()
			cfg, err := schema.ParseConfig(f)
			if err != nil {
				t.Fatalf("unexpected error for %s: %v", path, err)
			}
			if cfg == nil {
				t.Fatal("got nil config")
			}
		})
	}
}

func TestParseConfig_InvalidFixtures(t *testing.T) {
	cases := []struct {
		file    string
		wantErr string
	}{
		{"missing-schema-version.yaml", "schema_version"},
		{"unknown-schema-version.yaml", "schema_version"},
		{"repo-missing.yaml", "repo is required"},
		{"repo-not-url.yaml", "repo must be a git URL"},
		{"missing-capability-provider.yaml", "capability"},
		{"unknown-field.yaml", "unexpected_field"},
		{"job-unknown-service.yaml", `service "does-not-exist" does not match any service key`},
		{"job-missing-command.yaml", "command is required"},
		{"job-missing-schedule.yaml", "schedule is required"},
		{"replicas-zero.yaml", "replicas"},
		{"replicas-collision.yaml", "collides"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("../../testdata/invalid", tc.file)
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer f.Close()
			_, err = schema.ParseConfig(f)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.file)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestParseConfig_RoundTrip(t *testing.T) {
	path := "../../testdata/valid/full-config.yaml"
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	cfg, err := schema.ParseConfig(f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	data, err := schema.MarshalConfig(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	cfg2, err := schema.ParseConfig(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(cfg2.Services) != len(cfg.Services) {
		t.Errorf("services length mismatch: %d vs %d", len(cfg2.Services), len(cfg.Services))
	}
}

func TestParseConfig_Warnings_BlankRef(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Repo: "https://github.com/example/auth.git"},
		},
	}
	warns := cfg.Warnings()
	if len(warns) == 0 {
		t.Error("expected a warning for service with no ref")
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "ownbasectl deploy") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected deploy-to-pin warning, got: %v", warns)
	}
}

func TestParseConfig_Warnings_DeprecatedMode(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Repo: "https://github.com/example/auth.git", Mode: "managed", Ref: "v1.0.0"},
		},
	}
	warns := cfg.Warnings()
	found := false
	for _, w := range warns {
		if strings.Contains(w, "deprecated") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected deprecation warning for mode:, got: %v", warns)
	}
}

func TestParseConfig_Warnings_NoRefAndDeprecatedMode(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Repo: "https://github.com/example/auth.git", Mode: "pinned"},
		},
	}
	warns := cfg.Warnings()
	// Expect both: blank-ref warning + deprecated-mode warning.
	if len(warns) < 2 {
		t.Errorf("expected at least 2 warnings (blank ref + deprecated mode), got %d: %v", len(warns), warns)
	}
}

func TestParseConfig_Warnings_RefSet_NoMode_NoWarnings(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Repo: "https://github.com/example/auth.git", Ref: "v1.0.0"},
		},
	}
	if warns := cfg.Warnings(); len(warns) != 0 {
		t.Errorf("expected no warnings for service with ref set and no mode, got: %v", warns)
	}
}

// ---------------------------------------------------------------------------
// Repo service validation
// ---------------------------------------------------------------------------

func TestParseConfig_RepoService_ValidURL(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"postgres": {Repo: "https://github.com/docker-library/postgres"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid repo service, got: %v", err)
	}
}

func TestParseConfig_RepoService_InvalidURL(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"postgres": {Repo: "docker-library/postgres"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for repo without URL scheme")
	} else if !strings.Contains(err.Error(), "repo must be a git URL") {
		t.Errorf("error %q does not mention 'repo must be a git URL'", err.Error())
	}
}

func TestParseConfig_RepoRequired(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Port: 8080},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for service with no repo")
	} else if !strings.Contains(err.Error(), "repo is required") {
		t.Errorf("error %q does not mention 'repo is required'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Jobs (scheduled)
// ---------------------------------------------------------------------------

func TestParseConfig_Jobs_ParsesFromFixture(t *testing.T) {
	cfg, err := schema.ParseConfigFile("../../testdata/valid/jobs.yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	job, ok := cfg.Jobs["nightly-ingest"]
	if !ok {
		t.Fatal("expected a \"nightly-ingest\" job")
	}
	if job.Service != "api" {
		t.Errorf("Service = %q, want %q", job.Service, "api")
	}
	if job.Schedule != "*-*-* 08:00:00 UTC" {
		t.Errorf("Schedule = %q", job.Schedule)
	}
	want := []string{"python", "scripts/nightly_ingest.py", "--region", "mx"}
	if len(job.Command) != len(want) {
		t.Fatalf("Command = %v, want %v", job.Command, want)
	}
	for i := range want {
		if job.Command[i] != want[i] {
			t.Errorf("Command[%d] = %q, want %q", i, job.Command[i], want[i])
		}
	}
	if !job.EffectivePersistent() {
		t.Error("expected EffectivePersistent() to default true when persistent: is unset")
	}

	cleanup, ok := cfg.Jobs["cleanup"]
	if !ok {
		t.Fatal("expected a \"cleanup\" job")
	}
	if cleanup.EffectivePersistent() {
		t.Error("expected EffectivePersistent() to be false when persistent: false is set")
	}
}

func TestJobDecl_Validate_ServiceRequired(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"api": {Repo: "https://github.com/example/api.git"},
		},
		Jobs: map[string]schema.JobDecl{
			"nightly": {Command: []string{"true"}, Schedule: "daily"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for job with no service:, got nil")
	} else if !strings.Contains(err.Error(), "service is required") {
		t.Errorf("error %q does not mention 'service is required'", err.Error())
	}
}

func TestJobDecl_Validate_NameCollidesWithService(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"api": {Repo: "https://github.com/example/api.git"},
		},
		Jobs: map[string]schema.JobDecl{
			"api": {Service: "api", Command: []string{"true"}, Schedule: "daily"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for job name colliding with a service name, got nil")
	} else if !strings.Contains(err.Error(), "collides with a service") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestJobDecl_Validate_UnknownService(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"api": {Repo: "https://github.com/example/api.git"},
		},
		Jobs: map[string]schema.JobDecl{
			"nightly": {Service: "ghost", Command: []string{"true"}, Schedule: "daily"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for job referencing unknown service, got nil")
	} else if !strings.Contains(err.Error(), `service "ghost" does not match any service key`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_ServiceNameWithJobPrefix_Rejected(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"job-runner": {Repo: "https://github.com/example/api.git"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for service name starting with \"job-\", got nil")
	} else if !strings.Contains(err.Error(), `service names may not start with "job-"`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestJobDecl_EffectivePersistent_DefaultsTrue(t *testing.T) {
	j := schema.JobDecl{}
	if !j.EffectivePersistent() {
		t.Error("expected EffectivePersistent() to default to true")
	}
}

func TestJobDecl_EffectivePersistent_ExplicitFalse(t *testing.T) {
	f := false
	j := schema.JobDecl{Persistent: &f}
	if j.EffectivePersistent() {
		t.Error("expected EffectivePersistent() to honor an explicit false")
	}
}

// ---------------------------------------------------------------------------
// Core config
// ---------------------------------------------------------------------------

func TestParseConfig_CoreConfig_ParsesFromFixture(t *testing.T) {
	cfg, err := schema.ParseConfigFile("../../testdata/valid/full-config.yaml")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Core.Caddy.Email != "admin@example.com" {
		t.Errorf("core.caddy.email = %q, want admin@example.com", cfg.Core.Caddy.Email)
	}
}

// ---------------------------------------------------------------------------
// Taxonomy tests
// ---------------------------------------------------------------------------

func TestTaxonomy_AllActionsHaveOneTier(t *testing.T) {
	all := schema.AllActions()
	if len(all) == 0 {
		t.Fatal("AllActions returned empty slice")
	}
	seen := map[schema.ActionType]int{}
	for _, a := range all {
		seen[a.Type]++
		if a.DefaultTier == "" {
			t.Errorf("action %q has empty default tier", a.Type)
		}
	}
	for at, count := range seen {
		if count != 1 {
			t.Errorf("action %q appears %d times in AllActions", at, count)
		}
	}
}

func TestTaxonomy_NewActionKnownType(t *testing.T) {
	a, err := schema.NewAction(schema.ActionServiceStart, "auth")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Type != schema.ActionServiceStart {
		t.Errorf("type mismatch: %q", a.Type)
	}
	if a.DefaultTier != schema.TierAutonomous {
		t.Errorf("tier mismatch: %q", a.DefaultTier)
	}
}

func TestTaxonomy_NewActionUnknownType(t *testing.T) {
	_, err := schema.NewAction("not.in.taxonomy", "anything")
	if err == nil {
		t.Fatal("expected error for unknown action type, got nil")
	}
	if !strings.Contains(err.Error(), "not in taxonomy") {
		t.Errorf("error %q does not mention 'not in taxonomy'", err.Error())
	}
}

func TestTaxonomy_RestoreApplyIsApprove(t *testing.T) {
	a, err := schema.NewAction(schema.ActionRestoreApply, "base")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.DefaultTier != schema.TierApprove {
		t.Errorf("restore.apply should be TierApprove, got %q", a.DefaultTier)
	}
}

func TestTaxonomy_AllActionsAreSorted(t *testing.T) {
	all := schema.AllActions()
	for i := 1; i < len(all); i++ {
		if all[i].Type < all[i-1].Type {
			t.Errorf("AllActions not sorted at index %d: %q > %q", i, all[i-1].Type, all[i].Type)
		}
	}
}

func TestOwnAuthOwnbaseYAML(t *testing.T) {
	// The OwnAuth ownbase.yaml fixture must parse cleanly and contain auth + postgres.
	cfg, err := schema.ParseConfigFile("../../testdata/ownauth/ownbase.yaml")
	if err != nil {
		t.Fatalf("ParseConfigFile(ownauth): %v", err)
	}
	if _, ok := cfg.Services["auth"]; !ok {
		t.Error("ownauth/ownbase.yaml must have an auth service")
	}
	if _, ok := cfg.Services["postgres"]; !ok {
		t.Error("ownauth/ownbase.yaml must have a postgres service")
	}
	if cfg.Services["auth"].Repo != "https://github.com/anonlogin/anonlogin.git" {
		t.Errorf("auth.Repo = %q, want https://github.com/anonlogin/anonlogin.git", cfg.Services["auth"].Repo)
	}
	if cfg.Services["postgres"].Repo != "https://github.com/docker-library/postgres" {
		t.Errorf("postgres.Repo = %q, want https://github.com/docker-library/postgres",
			cfg.Services["postgres"].Repo)
	}
}

// ---------------------------------------------------------------------------
// BackupCoreConfig
// ---------------------------------------------------------------------------

func TestBackupCoreConfig_Disabled(t *testing.T) {
	b := schema.BackupCoreConfig{}
	if b.Enabled() {
		t.Error("empty BackupCoreConfig should not be Enabled()")
	}
}

func TestBackupCoreConfig_Enabled(t *testing.T) {
	b := schema.BackupCoreConfig{Repo: "s3:s3.amazonaws.com/bucket/ownbase"}
	if !b.Enabled() {
		t.Error("BackupCoreConfig with Repo set should be Enabled()")
	}
}

func TestBackupCoreConfig_EffectiveInterval_Default(t *testing.T) {
	b := schema.BackupCoreConfig{Repo: "local:/tmp/test"}
	d, err := b.EffectiveInterval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != schema.DefaultBackupInterval {
		t.Errorf("got %v, want %v", d, schema.DefaultBackupInterval)
	}
}

func TestBackupCoreConfig_EffectiveInterval_Custom(t *testing.T) {
	b := schema.BackupCoreConfig{Repo: "local:/tmp/test", Interval: "30m"}
	d, err := b.EffectiveInterval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 30*time.Minute {
		t.Errorf("got %v, want 30m", d)
	}
}

func TestBackupCoreConfig_EffectiveInterval_Invalid(t *testing.T) {
	b := schema.BackupCoreConfig{Repo: "local:/tmp/test", Interval: "not-a-duration"}
	if _, err := b.EffectiveInterval(); err == nil {
		t.Error("expected error for invalid interval, got nil")
	}
}

func TestBackupCoreConfig_EffectiveVerifyInterval_Default(t *testing.T) {
	b := schema.BackupCoreConfig{Repo: "local:/tmp/test"}
	d, err := b.EffectiveVerifyInterval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != schema.DefaultVerifyInterval {
		t.Errorf("got %v, want %v", d, schema.DefaultVerifyInterval)
	}
}

func TestBackupCoreConfig_EffectiveVerifyInterval_Custom(t *testing.T) {
	b := schema.BackupCoreConfig{Repo: "local:/tmp/test", VerifyInterval: "12h"}
	d, err := b.EffectiveVerifyInterval()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 12*time.Hour {
		t.Errorf("got %v, want 12h", d)
	}
}

// ---------------------------------------------------------------------------
// Multi-domain support
// ---------------------------------------------------------------------------

func TestServiceDecl_EffectiveDomains_Empty(t *testing.T) {
	s := schema.ServiceDecl{}
	if got := s.EffectiveDomains(); len(got) != 0 {
		t.Errorf("expected no domains, got %v", got)
	}
}

func TestServiceDecl_EffectiveDomains_DomainOnly(t *testing.T) {
	s := schema.ServiceDecl{Domain: "app.example.com"}
	got := s.EffectiveDomains()
	want := []string{"app.example.com"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestServiceDecl_EffectiveDomains_UnionAndDedup(t *testing.T) {
	s := schema.ServiceDecl{
		Domain:  "app.example.com",
		Domains: []string{"app.example.org", "app.example.com", "  app.example.net  "},
	}
	got := s.EffectiveDomains()
	want := []string{"app.example.com", "app.example.org", "app.example.net"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestOwnbaseConfig_HasPublicDomain(t *testing.T) {
	cases := []struct {
		name string
		cfg  schema.OwnbaseConfig
		want bool
	}{
		{"no services", schema.OwnbaseConfig{}, false},
		{"service with no domain", schema.OwnbaseConfig{
			Services: map[string]schema.ServiceDecl{"a": {Repo: "x", Port: 8080}},
		}, false},
		{"service with domain: and port:", schema.OwnbaseConfig{
			Services: map[string]schema.ServiceDecl{"a": {Repo: "x", Domain: "a.example.com", Port: 8080}},
		}, true},
		{"service with domains: and port:", schema.OwnbaseConfig{
			Services: map[string]schema.ServiceDecl{"a": {Repo: "x", Domains: []string{"a.example.com"}, Port: 8080}},
		}, true},
		{"service with domain: but no port", schema.OwnbaseConfig{
			Services: map[string]schema.ServiceDecl{"a": {Repo: "x", Domain: "a.example.com"}},
		}, false},
		{"service with port but no domain", schema.OwnbaseConfig{
			Services: map[string]schema.ServiceDecl{"a": {Repo: "x", Port: 8080}},
		}, false},
		{"one service domain-only, another port-only: still no routable public service", schema.OwnbaseConfig{
			Services: map[string]schema.ServiceDecl{
				"a": {Repo: "x", Domain: "a.example.com"},
				"b": {Repo: "y", Port: 8080},
			},
		}, false},
		{"internal: true service with domain and port is not a public domain", schema.OwnbaseConfig{
			Services: map[string]schema.ServiceDecl{
				"a": {Repo: "x", Domain: "a.example.com", Port: 8080, Internal: true},
			},
		}, false},
		{"internal service alongside public service: public one counts", schema.OwnbaseConfig{
			Services: map[string]schema.ServiceDecl{
				"admin": {Repo: "x", Domain: "admin.example.com", Port: 3000, Internal: true},
				"web":   {Repo: "y", Domain: "web.example.com", Port: 8080},
			},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.HasPublicDomain(); got != tc.want {
				t.Errorf("HasPublicDomain() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOwnbaseConfig_TunnelPorts_Empty(t *testing.T) {
	cfg := schema.OwnbaseConfig{}
	if got := cfg.TunnelPorts(); len(got) != 0 {
		t.Errorf("expected no ports for empty config, got %v", got)
	}
}

func TestOwnbaseConfig_TunnelPorts_AnyPortedServiceEligibleDomainOrNot(t *testing.T) {
	cfg := schema.OwnbaseConfig{
		Services: map[string]schema.ServiceDecl{
			"domain-and-port": {Repo: "x", Domain: "a.example.com", Port: 8080},
			"port-only":       {Repo: "y", Port: 8080},
			"domain-only":     {Repo: "z", Domain: "b.example.com"},
		},
	}
	got := cfg.TunnelPorts()
	// Keys are container names. Eligibility is Port != 0 alone.
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 eligible (port'd) containers, got %v", got)
	}
	if _, ok := got["ownbase-domain-and-port"]; !ok {
		t.Errorf("expected ownbase-domain-and-port to be assigned a port, got %v", got)
	}
	if _, ok := got["ownbase-port-only"]; !ok {
		t.Errorf("expected ownbase-port-only (domain-less) to also be assigned a port, got %v", got)
	}
	if _, ok := got["ownbase-domain-only"]; ok {
		t.Errorf("expected ownbase-domain-only (no port) to be excluded, got %v", got)
	}
}

func TestOwnbaseConfig_TunnelPorts_SingleService(t *testing.T) {
	cfg := schema.OwnbaseConfig{
		Services: map[string]schema.ServiceDecl{
			"hello": {Repo: "x", Domain: "hello.example.com", Port: 8080},
		},
	}
	got := cfg.TunnelPorts()
	if got["ownbase-hello"] != schema.TunnelBasePort {
		t.Errorf("ownbase-hello port = %d, want %d", got["ownbase-hello"], schema.TunnelBasePort)
	}
}

func TestOwnbaseConfig_TunnelPorts_DeterministicSortedAssignment(t *testing.T) {
	cfg := schema.OwnbaseConfig{
		Services: map[string]schema.ServiceDecl{
			"zeta":  {Repo: "z", Domain: "zeta.example.com", Port: 3000},
			"alpha": {Repo: "a", Domain: "alpha.example.com", Port: 3000},
			"multi": {Repo: "m", Domain: "multi.example.com", Port: 9090},
		},
	}
	// Run several times to make sure map iteration order never affects the result.
	for i := 0; i < 5; i++ {
		got := cfg.TunnelPorts()
		if got["ownbase-alpha"] != schema.TunnelBasePort {
			t.Errorf("ownbase-alpha port = %d, want %d", got["ownbase-alpha"], schema.TunnelBasePort)
		}
		if got["ownbase-multi"] != schema.TunnelBasePort+1 {
			t.Errorf("ownbase-multi port = %d, want %d", got["ownbase-multi"], schema.TunnelBasePort+1)
		}
		if got["ownbase-zeta"] != schema.TunnelBasePort+2 {
			t.Errorf("ownbase-zeta port = %d, want %d", got["ownbase-zeta"], schema.TunnelBasePort+2)
		}
	}
}

func TestOwnbaseConfig_TunnelPorts_Replicas(t *testing.T) {
	n := 3
	cfg := schema.OwnbaseConfig{
		Services: map[string]schema.ServiceDecl{
			"alpha":  {Repo: "a", Port: 3000},
			"worker": {Repo: "w", Port: 4096, Replicas: &n},
		},
	}
	got := cfg.TunnelPorts()
	// Strided: port(i,j) = base + i + j*N. alpha i=0; worker i=1; N=2.
	if got["ownbase-alpha"] != schema.TunnelBasePort {
		t.Errorf("alpha = %d, want %d", got["ownbase-alpha"], schema.TunnelBasePort)
	}
	for j := 0; j < 3; j++ {
		name := schema.ContainerName("worker", &n, j)
		want := schema.TunnelBasePort + 1 + j*2
		if got[name] != want {
			t.Errorf("%s = %d, want %d", name, got[name], want)
		}
	}
	if len(got) != 4 {
		t.Errorf("len = %d, want 4: %v", len(got), got)
	}
}

func TestOwnbaseConfig_TunnelPorts_ReplicasDoNotShiftNeighbors(t *testing.T) {
	// Adding replicas: to the middle service must not renumber zeta.
	plain := schema.OwnbaseConfig{
		Services: map[string]schema.ServiceDecl{
			"alpha": {Repo: "a", Port: 3000},
			"beta":  {Repo: "b", Port: 3000},
			"zeta":  {Repo: "z", Port: 3000},
		},
	}
	before := plain.TunnelPorts()

	n := 4
	withRep := schema.OwnbaseConfig{
		Services: map[string]schema.ServiceDecl{
			"alpha": {Repo: "a", Port: 3000},
			"beta":  {Repo: "b", Port: 3000, Replicas: &n},
			"zeta":  {Repo: "z", Port: 3000},
		},
	}
	after := withRep.TunnelPorts()

	if before["ownbase-alpha"] != after["ownbase-alpha"] {
		t.Errorf("alpha shifted: %d → %d", before["ownbase-alpha"], after["ownbase-alpha"])
	}
	if before["ownbase-zeta"] != after["ownbase-zeta"] {
		t.Errorf("zeta shifted: %d → %d", before["ownbase-zeta"], after["ownbase-zeta"])
	}
	// beta renames to beta-0; replica 0 keeps the old beta slot.
	if after["ownbase-beta-0"] != before["ownbase-beta"] {
		t.Errorf("beta-0 = %d, want former beta slot %d", after["ownbase-beta-0"], before["ownbase-beta"])
	}
}

func TestValidate_ReplicaNameCollision(t *testing.T) {
	n := 2
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"web":   {Repo: "https://github.com/org/web.git", Replicas: &n},
			"web-0": {Repo: "https://github.com/org/other.git"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !strings.Contains(err.Error(), "collides") {
		t.Errorf("error = %v, want collides", err)
	}
}

func TestValidate_VolumeReplicaNameCollision(t *testing.T) {
	// per-replica "state" index 0 → ownbase-w-state-0; shared "state-0" → same.
	n := 2
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"w": {
				Repo:     "https://github.com/org/w.git",
				Replicas: &n,
				Volumes: []schema.VolumeDecl{
					{Name: "state", Mount: "/state"},
					{Name: "state-0", Mount: "/other", PerReplica: boolPtr(false)},
				},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected volume collision error")
	}
	if !strings.Contains(err.Error(), "volume name") {
		t.Errorf("error = %v, want volume name collision", err)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestValidate_ReplicasRange(t *testing.T) {
	zero := 0
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"web": {Repo: "https://github.com/org/web.git", Replicas: &zero},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected range error for replicas: 0")
	}
}

func TestValidate_Resources(t *testing.T) {
	ok := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"web": {
				Repo:      "https://github.com/org/web.git",
				Resources: &schema.ResourcesDecl{Memory: "4g", CPUs: "1.5"},
			},
		},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid resources: %v", err)
	}

	for _, mem := range []string{"four gig", "0", "0g", "0.0m", "1.2.3g"} {
		cfg := &schema.OwnbaseConfig{
			SchemaVersion: "v1",
			Services: map[string]schema.ServiceDecl{
				"web": {
					Repo:      "https://github.com/org/web.git",
					Resources: &schema.ResourcesDecl{Memory: mem},
				},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected invalid memory error for %q", mem)
		}
	}

	badCPU := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"web": {
				Repo:      "https://github.com/org/web.git",
				Resources: &schema.ResourcesDecl{CPUs: "0"},
			},
		},
	}
	if err := badCPU.Validate(); err == nil {
		t.Fatal("expected invalid cpus error")
	}
}

func TestValidate_DomainUniqueness(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"alpha": {Repo: "https://github.com/org/a.git", Domain: "app.example.com", Port: 3000},
			"beta":  {Repo: "https://github.com/org/b.git", Domain: "app.example.com", Port: 4000},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected domain collision error")
	}
	if !strings.Contains(err.Error(), "app.example.com") {
		t.Errorf("error = %v, want domain name", err)
	}
}

func TestBackupCoreConfig_Validate_InvalidInterval(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Core: schema.CoreConfig{
			Backup: schema.BackupCoreConfig{
				Repo:     "local:/tmp/test",
				Interval: "bad",
			},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected validation error for bad interval, got nil")
	}
}

func TestBackupCoreConfig_RoundTrip(t *testing.T) {
	yaml := `schema_version: v1
core:
  backup:
    repo: s3:s3.amazonaws.com/my-bucket/ownbase
    interval: 2h
    verify_interval: 48h
`
	cfg, err := schema.ParseConfig(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Core.Backup.Repo != "s3:s3.amazonaws.com/my-bucket/ownbase" {
		t.Errorf("Repo = %q", cfg.Core.Backup.Repo)
	}
	if cfg.Core.Backup.Interval != "2h" {
		t.Errorf("Interval = %q", cfg.Core.Backup.Interval)
	}
	if cfg.Core.Backup.VerifyInterval != "48h" {
		t.Errorf("VerifyInterval = %q", cfg.Core.Backup.VerifyInterval)
	}
}

// ---------------------------------------------------------------------------
// generated_secrets tests
// ---------------------------------------------------------------------------

func TestGeneratedSecrets_RoundTrip(t *testing.T) {
	yaml := `schema_version: v1
services:
  pgbackrest:
    repo: https://github.com/ownbase-ai/pgbackrest
    generated_secrets:
      - type: ssh-ed25519
        public_key: PGBACKREST_CLIENT_PUBKEY
        private_key: postgres:PGBACKREST_SSH_KEY_B64
        private_encoding: base64
  postgres:
    repo: https://github.com/ownbase-ai/pgbackrest
    generated_secrets:
      - type: password
        key: POSTGRES_PASSWORD
        length: 48
`
	cfg, err := schema.ParseConfig(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	keypair := cfg.Services["pgbackrest"].GeneratedSecrets[0]
	if keypair.Type != schema.GeneratedSecretSSHEd25519 {
		t.Errorf("Type = %q", keypair.Type)
	}
	if !keypair.Base64Private() {
		t.Error("Base64Private() = false, want true for private_encoding: base64")
	}

	// The private half targets another service, which is how a keypair gets
	// split across the two ends of a connection.
	dests := keypair.Destinations()
	if len(dests) != 2 {
		t.Fatalf("Destinations() = %v, want 2", dests)
	}
	if dests[0] != (schema.SecretDest{Key: "PGBACKREST_CLIENT_PUBKEY"}) {
		t.Errorf("public destination = %+v", dests[0])
	}
	if dests[1] != (schema.SecretDest{Service: "postgres", Key: "PGBACKREST_SSH_KEY_B64"}) {
		t.Errorf("private destination = %+v", dests[1])
	}

	pw := cfg.Services["postgres"].GeneratedSecrets[0]
	if got := pw.EffectiveLength(); got != 48 {
		t.Errorf("EffectiveLength() = %d, want 48", got)
	}
}

func TestGeneratedSecrets_EffectiveLengthDefault(t *testing.T) {
	var g schema.GeneratedSecretDecl
	if got := g.EffectiveLength(); got != schema.DefaultGeneratedPasswordLength {
		t.Errorf("EffectiveLength() = %d, want %d", got, schema.DefaultGeneratedPasswordLength)
	}
}

func TestGeneratedSecrets_Validate(t *testing.T) {
	tests := []struct {
		name    string
		decl    schema.GeneratedSecretDecl
		wantErr string
	}{
		{
			name: "valid password",
			decl: schema.GeneratedSecretDecl{Type: schema.GeneratedSecretPassword, Key: "PW"},
		},
		{
			name: "valid keypair split across services",
			decl: schema.GeneratedSecretDecl{
				Type:       schema.GeneratedSecretSSHEd25519,
				PublicKey:  "PUB",
				PrivateKey: "other:PRIV",
			},
		},
		{
			name:    "missing type",
			decl:    schema.GeneratedSecretDecl{Key: "PW"},
			wantErr: "type is required",
		},
		{
			name:    "unknown type",
			decl:    schema.GeneratedSecretDecl{Type: "rsa-4096", PublicKey: "PUB", PrivateKey: "PRIV"},
			wantErr: "unknown type",
		},
		{
			name:    "password without key",
			decl:    schema.GeneratedSecretDecl{Type: schema.GeneratedSecretPassword},
			wantErr: "key is required",
		},
		{
			name: "password with keypair fields",
			decl: schema.GeneratedSecretDecl{
				Type: schema.GeneratedSecretPassword, Key: "PW", PublicKey: "PUB",
			},
			wantErr: "not used by type: password",
		},
		{
			name:    "keypair without private_key",
			decl:    schema.GeneratedSecretDecl{Type: schema.GeneratedSecretSSHEd25519, PublicKey: "PUB"},
			wantErr: "private_key is required",
		},
		{
			name: "keypair with key",
			decl: schema.GeneratedSecretDecl{
				Type: schema.GeneratedSecretSSHEd25519, PublicKey: "PUB", PrivateKey: "PRIV", Key: "PW",
			},
			wantErr: "key is not used",
		},
		{
			name: "unknown private_encoding",
			decl: schema.GeneratedSecretDecl{
				Type: schema.GeneratedSecretSSHEd25519, PublicKey: "PUB", PrivateKey: "PRIV",
				PrivateEncoding: "hex",
			},
			wantErr: "private_encoding",
		},
		{
			// A destination on a service that does not exist would write a
			// half of the keypair to a file nothing ever reads.
			name: "destination service does not exist",
			decl: schema.GeneratedSecretDecl{
				Type: schema.GeneratedSecretSSHEd25519, PublicKey: "PUB", PrivateKey: "ghost:PRIV",
			},
			wantErr: "does not match any service key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &schema.OwnbaseConfig{
				SchemaVersion: "v1",
				Services: map[string]schema.ServiceDecl{
					"svc":   {Repo: "https://github.com/org/svc", GeneratedSecrets: []schema.GeneratedSecretDecl{tc.decl}},
					"other": {Repo: "https://github.com/org/other"},
				},
			}
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestDatabaseRef_Parse(t *testing.T) {
	ref, ok := (schema.ServiceDecl{Database: " postgres / revolve "}).DatabaseRef()
	if !ok {
		t.Fatal("DatabaseRef() reported no declaration")
	}
	if ref.Service != "postgres" || ref.Name != "revolve" {
		t.Errorf("DatabaseRef() = %+v, want postgres/revolve", ref)
	}

	if _, ok := (schema.ServiceDecl{}).DatabaseRef(); ok {
		t.Error("DatabaseRef() reported a declaration for an empty field")
	}
}

func TestDatabase_Validate(t *testing.T) {
	tests := []struct {
		name     string
		database string
		requires []string
		wantErr  string
	}{
		{
			name:     "valid",
			database: "postgres/revolve",
			requires: []string{"postgres"},
		},
		{
			// The old bare-name form. Naming the provider is the whole point:
			// a Base can run two databases, and nothing should depend on one
			// of them happening to be called "postgres".
			name:     "bare database name",
			database: "revolve",
			requires: []string{"postgres"},
			wantErr:  `must be written as "<service>/<dbname>"`,
		},
		{
			name:     "missing database name",
			database: "postgres/",
			requires: []string{"postgres"},
			wantErr:  "missing a database name",
		},
		{
			name:     "provider does not exist",
			database: "ghost/revolve",
			requires: []string{"postgres"},
			wantErr:  "does not match any service key",
		},
		{
			// requires: is what joins the containers to a network. A database
			// on a provider this service cannot reach is not worth accepting.
			name:     "provider not in requires",
			database: "postgres/revolve",
			wantErr:  "must also be listed in requires:",
		},
		{
			name:     "provider is itself",
			database: "svc/revolve",
			requires: []string{"svc"},
			wantErr:  "names this service as its own provider",
		},
		{
			// CREATE DATABASE cannot bind its name as a parameter, so the name
			// is interpolated and has to be an identifier.
			name:     "name is not an identifier",
			database: `postgres/rev"; drop database postgres; --`,
			requires: []string{"postgres"},
			wantErr:  "not a usable database name",
		},
		{
			name:     "name starts with a digit",
			database: "postgres/1db",
			requires: []string{"postgres"},
			wantErr:  "not a usable database name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &schema.OwnbaseConfig{
				SchemaVersion: "v1",
				Services: map[string]schema.ServiceDecl{
					"svc": {
						Repo:     "https://github.com/org/svc",
						Database: tc.database,
						Requires: tc.requires,
					},
					"postgres": {Repo: "https://github.com/org/postgres"},
				},
			}
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate() = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
