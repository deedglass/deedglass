package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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

// The contract's 24 codes, then S03's 9 additions, then S07's 6, then S11's 1, then S12's 1, then
// S16's 7.
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

	CodePolicyUnavailable Code = "policy_unavailable"

	CodePolicyInvalid Code = "policy_invalid"

	// S16: connectors, the vault and egress. Most appear as an action's execution.error_code; the
	// connector routes answer with destination_blocked and vault_unavailable directly.
	CodeCredentialsMissing  Code = "credentials_missing"
	CodeDestinationBlocked  Code = "destination_blocked"
	CodeVaultUnavailable    Code = "vault_unavailable"
	CodeUpstreamAuthFailed  Code = "upstream_auth_failed"
	CodeUpstreamNotFound    Code = "upstream_not_found"
	CodeUpstreamRateLimited Code = "upstream_rate_limited"
	CodeResponseTooLarge    Code = "response_too_large"

	// S17: the Postgres connector. They appear as an action's execution.error_code.
	CodeStatementRefused   Code = "statement_refused"
	CodeRowLimitExceeded   Code = "row_limit_exceeded"
	CodeDatabaseRoleUnsafe Code = "database_role_unsafe"
	CodeDatabaseNotReady   Code = "database_not_ready"

	// S20: the decoder routes.
	CodeWitnessNotConfigured Code = "witness_not_configured"

	// S22: approvals and approval links.
	CodeApprovalDecided        Code = "approval_decided"
	CodeAlreadyCountersigned   Code = "already_countersigned"
	CodeApproverNotEligible    Code = "approver_not_eligible"
	CodeSelfApproval           Code = "self_approval"
	CodeApprovalNeedsPerson    Code = "approval_needs_person"
	CodeContextChanged         Code = "context_changed"
	CodeApprovalUnsatisfiable  Code = "approval_unsatisfiable"
	CodeApprovalLinkInvalid    Code = "approval_link_invalid"
	CodeApprovalLinkReviewOnly Code = "approval_link_review_only"
	// S29: a Slack account that maps to no active member of the org.
	CodeApproverUnmapped Code = "approver_unmapped"
	// S30: Stripe sandbox rails and Stripe's own approvals.
	CodeStripeLiveKeyRefused    Code = "stripe_live_key_refused"
	CodeStripeLivemodeDetected  Code = "stripe_livemode_detected"
	CodeUpstreamApprovalPending Code = "upstream_approval_pending"

	// S40: scheduled execution after approval, and the ops build's outbound-mail connector.
	CodeScheduleNeedsApproval Code = "schedule_needs_approval"
	CodeActionExecuting       Code = "action_executing"
	CodeFromDenied            Code = "from_denied"

	// HC-P02: the FHIR connector. It appears as an action's execution.error_code.
	CodeFHIRVersionConflict Code = "fhir_version_conflict"

	// S23: undo.
	CodeUndoUnavailable Code = "undo_unavailable"
	CodeUndoPlanStale   Code = "undo_plan_stale"
	CodeAlreadyUndone   Code = "already_undone"
	CodeUndoConflict    Code = "undo_conflict"
	// S27: Insights replay limits.
	CodeReplayTooLarge      Code = "replay_too_large"
	CodeReplayWindowInvalid Code = "replay_window_invalid"

	// S28: suggestions.
	CodeSuggestionStale   Code = "suggestion_stale"
	CodeShadowSlotTaken   Code = "shadow_slot_taken"
	CodeSuggestionDecided Code = "suggestion_decided"

	// S50: observe mode, and the shared Pro refusal (S50–S52).
	CodeObserveNotEnabled Code = "observe_not_enabled"
	CodeProRequired       Code = "pro_required"
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
	{CodeInvalidAPIKey, 401, TypeAuthentication, "The API key isn't valid. Check that you copied all of it (it starts with dg_live_ or dg_test_), or create a new one in Console › Agents.", "", "contract"},
	{CodeAPIKeyRevoked, 401, TypeAuthentication, "The API key has been revoked. Create a new key in Console › Agents.", "", "contract"},
	{CodeAPIKeyExpired, 401, TypeAuthentication, "The API key expired after it was rolled. Use the new key, or create one in Console › Agents.",
		"The API key expired at {time} UTC after it was rolled. Use the new key, or create one in Console › Agents.", "contract"},
	{CodeAgentSuspended, 403, TypePermission, "This agent is suspended. An admin can resume it in Console › Agents.",
		`Agent "{name}" is suspended. An admin can resume it in Console › Agents.`, "contract"},
	{CodeAgentStopped, 403, TypePermission, "This agent is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.",
		`Agent "{name}" is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.`, "contract"},
	{CodeOrgStopped, 403, TypePermission, "All agents in this org are stopped. An owner can start them again in Console › Settings.",
		"All agents in {org} are stopped. An owner can start them again in Console › Settings.", "contract"},
	{CodeInsufficientScope, 403, TypePermission, "This key doesn't have the scope this endpoint needs. Create a key with that scope in Console › Agents.",
		"This key doesn't have the {scope} scope. Create a key with {scope} in Console › Agents.", "contract"},
	{CodePolicyDenied, 403, TypePolicy, "Blocked by policy. The action in Console › Actions names the rule and the reason.",
		`Blocked by policy "{rule}": {reason}.`, "contract"},
	{CodeApprovalRequired, 403, TypePolicy, "This action needs approval. It runs once someone approves it in Console › Approvals.", "", "contract"},
	{CodeApprovalExpired, 409, TypeInvalidRequest, "This approval expired before anyone decided. Nothing ran. Ask the agent to try again if it's still needed.",
		"This approval expired at {time}. Nothing ran. Ask the agent to try again if it's still needed.", "contract"},
	{CodeBudgetExceeded, 429, TypeRateLimit, "This agent has used its budget for the period. It resets when the next period starts, or an admin can raise it.",
		"This agent has used its budget for the period. It resets at {time}, or an admin can raise it.", "contract"},
	{CodeIntentRequired, 403, TypePermission, "This agent must sign its requests. Send a Deedglass-Intent header.", "", "contract"},
	{CodeIntentInvalid, 403, TypePermission, "The signed intent doesn't match this request. Sign the exact tool and arguments you send.", "", "contract"},
	{CodeIntentReplay, 403, TypePermission, "This signed intent was already used. Sign a new one with a fresh nonce.", "", "contract"},
	{CodeToolUnknown, 404, TypeInvalidRequest, "No connector provides this tool. Check the name, or add a connector in Console › Connectors.",
		`No tool named "{tool}". Tools look like "connector.tool", for example "fake.echo".`, "contract"},
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
	{CodeIdempotencyKeyInvalid, 400, TypeInvalidRequest, "Idempotency-Key must be 1 to 255 printable ASCII characters.", "", "S03"},
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

	{CodePolicyUnavailable, 403, TypePolicy, "Deedglass could not evaluate the policy for this action, so it did not run. An admin can check Console › Policies.", "", "S11"},

	{CodePolicyInvalid, 400, TypeInvalidRequest, "The policy wasn't accepted. The diagnostics list each problem with its line and a hint.",
		"The policy has {n} errors. The first is on line {line}: {message}.", "S12"},

	{CodeCredentialsMissing, 409, TypeInvalidRequest, "This connector has no credentials yet. Store them with deedglass connector credentials set, then retry.",
		`The connector "{name}" has no credentials yet. Run deedglass connector credentials set {name} --from-env {NAME}_TOKEN.`, "S16"},
	{CodeDestinationBlocked, 400, TypeInvalidRequest, "The connector's destination is blocked by Deedglass's network rules. An admin can see why in Console › Connectors.",
		"base_url resolves to {ip}, {a class}. Deedglass never connects to it. Use the service's public address.", "S16"},
	{CodeVaultUnavailable, 503, TypeAPI, "Deedglass couldn't open this connector's credentials. An admin has been alerted.",
		"{n} credentials use {kek_id}, which isn't in DEEDGLASS_VAULT_KEY. Add it back as the second key, restart, then run deedglass vault rotate.", "S16"},
	{CodeUpstreamAuthFailed, 502, TypeAPI, "The service refused the connector's credentials. An admin can replace them in Console › Connectors.", "", "S16"},
	{CodeUpstreamNotFound, 502, TypeAPI, "The service has nothing at that address. Check the arguments.", "", "S16"},
	{CodeUpstreamRateLimited, 429, TypeRateLimit, "The service is limiting requests. Wait, then send the call again.", "", "S16"},
	{CodeResponseTooLarge, 502, TypeAPI, "The service answered with more than the connector's max_response_bytes, so Deedglass kept none of it. The call ran.", "", "S16"},

	{CodeStatementRefused, 400, TypeInvalidRequest, "Refused by the Postgres connector. Deedglass runs one plain statement per action, and never transaction control, schema changes or schema deedglass.",
		"Refused by the Postgres connector: the statement has {n} commands. Deedglass runs one statement per action.", "S17"},
	{CodeRowLimitExceeded, 400, TypeInvalidRequest, "The statement changed more rows than this connector allows. Nothing was committed. Narrow the WHERE clause.",
		"The statement changed {n} rows, over this connector's limit of {max}. Nothing was committed. Narrow the WHERE clause.", "S17"},
	{CodeDatabaseRoleUnsafe, 503, TypeAPI, "The connector's database role is a superuser. Deedglass refuses to run agent SQL with it. Run deedglass connector postgres init to create deedglass_agent.", "", "S17"},
	{CodeDatabaseNotReady, 503, TypeAPI, "Deedglass isn't set up in this database yet. Run deedglass connector postgres init.",
		"Deedglass isn't set up in this database yet. Run deedglass connector postgres init --connector {name}.", "S17"},

	{CodeWitnessNotConfigured, 409, TypeInvalidRequest, "Checks aren't on for this connector, so it has no decoder. Turn on witness.enabled in its config first.",
		`Checks aren't on for the connector "{name}". Set witness.enabled to true in its config, then run deedglass connector postgres check {name}.`, "S20"},

	{CodeApprovalDecided, 409, TypeInvalidRequest, "This approval was already decided. Console › Approvals shows who decided and when.",
		"This approval was already decided: {outcome} by {name} at {time}.", "S22"},
	{CodeAlreadyCountersigned, 409, TypeInvalidRequest, "You already countersigned this. It needs another person.",
		"You already countersigned this. It needs {n} more {person}.", "S22"},
	{CodeApproverNotEligible, 403, TypePermission, "You're not an approver for this rule. Ask one of its approvers to countersign.",
		`Rule "{rule}" needs {approvers} to countersign. Ask one of them.`, "S22"},
	{CodeSelfApproval, 403, TypePermission, "This action runs on your behalf, so another approver has to countersign it. You can still deny it.", "", "S22"},
	{CodeApprovalNeedsPerson, 403, TypePermission, "Approvals need a person. Agents and API keys can't countersign. Use Console › Approvals or the emailed link.", "", "S22"},
	{CodeContextChanged, 409, TypeInvalidRequest, "What you reviewed is out of date. Reload the approval and review it again.", "", "S22"},
	{CodeApprovalUnsatisfiable, 403, TypePolicy, "No one in this org can approve this, so it did not run. Invite another admin, or allow self-countersign in Console › Settings › Approvals.",
		"No one in this org can approve this: the rule needs {n} people and only {m} {is} eligible. Nothing was run. Invite another admin, or allow self-countersign in Console › Settings › Approvals.", "S22"},
	{CodeApprovalLinkInvalid, 401, TypeAuthentication, "This approval link has expired or was already used. Open Console › Approvals.", "", "S22"},
	{CodeApprovalLinkReviewOnly, 403, TypePermission, "Your org decides approvals in the Console. Open Console › Approvals to countersign.", "", "S22"},
	{CodeApproverUnmapped, 403, TypePermission, "No person in this org matches that Slack account. Invite them in Console › Settings › Members.", "", "S29"},
	{CodeStripeLiveKeyRefused, 400, TypeInvalidRequest, "This is a live-mode Stripe key. Deedglass connects to Stripe sandboxes only. Use a restricted key that starts with rk_test_.", "", "S30"},
	{CodeStripeLivemodeDetected, 502, TypeAPI, "The stripe connector is disabled. An admin has been alerted.",
		"Stripe answered in live mode for request {req}. Deedglass disabled the {name} connector. Check the request in your Stripe dashboard, then replace the key with a sandbox key.", "S30"},
	{CodeUpstreamApprovalPending, 502, TypeAPI, "Stripe is holding this for its own approval. Deedglass can't see whether a Stripe reviewer approves it. Check Settings › Approvals in Stripe.",
		"Stripe is holding this refund for its own approval ({apreq}). Deedglass can't see whether a Stripe reviewer approves it. Check Settings › Approvals in Stripe.", "S30"},

	{CodeScheduleNeedsApproval, 403, TypePolicy, "A scheduled action must wait for an approval. This policy allows it now, so it would run early. Add require_approval to the rule, or drop the schedule.", "", "S40"},
	{CodeActionExecuting, 409, TypeInvalidRequest, "It's running now and can't be cancelled. Its deed will show what happened.", "", "S40"},
	{CodeFromDenied, 400, TypeInvalidRequest, "From on deedglass.com is never allowed for outbound mail. Use the sending-domain mailbox.", "", "S40"},

	{CodeFHIRVersionConflict, 409, TypeInvalidRequest, "The FHIR server refused the write because the resource changed since the version sent. Nothing was written. Read it again and send the new version id.",
		"The FHIR server refused the write to {resource} (HTTP {status}): the version sent ({version}) isn't the current one. Nothing was written. Read it again and send the new version id.", "HC-P02"},

	{CodeUndoUnavailable, 409, TypeInvalidRequest, "Undo isn't available for this action. The checks field lists each failing check with its fix.",
		"Undo isn't available for {act}: {n} check failed ({ids}). Run deedglass undo preview {act} for the fix.", "S23"},
	{CodeUndoPlanStale, 409, TypeInvalidRequest, "The rows changed since you previewed this undo. Preview it again and use the new plan.", "", "S23"},
	{CodeAlreadyUndone, 409, TypeInvalidRequest, "This action was already undone, or an undo of it is waiting. Look at the action's undo object.",
		"{act} was already undone by {undo} at {time}. / An undo of {act} is already waiting for approval: {undo}.", "S23"},
	{CodeUndoConflict, 409, TypeInvalidRequest, "A row changed after the undo's plan was made, so Postgres rolled the undo back. Nothing was changed. Preview the undo again.",
		"Row {table} {key} changed after the plan was made ({column} is now {value}). Nothing was changed. Preview the undo again.", "S23"},

	{CodeReplayTooLarge, 400, TypeInvalidRequest, "This replay covers more than 1,000,000 actions. Narrow the window with from and to.",
		"This replay covers {n} actions; the limit is 1,000,000. Narrow the window with from and to.", "S27"},
	{CodeReplayWindowInvalid, 400, TypeInvalidRequest, "The window must start before it ends, and it can be at most 90 days long. Send from before to.", "", "S27"},

	{CodeSuggestionStale, 409, TypeInvalidRequest, "The policy changed since this suggestion was written. Generate suggestions again: POST /v1/suggestions/generate.",
		"{policy} v{now} has been enforced since this suggestion was written for v{base}. Generate suggestions again: POST /v1/suggestions/generate.", "S28"},
	{CodeShadowSlotTaken, 409, TypeInvalidRequest, "Another version is already running in shadow. Promote or clear it first, or accept with replace_shadow: true to replace it.",
		"{policy} v{n} is already running in shadow. Promote or clear it first, or accept with replace_shadow: true to replace it.", "S28"},
	{CodeSuggestionDecided, 409, TypeInvalidRequest, "This suggestion was already decided. Look at its status, or generate suggestions again.",
		"This suggestion was already accepted on {date} by {who}. It created {policy} v{n}.", "S28"},

	{CodeObserveNotEnabled, 409, TypeInvalidRequest, "Observe mode is off for this connector. Turn it on in Console › Connectors, or set witness.observe.enabled: true in its config.",
		"Observe mode is off for {connector}. Turn it on in Console › Connectors › {connector}, or set witness.observe.enabled: true in its config.", "S50"},
	{CodeProRequired, 403, TypePermission, "This is part of Pro. Everything free still works; see deedglass.com/pricing.",
		"Undoing a whole session in one step is part of Pro. Per-action undo and the undo SQL stay free: deedglass observe show {session} --undo-sql.", "S50"},
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
	// Diagnostics is the additive envelope field of policy_invalid (S12): the compiler's
	// diagnostics, each with its line, code and hint.
	Diagnostics any
	// Action is the additive envelope field of POST /v1/actions refusals (S13): the recorded,
	// sealed action, rendered as "action".
	Action any
	// Checks is the additive envelope field of undo_unavailable (S23): every check, with its fix.
	Checks any
	cause  error
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

// WithAction attaches the recorded action to a refusal of POST /v1/actions (rendered as "action").
func WithAction(v any) Opt { return func(e *Error) { e.Action = v } }

// WithChecks attaches undo_unavailable's checks (rendered as "checks", S23).
func WithChecks(v any) Opt { return func(e *Error) { e.Checks = v } }

// WithDiagnostics attaches policy_invalid's diagnostics (rendered as "diagnostics").
func WithDiagnostics(v any) Opt { return func(e *Error) { e.Diagnostics = v } }

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

// ErrInsufficientScope names the scope the endpoint needs, with the challenge RFC 6750 §3.1
// describes.
func ErrInsufficientScope(scope string) *Error {
	return E(CodeInsufficientScope, Msgf("This key doesn't have the %s scope. Create a key with %s in Console › Agents.", scope, scope),
		WithHeader("WWW-Authenticate", `Bearer realm="deedglass", error="insufficient_scope", scope="`+scope+`"`))
}

// ErrAgentKeyForAdmin is insufficient_scope for an agent key on a control-plane route: no agent key
// changes any agent, including its own (S08 §3.4).
func ErrAgentKeyForAdmin() *Error {
	e := ErrInsufficientScope(ScopeAdmin)
	e.Message = "This is an agent key, and managing agents needs an admin key. Create one with deedglass org keys create on the server."
	return e
}

// ErrAPIKeyExpired names when the rolled key stopped working.
func ErrAPIKeyExpired(at time.Time) *Error {
	return E(CodeAPIKeyExpired, Msgf("The API key expired at %s UTC after it was rolled. Use the new key, or create one in Console › Agents.",
		at.UTC().Format("2006-01-02 15:04")))
}

// ErrAgentSuspended, ErrAgentStopped and ErrOrgStopped are §3.5 steps 6–8. They never include the
// reason or who stopped the agent: the caller is the agent, and the reason may be sensitive.
func ErrAgentSuspended(name string) *Error {
	return E(CodeAgentSuspended, Msgf("Agent %q is suspended. An admin can resume it in Console › Agents.", clip(name, 64)))
}

// ErrAgentStopped is 403 agent_stopped.
func ErrAgentStopped(name string) *Error {
	return E(CodeAgentStopped, Msgf("Agent %q is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.", clip(name, 64)))
}

// ErrOrgStopped is 403 org_stopped.
func ErrOrgStopped(org string) *Error {
	if org == "" {
		org = "this org"
	}
	return E(CodeOrgStopped, Msgf("All agents in %s are stopped. An owner can start them again in Console › Settings.", clip(org, 100)))
}

// ErrOrgStoppedAdminWrite is 403 org_stopped for an org admin key that tries to change something
// during Stop all. Admin keys keep read-only access then, so incident response can look around.
func ErrOrgStoppedAdminWrite(org string) *Error {
	if org == "" {
		org = "this org"
	}
	return E(CodeOrgStopped, Msgf("All agents in %s are stopped, so admin keys can only read until an owner starts them again in Console › Settings. To change agents now, use a Console session.", clip(org, 100)))
}

// ErrKeyRateLimited is 429 for too many failed API-key attempts from one address or on one prefix.
func ErrKeyRateLimited(retryAfterSeconds int) *Error {
	n := max(retryAfterSeconds, 1)
	return E(CodeRateLimited, Msgf("Too many requests with invalid API keys from this address. Try again in %d seconds.", n),
		WithHeader("Retry-After", strconv.Itoa(n)))
}

// ErrKeyOnSessionRoute is 403 insufficient_role for an API key on a route only a signed-in person may
// call: Start all (owners), and minting admin keys (admin keys can't mint admin keys).
func ErrKeyOnSessionRoute(p Principal, need Role, what string) *Error {
	if what == "" {
		what = "this request"
	}
	if p.Kind == KindAdminKey {
		return E(CodeInsufficientRole, Msgf("This is an admin key, which acts as Admin, and %s needs %s signed in to the Console. Sign in to the Console, or run the operator command on the server.",
			what, roleArticle(need)))
	}
	return E(CodeInsufficientRole, Msgf("API keys can't be used for %s; it needs %s signed in to the Console. Sign in to the Console to do this.", what, roleArticle(need)))
}

func roleArticle(r Role) string {
	switch r {
	case RoleOwner:
		return "an owner"
	case RoleAdmin:
		return "an admin"
	}
	return "a member"
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

// --- signed intents (S09 §5.2) ---

// IntentCopy is the message for each signed-intent refusal, keyed by reason (docs/INTENTS.md §6).
// {placeholders} are filled by ErrIntent; a placeholder with no value renders empty, so every
// template must read well with its values present. intent_required and intent_replay are their own
// codes; every other reason is intent_invalid.
var IntentCopy = map[string]string{
	"required":                 `Agent "{agent}" requires a signed intent for every call. Send a Deedglass-Intent header; see docs.deedglass.com/intents.`,
	"required_mcp":             `Agent "{agent}" requires a signed intent for every call, and MCP clients can't sign intents yet. Call it through the SDK or the REST API, or an admin can set its intent mode to optional in Console › Agents.`,
	"syntax":                   "The Deedglass-Intent header must be two base64url parts joined by a dot: the intent, then its signature.",
	"too_large":                "The signed intent is larger than 2,048 bytes. Keep only the fields listed at docs.deedglass.com/intents.",
	"strict":                   "The signed intent isn't strict JSON: {detail}. Use the SDK signer, or follow the profile at docs.deedglass.com/intents.",
	"not_canonical":            "The signed intent isn't canonical JSON (RFC 8785): keys must be sorted and there must be no whitespace. Use the SDK signer, or canonicalize before signing.",
	"schema_unknown":           `The signed intent has an unknown field "{field}". Put arguments in the call, and their digest in args_digest.`,
	"schema_missing":           `The signed intent is missing the field "{field}". Sign every required field listed at docs.deedglass.com/intents.`,
	"schema":                   "The signed intent's {field} isn't valid: it must be {want}. See the field table at docs.deedglass.com/intents.",
	"version":                  `The signed intent's version is "{got}", and Deedglass reads deedglass.intent/1. Sign with deedglass.intent/1.`,
	"org_mismatch":             "The signed intent names {intent_org}, but this API key belongs to {org}. Sign with the agent's own org id.",
	"agent_mismatch":           "The signed intent names {intent_agent}, but this API key belongs to {agent} ({agent_id}). Sign with the agent's own identity.",
	"unknown_kid":              "The signed intent uses key {kid}, which isn't registered for {agent}. Register it with deedglass agent intent add {agent}, or in Console › Agents › Security.",
	"bad_signature":            `The signature doesn't verify with key {kid}. Sign the bytes "deedglass/intent/v1\n" + intent_hash with the matching private key.`,
	"expired":                  "The signed intent expired at {time} UTC, {ago} ago. Sign a fresh intent for each call; an intent lasts at most 5 minutes.",
	"not_yet_valid":            "The signed intent was issued {ahead} in the future. Check the agent's clock; Deedglass allows 60 seconds of skew.",
	"ttl":                      "The signed intent is valid for {ms} ms, and it must be more than 0 and at most 300000 (5 minutes). Sign it again with exp after iat, at most 5 minutes later.",
	"tool_mismatch":            "The signed intent is for {intent_tool}, but this call is for {tool}. Sign one intent per call.",
	"args_mismatch":            "The signed intent's args_digest doesn't match the arguments sent. Sign the exact arguments you send, canonicalized with RFC 8785.",
	"big_int":                  "The arguments contain an integer larger than 9007199254740991, which JSON can't carry exactly. Send it as a string.",
	"args_not_canonicalizable": "The arguments can't be canonicalized with RFC 8785: {detail}. Send JSON with unique keys, at most 32 levels deep and at most 256 KiB.",
	"obo_mismatch":             "Deedglass-On-Behalf-Of says {header}, but the signed intent says {intent}. When you sign an intent, send the same value in both, or only in the intent.",
	"session_mismatch":         "Deedglass-Session says {header}, but the signed intent says {intent}. When you sign an intent, send the same value in both, or only in the intent.",
	"unsigned_obo":             "{name} was sent, but the signed intent doesn't include {field}. Sign it too, or leave the header out.",
	"unsigned_session":         "{name} was sent, but the signed intent doesn't include {field}. Sign it too, or leave the header out.",
	"replay":                   "This signed intent was already used at {time} UTC. Sign a new intent, with a fresh nonce, for every call.",
	"replay_unknown":           "This signed intent was already used. Sign a new intent, with a fresh nonce, for every call.",
}

// IntentParam is the error's param for each reason that has a fixed one; the schema reasons,
// unsigned_obo and the OBO mismatches name their field at the call site.
var IntentParam = map[string]string{
	"required": "Deedglass-Intent", "required_mcp": "Deedglass-Intent", "syntax": "Deedglass-Intent", "too_large": "Deedglass-Intent", "strict": "Deedglass-Intent",
	"not_canonical": "Deedglass-Intent", "version": "v", "org_mismatch": "org", "agent_mismatch": "agent", "unknown_kid": "kid",
	"bad_signature": "kid", "expired": "exp", "not_yet_valid": "iat", "ttl": "exp", "tool_mismatch": "tool",
	"args_mismatch": "args_digest", "big_int": "args", "args_not_canonicalizable": "args",
	"obo_mismatch": "Deedglass-On-Behalf-Of", "session_mismatch": "Deedglass-Session",
	"unsigned_obo": "Deedglass-On-Behalf-Of", "unsigned_session": "Deedglass-Session", "replay": "nonce", "replay_unknown": "nonce",
}

// ErrIntent builds the 403 for a signed-intent refusal. kv are placeholder name/value pairs; param
// overrides IntentParam when not empty. All three intent codes are 403 permission_error, never 401,
// so OAuth-capable MCP clients don't start an authorization flow (contract §8).
func ErrIntent(reason, param string, kv ...string) *Error {
	code := CodeIntentInvalid
	switch reason {
	case "required", "required_mcp":
		code = CodeIntentRequired
	case "replay", "replay_unknown":
		code = CodeIntentReplay
	}
	tpl, ok := IntentCopy[reason]
	if !ok {
		tpl = byCode[code].Message
	}
	pairs := make([]string, 0, len(kv))
	for i := 0; i+1 < len(kv); i += 2 {
		pairs = append(pairs, "{"+kv[i]+"}", clipMsg(kv[i+1], 120))
	}
	msg := strings.NewReplacer(pairs...).Replace(tpl)
	if param == "" {
		param = IntentParam[reason]
	}
	return E(code, Msg(msg), Param(param))
}

// clipMsg shortens a value placed in a message, keeping printable text (unlike clip, it keeps
// non-ASCII letters such as an org name's).
func clipMsg(s string, n int) string {
	var b strings.Builder
	count := 0
	for _, r := range s {
		if count >= n {
			b.WriteString("…")
			break
		}
		if r < 0x20 || r == 0x7f {
			r = '?'
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

// ErrOBOInvalid is the 422 for a malformed Deedglass-On-Behalf-Of or Deedglass-Session header, or
// one sent twice.
func ErrOBOInvalid(header string, twice bool) *Error {
	msg := header + " must be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -. Use an opaque user id, not a name."
	if header == "Deedglass-Session" {
		msg = header + " must be 1 to 256 characters: letters, digits and . _ ~ : @ / + = -. Use an opaque conversation or run id."
	}
	if twice {
		msg = "Send " + header + " once. It names one person or session per call."
	}
	e := ErrValidation(header, msg)
	e.Status = http.StatusUnprocessableEntity
	return e
}
