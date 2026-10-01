package gate

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
)

// Mode is how an action runs: the Gate executes it (gateway), or the edge does and reports back
// (proxy, the MCP stdio wrapper of S14).
type Mode string

const (
	ModeGateway Mode = "gateway"
	ModeProxy   Mode = "proxy"
)

// ExecStatus is the deed's execution.status (contract §6).
type ExecStatus string

const (
	ExecSucceeded ExecStatus = "succeeded"
	ExecFailed    ExecStatus = "failed"
	ExecUnknown   ExecStatus = "unknown"
)

// Transports the context may name. MCP transports can't sign intents in v1 (S09).
const (
	TransportREST     = "rest"
	TransportSDK      = "sdk"
	TransportMCPStdio = "mcp_stdio"
	TransportMCPHTTP  = "mcp_http"
)

// RequestContext is who sent the action, as the client says: never in any digest, ≤ 4 KiB.
type RequestContext struct {
	Client    *ClientInfo `json:"client,omitempty"`
	Transport string      `json:"transport,omitempty"`
}

// ClientInfo names the client software.
type ClientInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ActionRequest is one POST /v1/actions, after the HTTP layer parsed the body and headers.
type ActionRequest struct {
	Tool           string
	Args           json.RawMessage
	Mode           Mode
	ToolDigest     string          // optional "sha256:…", sent by MCP edges (S14)
	ConnectorType  string          // proxy only: the edge's declared type from S10's enum (default "mcp")
	Context        *RequestContext // {client:{name,version}, transport}
	Wait           time.Duration   // gateway: 0..25 s; the default is gate.wait_timeout
	WaitSet        bool            // Wait was sent (0 means "don't wait")
	IdempotencyKey string          // header Idempotency-Key
	OnBehalfOf     string          // header Deedglass-On-Behalf-Of
	Session        string          // header Deedglass-Session
	Intent         string          // header Deedglass-Intent
	RequestID      string          // req_…, for the event rows
	// CancelOn (gateway, S15's MCP gateway): cancel the tool call (never its recording) when this
	// context ends: the client disconnected, or a Stop cancelled it (identity.Guard.Watch). Nil:
	// the execution is detached and finishes on its own.
	CancelOn context.Context
}

// EdgeResult is the body of POST /v1/actions/{id}/result (proxy mode).
type EdgeResult struct {
	Status       ExecStatus      `json:"status"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   time.Time       `json:"finished_at"`
	Result       json.RawMessage `json:"result,omitempty"`        // optional, ≤ gate.max_result_bytes
	ResultDigest string          `json:"result_digest,omitempty"` // required when Result is omitted; must match when both are sent
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

// Execution is what the Gate learned when the tool ran (or didn't answer).
type Execution struct {
	Status     ExecStatus
	StartedAt  time.Time
	FinishedAt time.Time
	Result     connector.Result
	ErrorCode  string // contract §8 codes or the connector's; ^[a-z][a-z0-9_]{0,63}$
	ErrorMsg   string // safe for the agent, already redacted, ≤ 1 KiB
	Reason     string // for unknown outcomes: timeout, outcome_unknown, edge_lost
	Sent       bool   // the call may have reached the upstream (false: Release, not RecordCost)
	// ResultDigest is the digest the edge reported without the result itself (proxy mode).
	ResultDigest string
}

// WitnessOutcome is a verdict about one execution (contract §6).
type WitnessOutcome struct {
	Level   string   // L0 | L1 | L2
	Verdict string   // VERIFIED | MISMATCH | UNVERIFIABLE
	Method  string   // "reported" (the tool said so), "edge_reported" (proxy)
	Reasons []string // short codes, never payloads
}

// Subject is what a seam needs to know about the action it serves.
type Subject struct {
	ID            ids.ActionID
	Org           ids.OrgID
	Agent         ids.AgentID
	Mode          Mode
	Tool          string
	ConnectorType string
}

// --- Seams. Later sessions plug in here without touching the pipeline. ---

// Executor runs a resolved tool (connector.Executor).
type Executor = connector.Executor

// Resolver finds the tool a qualified name names. S13 uses connector.Registry; S16's
// Registry.Resolve(ctx, org, qualified) replaces it behind the same signature.
type Resolver interface {
	Resolve(ctx context.Context, org ids.OrgID, qualified string) (Executor, connector.ToolSpec, string, error)
}

// Witnesser turns an execution into a verdict. S13's L0 witness answers inline; S19's
// witness.Engine replaces it and may defer (then the worker calls Deferred).
type Witnesser interface {
	Inline(ctx context.Context, s Subject, e Execution) (w WitnessOutcome, deferred bool)
	Deferred(ctx context.Context, s Subject) (WitnessOutcome, error)
}

// Approvals asks for a person's decision. S13's stub inserts a pending approval; S22 replaces it
// (notifications, four-eyes, links) and calls Pipeline.Resume when someone decides.
type Approvals interface {
	Request(ctx context.Context, tx pgx.Tx, s Subject, d policy.Decision) (ids.ApprovalID, time.Time, error)
}

// Policies is the active-policy cache (S12's *policy.Registry).
type Policies interface {
	Active(ctx context.Context, org string) (*policy.Active, error)
}

// Usage is S11's usage store inside the Gate's transactions (*policystore.Usage).
type Usage interface {
	SnapshotTx(ctx context.Context, tx pgx.Tx, in policy.SnapshotRequest) (policy.UsageSnapshot, error)
	ReserveTx(ctx context.Context, tx pgx.Tx, org string, rs []policy.Reservation) error
	ReleaseTx(ctx context.Context, tx pgx.Tx, org string, rs []policy.Reservation) error
	RecordCostTx(ctx context.Context, tx pgx.Tx, org string, targets []policy.CostTarget, cost policy.Money) error
}

// Action is the action object a Gate call answers with: ledger.Action's JSON (one shape for the
// Gate, the Ledger and the Console), plus what the HTTP layer needs to answer.
type Action struct {
	ledger.Action
	refusal  *httpx.Error // the error this action answers with (deny, budget, Stop, tool_unknown); nil otherwise
	replayed bool         // an Idempotency-Key replay
}

// Refusal is the error envelope the action answers with, with the action attached, or nil.
func (a *Action) Refusal() *httpx.Error { return a.refusal }

// Replayed reports whether the answer replays an earlier request with the same Idempotency-Key.
func (a *Action) Replayed() bool { return a.replayed }

// answer is (a, its refusal as an error, or nil): never a typed nil inside a non-nil error.
func (a *Action) answer() (*Action, error) {
	if a.refusal != nil {
		return a, a.refusal
	}
	return a, nil
}

// HTTPStatus is the status this action answers with (S13 §3.7).
func (a *Action) HTTPStatus() int {
	if a.refusal != nil {
		return a.refusal.Status
	}
	switch a.Status {
	case ledger.StatusPendingApproval, ledger.StatusProcessing:
		return 202
	}
	return 200
}

// principal is the identity.Principal the pipeline acts for.
type principal = identity.Principal
