package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/ids"
)

// The decoder's storage (S20, migration 0019): decoded frames and the per-connector slot rows.
// Every statement is a constant. The cross-org reads are the SECURITY DEFINER functions
// claim_decoder_leases, sync_decoder_slots and maint_prune_decoded_txs, named only here
// (TestPreAuthNamesStayInStore); everything else runs in an org transaction with row-level
// security on.

// DecodedFrame is one decoded_txs row.
type DecodedFrame struct {
	ID            string
	OrgID         string
	ConnectorID   string
	SystemID      string
	TimelineID    int
	CommitLSN     *string // text LSN, X/Y; nil only for class missing
	EndLSN        *string
	Xid           *int64
	CommitTime    *time.Time
	Class         string
	ActionID      *string
	MarkerLSN     *string // evidence only (ADR-003 point 5)
	NonTx         bool
	Ledger        json.RawMessage // array of decoded ledger rows, or nil
	Markers       []string
	Tables        json.RawMessage // {"public.orders": {relid, pk, identity, columns, type_oids}}
	Counts        json.RawMessage // {"public.orders": {insert, update, delete, truncate}}
	Changes       json.RawMessage // [{seq, op, table, key, old, new, changed}], the first decoder.max_images_per_tx
	ChangeCount   int
	ChangesDigest *string
	Flags         []string
	CreatedAt     time.Time
}

// InsertFrames inserts frames (all of one connector) and reports, per frame, whether it was new:
// a frame already stored (the same connector, system, timeline, commit LSN and kind, or the same
// missing action) is left as it is (delivery is at least once, storage exactly once).
func InsertFrames(ctx context.Context, tx pgx.Tx, frames []DecodedFrame) ([]bool, error) {
	if len(frames) == 0 {
		return nil, nil
	}
	b := &pgx.Batch{}
	for _, f := range frames {
		if f.ID == "" {
			f.ID = ids.New[ids.DecodedTxKind]().String()
		}
		ledger := nullableJSON(f.Ledger)
		markers, flags := f.Markers, f.Flags
		if markers == nil {
			markers = []string{}
		}
		if flags == nil {
			flags = []string{}
		}
		b.Queue(`INSERT INTO deedglass.decoded_txs (id, org_id, connector_id, system_id, timeline_id, commit_lsn, end_lsn, xid,
		    commit_time, class, action_id, marker_lsn, nontx, ledger, markers, tables, counts, changes, change_count, changes_digest, flags)
		  VALUES ($1, $2, $3, $4, $5, $6::text::pg_lsn, $7::text::pg_lsn, $8, $9, $10, $11, $12::text::pg_lsn, $13, $14::jsonb, $15,
		    $16::jsonb, $17::jsonb, $18::jsonb, $19, $20, $21)
		  ON CONFLICT DO NOTHING RETURNING true`,
			f.ID, f.OrgID, f.ConnectorID, f.SystemID, f.TimelineID, f.CommitLSN, f.EndLSN, f.Xid, f.CommitTime, f.Class, f.ActionID,
			f.MarkerLSN, f.NonTx, ledger, markers, jsonOr(f.Tables, "{}"), jsonOr(f.Counts, "{}"), jsonOr(f.Changes, "[]"),
			f.ChangeCount, f.ChangesDigest, flags)
	}
	br := tx.SendBatch(ctx, b)
	out := make([]bool, len(frames))
	for i := range frames {
		var ok bool
		err := br.QueryRow().Scan(&ok)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			_ = br.Close()
			return nil, err
		default:
			out[i] = ok
		}
	}
	return out, br.Close()
}

func nullableJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func jsonOr(b json.RawMessage, def string) string {
	if len(b) == 0 {
		return def
	}
	return string(b)
}

const frameCols = `id, org_id, connector_id, system_id, timeline_id, commit_lsn::text, end_lsn::text, xid, commit_time, class, action_id,
  marker_lsn::text, nontx, ledger, markers, tables, counts, changes, change_count, changes_digest, flags, created_at`

func scanFrame(row pgx.Row) (DecodedFrame, error) {
	var f DecodedFrame
	var ledger, tables, counts, changes []byte
	err := row.Scan(&f.ID, &f.OrgID, &f.ConnectorID, &f.SystemID, &f.TimelineID, &f.CommitLSN, &f.EndLSN, &f.Xid, &f.CommitTime,
		&f.Class, &f.ActionID, &f.MarkerLSN, &f.NonTx, &ledger, &f.Markers, &tables, &counts, &changes, &f.ChangeCount,
		&f.ChangesDigest, &f.Flags, &f.CreatedAt)
	f.Ledger, f.Tables, f.Counts, f.Changes = ledger, tables, counts, changes
	return f, err
}

// FramesForAction returns the frames that name actionID (bound, marker_count, marker_mismatch,
// publication_gap, missing), oldest first: S21's input.
func FramesForAction(ctx context.Context, tx pgx.Tx, actionID string) ([]DecodedFrame, error) {
	rows, err := tx.Query(ctx, `SELECT `+frameCols+` FROM deedglass.decoded_txs WHERE action_id = $1 ORDER BY created_at, id LIMIT 100`, actionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DecodedFrame, error) { return scanFrame(r) })
}

// RecentFrames returns a connector's newest stored frames, newest first (at most n, ≤ 50), without
// their images.
func RecentFrames(ctx context.Context, tx pgx.Tx, connectorID string, n int) ([]DecodedFrame, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `SELECT id, org_id, connector_id, system_id, timeline_id, commit_lsn::text, end_lsn::text, xid, commit_time,
	    class, action_id, marker_lsn::text, nontx, ledger, markers, tables, counts, NULL::jsonb, change_count, changes_digest, flags, created_at
	  FROM deedglass.decoded_txs WHERE connector_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, connectorID, min(n, 50))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DecodedFrame, error) { return scanFrame(r) })
}

// FramedActions returns which of actionIDs already have a stored frame for connectorID (any class).
func FramedActions(ctx context.Context, tx pgx.Tx, connectorID string, actionIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(actionIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT action_id FROM deedglass.decoded_txs
	    WHERE connector_id = $1 AND action_id = ANY($2::text[])`, connectorID, actionIDs)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// DecoderSlot is one decoder_slots row.
type DecoderSlot struct {
	OrgID              string
	ConnectorID        string
	SlotName           string
	Publication        string
	Status             string
	StatusReason       *string
	StatusDetail       json.RawMessage
	StatusSince        time.Time
	SlotState          string
	SystemID           *string
	TimelineID         *int
	ServerVersionNum   *int
	CheckpointLSN      *string
	ConfirmedFlushLSN  *string
	RestartLSN         *string
	CurrentWALLSN      *string
	LagBytes           *int64
	RetainedBytes      *int64
	LagSeconds         *float64
	WALStatus          *string
	LastCommitLSN      *string
	LastCommitAt       *time.Time
	LastHeartbeatAt    *time.Time
	Frames             map[string]int64
	Shapes             map[string]string
	Watching           []string
	ReconcileWatermark *time.Time
	SlotCreatedAt      *time.Time
	LeasedBy           *string
	LeaseUntil         *time.Time
	UpdatedAt          time.Time
}

const slotCols = `org_id, connector_id, slot_name, publication, status, status_reason, status_detail, status_since, slot_state,
  system_id, timeline_id, server_version_num, checkpoint_lsn::text, confirmed_flush_lsn::text, restart_lsn::text, current_wal_lsn::text,
  lag_bytes, retained_bytes, lag_seconds, wal_status, last_commit_lsn::text, last_commit_at, last_heartbeat_at, frames, shapes, watching,
  reconcile_watermark, slot_created_at, leased_by, lease_until, updated_at`

func scanSlot(row pgx.Row) (DecoderSlot, error) {
	var s DecoderSlot
	var detail, frames, shapes []byte
	err := row.Scan(&s.OrgID, &s.ConnectorID, &s.SlotName, &s.Publication, &s.Status, &s.StatusReason, &detail, &s.StatusSince,
		&s.SlotState, &s.SystemID, &s.TimelineID, &s.ServerVersionNum, &s.CheckpointLSN, &s.ConfirmedFlushLSN, &s.RestartLSN,
		&s.CurrentWALLSN, &s.LagBytes, &s.RetainedBytes, &s.LagSeconds, &s.WALStatus, &s.LastCommitLSN, &s.LastCommitAt,
		&s.LastHeartbeatAt, &frames, &shapes, &s.Watching, &s.ReconcileWatermark, &s.SlotCreatedAt, &s.LeasedBy, &s.LeaseUntil, &s.UpdatedAt)
	if err != nil {
		return s, notFound(err)
	}
	s.StatusDetail = detail
	if len(frames) > 0 {
		_ = json.Unmarshal(frames, &s.Frames)
	}
	if len(shapes) > 0 {
		_ = json.Unmarshal(shapes, &s.Shapes)
	}
	return s, nil
}

// GetDecoderSlot returns the slot row of connectorID (this org), or ErrNotFound.
func GetDecoderSlot(ctx context.Context, tx pgx.Tx, connectorID string, forUpdate bool) (DecoderSlot, error) {
	q := `SELECT ` + slotCols + ` FROM deedglass.decoder_slots WHERE connector_id = $1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	return scanSlot(tx.QueryRow(ctx, q, connectorID))
}

// ListDecoderSlots returns this org's slot rows, by connector id.
func ListDecoderSlots(ctx context.Context, tx pgx.Tx) ([]DecoderSlot, error) {
	rows, err := tx.Query(ctx, `SELECT `+slotCols+` FROM deedglass.decoder_slots ORDER BY connector_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DecoderSlot, error) { return scanSlot(r) })
}

// SlotProgress is what one poll writes on the slot row, in the same transaction as its frames.
type SlotProgress struct {
	CheckpointLSN   string // the advance target
	LastCommitLSN   *string
	LastCommitAt    *time.Time
	LastHeartbeatAt *time.Time
	Frames          map[string]int64 // counters per class (and heartbeats)
	Shapes          map[string]string
}

// SaveProgress writes p on connectorID's row, but only while worker holds a live lease and the
// row isn't dropped or disabled: the fence that keeps a runner whose lease ended from moving the
// checkpoint. It reports whether the row was written.
func SaveProgress(ctx context.Context, tx pgx.Tx, connectorID, worker string, p SlotProgress) (bool, error) {
	frames, _ := json.Marshal(p.Frames)
	shapes, _ := json.Marshal(p.Shapes)
	tag, err := tx.Exec(ctx, `UPDATE deedglass.decoder_slots SET checkpoint_lsn = $3::text::pg_lsn,
	    last_commit_lsn = COALESCE($4::text::pg_lsn, last_commit_lsn), last_commit_at = COALESCE($5, last_commit_at),
	    last_heartbeat_at = COALESCE($6, last_heartbeat_at), frames = $7::jsonb, shapes = $8::jsonb
	  WHERE connector_id = $1 AND leased_by = $2 AND lease_until > pg_catalog.now() AND status NOT IN ('dropped', 'disabled')`,
		connectorID, worker, p.CheckpointLSN, p.LastCommitLSN, p.LastCommitAt, p.LastHeartbeatAt, string(frames), string(shapes))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SlotHealth is the runner's view of its slot and status, written every few seconds and on every
// status change.
type SlotHealth struct {
	Status            string
	Reason            *string
	Detail            json.RawMessage
	SystemID          *string
	TimelineID        *int
	ServerVersionNum  *int
	ConfirmedFlushLSN *string
	RestartLSN        *string
	CurrentWALLSN     *string
	LagBytes          *int64
	RetainedBytes     *int64
	LagSeconds        *float64
	WALStatus         *string
	Watching          []string
}

// SaveHealth writes h on connectorID's row under the same lease fence as SaveProgress. The
// status_since time moves only when the status or reason changes.
func SaveHealth(ctx context.Context, tx pgx.Tx, connectorID, worker string, h SlotHealth) (bool, error) {
	detail := jsonOr(h.Detail, "{}")
	watching := h.Watching
	if watching == nil {
		watching = []string{}
	}
	tag, err := tx.Exec(ctx, `UPDATE deedglass.decoder_slots SET
	    status_since = CASE WHEN status IS DISTINCT FROM $3 OR status_reason IS DISTINCT FROM $4 THEN pg_catalog.now() ELSE status_since END,
	    status = $3, status_reason = $4, status_detail = $5::jsonb,
	    system_id = COALESCE($6, system_id), timeline_id = COALESCE($7, timeline_id), server_version_num = COALESCE($8, server_version_num),
	    confirmed_flush_lsn = COALESCE($9::text::pg_lsn, confirmed_flush_lsn), restart_lsn = COALESCE($10::text::pg_lsn, restart_lsn),
	    current_wal_lsn = COALESCE($11::text::pg_lsn, current_wal_lsn), lag_bytes = COALESCE($12, lag_bytes),
	    retained_bytes = COALESCE($13, retained_bytes), lag_seconds = COALESCE($14, lag_seconds), wal_status = COALESCE($15, wal_status),
	    watching = $16
	  WHERE connector_id = $1 AND leased_by = $2 AND lease_until > pg_catalog.now() AND status NOT IN ('dropped', 'disabled')`,
		connectorID, worker, h.Status, h.Reason, detail, h.SystemID, h.TimelineID, h.ServerVersionNum, h.ConfirmedFlushLSN, h.RestartLSN,
		h.CurrentWALLSN, h.LagBytes, h.RetainedBytes, h.LagSeconds, h.WALStatus, watching)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkSlotCreated records that the runner created the slot (its system id and the time). The first
// slot also starts the reconcile watermark; a later one (a new baseline) keeps it, so reconcile
// finds the actions committed while there was no slot.
func MarkSlotCreated(ctx context.Context, tx pgx.Tx, connectorID, worker, systemID string, timeline int) (bool, error) {
	tag, err := tx.Exec(ctx, `UPDATE deedglass.decoder_slots SET slot_state = 'created', slot_created_at = pg_catalog.now(),
	    system_id = $3, timeline_id = $4, checkpoint_lsn = NULL,
	    reconcile_watermark = COALESCE(reconcile_watermark, pg_catalog.now() - interval '1 second')
	  WHERE connector_id = $1 AND leased_by = $2 AND lease_until > pg_catalog.now() AND status NOT IN ('dropped', 'disabled')`,
		connectorID, worker, systemID, timeline)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// MarkSlotDropped records that the slot is gone: status dropped (the next sync makes a new
// baseline), or, for a disabled row, slot_state none. reason says why (dropped, connector_deleted,
// not_witnessed, slot_orphaned).
func MarkSlotDropped(ctx context.Context, tx pgx.Tx, connectorID, reason string, disabled bool, detail json.RawMessage) error {
	status := "dropped"
	if disabled {
		status = "disabled"
	}
	_, err := tx.Exec(ctx, `UPDATE deedglass.decoder_slots SET status = $2, status_reason = $3, status_detail = $4::jsonb,
	    status_since = pg_catalog.now(), slot_state = 'none', checkpoint_lsn = NULL, leased_by = NULL, lease_until = NULL
	  WHERE connector_id = $1`, connectorID, status, reason, jsonOr(detail, "{}"))
	return err
}

// SetReconcileWatermark moves the reconcile watermark forward (never back).
func SetReconcileWatermark(ctx context.Context, tx pgx.Tx, connectorID string, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.decoder_slots SET reconcile_watermark = GREATEST(reconcile_watermark, $2)
	  WHERE connector_id = $1`, connectorID, at)
	return err
}

// DecoderLease is one leased slot row.
type DecoderLease struct {
	OrgID       ids.OrgID
	ConnectorID ids.ConnectorID
	SlotName    string
	Status      string
}

// ClaimDecoderLeases leases (or renews for worker) up to limit slot rows that need a runner.
func (db *DB) ClaimDecoderLeases(ctx context.Context, worker string, lease time.Duration, limit int) ([]DecoderLease, error) {
	rows, err := db.pool.Query(ctx, `SELECT org_id, connector_id, slot_name, status FROM deedglass.claim_decoder_leases($1, $2, $3)`,
		worker, lease, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DecoderLease, error) {
		var l DecoderLease
		err := r.Scan(&l.OrgID, &l.ConnectorID, &l.SlotName, &l.Status)
		return l, err
	})
}

// ReleaseDecoderLease ends worker's lease on connectorID now (a runner that stops cleanly).
func ReleaseDecoderLease(ctx context.Context, tx pgx.Tx, connectorID, worker string) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.decoder_slots SET leased_by = NULL, lease_until = NULL WHERE connector_id = $1 AND leased_by = $2`,
		connectorID, worker)
	return err
}

// SyncDecoderSlots keeps one slot row per witnessed Postgres connector (sync_decoder_slots) and
// returns how many rows changed.
func (db *DB) SyncDecoderSlots(ctx context.Context) (int, error) {
	var n int
	err := db.pool.QueryRow(ctx, `SELECT deedglass.sync_decoder_slots()`).Scan(&n)
	return n, err
}

// PruneDecodedTxs deletes at most limit frames created before before (unattributed and forged_*
// frames only before keepBefore) and returns how many it deleted.
func (db *DB) PruneDecodedTxs(ctx context.Context, before, keepBefore time.Time, limit int) (int, error) {
	var n int
	err := db.pool.QueryRow(ctx, `SELECT deedglass.maint_prune_decoded_txs($1, $2, $3)`, before, keepBefore, limit).Scan(&n)
	return n, err
}

// RestoreDecoderSlot puts a row back after a drop that couldn't finish: status halted
// (drop_failed), so a runner starts again and re-checks the slot, with its slot state and
// checkpoint as they were.
func RestoreDecoderSlot(ctx context.Context, tx pgx.Tx, connectorID, slotState string, checkpoint *string) error {
	_, err := tx.Exec(ctx, `UPDATE deedglass.decoder_slots SET status = 'halted', status_reason = 'drop_failed',
	    status_since = pg_catalog.now(), slot_state = $2, checkpoint_lsn = $3::text::pg_lsn WHERE connector_id = $1`,
		connectorID, slotState, checkpoint)
	return err
}
