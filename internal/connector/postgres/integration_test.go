//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/connector/postgres/setup"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/vault"
)

// The S17 §6 integration suite, on a throwaway cluster (make integration; make matrix for 16/17).
// One database for the package, set up once under the cluster-wide role lock.

var (
	itDB     *pgtest.DB
	itUnlock func()
	itPlan   *setup.Plan
)

func TestMain(m *testing.M) {
	code := m.Run()
	if itUnlock != nil {
		itUnlock()
	}
	os.Exit(code)
}

var setupOnce sync.Once

// db sets the package's database up once (Lock, NewDB, Init) and returns it.
func db(t *testing.T) *pgtest.DB {
	t.Helper()
	admin := pgtest.AdminURL(t)
	setupOnce.Do(func() {
		unlock, err := pgtest.Lock(context.Background(), admin)
		if err != nil {
			t.Fatalf("lock: %v", err)
		}
		itUnlock = unlock
		itDB = pgtest.NewDB(t, admin, "deedglass_it_pgconn")
		itPlan = itDB.Init(t, pgtest.Desired())
	})
	if itDB == nil || itDB.AgentDSN == "" {
		t.Fatal("the package database isn't set up")
	}
	return itDB
}

// opener hands out a fixed DSN credential (the vault's role in production).
type opener struct{ fields map[string]string }

func (o opener) Open(_ context.Context, _ vault.Ref) (*vault.Secret, error) {
	return vault.NewSecretForTest("dsn", o.fields), nil
}

func devEnv() connector.Env {
	return connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
}

// inst builds an instance of the Acme config (with edits) over dsn.
func inst(t *testing.T, c *Connector, cfg string, dsn, witness string, env connector.Env) *connector.Instance {
	t.Helper()
	norm, errs := c.Validate(context.Background(), env, json.RawMessage(cfg))
	if len(errs) > 0 {
		t.Fatalf("config: %v", errs)
	}
	f := map[string]string{"dsn": dsn}
	if witness != "" {
		f["witness_dsn"] = witness
	}
	return connector.NewInstance(ids.New[ids.ConnectorKind](), ids.New[ids.OrgKind](), "postgres", Type, norm, setup.SHA256(string(norm)), env, opener{f})
}

func withCfg(edit func(m map[string]any)) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(pgtest.ConfigJSON), &m)
	if edit != nil {
		edit(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func run(t *testing.T, c *Connector, in *connector.Instance, tool string, args string) (connector.Result, error) {
	t.Helper()
	tools, err := c.Tools(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tools {
		if s.Name == tool {
			if err := connector.ValidateArgs(s.InputSchema, json.RawMessage(args)); err != nil {
				return connector.Result{}, err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return c.Execute(ctx, in, connector.Call{ActionID: ids.New[ids.ActionKind](), OrgID: in.OrgID, AgentID: ids.New[ids.AgentKind](),
				Tool: s, Qualified: "postgres." + tool, Args: json.RawMessage(args), Deadline: time.Now().Add(30 * time.Second)})
		}
	}
	t.Fatalf("no tool %s", tool)
	return connector.Result{}, nil
}

func code(err error) string {
	if ce := connector.AsError(err); ce != nil {
		return ce.Code
	}
	if err != nil {
		return "untyped: " + err.Error()
	}
	return ""
}

func TestInitAndCheck(t *testing.T) {
	d := db(t)
	if itPlan.Empty() {
		t.Fatal("the first init planned nothing")
	}
	// a second plan is empty
	conn := d.Admin(t)
	st, err := setup.Introspect(context.Background(), conn, pgtest.Desired().Want())
	if err != nil {
		t.Fatal(err)
	}
	if p := setup.Build(st, pgtest.Desired()); !p.Empty() {
		t.Fatalf("a second init isn't empty:\n%s", p.Body())
	}
	// SCRAM login works and the role is the agent
	ac, err := pgx.Connect(context.Background(), d.AgentDSN)
	if err != nil {
		t.Fatalf("SCRAM login: %v", err)
	}
	var user, method string
	_ = ac.QueryRow(context.Background(), "SELECT CURRENT_USER::text").Scan(&user)
	ac.Close(context.Background())
	if user != setup.RoleAgent {
		t.Fatalf("logged in as %s", user)
	}
	_ = conn.QueryRow(context.Background(), "SELECT substr(rolpassword, 1, 13) FROM pg_authid WHERE rolname = 'deedglass_agent'").Scan(&method)
	if method != "SCRAM-SHA-256" {
		t.Fatalf("stored password method %q", method)
	}
	// check: 0 failed
	c := New(nil)
	defer c.Close()
	r := RunChecks(context.Background(), CheckOptions{Config: mustCfg(t, pgtest.ConfigJSON), ConnectorID: "con_01k67c04kxevyex3pjyqe0t45d", Name: "postgres",
		AgentDSN: d.AgentDSN, WitnessDSN: d.WitnessDSN, Env: devEnv(), Probe: true})
	for _, ch := range r.Checks {
		t.Logf("%-5s %-22s %s", ch.Status, ch.ID, ch.Detail)
		if ch.Status == connector.CheckFail {
			t.Errorf("check %s failed: %s (%s)", ch.ID, ch.Detail, ch.Fix)
		}
	}
	if !r.Ready || !strings.HasPrefix(r.Line, "Ready for L2 · ") {
		t.Fatalf("readiness: %s", r.Line)
	}
	t.Log(r.Line)
}

func mustCfg(t *testing.T, s string) Config {
	t.Helper()
	c, errs := ParseConfig(json.RawMessage(s))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	return c
}

// TestNoPasswordInServerLog: init's statements with log_statement=all (for this session) never
// carry the generated password; only the verifier reaches Postgres.
func TestNoPasswordInServerLog(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	conn := d.Admin(t)
	if _, err := conn.Exec(ctx, "SET log_statement = 'all'"); err != nil {
		t.Fatal(err)
	}
	pw, _ := setup.GeneratePassword(nil)
	if err := setup.SetPasswords(ctx, conn, map[string]string{setup.RoleWitness: pw}); err != nil {
		t.Fatal(err)
	}
	// restore the package's witness password
	m := regexp.MustCompile(`deedglass_witness:([0-9A-Za-z]{32})@`).FindStringSubmatch(d.WitnessDSN)
	if err := setup.SetPasswords(ctx, conn, map[string]string{setup.RoleWitness: m[1]}); err != nil {
		t.Fatal(err)
	}
	logPath := os.Getenv("DEEDGLASS_IT_PG_LOG")
	if logPath == "" {
		t.Skip("DEEDGLASS_IT_PG_LOG names the server log in brew mode; docker mode can't read it")
	}
	time.Sleep(200 * time.Millisecond)
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "ALTER ROLE deedglass_witness PASSWORD 'SCRAM-SHA-256$4096:") {
		t.Fatal("the log doesn't show the statement; log_statement didn't take effect")
	}
	if strings.Contains(string(b), pw) || strings.Contains(string(b), m[1]) {
		t.Fatal("a generated password reached the server log")
	}
}

// TestOneTransaction: the ledger insert, the deedglass message and the change decode under one xid,
// equal to ledger.xid8 (S17 §6, acceptance).
func TestOneTransaction(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	conn := d.Admin(t)
	slot := "deedglass_it_peek"
	_, _ = conn.Exec(ctx, "SELECT pg_drop_replication_slot($1) FROM pg_replication_slots WHERE slot_name = $1", slot)
	if _, err := conn.Exec(ctx, "SELECT pg_create_logical_replication_slot($1, 'test_decoding')", slot); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "SELECT pg_drop_replication_slot($1)", slot)
	c := New(nil)
	defer c.Close()
	in := inst(t, c, pgtest.ConfigJSON, d.AgentDSN, d.WitnessDSN, devEnv())
	res, err := run(t, c, in, ToolExecute, `{"sql":"UPDATE orders SET status = 'shipped' WHERE id = $1","params":[42]}`)
	if err != nil {
		t.Fatal(err)
	}
	var out executeOutput
	_ = json.Unmarshal(res.Output, &out)
	if out.Command != "UPDATE" || out.RowsAffected != 1 || out.Returning != nil {
		t.Fatalf("output %s", res.Output)
	}
	if res.Evidence["xid8"] == "" || res.Evidence["marker_lsn"] == "" || !strings.HasPrefix(res.Evidence["sql_digest"], "sha256:") || res.Evidence["command_tag"] != "UPDATE 1" {
		t.Fatalf("evidence %v", res.Evidence)
	}
	if res.Meta["rows_affected"] != int64(1) {
		t.Fatalf("meta %v", res.Meta)
	}
	var ledgerXID, act string
	if err := conn.QueryRow(ctx, "SELECT xid8::text, action_id FROM deedglass.ledger ORDER BY created_at DESC LIMIT 1").Scan(&ledgerXID, &act); err != nil {
		t.Fatal(err)
	}
	if ledgerXID != res.Evidence["xid8"] {
		t.Fatalf("ledger xid8 %s, evidence %s", ledgerXID, res.Evidence["xid8"])
	}
	rows, err := conn.Query(ctx, "SELECT xid::text, data FROM pg_logical_slot_get_changes($1, NULL, NULL, 'include-xids', '1')", slot)
	if err != nil {
		t.Fatal(err)
	}
	type change struct{ xid, data string }
	var changes []change
	for rows.Next() {
		var ch change
		_ = rows.Scan(&ch.xid, &ch.data)
		changes = append(changes, ch)
	}
	rows.Close()
	var seen []string
	xids := map[string]bool{}
	for _, ch := range changes {
		t.Logf("%s  %s", ch.xid, clip(ch.data))
		switch {
		case strings.HasPrefix(ch.data, "table deedglass.ledger: INSERT"):
			seen = append(seen, "ledger")
			xids[ch.xid] = true
		case strings.HasPrefix(ch.data, "message: transactional: 1 prefix: deedglass") && strings.Contains(ch.data, "content:"+act):
			seen = append(seen, "message")
			xids[ch.xid] = true
		case strings.HasPrefix(ch.data, "table public.orders: UPDATE"):
			seen = append(seen, "update")
			xids[ch.xid] = true
		}
	}
	if strings.Join(seen, ",") != "ledger,message,update" || len(xids) != 1 || !xids[ledgerXID] {
		t.Fatalf("decoded %v under xids %v, want ledger,message,update under %s", seen, xids, ledgerXID)
	}
	var first, last string
	if len(changes) > 0 {
		first, last = changes[0].data, changes[len(changes)-1].data
	}
	if !strings.HasPrefix(first, "BEGIN") || !strings.HasPrefix(last, "COMMIT") {
		t.Fatalf("not one transaction: %q … %q", first, last)
	}
}

func TestQueryAndRowCap(t *testing.T) {
	d := db(t)
	c := New(nil)
	defer c.Close()
	in := inst(t, c, pgtest.ConfigJSON, d.AgentDSN, "", devEnv())
	res, err := run(t, c, in, ToolQuery, `{"sql":"SELECT id, status FROM orders ORDER BY id DESC LIMIT 5"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out queryOutput
	_ = json.Unmarshal(res.Output, &out)
	if out.RowCount != 5 || out.Truncated || len(out.Columns) != 2 || out.Columns[0].Type != "int4" || string(out.Rows[0]) != `[60,"paid"]` {
		t.Fatalf("query: %s", res.Output)
	}
	// 600 rows with row_limit 500: 500 rows, truncated, and the next query on the pool works
	small := inst(t, c, withCfg(func(m map[string]any) { m["max_connections"] = 1 }), d.AgentDSN, "", devEnv())
	res, err = run(t, c, small, ToolQuery, `{"sql":"SELECT * FROM wide ORDER BY id"}`)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.RowCount != 500 || !out.Truncated || len(out.Rows) != 500 {
		t.Fatalf("row cap: %d rows, truncated %v", out.RowCount, out.Truncated)
	}
	if _, err := run(t, c, small, ToolQuery, `{"sql":"SELECT 1 AS one"}`); err != nil {
		t.Fatalf("after a truncated read: %v", err)
	}
	// limit below row_limit
	res, _ = run(t, c, in, ToolQuery, `{"sql":"SELECT id FROM wide","limit":3}`)
	_ = json.Unmarshal(res.Output, &out)
	if out.RowCount != 3 || !out.Truncated {
		t.Fatalf("limit 3: %s", res.Output)
	}
	// the byte cap
	tiny := inst(t, c, withCfg(func(m map[string]any) { m["max_result_bytes"] = 1024 }), d.AgentDSN, "", devEnv())
	res, _ = run(t, c, tiny, ToolQuery, `{"sql":"SELECT label FROM wide ORDER BY id"}`)
	_ = json.Unmarshal(res.Output, &out)
	if !out.Truncated || out.RowCount == 0 || out.RowCount > 100 {
		t.Fatalf("byte cap: %d rows, truncated %v", out.RowCount, out.Truncated)
	}
	// query never commits: a write function through a SELECT is refused by Postgres (25006)
	c.hooks.skipAdmission = true
	_, err = run(t, c, in, ToolQuery, `{"sql":"WITH d AS (DELETE FROM refunds RETURNING *) SELECT count(*) FROM d"}`)
	c.hooks.skipAdmission = false
	if code(err) != CodeStatementRefused || !strings.Contains(connector.AsError(err).AgentMessage, "read-only") {
		t.Fatalf("CTE delete through query: %v", err)
	}
}

func TestValuesLive(t *testing.T) {
	d := db(t)
	c := New(nil)
	defer c.Close()
	in := inst(t, c, pgtest.ConfigJSON, d.AgentDSN, "", devEnv())
	res, err := run(t, c, in, ToolQuery, `{"sql":"SELECT 42::int2 AS a, 9007199254740992::int8 AS b, 0.1::numeric AS c, 'NaN'::float8 AS d, true AS e, decode('dead', 'hex') AS f, '2026-10-06'::date AS g, '2026-10-06 11:12:00.123456+00'::timestamptz AS h, '1 day 02:00:00'::interval AS i, '{\"x\":1}'::jsonb AS j, ARRAY[1,NULL,3] AS k, NULL::text AS l, $1::text AS m","params":["p"]}`)
	if err != nil {
		t.Fatal(err)
	}
	var out queryOutput
	_ = json.Unmarshal(res.Output, &out)
	want := `[42,"9007199254740992","0.1","NaN",true,"\\xdead","2026-10-06","2026-10-06T11:12:00.123456Z","1 day 02:00:00",{"x":1},[1,null,3],null,"p"]`
	if len(out.Rows) != 1 || string(out.Rows[0]) != want {
		t.Fatalf("values\n got %s\nwant %s", out.Rows, want)
	}
}

func TestRowsCapRollsBack(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	conn := d.Admin(t)
	c := New(nil)
	defer c.Close()
	in := inst(t, c, pgtest.ConfigJSON, d.AgentDSN, "", devEnv())
	var before int
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM deedglass.ledger").Scan(&before)
	_, err := run(t, c, in, ToolExecute, `{"sql":"UPDATE bulk SET flag = true WHERE id > $1","params":[0]}`)
	ce := connector.AsError(err)
	if ce == nil || ce.Code != CodeRowLimitExceeded || ce.AgentMessage != "The statement changed 1,204 rows, over this connector's limit of 1,000. Nothing was committed. Narrow the WHERE clause." {
		t.Fatalf("rows cap: %v", err)
	}
	if ce.Kind != connector.KindToolError {
		t.Fatalf("kind %s", ce.Kind)
	}
	var flagged, after int
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM bulk WHERE flag").Scan(&flagged)
	_ = conn.QueryRow(ctx, "SELECT count(*) FROM deedglass.ledger").Scan(&after)
	if flagged != 0 || after != before {
		t.Fatalf("something committed: %d flagged, ledger %d → %d", flagged, before, after)
	}
}

func TestTimeouts(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	c := New(nil)
	defer c.Close()
	in := inst(t, c, withCfg(func(m map[string]any) { m["statement_timeout_ms"] = 1000; m["lock_timeout_ms"] = 500 }), d.AgentDSN, "", devEnv())
	start := time.Now()
	_, err := run(t, c, in, ToolQuery, `{"sql":"SELECT pg_sleep(10)"}`)
	if code(err) != connector.CodeTimeout || time.Since(start) > 5*time.Second {
		t.Fatalf("statement timeout: %v after %s", err, time.Since(start))
	}
	// a row lock held elsewhere
	conn := d.Admin(t)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM orders WHERE id = 7 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, c, in, ToolExecute, `{"sql":"UPDATE orders SET total_cents = total_cents + 1 WHERE id = $1","params":[7]}`)
	_ = tx.Rollback(ctx)
	ce := connector.AsError(err)
	if ce == nil || ce.Code != connector.CodeTimeout || !strings.Contains(ce.AgentMessage, "row lock") || ce.Kind != connector.KindToolError {
		t.Fatalf("lock timeout: %v", err)
	}
}

// TestSessionReset: a session setting that slipped past admission doesn't reach the next action.
func TestSessionReset(t *testing.T) {
	d := db(t)
	c := New(nil)
	defer c.Close()
	in := inst(t, c, withCfg(func(m map[string]any) { m["max_connections"] = 1 }), d.AgentDSN, "", devEnv())
	c.hooks.skipAdmission = true
	if _, err := run(t, c, in, ToolQuery, `{"sql":"SELECT set_config('search_path', 'evil', false), set_config('application_name', 'evil', false)"}`); err != nil {
		t.Fatal(err)
	}
	c.hooks.skipAdmission = false
	// AfterRelease's DISCARD ALL runs off the request path; with one connection, the pool hands
	// it out only after the reset.
	var out queryOutput
	res, err := run(t, c, in, ToolQuery, `{"sql":"SELECT current_setting('search_path') AS sp, current_setting('application_name') AS app"}`)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(res.Output, &out)
	if string(out.Rows[0]) != `["\"public\", pg_temp","deedglass"]` {
		t.Fatalf("the next action saw %s", out.Rows[0])
	}
}

// TestStackedStatements: even with admission bypassed, the extended protocol runs one statement.
func TestStackedStatements(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	conn := d.Admin(t)
	c := New(nil)
	defer c.Close()
	in := inst(t, c, pgtest.ConfigJSON, d.AgentDSN, "", devEnv())
	c.hooks.skipAdmission = true
	defer func() { c.hooks.skipAdmission = false }()
	for _, sql := range []string{
		"UPDATE orders SET status = 'cancelled' WHERE id = 3; DROP TABLE public.wide",
		"SELECT 1; COMMIT; DROP TABLE public.wide",
	} {
		args, _ := json.Marshal(map[string]any{"sql": sql})
		_, err := run(t, c, in, ToolExecute, string(args))
		if code(err) != CodeStatementRefused {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var n int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM public.wide").Scan(&n); err != nil || n != 600 {
		t.Fatalf("wide: %d %v", n, err)
	}
	var st string
	_ = conn.QueryRow(ctx, "SELECT status FROM orders WHERE id = 3").Scan(&st)
	if st != "paid" {
		t.Fatalf("order 3 is %s", st)
	}
	// With admission on: refused before the database.
	c.hooks.skipAdmission = false
	if _, err := run(t, c, in, ToolExecute, `{"sql":"SELECT 1; DROP TABLE orders"}`); code(err) != CodeStatementRefused {
		t.Fatalf("admission: %v", err)
	}
}

func TestCallAndReturning(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	conn := d.Admin(t)
	c := New(nil)
	defer c.Close()
	in := inst(t, c, pgtest.ConfigJSON, d.AgentDSN, "", devEnv())
	res, err := run(t, c, in, ToolCall, `{"function":"public.ship_order","args":{"p_order_id":11}}`)
	if err != nil {
		t.Fatal(err)
	}
	var out executeOutput
	_ = json.Unmarshal(res.Output, &out)
	if out.Command != "SELECT" || out.Returning == nil || string(out.Returning.Rows[0]) != `[11,"shipped"]` {
		t.Fatalf("call: %s", res.Output)
	}
	var lx string
	if err := conn.QueryRow(ctx, "SELECT xid8::text FROM deedglass.ledger WHERE sql_digest = $1", res.Evidence["sql_digest"]).Scan(&lx); err != nil || lx != res.Evidence["xid8"] {
		t.Fatalf("ledger row for the call: %s %v", lx, err)
	}
	var xmin string
	_ = conn.QueryRow(ctx, "SELECT xmin::text FROM orders WHERE id = 11").Scan(&xmin)
	if !strings.HasSuffix(lx, xmin) && lx != xmin {
		// xid8 is the 64-bit form; xmin is its low 32 bits. On a fresh cluster they are equal.
		t.Fatalf("the change's xmin %s isn't the ledger's xid %s", xmin, lx)
	}
	// INSERT … RETURNING through a serial sequence
	res, err = run(t, c, in, ToolExecute, `{"sql":"INSERT INTO refunds (order_id, amount_cents, reason) VALUES ($1, $2, $3) RETURNING id, amount_cents","params":[12, 500, "damaged"]}`)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Command != "INSERT" || out.RowsAffected != 1 || out.Returning == nil || out.Returning.RowCount != 1 {
		t.Fatalf("insert returning: %s", res.Output)
	}
	// a constraint: the name only, nothing committed
	_, err = run(t, c, in, ToolExecute, `{"sql":"UPDATE orders SET status = $1 WHERE id = $2","params":["lost", 13]}`)
	ce := connector.AsError(err)
	if ce == nil || !strings.Contains(ce.AgentMessage, "orders_status_check") || ce.Kind != connector.KindToolError {
		t.Fatalf("constraint: %v", err)
	}
	// a table the agent can't write
	_, err = run(t, c, in, ToolExecute, `{"sql":"UPDATE products SET price_cents = 1 WHERE id = $1","params":[1]}`)
	if ce := connector.AsError(err); ce == nil || !strings.Contains(ce.AgentMessage, "42501") {
		t.Fatalf("permission: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	d := db(t)
	c := New(nil)
	defer c.Close()
	in := inst(t, c, pgtest.ConfigJSON, d.AgentDSN, "", devEnv())
	res, err := run(t, c, in, ToolDescribe, `{"table":"orders"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tables []describeTable `json:"tables"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if len(out.Tables) != 1 || out.Tables[0].Table != "orders" || !out.Tables[0].Writable || !out.Tables[0].Watched ||
		strings.Join(out.Tables[0].PrimaryKey, ",") != "id" || len(out.Tables[0].Columns) != 6 {
		t.Fatalf("describe: %s", res.Output)
	}
	res, _ = run(t, c, in, ToolDescribe, `{}`)
	_ = json.Unmarshal(res.Output, &out)
	if len(out.Tables) != 7 {
		t.Fatalf("describe all: %d tables", len(out.Tables))
	}
	for _, tb := range out.Tables {
		if tb.Schema == "deedglass" {
			t.Fatal("describe shows schema deedglass")
		}
	}
}

// TestUnsafeRole: a superuser DSN is refused in prod (database_role_unsafe), warned in dev.
func TestUnsafeRole(t *testing.T) {
	d := db(t)
	c := New(nil)
	defer c.Close()
	prod := connector.Env{Name: "prod", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}} // prod rules, a loopback-capable door for the test
	in := inst(t, c, pgtest.ConfigJSON, d.AdminURL, "", prod)
	_, err := run(t, c, in, ToolQuery, `{"sql":"SELECT 1"}`)
	ce := connector.AsError(err)
	if ce == nil || ce.Code != CodeDatabaseRoleUnsafe || ce.AgentMessage != msgRoleUnsafe {
		t.Fatalf("prod superuser: %v", err)
	}
	if ce.Kind != connector.KindConfig {
		t.Fatalf("kind %s (nothing ran: config)", ce.Kind)
	}
	dev := inst(t, c, pgtest.ConfigJSON, d.AdminURL, "", devEnv())
	if _, err := run(t, c, dev, ToolQuery, `{"sql":"SELECT 1"}`); err != nil {
		t.Fatalf("dev superuser should run with a warning: %v", err)
	}
	r := RunChecks(context.Background(), CheckOptions{Config: mustCfg(t, pgtest.ConfigJSON), Name: "postgres", AgentDSN: d.AdminURL, Env: devEnv()})
	if r.Checks[0].ID != "connection" || r.Checks[0].Status != connector.CheckFail || !strings.Contains(r.Checks[0].Detail, "SUPERUSER") {
		t.Fatalf("check with a superuser DSN: %+v", r.Checks[0])
	}
}

// TestCheckMatrixLive builds failing states on the throwaway database and reads the checks.
func TestCheckMatrixLive(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	conn := d.Admin(t)
	cfg := mustCfg(t, pgtest.ConfigJSON)
	checkFor := func(id string) connector.Check {
		r := RunChecks(ctx, CheckOptions{Config: cfg, ConnectorID: "con_01k67c04kxevyex3pjyqe0t45d", Name: "postgres", AgentDSN: d.AgentDSN, WitnessDSN: d.WitnessDSN, Env: devEnv()})
		for _, c := range r.Checks {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("no check %s", id)
		return connector.Check{}
	}
	restore := func() {
		t.Helper()
		st, err := setup.Introspect(ctx, conn, pgtest.Desired().Want())
		if err != nil {
			t.Fatal(err)
		}
		p := setup.Build(st, pgtest.Desired())
		if err := setup.Apply(ctx, conn, p, pgtest.Desired(), nil); err != nil {
			t.Fatalf("restore: %v", err)
		}
	}
	cases := []struct {
		name, sql, id string
		status        connector.CheckStatus
		detail        string
		undo          string
	}{
		{"ledger replica identity", "ALTER TABLE deedglass.ledger REPLICA IDENTITY DEFAULT", "schema", connector.CheckFail, "replica identity isn't full", ""},
		{"watched table out of the publication", "ALTER PUBLICATION deedglass_pub DROP TABLE public.refunds", "publication", connector.CheckFail, "missing public.refunds", ""},
		{"row filter", "ALTER PUBLICATION deedglass_pub SET TABLE deedglass.ledger, public.bulk, public.customers, public.orders WHERE (id > 0), public.refunds", "publication", connector.CheckFail, "row filters", ""},
		{"agent superuser", "ALTER ROLE deedglass_agent SUPERUSER", "agent_role", connector.CheckFail, "SUPERUSER", "ALTER ROLE deedglass_agent NOSUPERUSER"},
		{"witness without replication", "ALTER ROLE deedglass_witness NOREPLICATION", "witness_role", connector.CheckFail, "no replication", "ALTER ROLE deedglass_witness REPLICATION"},
		{"begin_action replaced", `CREATE OR REPLACE FUNCTION deedglass.begin_action(p_action_id text, p_org_id text, p_agent_id text, p_intent_hash text, p_sql_digest text)
			RETURNS TABLE (xid8 pg_catalog.xid8, marker_lsn pg_catalog.pg_lsn) LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, pg_temp SET TimeZone = 'UTC'
			AS $fn$ BEGIN RETURN QUERY SELECT pg_catalog.pg_current_xact_id(), '0/0'::pg_catalog.pg_lsn; END $fn$`, "begin_action", connector.CheckFail, "body changed: expected " + setup.ShortHash(setup.BeginActionBodySHA256), ""},
		{"unlogged watched table", "ALTER PUBLICATION deedglass_pub DROP TABLE public.bulk; ALTER TABLE public.bulk SET UNLOGGED", "witness.watched_tables", connector.CheckFail, "UNLOGGED", "ALTER TABLE public.bulk SET LOGGED"},
		{"trigger on orders", `CREATE FUNCTION public.orders_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;
			CREATE TRIGGER orders_audit BEFORE UPDATE ON public.orders FOR EACH ROW EXECUTE FUNCTION public.orders_audit()`, "witness.watched_tables", connector.CheckWarn, "orders_audit",
			"DROP TRIGGER orders_audit ON public.orders; DROP FUNCTION public.orders_audit()"},
		{"table without a primary key", "ALTER TABLE public.bulk DROP CONSTRAINT bulk_pkey", "witness.watched_tables", connector.CheckWarn, "public.bulk has no primary key", "ALTER TABLE public.bulk ADD PRIMARY KEY (id)"},
	}
	for _, cs := range cases {
		t.Run(cs.name, func(t *testing.T) {
			if _, err := conn.Exec(ctx, cs.sql); err != nil {
				t.Fatalf("building the state: %v", err)
			}
			got := checkFor(cs.id)
			t.Logf("%s %s: %s · fix: %s", got.Status, got.ID, got.Detail, got.Fix)
			if cs.undo != "" {
				if _, err := conn.Exec(ctx, cs.undo); err != nil {
					t.Fatalf("undo: %v", err)
				}
			}
			restore()
			if got.Status != cs.status || !strings.Contains(got.Detail, cs.detail) {
				t.Fatalf("%s: %s %q, want %s with %q", cs.id, got.Status, got.Detail, cs.status, cs.detail)
			}
			if got.Status == connector.CheckFail && got.Fix == "" {
				t.Fatal("a failing check without a fix")
			}
		})
	}
	// everything restored: no failures
	r := RunChecks(ctx, CheckOptions{Config: cfg, Name: "postgres", AgentDSN: d.AgentDSN, WitnessDSN: d.WitnessDSN, Env: devEnv()})
	if !r.Ready {
		t.Fatalf("after the matrix: %s", r.Line)
	}
}

// TestPlanChangedRefused: a plan applied after the database changed is refused.
func TestPlanChangedRefused(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	conn := d.Admin(t)
	if _, err := conn.Exec(ctx, "ALTER TABLE public.refunds REPLICA IDENTITY DEFAULT"); err != nil {
		t.Fatal(err)
	}
	st, _ := setup.Introspect(ctx, conn, pgtest.Desired().Want())
	p := setup.Build(st, pgtest.Desired())
	if p.Empty() {
		t.Fatal("expected a plan")
	}
	if _, err := conn.Exec(ctx, "ALTER TABLE public.refunds REPLICA IDENTITY FULL"); err != nil {
		t.Fatal(err)
	}
	if err := setup.Apply(ctx, conn, p, pgtest.Desired(), nil); !errors.Is(err, setup.ErrPlanChanged) {
		t.Fatalf("stale plan: %v", err)
	}
}

func TestMissingSetup(t *testing.T) {
	admin := pgtest.AdminURL(t)
	d := db(t)
	bare := pgtest.NewDB(t, admin, "deedglass_it_pgbare")
	c := New(nil)
	defer c.Close()
	dsn := strings.Replace(d.AgentDSN, "/"+d.Name+"?", "/"+bare.Name+"?", 1)
	in := inst(t, c, pgtest.ConfigJSON, dsn, "", devEnv())
	_, err := run(t, c, in, ToolExecute, `{"sql":"UPDATE orders SET status = 'paid' WHERE id = $1","params":[1]}`)
	if code(err) != CodeDatabaseNotReady {
		t.Fatalf("no setup: %v", err)
	}
	if !strings.Contains(fmt.Sprint(connector.AsError(err).AgentMessage), "deedglass connector postgres init --connector postgres") {
		t.Fatalf("message: %s", connector.AsError(err).AgentMessage)
	}
}
