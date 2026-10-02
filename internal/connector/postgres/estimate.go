package postgres

//deedglass:allow-sql-build EXPLAIN (FORMAT JSON) prefixes the agent's own statement after Admit accepted it as one DML or SELECT statement; it never runs ANALYZE, every value is a parameter, and the READ ONLY transaction is always rolled back

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
)

var _ connector.Estimator = (*Connector)(nil)

// EstimateBudget is the longest an estimate may take (S22 §3.4): a hard cap, also the
// transaction's statement_timeout. Typical estimates take a few milliseconds.
const EstimateBudget = 500 * time.Millisecond

// estimateConnHook sees the estimate's connection inside its transaction (tests: read-only, role).
var estimateConnHook func(ctx context.Context, conn *pgx.Conn)

// Estimate implements connector.Estimator for postgres.execute and postgres.query: the planner's
// row estimate from EXPLAIN (FORMAT JSON), never ANALYZE, in a READ ONLY transaction as the agent
// role through the connector's pinned pool, with statement_timeout at the budget. Nothing is
// executed: EXPLAIN without ANALYZE only plans, Postgres skips the read-only check for it, and the
// transaction is always rolled back. A statement Admit refuses gets no estimate.
func (c *Connector) Estimate(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Estimate, error) {
	start := time.Now()
	st, err := parse(inst)
	if err != nil {
		return connector.NoEstimate(connector.EstimateError), err
	}
	if call.Tool.Name != ToolExecute && call.Tool.Name != ToolQuery {
		return connector.NoEstimate(connector.EstimateUnsupported), nil
	}
	var a sqlArgs
	if err := decodeArgs(call.Args, &a); err != nil {
		return connector.NoEstimate(connector.EstimateRefused), nil
	}
	params, err := Params(a.Params)
	if err != nil {
		return connector.NoEstimate(connector.EstimateRefused), nil
	}
	if _, err := Admit(call.Tool.Name, a.SQL, len(params), st.cfg.AllowedSchemas[0]); err != nil {
		return connector.NoEstimate(connector.EstimateRefused), nil
	}
	budget := EstimateBudget
	if d, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(d))
	}
	if budget <= 0 {
		return connector.NoEstimate(connector.EstimateTimeout), nil
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	e, err := c.entry(ctx, inst, st)
	if err != nil {
		return estimateFailed(ctx, err)
	}
	pc, err := e.acquire(ctx, inst.Name)
	if err != nil {
		return estimateFailed(ctx, err)
	}
	defer pc.Release()
	conn := pc.Conn()
	defer rollback(pc)
	b := &pgx.Batch{}
	b.Queue("BEGIN READ ONLY")
	b.Queue(settingsSQL, strconv.FormatInt(max(budget.Milliseconds(), 1), 10)+"ms", strconv.Itoa(st.cfg.LockTimeoutMS)+"ms", searchPath(st.cfg))
	br := conn.SendBatch(ctx, b)
	_, err = br.Exec()
	if err == nil {
		_, err = br.Exec()
	}
	if cerr := br.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return estimateFailed(ctx, err)
	}
	if estimateConnHook != nil {
		estimateConnHook(ctx, conn)
	}
	var plan []byte
	args := append([]any{execMode(e), pgx.QueryResultFormats{pgx.TextFormatCode}}, params...)
	if err := conn.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+a.SQL, args...).Scan(&plan); err != nil {
		return estimateFailed(ctx, err)
	}
	rows, ok := PlanRows(plan)
	if !ok {
		return connector.NoEstimate(connector.EstimateError), errors.New("postgres: the plan has no row estimate")
	}
	return connector.Estimate{Rows: rows, Method: connector.EstimateExplain, Took: time.Since(start)}, nil
}

func estimateFailed(ctx context.Context, err error) (connector.Estimate, error) {
	if ctx.Err() != nil {
		return connector.NoEstimate(connector.EstimateTimeout), nil
	}
	return connector.NoEstimate(connector.EstimateError), err
}

// PlanRows reads the planner's row estimate from EXPLAIN (FORMAT JSON) output: for a write
// (ModifyTable) the rows its first subplan feeds it, else the top node's rows. Rounded up, never
// negative.
func PlanRows(out []byte) (int64, bool) {
	type node struct {
		Type  string   `json:"Node Type"`
		Rows  *float64 `json:"Plan Rows"`
		Plans []node   `json:"Plans"`
	}
	var doc []struct {
		Plan node `json:"Plan"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc) != 1 {
		return 0, false
	}
	n := doc[0].Plan
	if n.Type == "ModifyTable" && len(n.Plans) > 0 {
		n = n.Plans[0]
	}
	if n.Rows == nil || *n.Rows < 0 {
		return 0, false
	}
	r := int64(*n.Rows)
	if float64(r) < *n.Rows {
		r++
	}
	return r, true
}
