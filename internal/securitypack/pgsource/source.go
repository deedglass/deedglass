// Package pgsource collects a security pack's facts from Deedglass's own database (S51 §3.2):
// one REPEATABLE READ READ ONLY org transaction for the rows (a consistent snapshot), each
// collector in its own savepoint so one that fails is Can't tell without taking the others down,
// plus the Ledger's chain check and seal keys. It reads ids, counts, hashes, dates and the names of
// the org's agents, connectors and rules: never action arguments or results, row values, emails or
// credential values (the canary test in cmd/deedglass reads every file a pack writes).
package pgsource

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/securitypack"
	"github.com/deedglass/deedglass/internal/store"
)

// Input is one collection.
type Input struct {
	Org    ids.OrgID
	Period securitypack.Period
	Now    time.Time
	Build  securitypack.BuildFacts
	Egress *securitypack.Egress // nil: Can't tell
	// ChainFrom is where the chain check starts (the period's start for a pack; the readiness view
	// checks the last day, like the Overview). ChainBudget bounds it (0: the Ledger's default); a
	// check that runs out is Can't tell.
	ChainFrom   time.Time
	ChainBudget time.Duration
}

// Source reads facts from the store and the Ledger.
type Source struct {
	DB     *store.DB
	Ledger *ledger.Ledger
}

// readMsg is the reason every failed collector gives: the error itself stays in the server log.
const readMsg = "the database didn't answer"

// Collect reads every collector. It returns an error only when the transaction itself couldn't
// start; a collector that fails is recorded in Facts.Errors.
func (s *Source) Collect(ctx context.Context, in Input) (*securitypack.Facts, error) {
	f := &securitypack.Facts{Org: securitypack.Org{ID: in.Org.String()}, Period: in.Period, Now: in.Now,
		Version: in.Build.Version, Build: in.Build, Egress: in.Egress}
	if in.Egress == nil {
		f.Fail(securitypack.CollectorEgress, "the server's outbound settings weren't passed in")
	}
	err := s.DB.OrgSnapshotTx(ctx, in.Org, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('statement_timeout', '20s', true)`); err != nil {
			return err
		}
		_ = tx.QueryRow(ctx, `SELECT name FROM deedglass.orgs WHERE id = $1`, in.Org).Scan(&f.Org.Name)
		c := &collector{tx: tx, f: f, in: in}
		c.run(ctx, securitypack.CollectorAgents, c.agents)
		c.run(ctx, securitypack.CollectorKeys, c.keys)
		c.run(ctx, securitypack.CollectorConnectors, c.connectors)
		c.run(ctx, securitypack.CollectorMembers, c.members)
		c.run(ctx, securitypack.CollectorRules, c.rules)
		c.run(ctx, securitypack.CollectorApprovals, c.approvals)
		c.run(ctx, securitypack.CollectorStop, c.stop)
		c.run(ctx, securitypack.CollectorActions, c.actions)
		c.run(ctx, securitypack.CollectorSessions, c.sessions)
		c.run(ctx, securitypack.CollectorUndos, c.undos)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.records(ctx, f, in)
	return f, nil
}

type collector struct {
	tx pgx.Tx
	f  *securitypack.Facts
	in Input
}

// run runs one collector inside a savepoint; a failure rolls back to it and marks the collector.
func (c *collector) run(ctx context.Context, name string, fn func(ctx context.Context, tx pgx.Tx) error) {
	sp, err := c.tx.Begin(ctx)
	if err != nil {
		c.f.Fail(name, readMsg)
		return
	}
	if err := fn(ctx, sp); err != nil {
		_ = sp.Rollback(ctx)
		c.f.Fail(name, readMsg)
		return
	}
	if err := sp.Commit(ctx); err != nil {
		c.f.Fail(name, readMsg)
	}
}

func (c *collector) agents(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT id, name, status, intent_mode, owner_user_id IS NOT NULL, budgets <> '{}'::jsonb, created_at
	  FROM deedglass.agents WHERE org_id = $1 AND kind <> 'system' ORDER BY name`, c.in.Org)
	if err != nil {
		return err
	}
	c.f.Agents, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (securitypack.Agent, error) {
		var a securitypack.Agent
		err := r.Scan(&a.ID, &a.Name, &a.Status, &a.IntentMode, &a.HasOwner, &a.HasBudget, &a.CreatedAt)
		return a, err
	})
	return err
}

func (c *collector) keys(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT id, COALESCE(agent_id, ''), scopes, created_at, last_used_at, expires_at
	  FROM deedglass.api_keys WHERE org_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > $2)
	  ORDER BY created_at, id`, c.in.Org, c.in.Now)
	if err != nil {
		return err
	}
	c.f.Keys, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (securitypack.Key, error) {
		var k securitypack.Key
		err := r.Scan(&k.ID, &k.AgentID, &k.Scopes, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt)
		return k, err
	})
	if err == nil {
		for _, k := range c.f.Keys {
			if k.AgentID == "" {
				c.f.Members.AdminKeys++
			}
		}
	}
	return err
}

func (c *collector) connectors(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT c.id, c.name, c.type, c.status,
	    COALESCE(c.config #> '{witness,observe,enabled}' = 'true'::jsonb, false),
	    cr.version, cr.alg, cr.kek_id, cr.updated_at, cr.rotated_at
	  FROM deedglass.connectors AS c
	  LEFT JOIN deedglass.credentials AS cr ON cr.org_id = c.org_id AND cr.connector_id = c.id
	  WHERE c.org_id = $1 AND c.deleted_at IS NULL ORDER BY c.name`, c.in.Org)
	if err != nil {
		return err
	}
	c.f.Connectors, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (securitypack.Connector, error) {
		var x securitypack.Connector
		var ver *int
		var alg, kek *string
		var upd *time.Time
		var rot *time.Time
		if err := r.Scan(&x.ID, &x.Name, &x.Type, &x.Status, &x.Observe, &ver, &alg, &kek, &upd, &rot); err != nil {
			return x, err
		}
		// Every connector type can write; only Postgres writes can be undone from the record.
		x.Writes, x.Undo = true, x.Type == "postgres"
		if ver != nil {
			x.Credential = &securitypack.Credential{Version: *ver, Alg: deref(alg), KEK: deref(kek), RotatedAt: rot}
			if upd != nil {
				x.Credential.UpdatedAt = *upd
			}
		}
		return x, nil
	})
	return err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (c *collector) members(ctx context.Context, tx pgx.Tx) error {
	return tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FROM deedglass.memberships
	  WHERE org_id = $1 AND status = 'active' AND role IN ('owner', 'admin')`, c.in.Org).Scan(&c.f.Members.OwnersAdmins)
}

func (c *collector) rules(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT a.id, a.policy_id, p.name, v.version, v.id, v.version_hash, COALESCE(a.deed_id, ''),
	    COALESCE(a.activated_by, ''), a.activated_at, a.mode, v.yaml
	  FROM deedglass.policy_activations AS a
	  JOIN deedglass.policy_versions AS v ON v.org_id = a.org_id AND v.id = a.version_id
	  JOIN deedglass.policies AS p ON p.org_id = a.org_id AND p.id = a.policy_id
	  WHERE a.org_id = $1 AND a.deactivated_at IS NULL`, c.in.Org)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v securitypack.ActiveVersion
		var mode string
		if err := rows.Scan(&v.ActivationID, &v.PolicyID, &v.Name, &v.Version, &v.VersionID, &v.VersionHash, &v.DeedID,
			&v.ActivatedBy, &v.ActivatedAt, &mode, &v.YAML); err != nil {
			rows.Close()
			return err
		}
		if mode == "enforce" {
			c.f.Rules.Enforce = &v
		} else {
			c.f.Rules.Shadow = &v
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Changes: every live activation, plus activations and deactivations in the period.
	rows, err = tx.Query(ctx, `SELECT id, COALESCE(deed_id, ''), COALESCE(activated_by, ''), activated_at
	  FROM deedglass.policy_activations WHERE org_id = $1 AND (deactivated_at IS NULL OR activated_at >= $2)
	  UNION ALL
	  SELECT id, COALESCE(deactivation_deed_id, ''), COALESCE(deactivated_by, ''), deactivated_at
	  FROM deedglass.policy_activations WHERE org_id = $1 AND deactivated_at >= $2
	  ORDER BY 4, 1`, c.in.Org, c.in.Period.From)
	if err != nil {
		return err
	}
	c.f.Rules.Changes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (securitypack.RuleChange, error) {
		var x securitypack.RuleChange
		err := r.Scan(&x.ActivationID, &x.DeedID, &x.By, &x.At)
		return x, err
	})
	return err
}

func (c *collector) approvals(ctx context.Context, tx pgx.Tx) error {
	a := &c.f.Approvals
	var median *float64
	err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::int,
	    pg_catalog.count(*) FILTER (WHERE status = 'approved')::int,
	    pg_catalog.count(*) FILTER (WHERE status = 'denied')::int,
	    pg_catalog.count(*) FILTER (WHERE status = 'expired')::int,
	    pg_catalog.count(*) FILTER (WHERE status = 'cancelled')::int,
	    pg_catalog.count(*) FILTER (WHERE approvers_required >= 2)::int,
	    pg_catalog.percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM decided_at - created_at))
	      FILTER (WHERE status IN ('approved', 'denied'))
	  FROM deedglass.approvals WHERE org_id = $1 AND created_at >= $2 AND created_at < $3`,
		c.in.Org, c.in.Period.From, c.in.Period.To).Scan(&a.Total, &a.Approved, &a.Denied, &a.Expired, &a.Cancelled, &a.MultiApprover, &median)
	if err != nil {
		return err
	}
	a.MedianDecideSeconds = -1
	if median != nil {
		a.MedianDecideSeconds = int64(*median)
	}
	return tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::int FROM deedglass.approvals
	  WHERE org_id = $1 AND status = 'pending' AND expires_at < $2`, c.in.Org, c.in.Now).Scan(&a.PendingOverdue)
}

func (c *collector) stop(ctx context.Context, tx pgx.Tx) error {
	s := &c.f.Stop
	err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FILTER (WHERE created_at >= $2)::int
	  FROM deedglass.admin_events WHERE org_id = $1 AND action IN ('agent.stopped', 'org.stopped_all')`,
		c.in.Org, c.in.Period.From).Scan(&s.InPeriod)
	if err != nil {
		return err
	}
	var at time.Time
	var dd string
	err = tx.QueryRow(ctx, `SELECT created_at, COALESCE(deed_id, '') FROM deedglass.admin_events
	  WHERE org_id = $1 AND action IN ('agent.stopped', 'org.stopped_all') ORDER BY created_at DESC, id DESC LIMIT 1`, c.in.Org).Scan(&at, &dd)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	s.LastAt, s.LastDeed = &at, dd
	return nil
}

// readTool reports whether a tool's short name reads only. Anything else counts as a write
// (conservative: an unknown tool is a write, so it needs a check to count as checked).
func readTool(tool string) bool {
	_, t, _ := strings.Cut(tool, ".")
	if t == "query" {
		return true
	}
	for _, p := range []string{"get_", "list_", "search", "read_", "describe"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// inFlight are the states an action passes through before it is finished.
const inFlight = `'received', 'authenticated', 'evaluated', 'pending_approval', 'executing', 'witnessing'`

func (c *collector) actions(ctx context.Context, tx pgx.Tx) error {
	a := &c.f.Actions
	rows, err := tx.Query(ctx, `SELECT tool, mode, COALESCE(witness_level, ''), pg_catalog.count(*)::int
	  FROM deedglass.actions WHERE org_id = $1 AND created_at >= $2 AND created_at < $3 AND executed_at IS NOT NULL
	  GROUP BY 1, 2, 3`, c.in.Org, c.in.Period.From, c.in.Period.To)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tool, mode, level string
		var n int
		if err := rows.Scan(&tool, &mode, &level, &n); err != nil {
			rows.Close()
			return err
		}
		if mode == "proxy" {
			a.Proxy += n
		} else {
			a.Gateway += n
		}
		if readTool(tool) {
			continue
		}
		a.Writes += n
		switch level {
		case "L1":
			a.Checked += n
		case "L2":
			a.Proven += n
		default:
			a.Claimed += n
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var open []string
	err = tx.QueryRow(ctx, `SELECT pg_catalog.count(*) FILTER (WHERE witness_verdict = 'VERIFIED')::int,
	    pg_catalog.count(*) FILTER (WHERE witness_verdict = 'MISMATCH')::int,
	    pg_catalog.count(*) FILTER (WHERE witness_verdict = 'UNVERIFIABLE')::int,
	    pg_catalog.count(*) FILTER (WHERE witness_verdict = 'MISMATCH' AND EXISTS (SELECT 1 FROM deedglass.actions AS u
	      WHERE u.org_id = a.org_id AND u.undoes_action_id = a.id))::int,
	    (pg_catalog.array_agg(a.id::text ORDER BY a.created_at) FILTER (WHERE witness_verdict = 'MISMATCH' AND NOT EXISTS (
	      SELECT 1 FROM deedglass.actions AS u WHERE u.org_id = a.org_id AND u.undoes_action_id = a.id)))[1:5],
	    pg_catalog.count(*) FILTER (WHERE deed_id IS NOT NULL OR (state NOT IN (`+inFlight+`) AND updated_at < $4))::int,
	    pg_catalog.count(*) FILTER (WHERE deed_id IS NULL AND state NOT IN (`+inFlight+`) AND updated_at < $4)::int
	  FROM deedglass.actions AS a WHERE a.org_id = $1 AND a.created_at >= $2 AND a.created_at < $3`,
		c.in.Org, c.in.Period.From, c.in.Period.To, c.in.Now.Add(-UnsealedGrace)).Scan(
		&a.Matches, &a.Mismatch, &a.CantChk, &a.MismatchFollowed, &open, &a.Finished, &a.Unsealed)
	a.MismatchOpen = open
	return err
}

// UnsealedGrace is how long a finished action may wait for its deed before it counts as unsealed
// (the worker seals within seconds; this leaves room for a restart).
const UnsealedGrace = 5 * time.Minute

func (c *collector) sessions(ctx context.Context, tx pgx.Tx) error {
	s := &c.f.Sessions
	return tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::int,
	    pg_catalog.count(*) FILTER (WHERE state IN ('sealed', 'undoing', 'undone'))::int,
	    pg_catalog.count(*) FILTER (WHERE 'caught' = ANY (flags))::int
	  FROM deedglass.observe_sessions WHERE org_id = $1 AND opened_at >= $2 AND opened_at < $3`,
		c.in.Org, c.in.Period.From, c.in.Period.To).Scan(&s.Total, &s.Sealed, &s.Caught)
}

func (c *collector) undos(ctx context.Context, tx pgx.Tx) error {
	u := &c.f.Undos
	err := tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::int, pg_catalog.count(*) FILTER (WHERE witness_verdict = 'VERIFIED')::int
	  FROM deedglass.actions WHERE org_id = $1 AND undoes_action_id IS NOT NULL AND created_at >= $2 AND created_at < $3
	    AND state NOT IN (`+inFlight+`, 'denied', 'expired', 'cancelled')`,
		c.in.Org, c.in.Period.From, c.in.Period.To).Scan(&u.Actions, &u.Matched)
	if err != nil {
		return err
	}
	return tx.QueryRow(ctx, `SELECT pg_catalog.count(*)::int, pg_catalog.count(*) FILTER (WHERE state = 'undone')::int
	  FROM deedglass.observe_sessions WHERE org_id = $1 AND undo_action_id IS NOT NULL AND state IN ('undoing', 'undone')
	    AND opened_at >= $2 AND opened_at < $3`, c.in.Org, c.in.Period.From, c.in.Period.To).Scan(&u.Sessions, &u.SessionsDone)
}

// records runs the Ledger's chain check over the period (anchored on the deed before it) and reads
// the seal keys. An incomplete check is Can't tell, never intact.
func (s *Source) records(ctx context.Context, f *securitypack.Facts, in Input) {
	if s.Ledger == nil {
		f.Fail(securitypack.CollectorRecords, "this server has no Ledger")
		return
	}
	r := &f.Records
	r.Signer = s.Ledger.SignerKeyID()
	ks, err := s.Ledger.SealKeySet(ctx)
	if err != nil {
		f.Fail(securitypack.CollectorRecords, "the seal keys couldn't be read")
		return
	}
	for _, k := range ks.Keys {
		at, _ := time.Parse(time.RFC3339Nano, k.ActivatedAt)
		r.SealKeys = append(r.SealKeys, securitypack.SealKey{ID: k.ID, Status: k.Status, ActivatedAt: at})
	}
	from := in.ChainFrom
	if from.IsZero() {
		from = in.Period.From
	}
	opts := ledger.CheckOptions{Budget: in.ChainBudget}
	cc, err := s.Ledger.CheckChain(ctx, in.Org, ledger.Range{IssuedGTE: &from}, opts)
	if errors.Is(err, ledger.ErrEmptyRange) {
		// Nothing issued since from: check the last record and its link instead of nothing.
		if head, herr := s.Ledger.HeadSeq(ctx, in.Org); herr == nil && head > 0 {
			cc, err = s.Ledger.CheckChain(ctx, in.Org, ledger.Range{FromSeq: head}, opts)
		}
	}
	switch {
	case errors.Is(err, ledger.ErrEmptyRange):
		r.ChainIntact = true // no records yet: nothing is broken
		return
	case err != nil:
		f.Fail(securitypack.CollectorRecords, "the chain check didn't finish")
		return
	}
	r.Checked = cc.Checked
	switch cc.Status {
	case ledger.CheckIntact:
		r.ChainIntact = true
	case ledger.CheckBroken:
		r.FirstBroken = "an unknown record"
		if cc.Break != nil && cc.Break.Deed != nil {
			r.FirstBroken = *cc.Break.Deed
		}
	default:
		f.Fail(securitypack.CollectorRecords, "the chain check ran out of time; run deedglass ledger check")
		return
	}
	if cc.Head != nil {
		r.ChainHead, r.HeadSeq = cc.Head.BodyHash, cc.Head.Seq
	}
}
