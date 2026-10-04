//go:build integration

package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store/policystore"
)

var updateOverview = flag.Bool("update-overview", false, "rewrite testdata/overview_golden.json")

// usageReader is console.UsageFromStore without the import: S11's store, read in the Overview's
// transaction.
func usageReader(u *policystore.Usage) ledger.UsageReader {
	return func(ctx context.Context, tx pgx.Tx, org ids.OrgID, tz string, at time.Time, reqs []ledger.AgentUsageRequest) ([]ledger.AgentUsage, error) {
		loc, err := policy.LoadLocation(tz)
		if err != nil {
			loc = time.UTC
		}
		agents, curs := make([]string, len(reqs)), make([]string, len(reqs))
		for i, r := range reqs {
			agents[i], curs[i] = r.Agent, r.Currency
		}
		snaps, err := u.AgentsTx(ctx, tx, org, loc, at, agents, curs)
		if err != nil {
			return nil, err
		}
		out := make([]ledger.AgentUsage, len(snaps))
		for i, s := range snaps {
			out[i] = ledger.AgentUsage{CallsMinute: s.CallsMinute, CallsHour: s.CallsHour, CallsToday: s.CallsToday, SpendToday: s.SpendToday, Currency: s.Currency}
		}
		return out, nil
	}
}

// fixtureNow is half an hour into the current six-hour UTC block: the 24 h window ends half an
// hour later and the 7 d window five and a half hours later, so every seeded time sits at the same
// place in its buckets on every run, whatever the time of day.
func fixtureNow() time.Time { return time.Now().UTC().Truncate(6 * time.Hour).Add(30 * time.Minute) }

// seedTimes sets created_at of the actions, in the order given (fake.Result.IDs is plan order),
// to at(i).
func (en *env) seedTimes(t testing.TB, actionIDs []string, at func(i int) time.Time) {
	t.Helper()
	ts := make([]time.Time, len(actionIDs))
	for i := range actionIDs {
		ts[i] = at(i)
	}
	if _, err := en.e.Admin(t).Exec(ctx, `UPDATE deedglass.actions AS a SET created_at = k.at
	  FROM unnest($1::text[], $2::timestamptz[]) AS k (id, at) WHERE a.id = k.id`, actionIDs, ts); err != nil {
		t.Fatal(err)
	}
}

var (
	idRE   = regexp.MustCompile(`\b(org|usr|agt|key|con|act|apr|deed)_[0-7][0-9a-hjkmnp-tv-z]{25}\b`)
	timeRE = regexp.MustCompile(`"(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z)"`)
)

// masked renders an overview for a golden file: ids become <prefix_n> in order of appearance and
// timestamps become their offset from now in seconds.
func masked(t testing.TB, ov *ledger.Overview, now time.Time) []byte {
	t.Helper()
	b, err := json.MarshalIndent(ov, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	count := map[string]int{}
	b = idRE.ReplaceAllFunc(b, func(m []byte) []byte {
		s := string(m)
		if v, ok := seen[s]; ok {
			return []byte(v)
		}
		p := s[:strings.IndexByte(s, '_')]
		count[p]++
		seen[s] = fmt.Sprintf("<%s_%d>", p, count[p])
		return []byte(seen[s])
	})
	return timeRE.ReplaceAllFunc(b, func(m []byte) []byte {
		v, err := time.Parse(time.RFC3339Nano, string(m[1:len(m)-1]))
		if err != nil {
			t.Fatal(err)
		}
		return []byte(fmt.Sprintf(`"now%+.3fs"`, v.Sub(now).Seconds()))
	})
}

// TestOverviewGolden: a fixed fixture (3 agents, 240 actions from a fixed seed, seeded times, a
// budget, usage, two pending approvals) gives exactly the numbers in testdata/overview_golden.json
// (go test -tags integration ./internal/ledger -run TestOverviewGolden -args -update-overview).
func TestOverviewGolden(t *testing.T) {
	en := newEnv(t)
	now := fixtureNow()
	// 236 actions of every kind but MISMATCH, with four MISMATCH plans at positions 10 to 13 (inside
	// the last 24 h), so the attention list shows every kind the fixture sets up.
	plans := fake.Generate(25, 236, []fake.Kind{fake.Allow, fake.Deny, fake.Unverifiable, fake.Failed, fake.Pending, fake.Amend}, 3)
	plans = append(plans[:10], append(fake.Generate(26, 4, []fake.Kind{fake.Mismatch}, 3), plans[10:]...)...)
	res, err := fake.Run(ctx, en.led, en.org, en.agents, plans, fake.Options{Batch: 60})
	if err != nil {
		t.Fatal(err)
	}
	actionIDs := res.IDs
	_, to := ledger.Window24h.Bounds(now)
	// Every 7 minutes back from 30 s before the window's end: about 206 in the window, the rest in
	// the previous one; the newest five are after now (still in the current bucket).
	en.seedTimes(t, actionIDs, func(i int) time.Time { return to.Add(-30*time.Second - time.Duration(i)*7*time.Minute) })
	c := en.e.Admin(t)
	// A budget on billing-bot and today's usage: 85 of 100 calls (budget_near), 1,234 cents spent.
	if _, err := c.Exec(ctx, `UPDATE deedglass.agents SET budgets = '{"calls_per_day": 100, "calls_per_minute": 50}' WHERE org_id = $1 AND name = 'billing-bot'`, en.org); err != nil {
		t.Fatal(err)
	}
	u := policystore.NewUsage(en.e.DB)
	day := policy.WindowStart(policy.WindowDay, now, time.UTC)
	if err := u.Reserve(ctx, en.org.String(), []policy.Reservation{
		{Scope: en.agents[1].ID.String(), Counter: policy.CounterCalls, Window: policy.WindowDay, WindowStart: day, Amount: 85},
		{Scope: en.agents[1].ID.String(), Counter: policy.CounterCalls, Window: policy.WindowMinute, WindowStart: policy.WindowStart(policy.WindowMinute, now, nil), Amount: 3},
		{Scope: en.agents[0].ID.String(), Counter: policy.CounterCalls, Window: policy.WindowDay, WindowStart: day, Amount: 12},
		{Scope: en.agents[0].ID.String(), Counter: policy.SpendCounter("usd"), Window: policy.WindowDay, WindowStart: day, Amount: 1234},
	}); err != nil {
		t.Fatal(err)
	}
	// Two pending approvals on the two newest pending actions (plan order: the ids of a bulk run
	// aren't in plan order): one expires in 10 minutes, one in 2 hours.
	var pending []string
	for i, p := range plans {
		if p.Kind == fake.Pending {
			pending = append(pending, actionIDs[i])
		}
	}
	if len(pending) < 2 {
		t.Fatalf("the fixture has %d pending actions; pick another seed", len(pending))
	}
	for i, exp := range []time.Duration{10 * time.Minute, 2 * time.Hour} {
		if _, err := c.Exec(ctx, `INSERT INTO deedglass.approvals (id, org_id, action_id, expires_at, created_at, tool, agent_id)
		  SELECT $1, a.org_id, a.id, $3, $4, a.tool, a.agent_id FROM deedglass.actions AS a WHERE a.id = $2`,
			ids.New[ids.ApprovalKind]().String(), pending[i], now.Add(exp), now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	read := func(w ledger.OverviewWindow) *ledger.Overview {
		ov, err := en.led.Overview(ctx, en.org, ledger.OverviewOptions{Window: w, Now: now, Usage: usageReader(u), SlotLagAlertBytes: 512 << 20})
		if err != nil {
			t.Fatal(err)
		}
		return ov
	}
	d, w7 := read(ledger.Window24h), read(ledger.Window7d)
	got := append(append(masked(t, d, now), '\n'), masked(t, w7, now)...)
	got = append(got, '\n')
	path := filepath.Join("testdata", "overview_golden.json")
	if *updateOverview {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -args -update-overview)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the overview differs from %s (rerun with -args -update-overview and review the diff):\n%s", path, got)
	}

	// The invariants behind the numbers, independent of the golden.
	for _, ov := range []*ledger.Overview{d, w7} {
		var sum, blocked int64
		for i := range ov.Actions.Series {
			sum += ov.Actions.Series[i]
			blocked += ov.Blocked.Series[i]
		}
		v := ov.Verification
		if sum != ov.Actions.Total || blocked != ov.Blocked.Total {
			t.Errorf("%s: the series don't add up to the totals", ov.Window)
		}
		if v.ByLevel.L0+v.ByLevel.L1+v.ByLevel.L2 != v.Verified+v.Mismatch+v.Unverifiable {
			t.Errorf("%s: by_level %+v doesn't sum to the witnessed actions", ov.Window, v.ByLevel)
		}
		if v.Verified+v.Mismatch+v.Unverifiable+v.NotWitnessed != ov.Actions.Total {
			t.Errorf("%s: verdicts plus not_witnessed isn't the total", ov.Window)
		}
		var want int64
		if err := c.QueryRow(ctx, `SELECT count(*) FROM deedglass.actions WHERE org_id = $1 AND created_at >= $2 AND created_at < $3`,
			en.org, time.Time(ov.From), time.Time(ov.To)).Scan(&want); err != nil || want != ov.Actions.Total {
			t.Errorf("%s: total %d, the table says %d (%v)", ov.Window, ov.Actions.Total, want, err)
		}
	}
	if d.Actions.Total+d.Actions.Previous != int64(len(actionIDs)) || w7.Actions.Total != int64(len(actionIDs)) {
		t.Errorf("24h %d + previous %d, 7d %d, of %d actions", d.Actions.Total, d.Actions.Previous, w7.Actions.Total, len(actionIDs))
	}
	if d.Approvals.Pending != 2 || d.Approvals.ExpiringSoon != 1 || d.Approvals.NextExpiresAt == nil {
		t.Errorf("approvals %+v", d.Approvals)
	}
	kinds := map[string]int{}
	for _, it := range d.Attention {
		kinds[it.Kind]++
	}
	if kinds[ledger.AttentionApprovalExpiry] != 1 || kinds[ledger.AttentionBudgetNear] != 1 || kinds[ledger.AttentionMismatch] != 4 {
		t.Errorf("attention kinds %v", kinds)
	}
}

func pendingActions(t testing.TB, en *env, org ids.OrgID) []string {
	t.Helper()
	rows, err := en.e.Admin(t).Query(ctx, `SELECT id FROM deedglass.actions WHERE org_id = $1 AND state = 'pending_approval' ORDER BY id DESC`, org)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestOverviewWindowBoundaries: an action at exactly from counts, one at exactly to doesn't, one
// just before from is in the previous window, for both windows.
func TestOverviewWindowBoundaries(t *testing.T) {
	en := newEnv(t)
	now := time.Now().UTC()
	for _, w := range []ledger.OverviewWindow{ledger.Window24h, ledger.Window7d} {
		org, agents := en.newOrg(t, "bounds-"+w.Name[:2])
		res, err := fake.Run(ctx, en.led, org, agents, fake.Generate(3, 5, []fake.Kind{fake.Allow}, 1), fake.Options{})
		if err != nil {
			t.Fatal(err)
		}
		from, to := w.Bounds(now)
		at := []time.Time{from.Add(-time.Microsecond), from, to.Add(-time.Microsecond), to, from.Add(-w.Span)}
		en.seedTimes(t, res.IDs, func(i int) time.Time { return at[i] })
		ov, err := en.led.Overview(ctx, org, ledger.OverviewOptions{Window: w, Now: now})
		if err != nil {
			t.Fatal(err)
		}
		if ov.Actions.Total != 2 || ov.Actions.Previous != 2 {
			t.Errorf("%s: total %d previous %d, want 2 and 2 (at from counted, at to not)", w.Name, ov.Actions.Total, ov.Actions.Previous)
		}
		if ov.Actions.Series[0] != 1 || ov.Actions.Series[w.Buckets-1] != 1 || len(ov.Actions.Series) != w.Buckets {
			t.Errorf("%s: series %v", w.Name, ov.Actions.Series)
		}
		if time.Time(ov.From) != from || time.Time(ov.To) != to {
			t.Errorf("%s: from %s to %s", w.Name, time.Time(ov.From), time.Time(ov.To))
		}
		if b, _ := json.Marshal(ov); !bytes.Contains(b, []byte(`"attention":[]`)) {
			t.Errorf("%s: an empty attention list isn't []: %s", w.Name, b)
		}
	}
}

// TestOverviewIsolation: org B's overview is unchanged by org A's actions, approvals, budgets and
// slots, and never names one of A's ids.
func TestOverviewIsolation(t *testing.T) {
	en := newEnv(t)
	b, bAgents := en.newOrg(t, "globex")
	if _, err := fake.Run(ctx, en.led, b, bAgents, fake.Generate(9, 20, nil, 2), fake.Options{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Minute)
	u := policystore.NewUsage(en.e.DB)
	read := func(org ids.OrgID) []byte {
		ov, err := en.led.Overview(ctx, org, ledger.OverviewOptions{Window: ledger.Window7d, Now: now, Usage: usageReader(u), SlotLagAlertBytes: 1})
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(ov)
		return out
	}
	before := read(b)
	if _, err := fake.Run(ctx, en.led, en.org, en.agents, fake.Generate(10, 120, nil, 3), fake.Options{}); err != nil {
		t.Fatal(err)
	}
	c := en.e.Admin(t)
	pending := pendingActions(t, en, en.org)
	if len(pending) > 0 {
		if _, err := c.Exec(ctx, `INSERT INTO deedglass.approvals (id, org_id, action_id, expires_at) VALUES ($1, $2, $3, $4)`,
			ids.New[ids.ApprovalKind]().String(), en.org, pending[0], now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Exec(ctx, `UPDATE deedglass.agents SET budgets = '{"calls_per_day": 1}' WHERE org_id = $1`, en.org); err != nil {
		t.Fatal(err)
	}
	if err := u.Reserve(ctx, en.org.String(), []policy.Reservation{{Scope: en.agents[0].ID.String(), Counter: policy.CounterCalls,
		Window: policy.WindowDay, WindowStart: policy.WindowStart(policy.WindowDay, now, time.UTC), Amount: 5}}); err != nil {
		t.Fatal(err)
	}
	con := insertSlot(t, en, en.org, "orders-db", "halted", 9<<30)
	if _, err := c.Exec(ctx, `UPDATE deedglass.orgs SET stopped_at = now(), stopped_by = 'operator', stop_reason = 'drill' WHERE id = $1`, en.org); err != nil {
		t.Fatal(err)
	}
	after := read(b)
	if !bytes.Equal(before, after) {
		t.Errorf("org A's activity changed org B's overview:\nbefore %s\nafter  %s", before, after)
	}
	a := read(en.org)
	var aIDs []string
	for _, ag := range en.agents {
		aIDs = append(aIDs, ag.ID.String())
	}
	aIDs = append(aIDs, en.org.String(), con)
	aIDs = append(aIDs, pending...)
	for _, id := range aIDs {
		if bytes.Contains(after, []byte(id)) {
			t.Errorf("org B's overview names org A's %s", id)
		}
	}
	if !bytes.Contains(a, []byte(`"calls_today":5`)) {
		t.Errorf("org A's own overview lacks its usage: %s", a)
	}
	for _, kind := range []string{ledger.AttentionOrgStopped, ledger.AttentionSlotInvalidated} {
		if !bytes.Contains(a, []byte(`"kind":"`+kind+`"`)) {
			t.Errorf("org A's own overview lacks %s: %s", kind, a)
		}
	}
}

// insertSlot adds a Postgres connector and its decoder row, as S20 keeps it.
func insertSlot(t testing.TB, en *env, org ids.OrgID, name, status string, lag int64) string {
	t.Helper()
	c := en.e.Admin(t)
	con := ids.New[ids.ConnectorKind]().String()
	if _, err := c.Exec(ctx, `INSERT INTO deedglass.connectors (id, org_id, type, name) VALUES ($1, $2, 'postgres', $3)`, con, org, name); err != nil {
		t.Fatal(err)
	}
	slot := "deedglass_" + strings.ReplaceAll(strings.ToLower(con[4:20]), "-", "_")
	if _, err := c.Exec(ctx, `INSERT INTO deedglass.decoder_slots (org_id, connector_id, slot_name, status, status_reason, lag_bytes)
	  VALUES ($1, $2, $3, $4, $5, $6)`, org, con, slot, status, map[bool]any{true: "slot_invalidated", false: nil}[status == "halted"], lag); err != nil {
		t.Fatal(err)
	}
	return con
}

// TestOverviewAttention: each kind's rule, priority order and the cap, on one org.
func TestOverviewAttention(t *testing.T) {
	en := newEnv(t)
	now := time.Now().UTC().Add(time.Minute)
	res, err := fake.Run(ctx, en.led, en.org, en.agents, fake.Generate(4, 12, []fake.Kind{fake.Mismatch}, 3), fake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	read := func(lag int64, chain *ledger.ChainCheck) *ledger.Overview {
		ov, err := en.led.Overview(ctx, en.org, ledger.OverviewOptions{Now: now, SlotLagAlertBytes: lag, Chain: chain})
		if err != nil {
			t.Fatal(err)
		}
		return ov
	}
	ov := read(0, nil)
	if len(ov.Attention) == 0 || ov.Attention[0].Kind != ledger.AttentionMismatch {
		t.Fatalf("mismatches: %+v", ov.Attention)
	}
	m := ov.Attention[0]
	if m.Severity != ledger.SeverityHigh || m.Ref == nil || !strings.HasPrefix(*m.Ref, "act_") ||
		m.Data["tool"] != "postgres.execute" || m.Data["level"] == nil || m.Data["agent"] == nil {
		t.Errorf("mismatch item %+v", m)
	}
	if s, _ := m.Data["summary"].(*string); s == nil || *s != "Postgres changed 301 rows in public.orders. The agent reported 1." {
		t.Errorf("the witness summary isn't verbatim: %v", m.Data["summary"])
	}
	for i := 1; i < len(ov.Attention); i++ {
		if time.Time(ov.Attention[i].At).After(time.Time(ov.Attention[i-1].At)) {
			t.Errorf("mismatches aren't newest first")
		}
	}
	// A mismatch older than 24 h drops out.
	en.seedTimes(t, res.IDs, func(int) time.Time { return now.Add(-25 * time.Hour) })
	if ov := read(0, nil); len(ov.Attention) != 0 {
		t.Errorf("old mismatches still listed: %+v", ov.Attention)
	}
	en.seedTimes(t, res.IDs, func(i int) time.Time { return now.Add(-time.Duration(i+1) * time.Minute) })

	// Slots: halted → slot_invalidated; lag over the threshold → slot_lagging; a disabled row never shows.
	insertSlot(t, en, en.org, "orders-db", "halted", 0)
	insertSlot(t, en, en.org, "billing-db", "running", 600<<20)
	insertSlot(t, en, en.org, "archive-db", "disabled", 900<<20)
	ov = read(512<<20, nil)
	if len(ov.Attention) != ledger.AttentionMax {
		t.Errorf("%d items, want the cap %d", len(ov.Attention), ledger.AttentionMax)
	}
	if ov.Attention[0].Kind != ledger.AttentionSlotInvalidated || ov.Attention[0].Data["connector"] != "orders-db" ||
		ov.Attention[1].Kind != ledger.AttentionSlotLagging || ov.Attention[1].Data["lag_bytes"] != int64(600<<20) ||
		ov.Attention[1].Data["threshold_bytes"] != int64(512<<20) || ov.Attention[1].Severity != ledger.SeverityMedium {
		t.Errorf("slot items: %+v", ov.Attention[:2])
	}
	if ov := read(0, nil); ov.Attention[1].Kind == ledger.AttentionSlotLagging {
		t.Error("slot_lagging without a threshold")
	}
	// Org stopped, then a broken chain: both go first, in that order.
	if _, err := en.e.Admin(t).Exec(ctx, `UPDATE deedglass.orgs SET stopped_at = $2, stopped_by = 'key_x', stop_reason = 'drill' WHERE id = $1`,
		en.org, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	seq, deedID := int64(7), "deed_x"
	ov = read(512<<20, &ledger.ChainCheck{Status: ledger.CheckBroken, Break: &ledger.ChainBreak{Seq: &seq, Deed: &deedID, Code: "body_hash_mismatch"}})
	var kinds []string
	for _, it := range ov.Attention {
		kinds = append(kinds, it.Kind)
	}
	want := []string{"chain_broken", "org_stopped", "slot_invalidated", "slot_lagging", "mismatch", "mismatch", "mismatch", "mismatch"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("order %v, want %v", kinds, want)
	}
	if st := ov.Attention[1]; st.Data["by"].(*string) == nil || *st.Data["by"].(*string) != "key_x" || *st.Data["reason"].(*string) != "drill" {
		t.Errorf("org_stopped data %+v", st.Data)
	}
}

// TestOverviewNoSystemAgent: the agents strip never shows the org's system agent, and has_more
// says when more agents exist.
func TestOverviewNoSystemAgent(t *testing.T) {
	en := newEnv(t)
	c := en.e.Admin(t)
	if _, err := c.Exec(ctx, `INSERT INTO deedglass.agents (id, org_id, name, kind) VALUES ($1, $2, 'console', 'system')`,
		ids.New[ids.AgentKind]().String(), en.org); err != nil {
		t.Fatal(err)
	}
	for i := range 7 {
		if _, err := c.Exec(ctx, `INSERT INTO deedglass.agents (id, org_id, name) VALUES ($1, $2, $3)`,
			ids.New[ids.AgentKind]().String(), en.org, fmt.Sprintf("extra-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fake.Run(ctx, en.led, en.org, en.agents, fake.Generate(5, 30, []fake.Kind{fake.Allow}, 3), fake.Options{}); err != nil {
		t.Fatal(err)
	}
	ov, err := en.led.Overview(ctx, en.org, ledger.OverviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Agents.Data) != ledger.OverviewAgentsShown || !ov.Agents.HasMore {
		t.Fatalf("%d agents, has_more %v", len(ov.Agents.Data), ov.Agents.HasMore)
	}
	for i, a := range ov.Agents.Data {
		if a.Name == "console" {
			t.Error("the system agent is listed")
		}
		if i < 3 && a.LastActionAt == nil {
			t.Errorf("agent %d (%s) has no last action; active agents go first", i, a.Name)
		}
		if i >= 3 && a.LastActionAt != nil {
			t.Errorf("agent %d (%s) is out of order", i, a.Name)
		}
	}
	if ov.Agents.Data[3].Name != "extra-0" {
		t.Errorf("agents with no action aren't by name: %s", ov.Agents.Data[3].Name)
	}
}

// TestOverviewBudget measures S25 §3.8: GET /v1/overview's read (the cache bypassed) at 100,000
// actions spread over the last 24 h in one org, p99 < 80 ms, for both windows, with the usage
// store attached; and the EXPLAIN plans of the two scans. It runs only with DEEDGLASS_IT_BENCH=1
// (make bench-overview) and writes PERF JSON lines to the test log.
func TestOverviewBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-overview)")
	}
	n := 100_000
	if s := os.Getenv("DEEDGLASS_IT_BENCH_N"); s != "" {
		fmt.Sscan(s, &n)
	}
	en := newEnv(t)
	report := func(p perf) {
		b, _ := json.Marshal(p)
		t.Logf("PERF %s", b)
		if !p.Within {
			t.Errorf("%s: p99 %.2f ms over the %.0f ms budget", p.Name, p.P99ms, p.Budget)
		}
	}
	start := time.Now()
	res, err := fake.Run(ctx, en.led, en.org, en.agents, fake.Generate(42, n, nil, 3), fake.Options{Batch: 500})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Spread evenly over the last 24 h, newest first.
	step := 24 * time.Hour / time.Duration(n)
	en.seedTimes(t, res.IDs, func(i int) time.Time { return now.Add(-time.Duration(i+1) * step) })
	if _, err := en.e.Admin(t).Exec(ctx, "VACUUM ANALYZE"); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %d actions in %s", n, time.Since(start).Round(time.Second))
	u := policystore.NewUsage(en.e.DB)
	for _, w := range []ledger.OverviewWindow{ledger.Window24h, ledger.Window7d} {
		var got *ledger.Overview
		p := measure(t, "overview_"+w.Name+"_100k", 300, 80, func(int) error {
			var err error
			got, err = en.led.Overview(ctx, en.org, ledger.OverviewOptions{Window: w, Now: time.Now(), Usage: usageReader(u), SlotLagAlertBytes: 512 << 20})
			return err
		})
		p.Extra = map[string]any{"actions_in_window": got.Actions.Total, "buckets": len(got.Actions.Series), "attention": len(got.Attention),
			"cache": "bypassed"}
		report(p)
		var want int64
		if err := en.e.Admin(t).QueryRow(ctx, `SELECT count(*) FROM deedglass.actions WHERE org_id = $1 AND created_at >= $2 AND created_at < $3`,
			en.org, time.Time(got.From), time.Time(got.To)).Scan(&want); err != nil || got.Actions.Total != want || want < int64(n)*9/10 {
			t.Errorf("%s total %d, the table holds %d in the window (%v)", w.Name, got.Actions.Total, want, err)
		}
	}
	// The plans of every read, with their execution times; both action scans must read an
	// (org_id, created_at) index as a range: actions_overview (0023), index only.
	type plan struct {
		text string
		ms   string
	}
	plans := map[string]plan{}
	err = en.e.DB.OrgReadTx(ctx, en.org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, ledger.OverviewSettingsSQL, "2s"); err != nil {
			return err
		}
		for q, args := range ledger.OverviewQueries(en.org, time.Now()) {
			rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+q, args...)
			if err != nil {
				return err
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			first := strings.Fields(strings.TrimSpace(q))
			name := strings.Join(first[:min(6, len(first))], " ")
			p := plan{text: strings.Join(lines, "\n")}
			if l := lines[len(lines)-1]; strings.HasPrefix(l, "Execution Time: ") {
				p.ms = strings.TrimPrefix(l, "Execution Time: ")
			}
			plans[name] = p
			if strings.Contains(q, "date_bin") || strings.HasPrefix(q, "SELECT pg_catalog.count(*), pg_catalog.count(*) FILTER (WHERE a.decision") {
				if !strings.Contains(p.text, "Index Only Scan using actions_overview") {
					t.Errorf("an action scan isn't an index-only scan of actions_overview:\n%s", p.text)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range plans {
		t.Logf("EXPLAIN %s … (%s):\n%s", name, p.ms, p.text)
	}
}
