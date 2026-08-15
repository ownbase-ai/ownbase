package authz_test

import (
	"net/http"
	"testing"

	"github.com/ownbase/ownbase/internal/authz"
)

func TestRouteAccess_DefaultDeny(t *testing.T) {
	cases := []struct {
		method, path string
		wantScope    string
		ownerOnly    bool
		alwaysOK     bool
	}{
		{"GET", "/health", "", false, true},
		{"GET", "/status", authz.ScopeStatusRead, false, false},
		{"GET", "/status/", authz.ScopeStatusRead, false, false},
		{"GET", "/version", authz.ScopeStatusRead, false, false},
		{"GET", "/config", authz.ScopeConfigRead, false, false},
		{"POST", "/config", authz.ScopeConfigWrite, false, false},
		{"POST", "/reconcile", authz.ScopeReconcile, false, false},
		{"POST", "/backup/run", authz.ScopeBackupRun, false, false},
		{"POST", "/backup/verify", authz.ScopeBackupVerify, false, false},
		{"POST", "/backup/prune", "", true, false},
		{"GET", "/ssh-key", authz.ScopeSSHKeyRead, false, false},
		{"POST", "/ssh-key", "", true, false},
		{"GET", "/secrets/myapp", authz.ScopeSecretsRead("myapp"), false, false},
		{"GET", "/secrets/myapp/KEY", authz.ScopeSecretsRead("myapp"), false, false},
		{"POST", "/secrets/myapp", authz.ScopeSecretsWrite("myapp"), false, false},
		{"DELETE", "/secrets/myapp/KEY", authz.ScopeSecretsWrite("myapp"), false, false},
		{"GET", "/secrets/", "", true, false},
		{"POST", "/config/source", "", true, false},
		{"POST", "/self-update", "", true, false},
		{"POST", "/token/reset", "", true, false},
		{"POST", "/security/fix", "", true, false},
		{"POST", "/db/restore", "", true, false},
		{"POST", "/upgrade", "", true, false},
		{"GET", "/unknown", "", true, false},
	}
	for _, tc := range cases {
		got := authz.RouteAccess(tc.method, tc.path)
		if got.AlwaysOK != tc.alwaysOK || got.OwnerOnly != tc.ownerOnly || got.Scope != tc.wantScope {
			t.Errorf("%s %s = %+v, want scope=%q ownerOnly=%v alwaysOK=%v",
				tc.method, tc.path, got, tc.wantScope, tc.ownerOnly, tc.alwaysOK)
		}
	}
}

func TestRouteAccess_MethodMatters(t *testing.T) {
	// GET /reconcile is not a thing — owner only (default deny).
	got := authz.RouteAccess(http.MethodGet, "/reconcile")
	if !got.OwnerOnly {
		t.Errorf("GET /reconcile should be owner-only, got %+v", got)
	}
}
