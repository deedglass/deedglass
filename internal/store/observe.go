package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// Observe mode's storage (S50, migration 0029): watched sessions and the frames they hold. Every
// statement is a constant and runs in an org transaction with row-level security on, except
// ClaimObserveOrgs (claim_observe_orgs, ids only), named only here.

// ObserveSession is one observe_sessions row.
type ObserveSession struct {
	ID           string
	OrgID        string
	ConnectorID  string
	Label        *string
	AgentID      *string
	OpenedByType string
	OpenedByID   *string
	OpenedAt     time.Time
	ClosedAt     *time.Time
	ClosedByType *string
	ClosedByID   *string
	State        string
	Flags        []string
	Summary      json.RawMessage
	SealDeedID   *string
	UndoActionID *string
	CreatedAt    time.Time
}

const observeCols = `id, org_id, connector_id, label, agent_id, opened_by_type, opened_by_id, opened_at, closed_at, closed_by_type,
  closed_by_id, state, flags, summary, seal_deed_id, undo_action_id, created_at`

func scanObserve(row pgx.Row) (ObserveSession, error) {
	var s ObserveSession
	var summary []byte
	err := row.Scan(&s.ID, &s.OrgID, &s.ConnectorID, &s.Label, &s.AgentID, &s.OpenedByType, &s.OpenedByID, &s.OpenedAt, &s.ClosedAt,
		&s.ClosedByType, &s.ClosedByID, &s.State, &s.Flags, &summary, &s.SealDeedID, &s.UndoActionID, &s.CreatedAt)
	s.Summary = summary
	return s, notFound(err)
}

// InsertObserveSession opens a session (state open). opened_at comes from the database clock.
func InsertObserveSession(ctx context.Context, tx pgx.Tx, s ObserveSession) (ObserveSession, error) {
	return scanObserve(tx.QueryRow(ctx, `INSERT INTO deedglass.observe_sessions (id, org_id, connector_id, label, agent_id,
	    opened_by_type, opened_by_id) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+observeCols,
		s.ID, s.OrgID, s.ConnectorID, s.Label, s.AgentID, s.OpenedByType, s.OpenedByID))
}

// GetObserveSession returns one session of this org, or ErrNotFound.
func GetObserveSession(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (ObserveSession, error) {
	q := `SELECT ` + observeCols + ` FROM deedglass.observe_sessions WHERE id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scanObserve(tx.QueryRow(ctx, q, id))
}

// ObserveFilter is GET /v1/observe/sessions' filter and cursor.
type ObserveFilter struct {
	ConnectorID string
	State       string
	Caught      *bool // the "caught" flag
	Limit       int
	AfterAt     *time.Time // cursor: sessions opened strictly before (AfterAt, AfterID)
	AfterID     string
}

// ListObserveSessions returns this org's sessions, newest first.
func ListObserveSessions(ctx context.Context, tx pgx.Tx, f ObserveFilter) ([]ObserveSession, error) {
	limit := f.Limit
	if limit <= 0 || limit > 101 {
		limit = 101
	}
	rows, err := tx.Query(ctx, `SELECT `+observeCols+` FROM deedglass.observe_sessions
	  WHERE ($1 = '' OR connector_id = $1) AND ($2 = '' OR state = $2)
	    AND ($3::boolean IS NULL OR ('caught' = ANY (flags)) = $3::boolean)
	    AND ($4::timestamptz IS NULL OR (opened_at, id) < ($4::timestamptz, $5))
	  ORDER BY opened_at DESC, id DESC LIMIT $6`, f.ConnectorID, f.State, f.Caught, f.AfterAt, f.AfterID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ObserveSession, error) { return scanObserve(r) })
}

// LiveObserveSessions returns this org's sessions that are open, closing or undoing, oldest first.
func LiveObserveSessions(ctx context.Context, tx pgx.Tx) ([]ObserveSession, error) {
	rows, err := tx.Query(ctx, `SELECT `+observeCols+` FROM deedglass.observe_sessions
	  WHERE state IN ('open', 'closing', 'undoing') ORDER BY opened_at, id FOR UPDATE`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ObserveSession, error) { return scanObserve(r) })
}

// CloseObserveSession moves an open session to closing at closedAt (nil: the database clock now).
// It reports whether the row moved.
func CloseObserveSession(ctx context.Context, tx pgx.Tx, id string, closedAt *time.Time, byType string, byID *string, flags []string) (bool, error) {
	if flags == nil {
		flags = []string{}
	}
	tag, err := tx.Exec(ctx, `UPDATE deedglass.observe_sessions SET state = 'closing',
	    closed_at = GREATEST(opened_at, COALESCE($2::timestamptz, pg_catalog.now())), closed_by_type = $3, closed_by_id = $4,
	    flags = ARRAY(SELECT DISTINCT f FROM pg_catalog.unnest(flags || $5::text[]) AS f ORDER BY 1)
	  WHERE id = $1 AND state = 'open'`, id, closedAt, byType, byID, flags)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// AddObserveFlags adds flags to a session (kept sorted and unique).
func AddObserveFlags(ctx context.Context, tx pgx.Tx, id string, flags []string) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.observe_sessions SET
	    flags = ARRAY(SELECT DISTINCT f FROM pg_catalog.unnest(flags || $2::text[]) AS f ORDER BY 1)
	  WHERE id = $1 AND NOT (flags @> $2::text[])`, id, flags)
	return err
}

// SetObserveSummary writes a session's running summary while it is open or closing.
func SetObserveSummary(ctx context.Context, tx pgx.Tx, id string, summary json.RawMessage) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.observe_sessions SET summary = $2::jsonb
	  WHERE id = $1 AND state IN ('open', 'closing') AND summary IS DISTINCT FROM $2::jsonb`, id, string(summary))
	return err
}

// SealObserveSession moves a closing session to sealed with its final summary and seal deed.
func SealObserveSession(ctx context.Context, tx pgx.Tx, id string, summary json.RawMessage, deedID string, flags []string) error {
	if flags == nil {
		flags = []string{}
	}
	tag, err := tx.Exec(ctx, `UPDATE deedglass.observe_sessions SET state = 'sealed', summary = $2::jsonb, seal_deed_id = $3,
	    flags = ARRAY(SELECT DISTINCT f FROM pg_catalog.unnest(flags || $4::text[]) AS f ORDER BY 1)
	  WHERE id = $1 AND state = 'closing'`, id, string(summary), deedID, flags)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// StartObserveUndo moves a sealed session to undoing with the undo action's id. It reports false
// when the session isn't sealed any more (another undo started first).
func StartObserveUndo(ctx context.Context, tx pgx.Tx, id, actionID string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.observe_sessions SET state = 'undoing', undo_action_id = $2
	  WHERE id = $1 AND state = 'sealed'`, id, actionID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// FinishObserveUndo ends an undoing session: undone when the undo applied, else sealed again.
func FinishObserveUndo(ctx context.Context, tx pgx.Tx, id string, applied bool) error {
	to := "sealed"
	if applied {
		to = "undone"
	}
	_, err := tx.Exec(ctx, `UPDATE deedglass.observe_sessions SET state = $2 WHERE id = $1 AND state = 'undoing'`, id, to)
	return err
}

// ObserveCandidate is an unattributed frame of a connector, without its images.
type ObserveCandidate struct {
	ID         string
	CommitTime time.Time
	Tables     []string
}

// ObserveCandidates returns the unattributed frames of connectorID committed in [from, to], oldest
// first, with the tables each one touched (at most 5,000 per call).
func ObserveCandidates(ctx context.Context, tx pgx.Tx, connectorID string, from, to time.Time) ([]ObserveCandidate, error) {
	rows, err := tx.Query(ctx, `SELECT f.id, f.commit_time,
	    ARRAY(SELECT k FROM pg_catalog.jsonb_object_keys(f.counts) AS k ORDER BY 1)
	  FROM deedglass.decoded_txs AS f
	  WHERE f.connector_id = $1 AND f.class = 'unattributed' AND f.commit_time >= $2 AND f.commit_time <= $3
	  ORDER BY f.commit_time, f.id LIMIT 5000`, connectorID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ObserveCandidate, error) {
		var c ObserveCandidate
		err := r.Scan(&c.ID, &c.CommitTime, &c.Tables)
		return c, err
	})
}

// AttachObserveFrames attaches frames to a session and returns how many were new.
func AttachObserveFrames(ctx context.Context, tx pgx.Tx, org, session string, frameIDs []string) (int, error) {
	if len(frameIDs) == 0 {
		return 0, nil
	}
	tag, err := tx.Exec(ctx, `INSERT INTO deedglass.observe_frames (org_id, session_id, frame_id)
	  SELECT $1, $2, f FROM pg_catalog.unnest($3::text[]) AS f ON CONFLICT DO NOTHING`, org, session, frameIDs)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// ObserveOverlaps returns, of sessionIDs, those holding a frame another session also holds.
func ObserveOverlaps(ctx context.Context, tx pgx.Tx, sessionIDs []string) ([]string, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT a.session_id FROM deedglass.observe_frames AS a
	  JOIN deedglass.observe_frames AS b ON b.org_id = a.org_id AND b.frame_id = a.frame_id AND b.session_id <> a.session_id
	  WHERE a.session_id = ANY ($1::text[]) ORDER BY 1`, sessionIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ObserveFrameRow is one attached frame with its stored facts (no images). Expired is true when the
// frame itself is gone (decoder retention).
type ObserveFrameRow struct {
	FrameID       string
	Excluded      bool
	ExcludedBy    *string
	AttachedAt    time.Time
	Expired       bool
	CommitTime    *time.Time
	CommitLSN     *string
	Xid           *int64
	Counts        json.RawMessage
	ChangeCount   int
	ChangesDigest *string
	Flags         []string
	Changes       json.RawMessage // only when asked for
	Tables        json.RawMessage // only when asked for
}

// ObserveFrames returns a session's frames, oldest commit first (expired ones last). With images,
// each row also carries tables and changes (the planner's and the diff's input).
func ObserveFrames(ctx context.Context, tx pgx.Tx, session string, images bool) ([]ObserveFrameRow, error) {
	rows, err := tx.Query(ctx, `SELECT o.frame_id, o.excluded, o.excluded_by, o.attached_at, f.id IS NULL, f.commit_time,
	    f.commit_lsn::text, f.xid, f.counts, COALESCE(f.change_count, 0), f.changes_digest, COALESCE(f.flags, '{}'),
	    CASE WHEN $2 THEN f.changes END, CASE WHEN $2 THEN f.tables END
	  FROM deedglass.observe_frames AS o
	  LEFT JOIN deedglass.decoded_txs AS f ON f.id = o.frame_id
	  WHERE o.session_id = $1
	  ORDER BY f.commit_time NULLS LAST, f.commit_lsn NULLS LAST, o.frame_id LIMIT 20000`, session, images)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ObserveFrameRow, error) {
		var x ObserveFrameRow
		var counts, changes, tables []byte
		err := r.Scan(&x.FrameID, &x.Excluded, &x.ExcludedBy, &x.AttachedAt, &x.Expired, &x.CommitTime, &x.CommitLSN, &x.Xid, &counts,
			&x.ChangeCount, &x.ChangesDigest, &x.Flags, &changes, &tables)
		x.Counts, x.Changes, x.Tables = counts, changes, tables
		return x, err
	})
}

// frameColsF is frameCols qualified with the alias f.
const frameColsF = `f.id, f.org_id, f.connector_id, f.system_id, f.timeline_id, f.commit_lsn::text, f.end_lsn::text, f.xid, f.commit_time,
  f.class, f.action_id, f.marker_lsn::text, f.nontx, f.ledger, f.markers, f.tables, f.counts, f.changes, f.change_count, f.changes_digest,
  f.flags, f.created_at`

// ObserveDecodedFrames returns the full decoded frames a session holds (excluded ones left out
// unless asked), oldest commit first: the session undo's evidence.
func ObserveDecodedFrames(ctx context.Context, tx pgx.Tx, session string, withExcluded bool) ([]DecodedFrame, error) {
	rows, err := tx.Query(ctx, `SELECT `+frameColsF+` FROM deedglass.observe_frames AS o
	  JOIN deedglass.decoded_txs AS f ON f.id = o.frame_id
	  WHERE o.session_id = $1 AND ($2 OR NOT o.excluded)
	  ORDER BY f.commit_time, f.commit_lsn, f.id LIMIT 20000`, session, withExcluded)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DecodedFrame, error) { return scanFrame(r) })
}

// SetObserveFrameExcluded sets one attached frame's excluded flag. It reports whether the frame is
// attached to the session (and so whether anything could change).
func SetObserveFrameExcluded(ctx context.Context, tx pgx.Tx, session, frame string, excluded bool, by string) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.observe_frames SET excluded = $3,
	    excluded_by = CASE WHEN $3 THEN $4 END, excluded_at = CASE WHEN $3 THEN pg_catalog.now() END
	  WHERE session_id = $1 AND frame_id = $2`, session, frame, excluded, by)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClaimObserveOrgs lists the orgs with a session to attach to, close, seal or finish undoing.
func (db *DB) ClaimObserveOrgs(ctx context.Context, limit int) ([]ids.OrgID, error) {
	rows, err := db.pool.Query(ctx, `SELECT org_id FROM deedglass.claim_observe_orgs($1)`, limit)
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
			return nil, errors.Join(errors.New("store: claim_observe_orgs returned a malformed org id"), err)
		}
		out = append(out, o)
	}
	return out, nil
}

// DecodedFramesByID returns the stored frames with these ids (this org's), oldest commit first.
func DecodedFramesByID(ctx context.Context, tx pgx.Tx, frameIDs []string) ([]DecodedFrame, error) {
	if len(frameIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+frameCols+` FROM deedglass.decoded_txs WHERE id = ANY ($1::text[])
	  ORDER BY commit_time, commit_lsn, id`, frameIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DecodedFrame, error) { return scanFrame(r) })
}

// ConnectorAny returns a connector of this org by id, deleted or not (a session outlives its
// connector's deletion), or ErrNotFound.
func ConnectorAny(ctx context.Context, tx pgx.Tx, id string) (Connector, error) {
	return scanConnector(tx.QueryRow(ctx, `SELECT `+connectorCols+` FROM deedglass.connectors WHERE id = $1`, id))
}
