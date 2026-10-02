//go:build integration

package approve_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fake"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/notify"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var bg = context.Background()

// testPolicy pauses fake.write by table: approvals (1 of role:admin), four (2 of role:admin), sam
// (user:sam, a viewer), unsat (2 of role:owner: the org has one owner).
const testPolicy = `deedglass: policy/v1
name: approve-test
description: The approval integration tests.

defaults:
  decision: deny
  reason: No rule allows this action.

rules:
  - id: writes-need-approval
    match: { tool: [fake.write] }
    when: has(args.table) && args.table == "approvals"
    decision: require_approval
    reason: Writes wait for a person.
    approval: { approvers: [role:admin], required: 1, expires_in: 1h }
  - id: four-eyes
    match: { tool: [fake.write] }
    when: has(args.table) && args.table == "four"
    decision: require_approval
    reason: Two people must agree.
    approval: { approvers: [role:admin], required: 2, expires_in: 1h }
  - id: sam-decides
    match: { tool: [fake.write] }
    when: has(args.table) && args.table == "sam"
    decision: require_approval
    reason: Sam owns this table.
    approval: { approvers: ["user:sam@acme.example.test"], required: 1, expires_in: 1h }
  - id: needs-two-owners
    match: { tool: [fake.write] }
    when: has(args.table) && args.table == "unsat"
    decision: require_approval
    reason: Two owners must agree.
    approval: { approvers: [role:owner], required: 2, expires_in: 1h }
  - id: allow-fake
    match: { tool: ["fake.*"] }
    decision: allow
    reason: Fake tools are safe to run.
`

type recorder struct {
	mu      sync.Mutex
	notices []notify.ApprovalNotice
}

func (r *recorder) Name() string { return "test" }
func (r *recorder) ApprovalRequested(_ context.Context, n notify.ApprovalNotice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, n)
	return nil
}
func (r *recorder) ApprovalResolved(context.Context, notify.ApprovalNotice) error { return nil }

func (r *recorder) linkFor(apr, user string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.notices) - 1; i >= 0; i-- {
		if r.notices[i].ApprovalID != apr {
			continue
		}
		for _, p := range r.notices[i].Recipients {
			if p.UserID.String() == user {
				return p.Link
			}
		}
	}
	return ""
}

type world struct {
	t      testing.TB
	e      *storetest.Env
	led    *ledger.Ledger
	keys   *identity.KeyAuth
	svc    *policy.Service
	fake   *fake.Connector
	p      *gate.Pipeline
	ap     *approve.Service
	notes  *recorder
	org    ids.OrgID
	owner  ids.UserID
	people map[string]ids.UserID // priya, arjun (admins), sam, viv (viewers)
	agent  store.Agent
	logs   *bytes.Buffer
	skew   atomic.Int64 // clock offset, ns
}

func (w *world) now() time.Time { return time.Now().Add(time.Duration(w.skew.Load())) }

func newWorld(t testing.TB) *world {
	t.Helper()
	e := storetest.New(t, storetest.Options{MaxConns: 30})
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
	w := &world{t: t, e: e, logs: &bytes.Buffer{}, notes: &recorder{}, people: map[string]ids.UserID{}}
	log := slog.New(slog.NewJSONHandler(syncWriter{w.logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w.led = ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(log))
	w.keys, err = identity.NewKeyAuth(identity.KeyAuthOptions{DB: e.DB, Pepper: bytes.Repeat([]byte{9}, 32), Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	ch := policystore.NewChanges(w.keys.Listener())
	reg := policy.NewRegistry(policystore.NewRepo(e.DB, nil), ch, policy.WithRegistryLogger(log))
	w.svc = policy.NewService(policystore.NewRepo(e.DB, w.led), policy.WithRegistry(reg))
	w.fake = fake.New()
	w.ap, err = approve.New(approve.Options{DB: e.DB, Ledger: w.led, Notifiers: []notify.Notifier{w.notes}, Logger: log, Clock: w.now})
	if err != nil {
		t.Fatal(err)
	}
	w.p, err = gate.New(gate.Options{DB: e.DB, Ledger: w.led, Policies: reg, Usage: policystore.NewUsage(e.DB),
		Intents: identity.NewIntents(e.DB, w.keys, nil), Guard: w.keys, Resolver: connector.NewRegistry(w.fake),
		Approvals: w.ap, Logger: log, Clock: w.now})
	if err != nil {
		t.Fatal(err)
	}
	w.ap.Attach(w.p)
	w.p.SetJobHandler(gate.JobApprovalNotify, w.ap.NotifyJob)
	worker := w.p.NewWorker("approve-test")
	worker.PollEvery, worker.SweepEvery = 50*time.Millisecond, time.Hour
	w.keys.Listener().Listen(ledger.Channel, w.p.Broker().OnEvent, w.p.Broker().Reset)
	w.keys.Listener().Listen(store.JobsChannel, worker.Wake, func() { worker.Wake("") })
	rctx, cancel := context.WithCancel(bg)
	var wg sync.WaitGroup
	if _, _, err := ch.Subscribe(rctx); err != nil {
		t.Fatal(err)
	}
	wg.Go(func() { _ = reg.Run(rctx) })
	wg.Go(func() { w.keys.Run(rctx) })
	wg.Go(func() { _ = worker.Run(rctx) })
	t.Cleanup(func() {
		cctx, c2 := context.WithTimeout(bg, 10*time.Second)
		defer c2()
		_ = w.p.Close(cctx)
		cancel()
		wg.Wait()
	})
	for deadline := time.Now().Add(5 * time.Second); !w.keys.Listener().Healthy(); {
		if time.Now().After(deadline) {
			t.Fatal("the listener never became healthy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	o, u, _, err := e.DB.CreateOrg(bg, store.NewOrg{Name: "Acme", Slug: "acme", OwnerEmail: "owner@acme.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	w.org, w.owner = o.ID, u.ID
	caller := policy.Caller{Org: o.ID.String(), Actor: deed.Ref{Type: "user", ID: u.ID.String()}}
	pol, ver, err := w.svc.Create(bg, caller, policy.PushRequest{Source: []byte(testPolicy)})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if _, err := w.svc.Activate(bg, caller, pol.ID, policy.VersionRef{Number: ver.Version},
		policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "approval tests"}); err != nil {
		t.Fatalf("activate: %v", err)
	}
	for _, p := range []struct{ name, display, role string }{{"priya", "Priya Raman", "admin"}, {"arjun", "Arjun", "admin"},
		{"sam", "Sam Okafor", "viewer"}, {"viv", "Viv", "viewer"}} {
		w.people[p.name] = w.addMember(p.name+"@acme.example.test", p.display, p.role)
	}
	err = e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		w.agent, err = store.CreateAgent(ctx, tx, w.org, store.NewAgent{Name: "billing-sync", CreatedBy: "system"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

type syncWriter struct{ b *bytes.Buffer }

var logMu sync.Mutex

func (s syncWriter) Write(p []byte) (int, error) {
	logMu.Lock()
	defer logMu.Unlock()
	return s.b.Write(p)
}

func (w *world) addMember(email, name, role string) ids.UserID {
	w.t.Helper()
	var id ids.UserID
	err := w.e.DB.SystemTx(bg, store.ReasonMaintenance, func(ctx context.Context, tx pgx.Tx) error {
		u, err := store.UpsertUserByEmail(ctx, tx, email, name)
		if err != nil {
			return err
		}
		id = u.ID
		_, err = tx.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at)
		    VALUES ($1, $2, $3, $4, 'active', pg_catalog.now())`, ids.New[ids.MembershipKind](), w.org, u.ID, role)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE deedglass.users SET status = 'active' WHERE id = $1`, u.ID)
		}
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) settings(js string) {
	w.t.Helper()
	if err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.orgs SET settings = $2::jsonb WHERE id = $1`, w.org, js)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) agentPr() httpx.Principal {
	return httpx.Principal{Kind: httpx.KindAgentKey, OrgID: w.org, AgentID: w.agent.ID, AgentName: w.agent.Name, AgentStatus: "active",
		Scopes: httpx.DefaultAgentScopes}
}

// pause submits a fake.write to table and returns the paused action.
func (w *world) pause(table, obo string) *gate.Action {
	w.t.Helper()
	a, err := w.submit(table, obo)
	if err != nil {
		w.t.Fatalf("submit %s: %v", table, err)
	}
	if a.Status != ledger.StatusPendingApproval || a.Approval == nil {
		w.t.Fatalf("submit %s: status %s, approval %v", table, a.Status, a.Approval)
	}
	return a
}

func (w *world) submit(table, obo string) (*gate.Action, error) {
	return w.p.Submit(bg, w.agentPr(), gate.ActionRequest{Tool: "fake.write", Args: json.RawMessage(`{"rows":1,"table":"` + table + `"}`),
		WaitSet: true, Wait: 2 * time.Second, OnBehalfOf: obo})
}

func (w *world) viewer(name string) approve.Viewer {
	return approve.Viewer{Kind: httpx.KindUser, Org: w.org, UserID: w.people[name]}
}

func (w *world) digest(apr string) string {
	w.t.Helper()
	a, err := w.ap.Get(bg, approve.Viewer{Kind: httpx.KindAdminKey, Org: w.org}, apr)
	if err != nil || a.ContextDigest == nil {
		w.t.Fatalf("get %s: %v", apr, err)
	}
	return *a.ContextDigest
}

func (w *world) decide(name, apr string, v approve.Vote, reason string) (*approve.Approval, error) {
	return w.ap.Decide(bg, approve.DecideInput{Org: w.org, ApprovalID: apr, Vote: v, Reason: reason, ContextDigest: w.digest(apr),
		Approver: approve.Approver{UserID: w.people[name], Via: approve.ViaConsole}})
}

// waitAction polls an action until done says so (10 s).
func (w *world) waitAction(id string, done func(*ledger.Action) bool) *ledger.Action {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v, err := w.led.ReadActionView(bg, w.org, id)
		if err != nil {
			w.t.Fatal(err)
		}
		if done(v) {
			return v
		}
		if time.Now().After(deadline) {
			w.t.Fatalf("action %s stuck in %s (%s)", id, v.State, v.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sealed(v *ledger.Action) bool { return v.State == "sealed" }

// deedApproval is the action deed's approval section.
func (w *world) deedApproval(id string) *deed.ApprovalSection {
	w.t.Helper()
	var raw []byte
	if err := w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT d.envelope FROM deedglass.deeds d JOIN deedglass.actions a ON a.deed_id = d.id WHERE a.id = $1`, id).Scan(&raw)
	}); err != nil {
		w.t.Fatal(err)
	}
	env, err := deed.ParseEnvelope(raw)
	if err != nil {
		w.t.Fatal(err)
	}
	return env.Body.Approval
}

func (w *world) count(q string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, q, args...).Scan(&n)
	}); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func code(err error) httpx.Code {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

func storeCode(err error) string { return store.SQLState(err) }

func aid(t testing.TB, id string) ids.ActionID {
	t.Helper()
	a, err := ids.Parse[ids.ActionKind](id)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (w *world) newAgent(name string) ids.AgentID {
	w.t.Helper()
	var a store.Agent
	err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = store.CreateAgent(ctx, tx, w.org, store.NewAgent{Name: name, CreatedBy: "system"})
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return a.ID
}
