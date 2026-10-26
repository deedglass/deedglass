//go:build !(js && wasm)

package scan

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Connector opens the scan's one connection (tests replace it). It must honour cfg's runtime
// parameters: they make the whole session read-only.
type Connector func(ctx context.Context, cfg *pgx.ConnConfig) (*pgx.Conn, error)

// The six catalog queries (S32 §3.13.3), exactly as sent: no user input ever reaches SQL. The
// read-only test asserts the server log holds these, BEGIN READ ONLY and ROLLBACK, nothing else.
const (
	QueryWho  = `SELECT current_user, current_database(), current_setting('server_version'), current_setting('wal_level')`
	QueryRole = `SELECT rolsuper, rolbypassrls, rolreplication, rolcreaterole, rolcreatedb
  FROM pg_catalog.pg_roles WHERE rolname = current_user`
	QueryTables = `SELECT c.oid, n.nspname, c.relname, c.reltuples::bigint, c.relrowsecurity, c.relforcerowsecurity, c.relreplident,
       has_table_privilege(c.oid, 'INSERT')   AS can_insert,
       has_table_privilege(c.oid, 'UPDATE') OR has_any_column_privilege(c.oid, 'UPDATE') AS can_update,
       has_table_privilege(c.oid, 'DELETE')   AS can_delete,
       has_table_privilege(c.oid, 'TRUNCATE') AS can_truncate,
       EXISTS (SELECT 1 FROM pg_catalog.pg_index i WHERE i.indrelid = c.oid AND i.indisprimary) AS has_pk
  FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
 WHERE c.relkind IN ('r','p') AND n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname !~ '^pg_'
 ORDER BY n.nspname, c.relname LIMIT 5000`
	QueryTriggers = `SELECT t.tgrelid, t.tgname, t.tgenabled, pg_catalog.pg_get_triggerdef(t.oid), p.prosrc
  FROM pg_catalog.pg_trigger t JOIN pg_catalog.pg_proc p ON p.oid = t.tgfoid
 WHERE NOT t.tgisinternal`
	QueryFKs = `SELECT conrelid, confrelid, confdeltype, confupdtype FROM pg_catalog.pg_constraint
 WHERE contype = 'f' AND (confdeltype IN ('c','n','d') OR confupdtype IN ('c','n','d'))`
	QuerySecDef = `SELECT p.oid::regprocedure::text, pg_catalog.pg_get_userbyid(p.proowner)
  FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
 WHERE p.prosecdef AND has_function_privilege(p.oid, 'EXECUTE')
   AND n.nspname NOT IN ('pg_catalog','information_schema') LIMIT 50`
)

// Queries lists the six, in the order they run.
var Queries = []string{QueryWho, QueryRole, QueryTables, QueryTriggers, QueryFKs, QuerySecDef}

// RuntimeParams make the session read-only and bounded before any query runs.
var RuntimeParams = map[string]string{
	"default_transaction_read_only":       "on",
	"statement_timeout":                   "2000",
	"lock_timeout":                        "500",
	"idle_in_transaction_session_timeout": "5000",
	"application_name":                    "deedglass-scan",
}

// sqlstateNames name the SQLSTATEs a scan is likely to meet (anything else shows its class).
var sqlstateNames = map[string]string{
	"28P01": "invalid_password", "28000": "invalid_authorization_specification", "3D000": "invalid_catalog_name",
	"08001": "sqlclient_unable_to_establish_sqlconnection", "08006": "connection_failure", "57P03": "cannot_connect_now",
	"42501": "insufficient_privilege", "25006": "read_only_sql_transaction", "57014": "query_canceled",
	"53300": "too_many_connections", "0A000": "feature_not_supported",
}

// ReduceError turns any error into "SQLSTATE name": never a host, user, password or message text.
func ReduceError(err error) (code, name string) {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		n := sqlstateNames[pe.Code]
		if n == "" {
			n = "class_" + strings.ToLower(pe.Code[:2])
		}
		return pe.Code, n
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "57014", "query_canceled"
	}
	return "08001", "sqlclient_unable_to_establish_sqlconnection"
}

// afterReads is a test hook: it runs inside the scan's own read-only transaction, after the six
// queries (the refused-write test). Always nil outside tests.
var afterReads func(context.Context, pgx.Tx) error

// readPostgres runs the database part: one READ ONLY transaction, the six catalog queries, ROLLBACK.
func readPostgres(ctx context.Context, o Options) (*Postgres, *PostgresFail) {
	fail := func(reason string, err error) *PostgresFail {
		c, n := ReduceError(err)
		return &PostgresFail{EnvVar: o.DSNVar, Reason: reason, SQLState: c, Class: n}
	}
	cfg, err := pgx.ParseConfig(o.DSN)
	if err != nil {
		return nil, &PostgresFail{EnvVar: o.DSNVar, Reason: "parse", SQLState: "08001", Class: "invalid_dsn"}
	}
	for k, v := range RuntimeParams {
		cfg.RuntimeParams[k] = v
	}
	cfg.ConnectTimeout = 5 * time.Second
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	connect := o.Connect
	if connect == nil {
		connect = func(ctx context.Context, c *pgx.ConnConfig) (*pgx.Conn, error) { return pgx.ConnectConfig(ctx, c) }
	}
	conn, err := connect(cctx, cfg)
	if err != nil {
		return nil, fail("connect", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))
	tx, err := conn.BeginTx(cctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fail("query", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // a read-only transaction; nothing to keep
	pg, err := catalogReads(cctx, tx)
	if err != nil {
		return nil, fail("query", err)
	}
	if afterReads != nil {
		if err := afterReads(cctx, tx); err != nil {
			return nil, fail("query", err)
		}
	}
	if err := tx.Rollback(cctx); err != nil {
		return nil, fail("query", err)
	}
	return pg, nil
}

type tableRow struct {
	oid       uint32
	t         Table
	replident string
}

// catalogReads runs the six queries in a transaction the caller opened READ ONLY.
func catalogReads(ctx context.Context, tx pgx.Tx) (*Postgres, error) {
	pg := &Postgres{Tables: []Table{}, SecurityDefiner: []SecDefFunc{}}
	if err := tx.QueryRow(ctx, QueryWho).Scan(&pg.Role, &pg.Database, &pg.ServerVersion, &pg.WALLevel); err != nil {
		return nil, err
	}
	a := &pg.RoleAttributes
	if err := tx.QueryRow(ctx, QueryRole).Scan(&a.Superuser, &a.BypassRLS, &a.Replication, &a.CreateRole, &a.CreateDB); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, QueryTables)
	if err != nil {
		return nil, err
	}
	var all []tableRow
	byOID := map[uint32]int{}
	for rows.Next() {
		var r tableRow
		var force bool
		var ins, upd, del, trunc bool
		var rid string
		if err := rows.Scan(&r.oid, &r.t.Schema, &r.t.Table, &r.t.EstRows, &r.t.RLS, &force, &rid, &ins, &upd, &del, &trunc, &r.t.HasPK); err != nil {
			rows.Close()
			return nil, err
		}
		r.replident = rid
		for _, c := range []struct {
			on   bool
			name string
		}{{ins, "INSERT"}, {upd, "UPDATE"}, {del, "DELETE"}, {trunc, "TRUNCATE"}} {
			if c.on {
				r.t.Can = append(r.t.Can, c.name)
			}
		}
		byOID[r.oid] = len(all)
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, QueryTriggers)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var rel uint32
		var name, enabled, def, src string
		if err := rows.Scan(&rel, &name, &enabled, &def, &src); err != nil {
			rows.Close()
			return nil, err
		}
		i, ok := byOID[rel]
		if !ok {
			continue
		}
		tr := Trigger{Name: name, Enabled: enabled != "D", NamesTables: tablesNamedIn(src, all[i].t.Schema)}
		all[i].t.Triggers = append(all[i].t.Triggers, tr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, QueryFKs)
	if err != nil {
		return nil, err
	}
	// referenced → referencing: deleting or updating a referenced row changes the referencing table.
	edges := map[uint32][]uint32{}
	for rows.Next() {
		var con, conf uint32
		var del, upd string
		if err := rows.Scan(&con, &conf, &del, &upd); err != nil {
			rows.Close()
			return nil, err
		}
		edges[conf] = append(edges[conf], con)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, QuerySecDef)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f SecDefFunc
		if err := rows.Scan(&f.Signature, &f.Owner); err != nil {
			rows.Close()
			return nil, err
		}
		pg.SecurityDefiner = append(pg.SecurityDefiner, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(pg.SecurityDefiner, func(i, j int) bool { return pg.SecurityDefiner[i].Signature < pg.SecurityDefiner[j].Signature })
	logical := pg.WALLevel == "logical"
	for _, r := range all {
		t := r.t
		if len(t.Can) == 0 {
			pg.TablesReadOnly++
			continue
		}
		t.CascadesTo = cascades(r.oid, edges, all, byOID)
		// S17's readiness rules: a primary key and a replica identity that carries it (default with
		// a key, an index, or full); L2 also needs wal_level = logical.
		t.UndoReady = t.HasPK && r.replident != "n" || r.replident == "f"
		t.L2Ready = t.UndoReady && logical
		if t.Triggers == nil {
			t.Triggers = []Trigger{}
		}
		pg.Tables = append(pg.Tables, t)
	}
	return pg, nil
}

// cascades is the transitive closure of FK edges from a table, by name (at most 5, then "+N").
func cascades(from uint32, edges map[uint32][]uint32, all []tableRow, byOID map[uint32]int) []string {
	seen := map[uint32]bool{from: true}
	queue := []uint32{from}
	var names []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nx := range edges[cur] {
			if seen[nx] {
				continue
			}
			seen[nx] = true
			queue = append(queue, nx)
			if i, ok := byOID[nx]; ok {
				names = append(names, all[i].t.Name())
			}
		}
	}
	sort.Strings(names)
	if len(names) > 5 {
		names = append(names[:5], "+"+itoa(len(names)-5))
	}
	if names == nil {
		names = []string{}
	}
	return names
}

var reBodyTable = regexp.MustCompile(`(?i)\b(?:UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+(?:ONLY\s+)?((?:"[^"]+"|[a-z_][a-z0-9_$]*)(?:\.(?:"[^"]+"|[a-z_][a-z0-9_$]*))?)`)

// tablesNamedIn finds the tables a function body names after UPDATE, INSERT INTO or DELETE FROM.
func tablesNamedIn(src, defaultSchema string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, m := range reBodyTable.FindAllStringSubmatch(src, -1) {
		n := strings.ReplaceAll(strings.ToLower(m[1]), `"`, "")
		if !strings.Contains(n, ".") {
			n = defaultSchema + "." + n
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
