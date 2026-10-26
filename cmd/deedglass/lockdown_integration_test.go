//go:build integration

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// lockdownPolicy allows everything the agent's connector serves: lockdown has to hold without the
// policy's help.
const lockdownPolicy = `deedglass: policy/v1
name: allow-scratch
description: Allows every scratch tool. Lockdown, not this policy, makes writes wait.
defaults:
  decision: deny
  reason: No rule allows this action.
  verify: L0
rules:
  - id: allow-scratch
    match: { tool: "scratch.*" }
    decision: allow
    verify: L0
`

// TestLockdownLive is lockdown mode end to end on a real server (ADR-117): an agent without a
// connector label refused; a write the policy allows held for a person; a high-risk write that needs
// two different people (one person twice is refused); a Can't check result holding the agent's
// writes in every session until a person acknowledges it; and turning lockdown off, which the
// requester can't countersign (attack case M10) and a second person can.
func TestLockdownLive(t *testing.T) {
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 11)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "warn", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32}
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
	defer func() { s.sigs <- syscall.SIGTERM; s.wait(t) }()
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base

	r := cli(t, env, false, "org", "create", "--name", "Acme Health", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		Org      struct{ ID string }     `json:"org"`
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	admin := created.AdminKey.Secret
	env["DEEDGLASS_API_KEY"] = admin
	{ // A second admin, for four-eyes and the countersign.
		c := e.Admin(t)
		u := ids.New[ids.UserKind]()
		if _, err := c.Exec(context.Background(), `INSERT INTO deedglass.users (id, email, display_name, status) VALUES ($1, 'sam@acme.test', 'Sam', 'active')`, u); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Exec(context.Background(), `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
		    VALUES ($1, $2, $3, 'admin', 'active', now())`, ids.New[ids.MembershipKind](), created.Org.ID, u); err != nil {
			t.Fatal(err)
		}
	}
	pf := filepath.Join(t.TempDir(), "allow-scratch.yaml")
	_ = os.WriteFile(pf, []byte(lockdownPolicy), 0o600)
	if r = cli(t, env, false, "policy", "push", pf); r.code != 0 {
		t.Fatalf("push: %+v", r)
	}
	if r = cli(t, env, false, "policy", "activate", "allow-scratch", "--version", "1", "--mode", "enforce", "--reason", "lockdown e2e", "--yes"); r.code != 0 {
		t.Fatalf("activate: %+v", r)
	}

	call := func(key, method, path, body string, hdr ...string) (int, string) {
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
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		res, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := call(admin, "POST", "/v1/agents", `{"name":"ward-bot"}`)
	var agent struct{ ID string }
	if code != 201 || json.Unmarshal([]byte(body), &agent) != nil {
		t.Fatalf("agent: %d %s", code, body)
	}
	code, body = call(admin, "POST", "/v1/agents/"+agent.ID+"/keys", `{"name":"ward"}`, "Idempotency-Key", "lockdown-e2e-key")
	var key struct{ Secret string }
	if code != 201 || json.Unmarshal([]byte(body), &key) != nil || key.Secret == "" {
		t.Fatalf("key: %d %s", code, body)
	}

	// Lockdown on, sealed; status names the unlabelled agent.
	if r = cli(t, env, false, "lockdown", "on", "--reason", "Ward pilot"); r.code != 0 || !strings.Contains(r.stdout, "✓ Lockdown is on for Acme Health") ||
		!strings.Contains(r.stdout, "sealed") {
		t.Fatalf("lockdown on: %+v", r)
	}
	if r = cli(t, env, false, "lockdown", "status"); r.code != 0 || !strings.Contains(r.stdout, "1 agent without a connector label") ||
		!strings.Contains(r.stdout, "approvers  2 owners or admins") {
		t.Fatalf("status: %+v", r)
	}
	if code, body = call(admin, "GET", "/v1/whoami", ""); code != 200 || !strings.Contains(body, `"lockdown": true`) {
		t.Fatalf("whoami: %d %s", code, body)
	}

	act := func(tool, ctype, args string, hdr ...string) (int, string) {
		t.Helper()
		b := `{"tool":"` + tool + `","mode":"proxy","args":` + args + `,"wait":5`
		if ctype != "" {
			b += `,"connector_type":"` + ctype + `"`
		}
		return call(key.Secret, "POST", "/v1/actions", b+"}", hdr...)
	}
	field := func(body, path string) string {
		var v map[string]any
		_ = json.Unmarshal([]byte(body), &v)
		cur := any(v)
		for _, p := range strings.Split(path, ".") {
			m, _ := cur.(map[string]any)
			cur = m[p]
		}
		s, _ := cur.(string)
		return s
	}
	write := `{"path":"/notes/a.txt","content":"hi"}`

	// 1. No connector label: refused, whatever the policy allows (M07).
	if code, body = act("scratch.write_file", "", write); code != 403 || !strings.Contains(body, "lockdown.one-connector") {
		t.Fatalf("unlabelled: %d %s", code, body)
	}
	if code, body = call(admin, "PATCH", "/v1/agents/"+agent.ID, `{"labels":{"connector":"scratch"}}`, "Idempotency-Key", "lockdown-e2e-label"); code != 200 {
		t.Fatalf("label: %d %s", code, body)
	}

	// 2. A write the policy allows is held for a person (M01).
	code, body = act("scratch.write_file", "", write)
	held := field(body, "id")
	if code != 202 || field(body, "status") != "pending_approval" || !strings.Contains(body, "lockdown.write-needs-person") {
		t.Fatalf("held: %d %s", code, body)
	}
	apr := field(body, "approval.id")
	if r = cli(t, env, false, "approve", apr, "--as", "priya@acme.test", "--reason", "Note looks right"); r.code != 0 || !strings.Contains(r.stdout, "1 of 1") {
		t.Fatalf("approve: %+v", r)
	}
	report := func(id, status string) (int, string) {
		now := time.Now().UTC()
		return call(key.Secret, "POST", "/v1/actions/"+id+"/result", `{"status":"`+status+`","started_at":"`+now.Add(-20*time.Millisecond).Format(time.RFC3339Nano)+
			`","finished_at":"`+now.Format(time.RFC3339Nano)+`","result":{"content":[{"type":"text","text":"ok"}],"isError":false}}`)
	}
	waitAuthorized := func(id string) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); ; {
			_, b := call(key.Secret, "GET", "/v1/actions/"+id+"?wait=2", "")
			if field(b, "status") == "authorized" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("never authorized: %s", b)
			}
		}
	}
	waitAuthorized(held)
	if code, body = report(held, "succeeded"); code != 200 || !strings.Contains(body, `"state": "sealed"`) {
		t.Fatalf("report: %d %s", code, body)
	}

	// 3. A high-risk write (a DELETE) needs two different people (M02).
	code, body = act("scratch.execute", "postgres", `{"sql":"DELETE FROM public.orders WHERE id = 7"}`)
	four, apr4 := field(body, "id"), field(body, "approval.id")
	if code != 202 || apr4 == "" {
		t.Fatalf("four-eyes: %d %s", code, body)
	}
	if r = cli(t, env, false, "approve", apr4, "--as", "priya@acme.test", "--reason", "Duplicate order"); r.code != 0 || !strings.Contains(r.stdout, "1 of 2") {
		t.Fatalf("first vote: %+v", r)
	}
	if r = cli(t, env, false, "approve", apr4, "--as", "priya@acme.test", "--reason", "Again"); r.code == 0 || !strings.Contains(r.stdout+r.stderr, "already") {
		t.Fatalf("one person twice: %+v", r)
	}
	if r = cli(t, env, false, "approve", apr4, "--as", "sam@acme.test", "--reason", "Agreed"); r.code != 0 || !strings.Contains(r.stdout, "2 of 2") {
		t.Fatalf("second person: %+v", r)
	}
	waitAuthorized(four)
	if code, body = report(four, "succeeded"); code != 200 {
		t.Fatalf("report: %d %s", code, body)
	}

	// 4. A Can't check result holds the agent's writes, in every session (M06).
	code, body = act("scratch.write_file", "", write)
	cant, apr5 := field(body, "id"), field(body, "approval.id")
	if code != 202 {
		t.Fatalf("third write: %d %s", code, body)
	}
	if r = cli(t, env, false, "approve", apr5, "--as", "sam@acme.test", "--reason", "Fine"); r.code != 0 {
		t.Fatalf("approve: %+v", r)
	}
	waitAuthorized(cant)
	if code, body = report(cant, "unknown"); code != 200 || !strings.Contains(body, "UNVERIFIABLE") {
		t.Fatalf("unknown result: %d %s", code, body)
	}
	for _, session := range []string{"", "a-new-session"} {
		var hdr []string
		if session != "" {
			hdr = []string{"Deedglass-Session", session}
		}
		if code, body = act("scratch.write_file", "", `{"path":"/notes/b.txt","content":"x"}`, hdr...); code != 403 ||
			!strings.Contains(body, "lockdown.cant-check-hold") || !strings.Contains(body, cant) {
			t.Fatalf("held write (session %q): %d %s", session, code, body)
		}
	}
	if r = cli(t, env, false, "lockdown", "status"); !strings.Contains(r.stdout, "held") || !strings.Contains(r.stdout, cant) {
		t.Fatalf("status lists the hold: %+v", r)
	}
	if r = cli(t, env, false, "lockdown", "ack", cant, "--as", "sam@acme.test", "--reason", "Checked the note by hand"); r.code != 0 ||
		!strings.Contains(r.stdout, "Acknowledged "+cant) {
		t.Fatalf("ack: %+v", r)
	}
	if code, body = act("scratch.write_file", "", `{"path":"/notes/b.txt","content":"x"}`); code != 202 || field(body, "status") != "pending_approval" {
		t.Fatalf("after ack: %d %s", code, body)
	}

	// 5. Off needs two people (M10): the requester can't countersign; a second person can.
	if r = cli(t, env, false, "lockdown", "off", "--as", "priya@acme.test", "--reason", "Pilot over"); r.code != 0 || !strings.Contains(r.stdout, "waiting for a second person") {
		t.Fatalf("off request: %+v", r)
	}
	if r = cli(t, env, false, "--json", "lockdown", "off", "--countersign", "--as", "priya@acme.test"); r.code != 1 || !strings.Contains(r.stdout+r.stderr, "countersign_same_person") {
		t.Fatalf("same person: %+v", r)
	}
	if code, body = act("scratch.write_file", "", write); code != 202 {
		t.Fatalf("still on after a refused countersign: %d %s", code, body)
	}
	if r = cli(t, env, false, "lockdown", "off", "--countersign", "--as", "sam@acme.test"); r.code != 0 || !strings.Contains(r.stdout, "Lockdown is off for Acme Health") {
		t.Fatalf("countersign: %+v", r)
	}
	if code, body = act("scratch.write_file", "", write); code != 200 || field(body, "status") != "authorized" {
		t.Fatalf("off: the policy decides again: %d %s", code, body)
	}
	// The record: every change sealed, the chain intact.
	if r = cli(t, env, false, "ledger", "check"); r.code != 0 {
		t.Fatalf("ledger check: %+v", r)
	}
	if r = cli(t, env, false, "--json", "deeds", "list", "--kind", "admin", "--limit", "50"); r.code != 0 ||
		!strings.Contains(r.stdout, "lockdown.off_requested") || !strings.Contains(r.stdout, "lockdown.acknowledged") {
		t.Fatalf("admin deeds: %+v", r)
	}
}
