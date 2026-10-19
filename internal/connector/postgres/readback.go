package postgres

//deedglass:allow-sql-build the read-back's identifiers come only from catalog lookups (pg_class, pg_attribute) and pgx.Identifier.Sanitize; every value is a parameter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/sqlguard"
	"github.com/deedglass/deedglass/internal/witness"
)

var _ connector.ReadBacker = (*Connector)(nil)

// ReadBackTimeout is the row read-back's statement_timeout (S19 §3.5).
const ReadBackTimeout = 2 * time.Second

// Test seams, nil outside tests: readBackHook acts between the statement and the read-back
// (another session updates or deletes the row); readBackConnHook sees the read-back's connection
// inside its transaction (transaction_read_only, current_user).
var (
	readBackHook     func(ctx context.Context)
	readBackConnHook func(ctx context.Context, conn *pgx.Conn)
)

// ReadBack implements connector.ReadBacker for postgres.execute (S19 §3.5): a single-row UPDATE by
// primary key is read back in a READ ONLY transaction, as the agent role, through the connector's
// pinned pool. It compares each SET literal with the column in Postgres (sent as text with the
// column's type, so Postgres parses 't' as a boolean and 10.50 as numeric) and the row's xmin with
// the low 32 bits of the action's transaction id. Anything else is ErrReadBackSkipped.
func (c *Connector) ReadBack(ctx context.Context, inst *connector.Instance, call connector.Call, res connector.Result) (connector.Observation, error) {
	start := time.Now()
	st, err := parse(inst)
	if err != nil {
		return connector.Observation{}, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This connector's config can't be read.", Err: err}
	}
	if call.Tool.Name != ToolExecute {
		return connector.Observation{}, &connector.ErrReadBackSkipped{Why: "only postgres.execute statements are read back"}
	}
	var a sqlArgs
	if err := decodeArgs(call.Args, &a); err != nil {
		return connector.Observation{}, err
	}
	params, err := Params(a.Params)
	if err != nil {
		return connector.Observation{}, err
	}
	t, why := TargetOf(a.SQL, st.cfg.AllowedSchemas[0])
	if why != "" {
		return connector.Observation{}, &connector.ErrReadBackSkipped{Why: why}
	}
	xid8, err := strconv.ParseUint(res.Evidence["xid8"], 10, 64)
	if err != nil {
		return connector.Observation{}, &connector.ErrReadBackSkipped{Why: "the statement's transaction id wasn't recorded"}
	}
	if readBackHook != nil {
		readBackHook(ctx)
	}
	e, err := c.entry(ctx, inst, st)
	if err != nil {
		return connector.Observation{}, err
	}
	pc, err := e.acquire(ctx, inst.Name)
	if err != nil {
		return connector.Observation{}, err
	}
	defer pc.Release()
	row, err := readRow(ctx, pc, st.cfg, t, params, uint32(xid8))
	obs := connector.Observation{LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		return obs, err
	}
	obs.Body, _ = json.Marshal(row)
	return obs, nil
}

// pgColumn is one column as the catalog names it.
type pgColumn struct {
	name string
	oid  uint32
}

// readRow runs the read-back in one READ ONLY transaction and always rolls it back.
func readRow(ctx context.Context, pc *pgxpool.Conn, cfg Config, t RowTargetOf, params []any, xmin32 uint32) (witness.RowObservation, error) {
	conn := pc.Conn()
	defer rollback(pc)
	b := &pgx.Batch{}
	b.Queue("BEGIN READ ONLY")
	b.Queue(settingsSQL, strconv.FormatInt(ReadBackTimeout.Milliseconds(), 10)+"ms", strconv.Itoa(cfg.LockTimeoutMS)+"ms", searchPath(cfg))
	br := conn.SendBatch(ctx, b)
	_, err := br.Exec()
	if err == nil {
		_, err = br.Exec()
	}
	if cerr := br.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return witness.RowObservation{}, readErr(err)
	}
	if readBackConnHook != nil {
		readBackConnHook(ctx, conn)
	}
	schema, table, ok := strings.Cut(t.Shape.Table, ".")
	if !ok {
		return witness.RowObservation{}, &connector.ErrReadBackSkipped{Why: "the table name can't be read"}
	}
	// The table and its columns, from the catalog. A table the agent role can't see isn't found.
	var relid uint32
	var nsp, rel string
	err = conn.QueryRow(ctx, `SELECT c.oid, n.nspname, c.relname FROM pg_catalog.pg_class AS c
	    JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
	    WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p')`, schema, table).Scan(&relid, &nsp, &rel)
	if errors.Is(err, pgx.ErrNoRows) {
		return witness.RowObservation{}, &connector.ErrReadBackSkipped{Why: "the table " + t.Shape.Table + " isn't visible to the agent role"}
	}
	if err != nil {
		return witness.RowObservation{}, readErr(err)
	}
	var pk []string
	rows, err := conn.Query(ctx, `SELECT a.attname FROM pg_catalog.pg_index AS i
	    JOIN pg_catalog.pg_attribute AS a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
	    WHERE i.indrelid = $1 AND i.indisprimary`, relid)
	if err == nil {
		pk, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil {
		return witness.RowObservation{}, readErr(err)
	}
	where := append([]string(nil), t.Shape.Where.Columns...)
	slices.Sort(pk)
	slices.Sort(where)
	if len(pk) == 0 || !slices.Equal(pk, where) {
		return witness.RowObservation{}, &connector.ErrReadBackSkipped{Why: "the WHERE isn't the primary key of " + t.Shape.Table + "; L2 covers it when the connector is witnessed"}
	}
	names := append(append([]string(nil), t.Shape.Where.Columns...), setColumns(t.Shape)...)
	rows, err = conn.Query(ctx, `SELECT a.attname, a.atttypid FROM pg_catalog.pg_attribute AS a
	    WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped AND a.attname = ANY ($2)`, relid, names)
	var cols []pgColumn
	if err == nil {
		cols, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (pgColumn, error) {
			var c pgColumn
			return c, r.Scan(&c.name, &c.oid)
		})
	}
	if err != nil {
		return witness.RowObservation{}, readErr(err)
	}
	byName := map[string]pgColumn{}
	for _, c := range cols {
		byName[c.name] = c
	}
	// The statement, from catalog names only.
	var sel []string
	var vals [][]byte
	var oids []uint32
	sel = append(sel, "xmin::text")
	out := witness.RowObservation{Table: t.Shape.Table, Key: t.Key, Columns: []witness.ColumnResult{}}
	type compared struct{ i int }
	var cmp []compared
	for _, as := range t.Shape.Set {
		col, ok := byName[as.Column]
		cr := witness.ColumnResult{Column: as.Column}
		v, isNull, known := valueOf(as.Value, params)
		if !ok || !known {
			out.Columns = append(out.Columns, cr) // an expression, or a column the catalog didn't return: not compared
			continue
		}
		id := pgx.Identifier{col.name}.Sanitize()
		if isNull {
			sel = append(sel, fmt.Sprintf("(%s IS NULL)", id), id+"::text")
			cr.Expected = nil
		} else {
			vals, oids = append(vals, []byte(v)), append(oids, col.oid)
			sel = append(sel, fmt.Sprintf("(%s IS NOT DISTINCT FROM $%d)", id, len(vals)), id+"::text")
			cr.Expected = v
		}
		cr.Compared = true
		out.Columns = append(out.Columns, cr)
		cmp = append(cmp, compared{len(out.Columns) - 1})
	}
	var conds []string
	for i, k := range t.Shape.Where.Columns {
		col, ok := byName[k]
		v, isNull, known := valueOf(t.Shape.Where.Rows[0][i], params)
		if !ok || !known || isNull {
			return witness.RowObservation{}, &connector.ErrReadBackSkipped{Why: "the key value can't be read back"}
		}
		vals, oids = append(vals, []byte(v)), append(oids, col.oid)
		conds = append(conds, fmt.Sprintf("%s = $%d", pgx.Identifier{col.name}.Sanitize(), len(vals)))
	}
	q := "SELECT " + strings.Join(sel, ", ") + " FROM " + pgx.Identifier{nsp, rel}.Sanitize() + " WHERE " + strings.Join(conds, " AND ")
	formats := make([]int16, len(vals))
	rr := conn.PgConn().ExecParams(ctx, q, vals, oids, formats, nil)
	r := rr.Read()
	if r.Err != nil {
		return witness.RowObservation{}, readErr(r.Err)
	}
	if len(r.Rows) == 0 {
		return out, &connector.Error{Kind: connector.KindNotFound, Code: connector.CodeUpstreamNotFound,
			AgentMessage: "The row isn't there any more."}
	}
	if len(r.Rows) > 1 {
		return witness.RowObservation{}, &connector.ErrReadBackSkipped{Why: "the key matched more than one row"}
	}
	row := r.Rows[0]
	out.RowFound = true
	xmin, err := strconv.ParseUint(string(row[0]), 10, 32)
	out.XminMatches = err == nil && uint32(xmin) == xmin32
	for j, c := range cmp {
		m, v := row[1+2*j], row[2+2*j]
		cr := &out.Columns[c.i]
		cr.Match = string(m) == "t"
		if v != nil {
			cr.Observed = string(v)
		}
	}
	return out, nil
}

// setColumns are the SET list's column names.
func setColumns(s sqlguard.Shape) []string {
	out := make([]string, len(s.Set))
	for i, a := range s.Set {
		out[i] = a.Column
	}
	return out
}

// valueOf is a SET or key value as text for Postgres to parse with the column's type: literals as
// written, $n from the statement's params. known is false for an expression (x + 1, now()), which
// a read-back can't predict.
func valueOf(v sqlguard.Value, params []any) (text string, isNull, known bool) {
	switch v.Kind {
	case sqlguard.ShapeValueString, sqlguard.ShapeValueInteger, sqlguard.ShapeValueNumeric, sqlguard.ShapeValueBool:
		return v.Text, false, true
	case sqlguard.ShapeValueNull:
		return "", true, true
	case sqlguard.ShapeValueParam:
		if v.Param < 1 || v.Param > len(params) {
			return "", false, false
		}
		switch p := params[v.Param-1].(type) {
		case nil:
			return "", true, true
		case string:
			return p, false, true
		case int64:
			return strconv.FormatInt(p, 10), false, true
		case bool:
			return strconv.FormatBool(p), false, true
		}
	}
	return "", false, false
}

// readErr maps a read-back failure: a timeout is the witness's readback_timeout once the budget
// passes; anything else is a failed read-back with Postgres's class, never the statement's text.
func readErr(err error) error {
	var pe *pgconn.PgError
	code := connector.CodeUpstreamError
	if errors.As(err, &pe) && pe.Code == "57014" {
		code = connector.CodeTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &connector.Error{Kind: connector.KindUpstream, Code: code, AgentMessage: "The row read-back failed.", Err: err}
}
