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

// ToolSpec describes one tool a connector serves. S16 extends it; it never replaces it.
type ToolSpec struct {
	Name        string          // unqualified: "echo"
	Description string          //
	InputSchema json.RawMessage // a JSON Schema subset (ValidateArgs; S16 brings the full validator)
	SideEffect  SideEffect      //
	Idempotent  bool            // safe to run twice with the same arguments
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
}

// Money is integer minor units plus a lowercase ISO 4217 currency.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Result is what a tool returned when it ran.
type Result struct {
	Output  json.RawMessage // stored up to gate.max_result_bytes; larger outputs are digested and truncated
	Meta    map[string]any  // http_status, rows_affected… (S16); never stored in a deed
	Cost    *Money          // what the call cost, when the tool reports it
	Latency time.Duration   // measured by the Gate when the connector leaves it zero
}

// Kind classifies a connector failure. The Gate maps it to execution.status (gate/execute.go):
// invalid_request, config and tool_error certainly did not apply (failed); upstream and timeout
// may have (unknown). S16 adds auth, not_found, rate_limited, destination_blocked,
// credentials_missing, too_large and vault.
type Kind string

const (
	KindInvalidRequest Kind = "invalid_request"
	KindConfig         Kind = "config"
	KindUpstream       Kind = "upstream"
	KindTimeout        Kind = "timeout"
	KindToolError      Kind = "tool_error"
)

// Error is a typed connector failure. AgentMessage is safe to show the agent and already redacted;
// Err is the cause for the server log and is never shown or stored.
type Error struct {
	Kind         Kind
	Code         string // copied to execution.error_code: ^[a-z][a-z0-9_]{0,63}$
	AgentMessage string
	Err          error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("connector %s (%s): %v", e.Kind, e.Code, e.Err)
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
	// ErrConnectorUnavailable: the connector exists but can't run now (S16: no credentials, disabled).
	ErrConnectorUnavailable = errors.New("connector: unavailable")
)
