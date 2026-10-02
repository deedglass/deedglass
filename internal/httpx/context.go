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

	// Keys (S08). The key authenticator fills these; the pipeline refuses a stopped org, a stopped
	// agent or a suspended agent (§3.5 steps 6–8) unless the route sets SkipStopChecks.
	AgentName   string // the agent's name, for messages ("Agent \"support-bot\" is stopped.")
	AgentStatus string // "active" | "suspended" | "stopped"; "" for people and admin keys
	OrgStopped  bool   // the org's Stop all overlay is on
}

// Principal kinds.
const (
	KindUser     = "user"
	KindAgentKey = "agent_key"
	KindAdminKey = "admin_key"
	// KindConsole is a person's action through the org's console system agent (S23): AgentID is the
	// console agent, UserID the person. Only server code builds it; no credential authenticates as it.
	KindConsole = "console"
)

// API key scopes (contract §8). Agent keys default to the first three; admin is for org admin keys
// only, which act as the admin role and never as owner.
const (
	ScopeActionsExecute = "actions:execute"
	ScopeActionsRead    = "actions:read"
	ScopeDeedsRead      = "deeds:read"
	ScopeActionsUndo    = "actions:undo" // S23
	ScopeAdmin          = "admin"
)

// AgentScopes are the scopes an agent key may hold; DefaultAgentScopes are what a new key gets.
var (
	AgentScopes        = []string{ScopeActionsExecute, ScopeActionsRead, ScopeDeedsRead, ScopeActionsUndo}
	DefaultAgentScopes = []string{ScopeActionsExecute, ScopeActionsRead, ScopeDeedsRead}
)

// IsKey reports whether the principal is an API key (agent or admin).
func (p Principal) IsKey() bool { return p.Kind == KindAgentKey || p.Kind == KindAdminKey }

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

// KeyRequestHook is implemented by a KeyAuthenticator that also reads request headers once a key
// has passed every check (S09: Deedglass-On-Behalf-Of and Deedglass-Session). An error refuses the
// request. It runs for every route that authenticated with an API key.
type KeyRequestHook interface {
	AfterKey(r *http.Request, p Principal) error
}

// RouteGuard wraps the standard pipeline for a family of routes (S09: the MCP endpoints). Admit
// runs before every other route stage; false means it wrote the response itself. Challenge rewrites
// the WWW-Authenticate value of the route's error responses (adding resource_metadata and scope).
type RouteGuard interface {
	Admit(w http.ResponseWriter, r *http.Request) bool
	Challenge(r *http.Request, value string) string
}

// ErrorRenderer is an optional RouteGuard extension: it writes the route's error responses itself
// (S15: MCP endpoints answer with JSON-RPC error bodies). The status and the error's headers are
// the renderer's to write; the challenge rewrite has already been applied.
type ErrorRenderer interface {
	RenderError(w http.ResponseWriter, r *http.Request, e *Error, requestID string)
}

// SetRequestValue stores v under key for the rest of the request (the context already carries the
// per-request record, so no new context is needed). It is a no-op outside a request.
func SetRequestValue(ctx context.Context, key, v any) {
	i := info(ctx)
	if i == nil {
		return
	}
	if i.values == nil {
		i.values = map[any]any{}
	}
	i.values[key] = v
}

// RequestValue returns what SetRequestValue stored under key.
func RequestValue(ctx context.Context, key any) (any, bool) {
	i := info(ctx)
	if i == nil || i.values == nil {
		return nil, false
	}
	v, ok := i.values[key]
	return v, ok
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
	values          map[any]any // SetRequestValue (S09: on-behalf-of)
	rt              *Route      // the matched route (Recheck)
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
