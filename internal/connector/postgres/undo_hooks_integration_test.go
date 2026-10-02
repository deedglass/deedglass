//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
)

// Hooks for the S23 undo round trips (undo_property_integration_test.go, package postgres_test).
// That test needs internal/decoder to capture a transaction's frame, and internal/decoder imports
// this package, so it can't be an internal test: these expose the package's test database and its
// write recipe to it. Test files only; nothing in the binary sees them.

// UndoTestDB is the package's throwaway customer database (set up once, under the role lock).
func UndoTestDB(t *testing.T) *pgtest.DB { return db(t) }

// UndoTestInstance is the Acme connector instance over the agent and witness DSNs.
func UndoTestInstance(t *testing.T, c *Connector, dsn, witness string) *connector.Instance {
	return inst(t, c, pgtest.ConfigJSON, dsn, witness, devEnv())
}

// UndoTestTx is one write transaction through the connector's own recipe (S17 §3.6): beginWrite
// (BEGIN READ COMMITTED READ WRITE, the settings, begin_action as the first write: the ledger row
// and the marker), statements one at a time, then commitWrite.
type UndoTestTx struct {
	pc   *pgxpool.Conn
	cfg  Config
	mode pgx.QueryExecMode
	c    *Connector
	done bool
	// XID8 and MarkerLSN are begin_action's answer.
	XID8, MarkerLSN string
}

// UndoTestBegin opens a write transaction for an action as the agent role.
func (c *Connector) UndoTestBegin(ctx context.Context, in *connector.Instance, la LedgerArgs) (*UndoTestTx, error) {
	st, err := parse(in)
	if err != nil {
		return nil, err
	}
	e, err := c.entry(ctx, in, st)
	if err != nil {
		return nil, err
	}
	pc, res, err := c.beginWrite(ctx, e, in.Name, st.cfg, la)
	if err != nil {
		return nil, err
	}
	return &UndoTestTx{pc: pc, cfg: st.cfg, mode: execMode(e), c: c, XID8: res.XID8, MarkerLSN: res.MarkerLSN}, nil
}

// Exec runs one statement as execInTx does (extended protocol, text results, refused above
// max_rows_affected) and returns its row count and any RETURNING rows as text (nil = NULL). An
// error rolls the transaction back and releases the connection.
func (x *UndoTestTx) Exec(ctx context.Context, sql string, params ...any) (int64, [][]*string, error) {
	if x.done {
		return 0, nil, fmt.Errorf("the transaction already ended")
	}
	args := append([]any{x.mode, pgx.QueryResultFormats{pgx.TextFormatCode}}, params...)
	rows, err := x.pc.Conn().Query(ctx, sql, args...)
	if err != nil {
		x.abort()
		return 0, nil, statementError(err, x.cfg)
	}
	var out [][]*string
	for rows.Next() {
		raw := rows.RawValues()
		r := make([]*string, len(raw))
		for i, v := range raw {
			if v != nil {
				s := string(v)
				r[i] = &s
			}
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		x.abort()
		return 0, nil, statementError(err, x.cfg)
	}
	n := rows.CommandTag().RowsAffected()
	if n > int64(x.cfg.MaxRowsAffected) {
		x.abort()
		return 0, nil, fmt.Errorf("%d rows, over max_rows_affected", n)
	}
	return n, out, nil
}

// Commit is commitWrite.
func (x *UndoTestTx) Commit(ctx context.Context) error {
	if x.done {
		return fmt.Errorf("the transaction already ended")
	}
	x.done = true
	defer x.pc.Release()
	return x.c.commitWrite(ctx, x.pc, x.cfg)
}

// Rollback ends the transaction without committing (a no-op once it ended).
func (x *UndoTestTx) Rollback() {
	if !x.done {
		x.abort()
	}
}

func (x *UndoTestTx) abort() {
	x.done = true
	rollback(x.pc)
	x.pc.Release()
}
