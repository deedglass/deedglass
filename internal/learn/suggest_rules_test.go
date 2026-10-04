package learn

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// S28 §6 "Generators": fixtures of Evidence built with NewEvidence from rows whose inputs are real
// policy.Input JSON, so deriveFacts recomputes the SQL and amount facts the way the pass does.

// sugNow is the pass time of every generator fixture (memStore's clock base too).
var sugNow = time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)

// simSource reads examples/sim/acme-sim.v<n>.yaml.
func simSource(t testing.TB, n int) []byte {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("../../examples/sim/acme-sim.v%d.yaml", n))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// rowSpec is one decided action of a fixture. Decision, source and rule come from evaluating the
// input under the fixture's base, unless rule is set (a row recorded under other facts).
type rowSpec struct {
	at                     time.Time
	agentID, agent         string
	tool, ct, side         string
	args                   map[string]any
	approval               ApprovalOutcome
	wait                   float64
	approvers              []string
	verdict, level         string
	rule, decision, source string // overrides
	noInput                bool
}

// evFixture is a policy plus decided rows.
type evFixture struct {
	t    testing.TB
	org  OrgInfo
	src  []byte
	base *policy.Compiled
	rv   *ResolvedVersion
	rows []EvidenceRow
	eps  []EpisodeRef
	n    int
}

func newEvFixture(t testing.TB, src []byte) *evFixture {
	t.Helper()
	c, diags := policy.Compile(src, policy.Options{})
	if c == nil {
		t.Fatalf("compile: %v", diags)
	}
	org := OrgInfo{ID: ids.New[ids.OrgKind](), Name: "Acme", Slug: "acme", Timezone: "UTC", Settings: DefaultSettings()}
	rv := &ResolvedVersion{VersionInfo: VersionInfo{PolicyID: "pol_01jbq0000000000000000000aa", Name: c.Name, Version: 1, VersionID: "pv_01jbq0000000000000000000v1"},
		VersionHash: c.VersionHash, Source: src, Latest: 1, Found: true}
	return &evFixture{t: t, org: org, src: src, base: c, rv: rv}
}

// policyInput is a real stored input for one call.
func policyInput(t testing.TB, at time.Time, agentID, agent, tool, ct, side string, args map[string]any) json.RawMessage {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	ab, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	in := policy.Input{V: policy.InputVersion, Org: policy.OrgInput{OrgFacts: policy.OrgFacts{ID: "org_x", Timezone: "UTC"}},
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{ID: agentID, Name: agent, Status: policy.StatusActive}},
		Tool:  policy.ToolInput{ToolFacts: policy.ToolFacts{Name: tool, Connector: "conn", ConnectorType: ct, SideEffect: side}},
		Args:  ab, Time: at}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f *evFixture) add(s rowSpec) *EvidenceRow {
	f.t.Helper()
	f.n++
	if s.at.IsZero() {
		s.at = sugNow.Add(-time.Duration(f.n) * time.Minute)
	}
	if s.agentID == "" {
		s.agentID, s.agent = "agt_01jbq00000000000000000ops", "ops-agent"
	}
	if s.side == "" {
		s.side = "write"
	}
	raw := policyInput(f.t, s.at, s.agentID, s.agent, s.tool, s.ct, s.side, s.args)
	var d policy.Decision
	if s.rule == "" || s.decision == "" || s.source == "" { // all three given: recorded as is, no evaluation
		var in policy.Input
		_ = json.Unmarshal(raw, &in)
		d = policy.Evaluate(bg, f.base, in, policy.EvalOptions{Deadline: 10 * time.Second}) // no backstop trip under -race load
	}
	r := EvidenceRow{ActionID: fmt.Sprintf("act_fx%06d", f.n), At: s.at, AgentID: s.agentID, AgentName: s.agent, Tool: s.tool,
		ConnectorType: s.ct, SideEffect: s.side, Decision: string(d.Outcome), Source: d.Source, RuleID: d.RuleID,
		VersionID: f.rv.VersionID, Approval: s.approval, WaitSeconds: -1, Approvers: s.approvers, Verdict: s.verdict, Level: s.level, Input: raw}
	if s.rule != "" {
		r.RuleID, r.Source = s.rule, policy.SourceRule
	}
	if s.decision != "" {
		r.Decision = s.decision
	}
	if s.source != "" {
		r.Source = s.source
	}
	if s.approval == ApprovalApproved || s.approval == ApprovalDenied {
		r.WaitSeconds = s.wait
	}
	if s.noInput {
		r.Input = nil
	}
	f.rows = append(f.rows, r)
	return &f.rows[len(f.rows)-1]
}

func (f *evFixture) window() Window { return Window{From: sugNow.Add(-SuggestWindow), To: sugNow} }

func (f *evFixture) evidence() *Evidence {
	return NewEvidence(f.org, f.rv, f.base, f.window(), f.rows, f.eps)
}

// inputRows are the fixture's rows as replay reads them, in (evaluated_at, id) order.
func (f *evFixture) inputRows() []InputRow {
	out := make([]InputRow, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, InputRow{ActionID: r.ActionID, DecidedAt: r.At, Tool: r.Tool, Input: r.Input,
			Recorded: RecordedDecision{Decision: r.Decision, Source: r.Source, RuleID: r.RuleID, PolicyVersionID: r.VersionID}, Approval: r.Approval})
	}
	slices.SortFunc(out, func(a, b InputRow) int {
		if c := a.DecidedAt.Compare(b.DecidedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ActionID, b.ActionID)
	})
	return out
}

func orderUpdate(id int) map[string]any {
	return map[string]any{"sql": fmt.Sprintf("UPDATE public.orders SET status = 'shipped' WHERE id = %d", id)}
}

// addOrderApprovals adds approved and expired order updates under approve-order-updates.
func (f *evFixture) addOrderApprovals(approved, expired, denied int) {
	for i := range approved {
		f.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: orderUpdate(i), approval: ApprovalApproved, wait: 1260, approvers: []string{"usr_a", "usr_b"}})
	}
	for i := range expired {
		f.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: orderUpdate(1000 + i), approval: ApprovalExpired})
	}
	for i := range denied {
		f.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: orderUpdate(2000 + i), approval: ApprovalDenied, wait: 300})
	}
}

func onlyGen(t *testing.T, fn Generator, ev *Evidence) ([]Proposal, []Dropped) {
	t.Helper()
	return fn(ev)
}

const wantOrdersWhen = `sql.kind == "update" && sql.tables == ["public.orders"] && sql.statements == 1 && sql.has_where && !sql.unbounded_write && size(sql.dangerous_functions) == 0 && sql.tables_complete`

func TestGenLoosenOrders(t *testing.T) {
	t.Parallel()
	f := newEvFixture(t, simSource(t, 1))
	f.addOrderApprovals(401, 3, 0)
	ev := f.evidence()
	re := ev.ruleEv("approve-order-updates")
	if re == nil || re.Approvals.Requested != 404 || re.Approvals.Approved != 401 || re.Approvals.Expired != 3 || re.WaitMedian != 1260 || re.Approvers != 2 {
		t.Fatalf("rule evidence: %+v", re)
	}
	ps, ds := onlyGen(t, GenLoosenFn, ev)
	if len(ps) != 1 || len(ds) != 0 {
		t.Fatalf("want one loosen, got %d (dropped %v)", len(ps), ds)
	}
	p := ps[0]
	if p.Kind != KindLoosen || p.Generator != GenLoosenApprovalRate || p.TargetRuleID != "approve-order-updates" || p.MainCount != 401 {
		t.Fatalf("proposal: %+v", p)
	}
	op, ok := p.Ops[0].(policy.InsertRuleBefore)
	if !ok || len(p.Ops) != 1 || op.RuleID != "approve-order-updates" {
		t.Fatalf("ops: %#v", p.Ops)
	}
	if op.Rule.When != wantOrdersWhen {
		t.Errorf("when:\n got %s\nwant %s", op.Rule.When, wantOrdersWhen)
	}
	if op.Rule.ID != "allow-order-updates" || op.Rule.Decision != policy.Allow || !slices.Equal(op.Rule.Match.Tool, []string{"postgres.execute"}) ||
		!slices.Equal(op.Rule.Match.Agent, []string{"ops-agent"}) {
		t.Errorf("rule: %+v", op.Rule)
	}
	// verify copies the target's level (acme-sim v1 sets none, so the default L0).
	if op.Rule.Verify != policy.L0 {
		t.Errorf("verify %q, want L0 (copied)", op.Rule.Verify)
	}
	if want := "People approved 401 of 404 of these between 2026-09-25 and 2026-10-09."; op.Rule.Description != want {
		t.Errorf("description %q", op.Rule.Description)
	}
	if got, want := p.Title(), "Allow updates to public.orders without approval"; got != want {
		t.Errorf("title %q", got)
	}
	if p.TemplateTitle != "Allow updates to {table_1} without approval" {
		t.Errorf("template title %q", p.TemplateTitle)
	}
	if got, want := p.Rationale(), "People approved 401 of 404 updates to public.orders by ops-agent in the last 14 days and denied none. Each waited a median of 21 minutes."; got != want {
		t.Errorf("rationale:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(p.RiskNote(), "ops-agent could update single rows of public.orders without a person looking.") {
		t.Errorf("risk %q", p.RiskNote())
	}
	if p.Pattern["sql.kind"] != "update" || !slices.Equal(p.Pattern["sql.tables"].([]string), []string{"public.orders"}) {
		t.Errorf("pattern %v", p.Pattern)
	}
	appr := p.Evidence["approvals"].(map[string]any)
	if appr["requested"] != 404 || appr["approved"] != int64(401) || appr["expired"] != int64(3) || p.Evidence["median_wait_seconds"] != 1260.0 {
		t.Errorf("evidence %v", p.Evidence)
	}
	// The guard: allow only what people approved, let expire or left waiting.
	good := &ReplayResult{Transitions: []Transition{{From: "require_approval", To: "allow", Count: 404, HumanOutcome: &HumanOutcome{Approved: 401, Expired: 3}}}}
	if ok, why := p.Guard(good); !ok {
		t.Errorf("guard refused the honest replay: %s", why)
	}
	guardCases := []struct {
		name string
		r    *ReplayResult
		want string
	}{
		{"human denied", &ReplayResult{Transitions: []Transition{{From: "require_approval", To: "allow", Count: 2, HumanOutcome: &HumanOutcome{Denied: 1, Approved: 1}}}}, "a person denied"},
		{"policy deny", &ReplayResult{Transitions: []Transition{{From: "deny", To: "allow", Count: 3}}}, "the policy denies today"},
		{"never requested", &ReplayResult{Transitions: []Transition{{From: "require_approval", To: "allow", Count: 3, HumanOutcome: &HumanOutcome{NotRequested: 3}}}}, "never went to a person"},
		{"too many", &ReplayResult{Transitions: []Transition{{From: "require_approval", To: "allow", Count: 405, HumanOutcome: &HumanOutcome{Approved: 405}}}}, "more than people approved"},
		{"nothing", &ReplayResult{Transitions: []Transition{{From: "allow", To: "deny", Count: 1}}}, "changes nothing"},
	}
	for _, c := range guardCases {
		if ok, why := p.Guard(c.r); ok || !strings.Contains(why, c.want) {
			t.Errorf("guard %s: %v %q", c.name, ok, why)
		}
	}
	// Numbers used by the text.
	keys := []string{}
	for _, n := range p.Numbers {
		keys = append(keys, n.Key)
	}
	if !slices.Equal(keys, []string{"approved", "concluded", "median_wait", "window_days"}) {
		t.Errorf("numbers %v", p.Numbers)
	}
}

func TestGenLoosenVerifyCopied(t *testing.T) {
	t.Parallel()
	src := strings.Replace(string(simSource(t, 1)), "    approval: { approvers: [role:admin], required: 1, expires_in: 4h }\n",
		"    approval: { approvers: [role:admin], required: 1, expires_in: 4h }\n    verify: L2\n", 1)
	f := newEvFixture(t, []byte(src))
	f.addOrderApprovals(40, 0, 0)
	ps, _ := GenLoosenFn(f.evidence())
	if len(ps) != 1 {
		t.Fatalf("%d proposals", len(ps))
	}
	if v := ps[0].Ops[0].(policy.InsertRuleBefore).Rule.Verify; v != policy.L2 {
		t.Fatalf("verify %q, want L2 copied from the target rule", v)
	}
}

func TestGenLoosenNone(t *testing.T) {
	t.Parallel()
	t.Run("two human denials", func(t *testing.T) {
		f := newEvFixture(t, simSource(t, 1))
		f.addOrderApprovals(401, 3, 2)
		if ps, _ := GenLoosenFn(f.evidence()); len(ps) != 0 {
			t.Fatalf("got %d proposals", len(ps))
		}
	})
	t.Run("too few decided", func(t *testing.T) {
		f := newEvFixture(t, simSource(t, 1))
		f.addOrderApprovals(29, 0, 0)
		if ps, ds := GenLoosenFn(f.evidence()); len(ps) != 0 || len(ds) != 0 {
			t.Fatalf("got %v %v", ps, ds)
		}
	})
	forced := func(f *evFixture, sql string) {
		f.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: map[string]any{"sql": sql}, approval: ApprovalApproved, wait: 60,
			rule: "approve-order-updates", decision: "require_approval"})
	}
	cases := []struct {
		name  string
		setup func(f *evFixture)
		why   string
	}{
		{"two tables and one multi-statement", func(f *evFixture) {
			for i := range 30 {
				forced(f, fmt.Sprintf("UPDATE public.orders SET status = 'x' FROM public.customers c WHERE c.id = %d", i))
			}
			forced(f, "UPDATE public.orders SET status = 'x' WHERE id = 1; UPDATE public.orders SET status = 'y' WHERE id = 2")
		}, "single statement with a WHERE clause"},
		{"two kinds", func(f *evFixture) {
			f.addOrderApprovals(30, 0, 0)
			forced(f, "INSERT INTO public.orders (id) VALUES (1)")
		}, "single statement with a WHERE clause"},
		{"two kinds with where", func(f *evFixture) {
			f.addOrderApprovals(30, 0, 0)
			forced(f, "DELETE FROM public.orders WHERE id = 7")
		}, "more than one kind"},
		{"four tables", func(f *evFixture) {
			for i := range 30 {
				forced(f, fmt.Sprintf("UPDATE public.orders SET status = 'x' FROM public.a, public.b, public.c WHERE a.id = %d", i))
			}
		}, "touch 4 tables"},
		{"pruned inputs", func(f *evFixture) {
			f.addOrderApprovals(30, 0, 0)
			f.rows[3].Input = nil
		}, "pruned"},
		{"mixed sql", func(f *evFixture) {
			f.addOrderApprovals(30, 0, 0)
			f.add(rowSpec{tool: "postgres.execute", ct: "rest", args: map[string]any{"amount": 5}, approval: ApprovalApproved,
				rule: "approve-order-updates", decision: "require_approval"})
		}, "some approved calls carried SQL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newEvFixture(t, simSource(t, 1))
			c.setup(f)
			ps, ds := GenLoosenFn(f.evidence())
			if len(ps) != 0 || len(ds) != 1 || !strings.Contains(ds[0].Reason, c.why) || ds[0].Generator != GenLoosenApprovalRate || ds[0].Target != "approve-order-updates" {
				t.Fatalf("proposals %d, dropped %+v, want reason %q", len(ps), ds, c.why)
			}
		})
	}
	t.Run("two tables only", func(t *testing.T) {
		f := newEvFixture(t, simSource(t, 1))
		for i := range 30 {
			forced(f, fmt.Sprintf("UPDATE public.orders SET status = 'x' FROM public.customers c WHERE c.id = %d", i))
		}
		ps, _ := GenLoosenFn(f.evidence())
		if len(ps) != 1 || !strings.Contains(ps[0].Ops[0].(policy.InsertRuleBefore).Rule.When, `sql.tables.all(t, t in ["public.customers", "public.orders"])`) {
			t.Fatalf("%+v", ps)
		}
	})
}

// refundAmounts are 401 approved refunds over $30.00 whose nearest-rank p95 is 4,870.
func refundAmounts() []int64 {
	var out []int64
	for i := range 380 {
		out = append(out, 3100+int64(i%14)*100) // 3,100 … 4,400
	}
	out = append(out, 4870)
	for i := range 20 {
		out = append(out, 4900+int64(i)*250) // 4,900 … 9,650
	}
	return out
}

func TestGenLoosenRefundAmounts(t *testing.T) {
	t.Parallel()
	f := newEvFixture(t, simSource(t, 2))
	amounts := refundAmounts()
	if p := percentile(amounts, 95); p != 4870 {
		t.Fatalf("fixture p95 %d", p)
	}
	for _, a := range amounts {
		f.add(rowSpec{agentID: "agt_01jbq0000000000000000000sb", agent: "support-bot", tool: "stripe.create_refund", ct: "stripe",
			args: map[string]any{"amount": a, "currency": "usd", "charge": "ch_1"}, approval: ApprovalApproved, wait: 600})
	}
	ev := f.evidence()
	if re := ev.ruleEv("refund-approval-over-30"); re == nil || re.Approvals.Approved != 401 {
		t.Fatalf("rows not under refund-approval-over-30: %+v", re)
	}
	ps, ds := GenLoosenFn(ev)
	if len(ps) != 1 {
		t.Fatalf("proposals %d dropped %v", len(ps), ds)
	}
	p := ps[0]
	op := p.Ops[0].(policy.InsertRuleBefore)
	if !strings.Contains(op.Rule.When, `has(args.currency) && args.currency == "usd"`) || !strings.HasPrefix(op.Rule.When, "has(args.amount) && type(args.amount) == int && args.amount <= ") {
		t.Errorf("when %q", op.Rule.When)
	}
	if p.Tool != "stripe.create_refund" || !strings.Contains(p.Title(), "calls to stripe.create_refund up to $45.00") {
		t.Errorf("title %q", p.Title())
	}
	limit := p.Pattern["args.amount.max"].(int64)
	covered := 0
	for _, a := range amounts {
		if a <= limit {
			covered++
		}
	}
	if c := p.Pattern["covers"].(map[string]any); c["approved"] != covered || c["of"] != 401 {
		t.Errorf("covers %v, want %d of 401", c, covered)
	}
	if !strings.Contains(p.Rationale(), fmt.Sprintf("The limit covers %s of the 401 approved amounts.", fmtCount(int64(covered)))) {
		t.Errorf("rationale %q", p.Rationale())
	}
	// S28 §3.2/§6: the p95 4,870 rounds down to a nice limit of 4,500 ("args.amount <= 4500").
	if !strings.Contains(op.Rule.When, "args.amount <= 4500") {
		t.Errorf("BUG: niceFloor(4870) gives %d, so the rule reads %q; S28 §6 expects args.amount <= 4500 (a limit of %d covers %d of 401 approved refunds over $30.00, and its replay changes nothing)",
			niceFloor(4870), op.Rule.When, limit, covered)
	}
}

func TestGenTightenMismatch(t *testing.T) {
	t.Parallel()
	f := newEvFixture(t, simSource(t, 1))
	inv := map[string]any{"sql": "UPDATE public.inventory SET reserved = reserved + 1 WHERE sku = 'A1'"}
	for range 3 {
		f.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: inv, verdict: "MISMATCH", level: "L2"})
	}
	for range 5 {
		f.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: inv, verdict: "VERIFIED", level: "L2"})
	}
	ev := f.evidence()
	if f.rows[0].RuleID != "allow-db-writes" || f.rows[0].Decision != "allow" {
		t.Fatalf("fixture rule %s %s", f.rows[0].RuleID, f.rows[0].Decision)
	}
	ps, ds := GenTightenMismatchFn(ev)
	if len(ps) != 1 || len(ds) != 0 {
		t.Fatalf("got %d (%v)", len(ps), ds)
	}
	p := ps[0]
	op := p.Ops[0].(policy.InsertRuleBefore)
	if p.Kind != KindTighten || p.TargetRuleID != "allow-db-writes" || op.RuleID != "allow-db-writes" || op.Rule.Decision != policy.RequireApproval {
		t.Fatalf("%+v %+v", p, op)
	}
	// The approvers: the policy's default approval when it names any, else one admin.
	if op.Rule.ID != "approve-db-writes" || (op.Rule.Approval == nil) != (len(ev.Base.Defaults.Approval.Approvers) > 0) {
		t.Errorf("rule %+v (defaults %+v)", op.Rule, ev.Base.Defaults.Approval)
	}
	if !strings.HasSuffix(op.Rule.When, `&& sql.kind == "update" && "public.inventory" in sql.tables`) || !strings.HasPrefix(op.Rule.When, "(") {
		t.Errorf("when %q", op.Rule.When)
	}
	if p.Title() != "Send updates to public.inventory to approval after 3 mismatches" || p.MainCount != 3 {
		t.Errorf("title %q", p.Title())
	}
	if !strings.Contains(p.Rationale(), "3 actions allowed by allow-db-writes ended MISMATCH in the last 14 days") {
		t.Errorf("rationale %q", p.Rationale())
	}
	if ok, _ := p.Guard(&ReplayResult{Summary: ReplaySummary{Changed: 8}, Transitions: []Transition{{From: "allow", To: "require_approval", Count: 8}}}); !ok {
		t.Error("guard refused allow → require_approval")
	}
	if ok, why := p.Guard(&ReplayResult{}); ok || !strings.Contains(why, "changes nothing") {
		t.Errorf("guard on nothing: %v %s", ok, why)
	}
	if ok, why := p.Guard(&ReplayResult{Summary: ReplaySummary{Changed: 1}, Transitions: []Transition{{From: "allow", To: "deny", Count: 1}}}); ok || !strings.Contains(why, "from allow to deny") {
		t.Errorf("guard on deny: %v %s", ok, why)
	}
	// Mixed tables: no narrowing, the tool alone.
	f2 := newEvFixture(t, simSource(t, 1))
	f2.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: inv, verdict: "MISMATCH"})
	f2.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: map[string]any{"sql": "INSERT INTO public.audit (id) VALUES (1)"}, verdict: "MISMATCH"})
	ps, _ = GenTightenMismatchFn(f2.evidence())
	if len(ps) != 1 || strings.Contains(ps[0].Ops[0].(policy.InsertRuleBefore).Rule.When, "in sql.tables") || ps[0].Title() != "Send calls to postgres.execute to approval after 2 mismatches" {
		t.Fatalf("mixed: %+v", ps)
	}
	// A tool name is an identifier: it keeps its case at the start of a sentence.
	if r := ps[0].RiskNote(); !strings.HasPrefix(r, "Calls to postgres.execute that allow-db-writes allows today") {
		t.Errorf("risk note %q (a tool name keeps its case)", r)
	}
	if r := ps[0].Ops[0].(policy.InsertRuleBefore).Rule.Reason; !strings.HasPrefix(r, "Calls to postgres.execute wait for a person") {
		t.Errorf("rule reason %q (a tool name keeps its case)", r)
	}
}

func TestGenTightenVerifyLevel(t *testing.T) {
	t.Parallel()
	f := newEvFixture(t, simSource(t, 1))
	inv := map[string]any{"sql": "UPDATE public.inventory SET reserved = reserved + 1 WHERE sku = 'A1'"}
	for i := range 40 {
		lvl := "L2"
		if i < 2 {
			lvl = "L1"
		}
		f.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: inv, verdict: "VERIFIED", level: lvl})
	}
	ps, _ := GenTightenVerifyLevelFn(f.evidence())
	if len(ps) != 1 {
		t.Fatalf("%d", len(ps))
	}
	op, ok := ps[0].Ops[0].(policy.SetRuleField)
	if !ok || op.RuleID != "allow-db-writes" || op.Key != "verify" || op.Value != policy.L2 {
		t.Fatalf("%#v", ps[0].Ops)
	}
	if ps[0].Title() != "Ask allow-db-writes for L2 Witnessed" || !strings.Contains(ps[0].Rationale(), "38 of 40 writes allowed by allow-db-writes in the last 14 days were verified at L2 or above (95%)") {
		t.Errorf("%q %q", ps[0].Title(), ps[0].Rationale())
	}
}

// refundBudgetCounters are 7 days of refunds-per-day use (limit $4,000.00): three at 92–97%.
func refundBudgetCounters() []DayCounter {
	used := []int64{200000, 368000, 210000, 380000, 190000, 388000, 205000}
	var out []DayCounter
	for i, u := range used {
		out = append(out, DayCounter{Scope: policy.ScopeOrg, Counter: policy.BudgetCounter("refunds-per-day"), Day: time.Date(2026, 10, 2+i, 0, 0, 0, 0, time.UTC), Value: u})
	}
	return out
}

func TestGenBudget(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T, eps []EpisodeRef) []Proposal {
		f := newEvFixture(t, simSource(t, 1))
		f.add(rowSpec{agentID: "agt_sb", agent: "support-bot", tool: "stripe.create_refund", ct: "stripe", args: map[string]any{"amount": 2400, "currency": "usd"}})
		f.eps = eps
		ev := f.evidence()
		ev.Budgets = BudgetUses(refundBudgetCounters(), nil, f.base, time.UTC)
		ps, _ := GenBudgetFn(ev)
		return ps
	}
	ps := build(t, nil)
	if len(ps) != 1 {
		t.Fatalf("%d proposals", len(ps))
	}
	p := ps[0]
	op, ok := p.Ops[0].(policy.SetBudgetLimit)
	if p.Kind != KindBudget || !ok || op.BudgetID != "refunds-per-day" || op.MaxSpend == nil || op.MaxSpend.Amount != 500000 || op.MaxSpend.Currency != "usd" {
		t.Fatalf("%+v %#v", p, p.Ops)
	}
	// nice_ceil(p95 388,000 × 1.2 = 465,600) = 500,000.
	if p.Title() != "Raise refunds-per-day from $4,000.00 to $5,000.00" {
		t.Errorf("title %q", p.Title())
	}
	if want := "refunds-per-day reached 90% of its limit on 3 of the last 7 days, 97% at most. The busiest day used $3,880.00 of $4,000.00."; p.Rationale() != want {
		t.Errorf("rationale %q", p.Rationale())
	}
	// A volume_spike on a near-limit day: advice instead of a change.
	spike := []EpisodeRef{{ID: "anm_1", AgentID: "agt_sb", Kind: "volume_spike", FirstSeen: time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC), LastSeen: time.Date(2026, 10, 7, 16, 0, 0, 0, time.UTC)}}
	ps = build(t, spike)
	if len(ps) != 1 || ps[0].Kind != KindAdvisory || ps[0].Generator != GenBudgetNearLimit || len(ps[0].Ops) != 0 {
		t.Fatalf("with a spike: %+v", ps)
	}
	if !strings.Contains(ps[0].Rationale(), "Usage rose with an anomaly; check it before raising the budget.") || ps[0].Title() != "Check support-bot before raising refunds-per-day" {
		t.Errorf("advisory %q %q", ps[0].Title(), ps[0].Rationale())
	}
	// A spike on a quiet day, or another kind, changes nothing.
	quiet := []EpisodeRef{{ID: "anm_2", AgentID: "agt_sb", Kind: "volume_spike", FirstSeen: time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC), LastSeen: time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC)},
		{ID: "anm_3", AgentID: "agt_sb", Kind: "new_tool", FirstSeen: time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC), LastSeen: time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC)}}
	if ps = build(t, quiet); len(ps) != 1 || ps[0].Kind != KindBudget {
		t.Fatalf("quiet spike: %+v", ps)
	}
	// Two near days only: nothing.
	f := newEvFixture(t, simSource(t, 1))
	ev := f.evidence()
	c := refundBudgetCounters()
	c[5].Value = 100000
	ev.Budgets = BudgetUses(c, nil, f.base, time.UTC)
	if ps, _ := GenBudgetFn(ev); len(ps) != 0 {
		t.Fatalf("two near days: %d", len(ps))
	}
	// An agent budget: advice with the command, never a patch.
	ev.Budgets = BudgetUses([]DayCounter{
		{Scope: "agt_sb", Counter: policy.CounterCalls, Day: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), Value: 95},
		{Scope: "agt_sb", Counter: policy.CounterCalls, Day: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC), Value: 96},
		{Scope: "agt_sb", Counter: policy.CounterCalls, Day: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), Value: 99},
	}, []AgentBudgetRow{{ID: "agt_sb", Name: "support-bot", Budgets: json.RawMessage(`{"calls_per_day":100}`)}}, nil, time.UTC)
	ps, _ = GenBudgetFn(ev)
	if len(ps) != 1 || ps[0].Kind != KindAdvisory || len(ps[0].Ops) != 0 || !strings.Contains(ps[0].RiskNote(), `PATCH /v1/agents/agt_sb and {"budgets": {"calls_per_day": 200}}`) {
		t.Fatalf("agent budget: %+v", ps)
	}
}

func TestGenExplicitRule(t *testing.T) {
	t.Parallel()
	f := newEvFixture(t, simSource(t, 1))
	for i := range 14 {
		f.add(rowSpec{at: sugNow.Add(-time.Duration(1+i%3) * 24 * time.Hour).Add(-time.Duration(i) * time.Minute), tool: "github.merge_pr", ct: "github", args: map[string]any{"pr": i}})
	}
	ev := f.evidence()
	if f.rows[0].Source != policy.SourceDefault {
		t.Fatalf("fixture source %q", f.rows[0].Source)
	}
	ps, _ := GenExplicitRuleFn(ev)
	if len(ps) != 1 {
		t.Fatalf("%d", len(ps))
	}
	p := ps[0]
	op, ok := p.Ops[0].(policy.AppendRule)
	if !ok || p.Kind != KindExplicitRule || op.Rule.Decision != policy.Deny || op.Rule.ID != "explicit-github-merge-pr" || !strings.Contains(op.Rule.Reason, "github.merge_pr isn't allowed") {
		t.Fatalf("%+v", p)
	}
	if p.Title() != "Name github.merge_pr in the policy instead of leaving it to the default" ||
		!strings.Contains(p.Rationale(), "The default decided github.merge_pr 14 times on 3 of the last 14 days.") {
		t.Errorf("%q %q", p.Title(), p.Rationale())
	}
	if ok, _ := p.Guard(&ReplayResult{Summary: ReplaySummary{Evaluated: 14, RuleChanged: 14}}); !ok {
		t.Error("guard refused a 0-change replay")
	}
	if ok, why := p.Guard(&ReplayResult{Summary: ReplaySummary{Changed: 1}}); ok || !strings.Contains(why, "changes decisions") {
		t.Errorf("guard on a change: %v %s", ok, why)
	}
	// 14 hits on one day: nothing.
	f2 := newEvFixture(t, simSource(t, 1))
	for i := range 14 {
		f2.add(rowSpec{at: sugNow.Add(-2 * time.Hour).Add(-time.Duration(i) * time.Minute), tool: "github.merge_pr", ct: "github"})
	}
	if ps, _ := GenExplicitRuleFn(f2.evidence()); len(ps) != 0 {
		t.Errorf("one day: %d", len(ps))
	}
}

func TestGenDenyHeavy(t *testing.T) {
	t.Parallel()
	f := newEvFixture(t, simSource(t, 1))
	for i := range 180 {
		amt := 2400
		if i < 74 {
			amt = 20000 // over $100.00: refund-limit denies it
		}
		f.add(rowSpec{at: sugNow.Add(-time.Duration(i) * 30 * time.Minute), agentID: "agt_sb", agent: "support-bot", tool: "stripe.create_refund", ct: "stripe",
			args: map[string]any{"amount": amt, "currency": "usd"}})
	}
	ps, _ := GenDenyHeavyFn(f.evidence())
	if len(ps) != 1 {
		t.Fatalf("%d", len(ps))
	}
	p := ps[0]
	if p.Kind != KindAdvisory || p.Generator != GenAdvisoryDenyHeavy || len(p.Ops) != 0 {
		t.Fatalf("%+v", p)
	}
	if p.Title() != "support-bot is denied 41.1% of the time" ||
		p.Rationale() != "support-bot was denied 74 of 180 times in the last 7 days, mostly by refund-limit. Its instructions may ask for things the policy forbids." {
		t.Errorf("%q %q", p.Title(), p.Rationale())
	}
	// 49 actions: under the floor.
	f2 := newEvFixture(t, simSource(t, 1))
	for i := range 49 {
		f2.add(rowSpec{agentID: "agt_sb", agent: "support-bot", tool: "github.merge_pr", ct: "github", args: map[string]any{"pr": i}})
	}
	if ps, _ := GenDenyHeavyFn(f2.evidence()); len(ps) != 0 {
		t.Errorf("49 actions: %d", len(ps))
	}
	// 50 default denies: "the default".
	f2.add(rowSpec{agentID: "agt_sb", agent: "support-bot", tool: "github.merge_pr", ct: "github"})
	if ps, _ := GenDenyHeavyFn(f2.evidence()); len(ps) != 1 || !strings.Contains(ps[0].Rationale(), "mostly by the default") {
		t.Errorf("default: %+v", ps)
	}
}

func TestGeneratorsRegistry(t *testing.T) {
	t.Parallel()
	var names []string
	for _, g := range Generators {
		names = append(names, g.Name)
	}
	want := []string{GenTightenMismatch, GenTightenVerifyLevel, GenLoosenApprovalRate, GenBudgetNearLimit, GenExplicitDefaultHit, GenAdvisoryDenyHeavy}
	if !slices.Equal(names, want) {
		t.Fatalf("%v", names)
	}
	for _, k := range []string{KindTighten, KindLoosen, KindBudget, KindExplicitRule, KindAdvisory, "other"} {
		_ = kindRank(k)
	}
	if kindRank(KindTighten) >= kindRank(KindLoosen) || kindRank(KindLoosen) >= kindRank(KindAdvisory) {
		t.Fatal("tighten ranks before loosen before advice")
	}
}

func TestNiceFloorCeil(t *testing.T) {
	t.Parallel()
	// Multiples of 5 × 10^(d−2) (S28 §6: p95 4,870 → 4,500).
	floor := []struct{ in, want int64 }{{-5, 0}, {0, 0}, {1, 1}, {2, 2}, {3, 3}, {9, 9}, {24, 20}, {25, 25}, {26, 25}, {49, 45}, {50, 50}, {99, 95},
		{100, 100}, {1260, 1000}, {4870, 4500}, {5000, 5000}, {9999, 9500}, {1_234_567, 1_000_000}}
	for _, c := range floor {
		if got := niceFloor(c.in); got != c.want {
			t.Errorf("niceFloor(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	ceil := []struct{ in, want int64 }{{-1, 1}, {0, 1}, {1, 1}, {2, 2}, {3, 5}, {11, 20}, {21, 25}, {26, 50}, {101, 200}, {465_600, 500_000}, {2_000_001, 2_500_000}}
	for _, c := range ceil {
		if got := niceCeil(c.in); got != c.want {
			t.Errorf("niceCeil(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFmtWaitAndPercent(t *testing.T) {
	t.Parallel()
	waits := []struct {
		s    float64
		want string
	}{{0, "0 seconds"}, {1, "1 second"}, {45, "45 seconds"}, {89.4, "89 seconds"}, {90, "2 minutes"}, {1260, "21 minutes"},
		{5399, "90 minutes"}, {5400, "2 hours"}, {3600 * 36, "2 days"}, {3600*36 - 1, "36 hours"}, {86400 * 3, "3 days"}}
	for _, c := range waits {
		if got := fmtWait(c.s); got != c.want {
			t.Errorf("fmtWait(%v) = %q, want %q", c.s, got, c.want)
		}
	}
	pcts := []struct {
		r    float64
		want string
	}{{0, "0%"}, {0.95, "95%"}, {0.973, "97.3%"}, {1, "100%"}, {74.0 / 180, "41.1%"}, {0.9999, "100%"}, {0.0004, "0%"}}
	for _, c := range pcts {
		if got := fmtPercent(c.r); got != c.want {
			t.Errorf("fmtPercent(%v) = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestRefsPlaceholders(t *testing.T) {
	t.Parallel()
	r := NewRefs()
	if got := r.toolRef("postgres.execute"); got != "postgres.execute" {
		t.Errorf("product tool %q", got)
	}
	if got := r.toolRef("stripe.create_refund"); got != "stripe.create_refund" {
		t.Errorf("product tool %q", got)
	}
	if got := r.toolRef("acme.crm_sync"); got != "{tool_1}" {
		t.Errorf("org tool %q", got)
	}
	if got := r.toolRef("nodot"); got != "{tool_2}" {
		t.Errorf("org tool %q", got)
	}
	if got := r.toolRef("acme.crm_sync"); got != "{tool_1}" {
		t.Errorf("same tool, same placeholder: %q", got)
	}
	a := r.Ref("agent", "ops-agent")
	if a != "{agent_1}" || r.Ref("agent", "ops-agent") != a || r.Ref("agent", "support-bot") != "{agent_2}" || r.Ref("rule", "ops-agent") != "{rule_1}" {
		t.Fatalf("refs %v", r.Values)
	}
	if got := r.agentsRef([]string{"ops-agent", "support-bot"}); got != "{agent_1} and {agent_2}" {
		t.Errorf("agents %q", got)
	}
	if got := r.Resolve("{agent_1} ran {tool_1} on {table_9}"); got != "ops-agent ran acme.crm_sync on {table_9}" {
		t.Errorf("resolve %q", got)
	}
	var nilRefs *Refs
	if nilRefs.Resolve("{agent_1}") != "{agent_1}" {
		t.Error("nil refs")
	}
	if r.Kinds["{tool_1}"] != "tool" || r.Kinds["{agent_2}"] != "agent" {
		t.Errorf("kinds %v", r.Kinds)
	}
}

func TestFingerprintStability(t *testing.T) {
	t.Parallel()
	p1 := map[string]any{"tool": "postgres.execute", "sql.kind": "update", "sql.tables": []string{"public.orders"}, "sql.statements": 1, "agents": []string{"ops-agent"}}
	p2 := map[string]any{}
	for _, k := range []string{"agents", "sql.statements", "sql.tables", "sql.kind", "tool"} {
		p2[k] = p1[k]
	}
	p3 := map[string]any{"sql.statements": int64(1), "agents": []any{"ops-agent"}, "tool": "postgres.execute", "sql.tables": []any{"public.orders"}, "sql.kind": "update"}
	a := Fingerprint(GenLoosenApprovalRate, "pol_1", "approve-order-updates", p1)
	if !strings.HasPrefix(a, "sha256:") {
		t.Fatalf("fingerprint %q", a)
	}
	for i := range 50 {
		if b := Fingerprint(GenLoosenApprovalRate, "pol_1", "approve-order-updates", p2); b != a {
			t.Fatalf("run %d: %s != %s", i, b, a)
		}
	}
	if Fingerprint(GenLoosenApprovalRate, "pol_1", "approve-order-updates", p3) != a {
		t.Fatal("int vs int64 and []string vs []any changed the fingerprint")
	}
	diff := []string{
		Fingerprint(GenTightenMismatch, "pol_1", "approve-order-updates", p1),
		Fingerprint(GenLoosenApprovalRate, "pol_2", "approve-order-updates", p1),
		Fingerprint(GenLoosenApprovalRate, "pol_1", "other", p1),
		Fingerprint(GenLoosenApprovalRate, "pol_1", "approve-order-updates", map[string]any{"tool": "postgres.execute"}),
	}
	for i, d := range diff {
		if d == a {
			t.Errorf("variant %d has the same fingerprint", i)
		}
	}
	// The generator's own pattern is stable across two builds of the same evidence.
	mk := func() string {
		f := newEvFixture(t, simSource(t, 1))
		f.addOrderApprovals(31, 0, 0)
		ps, _ := GenLoosenFn(f.evidence())
		return Fingerprint(ps[0].Generator, "pol_1", ps[0].TargetRuleID, ps[0].Pattern)
	}
	if mk() != mk() {
		t.Fatal("same evidence, different fingerprint")
	}
	if jsonSafe(func() {}) != nil {
		t.Error("jsonSafe of an unmarshalable value")
	}
	if normalizeNumbers(json.Number("1.5")) != "1.5" {
		t.Error("a fraction stays a string")
	}
}

func TestEvidenceHelpers(t *testing.T) {
	t.Parallel()
	if median(nil) != 0 || median([]float64{3, 1, 2}) != 2 || median([]float64{4, 1, 3, 2}) != 2.5 {
		t.Error("median")
	}
	if percentile(nil, 95) != 0 || percentile([]int64{5}, 95) != 5 || percentile([]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 95) != 10 || percentile([]int64{1, 2, 3, 4}, 50) != 2 {
		t.Error("percentile")
	}
	for _, raw := range []string{"", "null", "{", `{"v":2}`, `{"v":1}`} {
		if deriveFacts(json.RawMessage(raw)) != nil {
			t.Errorf("deriveFacts(%q) not nil", raw)
		}
	}
	f := deriveFacts(policyInput(t, sugNow, "a", "b", "stripe.create_refund", "stripe", "write", map[string]any{"amount": -5, "currency": "USD"}))
	if f == nil || f.Amount != nil || f.Currency != "" || f.SQL != nil {
		t.Errorf("negative amount, upper-case currency: %+v", f)
	}
	f = deriveFacts(policyInput(t, sugNow, "a", "b", "stripe.create_refund", "stripe", "write", map[string]any{"amount": 1.5, "currency": "usd"}))
	if f == nil || f.Amount != nil || f.Currency != "usd" {
		t.Errorf("fractional amount: %+v", f)
	}
	ev := newEvFixture(t, simSource(t, 1)).evidence()
	if ev.days() != 14 || ev.rule("nope") != nil || ev.ruleEv("nope") != nil || ruleEvidence(nil, nil, "") != nil {
		t.Error("helpers")
	}
	if kindNoun("insert") != "inserts" || kindNoun("delete") != "deletes" || kindNoun("select") != "reads" || kindNoun("merge") != "merge statements" {
		t.Error("kindNoun")
	}
	if kindVerb("insert") != "insert into" || kindVerb("delete") != "delete from" || kindVerb("merge") != "run merge statements on" {
		t.Error("kindVerb")
	}
	if idPart("Acme.CRM__Sync!") != "acme-crm-sync" || clipRuleID(strings.Repeat("a-", 40)) != strings.TrimRight(strings.Repeat("a-", 28), "-") {
		t.Error("ids")
	}
	if upperFirst("") != "" || upperFirst("{table_1}") != "{table_1}" || upperFirst("updates") != "Updates" {
		t.Error("upperFirst")
	}
	if shortDigest("x") != "x" || shortDigest("sha256:7a1c0000e9d0") != "sha256:7a1c…e9d0" {
		t.Error("shortDigest")
	}
}

func TestSuggestCopy(t *testing.T) {
	t.Parallel()
	if got := SuggestionStaleMessage("acme-sim", 2, 1); got != "acme-sim v2 has been enforced since this suggestion was written for v1. Generate suggestions again: POST /v1/suggestions/generate." {
		t.Error(got)
	}
	if got := ShadowSlotTakenMessage("acme-sim", 2); !strings.HasPrefix(got, "acme-sim v2 is already running in shadow.") {
		t.Error(got)
	}
	at := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	s := &Suggestion{Status: SugAccepted, DecidedAt: &at, Policy: SuggestionPolicy{Name: "acme-sim"}, ResultingVersion: &VersionBrief{Version: 3}}
	if got := SuggestionDecidedMessage(s, "priya@acme.com"); got != "This suggestion was already accepted on 2026-10-09 by priya@acme.com. It created acme-sim v3." {
		t.Error(got)
	}
	for st, want := range map[string]string{SugRejected: "already rejected", SugSuperseded: "superseded", SugAccepting: "right now", SugOpen: "already decided"} {
		if got := SuggestionDecidedMessage(&Suggestion{Status: st}, ""); !strings.Contains(got, want) {
			t.Errorf("%s: %q", st, got)
		}
	}
	two := 2
	if AcceptedLine("acme-sim", 3, nil) != "acme-sim v3 is running in shadow" || AcceptedLine("acme-sim", 3, &two) != "acme-sim v3 is running in shadow (replaced v2)" {
		t.Error("AcceptedLine")
	}
	if GenerateLine(1, 0) != "1 suggestion" || GenerateLine(3, 1) != "3 suggestions · 1 dropped" {
		t.Error("GenerateLine")
	}
	mon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	if WeekLabel(2026, 41, mon) != "week 41 (5 to 11 Oct 2026)" || WeekLabel(2026, 40, time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)) != "week 40 (28 Sep to 4 Oct 2026)" {
		t.Error(WeekLabel(2026, 40, time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)))
	}
}
