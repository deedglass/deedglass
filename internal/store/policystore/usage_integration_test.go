//go:build integration

package policystore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/policy/usagetest"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var ctx = context.Background()

// The contract suite on Postgres: one database, a fresh org (and its seeded agent) per sub-test.
func TestUsageContract(t *testing.T) {
	env := storetest.New(t, storetest.Options{})
	u := policystore.NewUsage(env.DB)
	var mu sync.Mutex
	n := 0
	usagetest.Run(t, func(t *testing.T) (policy.UsageStore, string, string) {
		mu.Lock()
		n++
		slug := fmt.Sprintf("usage-%d", n)
		mu.Unlock()
		o := storetest.NewOrg(t, env.DB, slug)
		return u, o.ID.String(), o.Agent.String()
	})
}

func budgetRes(id, scope string, amount, limit int64) policy.Reservation {
	at := usagetest.At
	return policy.Reservation{Budget: id, Scope: scope, Counter: policy.BudgetCounter(id), Window: policy.WindowMinute,
		WindowStart: policy.WindowStart(policy.WindowMinute, at, nil), Amount: amount, Limit: limit,
		ResetsAt: policy.WindowEnd(policy.WindowMinute, at, nil)}
}

// Two server instances (two pools) racing on one budget: Postgres decides, so exactly the limit
// gets through.
func TestReserveTwoInstances(t *testing.T) {
	env := storetest.New(t, storetest.Options{MaxConns: 16})
	db2, err := store.Open(ctx, config.NewSecret(env.AppURL), store.Options{MaxConns: 16})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db2.Close)
	o := storetest.NewOrg(t, env.DB, "two-pools")
	stores := []*policystore.Usage{policystore.NewUsage(env.DB), policystore.NewUsage(db2)}
	r := budgetRes("shared_burst", policy.ScopeOrg, 1, 60)

	var mu sync.Mutex
	var ok, exceeded int
	var other []error
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func(s *policystore.Usage) {
			defer wg.Done()
			<-start
			err := s.Reserve(ctx, o.ID.String(), []policy.Reservation{r})
			var ex *policy.ExceededError
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.As(err, &ex):
				exceeded++
				if ex.Limit != 60 || ex.BudgetID != "shared_burst" || ex.Used > 60 {
					other = append(other, fmt.Errorf("bad exceeded error: %+v", ex))
				}
			default:
				other = append(other, err)
			}
		}(stores[i%2])
	}
	close(start)
	wg.Wait()
	if len(other) > 0 {
		t.Fatalf("%d unexpected errors, first: %v", len(other), other[0])
	}
	if ok != 60 || exceeded != 340 {
		t.Fatalf("%d succeeded, %d exceeded; want 60 and 340", ok, exceeded)
	}
	got, err := stores[1].Snapshot(ctx, policy.SnapshotRequest{Org: o.ID.String(), Agent: o.Agent.String(), At: usagetest.At,
		Budgets: []policy.BudgetRef{{ID: "shared_burst", Per: policy.PerOrg, Window: policy.WindowMinute}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Budgets["shared_burst"] != 60 {
		t.Fatalf("shared_burst = %d, want 60", got.Budgets["shared_burst"])
	}
}

// Org A's transactions never see or bump org B's counters.
func TestUsageRLS(t *testing.T) {
	env := storetest.New(t, storetest.Options{})
	u := policystore.NewUsage(env.DB)
	a := storetest.NewOrg(t, env.DB, "usage-a")
	b := storetest.NewOrg(t, env.DB, "usage-b")
	day := policy.WindowStart(policy.WindowDay, usagetest.At, nil)
	refs := []policy.BudgetRef{{ID: "shared_burst", Per: policy.PerOrg, Window: policy.WindowMinute}}

	if err := u.Reserve(ctx, b.ID.String(), []policy.Reservation{
		budgetRes("shared_burst", policy.ScopeOrg, 5, 5),
		{Scope: b.Agent.String(), Counter: policy.CounterCalls, Window: policy.WindowDay, WindowStart: day, Amount: 7},
	}); err != nil {
		t.Fatal(err)
	}
	// A reads its own counters only, even when it names B's agent.
	for _, agent := range []string{a.Agent.String(), b.Agent.String()} {
		got, err := u.Snapshot(ctx, policy.SnapshotRequest{Org: a.ID.String(), Agent: agent, At: usagetest.At, Budgets: refs})
		if err != nil {
			t.Fatal(err)
		}
		if got.Budgets["shared_burst"] != 0 || got.CallsToday != 0 {
			t.Fatalf("org A sees org B's counters: %+v", got)
		}
	}
	// A's budget of the same name is a separate counter with its own room.
	if err := u.Reserve(ctx, a.ID.String(), []policy.Reservation{budgetRes("shared_burst", policy.ScopeOrg, 5, 5)}); err != nil {
		t.Fatalf("org A's own budget: %v", err)
	}
	// A's release can't reach B's rows.
	if err := u.Release(ctx, a.ID.String(), []policy.Reservation{
		{Scope: b.Agent.String(), Counter: policy.CounterCalls, Window: policy.WindowDay, WindowStart: day, Amount: 7}}); err != nil {
		t.Fatal(err)
	}
	// Raw SQL in A's transaction: B's rows are invisible to UPDATE, and an INSERT for B is refused.
	err := env.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
		for _, q := range []struct {
			sql string
			arg any
		}{
			{`UPDATE deedglass.usage_counters SET value = 999 WHERE org_id = $1`, b.ID},
			{`UPDATE deedglass.usage_counters SET value = 999 WHERE scope = $1`, b.Agent.String()},
		} {
			tag, err := tx.Exec(ctx, q.sql, q.arg)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 0 {
				return fmt.Errorf("%s touched %d of org B's rows", q.sql, tag.RowsAffected())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = env.DB.OrgTx(ctx, a.ID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO deedglass.usage_counters (org_id, scope, counter, window_kind, window_start, value)
		    VALUES ($1, 'org', 'budget:shared_burst', 'minute', $2, 1)`,
			b.ID, policy.WindowStart(policy.WindowMinute, usagetest.At, nil))
		return err
	})
	if store.SQLState(err) != store.CodeInsufficientPriv {
		t.Fatalf("insert for org B inside org A's transaction: %v, want SQLSTATE 42501", err)
	}
	got, err := u.Snapshot(ctx, policy.SnapshotRequest{Org: b.ID.String(), Agent: b.Agent.String(), At: usagetest.At, Budgets: refs})
	if err != nil {
		t.Fatal(err)
	}
	if got.Budgets["shared_burst"] != 5 || got.CallsToday != 7 {
		t.Fatalf("org B's counters changed: %+v", got)
	}
	// A malformed org id never reaches the database.
	if err := u.Reserve(ctx, "org_nope", []policy.Reservation{budgetRes("x", policy.ScopeOrg, 1, 0)}); err == nil {
		t.Fatal("Reserve with a malformed org id")
	}
	if _, err := u.Snapshot(ctx, policy.SnapshotRequest{Org: "", Agent: a.Agent.String(), At: usagetest.At}); err == nil {
		t.Fatal("Snapshot without an org")
	}
}

// Pins S02's table to the S11 §3.6 shape, so no replacement migration is needed.
func TestUsageCountersShape(t *testing.T) {
	env := storetest.New(t, storetest.Options{})
	c := env.Admin(t)
	type col struct{ name, typ, nullable string }
	rows, err := c.Query(ctx, `SELECT column_name::text, data_type::text, is_nullable::text
	    FROM information_schema.columns WHERE table_schema = 'deedglass' AND table_name = 'usage_counters'
	    ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (col, error) {
		var x col
		return x, r.Scan(&x.name, &x.typ, &x.nullable)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []col{
		{"org_id", "text", "NO"}, {"scope", "text", "NO"}, {"counter", "text", "NO"}, {"window_kind", "text", "NO"},
		{"window_start", "timestamp with time zone", "NO"}, {"value", "bigint", "NO"},
		{"created_at", "timestamp with time zone", "NO"}, {"updated_at", "timestamp with time zone", "NO"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("columns:\n got %v\nwant %v", got, want)
	}

	rows, err = c.Query(ctx, `SELECT a.attname::text
	    FROM pg_catalog.pg_index AS i
	    CROSS JOIN LATERAL pg_catalog.generate_series(0, i.indnkeyatts - 1) AS n
	    JOIN pg_catalog.pg_attribute AS a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[n]
	    WHERE i.indrelid = 'deedglass.usage_counters'::regclass AND i.indisprimary
	    ORDER BY n`)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"org_id", "scope", "counter", "window_kind", "window_start"}; !slices.Equal(pk, want) {
		t.Fatalf("primary key %v, want %v", pk, want)
	}

	rows, err = c.Query(ctx, `SELECT pg_catalog.pg_get_constraintdef(oid) FROM pg_catalog.pg_constraint
	    WHERE conrelid = 'deedglass.usage_counters'::regclass AND contype = 'c'`)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range checks {
		if strings.Contains(d, "window_kind") && strings.Contains(d, "'minute'") && strings.Contains(d, "'hour'") && strings.Contains(d, "'day'") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no CHECK on window_kind in %q", checks)
	}

	var rls, force bool
	var policies []string
	if err := c.QueryRow(ctx, `SELECT c.relrowsecurity, c.relforcerowsecurity,
	    ARRAY(SELECT p.polname::text FROM pg_catalog.pg_policy AS p WHERE p.polrelid = c.oid ORDER BY 1)
	    FROM pg_catalog.pg_class AS c WHERE c.oid = 'deedglass.usage_counters'::regclass`).Scan(&rls, &force, &policies); err != nil {
		t.Fatal(err)
	}
	if !rls || force || !slices.Equal(policies, []string{"org_isolation", "system_scope"}) {
		t.Fatalf("RLS enabled %v, forced %v, policies %v; want enabled, not forced, org_isolation and system_scope", rls, force, policies)
	}

	for _, p := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		var has bool
		if err := c.QueryRow(ctx, `SELECT pg_catalog.has_table_privilege('deedglass_app', 'deedglass.usage_counters', $1)`, p).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if !has {
			t.Errorf("deedglass_app lacks %s on usage_counters", p)
		}
	}
}

// Reserve latency for a typical action: three standard rows and two budget rows. Run with
// DEEDGLASS_IT_BENCH=1.
func TestReserveLatency(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1")
	}
	env := storetest.New(t, storetest.Options{})
	u := policystore.NewUsage(env.DB)
	o := storetest.NewOrg(t, env.DB, "reserve-bench")
	agent := o.Agent.String()
	at := usagetest.At
	std := func(w string) policy.Reservation {
		return policy.Reservation{Scope: agent, Counter: policy.CounterCalls, Window: w,
			WindowStart: policy.WindowStart(w, at, nil), Amount: 1, ResetsAt: policy.WindowEnd(w, at, nil)}
	}
	rs := []policy.Reservation{
		std(policy.WindowMinute), std(policy.WindowHour), std(policy.WindowDay),
		budgetRes("per_agent", agent, 1, 1<<40), budgetRes("per_org", policy.ScopeOrg, 1, 1<<40),
	}
	for i := 0; i < 100; i++ { // warm the pool and the plan cache
		if err := u.Reserve(ctx, o.ID.String(), rs); err != nil {
			t.Fatal(err)
		}
	}
	const n = 2000
	d := make([]time.Duration, n)
	for i := range d {
		t0 := time.Now()
		if err := u.Reserve(ctx, o.ID.String(), rs); err != nil {
			t.Fatal(err)
		}
		d[i] = time.Since(t0)
	}
	slices.Sort(d)
	p50, p99 := d[n/2], d[n*99/100]
	var ver string
	if err := env.Admin(t).QueryRow(ctx, "SHOW server_version").Scan(&ver); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(struct {
		Name   string `json:"name"`
		N      int    `json:"n"`
		P50us  int64  `json:"p50_us"`
		P99us  int64  `json:"p99_us"`
		BudP50 int64  `json:"budget_p50_us"`
		Server string `json:"server"`
	}{"reserve_5_rows", n, p50.Microseconds(), p99.Microseconds(), 1500, "PostgreSQL " + ver})
	t.Logf("PERF %s", b)
	if p50 >= 1500*time.Microsecond {
		t.Errorf("Reserve (5 rows) p50 %v, budget 1.5 ms", p50)
	}
}
