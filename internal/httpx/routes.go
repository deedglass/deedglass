package httpx

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Auth is how a route authenticates. The zero value is invalid: Mount panics, so no route ships
// without a declared auth kind.
type Auth uint8

const (
	_ Auth = iota
	// Public needs no credentials.
	Public
	// Session needs a Console session (cookie plus CSRF; S07).
	Session
	// AgentKey accepts any API key (agent or org admin key); the route's Scopes decide which (S08).
	AgentKey
	// SessionOrKey accepts a Console session or an API key.
	SessionOrKey
)

var authNames = [...]string{"invalid", "public", "session", "agent_key", "session_or_key"}

func (a Auth) String() string {
	if int(a) < len(authNames) {
		return authNames[a]
	}
	return fmt.Sprintf("auth(%d)", a)
}

// ParseAuth is the inverse of String (the OpenAPI x-deedglass-auth values).
func ParseAuth(s string) (Auth, bool) {
	for i, n := range authNames {
		if i > 0 && n == s {
			return Auth(i), true
		}
	}
	return 0, false
}

// Idem is a POST route's Idempotency-Key rule. The zero value on a POST is invalid: Mount panics.
type Idem uint8

const (
	_ Idem = iota
	// IdemNever ignores Idempotency-Key (POST /v1/actions, whose Gate owns its idempotency; S13).
	IdemNever
	// IdemOptional stores and replays the response when the client sends a key.
	IdemOptional
	// IdemRequired refuses the request without a key.
	IdemRequired
)

// NoBody as Route.MaxBody refuses any request body.
const NoBody int64 = -1

// Route defaults.
const (
	DefaultTimeout = 30 * time.Second
	DefaultMaxBody = 1 << 20
	BodyJSON       = "application/json"
	BodyForm       = "application/x-www-form-urlencoded"
)

// HandlerFunc is a route handler. Returning an *Error renders it; any other error is logged with
// its cause and rendered as internal_error. A handler must respect r.Context().
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Route declares one endpoint: everything the pipeline needs to know before the handler runs.
type Route struct {
	Method, Pattern string // "GET", "/v1/health" (Go 1.22 mux syntax, which is also OpenAPI path syntax)
	OperationID     string // must exist in api/openapi.yaml
	Auth            Auth
	MinRole         Role // people (sessions): viewer|admin|owner, checked live on every request (S07)
	// Purpose says what the route does, for insufficient_role ("inviting people needs Admin").
	// Default "this request".
	Purpose string
	Scopes  []string // API key scopes; a key must hold every one (S08 defines the set)
	// AgentKeyMessage replaces insufficient_scope's message when an agent key calls a route that
	// needs the admin scope (S12: "Agents cannot read or test policies. …").
	AgentKeyMessage string
	// SkipStopChecks lets a stopped or suspended agent (or a stopped org) through, so it can learn
	// why it is refused: GET /v1/whoami only (S08 §3.5).
	SkipStopChecks bool
	// RecordStopped lets a stopped or suspended agent's key (or any key of a stopped org) through
	// to the handler with its status on the principal, so the refusal is recorded and sealed as a
	// deed instead of turned away at the door: POST /v1/actions only (S13). The handler must
	// refuse it with the stop code.
	RecordStopped bool
	Idempotency   Idem
	// Sensitive: the response carries a shown-once secret. It is stored for replay with the Redact
	// fields set to null, so the secret never reaches the database.
	Sensitive bool
	Redact    []string      // top-level JSON fields to null before storing (default ["secret"] when Sensitive)
	Timeout   time.Duration // default 30 s
	// MaxBody is the body limit in bytes. 0 means 1 MiB on POST, PUT and PATCH, and no body on the
	// other methods; NoBody refuses a body on any method.
	MaxBody  int64
	BodyType string // default "application/json"; Slack interactivity (S29) uses form encoding
	Since    string // the API version that introduced it, e.g. "2026-10-01"
	// Extension marks a route that isn't in the contract catalog, with the ADR that adds it
	// ("ADR-0NN"), or "test" for test-only routes.
	Extension string
	// Guard, when set, admits the request before any other stage and shapes its auth challenges
	// (S09: identity.MCPAuth on /mcp/*).
	Guard   RouteGuard
	Handler HandlerFunc
}

// Key is "METHOD pattern", the mux pattern and the golden-file key.
func (rt Route) Key() string { return rt.Method + " " + rt.Pattern }

func hasBody(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch
}

var validMethods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// normalize applies defaults and validates. It panics on a declaration error: a bad route must
// never start.
func (rt Route) normalize(catalog map[string]ContractRoute) Route {
	fail := func(msg string, args ...any) {
		panic(fmt.Sprintf("httpx: route %s: %s", rt.Key(), fmt.Sprintf(msg, args...)))
	}
	if !slices.Contains(validMethods, rt.Method) {
		fail("method must be one of %v", validMethods)
	}
	if !strings.HasPrefix(rt.Pattern, "/") || strings.ContainsAny(rt.Pattern, " \t") {
		fail("pattern must be a path starting with /")
	}
	if rt.Auth == 0 || int(rt.Auth) >= len(authNames) {
		fail("Auth is not declared (Public, Session, AgentKey or SessionOrKey)")
	}
	if rt.Handler == nil {
		fail("Handler is nil")
	}
	if rt.OperationID == "" {
		fail("OperationID is empty; it must match api/openapi.yaml")
	}
	if rt.Method == http.MethodPost && rt.Idempotency == 0 {
		fail("a POST must declare Idempotency (IdemNever, IdemOptional or IdemRequired)")
	}
	if rt.Method != http.MethodPost && rt.Idempotency > IdemNever {
		fail("only POST routes take Idempotency-Key")
	}
	if rt.Auth == Public && rt.Idempotency == IdemRequired {
		fail("a Public route has no principal to scope Idempotency-Key to")
	}
	if rt.RecordStopped && (rt.Auth != AgentKey || rt.Method != http.MethodPost) {
		fail("RecordStopped is for POST routes that only agent keys call (POST /v1/actions)")
	}
	if rt.Sensitive && rt.Idempotency == IdemNever {
		fail("a Sensitive route is stored for replay, so it can't be IdemNever")
	}
	if rt.MinRole != "" && rt.MinRole.Rank() == 0 {
		fail("MinRole %q is not viewer, admin or owner", rt.MinRole)
	}
	if rt.Purpose == "" {
		rt.Purpose = "this request"
	}
	if rt.Timeout == 0 {
		rt.Timeout = DefaultTimeout
	}
	if rt.Timeout < 0 {
		fail("Timeout must be positive")
	}
	if rt.MaxBody == 0 && hasBody(rt.Method) {
		rt.MaxBody = DefaultMaxBody
	}
	if rt.MaxBody == 0 {
		rt.MaxBody = NoBody
	}
	if rt.BodyType == "" {
		rt.BodyType = BodyJSON
	}
	if rt.Sensitive && len(rt.Redact) == 0 {
		rt.Redact = []string{"secret"}
	}
	if rt.Since == "" {
		rt.Since = latestVersion()
	}
	c, ok := catalog[rt.Key()]
	switch {
	case rt.Extension != "":
		if rt.Extension != "test" && !strings.HasPrefix(rt.Extension, "ADR-") {
			fail("Extension must name an ADR (ADR-0NN) or be \"test\"")
		}
	case !ok:
		fail("isn't in the contract catalog (contract_routes.go); add it there, or set Extension to its ADR")
	case c.Auth != rt.Auth:
		fail("declares auth %s, but the contract catalog says %s", rt.Auth, c.Auth)
	case Role(c.Access).Rank() > 0 && c.Access != string(rt.MinRole):
		fail("declares MinRole %q, but the contract catalog says %s", rt.MinRole, c.Access)
	}
	if (rt.Auth == Session || rt.Auth == SessionOrKey) && rt.MinRole == "" {
		fail("a route people can call must declare MinRole (viewer, admin or owner)")
	}
	return rt
}
