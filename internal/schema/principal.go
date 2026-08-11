package schema

import "fmt"

// PrincipalKind classifies who is asking to act.
type PrincipalKind string

const (
	// PrincipalOwner is the human operator (or anything holding the Base's
	// owner credential — CLI, desktop app). Full autonomy under V1 policy.
	PrincipalOwner PrincipalKind = "owner"
	// PrincipalService is an on-Base service (or replica) acting through a
	// scoped grant. Subject to tier enforcement and scope checks.
	PrincipalService PrincipalKind = "service"
)

// Principal is the subject of an authorization decision and an audit record.
// Zero value is invalid — use Owner() or Service().
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	// Name is the service name when Kind is PrincipalService; empty for owner.
	Name string `json:"name,omitempty"`
}

// Owner returns the owner principal.
func Owner() Principal {
	return Principal{Kind: PrincipalOwner}
}

// Service returns a service principal for the named service.
func Service(name string) Principal {
	return Principal{Kind: PrincipalService, Name: name}
}

// IsOwner reports whether p is the owner principal.
func (p Principal) IsOwner() bool { return p.Kind == PrincipalOwner }

// IsService reports whether p is a service principal.
func (p Principal) IsService() bool { return p.Kind == PrincipalService }

// Valid reports whether p has a known kind (and a name when required).
func (p Principal) Valid() bool {
	switch p.Kind {
	case PrincipalOwner:
		return true
	case PrincipalService:
		return p.Name != ""
	default:
		return false
	}
}

// String is a stable, secret-free label for logs and audit records.
func (p Principal) String() string {
	switch p.Kind {
	case PrincipalOwner:
		return "owner"
	case PrincipalService:
		if p.Name == "" {
			return "service:"
		}
		return "service:" + p.Name
	default:
		return fmt.Sprintf("principal:%s:%s", p.Kind, p.Name)
	}
}
