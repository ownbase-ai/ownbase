package install

import (
	"strings"
	"testing"
)

// Tier-1 tests for buildTemplateOwnbaseYAML — pure string generation, no root
// or Forgejo required. White-box (package install) because the function is
// unexported.

func TestBuildTemplateOwnbaseYAML_DevTLS(t *testing.T) {
	tmpl := buildTemplateOwnbaseYAML("forgejo.mybase.test", "", true)
	if !strings.Contains(tmpl, "dev_tls: true") {
		t.Errorf("expected dev_tls: true in template, got:\n%s", tmpl)
	}
	if strings.Contains(tmpl, "\n    email:") {
		t.Errorf("dev_tls template should not set an active email line, got:\n%s", tmpl)
	}
	if !strings.Contains(tmpl, "domain: forgejo.mybase.test") {
		t.Errorf("expected active forgejo domain line, got:\n%s", tmpl)
	}
}

func TestBuildTemplateOwnbaseYAML_ACME(t *testing.T) {
	tmpl := buildTemplateOwnbaseYAML("git.example.com", "admin@example.com", false)
	if !strings.Contains(tmpl, "domain: git.example.com") {
		t.Errorf("expected active forgejo domain line, got:\n%s", tmpl)
	}
	if !strings.Contains(tmpl, "email: admin@example.com") {
		t.Errorf("expected active caddy email line, got:\n%s", tmpl)
	}
	if strings.Contains(tmpl, "dev_tls") {
		t.Errorf("ACME template should not mention dev_tls, got:\n%s", tmpl)
	}
}

func TestBuildTemplateOwnbaseYAML_NoDomain(t *testing.T) {
	tmpl := buildTemplateOwnbaseYAML("", "", false)
	if !strings.Contains(tmpl, "# domain: git.mysite.com") {
		t.Errorf("expected commented-out domain example, got:\n%s", tmpl)
	}
	if !strings.Contains(tmpl, "# email: you@example.com") {
		t.Errorf("expected commented-out email example, got:\n%s", tmpl)
	}
}
