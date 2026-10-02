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

	"github.com/deedglass/deedglass/internal/connector/postgres/pgtest"
	"github.com/deedglass/deedglass/internal/decoder"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestDecoderLive is S20's acceptance (§9) on a real server: a witnessed postgres connector, init,
// the decoder reaching running on its own, an agent's UPDATE becoming a bound frame visible in
// deedglass decoder status, a forged marker counted and alerted, decoder drop and the new slot,
// and an export whose deeds (both decoder admin deeds included) verify.
func TestDecoderLive(t *testing.T) {
	admin := pgtest.AdminURL(t)
	unlock, err := pgtest.Lock(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cust := pgtest.NewDB(t, admin, "deedglass_it_declive")
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 11)
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
	defer func() {
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
	pf := filepath.Join(t.TempDir(), "postgres-allow-all.yaml")
	_ = os.WriteFile(pf, []byte(postgresPolicy), 0o600)
	if r = run("policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run("policy", "activate", "postgres-allow-all", "--version", "1", "--mode", "enforce", "--reason", "S20 acceptance", "--yes"); r.code != 0 {
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

	// The witnessed connector and its database.
	cf := filepath.Join(t.TempDir(), "postgres.json")
	_ = os.WriteFile(cf, []byte(`{"type":"postgres","name":"postgres","description":"Acme (test)","config":`+pgtest.ConfigJSON+`}`), 0o600)
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
	waitFor := func(what string, ok func(v decoder.View) bool) decoder.View {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			v := status()
			if ok(v) {
				return v
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %+v\n%s", what, v, clipOut(s.err.String()))
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	waitFor("running", func(v decoder.View) bool { return v.Status == decoder.StatusRunning })

	// An agent's UPDATE becomes a bound frame.
	code, body = call(ak.Secret, "POST", "/v1/actions", `{"tool":"postgres.execute","args":{"sql":"UPDATE public.orders SET status = 'shipped' WHERE id = 42"}}`)
	var act struct{ ID, State string }
	if code != 200 || json.Unmarshal([]byte(body), &act) != nil || act.State != "sealed" {
		t.Fatalf("act: %d %s", code, body)
	}
	waitFor("bound", func(v decoder.View) bool {
		return len(v.Recent) == 1 && v.Recent[0].Class == "bound" && v.Recent[0].Action != nil && *v.Recent[0].Action == act.ID
	})
	r = run("decoder", "status", "--connector", "postgres", "--recent", "1")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ postgres · healthy · slot deedglass_") || !strings.Contains(r.stdout, "bound") ||
		!strings.Contains(r.stdout, act.ID[:12]) || !strings.Contains(r.stdout, "public.orders U 1") {
		t.Fatalf("status: %+v", r)
	}
	t.Logf("\n%s", r.stdout)
	var class, action string
	var counts []byte
	if err := e.Admin(t).QueryRow(context.Background(), `SELECT class, action_id, counts FROM deedglass.decoded_txs ORDER BY created_at DESC, id DESC LIMIT 1`).
		Scan(&class, &action, &counts); err != nil || class != "bound" || action != act.ID || !strings.Contains(string(counts), `"public.orders": {"delete": 0, "insert": 0, "update": 1`) {
		t.Fatalf("decoded_txs: %s %s %s %v", class, action, counts, err)
	}
	var evidence []byte
	_ = e.Admin(t).QueryRow(context.Background(), `SELECT execution_evidence FROM deedglass.actions WHERE id = $1`, act.ID).Scan(&evidence)
	if !strings.Contains(string(evidence), `"marker_lsn"`) || !strings.Contains(string(evidence), `"xid8"`) {
		t.Fatalf("execution_evidence %s", evidence)
	}

	// A forged marker is counted and alerted (decoder.alert on deedglass_events; GET /v1/events is S24).
	lc, err := pgx.Connect(context.Background(), e.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer lc.Close(context.Background())
	if _, err := lc.Exec(context.Background(), "LISTEN deedglass_events"); err != nil {
		t.Fatal(err)
	}
	cc, err := pgx.Connect(context.Background(), cust.AdminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close(context.Background())
	if _, err := cc.Exec(context.Background(), `SELECT pg_catalog.pg_logical_emit_message(true, 'deedglass', $1::text)`, ids.New[ids.ActionKind]().String()); err != nil {
		t.Fatal(err)
	}
	alerted := false
	deadline := time.Now().Add(30 * time.Second)
	for !alerted && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		n, err := lc.WaitForNotification(ctx)
		cancel()
		if err == nil && strings.Contains(n.Payload, `"decoder.alert"`) && strings.Contains(n.Payload, `"kind":"forged_marker"`) {
			alerted = true
		}
	}
	if !alerted {
		t.Fatal("no decoder.alert for the forged marker")
	}
	waitFor("the forged marker counted", func(v decoder.View) bool { return v.Frames["forged_marker"] == 1 })

	// Drop: the slot goes, a new one comes on the next sync, and both deeds are in the chain.
	r = run("decoder", "drop", "--connector", "postgres", "--confirm", "postgres")
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Slot dropped · deedglass_") || !strings.Contains(r.stdout, "admin deed deed_") {
		t.Fatalf("drop: %+v", r)
	}
	t.Logf("\n%s", r.stdout)
	waitFor("running on a new slot", func(v decoder.View) bool { return v.Status == decoder.StatusRunning })
	var created2, dropped int
	_ = e.Admin(t).QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE action = 'decoder.slot_created'), count(*) FILTER (WHERE action = 'decoder.slot_dropped')
	    FROM deedglass.admin_events WHERE deed_id IS NOT NULL`).Scan(&created2, &dropped)
	if created2 != 2 || dropped != 1 {
		t.Fatalf("admin deeds: %d created, %d dropped", created2, dropped)
	}
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
