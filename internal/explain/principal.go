package explain

import (
	"context"
	"net/http"

	"github.com/ownbase/ownbase/internal/schema"
)

// principalContextKey is the context key for the acting principal.
type principalContextKey struct{}

// ContextWithPrincipal returns a child context carrying p.
func ContextWithPrincipal(ctx context.Context, p schema.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext returns the principal injected for this request, if any.
func PrincipalFromContext(ctx context.Context) (schema.Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(schema.Principal)
	return p, ok
}

// WithPrincipal returns a shallow clone of r whose context carries p.
func WithPrincipal(r *http.Request, p schema.Principal) *http.Request {
	return r.WithContext(ContextWithPrincipal(r.Context(), p))
}
