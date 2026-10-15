// Package pgsource reads a weekly summary's facts from Deedglass's own database (S52 §3.1): one
// REPEATABLE READ READ ONLY org transaction (RLS on) for the rows, plus the Ledger's chain check.
// It selects counts, ids, dates, the org's own agent, tool and rule names and each rule's reason:
// never action arguments or results, row images, emails (except the recipients' own, for the To
// line) or credentials. It also holds the Monday job's store (weekly.Store).
package pgsource

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/caught"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/weekly"
)

// Source reads summaries and serves the Monday job.
type Source struct {
	DB     *store.DB
	Ledger *ledger.Ledger
	// ChainBudget bounds the chain check (0: the Ledger's default); one that runs out is "not checked".
	ChainBudget time.Duration
}

// inFlight are the action states that haven't finished.
const inFlight = `'received', 'authenticated', 'evaluated', 'pending_approval', 'executing', 'witnessing'`

// packLabel starts the label S51 seals with a pack's index.html (securitypack.FileLabel).
const packLabel = "Security review pack · index.html · "

// Org reads an org's name, slug and time zone (inside its own transaction).
func (s *Source) Org(ctx context.Context, org ids.OrgID) (weekly.Org, string, error) {
	o := weekly.Org{ID: org.String()}
	var tz string
	err := s.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT name, slug, timezone FROM deedglass.orgs WHERE id = $1`, org).Scan(&o.Name, &o.Slug, &tz)
	})
	return o, tz, err
}

// Summarize builds org's summary for p.
func (s *Source) Summarize(ctx context.Context, org ids.OrgID, p weekly.Period, now time.Time) (weekly.Summary, error) {
	f, err := s.Facts(ctx, org, p, now)
	if err != nil {
		return weekly.Summary{}, err
	}
	return weekly.Build(*f), nil
}

// Facts reads org's facts for p.
func (s *Source) Facts(ctx context.Context, org ids.OrgID, p weekly.Period, now time.Time) (*weekly.Facts, error) {
	f := &weekly.Facts{Org: weekly.Org{ID: org.String()}, Period: p, Now: now, Verdicts: map[string]int64{}}
	err := s.DB.OrgSnapshotTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', '20s', true)`); err != nil {
			return err
		}
		var tz string
		if err := tx.QueryRow(ctx, `SELECT name, slug, timezone FROM deedglass.orgs WHERE id = $1`, org).Scan(&f.Org.Name, &f.Org.Slug, &tz); err != nil {
			return err
		}
		if f.Period.Timezone == "" {
			f.Period.Timezone = tz
		}
		for _, step := range []func(context.Context, pgx.Tx, ids.OrgID, *weekly.Facts) error{
			decisions, tools, verdicts, denials, approvals, undos, stops, sessions, caughtActions, pack,
		} {
			if err := step(ctx, tx, org, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	f.Chain = s.chain(ctx, org, p)
	return f, nil
}

// actionsIn is the period filter every action read shares: the org's own agents (not the system
// agent that runs a person's undo), created inside the period.
const actionsIn = `FROM deedglass.actions AS a JOIN deedglass.agents AS g ON g.org_id = a.org_id AND g.id = a.agent_id
	 WHERE a.org_id = $1 AND a.created_at >= $2 AND a.created_at < $3 AND g.kind <> 'system'`

func decisions(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	rows, err := tx.Query(ctx, `SELECT a.agent_id, g.name, COALESCE(a.decision, ''), pg_catalog.count(*) `+actionsIn+` GROUP BY 1, 2, 3`,
		org, f.Period.From, f.Period.To)
	if err != nil {
		return err
	}
	f.Decisions, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (weekly.AgentDecision, error) {
		var d weekly.AgentDecision
		err := r.Scan(&d.AgentID, &d.Name, &d.Decision, &d.Count)
		return d, err
	})
	if err != nil {
		return err
	}
	var prev int64
	span := f.Period.To.Sub(f.Period.From)
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*) `+actionsIn, org, f.Period.From.Add(-span), f.Period.From).Scan(&prev); err != nil {
		return err
	}
	f.Previous = &prev
	return nil
}

func tools(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	rows, err := tx.Query(ctx, `SELECT a.tool, pg_catalog.count(*) `+actionsIn+` GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 50`,
		org, f.Period.From, f.Period.To)
	if err != nil {
		return err
	}
	f.Tools, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (weekly.Count, error) {
		var c weekly.Count
		err := r.Scan(&c.Name, &c.Count)
		return c, err
	})
	return err
}

func verdicts(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	rows, err := tx.Query(ctx, `SELECT COALESCE(a.witness_verdict, ''), pg_catalog.count(*) `+actionsIn+` GROUP BY 1`,
		org, f.Period.From, f.Period.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		var n int64
		if err := rows.Scan(&v, &n); err != nil {
			return err
		}
		f.Verdicts[v] += n
	}
	return rows.Err()
}

// denials groups blocked actions by the rule that decided them; a deny with no rule (the default,
// a budget, Stop) is named by its source, as the Insights digest does. The reason is the rule's
// own reason, as the policy author wrote it.
func denials(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	rows, err := tx.Query(ctx, `SELECT COALESCE(NULLIF(a.decision_rule_id, ''), '@' || COALESCE(a.decision_source, 'none')),
	    COALESCE(pg_catalog.max(a.decision_reason) FILTER (WHERE a.decision_rule_id <> ''), ''), pg_catalog.count(*) `+actionsIn+`
	    AND a.decision = 'deny' GROUP BY 1 ORDER BY 3 DESC, 1 LIMIT 3`, org, f.Period.From, f.Period.To)
	if err != nil {
		return err
	}
	f.Denials, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (weekly.Denial, error) {
		var d weekly.Denial
		err := r.Scan(&d.Rule, &d.Reason, &d.Count)
		return d, err
	})
	return err
}

func approvals(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	a := &f.Approvals
	var med *float64
	err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*), pg_catalog.count(*) FILTER (WHERE status = 'approved'),
	    pg_catalog.count(*) FILTER (WHERE status = 'denied'), pg_catalog.count(*) FILTER (WHERE status = 'expired'),
	    pg_catalog.count(*) FILTER (WHERE status = 'pending'),
	    pg_catalog.percentile_disc(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM decided_at - created_at)::float8) FILTER (WHERE status IN ('approved', 'denied'))
	  FROM deedglass.approvals WHERE org_id = $1 AND created_at >= $2 AND created_at < $3`, org, f.Period.From, f.Period.To).
		Scan(&a.Requested, &a.Approved, &a.Denied, &a.Expired, &a.Pending, &med)
	if err == nil && med != nil {
		m := int64(*med + 0.5)
		a.MedianSeconds = &m
	}
	return err
}

func undos(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.actions WHERE org_id = $1 AND undoes_action_id IS NOT NULL
	    AND created_at >= $2 AND created_at < $3 AND state NOT IN (`+inFlight+`, 'denied', 'expired', 'cancelled')`,
		org, f.Period.From, f.Period.To).Scan(&f.Undos.Actions); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.observe_sessions WHERE org_id = $1 AND state = 'undone'
	    AND undo_action_id IS NOT NULL AND opened_at >= $2 AND opened_at < $3`, org, f.Period.From, f.Period.To).Scan(&f.Undos.Sessions)
}

func stops(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	return tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FILTER (WHERE action = 'agent.stopped'), pg_catalog.count(*) FILTER (WHERE action = 'org.stopped_all')
	  FROM deedglass.admin_events WHERE org_id = $1 AND created_at >= $2 AND created_at < $3 AND action IN ('agent.stopped', 'org.stopped_all')`,
		org, f.Period.From, f.Period.To).Scan(&f.Stops.Agent, &f.Stops.All)
}

// sessions counts watched sessions and reads the caught items each sealed summary holds (S50: built
// from frame counts, never row values).
func sessions(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	if err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*), pg_catalog.count(*) FILTER (WHERE 'caught' = ANY (flags))
	  FROM deedglass.observe_sessions WHERE org_id = $1 AND opened_at >= $2 AND opened_at < $3`, org, f.Period.From, f.Period.To).
		Scan(&f.Sessions.Watched, &f.Sessions.Caught); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT COALESCE(summary -> 'caught', '[]'::jsonb) FROM deedglass.observe_sessions
	  WHERE org_id = $1 AND opened_at >= $2 AND opened_at < $3 AND 'caught' = ANY (flags) AND state IN ('sealed', 'undoing', 'undone')
	  ORDER BY opened_at DESC, id DESC LIMIT 20`, org, f.Period.From, f.Period.To)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var items []caught.Item
		if err := json.Unmarshal(raw, &items); err != nil {
			continue // an unreadable summary shows nothing rather than something wrong
		}
		f.SessionCaught = append(f.SessionCaught, items...)
	}
	return rows.Err()
}

// caughtActions reads the period's actions the action detectors look at: a Doesn't match at L1 or
// L2, and actions a person undid (S50 internal/caught). Newest first, at most 50.
func caughtActions(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	rows, err := tx.Query(ctx, `SELECT a.id, g.name, a.tool, a.created_at, COALESCE(a.decision, ''), COALESCE(a.witness_verdict, ''),
	    COALESCE(a.witness_level, ''), a.state NOT IN (`+inFlight+`, 'denied', 'expired', 'cancelled', 'failed'),
	    COALESCE((SELECT u.id FROM deedglass.actions AS u WHERE u.org_id = a.org_id AND u.undoes_action_id = a.id
	      AND u.state NOT IN (`+inFlight+`, 'denied', 'expired', 'cancelled', 'failed') ORDER BY u.created_at LIMIT 1), '')
	  `+actionsIn+` AND (a.witness_verdict = 'MISMATCH' OR EXISTS (SELECT 1 FROM deedglass.actions AS u WHERE u.org_id = a.org_id AND u.undoes_action_id = a.id))
	  ORDER BY a.created_at DESC, a.id DESC LIMIT 50`, org, f.Period.From, f.Period.To)
	if err != nil {
		return err
	}
	f.Actions, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (caught.Action, error) {
		var a caught.Action
		err := r.Scan(&a.ID, &a.Agent, &a.Tool, &a.At, &a.Decision, &a.Verdict, &a.Level, &a.Executed, &a.UndoneBy)
		a.Undoable = a.Level == "L2" && a.Executed
		a.UndoAvailable = a.Undoable
		return a, err
	})
	return err
}

// pack finds the newest security review pack built in the period (S51 seals its index.html with a
// fixed label).
func pack(ctx context.Context, tx pgx.Tx, org ids.OrgID, f *weekly.Facts) error {
	var at time.Time
	var reason string
	err := tx.QueryRow(ctx, `SELECT created_at, reason FROM deedglass.admin_events WHERE org_id = $1 AND action = 'file.sealed'
	    AND created_at >= $2 AND created_at < $3 AND pg_catalog.starts_with(reason, $4) ORDER BY created_at DESC, id DESC LIMIT 1`,
		org, f.Period.From, f.Period.To, packLabel).Scan(&at, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	who := strings.TrimPrefix(reason, packLabel)
	folder := ""
	if isSlug(who) {
		folder = "security-pack-" + who + "-" + at.UTC().Format("2006-01-02")
	}
	f.Pack = &weekly.Pack{BuiltAt: at.UTC(), Folder: folder}
	return nil
}

func isSlug(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// chain checks the period's records (anchored on the record before it). A check that doesn't
// finish is "not checked", never intact.
func (s *Source) chain(ctx context.Context, org ids.OrgID, p weekly.Period) weekly.Chain {
	if s.Ledger == nil {
		return weekly.Chain{Status: weekly.ChainNotChecked}
	}
	from, to := p.From, p.To
	cc, err := s.Ledger.CheckChain(ctx, org, ledger.Range{IssuedGTE: &from, IssuedLT: &to}, ledger.CheckOptions{Budget: s.ChainBudget})
	switch {
	case errors.Is(err, ledger.ErrEmptyRange):
		if head, herr := s.Ledger.HeadSeq(ctx, org); herr == nil && head > 0 {
			if cc, err = s.Ledger.CheckChain(ctx, org, ledger.Range{FromSeq: head}, ledger.CheckOptions{Budget: s.ChainBudget}); err == nil {
				break
			}
		}
		if errors.Is(err, ledger.ErrEmptyRange) {
			return weekly.Chain{Status: weekly.ChainNoRecords}
		}
		return weekly.Chain{Status: weekly.ChainNotChecked}
	case err != nil:
		return weekly.Chain{Status: weekly.ChainNotChecked}
	}
	out := weekly.Chain{Checked: int64(cc.Checked)}
	switch cc.Status {
	case ledger.CheckIntact:
		out.Status = weekly.ChainIntact
	case ledger.CheckBroken:
		out.Status = weekly.ChainBroken
		if cc.Break != nil && cc.Break.Deed != nil {
			out.FirstBroken = *cc.Break.Deed
		}
	default:
		out.Status = weekly.ChainNotChecked
	}
	return out
}

// --- weekly.Store (the Monday job) ---

// ActiveOrgs lists every org with its time zone: the id list from the global orgs table, then each
// org's own row in its own transaction.
func (s *Source) ActiveOrgs(ctx context.Context) ([]weekly.OrgInfo, error) {
	list, err := s.DB.OrgIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]weekly.OrgInfo, 0, len(list))
	for _, id := range list {
		org, err := ids.Parse[ids.OrgKind](id)
		if err != nil {
			continue
		}
		var tz, status string
		if err := s.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT timezone, status FROM deedglass.orgs WHERE id = $1`, org).Scan(&tz, &status)
		}); err != nil || status != "active" {
			continue
		}
		out = append(out, weekly.OrgInfo{ID: org, Timezone: tz})
	}
	return out, nil
}

// ClaimRun inserts the org's run row for the period, once (learn_runs kind "weekly").
func (s *Source) ClaimRun(ctx context.Context, org ids.OrgID, kind, period string, at time.Time) (bool, error) {
	var ok bool
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO deedglass.learn_runs (org_id, kind, period_key, status, started_at) VALUES ($1, $2, $3, 'running', $4)
		    ON CONFLICT (org_id, kind, period_key) DO NOTHING`, org, kind, period, at)
		ok = tag.RowsAffected() == 1
		return err
	})
	return ok, err
}

// FinishRun records the run's outcome.
func (s *Source) FinishRun(ctx context.Context, org ids.OrgID, kind, period string, ok bool, stats map[string]any, errText string, at time.Time) error {
	b, err := json.Marshal(stats)
	if err != nil {
		return err
	}
	status := "succeeded"
	if !ok {
		status = "failed"
	}
	if len(errText) > 2000 {
		errText = errText[:2000]
	}
	return s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.learn_runs SET status = $4, finished_at = $5, stats = $6::jsonb, error = NULLIF($7, '')
		    WHERE org_id = $1 AND kind = $2 AND period_key = $3`, org, kind, period, status, at, string(b), errText)
		return err
	})
}

// Recipients lists the org's owners (active membership, account not disabled) whose weekly_email is on. An owner
// who has never signed in still gets it: the operator named them when creating the org.
func (s *Source) Recipients(ctx context.Context, org ids.OrgID) ([]weekly.Recipient, error) {
	var out []weekly.Recipient
	err := s.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT u.id, u.email FROM deedglass.memberships AS m JOIN deedglass.users AS u ON u.id = m.user_id
		    WHERE m.org_id = $1 AND m.status = 'active' AND m.role = 'owner' AND m.weekly_email AND u.status <> 'disabled'
		    ORDER BY u.email LIMIT 50`, org)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (weekly.Recipient, error) {
			var p weekly.Recipient
			err := r.Scan(&p.UserID, &p.Email)
			return p, err
		})
		return err
	})
	return out, err
}

// EmailSetting reads a member's weekly_email (false when they aren't an active member).
func (s *Source) EmailSetting(ctx context.Context, org ids.OrgID, user ids.UserID) (on bool, role string, err error) {
	err = s.DB.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT weekly_email, role FROM deedglass.memberships WHERE org_id = $1 AND user_id = $2 AND status = 'active'`,
			org, user).Scan(&on, &role)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return on, role, err
}

// SetEmail turns a member's weekly_email on or off. It reports whether the membership exists.
func (s *Source) SetEmail(ctx context.Context, org ids.OrgID, user ids.UserID, on bool) (bool, error) {
	var found bool
	err := s.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE deedglass.memberships SET weekly_email = $3 WHERE org_id = $1 AND user_id = $2 AND status <> 'removed'`, org, user, on)
		found = tag.RowsAffected() == 1
		return err
	})
	return found, err
}
