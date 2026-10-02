package decoder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
)

// Session is the runner's one ordinary connection to the watched database, as deedglass_witness.
// Its settings are pinned in the startup packet (a client setting outranks ALTER ROLE … SET, so
// hostile role defaults never reach the decoder) and asserted before every peek (S20 §3.4; attack
// case E11). The same connection peeks, advances, looks up primary keys, sends heartbeats and
// holds the advisory lock that keeps a second Deedglass process off the slot.
type Session struct {
	conn       *pgx.Conn
	VersionNum int // server_version_num
	Version    string

	mu      sync.Mutex
	notices []string
}

// ApplicationName identifies decoder sessions in pg_stat_activity.
const ApplicationName = "deedglass-decoder"

// pinned are the decode session's settings: S17's setup.WitnessSettings, in Postgres's own
// spelling (as current_setting reports them).
var pinned = setup.WitnessSettings

// Connect opens a session with cfg (the witness DSN, already dialing through egress). It copies
// cfg and pins the settings and the application name in the startup packet.
func Connect(ctx context.Context, cfg *pgx.ConnConfig) (*Session, error) {
	c := cfg.Copy()
	if c.RuntimeParams == nil {
		c.RuntimeParams = map[string]string{}
	}
	for _, kv := range pinned {
		c.RuntimeParams[kv[0]] = kv[1]
	}
	c.RuntimeParams["application_name"] = ApplicationName
	c.DefaultQueryExecMode = pgx.QueryExecModeExec
	s := &Session{}
	c.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.notices) < 64 {
			s.notices = append(s.notices, n.Severity+": "+n.Message)
		}
	}
	conn, err := pgx.ConnectConfig(ctx, c)
	if err != nil {
		return nil, err
	}
	s.conn = conn
	if err := conn.QueryRow(ctx, `SELECT pg_catalog.current_setting('server_version_num')::int, pg_catalog.current_setting('server_version')`).Scan(&s.VersionNum, &s.Version); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	if i := strings.IndexByte(s.Version, ' '); i > 0 {
		s.Version = s.Version[:i]
	}
	return s, nil
}

// Close closes the connection (releasing the advisory lock).
func (s *Session) Close(ctx context.Context) error { return s.conn.Close(ctx) }

// Conn is the underlying connection (tests).
func (s *Session) Conn() *pgx.Conn { return s.conn }

func (s *Session) takeNotices() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.notices
	s.notices = nil
	return n
}

// PubSnapshot is the publication as seen at one moment: its oid and a fingerprint of everything
// that changes what it publishes (operations, member tables, row filters, column lists).
type PubSnapshot struct {
	Exists      bool
	OID         uint32
	Fingerprint string
	Tables      []string
	AllOps      bool // insert, update, delete and truncate
	Filtered    bool // a row filter or column list on any member
	AllTables   bool
}

// Snapshot is step 1 of a poll: the settings, the flush LSN read before the peek, and the
// publication, in one round trip.
type Snapshot struct {
	Settings map[string]string
	FlushLSN LSN
	Pub      PubSnapshot
}

const snapshotSQL = `SELECT pg_catalog.current_setting('TimeZone'), pg_catalog.current_setting('DateStyle'),
  pg_catalog.current_setting('IntervalStyle'), pg_catalog.current_setting('bytea_output'),
  pg_catalog.current_setting('extra_float_digits'), pg_catalog.pg_current_wal_flush_lsn()::text,
  p.oid::bigint, p.puballtables, (p.pubinsert AND p.pubupdate AND p.pubdelete AND p.pubtruncate),
  COALESCE((SELECT pg_catalog.array_agg(t.schemaname || '.' || t.tablename ORDER BY t.schemaname, t.tablename)
              FROM pg_catalog.pg_publication_tables AS t WHERE t.pubname = $1), '{}'::text[]),
  COALESCE((SELECT pg_catalog.bool_or(r.prqual IS NOT NULL OR r.prattrs IS NOT NULL)
              FROM pg_catalog.pg_publication_rel AS r WHERE r.prpubid = p.oid), false)
FROM (SELECT 1) AS one
LEFT JOIN pg_catalog.pg_publication AS p ON p.pubname = $1`

// Snapshot reads the settings, the flush LSN and pub's state.
func (s *Session) Snapshot(ctx context.Context, pub string) (Snapshot, error) {
	var sn Snapshot
	var tz, ds, is, bo, efd, flush string
	var oid *int64
	var all, ops *bool
	var tables []string
	var filtered bool
	if err := s.conn.QueryRow(ctx, snapshotSQL, pub).Scan(&tz, &ds, &is, &bo, &efd, &flush, &oid, &all, &ops, &tables, &filtered); err != nil {
		return sn, err
	}
	sn.Settings = map[string]string{"TimeZone": tz, "DateStyle": ds, "IntervalStyle": is, "bytea_output": bo, "extra_float_digits": efd}
	lsn, err := pglogrepl.ParseLSN(flush)
	if err != nil {
		return sn, err
	}
	sn.FlushLSN = lsn
	if oid != nil {
		sn.Pub = PubSnapshot{Exists: true, OID: uint32(*oid), Tables: tables, AllOps: ops != nil && *ops, Filtered: filtered,
			AllTables: all != nil && *all}
		h := sha256.New()
		fmt.Fprintf(h, "%d\x00%v\x00%v\x00%v\x00%s", *oid, sn.Pub.AllTables, sn.Pub.AllOps, filtered, strings.Join(tables, "\x00"))
		sn.Pub.Fingerprint = hex.EncodeToString(h.Sum(nil)[:12])
	}
	return sn, nil
}

// SettingsProblem names the first pinned setting whose value differs, or "".
func SettingsProblem(got map[string]string) string {
	for _, kv := range pinned {
		if got[kv[0]] != kv[1] {
			return fmt.Sprintf("%s is %q", kv[0], got[kv[0]])
		}
	}
	return ""
}

// PeekStats describe one peek.
type PeekStats struct {
	Rows    int      // rows the peek returned (the unit upto_nchanges counts)
	Notices []string // notices Postgres sent during the peek (PG 18: "skipped loading publication")
}

// peekSQL is the one peek statement: every value is a parameter (S20 §3.3).
const peekSQL = `SELECT lsn::text, data FROM pg_catalog.pg_logical_slot_peek_binary_changes($1, NULL, $2,
  'proto_version', '1', 'publication_names', $3, 'messages', 'true', 'origin', 'any')`

// Peek streams whole committed transactions and non-transactional messages, in decoding order,
// from the slot's confirmed position. maxChanges (upto_nchanges) is checked by Postgres only at
// transaction boundaries, so a large transaction is returned whole. fn sees each row; an error
// from fn stops the peek and is returned.
func (s *Session) Peek(ctx context.Context, slot, pub string, maxChanges int, fn func(Raw) error) (PeekStats, error) {
	_ = s.takeNotices()
	var st PeekStats
	rows, err := s.conn.Query(ctx, peekSQL, slot, maxChanges, pub)
	if err != nil {
		st.Notices = s.takeNotices()
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var lsnS string
		var data []byte
		if err := rows.Scan(&lsnS, &data); err != nil {
			return st, err
		}
		lsn, err := pglogrepl.ParseLSN(lsnS)
		if err != nil {
			return st, err
		}
		st.Rows++
		if err := fn(Raw{LSN: lsn, Data: data}); err != nil {
			rows.Close()
			st.Notices = s.takeNotices()
			return st, err
		}
	}
	rows.Close()
	st.Notices = s.takeNotices()
	return st, rows.Err()
}

// Advance moves the slot's confirmed position to `to` (pg_replication_slot_advance) and returns
// the position Postgres reports. The new position is durable at Postgres's next checkpoint.
func (s *Session) Advance(ctx context.Context, slot string, to LSN) (LSN, error) {
	var end string
	if err := s.conn.QueryRow(ctx, `SELECT end_lsn::text FROM pg_catalog.pg_replication_slot_advance($1, $2::text::pg_lsn)`,
		slot, to.String()).Scan(&end); err != nil {
		return 0, err
	}
	return pglogrepl.ParseLSN(end)
}

// Identity is the cluster's system identifier and current timeline.
func (s *Session) Identity(ctx context.Context) (systemID string, timeline int, err error) {
	if err = s.conn.QueryRow(ctx, `SELECT system_identifier::text FROM pg_catalog.pg_control_system()`).Scan(&systemID); err != nil {
		return "", 0, err
	}
	// The timeline from the current WAL file's name (its first 8 hex digits); pg_control_checkpoint
	// would lag a promotion until the next checkpoint.
	var wal string
	if err = s.conn.QueryRow(ctx, `SELECT pg_catalog.pg_walfile_name(pg_catalog.pg_current_wal_lsn())`).Scan(&wal); err != nil {
		return "", 0, err
	}
	if len(wal) < 8 {
		return "", 0, fmt.Errorf("decoder: unexpected WAL file name %q", wal)
	}
	tl, err := strconv.ParseUint(wal[:8], 16, 32)
	if err != nil || tl == 0 {
		return "", 0, fmt.Errorf("decoder: unexpected WAL file name %q", wal)
	}
	return systemID, int(tl), nil
}

// TryLock takes the session-level advisory lock for slot: false when another session holds it.
func (s *Session) TryLock(ctx context.Context, slot string) (bool, error) {
	var ok bool
	err := s.conn.QueryRow(ctx, `SELECT pg_catalog.pg_try_advisory_lock(pg_catalog.hashtextextended('deedglass/decoder/' || $1, 0))`, slot).Scan(&ok)
	return ok, err
}

// LockHolder names the application holding slot's advisory lock (for the waiting detail).
func (s *Session) LockHolder(ctx context.Context, slot string) string {
	var app, addr *string
	_ = s.conn.QueryRow(ctx, `SELECT a.application_name, a.client_addr::text FROM pg_catalog.pg_locks AS l
	    JOIN pg_catalog.pg_stat_activity AS a ON a.pid = l.pid
	   WHERE l.locktype = 'advisory' AND l.granted
	     AND ((l.classid::bigint << 32) | l.objid::bigint) = pg_catalog.hashtextextended('deedglass/decoder/' || $1, 0)
	   LIMIT 1`, slot).Scan(&app, &addr)
	parts := []string{}
	if app != nil && *app != "" {
		parts = append(parts, *app)
	}
	if addr != nil && *addr != "" {
		parts = append(parts, "from "+*addr)
	}
	return strings.Join(parts, " ")
}

// Preflight is what the start checks read from the watched database.
type Preflight struct {
	WALLevel        string
	Replication     bool // the session's role has REPLICATION
	BeginActionBody *string
	MaxSlotKeep     int64 // max_slot_wal_keep_size in bytes; -1 = unlimited
}

// Preflight reads wal_level, the role's REPLICATION attribute, begin_action's body and
// max_slot_wal_keep_size.
func (s *Session) Preflight(ctx context.Context) (Preflight, error) {
	var p Preflight
	var keep string
	err := s.conn.QueryRow(ctx, `SELECT pg_catalog.current_setting('wal_level'),
	    (SELECT r.rolreplication FROM pg_catalog.pg_roles AS r WHERE r.rolname = current_user),
	    (SELECT f.prosrc FROM pg_catalog.pg_proc AS f JOIN pg_catalog.pg_namespace AS n ON n.oid = f.pronamespace
	      WHERE n.nspname = 'deedglass' AND f.proname = 'begin_action' LIMIT 1),
	    (SELECT s.setting FROM pg_catalog.pg_settings AS s WHERE s.name = 'max_slot_wal_keep_size')`).Scan(&p.WALLevel, &p.Replication, &p.BeginActionBody, &keep)
	if err != nil {
		return p, err
	}
	// pg_settings reports max_slot_wal_keep_size in MB (-1 = unlimited).
	mb, err := strconv.ParseInt(keep, 10, 64)
	if err != nil || mb < 0 {
		p.MaxSlotKeep = -1
	} else {
		p.MaxSlotKeep = mb << 20
	}
	return p, nil
}

// SlotState is the slot as pg_replication_slots shows it.
type SlotState struct {
	Exists             bool
	Plugin             string
	Database           string
	Active             bool
	WALStatus          string
	Conflicting        bool
	InvalidationReason string
	ConfirmedFlush     LSN
	Restart            LSN
	Current            LSN
	LagBytes           int64
	RetainedBytes      int64
}

// Invalidated reports whether Postgres has given up the slot: on 16, wal_status lost alone says
// so; 17 and 18 also name the reason (wal_removed, idle_timeout, rows_removed, …).
func (st SlotState) Invalidated() bool {
	return st.WALStatus == "lost" || st.InvalidationReason != "" || st.Conflicting
}

// slotSQL16 and slotSQL17 read a slot (S20 §3.12): 16 has no invalidation_reason (spike P2), so
// there wal_status lost alone says the slot is invalidated.
const (
	slotSQL16 = `SELECT plugin, database, active, wal_status, COALESCE(conflicting, false), NULL::text,
    confirmed_flush_lsn::text, restart_lsn::text, pg_catalog.pg_current_wal_lsn()::text,
    pg_catalog.pg_wal_lsn_diff(pg_catalog.pg_current_wal_lsn(), confirmed_flush_lsn)::bigint,
    pg_catalog.pg_wal_lsn_diff(pg_catalog.pg_current_wal_lsn(), restart_lsn)::bigint
  FROM pg_catalog.pg_replication_slots WHERE slot_name = $1`
	slotSQL17 = `SELECT plugin, database, active, wal_status, COALESCE(conflicting, false), invalidation_reason,
    confirmed_flush_lsn::text, restart_lsn::text, pg_catalog.pg_current_wal_lsn()::text,
    pg_catalog.pg_wal_lsn_diff(pg_catalog.pg_current_wal_lsn(), confirmed_flush_lsn)::bigint,
    pg_catalog.pg_wal_lsn_diff(pg_catalog.pg_current_wal_lsn(), restart_lsn)::bigint
  FROM pg_catalog.pg_replication_slots WHERE slot_name = $1`
)

// Slot reads slot's row with the column list of this server's version (S20 §3.12; spike P2).
func (s *Session) Slot(ctx context.Context, slot string) (SlotState, error) {
	var st SlotState
	q := slotSQL16
	if s.VersionNum >= 170000 {
		q = slotSQL17
	}
	var plugin, db, ws, inv, conf, restart, cur *string
	var conflicting *bool
	var lag, ret *int64
	err := s.conn.QueryRow(ctx, q, slot).Scan(&plugin, &db, &st.Active, &ws, &conflicting, &inv, &conf, &restart, &cur, &lag, &ret)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Exists = true
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	lsn := func(p *string) LSN {
		if p == nil {
			return 0
		}
		v, _ := pglogrepl.ParseLSN(*p)
		return v
	}
	st.Plugin, st.Database, st.WALStatus, st.InvalidationReason = str(plugin), str(db), str(ws), str(inv)
	st.Conflicting = conflicting != nil && *conflicting
	st.ConfirmedFlush, st.Restart, st.Current = lsn(conf), lsn(restart), lsn(cur)
	if lag != nil {
		st.LagBytes = max(*lag, 0)
	}
	if ret != nil {
		st.RetainedBytes = max(*ret, 0)
	}
	return st, nil
}

// CurrentDatabase is the session's database name.
func (s *Session) CurrentDatabase(ctx context.Context) (string, error) {
	var db string
	err := s.conn.QueryRow(ctx, `SELECT pg_catalog.current_database()`).Scan(&db)
	return db, err
}

// CreateSlot creates slot with pgoutput (not temporary, no two-phase), as this session's role.
// Call it only after the publication check passed: on PG 18 a missing publication is skipped
// silently (spike P5), so the order matters.
func (s *Session) CreateSlot(ctx context.Context, slot string) (LSN, error) {
	var l string
	if err := s.conn.QueryRow(ctx, `SELECT lsn::text FROM pg_catalog.pg_create_logical_replication_slot($1, 'pgoutput', false, false)`, slot).Scan(&l); err != nil {
		return 0, err
	}
	return pglogrepl.ParseLSN(l)
}

// DropSlot drops slot. It fails while another session has the slot active.
func (s *Session) DropSlot(ctx context.Context, slot string) error {
	_, err := s.conn.Exec(ctx, `SELECT pg_catalog.pg_drop_replication_slot($1)`, slot)
	return err
}

// EmitHeartbeat sends a non-transactional deedglass message (the 3-argument form, which every
// supported major has; spike P1).
func (s *Session) EmitHeartbeat(ctx context.Context, content string) error {
	_, err := s.conn.Exec(ctx, `SELECT pg_catalog.pg_logical_emit_message(false, 'deedglass', $1::text)`, content)
	return err
}

// PrimaryKeys returns the primary key columns of relids, in index order (pg_index.indisprimary;
// with REPLICA IDENTITY FULL the relation message flags every column as key, so they can't say).
func (s *Session) PrimaryKeys(ctx context.Context, relids []uint32) (map[uint32][]string, error) {
	out := map[uint32][]string{}
	if len(relids) == 0 {
		return out, nil
	}
	oids := make([]int64, len(relids))
	for i, r := range relids {
		oids[i] = int64(r)
		out[r] = nil
	}
	rows, err := s.conn.Query(ctx, `SELECT i.indrelid::bigint, a.attname::text
	    FROM pg_catalog.pg_index AS i
	    JOIN pg_catalog.pg_attribute AS a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
	   WHERE i.indrelid = ANY ($1::bigint[]::oid[]) AND i.indisprimary
	   ORDER BY i.indrelid, pg_catalog.array_position(i.indkey::int2[], a.attnum)`, oids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rel int64
		var col string
		if err := rows.Scan(&rel, &col); err != nil {
			return nil, err
		}
		out[uint32(rel)] = append(out[uint32(rel)], col)
	}
	return out, rows.Err()
}

// LedgerSince reads up to 1,000 ledger rows created after since, oldest first (reconcile; the
// witness role has SELECT on the ledger, S17).
func (s *Session) LedgerSince(ctx context.Context, since string) (rows []LedgerSeen, err error) {
	r, err := s.conn.Query(ctx, `SELECT action_id, org_id, created_at::text FROM deedglass.ledger
	    WHERE created_at > $1::timestamptz ORDER BY created_at, action_id LIMIT 1000`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(r, func(row pgx.CollectableRow) (LedgerSeen, error) {
		var l LedgerSeen
		err := row.Scan(&l.ActionID, &l.OrgID, &l.CreatedAt)
		return l, err
	})
}

// LedgerSeen is one ledger row as reconcile reads it.
type LedgerSeen struct {
	ActionID  string
	OrgID     string
	CreatedAt string
}

// CurrentWAL reads pg_current_wal_lsn().
func (s *Session) CurrentWAL(ctx context.Context) (LSN, error) {
	var l string
	if err := s.conn.QueryRow(ctx, `SELECT pg_catalog.pg_current_wal_lsn()::text`).Scan(&l); err != nil {
		return 0, err
	}
	return pglogrepl.ParseLSN(l)
}
