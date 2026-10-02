package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/deedglass/deedglass/internal/connector"
)

// The transaction recipes (S17 §3.6). Every statement an agent sends goes through
// Conn.Query with an explicit exec mode, never Conn.Exec: pgx v5.11.0's Exec switches to the
// simple protocol when there are no arguments (conn.go: "Always use simple protocol when there
// are no arguments"), and the simple protocol runs stacked statements. Query with
// QueryExecModeDescribeExec always uses the extended protocol, where Postgres refuses a second
// statement ("cannot insert multiple commands into a prepared statement").

// Params converts the agent's params to values pgx sends: integers (≤ 2^53−1) as int64, strings,
// booleans and null. Anything else (fractions, objects, arrays) is validation_failed: send
// decimals as strings, so the digest is exact.
func Params(raw []json.RawMessage) ([]any, error) {
	out := make([]any, len(raw))
	for i, r := range raw {
		dec := json.NewDecoder(strings.NewReader(string(r)))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, connector.ErrValidation(fmt.Sprintf("params[%d]", i), "can't be read")
		}
		switch x := v.(type) {
		case nil, string, bool:
			out[i] = x
		case json.Number:
			n, err := strconv.ParseInt(string(x), 10, 64)
			if err != nil || n > maxSafeInt || n < -maxSafeInt {
				return nil, connector.ErrValidation(fmt.Sprintf("params[%d]", i), "must be an integer up to 2^53−1, a string, a boolean or null; send decimals and large numbers as strings")
			}
			out[i] = n
		default:
			return nil, connector.ErrValidation(fmt.Sprintf("params[%d]", i), "must be a string, an integer, a boolean or null")
		}
	}
	return out, nil
}

// execMode is the agent statement's exec mode: DescribeExec on a direct connection (the server
// says the parameter types), Exec through a transaction pooler (one round trip on one backend).
// Both are the extended protocol.
func execMode(e *poolEntry) pgx.QueryExecMode {
	if e.dsn.Pooler {
		return pgx.QueryExecModeExec
	}
	return pgx.QueryExecModeDescribeExec
}

// searchPath is the configured schemas, quoted, then pg_temp last (so a temporary table can never
// shadow a real one).
func searchPath(cfg Config) string {
	parts := make([]string, 0, len(cfg.AllowedSchemas)+1)
	for _, s := range cfg.AllowedSchemas {
		parts = append(parts, pgx.Identifier{s}.Sanitize())
	}
	return strings.Join(append(parts, "pg_temp"), ", ")
}

// settingsSQL sets the transaction's limits and output formats in one statement.
const settingsSQL = `SELECT pg_catalog.set_config('statement_timeout', $1, true), pg_catalog.set_config('lock_timeout', $2, true),
	pg_catalog.set_config('search_path', $3, true), pg_catalog.set_config('TimeZone', 'UTC', true),
	pg_catalog.set_config('DateStyle', 'ISO, YMD', true), pg_catalog.set_config('IntervalStyle', 'postgres', true),
	pg_catalog.set_config('bytea_output', 'hex', true)`

func settingsArgs(cfg Config) []any {
	return []any{strconv.Itoa(cfg.StatementTimeoutMS) + "ms", strconv.Itoa(cfg.LockTimeoutMS) + "ms", searchPath(cfg)}
}

// Table is a result set as tools return it.
type Table struct {
	Columns   []Column          `json:"columns"`
	Rows      []json.RawMessage `json:"rows"`
	RowCount  int               `json:"row_count"`
	Truncated bool              `json:"truncated"`
}

// Column is one result column.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// readRows reads up to limit rows and maxBytes of JSON. With stopEarly, it stops reading at the
// first row past a cap and reports truncated (the caller closes the connection instead of draining
// it); without, it drains every row (a write's RETURNING must finish for its command tag).
func readRows(ctx context.Context, conn *pgx.Conn, rows pgx.Rows, limit, maxBytes int, stopEarly bool) (*Table, pgconn.CommandTag, bool, error) {
	fds := rows.FieldDescriptions()
	t := &Table{Columns: make([]Column, len(fds)), Rows: []json.RawMessage{}}
	var unknown []uint32
	for i, fd := range fds {
		t.Columns[i] = Column{Name: fd.Name, Type: TypeName(fd.DataTypeOID)}
		if t.Columns[i].Type == "" {
			unknown = append(unknown, fd.DataTypeOID)
		}
	}
	size, stopped := 0, false
	for rows.Next() {
		if t.Truncated {
			if stopEarly {
				stopped = true
				break
			}
			continue
		}
		raw := rows.RawValues()
		var sb strings.Builder
		sb.WriteByte('[')
		for i, v := range raw {
			if i > 0 {
				sb.WriteByte(',')
			}
			j, err := ToJSON(fds[i].DataTypeOID, v)
			if err != nil {
				return nil, pgconn.CommandTag{}, false, err
			}
			sb.Write(j)
		}
		sb.WriteByte(']')
		if len(t.Rows) >= limit || size+sb.Len() > maxBytes {
			t.Truncated = true
			if stopEarly {
				stopped = true
				break
			}
			continue
		}
		size += sb.Len() + 1
		t.Rows = append(t.Rows, json.RawMessage(sb.String()))
	}
	if stopped {
		return t, pgconn.CommandTag{}, true, nil
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, pgconn.CommandTag{}, false, err
	}
	t.RowCount = len(t.Rows)
	if len(unknown) > 0 && conn != nil {
		names := map[uint32]string{}
		r, err := conn.Query(ctx, `SELECT t.oid, pg_catalog.format_type(t.oid, NULL) FROM pg_catalog.pg_type AS t WHERE t.oid = ANY($1)`, unknown)
		if err == nil {
			for r.Next() {
				var oid uint32
				var name string
				if r.Scan(&oid, &name) == nil {
					names[oid] = name
				}
			}
			r.Close()
		}
		for i, fd := range fds {
			if t.Columns[i].Type == "" {
				t.Columns[i].Type = names[fd.DataTypeOID]
			}
		}
	}
	return t, rows.CommandTag(), false, nil
}

// abandon closes a connection whose session can't be trusted or whose result is still streaming;
// the pool then destroys it instead of reusing it.
func abandon(c *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.Conn().Close(ctx)
}

// rollback ends a transaction; when it can't, the connection is abandoned.
func rollback(c *pgxpool.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Exec(ctx, "ROLLBACK"); err != nil {
		abandon(c)
	}
}

// LedgerArgs are begin_action's arguments.
type LedgerArgs struct {
	ActionID, OrgID, AgentID string
	IntentHash               string // "" is NULL
	SQLDigest                string
}

// Stmt is one statement execInTx runs after begin_action. Agent statements arrive one at a time;
// S23's postgres.undo sends several Deedglass-generated statements in one transaction.
type Stmt struct {
	SQL    string
	Params []any
}

// StmtResult is what one statement did.
type StmtResult struct {
	Tag       pgconn.CommandTag
	Returning *Table // nil when the statement returns no rows
}

// TxResult is a committed transaction's evidence.
type TxResult struct {
	XID8      string
	MarkerLSN string
	Results   []StmtResult
}

// execInTx is the write recipe (S17 §3.6): BEGIN READ COMMITTED READ WRITE, set_config and
// begin_action in one batch (one round trip), each statement in turn (refused above
// max_rows_affected, which rolls everything back), then COMMIT. The ledger row, the marker and
// every change commit together or not at all. Nothing is ever retried.
//
// Errors before COMMIT leave nothing committed: they are failed kinds. A COMMIT whose answer is
// lost is unknown (the change may have applied). Kept internal and reusable for S23's undo.
func (c *Connector) execInTx(ctx context.Context, e *poolEntry, name string, cfg Config, la LedgerArgs, stmts []Stmt) (*TxResult, error) {
	pc, res, err := c.beginWrite(ctx, e, name, cfg, la)
	if err != nil {
		return nil, err
	}
	defer pc.Release()
	conn := pc.Conn()
	mode := execMode(e)
	for _, s := range stmts {
		args := append([]any{mode, pgx.QueryResultFormats{pgx.TextFormatCode}}, s.Params...)
		rows, err := conn.Query(ctx, s.SQL, args...)
		if err != nil {
			rollback(pc)
			return nil, statementError(err, cfg)
		}
		t, tag, _, err := readRows(ctx, nil, rows, cfg.RowLimit, cfg.MaxResultBytes, false)
		if err != nil {
			rollback(pc)
			return nil, statementError(err, cfg)
		}
		if n := tag.RowsAffected(); n > int64(cfg.MaxRowsAffected) {
			rollback(pc)
			return nil, &connector.Error{Kind: connector.KindToolError, Code: CodeRowLimitExceeded,
				AgentMessage: fmt.Sprintf(msgRows, thousands(n), thousands(int64(cfg.MaxRowsAffected)))}
		}
		sr := StmtResult{Tag: tag}
		if len(t.Columns) > 0 {
			sr.Returning = t
		}
		res.Results = append(res.Results, sr)
	}
	if err := c.commitWrite(ctx, pc, cfg); err != nil {
		return nil, err
	}
	return res, nil
}

// beginWrite opens the write transaction (S17 §3.6): BEGIN READ COMMITTED READ WRITE, set_config,
// any extra settings, and begin_action in one batch (one round trip). begin_action is the
// transaction's first write: its guard refuses it once the transaction has an xid. The caller
// releases the connection; on an error it is already rolled back and released.
func (c *Connector) beginWrite(ctx context.Context, e *poolEntry, name string, cfg Config, la LedgerArgs, extra ...string) (*pgxpool.Conn, *TxResult, error) {
	pc, err := e.acquire(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	conn := pc.Conn()
	b := &pgx.Batch{}
	b.Queue("BEGIN ISOLATION LEVEL READ COMMITTED READ WRITE")
	b.Queue(settingsSQL, settingsArgs(cfg)...)
	for _, x := range extra {
		b.Queue(x)
	}
	var intent any
	if la.IntentHash != "" {
		intent = la.IntentHash
	}
	b.Queue(`SELECT b.xid8::text, b.marker_lsn::text FROM deedglass.begin_action($1, $2, $3, $4, $5) AS b`, la.ActionID, la.OrgID, la.AgentID, intent, la.SQLDigest)
	br := conn.SendBatch(ctx, b)
	res := &TxResult{}
	for i := 0; i < 2+len(extra) && err == nil; i++ {
		_, err = br.Exec()
	}
	if err == nil {
		err = br.QueryRow().Scan(&res.XID8, &res.MarkerLSN)
	}
	if cerr := br.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		rollback(pc)
		pc.Release()
		if conn.IsClosed() || ctx.Err() != nil {
			return nil, nil, &connector.Error{Kind: connector.KindToolError, Code: connector.CodeTimeout,
				AgentMessage: "The database didn't answer before the transaction began, so nothing ran.", Err: err}
		}
		ce := setupError(err, name, cfg)
		if ce.Code == "internal_error" {
			c.log.ErrorContext(ctx, "postgres: begin_action refused the connector's own transaction", "connector", name,
				"sqlstate", sqlState(err), "alert", "begin_action_guard")
		}
		return nil, nil, ce
	}
	return pc, res, nil
}

// commitWrite runs the beforeCommit hook and COMMIT. A server answer other than COMMIT means it
// rolled back (a deferred constraint, a serialization failure); a lost answer means it may have
// applied (unknown).
func (c *Connector) commitWrite(ctx context.Context, pc *pgxpool.Conn, cfg Config) error {
	if c.hooks.beforeCommit != nil {
		if err := c.hooks.beforeCommit(ctx); err != nil {
			rollback(pc)
			return err
		}
	}
	tag, err := pc.Conn().Exec(context.WithoutCancel(ctx), "COMMIT")
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			abandonIfBusy(pc)
			return statementError(err, cfg)
		}
		abandon(pc)
		return &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, AgentMessage: msgCommitLost, Err: err}
	}
	if tag.String() != "COMMIT" {
		return &connector.Error{Kind: connector.KindToolError, Code: connector.CodeUpstreamError,
			AgentMessage: "Postgres rolled the transaction back at COMMIT. Nothing was committed."}
	}
	return nil
}

func abandonIfBusy(pc *pgxpool.Conn) {
	if pc.Conn().PgConn().TxStatus() != 'I' {
		rollback(pc)
	}
}

// queryInTx is the read recipe: BEGIN REPEATABLE READ READ ONLY, set_config, the statement
// (reading at most limit+1 rows and maxBytes, then stopping), ROLLBACK. It never commits.
func (c *Connector) queryInTx(ctx context.Context, e *poolEntry, name string, cfg Config, sql string, params []any, limit int) (*Table, error) {
	pc, err := e.acquire(ctx, name)
	if err != nil {
		return nil, err
	}
	defer pc.Release()
	conn := pc.Conn()
	b := &pgx.Batch{}
	b.Queue("BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY")
	b.Queue(settingsSQL, settingsArgs(cfg)...)
	br := conn.SendBatch(ctx, b)
	_, err = br.Exec()
	if err == nil {
		_, err = br.Exec()
	}
	if cerr := br.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		rollback(pc)
		return nil, setupError(err, name, cfg)
	}
	args := append([]any{execMode(e), pgx.QueryResultFormats{pgx.TextFormatCode}}, params...)
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		rollback(pc)
		return nil, statementError(err, cfg)
	}
	t, _, stopped, err := readRows(ctx, conn, rows, limit, cfg.MaxResultBytes, true)
	if stopped {
		// More rows are on the wire: closing the connection stops Postgres sending them. The
		// transaction was read-only, so nothing is lost.
		abandon(pc)
		t.RowCount = len(t.Rows)
		return t, nil
	}
	if err != nil {
		rollback(pc)
		return nil, statementError(err, cfg)
	}
	rollback(pc)
	return t, nil
}

// clampLimit applies the config's row_limit to a requested limit.
func clampLimit(req, max int) int {
	if req <= 0 || req > max {
		return max
	}
	return req
}
