package policy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// MemUsage is an in-memory UsageStore with the same semantics as the Postgres store
// (internal/store/policystore). Tests, the policy test runner and single-process tools use it;
// the server never does, because counters must agree across instances.
type MemUsage struct {
	mu   sync.Mutex
	rows map[memKey]int64
}

type memKey struct {
	org, scope, counter, window string
	start                       int64 // UnixNano of the UTC window start
}

// NewMemUsage returns an empty in-memory store.
func NewMemUsage() *MemUsage { return &MemUsage{rows: map[memKey]int64{}} }

var _ UsageStore = (*MemUsage)(nil)

func keyOf(org, scope, counter, window string, start time.Time) memKey {
	return memKey{org: org, scope: scope, counter: counter, window: window, start: start.UTC().UnixNano()}
}

var errNoOrg = errors.New("policy: org id is required")

// SnapshotKey is one counter a snapshot reads.
type SnapshotKey struct {
	Scope, Counter, Window string
	WindowStart            time.Time
	Budget                 string // the budget id for a budget key, else ""
}

// SnapshotKeys lists the counters a snapshot reads, in a fixed order: calls for the minute, hour
// and day; spend for the day; then one per budget. Both stores use it so they read the same rows.
// It returns ErrBadReservation for a budget with an unknown per or window. The currency (the
// second result) defaults to "usd".
func SnapshotKeys(in SnapshotRequest) ([]SnapshotKey, string, error) {
	if in.Agent == "" {
		return nil, "", fmt.Errorf("%w: snapshot without an agent", ErrBadReservation)
	}
	cur := in.Currency
	if cur == "" {
		cur = "usd"
	}
	ks := []SnapshotKey{
		{Scope: in.Agent, Counter: CounterCalls, Window: WindowMinute, WindowStart: WindowStart(WindowMinute, in.At, in.Location)},
		{Scope: in.Agent, Counter: CounterCalls, Window: WindowHour, WindowStart: WindowStart(WindowHour, in.At, in.Location)},
		{Scope: in.Agent, Counter: CounterCalls, Window: WindowDay, WindowStart: WindowStart(WindowDay, in.At, in.Location)},
		{Scope: in.Agent, Counter: SpendCounter(cur), Window: WindowDay, WindowStart: WindowStart(WindowDay, in.At, in.Location)},
	}
	for _, b := range in.Budgets {
		if b.ID == "" || !ValidWindow(b.Window) {
			return nil, "", fmt.Errorf("%w: budget %+v", ErrBadReservation, b)
		}
		scope := in.Agent
		switch b.Per {
		case PerAgent, "":
		case PerOrg:
			scope = ScopeOrg
		default:
			return nil, "", fmt.Errorf("%w: budget %q has per %q", ErrBadReservation, b.ID, b.Per)
		}
		ks = append(ks, SnapshotKey{Scope: scope, Counter: BudgetCounter(b.ID), Window: b.Window,
			WindowStart: WindowStart(b.Window, in.At, in.Location), Budget: b.ID})
	}
	return ks, cur, nil
}

// FillSnapshot builds the snapshot from the value of each key (same order as SnapshotKeys).
func FillSnapshot(ks []SnapshotKey, cur string, values []int64) UsageSnapshot {
	s := UsageSnapshot{Currency: cur, Budgets: map[string]int64{}}
	s.CallsMinute, s.CallsHour, s.CallsToday, s.SpendToday = values[0], values[1], values[2], values[3]
	for i := 4; i < len(ks); i++ {
		s.Budgets[ks[i].Budget] = values[i]
	}
	return s
}

// CostReservations turns a reported cost into the unconditional increments it makes: one per
// target whose currency matches. A cost of 0 or less makes none; an empty currency is refused.
func CostReservations(targets []CostTarget, cost Money) ([]Reservation, error) {
	if cost.Amount <= 0 {
		return nil, nil
	}
	if cost.Currency == "" {
		return nil, fmt.Errorf("%w: a cost without a currency", ErrBadReservation)
	}
	var rs []Reservation
	for _, t := range targets {
		c := t.Counter(cost.Currency)
		if c == "" {
			continue // currency_mismatch: no FX
		}
		r := Reservation{Budget: t.Budget, Scope: t.Scope, Counter: c, Window: t.Window, WindowStart: t.WindowStart.UTC(), Amount: cost.Amount}
		if err := CheckReservation(r); err != nil {
			return nil, err
		}
		rs = append(rs, r)
	}
	return rs, nil
}

// Snapshot implements UsageStore.
func (m *MemUsage) Snapshot(_ context.Context, in SnapshotRequest) (UsageSnapshot, error) {
	if in.Org == "" {
		return UsageSnapshot{}, errNoOrg
	}
	ks, cur, err := SnapshotKeys(in)
	if err != nil {
		return UsageSnapshot{}, err
	}
	vals := make([]int64, len(ks))
	m.mu.Lock()
	for i, k := range ks {
		vals[i] = m.rows[keyOf(in.Org, k.Scope, k.Counter, k.Window, k.WindowStart)]
	}
	m.mu.Unlock()
	return FillSnapshot(ks, cur, vals), nil
}

// Reserve implements UsageStore: all or nothing, conditional rows checked against their limit.
func (m *MemUsage) Reserve(_ context.Context, org string, rs []Reservation) error {
	if org == "" {
		return errNoOrg
	}
	for _, r := range rs {
		if err := CheckReservation(r); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// An amount larger than its own limit can never fit: refuse before writing anything.
	for _, r := range rs {
		if r.Limit > 0 && r.Amount > r.Limit {
			used := m.rows[keyOf(org, r.Scope, r.Counter, r.Window, r.WindowStart)]
			return &ExceededError{BudgetID: r.Budget, Used: used, Limit: r.Limit, ResetsAt: r.ResetsAt}
		}
	}
	staged := map[memKey]int64{}
	for _, r := range rs {
		k := keyOf(org, r.Scope, r.Counter, r.Window, r.WindowStart)
		cur, ok := staged[k]
		if !ok {
			cur = m.rows[k]
		}
		if r.Limit > 0 && cur+r.Amount > r.Limit {
			return &ExceededError{BudgetID: r.Budget, Used: cur, Limit: r.Limit, ResetsAt: r.ResetsAt}
		}
		staged[k] = cur + r.Amount
	}
	for k, v := range staged {
		m.rows[k] = v
	}
	return nil
}

// Release implements UsageStore: it floors at 0 and never creates a row.
func (m *MemUsage) Release(_ context.Context, org string, rs []Reservation) error {
	if org == "" {
		return errNoOrg
	}
	for _, r := range rs {
		if err := CheckReservation(r); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rs {
		k := keyOf(org, r.Scope, r.Counter, r.Window, r.WindowStart)
		v, ok := m.rows[k]
		if !ok {
			continue
		}
		m.rows[k] = max(v-r.Amount, 0)
	}
	return nil
}

// RecordCost implements UsageStore.
func (m *MemUsage) RecordCost(ctx context.Context, org string, targets []CostTarget, cost Money) error {
	if org == "" {
		return errNoOrg
	}
	rs, err := CostReservations(targets, cost)
	if err != nil || len(rs) == 0 {
		return err
	}
	return m.Reserve(ctx, org, rs)
}

// Prune implements UsageStore.
func (m *MemUsage) Prune(_ context.Context, now time.Time) (int64, error) {
	cut := map[string]int64{
		WindowMinute: now.Add(-KeepMinute).UnixNano(),
		WindowHour:   now.Add(-KeepHour).UnixNano(),
		WindowDay:    now.Add(-KeepDay).UnixNano(),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k := range m.rows {
		if k.start < cut[k.window] {
			delete(m.rows, k)
			n++
		}
	}
	return n, nil
}
