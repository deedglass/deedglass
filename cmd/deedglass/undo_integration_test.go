//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/deedglass/deedglass/internal/undoapi"
)

// undoPolicy is S21's L2 policy plus the starter's undo rule (S23): a person's undo waits for a
// countersign and is witnessed at L2.
const undoPolicy = `deedglass: policy/v1
name: postgres-undo
description: Every Postgres tool runs; writes are witnessed by Postgres; an undo waits for a person (S23 tests).

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: undo-needs-approval
    match: { tool: "*.undo", connector_type: postgres }
    decision: require_approval
    reason: Undoing a write changes data again, so a human confirms it first.
    approval: { approvers: [role:admin], required: 1, expires_in: 30m }
    verify: L2
  - id: postgres-writes
    match: { tool: postgres.execute }
    decision: allow
    verify: L2
  - id: postgres-tools
    match: { tool: ["postgres.query", "postgres.describe"] }
    decision: allow
`

// TestUndoLive is S23's acceptance on a real server, a real decoder and a real Postgres: the hero
// MISMATCH (a legacy trigger ships 50 more orders), a preview refused while the trigger is on, a
// clean preview, an undo countersigned by another person (the requester can't), L2 VERIFIED, the
// table back exactly as it was, a second undo refused, a redo, the race (a row changed between
// the plan and the run: failed, undo_conflict, nothing changed), an agent refused the internal
// tool, and the chain verifying with the original deed's bytes unchanged.
func TestUndoLive(t *testing.T) {
	admin := pgtest.AdminURL(t)
	unlock, err := pgtest.Lock(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	cust := pgtest.NewDB(t, admin, "deedglass_it_undolive")
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 23)
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
	orders := func() string {
		t.Helper()
		c, err := pgx.Connect(context.Background(), cust.AdminURL)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close(context.Background())
		var out string
		if err := c.QueryRow(context.Background(), `SELECT string_agg(o::text, E'\n' ORDER BY id) FROM public.orders AS o`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
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
	// The admin key stands for priya (as if she minted it in the Console), and omar is a second
	// admin who can countersign her undo.
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
	_ = os.WriteFile(pf, []byte(undoPolicy), 0o600)
	if r = run("policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = run("policy", "activate", "postgres-undo", "--version", "1", "--mode", "enforce", "--reason", "S23 acceptance", "--yes"); r.code != 0 {
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
	code, body = call(adminKey, "POST", "/v1/agents/"+agent.ID+"/keys", `{"scopes":["actions:execute","actions:read","deeds:read","actions:undo"]}`)
	var ak struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &ak) != nil {
		t.Fatalf("%d %s", code, body)
	}
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
	agentEnv := map[string]string{}
	for k, v := range env {
		agentEnv[k] = v
	}
	agentEnv["DEEDGLASS_API_KEY"] = ak.Secret
	reAct := regexp.MustCompile(`act_[0-9a-hjkmnp-tv-z]{26}`)

	// 1. The hero: the legacy trigger ships 50 more orders. L2 · MISMATCH.
	custSQL(l2Trigger)
	before := orders()
	r = cli(t, agentEnv, false, "act", "postgres.execute", "--args", `{"sql":"UPDATE public.orders SET status = 'shipped' WHERE id = 42"}`)
	outputs = append(outputs, r.stdout, r.stderr)
	hero := reAct.FindString(r.stdout + r.stderr)
	if r.code != 1 || hero == "" || !strings.Contains(r.stdout, "L2 · MISMATCH · Postgres changed 51 rows in public.orders.") {
		t.Fatalf("hero: %+v", r)
	}
	after := orders()
	_, heroBody := call(adminKey, "GET", "/v1/actions/"+hero, "")
	var heroAct struct {
		Deed *string `json:"deed"`
		Undo struct {
			Eligible bool    `json:"eligible"`
			UndoneBy *string `json:"undone_by"`
		} `json:"undo"`
	}
	_ = json.Unmarshal([]byte(heroBody), &heroAct)
	if heroAct.Deed == nil || !heroAct.Undo.Eligible || heroAct.Undo.UndoneBy != nil {
		t.Fatalf("the hero's undo object: %s", heroBody)
	}
	_, deedBefore := call(adminKey, "GET", "/v1/deeds/"+*heroAct.Deed, "")

	// 2. The preview while the trigger is on: refused, with the fix SQL. Exit 1.
	r = run("undo", "preview", hero)
	t.Logf("deedglass undo preview (trigger on):\n%s%s", r.stdout, r.stderr)
	if r.code != 1 || !strings.Contains(r.stdout, "✕ Undo not available") || !strings.Contains(r.stdout, "public.orders has trigger orders_ship_open_batch.") ||
		!strings.Contains(r.stdout, "Fix: ALTER TABLE public.orders DISABLE TRIGGER orders_ship_open_batch;") ||
		!strings.Contains(r.stdout, "✓ unchanged") || !strings.Contains(r.stdout, "51 of 51 rows still hold what the action wrote") {
		t.Fatalf("preview with the trigger: %+v", r)
	}
	// An agent key can't preview or run an undo.
	if code, body := call(ak.Secret, "POST", "/v1/actions/"+hero+"/undo/preview", `{}`); code != 403 || !strings.Contains(body, "Agent keys can't undo actions.") {
		t.Fatalf("agent preview: %d %s", code, body)
	}

	// 3. The trigger off: available. Exit 0.
	custSQL(`DROP TRIGGER orders_ship_open_batch ON public.orders`)
	r = run("undo", "preview", hero)
	t.Logf("deedglass undo preview:\n%s%s", r.stdout, r.stderr)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Undo available · 51 rows in public.orders · 51 updates") ||
		!strings.Contains(r.stdout, "policy   undo-needs-approval · a person countersigns first") {
		t.Fatalf("preview: %+v", r)
	}
	preview := func(id string) undoapi.PreviewJSON {
		t.Helper()
		r := run("undo", "preview", id, "--json")
		var pv undoapi.PreviewJSON
		if err := json.Unmarshal([]byte(r.stdout), &pv); err != nil || pv.PlanDigest == nil {
			t.Fatalf("preview --json: %v %+v", err, r)
		}
		return pv
	}
	pv := preview(hero)
	if !pv.Available || pv.Rows != 51 || pv.Counts.Update != 51 || pv.Steps.Total != 51 || len(pv.Steps.Data) != 20 {
		t.Fatalf("preview object: %+v", pv)
	}

	// 4. An agent naming the internal tool is recorded and sealed as tool_unknown.
	r = cli(t, agentEnv, false, "act", "postgres.undo", "--args",
		fmt.Sprintf(`{"undoes":%q,"plan_digest":%q,"reason":"smuggled"}`, hero, *pv.PlanDigest))
	outputs = append(outputs, r.stdout, r.stderr)
	if r.code != 1 || !strings.Contains(r.stdout, "✕ denied · postgres.undo") || !strings.Contains(r.stdout, `No tool named "postgres.undo".`) {
		t.Fatalf("agent undo: %+v", r)
	}

	// 5. A stale digest is refused.
	r = run("undo", "run", hero, "--plan", "sha256:"+strings.Repeat("0", 64), "--reason", "Trigger bug touched 50 extra orders.")
	if r.code != 1 || !strings.Contains(r.stderr, "Preview it again and use the new plan.") {
		t.Fatalf("stale: %+v", r)
	}

	// 6. The undo: pending a countersign. Exit 2.
	undoRun := func(id, plan string) (string, string) {
		t.Helper()
		r := run("undo", "run", id, "--plan", plan, "--reason", "Trigger bug touched 50 extra orders.", "--json")
		var a struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Approval *struct {
				ID string `json:"id"`
			} `json:"approval"`
		}
		if r.code != 2 || json.Unmarshal([]byte(r.stdout), &a) != nil || a.Status != "pending_approval" || a.Approval == nil {
			t.Fatalf("undo run: %+v", r)
		}
		return a.ID, a.Approval.ID
	}
	r = run("undo", "run", hero, "--plan", *pv.PlanDigest, "--reason", "Trigger bug touched 50 extra orders.")
	t.Logf("deedglass undo run:\n%s%s", r.stdout, r.stderr)
	undoID := reAct.FindString(strings.Replace(r.stdout, hero, "", -1))
	if r.code != 2 || !strings.Contains(r.stdout, "◐ pending approval · postgres.undo") || undoID == "" {
		t.Fatalf("undo run: %+v", r)
	}
	// A second request while it waits: already_undone.
	if code, body := call(adminKey, "POST", "/v1/actions/"+hero+"/undo", fmt.Sprintf(`{"plan_digest":%q,"reason":"again"}`, *pv.PlanDigest)); code != 409 ||
		!strings.Contains(body, `"already_undone"`) || !strings.Contains(body, "already waiting for approval") {
		t.Fatalf("second undo: %d %s", code, body)
	}
	_, ub := call(adminKey, "GET", "/v1/actions/"+undoID, "")
	var undoAct struct {
		Approval struct{ ID string }      `json:"approval"`
		Undo     struct{ Undoes *string } `json:"undo"`
		OBO      *string                  `json:"on_behalf_of"`
		Agent    struct{ Name string }    `json:"agent"`
	}
	_ = json.Unmarshal([]byte(ub), &undoAct)
	if undoAct.Undo.Undoes == nil || *undoAct.Undo.Undoes != hero || undoAct.Agent.Name != "console" || undoAct.OBO == nil ||
		!strings.HasPrefix(*undoAct.OBO, "usr_") {
		t.Fatalf("the undo action: %s", ub)
	}
	// The requester can't countersign her own undo (S22's self rule).
	r = run("approve", undoAct.Approval.ID, "--as", "priya@acme.test")
	if r.code == 0 || !strings.Contains(r.stdout+r.stderr, "another approver") {
		t.Fatalf("self approve: %+v", r)
	}
	waitSealed := func(id string) string {
		t.Helper()
		var r result
		for i := 0; i < 8; i++ {
			r = run("actions", "show", id, "--wait", "--org", "acme")
			if r.code == 0 && strings.Contains(r.stdout, "deed ") {
				return r.stdout
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("%s never sealed: %+v\n%s", id, r, clipOut(s.err.String()))
		return ""
	}
	r = run("approve", undoAct.Approval.ID, "--as", "omar@acme.test")
	if r.code != 0 {
		t.Fatalf("approve: %+v", r)
	}
	out := waitSealed(undoID)
	t.Logf("deedglass actions show (the undo):\n%s", out)
	if !strings.Contains(out, "L2 · VERIFIED") || !strings.Contains(out, "Restored 51 rows in public.orders exactly as planned") {
		t.Fatalf("the undo isn't L2 VERIFIED:\n%s", out)
	}
	if got := orders(); got != before {
		t.Fatalf("the orders aren't back:\n%s\nwant\n%s", got, before)
	}
	_, heroBody = call(adminKey, "GET", "/v1/actions/"+hero, "")
	_ = json.Unmarshal([]byte(heroBody), &heroAct)
	if heroAct.Undo.UndoneBy == nil || *heroAct.Undo.UndoneBy != undoID || heroAct.Undo.Eligible {
		t.Fatalf("undone_by: %s", heroBody)
	}
	r = run("undo", "run", hero, "--plan", *pv.PlanDigest, "--reason", "again")
	if r.code != 1 || !strings.Contains(r.stderr, "was already undone by") {
		t.Fatalf("undo after undo: %+v", r)
	}

	// 7. A redo: undo the undo.
	pv2 := preview(undoID)
	redo, apr := undoRun(undoID, *pv2.PlanDigest)
	if r = run("approve", apr, "--as", "omar@acme.test"); r.code != 0 {
		t.Fatalf("approve redo: %+v", r)
	}
	if out := waitSealed(redo); !strings.Contains(out, "L2 · VERIFIED") {
		t.Fatalf("redo:\n%s", out)
	}
	if got := orders(); got != after {
		t.Fatalf("the redo didn't put the hero's rows back:\n%s", got)
	}

	// 8. The race: a row changes after the plan, before the countersign. Failed, nothing changed.
	pv3 := preview(redo)
	race, apr3 := undoRun(redo, *pv3.PlanDigest)
	custSQL(`UPDATE public.orders SET status = 'cancelled' WHERE id = 43`)
	mid := orders()
	if r = run("approve", apr3, "--as", "omar@acme.test"); r.code != 0 {
		t.Fatalf("approve race: %+v", r)
	}
	out = waitSealed(race)
	t.Logf("deedglass actions show (the race):\n%s", out)
	_, rb := call(adminKey, "GET", "/v1/actions/"+race, "")
	var ra struct {
		Status    string `json:"status"`
		Execution struct {
			ErrorCode    string `json:"error_code"`
			ErrorMessage string `json:"error_message"`
		} `json:"execution"`
	}
	if json.Unmarshal([]byte(rb), &ra) != nil || ra.Status != "failed" || ra.Execution.ErrorCode != "undo_conflict" ||
		ra.Execution.ErrorMessage != "Row public.orders id=43 changed after the plan was made (status is now 'cancelled'). Nothing was changed. Preview the undo again." {
		t.Fatalf("race: %s", rb)
	}
	if got := orders(); got != mid {
		t.Fatalf("the failed undo changed rows")
	}
	// The slot is free again: a new preview is available (the plan now refuses row 43).
	if pv4 := preview(redo); pv4.Available {
		t.Fatalf("row 43 changed since, so the undo must be unavailable: %+v", pv4.Checks)
	}

	// 9. The chain verifies, and the hero's deed is byte for byte what it was.
	if _, deedAfter := call(adminKey, "GET", "/v1/deeds/"+*heroAct.Deed, ""); deedAfter != deedBefore {
		t.Fatalf("the hero's deed changed")
	}
	exp := filepath.Join(t.TempDir(), "deeds.tar.gz")
	if r = run("deeds", "export", "--out", exp); r.code != 0 {
		t.Fatalf("export: %+v", r)
	}
	if r = run("verify", exp); r.code != 0 || !strings.Contains(r.stdout, "chain intact") {
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
