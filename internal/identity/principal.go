package identity

import (
	"context"

	"github.com/deedglass/deedglass/internal/httpx"
)

// Principal is who made a request. There is one principal type in the whole codebase, S03's
// httpx.Principal: Kind "user" (people, S07), "agent_key" or "admin_key" (S08). UserID is set for
// people (and names the creator of an admin key); Role is the live membership role.
type Principal = httpx.Principal

// Role is a membership role: viewer < admin < owner.
type Role = httpx.Role

// Roles.
const (
	RoleViewer = httpx.RoleViewer
	RoleAdmin  = httpx.RoleAdmin
	RoleOwner  = httpx.RoleOwner
)

// KindUser is Principal.Kind for a person signed in with a session.
const KindUser = "user"

type principalKey struct{}

// WithPrincipal returns ctx carrying p, for code that runs outside an HTTP request (workers,
// tests). Handlers read the request's principal with PrincipalFrom.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal of ctx: the one WithPrincipal stored, else the one the HTTP
// pipeline authenticated.
func PrincipalFrom(ctx context.Context) (*Principal, bool) {
	if p, ok := ctx.Value(principalKey{}).(*Principal); ok && p != nil {
		return p, true
	}
	if p, ok := httpx.PrincipalFrom(ctx); ok {
		return &p, true
	}
	return nil, false
}

// ParseRole reads an API role ("viewer", "admin", "owner").
func ParseRole(s string) (Role, bool) {
	r := Role(s)
	return r, r.Rank() > 0
}
