//go:build integration

// The S23 §6 round trips and property test: an undo puts a table back exactly, through real
// Postgres logical decoding (pgoutput, the decoder's own SafeParse, Assembler and ApplyKeys), the
// connector's own write path (begin_action: the ledger row and the marker) and RunUndo. Run on
// PG 16, 17 and 18 with make undo-matrix (DEEDGLASS_UNDO_SEEDS=2000); make integration runs 200
// seeds.
//
// It is an external test package because internal/decoder imports internal/connector/postgres;
// undo_hooks_integration_test.go exposes the package's test database and write recipe to it.
package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres"
	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/l2"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

const (
	undoPub    = "deedglass_pub"
	undoSlot   = "deedglass_it_undo_rt"
	undoPrefix = "ut_" // every table this test creates
)

// ---------------------------------------------------------------------------------------------
// Column kinds: the S23 §6 type list, each with a generator of text values (nil = NULL).

type colKind struct {
	id      string // coverage name
	col     string // column name (one is quoted on purpose)
	typ     string // DDL type and cast target
	nullPct int
	gen     func(r *rand.Rand) string
}

const toastLen = 10 * 1024

var kinds = []colKind{
	{id: "bigint", col: "c_bigint", typ: "bigint", nullPct: 10, gen: func(r *rand.Rand) string {
		return strconv.FormatInt(r.Int64N(2_000_000_000_000_000)-1_000_000_000_000_000, 10)
	}},
	{id: "text", col: "c_text", typ: "text", nullPct: 10, gen: func(r *rand.Rand) string { return randText(r, 40, true) }},
	{id: "numeric(12,2)", col: "c_numeric", typ: "numeric(12,2)", nullPct: 10, gen: func(r *rand.Rand) string {
		s := fmt.Sprintf("%d.%02d", r.Int64N(10_000_000_000), r.IntN(100))
		if r.IntN(3) == 0 {
			s = "-" + s
		}
		return s
	}},
	{id: "timestamptz", col: "c_ts", typ: "timestamptz", nullPct: 10, gen: func(r *rand.Rand) string {
		switch r.IntN(30) {
		case 0:
			return "infinity"
		case 1:
			return "-infinity"
		}
		t := time.Unix(r.Int64N(4_102_444_800), r.Int64N(1_000_000)*1000).UTC()
		// a non-UTC input offset: stored as an instant, printed in UTC
		return t.In(time.FixedZone("x", (r.IntN(27)-13)*3600)).Format("2006-01-02 15:04:05.999999-07:00")
	}},
	{id: "date", col: "c_date", typ: "date", nullPct: 10, gen: func(r *rand.Rand) string {
		if r.IntN(30) == 0 {
			return "infinity"
		}
		return time.Date(1900+r.IntN(300), time.Month(1+r.IntN(12)), 1+r.IntN(28), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	}},
	{id: "interval", col: "c_interval", typ: "interval", nullPct: 10, gen: func(r *rand.Rand) string {
		sign := func() int {
			if r.IntN(4) == 0 {
				return -1
			}
			return 1
		}
		return fmt.Sprintf("%d years %d mons %d days %d:%02d:%02d.%06d", sign()*r.IntN(50), sign()*r.IntN(12), sign()*r.IntN(40),
			sign()*r.IntN(100), r.IntN(60), r.IntN(60), r.IntN(1_000_000))
	}},
	{id: "boolean", col: "c_bool", typ: "boolean", nullPct: 10, gen: func(r *rand.Rand) string {
		return []string{"t", "f", "true", "no"}[r.IntN(4)]
	}},
	{id: "jsonb", col: "c_jsonb", typ: "jsonb", nullPct: 10, gen: func(r *rand.Rand) string { return randJSON(r, 3, false) }},
	{id: "json", col: "c_json", typ: "json", nullPct: 10, gen: func(r *rand.Rand) string { return randJSON(r, 3, true) }},
	{id: "bytea", col: "c_bytea", typ: "bytea", nullPct: 10, gen: func(r *rand.Rand) string {
		b := make([]byte, r.IntN(64))
		for i := range b {
			b[i] = byte(r.IntN(256))
		}
		return `\x` + hex.EncodeToString(b)
	}},
	{id: "integer[]", col: "c_intarr", typ: "integer[]", nullPct: 10, gen: func(r *rand.Rand) string {
		switch r.IntN(6) {
		case 0:
			return "{}"
		case 1:
			return fmt.Sprintf("{{%d,%d},{%d,NULL}}", r.Int32()-1<<30, r.IntN(9), r.IntN(9))
		}
		parts := make([]string, 1+r.IntN(6))
		for i := range parts {
			if r.IntN(6) == 0 {
				parts[i] = "NULL"
			} else {
				parts[i] = strconv.Itoa(int(r.Int32()) - 1<<30)
			}
		}
		return "{" + strings.Join(parts, ",") + "}"
	}},
	{id: "enum", col: "c_enum", typ: "public.ut_mood", nullPct: 10, gen: func(r *rand.Rand) string {
		return []string{"sad", "ok", "happy", "very happy"}[r.IntN(4)]
	}},
	{id: "uuid", col: "c_uuid", typ: "uuid", nullPct: 10, gen: func(r *rand.Rand) string {
		b := make([]byte, 16)
		for i := range b {
			b[i] = byte(r.IntN(256))
		}
		s := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
		if r.IntN(3) == 0 {
			s = strings.ToUpper(s)
		}
		return s
	}},
	{id: "domain", col: "Dom \"Label\"", typ: "public.ut_label", nullPct: 10, gen: func(r *rand.Rand) string {
		return "d" + randText(r, 20, false)
	}},
	{id: "nullable", col: "c_nullable", typ: "text", nullPct: 60, gen: func(r *rand.Rand) string { return randText(r, 12, false) }},
	{id: "toast", col: "c_toast", typ: "text", nullPct: 0, gen: func(r *rand.Rand) string {
		const alpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		b := make([]byte, toastLen)
		for i := range b {
			b[i] = alpha[r.IntN(len(alpha))]
		}
		return string(b)
	}},
}

func kindByID(id string) colKind {
	for _, k := range kinds {
		if k.id == id {
			return k
		}
	}
	panic("no kind " + id)
}

func randText(r *rand.Rand, max int, allowEmpty bool) string {
	pieces := []string{"a", "b", "z", "Q", " ", "'", `"`, `\`, "\n", "\t", "é", "日本", "🙂", "{", "}", ",", "NULL", "0", "-"}
	n := r.IntN(max + 1)
	if !allowEmpty && n == 0 {
		n = 1
	}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(pieces[r.IntN(len(pieces))])
	}
	return sb.String()
}

// randJSON writes a JSON value. loose adds the whitespace and duplicate keys json keeps verbatim.
func randJSON(r *rand.Rand, depth int, loose bool) string {
	ws := func() string {
		if loose && r.IntN(3) == 0 {
			return []string{" ", "  ", "\n", "\t"}[r.IntN(4)]
		}
		return ""
	}
	str := func() string {
		b, _ := json.Marshal(randText(r, 8, true))
		return string(b)
	}
	if depth == 0 {
		switch r.IntN(6) {
		case 0:
			return "null"
		case 1:
			return []string{"true", "false"}[r.IntN(2)]
		case 2:
			return strconv.Itoa(r.IntN(2000) - 1000)
		case 3:
			return []string{"1.50", "-0.0", "1e3", "3.14159265358979", "12345678901234567890"}[r.IntN(5)]
		}
		return str()
	}
	switch r.IntN(3) {
	case 0:
		n := r.IntN(4)
		parts := make([]string, n)
		for i := range parts {
			parts[i] = ws() + randJSON(r, depth-1, loose) + ws()
		}
		return "[" + strings.Join(parts, ",") + "]"
	case 1:
		n := r.IntN(4)
		parts := make([]string, 0, n+1)
		for i := 0; i < n; i++ {
			parts = append(parts, ws()+fmt.Sprintf("%q", "k"+strconv.Itoa(r.IntN(5)))+ws()+":"+ws()+randJSON(r, depth-1, loose))
		}
		if loose && n > 0 && r.IntN(3) == 0 {
			parts = append(parts, `"k0":1`) // a duplicate key: json keeps it, jsonb doesn't
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return randJSON(r, 0, loose)
}

// ---------------------------------------------------------------------------------------------
// Tables.

type tableSpec struct {
	name     string
	identity bool // id bigint GENERATED ALWAYS AS IDENTITY, else a plain bigint key
	cols     []colKind
	stored   bool // g_stored, a STORED generated column
	virtual  bool // g_virtual, a VIRTUAL generated column (PG 18)
}

func (s tableSpec) qname() string { return pgx.Identifier{"public", s.name}.Sanitize() }

func (s tableSpec) has(id string) bool {
	for _, c := range s.cols {
		if c.id == id {
			return true
		}
	}
	return false
}

func qi(s string) string { return pgx.Identifier{s}.Sanitize() }

func (s tableSpec) ddl() []string {
	var cols []string
	if s.identity {
		cols = append(cols, `"id" bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY`)
	} else {
		cols = append(cols, `"id" bigint PRIMARY KEY`)
	}
	for _, c := range s.cols {
		cols = append(cols, qi(c.col)+" "+c.typ)
	}
	if s.stored {
		expr := `("id"::text || ':s')`
		if s.has("text") {
			expr = `("id"::text || ':' || coalesce(left("c_text", 8), '-'))`
		}
		cols = append(cols, `"g_stored" text GENERATED ALWAYS AS `+expr+` STORED`)
	}
	if s.virtual {
		cols = append(cols, `"g_virtual" bigint GENERATED ALWAYS AS ("id" * 3) VIRTUAL`)
	}
	out := []string{fmt.Sprintf("CREATE TABLE %s (%s)", s.qname(), strings.Join(cols, ", "))}
	if s.has("toast") {
		// EXTERNAL: out of line and uncompressed, so an UPDATE that doesn't touch it sends 'u'.
		out = append(out, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN "c_toast" SET STORAGE EXTERNAL`, s.qname()))
	}
	out = append(out,
		fmt.Sprintf("ALTER TABLE %s REPLICA IDENTITY FULL", s.qname()),
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON %s TO deedglass_agent", s.qname()),
		fmt.Sprintf("ALTER PUBLICATION %s ADD TABLE %s", undoPub, s.qname()))
	return out
}

// ---------------------------------------------------------------------------------------------
// The rig: the package's database, the connector, and a pgoutput slot read by a decoder session
// as deedglass_witness with the pinned settings.

type undoRig struct {
	ctx   context.Context
	d     *pgtest.DB
	admin *pgx.Conn
	c     *postgres.Connector
	in    *connector.Instance
	sess  *decoder.Session
	ver   int
	agent ids.AgentID
	sens  map[string]bool // the assembler's sensitive columns ("schema.table.column")
}

func newUndoRig(t *testing.T) *undoRig {
	t.Helper()
	ctx := context.Background()
	d := postgres.UndoTestDB(t)
	admin := d.Admin(t)
	c := postgres.New(nil)
	t.Cleanup(func() { c.Close() })
	in := postgres.UndoTestInstance(t, c, d.AgentDSN, d.WitnessDSN)
	cfg, err := pgx.ParseConfig(d.WitnessDSN)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := decoder.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("decoder session: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	r := &undoRig{ctx: ctx, d: d, admin: admin, c: c, in: in, sess: sess, ver: sess.VersionNum, agent: ids.New[ids.AgentKind]()}
	// Leftovers of an earlier failed run in this database, then the shared types.
	r.dropLeftovers(t)
	if r.ver >= 180000 {
		// PostgreSQL 18 refuses UPDATE and DELETE on a published REPLICA IDENTITY FULL table whose
		// generated columns the publication doesn't publish (42P10). Stored ones can be published;
		// S17's setup doesn't set this yet (S23 report: a carry-over for the connector's init).
		r.exec(t, `ALTER PUBLICATION `+undoPub+` SET (publish_generated_columns = stored)`)
	}
	r.exec(t, `CREATE TYPE public.ut_mood AS ENUM ('sad', 'ok', 'happy', 'very happy')`)
	r.exec(t, `CREATE DOMAIN public.ut_label AS text CHECK (VALUE <> '')`)
	t.Cleanup(func() { r.dropLeftovers(t) })
	// The slot: created after the publication exists, dropped at the end (also on failure).
	_, _ = admin.Exec(ctx, "SELECT pg_catalog.pg_drop_replication_slot(slot_name) FROM pg_catalog.pg_replication_slots WHERE slot_name = $1", undoSlot)
	if _, err := sess.CreateSlot(ctx, undoSlot); err != nil {
		t.Fatalf("create slot: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := r.admin.Exec(c, "SELECT pg_catalog.pg_drop_replication_slot(slot_name) FROM pg_catalog.pg_replication_slots WHERE slot_name = $1", undoSlot); err != nil {
			t.Errorf("drop slot %s: %v", undoSlot, err)
		}
	})
	return r
}

func (r *undoRig) dropLeftovers(t *testing.T) {
	t.Helper()
	rows, err := r.admin.Query(r.ctx, `SELECT c.relname::text FROM pg_catalog.pg_class AS c JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname LIKE 'ut\_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		names = append(names, s)
	}
	rows.Close()
	for _, n := range names {
		r.exec(t, "DROP TABLE IF EXISTS "+pgx.Identifier{"public", n}.Sanitize()+" CASCADE")
	}
	r.exec(t, "DROP DOMAIN IF EXISTS public.ut_label CASCADE")
	r.exec(t, "DROP TYPE IF EXISTS public.ut_mood CASCADE")
}

func (r *undoRig) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := r.admin.Exec(r.ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", clipSQL(sql), err)
	}
}

func clipSQL(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// create makes a table and drops it when the (sub)test ends.
func (r *undoRig) create(t *testing.T, s tableSpec) {
	t.Helper()
	for _, q := range s.ddl() {
		r.exec(t, q)
	}
	t.Cleanup(func() { _, _ = r.admin.Exec(context.Background(), "DROP TABLE IF EXISTS "+s.qname()+" CASCADE") })
}

// snapshot is the table as row_to_json text, by key (every column; generated ones recompute).
func (r *undoRig) snapshot(t *testing.T, s tableSpec) []string {
	t.Helper()
	return r.snapshotOf(t, s.qname())
}

func (r *undoRig) snapshotOf(t *testing.T, qname string) []string {
	t.Helper()
	rows, err := r.admin.Query(r.ctx, "SELECT pg_catalog.row_to_json(t)::text FROM "+qname+` AS t ORDER BY t."id"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// act runs fn in one write transaction of a new action through the connector's recipe and commits.
func (r *undoRig) act(t *testing.T, digestOf string, fn func(tx *postgres.UndoTestTx) error) ids.ActionID {
	t.Helper()
	action := ids.New[ids.ActionKind]()
	sum := sha256.Sum256([]byte(digestOf))
	ctx, cancel := context.WithTimeout(r.ctx, 60*time.Second)
	defer cancel()
	tx, err := r.c.UndoTestBegin(ctx, r.in, postgres.LedgerArgs{ActionID: action.String(), OrgID: r.in.OrgID.String(), AgentID: r.agent.String(),
		SQLDigest: "sha256:" + hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		t.Fatalf("the action's statements: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return action
}

// capture peeks the slot, assembles and classifies every frame with the decoder's own code,
// advances the slot past them, and returns the frames that name action, as l2 frames.
func (r *undoRig) capture(t *testing.T, action string) []l2.Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(r.ctx, 60*time.Second)
	defer cancel()
	asm := decoder.NewAssembler(decoder.AssembleOptions{Sensitive: r.sens})
	if _, err := r.sess.Peek(ctx, undoSlot, undoPub, 0, func(raw decoder.Raw) error {
		m, err := decoder.SafeParse(raw.Data)
		if err != nil {
			return err
		}
		return asm.Add(raw.LSN, m)
	}); err != nil {
		t.Fatalf("peek: %v", err)
	}
	batch, err := asm.Finish()
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	relids := make([]uint32, 0, len(batch.Relations))
	for id := range batch.Relations {
		relids = append(relids, id)
	}
	pk, err := r.sess.PrimaryKeys(ctx, relids)
	if err != nil {
		t.Fatalf("primary keys: %v", err)
	}
	decoder.ApplyKeys(batch.Frames, pk)
	var out []l2.Frame
	for _, f := range batch.Frames {
		if !decoder.Classify(f, r.in.OrgID.String(), false) || f.ActionID != action {
			continue
		}
		out = append(out, toL2(t, f))
	}
	if batch.LastCommitEnd > 0 {
		if _, err := r.sess.Advance(ctx, undoSlot, batch.LastCommitEnd); err != nil {
			t.Fatalf("advance: %v", err)
		}
	}
	return out
}

// toL2 is the frame as the store keeps it (decoded_txs' JSON columns) read back by l2, as
// gate/witness_l2.go and undoapi/service.go framesOf do.
func toL2(t *testing.T, f *decoder.Frame) l2.Frame {
	t.Helper()
	x := l2.Frame{ID: "dtx_it_" + f.CommitLSN.String(), Class: string(f.Class), CommitLSN: f.CommitLSN.String(), Xid: int64(f.Xid),
		CommitTime: f.CommitTime, MarkerLSN: f.MarkerLSN.String(), Markers: f.Markers, Total: f.Total, Digest: f.Digest, Flags: f.Flags}
	parts := make([][]byte, 4)
	for i, v := range []any{f.Ledger, f.Tables, f.Counts, f.Changes} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		parts[i] = b
	}
	if err := x.ParseParts(parts[0], parts[1], parts[2], parts[3]); err != nil {
		t.Fatal(err)
	}
	return x
}

// planned is one action's evidence, catalog, plan and checks, as the preview builds them.
type planned struct {
	frames   []l2.Frame
	ev       *undo.Evidence
	cat      *undo.Catalog
	plan     *undo.Plan
	findings []undo.Finding
	checks   []undo.Check
}

func (p *planned) refusal() string {
	var why []string
	if p.plan == nil {
		for _, f := range p.findings {
			if !f.Warn {
				why = append(why, f.Check+": "+f.Text)
			}
		}
		if len(why) == 0 {
			why = append(why, "no plan")
		}
		return strings.Join(why, " | ")
	}
	for _, c := range p.checks {
		if c.Status == undo.StatusFail {
			why = append(why, c.ID+": "+c.Detail)
		}
	}
	return strings.Join(why, " | ")
}

// plan captures action's frame and builds its undo the way the preview does: FromFrames, the
// catalog through the connector's pool as deedglass_agent, Build, the guard rows, the twelve checks.
func (r *undoRig) plan(t *testing.T, action ids.ActionID) *planned {
	t.Helper()
	p := &planned{frames: r.capture(t, action.String())}
	if len(p.frames) != 1 || p.frames[0].Class != l2.ClassBound {
		t.Fatalf("frames naming %s: %d (want one bound frame)", action, len(p.frames))
	}
	p.ev = undo.FromFrames(undo.FrameInput{Action: action.String(), ConnectorID: r.in.ID.String(), Frames: p.frames})
	relids := make([]uint32, 0, len(p.ev.Tables))
	for _, ti := range p.ev.Tables {
		relids = append(relids, ti.RelID)
	}
	slices.Sort(relids)
	var err error
	p.cat, err = r.c.UndoCatalog(r.ctx, r.in, relids)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	p.plan, p.findings = undo.Build(undo.BuildInput{Undoes: action.String(), ConnectorID: r.in.ID.String(), Evidence: p.ev, Catalog: p.cat})
	var cur undo.Current
	if p.plan != nil {
		if cur, err = r.c.UndoRows(r.ctx, r.in, p.plan); err != nil {
			t.Fatalf("guard rows: %v", err)
		}
	}
	now := time.Now()
	p.checks = undo.Checks(undo.CheckInput{
		Action: undo.ActionFacts{ID: action.String(), Sealed: true, Mode: "gateway", Tool: "postgres.execute", WitnessLevel: "L2",
			Verdict: "VERIFIED", SealedAt: now, Executed: true},
		Connector: undo.ConnectorFacts{Exists: true, Name: "postgres", Type: "postgres", Status: "active", HasCredentials: true, WitnessEnabled: true},
		Now:       now, MaxAge: 7 * 24 * time.Hour, MaxRows: 1000, Evidence: p.ev, Plan: p.plan, Findings: p.findings, Catalog: p.cat, Current: cur,
	})
	return p
}

// undo runs the plan through RunUndo as a new action.
func (r *undoRig) undo(t *testing.T, p *undo.Plan, o undo.RunOptions) (ids.ActionID, connector.Result, error) {
	t.Helper()
	action := ids.New[ids.ActionKind]()
	ctx, cancel := context.WithTimeout(r.ctx, 60*time.Second)
	defer cancel()
	res, err := r.c.RunUndo(ctx, r.in, connector.Call{ActionID: action, OrgID: r.in.OrgID, AgentID: r.agent, Qualified: "postgres." + postgres.ToolUndo,
		Deadline: time.Now().Add(60 * time.Second)}, p, o)
	return action, res, err
}

var runOpts = undo.RunOptions{LockTimeout: 2 * time.Second, StatementTimeout: 10 * time.Second}

// verifyUndoFrame captures the undo's own transaction and compares it with the plan's exact
// footprint (S23 invariant 7): every reason is undo_restored (pass), no violation and no gap.
//
// Two steps may name the same key (a key freed and taken again inside the original transaction):
// l2's exact checks match each observed change to an unused planned row of its operation, and the
// independent check (matchesSteps) explains any failure.
func (r *undoRig) verifyUndoFrame(t *testing.T, action ids.ActionID, p *undo.Plan) (rs []witness.Reason, known bool) {
	t.Helper()
	fs := r.capture(t, action.String())
	if len(fs) != 1 || fs[0].Class != l2.ClassBound {
		t.Fatalf("the undo %s: %d frames (want one bound frame)", action, len(fs))
	}
	f := fs[0]
	if len(f.Ledger) != 1 || f.Ledger[0].SQLDigest != p.Digest {
		t.Fatalf("the undo's ledger row: %+v, want sql_digest %s", f.Ledger, p.Digest)
	}
	rs = l2.Compare(undo.ExactFootprint(p), &f, l2.Config{})
	ok := len(rs) > 0
	for _, x := range rs {
		if x.Code != witness.CodeUndoRestored || x.Class != witness.Pass {
			ok = false
		}
	}
	if ok {
		return rs, false
	}
	var lines []string
	for _, x := range rs {
		lines = append(lines, fmt.Sprintf("%s/%s: %s", x.Class, x.Code, x.Message))
	}
	steps, _ := json.Marshal(p.Steps)
	why := matchesSteps(p, &f)
	t.Fatalf("the undo's L2 compare isn't exactly undo_restored:\n  %s\nindependent check: %s\nsteps: %s", strings.Join(lines, "\n  "),
		nonEmpty(why, "the frame matches the steps"), clipSQL(string(steps)))
	return rs, false
}

// verifyUndoFrameStrict is verifyUndoFrame for a plan whose steps never share a key.
func (r *undoRig) verifyUndoFrameStrict(t *testing.T, action ids.ActionID, p *undo.Plan) []witness.Reason {
	t.Helper()
	rs, known := r.verifyUndoFrame(t, action, p)
	if known {
		t.Fatal("the plan's steps share a key")
	}
	return rs
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// sharedStepKey names a table and key two steps of p both address, or "".
func sharedStepKey(p *undo.Plan) string {
	seen := map[string]bool{}
	for _, s := range p.Steps {
		t, _ := p.Table(s.Table)
		k := t.String() + " " + undo.KeyText(s.Key, t.PK)
		if seen[k] {
			return k
		}
		seen[k] = true
	}
	return ""
}

// matchesSteps checks the undo's frame against the plan independently of l2: the changes outside
// the ledger are exactly the steps (table, operation and key, as a multiset), and every insert or
// update wrote its planned values (an unchanged TOAST value read from the old image). It returns
// why not, or "".
func matchesSteps(p *undo.Plan, f *l2.Frame) string {
	type sk struct{ table, op, key string }
	want := map[sk][]undo.Step{}
	for _, s := range p.Steps {
		t, _ := p.Table(s.Table)
		k := sk{t.String(), string(s.Op), undo.KeyText(s.Key, t.PK)}
		want[k] = append(want[k], s)
	}
	n := 0
	for _, ch := range f.Changes {
		if ch.Table == l2.LedgerTable {
			continue
		}
		n++
		var t undo.PlanTable
		for _, pt := range p.Tables {
			if pt.String() == ch.Table {
				t = pt
			}
		}
		key := undo.Key{}
		for _, c := range t.PK {
			if v := ch.Key[c]; v != nil {
				key[c] = *v
			}
		}
		op := map[string]string{"I": "insert", "U": "update", "D": "delete"}[ch.Op]
		k := sk{ch.Table, op, undo.KeyText(key, t.PK)}
		if len(want[k]) == 0 {
			return fmt.Sprintf("an unplanned %s of %s %s", op, ch.Table, k.key)
		}
		s := want[k][0]
		want[k] = want[k][1:]
		for c, v := range s.Set {
			got, ok := ch.New[c]
			if ok && got.U {
				got, ok = ch.Old[c]
			}
			if !ok || !undo.Same(v, got) {
				return fmt.Sprintf("%s %s: %s isn't the planned value", ch.Table, k.key, c)
			}
		}
	}
	if n != len(p.Steps) || f.Total != len(p.Steps) {
		return fmt.Sprintf("%d changes (%d in all) for %d steps", n, f.Total, len(p.Steps))
	}
	return ""
}

func equalRows(t *testing.T, what string, got, want []string) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	for i := 0; i < max(len(got), len(want)); i++ {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			t.Fatalf("%s: %d rows, want %d; first difference at row %d:\n got %s\nwant %s", what, len(got), len(want), i, clipSQL(g), clipSQL(w))
		}
	}
}

// ---------------------------------------------------------------------------------------------
// The random transaction, kept in step with a model of the live keys.

type model struct {
	s     tableSpec
	r     *rand.Rand
	live  []int64 // keys present now
	freed []int64 // keys a change in this transaction vacated
	next  int64   // the next fresh key (plain-key tables)
	sql   strings.Builder
	stats map[string]int
	// reuse lets an insert or key move take a key vacated earlier in the same transaction.
	reuse bool
}

func (m *model) remove(k int64) {
	i := slices.Index(m.live, k)
	m.live = slices.Delete(m.live, i, i+1)
	m.freed = append(m.freed, k)
}

func (m *model) takeFreed() (int64, bool) {
	for i := len(m.freed) - 1; i >= 0; i-- {
		k := m.freed[i]
		if !slices.Contains(m.live, k) {
			m.freed = slices.Delete(m.freed, i, i+1)
			return k, true
		}
	}
	return 0, false
}

func (m *model) value(c colKind) any {
	if c.nullPct > 0 && m.r.IntN(100) < c.nullPct {
		return nil
	}
	return c.gen(m.r)
}

func parseKey(rows [][]*string) (int64, error) {
	if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] == nil {
		return 0, fmt.Errorf("RETURNING gave %d rows", len(rows))
	}
	return strconv.ParseInt(*rows[0][0], 10, 64)
}

// seed inserts n rows as the superuser (not an action).
func (r *undoRig) seed(t *testing.T, m *model, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		names, vals, args := m.insertParts(false, 0)
		q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING \"id\"::text", m.s.qname(), strings.Join(names, ", "), strings.Join(vals, ", "))
		var k string
		if err := r.admin.QueryRow(r.ctx, q, args...).Scan(&k); err != nil {
			t.Fatalf("seed: %v", err)
		}
		id, _ := strconv.ParseInt(k, 10, 64)
		m.live = append(m.live, id)
	}
}

// insertParts are an INSERT's columns, placeholders and values (with the key when overriding or
// on a plain-key table).
func (m *model) insertParts(override bool, key int64) (names, vals []string, args []any) {
	if !m.s.identity || override {
		if !m.s.identity && !override {
			key = m.next
			m.next++
		}
		names, vals, args = append(names, `"id"`), append(vals, "$1::text::bigint"), append(args, strconv.FormatInt(key, 10))
	}
	for _, c := range m.s.cols {
		if c.id != "toast" && m.r.IntN(8) == 0 {
			continue // left to its default (NULL)
		}
		args = append(args, m.value(c))
		names, vals = append(names, qi(c.col)), append(vals, fmt.Sprintf("$%d::text::%s", len(args), c.typ))
	}
	if len(names) == 0 {
		return []string{qi(m.s.cols[0].col)}, []string{"$1::text::" + m.s.cols[0].typ}, []any{m.value(m.s.cols[0])}
	}
	return names, vals, args
}

// step runs one random statement in tx.
func (m *model) step(ctx context.Context, tx *postgres.UndoTestTx) error {
	op := m.r.IntN(100)
	if len(m.live) == 0 {
		op = 0
	}
	run := func(sql string, args ...any) ([][]*string, error) {
		m.sql.WriteString(sql + ";\n")
		n, rows, err := tx.Exec(ctx, sql, args...)
		if err == nil && n != 1 {
			err = fmt.Errorf("%s changed %d rows", clipSQL(sql), n)
		}
		return rows, err
	}
	switch {
	case op < 30: // insert
		override, key := false, int64(0)
		if m.reuse && m.s.identity && m.r.IntN(8) == 0 {
			key, override = m.takeFreed()
		} else if m.reuse && !m.s.identity && m.r.IntN(6) == 0 {
			if k, ok := m.takeFreed(); ok {
				key, override = k, true
			}
		}
		names, vals, args := m.insertParts(override, key)
		ov := ""
		if override && m.s.identity {
			ov = " OVERRIDING SYSTEM VALUE"
		}
		rows, err := run(fmt.Sprintf(`INSERT INTO %s (%s)%s VALUES (%s) RETURNING "id"::text`, m.s.qname(), strings.Join(names, ", "), ov, strings.Join(vals, ", ")), args...)
		if err != nil {
			return err
		}
		k, err := parseKey(rows)
		if err != nil {
			return err
		}
		m.live = append(m.live, k)
		m.stats["insert"]++
		if override {
			m.stats["insert_reused_key"]++
		}
	case op < 75: // update
		k := m.live[m.r.IntN(len(m.live))]
		var sets []string
		var args []any
		var cols []colKind
		for _, c := range m.r.Perm(len(m.s.cols))[:1+m.r.IntN(min(3, len(m.s.cols)))] {
			cols = append(cols, m.s.cols[c])
		}
		toastTouched := false
		for _, c := range cols {
			if c.id == "toast" && m.r.IntN(10) != 0 {
				continue // most updates leave the TOAST value alone: pgoutput sends 'u'
			}
			toastTouched = toastTouched || c.id == "toast"
			args = append(args, m.value(c))
			sets = append(sets, fmt.Sprintf("%s = $%d::text::%s", qi(c.col), len(args), c.typ))
		}
		newKey, moved, viaDefault := int64(0), false, false
		switch {
		case !m.s.identity && m.r.IntN(7) == 0:
			newKey, moved = m.next, true
			if m.reuse && m.r.IntN(3) == 0 {
				if f, ok := m.takeFreed(); ok {
					newKey = f
				} else {
					m.next++
				}
			} else {
				m.next++
			}
			args = append(args, strconv.FormatInt(newKey, 10))
			sets = append(sets, fmt.Sprintf(`"id" = $%d::text::bigint`, len(args)))
		case m.s.identity && m.r.IntN(15) == 0:
			moved, viaDefault = true, true
			sets = append(sets, `"id" = DEFAULT`)
		}
		if len(sets) == 0 {
			c := m.s.cols[0]
			for _, x := range m.s.cols {
				if x.id != "toast" {
					c = x
				}
			}
			args = append(args, m.value(c))
			sets = append(sets, fmt.Sprintf("%s = $%d::text::%s", qi(c.col), len(args), c.typ))
		}
		args = append(args, strconv.FormatInt(k, 10))
		rows, err := run(fmt.Sprintf(`UPDATE %s SET %s WHERE "id" = $%d::text::bigint RETURNING "id"::text`, m.s.qname(), strings.Join(sets, ", "), len(args)), args...)
		if err != nil {
			return err
		}
		m.stats["update"]++
		if m.s.has("toast") && !toastTouched {
			m.stats["update_toast_untouched"]++
		}
		if moved {
			got, err := parseKey(rows)
			if err != nil {
				return err
			}
			if !viaDefault && got != newKey {
				return fmt.Errorf("moved to %d, want %d", got, newKey)
			}
			m.remove(k)
			m.live = append(m.live, got)
			m.stats["key_move"]++
		}
	default: // delete
		k := m.live[m.r.IntN(len(m.live))]
		if _, err := run(fmt.Sprintf(`DELETE FROM %s WHERE "id" = $1::text::bigint`, m.s.qname()), strconv.FormatInt(k, 10)); err != nil {
			return err
		}
		m.remove(k)
		m.stats["delete"]++
	}
	return nil
}

// randomSpec draws a table: a key kind, 1 to 8 column kinds, and generated columns.
func randomSpec(r *rand.Rand, name string, pg18 bool) tableSpec {
	// No virtual generated column: PostgreSQL 18 refuses UPDATE and DELETE on a published table with
	// REPLICA IDENTITY FULL and a virtual column (42P10), so such a table is never witnessed at L2
	// and never undone (the named case "a virtual generated column" shows it).
	_ = pg18
	s := tableSpec{name: name, identity: r.IntN(5) < 2, stored: r.IntN(5) < 2}
	for _, i := range r.Perm(len(kinds))[:1+r.IntN(8)] {
		s.cols = append(s.cols, kinds[i])
	}
	if len(s.cols) == 1 && s.cols[0].id == "toast" {
		s.cols = append(s.cols, kindByID("bigint")) // so some updates leave the TOAST value alone
	}
	return s
}

// ---------------------------------------------------------------------------------------------
// One round trip.

type tripResult struct {
	refused string // why the plan or the checks refused ("" when the undo ran)
	noop    bool   // the transaction left the table as it was: nothing to undo
	plan    *undo.Plan
	ev      *undo.Evidence
	reasons []witness.Reason
	known   bool // the undo was exact, but l2's exact compare hit the shared-key issue (verifyUndoFrame)
	unsent  bool // the evidence held an unchanged TOAST value ('u')
}

// roundTrip: snapshot, the action, its frame, the plan, RunUndo, the table equals the snapshot,
// and the undo's own frame matches the plan exactly.
func (r *undoRig) roundTrip(t *testing.T, m *model, ops int) tripResult {
	t.Helper()
	before := r.snapshot(t, m.s)
	action := r.act(t, m.s.name, func(tx *postgres.UndoTestTx) error {
		for i := 0; i < ops; i++ {
			if err := m.step(r.ctx, tx); err != nil {
				return fmt.Errorf("%w\n%s", err, clipSQL(m.sql.String()))
			}
		}
		return nil
	})
	after := r.snapshot(t, m.s)
	p := r.plan(t, action)
	res := tripResult{plan: p.plan, ev: p.ev}
	for _, c := range p.ev.Changes {
		for _, v := range c.New {
			res.unsent = res.unsent || v.U
		}
	}
	if p.plan != nil && len(p.plan.Steps) == 0 {
		res.noop = true
		equalRows(t, "a transaction with nothing to undo left the table changed", after, before)
		return res
	}
	if p.plan == nil || !undo.Available(p.checks) {
		res.refused = p.refusal()
		equalRows(t, "a refused undo changed the table", r.snapshot(t, m.s), after)
		return res
	}
	undoAct, out, err := r.undo(t, p.plan, runOpts)
	if err != nil {
		steps, _ := json.Marshal(p.plan.Steps)
		t.Fatalf("RunUndo: %v\nthe action:\n%s\nsteps: %s", err, clipSQL(m.sql.String()), clipSQL(string(steps)))
	}
	if out.Evidence["sql_digest"] != p.plan.Digest {
		t.Fatalf("RunUndo evidence %v", out.Evidence)
	}
	equalRows(t, "after the undo", r.snapshot(t, m.s), before)
	res.reasons, res.known = r.verifyUndoFrame(t, undoAct, p.plan)
	return res
}

func seedsFromEnv(t *testing.T) int {
	s := os.Getenv("DEEDGLASS_UNDO_SEEDS")
	if s == "" {
		return 200
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		t.Fatalf("DEEDGLASS_UNDO_SEEDS=%q", s)
	}
	return n
}

// ---------------------------------------------------------------------------------------------
// The test.

func TestUndoRoundTrip(t *testing.T) {
	r := newUndoRig(t)
	pg18 := r.ver >= 180000
	t.Logf("PostgreSQL %d", r.ver)

	t.Run("hero", func(t *testing.T) {
		s := tableSpec{name: undoPrefix + "hero"}
		r.exec(t, `CREATE TABLE public.ut_hero ("id" bigint PRIMARY KEY, "status" text NOT NULL, "total" numeric(12,2) NOT NULL, "updated_at" timestamptz NOT NULL)`)
		t.Cleanup(func() { _, _ = r.admin.Exec(context.Background(), "DROP TABLE IF EXISTS public.ut_hero") })
		for _, q := range []string{"ALTER TABLE public.ut_hero REPLICA IDENTITY FULL", "GRANT SELECT, INSERT, UPDATE, DELETE ON public.ut_hero TO deedglass_agent",
			"ALTER PUBLICATION deedglass_pub ADD TABLE public.ut_hero"} {
			r.exec(t, q)
		}
		r.exec(t, `INSERT INTO public.ut_hero SELECT g, CASE WHEN g <= 301 THEN 'paid' ELSE 'shipped' END, g * 1.25, '2026-09-01T00:00:00Z'::timestamptz + g * interval '1 minute'
			FROM pg_catalog.generate_series(1, 400) AS g`)
		before := r.snapshot(t, s)
		action := r.act(t, "hero", func(tx *postgres.UndoTestTx) error {
			n, _, err := tx.Exec(r.ctx, `UPDATE public.ut_hero SET "status" = 'shipped', "updated_at" = pg_catalog.now() WHERE "status" = $1`, "paid")
			if err == nil && n != 301 {
				err = fmt.Errorf("%d rows", n)
			}
			return err
		})
		p := r.plan(t, action)
		if p.plan == nil || !undo.Available(p.checks) {
			t.Fatalf("refused: %s", p.refusal())
		}
		if p.plan.Rows != 301 || p.plan.Counts.Update != 301 {
			t.Fatalf("plan: %d rows, counts %+v", p.plan.Rows, p.plan.Counts)
		}
		undoAct, out, err := r.undo(t, p.plan, runOpts)
		if err != nil {
			t.Fatal(err)
		}
		equalRows(t, "after the undo", r.snapshot(t, s), before)
		rs := r.verifyUndoFrameStrict(t, undoAct, p.plan)
		t.Logf("%s · %s", out.Output, rs[0].Message)
	})

	// Each type alone: a table with the key and that column (plus a bigint beside a TOAST column).
	for i, k := range kinds {
		t.Run("type "+k.id, func(t *testing.T) {
			rnd := rand.New(rand.NewPCG(uint64(len(k.id)), 23))
			for _, identity := range []bool{false, true} {
				s := tableSpec{name: fmt.Sprintf("%stype%d_%v", undoPrefix, i, identity), identity: identity, cols: []colKind{k}}
				if k.id == "toast" {
					s.cols = append(s.cols, kindByID("bigint"))
				}
				r.create(t, s)
				m := &model{s: s, r: rnd, next: 1, stats: map[string]int{}}
				r.seed(t, m, 12)
				res := r.roundTrip(t, m, 20)
				if res.refused != "" || res.noop || res.known {
					t.Fatalf("refused %q, noop %v, shared step key %v", res.refused, res.noop, res.known)
				}
				if k.id == "toast" && !res.unsent {
					t.Fatal("no update left the TOAST value unsent ('u'): the case didn't exercise it")
				}
			}
		})
	}

	t.Run("identity always re-insert keeps its id", func(t *testing.T) {
		s := tableSpec{name: undoPrefix + "identity", identity: true, cols: []colKind{kindByID("text")}}
		r.create(t, s)
		r.exec(t, `INSERT INTO public.ut_identity ("c_text") SELECT 'row ' || g FROM pg_catalog.generate_series(1, 5) AS g`)
		seq := func() string {
			var v string
			if err := r.admin.QueryRow(r.ctx, `SELECT last_value::text FROM pg_catalog.pg_sequences WHERE schemaname = 'public' AND sequencename = 'ut_identity_id_seq'`).Scan(&v); err != nil {
				t.Fatal(err)
			}
			return v
		}
		before := r.snapshot(t, s)
		action := r.act(t, "identity", func(tx *postgres.UndoTestTx) error {
			for _, q := range []string{`DELETE FROM public.ut_identity WHERE "id" = 3`, `INSERT INTO public.ut_identity ("c_text") VALUES ('new')`} {
				if _, _, err := tx.Exec(r.ctx, q); err != nil {
					return err
				}
			}
			return nil
		})
		p := r.plan(t, action)
		if p.plan == nil || !undo.Available(p.checks) {
			t.Fatalf("refused: %s", p.refusal())
		}
		var ins *undo.Step
		for i := range p.plan.Steps {
			if p.plan.Steps[i].Op == undo.OpInsert {
				ins = &p.plan.Steps[i]
			}
		}
		if ins == nil || !ins.Overriding || ins.Key["id"] != "3" {
			t.Fatalf("the re-insert step: %+v", ins)
		}
		seqBefore := seq()
		undoAct, _, err := r.undo(t, p.plan, runOpts)
		if err != nil {
			t.Fatal(err)
		}
		equalRows(t, "after the undo", r.snapshot(t, s), before)
		if got := seq(); got != seqBefore || got != "6" {
			t.Fatalf("the sequence moved: %s → %s (sequences are not rewound, and the undo takes no value)", seqBefore, got)
		}
		r.verifyUndoFrameStrict(t, undoAct, p.plan)
	})

	t.Run("a virtual generated column (PG 18)", func(t *testing.T) {
		if !pg18 {
			t.Skip("virtual generated columns are PostgreSQL 18")
		}
		s := tableSpec{name: undoPrefix + "virtual", cols: []colKind{kindByID("text")}, virtual: true}
		r.create(t, s)
		r.exec(t, `INSERT INTO `+s.qname()+` ("id", "c_text") VALUES (1, 'a')`)
		_, err := r.admin.Exec(r.ctx, `UPDATE `+s.qname()+` SET "c_text" = 'b' WHERE "id" = 1`)
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "42P10" {
			t.Fatalf("PostgreSQL 18 should refuse an UPDATE of a published REPLICA IDENTITY FULL table with a virtual column (42P10), got %v", err)
		}
		t.Logf("PG 18 refuses it as documented: %s", pe.Message)
	})

	t.Run("stored generated column recomputed, not written", func(t *testing.T) {
		s := tableSpec{name: undoPrefix + "stored", cols: []colKind{kindByID("text"), kindByID("bigint")}, stored: true}
		r.create(t, s)
		m := &model{s: s, r: rand.New(rand.NewPCG(7, 7)), next: 1, stats: map[string]int{}}
		r.seed(t, m, 10)
		before := r.snapshot(t, s)
		action := r.act(t, "stored", func(tx *postgres.UndoTestTx) error {
			for _, q := range []string{`UPDATE public.ut_stored SET "c_text" = 'changed ' || "id" WHERE "id" <= 4`, `DELETE FROM public.ut_stored WHERE "id" = 9`,
				`INSERT INTO public.ut_stored ("id", "c_text") VALUES (100, 'inserted')`} {
				if _, _, err := tx.Exec(r.ctx, q); err != nil {
					return err
				}
			}
			return nil
		})
		p := r.plan(t, action)
		if p.plan == nil || !undo.Available(p.checks) {
			t.Fatalf("refused: %s", p.refusal())
		}
		if len(p.plan.Tables) != 1 || !slices.Equal(p.plan.Tables[0].Generated, []string{"g_stored"}) {
			t.Fatalf("plan tables: %+v", p.plan.Tables)
		}
		for _, st := range p.plan.Steps {
			for c := range st.Set {
				if strings.HasPrefix(c, "g_") {
					t.Fatalf("a step writes generated column %s: %+v", c, st)
				}
			}
		}
		for _, ti := range p.ev.Tables {
			for _, c := range ti.Columns {
				if c == "g_virtual" {
					t.Fatal("a virtual generated column is in the evidence")
				}
			}
		}
		undoAct, _, err := r.undo(t, p.plan, runOpts)
		if err != nil {
			t.Fatal(err)
		}
		equalRows(t, "after the undo", r.snapshot(t, s), before)
		r.verifyUndoFrameStrict(t, undoAct, p.plan)
	})

	t.Run("a row changed after planning", func(t *testing.T) {
		s := tableSpec{name: undoPrefix + "race", cols: []colKind{kindByID("text")}}
		r.create(t, s)
		r.exec(t, `INSERT INTO public.ut_race SELECT g, 'paid' FROM pg_catalog.generate_series(1, 50) AS g`)
		action := r.act(t, "race", func(tx *postgres.UndoTestTx) error {
			_, _, err := tx.Exec(r.ctx, `UPDATE public.ut_race SET "c_text" = 'shipped' WHERE "id" <= 10`)
			return err
		})
		p := r.plan(t, action)
		if p.plan == nil || !undo.Available(p.checks) {
			t.Fatalf("refused: %s", p.refusal())
		}
		r.exec(t, `UPDATE public.ut_race SET "c_text" = 'cancelled' WHERE "id" = 7`)
		_ = r.capture(t, "") // past the writer's transaction
		mid := r.snapshot(t, s)
		undoAct, _, err := r.undo(t, p.plan, runOpts)
		ce := connector.AsError(err)
		if ce == nil || ce.Code != postgres.CodeUndoConflict || ce.Kind != connector.KindToolError ||
			!strings.Contains(ce.AgentMessage, `public.ut_race id=7 changed after the plan was made (c_text is now 'cancelled')`) {
			t.Fatalf("RunUndo after a writer: %v", err)
		}
		equalRows(t, "a conflicting undo changed the table (invariant 3)", r.snapshot(t, s), mid)
		if fs := r.capture(t, undoAct.String()); len(fs) != 0 {
			t.Fatalf("a rolled-back undo left %d frames to witness (invariant 6)", len(fs))
		}
		t.Log(ce.AgentMessage)
	})

	t.Run("a row lock held elsewhere", func(t *testing.T) {
		s := tableSpec{name: undoPrefix + "lock", cols: []colKind{kindByID("text")}}
		r.create(t, s)
		r.exec(t, `INSERT INTO public.ut_lock SELECT g, 'paid' FROM pg_catalog.generate_series(1, 20) AS g`)
		action := r.act(t, "lock", func(tx *postgres.UndoTestTx) error {
			_, _, err := tx.Exec(r.ctx, `UPDATE public.ut_lock SET "c_text" = 'shipped' WHERE "id" IN (3, 4, 5)`)
			return err
		})
		p := r.plan(t, action)
		if p.plan == nil || !undo.Available(p.checks) {
			t.Fatalf("refused: %s", p.refusal())
		}
		before := r.snapshot(t, s)
		holder := r.d.Admin(t)
		tx, err := holder.Begin(r.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(r.ctx, `SELECT 1 FROM public.ut_lock WHERE "id" = 4 FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		undoAct, _, err := r.undo(t, p.plan, runOpts)
		took := time.Since(start)
		_ = tx.Rollback(r.ctx)
		ce := connector.AsError(err)
		if ce == nil || ce.Code != postgres.CodeUndoConflict || !strings.Contains(ce.AgentMessage, "is locked by another transaction") {
			t.Fatalf("RunUndo against a held lock: %v", err)
		}
		if took < 1800*time.Millisecond || took > 6*time.Second {
			t.Fatalf("the lock wait took %s; lock_timeout is %s", took, runOpts.LockTimeout)
		}
		equalRows(t, "a conflicting undo changed the table", r.snapshot(t, s), before)
		if fs := r.capture(t, undoAct.String()); len(fs) != 0 {
			t.Fatalf("a rolled-back undo left %d frames", len(fs))
		}
		t.Logf("%s after %s", ce.AgentMessage, took.Round(time.Millisecond))
	})

	t.Run("cascades", func(t *testing.T) {
		for _, cs := range []struct {
			name, onDelete string
			insert         bool // the action inserts a parent row (its undo deletes one)
			fail           bool
		}{
			{"cascade child, undo deletes", "CASCADE", true, true},
			{"set null child, undo deletes", "SET NULL", true, true},
			{"restrict child, undo deletes", "RESTRICT", true, false},
			{"cascade child, undo only updates", "CASCADE", false, false},
		} {
			t.Run(cs.name, func(t *testing.T) {
				parent := tableSpec{name: undoPrefix + "parent", cols: []colKind{kindByID("text")}}
				r.create(t, parent)
				r.exec(t, `CREATE TABLE public.ut_child ("id" bigint PRIMARY KEY, "parent_id" bigint REFERENCES public.ut_parent ("id") ON DELETE `+cs.onDelete+`)`)
				t.Cleanup(func() { _, _ = r.admin.Exec(context.Background(), "DROP TABLE IF EXISTS public.ut_child") })
				r.exec(t, `INSERT INTO public.ut_parent SELECT g, 'p' || g FROM pg_catalog.generate_series(1, 5) AS g`)
				r.exec(t, `INSERT INTO public.ut_child SELECT g, g FROM pg_catalog.generate_series(1, 5) AS g`)
				before := r.snapshot(t, parent)
				action := r.act(t, cs.name, func(tx *postgres.UndoTestTx) error {
					q := `UPDATE public.ut_parent SET "c_text" = 'renamed' WHERE "id" = 2`
					if cs.insert {
						q = `INSERT INTO public.ut_parent VALUES (6, 'p6')`
					}
					_, _, err := tx.Exec(r.ctx, q)
					return err
				})
				p := r.plan(t, action)
				if p.plan == nil {
					t.Fatalf("no plan: %s", p.refusal())
				}
				var casc undo.Check
				for _, c := range p.checks {
					if c.ID == undo.CheckCascades {
						casc = c
					}
				}
				if (casc.Status == undo.StatusFail) != cs.fail || undo.Available(p.checks) == cs.fail {
					t.Fatalf("cascades: %s %q (want fail=%v)", casc.Status, casc.Detail, cs.fail)
				}
				t.Logf("cascades %s: %s", casc.Status, casc.Detail)
				after := r.snapshot(t, parent)
				undoAct, _, err := r.undo(t, p.plan, runOpts)
				if cs.fail {
					// RunUndo repeats the check inside its transaction and refuses.
					if code := connector.AsError(err); code == nil || code.Code != postgres.CodeUndoConflict {
						t.Fatalf("RunUndo with a cascading foreign key: %v", err)
					}
					equalRows(t, "a refused undo changed the table", r.snapshot(t, parent), after)
					if fs := r.capture(t, undoAct.String()); len(fs) != 0 {
						t.Fatalf("a refused undo left %d frames", len(fs))
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				equalRows(t, "after the undo", r.snapshot(t, parent), before)
				r.verifyUndoFrameStrict(t, undoAct, p.plan)
			})
		}
	})

	// Refusals through real evidence: the plan is refused and nothing touches the table.
	t.Run("refused: keys swapped", func(t *testing.T) {
		s := tableSpec{name: undoPrefix + "swap", cols: []colKind{kindByID("text")}}
		r.create(t, s)
		r.exec(t, `INSERT INTO public.ut_swap SELECT g, 'r' || g FROM pg_catalog.generate_series(1, 5) AS g`)
		action := r.act(t, "swap", func(tx *postgres.UndoTestTx) error {
			for _, q := range []string{`UPDATE public.ut_swap SET "id" = 100 WHERE "id" = 1`, `UPDATE public.ut_swap SET "id" = 1 WHERE "id" = 2`,
				`UPDATE public.ut_swap SET "id" = 2 WHERE "id" = 100`} {
				if _, _, err := tx.Exec(r.ctx, q); err != nil {
					return err
				}
			}
			return nil
		})
		after := r.snapshot(t, s)
		p := r.plan(t, action)
		if p.plan != nil || !strings.Contains(p.refusal(), "swapped keys") || undo.Available(p.checks) {
			t.Fatalf("a key swap: plan %v, refusal %q", p.plan != nil, p.refusal())
		}
		equalRows(t, "a refused undo changed the table", r.snapshot(t, s), after)
		t.Log(p.refusal())
	})

	t.Run("sensitive column", func(t *testing.T) {
		r.sens = map[string]bool{"public.ut_sens.c_text": true}
		defer func() { r.sens = nil }()
		s := tableSpec{name: undoPrefix + "sens", cols: []colKind{kindByID("text"), kindByID("bigint")}}
		r.create(t, s)
		r.exec(t, `INSERT INTO public.ut_sens SELECT g, 'secret ' || g, g FROM pg_catalog.generate_series(1, 5) AS g`)
		// An update that leaves the sensitive column alone: the guard holds by digest, the undo runs.
		before := r.snapshot(t, s)
		action := r.act(t, "sens update", func(tx *postgres.UndoTestTx) error {
			_, _, err := tx.Exec(r.ctx, `UPDATE public.ut_sens SET "c_bigint" = "c_bigint" + 100 WHERE "id" <= 3`)
			return err
		})
		p := r.plan(t, action)
		if p.plan == nil || !undo.Available(p.checks) || !slices.Contains(p.plan.Notes, undo.NoteSensitive) {
			t.Fatalf("an update beside a sensitive column: %s (notes %v)", p.refusal(), p.plan)
		}
		undoAct, _, err := r.undo(t, p.plan, runOpts)
		if err != nil {
			t.Fatal(err)
		}
		equalRows(t, "after the undo", r.snapshot(t, s), before)
		r.verifyUndoFrameStrict(t, undoAct, p.plan)
		// A delete: putting the row back needs the value, and only its digest was kept.
		action = r.act(t, "sens delete", func(tx *postgres.UndoTestTx) error {
			_, _, err := tx.Exec(r.ctx, `DELETE FROM public.ut_sens WHERE "id" = 4`)
			return err
		})
		after := r.snapshot(t, s)
		p = r.plan(t, action)
		if p.plan != nil || !strings.Contains(p.refusal(), "public.ut_sens.c_text is marked sensitive") {
			t.Fatalf("a delete of a sensitive value: plan %v, refusal %q", p.plan != nil, p.refusal())
		}
		equalRows(t, "a refused undo changed the table", r.snapshot(t, s), after)
		t.Log(p.refusal())
	})

	// A key freed and taken again inside the original transaction: the undo puts the table back
	// exactly, but two of its steps name the same key, which l2's exact compare can't tell apart
	// (the known issue in verifyUndoFrame). Skipped with the reasons unless DEEDGLASS_UNDO_STRICT=1.
	t.Run("a key reused in the same transaction", func(t *testing.T) {
		for _, cs := range []struct {
			name     string
			identity bool
			stmts    []string
			shared   string
		}{
			{"plain key: delete 7, move 9 onto 7", false, []string{`DELETE FROM public.ut_reuse WHERE "id" = 7`,
				`UPDATE public.ut_reuse SET "id" = 7, "c_text" = 'moved' WHERE "id" = 9`}, "public.ut_reuse id=7"},
			{"identity: move 2 to DEFAULT, insert 2 overriding", true, []string{`UPDATE public.ut_reuse SET "id" = DEFAULT WHERE "id" = 2`,
				`INSERT INTO public.ut_reuse ("id", "c_text") OVERRIDING SYSTEM VALUE VALUES (2, 'taken')`}, "public.ut_reuse id=2"},
		} {
			t.Run(cs.name, func(t *testing.T) {
				s := tableSpec{name: undoPrefix + "reuse", identity: cs.identity, cols: []colKind{kindByID("text")}}
				r.create(t, s)
				m := &model{s: s, r: rand.New(rand.NewPCG(3, 3)), next: 1, stats: map[string]int{}}
				r.seed(t, m, 10)
				before := r.snapshot(t, s)
				action := r.act(t, cs.name, func(tx *postgres.UndoTestTx) error {
					for _, q := range cs.stmts {
						if n, _, err := tx.Exec(r.ctx, q); err != nil || n != 1 {
							return fmt.Errorf("%s: %d rows, %v", q, n, err)
						}
					}
					return nil
				})
				p := r.plan(t, action)
				if p.plan == nil || !undo.Available(p.checks) {
					t.Fatalf("refused: %s", p.refusal())
				}
				if got := sharedStepKey(p.plan); got != cs.shared {
					t.Fatalf("shared step key %q, want %q", got, cs.shared)
				}
				undoAct, _, err := r.undo(t, p.plan, runOpts)
				if err != nil {
					t.Fatal(err)
				}
				equalRows(t, "after the undo", r.snapshot(t, s), before)
				if rs, known := r.verifyUndoFrame(t, undoAct, p.plan); known {
					var lines []string
					for _, x := range rs {
						lines = append(lines, string(x.Class)+"/"+string(x.Code))
					}
					steps, _ := json.Marshal(p.plan.Steps)
					t.Skipf("KNOWN ISSUE: the table is back exactly, but l2.Compare(undo.ExactFootprint(plan)) gives %s for steps %s",
						strings.Join(lines, ", "), steps)
				}
			})
		}
	})

	t.Run("property", func(t *testing.T) {
		seeds := seedsFromEnv(t)
		reuse := os.Getenv("DEEDGLASS_UNDO_REUSE_KEYS") != "0"
		start := time.Now()
		var refused, noops, known int
		reasons := map[string]int{}
		covered := map[string]int{}
		stats := map[string]int{}
		steps := 0
		for seed := 1; seed <= seeds; seed++ {
			ok := t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
				rnd := rand.New(rand.NewPCG(uint64(seed), uint64(r.ver)))
				s := randomSpec(rnd, fmt.Sprintf("%sp%d", undoPrefix, seed), pg18)
				r.create(t, s)
				m := &model{s: s, r: rnd, next: 1, stats: stats, reuse: reuse}
				r.seed(t, m, 1+rnd.IntN(30))
				res := r.roundTrip(t, m, 1+rnd.IntN(50))
				switch {
				case res.refused != "":
					refused++
					reasons[res.refused]++
					t.Logf("refused: %s", res.refused)
				case res.noop:
					noops++
				default:
					if res.known {
						known++
					}
					steps += len(res.plan.Steps)
					for _, c := range s.cols {
						covered[c.id]++
					}
					if s.identity {
						covered["identity always"]++
					} else {
						covered["plain key"]++
					}
					if s.stored {
						covered["stored generated"]++
					}
					if s.virtual {
						covered["virtual generated"]++
					}
					if res.unsent {
						covered["toast 'u' in evidence"]++
					}
					for _, st := range res.plan.Steps {
						if st.Overriding {
							covered["overriding re-insert"]++
						}
					}
				}
				_, _ = r.admin.Exec(r.ctx, "DROP TABLE IF EXISTS "+s.qname())
			})
			if !ok {
				t.Fatalf("seed %d failed (rerun: DEEDGLASS_UNDO_SEEDS=%d, -run 'TestUndoRoundTrip/property/seed_%d$')", seed, seed, seed)
			}
		}
		took := time.Since(start)
		var cov []string
		for k, n := range covered {
			cov = append(cov, fmt.Sprintf("%s %d", k, n))
		}
		sort.Strings(cov)
		var st []string
		for k, n := range stats {
			st = append(st, fmt.Sprintf("%s %d", k, n))
		}
		sort.Strings(st)
		t.Logf("UNDO-PROPERTY pg=%d seeds=%d undone=%d refused=%d noop=%d steps=%d took=%s per_seed=%s reuse_keys=%v known_l2_shared_key=%d",
			r.ver, seeds, seeds-refused-noops, refused, noops, steps, took.Round(time.Millisecond), (took / time.Duration(seeds)).Round(time.Microsecond), reuse, known)
		t.Logf("UNDO-PROPERTY covered (seeds undone with it): %s", strings.Join(cov, ", "))
		t.Logf("UNDO-PROPERTY statements: %s", strings.Join(st, ", "))
		for why, n := range reasons {
			t.Logf("UNDO-PROPERTY refused %d× %s", n, why)
		}
		if refused*100 > seeds*5 {
			t.Fatalf("%d of %d seeds refused (over 5%%)", refused, seeds)
		}
	})
}
