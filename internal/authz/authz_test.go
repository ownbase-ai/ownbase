package authz_test

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/schema"
)

func TestOwnerCheckpoint_AllowsOwner(t *testing.T) {
	cp := authz.NewOwnerCheckpoint()
	a := schema.MustNewAction(schema.ActionServiceStart, "web").WithPrincipal(schema.Owner())
	if err := cp.Authorize(a); err != nil {
		t.Fatalf("Authorize owner: %v", err)
	}
	// Empty principal defaults to owner.
	a2 := schema.MustNewAction(schema.ActionRestoreApply, "base")
	if err := cp.Authorize(a2); err != nil {
		t.Fatalf("Authorize empty principal (owner default): %v", err)
	}
}

func TestOwnerCheckpoint_RefusesService(t *testing.T) {
	cp := authz.NewOwnerCheckpoint()
	a := schema.MustNewAction(schema.ActionServiceStart, "web").WithPrincipal(schema.Service("opencode"))
	if err := cp.Authorize(a); err == nil {
		t.Fatal("expected refuse for service principal")
	}
}

func TestOwnerCheckpoint_RefusesEmptyType(t *testing.T) {
	cp := authz.NewOwnerCheckpoint()
	if err := cp.Authorize(schema.Action{}); err == nil {
		t.Fatal("expected refuse for empty type")
	}
}

func TestGrantCheckpoint_AllowsScopedAction(t *testing.T) {
	cp := authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "opencode", Scopes: []string{"service:web:deploy"}},
	})
	a := schema.MustNewAction(schema.ActionServiceRestart, "web").WithPrincipal(schema.Service("opencode"))
	if err := cp.Authorize(a); err != nil {
		t.Fatalf("Authorize granted scope: %v", err)
	}
}

func TestGrantCheckpoint_RefusesUngranted(t *testing.T) {
	cp := authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "opencode", Scopes: []string{"status:read"}},
	})
	a := schema.MustNewAction(schema.ActionServiceRestart, "web").WithPrincipal(schema.Service("opencode"))
	err := cp.Authorize(a)
	if err == nil {
		t.Fatal("expected refuse")
	}
	if !strings.Contains(err.Error(), "not granted") {
		t.Errorf("error = %v, want not granted", err)
	}
}

func TestGrantCheckpoint_RefusesTierApprove(t *testing.T) {
	cp := authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "opencode", Scopes: []string{"*"}},
	})
	a := schema.MustNewAction(schema.ActionRestoreApply, "base").WithPrincipal(schema.Service("opencode"))
	err := cp.Authorize(a)
	if err == nil {
		t.Fatal("expected refuse for tier approve")
	}
	if !strings.Contains(err.Error(), "approve") {
		t.Errorf("error = %v, want approve", err)
	}
}

func TestGrantCheckpoint_RefusesUnknownService(t *testing.T) {
	cp := authz.NewGrantCheckpoint(nil)
	a := schema.MustNewAction(schema.ActionBackupRun, "base").WithPrincipal(schema.Service("nope"))
	if err := cp.Authorize(a); err == nil {
		t.Fatal("expected refuse for unknown service")
	}
}

func TestGrantCheckpoint_WildcardScope(t *testing.T) {
	cp := authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "opencode", Scopes: []string{"service:web:*"}},
	})
	a := schema.MustNewAction(schema.ActionServiceStart, "web").WithPrincipal(schema.Service("opencode"))
	if err := cp.Authorize(a); err != nil {
		t.Fatalf("prefix wildcard: %v", err)
	}
}

func TestAuditLog_RecordsPrincipal(t *testing.T) {
	mem := &authz.MemAuditLog{}
	a := schema.MustNewAction(schema.ActionHostPatch, "host").
		WithPrincipal(schema.Service("ops")).
		WithSessionID("sess-1")
	if err := mem.Record(a, authz.OutcomeApplied, ""); err != nil {
		t.Fatal(err)
	}
	if len(mem.Records) != 1 {
		t.Fatalf("records = %d", len(mem.Records))
	}
	r := mem.Records[0]
	if r.Principal != "service:ops" {
		t.Errorf("Principal = %q, want service:ops", r.Principal)
	}
	if r.SessionID != "sess-1" {
		t.Errorf("SessionID = %q", r.SessionID)
	}
}

func TestNewTrivialCheckpoint_Alias(t *testing.T) {
	// Compatibility alias still works for older call sites / tests.
	cp := authz.NewTrivialCheckpoint()
	a := schema.MustNewAction(schema.ActionServiceStart, "x")
	if err := cp.Authorize(a); err != nil {
		t.Fatal(err)
	}
}
