package main

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
)

// TestDefaultOwnbaseYAML_IsValid guards the seed written by
// `config setup --init`: it must parse and validate as an ownbase.yaml so the
// first reconcile on a freshly-seeded config repo never fails.
func TestDefaultOwnbaseYAML_IsValid(t *testing.T) {
	cfg, err := schema.ParseConfig(strings.NewReader(defaultOwnbaseYAML))
	if err != nil {
		t.Fatalf("seeded default ownbase.yaml is invalid: %v", err)
	}
	if cfg.SchemaVersion != schema.CurrentSchemaVersion {
		t.Errorf("schema_version = %q, want %q", cfg.SchemaVersion, schema.CurrentSchemaVersion)
	}
	if len(cfg.Services) != 0 {
		t.Errorf("seed should have no services, got %d", len(cfg.Services))
	}
}
