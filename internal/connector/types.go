package connector

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// SideEffect is what a tool does to the world, as its connector declares it. MCP tools are always
// unknown: their annotations are the server's claim, not Deedglass's (S13 §3.6).
type SideEffect string

const (
	SideEffectRead        SideEffect = "read"
	SideEffectWrite       SideEffect = "write"
	SideEffectDestructive SideEffect = "destructive"
	SideEffectUnknown     SideEffect = "unknown"
)

// Rank orders side effects: read < write < destructive. Declared side effects can be raised,
// never lowered (S16 §3.4).
func (s SideEffect) Rank() int {
	switch s {
	case SideEffectRead:
		return 1
	case SideEffectWrite:
		return 2
	case SideEffectDestructive:
		return 3
	}
	return 0
}

// ReadBackSpec is how the L1 witness reads a write back (S19 §3.5): a read tool of the same
// connector, with whole-value templated arguments ("{{args.id}}"), the fields to compare (expect),
// and how patient to be with an eventually consistent target. witness.CompileToolSpec checks it
// when the config is saved; witness.ReadBackSpec has the same JSON shape.
type ReadBackSpec struct {
	Tool        string            `json:"tool"`
	Args        map[string]string `json:"args,omitempty"`
	Expect      map[string]any    `json:"expect,omitempty"`      // path → template or literal JSON value (≤ 32)
	Consistency string            `json:"consistency,omitempty"` // strong (default) | eventual
	Attempts    int               `json:"attempts,omitempty"`    // 1..5; eventual defaults to 3
	IntervalMS  int               `json:"interval_ms,omitempty"` // 100..2000
	OnNotFound  string            `json:"on_not_found,omitempty"`
}

// ToolSpec describes one tool a connector serves. S16 extends S13's shape; it never replaces it.
type ToolSpec struct {
	Name          string          // unqualified: "echo", "get", "update_status"
	Description   string          // ≤ 1,024 characters
	InputSchema   json.RawMessage // the connector/schema subset
	OutputSchema  json.RawMessage // feeds S19's L0 check
	SideEffect    SideEffect      //
	Idempotent    bool            // safe to run twice with the same arguments
	Internal      bool            // never listed to agents (S23 postgres.undo)
	Postcondition string          // CEL, S19
	ReadBack      *ReadBackSpec   // S19
	// L1 is the sentence a native read-back checks ("GitHub shows the issue with the same title,
	// body and marker"): set, the connector reads the write back in code (NativeReadBacker, S29).
	L1 string
	// RequiresScope names an optional upstream scope the tool needs (S30: gmail.readonly for
	// gmail.get_thread); the catalog shows it.
	RequiresScope string
}

// Call is one execution the Gate asks a connector for. Args are the normalized arguments; the
// Deadline is the Gate's (gate.execute_timeout), also carried by the context.
type Call struct {
	ActionID       ids.ActionID
	OrgID          ids.OrgID
	AgentID        ids.AgentID
	Tool           ToolSpec
	Qualified      string          // "fake.echo"
	Args           json.RawMessage // a JSON object
	IdempotencyKey string          // "act_…": connectors pass it upstream where supported
	Deadline       time.Time
	IntentHash     string // "sha256:…" of the signed intent, when the call carried one (S17: the Postgres ledger row)
}

// Money is integer minor units plus a lowercase ISO 4217 currency.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Result is what a tool returned when it ran.
type Result struct {
	Output   json.RawMessage   // to the agent; ≤ MaxOutput; redacted by the registry
	Meta     map[string]any    // http_status, rows_affected…; the witness and the Console see it, never a deed
	Evidence map[string]string // xid8, marker_lsn… (S17); the witness only, never sent to agents
	Cost     *Money            // what the call cost, when the tool reports it
	Latency  time.Duration     // measured by the Gate when the connector leaves it zero
}

// MaxOutput is the most a connector's output may be after redaction.
const MaxOutput = 1 << 20

// Kind classifies a connector failure. The Gate maps it to execution.status (gate/execute.go):
// the kinds that certainly did not apply are failed; upstream, timeout and too_large may have
// (unknown).
type Kind string

const (
	KindInvalidRequest Kind = "invalid_request"
	KindConfig         Kind = "config"
	KindUpstream       Kind = "upstream"
	KindTimeout        Kind = "timeout"
	KindToolError      Kind = "tool_error"
	// KindCancelled: the caller went away (or the agent was stopped) before the call was sent, so
	// nothing reached the upstream (S15).
	KindCancelled Kind = "cancelled"

	// S16.
	KindAuth               Kind = "auth"                // the upstream refused the credential (401/403)
	KindNotFound           Kind = "not_found"           // 404
	KindRateLimited        Kind = "rate_limited"        // 429, after the retries the call allows
	KindDestinationBlocked Kind = "destination_blocked" // egress refused the destination: nothing was sent
	KindCredentialsMissing Kind = "credentials_missing" // the connector has no credentials: nothing was sent
	KindTooLarge           Kind = "too_large"           // the response passed the cap: the call ran
	KindVault              Kind = "vault"               // the credential couldn't be opened: nothing was sent

	// S30: the service holds the call for its own approval (Stripe's approval_required for an
	// agent-tagged key). Nothing happened yet, but the service completes it by itself if its
	// reviewer approves, so the Gate records it as unknown, never failed.
	KindUpstreamPending Kind = "upstream_pending"
)

// Error codes connectors use (contract §8; each has a doc page).
const (
	CodeValidationFailed     = "validation_failed"
	CodeUpstreamError        = "upstream_error"
	CodeTimeout              = "timeout"
	CodeUpstreamAuthFailed   = "upstream_auth_failed"
	CodeUpstreamNotFound     = "upstream_not_found"
	CodeUpstreamRateLimited  = "upstream_rate_limited"
	CodeDestinationBlocked   = "destination_blocked"
	CodeCredentialsMissing   = "credentials_missing"
	CodeResponseTooLarge     = "response_too_large"
	CodeConnectorUnavailable = "connector_unavailable"
	CodeVaultUnavailable     = "vault_unavailable"

	// S30.
	CodeStripeLiveKeyRefused    = "stripe_live_key_refused"
	CodeStripeLivemodeDetected  = "stripe_livemode_detected"
	CodeUpstreamApprovalPending = "upstream_approval_pending"
)

// Error is a typed connector failure. AgentMessage is safe to show the agent and already
// redacted; AdminDetail is for the server log and the Console (never the agent); Err is the cause
// for the server log and is never shown or stored.
type Error struct {
	Kind         Kind
	Code         string // copied to execution.error_code: ^[a-z][a-z0-9_]{0,63}$
	AgentMessage string
	AdminDetail  string
	Retryable    bool
	RetryAfter   time.Duration
	HTTPStatus   int
	Err          error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("connector %s (%s): %v", e.Kind, e.Code, e.Err)
	}
	if e.AdminDetail != "" {
		return fmt.Sprintf("connector %s (%s): %s", e.Kind, e.Code, e.AdminDetail)
	}
	return fmt.Sprintf("connector %s (%s)", e.Kind, e.Code)
}

func (e *Error) Unwrap() error { return e.Err }

// AsError returns the *Error in err's chain, or nil.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return nil
}

// Errors of Resolve.
var (
	// ErrToolUnknown: no connector serves this tool (or the name isn't a tool name).
	ErrToolUnknown = errors.New("connector: no tool by that name")
	// ErrConnectorUnavailable: the connector exists but can't run now (disabled).
	ErrConnectorUnavailable = errors.New("connector: unavailable")
)
