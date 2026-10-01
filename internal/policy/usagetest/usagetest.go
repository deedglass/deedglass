// Package usagetest is the contract suite every policy.UsageStore passes: the in-memory store in
// internal/policy and the Postgres store in internal/store/policystore run the same sub-tests.
package usagetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/policy"
)

// NewStore returns a fresh store, plus an org id and an agent id that are valid for it.
type NewStore func(t *testing.T) (s policy.UsageStore, org string, agent string)

// otherAgent is a well-formed agent id that no seed creates: scopes are not foreign keys.
const otherAgent = "agt_7zzzzzzzzzzzzzzzzzzzzzzzzz"

// At is the fixed decision instant of the suite: 2026-10-04 20:00 UTC, 2026-10-05 01:30 in Kolkata.
var At = time.Date(2026, 10, 4, 20, 0, 0, 0, time.UTC)

// Run runs the suite. Every sub-test calls newStore once.
func Run(t *testing.T, newStore func(t *testing.T) (s policy.UsageStore, org string, agent string)) {
	ctx := context.Background()

	t.Run("SnapshotEmpty", func(t *testing.T) {
		s, org, agent := newStore(t)
		got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: []policy.BudgetRef{
			{ID: "per_minute", Per: policy.PerAgent, Window: policy.WindowMinute},
			{ID: "org_daily", Per: policy.PerOrg, Window: policy.WindowDay},
		}})
		if got.CallsMinute != 0 || got.CallsHour != 0 || got.CallsToday != 0 || got.SpendToday != 0 {
			t.Fatalf("empty snapshot: %+v", got)
		}
		if got.Currency != "usd" {
			t.Fatalf("currency %q, want usd", got.Currency)
		}
		if len(got.Budgets) != 2 {
			t.Fatalf("budgets %v, want both ids", got.Budgets)
		}
		for _, id := range []string{"per_minute", "org_daily"} {
			if v, ok := got.Budgets[id]; !ok || v != 0 {
				t.Fatalf("budget %s: %v %v", id, v, ok)
			}
		}
	})

	t.Run("StandardCounters", func(t *testing.T) {
		s, org, agent := newStore(t)
		for i := 0; i < 3; i++ {
			mustReserve(t, s, org, calls(agent, policy.WindowMinute, At, nil, 1, 0),
				calls(agent, policy.WindowHour, At, nil, 1, 0), calls(agent, policy.WindowDay, At, nil, 1, 0))
		}
		mustReserve(t, s, org, policy.Reservation{Scope: agent, Counter: policy.SpendCounter("eur"), Window: policy.WindowDay,
			WindowStart: policy.WindowStart(policy.WindowDay, At, nil), Amount: 250})
		got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Currency: "eur"})
		if got.CallsMinute != 3 || got.CallsHour != 3 || got.CallsToday != 3 || got.SpendToday != 250 || got.Currency != "eur" {
			t.Fatalf("snapshot: %+v", got)
		}
		if got.Budgets == nil {
			t.Fatal("Budgets is nil; want an empty map")
		}
		usd := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At})
		if usd.SpendToday != 0 || usd.Currency != "usd" || usd.CallsToday != 3 {
			t.Fatalf("usd snapshot: %+v", usd)
		}
		later := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At.Add(time.Minute), Currency: "eur"})
		if later.CallsMinute != 0 || later.CallsHour != 3 || later.CallsToday != 3 || later.SpendToday != 250 {
			t.Fatalf("a minute later: %+v", later)
		}
		other := snap(t, s, policy.SnapshotRequest{Org: org, Agent: otherAgent, At: At, Currency: "eur"})
		if other.CallsMinute != 0 || other.CallsToday != 0 || other.SpendToday != 0 {
			t.Fatalf("another agent sees this agent's calls: %+v", other)
		}
	})

	t.Run("BudgetScopes", func(t *testing.T) {
		s, org, agent := newStore(t)
		mustReserve(t, s, org,
			budget("mine", agent, policy.WindowHour, At, nil, 2, 0),
			budget("shared", policy.ScopeOrg, policy.WindowHour, At, nil, 5, 0))
		refs := []policy.BudgetRef{{ID: "mine", Per: policy.PerAgent, Window: policy.WindowHour}, {ID: "shared", Per: policy.PerOrg, Window: policy.WindowHour}}
		got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: refs})
		if got.Budgets["mine"] != 2 || got.Budgets["shared"] != 5 {
			t.Fatalf("budgets %v", got.Budgets)
		}
		oth := snap(t, s, policy.SnapshotRequest{Org: org, Agent: otherAgent, At: At, Budgets: refs})
		if oth.Budgets["mine"] != 0 || oth.Budgets["shared"] != 5 {
			t.Fatalf("another agent's budgets %v; want mine 0 (per agent) and shared 5 (per org)", oth.Budgets)
		}
	})

	t.Run("ConditionalLimit", func(t *testing.T) {
		s, org, agent := newStore(t)
		r := budget("cap", agent, policy.WindowMinute, At, nil, 1, 3)
		for i := 0; i < 3; i++ {
			mustReserve(t, s, org, r)
		}
		wantExceeded(t, s.Reserve(ctx, org, []policy.Reservation{r}), "cap", 3, 3, policy.WindowEnd(policy.WindowMinute, At, nil))
		got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: []policy.BudgetRef{{ID: "cap", Per: policy.PerAgent, Window: policy.WindowMinute}}})
		if got.Budgets["cap"] != 3 {
			t.Fatalf("cap = %d after the refused reserve, want 3", got.Budgets["cap"])
		}
		// A standard counter becomes conditional when an agent budget sets its limit.
		c := calls(agent, policy.WindowMinute, At, nil, 1, 2)
		c.Budget = policy.AgentCallsPerMinute
		mustReserve(t, s, org, c)
		mustReserve(t, s, org, c)
		wantExceeded(t, s.Reserve(ctx, org, []policy.Reservation{c}), policy.AgentCallsPerMinute, 2, 2, policy.WindowEnd(policy.WindowMinute, At, nil))
	})

	t.Run("AllOrNothing", func(t *testing.T) {
		s, org, agent := newStore(t)
		full := budget("full", agent, policy.WindowDay, At, nil, 5, 5)
		mustReserve(t, s, org, full)
		full.Amount = 1
		err := s.Reserve(ctx, org, []policy.Reservation{
			calls(agent, policy.WindowMinute, At, nil, 1, 0),
			budget("roomy", policy.ScopeOrg, policy.WindowDay, At, nil, 1, 100),
			full,
		})
		wantExceeded(t, err, "full", 5, 5, policy.WindowEnd(policy.WindowDay, At, nil))
		got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: []policy.BudgetRef{
			{ID: "full", Per: policy.PerAgent, Window: policy.WindowDay}, {ID: "roomy", Per: policy.PerOrg, Window: policy.WindowDay}}})
		if got.CallsMinute != 0 || got.Budgets["roomy"] != 0 || got.Budgets["full"] != 5 {
			t.Fatalf("a refused batch left writes behind: %+v", got)
		}
		// Two rows on the same counter in one batch add up before the limit is checked.
		twice := budget("twice", agent, policy.WindowDay, At, nil, 2, 3)
		wantExceeded(t, s.Reserve(ctx, org, []policy.Reservation{twice, twice}), "twice", 2, 3, policy.WindowEnd(policy.WindowDay, At, nil))
		got = snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: []policy.BudgetRef{{ID: "twice", Per: policy.PerAgent, Window: policy.WindowDay}}})
		if got.Budgets["twice"] != 0 {
			t.Fatalf("twice = %d, want 0", got.Budgets["twice"])
		}
	})

	t.Run("AmountOverLimit", func(t *testing.T) {
		s, org, agent := newStore(t)
		end := policy.WindowEnd(policy.WindowHour, At, nil)
		wantExceeded(t, s.Reserve(ctx, org, []policy.Reservation{budget("big", agent, policy.WindowHour, At, nil, 6, 5)}), "big", 0, 5, end)
		mustReserve(t, s, org, budget("big", agent, policy.WindowHour, At, nil, 2, 5))
		wantExceeded(t, s.Reserve(ctx, org, []policy.Reservation{
			calls(agent, policy.WindowHour, At, nil, 1, 0),
			budget("big", agent, policy.WindowHour, At, nil, 6, 5),
		}), "big", 2, 5, end)
		got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: []policy.BudgetRef{{ID: "big", Per: policy.PerAgent, Window: policy.WindowHour}}})
		if got.Budgets["big"] != 2 || got.CallsHour != 0 {
			t.Fatalf("snapshot %+v; want big 2 and no calls", got)
		}
	})

	t.Run("ReleaseFloors", func(t *testing.T) {
		s, org, agent := newStore(t)
		r := calls(agent, policy.WindowDay, At, nil, 3, 0)
		mustReserve(t, s, org, r)
		r.Amount = 2
		mustRelease(t, s, org, r)
		if got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At}); got.CallsToday != 1 {
			t.Fatalf("after releasing 2 of 3: %d", got.CallsToday)
		}
		mustRelease(t, s, org, r)
		mustRelease(t, s, org, r)
		if got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At}); got.CallsToday != 0 {
			t.Fatalf("release went below 0 or failed: %d", got.CallsToday)
		}
		// Releasing a counter that was never written is fine and creates nothing.
		mustRelease(t, s, org, budget("never", policy.ScopeOrg, policy.WindowMinute, At, nil, 4, 10))
		got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: []policy.BudgetRef{{ID: "never", Per: policy.PerOrg, Window: policy.WindowMinute}}})
		if got.Budgets["never"] != 0 {
			t.Fatalf("never = %d", got.Budgets["never"])
		}
		// Released room can be reserved again.
		b := budget("again", agent, policy.WindowMinute, At, nil, 1, 1)
		mustReserve(t, s, org, b)
		mustRelease(t, s, org, b)
		mustReserve(t, s, org, b)
	})

	t.Run("RecordCost", func(t *testing.T) {
		s, org, agent := newStore(t)
		day := policy.WindowStart(policy.WindowDay, At, nil)
		hour := policy.WindowStart(policy.WindowHour, At, nil)
		targets := []policy.CostTarget{
			{Scope: agent, Window: policy.WindowDay, WindowStart: day},
			{Budget: "usd_cap", Scope: agent, Window: policy.WindowDay, WindowStart: day, Currency: "usd"},
			{Budget: "org_usd", Scope: policy.ScopeOrg, Window: policy.WindowHour, WindowStart: hour, Currency: "usd"},
		}
		refs := []policy.BudgetRef{{ID: "usd_cap", Per: policy.PerAgent, Window: policy.WindowDay}, {ID: "org_usd", Per: policy.PerOrg, Window: policy.WindowHour}}
		if err := s.RecordCost(ctx, org, targets, policy.Money{Amount: 500, Currency: "usd"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordCost(ctx, org, targets, policy.Money{Amount: 300, Currency: "eur"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordCost(ctx, org, targets, policy.Money{Amount: 0, Currency: "usd"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordCost(ctx, org, nil, policy.Money{Amount: 10, Currency: "usd"}); err != nil {
			t.Fatal(err)
		}
		u := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Currency: "usd", Budgets: refs})
		if u.SpendToday != 500 || u.Budgets["usd_cap"] != 500 || u.Budgets["org_usd"] != 500 {
			t.Fatalf("usd: %+v", u)
		}
		e := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Currency: "eur", Budgets: refs})
		if e.SpendToday != 300 || e.Budgets["usd_cap"] != 500 {
			t.Fatalf("eur: %+v; the standard counter takes any currency, a usd budget only usd", e)
		}
		// The agent's spend_per_day budget is its standard spend counter.
		agentDay := []policy.CostTarget{{Budget: policy.AgentSpendPerDay, Scope: agent, Window: policy.WindowDay, WindowStart: day, Currency: "usd"}}
		if err := s.RecordCost(ctx, org, agentDay, policy.Money{Amount: 7, Currency: "usd"}); err != nil {
			t.Fatal(err)
		}
		if got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At}); got.SpendToday != 507 {
			t.Fatalf("spend after agent.spend_per_day: %d, want 507", got.SpendToday)
		}
	})

	t.Run("Windows", func(t *testing.T) {
		s, org, agent := newStore(t)
		kol, err := policy.LoadLocation("Asia/Kolkata")
		if err != nil {
			t.Fatal(err)
		}
		if got, want := policy.WindowStart(policy.WindowDay, At, kol), time.Date(2026, 10, 4, 18, 30, 0, 0, time.UTC); !got.Equal(want) {
			t.Fatalf("Kolkata day start %v, want %v", got, want)
		}
		at := At.Add(30 * time.Second)
		mustReserve(t, s, org, calls(agent, policy.WindowMinute, at, kol, 1, 0),
			calls(agent, policy.WindowHour, at, kol, 1, 0), calls(agent, policy.WindowDay, at, kol, 1, 0))
		for _, c := range []struct {
			at                  time.Time
			minute, hour, today int64
		}{
			{time.Date(2026, 10, 4, 20, 0, 0, 0, time.UTC), 1, 1, 1},
			{time.Date(2026, 10, 4, 20, 0, 59, 999e6, time.UTC), 1, 1, 1},
			{time.Date(2026, 10, 4, 20, 1, 0, 0, time.UTC), 0, 1, 1},
			{time.Date(2026, 10, 4, 20, 59, 59, 0, time.UTC), 0, 1, 1},
			{time.Date(2026, 10, 4, 21, 0, 0, 0, time.UTC), 0, 0, 1},
			{time.Date(2026, 10, 4, 18, 30, 0, 0, time.UTC), 0, 0, 1},
			{time.Date(2026, 10, 4, 18, 29, 59, 0, time.UTC), 0, 0, 0},
			{time.Date(2026, 10, 5, 18, 29, 59, 0, time.UTC), 0, 0, 1},
			{time.Date(2026, 10, 5, 18, 30, 0, 0, time.UTC), 0, 0, 0},
		} {
			got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: c.at, Location: kol})
			if got.CallsMinute != c.minute || got.CallsHour != c.hour || got.CallsToday != c.today {
				t.Errorf("at %v: minute %d hour %d today %d, want %d %d %d", c.at, got.CallsMinute, got.CallsHour, got.CallsToday, c.minute, c.hour, c.today)
			}
		}
		// The same day window in UTC is a different row.
		if got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: at}); got.CallsToday != 0 || got.CallsMinute != 1 {
			t.Fatalf("UTC view: %+v", got)
		}
		// A new window starts at zero, so a full budget has room again.
		b := budget("one_a_day", agent, policy.WindowDay, at, kol, 1, 1)
		mustReserve(t, s, org, b)
		wantExceeded(t, s.Reserve(ctx, org, []policy.Reservation{b}), "one_a_day", 1, 1, policy.WindowEnd(policy.WindowDay, at, kol))
		next := budget("one_a_day", agent, policy.WindowDay, time.Date(2026, 10, 5, 18, 30, 0, 0, time.UTC), kol, 1, 1)
		mustReserve(t, s, org, next)
	})

	t.Run("Prune", func(t *testing.T) {
		s, org, agent := newStore(t)
		// A separate era, so rows other sub-tests leave in a shared database are never old enough.
		now := time.Date(2020, 6, 1, 12, 0, 0, 0, time.UTC)
		put := func(w string, ws time.Time) {
			t.Helper()
			mustReserve(t, s, org, policy.Reservation{Scope: agent, Counter: policy.CounterCalls, Window: w,
				WindowStart: policy.WindowStart(w, ws, nil), Amount: 1})
		}
		put(policy.WindowMinute, now.Add(-3*time.Hour))   // old
		put(policy.WindowMinute, now.Add(-time.Hour))     // kept
		put(policy.WindowHour, now.Add(-4*24*time.Hour))  // old
		put(policy.WindowHour, now.Add(-2*24*time.Hour))  // kept
		put(policy.WindowDay, now.Add(-401*24*time.Hour)) // old
		put(policy.WindowDay, now.Add(-399*24*time.Hour)) // kept
		put(policy.WindowMinute, now.Add(-2*time.Hour))   // exactly at the edge: kept
		n, err := s.Prune(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		if n != 3 {
			t.Fatalf("Prune deleted %d rows, want 3", n)
		}
		for _, c := range []struct {
			at                  time.Time
			minute, hour, today int64
		}{
			{now.Add(-time.Hour), 1, 0, 0},
			{now.Add(-2 * time.Hour), 1, 0, 0},
			{now.Add(-3 * time.Hour), 0, 0, 0},
			{now.Add(-2 * 24 * time.Hour), 0, 1, 0},
			{now.Add(-4 * 24 * time.Hour), 0, 0, 0},
			{now.Add(-399 * 24 * time.Hour), 0, 0, 1},
			{now.Add(-401 * 24 * time.Hour), 0, 0, 0},
		} {
			got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: c.at})
			if got.CallsMinute != c.minute || got.CallsHour != c.hour || got.CallsToday != c.today {
				t.Errorf("at %v: %d %d %d, want %d %d %d", c.at, got.CallsMinute, got.CallsHour, got.CallsToday, c.minute, c.hour, c.today)
			}
		}
		if n, err := s.Prune(ctx, now); err != nil || n != 0 {
			t.Fatalf("second Prune: %d, %v", n, err)
		}
	})

	t.Run("Concurrency", func(t *testing.T) {
		s, org, agent := newStore(t)
		r := budget("burst", policy.ScopeOrg, policy.WindowMinute, At, nil, 1, 60)
		var mu sync.Mutex
		var ok, exceeded int
		var other []error
		var wg sync.WaitGroup
		for g := 0; g < 50; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 4; i++ {
					err := s.Reserve(ctx, org, []policy.Reservation{r})
					var ex *policy.ExceededError
					mu.Lock()
					switch {
					case err == nil:
						ok++
					case errors.As(err, &ex):
						exceeded++
					default:
						other = append(other, err)
					}
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if len(other) > 0 {
			t.Fatalf("%d unexpected errors, first: %v", len(other), other[0])
		}
		if ok != 60 || exceeded != 140 {
			t.Fatalf("%d succeeded and %d exceeded; want 60 and 140", ok, exceeded)
		}
		got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: []policy.BudgetRef{{ID: "burst", Per: policy.PerOrg, Window: policy.WindowMinute}}})
		if got.Budgets["burst"] != 60 {
			t.Fatalf("burst = %d, want 60", got.Budgets["burst"])
		}
	})

	t.Run("InvalidReservation", func(t *testing.T) {
		s, org, agent := newStore(t)
		good := calls(agent, policy.WindowMinute, At, nil, 1, 0)
		bad := []policy.Reservation{good, good, good, good, good, good}
		bad[0].Scope = ""
		bad[1].Counter = ""
		bad[2].Window = "week"
		bad[3].Amount = -1
		bad[4].Limit = -1
		bad[5].WindowStart = time.Time{}
		for i, b := range bad {
			if err := s.Reserve(ctx, org, []policy.Reservation{good, b}); !errors.Is(err, policy.ErrBadReservation) {
				t.Errorf("Reserve bad[%d]: %v, want ErrBadReservation", i, err)
			}
			if err := s.Release(ctx, org, []policy.Reservation{b}); !errors.Is(err, policy.ErrBadReservation) {
				t.Errorf("Release bad[%d]: %v, want ErrBadReservation", i, err)
			}
		}
		if _, err := s.Snapshot(ctx, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: []policy.BudgetRef{{ID: "x", Per: policy.PerAgent, Window: "week"}}}); !errors.Is(err, policy.ErrBadReservation) {
			t.Errorf("Snapshot with a bad window: %v", err)
		}
		if _, err := s.Snapshot(ctx, policy.SnapshotRequest{Org: org, Agent: agent, At: At, Budgets: []policy.BudgetRef{{ID: "x", Per: "team", Window: policy.WindowDay}}}); !errors.Is(err, policy.ErrBadReservation) {
			t.Errorf("Snapshot with a bad per: %v", err)
		}
		if got := snap(t, s, policy.SnapshotRequest{Org: org, Agent: agent, At: At}); got.CallsMinute != 0 {
			t.Fatalf("a refused batch wrote: %+v", got)
		}
		// Empty batches are no-ops.
		mustReserve(t, s, org)
		mustRelease(t, s, org)
	})
}

func calls(agent, window string, at time.Time, loc *time.Location, amount, limit int64) policy.Reservation {
	return policy.Reservation{Scope: agent, Counter: policy.CounterCalls, Window: window,
		WindowStart: policy.WindowStart(window, at, loc), Amount: amount, Limit: limit, ResetsAt: policy.WindowEnd(window, at, loc)}
}

func budget(id, scope, window string, at time.Time, loc *time.Location, amount, limit int64) policy.Reservation {
	return policy.Reservation{Budget: id, Scope: scope, Counter: policy.BudgetCounter(id), Window: window,
		WindowStart: policy.WindowStart(window, at, loc), Amount: amount, Limit: limit, ResetsAt: policy.WindowEnd(window, at, loc)}
}

func snap(t *testing.T, s policy.UsageStore, in policy.SnapshotRequest) policy.UsageSnapshot {
	t.Helper()
	got, err := s.Snapshot(context.Background(), in)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return got
}

func mustReserve(t *testing.T, s policy.UsageStore, org string, rs ...policy.Reservation) {
	t.Helper()
	if err := s.Reserve(context.Background(), org, rs); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
}

func mustRelease(t *testing.T, s policy.UsageStore, org string, rs ...policy.Reservation) {
	t.Helper()
	if err := s.Release(context.Background(), org, rs); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func wantExceeded(t *testing.T, err error, id string, used, limit int64, resets time.Time) {
	t.Helper()
	var ex *policy.ExceededError
	if !errors.As(err, &ex) {
		t.Fatalf("got %v, want *ExceededError", err)
	}
	if ex.BudgetID != id || ex.Used != used || ex.Limit != limit || !ex.ResetsAt.Equal(resets) {
		t.Fatalf("got %+v, want budget %q used %d limit %d resets %v", ex, id, used, limit, resets)
	}
}
