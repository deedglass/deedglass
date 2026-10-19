package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// PHI mode's storage (HC-P09, migration 0031). Every statement is a constant and runs in an org
// transaction with row-level security on, except ClaimPHIOrgs (claim_phi_orgs, ids only), named
// only here.

// PHIPending is a sealed PHI-mode action whose payloads are still stored.
type PHIPending struct {
	ID            string
	Tool          string
	Mode          string
	Request       json.RawMessage
	Result        json.RawMessage
	ErrorMessage  *string
	WitnessDetail json.RawMessage
}

// PHIMinimized is what replaces a PHIPending's payloads. The policy input is always cleared.
type PHIMinimized struct {
	ID            string
	Request       json.RawMessage
	Result        json.RawMessage
	ErrorMessage  *string
	WitnessDetail json.RawMessage
}

// ClaimPHIOrgs lists the orgs with a sealed PHI-mode action still holding its payloads.
func (db *DB) ClaimPHIOrgs(ctx context.Context, limit int) ([]ids.OrgID, error) {
	rows, err := db.pool.Query(ctx, `SELECT org_id FROM deedglass.claim_phi_orgs($1)`, limit)
	if err != nil {
		return nil, err
	}
	raw, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make([]ids.OrgID, 0, len(raw))
	for _, s := range raw {
		o, err := ids.Parse[ids.OrgKind](s)
		if err != nil {
			return nil, errors.Join(errors.New("store: claim_phi_orgs returned a malformed org id"), err)
		}
		out = append(out, o)
	}
	return out, nil
}

// PendingPHI locks up to limit of the org's sealed PHI-mode actions that still hold payloads and
// have no witness job open (a read-back or an L2 match still needs the request and result), oldest
// seal first. Rows another transaction holds are skipped.
func PendingPHI(ctx context.Context, tx pgx.Tx, org ids.OrgID, limit int) ([]PHIPending, error) {
	rows, err := tx.Query(ctx, `SELECT a.id, a.tool, a.mode, a.request, a.result, a.error_message, a.witness_detail
	    FROM deedglass.actions AS a
	    WHERE a.org_id = $1 AND a.phi AND a.payload_minimized_at IS NULL AND a.payload_pruned_at IS NULL AND a.state = 'sealed'
	      AND NOT EXISTS (SELECT 1 FROM deedglass.witness_jobs AS j
	                      WHERE j.org_id = a.org_id AND j.action_id = a.id AND j.kind IN ('witness', 'l1_readback', 'l2_match'))
	    ORDER BY a.sealed_at, a.id LIMIT $2 FOR UPDATE OF a SKIP LOCKED`, org, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PHIPending, error) {
		var p PHIPending
		var req, res, wd []byte
		err := r.Scan(&p.ID, &p.Tool, &p.Mode, &req, &res, &p.ErrorMessage, &wd)
		p.Request, p.Result, p.WitnessDetail = req, res, wd
		return p, err
	})
}

// MinimizePHI replaces one action's payloads (locked by PendingPHI in the same transaction) and
// clears its policy input. The digest columns stay: they are what the deed holds.
func MinimizePHI(ctx context.Context, tx pgx.Tx, org ids.OrgID, m PHIMinimized) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.actions SET request = $3::jsonb, result = $4::jsonb, error_message = $5,
	    witness_detail = $6::jsonb, policy_input = NULL, payload_minimized_at = pg_catalog.clock_timestamp()
	  WHERE org_id = $1 AND id = $2 AND payload_minimized_at IS NULL`,
		org, m.ID, nullJSON(m.Request), nullJSON(m.Result), m.ErrorMessage, nullJSON(m.WitnessDetail))
	return err
}

// MarkPHI marks an org's actions that still hold payloads as PHI-mode actions (turning PHI mode on
// covers the org's earlier actions too). It returns how many rows it marked.
func MarkPHI(ctx context.Context, tx pgx.Tx, org ids.OrgID) (int64, error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.actions SET phi = true
	  WHERE org_id = $1 AND NOT phi AND payload_minimized_at IS NULL AND payload_pruned_at IS NULL`, org)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// nullJSON is SQL NULL for an empty value, else the JSON text.
func nullJSON(b json.RawMessage) *string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	return &s
}
