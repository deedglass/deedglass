// Package fake is the dev and test connector: four tools with real JSON Schemas and configurable
// latency, cost and outcome, so the Gate's every path runs without a real system (S13 §3.9). It
// registers only when DEEDGLASS_ENV=dev or in tests; S16 reserves the name "fake" outside dev.
//
//	fake.echo   read    returns {"echo": args}
//	fake.write  write   returns {"rows": n, "table": t}; costs 1 minor unit (usd) per row
//	fake.fail   write   returns a tool error, so the action is failed (certainly not applied)
//	fake.slow   write   sleeps latency_ms (default 60 s) to exercise timeouts (unknown outcome)
//
// Every tool takes an optional "_fake" object: latency_ms, cost, currency, code, message and
// panic (true makes Execute panic, which the Gate recovers and records as an unknown outcome).
package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

// Name is the connector slug and type.
const Name = "fake"

// DefaultSlow is fake.slow's latency when the call sets none.
const DefaultSlow = 60 * time.Second

// Connector is the fake connector. Its counters let tests check that a tool ran exactly once.
type Connector struct {
	calls    atomic.Int64
	mu       sync.Mutex
	byAction map[string]int
}

// New returns the fake connector.
func New() *Connector { return &Connector{byAction: map[string]int{}} }

var _ connector.Connector = (*Connector)(nil)

// Name implements connector.Connector.
func (c *Connector) Name() string { return Name }

// Type implements connector.Connector.
func (c *Connector) Type() string { return Name }

const fakeProp = `"_fake": {"type": "object", "properties": {
  "latency_ms": {"type": "integer", "minimum": 0, "maximum": 600000},
  "cost": {"type": "integer", "minimum": 0, "maximum": 9007199254740991},
  "currency": {"type": "string", "maxLength": 3},
  "code": {"type": "string", "maxLength": 64},
  "message": {"type": "string", "maxLength": 1024},
  "panic": {"type": "boolean"}}, "additionalProperties": false}`

var tools = []connector.ToolSpec{
	{Name: "echo", Description: "Returns its arguments.", SideEffect: connector.SideEffectRead, Idempotent: true,
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
  "message": {"type": "string", "maxLength": 65536},
  "n": {"type": "integer"},
  "data": {"type": "object"},
  ` + fakeProp + `}, "additionalProperties": false}`)},
	{Name: "write", Description: "Pretends to write rows to a table; costs 1 minor unit per row.", SideEffect: connector.SideEffectWrite,
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
  "rows": {"type": "integer", "minimum": 0, "maximum": 1000000000},
  "table": {"type": "string", "maxLength": 63},
  ` + fakeProp + `}, "additionalProperties": false}`)},
	{Name: "fail", Description: "Always fails with a tool error, before anything is applied.", SideEffect: connector.SideEffectWrite,
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
  "message": {"type": "string", "maxLength": 1024},
  ` + fakeProp + `}, "additionalProperties": false}`)},
	{Name: "slow", Description: "Waits latency_ms (60 s by default), to exercise timeouts.", SideEffect: connector.SideEffectWrite,
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
  "message": {"type": "string", "maxLength": 1024},
  ` + fakeProp + `}, "additionalProperties": false}`)},
}

// Tools implements connector.Connector. The slice is a copy.
func (c *Connector) Tools() []connector.ToolSpec { return append([]connector.ToolSpec(nil), tools...) }

// Calls is the number of Execute calls so far.
func (c *Connector) Calls() int64 { return c.calls.Load() }

// CallsFor is the number of Execute calls for one action.
func (c *Connector) CallsFor(action string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byAction[action]
}

// opts is the "_fake" object.
type opts struct {
	LatencyMS *int64  `json:"latency_ms"`
	Cost      *int64  `json:"cost"`
	Currency  string  `json:"currency"`
	Code      string  `json:"code"`
	Message   *string `json:"message"`
	Panic     bool    `json:"panic"`
}

// Execute implements connector.Connector.
func (c *Connector) Execute(ctx context.Context, call connector.Call) (connector.Result, error) {
	c.calls.Add(1)
	c.mu.Lock()
	c.byAction[call.ActionID.String()]++
	c.mu.Unlock()

	if err := connector.ValidateArgs(call.Tool.InputSchema, call.Args); err != nil {
		return connector.Result{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(call.Args))
	dec.UseNumber()
	var args map[string]any
	if err := dec.Decode(&args); err != nil {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "validation_failed",
			AgentMessage: "args must be a JSON object.", Err: err}
	}
	var o opts
	if raw, ok := args["_fake"]; ok {
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &o); err != nil {
			return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "validation_failed",
				AgentMessage: "args._fake must be an object of latency_ms, cost, currency, code, message and panic.", Err: err}
		}
		delete(args, "_fake")
	}
	if o.Panic {
		panic("fake: the call asked for a panic")
	}
	latency := time.Duration(0)
	if call.Tool.Name == "slow" {
		latency = DefaultSlow
	}
	if o.LatencyMS != nil {
		latency = time.Duration(*o.LatencyMS) * time.Millisecond
	}
	if latency > 0 {
		t := time.NewTimer(latency)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			kind, code := connector.KindTimeout, "timeout"
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				kind, code = connector.KindUpstream, "upstream_error"
			}
			return connector.Result{}, &connector.Error{Kind: kind, Code: code, Err: ctx.Err(),
				AgentMessage: "The tool did not answer in time. It may have run."}
		}
	}
	cur := "usd"
	if o.Currency != "" {
		cur = o.Currency
	}
	var out any
	var cost int64
	switch call.Tool.Name {
	case "echo":
		out = map[string]any{"echo": args}
	case "write":
		rows, table := int64(1), "t"
		if n, ok := args["rows"].(json.Number); ok {
			rows, _ = n.Int64()
		}
		if s, ok := args["table"].(string); ok {
			table = s
		}
		out, cost = map[string]any{"rows": rows, "table": table}, rows
	case "fail":
		code, msg := "tool_error", "The fake tool failed on purpose. Nothing was written."
		if o.Code != "" {
			code = o.Code
		}
		if o.Message != nil {
			msg = *o.Message
		} else if m, ok := args["message"].(string); ok && m != "" {
			msg = m
		}
		return connector.Result{}, &connector.Error{Kind: connector.KindToolError, Code: code, AgentMessage: msg}
	case "slow":
		out = map[string]any{"slept_ms": latency.Milliseconds()}
	default:
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown",
			AgentMessage: "The fake connector has no tool named " + call.Tool.Name + "."}
	}
	if o.Cost != nil {
		cost = *o.Cost
	}
	b, err := json.Marshal(out)
	if err != nil {
		return connector.Result{}, err
	}
	return connector.Result{Output: b, Cost: &connector.Money{Amount: cost, Currency: cur}, Latency: latency}, nil
}
