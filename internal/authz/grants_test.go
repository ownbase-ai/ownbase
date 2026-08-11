package authz_test

import (
	"testing"

	"github.com/ownbase/ownbase/internal/authz"
	"github.com/ownbase/ownbase/internal/schema"
)

func TestGrantsFromConfig(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		Services: map[string]schema.ServiceDecl{
			"opencode": {OwnbaseAccess: []string{"status:read", "service:web:deploy"}},
			"web":      {},
		},
	}
	grants := authz.GrantsFromConfig(cfg)
	if len(grants) != 1 || grants[0].Service != "opencode" {
		t.Fatalf("grants = %+v", grants)
	}
	cp := authz.NewGrantCheckpoint(grants)
	if !authz.ScopeAllowed(cp.ScopesFor("opencode"), authz.ScopeStatusRead) {
		t.Error("status:read should be granted")
	}
	if cp.HasGrant("web") {
		t.Error("web should have no grant")
	}
}

func TestCompositeCheckpoint(t *testing.T) {
	cp := authz.NewCompositeCheckpoint(authz.NewGrantCheckpoint([]authz.Grant{
		{Service: "opencode", Scopes: []string{"service:web:deploy"}},
	}))
	owner := schema.MustNewAction(schema.ActionRestoreApply, "base").WithPrincipal(schema.Owner())
	if err := cp.Authorize(owner); err != nil {
		t.Fatalf("owner: %v", err)
	}
	svc := schema.MustNewAction(schema.ActionServiceRestart, "web").WithPrincipal(schema.Service("opencode"))
	if err := cp.Authorize(svc); err != nil {
		t.Fatalf("service: %v", err)
	}
	deny := schema.MustNewAction(schema.ActionRestoreApply, "base").WithPrincipal(schema.Service("opencode"))
	if err := cp.Authorize(deny); err == nil {
		t.Fatal("expected refuse tier approve for service")
	}
}
