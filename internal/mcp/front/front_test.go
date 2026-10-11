package front

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fake"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/policy"
)

// scripted is a Gate whose answers the test sets per tool.
type scripted struct {
	submit   func(req gate.ActionRequest) (*gate.Action, error)
	get      func(id string) (*gate.Action, error)
	approval func(id string) (*gate.Action, error)
	last     gate.ActionRequest
}

func (s *scripted) Submit(_ context.Context, _ identity.Principal, req gate.ActionRequest) (*gate.Action, error) {
	s.last = req
	return s.submit(req)
}
func (s *scripted) Get(_ context.Context, _ identity.Principal, id string, _ time.Duration) (*gate.Action, error) {
	if s.get == nil {
		return nil, httpx.E(httpx.CodeNotFound)
	}
	return s.get(id)
}
func (s *scripted) ByApproval(_ context.Context, _ identity.Principal, id string, _ time.Duration) (*gate.Action, error) {
	if s.approval == nil {
		return nil, httpx.E(httpx.CodeNotFound)
	}
	return s.approval(id)
}

type stash map[string][2]json.RawMessage

func (s stash) Take(id string) (json.RawMessage, json.RawMessage, bool) {
	v, ok := s[id]
	delete(s, id)
	return v[0], v[1], ok
}

type tools struct {
	n   int
	err error
}

func (t tools) List(context.Context, identity.Principal) ([]Tool, error) {
	if t.err != nil {
		return nil, t.err
	}
	out := []Tool{}
	for i := 0; i < t.n; i++ {
		name := fmt.Sprintf("t%03d", i)
		out = append(out, Tool{Name: name, Gate: "up." + name, Def: json.RawMessage(`{"name":"` + name + `"}`)})
	}
	out = append(out, Tool{Name: "secret", Gate: "up.secret", Def: json.RawMessage(`{"name":"secret"}`), Hidden: true})
	return out, nil
}
func (t tools) Lookup(_ context.Context, _ identity.Principal, name string) (Tool, bool, error) {
	if name == "boom" {
		return Tool{}, false, errors.New("lookup failed")
	}
	if name == "missing" {
		return Tool{}, false, nil
	}
	return Tool{Name: name, Gate: "up." + name, Digest: "sha256:" + strings.Repeat("a", 64)}, true, nil
}
func (tools) Label() string { return "up" }

func sp(s string) *string { return &s }

func act(id, status string) *gate.Action {
	a := &gate.Action{}
	a.Action = ledger.Action{ID: id, Status: status, State: "sealed", Tool: "up.x", Deed: sp("deed_" + id[4:])}
	return a
}

func executed(id, result string) *gate.Action {
	a := act(id, ledger.StatusExecuted)
	a.Execution = &ledger.ActionExecution{Status: sp("succeeded"), Result: json.RawMessage(result)}
	a.Witness = &ledger.ActionWitness{Level: "L0", Verdict: "VERIFIED"}
	return a
}

const id1 = "act_01k6w4a3d5f7h9k1n3q5s7v9x1"

func req(method, params string, modern bool) Request {
	p := "{" + params + "}"
	if modern {
		if params != "" {
			params += ","
		}
		p = `{` + params + `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"t","version":"1"}}}`
	}
	e, err := jsonrpc.Parse([]byte(`{"jsonrpc":"2.0","id":7,"method":"` + method + `","params":` + p + `}`))
	if err != nil {
		panic(err)
	}
	return Request{Env: e, Modern: modern, Transport: gate.TransportMCPHTTP, RequestID: "req_x"}
}

func result(t *testing.T, r Response) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(r.Env["result"], &m); err != nil {
		t.Fatalf("no result: %s", r.Env.Marshal())
	}
	return m
}

func textOf(t *testing.T, r Response) string {
	t.Helper()
	var c []struct{ Text string }
	_ = json.Unmarshal(result(t, r)["content"], &c)
	var b strings.Builder
	for _, x := range c {
		b.WriteString(x.Text)
	}
	return b.String()
}

func TestMethods(t *testing.T) {
	f := &Front{Version: "1.2.3", Tools: tools{n: 150}, Gate: &scripted{}}
	ctx := context.Background()
	// initialize: echo a served version, else 2025-11-25.
	for in, want := range map[string]string{"2025-06-18": "2025-06-18", "2025-03-26": "2025-03-26", "2024-11-05": "2025-11-25", "": "2025-11-25"} {
		r := f.Handle(ctx, req("initialize", `"protocolVersion":"`+in+`"`, false))
		if got := string(result(t, r)["protocolVersion"]); got != `"`+want+`"` {
			t.Errorf("initialize %q → %s", in, got)
		}
	}
	r := f.Handle(ctx, req("server/discover", "", true))
	m := result(t, r)
	if string(m["supportedVersions"]) != `["2026-07-28"]` || string(m["ttlMs"]) != "3600000" || !strings.Contains(string(m["_meta"]), `"version":"1.2.3"`) {
		t.Fatalf("%s", r.Env.Marshal())
	}
	if r := f.Handle(ctx, req("ping", "", true)); string(result(t, r)["resultType"]) != `"complete"` {
		t.Fatal("ping")
	}
	if r := f.Handle(ctx, req("ping", "", false)); result(t, r)["resultType"] != nil {
		t.Fatal("legacy ping has resultType")
	}
	// Notifications are 202 with nothing; a response is refused.
	n, _ := jsonrpc.Parse([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if r := f.Handle(ctx, Request{Env: n}); r.HTTPStatus != 202 || r.Env != nil {
		t.Fatal("notification")
	}
	resp, _ := jsonrpc.Parse([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	if r := f.Handle(ctx, Request{Env: resp}); r.HTTPStatus != 400 {
		t.Fatal("response accepted")
	}
	// Unknown method: 404 modern, 200 legacy; passthrough families need opting in.
	if r := f.Handle(ctx, req("resources/list", "", true)); r.HTTPStatus != 404 || !strings.Contains(string(r.Env["error"]), "-32601") {
		t.Fatalf("%d %s", r.HTTPStatus, r.Env.Marshal())
	}
	if r := f.Handle(ctx, req("prompts/list", "", false)); r.HTTPStatus != 200 || !strings.Contains(string(r.Env["error"]), "-32601") {
		t.Fatal("legacy unknown")
	}
}

func TestListPagination(t *testing.T) {
	f := &Front{Tools: tools{n: 150}, Gate: &scripted{}}
	ctx := context.Background()
	var names []string
	cursor := ""
	for page := 0; page < 5; page++ {
		params := ""
		if cursor != "" {
			params = `"cursor":"` + cursor + `"`
		}
		m := result(t, f.Handle(ctx, req("tools/list", params, true)))
		var ts []struct{ Name string }
		_ = json.Unmarshal(m["tools"], &ts)
		for _, x := range ts {
			names = append(names, x.Name)
		}
		if string(m["cacheScope"]) != `"private"` || string(m["ttlMs"]) != "30000" {
			t.Fatalf("hints %v", m)
		}
		_ = json.Unmarshal(m["nextCursor"], &cursor)
		if m["nextCursor"] == nil {
			break
		}
	}
	if len(names) != 151 || names[0] != "t000" || names[150] != "deedglass.check_approval" || strings.Contains(strings.Join(names, ","), "secret") {
		t.Fatalf("%d %v", len(names), names[len(names)-3:])
	}
	if r := f.Handle(ctx, req("tools/list", `"cursor":"nope!"`, true)); !strings.Contains(string(r.Env["error"]), "-32602") {
		t.Fatal("bad cursor")
	}
	down := &Front{Tools: tools{err: ErrUpstreamDown}, Gate: &scripted{}}
	if r := down.Handle(ctx, req("tools/list", "", true)); !strings.Contains(string(r.Env["error"]), `The \"up\" MCP server did not answer`) {
		t.Fatalf("%s", r.Env.Marshal())
	}
	other := &Front{Tools: tools{err: errors.New("db")}, Gate: &scripted{}}
	if r := other.Handle(ctx, req("tools/list", "", false)); !strings.Contains(string(r.Env["error"]), "could not handle") {
		t.Fatalf("%s", r.Env.Marshal())
	}
}

func TestCallOutcomes(t *testing.T) {
	ctx := context.Background()
	pending := act(id1, ledger.StatusPendingApproval)
	pending.Approval = &ledger.ActionApproval{ID: "apr_01k6w4a3d5f7h9k1n3q5s7v9x1", Status: "pending", ExpiresAt: ledger.Time(time.Date(2026, 10, 5, 10, 30, 0, 0, time.UTC))}
	pending.Decision = &ledger.ActionDecision{Outcome: "require_approval", Message: sp("A person approves this first.")}
	denied := act(id1, ledger.StatusDenied)
	denied.Decision = &ledger.ActionDecision{Outcome: "deny", Message: sp(`Blocked by policy "x": no.`), Source: sp("rule"), RuleID: sp("x")}
	unknown := act(id1, ledger.StatusExecuted)
	unknown.Execution = &ledger.ActionExecution{Status: sp("unknown"), ErrorCode: sp("timeout")}
	unknown.Witness = &ledger.ActionWitness{Level: "L0", Verdict: "UNVERIFIABLE"}
	down := act(id1, ledger.StatusFailed)
	down.Execution = &ledger.ActionExecution{Status: sp("failed"), ErrorCode: sp("connector_unavailable")}
	failedNoResult := act(id1, ledger.StatusFailed)
	failedNoResult.Execution = &ledger.ActionExecution{Status: sp("failed"), ErrorCode: sp("tool_error")}
	processing := act(id1, ledger.StatusProcessing)
	expired := act(id1, ledger.StatusExpired)
	expired.Approval = pending.Approval
	cancelled := act(id1, ledger.StatusCancelled)
	cases := []struct {
		name   string
		tool   string
		submit func(gate.ActionRequest) (*gate.Action, error)
		stash  stash
		check  func(t *testing.T, r Response)
	}{
		{"upstream result with _meta", "echo", func(gate.ActionRequest) (*gate.Action, error) {
			return executed(id1, `{"content":[{"type":"text","text":"Echo: hi"}],"_meta":{"x":"y"}}`), nil
		}, nil, func(t *testing.T, r Response) {
			m := result(t, r)
			if textOf(t, r) != "Echo: hi" || !strings.Contains(string(m["_meta"]), `"com.deedglass/action":"`+id1+`"`) || !strings.Contains(string(m["_meta"]), `"x":"y"`) ||
				string(m["resultType"]) != `"complete"` {
				t.Fatalf("%s", r.Env.Marshal())
			}
		}},
		{"a VERIFIED verdict: _meta only (S19)", "echo", func(gate.ActionRequest) (*gate.Action, error) { return executed(id1, `{"ok":true}`), nil }, nil,
			func(t *testing.T, r Response) {
				m := result(t, r)
				if !strings.Contains(string(m["_meta"]), `"com.deedglass/witness":{"level":"L0","verdict":"VERIFIED"}`) || textOf(t, r) != `{"ok":true}` {
					t.Fatalf("%s", r.Env.Marshal())
				}
			}},
		{"a MISMATCH: _meta and one line, isError false (S19)", "echo", func(gate.ActionRequest) (*gate.Action, error) {
			a := executed(id1, `{"content":[{"type":"text","text":"Updated."}]}`)
			sum := `orders.update_status reported success, but orders.get says status is "pending", not "shipped".`
			a.Witness = &ledger.ActionWitness{Level: "L1", Verdict: "MISMATCH", Summary: &sum}
			return a, nil
		}, nil, func(t *testing.T, r Response) {
			m := result(t, r)
			want := `Updated.Deedglass check · Checked · Doesn't match · orders.update_status reported success, but orders.get says status is "pending", not "shipped".`
			if !strings.Contains(string(m["_meta"]), `"com.deedglass/witness":{"level":"L1","verdict":"MISMATCH"}`) || textOf(t, r) != want ||
				string(m["isError"]) == "true" {
				t.Fatalf("%s", r.Env.Marshal())
			}
		}},
		{"a native connector's JSON", "echo", func(gate.ActionRequest) (*gate.Action, error) { return executed(id1, `{"echo":{"message":"hi"}}`), nil }, nil,
			func(t *testing.T, r Response) {
				m := result(t, r)
				if textOf(t, r) != `{"echo":{"message":"hi"}}` || string(m["structuredContent"]) != `{"echo":{"message":"hi"}}` || string(m["isError"]) != "false" {
					t.Fatalf("%s", r.Env.Marshal())
				}
			}},
		{"the tap wins over the stored result", "echo", func(gate.ActionRequest) (*gate.Action, error) { return executed(id1, `{"content":[]}`), nil },
			stash{id1: {json.RawMessage(`{"content":[{"type":"text","text":"from the tap"}]}`), nil}},
			func(t *testing.T, r Response) {
				if textOf(t, r) != "from the tap" {
					t.Fatalf("%s", r.Env.Marshal())
				}
			}},
		{"an upstream JSON-RPC error is relayed", "echo", func(gate.ActionRequest) (*gate.Action, error) {
			a := act(id1, ledger.StatusFailed)
			a.Execution = &ledger.ActionExecution{Status: sp("failed"), ErrorCode: sp("upstream_error")}
			return a, nil
		}, stash{id1: {nil, json.RawMessage(`{"code":-32602,"message":"bad args"}`)}}, func(t *testing.T, r Response) {
			if !strings.Contains(string(r.Env["error"]), "bad args") || string(r.Env.ID()) != "7" {
				t.Fatalf("%s", r.Env.Marshal())
			}
		}},
		{"denied", "echo", func(gate.ActionRequest) (*gate.Action, error) { return denied, httpx.E(httpx.CodePolicyDenied) }, nil, func(t *testing.T, r Response) {
			if !strings.HasPrefix(textOf(t, r), `Blocked by policy "x": no. Nothing was run.`) || string(result(t, r)["isError"]) != "true" {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"pending", "echo", func(gate.ActionRequest) (*gate.Action, error) { return pending, nil }, nil, func(t *testing.T, r Response) {
			if !strings.Contains(textOf(t, r), "Nothing has run yet. Approval apr_01k6w4a3d5f7h9k1n3q5s7v9x1 expires at 10:30 UTC") {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"timeout", "echo", func(gate.ActionRequest) (*gate.Action, error) { return unknown, nil }, nil, func(t *testing.T, r Response) {
			if !strings.Contains(textOf(t, r), "didn't answer up.x within 30 s") {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"upstream down", "echo", func(gate.ActionRequest) (*gate.Action, error) { return down, nil }, nil, func(t *testing.T, r Response) {
			if !strings.HasPrefix(textOf(t, r), `The "up" MCP server did not answer, so nothing was run.`) {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"failed without a result", "echo", func(gate.ActionRequest) (*gate.Action, error) { return failedNoResult, nil }, nil, func(t *testing.T, r Response) {
			if !strings.Contains(textOf(t, r), "failed before it applied anything") {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"still processing", "echo", func(gate.ActionRequest) (*gate.Action, error) { return processing, nil }, nil, func(t *testing.T, r Response) {
			if textOf(t, r) != "Still running. Deedglass will record the outcome ("+id1+")." {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"expired", "echo", func(gate.ActionRequest) (*gate.Action, error) { return expired, nil }, nil, func(t *testing.T, r Response) {
			if !strings.Contains(textOf(t, r), "expired before a person decided") {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"cancelled", "echo", func(gate.ActionRequest) (*gate.Action, error) { return cancelled, nil }, nil, func(t *testing.T, r Response) {
			if !strings.Contains(textOf(t, r), "was cancelled") {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"a validation refusal isn't recorded", "echo", func(gate.ActionRequest) (*gate.Action, error) {
			return nil, httpx.ErrValidation("args", "args must be a JSON object.")
		}, nil, func(t *testing.T, r Response) {
			if textOf(t, r) != "args must be a JSON object. Nothing was run." {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"connector unavailable before recording", "echo", func(gate.ActionRequest) (*gate.Action, error) {
			return nil, httpx.E(httpx.CodeConnectorUnavailable)
		}, nil, func(t *testing.T, r Response) {
			if !strings.Contains(textOf(t, r), `"up" MCP server did not answer`) {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"a server error", "echo", func(gate.ActionRequest) (*gate.Action, error) { return nil, errors.New("db down") }, nil, func(t *testing.T, r Response) {
			if textOf(t, r) != "Deedglass could not handle this request, so nothing was run. Try again in a moment." {
				t.Fatalf("%s", textOf(t, r))
			}
		}},
		{"an unknown tool", "missing", nil, nil, func(t *testing.T, r Response) {
			if !strings.Contains(string(r.Env["error"]), "-32602") {
				t.Fatalf("%s", r.Env.Marshal())
			}
		}},
		{"a deedglass. name from a client", "deedglass.evil", nil, nil, func(t *testing.T, r Response) {
			if !strings.Contains(string(r.Env["error"]), "-32602") {
				t.Fatalf("%s", r.Env.Marshal())
			}
		}},
		{"a lookup failure", "boom", nil, nil, func(t *testing.T, r Response) {
			if !strings.Contains(textOf(t, r), "could not handle") {
				t.Fatalf("%s", r.Env.Marshal())
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &scripted{submit: c.submit}
			f := &Front{Tools: tools{}, Gate: g, ExecuteTimeout: 30 * time.Second, CallWait: 50 * time.Millisecond}
			if c.stash != nil {
				f.Results = c.stash
			}
			c.check(t, f.Handle(context.Background(), req("tools/call", `"name":"`+c.tool+`","arguments":{"message":"hi"}`, true)))
			if c.submit != nil && (g.last.Mode != gate.ModeGateway || g.last.Tool != "up."+c.tool || g.last.Context.Transport != gate.TransportMCPHTTP ||
				g.last.Context.Client.Name != "t" || g.last.CancelOn == nil || g.last.ToolDigest == "") {
				t.Fatalf("request %+v", g.last)
			}
		})
	}
	// Missing name, null arguments.
	f := &Front{Tools: tools{}, Gate: &scripted{submit: func(r gate.ActionRequest) (*gate.Action, error) { return executed(id1, `{}`), nil }}}
	if r := f.Handle(ctx, req("tools/call", `"arguments":{}`, false)); !strings.Contains(string(r.Env["error"]), "-32602") {
		t.Fatal("missing name")
	}
	g := &scripted{submit: func(r gate.ActionRequest) (*gate.Action, error) { return executed(id1, `{}`), nil }}
	f.Gate = g
	f.Handle(ctx, req("tools/call", `"name":"echo","arguments":null`, false))
	if string(g.last.Args) != "{}" {
		t.Fatalf("args %s", g.last.Args)
	}
	// A processing answer is followed with Get until it settles.
	g2 := &scripted{submit: func(gate.ActionRequest) (*gate.Action, error) { return processing, nil },
		get: func(string) (*gate.Action, error) {
			return executed(id1, `{"content":[{"type":"text","text":"later"}]}`), nil
		}}
	f2 := &Front{Tools: tools{}, Gate: g2, CallWait: time.Second}
	if r := f2.Handle(ctx, req("tools/call", `"name":"echo"`, false)); textOf(t, r) != "later" || result(t, r)["resultType"] != nil {
		t.Fatalf("%s", r.Env.Marshal())
	}
}

func TestCheckApproval(t *testing.T) {
	ctx := context.Background()
	apr := "apr_01k6w4a3d5f7h9k1n3q5s7v9x1"
	pending := act(id1, ledger.StatusPendingApproval)
	pending.Approval = &ledger.ActionApproval{ID: apr, Status: "pending", ExpiresAt: ledger.Time(time.Date(2026, 10, 5, 10, 30, 0, 0, time.UTC))}
	ran := executed(id1, `{"content":[{"type":"text","text":"approved: x"}]}`)
	ran.Tool = "up.approve_me"
	deniedByPerson := act(id1, ledger.StatusDenied)
	deniedByPerson.Approval = &ledger.ActionApproval{ID: apr, Status: "denied"}
	cases := []struct {
		name string
		args string
		a    *gate.Action
		err  error
		want string
	}{
		{"bad id", `{"approval_id":"nope"}`, nil, nil, "approval_id must be an approval id"},
		{"not found", `{"approval_id":"` + apr + `"}`, nil, httpx.E(httpx.CodeNotFound), "No approval " + apr + " for this agent"},
		{"server error", `{"approval_id":"` + apr + `"}`, nil, errors.New("db"), "could not handle"},
		{"still pending", `{"approval_id":"` + apr + `"}`, pending, nil, "Still waiting for approval " + apr},
		{"approved and ran", `{"approval_id":"` + apr + `"}`, ran, nil, "Approved by a person. Ran up.approve_me.approved: x"},
		{"denied by a person", `{"approval_id":"` + apr + `"}`, deniedByPerson, nil, "A person denied this action. Nothing was run."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := &scripted{approval: func(string) (*gate.Action, error) { return c.a, c.err }}
			f := &Front{Tools: tools{}, Gate: g}
			r := f.Handle(ctx, req("tools/call", `"name":"deedglass.check_approval","arguments":`+c.args, false))
			if got := textOf(t, r); !strings.Contains(got, c.want) {
				t.Fatalf("%q, want %q", got, c.want)
			}
		})
	}
}

type pass struct {
	res json.RawMessage
	rpc *jsonrpc.ErrorObject
	err error
}

func (p pass) Forward(context.Context, string, json.RawMessage) (json.RawMessage, *jsonrpc.ErrorObject, error) {
	return p.res, p.rpc, p.err
}

func TestPassthrough(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		p    pass
		want string
	}{
		{pass{res: json.RawMessage(`{"resources":[]}`)}, `"result":{"resources":[]}`},
		{pass{rpc: &jsonrpc.ErrorObject{Code: -32002, Message: "no such resource"}}, `no such resource`},
		{pass{err: ErrUpstreamDown}, `did not answer`},
		{pass{err: errors.New("x")}, `could not handle`},
	} {
		f := &Front{Tools: tools{}, Gate: &scripted{}, Passthrough: c.p, PassMethods: map[string]bool{"resources": true}}
		if r := f.Handle(ctx, req("resources/list", "", false)); !strings.Contains(string(r.Env.Marshal()), c.want) {
			t.Fatalf("%s", r.Env.Marshal())
		}
	}
	// A family that isn't listed, and a method outside the passable set, stay -32601.
	f := &Front{Tools: tools{}, Gate: &scripted{}, Passthrough: pass{}, PassMethods: map[string]bool{"resources": true}}
	for _, m := range []string{"prompts/list", "resources/subscribe"} {
		if r := f.Handle(ctx, req(m, "", false)); !strings.Contains(string(r.Env["error"]), "-32601") {
			t.Fatalf("%s: %s", m, r.Env.Marshal())
		}
	}
}

// visAll hides tools whose name contains "write".
type visAll struct{}

func (visAll) Visibility(_ context.Context, _ identity.Principal, ts []policy.ToolFacts) (*gate.ToolVisibilityList, error) {
	out := &gate.ToolVisibilityList{PolicyVersion: sp("pv_x")}
	for _, f := range ts {
		v := gate.VisibilityVisible
		if strings.Contains(f.Name, "write") || strings.Contains(f.Name, "fail") || strings.Contains(f.Name, "slow") {
			v = gate.VisibilityHidden
		}
		out.Data = append(out.Data, gate.ToolVisibility{Name: f.Name, Visibility: v})
	}
	return out, nil
}

func TestNativeSource(t *testing.T) {
	ns := &NativeSource{Connectors: []connector.Connector{fake.New()}, Vis: visAll{}}
	nts, pv, err := ns.AllWithPolicy(context.Background(), identity.Principal{})
	if err != nil || pv == nil || *pv != "pv_x" || len(nts) != 4 || nts[0].Name != "fake.echo" || nts[0].Hidden || !nts[3].Hidden {
		t.Fatalf("%v %v %+v", err, pv, nts)
	}
	if nts[0].Title != "Echo" || nts[0].SideEffect != "read" || DefDigest(nts[0].Def) == "" {
		t.Fatalf("%+v", nts[0])
	}
	var def map[string]any
	_ = json.Unmarshal(nts[0].Def, &def)
	if def["name"] != "fake.echo" || def["annotations"].(map[string]any)["readOnlyHint"] != true {
		t.Fatalf("%v", def)
	}
	if _, ok, _ := ns.Lookup(context.Background(), identity.Principal{}, "fake.nope"); ok {
		t.Fatal("found a missing tool")
	}
	if _, ok, _ := ns.Lookup(context.Background(), identity.Principal{}, "echo"); ok {
		t.Fatal("found an unqualified name")
	}
	if tl, ok, _ := ns.Lookup(context.Background(), identity.Principal{}, "fake.write"); !ok || tl.Gate != "fake.write" {
		t.Fatal("lookup")
	}
	list, _ := ns.List(context.Background(), identity.Principal{})
	if len(list) != 4 || ns.Label() != "native" {
		t.Fatal("list")
	}
	if title("list_rows") != "List rows" || title("") != "" {
		t.Fatal("title")
	}
}

// TestNativeSourceRows: the org's native connector rows (S18) join the built-ins on /mcp/native.
func TestNativeSourceRows(t *testing.T) {
	calls := 0
	ns := &NativeSource{Connectors: []connector.Connector{fake.New()}, Vis: visAll{},
		Rows: func(context.Context, identity.Principal) ([]connector.RowTools, error) {
			calls++
			return []connector.RowTools{{Name: "postgres", Type: "postgres", Tools: []connector.ToolSpec{
				{Name: "query", SideEffect: connector.SideEffectRead},
				{Name: "execute", SideEffect: connector.SideEffectWrite},
				{Name: "undo", Internal: true},
			}}}, nil
		}}
	ctx := context.Background()
	nts, _, err := ns.AllWithPolicy(ctx, identity.Principal{})
	if err != nil || len(nts) != 6 {
		t.Fatalf("%v %d", err, len(nts))
	}
	names := []string{}
	for _, nt := range nts {
		names = append(names, nt.Name+"/"+nt.ConnectorType)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, "postgres.execute/postgres") || strings.Contains(got, "postgres.undo") {
		t.Fatal(got)
	}
	if tl, ok, err := ns.Lookup(ctx, identity.Principal{}, "postgres.execute"); err != nil || !ok || tl.Gate != "postgres.execute" {
		t.Fatalf("lookup %v %v", ok, err)
	}
	for _, miss := range []string{"postgres.undo", "postgres.nope", "other.query"} {
		if _, ok, _ := ns.Lookup(ctx, identity.Principal{}, miss); ok {
			t.Fatalf("found %s", miss)
		}
	}
	// A built-in name never consults the rows.
	before := calls
	if _, ok, _ := ns.Lookup(ctx, identity.Principal{}, "fake.echo"); !ok || calls != before {
		t.Fatal("built-in lookup read the rows")
	}
	boom := &NativeSource{Rows: func(context.Context, identity.Principal) ([]connector.RowTools, error) {
		return nil, errors.New("db down")
	}}
	if _, _, err := boom.AllWithPolicy(ctx, identity.Principal{}); err == nil {
		t.Fatal("a rows error was swallowed")
	}
}

func TestParamHeaders(t *testing.T) {
	ok := []struct {
		schema string
		n      int
	}{
		{`{"type":"object","properties":{"a":{"type":"string","x-mcp-header":"A"},"o":{"type":"object","properties":{"b":{"type":"integer","x-mcp-header":"B"}}}}}`, 2},
		{`{"type":"object"}`, 0},
		{``, 0},
	}
	for _, c := range ok {
		hs, valid := ParamHeaders(json.RawMessage(c.schema))
		if !valid || len(hs) != c.n {
			t.Errorf("%s: %v %v", c.schema, hs, valid)
		}
	}
	bad := []string{
		`{"properties":{"a":{"type":"number","x-mcp-header":"A"}}}`,
		`{"properties":{"a":{"type":"string","x-mcp-header":""}}}`,
		`{"properties":{"a":{"type":"string","x-mcp-header":"A B"}}}`,
		`{"properties":{"a":{"type":"string","x-mcp-header":"A"},"b":{"type":"string","x-mcp-header":"a"}}}`,
		`{"properties":{"l":{"type":"array","items":{"type":"string","x-mcp-header":"I"}}}}`,
		`{"oneOf":[{"properties":{"a":{"type":"string","x-mcp-header":"A"}}}]}`,
	}
	for _, s := range bad {
		if _, valid := ParamHeaders(json.RawMessage(s)); valid {
			t.Errorf("accepted %s", s)
		}
	}
}

// FuzzFrontEnvelope: any message the transport lets through gets a JSON-RPC response or a 202,
// never a panic.
func FuzzFrontEnvelope(f *testing.F) {
	for _, s := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":"x","method":"tools/list","params":{"cursor":"bzE="}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled"}`,
		`{"jsonrpc":"2.0","id":null,"method":"initialize","params":{"protocolVersion":7}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"deedglass.check_approval","arguments":{"approval_id":1}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":"x"}`,
	} {
		f.Add(s)
	}
	g := &scripted{submit: func(gate.ActionRequest) (*gate.Action, error) { return executed(id1, `{"content":[]}`), nil },
		approval: func(string) (*gate.Action, error) { return nil, httpx.E(httpx.CodeNotFound) }}
	fr := &Front{Tools: tools{n: 3}, Gate: g, CallWait: time.Millisecond}
	f.Fuzz(func(t *testing.T, s string) {
		e, err := jsonrpc.Parse([]byte(s))
		if err != nil {
			return
		}
		r := fr.Handle(context.Background(), Request{Env: e, Modern: e.ModernVersion() != ""})
		if r.Env == nil {
			if r.HTTPStatus != 202 {
				t.Fatalf("no body with %d", r.HTTPStatus)
			}
			return
		}
		if _, err := jsonrpc.Parse(r.Env.Marshal()); err != nil {
			t.Fatalf("not JSON-RPC: %s", r.Env.Marshal())
		}
	})
}
