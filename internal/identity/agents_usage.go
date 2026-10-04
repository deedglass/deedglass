package identity

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store/policystore"
)

// ?expand[]=usage on GET /v1/agents and GET /v1/agents/{id} (S25 §3.2): each agent gains its S11
// usage counters, in the windows its budgets use, and when its newest action was created. A page
// is read in one org transaction with two statements, whatever its size. Without the expansion
// the agent object is exactly as before.

// The expansion's copy.
const (
	MsgExpandUsage = "expand[] takes only usage here."
	MsgAgentQuery  = "This endpoint takes only expand[]=usage."
)

// AgentUsage is the usage expansion: calls this UTC minute, this UTC hour and this day in the
// org's time zone, and spend today in the agent's spend-budget currency (usd without one). Spend
// is null when nothing was spent today and the agent has no spend budget.
type AgentUsage struct {
	CallsMinute int64        `json:"calls_minute"`
	CallsHour   int64        `json:"calls_hour"`
	CallsToday  int64        `json:"calls_today"`
	SpendToday  *httpx.Money `json:"spend_today"`
}

// AgentWithUsage is an agent with ?expand[]=usage.
type AgentWithUsage struct {
	Agent
	Usage        AgentUsage  `json:"usage"`
	LastActionAt *httpx.Time `json:"last_action_at"`
}

// parseExpandUsage reads expand[] (or expand) and reports whether usage was asked for.
func parseExpandUsage(r *http.Request) (bool, error) {
	usage := false
	for _, k := range []string{"expand[]", "expand"} {
		for _, v := range r.URL.Query()[k] {
			if v != "usage" {
				return false, httpx.ErrValidation(k, MsgExpandUsage)
			}
			usage = true
		}
	}
	return usage, nil
}

// spendCurrency is the currency the agent's spend counter is read in.
func spendCurrency(b Budgets) string {
	if b.SpendPerDay != nil && b.SpendPerDay.Currency != "" {
		return b.SpendPerDay.Currency
	}
	return "usd"
}

// WithUsage adds the usage expansion to agents (all of org), in one read-only org transaction:
// the org's time zone, every agent's newest action in one statement, and every counter in one
// more (policystore.Usage.AgentsTx, the evaluator's keys and windows).
func (s *Service) WithUsage(ctx context.Context, org ids.OrgID, agents []Agent) ([]AgentWithUsage, error) {
	out := make([]AgentWithUsage, len(agents))
	for i, a := range agents {
		out[i].Agent = a
	}
	if len(agents) == 0 {
		return out, nil
	}
	now := s.now()
	err := s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var tz string
		if err := tx.QueryRow(ctx, `SELECT timezone FROM deedglass.orgs WHERE id = $1`, org).Scan(&tz); err != nil {
			return err
		}
		loc, err := policy.LoadLocation(tz)
		if err != nil {
			loc = time.UTC // as the Gate does for an unknown zone
		}
		agentIDs, curs := make([]string, len(agents)), make([]string, len(agents))
		for i, a := range agents {
			agentIDs[i], curs[i] = a.ID, spendCurrency(a.Budgets)
		}
		rows, err := tx.Query(ctx, `SELECT k.i, la.at
		  FROM unnest($2::text[]) WITH ORDINALITY AS k (id, i)
		  LEFT JOIN LATERAL (SELECT a.created_at AS at FROM deedglass.actions AS a
		     WHERE a.org_id = $1 AND a.agent_id = k.id ORDER BY a.created_at DESC, a.id DESC LIMIT 1) AS la ON true`, org, agentIDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			var (
				i  int64
				at *time.Time
			)
			if err := rows.Scan(&i, &at); err != nil {
				rows.Close()
				return err
			}
			if i >= 1 && int(i) <= len(out) {
				out[i-1].LastActionAt = optTime(at)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		snaps, err := policystore.NewUsage(s.db).AgentsTx(ctx, tx, org, loc, now, agentIDs, curs)
		if err != nil {
			return err
		}
		for i, u := range snaps {
			out[i].Usage = AgentUsage{CallsMinute: u.CallsMinute, CallsHour: u.CallsHour, CallsToday: u.CallsToday}
			if u.SpendToday != 0 || agents[i].Budgets.SpendPerDay != nil {
				out[i].Usage.SpendToday = &httpx.Money{Amount: u.SpendToday, Currency: u.Currency}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
