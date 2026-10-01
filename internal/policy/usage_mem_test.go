package policy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/policy/usagetest"
)

func TestUsageMem(t *testing.T) {
	usagetest.Run(t, func(t *testing.T) (policy.UsageStore, string, string) {
		return policy.NewMemUsage(), "org_01k6abcdefghjkmnpqrstvwxyz", "agt_01k6abcdefghjkmnpqrstvwxyz"
	})
}

func TestUsageMemNeedsOrg(t *testing.T) {
	ctx := context.Background()
	m := policy.NewMemUsage()
	r := policy.Reservation{Scope: "org", Counter: "calls", Window: policy.WindowMinute, WindowStart: usagetest.At, Amount: 1}
	if err := m.Reserve(ctx, "", []policy.Reservation{r}); err == nil {
		t.Error("Reserve without an org")
	}
	if err := m.Release(ctx, "", []policy.Reservation{r}); err == nil {
		t.Error("Release without an org")
	}
	if err := m.RecordCost(ctx, "", nil, policy.Money{Amount: 1, Currency: "usd"}); err == nil {
		t.Error("RecordCost without an org")
	}
	if _, err := m.Snapshot(ctx, policy.SnapshotRequest{Agent: "agt_x", At: usagetest.At}); err == nil {
		t.Error("Snapshot without an org")
	}
	if _, err := m.Snapshot(ctx, policy.SnapshotRequest{Org: "org_x", At: usagetest.At}); !errors.Is(err, policy.ErrBadReservation) {
		t.Errorf("Snapshot without an agent: %v", err)
	}
}

func TestUsageCostReservations(t *testing.T) {
	ts := []policy.CostTarget{{Scope: "agt_x", Window: policy.WindowDay, WindowStart: usagetest.At}}
	if _, err := policy.CostReservations(ts, policy.Money{Amount: 5}); !errors.Is(err, policy.ErrBadReservation) {
		t.Errorf("a cost without a currency: %v", err)
	}
	if rs, err := policy.CostReservations(ts, policy.Money{Amount: -5, Currency: "usd"}); err != nil || rs != nil {
		t.Errorf("a negative cost: %v %v", rs, err)
	}
	bad := []policy.CostTarget{{Scope: "agt_x", Window: "week", WindowStart: usagetest.At}}
	if _, err := policy.CostReservations(bad, policy.Money{Amount: 5, Currency: "usd"}); !errors.Is(err, policy.ErrBadReservation) {
		t.Errorf("a target with a bad window: %v", err)
	}
	rs, err := policy.CostReservations(ts, policy.Money{Amount: 5, Currency: "inr"})
	if err != nil || len(rs) != 1 || rs[0].Counter != "spend:inr" || rs[0].Amount != 5 || rs[0].Limit != 0 {
		t.Errorf("standard target: %+v %v", rs, err)
	}
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestWindowStartEnd(t *testing.T) {
	kol, err := policy.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		window     string
		at         string
		loc        *time.Location
		start, end string
	}{
		{policy.WindowMinute, "2026-10-04T20:00:00Z", nil, "2026-10-04T20:00:00Z", "2026-10-04T20:01:00Z"},
		{policy.WindowMinute, "2026-10-04T20:00:59.999999999Z", kol, "2026-10-04T20:00:00Z", "2026-10-04T20:01:00Z"},
		{policy.WindowHour, "2026-10-04T20:59:59Z", kol, "2026-10-04T20:00:00Z", "2026-10-04T21:00:00Z"},
		{policy.WindowHour, "2026-10-04T23:59:59Z", nil, "2026-10-04T23:00:00Z", "2026-10-05T00:00:00Z"},
		{policy.WindowDay, "2026-10-04T20:00:00Z", nil, "2026-10-04T00:00:00Z", "2026-10-05T00:00:00Z"},
		{policy.WindowDay, "2026-10-04T20:00:00Z", time.UTC, "2026-10-04T00:00:00Z", "2026-10-05T00:00:00Z"},
		// 2026-10-05 01:30 in Kolkata: the local day began at 18:30 UTC the evening before.
		{policy.WindowDay, "2026-10-04T20:00:00Z", kol, "2026-10-04T18:30:00Z", "2026-10-05T18:30:00Z"},
		{policy.WindowDay, "2026-10-04T18:29:59Z", kol, "2026-10-03T18:30:00Z", "2026-10-04T18:30:00Z"},
		{policy.WindowDay, "2026-10-04T18:30:00Z", kol, "2026-10-04T18:30:00Z", "2026-10-05T18:30:00Z"},
	} {
		at := utc(c.at)
		if got := policy.WindowStart(c.window, at, c.loc); !got.Equal(utc(c.start)) || got.Location() != time.UTC {
			t.Errorf("WindowStart(%s, %s) = %v, want %s in UTC", c.window, c.at, got, c.start)
		}
		if got := policy.WindowEnd(c.window, at, c.loc); !got.Equal(utc(c.end)) || got.Location() != time.UTC {
			t.Errorf("WindowEnd(%s, %s) = %v, want %s in UTC", c.window, c.at, got, c.end)
		}
	}
	// A non-UTC instant floors the same as its UTC form.
	at := time.Date(2026, 10, 5, 1, 30, 45, 0, kol)
	if got := policy.WindowStart(policy.WindowMinute, at, nil); !got.Equal(utc("2026-10-04T20:00:00Z")) {
		t.Errorf("minute floor of a Kolkata instant: %v", got)
	}
}

func TestWindowDST(t *testing.T) {
	ny, err := policy.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		at, start string
		hours     time.Duration
	}{
		{"2026-11-01T17:00:00Z", "2026-11-01T04:00:00Z", 25}, // clocks go back: a 25-hour day
		{"2026-03-08T17:00:00Z", "2026-03-08T05:00:00Z", 23}, // clocks go forward: a 23-hour day
		{"2026-10-04T17:00:00Z", "2026-10-04T04:00:00Z", 24},
	} {
		at := utc(c.at)
		s, e := policy.WindowStart(policy.WindowDay, at, ny), policy.WindowEnd(policy.WindowDay, at, ny)
		if !s.Equal(utc(c.start)) {
			t.Errorf("%s: start %v, want %s", c.at, s, c.start)
		}
		if e.Sub(s) != c.hours*time.Hour {
			t.Errorf("%s: day is %v, want %dh", c.at, e.Sub(s), c.hours)
		}
		if !policy.WindowStart(policy.WindowDay, e, ny).Equal(e) {
			t.Errorf("%s: the end %v is not the next day's start", c.at, e)
		}
	}
}

func TestLocationCache(t *testing.T) {
	for _, n := range []string{"", "UTC"} {
		l, err := policy.LoadLocation(n)
		if err != nil || l != time.UTC {
			t.Errorf("LoadLocation(%q) = %v, %v; want time.UTC", n, l, err)
		}
	}
	a, err := policy.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	b, err := policy.LoadLocation("Asia/Kolkata")
	if err != nil || a != b {
		t.Errorf("second load is not the cached *Location: %p %p %v", a, b, err)
	}
	for _, n := range []string{"Not/AZone", "../etc/passwd", "Asia/Kolkata\x00"} {
		if l, err := policy.LoadLocation(n); err == nil || l != nil {
			t.Errorf("LoadLocation(%q) = %v, %v; want an error", n, l, err)
		}
	}
	if _, err := policy.LoadLocation("Not/AZone"); err == nil {
		t.Error("an error was cached as a location")
	}
}

func TestCostTargetCounter(t *testing.T) {
	for _, c := range []struct {
		t    policy.CostTarget
		cur  string
		want string
	}{
		{policy.CostTarget{}, "usd", "spend:usd"},
		{policy.CostTarget{}, "eur", "spend:eur"},
		{policy.CostTarget{Currency: "usd"}, "eur", "spend:eur"}, // the standard counter takes any currency
		{policy.CostTarget{Budget: policy.AgentSpendPerDay, Currency: "usd"}, "inr", "spend:inr"},
		{policy.CostTarget{Budget: "cap", Currency: "usd"}, "usd", "budget:cap"},
		{policy.CostTarget{Budget: "cap", Currency: "usd"}, "eur", ""}, // no FX
		{policy.CostTarget{Budget: "cap"}, "eur", "budget:cap"},
	} {
		if got := c.t.Counter(c.cur); got != c.want {
			t.Errorf("%+v.Counter(%q) = %q, want %q", c.t, c.cur, got, c.want)
		}
	}
}

func TestCheckReservation(t *testing.T) {
	good := policy.Reservation{Scope: "org", Counter: "calls", Window: policy.WindowHour, WindowStart: usagetest.At, Amount: 1}
	if err := policy.CheckReservation(good); err != nil {
		t.Fatal(err)
	}
	zero := good
	zero.Amount = 0
	if err := policy.CheckReservation(zero); err != nil {
		t.Errorf("amount 0: %v", err)
	}
	for name, f := range map[string]func(*policy.Reservation){
		"scope":        func(r *policy.Reservation) { r.Scope = "" },
		"counter":      func(r *policy.Reservation) { r.Counter = "" },
		"window":       func(r *policy.Reservation) { r.Window = "week" },
		"empty window": func(r *policy.Reservation) { r.Window = "" },
		"amount":       func(r *policy.Reservation) { r.Amount = -1 },
		"limit":        func(r *policy.Reservation) { r.Limit = -1 },
		"start":        func(r *policy.Reservation) { r.WindowStart = time.Time{} },
	} {
		r := good
		f(&r)
		if err := policy.CheckReservation(r); !errors.Is(err, policy.ErrBadReservation) {
			t.Errorf("%s: %v, want ErrBadReservation", name, err)
		}
	}
}

func TestExceededError(t *testing.T) {
	e := &policy.ExceededError{BudgetID: "daily_calls", Used: 60, Limit: 60, ResetsAt: usagetest.At}
	if got, want := e.Error(), `budget "daily_calls" reached: 60 of 60`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	var err error = e
	var ex *policy.ExceededError
	if !errors.As(err, &ex) || ex.Limit != 60 {
		t.Error("errors.As")
	}
}
