//go:build integration

package gate

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fake"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var bg = context.Background()

// testPolicy is the policy the pipeline tests enforce: S13 §5's demo rules on the fake connector.
const testPolicy = `deedglass: policy/v1
name: gate-test
description: The Gate's integration tests.

defaults:
  decision: deny
  reason: No rule allows this action.

budgets:
  - id: agent-rate
    window: minute
    max_calls: 100000

rules:
  - id: no-bulk-writes
    match: { tool: [fake.write] }
    when: has(args.rows) && args.rows > 100
    decision: deny
    reason: writes over 100 rows need a person, not an agent.
  - id: writes-need-approval
    match: { tool: [fake.write] }
    when: has(args.table) && args.table == "approvals"
    decision: require_approval
    reason: Writes wait for a human.
    approval: { approvers: [role:admin], required: 1, expires_in: 1h }
  - id: allow-fake
    match: { tool: ["fake.*"] }
    decision: allow
    reason: Fake tools are safe to run.
  - id: allow-edge
    match: { tool: ["scratch.*", "pg.*"] }
    when: '!sql.present || sql.kind != "drop"'
    decision: allow
    reason: The test edge may run its tools.
`

type world struct {
	t      testing.TB
	e      *storetest.Env
	signer *deed.FileSigner
	led    *ledger.Ledger
	keys   *identity.KeyAuth
	reg    *policy.Registry
	svc    *policy.Service
	fake   *fake.Connector
	p      *Pipeline
	org    ids.OrgID
	owner  ids.UserID
	agents []store.Agent
	logs   *bytes.Buffer
}

type worldOpts struct {
	cfg       Config
	maxConns  int32
	policy    string // "" testPolicy
	agents    int
	clock     func() time.Time // nil: time.Now (S27: a virtual pipeline clock)
	anomalies AnomalySource    // S27: the opt-in anomaly facts
}

func newWorld(t testing.TB, o worldOpts) *world {
	t.Helper()
	if o.maxConns == 0 {
		o.maxConns = 20
	}
	if o.agents == 0 {
		o.agents = 3
	}
	if o.policy == "" {
		o.policy = testPolicy
	}
	e := storetest.New(t, storetest.Options{MaxConns: o.maxConns})
	seed, err := deed.NewSeed(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(bg, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, e: e, signer: signer, logs: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(syncWriter{w: w.logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w.led = ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(log))
	w.keys, err = identity.NewKeyAuth(identity.KeyAuthOptions{DB: e.DB, Pepper: bytes.Repeat([]byte{9}, 32), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	ch := policystore.NewChanges(w.keys.Listener())
	w.reg = policy.NewRegistry(policystore.NewRepo(e.DB, nil), ch, policy.WithRegistryLogger(log))
	w.svc = policy.NewService(policystore.NewRepo(e.DB, w.led), policy.WithRegistry(w.reg))
	w.fake = fake.New()
	w.p, err = New(Options{DB: e.DB, Ledger: w.led, Policies: w.reg, Usage: policystore.NewUsage(e.DB),
		Intents: identity.NewIntents(e.DB, w.keys, nil), Guard: w.keys, Resolver: connector.NewRegistry(w.fake),
		Config: o.cfg, Logger: log, Clock: o.clock, Anomalies: o.anomalies})
	if err != nil {
		t.Fatal(err)
	}
	w.keys.Listener().Listen(ledger.Channel, w.p.Broker().OnEvent, w.p.Broker().Reset)
	rctx, cancel := context.WithCancel(bg)
	var wg sync.WaitGroup
	if _, _, err := ch.Subscribe(rctx); err != nil {
		t.Fatal(err)
	}
	wg.Go(func() { _ = w.reg.Run(rctx) })
	wg.Go(func() { w.keys.Run(rctx) })
	t.Cleanup(func() {
		cctx, c2 := context.WithTimeout(bg, 10*time.Second)
		defer c2()
		_ = w.p.Close(cctx)
		cancel()
		wg.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for !w.keys.Listener().Healthy() {
		if time.Now().After(deadline) {
			t.Fatal("the listener never became healthy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.org, w.owner = w.newOrg(t, "acme", o.policy)
	w.agents = w.newAgents(t, w.org, o.agents)
	return w
}

type syncWriter struct{ w *bytes.Buffer }

func (s syncWriter) Write(b []byte) (int, error) {
	logMu.Lock()
	defer logMu.Unlock()
	return s.w.Write(b)
}

var logMu sync.Mutex

// newOrg creates an org with policy src enforced (as its owner).
func (w *world) newOrg(t testing.TB, slug, src string) (ids.OrgID, ids.UserID) {
	t.Helper()
	o, u, _, err := w.e.DB.CreateOrg(bg, store.NewOrg{Name: "Org " + slug, Slug: slug, OwnerEmail: "owner@" + slug + ".example.test"})
	if err != nil {
		t.Fatal(err)
	}
	caller := policy.Caller{Org: o.ID.String(), Actor: deed.Ref{Type: "user", ID: u.ID.String()}}
	pol, ver, err := w.svc.Create(bg, caller, policy.PushRequest{Source: []byte(src)})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if _, err := w.svc.Activate(bg, caller, pol.ID, policy.VersionRef{Number: ver.Version}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "Gate tests"}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	return o.ID, u.ID
}

func (w *world) newAgents(t testing.TB, org ids.OrgID, n int) []store.Agent {
	t.Helper()
	var out []store.Agent
	err := w.e.DB.OrgTx(bg, org, func(ctx context.Context, tx pgx.Tx) error {
		for i := range n {
			a, err := store.CreateAgent(ctx, tx, org, store.NewAgent{Name: "agent-" + string(rune('a'+i)), CreatedBy: "system"})
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// pr is the principal of agent i's key.
func (w *world) pr(i int) principal {
	a := w.agents[i]
	return principal{Kind: httpx.KindAgentKey, OrgID: w.org, AgentID: a.ID, AgentName: a.Name, AgentStatus: "active",
		Scopes: httpx.DefaultAgentScopes}
}

func req(tool, args string) ActionRequest {
	return ActionRequest{Tool: tool, Args: json.RawMessage(args), WaitSet: true, Wait: 10 * time.Second}
}

// submit sends one action as agent i.
func (w *world) submit(i int, r ActionRequest) (*Action, error) {
	w.t.Helper()
	return w.p.Submit(bg, w.pr(i), r)
}

// must is submit for a call that must not error.
func (w *world) must(i int, r ActionRequest) *Action {
	w.t.Helper()
	a, err := w.submit(i, r)
	if err != nil {
		w.t.Fatalf("%s: %v", r.Tool, err)
	}
	return a
}

// events reads an action's events, in order.
func (w *world) events(id string) [][2]string {
	w.t.Helper()
	var out [][2]string
	err := w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT COALESCE(from_state, ''), to_state FROM deedglass.action_events WHERE action_id = $1 ORDER BY seq`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f, to string
			if err := rows.Scan(&f, &to); err != nil {
				return err
			}
			out = append(out, [2]string{f, to})
		}
		return rows.Err()
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// deedOf returns the action's deed, verified against the seal key.
func (w *world) deedOf(id string) *deed.Envelope {
	w.t.Helper()
	var raw []byte
	err := w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT d.envelope FROM deedglass.deeds d JOIN deedglass.actions a ON a.deed_id = d.id
		    WHERE a.id = $1 AND d.kind = 'action'`, id).Scan(&raw)
	})
	if err != nil {
		w.t.Fatalf("deed of %s: %v", id, err)
	}
	env, err := deed.ParseEnvelope(raw)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := deed.VerifyEnvelope(raw, w.keySet(), deed.VerifyOptions{}); err != nil {
		w.t.Fatalf("deed of %s doesn't verify: %v", id, err)
	}
	return env
}

func (w *world) keySet() *deed.KeySet {
	w.t.Helper()
	rows, err := w.e.DB.SealKeys(bg)
	if err != nil {
		w.t.Fatal(err)
	}
	var recs []deed.KeyRecord
	for _, r := range rows {
		recs = append(recs, r.Record())
	}
	ks, err := deed.NewKeySet("http://127.0.0.1:8080", recs)
	if err != nil {
		w.t.Fatal(err)
	}
	return ks
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
