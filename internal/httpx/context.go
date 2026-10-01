package httpx

import (
	"context"
	"net/http"

	"github.com/deedglass/deedglass/internal/ids"
)

// Role is a membership role (contract §4). The zero value means no role requirement.
type Role string

const (
	RoleViewer Role = "viewer"
	RoleAdmin  Role = "admin"
	RoleOwner  Role = "owner"
)

// Rank orders roles: viewer < admin < owner. Unknown roles rank 0.
func (r Role) Rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleAdmin:
		return 2
	case RoleOwner:
		return 3
	}
	return 0
}

// AtLeast reports whether r ranks at or above min. An unknown role is never enough.
func (r Role) AtLeast(min Role) bool { return r.Rank() > 0 && r.Rank() >= min.Rank() }

// Title is the role as prose shows it: "Viewer", "Admin", "Owner" (the API keeps lower case).
func (r Role) Title() string {
	switch r {
	case RoleViewer:
		return "Viewer"
	case RoleAdmin:
		return "Admin"
	case RoleOwner:
		return "Owner"
	}
	return string(r)
}

// Principal is who made a request. It is the ONE principal type: S07 aliases it
// (type Principal = httpx.Principal) and adds helpers; S08 fills the key fields.
type Principal struct {
	Kind       string // "user" | "agent_key" | "admin_key" (S23 adds "console")
	OrgID      ids.OrgID
	AgentID    ids.AgentID
	UserID     ids.UserID
	KeyID      ids.APIKeyID
	SessionID  ids.SessionID
	Role       Role
	Scopes     []string
	Livemode   bool
	APIVersion string // the org's pinned version (orgs.api_version)
	OrgName    string // for messages ("Your role in Acme is Viewer, …"); people only (S07)
	// MembershipID is the person's membership in OrgID (S07).
	MembershipID ids.MembershipID
}

// Ref is the principal as idempotency scopes it: the agent for agent keys (so rolling a key keeps
// its stored responses), the key for org admin keys, the user for people.
func (p Principal) Ref() string {
	switch {
	case !p.AgentID.IsZero():
		return p.AgentID.String()
	case !p.UserID.IsZero():
		return p.UserID.String()
	case !p.KeyID.IsZero():
		return p.KeyID.String()
	}
	return ""
}

// KeyAuthenticator checks a bearer API key (S08). It returns an *Error (invalid_api_key,
// api_key_revoked, …) for a key it refuses.
type KeyAuthenticator interface {
	AuthenticateKey(ctx context.Context, bearer string) (Principal, error)
}

// SessionAuthenticator checks the Console session cookie and CSRF (S07).
type SessionAuthenticator interface {
	AuthenticateSession(ctx context.Context, r *http.Request) (Principal, error)
}

// reqInfo is the mutable per-request record the access log reads at the end. Later stages fill
// org, agent and action ids through Annotate.
type reqInfo struct {
	requestID       string
	clientRequestID string
	ip              string
	route           string // the pattern, or "unmatched"
	apiVersion      string
	principal       *Principal
	orgID           string
	agentID         string
	actionID        string
	replayed        bool
}

type ctxKey struct{}

func info(ctx context.Context) *reqInfo {
	i, _ := ctx.Value(ctxKey{}).(*reqInfo)
	return i
}

// RequestID returns the request's req_… id, or "".
func RequestID(ctx context.Context) string {
	if i := info(ctx); i != nil {
		return i.requestID
	}
	return ""
}

// APIVersion returns the resolved Deedglass-Version for the request.
func APIVersion(ctx context.Context) string {
	if i := info(ctx); i != nil && i.apiVersion != "" {
		return i.apiVersion
	}
	return latestVersion()
}

// PrincipalFrom returns the authenticated principal, if any.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	if i := info(ctx); i != nil && i.principal != nil {
		return *i.principal, true
	}
	return Principal{}, false
}

// ClientIP returns the client address the realIP stage resolved.
func ClientIP(ctx context.Context) string {
	if i := info(ctx); i != nil {
		return i.ip
	}
	return ""
}

// Annotate adds ids to the request's access-log line. Empty values are ignored.
func Annotate(ctx context.Context, orgID, agentID, actionID string) {
	i := info(ctx)
	if i == nil {
		return
	}
	if orgID != "" {
		i.orgID = orgID
	}
	if agentID != "" {
		i.agentID = agentID
	}
	if actionID != "" {
		i.actionID = actionID
	}
}
