package witness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Execution statuses the Witness sees (a failed execution is sealed without a witness, S13).
const (
	StatusSucceeded = "succeeded"
	StatusUnknown   = "unknown"
)

// Modes (contract §6).
const (
	ModeGateway = "gateway"
	ModeProxy   = "proxy"
)

// MaxOutputBytes is the largest output L0 checks against a schema (S13 already digests larger ones).
const MaxOutputBytes = 256 << 10

// ResultView is what the tool returned, as plain data.
type ResultView struct {
	Status      string            // succeeded | unknown
	Cause       string            // unknown only: S13's reason (timeout, outcome_unknown, edge_lost, client_cancelled)
	Output      json.RawMessage   // ≤ 256 KiB; nil when it wasn't kept
	OutputBytes int               // the output's size, also when Output is nil
	Meta        map[string]any    // http_status, rows_affected, …
	Evidence    map[string]string // xid8, marker_lsn, … (S17)
}

// ReadBackSpec is a write tool's read-back (S19 §3.5), as stored in the connector config.
type ReadBackSpec struct {
	Tool        string            `json:"tool"`                  // a read tool of the same connector
	Args        map[string]string `json:"args,omitempty"`        // whole-value templates {{args.…}} / {{result.body.…}}, or literals
	Expect      map[string]any    `json:"expect,omitempty"`      // path → template or literal JSON value
	Consistency string            `json:"consistency,omitempty"` // strong (default) | eventual
	Attempts    int               `json:"attempts,omitempty"`    // 1..5; eventual defaults to 3
	IntervalMS  int               `json:"interval_ms,omitempty"` // 100..2000; default 300
	OnNotFound  string            `json:"on_not_found,omitempty"`
}

// Read-back settings and their limits.
const (
	ConsistencyStrong   = "strong"
	ConsistencyEventual = "eventual"
	OnNotFoundGap       = "gap"
	OnNotFoundViolation = "violation"
	MaxExpectPaths      = 32
	MaxAttempts         = 5
	DefaultEventual     = 3
	DefaultIntervalMS   = 300
	MinIntervalMS       = 100
	MaxIntervalMS       = 2000
)

// attempts is how many read-backs the spec allows: 1 for strong.
func (r *ReadBackSpec) attempts() int {
	if r.Consistency != ConsistencyEventual {
		return 1
	}
	if r.Attempts <= 0 {
		return DefaultEventual
	}
	return min(r.Attempts, MaxAttempts)
}

func (r *ReadBackSpec) interval() time.Duration {
	ms := r.IntervalMS
	if ms <= 0 {
		ms = DefaultIntervalMS
	}
	return time.Duration(min(max(ms, MinIntervalMS), MaxIntervalMS)) * time.Millisecond
}

// ToolWitnessSpec is what the Witness knows about the tool that ran.
type ToolWitnessSpec struct {
	Connector     string          // the connector's name: "orders"
	Tool          string          // unqualified: "update_status"
	OutputSchema  json.RawMessage // the L0 schema check
	ReadBack      *ReadBackSpec   // L1
	Postcondition string          // CEL, evaluated over the read-back's observation
	ConfigHash    string          // the connector config's hash: the compiled-postcondition cache key
	// Row: the connector offers the row read-back for this write (postgres.execute); the connector,
	// not the Witness, says which row (TY-04). Nil when the tool has no row read-back.
	Row *RowSpec
	// Native: the connector reads this tool's writes back and compares them in code (S29: github,
	// slack); NativeL1 is the tool's sentence for what that check shows.
	Native   bool
	NativeL1 string
	// NativeUnknown: the connector also looks for the write after an unknown outcome (HC-P02: a FHIR
	// server's history since the call). Its findings can't turn the missing claim into a pass on
	// their own: a found write is compared, and anything else is a gap.
	NativeUnknown bool
}

// Subject is one action, as plain data the Gate fills: no storage types.
type Subject struct {
	ActionID, Org, Agent, Tool, ConnectorType, Mode, SideEffect string
	Args                                                        json.RawMessage
	Result                                                      ResultView
	Spec                                                        ToolWitnessSpec
	Required                                                    Level
}

// Observation is what a read-back saw.
type Observation struct {
	Body   any            // decoded JSON (UseNumber); Postgres: {"row_found", "xmin_matches", "columns", …}
	Status int            // HTTP status, or 0
	Meta   map[string]any // redacted: latency_ms, attempts, …
	At     time.Time
	Source string // "orders.get"
	Target string // native read-backs (S29): what was read, as people name it ("issue #42 on GitHub")
	// Findings are a native read-back's compared fields (S29). Nil for every other read-back.
	Findings       []Finding
	RequestDigest  string // sha256:… of the read-back's arguments
	ResponseDigest string // sha256:… of the response body; the body itself is never kept
	LatencyMS      int64
}

// Finding is one field a native read-back compared (S29): what the call implies and what the
// service shows. The connector digests long values.
type Finding struct {
	Field    string
	Expected any
	Observed any
	Match    bool
	Code     Code // a catalog code for a connector's own trap (fhir_conditional_matched); "" for a plain comparison
}

// ReadRequest is one read-back to run: the read tool and its resolved arguments. Postgres row
// read-backs have no read tool; the observer reads the row the statement named.
type ReadRequest struct {
	Tool   string          // qualified: "orders.get"; "" for the Postgres row read-back
	Args   json.RawMessage // the resolved arguments
	Target string          // how the docs and messages name what was read: "/v1/orders/42", "id = 42 in public.orders"
	Native bool            // S29: the connector's native read-back of the write itself
}

// Observer reads a write back from the target. The Gate implements it through the connector's
// ReadBacker; the Witness never does I/O itself.
type Observer interface {
	ReadBack(ctx context.Context, s Subject, req ReadRequest, attempt int) (Observation, error)
}

// ErrNotFound is the observer's answer when the read-back found nothing at the target.
var ErrNotFound = errors.New("witness: the read-back found nothing")

// ReadError is a read-back that failed: Code is the connector's error code (upstream_error,
// validation_failed, destination_blocked, …).
type ReadError struct {
	Code string
	Err  error
}

func (e *ReadError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("witness: read-back failed (%s): %v", e.Code, e.Err)
	}
	return "witness: read-back failed (" + e.Code + ")"
}

func (e *ReadError) Unwrap() error { return e.Err }

// SkipError is the observer saying the read-back doesn't apply (a Postgres statement whose WHERE
// isn't the primary key). It becomes the note readback_skipped.
type SkipError struct{ Why string }

func (e *SkipError) Error() string { return "witness: no read-back: " + e.Why }

// L1 kinds.
const (
	KindReadBack = "readback"
	KindRow      = "row"    // the row read-back a connector offers (RowSpec)
	KindNative   = "native" // S29
)

// Plan is what the Witness will check for one execution.
type Plan struct {
	L1   bool   // a read-back runs
	Kind string // KindReadBack | KindRow | KindNative
	Why  string // when L1 is false for a write that has no read-back: the readback_skipped note's text
}

// PlanFor decides the levels: L0 always; L1 for a gateway call that succeeded and has a read-back
// (or a row read-back its connector offers, like a simple single-row Postgres update); L2 when
// S21 says so. An unknown outcome is never read back: a read-back can't contradict a tool that
// made no claim, unless the connector looks for the write itself (NativeUnknown, HC-P02).
func PlanFor(s Subject) Plan {
	if s.Mode == ModeProxy {
		return Plan{}
	}
	if s.Result.Status == StatusUnknown && s.Spec.Native && s.Spec.NativeUnknown {
		return Plan{L1: true, Kind: KindNative}
	}
	if s.Result.Status != StatusSucceeded {
		return Plan{}
	}
	if s.Spec.Native {
		return Plan{L1: true, Kind: KindNative}
	}
	if s.Spec.ReadBack != nil {
		return Plan{L1: true, Kind: KindReadBack}
	}
	if s.Spec.Row != nil {
		if s.Spec.Row.Why != "" {
			return Plan{Why: s.Spec.Row.Why}
		}
		return Plan{L1: true, Kind: KindRow}
	}
	return Plan{}
}
