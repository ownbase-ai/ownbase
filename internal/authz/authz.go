// Package authz implements the authorization checkpoint and audit log.
//
// Architecture Principle 15: the checkpoint is a first-class component, not
// an afterthought grafted on later. Every Agent action routes through it.
package authz

import (
	"fmt"

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
