package learn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

var bg = context.Background()

// memSource is an InputSource over a slice, already in (evaluated_at, id) order.
type memSource struct {
	mu        sync.Mutex // runners share one source across goroutines
	rows      []InputRow
	count     int64 // when > 0, Count answers this (the limit test)
	scanned   int
	failAfter int // fail Scan after this many calls (0: never)
	calls     int
}

func (m *memSource) Count(_ context.Context, _ ids.OrgID, w Window) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.count > 0 {
		return m.count, nil
	}
	var n int64
	for _, r := range m.rows {
		if !r.DecidedAt.Before(w.From) && r.DecidedAt.Before(w.To) {
			n++
		}
	}
	return n, nil
}

func (m *memSource) Scan(_ context.Context, _ ids.OrgID, w Window, after Cursor, limit int) ([]InputRow, Cursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.failAfter > 0 && m.calls > m.failAfter {
		return nil, Cursor{}, errors.New("scan failed")
	}
	var out []InputRow
	for _, r := range m.rows {
		if r.DecidedAt.Before(w.From) || !r.DecidedAt.Before(w.To) {
			continue
		}
		if !after.At.IsZero() && (r.DecidedAt.Before(after.At) || r.DecidedAt.Equal(after.At) && r.ActionID <= after.ID) {
			continue
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	m.scanned += len(out)
	if len(out) == 0 {
		return nil, after, nil
	}
	last := out[len(out)-1]
	return out, Cursor{At: last.DecidedAt, ID: last.ActionID}, nil
}

const replayV1 = `deedglass: policy/v1
name: replay-test
defaults: { decision: deny, reason: No rule allows this action. }
rules:
  - id: refund-limit
    match: { tool: stripe.create_refund }
    when: args.amount > 7000
    decision: deny
    reason: Refunds over $70.00 need a person outside Deedglass.
  - id: refunds
    match: { tool: stripe.create_refund }
    decision: allow
  - id: approve-order-updates
    match: { tool: postgres.execute }
    decision: require_approval
    reason: Order updates wait for a person.
  - id: reads
    match: { tool: postgres.query }
    decision: allow
`

const replayV3 = `deedglass: policy/v1
name: replay-test
defaults: { decision: deny, reason: No rule allows this action. }
rules:
  - id: refund-limit-strict
    match: { tool: stripe.create_refund }
    when: args.amount > 5000
    decision: deny
    reason: Refunds over $50.00 need a person outside Deedglass.
  - id: refunds
    match: { tool: stripe.create_refund }
    decision: allow
  - id: allow-order-status-updates
    match: { tool: postgres.execute }
    when: '!anomaly.active'
    decision: allow
  - id: reads-v3
    match: { tool: postgres.query }
    decision: allow
`

func compile(t testing.TB, src string) *policy.Compiled {
	t.Helper()
	c, diags := policy.Compile([]byte(src), policy.Options{})
	if c == nil {
		t.Fatalf("compile: %v", diags)
	}
	return c
}

var t0 = time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)

// fixture builds n recorded actions decided under v1, with a mix of tools, approvals and unknowns.
func fixture(t testing.TB, n int) []InputRow {
	t.Helper()
	v1 := compile(t, replayV1)
	tools := []string{"stripe.create_refund", "postgres.execute", "postgres.query"}
	outcomes := []ApprovalOutcome{ApprovalApproved, ApprovalApproved, ApprovalApproved, ApprovalExpired, ApprovalDenied, ApprovalPending}
	rows := make([]InputRow, 0, n)
	for i := range n {
		tool := tools[i%3]
		at := t0.Add(time.Duration(i) * time.Minute)
		args := map[string]any{}
		if tool == "stripe.create_refund" {
			args["amount"] = 1000 + (i*137)%9000
			args["currency"] = "usd"
		} else {
			args["sql"] = "SELECT 1"
		}
		ab, _ := json.Marshal(args)
		in := policy.Input{V: policy.InputVersion, Org: policy.OrgInput{OrgFacts: policy.OrgFacts{ID: "org_x", Timezone: "UTC"}},
			Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{ID: "agt_x", Name: "ops-agent", Status: policy.StatusActive}},
			Tool:  policy.ToolInput{ToolFacts: policy.ToolFacts{Name: tool, Connector: "x", ConnectorType: "stripe", SideEffect: "write"}},
			Args:  ab, Time: at}
		if i%17 == 0 {
			in.Anomaly = policy.AnomalyFacts{Active: true, Kinds: []string{"volume_spike"}, Severity: "warn"}
		}
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		// The replay's own backstop, not the 50 ms default: a loaded runner must not record a
		// deadline deny that the replay (2 s) then reports as changed.
		d := policy.Evaluate(bg, v1, in, policy.EvalOptions{Deadline: 2 * time.Second})
		r := InputRow{ActionID: fmt.Sprintf("act_%04d", i), DecidedAt: at, Tool: tool, Input: raw,
			Recorded: RecordedDecision{Decision: string(d.Outcome), Code: string(d.Code), Source: d.Source, RuleID: d.RuleID, PolicyVersionID: "pv_1"}}
		if d.Outcome == policy.RequireApproval {
			r.Approval = outcomes[(i/3)%len(outcomes)]
		}
		switch {
		case i%101 == 0:
			r.Input, r.Pruned = nil, true
		case i%103 == 0:
			r.Input = nil
		case i%107 == 0:
			r.Input = json.RawMessage(`{"v":2,"time":"2026-10-02T03:00:00Z"}`)
		}
		rows = append(rows, r)
	}
	return rows
}

func window() Window { return Window{From: t0, To: t0.Add(30 * 24 * time.Hour)} }

func TestReplayOracleAndInvariants(t *testing.T) {
	rows := fixture(t, 1200)
	v3 := compile(t, replayV3)
	e := &Engine{Src: &memSource{rows: rows}, Batch: 100, Workers: 4}
	res, err := e.Run(bg, ids.OrgID{}, Candidate{Compiled: v3, VersionID: "pv_3", Version: 3}, Baseline{}, window(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Summary
	if s.Actions != int64(len(rows)) || s.Actions != s.Evaluated+s.Unknown || s.Evaluated != s.Unchanged+s.Changed+s.RuleChanged {
		t.Fatalf("invariants: %+v", s)
	}
	// The oracle: evaluate every row here and count transitions independently.
	want := map[[2]string]int64{}
	var unknown int64
	for _, r := range rows {
		var in policy.Input
		if r.Input == nil || json.Unmarshal(r.Input, &in) != nil || in.V != policy.InputVersion {
			unknown++
			continue
		}
		d := policy.Evaluate(bg, v3, in, policy.EvalOptions{Mode: policy.ModeShadow})
		if string(d.Outcome) != r.Recorded.Decision {
			want[[2]string{r.Recorded.Decision, string(d.Outcome)}]++
		}
	}
	got := map[[2]string]int64{}
	for _, tr := range res.Transitions {
		got[[2]string{tr.From, tr.To}] = tr.Count
		var byRule, byTool int64
		for _, c := range tr.ByRule {
			byRule += c.Count
		}
		for _, c := range tr.ByTool {
			byTool += c.Count
		}
		if byRule != tr.Count || byTool != tr.Count || len(tr.Samples) > MaxSamples {
			t.Errorf("%s→%s: by_rule %d by_tool %d count %d samples %d", tr.From, tr.To, byRule, byTool, tr.Count, len(tr.Samples))
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) || unknown != s.Unknown {
		t.Fatalf("transitions %v want %v; unknown %d want %d", got, want, s.Unknown, unknown)
	}
	// require_approval → allow carries what people did, and anomaly.active replays as recorded.
	var ra *Transition
	for i := range res.Transitions {
		if res.Transitions[i].From == "require_approval" && res.Transitions[i].To == "allow" {
			ra = &res.Transitions[i]
		}
	}
	if ra == nil || ra.HumanOutcome == nil {
		t.Fatalf("no require_approval → allow transition: %+v", res.Transitions)
	}
	h := ra.HumanOutcome
	if h.Approved+h.Denied+h.Expired+h.Cancelled+h.Pending != ra.Count || h.Approved == 0 || h.Expired == 0 {
		t.Fatalf("human outcome %+v for %d", h, ra.Count)
	}
	// Rows with anomaly.active stayed require_approval under v3 (its allow rule reads !anomaly.active).
	for _, r := range rows {
		if r.Tool == "postgres.execute" && slices.Contains(ra.Samples, r.ActionID) {
			var in policy.Input
			_ = json.Unmarshal(r.Input, &in)
			if in.Anomaly.Active {
				t.Fatalf("%s was active at decision time but replayed as allow", r.ActionID)
			}
		}
	}
	if res.Unknown.InputPruned == 0 || res.Unknown.InputMissing == 0 || res.Unknown.InputUnsupported == 0 {
		t.Fatalf("unknowns %+v", res.Unknown)
	}
	if res.SQLGuard.Current == "" || res.Candidate.VersionHash != v3.VersionHash {
		t.Fatalf("result header %+v %+v", res.SQLGuard, res.Candidate)
	}
}

func TestReplayDeterminism(t *testing.T) {
	rows := fixture(t, 900)
	v3 := compile(t, replayV3)
	var digests []string
	for _, cfg := range []struct{ batch, workers int }{{1, 1}, {7, 8}, {1000, 8}, {1000, 1}, {64, 3}} {
		e := &Engine{Src: &memSource{rows: rows}, Batch: cfg.batch, Workers: cfg.workers}
		res, err := e.Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{}, window(), nil)
		if err != nil {
			t.Fatal(err)
		}
		again, err := ResultDigest(res)
		if err != nil || again != res.ResultDigest {
			t.Fatalf("digest not re-derivable: %s %s %v", again, res.ResultDigest, err)
		}
		digests = append(digests, res.ResultDigest)
	}
	for _, d := range digests[1:] {
		if d != digests[0] {
			t.Fatalf("digests differ: %v", digests)
		}
	}
}

func TestReplaySelfAndVersionBaseline(t *testing.T) {
	rows := fixture(t, 600)
	v1 := compile(t, replayV1)
	e := &Engine{Src: &memSource{rows: rows}}
	res, err := e.Run(bg, ids.OrgID{}, Candidate{Compiled: v1}, Baseline{Type: BaselineRecorded}, window(), nil)
	if err != nil || res.Summary.Changed != 0 || res.Summary.RuleChanged != 0 {
		t.Fatalf("recorded version vs itself: %v %+v", err, res.Summary)
	}
	v3 := compile(t, replayV3)
	res, err = e.Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{Type: BaselineVersion, Compiled: v3, Version: 3}, window(), nil)
	if err != nil || res.Summary.Changed != 0 || res.Summary.RuleChanged != 0 || res.Baseline.VersionHash != v3.VersionHash {
		t.Fatalf("version baseline = candidate: %v %+v", err, res.Summary)
	}
	// v1 vs v3 as a version baseline: the same transitions as the recorded baseline (recorded = v1).
	a, _ := e.Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{Type: BaselineVersion, Compiled: v1}, window(), nil)
	b, _ := e.Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{}, window(), nil)
	if a.Summary.Changed != b.Summary.Changed || a.Summary.Changed == 0 {
		t.Fatalf("version baseline v1 %+v vs recorded %+v", a.Summary, b.Summary)
	}
	if _, err := e.Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{Type: BaselineVersion}, window(), nil); err == nil {
		t.Fatal("a version baseline without a policy must fail")
	}
}

func TestReplayRuleChangedAndBuiltin(t *testing.T) {
	v1 := compile(t, replayV1)
	in := policy.Input{V: policy.InputVersion, Org: policy.OrgInput{OrgFacts: policy.OrgFacts{Timezone: "UTC"}},
		Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{Name: "a", Status: policy.StatusActive}},
		Tool:  policy.ToolInput{ToolFacts: policy.ToolFacts{Name: "postgres.query"}}, Args: json.RawMessage(`{}`), Time: t0}
	raw, _ := json.Marshal(in)
	unknownTool := in
	unknownTool.Tool.Name = "nope.thing"
	raw2, _ := json.Marshal(unknownTool)
	rows := []InputRow{
		{ActionID: "act_1", DecidedAt: t0, Tool: "postgres.query", Input: raw, Recorded: RecordedDecision{Decision: "allow", RuleID: "reads"}},
		{ActionID: "act_2", DecidedAt: t0.Add(time.Second), Tool: "nope.thing", Input: raw2,
			Recorded: RecordedDecision{Decision: "deny", Code: "tool_unknown", Source: policy.SourceBuiltin, RuleID: "tool-unknown"}},
	}
	v3 := compile(t, replayV3)
	res, err := (&Engine{Src: &memSource{rows: rows}}).Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{}, window(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Summary.RuleChanged != 1 || res.Summary.Unchanged != 1 || len(res.RuleChanges) != 1 ||
		res.RuleChanges[0].FromRule != "reads" || res.RuleChanges[0].ToRule != "reads-v3" {
		t.Fatalf("%+v %+v", res.Summary, res.RuleChanges)
	}
	_ = v1
}

func TestReplayLimits(t *testing.T) {
	v3 := compile(t, replayV3)
	e := &Engine{Src: &memSource{count: MaxReplayActions + 1}}
	_, err := e.Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{}, window(), nil)
	var tl *TooLargeError
	if !errors.As(err, &tl) || !errors.Is(err, ErrReplayTooLarge) || tl.Actions != MaxReplayActions+1 {
		t.Fatalf("too large: %v", err)
	}
	for _, w := range []Window{{From: t0, To: t0.Add(91 * 24 * time.Hour)}, {From: t0, To: t0}, {From: t0.Add(time.Hour), To: t0}, {}} {
		if _, err := e.Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{}, w, nil); !errors.Is(err, ErrWindowInvalid) {
			t.Fatalf("window %v: %v", w, err)
		}
	}
	if _, err := e.Run(bg, ids.OrgID{}, Candidate{}, Baseline{}, window(), nil); err == nil {
		t.Fatal("no candidate must fail")
	}
	// A scan error surfaces; a cancelled context stops between batches.
	src := &memSource{rows: fixture(t, 50), failAfter: 1}
	if _, err := (&Engine{Src: src, Batch: 10}).Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{}, window(), nil); err == nil {
		t.Fatal("scan error must fail the run")
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, err := (&Engine{Src: &memSource{rows: fixture(t, 50)}}).Run(ctx, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{}, window(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestReplayProgress(t *testing.T) {
	v3 := compile(t, replayV3)
	var calls []int64
	_, err := (&Engine{Src: &memSource{rows: fixture(t, 250)}, Batch: 100}).Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{}, window(),
		func(p, total int64) { calls = append(calls, p); _ = total })
	if err != nil || fmt.Sprint(calls) != "[100 200 250]" {
		t.Fatalf("%v %v", calls, err)
	}
}

// TestReplayOrdering: equal counts order by decision words, then rules; every unknown reason is
// counted; the errors read well.
func TestReplayOrdering(t *testing.T) {
	src := `deedglass: policy/v1
name: order-test
defaults: { decision: deny, reason: No rule allows this action. }
rules:
  - id: a-allow
    match: { tool: a.x }
    decision: allow
  - id: b-approve
    match: { tool: b.x }
    decision: require_approval
    reason: People decide b.
  - id: c-allow-2
    match: { tool: c.x }
    decision: allow
  - id: d-allow-3
    match: { tool: d.x }
    decision: allow
`
	cand := compile(t, src)
	mk := func(i int, tool, dec, rule string) InputRow {
		in := policy.Input{V: policy.InputVersion, Org: policy.OrgInput{OrgFacts: policy.OrgFacts{Timezone: "UTC"}},
			Agent: policy.AgentInput{AgentFacts: policy.AgentFacts{Name: "a", Status: policy.StatusActive}},
			Tool:  policy.ToolInput{ToolFacts: policy.ToolFacts{Name: tool}}, Args: json.RawMessage(`{}`), Time: t0}
		raw, _ := json.Marshal(in)
		return InputRow{ActionID: fmt.Sprintf("act_%03d", i), DecidedAt: t0.Add(time.Duration(i) * time.Second), Tool: tool, Input: raw,
			Recorded: RecordedDecision{Decision: dec, RuleID: rule}}
	}
	rows := []InputRow{
		mk(1, "a.x", "deny", ""),               // deny → allow
		mk(2, "b.x", "deny", ""),               // deny → require_approval
		mk(3, "c.x", "allow", "old-c"),         // rule changed
		mk(4, "d.x", "allow", "old-d"),         // rule changed
		mk(5, "a.x", "require_approval", "r1"), // require_approval → allow, not approved
		{ActionID: "act_006", DecidedAt: t0.Add(6 * time.Second), Tool: "z", Input: json.RawMessage(`{"v":`)},
		{ActionID: "act_007", DecidedAt: t0.Add(7 * time.Second), Tool: "z", Input: json.RawMessage(`null`)},
	}
	rows[4].Approval = ApprovalCancelled
	res, err := (&Engine{Src: &memSource{rows: rows}}).Run(bg, ids.OrgID{}, Candidate{Compiled: cand}, Baseline{}, window(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, tr := range res.Transitions {
		order = append(order, tr.From+">"+tr.To)
	}
	if strings.Join(order, ",") != "deny>allow,deny>require_approval,require_approval>allow" {
		t.Fatalf("order %v", order)
	}
	if len(res.RuleChanges) != 2 || res.RuleChanges[0].FromRule != "old-c" || res.Unknown.InputUnreadable != 1 || res.Unknown.InputMissing != 1 {
		t.Fatalf("%+v %+v", res.RuleChanges, res.Unknown)
	}
	if h := res.Transitions[2].HumanOutcome; h == nil || h.Cancelled != 1 {
		t.Fatalf("human %+v", h)
	}
	if !strings.Contains((&TooLargeError{Actions: 7}).Error(), "the limit is") || ErrNotFound.Error() == "" || ErrStateConflict.Error() == "" {
		t.Fatal("errors")
	}
	for _, c := range [][2]string{{"a", "b"}, {"b", "a"}, {"a", "a"}} {
		_ = compareStrings(c[0], c[1])
	}
	if sc := sortedCounts(map[string]int64{"b": 2, "a": 2, "c": 5}); sc[0].Key != "c" || sc[1].Key != "a" {
		t.Fatal(sc)
	}
	// A version baseline: a tool_unknown action stays as decided under both.
	tu := mk(8, "nope.x", "deny", "tool-unknown")
	tu.Recorded.Source, tu.Recorded.Code = policy.SourceBuiltin, "tool_unknown"
	res, err = (&Engine{Src: &memSource{rows: []InputRow{tu}}}).Run(bg, ids.OrgID{}, Candidate{Compiled: cand},
		Baseline{Type: BaselineVersion, Compiled: cand}, window(), nil)
	if err != nil || res.Summary.Unchanged != 1 {
		t.Fatalf("%v %+v", err, res.Summary)
	}
}

// TestReplayTimeLimit: an evaluation that hits its backstop is unknown, never guessed.
func TestReplayTimeLimit(t *testing.T) {
	prev := replayDeadline
	replayDeadline = time.Nanosecond
	defer func() { replayDeadline = prev }()
	v3 := compile(t, replayV3)
	rows := fixture(t, 20)
	res, err := (&Engine{Src: &memSource{rows: rows}}).Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{Type: BaselineVersion, Compiled: v3}, window(), nil)
	if err != nil || res.Unknown.EvaluationTimedOut == 0 {
		t.Fatalf("%v %+v", err, res.Unknown)
	}
	res, err = (&Engine{Src: &memSource{rows: rows}}).Run(bg, ids.OrgID{}, Candidate{Compiled: v3}, Baseline{}, window(), nil)
	if err != nil || res.Unknown.EvaluationTimedOut == 0 {
		t.Fatalf("%v %+v", err, res.Unknown)
	}
}
