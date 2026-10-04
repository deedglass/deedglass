//go:build integration

package identity_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/deedglass/deedglass/api"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger/fake"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
)

// TestAgentsExpandUsage (S25 §3.2): ?expand[]=usage adds S11's counters and last_action_at to every
// agent of a page and to one agent; the values equal the usage store's snapshot for the same
// windows; without expand the agent object is unchanged; any other expand is a 400; agent keys
// stay refused.
func TestAgentsExpandUsage(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "owner@acme.example.test")
	a := f.agent(t, "support-bot")
	b := f.agent(t, "billing-bot")
	idle := f.agent(t, "idle-bot")
	// billing-bot counts spend in eur.
	r := w.call(t, f.admin, "PATCH", "/v1/agents/"+b.ID, `{"budgets":{"calls_per_day":100,"spend_per_day":{"amount":5000,"currency":"eur"}}}`)
	expect(t, r, 200, "")
	org := f.org.Org.ID
	now := w.clock.Now()
	u := policystore.NewUsage(w.e.DB)
	res := func(agent, counter, window string, n int64) policy.Reservation {
		return policy.Reservation{Scope: agent, Counter: counter, Window: window, WindowStart: policy.WindowStart(window, now, time.UTC), Amount: n}
	}
	if err := u.Reserve(ctx, org.String(), []policy.Reservation{
		res(a.ID, policy.CounterCalls, policy.WindowMinute, 2), res(a.ID, policy.CounterCalls, policy.WindowHour, 17),
		res(a.ID, policy.CounterCalls, policy.WindowDay, 212), res(a.ID, policy.SpendCounter("usd"), policy.WindowDay, 1200),
		res(b.ID, policy.CounterCalls, policy.WindowDay, 9), res(b.ID, policy.SpendCounter("eur"), policy.WindowDay, 300),
		res(b.ID, policy.SpendCounter("usd"), policy.WindowDay, 999), // another currency: not billing-bot's
		// The previous day's counter isn't today's.
		{Scope: a.ID, Counter: policy.CounterCalls, Window: policy.WindowDay, WindowStart: policy.WindowStart(policy.WindowDay, now, time.UTC).Add(-24 * time.Hour), Amount: 5000},
	}); err != nil {
		t.Fatal(err)
	}
	// Two actions for support-bot, so it has a last action.
	aID, err := ids.Parse[ids.AgentKind](a.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := fake.Run(ctx, w.led, org, []store.Agent{{ID: aID, OrgID: org, Name: "support-bot"}}, fake.Generate(1, 2, []fake.Kind{fake.Allow}, 1), fake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var lastAt time.Time
	if err := w.e.Admin(t).QueryRow(ctx, `SELECT max(created_at) FROM deedglass.actions WHERE id = ANY($1)`, run.IDs).Scan(&lastAt); err != nil {
		t.Fatal(err)
	}

	type usage struct {
		CallsMinute int64 `json:"calls_minute"`
		CallsHour   int64 `json:"calls_hour"`
		CallsToday  int64 `json:"calls_today"`
		SpendToday  *struct {
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
		} `json:"spend_today"`
	}
	type expanded struct {
		ID           string  `json:"id"`
		Usage        *usage  `json:"usage"`
		LastActionAt *string `json:"last_action_at"`
	}
	matches := func(e expanded, cur string, hasBudget bool) {
		t.Helper()
		snap, err := u.Snapshot(ctx, policy.SnapshotRequest{Org: org.String(), Agent: e.ID, Location: time.UTC, At: now, Currency: cur})
		if err != nil {
			t.Fatal(err)
		}
		if e.Usage == nil || e.Usage.CallsMinute != snap.CallsMinute || e.Usage.CallsHour != snap.CallsHour || e.Usage.CallsToday != snap.CallsToday {
			t.Errorf("%s: usage %+v, the store says %+v", e.ID, e.Usage, snap)
			return
		}
		switch {
		case snap.SpendToday == 0 && !hasBudget:
			if e.Usage.SpendToday != nil {
				t.Errorf("%s: spend_today %+v, want null", e.ID, e.Usage.SpendToday)
			}
		case e.Usage.SpendToday == nil || e.Usage.SpendToday.Amount != snap.SpendToday || e.Usage.SpendToday.Currency != cur:
			t.Errorf("%s: spend_today %+v, the store says %d %s", e.ID, e.Usage.SpendToday, snap.SpendToday, cur)
		}
	}

	// The list, both spellings.
	for _, q := range []string{"?expand[]=usage", "?expand=usage&limit=10"} {
		r := w.call(t, f.admin, "GET", "/v1/agents"+q, "")
		expect(t, r, 200, "")
		var list struct {
			Data []expanded `json:"data"`
		}
		r.json(t, &list)
		if len(list.Data) != 3 {
			t.Fatalf("%s: %d agents", q, len(list.Data))
		}
		for _, e := range list.Data {
			switch e.ID {
			case a.ID:
				matches(e, "usd", false)
				if e.Usage.CallsToday != 212 || e.Usage.SpendToday == nil || e.Usage.SpendToday.Amount != 1200 {
					t.Errorf("support-bot: %+v", e.Usage)
				}
				if e.LastActionAt == nil || *e.LastActionAt != lastAt.UTC().Format("2006-01-02T15:04:05.000Z07:00") {
					t.Errorf("support-bot last_action_at %v, want %s", e.LastActionAt, lastAt)
				}
			case b.ID:
				matches(e, "eur", true)
				if e.Usage.SpendToday == nil || e.Usage.SpendToday.Amount != 300 || e.Usage.SpendToday.Currency != "eur" {
					t.Errorf("billing-bot: %+v", e.Usage.SpendToday)
				}
				if e.LastActionAt != nil {
					t.Errorf("billing-bot has no action but last_action_at %s", *e.LastActionAt)
				}
			case idle.ID:
				matches(e, "usd", false)
			}
		}
	}
	// One agent.
	r = w.call(t, f.admin, "GET", "/v1/agents/"+a.ID+"?expand[]=usage", "")
	expect(t, r, 200, "")
	for _, err := range httpx.CheckRequired(api.Spec, "Agent", r.body) {
		t.Errorf("api/openapi.yaml Agent: %v", err)
	}
	var withUsage struct {
		Usage json.RawMessage `json:"usage"`
	}
	r.json(t, &withUsage)
	for _, err := range httpx.CheckRequired(api.Spec, "AgentUsage", withUsage.Usage) {
		t.Errorf("api/openapi.yaml AgentUsage: %v", err)
	}
	var one expanded
	r.json(t, &one)
	matches(one, "usd", false)

	// Without expand: exactly the agent object's keys, as before S25.
	golden, err := os.ReadFile(filepath.Join("testdata", "agent_object.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	keys := func(b []byte) []string {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	want := keys(golden)
	r = w.call(t, f.admin, "GET", "/v1/agents/"+a.ID, "")
	expect(t, r, 200, "")
	if got := keys(r.body); !slices.Equal(got, want) {
		t.Errorf("GET /v1/agents/{id} keys %v, want %v", got, want)
	}
	plain, err := w.svc.GetAgent(ctx, org, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := json.Marshal(plain)
	if flat(r) != string(pb) {
		t.Errorf("GET /v1/agents/{id} without expand:\n%s\nwant\n%s", flat(r), pb)
	}
	r = w.call(t, f.admin, "GET", "/v1/agents", "")
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	r.json(t, &list)
	for _, d := range list.Data {
		if got := keys(d); !slices.Equal(got, want) {
			t.Errorf("GET /v1/agents keys %v, want %v", got, want)
		}
	}

	// Refusals.
	for _, p := range []string{"/v1/agents?expand[]=keys", "/v1/agents/" + a.ID + "?expand=witness.detail", "/v1/agents/" + a.ID + "?limit=1"} {
		expect(t, w.call(t, f.admin, "GET", p, ""), 400, "validation_failed")
	}
	k := f.key(t, a.ID, "")
	expect(t, w.call(t, *k.Secret, "GET", "/v1/agents?expand[]=usage", ""), 403, "insufficient_scope")
	// Another org's agent is 404 with or without the expansion.
	g := newFleet(t, w, "Globex", "owner@globex.example.test")
	expect(t, w.call(t, g.admin, "GET", "/v1/agents/"+a.ID+"?expand[]=usage", ""), 404, "not_found")
	r = w.call(t, g.admin, "GET", "/v1/agents?expand[]=usage", "")
	expect(t, r, 200, "")
	if flat(r) != `{"object":"list","data":[],"has_more":false,"next_cursor":null}` {
		t.Errorf("globex's list: %s", r.body)
	}
}
