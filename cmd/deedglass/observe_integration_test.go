//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/licence"
	"github.com/deedglass/deedglass/internal/observe"
	"github.com/deedglass/deedglass/internal/observeapi"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// observePolicy is the undo policy plus the session undo's rule (S50): a person's session undo
// waits for a countersign and is witnessed at L2.
const observePolicy = undoPolicy + `  - id: undo-session-needs-approval
    match: { tool: "*.undo_session", connector_type: postgres }
    decision: require_approval
    reason: Undoing a whole session changes data again, so a human confirms it first.
    approval: { approvers: [role:admin], required: 1, expires_in: 30m }
    verify: L2
`

// observeCanary is a row value that must never reach a record: the seal holds ids, counts and digests.
const observeCanary = "canary-row-value-s50-7731"

// TestObserveChildHelper is the agent observe run starts in TestObserveLive (the helper-process
// pattern): it writes to the watched database, checks it sees no DEEDGLASS_* variable, and exits
// with the code it was given.
func TestObserveChildHelper(t *testing.T) {
	dsn := os.Getenv("S50_CHILD_DSN")
	if dsn == "" {
		t.Skip("run by TestObserveLive")
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "DEEDGLASS_") {
			os.Exit(9)
		}
	}
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		os.Exit(8)
	}
	for _, q := range strings.Split(os.Getenv("S50_CHILD_SQL"), ";;") {
		if q = strings.TrimSpace(q); q != "" {
			if _, err := c.Exec(context.Background(), q); err != nil {
				os.Exit(7)
			}
		}
	}
	_ = c.Close(context.Background())
	code := 0
	fmt.Sscanf(os.Getenv("S50_CHILD_EXIT"), "%d", &code)
	os.Exit(code)
}

// TestObserveLive is S50's acceptance on a real server, a real decoder and a real Postgres: a
// watched session sealed with what Postgres committed (301 rows caught), the seal holding no row
// values, the free undo SQL restoring the tables exactly, a second preview blocked by conflicts,
// observe run passing exit codes through with DEEDGLASS_* kept from the child, pro_required without
// a licence, a coding agent's unbounded UPDATE through psql caught by observe run --undo-sql with the
// saved SQL putting the table back (FEAT-04), then (with a test Pro licence) an excluded
// transaction, a one-step undo countersigned by another person, its Check reading Matches and the
// session undone, and agent keys refused.
func TestObserveLive(t *testing.T) {
	admin := pgtest.AdminURL(t)
	unlock, err := pgtest.Lock(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cust := pgtest.NewDB(t, admin, "deedglass_it_observelive")
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 50)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "debug", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true",
		"DEEDGLASS_IT_YAML": "decoder:\n  heartbeat_interval: 1s\n"}
	var outputs []string
	start := func(extra map[string]string, migrate bool) (*served, string) {
		t.Helper()
		se := map[string]string{}
		for k, v := range env {
			se[k] = v
		}
		for k, v := range extra {
			se[k] = v
		}
		args := []string{}
		if migrate {
			args = append(args, "--migrate")
		}
		s := startServe(t, se, args...)
		select {
		case a := <-s.addr:
			return s, "http://" + a.String()
		case c := <-s.code:
			t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
		case <-time.After(30 * time.Second):
			t.Fatal("serve never listened")
		}
		return nil, ""
	}
	s, base := start(nil, true)
	running := true
	stop := func() {
		if running {
			s.sigs <- syscall.SIGTERM
			s.wait(t)
			outputs = append(outputs, s.out.String(), s.err.String())
			running = false
		}
	}
	defer func() {
		stop()
		c, err := pgx.Connect(context.Background(), cust.AdminURL)
		if err == nil {
			_, _ = c.Exec(context.Background(), `SELECT pg_catalog.pg_drop_replication_slot(slot_name) FROM pg_catalog.pg_replication_slots
			    WHERE database = pg_catalog.current_database() AND NOT active`)
			_ = c.Close(context.Background())
		}
	}()
	custSQL := func(q string, args ...any) {
		t.Helper()
		c, err := pgx.Connect(context.Background(), cust.AdminURL)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(context.Background())
		if _, err := c.Exec(context.Background(), q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	snapshot := func() string {
		t.Helper()
		c, err := pgx.Connect(context.Background(), cust.AdminURL)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(context.Background())
		var out string
		if err := c.QueryRow(context.Background(), `SELECT
		    (SELECT string_agg(o::text, E'\n' ORDER BY id) FROM public.orders AS o) || E'\n--\n' ||
		    (SELECT string_agg(b::text, E'\n' ORDER BY id) FROM public.bulk AS b) || E'\n--\n' ||
		    COALESCE((SELECT string_agg(r.id || ':' || r.order_id || ':' || r.amount_cents || ':' || r.reason, E'\n' ORDER BY id) FROM public.refunds AS r), '')`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	run := func(args ...string) result {
		t.Helper()
		r := cli(t, env, false, args...)
		outputs = append(outputs, r.stdout, r.stderr)
		return r
	}
	r := run("org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		Org      struct{ ID string } `json:"org"`
		AdminKey struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
		} `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	env["DEEDGLASS_API_KEY"] = created.AdminKey.Secret
	{
		c := e.Admin(t)
		var priya string
		if err := c.QueryRow(context.Background(), `SELECT id FROM deedglass.users WHERE email = 'priya@acme.test'`).Scan(&priya); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(context.Background(), `UPDATE deedglass.api_keys SET created_by = $1 WHERE org_id = $2 AND agent_id IS NULL`, priya, created.Org.ID); err != nil {
			t.Fatal(err)
		}
		omar := ids.New[ids.UserKind]()
		if _, err := c.Exec(context.Background(), `INSERT INTO deedglass.users (id, email, display_name, status) VALUES ($1, 'omar@acme.test', 'Omar', 'active')`, omar); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(context.Background(), `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
		    VALUES ($1, $2, $3, 'admin', 'active', now())`, ids.New[ids.MembershipKind](), created.Org.ID, omar); err != nil {
			t.Fatal(err)
		}
	}
	pf := filepath.Join(t.TempDir(), "postgres-undo.yaml")
	_ = os.WriteFile(pf, []byte(strings.Replace(observePolicy, "name: postgres-undo", "name: postgres-observe", 1)), 0o600)
	if r = run("policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run("policy", "activate", "postgres-observe", "--version", "1", "--mode", "enforce", "--reason", "S50 acceptance", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	call := func(key, method, path, body string) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequestWithContext(context.Background(), method, base+path, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		outputs = append(outputs, string(b))
		return res.StatusCode, string(b)
	}
	adminKey := created.AdminKey.Secret
	code, body := call(adminKey, "POST", "/v1/agents", `{"name":"orders-bot"}`)
	var agent struct{ ID string }
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("%d %s", code, body)
	}
	code, body = call(adminKey, "POST", "/v1/agents/"+agent.ID+"/keys", `{"scopes":["actions:execute","actions:read","deeds:read"]}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
	// The connector, first with observe mode off.
	cfgOff := pgtest.ConfigJSON
	cf := filepath.Join(t.TempDir(), "postgres.json")
	_ = os.WriteFile(cf, []byte(`{"type":"postgres","name":"postgres","description":"Acme (test)","config":`+cfgOff+`}`), 0o600)
	if r = run("connector", "create", "--config", cf); r.code != 0 {
		t.Fatalf("create: %+v", r)
	}
	env["DEEDGLASS_INIT_DSN"] = cust.AdminURL
	if r = run("connector", "postgres", "init", "--connector", "postgres", "--plan-dir", t.TempDir(), "--apply", "--rotate-passwords"); r.code != 0 {
		t.Fatalf("init: %+v", r)
	}
	delete(env, "DEEDGLASS_INIT_DSN")
	// Observe mode off: observe_not_enabled, with the next step.
	r = run("observe", "start", "--connector", "postgres")
	if r.code != 1 || !strings.Contains(r.stderr, "Observe mode is off for postgres") || !strings.Contains(r.stderr, "witness.observe.enabled: true") {
		t.Fatalf("observe off: %+v", r)
	}
	// Turn it on (PATCH the connector's config).
	cfgOn := strings.Replace(cfgOff, `"sensitive_columns":["public.customers.email"]}`, `"sensitive_columns":["public.customers.email"],"observe":{"enabled":true,"caught_rows":50}}`, 1)
	if cfgOn == cfgOff {
		t.Fatal("the config replacement didn't apply")
	}
	if code, body := call(adminKey, "PATCH", "/v1/connectors/postgres", `{"config":`+cfgOn+`}`); code != 200 {
		t.Fatalf("patch: %d %s", code, body)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		r := run("decoder", "status", "--connector", "postgres", "--recent", "1", "--json")
		var v decoder.View
		if json.Unmarshal([]byte(r.stdout), &v) == nil && v.Status == decoder.StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the decoder never ran: %+v\n%s", r, clipOut(s.err.String()))
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Agent keys are refused on every observe route.
	for _, rt := range [][2]string{{"POST", "/v1/observe/sessions"}, {"GET", "/v1/observe/sessions"}} {
		body := ""
		if rt[0] == "POST" {
			body = `{"connector":"postgres"}`
		}
		if code, b := call(ak.Secret, rt[0], rt[1], body); code != 403 || !strings.Contains(b, "Agents can't open, read, change or undo watched sessions") {
			t.Fatalf("agent key %v: %d %s", rt, code, b)
		}
	}

	// 1. A watched session around another program's writes.
	before := snapshot()
	r = run("observe", "start", "--connector", "postgres", "--label", "nightly agent", "--json")
	var sess observeapi.SessionJSON
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &sess) != nil || sess.State != "open" || !strings.HasPrefix(sess.ID, "obs_") {
		t.Fatalf("observe start: %+v", r)
	}
	if code, b := call(ak.Secret, "POST", "/v1/observe/sessions/"+sess.ID+"/close", `{}`); code != 403 {
		t.Fatalf("an agent closed a session: %d %s", code, b)
	}
	custSQL(`UPDATE public.bulk SET flag = true WHERE id <= 301`)
	custSQL(`UPDATE public.orders SET status = 'shipped' WHERE id IN (1, 2, 3)`)
	custSQL(`INSERT INTO public.refunds (order_id, amount_cents, reason) VALUES (1, 500, '` + observeCanary + `')`)
	r = run("observe", "stop", sess.ID)
	t.Logf("deedglass observe stop:\n%s%s", r.stdout, r.stderr)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Session sealed · 3 transactions") || !strings.Contains(r.stdout, "public.bulk     301 updated") ||
		!strings.Contains(r.stdout, "public.orders   3 updated") || !strings.Contains(r.stdout, "public.refunds  1 inserted") ||
		!strings.Contains(r.stdout, "Deedglass caught this: one transaction changed 301 rows in public.bulk.") ||
		!strings.Contains(r.stdout, "Or print the undo SQL and run it yourself (free): deedglass observe show "+sess.ID+" --undo-sql") ||
		!strings.Contains(r.stdout, "Observed: what Postgres committed. Nothing was compared with what the agent said.") {
		t.Fatalf("observe stop: %+v", r)
	}
	code, body = call(adminKey, "GET", "/v1/observe/sessions/"+sess.ID, "")
	if code != 200 || json.Unmarshal([]byte(body), &sess) != nil || sess.State != "sealed" || sess.Seal == nil || sess.Summary.Transactions != 3 ||
		sess.Frames == nil || len(sess.Frames.Data) != 3 || sess.Frames.Data[0].Diff == nil || len(sess.Caught) != 1 || !obsContains(sess.Flags, "caught") {
		t.Fatalf("the session: %d %s", code, body)
	}
	if strings.Contains(body, `"level"`) || strings.Contains(body, `"verdict"`) {
		t.Fatalf("a watched session never carries a level or a verdict: %s", body)
	}
	// The seal holds ids, counts and digests: no row value.
	{
		c := e.Admin(t)
		var data, deedBody string
		if err := c.QueryRow(context.Background(), `SELECT e.data::text, d.body::text FROM deedglass.admin_events AS e JOIN deedglass.deeds AS d ON d.id = e.deed_id
		    WHERE e.action = 'observe.session_sealed' AND e.target_id = $1`, sess.ID).Scan(&data, &deedBody); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(data, observeCanary) || strings.Contains(deedBody, observeCanary) || strings.Contains(data, "shipped") ||
			!strings.Contains(data, `"frame_count": 3`) || !strings.Contains(data, `"changes_digest": "sha256:`) {
			t.Fatalf("the seal's data: %s", data)
		}
	}
	// Another org's session id is 404, like an unknown one.
	if code, _ := call(adminKey, "GET", "/v1/observe/sessions/"+ids.New[ids.ObserveSessionKind]().String(), ""); code != 404 {
		t.Fatalf("unknown session: %d", code)
	}

	// 2. The free path: the undo SQL, run by hand, restores every row.
	r = run("observe", "show", sess.ID, "--undo-sql")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "-- Deedglass undo SQL for watched session "+sess.ID) || !strings.Contains(r.stdout, "COMMIT;") {
		t.Fatalf("undo sql: %+v", r)
	}
	sqlPath := filepath.Join(t.TempDir(), "undo.sql")
	_ = os.WriteFile(sqlPath, []byte(r.stdout), 0o600)
	t.Logf("undo.sql (%d bytes, %d lines)", len(r.stdout), strings.Count(r.stdout, "\n"))
	{
		c, err := pgx.Connect(context.Background(), cust.AdminURL)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(context.Background(), r.stdout, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("running the undo SQL: %v", err)
		}
		_ = c.Close(context.Background())
	}
	if got := snapshot(); got != before {
		t.Fatalf("the undo SQL didn't restore the tables")
	}
	// Running it twice changes nothing: the guards stop it and Postgres rolls back.
	{
		c, err := pgx.Connect(context.Background(), cust.AdminURL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Exec(context.Background(), r.stdout, pgx.QueryExecModeSimpleProtocol)
		_ = c.Close(context.Background())
		if err == nil || !strings.Contains(err.Error(), "Deedglass undo stopped") {
			t.Fatalf("a second run must stop: %v", err)
		}
	}
	if got := snapshot(); got != before {
		t.Fatalf("the stopped second run changed rows")
	}
	// The session's preview now sees the conflict (the rows changed after the session).
	code, body = call(adminKey, "GET", "/v1/observe/sessions/"+sess.ID+"/undo_preview", "")
	var pv observeapi.PreviewJSON
	if code != 200 || json.Unmarshal([]byte(body), &pv) != nil || pv.Available || pv.Conflicts == 0 || pv.Pro {
		t.Fatalf("preview after the restore: %d %s", code, body)
	}

	// 3. observe run: the child's exit code passes through, it never sees DEEDGLASS_*, and its
	// writes are the session's.
	t.Setenv("DEEDGLASS_API_KEY", "dg_live_never_for_the_child")
	t.Setenv("S50_CHILD_DSN", cust.AdminURL)
	t.Setenv("S50_CHILD_SQL", `UPDATE public.orders SET status = 'cancelled' WHERE id = 10;; INSERT INTO public.refunds (order_id, amount_cents, reason) VALUES (10, 700, 'child')`)
	t.Setenv("S50_CHILD_EXIT", "3")
	r = run("observe", "run", "--connector", "postgres", "--label", "child", "--", os.Args[0], "-test.run=^TestObserveChildHelper$")
	t.Logf("deedglass observe run:\n%s%s", r.stdout, r.stderr)
	if r.code != 3 || !strings.Contains(r.stderr, "◐ Watching postgres (public.bulk, public.customers, public.orders, public.refunds) · session obs_") ||
		!strings.Contains(r.stderr, "✓ Session sealed · 2 transactions") || !strings.Contains(r.stderr, "public.orders   1 updated") {
		t.Fatalf("observe run: %+v", r)
	}
	runID := regexp.MustCompile(`obs_[0-9a-hjkmnp-tv-z]{26}`).FindString(r.stderr)
	afterRun := snapshot()

	// 4. Without Pro: pro_required, with the free path.
	r = run("observe", "undo", runID, "--reason", "The agent's run went wrong.")
	if r.code != 1 || !strings.Contains(r.stderr, "Undoing a whole session in one step is part of Pro.") || !strings.Contains(r.stderr, "--undo-sql") {
		t.Fatalf("no Pro: %+v", r)
	}
	if code, b := call(adminKey, "POST", "/v1/observe/sessions/"+runID+"/undo", `{"plan_digest":"sha256:`+strings.Repeat("0", 64)+`","reason":"x"}`); code != 403 ||
		!strings.Contains(b, `"pro_required"`) || !strings.Contains(b, "observe show "+runID+" --undo-sql") {
		t.Fatalf("no Pro (API): %d %s", code, b)
	}

	// 4b. FEAT-04, watching a coding agent: the agent "fixes" order statuses with psql and forgets the
	// WHERE, so every order changes. observe run --undo-sql ends with the caught line, the undo SQL
	// saved for the person (free), and the any-writer note; the saved file, run with the psql line
	// the run printed, puts public.orders back exactly.
	if psql := psqlPath(t); psql != "" {
		beforeAgent := snapshot()
		// A folder outside the agent's working folder, as the docs suggest: the file is undo-<session>.sql.
		undoDir := t.TempDir()
		r = run("observe", "run", "--connector", "postgres", "--label", "coding agent", "--undo-sql", undoDir, "--",
			psql, "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", cust.AdminURL, "-c", "UPDATE public.orders SET status = 'cancelled'")
		t.Logf("deedglass observe run -- psql:\n%s%s", r.stdout, r.stderr)
		files, _ := filepath.Glob(filepath.Join(undoDir, "undo-obs_*.sql"))
		if len(files) != 1 {
			t.Fatalf("observe run -- psql: want one undo-<session>.sql in the folder, got %v: %+v", files, r)
		}
		undoFile := files[0]
		for _, want := range []string{"✓ Session sealed · 1 transaction", "  public.orders  60 updated",
			"Deedglass caught this: one transaction changed 60 rows in public.orders.",
			"  Undo SQL saved to " + undoFile + " (free). Deedglass doesn't run it: review it, then run it yourself:",
			`    psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f ` + undoFile,
			"  It holds row values: keep it out of version control and out of the agent's folder.",
			"  " + observe.NoteAttribution, observe.NoteObserved} {
			if !strings.Contains(r.stderr, want) {
				t.Fatalf("observe run -- psql: missing %q: %+v", want, r)
			}
		}
		if r.code != 0 || snapshot() == beforeAgent {
			t.Fatalf("observe run -- psql: exit %d, the agent's UPDATE changed nothing or failed", r.code)
		}
		b, err := os.ReadFile(undoFile)
		if err != nil || !strings.HasPrefix(string(b), "-- Deedglass undo SQL for watched session obs_") || !strings.Contains(string(b), "COMMIT;") {
			t.Fatalf("the saved undo SQL: %v\n%s", err, b)
		}
		if fi, err := os.Stat(undoFile); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("the saved undo SQL's mode: %v %v", fi, err)
		}
		out, err := exec.Command(psql, "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", cust.AdminURL, "-f", undoFile).CombinedOutput()
		if err != nil {
			t.Fatalf("running the saved undo SQL with psql: %v\n%s", err, out)
		}
		if got := snapshot(); got != beforeAgent {
			t.Fatalf("the saved undo SQL didn't put public.orders back")
		}
	}

	// 5. With a test Pro licence (restart serve so it asks at start).
	stop()
	f, lenv, _ := licenceSeams(t)
	f.plan = licence.PlanPro
	if r := cli(t, lenv, false, "licence", "activate", "DG-PRO-S50-0001"); r.code != 0 {
		t.Fatalf("activate: %+v", r)
	}
	s, base = start(lenv, false)
	running = true
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	// A third transaction from another writer joins a new session; leave it out of the undo.
	r = run("observe", "start", "--connector", "postgres", "--json")
	var s3 observeapi.SessionJSON
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &s3) != nil {
		t.Fatalf("start 3: %+v", r)
	}
	before3 := snapshot()
	custSQL(`UPDATE public.orders SET status = 'refunded' WHERE id IN (20, 21)`)
	custSQL(`UPDATE public.bulk SET flag = true WHERE id BETWEEN 500 AND 504`) // another writer's
	r = run("observe", "stop", s3.ID, "--json")
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &s3) != nil || s3.State != "sealed" {
		t.Fatalf("stop 3: %+v", r)
	}
	code, body = call(adminKey, "GET", "/v1/observe/sessions/"+s3.ID, "")
	_ = json.Unmarshal([]byte(body), &s3)
	var other string
	for _, fr := range s3.Frames.Data {
		if _, ok := fr.Tables["public.bulk"]; ok {
			other = fr.ID
		}
	}
	if other == "" {
		t.Fatalf("no bulk transaction: %s", body)
	}
	if r = run("observe", "exclude", s3.ID, other); r.code != 0 || !strings.Contains(r.stdout, "left out of the undo") {
		t.Fatalf("exclude: %+v", r)
	}
	code, body = call(adminKey, "GET", "/v1/observe/sessions/"+s3.ID+"/undo_preview", "")
	if code != 200 || json.Unmarshal([]byte(body), &pv) != nil || !pv.Available || !pv.Pro || pv.Rows != 2 || len(pv.Excluded) != 1 {
		t.Fatalf("preview 3: %d %s", code, body)
	}
	r = run("observe", "undo", s3.ID, "--reason", "Refunds marked by mistake.", "--json")
	var ua struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Tool     string `json:"tool"`
		Approval *struct {
			ID string `json:"id"`
		} `json:"approval"`
	}
	if r.code != 2 || json.Unmarshal([]byte(r.stdout), &ua) != nil || ua.Status != "pending_approval" || ua.Approval == nil || ua.Tool != "postgres.undo_session" {
		t.Fatalf("observe undo: %+v", r)
	}
	// The undo's own record lists what was left out (its args, digested in its deed).
	{
		var req string
		if err := e.Admin(t).QueryRow(context.Background(), `SELECT request::text FROM deedglass.actions WHERE id = $1`, ua.ID).Scan(&req); err != nil ||
			!strings.Contains(req, other) {
			t.Fatalf("the undo action doesn't list the excluded transaction %s: %v %s", other, err, req)
		}
	}
	// Excluding after the undo started is refused.
	if code, b := call(adminKey, "POST", "/v1/observe/sessions/"+s3.ID+"/frames/"+other+"/exclude", `{"excluded":false}`); code != 409 {
		t.Fatalf("exclude while undoing: %d %s", code, b)
	}
	// A second undo while it waits: already_undone.
	if code, b := call(adminKey, "POST", "/v1/observe/sessions/"+s3.ID+"/undo", `{"plan_digest":"`+*pv.PlanDigest+`","reason":"again"}`); code != 409 || !strings.Contains(b, "already_undone") {
		t.Fatalf("second undo: %d %s", code, b)
	}
	if r = run("approve", ua.Approval.ID, "--as", "priya@acme.test"); r.code == 0 {
		t.Fatalf("self approve: %+v", r)
	}
	if r = run("approve", ua.Approval.ID, "--as", "omar@acme.test"); r.code != 0 {
		t.Fatalf("approve: %+v", r)
	}
	var out string
	for i := 0; i < 20; i++ {
		r = run("actions", "show", ua.ID, "--wait", "--org", "acme")
		if r.code == 0 && strings.Contains(r.stdout, "record ") {
			out = r.stdout
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("deedglass actions show (the session undo):\n%s", out)
	if !strings.Contains(out, "Proven · Matches") {
		t.Fatalf("the session undo isn't Matches:\n%s\n%s", out, clipOut(s.err.String()))
	}
	// Orders 20 and 21 are back; the excluded bulk rows keep the other writer's change.
	{
		c, err := pgx.Connect(context.Background(), cust.AdminURL)
		if err != nil {
			t.Fatal(err)
		}
		var paid, flagged int
		if err := c.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM public.orders WHERE id IN (20, 21) AND status = 'paid'),
		    (SELECT count(*) FROM public.bulk WHERE id BETWEEN 500 AND 504 AND flag)`).Scan(&paid, &flagged); err != nil {
			t.Fatal(err)
		}
		_ = c.Close(context.Background())
		if paid != 2 || flagged != 5 || snapshot() == before3 {
			t.Fatalf("after the session undo: %d of 2 orders back, %d of 5 excluded rows kept", paid, flagged)
		}
	}
	for i := 0; i < 20; i++ {
		_, body = call(adminKey, "GET", "/v1/observe/sessions/"+s3.ID+"?diff_frames=0", "")
		_ = json.Unmarshal([]byte(body), &s3)
		if s3.State == "undone" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if s3.State != "undone" || s3.Undo.Action == nil || *s3.Undo.Action != ua.ID {
		t.Fatalf("the session isn't undone: %s", body)
	}
	_ = afterRun

	// 6. The chain verifies.
	exp := filepath.Join(t.TempDir(), "deeds.tar.gz")
	if r = run("deeds", "export", "--out", exp); r.code != 0 {
		t.Fatalf("export: %+v", r)
	}
	if r = run("verify", exp); r.code != 0 || !strings.Contains(r.stdout, "chain intact") {
		t.Fatalf("verify: %+v", r)
	}
	stop()
	reDSN := regexp.MustCompile(`deedglass_(agent|witness):[^@\s]+@`)
	for _, o := range outputs {
		if reDSN.MatchString(o) || strings.Contains(o, observeCanary) && strings.Contains(o, "observe.session_sealed") {
			t.Fatalf("a credential or row value reached an output: %s", clipOut(o))
		}
	}
}

// psqlPath is the psql FEAT-04's step drives as the agent: PATH first, then the Homebrew kegs. CI
// (GitHub's Ubuntu runner ships the client) must have one; a laptop without it skips the step.
func psqlPath(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("psql"); err == nil {
		return p
	}
	for _, v := range []string{"18", "17", "16"} {
		if p := "/opt/homebrew/opt/postgresql@" + v + "/bin/psql"; fileExists(p) {
			return p
		}
	}
	if os.Getenv("CI") != "" {
		t.Fatal("psql isn't installed; FEAT-04's step drives it as the agent")
	}
	t.Log("psql isn't installed: skipping FEAT-04's psql step")
	return ""
}

func obsContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
