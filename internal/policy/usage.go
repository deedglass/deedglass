package policy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Usage counters and budgets (S11 §3.6, docs/BUDGETS.md). Postgres is authoritative: every
// check-and-increment is one conditional upsert (internal/store/policystore). This file holds the
// pure types and window maths that both stores and the evaluator share. policy can't import
// internal/ids (archtest), so org and agent ids are plain strings here.

// Windows.
const (
	WindowMinute = "minute"
	WindowHour   = "hour"
	WindowDay    = "day"
)

// Scopes and counters (contract §4: usage_counters(org_id, scope, counter, window_kind, window_start, value)).
const (
	ScopeOrg     = "org" // a per: org budget; otherwise the scope is the agent id
	CounterCalls = "calls"
	PerAgent     = "agent"
	PerOrg       = "org"
)

// SpendCounter is the standard per-agent spend counter for one currency.
func SpendCounter(currency string) string { return "spend:" + currency }

// BudgetCounter is a policy budget's counter.
func BudgetCounter(id string) string { return "budget:" + id }

// Agent budget ids (S08's budgets on the agent row), as the trace and Decision name them.
const (
	AgentCallsPerMinute = "agent.calls_per_minute"
	AgentCallsPerDay    = "agent.calls_per_day"
	AgentSpendPerDay    = "agent.spend_per_day"
)

// UsageSnapshot is usage in the current windows, as Input.usage carries it. It is the CEL-visible
// shape (UsageFacts): calls per agent for the minute, hour and org-local day; spend per agent for
// the day in one currency; and each policy budget's value in its own current window.
type UsageSnapshot = UsageFacts

// AgentBudgets are the limits S08 stores on the agent row. They are not visible to CEL.
type AgentBudgets struct {
	CallsPerMinute *int64 `json:"calls_per_minute,omitempty"`
	CallsPerDay    *int64 `json:"calls_per_day,omitempty"`
	SpendPerDay    *Money `json:"spend_per_day,omitempty"`
}

// BudgetRef names one policy budget's counter for a snapshot.
type BudgetRef struct {
	ID     string `json:"id"`
	Per    string `json:"per"`    // agent | org
	Window string `json:"window"` // minute | hour | day
}

// SnapshotRequest asks a store for the counters one evaluation reads.
type SnapshotRequest struct {
	Org      string         // org_…
	Agent    string         // agt_…
	Location *time.Location // the org's time zone; nil means UTC
	At       time.Time      // the decision instant (Input.Time)
	Currency string         // the spend counter that fills SpendToday; "" means "usd"
	Budgets  []BudgetRef
}

// SnapshotRequestFor builds the request for every budget of c.
func SnapshotRequestFor(c *Compiled, org, agent string, loc *time.Location, at time.Time, currency string) SnapshotRequest {
	r := SnapshotRequest{Org: org, Agent: agent, Location: loc, At: at, Currency: currency}
	if c != nil {
		for _, b := range c.Budgets {
			r.Budgets = append(r.Budgets, BudgetRef{ID: b.ID, Per: b.Per, Window: b.Window})
		}
	}
	return r
}

// Reservation is one counter increment the caller makes after an allow or require_approval.
// Limit 0 means unconditional (a standard counter with no agent budget on it).
type Reservation struct {
	Budget      string    `json:"budget,omitempty"` // budget id, an agent.* id, or "" for a plain standard counter
	Scope       string    `json:"scope"`            // agt_… or "org"
	Counter     string    `json:"counter"`          // calls | spend:<currency> | budget:<id>
	Window      string    `json:"window"`           // minute | hour | day
	WindowStart time.Time `json:"window_start"`     // UTC
	Amount      int64     `json:"amount"`
	Limit       int64     `json:"limit,omitempty"`
	ResetsAt    time.Time `json:"resets_at"` // the end of the window, for Retry-After
}

// CostTarget is one counter that the reported cost of an action adds to after it runs. Budget ""
// is the agent's standard spend counter, which takes any currency (spend:<cost currency>); a
// budget target counts only costs in its own currency (no FX; docs/BUDGETS.md).
type CostTarget struct {
	Budget      string    `json:"budget,omitempty"`
	Scope       string    `json:"scope"`
	Window      string    `json:"window"`
	WindowStart time.Time `json:"window_start"`
	Currency    string    `json:"currency,omitempty"` // "" for the standard counter
}

// Counter is the counter a cost in currency cur adds to, or "" when the currencies don't match.
func (t CostTarget) Counter(cur string) string {
	if t.Budget == "" || t.Budget == AgentSpendPerDay {
		return SpendCounter(cur)
	}
	if t.Currency != "" && t.Currency != cur {
		return ""
	}
	return BudgetCounter(t.Budget)
}

// BudgetHit says which budget denied an action, with exact numbers for the message and Retry-After.
type BudgetHit struct {
	ID     string `json:"id"`
	Per    string `json:"per"`
	Window string `json:"window"`
	Limit  int64  `json:"limit"`
	Used   int64  `json:"used"`
	// Projected is used plus this action's measured amount, for a measured budget the action would
	// pass (DL-025); 0 otherwise.
	Projected   int64     `json:"projected,omitzero"`
	Unit        string    `json:"unit"` // "calls", or a currency (minor units)
	ResetsAt    time.Time `json:"resets_at"`
	RetryAfterS int64     `json:"retry_after_s"`
}

// ExceededError is what Reserve returns when a conditional increment would pass its limit. The
// gate turns it into a deny with code budget_exceeded (S13).
type ExceededError struct {
	BudgetID string
	Used     int64
	Limit    int64
	ResetsAt time.Time
}

func (e *ExceededError) Error() string {
	return fmt.Sprintf("budget %q reached: %d of %d", e.BudgetID, e.Used, e.Limit)
}

// ErrBadReservation is returned for a reservation a store refuses to write (a negative amount, an
// unknown window, an empty scope or counter).
var ErrBadReservation = errors.New("policy: invalid reservation")

// UsageStore keeps the counters. Both implementations pass internal/policy/usagetest.
type UsageStore interface {
	// Snapshot reads the counters one evaluation needs, in one query.
	Snapshot(ctx context.Context, in SnapshotRequest) (UsageSnapshot, error)
	// Reserve adds every reservation, all or nothing. A conditional one that would pass its limit
	// rolls everything back and returns *ExceededError.
	Reserve(ctx context.Context, org string, rs []Reservation) error
	// Release takes reservations back, flooring at 0. The gate calls it once per action, from the
	// state transition that ends the action without executing (S13, S22).
	Release(ctx context.Context, org string, rs []Reservation) error
	// RecordCost adds a reported cost to each target whose currency matches.
	RecordCost(ctx context.Context, org string, targets []CostTarget, cost Money) error
	// Prune deletes windows that can no longer be read: minute rows older than 2 h, hour rows older
	// than 3 d, day rows older than 400 d. It returns the number of rows deleted.
	Prune(ctx context.Context, now time.Time) (int64, error)
}

// Retention of Prune.
const (
	KeepMinute = 2 * time.Hour
	KeepHour   = 3 * 24 * time.Hour
	KeepDay    = 400 * 24 * time.Hour
)

// WindowStart returns the start of the window that contains at: UTC floors for minute and hour;
// the org's local midnight for day, returned in UTC.
func WindowStart(window string, at time.Time, loc *time.Location) time.Time {
	switch window {
	case WindowMinute:
		return at.UTC().Truncate(time.Minute)
	case WindowHour:
		return at.UTC().Truncate(time.Hour)
	default:
		if loc == nil {
			loc = time.UTC
		}
		l := at.In(loc)
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc).UTC()
	}
}

// WindowEnd returns when the window that contains at ends (the next window's start).
func WindowEnd(window string, at time.Time, loc *time.Location) time.Time {
	switch window {
	case WindowMinute:
		return WindowStart(window, at, loc).Add(time.Minute)
	case WindowHour:
		return WindowStart(window, at, loc).Add(time.Hour)
	default:
		if loc == nil {
			loc = time.UTC
		}
		l := at.In(loc)
		// AddDate handles 23- and 25-hour days across a DST change.
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1).UTC()
	}
}

// ValidWindow reports whether w is a window name.
func ValidWindow(w string) bool { return w == WindowMinute || w == WindowHour || w == WindowDay }

// CheckReservation validates one reservation before a store writes it. The counter must have the
// shape the table's CHECK constraint accepts, so both stores refuse the same rows.
func CheckReservation(r Reservation) error {
	if r.Scope == "" || !ValidCounter(r.Counter) || !ValidWindow(r.Window) || r.Amount < 0 || r.Limit < 0 || r.WindowStart.IsZero() {
		return fmt.Errorf("%w: %+v", ErrBadReservation, r)
	}
	return nil
}

// ValidCounter mirrors usage_counters' CHECK: calls, spend:[a-z]{3} or budget:[a-z0-9_-]{1,64}.
func ValidCounter(c string) bool {
	switch {
	case c == CounterCalls:
		return true
	case strings.HasPrefix(c, "spend:"):
		cur := c[len("spend:"):]
		if len(cur) != 3 {
			return false
		}
		for i := 0; i < 3; i++ {
			if cur[i] < 'a' || cur[i] > 'z' {
				return false
			}
		}
		return true
	case strings.HasPrefix(c, "budget:"):
		id := c[len("budget:"):]
		if len(id) < 1 || len(id) > 64 {
			return false
		}
		for i := 0; i < len(id); i++ {
			ch := id[i]
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
				return false
			}
		}
		return true
	}
	return false
}

var (
	locMu    sync.RWMutex
	locCache = map[string]*time.Location{}
)

// LoadLocation is time.LoadLocation with a cache: evaluation must not read zoneinfo every call.
// "" is UTC. The binary embeds the time zone database (time/tzdata in cmd/deedglass).
func LoadLocation(name string) (*time.Location, error) {
	if name == "" || name == "UTC" {
		return time.UTC, nil
	}
	locMu.RLock()
	l, ok := locCache[name]
	locMu.RUnlock()
	if ok {
		return l, nil
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	locMu.Lock()
	if len(locCache) < 1024 {
		locCache[name] = l
	}
	locMu.Unlock()
	return l, nil
}

// FillLocalUsage completes a usage snapshot for local evaluation (deedglass policy evaluate and
// S12's test runner), where no store is attached: a per-agent calls budget with no value given
// takes the agent's standard calls counter for its window (calls_minute, calls_hour or
// calls_today). A server never calls it; its store reads each budget's own counter.
func FillLocalUsage(c *Compiled, u UsageSnapshot) UsageSnapshot {
	if c == nil || len(c.Budgets) == 0 {
		return u
	}
	m := make(map[string]int64, len(u.Budgets)+len(c.Budgets))
	for k, v := range u.Budgets {
		m[k] = v
	}
	for _, b := range c.Budgets {
		if _, ok := m[b.ID]; ok || b.MaxSpend != nil || b.Per == PerOrg {
			continue
		}
		switch b.Window {
		case WindowMinute:
			m[b.ID] = u.CallsMinute
		case WindowHour:
			m[b.ID] = u.CallsHour
		case WindowDay:
			m[b.ID] = u.CallsToday
		}
	}
	u.Budgets = m
	return u
}
