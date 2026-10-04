package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// GET /v1/overview (S25 §3.2): the Console's first screen as numbers. Everything here is read in
// one read-only REPEATABLE READ org transaction (RLS on) with a 2 s statement timeout, from fixed
// SQL with bound values. docs/OVERVIEW_METRICS.md defines every number.

// OverviewWindow is one of the two windows the Overview shows.
type OverviewWindow struct {
	Name    string        // "24h" | "7d"
	Span    time.Duration // the window's length
	Bucket  time.Duration // one series bucket
	Buckets int           // Span / Bucket
}

// The Overview's windows: 24 hourly buckets, or 28 six-hour buckets.
var (
	Window24h = OverviewWindow{Name: "24h", Span: 24 * time.Hour, Bucket: time.Hour, Buckets: 24}
	Window7d  = OverviewWindow{Name: "7d", Span: 7 * 24 * time.Hour, Bucket: 6 * time.Hour, Buckets: 28}
)

// OverviewWindowOf returns the window named name ("24h" or "7d").
func OverviewWindowOf(name string) (OverviewWindow, bool) {
	switch name {
	case Window24h.Name:
		return Window24h, true
	case Window7d.Name:
		return Window7d, true
	}
	return OverviewWindow{}, false
}

// Bounds returns the window ending at the first bucket boundary at or after now (UTC; buckets
// are aligned on UTC multiples of the bucket, so the current partial bucket is the last one).
// Counts take from <= created_at < to.
func (w OverviewWindow) Bounds(now time.Time) (from, to time.Time) {
	t := now.UTC()
	to = t.Truncate(w.Bucket)
	if to.Before(t) {
		to = to.Add(w.Bucket)
	}
	return to.Add(-w.Span), to
}

// BucketIndex is the series index of t in the window that starts at from, or -1 outside it.
func (w OverviewWindow) BucketIndex(from, t time.Time) int {
	if t.Before(from) {
		return -1
	}
	i := int(t.Sub(from) / w.Bucket)
	if i >= w.Buckets {
		return -1
	}
	return i
}

// Overview thresholds (docs/OVERVIEW_METRICS.md).
const (
	OverviewAgentsShown = 8 // the agents strip
	AttentionMax        = 8 // attention items in one answer
	// After the system items, slots kept for approval_expiring and budget_near before mismatches
	// fill the rest (S26, DL-041), so a burst of mismatches never hides an expiring approval.
	AttentionReservedApprovals = 3
	AttentionReservedBudgets   = 2
	ApprovalExpiringSoon       = 15 * time.Minute // approvals.expiring_soon and approval_expiring
	MismatchLookback           = 24 * time.Hour   // mismatch items, whatever the window
	BudgetNearPercent          = 80               // budget_near: at least this share of a budget used
	overviewTimeout            = "2s"             // statement_timeout for the Overview's reads
	overviewBudgetAgents       = 500              // agents with budgets checked for budget_near
)

// Attention kinds, in priority order, and severities.
const (
	AttentionChainBroken     = "chain_broken"
	AttentionOrgStopped      = "org_stopped"
	AttentionSlotInvalidated = "slot_invalidated"
	AttentionSlotLagging     = "slot_lagging"
	AttentionMismatch        = "mismatch"
	AttentionApprovalExpiry  = "approval_expiring"
	AttentionBudgetNear      = "budget_near"

	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// AttentionKinds lists the kinds in priority order.
var AttentionKinds = []string{AttentionChainBroken, AttentionOrgStopped, AttentionSlotInvalidated, AttentionSlotLagging,
	AttentionMismatch, AttentionApprovalExpiry, AttentionBudgetNear}

// Overview is the overview object.
type Overview struct {
	Object       string               `json:"object"` // "overview"
	Window       string               `json:"window"`
	From         Time                 `json:"from"`
	To           Time                 `json:"to"`
	Timezone     string               `json:"timezone"`
	GeneratedAt  Time                 `json:"generated_at"`
	Actions      OverviewCount        `json:"actions"`
	Blocked      OverviewCount        `json:"blocked"`
	Approvals    OverviewApprovals    `json:"approvals"`
	Verification OverviewVerification `json:"verification"`
	Agents       OverviewAgents       `json:"agents"`
	Attention    []AttentionItem      `json:"attention"`
}

// OverviewCount is a total for the window, the previous window's total, and the series.
type OverviewCount struct {
	Total    int64   `json:"total"`
	Previous int64   `json:"previous"`
	Series   []int64 `json:"series"`
}

// OverviewApprovals counts pending approvals (whatever the window).
type OverviewApprovals struct {
	Pending       int64 `json:"pending"`
	ExpiringSoon  int64 `json:"expiring_soon"`
	NextExpiresAt *Time `json:"next_expires_at"`
}

// OverviewVerification counts the window's actions by witness verdict and level.
type OverviewVerification struct {
	Verified     int64                 `json:"verified"`
	Mismatch     int64                 `json:"mismatch"`
	Unverifiable int64                 `json:"unverifiable"`
	NotWitnessed int64                 `json:"not_witnessed"`
	ByLevel      OverviewLevels        `json:"by_level"`
	Series       OverviewVerdictSeries `json:"series"`
}

// OverviewLevels counts witnessed actions by level.
type OverviewLevels struct {
	L0 int64 `json:"L0"`
	L1 int64 `json:"L1"`
	L2 int64 `json:"L2"`
}

// OverviewVerdictSeries is one series per verdict.
type OverviewVerdictSeries struct {
	Verified     []int64 `json:"verified"`
	Mismatch     []int64 `json:"mismatch"`
	Unverifiable []int64 `json:"unverifiable"`
}

// OverviewAgents is the agents strip, a list.
type OverviewAgents struct {
	Object  string          `json:"object"` // "list"
	HasMore bool            `json:"has_more"`
	Data    []OverviewAgent `json:"data"`
}

// OverviewAgent is one agent in the strip.
type OverviewAgent struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Status       string             `json:"status"`
	LastActionAt *Time              `json:"last_action_at"`
	Usage        OverviewAgentUsage `json:"usage"`
	Budgets      AgentBudgets       `json:"budgets"`
}

// OverviewAgentUsage is what the agent used today (the org's time zone).
type OverviewAgentUsage struct {
	CallsToday int64  `json:"calls_today"`
	SpendToday *Money `json:"spend_today"`
}

// AgentBudgets are the limits on the agent row (S08); a limit that isn't set is left out.
type AgentBudgets struct {
	CallsPerMinute *int64 `json:"calls_per_minute,omitempty"`
	CallsPerDay    *int64 `json:"calls_per_day,omitempty"`
	SpendPerDay    *Money `json:"spend_per_day,omitempty"`
}

// AttentionItem is one thing that needs a person: a kind and its data, never a sentence (the
// Console words it), except a mismatch's witness summary, verbatim.
type AttentionItem struct {
	Object   string         `json:"object"` // "attention_item"
	Kind     string         `json:"kind"`
	Severity string         `json:"severity"`
	At       Time           `json:"at"`
	Ref      *string        `json:"ref"`
	Data     map[string]any `json:"data"`
}

// AgentUsageRequest asks for one agent's standard counters, spend in Currency ("" means usd).
type AgentUsageRequest struct {
	Agent    string
	Currency string
}

// AgentUsage is one agent's standard counters in the windows the policy budgets use.
type AgentUsage struct {
	CallsMinute, CallsHour, CallsToday, SpendToday int64
	Currency                                       string
}

// UsageReader reads several agents' counters inside the Overview's transaction, in one statement,
// for the org's time zone at the instant at (S11's usage store; the ledger can't import it).
type UsageReader func(ctx context.Context, tx pgx.Tx, org ids.OrgID, timezone string, at time.Time, reqs []AgentUsageRequest) ([]AgentUsage, error)

// OverviewOptions are one Overview's inputs.
type OverviewOptions struct {
	Window OverviewWindow
	Now    time.Time
	// Usage reads the agents' counters; nil reads none (every count 0).
	Usage UsageReader
	// SlotLagAlertBytes is DEEDGLASS_SLOT_LAG_ALERT_MB in bytes; 0 or less: no slot_lagging.
	SlotLagAlertBytes int64
	// Chain is the latest background check of the last 24 h of deeds; nil when none has finished.
	Chain *ChainCheck
}

// Overview reads org's overview.
func (l *Ledger) Overview(ctx context.Context, org ids.OrgID, o OverviewOptions) (*Overview, error) {
	if o.Window.Buckets == 0 {
		o.Window = Window24h
	}
	if o.Now.IsZero() {
		o.Now = l.now()
	}
	now := o.Now.UTC()
	from, to := o.Window.Bounds(now)
	n := o.Window.Buckets
	ov := &Overview{Object: "overview", Window: o.Window.Name, From: Time(from), To: Time(to), GeneratedAt: Time(now),
		Actions: OverviewCount{Series: make([]int64, n)}, Blocked: OverviewCount{Series: make([]int64, n)},
		Verification: OverviewVerification{Series: OverviewVerdictSeries{Verified: make([]int64, n), Mismatch: make([]int64, n),
			Unverifiable: make([]int64, n)}},
		Agents: OverviewAgents{Object: "list", Data: []OverviewAgent{}}, Attention: []AttentionItem{}}
	var att attention
	err := l.db.OrgSnapshotTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		// Two planner settings for this transaction only (overviewSettingsSQL). A custom plan for
		// every statement: with a generic plan the window's bounds are unknown and Postgres walks
		// every row of the org. random_page_cost 1.1 (solid-state storage): with the default 4, a
		// bloated actions_overview (every transition writes a new index entry until vacuum) makes the
		// planner read the wide heap instead of the index alone, 2 to 3 times slower when one org
		// owns most of the table.
		if _, err := tx.Exec(ctx, overviewSettingsSQL, overviewTimeout); err != nil {
			return err
		}
		stop, err := overviewOrg(ctx, tx, org, ov)
		if err != nil {
			return err
		}
		if err := overviewSeries(ctx, tx, org, o.Window, from, to, ov); err != nil {
			return err
		}
		if err := overviewApprovals(ctx, tx, org, now, ov); err != nil {
			return err
		}
		budgets, err := overviewAgents(ctx, tx, org, ov, o.Usage, now)
		if err != nil {
			return err
		}
		// Attention, in priority order.
		att.chain(o.Chain)
		att.add(stop)
		if err := att.slots(ctx, tx, org, o.SlotLagAlertBytes); err != nil {
			return err
		}
		if err := att.mismatches(ctx, tx, org, now); err != nil {
			return err
		}
		if err := att.approvals(ctx, tx, org, now); err != nil {
			return err
		}
		att.budgets(budgets, now)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if items := att.list(); len(items) > 0 {
		ov.Attention = items // otherwise the empty list, never null
	}
	return ov, nil
}

// overviewOrg fills the time zone and returns the org_stopped item, or nil.
func overviewOrg(ctx context.Context, tx pgx.Tx, org ids.OrgID, ov *Overview) (*AttentionItem, error) {
	var (
		at      *time.Time
		by, why *string
	)
	// stopped_by as GET /v1/org shows it (S24): the person's display name or email, else the key id.
	err := tx.QueryRow(ctx, overviewOrgSQL, org).Scan(&ov.Timezone, &at, &by, &why)
	if err != nil {
		return nil, err
	}
	if at == nil {
		return nil, nil
	}
	return &AttentionItem{Object: "attention_item", Kind: AttentionOrgStopped, Severity: SeverityHigh, At: Time(*at),
		Data: map[string]any{"since": Time(*at), "by": by, "reason": why}}, nil
}

// overviewSettingsSQL sets the Overview transaction's statement timeout and planner settings.
const overviewSettingsSQL = `SELECT pg_catalog.set_config('statement_timeout', $1, true),
	    pg_catalog.set_config('plan_cache_mode', 'force_custom_plan', true),
	    pg_catalog.set_config('random_page_cost', '1.1', true)`

// The Overview's other reads (fixed SQL, bound values; the bench EXPLAINs every one).
const (
	overviewOrgSQL = `SELECT o.timezone, o.stopped_at,
	    COALESCE((SELECT COALESCE(NULLIF(u.display_name, ''), u.email::text) FROM deedglass.users AS u WHERE u.id = o.stopped_by), o.stopped_by),
	    o.stop_reason
	  FROM deedglass.orgs AS o WHERE o.id = $1`

	overviewApprovalsSQL = `SELECT pg_catalog.count(*), pg_catalog.count(*) FILTER (WHERE p.expires_at < $2), pg_catalog.min(p.expires_at)
	  FROM deedglass.approvals AS p WHERE p.org_id = $1 AND p.status = 'pending'`

	overviewStripSQL = `SELECT g.id, g.name, g.status, g.budgets, la.at
	  FROM deedglass.agents AS g
	  LEFT JOIN LATERAL (SELECT a.created_at AS at FROM deedglass.actions AS a
	     WHERE a.org_id = g.org_id AND a.agent_id = g.id ORDER BY a.created_at DESC, a.id DESC LIMIT 1) AS la ON true
	 WHERE g.org_id = $1 AND g.kind <> 'system'
	 ORDER BY la.at DESC NULLS LAST, g.name, g.id
	 LIMIT $2`

	overviewBudgetedSQL = `SELECT g.id, g.name, g.budgets FROM deedglass.agents AS g
	 WHERE g.org_id = $1 AND g.kind <> 'system' AND g.budgets <> '{}'::jsonb
	 ORDER BY g.name, g.id LIMIT $2`

	overviewSlotsSQL = `SELECT s.connector_id, c.name, s.status, s.status_reason, s.wal_status, s.lag_bytes, s.status_since
	  FROM deedglass.decoder_slots AS s
	  JOIN deedglass.connectors AS c ON c.org_id = s.org_id AND c.id = s.connector_id
	 WHERE s.org_id = $1 AND s.status NOT IN ('disabled', 'dropped')
	 ORDER BY c.name, s.connector_id LIMIT 100`

	overviewMismatchSQL = `SELECT a.id, a.created_at, a.tool, a.agent_id, g.name, a.witness_level, a.witness_detail
	  FROM deedglass.actions AS a
	  JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id
	 WHERE a.org_id = $1 AND a.witness_verdict = 'MISMATCH' AND a.created_at >= $2 AND a.created_at <= $3
	 ORDER BY a.created_at DESC, a.id DESC LIMIT $4`

	overviewExpiringSQL = `SELECT p.id, p.action_id, COALESCE(p.tool, x.tool), COALESCE(p.agent_id, x.agent_id), g.name, p.expires_at, p.created_at
	  FROM deedglass.approvals AS p
	  JOIN deedglass.actions AS x ON x.org_id = p.org_id AND x.id = p.action_id
	  LEFT JOIN deedglass.agents AS g ON g.org_id = p.org_id AND g.id = COALESCE(p.agent_id, x.agent_id)
	 WHERE p.org_id = $1 AND p.status = 'pending' AND p.expires_at < $2
	 ORDER BY p.expires_at, p.id LIMIT $3`
)

// The Overview's two scans of actions (the bench EXPLAINs them). Both read actions_overview
// (0023: (org_id, created_at) INCLUDE (decision, witness_verdict, witness_level)) as a range, an
// index-only scan where the rows are all-visible. The window's scan bins rows with date_bin from
// the window's start and groups by what the counts need, so at most 24 × 2 × 4 × 4 rows come back.
const (
	overviewSeriesSQL = `SELECT pg_catalog.date_bin(pg_catalog.make_interval(secs => $4::double precision), a.created_at, $2::timestamptz) AS bucket,
	    a.decision IS NOT DISTINCT FROM 'deny' AS denied, a.witness_verdict, a.witness_level, pg_catalog.count(*)
	  FROM deedglass.actions AS a
	 WHERE a.org_id = $1 AND a.created_at >= $2 AND a.created_at < $3
	 GROUP BY 1, 2, 3, 4`

	overviewPreviousSQL = `SELECT pg_catalog.count(*), pg_catalog.count(*) FILTER (WHERE a.decision = 'deny')
	  FROM deedglass.actions AS a WHERE a.org_id = $1 AND a.created_at >= $2 AND a.created_at < $3`
)

// overviewSeries fills the totals and series of the window and the previous window's totals.
func overviewSeries(ctx context.Context, tx pgx.Tx, org ids.OrgID, w OverviewWindow, from, to time.Time, ov *Overview) error {
	rows, err := tx.Query(ctx, overviewSeriesSQL, org, from, to, w.Bucket.Seconds())
	if err != nil {
		return err
	}
	defer rows.Close()
	// One row per (bucket, denied, verdict, level), at most 24 × 2 × 4 × 4: the sums are made here.
	v := &ov.Verification
	for rows.Next() {
		var (
			bucket         time.Time
			denied         bool
			verdict, level *string
			n              int64
		)
		if err := rows.Scan(&bucket, &denied, &verdict, &level, &n); err != nil {
			return err
		}
		i := w.BucketIndex(from, bucket)
		if i < 0 {
			return errors.New("ledger: overview bucket outside the window")
		}
		ov.Actions.Series[i] += n
		ov.Actions.Total += n
		if denied {
			ov.Blocked.Series[i] += n
			ov.Blocked.Total += n
		}
		if verdict == nil {
			continue
		}
		switch *verdict {
		case "VERIFIED":
			v.Series.Verified[i] += n
			v.Verified += n
		case "MISMATCH":
			v.Series.Mismatch[i] += n
			v.Mismatch += n
		case "UNVERIFIABLE":
			v.Series.Unverifiable[i] += n
			v.Unverifiable += n
		}
		if level != nil {
			switch *level {
			case "L0":
				v.ByLevel.L0 += n
			case "L1":
				v.ByLevel.L1 += n
			case "L2":
				v.ByLevel.L2 += n
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	v.NotWitnessed = ov.Actions.Total - v.Verified - v.Mismatch - v.Unverifiable
	return tx.QueryRow(ctx, overviewPreviousSQL,
		org, from.Add(-w.Span), from).Scan(&ov.Actions.Previous, &ov.Blocked.Previous)
}

// overviewApprovals counts pending approvals (approvals_org_pending).
func overviewApprovals(ctx context.Context, tx pgx.Tx, org ids.OrgID, now time.Time, ov *Overview) error {
	var next *time.Time
	if err := tx.QueryRow(ctx, overviewApprovalsSQL, org, now.Add(ApprovalExpiringSoon)).
		Scan(&ov.Approvals.Pending, &ov.Approvals.ExpiringSoon, &next); err != nil {
		return err
	}
	ov.Approvals.NextExpiresAt = tptr(next)
	return nil
}

// budgetAgent is an agent with at least one budget, and its usage, for budget_near.
type budgetAgent struct {
	id, name string
	budgets  AgentBudgets
	usage    AgentUsage
}

// overviewAgents fills the strip: the most recently active agents (their newest action, then
// name; agents with no action last), never the org's system agent. It returns every agent with a
// budget and its usage. Usage is one read for the strip and the budgeted agents together.
func overviewAgents(ctx context.Context, tx pgx.Tx, org ids.OrgID, ov *Overview, usage UsageReader, now time.Time) ([]budgetAgent, error) {
	rows, err := tx.Query(ctx, overviewStripSQL, org, OverviewAgentsShown+1)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			a   OverviewAgent
			raw []byte
			at  *time.Time
		)
		if err := rows.Scan(&a.ID, &a.Name, &a.Status, &raw, &at); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &a.Budgets); err != nil {
			rows.Close()
			return nil, err
		}
		a.LastActionAt = tptr(at)
		ov.Agents.Data = append(ov.Agents.Data, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ov.Agents.Data) > OverviewAgentsShown {
		ov.Agents.Data, ov.Agents.HasMore = ov.Agents.Data[:OverviewAgentsShown], true
	}

	rows, err = tx.Query(ctx, overviewBudgetedSQL, org, overviewBudgetAgents)
	if err != nil {
		return nil, err
	}
	var budgeted []budgetAgent
	for rows.Next() {
		var (
			b   budgetAgent
			raw []byte
		)
		if err := rows.Scan(&b.id, &b.name, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &b.budgets); err != nil {
			rows.Close()
			return nil, err
		}
		if b.budgets.CallsPerMinute != nil || b.budgets.CallsPerDay != nil || b.budgets.SpendPerDay != nil {
			budgeted = append(budgeted, b)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if usage == nil {
		for i := range ov.Agents.Data {
			ov.Agents.Data[i].Usage.SpendToday = spendToday(ov.Agents.Data[i].Budgets, AgentUsage{})
		}
		return budgeted, nil
	}
	var reqs []AgentUsageRequest
	for _, a := range ov.Agents.Data {
		reqs = append(reqs, AgentUsageRequest{Agent: a.ID, Currency: SpendCurrency(a.Budgets)})
	}
	for _, b := range budgeted {
		reqs = append(reqs, AgentUsageRequest{Agent: b.id, Currency: SpendCurrency(b.budgets)})
	}
	if len(reqs) == 0 {
		return budgeted, nil
	}
	us, err := usage(ctx, tx, org, ov.Timezone, now, reqs)
	if err != nil {
		return nil, err
	}
	if len(us) != len(reqs) {
		return nil, errors.New("ledger: the usage store answered for the wrong number of agents")
	}
	for i := range ov.Agents.Data {
		a := &ov.Agents.Data[i]
		a.Usage = OverviewAgentUsage{CallsToday: us[i].CallsToday, SpendToday: spendToday(a.Budgets, us[i])}
	}
	k := len(ov.Agents.Data)
	for i := range budgeted {
		budgeted[i].usage = us[k+i]
	}
	return budgeted, nil
}

// SpendCurrency is the currency an agent's spend is counted in: its spend budget's, else usd.
func SpendCurrency(b AgentBudgets) string {
	if b.SpendPerDay != nil && b.SpendPerDay.Currency != "" {
		return b.SpendPerDay.Currency
	}
	return "usd"
}

// spendToday is null when nothing was spent today and the agent has no spend budget.
func spendToday(b AgentBudgets, u AgentUsage) *Money {
	if u.SpendToday == 0 && b.SpendPerDay == nil {
		return nil
	}
	cur := u.Currency
	if cur == "" {
		cur = SpendCurrency(b)
	}
	return &Money{Amount: u.SpendToday, Currency: cur}
}

// attention collects candidates per group, each in its own order and at most AttentionMax, and
// list selects the answer. System items (chain_broken, org_stopped, slot_invalidated,
// slot_lagging) come first, up to the cap; then up to AttentionReservedApprovals approval_expiring
// and AttentionReservedBudgets budget_near; then mismatches fill what is left; then more
// approval_expiring and budget_near if slots remain. The answer keeps the kinds' priority order.
type attention struct {
	system, mismatch, expiring, budget []AttentionItem
}

// add files it under its kind's group; a group past AttentionMax drops the rest.
func (a *attention) add(it *AttentionItem) {
	if it == nil {
		return
	}
	g := &a.system
	switch it.Kind {
	case AttentionMismatch:
		g = &a.mismatch
	case AttentionApprovalExpiry:
		g = &a.expiring
	case AttentionBudgetNear:
		g = &a.budget
	}
	if len(*g) >= AttentionMax {
		return
	}
	if it.Data == nil {
		it.Data = map[string]any{}
	}
	*g = append(*g, *it)
}

// list is the selected items, at most AttentionMax, in priority order.
func (a *attention) list() []AttentionItem {
	take := func(have, limit, free int) int { return max(0, min(have, limit, free)) }
	sys := take(len(a.system), AttentionMax, AttentionMax)
	free := AttentionMax - sys
	exp := take(len(a.expiring), AttentionReservedApprovals, free)
	free -= exp
	bud := take(len(a.budget), AttentionReservedBudgets, free)
	free -= bud
	mis := take(len(a.mismatch), AttentionMax, free)
	free -= mis
	more := take(len(a.expiring)-exp, AttentionMax, free)
	exp, free = exp+more, free-more
	bud += take(len(a.budget)-bud, AttentionMax, free)
	out := make([]AttentionItem, 0, sys+mis+exp+bud)
	out = append(out, a.system[:sys]...)
	out = append(out, a.mismatch[:mis]...)
	out = append(out, a.expiring[:exp]...)
	return append(out, a.budget[:bud]...)
}

func ref(s string) *string { return &s }

// chain adds chain_broken when the latest background check found a break.
func (a *attention) chain(c *ChainCheck) {
	if c == nil || c.Status != CheckBroken || c.Break == nil {
		return
	}
	it := &AttentionItem{Object: "attention_item", Kind: AttentionChainBroken, Severity: SeverityHigh, At: c.CheckedAt,
		Ref: c.Break.Deed, Data: map[string]any{"seq": c.Break.Seq, "deed": c.Break.Deed, "code": c.Break.Code,
			"checked": c.Checked, "checked_at": c.CheckedAt}}
	a.add(it)
}

// slots adds slot_invalidated (a halted runner, an invalidated slot, or wal_status lost) and
// slot_lagging (lag above DEEDGLASS_SLOT_LAG_ALERT_MB, as the decoder's lag_high).
func (a *attention) slots(ctx context.Context, tx pgx.Tx, org ids.OrgID, lagAlert int64) error {
	rows, err := tx.Query(ctx, overviewSlotsSQL, org)
	if err != nil {
		return err
	}
	defer rows.Close()
	var invalid, lagging []AttentionItem
	for rows.Next() {
		var (
			id, name, status string
			reason, wal      *string
			lag              *int64
			since            time.Time
		)
		if err := rows.Scan(&id, &name, &status, &reason, &wal, &lag, &since); err != nil {
			return err
		}
		switch {
		case status == "halted" || (reason != nil && *reason == "slot_invalidated") || (wal != nil && *wal == "lost"):
			invalid = append(invalid, AttentionItem{Object: "attention_item", Kind: AttentionSlotInvalidated, Severity: SeverityHigh,
				At: Time(since), Ref: ref(id), Data: map[string]any{"connector": name, "connector_id": id, "status": status,
					"status_reason": reason}})
		case lagAlert > 0 && lag != nil && *lag > lagAlert:
			lagging = append(lagging, AttentionItem{Object: "attention_item", Kind: AttentionSlotLagging, Severity: SeverityMedium,
				At: Time(since), Ref: ref(id), Data: map[string]any{"connector": name, "connector_id": id, "lag_bytes": *lag,
					"threshold_bytes": lagAlert}})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range invalid {
		a.add(&invalid[i])
	}
	for i := range lagging {
		a.add(&lagging[i])
	}
	return nil
}

// mismatches adds the last 24 h's MISMATCH verdicts, newest first (actions_verdict_time).
func (a *attention) mismatches(ctx context.Context, tx pgx.Tx, org ids.OrgID, now time.Time) error {
	rows, err := tx.Query(ctx, overviewMismatchSQL, org, now.Add(-MismatchLookback), now, AttentionMax)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, tool, agentID, agent string
			level                    *string
			at                       time.Time
			detail                   []byte
		)
		if err := rows.Scan(&id, &at, &tool, &agentID, &agent, &level, &detail); err != nil {
			return err
		}
		w := &ActionWitness{}
		w.fill(detail, false)
		a.add(&AttentionItem{Object: "attention_item", Kind: AttentionMismatch, Severity: SeverityHigh, At: Time(at), Ref: ref(id),
			Data: map[string]any{"tool": tool, "agent": agent, "agent_id": agentID, "summary": w.Summary, "level": level}})
	}
	return rows.Err()
}

// approvals adds pending approvals that expire within 15 minutes, soonest first.
func (a *attention) approvals(ctx context.Context, tx pgx.Tx, org ids.OrgID, now time.Time) error {
	rows, err := tx.Query(ctx, overviewExpiringSQL, org, now.Add(ApprovalExpiringSoon), AttentionMax)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, action, tool, agentID string
			agent                     *string
			expires, created          time.Time
		)
		if err := rows.Scan(&id, &action, &tool, &agentID, &agent, &expires, &created); err != nil {
			return err
		}
		a.add(&AttentionItem{Object: "attention_item", Kind: AttentionApprovalExpiry, Severity: SeverityMedium, At: Time(created),
			Ref: ref(id), Data: map[string]any{"tool": tool, "agent": agent, "agent_id": agentID, "expires_at": Time(expires),
				"action": action}})
	}
	return rows.Err()
}

// budgets adds budget_near for every agent budget at least 80% used in its current window,
// fullest first.
func (a *attention) budgets(bs []budgetAgent, now time.Time) {
	type near struct {
		it         AttentionItem
		used, lim  int64
		agent, key string
	}
	var ns []near
	for _, b := range bs {
		check := func(key string, used int64, lim *int64, cur string) {
			if lim == nil || *lim <= 0 || used*100 < *lim*BudgetNearPercent {
				return
			}
			d := map[string]any{"agent": b.name, "agent_id": b.id, "budget": key, "used": used, "limit": *lim}
			if cur != "" {
				d["currency"] = cur
			}
			ns = append(ns, near{it: AttentionItem{Object: "attention_item", Kind: AttentionBudgetNear, Severity: SeverityLow,
				At: Time(now), Ref: ref(b.id), Data: d}, used: used, lim: *lim, agent: b.name, key: key})
		}
		check("calls_per_minute", b.usage.CallsMinute, b.budgets.CallsPerMinute, "")
		check("calls_per_day", b.usage.CallsToday, b.budgets.CallsPerDay, "")
		if s := b.budgets.SpendPerDay; s != nil {
			lim := s.Amount
			check("spend_per_day", b.usage.SpendToday, &lim, s.Currency)
		}
	}
	sort.SliceStable(ns, func(i, j int) bool {
		l, r := float64(ns[i].used)/float64(ns[i].lim), float64(ns[j].used)/float64(ns[j].lim)
		if l != r {
			return l > r // the fullest first
		}
		if ns[i].agent != ns[j].agent {
			return ns[i].agent < ns[j].agent
		}
		return ns[i].key < ns[j].key
	})
	for i := range ns {
		a.add(&ns[i].it)
	}
}
