package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/gate/lifecycle"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/intent"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

var update = flag.Bool("update", false, "rewrite testdata/gate/digests")

func TestNormalizeTable(t *testing.T) {
	big := `{"s":"` + strings.Repeat("x", MaxArgsBytes) + `"}`
	deep := strings.Repeat(`{"a":`, 33) + `1` + strings.Repeat(`}`, 33)
	cases := []struct {
		name      string
		r         ActionRequest
		param     string // a 400's param; "" for success
		connector string
		tool      string
		unknown   bool
		ctype     string
	}{
		{name: "native", r: ActionRequest{Tool: "fake.echo", Args: raw(`{}`)}, connector: "fake", tool: "echo"},
		{name: "split at the first dot", r: ActionRequest{Tool: "gh.github.create_issue", Args: raw(`{}`), Mode: ModeProxy}, connector: "gh", tool: "github.create_issue", ctype: "mcp"},
		{name: "upper case", r: ActionRequest{Tool: "Fake.echo", Args: raw(`{}`)}, param: "tool"},
		{name: "no dot", r: ActionRequest{Tool: "fake", Args: raw(`{}`)}, param: "tool"},
		{name: "no connector", r: ActionRequest{Tool: ".echo", Args: raw(`{}`)}, param: "tool"},
		{name: "300 bytes", r: ActionRequest{Tool: strings.Repeat("a", 300), Args: raw(`{}`)}, param: "tool"},
		{name: "empty", r: ActionRequest{Tool: "", Args: raw(`{}`)}, param: "tool"},
		{name: "not utf-8", r: ActionRequest{Tool: "fake.\xff", Args: raw(`{}`)}, param: "tool"},
		{name: "control", r: ActionRequest{Tool: "fake.ec\x01ho", Args: raw(`{}`)}, param: "tool"},
		{name: "native tool grammar", r: ActionRequest{Tool: "fake.readTextFile", Args: raw(`{}`)}, param: "tool"},
		{name: "mcp charset in proxy", r: ActionRequest{Tool: "scratch.readTextFile", Args: raw(`{}`), Mode: ModeProxy}, connector: "scratch", tool: "readTextFile", ctype: "mcp"},
		{name: "look-alike is recorded", r: ActionRequest{Tool: "fаke.echo", Args: raw(`{}`)}, unknown: true, tool: "echo"},
		{name: "args array", r: ActionRequest{Tool: "fake.echo", Args: raw(`[]`)}, param: "args"},
		{name: "args string", r: ActionRequest{Tool: "fake.echo", Args: raw(`"x"`)}, param: "args"},
		{name: "args missing", r: ActionRequest{Tool: "fake.echo"}, param: "args"},
		{name: "args too large", r: ActionRequest{Tool: "fake.echo", Args: raw(big)}, param: "args"},
		{name: "args too deep", r: ActionRequest{Tool: "fake.echo", Args: raw(deep)}, param: "args"},
		{name: "big integer", r: ActionRequest{Tool: "fake.echo", Args: raw(`{"n": 9007199254740993}`)}, param: "args"},
		{name: "duplicate key", r: ActionRequest{Tool: "fake.echo", Args: raw(`{"a":1,"a":2}`)}, param: "args"},
		{name: "fraction ok", r: ActionRequest{Tool: "fake.echo", Args: raw(`{"amount": 12.5}`)}, connector: "fake", tool: "echo"},
		{name: "bad mode", r: ActionRequest{Tool: "fake.echo", Args: raw(`{}`), Mode: "edge"}, param: "mode"},
		{name: "gateway can't declare a type", r: ActionRequest{Tool: "fake.echo", Args: raw(`{}`), ConnectorType: "postgres"}, param: "connector_type"},
		{name: "unknown type", r: ActionRequest{Tool: "pg.query", Args: raw(`{}`), Mode: ModeProxy, ConnectorType: "oracle"}, param: "connector_type"},
		{name: "postgres edge", r: ActionRequest{Tool: "pg.query", Args: raw(`{}`), Mode: ModeProxy, ConnectorType: "postgres"}, connector: "pg", tool: "query", ctype: "postgres"},
		{name: "bad tool digest", r: ActionRequest{Tool: "fake.echo", Args: raw(`{}`), ToolDigest: "sha256:XY"}, param: "tool_digest"},
		{name: "bad transport", r: ActionRequest{Tool: "fake.echo", Args: raw(`{}`), Context: &RequestContext{Transport: "MCP!"}}, param: "context.transport"},
		{name: "long client", r: ActionRequest{Tool: "fake.echo", Args: raw(`{}`), Context: &RequestContext{Client: &ClientInfo{Name: strings.Repeat("c", 65)}}}, param: "context.client"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n, err := Normalize(c.r, 0)
			if c.param != "" {
				he := httpx.AsError(err)
				if he == nil || he.Code != httpx.CodeValidationFailed || he.Param != c.param || he.Status != 400 {
					t.Fatalf("got %v, want validation_failed on %s", err, c.param)
				}
				if strings.ContainsAny(he.Message, "—–!") || !strings.HasSuffix(he.Message, ".") {
					t.Fatalf("copy: %q", he.Message)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if n.Connector != c.connector || n.ToolName != c.tool || n.Unknown != c.unknown || n.ConnectorType != c.ctype {
				t.Fatalf("%+v", n)
			}
		})
	}
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestArgsTooLargeMessage(t *testing.T) {
	args := `{"s":"` + strings.Repeat("x", 262145-8) + `"}`
	_, err := Normalize(ActionRequest{Tool: "fake.echo", Args: raw(args)}, 0)
	if he := httpx.AsError(err); he == nil || he.Message != "args is 257 KiB; the limit is 256 KiB. Send a reference to the data instead of the data itself." {
		t.Fatalf("%v", err)
	}
	_, err = Normalize(ActionRequest{Tool: "fake.echo", Args: raw(`{"s":"` + strings.Repeat("x", 2000) + `"}`)}, 1024)
	if he := httpx.AsError(err); he == nil || !strings.Contains(he.Message, "the limit is 1 KiB") {
		t.Fatalf("gate.max_args_bytes: %v", err)
	}
}

// TestDigests pins request_digest for a few requests (testdata/gate/digests), and checks the
// invariances: key order and whitespace never change it; mode and tool_digest do.
func TestDigests(t *testing.T) {
	td := "sha256:" + strings.Repeat("ab", 32)
	type golden struct {
		Tool        string          `json:"tool"`
		Args        json.RawMessage `json:"args"`
		Mode        Mode            `json:"mode"`
		ToolDigest  *string         `json:"tool_digest"`
		OnBehalfOf  string          `json:"on_behalf_of,omitempty"`
		Session     string          `json:"session,omitempty"`
		Digest      string          `json:"request_digest"`
		Fingerprint string          `json:"fingerprint"`
	}
	inputs := map[string]golden{
		"echo":         {Tool: "fake.echo", Args: raw(`{"message":"hello"}`), Mode: ModeGateway},
		"fraction":     {Tool: "fake.echo", Args: raw(`{"amount":12.5}`), Mode: ModeGateway},
		"proxy":        {Tool: "scratch.readTextFile", Args: raw(`{"path":"/tmp/x"}`), Mode: ModeProxy, ToolDigest: &td},
		"obo":          {Tool: "fake.echo", Args: raw(`{}`), Mode: ModeGateway, OnBehalfOf: "u_4821", Session: "chat_77"},
		"nested_order": {Tool: "fake.write", Args: raw(`{"table":"orders","rows":3,"meta":{"b":1,"a":[1,2.5,"x"]}}`), Mode: ModeGateway},
	}
	for name, g := range inputs {
		n, err := Normalize(ActionRequest{Tool: g.Tool, Args: g.Args, Mode: g.Mode, ToolDigest: strOr(g.ToolDigest), OnBehalfOf: g.OnBehalfOf, Session: g.Session}, 0)
		if err != nil {
			t.Fatal(name, err)
		}
		g.Digest, g.Fingerprint = n.Digest, n.Fingerprint
		got, _ := json.MarshalIndent(g, "", "  ")
		got = append(got, '\n')
		p := filepath.Join("..", "..", "testdata", "gate", "digests", name+".json")
		if *update {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: digest golden differs (go test ./internal/gate -run TestDigests -update):\n%s", name, got)
		}
	}
	a, _ := Normalize(ActionRequest{Tool: "fake.write", Args: raw(`{"rows":1,"table":"t"}`)}, 0)
	b, _ := Normalize(ActionRequest{Tool: "fake.write", Args: raw(` { "table" : "t", "rows" : 1 } `)}, 0)
	c, _ := Normalize(ActionRequest{Tool: "fake.write", Args: raw(`{"rows":1,"table":"t"}`), Mode: ModeProxy}, 0)
	d, _ := Normalize(ActionRequest{Tool: "fake.write", Args: raw(`{"rows":1,"table":"t"}`), Session: "s"}, 0)
	if a.Digest != b.Digest || a.Fingerprint != b.Fingerprint || a.Digest == c.Digest || a.Digest != d.Digest || a.Fingerprint == d.Fingerprint {
		t.Fatal("digest invariances")
	}
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func FuzzNormalize(f *testing.F) {
	f.Add("fake.echo", []byte(`{"message":"hello"}`), false)
	f.Add("gh.github.create_issue", []byte(`{"title":"x","n":1.5}`), true)
	f.Add("fаke.echo", []byte(`{}`), false)
	f.Add("", []byte(`[]`), true)
	f.Add("a.b", []byte(`{"n":9007199254740993}`), false)
	f.Fuzz(func(t *testing.T, tool string, args []byte, proxy bool) {
		mode := ModeGateway
		if proxy {
			mode = ModeProxy
		}
		n, err := Normalize(ActionRequest{Tool: tool, Args: args, Mode: mode}, 0)
		if err != nil {
			if he := httpx.AsError(err); he == nil || he.Code != httpx.CodeValidationFailed || he.Param == "" {
				t.Fatalf("an untyped error: %v", err)
			}
			return
		}
		if !reDigest.MatchString(n.Digest) || !reDigest.MatchString(n.Fingerprint) {
			t.Fatalf("digest %q", n.Digest)
		}
		if !n.Unknown && (!reConnector.MatchString(n.Connector) || n.ToolName == "") {
			t.Fatalf("a recordable name without a connector: %+v", n)
		}
	})
}

func BenchmarkNormalize(b *testing.B) {
	args := raw(`{"message":"` + strings.Repeat("x", 2000) + `","n":1,"amount":12.5}`)
	for b.Loop() {
		if _, err := Normalize(ActionRequest{Tool: "fake.echo", Args: args}, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// --- execution and the L0 witness ---

type execFunc func(context.Context, connector.Call) (connector.Result, error)

func (f execFunc) Execute(ctx context.Context, c connector.Call) (connector.Result, error) {
	return f(ctx, c)
}

func TestClassify(t *testing.T) {
	p := &Pipeline{now: time.Now, log: slog.New(slog.DiscardHandler), m: NewMetrics()}
	call := connector.Call{Deadline: time.Now().Add(time.Second)}
	cases := []struct {
		name   string
		f      execFunc
		status ExecStatus
		code   string
		reason string
		sent   bool
	}{
		{"ok", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{Output: raw(`{}`)}, nil
		}, ExecSucceeded, "", "", true},
		{"tool error", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{}, &connector.Error{Kind: connector.KindToolError, Code: "card_declined", AgentMessage: "No."}
		}, ExecFailed, "card_declined", "", true},
		{"invalid request never sent", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "BAD CODE"}
		}, ExecFailed, "tool_error", "", false},
		{"config", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured"}
		}, ExecFailed, "connector_misconfigured", "", false},
		{"upstream", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{}, &connector.Error{Kind: connector.KindUpstream, Code: "upstream_error"}
		}, ExecUnknown, "upstream_error", ReasonOutcomeUnknown, true},
		{"timeout kind", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{}, &connector.Error{Kind: connector.KindTimeout}
		}, ExecUnknown, "timeout", ReasonTimeout, true},
		{"an unknown kind is unknown", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{}, &connector.Error{Kind: "vault"}
		}, ExecUnknown, "upstream_error", ReasonOutcomeUnknown, true},
		{"untyped", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{}, errors.New("socket closed")
		}, ExecUnknown, "upstream_error", ReasonOutcomeUnknown, true},
		{"deadline", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{}, context.DeadlineExceeded
		}, ExecUnknown, "timeout", ReasonTimeout, true},
		{"panic", func(context.Context, connector.Call) (connector.Result, error) { panic("boom") }, ExecUnknown, "internal_error", ReasonOutcomeUnknown, true},
		{"not json", func(context.Context, connector.Call) (connector.Result, error) {
			return connector.Result{Output: raw(`{nope`)}, nil
		}, ExecUnknown, "internal_error", ReasonOutcomeUnknown, true},
	}
	for _, c := range cases {
		e := p.execute(context.Background(), c.f, call)
		if e.Status != c.status || e.ErrorCode != c.code || e.Reason != c.reason || e.Sent != c.sent {
			t.Errorf("%s: %+v", c.name, e)
		}
		if e.Status == ExecSucceeded && e.ErrorMsg != "" || e.Status != ExecSucceeded && c.name != "invalid request never sent" && c.name != "config" && c.name != "upstream" && c.name != "timeout kind" && e.ErrorMsg == "" && c.name != "an unknown kind is unknown" {
			t.Errorf("%s: message %q", c.name, e.ErrorMsg)
		}
		w, deferred := L0{}.Inline(context.Background(), Subject{}, e)
		switch {
		case deferred:
			t.Error("L0 deferred")
		case e.Status == ExecSucceeded && w.Verdict != VerdictVerified:
			t.Errorf("%s: %+v", c.name, w)
		case e.Status == ExecUnknown && (w.Verdict != VerdictUnverifiable || len(w.Reasons) != 1):
			t.Errorf("%s: an unknown outcome must be UNVERIFIABLE: %+v", c.name, w)
		}
	}
	if w, _ := (L0{}).Inline(context.Background(), Subject{Mode: ModeProxy}, Execution{Status: ExecSucceeded}); w.Method != MethodEdgeReported {
		t.Fatal(w)
	}
	if _, err := (L0{}).Deferred(context.Background(), Subject{}); !errors.Is(err, ErrNoDeferredWitness) {
		t.Fatal(err)
	}
	if g := gaveUp(MethodReported); g.Verdict != VerdictUnverifiable || g.Reasons[0] != ReasonWitnessGaveUp {
		t.Fatal(g)
	}
	if long := clipMessage(strings.Repeat("é", 2000)); len(long) > MaxErrorMessage || !strings.HasSuffix(long, "…") {
		t.Fatalf("%d", len(long))
	}
	if safeErrorCode("Bad-Code", "x") != "x" || safeErrorCode("ok_code", "x") != "ok_code" {
		t.Fatal("safeErrorCode")
	}
}

func TestResultColumns(t *testing.T) {
	s, d, err := resultColumns(raw(`{"a":1}`), 1024)
	if err != nil || string(s) != `{"a":1}` || d == nil {
		t.Fatal(err)
	}
	s, d2, _ := resultColumns(raw(`{"a":1}`), 3)
	if s != nil || *d2 != *d {
		t.Fatal("a large result keeps its digest only")
	}
	if s, d, _ := resultColumns(nil, 10); s != nil || d != nil {
		t.Fatal("empty")
	}
	if _, _, err := resultColumns(raw(`{x`), 10); err == nil {
		t.Fatal("invalid json digested")
	}
}

// --- copy ---

// reBadCopy: dashes, exclamation marks, filler and banned words (assembled here so this file needs
// no .banned-words-allow entry).
var reBadCopy = regexp.MustCompile(`[—–!]|(?i)\b(successfully|oops|` + "rece" + "ipt|guaran" + "teed|pro" + "ves)\b")

func TestCopy(t *testing.T) {
	msgs := []string{msgToolEmpty, msgToolTooLong, msgToolNotUTF8, msgArgsMissing, msgArgsNotObject, msgConnectorTypeGateway, msgToolDigest,
		msgTransport, msgClient, msgWait, msgMode("x"), msgToolGrammar("Fake.echo"), msgConnectorTypeUnknown("oracle"),
		msgArgsTooLarge(320_000, MaxArgsBytes), ToolUnknownMessage("fake.ecko"), ConnectorUnavailableMessage("fake"),
		MsgIdempotencyConflict, MsgCancelExecuting, MsgCancelDone, MsgResultReported, MsgResultLate, MsgResultNotProxy,
		MsgResultNotAuthorized, MsgResultDigest, MsgResultMissing, MsgResultStatus, MsgResultTimes, MsgResultTooLarge,
		MsgResultErrorCode, MsgWaitTooMany, NothingRan, UnreachableMessage("http://127.0.0.1:8080")}
	for _, code := range []intent.Code{intent.CodeIntegerRange, intent.CodeTooDeep, intent.CodeDuplicateKey, intent.CodeLoneSurrogate, intent.CodeNumberRange, intent.CodeSyntax} {
		msgs = append(msgs, msgArgsInvalid(&intent.Error{Code: code}))
	}
	for _, m := range msgs {
		if reBadCopy.MatchString(m) || !strings.HasSuffix(m, ".") || m[0] < 'A' && m[0] != '"' && !strings.HasPrefix(m, "args") {
			t.Errorf("copy: %q", m)
		}
	}
	if ToolUnknownMessage("fake.ecko") != `No tool named "fake.ecko". Tools look like "connector.tool", for example "fake.echo".` {
		t.Fatal(ToolUnknownMessage("fake.ecko"))
	}
	if ConnectorUnavailableMessage("fake") != `The "fake" connector is not available on this server. Check Console › Connectors.` {
		t.Fatal(ConnectorUnavailableMessage("fake"))
	}
	if clip(strings.Repeat("é", 100), 9) != "éééé…" || clip("abc", 5) != "abc" {
		t.Fatal(clip(strings.Repeat("é", 100), 9))
	}
}

func storeSubject(name, status string, orgStopped bool) store.GateSubject {
	return store.GateSubject{AgentName: name, AgentStatus: status, OrgStopped: orgStopped}
}

func TestFraming(t *testing.T) {
	msg := `Blocked by policy "no-bulk-writes": writes over 100 rows need a person, not an agent.`
	src, code := "rule", "policy_denied"
	deny := &Action{Action: ledger.Action{ID: "act_x", Status: ledger.StatusDenied, Decision: &ledger.ActionDecision{Message: &msg, Source: &src, Code: &code}}}
	if DenyMessage(deny) != msg+" Nothing was run." || NextStep(deny) != "Next: change the rule in Console › Policies, or ask an admin to run it by hand." {
		t.Fatal(DenyMessage(deny), NextStep(deny))
	}
	stop := "stop"
	if NextStep(&Action{Action: ledger.Action{Status: ledger.StatusDenied, Decision: &ledger.ActionDecision{Source: &stop}}}) != "Next: an admin can start the agent again in Console › Agents." {
		t.Fatal("stop next step")
	}
	budget := "budget_exceeded"
	if !strings.Contains(NextStep(&Action{Action: ledger.Action{Status: ledger.StatusDenied, Decision: &ledger.ActionDecision{Code: &budget}}}), "budget") {
		t.Fatal("budget next step")
	}
	if DenyMessage(&Action{refusal: httpx.E(httpx.CodeAgentStopped)}) == "" || DenyMessage(&Action{}) != "Deedglass refused this action. Nothing was run." {
		t.Fatal("deny fallbacks")
	}
	pm := "Waiting for approval under policy \"gate-test\": Writes wait for a human."
	exp := httpx.Time(time.Date(2026, 10, 5, 4, 42, 0, 0, time.UTC))
	pend := &Action{Action: ledger.Action{ID: "act_p", Status: ledger.StatusPendingApproval, Decision: &ledger.ActionDecision{Message: &pm},
		Approval: &ledger.ActionApproval{ID: "apr_1", ExpiresAt: exp}}}
	ist, _ := time.LoadLocation("Asia/Kolkata")
	if got := PendingMessage(pend, ist); got != `Waiting for approval under policy "gate-test": Writes wait for a human. Expires 10:12 IST.` {
		t.Fatal(got)
	}
	if got := PendingMessage(&Action{}, nil); got != "Waiting for a person to approve this action." {
		t.Fatal(got)
	}
	if !strings.Contains(NextStep(pend), "deedglass actions show act_p --wait") {
		t.Fatal(NextStep(pend))
	}
	for _, st := range []string{ledger.StatusAuthorized, ledger.StatusFailed, ledger.StatusProcessing, ledger.StatusExpired, ledger.StatusExecuted} {
		_ = NextStep(&Action{Action: ledger.Action{ID: "act_x", Status: st}})
	}
	reasons := []string{ReasonTimeout}
	unv := &Action{Action: ledger.Action{Status: ledger.StatusExecuted, Witness: &ledger.ActionWitness{Verdict: VerdictUnverifiable, Reasons: &reasons}}}
	if NextStep(unv) != "Next: check the target system by hand, or raise gate.execute_timeout." || UnknownOutcomeMessage(unv, 1500*time.Millisecond) != "The tool did not answer within 1.5 s. It may have run." {
		t.Fatal(UnknownOutcomeMessage(unv, 1500*time.Millisecond))
	}
	lost := []string{ReasonEdgeLost}
	if UnknownOutcomeMessage(&Action{Action: ledger.Action{Witness: &ledger.ActionWitness{Reasons: &lost}}}, 0) != "The edge never reported a result. The tool may have run." ||
		UnknownOutcomeMessage(&Action{}, 0) != "Deedglass could not learn whether the tool ran. It may have run." {
		t.Fatal("unknown messages")
	}
	for _, c := range []struct{ v, m, want string }{
		{VerdictVerified, MethodReported, "the tool reported success"},
		{VerdictVerified, MethodEdgeReported, "the edge reported success"},
		{VerdictUnverifiable, MethodReported, "the outcome is unknown"},
		{VerdictMismatch, "postgres.pgoutput", "postgres.pgoutput"},
	} {
		if got := WitnessPhrase(LevelL0, c.v, c.m, nil); got != c.want {
			t.Errorf("%s/%s: %q", c.v, c.m, got)
		}
	}
	for r, want := range map[string]string{ReasonTimeout: "the tool did not answer in time", ReasonEdgeLost: "the edge never reported", ReasonWitnessGaveUp: "the witness gave up"} {
		if got := WitnessPhrase(LevelL0, VerdictUnverifiable, MethodReported, []string{r}); got != want {
			t.Errorf("%s: %q", r, got)
		}
	}
}

// --- metrics, broker, config ---

func TestTimeLimitWatch(t *testing.T) {
	var logs bytes.Buffer
	p := &Pipeline{log: slog.New(slog.NewJSONHandler(&logs, nil)), m: NewMetrics()}
	trip := policy.Unavailable("allow-reads: stopped at the evaluation time limit of 50ms")
	other := policy.Unavailable("no policy")
	p.watchTimeLimit(ids.OrgID{}, ids.AgentID{}, trip, &trip)
	p.watchTimeLimit(ids.OrgID{}, ids.AgentID{}, other, nil)
	if e, s := p.m.TimeLimitTrips(); e != 1 || s != 1 {
		t.Fatalf("%d %d", e, s)
	}
	if !strings.Contains(logs.String(), "time limit") || strings.Count(logs.String(), "\n") != 2 {
		t.Fatalf("%s", logs.String())
	}
	var b strings.Builder
	p.m.action(ModeGateway, "executed")
	p.m.job("witness", "done")
	p.m.sweep("crash_recovery", "done")
	p.m.observeOverhead(3 * time.Millisecond)
	p.m.Collect(&b)
	for _, want := range []string{
		`deedglass_gate_actions_total{mode="gateway",status="executed"} 1`,
		`deedglass_gate_overhead_seconds_bucket{le="0.005"} 1`,
		`deedglass_gate_overhead_seconds_bucket{le="0.002"} 0`,
		`deedglass_gate_overhead_seconds_count 1`,
		`deedglass_worker_jobs_total{kind="witness",result="done"} 1`,
		`deedglass_worker_queue_depth 0`,
		`deedglass_gate_sweeps_total{sweeper="crash_recovery",result="done"} 1`,
		`deedglass_policy_eval_time_limit_total{mode="enforce"} 1`,
		`deedglass_policy_eval_time_limit_total{mode="shadow"} 1`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %s in\n%s", want, b.String())
		}
	}
	if p.m.ActionsTotal(ModeProxy, "x") != 0 {
		t.Fatal("ActionsTotal")
	}
	var nilM *Metrics
	nilM.action(ModeGateway, "x")
	nilM.observeOverhead(time.Second)
}

func TestBroker(t *testing.T) {
	b := NewBroker()
	ch, cancel := b.Subscribe("act_1")
	ch2, cancel2 := b.Subscribe("act_1")
	if b.Waiters() != 2 {
		t.Fatal(b.Waiters())
	}
	b.OnEvent(`{"action":"act_1","type":"deed.sealed"}`)
	b.OnEvent(`not json`)
	b.Notify("act_1") // a full channel never blocks
	<-ch
	<-ch2
	b.Reset()
	<-ch
	cancel()
	cancel2()
	if b.Waiters() != 0 {
		t.Fatal("leak")
	}
}

func TestConfigDefaultsAndBackoff(t *testing.T) {
	c := Config{WaitTimeout: time.Hour, MaxArgsBytes: 1 << 30}.withDefaults()
	if c.WaitTimeout != MaxWait || c.MaxArgsBytes != MaxArgsBytes || c.ExecuteTimeout != 30*time.Second || c.EdgeResultTimeout != 10*time.Minute ||
		c.WorkerConcurrency != 4 || c.WorkerMaxAttempts != 8 || c.ApprovalTTL != DefaultApprovalTTL || c.MaxWaitsPerAgent != 20 || c.MaxResultBytes != 256<<10 {
		t.Fatalf("%+v", c)
	}
	for a, want := range map[int]time.Duration{0: time.Second, 3: 8 * time.Second, 8: 256 * time.Second, 9: 5 * time.Minute, 40: 5 * time.Minute} {
		for range 20 {
			if d := backoff(a); d < want*8/10 || d > want*12/10 {
				t.Fatalf("backoff(%d) = %s", a, d)
			}
		}
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("New without dependencies")
	}
	if !validKey("demo-1") || validKey("") || validKey(strings.Repeat("k", 256)) || validKey("tab\there") {
		t.Fatal("validKey")
	}
	if !reCurrency("usd") || reCurrency("USD") || reCurrency("us") {
		t.Fatal("reCurrency")
	}
	if !isMCP(TransportMCPHTTP) || isMCP(TransportREST) {
		t.Fatal("isMCP")
	}
}

func TestStopAndUnknownDecisions(t *testing.T) {
	_, stopped, suspended := policy.StopMessages("bot")
	if d := stopDecision(storeSubject("bot", "stopped", false)); d.Code != policy.CodeAgentStopped || d.Message != stopped || d.Source != "stop" {
		t.Fatal(d)
	}
	if d := stopDecision(storeSubject("bot", "suspended", false)); d.Code != policy.CodeAgentSuspended || d.Message != suspended {
		t.Fatal(d)
	}
	if d := stopDecision(storeSubject("bot", "active", true)); d.Code != policy.CodeOrgStopped {
		t.Fatal(d)
	}
	d := toolUnknownDecision("nope.echo")
	if d.Outcome != policy.Deny || d.RuleID != RuleToolUnknown || string(d.Code) != "tool_unknown" {
		t.Fatal(d)
	}
	e := refusalFor(d, &ledger.Action{ID: "act_1"})
	if e.Code != httpx.CodeToolUnknown || e.Status != 404 || e.Action == nil {
		t.Fatal(e)
	}
	b := refusalFor(policy.Decision{Code: policy.CodeBudgetExceeded, Message: "Budget.", Budget: &policy.BudgetHit{RetryAfterS: 14}}, &ledger.Action{})
	if b.Status != 429 || b.Header.Get("Retry-After") != "14" {
		t.Fatal(b)
	}
	if x := refusalFor(policy.Decision{Code: "made_up"}, &ledger.Action{}); x.Code != httpx.CodePolicyDenied {
		t.Fatal(x)
	}
}

// TestGateDoc keeps docs/GATE.md's state diagram equal to the edge table.
func TestGateDoc(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "GATE.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := "```mermaid\n" + lifecycle.Mermaid() + "```"
	if !strings.Contains(string(b), want) {
		t.Fatalf("docs/GATE.md's state diagram drifted from internal/gate/lifecycle. It must contain:\n%s", want)
	}
	for _, e := range lifecycle.EdgeList() {
		parts := strings.Split(e, " → ")
		if !strings.Contains(string(b), "    "+parts[0]+" --> "+parts[1]+"\n") {
			t.Errorf("edge %s missing from the diagram", e)
		}
	}
}

// --- routes ---

type who struct{ p httpx.Principal }

func (w who) AuthenticateSession(context.Context, *http.Request) (httpx.Principal, error) {
	if w.p.Kind != httpx.KindUser {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return w.p, nil
}

func (w who) AuthenticateKey(context.Context, string) (httpx.Principal, error) {
	if w.p.Kind == httpx.KindUser || w.p.Kind == "" {
		return httpx.Principal{}, httpx.E(httpx.CodeInvalidAPIKey)
	}
	return w.p, nil
}

// TestRouteRoleMatrix: who reaches each Gate route. A request that reaches the handler answers 503
// here (no pipeline); everything else is refused by the httpx pipeline first.
func TestRouteRoleMatrix(t *testing.T) {
	org := ids.New[ids.OrgKind]()
	user := func(r httpx.Role) httpx.Principal {
		return httpx.Principal{Kind: httpx.KindUser, OrgID: org, UserID: ids.New[ids.UserKind](), Role: r, OrgName: "Acme"}
	}
	agent := func(status string) httpx.Principal {
		return httpx.Principal{Kind: httpx.KindAgentKey, OrgID: org, KeyID: ids.New[ids.APIKeyKind](), AgentID: ids.New[ids.AgentKind](),
			Scopes: httpx.DefaultAgentScopes, AgentStatus: status, AgentName: "support-bot"}
	}
	principals := map[string]httpx.Principal{
		"owner": user(httpx.RoleOwner), "admin": user(httpx.RoleAdmin), "viewer": user(httpx.RoleViewer),
		"admin key":         {Kind: httpx.KindAdminKey, OrgID: org, KeyID: ids.New[ids.APIKeyKind](), Role: httpx.RoleAdmin, Scopes: []string{httpx.ScopeAdmin}},
		"agent key":         agent("active"),
		"stopped agent key": agent("stopped"),
		"read-only key":     {Kind: httpx.KindAgentKey, OrgID: org, KeyID: ids.New[ids.APIKeyKind](), AgentID: ids.New[ids.AgentKind](), Scopes: []string{httpx.ScopeActionsRead}, AgentStatus: "active", AgentName: "reader"},
		"none":              {},
	}
	want := map[string]map[string]int{
		"POST /v1/actions": {"owner": 401, "admin": 401, "viewer": 401, "admin key": 403, "agent key": 503, "stopped agent key": 503,
			"read-only key": 403, "none": 401},
		"POST /v1/actions/{id}/cancel": {"owner": 503, "admin": 503, "viewer": 403, "admin key": 503, "agent key": 503, "stopped agent key": 403,
			"read-only key": 403, "none": 401},
		"POST /v1/actions/{id}/result": {"owner": 401, "admin": 401, "viewer": 401, "admin key": 403, "agent key": 503, "stopped agent key": 403,
			"read-only key": 403, "none": 401},
	}
	routes := Routes(nil)
	if len(routes) != 3 {
		t.Fatalf("%d routes", len(routes))
	}
	for name, p := range principals {
		srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: who{p}, Keys: who{p}})
		srv.Mount(routes...)
		ts := httptest.NewServer(srv)
		for _, rt := range routes {
			path := strings.Replace(rt.Pattern, "{id}", "act_01j9zq3m8x2k7w4c5v6b7n8p9r", 1)
			var body io.Reader
			if rt.MaxBody != httpx.NoBody {
				body = strings.NewReader(`{"tool":"fake.echo","args":{}}`)
			}
			req, _ := http.NewRequest(rt.Method, ts.URL+path, body)
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			if p.Kind != httpx.KindUser && p.Kind != "" {
				req.Header.Set("Authorization", "Bearer dg_test_AAAAAAAA_"+strings.Repeat("B", 32))
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if got, w := r.StatusCode, want[rt.Key()][name]; got != w {
				t.Errorf("%s · %s: %d, want %d\n%s", name, rt.Key(), got, w, b)
			}
		}
		ts.Close()
	}
}
