//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// l2Policy lets every Postgres tool run and asks postgres.execute for L2.
const l2Policy = `deedglass: policy/v1
name: postgres-l2
description: Every Postgres tool runs; writes are witnessed by Postgres (S21 tests).

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: postgres-writes
    match: { tool: postgres.execute }
    decision: allow
    verify: L2
  - id: postgres-tools
    match: { tool: ["postgres.*"] }
    decision: allow
`

// l2Trigger is the hero's legacy trigger on the test database (pgtest has 60 paid orders): when
// one order ships, it ships the next 50 open ones too.
const l2Trigger = `CREATE SCHEMA IF NOT EXISTS acme_legacy;
CREATE OR REPLACE FUNCTION acme_legacy.ship_open_batch() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $fn$
BEGIN
  IF pg_catalog.pg_trigger_depth() = 1 THEN
    UPDATE public.orders o SET status = 'shipped'
     WHERE o.id IN (SELECT i.id FROM public.orders i
                     WHERE i.id <> NEW.id AND i.status IN ('pending', 'paid')
                     ORDER BY i.id LIMIT 50);
  END IF;
  RETURN NULL;
END $fn$;
REVOKE ALL ON FUNCTION acme_legacy.ship_open_batch() FROM PUBLIC;
DROP TRIGGER IF EXISTS orders_ship_open_batch ON public.orders;
CREATE TRIGGER orders_ship_open_batch AFTER UPDATE OF status ON public.orders
  FOR EACH ROW WHEN (NEW.status = 'shipped' AND OLD.status IS DISTINCT FROM 'shipped')
  EXECUTE FUNCTION acme_legacy.ship_open_batch();`

// TestWitnessL2Live is S21's acceptance on a real server, a real decoder and a real Postgres: the
// clean path seals L2 · VERIFIED; the legacy trigger seals L2 · MISMATCH with the exact count and
// the diff; the API's witness object and history; actions show --diff; a decoder held back past
// l2_timeout seals UNVERIFIABLE, then the late frame seals an amendment deed; export → verify.
func TestWitnessL2Live(t *testing.T) {
	admin := pgtest.AdminURL(t)
	unlock, err := pgtest.Lock(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cust := pgtest.NewDB(t, admin, "deedglass_it_l2live")
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 21)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "debug", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true"}
	var outputs []string
	s := startServe(t, env, "--migrate")
	var base string
	select {
	case a := <-s.addr:
		base = "http://" + a.String()
	case c := <-s.code:
		t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve never listened")
	}
	stopped := false
	adminSQL := func(q string, args ...any) {
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
	defer func() {
		adminSQL(`ALTER ROLE deedglass_witness LOGIN`)
		if !stopped {
			s.sigs <- syscall.SIGTERM
			s.wait(t)
		}
		c, err := pgx.Connect(context.Background(), cust.AdminURL)
		if err == nil {
			_, _ = c.Exec(context.Background(), `SELECT pg_catalog.pg_drop_replication_slot(slot_name) FROM pg_catalog.pg_replication_slots
			    WHERE database = pg_catalog.current_database() AND NOT active`)
			_ = c.Close(context.Background())
		}
	}()
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	run := func(args ...string) result {
		t.Helper()
		r := cli(t, env, false, args...)
		outputs = append(outputs, r.stdout, r.stderr)
		return r
	}
	r := run("org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		Org      struct{ ID string }     `json:"org"`
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	env["DEEDGLASS_API_KEY"] = created.AdminKey.Secret
	pf := filepath.Join(t.TempDir(), "postgres-l2.yaml")
	_ = os.WriteFile(pf, []byte(l2Policy), 0o600)
	if r = run("policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run("policy", "activate", "postgres-l2", "--version", "1", "--mode", "enforce", "--reason", "S21 acceptance", "--yes"); r.code != 0 {
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
	code, body = call(adminKey, "POST", "/v1/agents/"+agent.ID+"/keys", `{}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}

	// The witnessed connector, with a short l2_timeout for the amendment step.
	cfg := strings.Replace(pgtest.ConfigJSON, `"sensitive_columns":["public.customers.email"]`,
		`"sensitive_columns":["public.customers.email"],"l2_timeout":"3s"`, 1)
	cf := filepath.Join(t.TempDir(), "postgres.json")
	_ = os.WriteFile(cf, []byte(`{"type":"postgres","name":"postgres","description":"Acme (test)","config":`+cfg+`}`), 0o600)
	if r = run("connector", "create", "--config", cf); r.code != 0 {
		t.Fatalf("create: %+v", r)
	}
	env["DEEDGLASS_INIT_DSN"] = cust.AdminURL
	if r = run("connector", "postgres", "init", "--connector", "postgres", "--plan-dir", t.TempDir(), "--apply", "--rotate-passwords"); r.code != 0 {
		t.Fatalf("init: %+v", r)
	}
	delete(env, "DEEDGLASS_INIT_DSN")
	status := func() decoder.View {
		t.Helper()
		r := run("decoder", "status", "--connector", "postgres", "--recent", "1", "--json")
		var v decoder.View
		_ = json.Unmarshal([]byte(r.stdout), &v)
		return v
	}
	deadline := time.Now().Add(60 * time.Second)
	for status().Status != decoder.StatusRunning {
		if time.Now().After(deadline) {
			t.Fatalf("the decoder never ran: %+v\n%s", status(), clipOut(s.err.String()))
		}
		time.Sleep(200 * time.Millisecond)
	}
	agentEnv := func() map[string]string {
		m := map[string]string{}
		for k, v := range env {
			m[k] = v
		}
		m["DEEDGLASS_API_KEY"] = ak.Secret
		return m
	}
	act := func(sql string) result {
		t.Helper()
		r := cli(t, agentEnv(), false, "act", "postgres.execute", "--args", `{"sql":"`+sql+`"}`)
		outputs = append(outputs, r.stdout, r.stderr)
		t.Logf("deedglass act:\n%s%s", r.stdout, r.stderr)
		return r
	}
	reAct := regexp.MustCompile(`act_[0-9a-hjkmnp-tv-z]{26}`)
	const ship = "UPDATE public.orders SET status = 'shipped' WHERE id = 42"

	// 1. The clean path: L2 · VERIFIED, witnessed by Postgres.
	r = act(ship)
	if r.code != 0 || !strings.Contains(r.stdout, "Proven · Matches · Postgres committed 1 row in public.orders, as the statement said.") ||
		!strings.Contains(r.stdout, "Proven by Postgres · commit ") {
		t.Fatalf("clean act: %+v", r)
	}

	// 2. The hero: the legacy trigger ships 50 more.
	adminSQL(l2Trigger)
	adminSQL(`UPDATE public.orders SET status = 'paid' WHERE id = 42`)
	r = act(ship)
	if r.code != 1 || !strings.Contains(r.stdout, "Proven · Doesn't match · Postgres changed 51 rows in public.orders. The agent reported 1.") ||
		!strings.Contains(r.stdout, "expected   UPDATE 1 row in public.orders where id = 42 · sets status") ||
		!strings.Contains(r.stdout, "observed   UPDATE 51 rows in public.orders · 50 outside id = 42") ||
		!strings.Contains(r.stdout, "✓ expected       42   paid → shipped") {
		t.Fatalf("hero act: %+v", r)
	}
	hero := reAct.FindString(r.stdout)
	code, body = call(adminKey, "GET", "/v1/actions/"+hero, "")
	var got struct {
		Witness struct {
			Level, Verdict, Summary string
			Method                  *string
			History                 []struct {
				Deed, Kind, Level, Verdict string
				Reason                     *string
			}
			Reasons []struct{ Code, Level string }
		}
	}
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || got.Witness.Level != "L2" || got.Witness.Verdict != "MISMATCH" ||
		got.Witness.Summary != "Postgres changed 51 rows in public.orders. The agent reported 1." || len(got.Witness.History) != 1 ||
		got.Witness.Method == nil || *got.Witness.Method != "postgres.pgoutput" {
		t.Fatalf("GET: %d %s", code, body)
	}
	codes := []string{}
	for _, x := range got.Witness.Reasons {
		codes = append(codes, x.Level+":"+x.Code)
	}
	if strings.Join(codes, ",") != "L2:row_count_exceeded,L2:unexpected_key" {
		t.Fatalf("reasons %v", codes)
	}
	// The detail names the trigger and the transaction.
	code, body = call(adminKey, "GET", "/v1/actions/"+hero+"?expand[]=witness.detail", "")
	if code != 200 || !strings.Contains(body, "orders_ship_open_batch") || !strings.Contains(body, `"commit_lsn"`) || !strings.Contains(body, `"system_id"`) {
		t.Fatalf("detail: %d %s", code, clipOut(body))
	}
	r = run("actions", "show", hero, "--diff", "--org", "acme")
	if r.code != 0 || !strings.Contains(r.stdout, "Proven · Doesn't match") || !strings.Contains(r.stdout, "✕ unexpected") ||
		!strings.Contains(r.stdout, "first 20 of 51 rows") {
		t.Fatalf("show --diff: %+v", r)
	}
	t.Logf("deedglass actions show --diff:\n%s", r.stdout)

	// 2b. The same through /mcp/native with the official Go SDK: the agent's tool result carries the
	// one line it can repeat, and _meta["com.deedglass/witness"].
	adminSQL(`UPDATE public.orders SET status = 'paid' WHERE id = 42`)
	adminSQL(`UPDATE public.orders SET status = 'paid' WHERE id <> 42`)
	sess := sdkSession(t, base, "/mcp/native", ak.Secret, "2026-07-28")
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "postgres.execute", Arguments: map[string]any{"sql": ship}})
	if err != nil {
		t.Fatal(err)
	}
	const line = "Deedglass check · Proven · Doesn't match · Postgres changed 51 rows in public.orders. The agent reported 1."
	meta, _ := json.Marshal(res.Meta["com.deedglass/witness"])
	if !strings.Contains(text(res), line) || string(meta) != `{"level":"L2","verdict":"MISMATCH"}` || res.IsError {
		t.Fatalf("MCP: %q meta %s isError %v", text(res), meta, res.IsError)
	}
	t.Logf("/mcp/native tools/call:\n%s\n_meta[com.deedglass/witness] = %s", text(res), meta)

	// 3. The amendment: the decoder is held back (its role can't log in), the action seals at
	// l2_timeout, then the decoder catches up and an amendment deed records the L2 verdict.
	adminSQL(`DROP TRIGGER orders_ship_open_batch ON public.orders`)
	adminSQL(`UPDATE public.orders SET status = 'paid' WHERE id = 42`)
	adminSQL(`ALTER ROLE deedglass_witness NOLOGIN`)
	adminSQL(`SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity WHERE usename = 'deedglass_witness'`)
	r = act(ship)
	if r.code != 0 || !strings.Contains(r.stdout, "Can't check") || !strings.Contains(r.stdout, "Postgres's change log didn't show this action within 3s.") ||
		!strings.Contains(r.stdout, "Deedglass keeps watching.") {
		t.Fatalf("timeout act: %+v", r)
	}
	late := reAct.FindString(r.stdout)
	adminSQL(`ALTER ROLE deedglass_witness LOGIN`)
	deadline = time.Now().Add(90 * time.Second)
	for {
		code, body = call(adminKey, "GET", "/v1/actions/"+late, "")
		got.Witness.History = nil
		_ = json.Unmarshal([]byte(body), &got)
		if len(got.Witness.History) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no amendment: %s\n%s", body, clipOut(s.err.String()))
		}
		time.Sleep(500 * time.Millisecond)
	}
	if h := got.Witness.History; h[0].Kind != "action" || h[1].Kind != "amendment" || h[1].Level != "L2" || h[1].Verdict != "VERIFIED" ||
		got.Witness.Level != "L2" || got.Witness.Verdict != "VERIFIED" || h[0].Reason == nil || *h[0].Reason != "l2_timeout" {
		t.Fatalf("history: %s", body)
	}
	var amends string
	if err := e.Admin(t).QueryRow(context.Background(), `SELECT body ->> 'amends' FROM deedglass.deeds WHERE action_id = $1 AND kind = 'amendment'`, late).
		Scan(&amends); err != nil || amends != got.Witness.History[0].Deed {
		t.Fatalf("amends %q, want %s (%v)", amends, got.Witness.History[0].Deed, err)
	}
	r = run("actions", "show", late, "--org", "acme")
	words := strings.Join(strings.Fields(r.stdout), " ") // actions show wraps at 80 columns
	if r.code != 0 || !strings.Contains(words, "history") || !strings.Contains(words, "the Proven check timed out") || !strings.Contains(words, "amendment deed_") {
		t.Fatalf("show: %+v", r)
	}
	t.Logf("deedglass actions show:\n%s", r.stdout)

	// 4. The chain verifies with its amendment.
	out := filepath.Join(t.TempDir(), "deeds.tar.gz")
	if r = run("deeds", "export", "--out", out); r.code != 0 {
		t.Fatalf("export: %+v", r)
	}
	if r = run("verify", out); r.code != 0 || !strings.Contains(r.stdout, "chain intact") {
		t.Fatalf("verify: %+v", r)
	}
	t.Logf("%s", r.stdout)

	s.sigs <- syscall.SIGTERM
	s.wait(t)
	stopped = true
	outputs = append(outputs, s.out.String(), s.err.String())
	reDSN := regexp.MustCompile(`deedglass_(agent|witness):[^@\s]+@`)
	for _, o := range outputs {
		if reDSN.MatchString(o) {
			t.Fatalf("a credential reached an output: %s", clipOut(o))
		}
	}
}
