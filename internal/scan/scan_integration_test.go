//go:build integration

package scan

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
)

// recorder is a pgx tracer: every SQL text the scan's connection sends.
type recorder struct{ sql []string }

func (r *recorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.sql = append(r.sql, d.SQL)
	return ctx
}
func (r *recorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func roleDSN(t *testing.T, admin *pgx.Conn, base, role, pw string, grant string) string {
	t.Helper()
	ctx := context.Background()
	_, _ = admin.Exec(ctx, "DROP OWNED BY "+role+" CASCADE")
	_, _ = admin.Exec(ctx, "DROP ROLE IF EXISTS "+role)
	if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD '"+pw+"'"); err != nil {
		t.Fatal(err)
	}
	for _, g := range strings.Split(grant, ";") {
		if strings.TrimSpace(g) != "" {
			if _, err := admin.Exec(ctx, g); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP OWNED BY "+role+" CASCADE")
		_, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
	u := strings.Replace(base, "://", "://"+role+":"+pw+"@", 1)
	if i := strings.Index(base, "@"); i > 0 {
		// base already has credentials: swap them
		u = base[:strings.Index(base, "://")+3] + role + ":" + pw + base[i:]
	}
	return u
}

// TestScanReadOnly: the six SELECTs between BEGIN READ ONLY and ROLLBACK, nothing else; a write in
// the scan's own transaction is refused by Postgres (25006) and changes nothing; the trigger, the
// cascade and the rank; severities by role; no canary in any output.
func TestScanReadOnly(t *testing.T) {
	adminURL := pgtest.AdminURL(t)
	d := pgtest.NewDB(t, adminURL, "deedglass_it_scan")
	ctx := context.Background()
	admin := d.Admin(t)
	for _, q := range []string{
		`CREATE SCHEMA acme_legacy`,
		`CREATE FUNCTION acme_legacy.ship_open_batch() RETURNS trigger LANGUAGE plpgsql AS $fn$ BEGIN UPDATE public.orders o SET status = 'shipped' WHERE o.id <> NEW.id; RETURN NULL; END $fn$`,
		`CREATE TRIGGER orders_ship_open_batch AFTER UPDATE OF status ON public.orders FOR EACH ROW WHEN (NEW.status = 'shipped') EXECUTE FUNCTION acme_legacy.ship_open_batch()`,
		`CREATE TABLE public.notes (body text)`,
		`ALTER TABLE public.notes OWNER TO deedglass_it_acme`,
		`ALTER TABLE public.order_items DROP CONSTRAINT order_items_order_id_fkey`,
		`ALTER TABLE public.order_items ADD FOREIGN KEY (order_id) REFERENCES public.orders (id) ON DELETE CASCADE`,
		`ANALYZE`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rw := roleDSN(t, admin, d.AdminURL, "deedglass_it_scan_rw", "canary-pass-7f3", "GRANT deedglass_it_acme TO deedglass_it_scan_rw;ALTER ROLE deedglass_it_scan_rw SET log_statement = 'all'")
	ro := roleDSN(t, admin, d.AdminURL, "deedglass_it_scan_ro", "canary-pass-8q1", "GRANT USAGE ON SCHEMA public TO deedglass_it_scan_ro;GRANT SELECT ON ALL TABLES IN SCHEMA public TO deedglass_it_scan_ro")

	root := t.TempDir()
	write(t, root, ".cursor/mcp.json", `{"mcpServers":{"pg":{"command":"npx","args":["-y","@modelcontextprotocol/server-postgres@0.6.2"]}}}`)
	rec := &recorder{}
	connect := func(ctx context.Context, c *pgx.ConnConfig) (*pgx.Conn, error) {
		c.Tracer = rec
		return pgx.ConnectConfig(ctx, c)
	}
	var sum1, sum2 string
	_ = admin.QueryRow(ctx, "SELECT md5(string_agg(id::text || status, ',' ORDER BY id)) FROM public.orders").Scan(&sum1)
	var writeErr error
	afterReads = func(ctx context.Context, tx pgx.Tx) error {
		_, writeErr = tx.Exec(ctx, "UPDATE public.orders SET status = status")
		return nil
	}
	t0 := time.Now()
	r, err := Scan(ctx, Options{Dir: root, DirLabel: "fixture", DSN: rw, DSNVar: "DEMO_DSN", Connect: connect})
	afterReads = nil
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scan with the database part: %s", time.Since(t0))
	var pe *pgconn.PgError
	if !errors.As(writeErr, &pe) || pe.Code != "25006" {
		t.Fatalf("the write inside the scan's transaction: %v", writeErr)
	}
	_ = admin.QueryRow(ctx, "SELECT md5(string_agg(id::text || status, ',' ORDER BY id)) FROM public.orders").Scan(&sum2)
	if sum1 != sum2 {
		t.Fatal("the orders changed")
	}
	// The statements the connection sent: begin read only, the six queries, the test's UPDATE (refused), rollback.
	var sent []string
	for _, s := range rec.sql {
		if s != "UPDATE public.orders SET status = status" {
			sent = append(sent, s)
		}
	}
	want := append([]string{"begin isolation level read committed read only"}, Queries...)
	want = append(want, "rollback")
	if len(sent) != len(want) {
		t.Fatalf("sent %d statements:\n%s", len(sent), strings.Join(sent, "\n---\n"))
	}
	for i := range want {
		if !strings.EqualFold(strings.TrimSpace(sent[i]), strings.TrimSpace(want[i])) && !(i == 0 && strings.Contains(strings.ToLower(sent[i]), "read only")) {
			t.Errorf("statement %d:\n got %s\nwant %s", i, sent[i], want[i])
		}
	}
	pg := r.Postgres
	if pg == nil || pg.Role != "deedglass_it_scan_rw" || pg.WALLevel == "" {
		t.Fatalf("%+v %+v", pg, r.PostgresError)
	}
	if pg.Tables[0].Name() != "public.orders" || !strings.Contains(r.Score.Deductions[len(r.Score.Deductions)-1].Detail+deductionText(r), "orders_ship_open_batch") {
		t.Fatalf("rank: %v / %+v", names(pg.Tables), r.Score.Deductions)
	}
	o := pg.Tables[0]
	if strings.Join(o.Triggers[0].NamesTables, ",") != "public.orders" || strings.Join(o.CascadesTo, ",") != "public.order_items" || !o.UndoReady {
		t.Fatalf("%+v", o)
	}
	var notes *Table
	for i := range pg.Tables {
		if pg.Tables[i].Table == "notes" {
			notes = &pg.Tables[i]
		}
	}
	if notes == nil || notes.HasPK || notes.UndoReady {
		t.Fatalf("notes: %+v", notes)
	}
	sev := map[string]string{}
	for _, f := range r.Findings {
		sev[f.ID] = f.Severity
	}
	if sev["archived_postgres_mcp"] != "high" || sev["security_definer_executable"] != "medium" || sev["no_primary_key_on_writable_table"] != "low" {
		t.Fatalf("%v", sev)
	}
	// A SELECT-only role: the archived server is low.
	r2, err := Scan(ctx, Options{Dir: root, DirLabel: "fixture", DSN: ro, DSNVar: "DEMO_DSN"})
	if err != nil || r2.Postgres == nil || len(r2.Postgres.Tables) != 0 {
		var tb []string
		if r2.Postgres != nil {
			for _, x := range r2.Postgres.Tables {
				tb = append(tb, x.Name()+":"+strings.Join(x.Can, "+"))
			}
		}
		t.Fatalf("%v %+v %v", err, r2.PostgresError, tb)
	}
	for _, f := range r2.Findings {
		if f.ID == "archived_postgres_mcp" && f.Severity != "low" {
			t.Fatalf("%+v", f)
		}
	}
	// A database that doesn't exist (the throwaway cluster trusts local connections, so a wrong
	// password would still connect): the SQLSTATE and its class, nothing else.
	bad := strings.Replace(rw, "/deedglass_it_scan", "/deedglass_it_scan_missing_canary-wrong", 1)
	r3, _ := Scan(ctx, Options{Dir: root, DirLabel: "fixture", DSN: bad, DSNVar: "DEMO_DSN"})
	pe3 := r3.PostgresError
	if pe3 == nil || len(pe3.SQLState) != 5 || pe3.Class == "" || strings.Contains(pe3.Class, "canary") {
		t.Fatalf("%+v", pe3)
	}
	t.Logf("a missing database: %s %s", pe3.SQLState, pe3.Class)
	if !strings.Contains(RenderText(r3, Style{}, 100, "h", ""), "Couldn't connect with the URL in DEMO_DSN ("+pe3.SQLState+" "+pe3.Class+"). The file scan was written. Run again when the database is reachable.") {
		t.Fatal("the connection-failed line")
	}
	for _, rep := range []*Report{r, r2, r3} {
		for _, out := range outputs(t, rep) {
			for _, c := range []string{"canary-pass", "canary-wrong", "postgres://", "postgresql://"} {
				if strings.Contains(out, c) {
					t.Fatalf("%q reached an output", c)
				}
			}
		}
	}
	if logPath := os.Getenv("DEEDGLASS_IT_PG_LOG"); logPath != "" {
		// The server's own record (log_statement=all on the scan's role): every statement of the
		// scan's backend, found by the PID of its BEGIN READ ONLY.
		time.Sleep(300 * time.Millisecond)
		b, _ := os.ReadFile(logPath)
		lines := strings.Split(string(b), "\n")
		pid := ""
		for _, l := range lines {
			if strings.HasSuffix(l, "statement: begin read only") {
				if i := strings.Index(l, "["); i >= 0 {
					pid = l[i : strings.Index(l, "]")+1]
				}
			}
		}
		if pid == "" {
			t.Fatal("log_statement didn't take effect: no BEGIN READ ONLY in the server log")
		}
		var stmts []string
		for _, l := range lines {
			switch {
			case strings.Contains(l, pid+" LOG:  statement: "):
				stmts = append(stmts, l[strings.Index(l, "statement: ")+len("statement: "):])
			case strings.Contains(l, pid+" LOG:  execute "):
				rest := l[strings.Index(l, " LOG:  execute ")+len(" LOG:  execute "):]
				stmts = append(stmts, strings.TrimSpace(rest[strings.Index(rest, ":")+1:]))
			}
		}
		for _, st := range stmts {
			u := strings.ToUpper(st)
			if !(u == "BEGIN READ ONLY" || strings.HasPrefix(u, "SELECT ") || u == "ROLLBACK" || st == "UPDATE public.orders SET status = status") {
				t.Errorf("the server log shows a statement that isn't a read: %s", st)
			}
		}
		t.Logf("server log, backend %s: %d statements: %s", pid, len(stmts), strings.Join(firstWords(stmts), " · "))
		if strings.Contains(string(b), "canary-pass-7f3") {
			t.Error("the password reached the server log")
		}
	}
}

func deductionText(r *Report) string {
	var b strings.Builder
	for _, d := range r.Score.Deductions {
		b.WriteString(d.Detail)
	}
	return b.String()
}

func names(ts []Table) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name())
	}
	return out
}

func outputs(t *testing.T, r *Report) []string {
	h, _ := RenderHTML(r)
	s, _, _ := RenderSARIF(r)
	link, _ := ShareLink(NewShare(r))
	return []string{RenderText(r, Style{}, 100, "h", ""), RenderMarkdown(r), h, string(s), link}
}

func firstWords(ss []string) []string {
	var out []string
	for _, s := range ss {
		f := strings.Fields(s)
		if len(f) > 3 {
			f = f[:3]
		}
		out = append(out, strings.Join(f, " "))
	}
	return out
}
