package policy

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBuildInputFull(t *testing.T) {
	v := NormalizeJSONValue(map[string]any{
		"tool": map[string]any{"name": "stripe.create_refund", "side_effect": "write", "default_schema": "billing"},
		"args": map[string]any{"amount": json.Number("2000"), "note": "late", "ratio": json.Number("0.5")},
		"agent": map[string]any{"id": "agt_01k6f6evxjk5zr0t0kntw9ehx5", "name": "support-bot", "status": "active", "labels": map[string]any{"env": "prod", "tier": int64(2)},
			"budgets": map[string]any{"calls_per_minute": int64(60), "calls_per_day": int64(5000), "spend_per_day": "50.00 usd"}},
		"org":     map[string]any{"id": "org_01k6f6evxjk5zr0t0kntw9ehx5", "timezone": "Asia/Kolkata", "stopped": false},
		"user":    map[string]any{"ref": "u-42", "sub": "s", "iss": "i"},
		"session": map[string]any{"ref": "sess-1"},
		"request": map[string]any{"mode": "proxy", "intent_signed": true, "bytes": int64(99), "digest": "sha256:ab"},
		"time":    "2026-10-04T10:00:00+05:30",
		"usage":   map[string]any{"calls_minute": int64(3), "calls_hour": int64(4), "calls_today": int64(5), "spend_today": int64(600), "currency": "USD", "refunds": int64(7), "budgets": map[string]any{"other": int64(8)}},
		"anomaly": map[string]any{"active": true},
	})
	in, p := BuildInput(v)
	if p != nil {
		t.Fatal(p)
	}
	if in.Tool.ConnectorType != "stripe" || in.Tool.Connector != "stripe" || in.Tool.SideEffect != "write" || in.Tool.DefaultSchema != "billing" ||
		in.Agent.Labels["tier"] != "2" || *in.Agent.Budgets.CallsPerMinute != 60 || in.Agent.Budgets.SpendPerDay.Amount != 5000 ||
		in.Org.Timezone != "Asia/Kolkata" || in.User.Ref != "u-42" || in.Session.Ref != "sess-1" || in.Request.Mode != "proxy" ||
		!in.Request.IntentSigned || in.Request.Bytes != 99 || !in.Time.Equal(time.Date(2026, 10, 4, 4, 30, 0, 0, time.UTC)) ||
		in.Usage.Currency != "usd" || in.Usage.Budgets["refunds"] != 7 || in.Usage.Budgets["other"] != 8 || in.Usage.SpendToday != 600 || !in.Anomaly.Active {
		t.Fatalf("%+v", in)
	}
	if string(in.Args) != `{"amount":2000,"note":"late","ratio":0.5}` {
		t.Fatal(string(in.Args))
	}
	// Defaults.
	in, p = BuildInput(map[string]any{"tool": "custom.thing"})
	if p != nil || in.Agent.ID != LocalAgentID || in.Org.ID != LocalOrgID || in.Tool.ConnectorType != "" || in.Tool.SideEffect != "unknown" ||
		!in.Time.Equal(DefaultCaseTime) || in.Request.Mode != "gateway" || string(in.Args) != "{}" {
		t.Fatalf("%+v", in)
	}
	// The API-safe spelling an MCP client lists is the dotted tool, in both forms.
	for _, v := range []any{"postgres_query", map[string]any{"name": "postgres_query"}} {
		in, p = BuildInput(map[string]any{"tool": v})
		if p != nil || in.Tool.Name != "postgres.query" || in.Tool.Connector != "postgres" || in.Tool.ConnectorType != "postgres" || in.Tool.Op != "query" {
			t.Fatalf("%v: %v %+v", v, p, in.Tool)
		}
	}
	// Money as an object.
	in, p = BuildInput(map[string]any{"tool": "a.b", "agent": map[string]any{"budgets": map[string]any{"spend_per_day": map[string]any{"amount": int64(100), "currency": "INR"}}}})
	if p != nil || in.Agent.Budgets.SpendPerDay.Currency != "inr" {
		t.Fatal(p)
	}
}

func TestBuildInputProblems(t *testing.T) {
	for _, c := range []struct {
		in   any
		path string
		msg  string
	}{
		{"nope", "", "The input must be a mapping"},
		{map[string]any{}, "tool", "needs a tool"},
		{map[string]any{"tool": ""}, "tool", "empty"},
		{map[string]any{"tool": int64(3)}, "tool", "must be a tool name or a mapping; found a number"},
		{map[string]any{"tool": map[string]any{"connector": "x"}}, "tool.name", "needs a name"},
		{map[string]any{"tool": map[string]any{"nme": "x"}}, "tool.nme", `Did you mean`},
		{map[string]any{"tool": "a.b", "args": []any{}}, "args", "must be a mapping; found a list"},
		{map[string]any{"tool": "a.b", "sql": true}, "sql", "must be a string"},
		{map[string]any{"tool": "a.b", "sql": "x", "args": map[string]any{"sql": "y"}}, "sql", "both sql and args.sql"},
		{map[string]any{"tool": "a.b", "agent": "x"}, "agent", "must be a mapping"},
		{map[string]any{"tool": "a.b", "agent": map[string]any{"id": "bob"}}, "agent.id", "isn't an agent id"},
		{map[string]any{"tool": "a.b", "agent": map[string]any{"status": "asleep"}}, "agent.status", "active, suspended or stopped"},
		{map[string]any{"tool": "a.b", "agent": map[string]any{"labels": "x"}}, "agent.labels", "must be a mapping"},
		{map[string]any{"tool": "a.b", "agent": map[string]any{"labels": map[string]any{"k": []any{}}}}, "agent.labels.k", "must be a string"},
		{map[string]any{"tool": "a.b", "agent": map[string]any{"budgets": "x"}}, "agent.budgets", "must be a mapping"},
		{map[string]any{"tool": "a.b", "agent": map[string]any{"budgets": map[string]any{"calls_per_minute": -1.0}}}, "agent.budgets.calls_per_minute", "whole number"},
		{map[string]any{"tool": "a.b", "agent": map[string]any{"budgets": map[string]any{"spend_per_day": "fifty"}}}, "agent.budgets.spend_per_day", ""},
		{map[string]any{"tool": "a.b", "agent": map[string]any{"budgets": map[string]any{"spend_per_day": map[string]any{"amount": int64(1), "currency": "zzz"}}}}, "agent.budgets.spend_per_day.currency", "ISO 4217"},
		{map[string]any{"tool": "a.b", "agent": map[string]any{"budgets": map[string]any{"spend_per_day": true}}}, "agent.budgets.spend_per_day", "money"},
		{map[string]any{"tool": "a.b", "org": "x"}, "org", "must be a mapping"},
		{map[string]any{"tool": "a.b", "org": map[string]any{"timezone": "Mars/Base"}}, "org.timezone", "IANA"},
		{map[string]any{"tool": "a.b", "org": map[string]any{"stopped": "yes"}}, "org.stopped", "true or false"},
		{map[string]any{"tool": "a.b", "user": "x"}, "user", "must be a mapping"},
		{map[string]any{"tool": "a.b", "user": map[string]any{"name": "x"}}, "user.name", "Unknown key"},
		{map[string]any{"tool": "a.b", "session": map[string]any{"ref": int64(1)}}, "session.ref", "must be a string"},
		{map[string]any{"tool": "a.b", "session": "x"}, "session", "must be a mapping"},
		{map[string]any{"tool": "a.b", "request": map[string]any{"mode": "batch"}}, "request.mode", "gateway or proxy"},
		{map[string]any{"tool": "a.b", "request": map[string]any{"bytes": "x"}}, "request.bytes", "whole number"},
		{map[string]any{"tool": "a.b", "request": "x"}, "request", "must be a mapping"},
		{map[string]any{"tool": "a.b", "anomaly": "x"}, "anomaly", "must be a mapping"},
		{map[string]any{"tool": "a.b", "anomaly": map[string]any{"active": int64(1)}}, "anomaly.active", "true or false"},
		{map[string]any{"tool": "a.b", "time": "yesterday"}, "time", "RFC 3339"},
		{map[string]any{"tool": "a.b", "time": int64(5)}, "time", "RFC 3339"},
		{map[string]any{"tool": "a.b", "usage": "x"}, "usage", "must be a mapping"},
		{map[string]any{"tool": "a.b", "usage": map[string]any{"currency": int64(1)}}, "usage.currency", "currency code"},
		{map[string]any{"tool": "a.b", "usage": map[string]any{"budgets": "x"}}, "usage.budgets", "mapping of budget ids"},
		{map[string]any{"tool": "a.b", "usage": map[string]any{"budgets": map[string]any{"x": "y"}}}, "usage.budgets.x", "whole number"},
		{map[string]any{"tool": "a.b", "usage": map[string]any{"Calls Minute": int64(1)}}, "usage.Calls Minute", "Unknown key"},
		{map[string]any{"tool": "a.b", "usage": map[string]any{"calls_minute": -2.5}}, "usage.calls_minute", "whole number"},
		{map[string]any{"tool": "a.b", "usage": map[string]any{"calls_minute": int64(-2)}}, "usage.calls_minute", "negative"},
		{map[string]any{"tool": "a.b", "agnet": map[string]any{}}, "agnet", `Did you mean "agent"?`},
	} {
		_, p := BuildInput(c.in)
		if p == nil || p.Path != c.path || !strings.Contains(p.Message+" "+p.Hint, c.msg) {
			t.Errorf("%v: got %+v, want path %q containing %q", c.in, p, c.path, c.msg)
		}
		if p != nil && p.Error() != p.Message {
			t.Error("Error() is the message")
		}
	}
	if typeWord(struct{}{}) != "an unsupported value" || typeWord(nil) != "nothing" {
		t.Fatal("typeWord")
	}
	if MergeCaseInput(map[string]any{"a": 1}, nil).(map[string]any)["a"] != 1 {
		t.Fatal("merge nil")
	}
	if NormalizeJSONValue([]any{json.Number("1.5")}).([]any)[0] != 1.5 {
		t.Fatal("normalize list")
	}
}

func TestCasesFileProblems(t *testing.T) {
	for _, c := range []struct {
		src, want string
		line      int
	}{
		{"cases: x\n", "cases must be a list", 1},
		{"policy: 3\ncases: []\n", "policy must be a file name", 1},
		{"nope: 1\n", `Unknown key "nope" in the cases file.`, 1},
		{"defaults: 3\ncases: []\n", "defaults must be a mapping", 1},
		{"defaults: {agnet: 1}\ncases: []\n", `Unknown key "agnet" in the input.`, 1},
		{"policy: x.yaml\n", "This cases file has no cases.", 0},
		{"cases: []\n", "This cases file has no cases.", 1},
		{"cases:\n  - 3\n", "Each case must be a mapping", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: allow}, extra: 1}\n", `Unknown key "extra" in this case.`, 2},
		{"cases:\n  - {input: {tool: a.b}, expect: {decision: allow}}\n", "Case 1 has no name.", 2},
		{"cases:\n  - {name: a, expect: {decision: allow}}\n", `Case "a" has no input.`, 2},
		{"cases:\n  - {name: a, input: {tool: a.b}}\n", `Case "a" has no expect.`, 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: allow}}\n  - {name: a, input: {tool: a.b}, expect: {decision: allow}}\n", `Case "a" appears twice`, 3},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: 3}\n", "expect must be a mapping", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: allw}}\n", `"allw" isn't a decision`, 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {}}\n", "expect needs a decision", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, rule: 'Not A Rule'}}\n", "isn't a rule id", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, code: 3}}\n", "code must be a string", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, verify: L9}}\n", "verify must be L0, L1 or L2", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, notify: slack}}\n", "notify must be a list", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, notify: [3]}}\n", "list of channels", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, approval: 3}}\n", "approval must be a mapping", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, approval: {reqired: 1}}}\n", `Did you mean "required"?`, 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, approval: {approvers: x}}}\n", "approval.approvers must be a list", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, approval: {approvers: [3]}}}\n", "list of approvers", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, approval: {expires_in: soon}}}\n", `"soon" isn't a duration`, 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, approval: {expires_in: x1.5}}}\n", "isn't a duration", 2},
		{"cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, approval: {required: x}}}\n", "whole number", 2},
		{"cases:\n  - &x {name: a}\n", "Anchors and aliases", 2},
		{"cases:\n  - {name: " + strings.Repeat("n", 201) + ", input: {tool: a.b}, expect: {decision: deny}}\n", "longer than 200", 2},
		{"cases:\n  - {name: a, input: {tool: a.b, args: {n: .inf}}, expect: {decision: deny}}\n", "usable number", 0},
		{strings.Repeat("#", MaxCasesBytes+1), "limit is", 0},
	} {
		cf, ds := ParseCasesYAML([]byte(c.src), "x.cases.yaml")
		if cf != nil || len(ds) == 0 || !strings.Contains(ds[0].Message+" "+ds[0].Hint, c.want) || (c.line > 0 && ds[0].Line != c.line) {
			t.Errorf("%q: %+v", c.src, ds)
		}
	}
	// JSON: a bare list of cases, an object, and a non-mapping.
	cf, ds := ParseCasesValue([]any{map[string]any{"name": "a", "input": map[string]any{"tool": "a.b"}, "expect": map[string]any{"decision": "deny",
		"approval": map[string]any{"required": json.Number("1"), "expires_in": json.Number("1800")}}}}, "api")
	if cf == nil || len(ds) != 0 || *cf.Cases[0].Expect.Approval.ExpiresIn != 1800 {
		t.Fatalf("%+v", ds)
	}
	if cf, _ := ParseCasesValue("x", "api"); cf != nil {
		t.Fatal("a string is not a cases object")
	}
	big := make([]any, MaxCases+1)
	for i := range big {
		big[i] = map[string]any{}
	}
	if _, ds := ParseCasesValue(big, "api"); len(ds) == 0 || !strings.Contains(ds[0].Message, "limit is 1,000") {
		t.Fatalf("%+v", ds)
	}
}

func TestRunCasesCompare(t *testing.T) {
	src, _ := DefaultDenySource()
	c, _ := Compile(src, Options{})
	pg, _ := Compile([]byte(`deedglass: policy/v1
name: approvals
defaults: {decision: deny, reason: No.}
rules:
  - id: wait
    decision: require_approval
    reason: Wait for a human.
    approval: {approvers: [role:admin], required: 2, expires_in: 2h}
    notify: [email]
    verify: L1
`), Options{})
	cf, ds := ParseCasesYAML([]byte(`cases:
  - name: every field matches
    input: {tool: a.b}
    expect: {decision: require_approval, rule: wait, code: approval_required, reason_contains: human, message: 'Waiting for approval under policy "wait": Wait for a human.',
      verify: L1, notify: [email], approval: {required: 2, approvers: [role:admin], expires_in: 2h}}
  - name: every field differs
    input: {tool: a.b}
    expect: {decision: allow, rule: other, code: x, reason_contains: nope, message: nope, verify: L2, notify: [slack], approval: {required: 1, approvers: [role:owner], expires_in: 30m}}
`), "x")
	if cf == nil {
		t.Fatal(ds)
	}
	r := RunCases(context.Background(), pg, cf, TestOptions{})
	if r.Passed != 1 || r.Failed != 1 || len(r.Cases[1].Problems) != 10 || r.OK {
		t.Fatalf("%+v %v", r, r.Cases[1].Problems)
	}
	// An approval expected where the policy has none.
	cf2, _ := ParseCasesYAML([]byte("cases:\n  - {name: a, input: {tool: a.b}, expect: {decision: deny, rule: default, approval: {required: 1}}}\n"), "x")
	r = RunCases(context.Background(), c, cf2, TestOptions{Run: regexp.MustCompile("a")})
	if r.Failed != 1 || !strings.Contains(r.Cases[0].Problems[0], "expected one, got none") || r.BudgetsCovered(c) != 0 {
		t.Fatalf("%+v", r)
	}
	if secondsWords(90) != "90s" || secondsWords(120) != "2m" || orNone("") != "none" {
		t.Fatal("words")
	}
}

func TestObjectsJSON(t *testing.T) {
	at := time.Date(2026, 10, 4, 9, 12, 44, 0, time.UTC)
	b, _ := json.Marshal(Time(at))
	if string(b) != `"2026-10-04T09:12:44.000Z"` {
		t.Fatal(string(b))
	}
	var back Time
	if err := json.Unmarshal(b, &back); err != nil || !time.Time(back).Equal(at) {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`"yesterday"`), &back); err == nil {
		t.Fatal("bad time accepted")
	}
	if err := json.Unmarshal([]byte(`3`), &back); err == nil {
		t.Fatal("number accepted")
	}
	for s, ok := range map[string]bool{"3": true, "03": false, "0": false, "pv_01k6f6evxjk5zr0t0kntw9ehx5": true, "pv_short": false, "x": false, "1000001": false} {
		if _, got := ParseVersionRef(s); got != ok {
			t.Errorf("ParseVersionRef(%q) = %v", s, got)
		}
	}
	if r := refFromBy("key_01"); r.Object != "api_key" || refFromBy("system").ID != nil || refFromBy("usr_1").Object != "user" {
		t.Fatal("refFromBy")
	}
	live := []ActivationRow{{PolicyID: "pol_a", VersionID: "pv_a", Mode: ModeShadow, Version: 2}, {PolicyID: "pol_a", VersionID: "pv_b", Mode: ModeEnforce, Version: 1}}
	p := policyObject(&PolicyRow{ID: "pol_a"}, 2, live)
	if p.Status != StatusEnforced || *p.ActiveVersion != 1 || *p.ShadowVersion != 2 {
		t.Fatalf("%+v", p)
	}
	if p := policyObject(&PolicyRow{ID: "pol_a"}, 2, live[:1]); p.Status != StatusShadow {
		t.Fatal(p.Status)
	}
	if statusOf("pv_a", live) != StatusShadow || statusOf("pv_b", live) != StatusEnforced || statusOf("pv_c", live) != StatusInactive {
		t.Fatal("statusOf")
	}
	if clampLimit(0) != 25 || clampLimit(1000) != MaxListLimit || clampLimit(7) != 7 {
		t.Fatal("clampLimit")
	}
	if !strings.Contains(string(MarshalIndent(map[string]string{"a": "<b>"})), "<b>") {
		t.Fatal("no HTML escaping")
	}
	e := &Error{Kind: KindConflict, Message: "No."}
	if e.Error() != "conflict: No." {
		t.Fatal(e.Error())
	}
	if d := Unavailable("why"); d.Code != CodePolicyUnavailable || d.Errors[0] != "why" {
		t.Fatalf("%+v", d)
	}
}

// Bootstrap in an org that has default-deny saved but nothing enforced (a dev database), and in one
// whose default-deny content differs: it enforces the embedded content either way.
func TestBootstrapExistingPolicy(t *testing.T) {
	w := newWorld(t)
	w.push("default-deny")
	var a *Activation
	if err := w.repo.InOrgTx(bg, w.org, func(tx Tx) error {
		var err error
		a, err = Bootstrap(bg, tx)
		return err
	}); err != nil || a == nil || a.Version.Version != 1 {
		t.Fatalf("%v %+v", err, a)
	}
	w2 := newWorld(t)
	src := strings.Replace(string(starter(t, "default-deny")), "max_calls: 60", "max_calls: 30", 1)
	if _, _, err := w2.svc.Create(bg, w2.who, PushRequest{Source: []byte(src)}); err != nil {
		t.Fatal(err)
	}
	if err := w2.repo.InOrgTx(bg, w2.org, func(tx Tx) error {
		var err error
		a, err = Bootstrap(bg, tx)
		return err
	}); err != nil || a == nil || a.Version.Version != 2 {
		t.Fatalf("%v %+v", err, a)
	}
	// A clock option for completeness.
	s := NewService(w.repo, WithServiceClock(func() time.Time { return DefaultCaseTime }))
	res, err := s.Evaluate(bg, w.who, DryRunRequest{Input: map[string]any{"tool": "a.b"}})
	if err != nil || res.Decision != Deny {
		t.Fatalf("%v %+v", err, res)
	}
	_ = NewRegistry(w.repo, nil, WithRegistryLogger(nil))
}
