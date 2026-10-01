package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/buildinfo"
)

// ErrType is the error envelope's "type" (contract §8).
type ErrType string

const (
	TypeInvalidRequest ErrType = "invalid_request_error"
	TypeAuthentication ErrType = "authentication_error"
	TypePermission     ErrType = "permission_error"
	TypePolicy         ErrType = "policy_error"
	TypeRateLimit      ErrType = "rate_limit_error"
	TypeAPI            ErrType = "api_error"
)

// Code is the error envelope's "code". Codes are never renamed (contract §8).
type Code string

// The contract's 24 codes, then S03's 9 additions, then S07's 6.
const (
	CodeInvalidAPIKey        Code = "invalid_api_key"
	CodeAPIKeyRevoked        Code = "api_key_revoked"
	CodeAPIKeyExpired        Code = "api_key_expired"
	CodeAgentSuspended       Code = "agent_suspended"
	CodeAgentStopped         Code = "agent_stopped"
	CodeOrgStopped           Code = "org_stopped"
	CodeInsufficientScope    Code = "insufficient_scope"
	CodePolicyDenied         Code = "policy_denied"
	CodeApprovalRequired     Code = "approval_required"
	CodeApprovalExpired      Code = "approval_expired"
	CodeBudgetExceeded       Code = "budget_exceeded"
	CodeIntentRequired       Code = "intent_required"
	CodeIntentInvalid        Code = "intent_invalid"
	CodeIntentReplay         Code = "intent_replay"
	CodeToolUnknown          Code = "tool_unknown"
	CodeConnectorUnavailable Code = "connector_unavailable"
	CodeUpstreamError        Code = "upstream_error"
	CodeTimeout              Code = "timeout"
	CodeRateLimited          Code = "rate_limited"
	CodeIdempotencyConflict  Code = "idempotency_conflict"
	CodeValidationFailed     Code = "validation_failed"
	CodeNotFound             Code = "not_found"
	CodeConflict             Code = "conflict"
	CodeInternalError        Code = "internal_error"

	CodeAuthenticationRequired Code = "authentication_required"
	CodeMethodNotAllowed       Code = "method_not_allowed"
	CodeRequestTooLarge        Code = "request_too_large"
	CodeUnsupportedMediaType   Code = "unsupported_media_type"
	CodeInvalidJSON            Code = "invalid_json"
	CodeAPIVersionInvalid      Code = "api_version_invalid"
	CodeIdempotencyKeyInvalid  Code = "idempotency_key_invalid"
	CodeIdempotencyKeyInUse    Code = "idempotency_key_in_use"
	CodeServiceUnavailable     Code = "service_unavailable"

	CodeSessionExpired   Code = "session_expired"
	CodeCSRFFailed       Code = "csrf_failed"
	CodeInsufficientRole Code = "insufficient_role"
	CodeLoginLinkInvalid Code = "login_link_invalid"
	CodeLoginCodeInvalid Code = "login_code_invalid"
	CodeLastOwner        Code = "last_owner"
)

// DocURLPrefix is where every code has a page (docs/api/errors.md, published by S33).
const DocURLPrefix = "https://docs.deedglass.com/errors/"

// CatalogEntry is one code: its status, type and copy. This file is the one place error copy
// lives (contract §8, brand §4).
type CatalogEntry struct {
	Code    Code
	Status  int
	Type    ErrType
	Message string // rendered when the call site gives nothing more exact; never has placeholders
	// Template is the exact form a call site fills ({…} placeholders), for docs. "" when Message is
	// already exact.
	Template string
	Added    string // the session that added the code
}

var catalog = []CatalogEntry{
	{CodeInvalidAPIKey, 401, TypeAuthentication, "The API key isn't valid. Check for a typo, or create a new key in Console › Agents.", "", "contract"},
	{CodeAPIKeyRevoked, 401, TypeAuthentication, "The API key has been revoked. Create a new key in Console › Agents.", "", "contract"},
	{CodeAPIKeyExpired, 401, TypeAuthentication, "The API key expired. Roll it in Console › Agents › Keys.", "", "contract"},
	{CodeAgentSuspended, 403, TypePermission, "This agent is suspended. Resume it in Console › Agents to continue.", "", "contract"},
	{CodeAgentStopped, 403, TypePermission, "This agent was stopped. An admin can start it again in Console › Agents.", "", "contract"},
	{CodeOrgStopped, 403, TypePermission, "All agents in this org are stopped. An owner can start them in Console › Settings.", "", "contract"},
	{CodeInsufficientScope, 403, TypePermission, "This key doesn't have the scope this endpoint needs. Create a key with the scope the API reference lists for it.",
		"This key doesn't have the scope this endpoint needs. Create a key with the {scope} scope.", "contract"},
	{CodePolicyDenied, 403, TypePolicy, "Blocked by policy. The action in Console › Actions names the rule and the reason.",
		`Blocked by policy "{rule}": {reason}.`, "contract"},
	{CodeApprovalRequired, 403, TypePolicy, "This action needs approval. It runs once someone approves it in Console › Approvals.", "", "contract"},
	{CodeApprovalExpired, 403, TypePolicy, "No one decided before the approval window closed. Send the action again to ask for a new approval.", "", "contract"},
	{CodeBudgetExceeded, 429, TypeRateLimit, "This agent has used its budget for the period. It resets when the next period starts, or an admin can raise it.",
		"This agent has used its budget for the period. It resets at {time}, or an admin can raise it.", "contract"},
	{CodeIntentRequired, 403, TypePermission, "This agent must sign its requests. Send a Deedglass-Intent header.", "", "contract"},
	{CodeIntentInvalid, 403, TypePermission, "The signed intent doesn't match this request. Sign the exact tool and arguments you send.", "", "contract"},
	{CodeIntentReplay, 403, TypePermission, "This signed intent was already used. Sign a new one with a fresh nonce.", "", "contract"},
	{CodeToolUnknown, 404, TypeInvalidRequest, "No connector provides this tool. Check the name, or add a connector in Console › Connectors.",
		`No connector provides "{tool}". Check the name, or add a connector in Console › Connectors.`, "contract"},
	{CodeConnectorUnavailable, 503, TypeAPI, "The connector isn't reachable right now. Check Console › Connectors, then retry.", "", "contract"},
	{CodeUpstreamError, 502, TypeAPI, "The tool returned an error. The details are on the action in Console › Actions.", "", "contract"},
	{CodeTimeout, 504, TypeAPI, "The request timed out. It may have partly run, so fetch the object to see its state before you send it again.",
		"The request timed out after {n} s. It may have partly run, so fetch the object to see its state before you send it again.", "contract"},
	{CodeRateLimited, 429, TypeRateLimit, "Too many requests. Retry after the number of seconds in the Retry-After header.",
		"Too many requests. Retry after {n} s.", "contract"},
	{CodeIdempotencyConflict, 422, TypeInvalidRequest, "This Idempotency-Key was already used with a different request. Use a new key for a new request.", "", "contract"},
	{CodeValidationFailed, 400, TypeInvalidRequest, "A field in the request isn't valid. The param field names it.",
		"{field-specific sentence}.", "contract"},
	{CodeNotFound, 404, TypeInvalidRequest, "There's no object with that id in this org. Check the id.",
		"There's no {object} with id {id}. / There's no endpoint at {METHOD} {path}. Did you mean {p}?", "contract"},
	{CodeConflict, 409, TypeInvalidRequest, "That conflicts with the current state. Fetch the latest version, then retry.", "", "contract"},
	{CodeInternalError, 500, TypeAPI, "Something went wrong on our side. It's logged under this request id. Retry, and share the id if it happens again.", "", "contract"},

	{CodeAuthenticationRequired, 401, TypeAuthentication, "This endpoint needs an API key (Authorization: Bearer dg_live_…) or a Console session.", "", "S03"},
	{CodeMethodNotAllowed, 405, TypeInvalidRequest, "This method isn't supported on this path. The Allow header lists the ones that are.",
		"{METHOD} isn't supported on {path}. Use {allowed}.", "S03"},
	{CodeRequestTooLarge, 413, TypeInvalidRequest, "The request body is too large for this endpoint. Send a smaller body.",
		"The request body is larger than {limit}. Send a smaller body.", "S03"},
	{CodeUnsupportedMediaType, 415, TypeInvalidRequest, "Send the body as JSON, with Content-Type: application/json.", "", "S03"},
	{CodeInvalidJSON, 400, TypeInvalidRequest, "The body isn't valid JSON. Fix it and retry.",
		"The body isn't valid JSON (line {l}, column {c}). Fix it and retry.", "S03"},
	{CodeAPIVersionInvalid, 400, TypeInvalidRequest, "Deedglass-Version isn't a known version. Use " + buildinfo.LatestAPIVersion + ", or leave it out to use your org's pinned version.", "", "S03"},
	{CodeIdempotencyKeyInvalid, 400, TypeInvalidRequest, "Idempotency-Key must be 1–255 printable ASCII characters.", "", "S03"},
	{CodeIdempotencyKeyInUse, 409, TypeInvalidRequest, "A request with this Idempotency-Key is still running. Retry in a moment.", "", "S03"},
	{CodeServiceUnavailable, 503, TypeAPI, "Deedglass is starting or shutting down. Retry in a few seconds.", "", "S03"},

	{CodeSessionExpired, 401, TypeAuthentication, "Your session ended. Sign in again.",
		"Your session ended after 12 hours without activity. Sign in again at {base}/login.", "S07"},
	{CodeCSRFFailed, 403, TypePermission, "This request is missing its security token. Reload the page and try again.", "", "S07"},
	{CodeInsufficientRole, 403, TypePermission, "Your role in this org doesn't allow this. Ask an owner to change your role.",
		"Your role in {org} is {Role}, and {what} needs {MinRole}. Ask an owner to change your role.", "S07"},
	{CodeLoginLinkInvalid, 400, TypeInvalidRequest, "This sign-in link has expired or was already used. Get a new one at /login.",
		"This sign-in link has expired or was already used. Get a new one at {base}/login.", "S07"},
	{CodeLoginCodeInvalid, 400, TypeInvalidRequest, "That code doesn't match, or it has expired. Check the latest email, or get a new code.", "", "S07"},
	{CodeLastOwner, 409, TypeInvalidRequest, "This org needs at least one owner. Make someone else an owner first.",
		"{org} needs at least one owner. Make someone else an owner first.", "S07"},
}

var byCode = func() map[Code]CatalogEntry {
	m := make(map[Code]CatalogEntry, len(catalog))
	for _, c := range catalog {
		if _, dup := m[c.Code]; dup {
			panic("httpx: duplicate error code " + string(c.Code))
		}
		m[c.Code] = c
	}
	return m
}()

// Catalog returns every error code, in contract order. The slice is a copy.
func Catalog() []CatalogEntry { return append([]CatalogEntry(nil), catalog...) }

// Lookup returns the catalog entry for c.
func Lookup(c Code) (CatalogEntry, bool) { e, ok := byCode[c]; return e, ok }

// Error is an API error. Handlers return it; the pipeline renders it as the envelope. Only the
// catalog's status, type and code, plus Message and Param, ever reach the client: cause is logged
// with the request id and never rendered.
type Error struct {
	Status  int
	Type    ErrType
	Code    Code
	Message string      // from the catalog unless a call site gave something more exact
	Param   string      // "" renders as null
	Header  http.Header // Allow, Retry-After, WWW-Authenticate, …
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.cause)
	}
	return string(e.Code) + ": " + e.Message
}

// Unwrap returns the cause, so errors.Is works through an *Error.
func (e *Error) Unwrap() error { return e.cause }

// Cause returns the logged cause, or nil.
func (e *Error) Cause() error { return e.cause }

// Opt refines an Error.
type Opt func(*Error)

// Param names the request field the error is about (a JSON member, a query parameter or a header).
func Param(p string) Opt { return func(e *Error) { e.Param = p } }

// Msg replaces the catalog message with a more exact one. It must still be one or more calm
// sentences, sentence case, ending in a period, with a next step.
func Msg(m string) Opt { return func(e *Error) { e.Message = m } }

// Msgf is Msg with formatting.
func Msgf(format string, args ...any) Opt {
	return func(e *Error) { e.Message = fmt.Sprintf(format, args...) }
}

// WithHeader adds a response header (Allow, Retry-After, …).
func WithHeader(k, v string) Opt {
	return func(e *Error) {
		if e.Header == nil {
			e.Header = http.Header{}
		}
		e.Header.Add(k, v)
	}
}

// Cause records the underlying error for the log. It is never rendered.
func Cause(err error) Opt { return func(e *Error) { e.cause = err } }

// E builds the error for code c. An unknown code is a programming error: it panics in tests and
// renders internal_error otherwise (the catalog test lists every code in use).
func E(c Code, opts ...Opt) *Error {
	ce, ok := byCode[c]
	if !ok {
		ce = byCode[CodeInternalError]
		opts = append([]Opt{Cause(fmt.Errorf("httpx: unknown error code %q", c))}, opts...)
	}
	e := &Error{Status: ce.Status, Type: ce.Type, Code: ce.Code, Message: ce.Message}
	for _, o := range opts {
		o(e)
	}
	return e
}

// AsError returns the *Error in err's chain, or nil.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// --- exact messages the pipeline itself fills in ---

// errEndpointNotFound is the 404 for a path no route matches.
func errEndpointNotFound(method, path, suggestion string) *Error {
	msg := fmt.Sprintf("There's no endpoint at %s %s.", method, clip(path, 120))
	if suggestion != "" {
		msg += " Did you mean " + suggestion + "?"
	} else {
		msg += " Check the path in the API reference."
	}
	return E(CodeNotFound, Msg(msg))
}

// NotFound is the 404 for an object id that doesn't exist (or belongs to another org).
func NotFound(object, id string) *Error {
	return E(CodeNotFound, Msgf("There's no %s with id %s.", object, clip(id, 64)))
}

func errMethodNotAllowed(method, path string, allowed []string) *Error {
	list := strings.Join(allowed, ", ")
	return E(CodeMethodNotAllowed, Msgf("%s isn't supported on %s. Use %s.", method, clip(path, 120), orList(allowed)),
		WithHeader("Allow", list))
}

func errTooLarge(limit int64) *Error {
	return E(CodeRequestTooLarge, Msgf("The request body is larger than %s. Send a smaller body.", byteSize(limit)))
}

func errTimeout(seconds float64) *Error {
	return E(CodeTimeout, Msgf("The request timed out after %s s. It may have partly run, so fetch the object to see its state before you send it again.",
		strconv.FormatFloat(seconds, 'f', -1, 64)))
}

// ErrRateLimited is 429 rate_limited with Retry-After.
func ErrRateLimited(retryAfterSeconds int) *Error {
	n := max(retryAfterSeconds, 1)
	return E(CodeRateLimited, Msgf("Too many requests. Retry after %d s.", n), WithHeader("Retry-After", strconv.Itoa(n)))
}

// ErrValidation is 400 validation_failed about one field.
func ErrValidation(param, message string) *Error {
	return E(CodeValidationFailed, Param(param), Msg(message))
}

// ErrInsufficientScope names the scope the endpoint needs.
func ErrInsufficientScope(scope string) *Error {
	return E(CodeInsufficientScope, Msgf("This key doesn't have the scope this endpoint needs. Create a key with the %s scope.", scope))
}

// ErrInsufficientRole is 403 insufficient_role: "Your role in Acme is Viewer, and inviting people
// needs Admin. Ask an owner to change your role."
func ErrInsufficientRole(org string, have, need Role, what string) *Error {
	if org == "" {
		org = "this org"
	}
	if what == "" {
		what = "this request"
	}
	return E(CodeInsufficientRole, Msgf("Your role in %s is %s, and %s needs %s. Ask an owner to change your role.",
		clip(org, 100), have.Title(), what, need.Title()))
}

func orList(xs []string) string {
	switch len(xs) {
	case 0:
		return "another method"
	case 1:
		return xs[0]
	default:
		return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
	}
}

func byteSize(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// clip shortens s for a message and drops anything that isn't printable ASCII, so a request path
// can't inject control characters or long payloads into the copy.
func clip(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < n; i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e {
			b.WriteByte('?')
			continue
		}
		b.WriteByte(c)
	}
	if len(s) > n {
		b.WriteString("…")
	}
	return b.String()
}
