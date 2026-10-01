//go:build integration

package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
)

// call sends one request with a bearer key (empty: none) from the client address ip.
func (w *world) call(t testing.TB, key, method, path, body string, hdr ...string) resp {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, w.ts.URL+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	out, _ := io.ReadAll(r.Body)
	return resp{status: r.StatusCode, header: r.Header, body: out}
}

// flat is the body without insignificant whitespace, for matching JSON fragments.
func flat(r resp) string {
	var b bytes.Buffer
	if err := json.Compact(&b, r.body); err != nil {
		return string(r.body)
	}
	return b.String()
}

func expect(t testing.TB, r resp, status int, code string) {
	t.Helper()
	if r.status != status || (code != "" && r.code() != code) {
		t.Fatalf("got %d %s, want %d %s", r.status, r.body, status, code)
	}
}

// fleet is an org with an owner session and an operator-made admin key.
type fleet struct {
	w     *world
	org   identity.CreatedOrg
	owner *browser
	admin string // the admin key's secret
}

func newFleet(t testing.TB, w *world, name, owner string) *fleet {
	t.Helper()
	c, b := w.org(t, name, owner)
	k, err := w.svc.CreateAdminKey(ctx, identity.Operator, c.Org.ID, identity.CreateKeyInput{Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	return &fleet{w: w, org: c, owner: b, admin: *k.Secret}
}

func (f *fleet) agent(t testing.TB, name string, labels ...string) identity.AgentResult {
	t.Helper()
	l := map[string]string{}
	for i := 0; i+1 < len(labels); i += 2 {
		l[labels[i]] = labels[i+1]
	}
	b, _ := json.Marshal(map[string]any{"name": name, "labels": l})
	r := f.w.call(t, f.admin, "POST", "/v1/agents", string(b))
	expect(t, r, 201, "")
	var a identity.AgentResult
	r.json(t, &a)
	return a
}

func (f *fleet) key(t testing.TB, agent string, body string) identity.KeyResult {
	t.Helper()
	if body == "" {
		body = `{"name":"prod runner"}`
	}
	r := f.w.call(t, f.admin, "POST", "/v1/agents/"+agent+"/keys", body)
	expect(t, r, 201, "")
	var k identity.KeyResult
	r.json(t, &k)
	if k.Secret == nil || len(*k.Secret) != identity.KeyLen {
		t.Fatalf("no secret: %s", r.body)
	}
	return k
}

func (f *fleet) adminEvents(t testing.TB, action, target string) int {
	t.Helper()
	var n int
	c := f.w.e.Admin(t)
	if err := c.QueryRow(ctx, `SELECT count(*) FROM deedglass.admin_events WHERE org_id = $1 AND action = $2 AND target_id = $3 AND deed_id IS NOT NULL`,
		f.org.Org.ID, action, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAgentLifecycle(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")

	r := w.call(t, f.admin, "POST", "/v1/agents", `{"name":"support-bot","description":"Answers refund tickets in Zendesk",
	  "labels":{"env":"prod","team":"support"},"budgets":{"calls_per_minute":60,"calls_per_day":5000,"spend_per_day":{"amount":5000,"currency":"usd"}}}`)
	expect(t, r, 201, "")
	var a identity.AgentResult
	r.json(t, &a)
	if a.Object != "agent" || a.Status != identity.AgentActive || a.Deed == nil || a.Labels["team"] != "support" ||
		*a.Budgets.CallsPerMinute != 60 || a.Budgets.SpendPerDay.Amount != 5000 || a.Owner != nil {
		t.Fatalf("created: %s", r.body)
	}
	if !strings.Contains(flat(r), `"budgets":{"calls_per_minute":60,"calls_per_day":5000,"spend_per_day":{"amount":5000,"currency":"usd"}}`) {
		t.Fatalf("budgets shape: %s", r.body)
	}
	id := a.ID

	// Validation and conflicts.
	expect(t, w.call(t, f.admin, "POST", "/v1/agents", `{"name":"support-bot"}`), 409, "conflict")
	r = w.call(t, f.admin, "POST", "/v1/agents", `{"name":"Support Bot"}`)
	expect(t, r, 400, "validation_failed")
	if r.message() != "Agent names use lowercase letters, digits and hyphens, 2 to 63 characters, like support-bot." {
		t.Fatalf("name message: %s", r.message())
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/agents", `{"name":"console"}`), 400, "validation_failed")
	expect(t, w.call(t, f.admin, "POST", "/v1/agents", `{"name":"x-1","labels":{"Env":"prod"}}`), 400, "validation_failed")
	expect(t, w.call(t, f.admin, "POST", "/v1/agents", `{"name":"x-1","budgets":{"spend_per_day":{"amount":5,"currency":"USD"}}}`), 400, "validation_failed")
	expect(t, w.call(t, f.admin, "POST", "/v1/agents", `{"name":"x-1","budgets":{"calls_per_hour":5}}`), 400, "validation_failed")
	r = w.call(t, f.admin, "POST", "/v1/agents", `{"name":"support-bot"}`)
	if r.message() != "An agent named support-bot already exists in Acme. Pick another name." {
		t.Fatalf("conflict message: %s", r.message())
	}

	// Reads.
	r = w.call(t, f.admin, "GET", "/v1/agents?name=support-bot", "")
	expect(t, r, 200, "")
	var l httpx.List[identity.Agent]
	r.json(t, &l)
	if len(l.Data) != 1 || l.Data[0].ID != id {
		t.Fatalf("by name: %s", r.body)
	}
	f.agent(t, "refund-agent", "env", "staging")
	r = w.call(t, f.admin, "GET", "/v1/agents?label=env:prod", "")
	r.json(t, &l)
	if len(l.Data) != 1 || l.Data[0].Name != "support-bot" {
		t.Fatalf("by label: %s", r.body)
	}
	expect(t, w.call(t, f.admin, "GET", "/v1/agents/agt_01k633rrmh3bcz2p39gg4vp46q", ""), 404, "not_found")

	// PATCH: names are immutable; a no-op seals nothing; labels seal agent.updated.
	expect(t, w.call(t, f.admin, "PATCH", "/v1/agents/"+id, `{"name":"helper"}`), 422, "validation_failed")
	r = w.call(t, f.admin, "PATCH", "/v1/agents/"+id, `{"labels":{"env":"prod","team":"support"}}`)
	expect(t, r, 200, "")
	if strings.Contains(flat(r), `"deed":"deed_`) {
		t.Fatalf("a no-op patch sealed a deed: %s", r.body)
	}
	r = w.call(t, f.admin, "PATCH", "/v1/agents/"+id, `{"labels":{"env":"prod","team":"billing"},"description":"Refunds"}`)
	expect(t, r, 200, "")
	r.json(t, &a)
	if a.Deed == nil || a.Labels["team"] != "billing" || a.Description != "Refunds" || f.adminEvents(t, ledger.AdminAgentUpdated, id) != 1 {
		t.Fatalf("patch: %s", r.body)
	}

	// Every edge of §3.1, and the refusals.
	step := func(op, body string, status int, code string, want identity.AgentStatus, sealed bool) {
		t.Helper()
		r := w.call(t, f.admin, "POST", "/v1/agents/"+id+"/"+op, body)
		expect(t, r, status, code)
		if status != 200 {
			return
		}
		var a identity.AgentResult
		r.json(t, &a)
		if a.Status != want || (a.Deed != nil) != sealed {
			t.Fatalf("%s: %s", op, r.body)
		}
	}
	step("suspend", "", 200, "", identity.AgentSuspended, true)
	step("suspend", "", 200, "", identity.AgentSuspended, false)
	step("start", `{"reason":"fixed"}`, 409, "conflict", "", false)
	step("resume", "", 200, "", identity.AgentActive, true)
	step("resume", "", 200, "", identity.AgentActive, false)
	step("suspend", "", 200, "", identity.AgentSuspended, true)
	step("stop", `{"reason":"Refund loop in staging"}`, 200, "", identity.AgentStopped, true) // suspended → stopped
	step("start", `{"reason":"fixed"}`, 200, "", identity.AgentActive, true)
	step("stop", `{"reason":"Refund loop in staging"}`, 200, "", identity.AgentStopped, true) // active → stopped
	step("stop", "", 200, "", identity.AgentStopped, false)
	step("suspend", "", 409, "conflict", "", false)
	r = w.call(t, f.admin, "POST", "/v1/agents/"+id+"/resume", "")
	expect(t, r, 409, "conflict")
	if want := "support-bot is stopped, not suspended. Start it with POST /v1/agents/" + id + "/start."; r.message() != want {
		t.Fatalf("resume on stopped: %q", r.message())
	}
	step("start", "", 400, "validation_failed", "", false)
	step("start", `{"reason":"ok"}`, 400, "validation_failed", "", false)
	if n := f.adminEvents(t, ledger.AdminAgentStopped, id); n != 2 {
		t.Fatalf("agent.stopped deeds: %d, want 2 (a repeated stop seals nothing)", n)
	}
	r = w.call(t, f.admin, "GET", "/v1/agents/"+id, "")
	r.json(t, &a)
	if a.StatusReason == nil || *a.StatusReason != "Refund loop in staging" || a.StatusChangedBy == nil || !strings.HasPrefix(*a.StatusChangedBy, "key_") {
		t.Fatalf("status fields: %s", r.body)
	}
}

func TestTransitionTable(t *testing.T) {
	ops := []identity.StatusOp{identity.OpSuspend, identity.OpResume, identity.OpStop, identity.OpStart}
	all := []identity.AgentStatus{identity.AgentActive, identity.AgentSuspended, identity.AgentStopped}
	want := map[string]string{
		"suspend/active": "apply", "suspend/suspended": "noop", "suspend/stopped": "",
		"resume/active": "noop", "resume/suspended": "apply", "resume/stopped": "",
		"stop/active": "apply", "stop/suspended": "apply", "stop/stopped": "noop",
		"start/active": "noop", "start/suspended": "", "start/stopped": "apply",
	}
	for _, op := range ops {
		for _, s := range all {
			got, _ := identity.Transition(op, s)
			if k := string(op) + "/" + string(s); got != want[k] {
				t.Errorf("%s: %q, want %q", k, got, want[k])
			}
		}
	}
}

func TestKeysAndWhoami(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot", "env", "prod")
	k := f.key(t, a.ID, "")
	if k.Object != "api_key" || k.Mode != identity.ModeLive || !strings.HasPrefix(*k.Secret, k.Prefix+"_") || k.Deed == nil ||
		strings.Join(k.Scopes, ",") != "actions:execute,actions:read,deeds:read" {
		t.Fatalf("key: %+v", k)
	}
	r := w.call(t, *k.Secret, "GET", "/v1/whoami", "")
	expect(t, r, 200, "")
	var who identity.Whoami
	r.json(t, &who)
	if who.Agent == nil || who.Agent.Status != identity.AgentActive || who.Agent.Name != "support-bot" || who.Key.Prefix != k.Prefix ||
		who.Org.Name != "Acme" || who.Org.Stopped || strings.Contains(string(r.body), *k.Secret) {
		t.Fatalf("whoami: %s", r.body)
	}
	// The list never shows a secret.
	r = w.call(t, f.admin, "GET", "/v1/agents/"+a.ID+"/keys", "")
	expect(t, r, 200, "")
	if strings.Contains(flat(r), `"secret"`) || !strings.Contains(string(r.body), k.ID) {
		t.Fatalf("list: %s", r.body)
	}
	// Admin whoami: no agent, no budgets.
	r = w.call(t, f.admin, "GET", "/v1/whoami", "")
	expect(t, r, 200, "")
	if !strings.Contains(flat(r), `"agent":null`) || !strings.Contains(flat(r), `"scopes":["admin"]`) {
		t.Fatalf("admin whoami: %s", r.body)
	}
	// Test keys carry livemode=false; agent keys can't hold admin.
	tk := f.key(t, a.ID, `{"mode":"test"}`)
	if tk.Mode != identity.ModeTest || !strings.HasPrefix(*tk.Secret, "dg_test_") {
		t.Fatalf("test key: %+v", tk)
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/keys", `{"scopes":["admin"]}`), 400, "validation_failed")
	// At most 10 active keys.
	for range 8 {
		f.key(t, a.ID, "")
	}
	r = w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/keys", `{}`)
	expect(t, r, 409, "conflict")
	if r.message() != "support-bot already has 10 active keys. Revoke one before creating another." {
		t.Fatalf("limit: %s", r.message())
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/keys/"+tk.ID+"/revoke", ""), 200, "")
	f.key(t, a.ID, "")
}

func TestWhoamiShowsStatus(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	k := *f.key(t, a.ID, "").Secret
	status := func() (string, bool) {
		r := w.call(t, k, "GET", "/v1/whoami", "")
		expect(t, r, 200, "")
		var who identity.Whoami
		r.json(t, &who)
		if strings.Contains(string(r.body), "Refund loop") {
			t.Fatalf("whoami leaked the reason: %s", r.body)
		}
		return string(who.Agent.Status), who.Org.Stopped
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", `{"reason":"Refund loop"}`), 200, "")
	if s, _ := status(); s != "stopped" {
		t.Fatalf("stopped: %s", s)
	}
	r := w.call(t, k, "GET", "/v1/deeds?limit=1", "")
	expect(t, r, 403, "agent_stopped")
	if r.message() != `Agent "support-bot" is stopped. Deedglass refuses its calls until an admin starts it in Console › Agents.` ||
		strings.Contains(string(r.body), "Refund loop") {
		t.Fatalf("refusal: %s", r.body)
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/start", `{"reason":"fixed"}`), 200, "")
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/suspend", ""), 200, "")
	if s, _ := status(); s != "suspended" {
		t.Fatalf("suspended: %s", s)
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/resume", ""), 200, "")
	expect(t, w.call(t, f.admin, "POST", "/v1/org/stop-all", `{"reason":"Investigating"}`), 200, "")
	if s, stopped := status(); s != "active" || !stopped {
		t.Fatalf("org stopped: %s %v", s, stopped)
	}
}

func TestPrecedence(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	good := *f.key(t, a.ID, "").Secret
	readOnly := *f.key(t, a.ID, `{"scopes":["actions:read"]}`).Secret
	revoked := f.key(t, a.ID, "")
	expect(t, w.call(t, f.admin, "POST", "/v1/keys/"+revoked.ID+"/revoke", ""), 200, "")
	p, _ := identity.ParseAPIKey(good)
	wrongSecret := identity.DisplayPrefix(p.Mode, p.Prefix) + "_" + strings.Repeat("A", 32)
	wrongMode := identity.DisplayPrefix(identity.ModeTest, p.Prefix) + "_" + p.Secret
	unknown := "dg_live_ZZZZZZZZ_" + strings.Repeat("b", 32)

	const path = "/v1/deeds?limit=1"
	cases := []struct {
		name, auth   string
		status       int
		code         string
		challengeHas string
	}{
		{"missing", "", 401, "authentication_required", `Bearer realm="deedglass"`},
		{"malformed", "Bearer sk_live_abc", 401, "invalid_api_key", `error="invalid_token"`},
		{"basic scheme", "Basic Zm9vOmJhcg==", 401, "invalid_api_key", `error="invalid_token"`},
		{"unknown prefix", "Bearer " + unknown, 401, "invalid_api_key", `error="invalid_token"`},
		{"wrong secret", "Bearer " + wrongSecret, 401, "invalid_api_key", `error="invalid_token"`},
		{"wrong mode", "Bearer " + wrongMode, 401, "invalid_api_key", `error="invalid_token"`},
		{"revoked", "Bearer " + *revoked.Secret, 401, "api_key_revoked", `error_description="The API key has been revoked. Create a new key in Console > Agents."`},
		{"read-only key on deeds", "Bearer " + readOnly, 403, "insufficient_scope", `error="insufficient_scope", scope="deeds:read"`},
		{"good", "Bearer " + good, 200, "", ""},
	}
	for _, c := range cases {
		hdr := []string{}
		if c.auth != "" {
			hdr = []string{"Authorization", c.auth}
		}
		r := w.call(t, "", "GET", path, "", hdr...)
		if r.status != c.status || r.code() != c.code {
			t.Errorf("%s: %d %s", c.name, r.status, r.body)
			continue
		}
		if ch := r.header.Get("WWW-Authenticate"); !strings.Contains(ch, c.challengeHas) {
			t.Errorf("%s: challenge %q lacks %q", c.name, ch, c.challengeHas)
		}
	}
	// Unknown prefix and wrong secret are indistinguishable.
	r1 := w.call(t, unknown, "GET", path, "")
	r2 := w.call(t, wrongSecret, "GET", path, "")
	if r1.message() != r2.message() || r1.header.Get("WWW-Authenticate") != r2.header.Get("WWW-Authenticate") {
		t.Fatal("an unknown prefix and a wrong secret must look the same")
	}
	// Combinations: revoked beats stopped; a bad secret beats revoked.
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""), 200, "")
	expect(t, w.call(t, *revoked.Secret, "GET", path, ""), 401, "api_key_revoked")
	rp, _ := identity.ParseAPIKey(*revoked.Secret)
	expect(t, w.call(t, identity.DisplayPrefix(rp.Mode, rp.Prefix)+"_"+strings.Repeat("C", 32), "GET", path, ""), 401, "invalid_api_key")
	expect(t, w.call(t, good, "GET", path, ""), 403, "agent_stopped")
	// org stopped beats agent stopped; suspended is checked after stopped.
	expect(t, w.call(t, f.admin, "POST", "/v1/org/stop-all", ""), 200, "")
	r := w.call(t, good, "GET", path, "")
	expect(t, r, 403, "org_stopped")
	if r.message() != "All agents in Acme are stopped. An owner can start them again in Console › Settings." {
		t.Fatalf("org_stopped: %s", r.message())
	}
}

func TestAgentKeysNeverManage(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	k := *f.key(t, a.ID, "").Secret
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/v1/agents/" + a.ID + "/start", `{"reason":"let me out"}`},
		{"POST", "/v1/agents/" + a.ID + "/stop", ""},
		{"PATCH", "/v1/agents/" + a.ID, `{"labels":{"env":"dev"}}`},
		{"POST", "/v1/agents/" + a.ID + "/keys", `{}`},
		{"GET", "/v1/agents", ""},
		{"POST", "/v1/org/stop-all", ""},
		{"GET", "/v1/deeds/export", ""},
		{"GET", "/v1/ledger/check", ""},
	} {
		r := w.call(t, k, c.method, c.path, c.body)
		if r.status != 403 || r.code() != "insufficient_scope" {
			t.Errorf("%s %s: %d %s", c.method, c.path, r.status, r.body)
		}
		if c.path == "/v1/agents" && r.message() != "This is an agent key, and managing agents needs an admin key. Create one with deedglass org keys create on the server." {
			t.Errorf("message: %s", r.message())
		}
	}
	expect(t, w.call(t, k, "POST", "/v1/org/start-all", `{"reason":"let me out"}`), 403, "insufficient_role")
}

func TestAdminKeys(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""), 200, "")
	expect(t, w.call(t, f.admin, "POST", "/v1/org/stop-all", ""), 200, "")
	r := w.call(t, f.admin, "POST", "/v1/org/start-all", `{"reason":"ok then"}`)
	expect(t, r, 403, "insufficient_role")
	expect(t, w.call(t, f.admin, "GET", "/v1/whoami", ""), 200, "")
	r = w.call(t, f.admin, "POST", "/v1/org/keys", `{"name":"another"}`)
	expect(t, r, 403, "insufficient_role")
	expect(t, w.call(t, f.admin, "GET", "/v1/org/keys", ""), 403, "insufficient_role")
	// While the org is stopped, admin keys can still look around (GET), but nothing that changes
	// state goes through (DL-022). Agent keys can't even read.
	expect(t, w.call(t, f.admin, "GET", "/v1/agents", ""), 200, "")
	expect(t, w.call(t, f.admin, "GET", "/v1/agents/"+a.ID, ""), 200, "")
	expect(t, w.call(t, f.admin, "GET", "/v1/agents/"+a.ID+"/keys", ""), 200, "")
	expect(t, w.call(t, f.admin, "GET", "/v1/deeds?limit=1", ""), 200, "")
	expect(t, w.call(t, f.admin, "GET", "/v1/ledger/check", ""), 200, "")
	r = w.call(t, f.admin, "POST", "/v1/agents", `{"name":"during-stop"}`)
	expect(t, r, 403, "org_stopped")
	if !strings.Contains(r.message(), "admin keys can only read") {
		t.Fatalf("message: %s", r.message())
	}
	expect(t, w.call(t, f.admin, "PATCH", "/v1/agents/"+a.ID, `{"description":"x"}`), 403, "org_stopped")
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/start", `{"reason":"sneaky"}`), 403, "org_stopped")
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/keys", `{}`), 403, "org_stopped")
	// An owner's and an admin's Console sessions keep working as before.
	expect(t, f.owner.do("GET", "/v1/agents", ""), 200, "")
	expect(t, f.owner.do("PATCH", "/v1/agents/"+a.ID, `{"description":"during the incident"}`), 200, "")

	// An owner (session) starts all, mints an admin key, and an admin key can't roll it.
	r = f.owner.do("POST", "/v1/org/start-all", `{"reason":"Root cause fixed"}`)
	expect(t, r, 200, "")
	r = f.owner.do("POST", "/v1/org/keys", `{"name":"deploys"}`)
	expect(t, r, 201, "")
	var ak identity.KeyResult
	r.json(t, &ak)
	if ak.Agent != nil || strings.Join(ak.Scopes, ",") != "admin" || ak.Secret == nil {
		t.Fatalf("admin key: %s", r.body)
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/keys/"+ak.ID+"/roll", ""), 403, "insufficient_role")
	expect(t, f.owner.do("GET", "/v1/org/keys", ""), 200, "")
	// A viewer can read agents but not mint admin keys.
	_, viewer := w.invite(t, f.owner, "sam@acme.com", "viewer")
	expect(t, viewer.do("GET", "/v1/agents", ""), 200, "")
	expect(t, viewer.do("POST", "/v1/org/keys", `{}`), 403, "insufficient_role")
	expect(t, viewer.do("POST", "/v1/agents/"+a.ID+"/start", `{"reason":"viewer"}`), 403, "insufficient_role")
	// An admin session can stop all but not start all.
	_, adminP := w.invite(t, f.owner, "ali@acme.com", "admin")
	expect(t, adminP.do("POST", "/v1/org/stop-all", ""), 200, "")
	expect(t, adminP.do("POST", "/v1/org/start-all", `{"reason":"admins can't"}`), 403, "insufficient_role")
}

func TestRolling(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	old := f.key(t, a.ID, "")
	r := w.call(t, f.admin, "POST", "/v1/keys/"+old.ID+"/roll", `{"expires_in":86400}`)
	expect(t, r, 201, "")
	var nk identity.KeyResult
	r.json(t, &nk)
	if nk.RolledFrom == nil || *nk.RolledFrom != old.ID || nk.Secret == nil || nk.Previous == nil || nk.Previous.ExpiresAt == nil ||
		nk.Previous.Prefix != old.Prefix {
		t.Fatalf("roll: %s", r.body)
	}
	until := time.Time(*nk.Previous.ExpiresAt)
	if d := until.Sub(time.Now()); d < 23*time.Hour+59*time.Minute || d > 24*time.Hour+time.Minute {
		t.Fatalf("overlap ends in %v", d)
	}
	// The old key works until the overlap ends, then api_key_expired (the clock is the key
	// authenticator's).
	base := w.clock.Now()
	w.clock.Add(until.Sub(base) - time.Second)
	expect(t, w.call(t, *old.Secret, "GET", "/v1/deeds?limit=1", ""), 200, "")
	expect(t, w.call(t, *nk.Secret, "GET", "/v1/deeds?limit=1", ""), 200, "")
	w.clock.Add(2 * time.Second)
	r = w.call(t, *old.Secret, "GET", "/v1/deeds?limit=1", "")
	expect(t, r, 401, "api_key_expired")
	if !strings.Contains(r.message(), until.UTC().Format("2006-01-02 15:04")+" UTC after it was rolled") {
		t.Fatalf("expired message: %s", r.message())
	}
	expect(t, w.call(t, *nk.Secret, "GET", "/v1/deeds?limit=1", ""), 200, "")
	expect(t, w.call(t, f.admin, "POST", "/v1/keys/"+old.ID+"/roll", ""), 409, "conflict") // expired

	// Overlap 0: the old key stops at once. A revoked key can't be rolled.
	k2 := f.key(t, a.ID, "")
	r = w.call(t, f.admin, "POST", "/v1/keys/"+k2.ID+"/roll", `{"expires_in":0}`)
	expect(t, r, 201, "")
	expect(t, w.call(t, *k2.Secret, "GET", "/v1/deeds?limit=1", ""), 401, "api_key_expired")
	k3 := f.key(t, a.ID, "")
	expect(t, w.call(t, f.admin, "POST", "/v1/keys/"+k3.ID+"/revoke", `{"reason":"leaked"}`), 200, "")
	expect(t, w.call(t, f.admin, "POST", "/v1/keys/"+k3.ID+"/roll", ""), 409, "conflict")
	r = w.call(t, f.admin, "POST", "/v1/keys/"+k3.ID+"/revoke", "")
	expect(t, r, 200, "")
	if !strings.Contains(flat(r), `"deed":null`) {
		t.Fatalf("a repeated revoke sealed again: %s", r.body)
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/keys/"+k3.ID+"/roll", `{"expires_in":604801}`), 400, "validation_failed")
}

func TestStopAllOverlay(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	b := f.agent(t, "refund-agent")
	ka, kb := *f.key(t, a.ID, "").Secret, *f.key(t, b.ID, "").Secret
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""), 200, "")
	r := w.call(t, f.admin, "POST", "/v1/org/stop-all", `{"reason":"Investigating unexpected refunds"}`)
	expect(t, r, 200, "")
	var st identity.OrgStop
	r.json(t, &st)
	if !st.Stopped || st.AgentsAffected != 1 || st.Deed == nil || st.Reason == nil || st.StoppedBy == nil {
		t.Fatalf("stop-all: %s", r.body)
	}
	expect(t, w.call(t, kb, "GET", "/v1/deeds?limit=1", ""), 403, "org_stopped")
	// Start all needs a reason, and leaves A stopped.
	expect(t, f.owner.do("POST", "/v1/org/start-all", ``), 400, "validation_failed")
	r = f.owner.do("POST", "/v1/org/start-all", `{"reason":"Root cause fixed: refund threshold was 100x"}`)
	expect(t, r, 200, "")
	r.json(t, &st)
	if st.Stopped || st.Deed == nil {
		t.Fatalf("start-all: %s", r.body)
	}
	expect(t, w.call(t, ka, "GET", "/v1/deeds?limit=1", ""), 403, "agent_stopped")
	expect(t, w.call(t, kb, "GET", "/v1/deeds?limit=1", ""), 200, "")
	r = f.owner.do("POST", "/v1/org/start-all", `{"reason":"again"}`)
	if r.status != 200 || !strings.Contains(flat(r), `"deed":null`) {
		t.Fatalf("a repeated start-all: %s", r.body)
	}
}

func TestNotifyFailureChangesNothing(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	before := countRows(t, w, f, "admin_events") + countRows(t, w, f, "deeds")
	identity.SetFailNotify(w.svc, func() error { return errors.New("notification queue is full") })
	r := w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", `{"reason":"x"}`)
	identity.SetFailNotify(w.svc, nil)
	expect(t, r, 500, "internal_error")
	if r.message() != "Deedglass couldn't record the stop, so nothing changed. Try again. If it keeps failing, revoke the agent's keys." {
		t.Fatalf("message: %s", r.message())
	}
	r = w.call(t, f.admin, "GET", "/v1/agents/"+a.ID, "")
	if !strings.Contains(flat(r), `"status":"active"`) {
		t.Fatalf("status changed: %s", r.body)
	}
	if after := countRows(t, w, f, "admin_events") + countRows(t, w, f, "deeds"); after != before {
		t.Fatalf("rows %d → %d", before, after)
	}
}

func countRows(t testing.TB, w *world, f *fleet, table string) int {
	t.Helper()
	var n int
	if err := w.e.Admin(t).QueryRow(ctx, `SELECT count(*) FROM deedglass.`+table+` WHERE org_id = $1`, f.org.Org.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReplayedKeyCreateHidesTheSecret(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	r1 := w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/keys", `{"name":"prod runner"}`, "Idempotency-Key", "5f0c1e9e-key-1")
	expect(t, r1, 201, "")
	r2 := w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/keys", `{"name":"prod runner"}`, "Idempotency-Key", "5f0c1e9e-key-1")
	if r2.status != 201 || r2.header.Get("Deedglass-Idempotent-Replayed") != "true" || !strings.Contains(flat(r2), `"secret":null`) {
		t.Fatalf("replay: %d %v %s", r2.status, r2.header, r2.body)
	}
	var k1, k2 identity.KeyResult
	r1.json(t, &k1)
	r2.json(t, &k2)
	if k1.ID != k2.ID || k1.Secret == nil {
		t.Fatal("the replay must be the same key")
	}
}

func TestLastUsedIsBatched(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	k := f.key(t, a.ID, "")
	b := f.agent(t, "refund-agent")
	kb := f.key(t, b.ID, "")
	for range 100 {
		expect(t, w.call(t, *k.Secret, "GET", "/v1/whoami", ""), 200, "")
	}
	expect(t, w.call(t, *kb.Secret, "GET", "/v1/whoami", ""), 200, "")
	n, err := w.keys.FlushLastUsed(ctx)
	if err != nil || n != 1 {
		t.Fatalf("flush: %d updates (want 1, one per org) %v", n, err)
	}
	r := w.call(t, f.admin, "GET", "/v1/agents/"+a.ID+"/keys", "")
	var l httpx.List[identity.APIKey]
	r.json(t, &l)
	found := false
	for _, x := range l.Data {
		if x.ID == k.ID {
			found = x.LastUsedAt != nil && time.Time(*x.LastUsedAt).Sub(w.clock.Now()).Abs() < time.Minute
		}
	}
	if !found {
		t.Fatalf("last_used_at: %s", r.body)
	}
	_, _ = w.keys.FlushLastUsed(ctx) // the admin key's own use above
	if n, _ := w.keys.FlushLastUsed(ctx); n != 0 {
		t.Fatalf("an empty flush ran %d updates", n)
	}
}

func TestFailedAuthLimiter(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	good := *f.key(t, a.ID, "").Secret
	bad := func(ip string, i int) resp {
		return w.call(t, fmt.Sprintf("dg_live_Bad%05d_%s", i, strings.Repeat("x", 32)), "GET", "/v1/whoami", "", "X-Forwarded-For", ip)
	}
	for i := range 10 {
		expect(t, bad("198.51.100.1", i), 401, "invalid_api_key")
	}
	r := bad("198.51.100.1", 10)
	expect(t, r, 429, "rate_limited")
	if r.header.Get("Retry-After") != "1" || r.message() != "Too many requests with invalid API keys from this address. Try again in 1 seconds." {
		t.Fatalf("first penalty: %v %s", r.header, r.message())
	}
	w.clock.Add(1100 * time.Millisecond)
	expect(t, bad("198.51.100.1", 11), 401, "invalid_api_key") // the penalty ended; this failure doubles it
	r = bad("198.51.100.1", 12)
	if r.status != 429 || r.header.Get("Retry-After") != "2" {
		t.Fatalf("doubled penalty: %d %v", r.status, r.header)
	}
	// Another address with a good key is unaffected; successes never count.
	expect(t, w.call(t, good, "GET", "/v1/whoami", "", "X-Forwarded-For", "198.51.100.2"), 200, "")
	// Ten clean minutes reset it.
	w.clock.Add(11 * time.Minute)
	expect(t, w.call(t, good, "GET", "/v1/whoami", "", "X-Forwarded-For", "198.51.100.1"), 200, "")
}

func TestAgentRLS(t *testing.T) {
	w := newWorld(t)
	acme := newFleet(t, w, "Acme", "priya@acme.com")
	beta := newFleet(t, w, "Beta", "bo@beta.io")
	a := acme.agent(t, "support-bot")
	ka := acme.key(t, a.ID, "")
	expect(t, w.call(t, beta.admin, "GET", "/v1/agents/"+a.ID, ""), 404, "not_found")
	expect(t, w.call(t, beta.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""), 404, "not_found")
	expect(t, w.call(t, beta.admin, "POST", "/v1/keys/"+ka.ID+"/revoke", ""), 404, "not_found")
	r := w.call(t, beta.admin, "GET", "/v1/agents", "")
	if strings.Contains(string(r.body), a.ID) {
		t.Fatal("another org's agent is listed")
	}
	// Directly, under RLS: Beta's transaction sees none of Acme's agents or keys.
	var n int
	if err := w.e.DB.OrgReadTx(ctx, beta.org.Org.ID, func(c2 context.Context, tx pgx.Tx) error {
		return tx.QueryRow(c2, `SELECT (SELECT count(*) FROM deedglass.agents) + (SELECT count(*) FROM deedglass.api_keys WHERE agent_id IS NOT NULL)`).Scan(&n)
	}); err != nil || n != 0 {
		t.Fatalf("Beta sees %d of Acme's rows (%v)", n, err)
	}
}

func TestAdminDeedsChain(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	k := f.key(t, a.ID, "")
	steps := []struct{ method, path, body string }{
		{"PATCH", "/v1/agents/" + a.ID, `{"labels":{"env":"prod"}}`},
		{"POST", "/v1/agents/" + a.ID + "/suspend", ""},
		{"POST", "/v1/agents/" + a.ID + "/resume", ""},
		{"POST", "/v1/agents/" + a.ID + "/stop", `{"reason":"Refund loop"}`},
		{"POST", "/v1/agents/" + a.ID + "/start", `{"reason":"fixed"}`},
		{"POST", "/v1/keys/" + k.ID + "/roll", `{"expires_in":60}`},
		{"POST", "/v1/keys/" + k.ID + "/revoke", ""},
		{"POST", "/v1/org/stop-all", ""},
	}
	for _, s := range steps {
		if r := w.call(t, f.admin, s.method, s.path, s.body); r.status/100 != 2 {
			t.Fatalf("%s %s: %d %s", s.method, s.path, r.status, r.body)
		}
	}
	expect(t, f.owner.do("POST", "/v1/org/start-all", `{"reason":"ok"}`), 200, "")
	rows, err := w.e.Admin(t).Query(ctx, `SELECT DISTINCT action FROM deedglass.admin_events WHERE org_id = $1 AND deed_id IS NOT NULL`, f.org.Org.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		got[s] = true
	}
	for _, want := range []string{ledger.AdminAgentCreated, ledger.AdminAgentUpdated, ledger.AdminAgentSuspended, ledger.AdminAgentResumed,
		ledger.AdminAgentStopped, ledger.AdminAgentStarted, ledger.AdminOrgStoppedAll, ledger.AdminOrgStartedAll,
		ledger.AdminAPIKeyCreated, ledger.AdminAPIKeyRolled, ledger.AdminAPIKeyRevoked} {
		if !got[want] {
			t.Errorf("no sealed %s", want)
		}
	}
	res, err := w.led.CheckChain(ctx, f.org.Org.ID, ledger.Range{}, ledger.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != ledger.CheckIntact {
		b, _ := json.Marshal(res)
		t.Fatalf("chain: %s", b)
	}
	// The admin key is the actor of what it changed.
	var actor string
	if err := w.e.Admin(t).QueryRow(ctx, `SELECT actor_type FROM deedglass.admin_events WHERE org_id = $1 AND action = 'agent.stopped'`, f.org.Org.ID).Scan(&actor); err != nil || actor != "api_key" {
		t.Fatalf("actor %q %v", actor, err)
	}
}

func TestAgentKeyReadsOnlyItsOwnRows(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	b := f.agent(t, "refund-agent")
	ka := *f.key(t, a.ID, "").Secret
	// Admin deeds exist for both agents; an agent key sees no admin deeds at all.
	r := w.call(t, ka, "GET", "/v1/deeds", "")
	expect(t, r, 200, "")
	if strings.Contains(string(r.body), b.ID) || strings.Contains(flat(r), `"kind":"admin"`) {
		t.Fatalf("an agent key read other rows: %s", r.body)
	}
	expect(t, w.call(t, ka, "GET", "/v1/deeds/"+*b.Deed, ""), 404, "not_found")
	expect(t, w.call(t, ka, "GET", "/v1/actions?agent="+b.ID, ""), 400, "validation_failed")
	expect(t, w.call(t, ka, "GET", "/v1/actions", ""), 200, "")
	// The owner sees everything.
	r = f.owner.do("GET", "/v1/deeds?kind=admin", "")
	if !strings.Contains(string(r.body), b.ID) {
		t.Fatalf("owner: %s", r.body)
	}
	expect(t, w.call(t, f.admin, "GET", "/v1/deeds/"+*b.Deed, ""), 200, "")
}

func TestNoKeyInLogs(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	k := f.key(t, a.ID, "")
	expect(t, w.call(t, *k.Secret, "GET", "/v1/whoami", ""), 200, "")
	expect(t, w.call(t, *k.Secret+"x", "GET", "/v1/whoami", ""), 401, "invalid_api_key")
	r := w.call(t, f.admin, "POST", "/v1/keys/"+k.ID+"/roll", "")
	var nk identity.KeyResult
	r.json(t, &nk)
	logs := w.logText()
	for _, s := range []string{*k.Secret, *nk.Secret, f.admin} {
		p, _ := identity.ParseAPIKey(s)
		if strings.Contains(logs, s) || strings.Contains(logs, p.Secret) {
			t.Fatalf("a key or its secret is in the log")
		}
	}
	if strings.Contains(logs, string([]byte{9, 9, 9, 9})) {
		t.Fatal("the pepper is in the log")
	}
}

func TestOwnerAndGuardCache(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	owner := f.org.Owner.ID.String()
	// A session creates: the person is the owner by default. An admin key can name one.
	r := f.owner.do("POST", "/v1/agents", `{"name":"support-bot"}`)
	expect(t, r, 201, "")
	var a identity.AgentResult
	r.json(t, &a)
	if a.Owner == nil || *a.Owner != owner {
		t.Fatalf("owner: %s", r.body)
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/agents", `{"name":"x-1","owner":"usr_01k6c9kb2zcjsyn7b8g2k1vqvp"}`), 400, "validation_failed")
	expect(t, w.call(t, f.admin, "POST", "/v1/agents", `{"name":"x-1","owner":"nobody"}`), 400, "validation_failed")
	r = w.call(t, f.admin, "PATCH", "/v1/agents/"+a.ID, `{"owner":""}`)
	expect(t, r, 200, "")
	r.json(t, &a)
	if a.Owner != nil || a.Deed == nil {
		t.Fatalf("clear owner: %s", r.body)
	}
	r = w.call(t, f.admin, "PATCH", "/v1/agents/"+a.ID, `{"owner":"`+owner+`","budgets":{"calls_per_day":10}}`)
	expect(t, r, 200, "")
	r.json(t, &a)
	if a.Owner == nil || *a.Budgets.CallsPerDay != 10 {
		t.Fatalf("set owner: %s", r.body)
	}
	k := f.key(t, a.ID, "")
	if got, err := w.svc.GetKey(ctx, f.org.Org.ID, k.ID); err != nil || got.Prefix != k.Prefix {
		t.Fatalf("GetKey: %+v %v", got, err)
	}
	if _, err := w.svc.GetKey(ctx, f.org.Org.ID, "key_nope"); err == nil {
		t.Fatal("GetKey of a bad id")
	}

	// The Guard from the cache: a miss loads and caches, a hit answers without Postgres.
	waitHealthy(t, w.keys)
	agentID, _ := ids.Parse[ids.AgentKind](a.ID)
	expect(t, w.call(t, *k.Secret, "GET", "/v1/whoami", ""), 200, "") // fills the org entry
	for range 2 {
		if err := w.keys.CheckActive(ctx, f.org.Org.ID, agentID, false); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/suspend", ""), 200, "")
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := w.keys.CheckActive(ctx, f.org.Org.ID, agentID, false)
		if he := httpx.AsError(err); he != nil && he.Code == httpx.CodeAgentSuspended && errors.Is(err, identity.ErrAgentSuspended) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %v 2 s after the suspend", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	missing := ids.New[ids.AgentKind]()
	if err := w.keys.CheckActive(ctx, f.org.Org.ID, missing, true); !errors.Is(err, identity.ErrAgentStopped) {
		t.Fatalf("an unknown agent must never act: %v", err)
	}
}
