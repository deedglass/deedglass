package postgres

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

var _ connector.L2Witnesser = (*Connector)(nil)

// L2Target implements connector.L2Witnesser (S21): for a witnessed connector's postgres.execute
// and postgres.call, the authorized statement, its ledger digest with S17's exact recipe, and the
// witness.* settings. Reads and describe have nothing for L2.
func (c *Connector) L2Target(inst *connector.Instance, call connector.Call) (*connector.L2Target, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	w := st.cfg.Witness
	if !w.Enabled {
		return nil, nil
	}
	t := &connector.L2Target{Tool: call.Tool.Name, DefaultSchema: st.cfg.AllowedSchemas[0], Watched: append([]string{}, w.WatchedTables...),
		AutoColumns: w.AutoColumns, MaxRows: w.MaxRows, StrictTriggers: w.StrictTriggers, Timeout: w.L2TimeoutOrDefault(), DiffRows: w.DiffRows}
	switch call.Tool.Name {
	case ToolExecute:
		var a sqlArgs
		if err := decodeArgs(call.Args, &a); err != nil {
			return nil, err
		}
		t.SQL, t.Params = a.SQL, a.Params
		if params, err := Params(a.Params); err == nil {
			t.SQLDigest, _ = SQLDigest(a.SQL, params)
		}
	case ToolCall:
		var a callArgs
		if err := decodeArgs(call.Args, &a); err != nil {
			return nil, err
		}
		f, ok := st.cfg.Function(a.Function)
		if !ok {
			return nil, nil
		}
		if _, digestArgs, err := CallStatement(f, a.Args); err == nil {
			t.SQLDigest, _ = CallDigest(f.Name, digestArgs)
		}
		for _, wr := range f.Writes {
			t.Writes = append(t.Writes, connector.L2Write{Table: wr.Table, Ops: wr.Ops, MaxRows: wr.MaxRows, Columns: wr.Columns})
		}
	case ToolUndo:
		// S23: the ledger row's sql_digest is the plan digest; the footprint is the plan's exact
		// one (actions.witness_expect), which the Gate reads itself.
		var a undoArgs
		if err := decodeArgs(call.Args, &a); err != nil {
			return nil, err
		}
		t.SQLDigest = a.PlanDigest
	default:
		return nil, nil
	}
	return t, nil
}

// triggerCacheTTL is how long a table's trigger list is reused (S21 §4 step 12).
const triggerCacheTTL = 60 * time.Second

type triggerEntry struct {
	at    time.Time
	names []string
}

var (
	triggerMu    sync.Mutex
	triggerCache = map[string]triggerEntry{} // connector id + config hash + table → names
)

// triggersSQL lists the enabled user triggers of the named tables, as the agent role sees the
// catalog (no privilege on the tables is needed to read pg_trigger).
const triggersSQL = `SELECT n.nspname || '.' || c.relname, t.tgname
FROM pg_catalog.pg_trigger AS t
JOIN pg_catalog.pg_class AS c ON c.oid = t.tgrelid
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE NOT t.tgisinternal AND t.tgenabled <> 'D' AND (n.nspname || '.' || c.relname) = ANY($1::text[])
ORDER BY 1, 2`

// L2Triggers implements connector.L2Witnesser: the enabled user triggers per table, read through
// the connector's own pool (never inside a control-plane transaction). A table with no triggers
// is cached for 60 s; a table with some is read every time, so a dropped trigger is never named.
func (c *Connector) L2Triggers(ctx context.Context, inst *connector.Instance, tables []string, fresh bool) (map[string][]string, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	var need []string
	now := time.Now()
	key := func(t string) string { return inst.ID.String() + "|" + inst.ConfigHash + "|" + t }
	triggerMu.Lock()
	for _, t := range tables {
		// Only "no triggers" is reused: a trigger dropped a moment ago must never be named, so a
		// table that had triggers is always looked up again.
		if e, ok := triggerCache[key(t)]; ok && !fresh && len(e.names) == 0 && now.Sub(e.at) < triggerCacheTTL {
			if len(e.names) > 0 {
				out[t] = e.names
			}
			continue
		}
		need = append(need, t)
	}
	triggerMu.Unlock()
	if len(need) == 0 {
		return out, nil
	}
	sort.Strings(need)
	e, err := c.entry(ctx, inst, st)
	if err != nil {
		return nil, err
	}
	pc, err := e.acquire(ctx, inst.Name)
	if err != nil {
		return nil, err
	}
	defer pc.Release()
	qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := pc.Query(qctx, triggersSQL, need)
	if err != nil {
		return nil, statementError(err, st.cfg)
	}
	found := map[string][]string{}
	for rows.Next() {
		var t, name string
		if err := rows.Scan(&t, &name); err != nil {
			rows.Close()
			return nil, statementError(err, st.cfg)
		}
		found[t] = append(found[t], name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, statementError(err, st.cfg)
	}
	triggerMu.Lock()
	for _, t := range need {
		triggerCache[key(t)] = triggerEntry{at: now, names: found[t]}
		if len(found[t]) > 0 {
			out[t] = found[t]
		}
	}
	triggerMu.Unlock()
	return out, nil
}
