package learn

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// Stats (S27 §3.6): deterministic SQL counts and percentiles over actions joined to approvals, by
// decision time; the rates are computed here from the counts and rounded to 4 decimals, with the
// counts always returned next to them. Every number states its window.

// StatsQuery is GET /v1/insights/stats.
type StatsQuery struct {
	From, To time.Time
	By       string // rule | tool | agent | none
	Agent    string // agt_…, or ""
	Tool     string // a tool name or a glob with *, or ""
	Compare  bool   // compare=previous
	// Relative is the window parameter's length (7d, 30d) when the window ends now; the 60 s stats
	// cache keys on it instead of the exact instants, so repeats within a minute share one answer.
	Relative string
}

// Groupings.
const (
	ByRule  = "rule"
	ByTool  = "tool"
	ByAgent = "agent"
	ByNone  = "none"
)

// GroupKey names a group.
type GroupKey struct {
	RuleID    *string `json:"rule_id,omitempty"`
	Tool      *string `json:"tool,omitempty"`
	Agent     *string `json:"agent,omitempty"`
	AgentName *string `json:"name,omitempty"`
}

// StatsRow is one group's raw aggregates, as the store reads them.
type StatsRow struct {
	Key                                                               string // rule id, tool or agent id; "" for by=none
	AgentName                                                         string
	Versions                                                          []int
	Actions, Allow, Deny, RequireApproval, StopDecided, DenyNonStop   int64
	Approved, ApprovalDenied, Expired, Cancelled, Pending             int64
	WaitP50, WaitP95                                                  *float64 // seconds: approvals.decided_at − actions.pending_approval_at
	Verified, Mismatch, Unverifiable, NoVerdict, L0, L1, L2, Executed int64
	LatencyP50, LatencyP95                                            *float64
	Costs                                                             []CostRow
}

// CostRow is one currency's cost in a group (integer minor units).
type CostRow struct {
	Currency string  `json:"currency"`
	Total    int64   `json:"total"`
	P50      float64 `json:"p50"`
	P95      float64 `json:"p95"`
}

// SeriesRow is one org-local day.
type SeriesRow struct {
	Date         string `json:"date"`
	Actions      int64  `json:"actions"`
	Denied       int64  `json:"denied"`
	Verified     int64  `json:"verified"`
	Mismatch     int64  `json:"mismatch"`
	Unverifiable int64  `json:"unverifiable"`
}

// DayCounter is one usage counter's value on one org-local day (usage_counters, window day).
type DayCounter struct {
	Scope, Counter string
	Day            time.Time
	Value          int64
}

// AgentBudgetRow is an agent's S08 budgets (the agent row's JSON).
type AgentBudgetRow struct {
	ID, Name string
	Budgets  json.RawMessage
}

// StatsStore is what stats read (store/learnstore.Store).
type StatsStore interface {
	// StatsGroups is one grouping's rows and the window's totals (by=none: one row, equal to the totals).
	StatsGroups(ctx context.Context, org ids.OrgID, q StatsQuery, tz string) ([]StatsRow, StatsRow, error)
	StatsSeries(ctx context.Context, org ids.OrgID, q StatsQuery, tz string) ([]SeriesRow, error)
	DayCounters(ctx context.Context, org ids.OrgID, from, to time.Time, tz string) ([]DayCounter, error)
	AgentBudgets(ctx context.Context, org ids.OrgID) ([]AgentBudgetRow, error)
	// EnforcedSource is the org's enforced version and its source ("" and nil when only the builtin
	// default-deny decides).
	EnforcedSource(ctx context.Context, org ids.OrgID) (*VersionInfo, []byte, error)
}

// StatsObject is the insights_stats object.
type StatsObject struct {
	Object      string       `json:"object"` // insights_stats
	Window      StatsWindow  `json:"window"`
	Timezone    string       `json:"timezone"`
	By          string       `json:"by"`
	Filters     StatsFilters `json:"filters"`
	Totals      Group        `json:"totals"`
	Groups      []Group      `json:"groups"`
	Budgets     []BudgetUse  `json:"budgets"`
	Series      []SeriesRow  `json:"series"`
	GeneratedAt time.Time    `json:"generated_at"`
	Empty       *string      `json:"empty"` // the sentence when the window has no actions
}

// StatsWindow is the window with its length in days.
type StatsWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Days float64   `json:"days"`
}

// StatsFilters echoes the filters.
type StatsFilters struct {
	Agent *string `json:"agent"`
	Tool  *string `json:"tool"`
}

// Group is one group's metrics.
type Group struct {
	Key              *GroupKey        `json:"key,omitempty"`
	Versions         []int            `json:"versions,omitempty"`
	Actions          int64            `json:"actions"`
	Decisions        Decisions        `json:"decisions"`
	Approval         ApprovalStats    `json:"approval"`
	DenyRate         float64          `json:"deny_rate"`
	Verdicts         VerdictCounts    `json:"verdicts"`
	Levels           LevelCounts      `json:"levels"`
	Executed         int64            `json:"executed"`
	MismatchRate     float64          `json:"mismatch_rate"`
	UnverifiableRate float64          `json:"unverifiable_rate"`
	LatencyMS        Percentiles      `json:"latency_ms"`
	Cost             []CostRow        `json:"cost"`
	Trend            map[string]Trend `json:"trend,omitempty"`
}

// Decisions counts the recorded decisions.
type Decisions struct {
	Allow           int64 `json:"allow"`
	Deny            int64 `json:"deny"`
	RequireApproval int64 `json:"require_approval"`
}

// ApprovalStats: rate = approved / (approved + denied); waits in seconds.
type ApprovalStats struct {
	Requested     int64    `json:"requested"`
	Approved      int64    `json:"approved"`
	Denied        int64    `json:"denied"`
	Expired       int64    `json:"expired"`
	Cancelled     int64    `json:"cancelled"`
	Pending       int64    `json:"pending"`
	Rate          *float64 `json:"rate"`
	MedianSeconds *int64   `json:"median_seconds"`
	P95Seconds    *int64   `json:"p95_seconds"`
}

// VerdictCounts counts verdicts; None is actions with no verdict (not executed).
type VerdictCounts struct {
	Verified     int64 `json:"VERIFIED"`
	Mismatch     int64 `json:"MISMATCH"`
	Unverifiable int64 `json:"UNVERIFIABLE"`
	None         int64 `json:"none"`
}

// LevelCounts counts the witnessed levels.
type LevelCounts struct {
	L0 int64 `json:"L0"`
	L1 int64 `json:"L1"`
	L2 int64 `json:"L2"`
}

// Percentiles in milliseconds.
type Percentiles struct {
	P50 *int64 `json:"p50"`
	P95 *int64 `json:"p95"`
}

// Trend compares with the previous equal window: delta_pct for counts, delta_pp for rates; null
// when the previous window has fewer than 20 actions.
type Trend struct {
	DeltaPct *float64 `json:"delta_pct,omitempty"`
	DeltaPP  *float64 `json:"delta_pp,omitempty"`
	Previous *float64 `json:"previous"`
}

// MinTrendActions is the previous window's floor for a trend.
const MinTrendActions = 20

// BudgetUse is one budget's use over the window's days.
type BudgetUse struct {
	Agent          *string     `json:"agent"` // null for a per-org budget
	AgentName      *string     `json:"name,omitempty"`
	Budget         string      `json:"budget"` // a policy budget id, or agent.calls_per_day / agent.spend_per_day
	Kind           string      `json:"kind"`   // policy | agent
	Limit          BudgetLimit `json:"limit"`
	MaxUtilisation float64     `json:"max_utilisation"`
	DaysOver80     int         `json:"days_over_80"`
	Days           []BudgetDay `json:"days"`
}

// BudgetLimit is a calls or money limit.
type BudgetLimit struct {
	Calls    *int64  `json:"calls,omitempty"`
	Amount   *int64  `json:"amount,omitempty"`
	Currency *string `json:"currency,omitempty"`
}

// BudgetDay is one day's use.
type BudgetDay struct {
	Date        string  `json:"date"`
	Used        int64   `json:"used"`
	Utilisation float64 `json:"utilisation"`
}

// Rate rounds n/d to 4 decimals (0 when d is 0).
func Rate(n, d int64) float64 {
	if d <= 0 {
		return 0
	}
	return round4(float64(n) / float64(d))
}

// BuildGroup turns one row of raw aggregates into a group's metrics.
func BuildGroup(r StatsRow, by string) Group {
	g := Group{Actions: r.Actions, Versions: r.Versions,
		Decisions: Decisions{Allow: r.Allow, Deny: r.Deny, RequireApproval: r.RequireApproval},
		Approval: ApprovalStats{Requested: r.RequireApproval, Approved: r.Approved, Denied: r.ApprovalDenied, Expired: r.Expired,
			Cancelled: r.Cancelled, Pending: r.Pending},
		DenyRate: Rate(r.DenyNonStop, r.Actions-r.StopDecided),
		Verdicts: VerdictCounts{Verified: r.Verified, Mismatch: r.Mismatch, Unverifiable: r.Unverifiable, None: r.NoVerdict},
		Levels:   LevelCounts{L0: r.L0, L1: r.L1, L2: r.L2}, Executed: r.Executed,
		MismatchRate: Rate(r.Mismatch, r.Executed), UnverifiableRate: Rate(r.Unverifiable, r.Executed), Cost: r.Costs}
	if g.Cost == nil {
		g.Cost = []CostRow{}
	}
	if decided := r.Approved + r.ApprovalDenied; decided > 0 {
		x := Rate(r.Approved, decided)
		g.Approval.Rate = &x
	}
	g.Approval.MedianSeconds, g.Approval.P95Seconds = roundPtr(r.WaitP50), roundPtr(r.WaitP95)
	g.LatencyMS = Percentiles{P50: roundPtr(r.LatencyP50), P95: roundPtr(r.LatencyP95)}
	switch by {
	case ByRule:
		k := r.Key
		g.Key = &GroupKey{RuleID: &k}
	case ByTool:
		k := r.Key
		g.Key = &GroupKey{Tool: &k}
	case ByAgent:
		k, n := r.Key, r.AgentName
		g.Key = &GroupKey{Agent: &k, AgentName: &n}
	}
	return g
}

func roundPtr(f *float64) *int64 {
	if f == nil || math.IsNaN(*f) {
		return nil
	}
	n := int64(math.Round(*f))
	return &n
}

// TrendOf compares cur with prev (nil when prev has fewer than MinTrendActions actions).
func TrendOf(cur, prev Group) map[string]Trend {
	t := map[string]Trend{}
	if prev.Actions < MinTrendActions {
		t["actions"] = Trend{}
		return t
	}
	pct := func(c, p int64) Trend {
		pp := float64(p)
		x := round4(float64(c-p) / float64(p) * 100)
		return Trend{DeltaPct: &x, Previous: &pp}
	}
	pp := func(c, p float64) Trend {
		x := round4((c - p) * 100)
		prev := p
		return Trend{DeltaPP: &x, Previous: &prev}
	}
	t["actions"] = pct(cur.Actions, prev.Actions)
	t["deny_rate"] = pp(cur.DenyRate, prev.DenyRate)
	t["mismatch_rate"] = pp(cur.MismatchRate, prev.MismatchRate)
	t["unverifiable_rate"] = pp(cur.UnverifiableRate, prev.UnverifiableRate)
	if cur.Approval.Rate != nil && prev.Approval.Rate != nil {
		t["approval_rate"] = pp(*cur.Approval.Rate, *prev.Approval.Rate)
	}
	return t
}

// SumRows adds rows into one (the totals); percentiles can't be summed, so the store returns a
// by=none row for them and this keeps only the counts.
func SumRows(rows []StatsRow) StatsRow {
	var t StatsRow
	costs := map[string]*CostRow{}
	for _, r := range rows {
		t.Actions += r.Actions
		t.Allow += r.Allow
		t.Deny += r.Deny
		t.RequireApproval += r.RequireApproval
		t.StopDecided += r.StopDecided
		t.DenyNonStop += r.DenyNonStop
		t.Approved += r.Approved
		t.ApprovalDenied += r.ApprovalDenied
		t.Expired += r.Expired
		t.Cancelled += r.Cancelled
		t.Pending += r.Pending
		t.Verified += r.Verified
		t.Mismatch += r.Mismatch
		t.Unverifiable += r.Unverifiable
		t.NoVerdict += r.NoVerdict
		t.L0 += r.L0
		t.L1 += r.L1
		t.L2 += r.L2
		t.Executed += r.Executed
		for _, c := range r.Costs {
			x := costs[c.Currency]
			if x == nil {
				x = &CostRow{Currency: c.Currency}
				costs[c.Currency] = x
			}
			x.Total += c.Total
		}
	}
	return t
}

// BudgetUses computes each budget's daily use from the day counters and the limits: the agents'
// own budgets (S08) and the enforced version's day budgets (S11). Minute and hour budgets have no
// daily picture and are left out.
func BudgetUses(counters []DayCounter, agents []AgentBudgetRow, enforced *policy.Compiled, loc *time.Location) []BudgetUse {
	type key struct{ scope, counter string }
	byKey := map[key][]DayCounter{}
	for _, c := range counters {
		k := key{c.Scope, c.Counter}
		byKey[k] = append(byKey[k], c)
	}
	names := map[string]string{}
	for _, a := range agents {
		names[a.ID] = a.Name
	}
	var out []BudgetUse
	use := func(b BudgetUse, k key, limit int64) {
		if limit <= 0 {
			return
		}
		b.Days = []BudgetDay{}
		for _, c := range byKey[k] {
			u := Rate(c.Value, limit)
			b.Days = append(b.Days, BudgetDay{Date: c.Day.In(loc).Format("2006-01-02"), Used: c.Value, Utilisation: u})
			b.MaxUtilisation = max(b.MaxUtilisation, u)
			if u >= 0.8 {
				b.DaysOver80++
			}
		}
		slices.SortFunc(b.Days, func(x, y BudgetDay) int { return compareStrings(x.Date, y.Date) })
		out = append(out, b)
	}
	for _, a := range agents {
		if len(a.Budgets) == 0 {
			continue
		}
		var ab policy.AgentBudgets
		if json.Unmarshal(a.Budgets, &ab) != nil {
			continue
		}
		id, name := a.ID, a.Name
		if ab.CallsPerDay != nil {
			n := *ab.CallsPerDay
			use(BudgetUse{Agent: &id, AgentName: &name, Budget: "agent.calls_per_day", Kind: "agent", Limit: BudgetLimit{Calls: &n}},
				key{a.ID, policy.CounterCalls}, n)
		}
		if ab.SpendPerDay != nil {
			amt, cur := ab.SpendPerDay.Amount, ab.SpendPerDay.Currency
			use(BudgetUse{Agent: &id, AgentName: &name, Budget: "agent.spend_per_day", Kind: "agent", Limit: BudgetLimit{Amount: &amt, Currency: &cur}},
				key{a.ID, policy.SpendCounter(cur)}, amt)
		}
	}
	if enforced != nil {
		for _, b := range enforced.Budgets {
			if b.Window != "day" {
				continue
			}
			limit := b.MaxCalls
			lim := BudgetLimit{}
			if b.MaxSpend != nil {
				amt, cur := b.MaxSpend.Amount, b.MaxSpend.Currency
				limit, lim.Amount, lim.Currency = amt, &amt, &cur
			} else {
				n := b.MaxCalls
				lim.Calls = &n
			}
			counter := policy.BudgetCounter(b.ID)
			if b.Per == policy.ScopeOrg {
				use(BudgetUse{Budget: b.ID, Kind: "policy", Limit: lim}, key{policy.ScopeOrg, counter}, limit)
				continue
			}
			for scope := range byKey {
				if scope.counter != counter || scope.scope == policy.ScopeOrg {
					continue
				}
				id := scope.scope
				name := names[id]
				use(BudgetUse{Agent: &id, AgentName: &name, Budget: b.ID, Kind: "policy", Limit: lim}, scope, limit)
			}
		}
	}
	slices.SortFunc(out, func(x, y BudgetUse) int {
		if c := compareStrings(x.Budget, y.Budget); c != 0 {
			return c
		}
		return compareStrings(deref(x.Agent), deref(y.Agent))
	})
	if out == nil {
		out = []BudgetUse{}
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ToolGlobSQL turns a tool filter with * into a LIKE pattern (backslash-escaped); "" stays "".
func ToolGlobSQL(glob string) string {
	if glob == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteByte('%')
		case '%', '_', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
