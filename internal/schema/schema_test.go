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
		{"missing-build-context.yaml", "source"},
		{"source-is-url.yaml", "repo-relative path"},
		{"mirror-is-not-url.yaml", "mirror must be a git URL"},
		{"missing-capability-provider.yaml", "capability"},
		{"unknown-field.yaml", "unexpected_field"},
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
			"auth": {Source: "services/auth"},
		},
	}
	warns := cfg.Warnings()
	if len(warns) == 0 {
		t.Error("expected a warning for service with no ref")
	}
	found := false
	for _, w := range warns {
		if strings.Contains(w, "auto-pin") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected auto-pin warning, got: %v", warns)
	}
}

func TestParseConfig_Warnings_DeprecatedMode(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Source: "services/auth", Mode: "managed", Ref: "v1.0.0"},
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
			"auth": {Source: "services/auth", Mode: "pinned"},
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
			"auth": {Source: "services/auth", Ref: "v1.0.0"},
		},
	}
	if warns := cfg.Warnings(); len(warns) != 0 {
		t.Errorf("expected no warnings for service with ref set and no mode, got: %v", warns)
	}
}

// ---------------------------------------------------------------------------
// Mirror service validation
// ---------------------------------------------------------------------------

func TestParseConfig_MirrorService_ValidURL(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"postgres": {Mirror: "https://github.com/docker-library/postgres"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected valid mirror service, got: %v", err)
	}
}

func TestParseConfig_MirrorService_InvalidURL(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"postgres": {Mirror: "docker-library/postgres"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for mirror without URL scheme")
	} else if !strings.Contains(err.Error(), "mirror must be a git URL") {
		t.Errorf("error %q does not mention 'mirror must be a git URL'", err.Error())
	}
}

func TestParseConfig_MirrorAndSource_Exclusive(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"auth": {Source: "services/auth", Mirror: "https://github.com/org/auth"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for source + mirror together")
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
	if cfg.Services["auth"].Source != "services/auth" {
		t.Errorf("auth.Source = %q, want services/auth", cfg.Services["auth"].Source)
	}
	if cfg.Services["postgres"].Mirror != "https://github.com/docker-library/postgres" {
		t.Errorf("postgres.Mirror = %q, want https://github.com/docker-library/postgres",
			cfg.Services["postgres"].Mirror)
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
