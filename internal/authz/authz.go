// Package authz implements the authorization checkpoint and audit log.
//
// Architecture Principle 15: the checkpoint is a first-class component, not
// an afterthought grafted on later. Every Agent action routes through it.
package authz

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ownbase/ownbase/internal/schema"
)

// Checkpoint is the authorization gate all actions pass through before
// executing. Implementations decide based on the action's principal, type,
// tier, and (for service principals) declared scopes.
type Checkpoint interface {
	// Authorize returns nil if the action is permitted, or an error if it is
	// refused. An action that is not in the taxonomy must always be refused,
	// regardless of policy. Principal is read from action.EffectivePrincipal().
	Authorize(action schema.Action) error
}

// OwnerCheckpoint is the owner-principal policy: every taxonomy-validated
// action is authorized. Risk tiers are recorded in the audit log but do not
// block the owner (device approval is post-V1).
//
// This is what the daemon and CLI use today for bearer-token / reconcile paths.
type OwnerCheckpoint struct{}

// NewOwnerCheckpoint returns the owner policy checkpoint. Safe for concurrent use.
func NewOwnerCheckpoint() Checkpoint {
	return OwnerCheckpoint{}
}

// NewTrivialCheckpoint is a compatibility alias for NewOwnerCheckpoint.
// Prefer NewOwnerCheckpoint in new code.
func NewTrivialCheckpoint() Checkpoint { return NewOwnerCheckpoint() }

// Authorize approves every taxonomied action for the owner. Non-owner
// principals are refused here — use GrantCheckpoint for services.
func (OwnerCheckpoint) Authorize(action schema.Action) error {
	if action.Type == "" {
		return fmt.Errorf("authz: action has empty type (was it created via schema.NewAction?)")
	}
	p := action.EffectivePrincipal()
	if !p.IsOwner() {
		return fmt.Errorf("authz: owner checkpoint refuses principal %s", p)
	}
	return nil
}

// Grant is a declared permission for a service principal. Scopes are closed
// strings matching the ownbase_access taxonomy (P5); P4 enforces tier rules
// and the presence of a grant list.
type Grant struct {
	// Service is the principal name this grant applies to.
	Service string
	// Scopes are allowed action patterns, e.g. "status:read", "service:myapp:deploy".
	// Empty means no scopes — the principal can do nothing.
	Scopes []string
}

// GrantCheckpoint authorizes service principals against a static grant table.
// Owner principals are refused (use OwnerCheckpoint). TierApprove actions are
// always refused for services — there is no approval channel for agents yet.
//
// Scope matching is exact string equality for now; P5 may add patterns.
type GrantCheckpoint struct {
	// ByService maps service name → granted scopes.
	ByService map[string][]string
}

// NewGrantCheckpoint builds a GrantCheckpoint from grants. Safe for concurrent
// read after construction (the map is not mutated).
func NewGrantCheckpoint(grants []Grant) *GrantCheckpoint {
	m := make(map[string][]string, len(grants))
	for _, g := range grants {
		if g.Service == "" {
			continue
		}
		// Copy scopes so callers cannot mutate the table later.
		scopes := append([]string(nil), g.Scopes...)
		m[g.Service] = scopes
	}
	return &GrantCheckpoint{ByService: m}
}

// Authorize enforces service-principal rules.
func (c *GrantCheckpoint) Authorize(action schema.Action) error {
	if action.Type == "" {
		return fmt.Errorf("authz: action has empty type (was it created via schema.NewAction?)")
	}
	p := action.EffectivePrincipal()
	if p.IsOwner() {
		return fmt.Errorf("authz: grant checkpoint refuses owner principal (use OwnerCheckpoint)")
	}
	if !p.IsService() || !p.Valid() {
		return fmt.Errorf("authz: invalid principal %s", p)
	}
	// TierApprove is owner-only until an approval channel exists for agents.
	if action.DefaultTier == schema.TierApprove {
		return fmt.Errorf("authz: action %s is tier approve — refused for service principal %s",
			action.Type, p)
	}
	scopes, ok := c.ByService[p.Name]
	if !ok {
		return fmt.Errorf("authz: service %q has no grants", p.Name)
	}
	want := scopeForAction(action)
	if want == "" {
		return fmt.Errorf("authz: action %s has no scope mapping — refused for services", action.Type)
	}
	if !scopeAllowed(scopes, want) {
		return fmt.Errorf("authz: service %q is not granted scope %q (action %s)", p.Name, want, action.Type)
	}
	return nil
}

// ScopeStatusRead is the grant scope for GET /status (and similar read-only
// observability). Not a taxonomy ActionType — checked directly by the HTTP
// layer via ScopeAllowed.
const ScopeStatusRead = "status:read"

// ScopeConfigRead is the grant scope for GET /config.
const ScopeConfigRead = "config:read"

// ScopeReconcile is the grant scope for POST /reconcile.
const ScopeReconcile = "reconcile"

// ScopeBackupRun is the grant scope for POST /backup/run.
const ScopeBackupRun = "backup:run"

// ScopeBackupVerify is the grant scope for POST /backup/verify.
const ScopeBackupVerify = "backup:verify"

// ScopeSSHKeyRead is the grant scope for GET /ssh-key.
const ScopeSSHKeyRead = "sshkey:read"

// ScopeSecretsRead returns the grant scope for reading secrets of a service.
func ScopeSecretsRead(service string) string { return "secrets:" + service + ":read" }

// ScopeSecretsWrite returns the grant scope for writing secrets of a service.
func ScopeSecretsWrite(service string) string { return "secrets:" + service + ":write" }

// HTTPAccess describes what a service principal needs to call an HTTP route.
// Owner-only routes refuse every service principal, even those granted "*".
type HTTPAccess struct {
	// Scope is the grant string required (e.g. "status:read"). Empty when
	// OwnerOnly is true, or when the route is always allowed (health).
	Scope string
	// OwnerOnly means only the owner principal may call this route.
	OwnerOnly bool
	// AlwaysOK means any service with a non-empty grant entry may call
	// (used for /health liveness only).
	AlwaysOK bool
}

// RouteAccess maps an HTTP method+path to the access rule for service
// principals. Unknown routes and host-mutating routes are owner-only.
// Default-deny: a missing mapping is OwnerOnly, never open.
func RouteAccess(method, path string) HTTPAccess {
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		path = "/"
	}
	method = strings.ToUpper(method)

	if path == "/health" {
		return HTTPAccess{AlwaysOK: true}
	}

	switch path {
	case "/status", "/version", "/core/status", "/db/status":
		if method == http.MethodGet {
			return HTTPAccess{Scope: ScopeStatusRead}
		}
	case "/config":
		if method == http.MethodGet {
			return HTTPAccess{Scope: ScopeConfigRead}
		}
	case "/reconcile":
		if method == http.MethodPost {
			return HTTPAccess{Scope: ScopeReconcile}
		}
	case "/backup/run":
		if method == http.MethodPost {
			return HTTPAccess{Scope: ScopeBackupRun}
		}
	case "/backup/verify":
		if method == http.MethodPost {
			return HTTPAccess{Scope: ScopeBackupVerify}
		}
	case "/ssh-key":
		if method == http.MethodGet {
			return HTTPAccess{Scope: ScopeSSHKeyRead}
		}
		// POST creates/rotates the deploy key — owner only.
		return HTTPAccess{OwnerOnly: true}
	}

	if strings.HasPrefix(path, "/secrets/") || path == "/secrets" {
		return secretsRouteAccess(method, path)
	}

	// /config/source, /self-update, /upgrade, /token/reset, /security/*,
	// /db/restore, and anything else — owner only.
	return HTTPAccess{OwnerOnly: true}
}

func secretsRouteAccess(method, path string) HTTPAccess {
	// /secrets/ or /secrets → list all services (no single target) — owner only.
	rest := strings.TrimPrefix(path, "/secrets")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return HTTPAccess{OwnerOnly: true}
	}
	parts := strings.SplitN(rest, "/", 2)
	service := parts[0]
	if service == "" {
		return HTTPAccess{OwnerOnly: true}
	}
	switch method {
	case http.MethodGet:
		return HTTPAccess{Scope: ScopeSecretsRead(service)}
	case http.MethodPost, http.MethodDelete:
		return HTTPAccess{Scope: ScopeSecretsWrite(service)}
	default:
		return HTTPAccess{OwnerOnly: true}
	}
}

// scopeForAction maps a taxonomy action to a grant scope string. Returns ""
// when the action is not exposable to services at all.
func scopeForAction(action schema.Action) string {
	switch action.Type {
	case schema.ActionServiceStart, schema.ActionServiceRestart, schema.ActionServiceReload:
		// Deploy-ish lifecycle on a named target.
		if action.Target != "" {
			return "service:" + action.Target + ":deploy"
		}
		return "service:deploy"
	case schema.ActionServiceStop:
		if action.Target != "" {
			return "service:" + action.Target + ":stop"
		}
		return "service:stop"
	case schema.ActionDeployApply:
		if action.Target != "" {
			return "service:" + action.Target + ":deploy"
		}
		return "deploy"
	case schema.ActionSecretIssue:
		if action.Target != "" {
			return "secrets:" + action.Target + ":write"
		}
		return "secrets:write"
	case schema.ActionBackupRun:
		return "backup:run"
	case schema.ActionRestoreVerify:
		return "backup:verify"
	// Intentionally unmapped (and therefore refused): restore.apply,
	// host.*, self-update, config source, token reset, etc.
	default:
		return ""
	}
}

// ScopeAllowed reports whether want is covered by granted scopes.
// Exported so the HTTP layer can check non-taxonomy scopes (status:read).
func ScopeAllowed(granted []string, want string) bool {
	return scopeAllowed(granted, want)
}

// ScopesFor reports the scopes granted to service, or nil if none.
func (c *GrantCheckpoint) ScopesFor(service string) []string {
	if c == nil {
		return nil
	}
	return c.ByService[service]
}

// HasGrant reports whether service has any grant entry (even empty scopes).
func (c *GrantCheckpoint) HasGrant(service string) bool {
	if c == nil {
		return false
	}
	_, ok := c.ByService[service]
	return ok
}

func scopeAllowed(granted []string, want string) bool {
	for _, g := range granted {
		if g == want || g == "*" {
			return true
		}
		// Prefix grant: "service:myapp:*" matches "service:myapp:deploy".
		if len(g) > 0 && g[len(g)-1] == '*' {
			prefix := g[:len(g)-1]
			if len(want) >= len(prefix) && want[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}

// GrantsFromConfig builds grant entries from ownbase_access declarations.
func GrantsFromConfig(cfg *schema.OwnbaseConfig) []Grant {
	if cfg == nil {
		return nil
	}
	var out []Grant
	for name, svc := range cfg.Services {
		if len(svc.OwnbaseAccess) == 0 {
			continue
		}
		scopes := append([]string(nil), svc.OwnbaseAccess...)
		out = append(out, Grant{Service: name, Scopes: scopes})
	}
	return out
}

// CompositeCheckpoint routes owner principals to OwnerCheckpoint and service
// principals to GrantCheckpoint.
type CompositeCheckpoint struct {
	Owner  Checkpoint
	Grants *GrantCheckpoint
}

// NewCompositeCheckpoint returns a checkpoint that dispatches by principal.
func NewCompositeCheckpoint(grants *GrantCheckpoint) Checkpoint {
	if grants == nil {
		grants = NewGrantCheckpoint(nil)
	}
	return &CompositeCheckpoint{
		Owner:  NewOwnerCheckpoint(),
		Grants: grants,
	}
}

// Authorize dispatches on principal kind.
func (c *CompositeCheckpoint) Authorize(action schema.Action) error {
	p := action.EffectivePrincipal()
	if p.IsOwner() {
		return c.Owner.Authorize(action)
	}
	return c.Grants.Authorize(action)
}
