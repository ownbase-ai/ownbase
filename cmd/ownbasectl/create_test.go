package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/vmhost"
)

// fakeVMRunner is a minimal vmhost.Runner fake for exercising code that
// calls m.Exec (which becomes "exec <name> -- <command...>" under the
// hood) without touching a real Multipass VM. Responses are keyed by a
// prefix of the joined argument string.
type fakeVMRunner struct {
	responses map[string]string
	errs      map[string]error
}

func (f *fakeVMRunner) Run(_ context.Context, args ...string) (string, error) {
	key := strings.Join(args, " ")
	for prefix, err := range f.errs {
		if strings.HasPrefix(key, prefix) {
			return f.responses[prefix], err
		}
	}
	for prefix, resp := range f.responses {
		if strings.HasPrefix(key, prefix) {
			return resp, nil
		}
	}
	return "", nil
}

// ---------------------------------------------------------------------------
// detectPostInstallDevTLS
//
// Regression coverage for the Bugbot finding "Restore breaks local dev-TLS":
// a restored Base's *actual* ownbase.yaml (not what this create/restore run
// itself decided) must be the source of truth for whether dev-TLS is on.
// ---------------------------------------------------------------------------

const catOwnbaseYAML = "exec myvm -- sudo cat /opt/ownbase/checkout/ownbase.yaml"

func TestDetectPostInstallDevTLS_DevTLSEnabled(t *testing.T) {
	yaml := "schema_version: v1\ncore:\n  forgejo:\n    domain: forgejo.origbase.test\n  caddy:\n    dev_tls: true\nservices:\n  app:\n    source: apps/app\n    port: 3000\n    domain: app.origbase.test\n"
	m := &vmhost.Multipass{Runner: &fakeVMRunner{responses: map[string]string{catOwnbaseYAML: yaml}}}

	domain, hostnames, ok := detectPostInstallDevTLS(context.Background(), m, "myvm")
	if !ok {
		t.Fatal("expected ok=true for a restored config with dev_tls: true and a forgejo.<domain>")
	}
	if domain != "origbase.test" {
		t.Errorf("domain = %q, want origbase.test", domain)
	}
	wantHosts := []string{"app.origbase.test", "forgejo.origbase.test"}
	if strings.Join(hostnames, ",") != strings.Join(wantHosts, ",") {
		t.Errorf("hostnames = %v, want %v", hostnames, wantHosts)
	}
}

func TestDetectPostInstallDevTLS_DevTLSDisabled(t *testing.T) {
	yaml := "schema_version: v1\ncore:\n  forgejo:\n    domain: git.realdomain.com\n  caddy:\n    email: ops@realdomain.com\n"
	m := &vmhost.Multipass{Runner: &fakeVMRunner{responses: map[string]string{catOwnbaseYAML: yaml}}}

	_, _, ok := detectPostInstallDevTLS(context.Background(), m, "myvm")
	if ok {
		t.Error("expected ok=false for a restored config that uses ACME, not dev-TLS")
	}
}

func TestDetectPostInstallDevTLS_NoForgejoDomain(t *testing.T) {
	yaml := "schema_version: v1\ncore:\n  caddy:\n    dev_tls: true\nservices:\n  app:\n    source: apps/app\n    port: 3000\n    domain: app.origbase.test\n"
	m := &vmhost.Multipass{Runner: &fakeVMRunner{responses: map[string]string{catOwnbaseYAML: yaml}}}

	_, _, ok := detectPostInstallDevTLS(context.Background(), m, "myvm")
	if ok {
		t.Error("expected ok=false when there is no core.forgejo.domain to derive the wildcard domain from")
	}
}

func TestDetectPostInstallDevTLS_ForgejoDomainDoesNotFollowConvention(t *testing.T) {
	// dev_tls: true but core.forgejo.domain was hand-edited to not follow
	// the "forgejo.<domain>" convention every dev-TLS Base is created
	// with — we must not guess wrong and generate a cert for the wrong
	// wildcard domain.
	yaml := "schema_version: v1\ncore:\n  forgejo:\n    domain: git.origbase.test\n  caddy:\n    dev_tls: true\n"
	m := &vmhost.Multipass{Runner: &fakeVMRunner{responses: map[string]string{catOwnbaseYAML: yaml}}}

	_, _, ok := detectPostInstallDevTLS(context.Background(), m, "myvm")
	if ok {
		t.Error("expected ok=false when core.forgejo.domain does not start with \"forgejo.\"")
	}
}

func TestDetectPostInstallDevTLS_ReadFailure(t *testing.T) {
	m := &vmhost.Multipass{Runner: &fakeVMRunner{
		errs:      map[string]error{catOwnbaseYAML: errAborted},
		responses: map[string]string{},
	}}

	_, _, ok := detectPostInstallDevTLS(context.Background(), m, "myvm")
	if ok {
		t.Error("expected ok=false when ownbase.yaml cannot be read from the VM")
	}
}
