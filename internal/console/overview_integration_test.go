//go:build integration

package console

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	apispec "github.com/deedglass/deedglass/api"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestOverviewLive runs GET /v1/overview on a throwaway cluster: a real Ledger, S11's usage
// store, the background chain check noticing a rewritten deed, and two orgs that never see each
// other through the route.
func TestOverviewLive(t *testing.T) {
	ctx := context.Background()
	e := storetest.New(t, storetest.Options{MaxConns: 8})
	seed, err := deed.NewSeed(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	led := ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(slog.New(slog.DiscardHandler)))
	newOrg := func(slug string) (ids.OrgID, []store.Agent) {
		o, _, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: "Org " + slug, Slug: slug, OwnerEmail: "owner@" + slug + ".example.test"})
		if err != nil {
			t.Fatal(err)
		}
		var agents []store.Agent
		if err := e.DB.OrgTx(ctx, o.ID, func(ctx context.Context, tx pgx.Tx) error {
			for _, n := range fake.AgentNames[:2] {
				a, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: n, CreatedBy: "system"})
				if err != nil {
					return err
				}
				agents = append(agents, a)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return o.ID, agents
	}
	acme, acmeAgents := newOrg("acme")
	globex, globexAgents := newOrg("globex")
	if _, err := fake.Run(ctx, led, acme, acmeAgents, fake.Generate(11, 30, []fake.Kind{fake.Allow, fake.Deny, fake.Mismatch}, 2), fake.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Run(ctx, led, globex, globexAgents, fake.Generate(12, 5, []fake.Kind{fake.Allow}, 2), fake.Options{}); err != nil {
		t.Fatal(err)
	}
	u := policystore.NewUsage(e.DB)
	now := time.Now().UTC()
	if err := u.Reserve(ctx, acme.String(), []policy.Reservation{{Scope: acmeAgents[0].ID.String(), Counter: policy.CounterCalls,
		Window: policy.WindowDay, WindowStart: policy.WindowStart(policy.WindowDay, now, time.UTC), Amount: 30}}); err != nil {
		t.Fatal(err)
	}

	ov := NewOverview(OverviewOptions{Ledger: led, Usage: UsageFromStore(u), SlotLagAlertBytes: 512 << 20, TTL: -1})
	t.Cleanup(ov.Wait)
	serve := func(org ids.OrgID) func(string) *ledger.Overview {
		a := &evAuth{org: org, agent: ids.New[ids.AgentKind](), user: ids.New[ids.UserKind]()}
		api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Keys: a, Sessions: a})
		api.Mount(ov.Routes()...)
		return func(q string) *ledger.Overview {
			r := httptest.NewRequest("GET", "/v1/overview"+q, nil)
			r.AddCookie(&http.Cookie{Name: "deedglass_session", Value: evCookie})
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("HTTP %d %s", w.Code, w.Body)
			}
			var out ledger.Overview
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			for _, err := range httpx.CheckRequired(apispec.Spec, "Overview", w.Body.Bytes()) {
				t.Errorf("api/openapi.yaml Overview: %v", err)
			}
			return &out
		}
	}
	getAcme, getGlobex := serve(acme), serve(globex)

	first := getAcme("")
	if first.Actions.Total != 30 || len(first.Agents.Data) != 2 {
		t.Fatalf("acme: %d actions, %d agents", first.Actions.Total, len(first.Agents.Data))
	}
	for _, it := range first.Attention {
		if it.Kind == ledger.AttentionChainBroken {
			t.Fatal("chain_broken before any check ran")
		}
	}
	var sawUsage bool
	for _, a := range first.Agents.Data {
		if a.ID == acmeAgents[0].ID.String() && a.Usage.CallsToday == 30 {
			sawUsage = true
		}
	}
	if !sawUsage {
		t.Errorf("the usage store's 30 calls aren't on the strip: %+v", first.Agents.Data)
	}
	g := getGlobex("?window=7d")
	if g.Actions.Total != 5 {
		t.Fatalf("globex sees %d actions, want its own 5", g.Actions.Total)
	}
	gb, _ := json.Marshal(g)
	for _, a := range acmeAgents {
		if bytes.Contains(gb, []byte(a.ID.String())) {
			t.Fatalf("globex's overview names acme's agent %s", a.ID)
		}
	}

	// Rewrite one of acme's deeds underneath Deedglass; the next background check finds it.
	ov.Wait()
	c := e.Admin(t)
	for _, q := range []string{"ALTER TABLE deedglass.deeds DISABLE TRIGGER deeds_append_only", "ALTER TABLE deedglass.deeds DISABLE TRIGGER deeds_append_only_stmt"} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var (
		env []byte
		seq int64
	)
	if err := c.QueryRow(ctx, `SELECT seq, envelope FROM deedglass.deeds WHERE org_id = $1 AND kind = 'action' ORDER BY seq DESC LIMIT 1`, acme).Scan(&seq, &env); err != nil {
		t.Fatal(err)
	}
	i := bytes.Index(env, []byte(`"tool":"`))
	if i < 0 {
		t.Fatalf("deed %d has no tool: %s", seq, env)
	}
	env[i+len(`"tool":"`)] ^= 0x01 // one letter changes
	if _, err := c.Exec(ctx, `UPDATE deedglass.deeds SET envelope = $1 WHERE org_id = $2 AND seq = $3`, env, acme, seq); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"ALTER TABLE deedglass.deeds ENABLE TRIGGER deeds_append_only", "ALTER TABLE deedglass.deeds ENABLE TRIGGER deeds_append_only_stmt"} {
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	ov.mu.Lock()
	ov.chains[acme].started = time.Time{} // as if 5 minutes had passed
	ov.mu.Unlock()
	_ = getAcme("") // starts the check
	ov.Wait()
	after := getAcme("")
	if len(after.Attention) == 0 || after.Attention[0].Kind != ledger.AttentionChainBroken || after.Attention[0].Severity != ledger.SeverityHigh {
		t.Fatalf("no chain_broken after the rewrite: %+v", after.Attention)
	}
	if code, _ := after.Attention[0].Data["code"].(string); code == "" {
		t.Errorf("chain_broken without its code: %+v", after.Attention[0].Data)
	}
	// globex's chain is its own: intact.
	_ = getGlobex("")
	ov.Wait()
	for _, it := range getGlobex("").Attention {
		if it.Kind == ledger.AttentionChainBroken {
			t.Fatal("acme's broken chain shows on globex's overview")
		}
	}
}
