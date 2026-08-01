package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/vault"
)

func TestParseConfigSourceYAML(t *testing.T) {
	url, ref := parseConfigSourceYAML([]byte("repo_url: git@github.com:org/c.git\nref: main\n"))
	if url != "git@github.com:org/c.git" || ref != "main" {
		t.Fatalf("got %q %q", url, ref)
	}
	url, ref = parseConfigSourceYAML([]byte("repo_url: git@x/y.git\n"))
	if url != "git@x/y.git" || ref != "" {
		t.Fatalf("got %q %q", url, ref)
	}
	url, ref = parseConfigSourceYAML([]byte("not: yaml: :"))
	if url != "" || ref != "" {
		t.Fatalf("expected empty on bad yaml, got %q %q", url, ref)
	}
}

func TestApplyConfigSource(t *testing.T) {
	var p vault.Profile
	applyConfigSource(&p, "git@a/b.git", "dev")
	if p.ConfigRepoURL != "git@a/b.git" || p.ConfigRef != "dev" {
		t.Fatalf("got %+v", p)
	}
	applyConfigSource(&p, "git@c/d.git", "")
	if p.ConfigRepoURL != "git@c/d.git" || p.ConfigRef != "dev" {
		// empty ref must not wipe an existing one
		t.Fatalf("empty ref wiped existing: %+v", p)
	}
	var q vault.Profile
	applyConfigSource(&q, "git@e/f.git", "")
	if q.ConfigRef != vault.DefaultConfigRef {
		t.Fatalf("expected default ref, got %q", q.ConfigRef)
	}
}

func TestConfigFromStatusJSON(t *testing.T) {
	url, ref := configFromStatusJSON([]byte(`{"config":{"repo_url":"git@x/y.git","ref":"main"}}`))
	if url != "git@x/y.git" || ref != "main" {
		t.Fatalf("got %q %q", url, ref)
	}
	url, ref = configFromStatusJSON([]byte(`{"services":[]}`))
	if url != "" || ref != "" {
		t.Fatalf("expected empty, got %q %q", url, ref)
	}
}

func TestInjectStatusConfig(t *testing.T) {
	in := []byte(`{"schema_version":"v3","services":[]}`)
	out, err := injectStatusConfig(in, "git@github.com:org/c.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["services"]; !ok {
		t.Fatal("services missing after inject")
	}
	url, ref := configFromStatusJSON(out)
	if url != "git@github.com:org/c.git" || ref != "main" {
		t.Fatalf("injected config = %q %q", url, ref)
	}
	if !strings.Contains(string(out), `"repo_url"`) {
		t.Fatalf("output missing repo_url: %s", out)
	}
}

func TestEnsureConfigKnown_PreservesWhenPresent(t *testing.T) {
	in := []byte(`{"config":{"repo_url":"git@a/b.git","ref":"dev"},"services":[]}`)
	// No vault available in unit tests — ensureConfigKnown should still return
	// body unchanged when config is already present (sync is best-effort).
	out := ensureConfigKnown("nonexistent-base-for-test", in)
	url, ref := configFromStatusJSON(out)
	if url != "git@a/b.git" || ref != "dev" {
		t.Fatalf("got %q %q from %s", url, ref, out)
	}
}
