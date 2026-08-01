package main

import (
	"encoding/json"
	"strings"
	"testing"
)

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
