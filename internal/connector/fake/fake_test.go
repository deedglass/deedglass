package fake

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
)

func call(t *testing.T, c *Connector, tool, args string) (connector.Result, error) {
	t.Helper()
	ex, spec, typ, err := connector.NewRegistry(c).Resolve(context.Background(), ids.OrgID{}, "fake."+tool)
	if err != nil {
		t.Fatalf("resolve fake.%s: %v", tool, err)
	}
	if typ != "fake" {
		t.Fatalf("type %q", typ)
	}
	return ex.Execute(context.Background(), connector.Call{ActionID: ids.New[ids.ActionKind](), Tool: spec, Qualified: "fake." + tool, Args: json.RawMessage(args)})
}

func TestTools(t *testing.T) {
	c := New()
	cases := []struct {
		tool, args, out string
		cost            int64
		code            string // a failure's code
		kind            connector.Kind
	}{
		{tool: "echo", args: `{"message":"hello"}`, out: `{"echo":{"message":"hello"}}`},
		{tool: "echo", args: `{"message":"hi","_fake":{"latency_ms":1,"cost":7,"currency":"eur"}}`, out: `{"echo":{"message":"hi"}}`, cost: 7},
		{tool: "echo", args: `{}`, out: `{"echo":{}}`},
		{tool: "write", args: `{"rows":301,"table":"orders"}`, out: `{"rows":301,"table":"orders"}`, cost: 301},
		{tool: "write", args: `{}`, out: `{"rows":1,"table":"t"}`, cost: 1},
		{tool: "fail", args: `{}`, code: "tool_error", kind: connector.KindToolError},
		{tool: "fail", args: `{"_fake":{"code":"card_declined","message":"No."}}`, code: "card_declined", kind: connector.KindToolError},
		{tool: "fail", args: `{"message":"Disk full."}`, code: "tool_error", kind: connector.KindToolError},
		{tool: "slow", args: `{"_fake":{"latency_ms":2}}`, out: `{"slept_ms":2}`},
		{tool: "echo", args: `{"nope":1}`, code: "validation_failed", kind: connector.KindInvalidRequest},
		{tool: "write", args: `{"rows":-1}`, code: "validation_failed", kind: connector.KindInvalidRequest},
		{tool: "write", args: `{"rows":1.5}`, code: "validation_failed", kind: connector.KindInvalidRequest},
		{tool: "write", args: `{"table":5}`, code: "validation_failed", kind: connector.KindInvalidRequest},
		{tool: "echo", args: `[]`, code: "validation_failed", kind: connector.KindInvalidRequest},
		{tool: "echo", args: `{"_fake":{"panic":"yes"}}`, code: "validation_failed", kind: connector.KindInvalidRequest},
		{tool: "echo", args: `{"_fake":{"other":1}}`, code: "validation_failed", kind: connector.KindInvalidRequest},
	}
	for _, tc := range cases {
		r, err := call(t, c, tc.tool, tc.args)
		if tc.code != "" {
			ce := connector.AsError(err)
			if ce == nil || ce.Code != tc.code || ce.Kind != tc.kind || ce.AgentMessage == "" {
				t.Errorf("fake.%s %s: err %v, want %s/%s", tc.tool, tc.args, err, tc.kind, tc.code)
			}
			continue
		}
		if err != nil {
			t.Errorf("fake.%s %s: %v", tc.tool, tc.args, err)
			continue
		}
		if string(r.Output) != tc.out {
			t.Errorf("fake.%s %s: output %s, want %s", tc.tool, tc.args, r.Output, tc.out)
		}
		if r.Cost == nil || r.Cost.Amount != tc.cost {
			t.Errorf("fake.%s %s: cost %+v, want %d", tc.tool, tc.args, r.Cost, tc.cost)
		}
	}
	if c.Calls() != int64(len(cases)) {
		t.Errorf("calls %d, want %d", c.Calls(), len(cases))
	}
}

func TestSlowRespectsDeadline(t *testing.T) {
	c := New()
	spec := tools[3]
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Execute(ctx, connector.Call{Tool: spec, Args: json.RawMessage(`{}`)})
	ce := connector.AsError(err)
	if ce == nil || ce.Kind != connector.KindTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want a timeout", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("fake.slow ignored the deadline")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, err = c.Execute(ctx2, connector.Call{Tool: spec, Args: json.RawMessage(`{}`)})
	if ce := connector.AsError(err); ce == nil || ce.Kind != connector.KindUpstream {
		t.Fatalf("cancelled: %v, want upstream", err)
	}
}

func TestPanicAndCounters(t *testing.T) {
	c := New()
	id := ids.New[ids.ActionKind]()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("no panic")
			}
		}()
		_, _ = c.Execute(context.Background(), connector.Call{ActionID: id, Tool: tools[0], Args: json.RawMessage(`{"_fake":{"panic":true}}`)})
	}()
	if c.CallsFor(id.String()) != 1 || c.CallsFor("act_other") != 0 {
		t.Fatalf("CallsFor %d", c.CallsFor(id.String()))
	}
	if _, err := c.Execute(context.Background(), connector.Call{Tool: connector.ToolSpec{Name: "nope"}, Args: json.RawMessage(`{}`)}); connector.AsError(err) == nil {
		t.Fatal("unknown tool ran")
	}
	if c.Name() != "fake" || c.Type() != "fake" || len(c.Tools()) != 4 {
		t.Fatal("identity")
	}
	for _, sp := range c.Tools() {
		if !json.Valid(sp.InputSchema) || !strings.Contains(string(sp.InputSchema), `"additionalProperties": false`) {
			t.Errorf("%s: schema", sp.Name)
		}
	}
}

func TestRegistry(t *testing.T) {
	r := connector.NewRegistry(New())
	for _, q := range []string{"fake", "fake.nope", "nope.echo", ""} {
		if _, _, _, err := r.Resolve(context.Background(), ids.OrgID{}, q); !errors.Is(err, connector.ErrToolUnknown) {
			t.Errorf("%q: %v", q, err)
		}
	}
	if got := r.Names(); len(got) != 1 || got[0] != "fake" {
		t.Fatalf("names %v", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registered")
		}
	}()
	r.Register(New())
}
