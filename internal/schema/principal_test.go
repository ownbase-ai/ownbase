package schema_test

import (
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
)

func TestPrincipal_OwnerAndService(t *testing.T) {
	o := schema.Owner()
	if !o.IsOwner() || o.IsService() || !o.Valid() {
		t.Errorf("Owner = %+v", o)
	}
	if o.String() != "owner" {
		t.Errorf("String = %q", o.String())
	}
	s := schema.Service("opencode")
	if !s.IsService() || s.IsOwner() || !s.Valid() {
		t.Errorf("Service = %+v", s)
	}
	if s.String() != "service:opencode" {
		t.Errorf("String = %q", s.String())
	}
	if schema.Service("").Valid() {
		t.Error("empty service name should be invalid")
	}
}

func TestAction_WithPrincipalAndSession(t *testing.T) {
	a := schema.MustNewAction(schema.ActionBackupRun, "base")
	if !a.EffectivePrincipal().IsOwner() {
		t.Error("default principal should be owner")
	}
	a = a.WithPrincipal(schema.Service("harness")).WithSessionID("abc")
	if a.EffectivePrincipal().String() != "service:harness" {
		t.Errorf("principal = %s", a.EffectivePrincipal())
	}
	if a.SessionID != "abc" {
		t.Errorf("session = %q", a.SessionID)
	}
}
