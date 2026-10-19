package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// Version-bound L2 (ADR-108, migration 0032): the decoder binds a FHIR server's transaction to the
// action whose execution evidence names a version the transaction holds. Every statement is a
// constant and runs in an org transaction (RLS).

// VersionClaim is an action whose execution evidence names a resource version ("Type/id/version")
// and the binding token it sent.
type VersionClaim struct {
	ActionID   string
	VersionKey string
	Token      string
}

// VersionClaims returns the actions on the connector named name whose evidence names one of keys.
func VersionClaims(ctx context.Context, tx pgx.Tx, org ids.OrgID, name string, keys []string) ([]VersionClaim, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT a.id, a.execution_evidence ->> 'version_key', COALESCE(a.execution_evidence ->> 'binding_token', '')
	    FROM deedglass.actions AS a
	   WHERE a.org_id = $1 AND a.connector = $2 AND a.execution_evidence ? 'version_key'
	     AND (a.execution_evidence ->> 'version_key') = ANY ($3::text[])
	   ORDER BY a.id LIMIT 1000`, org, name, keys)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (VersionClaim, error) {
		var c VersionClaim
		err := r.Scan(&c.ActionID, &c.VersionKey, &c.Token)
		return c, err
	})
}

// TokenClaims returns, for each of tokens that an action on the connector named name sent, that
// action's id.
func TokenClaims(ctx context.Context, tx pgx.Tx, org ids.OrgID, name string, tokens []string) (map[string]string, error) {
	out := map[string]string{}
	if len(tokens) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT a.execution_evidence ->> 'binding_token', a.id FROM deedglass.actions AS a
	   WHERE a.org_id = $1 AND a.connector = $2 AND a.execution_evidence ? 'binding_token'
	     AND (a.execution_evidence ->> 'binding_token') = ANY ($3::text[])
	   ORDER BY a.id LIMIT 1000`, org, name, tokens)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tok, id string
		if err := rows.Scan(&tok, &id); err != nil {
			return nil, err
		}
		if _, ok := out[tok]; !ok {
			out[tok] = id
		}
	}
	return out, rows.Err()
}

// ConnectorBusy reports whether an action on the connector named name is executing: its call
// may still be running, so its execution evidence isn't recorded yet.
func ConnectorBusy(ctx context.Context, tx pgx.Tx, org ids.OrgID, name string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deedglass.actions WHERE org_id = $1 AND connector = $2 AND state = 'executing')`,
		org, name).Scan(&ok)
	return ok, err
}

// UnattributedFrames returns connectorID's unattributed frames that committed in [from, to],
// oldest first (at most 100): the out-of-band writes while an action's call was open.
func UnattributedFrames(ctx context.Context, tx pgx.Tx, connectorID string, from, to time.Time) ([]DecodedFrame, error) {
	rows, err := tx.Query(ctx, `SELECT `+frameCols+` FROM deedglass.decoded_txs
	   WHERE connector_id = $1 AND class = 'unattributed' AND commit_time BETWEEN $2 AND $3
	   ORDER BY commit_time, id LIMIT 100`, connectorID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DecodedFrame, error) { return scanFrame(r) })
}
