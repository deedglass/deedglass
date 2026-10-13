package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/witness/undo"
)

// ToolUndo is the internal undo tool (S23): never listed to agents, only a person's undo reaches it
// (the Gate's console principal), and its plan comes only from Postgres's own row images.
const ToolUndo = "undo"

// CodeUndoConflict is the failed undo's error code: Postgres rolled it back, nothing was changed.
const CodeUndoConflict = "undo_conflict"

const undoInput = `{"type":"object","additionalProperties":false,"required":["undoes","plan_digest","reason"],"properties":{
"undoes":{"type":"string","maxLength":64,"description":"The action to undo."},
"plan_digest":{"type":"string","maxLength":71,"description":"The previewed plan's digest."},
"allow_triggers":{"type":"boolean","description":"Let triggers on the tables fire again (admins only)."},
"reason":{"type":"string","maxLength":280,"description":"Why the undo runs; the approver and the Ledger read it."}}}`

// ToolUndoSession is S50's internal tool: one step that undoes every transaction of a watched
// session (Pro, ee/undosession). Like undo, only a person's request reaches it, and its plan comes
// only from Postgres's own row images.
const ToolUndoSession = "undo_session"

const undoSessionInput = `{"type":"object","additionalProperties":false,"required":["session","plan_digest","reason"],"properties":{
"session":{"type":"string","maxLength":64,"description":"The watched session to undo."},
"plan_digest":{"type":"string","maxLength":71,"description":"The previewed plan's digest."},
"allow_triggers":{"type":"boolean","description":"Let triggers on the tables fire again (admins only)."},
"reason":{"type":"string","maxLength":280,"description":"Why the undo runs; the approver and the Ledger read it."},
"excluded":{"type":"array","items":{"type":"string","maxLength":32},"description":"The transactions a person left out of the undo."}}}`

// undoSessionSpec is the session undo's internal tool (S50 §3.6).
func undoSessionSpec() connector.ToolSpec {
	return connector.ToolSpec{Name: ToolUndoSession, SideEffect: connector.SideEffectWrite, Internal: true,
		Description: "Put back every row a watched session changed, from the values Postgres recorded before it.",
		InputSchema: json.RawMessage(undoSessionInput)}
}

// undoSpec is the internal tool every Postgres connector declares (S23 §2).
func undoSpec() connector.ToolSpec {
	return connector.ToolSpec{Name: ToolUndo, SideEffect: connector.SideEffectWrite, Internal: true,
		Description: "Put back the rows an earlier action changed, from the values Postgres recorded before it.",
		InputSchema: json.RawMessage(undoInput)}
}

// The catalog queries (S23 §4 step 4): constants, by relid, read as deedglass_agent.
const (
	undoTablesSQL = `SELECT c.oid, n.nspname, c.relname, c.relkind::text, c.relreplident::text, c.relrowsecurity, c.relforcerowsecurity,
  pg_catalog.has_table_privilege(c.oid, 'SELECT'), pg_catalog.has_table_privilege(c.oid, 'INSERT'),
  pg_catalog.has_table_privilege(c.oid, 'UPDATE') OR pg_catalog.has_any_column_privilege(c.oid, 'UPDATE'),
  pg_catalog.has_table_privilege(c.oid, 'DELETE')
FROM pg_catalog.pg_class AS c JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE c.oid = ANY($1::oid[]) AND c.relkind IN ('r', 'p')`
	undoColumnsSQL = `SELECT a.attrelid, a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod), a.atttypid, a.attgenerated::text, a.attidentity::text
FROM pg_catalog.pg_attribute AS a
WHERE a.attrelid = ANY($1::oid[]) AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attrelid, a.attnum`
	undoPKSQL = `SELECT i.indrelid, a.attname
FROM pg_catalog.pg_index AS i
CROSS JOIN LATERAL pg_catalog.unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord)
JOIN pg_catalog.pg_attribute AS a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
WHERE i.indrelid = ANY($1::oid[]) AND i.indisprimary
ORDER BY i.indrelid, k.ord`
	undoTriggersSQL = `SELECT t.tgrelid, t.tgname FROM pg_catalog.pg_trigger AS t
WHERE t.tgrelid = ANY($1::oid[]) AND NOT t.tgisinternal AND t.tgenabled <> 'D'
ORDER BY 1, 2`
	undoRulesSQL = `SELECT r.ev_class, r.rulename FROM pg_catalog.pg_rewrite AS r
WHERE r.ev_class = ANY($1::oid[]) AND r.ev_type <> '1' AND r.ev_enabled <> 'D'
ORDER BY 1, 2`
	undoReferrersSQL = `SELECT con.confrelid, con.conname, cn.nspname || '.' || cc.relname, con.confdeltype::text, con.confupdtype::text,
  ARRAY(SELECT a.attname::text FROM pg_catalog.unnest(con.confkey) WITH ORDINALITY AS k(attnum, ord)
        JOIN pg_catalog.pg_attribute AS a ON a.attrelid = con.confrelid AND a.attnum = k.attnum ORDER BY k.ord),
  ARRAY(SELECT a.attname::text FROM pg_catalog.unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord)
        JOIN pg_catalog.pg_attribute AS a ON a.attrelid = con.conrelid AND a.attnum = k.attnum ORDER BY k.ord)
FROM pg_catalog.pg_constraint AS con
JOIN pg_catalog.pg_class AS cc ON cc.oid = con.conrelid
JOIN pg_catalog.pg_namespace AS cn ON cn.oid = cc.relnamespace
WHERE con.contype = 'f' AND con.confrelid = ANY($1::oid[])
ORDER BY 1, 2`
	undoRoleSQL = `SELECT current_user::text, r.rolbypassrls FROM pg_catalog.pg_roles AS r WHERE r.rolname = current_user`
)

// querier is what the catalog reader needs (a connection or a transaction).
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readUndoCatalog reads the catalog of relids with the constant queries.
func readUndoCatalog(ctx context.Context, q querier, relids []uint32) (*undo.Catalog, error) {
	cat := &undo.Catalog{Tables: map[uint32]*undo.TableCatalog{}}
	for _, r := range relids {
		cat.Tables[r] = &undo.TableCatalog{RelID: r}
	}
	if err := q.QueryRow(ctx, undoRoleSQL).Scan(&cat.Role, &cat.BypassRLS); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, undoTablesSQL, relids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t undo.TableCatalog
		if err := rows.Scan(&t.RelID, &t.Schema, &t.Name, &t.Kind, &t.ReplIdent, &t.RLS, &t.ForceRLS, &t.Select, &t.Insert, &t.Update, &t.Delete); err != nil {
			rows.Close()
			return nil, err
		}
		t.Exists = true
		x := t
		cat.Tables[t.RelID] = &x
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	each := func(sql string, f func(rows pgx.Rows) error) error {
		rows, err := q.Query(ctx, sql, relids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := f(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	err = each(undoColumnsSQL, func(rows pgx.Rows) error {
		var rel uint32
		var c undo.ColumnCatalog
		if err := rows.Scan(&rel, &c.Name, &c.Type, &c.TypeOID, &c.Generated, &c.Identity); err != nil {
			return err
		}
		if t := cat.Tables[rel]; t != nil && t.Exists {
			t.Columns = append(t.Columns, c)
		}
		return nil
	})
	if err == nil {
		err = each(undoPKSQL, func(rows pgx.Rows) error {
			var rel uint32
			var name string
			if err := rows.Scan(&rel, &name); err != nil {
				return err
			}
			if t := cat.Tables[rel]; t != nil && t.Exists {
				t.PK = append(t.PK, name)
			}
			return nil
		})
	}
	if err == nil {
		err = each(undoTriggersSQL, func(rows pgx.Rows) error {
			var rel uint32
			var name string
			if err := rows.Scan(&rel, &name); err != nil {
				return err
			}
			if t := cat.Tables[rel]; t != nil && t.Exists {
				t.Triggers = append(t.Triggers, name)
			}
			return nil
		})
	}
	if err == nil {
		err = each(undoRulesSQL, func(rows pgx.Rows) error {
			var rel uint32
			var name string
			if err := rows.Scan(&rel, &name); err != nil {
				return err
			}
			if t := cat.Tables[rel]; t != nil && t.Exists {
				t.Rules = append(t.Rules, name)
			}
			return nil
		})
	}
	if err == nil {
		err = each(undoReferrersSQL, func(rows pgx.Rows) error {
			var rel uint32
			var fk undo.ForeignKey
			if err := rows.Scan(&rel, &fk.Name, &fk.From, &fk.OnDelete, &fk.OnUpdate, &fk.Columns, &fk.FromColumns); err != nil {
				return err
			}
			if t := cat.Tables[rel]; t != nil && t.Exists {
				t.Referrers = append(t.Referrers, fk)
			}
			return nil
		})
	}
	if err != nil {
		return nil, err
	}
	return cat, nil
}

// readGuards reads the current state of every guard row: one statement per table, keys sorted, in
// text format (each value is the type's output under the decoder's pinned settings), FOR UPDATE
// when lock is set. A lock that isn't granted within lock_timeout is undo_conflict's lock variant.
func readGuards(ctx context.Context, q querier, mode pgx.QueryExecMode, p *undo.Plan, lock bool) (undo.Current, error) {
	cur := undo.Current{}
	for _, t := range p.Tables {
		keys := undo.GuardKeys(p, t.RelID)
		if len(keys) == 0 {
			continue
		}
		cols := undo.GuardColumns(p, t.RelID)
		st, err := undo.GuardSelect(t, cols, keys, lock)
		if err != nil {
			return nil, err
		}
		args := append([]any{mode, pgx.QueryResultFormats{pgx.TextFormatCode}}, st.Args...)
		rows, err := q.Query(ctx, st.SQL, args...)
		if err != nil {
			return nil, err
		}
		names := append(append([]string{}, t.PK...), cols...)
		byKey := map[string]undo.Image{}
		for rows.Next() {
			raw := rows.RawValues()
			img := undo.Image{}
			key := undo.Key{}
			for i, name := range names {
				if i >= len(raw) {
					break
				}
				v := textValue(raw[i])
				img[name] = v
				if i < len(t.PK) && v.T != nil {
					key[name] = *v.T
				}
			}
			byKey[key.ID(t.PK)] = img
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		cur[t.RelID] = byKey
	}
	return cur, nil
}

// textValue is one text-format column as the plan compares it: NULL, text, or hex for bytes that
// aren't UTF-8 (a SQL_ASCII database), as the decoder stored them.
func textValue(b []byte) undo.Value {
	if b == nil {
		return undo.Value{N: true}
	}
	if !utf8.Valid(b) {
		return undo.Value{X: hex.EncodeToString(b)}
	}
	s := string(b)
	return undo.Value{T: &s}
}

// UndoCatalog reads the catalog of the tables relids name, through the connector's own pool as
// deedglass_agent, in a READ ONLY transaction. It never takes a lock beyond the catalog's.
func (c *Connector) UndoCatalog(ctx context.Context, inst *connector.Instance, relids []uint32) (*undo.Catalog, error) {
	var cat *undo.Catalog
	err := c.readOnly(ctx, inst, func(ctx context.Context, tx pgx.Tx, _ pgx.QueryExecMode) error {
		var err error
		cat, err = readUndoCatalog(ctx, tx, relids)
		return err
	})
	return cat, err
}

// UndoRows reads the guard rows of a plan without locks (the preview).
func (c *Connector) UndoRows(ctx context.Context, inst *connector.Instance, p *undo.Plan) (undo.Current, error) {
	var cur undo.Current
	err := c.readOnly(ctx, inst, func(ctx context.Context, tx pgx.Tx, mode pgx.QueryExecMode) error {
		var err error
		cur, err = readGuards(ctx, tx, mode, p, false)
		return err
	})
	return cur, err
}

// readOnly runs f in BEGIN READ ONLY with the connector's settings, then rolls back.
func (c *Connector) readOnly(ctx context.Context, inst *connector.Instance, f func(ctx context.Context, tx pgx.Tx, mode pgx.QueryExecMode) error) error {
	st, err := parse(inst)
	if err != nil {
		return err
	}
	e, err := c.entry(ctx, inst, st)
	if err != nil {
		return err
	}
	pc, err := e.acquire(ctx, inst.Name)
	if err != nil {
		return err
	}
	defer pc.Release()
	tx, err := pc.Conn().BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, settingsSQL, settingsArgs(st.cfg)...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_catalog.set_config('extra_float_digits', '3', true)`); err != nil {
		return err
	}
	return f(ctx, tx, execMode(e))
}

// undoArgs are postgres.undo's arguments.
type undoArgs struct {
	Undoes        string `json:"undoes"`
	PlanDigest    string `json:"plan_digest"`
	AllowTriggers bool   `json:"allow_triggers"`
	Reason        string `json:"reason"`
}

// RunUndo runs a plan in one transaction as deedglass_agent (S23 §4 step 7): the transaction
// settings of the decode session, SET CONSTRAINTS ALL DEFERRED, begin_action as the first write
// (the ledger row and the marker; its sql_digest is the plan digest), the catalog re-checked, the
// guard rows locked FOR UPDATE in key order and compared byte for byte, then every step, each of
// which must change exactly one row, then COMMIT. Any miss rolls everything back: the action is
// failed with undo_conflict, and nothing was changed.
func (c *Connector) RunUndo(ctx context.Context, inst *connector.Instance, call connector.Call, p *undo.Plan, o undo.RunOptions) (connector.Result, error) {
	st, err := parse(inst)
	if err != nil {
		return connector.Result{}, err
	}
	if p == nil || p.Digest == "" || len(p.Steps) == 0 {
		return connector.Result{}, conflict("The plan is empty.")
	}
	e, err := c.entry(ctx, inst, st)
	if err != nil {
		return connector.Result{}, err
	}
	cfg := st.cfg
	if o.LockTimeout > 0 {
		cfg.LockTimeoutMS = int(o.LockTimeout / time.Millisecond)
	}
	if o.StatementTimeout > 0 {
		cfg.StatementTimeoutMS = int(o.StatementTimeout / time.Millisecond)
	}
	cfg.MaxRowsAffected = 1
	la := LedgerArgs{ActionID: call.ActionID.String(), OrgID: call.OrgID.String(), AgentID: call.AgentID.String(), IntentHash: call.IntentHash,
		SQLDigest: p.Digest}
	pc, res, err := c.beginWrite(ctx, e, inst.Name, cfg, la,
		`SELECT pg_catalog.set_config('extra_float_digits', '3', true)`, `SET CONSTRAINTS ALL DEFERRED`)
	if err != nil {
		return connector.Result{}, err
	}
	defer pc.Release()
	conn := pc.Conn()
	mode := execMode(e)
	fail := func(err error) (connector.Result, error) {
		rollback(pc)
		return connector.Result{}, err
	}
	// The catalog, again, inside the transaction.
	relids := make([]uint32, 0, len(p.Tables))
	for _, t := range p.Tables {
		relids = append(relids, t.RelID)
	}
	cat, err := readUndoCatalog(ctx, conn, relids)
	if err != nil {
		return fail(stepError(err, cfg, p))
	}
	if why := undo.Recheck(p, cat, o.AllowTriggers); why != "" {
		return fail(conflict(fmt.Sprintf(undo.MsgConflictPlan, why)))
	}
	// The guards, locked in key order, compared byte for byte.
	cur, err := readGuards(ctx, conn, mode, p, true)
	if err != nil {
		return fail(stepError(err, cfg, p))
	}
	if bad, _ := undo.Compare(p, cur); len(bad) > 0 {
		return fail(conflict(conflictText(bad[0])))
	}
	// The steps: each exactly one row.
	for _, s := range p.Steps {
		t, ok := p.Table(s.Table)
		if !ok {
			return fail(conflict(fmt.Sprintf(undo.MsgConflictPlan, "a step names a table the plan doesn't")))
		}
		stmt, err := undo.StepStatement(t, s)
		if err != nil {
			return fail(conflict(fmt.Sprintf(undo.MsgConflictPlan, err.Error())))
		}
		args := append([]any{mode}, stmt.Args...)
		tag, err := execOne(ctx, conn, stmt.SQL, args)
		if err != nil {
			return fail(stepError(err, cfg, p))
		}
		if n := tag.RowsAffected(); n != 1 {
			return fail(conflict(fmt.Sprintf(undo.MsgConflictCount, n, t.String())))
		}
	}
	if err := c.commitWrite(ctx, pc, cfg); err != nil {
		return connector.Result{}, err
	}
	tables := p.TableNames()
	out, _ := json.Marshal(map[string]any{"undoes": p.Undoes, "rows_restored": p.Rows, "tables": tables, "plan_digest": p.Digest,
		"summary": undo.ResultText(p.Rows, tables)})
	return connector.Result{
		Output: out,
		Meta:   map[string]any{"command": "UNDO", "rows_restored": p.Rows, "steps": len(p.Steps)},
		Evidence: map[string]string{"xid8": res.XID8, "marker_lsn": res.MarkerLSN, "sql_digest": p.Digest,
			"command_tag": "UNDO " + strconv.Itoa(len(p.Steps))},
	}, nil
}

// execOne runs one generated statement through Query with an explicit exec mode (never Exec: the
// extended protocol only) and returns its command tag.
func execOne(ctx context.Context, conn querier, sql string, args []any) (pgconn.CommandTag, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	rows.Close()
	return rows.CommandTag(), rows.Err()
}

// conflict is a failed undo: Postgres rolled it back and nothing was changed.
func conflict(msg string) *connector.Error {
	return &connector.Error{Kind: connector.KindToolError, Code: CodeUndoConflict, AgentMessage: msg}
}

// conflictText is undo_conflict's sentence for the first guard that didn't hold.
func conflictText(m undo.Mismatch) string {
	switch {
	case strings.Contains(m.Text, "exists again"):
		return fmt.Sprintf(undo.MsgConflictExists, m.Table, m.Key)
	case strings.Contains(m.Text, "is gone"):
		return fmt.Sprintf(undo.MsgConflictGone, m.Table, m.Key)
	}
	// "public.orders id=42 changed since the action: status is now 'cancelled'."
	detail := m.Text
	if _, after, ok := strings.Cut(detail, ": "); ok {
		detail = strings.TrimSuffix(after, ".")
	}
	return fmt.Sprintf(undo.MsgConflictRow, m.Table, m.Key, detail)
}

// stepError maps a Postgres error inside the undo to undo_conflict (every one is certain: the
// transaction rolled back, nothing was changed). A lock that wasn't granted names the lock.
func stepError(err error, cfg Config, p *undo.Plan) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "55P03": // lock_not_available
			t := "A row"
			if len(p.Tables) > 0 {
				t = "A row in " + p.Tables[0].String()
			}
			return conflict(fmt.Sprintf(undo.MsgConflictLock, t, "the undo needs"))
		case "57014": // query_canceled: statement_timeout
			return conflict(fmt.Sprintf(undo.MsgConflictFailed, "it ran past the statement timeout"))
		}
		return conflict(fmt.Sprintf(undo.MsgConflictFailed, pgReason(pe)))
	}
	if ce := connector.AsError(err); ce != nil {
		return ce
	}
	return statementError(err, cfg)
}

// pgReason is a Postgres error as a short phrase with its SQLSTATE, never a value.
func pgReason(pe *pgconn.PgError) string {
	switch {
	case pe.Code == "23505":
		return "a row with that key exists, SQLSTATE 23505"
	case pe.Code == "23503":
		return "a foreign key, SQLSTATE 23503"
	case pe.Code == "42501":
		return "permission denied, SQLSTATE 42501"
	case slices.Contains([]string{"23502", "23514", "23P01"}, pe.Code):
		return "a constraint, SQLSTATE " + pe.Code
	}
	return "SQLSTATE " + pe.Code
}
