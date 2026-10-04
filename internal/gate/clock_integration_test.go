//go:build integration

package gate

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
)

// virtualClock is a settable pipeline clock.
type virtualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *virtualClock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *virtualClock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

// TestDecisionClock (S27 §3.2): every timestamp the Gate writes on actions, action_events.at and
// the approval decision come from the pipeline clock; the deed's issued_at stays the database clock.
func TestDecisionClock(t *testing.T) {
	clk := &virtualClock{t: time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC)}
	w := newWorld(t, worldOpts{clock: clk.now})
	// An allowed write: evaluated_at = policy_input.time = the virtual decision time.
	a := w.must(0, req("fake.write", `{"table":"orders","rows":1}`))
	var (
		evaluated, executed, sealed time.Time
		inputTime                   string
		eventTimes                  []time.Time
	)
	w.adminRow(`SELECT evaluated_at, executed_at, sealed_at, policy_input ->> 'time' FROM deedglass.actions WHERE id = $1`, []any{a.ID},
		&evaluated, &executed, &sealed, &inputTime)
	in, _ := time.Parse(time.RFC3339Nano, inputTime)
	want := clk.now()
	if !evaluated.Equal(want) || !in.Equal(want) || !executed.Equal(want) || !sealed.Equal(want) {
		t.Fatalf("evaluated %v, policy_input.time %v, executed %v, sealed %v; want all %v", evaluated, in, executed, sealed, want)
	}
	err := w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT at FROM deedglass.action_events WHERE action_id = $1 ORDER BY seq`, a.ID)
		if err != nil {
			return err
		}
		eventTimes, err = pgx.CollectRows(rows, pgx.RowTo[time.Time])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, at := range eventTimes {
		if !at.Equal(want) {
			t.Fatalf("event %d at %v, want the virtual %v", i, at, want)
		}
	}
	env := w.deedOf(a.ID)
	issued, _ := time.Parse(time.RFC3339Nano, env.Body.IssuedAt)
	if time.Since(issued) > time.Minute {
		t.Fatalf("issued_at %v is not the database's real clock", issued)
	}
	// A write that waits for a person: pending_approval_at is virtual, and so is the decision.
	p := w.must(0, req("fake.write", `{"table":"approvals","rows":1}`))
	if p.Status != "pending_approval" || p.Approval == nil {
		t.Fatalf("want pending_approval, got %s", p.Status)
	}
	decided := want.Add(20 * time.Minute)
	clk.set(decided)
	if err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		by := w.owner.String()
		return store.DecideApproval(ctx, tx, w.org, p.Approval.ID, "approved", &by, nil, clk.now())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.p.Resume(bg, w.org, ids.MustParse[ids.ActionKind](p.ID)); err != nil {
		t.Fatal(err)
	}
	var pending, decidedAt, executing time.Time
	w.adminRow(`SELECT a.pending_approval_at, ap.decided_at, a.executing_at FROM deedglass.actions a
	    JOIN deedglass.approvals ap ON ap.id = a.approval_id WHERE a.id = $1`, []any{p.ID}, &pending, &decidedAt, &executing)
	if !pending.Equal(want) || !decidedAt.Equal(decided) || !executing.Equal(decided) {
		t.Fatalf("pending %v decided %v executing %v; want %v, %v, %v", pending, decidedAt, executing, want, decided, decided)
	}
	_ = json.RawMessage(nil)
	_ = policy.InputVersion
}

// staticFacts is an AnomalySource with fixed answers per agent.
type staticFacts map[ids.AgentID]policy.AnomalyFacts

func (s staticFacts) Facts(_ context.Context, _ ids.OrgID, agent ids.AgentID, _ time.Time) (policy.AnomalyFacts, string) {
	f, ok := s[agent]
	if !ok || !f.Active {
		return policy.AnomalyFacts{}, ""
	}
	return f, "anomaly facts: " + f.Kinds[0]
}

const anomalyPolicy = `deedglass: policy/v1
name: gate-anomaly
defaults: { decision: deny, reason: No rule allows this action. }
rules:
  - id: approve-while-anomalous
    match: { side_effect: [write, destructive] }
    when: anomaly.active && anomaly.severity in ["warn", "high"]
    decision: require_approval
    reason: This agent is behaving unusually right now. A person approves writes until the anomaly is resolved.
  - id: allow-fake
    match: { tool: ["fake.*"] }
    decision: allow
`

// TestAnomalyFactsDecide (S27 §3.8): the Gate passes the AnomalySource's facts into the input, a
// policy that reads them decides on them, the stored input keeps them for replay, and the action's
// decision carries the trace line.
func TestAnomalyFactsDecide(t *testing.T) {
	var src staticFacts = map[ids.AgentID]policy.AnomalyFacts{}
	w := newWorld(t, worldOpts{policy: anomalyPolicy, anomalies: src})
	src[w.agents[0].ID] = policy.AnomalyFacts{Active: true, Kinds: []string{"volume_spike"}, Severity: "warn"}
	a := w.must(0, req("fake.write", `{"table":"orders","rows":1}`))
	if a.Status != "pending_approval" || a.Decision == nil || a.Decision.RuleID == nil || *a.Decision.RuleID != "approve-while-anomalous" {
		t.Fatalf("anomalous agent: %s %+v", a.Status, a.Decision)
	}
	got, err := w.p.Get(bg, w.pr(0), a.ID, 0)
	if err != nil || got.Decision == nil || got.Decision.Anomaly == nil || *got.Decision.Anomaly != "anomaly facts: volume_spike" {
		t.Fatalf("decision.anomaly: %v %+v", err, got.Decision)
	}
	var stored policy.Input
	var raw []byte
	w.adminRow(`SELECT policy_input FROM deedglass.actions WHERE id = $1`, []any{a.ID}, &raw)
	if err := json.Unmarshal(raw, &stored); err != nil || !stored.Anomaly.Active || stored.Anomaly.Severity != "warn" || stored.Anomaly.Kinds[0] != "volume_spike" {
		t.Fatalf("stored input anomaly %+v %v", stored.Anomaly, err)
	}
	// Another agent with no episode: allowed, and no trace line.
	b := w.must(1, req("fake.write", `{"table":"orders","rows":1}`))
	if b.Decision == nil || b.Decision.RuleID == nil || *b.Decision.RuleID != "allow-fake" || b.Decision.Anomaly != nil {
		t.Fatalf("quiet agent: %+v", b.Decision)
	}
}
