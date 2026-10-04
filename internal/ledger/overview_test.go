package ledger

import (
	"encoding/json"
	"testing"
	"time"
)

func mustUTC(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestOverviewBounds pins the window maths: to is the first bucket boundary at or after now (UTC),
// from is to minus the window, and the current partial bucket is the last one.
func TestOverviewBounds(t *testing.T) {
	cases := []struct {
		w        OverviewWindow
		now      string
		from, to string
	}{
		{Window24h, "2026-10-09T13:59:58.123Z", "2026-10-08T14:00:00Z", "2026-10-09T14:00:00Z"},
		{Window24h, "2026-10-09T14:00:00Z", "2026-10-08T14:00:00Z", "2026-10-09T14:00:00Z"}, // on a boundary: to = now
		{Window24h, "2026-10-09T14:00:00.001Z", "2026-10-08T15:00:00Z", "2026-10-09T15:00:00Z"},
		{Window24h, "2026-12-31T23:30:00Z", "2026-12-31T00:00:00Z", "2027-01-01T00:00:00Z"},
		{Window7d, "2026-10-09T13:59:58Z", "2026-10-02T18:00:00Z", "2026-10-09T18:00:00Z"},
		{Window7d, "2026-10-09T18:00:00Z", "2026-10-02T18:00:00Z", "2026-10-09T18:00:00Z"},
		{Window7d, "2026-10-09T00:00:00.5Z", "2026-10-02T06:00:00Z", "2026-10-09T06:00:00Z"},
		{Window7d, "2026-03-29T01:30:00Z", "2026-03-22T06:00:00Z", "2026-03-29T06:00:00Z"}, // the EU DST change: UTC is unaffected
	}
	for _, c := range cases {
		from, to := c.w.Bounds(mustUTC(t, c.now))
		if !from.Equal(mustUTC(t, c.from)) || !to.Equal(mustUTC(t, c.to)) {
			t.Errorf("%s at %s: [%s, %s), want [%s, %s)", c.w.Name, c.now, from.Format(time.RFC3339), to.Format(time.RFC3339), c.from, c.to)
		}
		if to.Sub(from) != c.w.Span || c.w.Span != time.Duration(c.w.Buckets)*c.w.Bucket {
			t.Errorf("%s: span %s for %d buckets of %s", c.w.Name, to.Sub(from), c.w.Buckets, c.w.Bucket)
		}
		if from.Location() != time.UTC || to.Location() != time.UTC {
			t.Errorf("%s: bounds not in UTC", c.w.Name)
		}
	}
	// A local time is the same instant: Asia/Kolkata (+05:30) doesn't move the buckets off UTC.
	ist := time.FixedZone("IST", 5*3600+1800)
	from, to := Window24h.Bounds(time.Date(2026, 10, 9, 19, 29, 0, 0, ist)) // 13:59 UTC
	if !from.Equal(mustUTC(t, "2026-10-08T14:00:00Z")) || !to.Equal(mustUTC(t, "2026-10-09T14:00:00Z")) {
		t.Errorf("a local now moved the window: [%s, %s)", from, to)
	}
}

// TestOverviewBucketIndex: from is in the first bucket, to is outside, and every bucket boundary
// starts the next bucket.
func TestOverviewBucketIndex(t *testing.T) {
	for _, w := range []OverviewWindow{Window24h, Window7d} {
		from, to := w.Bounds(mustUTC(t, "2026-10-09T13:59:58Z"))
		if got := w.BucketIndex(from, from); got != 0 {
			t.Errorf("%s: from is bucket %d", w.Name, got)
		}
		if got := w.BucketIndex(from, to); got != -1 {
			t.Errorf("%s: to is bucket %d, want outside", w.Name, got)
		}
		if got := w.BucketIndex(from, to.Add(-time.Nanosecond)); got != w.Buckets-1 {
			t.Errorf("%s: just before to is bucket %d", w.Name, got)
		}
		if got := w.BucketIndex(from, from.Add(-time.Nanosecond)); got != -1 {
			t.Errorf("%s: before from is bucket %d", w.Name, got)
		}
		for i := 0; i < w.Buckets; i++ {
			b := from.Add(time.Duration(i) * w.Bucket)
			if got := w.BucketIndex(from, b); got != i {
				t.Errorf("%s: boundary %d is bucket %d", w.Name, i, got)
			}
			if got := w.BucketIndex(from, b.Add(w.Bucket-time.Millisecond)); got != i {
				t.Errorf("%s: the end of bucket %d is bucket %d", w.Name, i, got)
			}
		}
	}
	if _, ok := OverviewWindowOf("24h"); !ok {
		t.Error("24h")
	}
	if _, ok := OverviewWindowOf("7d"); !ok {
		t.Error("7d")
	}
	for _, bad := range []string{"", "1h", "24H", "7D", "30d", "24h "} {
		if _, ok := OverviewWindowOf(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func i64(n int64) *int64 { return &n }

// TestAttentionOrderAndCap: kinds keep their priority, budget_near is fullest first, nothing under
// 80% shows, and at most 8 items go out.
func TestAttentionOrderAndCap(t *testing.T) {
	now := mustUTC(t, "2026-10-09T14:00:00Z")
	var a attention
	seq, deed := int64(42), "deed_x"
	a.chain(&ChainCheck{Status: CheckIntact})
	if len(a.items) != 0 {
		t.Fatal("an intact chain made an item")
	}
	a.chain(&ChainCheck{Status: CheckBroken, Break: &ChainBreak{Seq: &seq, Deed: &deed, Code: "body_hash_mismatch", Message: "A sentence."}})
	if len(a.items) != 1 || a.items[0].Kind != AttentionChainBroken || a.items[0].Severity != SeverityHigh || *a.items[0].Ref != deed {
		t.Fatalf("chain: %+v", a.items)
	}
	if _, ok := a.items[0].Data["message"]; ok {
		t.Error("chain_broken carries the break's sentence; the server sends data, not sentences")
	}
	a.budgets([]budgetAgent{
		{id: "agt_b", name: "billing-bot", budgets: AgentBudgets{CallsPerDay: i64(100)}, usage: AgentUsage{CallsToday: 79}}, // 79%: no
		{id: "agt_c", name: "ops-bot", budgets: AgentBudgets{CallsPerDay: i64(100), CallsPerMinute: i64(10)},
			usage: AgentUsage{CallsToday: 80, CallsMinute: 10}}, // 80% and 100%
		{id: "agt_d", name: "data-bot", budgets: AgentBudgets{SpendPerDay: &Money{Amount: 1000, Currency: "inr"}},
			usage: AgentUsage{SpendToday: 950, Currency: "inr"}}, // 95%
		{id: "agt_e", name: "empty-bot", budgets: AgentBudgets{CallsPerDay: i64(0)}, usage: AgentUsage{CallsToday: 5}}, // no limit
	}, now)
	var got []string
	for _, it := range a.items[1:] {
		got = append(got, it.Data["agent"].(string)+"/"+it.Data["budget"].(string))
		if it.Kind != AttentionBudgetNear || it.Severity != SeverityLow || !time.Time(it.At).Equal(now) {
			t.Errorf("budget item: %+v", it)
		}
	}
	want := []string{"ops-bot/calls_per_minute", "data-bot/spend_per_day", "ops-bot/calls_per_day"}
	if len(got) != len(want) {
		t.Fatalf("budget_near: %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("budget_near: %v, want %v", got, want)
		}
	}
	if a.items[2].Data["currency"] != "inr" {
		t.Errorf("spend item without its currency: %+v", a.items[2].Data)
	}
	for range 20 {
		a.add(&AttentionItem{Object: "attention_item", Kind: AttentionMismatch})
	}
	if len(a.items) != AttentionMax {
		t.Fatalf("%d items, want at most %d", len(a.items), AttentionMax)
	}
	b, err := json.Marshal(a.items[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"object", "kind", "severity", "at", "ref", "data"} {
		if _, ok := m[k]; !ok {
			t.Errorf("attention_item lacks %q: %s", k, b)
		}
	}
}

// TestSpendToday: null only when nothing was spent and the agent has no spend budget.
func TestSpendToday(t *testing.T) {
	if s := spendToday(AgentBudgets{}, AgentUsage{Currency: "usd"}); s != nil {
		t.Errorf("no spend, no budget: %+v", s)
	}
	if s := spendToday(AgentBudgets{}, AgentUsage{SpendToday: 5, Currency: "usd"}); s == nil || s.Amount != 5 || s.Currency != "usd" {
		t.Errorf("spend without budget: %+v", s)
	}
	b := AgentBudgets{SpendPerDay: &Money{Amount: 100, Currency: "eur"}}
	if s := spendToday(b, AgentUsage{}); s == nil || s.Amount != 0 || s.Currency != "eur" {
		t.Errorf("a budget with nothing spent: %+v", s)
	}
	if SpendCurrency(AgentBudgets{}) != "usd" || SpendCurrency(b) != "eur" {
		t.Error("SpendCurrency")
	}
}
