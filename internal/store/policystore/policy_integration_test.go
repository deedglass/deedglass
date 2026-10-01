//go:build integration

package policystore_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/examples/policies"
	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// pworld is one database, a seal key, a Ledger, and instance A: its key authenticator (whose
// listener carries deedglass_policy), its registry and the policy service.
type pworld struct {
	e      *storetest.Env
	signer *deed.FileSigner
	led    *ledger.Ledger
	a      *instance
	svc    *policy.Service
}

type instance struct {
	db   *store.DB
	keys *identity.KeyAuth
	reg  *policy.Registry
	swap chan swapped
}

type swapped struct {
	org  string
	hash string
	at   time.Time
}

func newPWorld(t *testing.T) *pworld {
	t.Helper()
	e := storetest.New(t, storetest.Options{MaxConns: 16})
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
	w := &pworld{e: e, signer: signer, led: ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true))}
	w.a = w.instance(t, e.DB)
	w.svc = policy.NewService(policystore.NewRepo(e.DB, w.led), policy.WithRegistry(w.a.reg))
	hooks := identity.OrgCreateHooks
	identity.OrgCreateHooks = append(slices.Clone(hooks), policystore.BootstrapOrg)
	t.Cleanup(func() { identity.OrgCreateHooks = hooks })
	return w
}

// instance starts a key authenticator (its listener) and a registry on db: one Deedglass instance.
func (w *pworld) instance(t *testing.T, db *store.DB, opts ...policy.RegistryOption) *instance {
	t.Helper()
	keys, err := identity.NewKeyAuth(identity.KeyAuthOptions{DB: db, Pepper: bytes.Repeat([]byte{9}, 32), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	in := &instance{db: db, keys: keys, swap: make(chan swapped, 256)}
	opts = append(opts, policy.WithSwapHook(func(org string, a *policy.Active) {
		select {
		case in.swap <- swapped{org, a.Enforce.VersionHash, time.Now()}:
		default:
		}
	}))
	ch := policystore.NewChanges(keys.Listener())
	in.reg = policy.NewRegistry(policystore.NewRepo(db, nil), ch, opts...)
	rctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	if _, _, err := ch.Subscribe(rctx); err != nil {
		t.Fatal(err)
	}
	wg.Go(func() { _ = in.reg.Run(rctx) })
	wg.Go(func() { keys.Run(rctx) })
	t.Cleanup(func() { cancel(); wg.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for !keys.Listener().Healthy() {
		if time.Now().After(deadline) {
			t.Fatal("the listener never became healthy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return in
}

func (w *pworld) org(t *testing.T, name string) (string, policy.Caller) {
	t.Helper()
	o, err := identity.CreateOrg(ctx, w.e.DB, w.led, name, "", strings.ToLower(strings.ReplaceAll(name, " ", ""))+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	return o.Org.ID.String(), policy.Caller{Org: o.Org.ID.String(), Actor: deed.Ref{Type: "user", ID: o.Owner.ID.String()}, RequestID: ""}
}

func source(t *testing.T, name string) []byte {
	t.Helper()
	b, err := policies.Source(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sqlstate(err error) string { return store.SQLState(err) }

// The table guards (S12 step 1): versions are append-only, one live enforce per org, only the end
// columns of an activation are writable, an ended activation never changes, and RLS isolates orgs.
func TestPolicyTableGuards(t *testing.T) {
	w := newPWorld(t)
	orgA, _ := w.org(t, "Acme")
	orgB, _ := w.org(t, "Globex")
	idA := ids.MustParse[ids.OrgKind](orgA)
	idB := ids.MustParse[ids.OrgKind](orgB)
	in := func(org ids.OrgID, q string, args ...any) error {
		return w.e.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, q, args...); return err })
	}
	// The runtime role has no UPDATE or DELETE on versions at all; even the table owner meets the
	// append-only trigger (DG001).
	for _, q := range []string{`UPDATE deedglass.policy_versions SET notes = 'x'`, `DELETE FROM deedglass.policy_versions`} {
		if err := in(idA, q); sqlstate(err) != store.CodeInsufficientPriv {
			t.Fatalf("%s as deedglass_app: %v", q, err)
		}
		if _, err := w.e.Admin(t).Exec(ctx, q); sqlstate(err) != store.CodeAppendOnly {
			t.Fatalf("%s as the owner: %v", q, err)
		}
	}
	if _, err := w.e.Admin(t).Exec(ctx, `TRUNCATE deedglass.policy_versions CASCADE`); sqlstate(err) != store.CodeAppendOnly {
		t.Fatalf("TRUNCATE policy_versions: %v", err)
	}
	var live struct{ id, policy, version string }
	if err := w.e.DB.OrgReadTx(ctx, idA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, policy_id, version_id FROM deedglass.policy_activations WHERE deactivated_at IS NULL`).Scan(&live.id, &live.policy, &live.version)
	}); err != nil {
		t.Fatal(err)
	}
	// A second live enforce row behind the service's back fails on S02's partial unique index.
	if err := in(idA, `INSERT INTO deedglass.policy_activations (id, org_id, policy_id, version_id, mode) VALUES ($1, $2, $3, $4, 'enforce')`,
		ids.New[ids.PolicyActivationKind](), idA, live.policy, live.version); sqlstate(err) != store.CodeUniqueViolation {
		t.Fatalf("second live enforce: %v", err)
	}
	for _, q := range []string{
		`UPDATE deedglass.policy_activations SET mode = 'shadow'`,
		`UPDATE deedglass.policy_activations SET reason = 'edited'`,
		`UPDATE deedglass.policy_activations SET deed_id = NULL`,
		`DELETE FROM deedglass.policy_activations`,
	} {
		if err := in(idA, q); sqlstate(err) != store.CodeInsufficientPriv {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Ending without a deed breaks the ended-check; ending properly is done by the service.
	if err := in(idA, `UPDATE deedglass.policy_activations SET deactivated_at = pg_catalog.now()`); sqlstate(err) != store.CodeCheckViolation {
		t.Fatalf("end without a deed: %v", err)
	}
	// RLS: B sees none of A's policy rows, and can't write into A.
	var n int
	if err := w.e.DB.OrgReadTx(ctx, idB, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM deedglass.policies WHERE org_id = $1) + (SELECT count(*) FROM deedglass.policy_versions WHERE org_id = $1)
		    + (SELECT count(*) FROM deedglass.policy_activations WHERE org_id = $1)`, orgA).Scan(&n)
	}); err != nil || n != 0 {
		t.Fatalf("B sees %d of A's rows (%v)", n, err)
	}
	if err := in(idB, `INSERT INTO deedglass.policies (id, org_id, name) VALUES ($1, $2, 'x')`, ids.New[ids.PolicyKind](), idA); err == nil {
		t.Fatal("B wrote a policy into A")
	}
	// The service can't see another org's policy either.
	_, callerB := orgB, policy.Caller{Org: orgB, Actor: policy.SystemActor}
	if _, err := w.svc.Get(ctx, callerB, live.policy); err == nil {
		t.Fatal("B read A's policy through the service")
	}
}

// Onboarding (S12 §3.10): CreateOrg leaves default-deny enforced, sealed, actor system, in the
// same transaction.
func TestOnboardingOnPostgres(t *testing.T) {
	w := newPWorld(t)
	org, caller := w.org(t, "Initech")
	set, err := w.svc.Active(ctx, caller)
	if err != nil || set.Enforce == nil || set.Enforce.Name != "default-deny" || set.Shadow != nil || set.Enforce.Version != 1 {
		t.Fatalf("%v %+v", err, set)
	}
	ks := w.keySet(t)
	raw := w.deed(t, org, set.Enforce.Deed)
	res, err := deed.VerifyEnvelope(raw, ks, deed.VerifyOptions{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	b := res.Envelope.Body
	if b.Admin.Action != ledger.AdminPolicyActivated || b.Admin.Actor != policy.SystemActor || b.Policy != nil || *b.Admin.Reason != policy.BootstrapReason ||
		b.Admin.Target.ID != set.Enforce.VersionID {
		t.Fatalf("%+v", b.Admin)
	}
	v, err := w.svc.GetVersion(ctx, caller, "default-deny", policy.VersionRef{Number: 1})
	if err != nil || v.Origin != policy.OriginOnboarding || v.Author.Object != "system" || v.Author.ID != nil {
		t.Fatalf("%v %+v", err, v)
	}
	a, err := w.a.reg.Active(ctx, org)
	if err != nil || a.Enforce.Unavailable != nil || a.Enforce.Name != "default-deny" {
		t.Fatalf("%v %+v", err, a.Enforce)
	}
	// Bootstrapping again is a no-op.
	if done, err := policystore.BootstrapExisting(ctx, w.e.DB, w.led, org); err != nil || done {
		t.Fatalf("%v %v", done, err)
	}
	// An org made before S12 (no hook) gets it from BootstrapExisting (dev seed).
	o, _, _, err := w.e.DB.CreateOrg(ctx, store.NewOrg{Name: "Old Dev", Slug: "old-dev", OwnerEmail: "old@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ = w.a.reg.Active(ctx, o.ID.String())
	if !a.Enforce.Builtin || a.Enforce.Name != policy.BuiltinNoPolicy {
		t.Fatalf("an org with no activation gets the builtin deny: %+v", a.Enforce)
	}
	if done, err := policystore.BootstrapExisting(ctx, w.e.DB, w.led, o.ID.String()); err != nil || !done {
		t.Fatalf("%v %v", done, err)
	}
}

func (w *pworld) keySet(t *testing.T) *deed.KeySet {
	t.Helper()
	rows, err := w.e.DB.SealKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var recs []deed.KeyRecord
	for _, r := range rows {
		recs = append(recs, r.Record())
	}
	ks, err := deed.NewKeySet("", recs)
	if err != nil {
		t.Fatal(err)
	}
	return ks
}

func (w *pworld) deed(t *testing.T, org, id string) []byte {
	t.Helper()
	var raw []byte
	if err := w.e.DB.OrgReadTx(ctx, ids.MustParse[ids.OrgKind](org), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT envelope FROM deedglass.deeds WHERE id = $1`, id).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	return raw
}

func wantKind(t *testing.T, err error, kind policy.ErrKind) {
	t.Helper()
	var e *policy.Error
	if !errors.As(err, &e) || e.Kind != kind {
		t.Fatalf("want %s, got %v", kind, err)
	}
}

// The service on Postgres: §6's "Service and activation" table.
func TestServiceOnPostgres(t *testing.T) {
	w := newPWorld(t)
	org, caller := w.org(t, "Acme")
	p, v1, err := w.svc.Create(ctx, caller, policy.PushRequest{Source: source(t, "postgres-safe"), Notes: "Orders DB, first cut"})
	if err != nil || p.Status != policy.StatusInactive || v1.Version != 1 || v1.Rules != 12 || v1.Author.Object != "user" {
		t.Fatalf("%v %+v %+v", err, p, v1)
	}
	var deedsBefore int64
	if deedsBefore, err = w.led.HeadSeq(ctx, ids.MustParse[ids.OrgKind](org)); err != nil {
		t.Fatal(err)
	}
	same, created, err := w.svc.AddVersion(ctx, caller, "postgres-safe", policy.PushRequest{Source: source(t, "postgres-safe")})
	if err != nil || created || same.ID != v1.ID {
		t.Fatalf("identical push: %v %v", err, created)
	}
	_, _, err = w.svc.AddVersion(ctx, caller, p.ID, policy.PushRequest{Source: source(t, "stripe-refunds")})
	wantKind(t, err, policy.KindValidation)
	_, _, err = w.svc.Create(ctx, caller, policy.PushRequest{Source: []byte("deedglass: policy/v1\nname: demo\nrules:\n  - id: a\n    desicion: deny\n")})
	wantKind(t, err, policy.KindInvalid)
	if n, _ := w.led.HeadSeq(ctx, ids.MustParse[ids.OrgKind](org)); n != deedsBefore {
		t.Fatal("a push must not seal a deed")
	}
	// Shadow, then promote: two deeds, shadow cleared.
	sh, err := w.svc.Activate(ctx, caller, "postgres-safe", policy.VersionRef{Number: 1}, policy.ActivateRequest{Mode: policy.ModeShadow, Reason: "Watch it first."})
	if err != nil || len(sh.Deeds) != 1 {
		t.Fatalf("%v %+v", err, sh)
	}
	pr, err := w.svc.Activate(ctx, caller, "postgres-safe", policy.VersionRef{Number: 1}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "Guard orders."})
	if err != nil || len(pr.Deeds) != 2 || !pr.PromotedFromShadow || pr.Previous.Name != "default-deny" {
		t.Fatalf("%v %+v", err, pr)
	}
	ks := w.keySet(t)
	for i, id := range pr.Deeds {
		res, err := deed.VerifyEnvelope(w.deed(t, org, id), ks, deed.VerifyOptions{Strict: true})
		if err != nil || res.Envelope.Body.Policy != nil {
			t.Fatalf("deed %d: %v", i, err)
		}
		want := []string{ledger.AdminPolicyDeactivated, ledger.AdminPolicyActivated}[i]
		if res.Envelope.Body.Admin.Action != want {
			t.Fatalf("deed %d is %s", i, res.Envelope.Body.Admin.Action)
		}
	}
	a, _ := w.a.reg.Active(ctx, org)
	if a.Enforce.VersionHash != v1.VersionHash || a.Shadow != nil {
		t.Fatalf("the registry serves %s", a.Enforce.Name)
	}
	// Conflicts.
	_, err = w.svc.Activate(ctx, caller, "postgres-safe", policy.VersionRef{Number: 1}, policy.ActivateRequest{Mode: policy.ModeShadow, Reason: "Shadow it."})
	wantKind(t, err, policy.KindConflict)
	_, err = w.svc.Deactivate(ctx, caller, "postgres-safe", policy.VersionRef{Number: 1}, policy.DeactivateRequest{Reason: "Off."})
	wantKind(t, err, policy.KindConflict)
	// default_allow needs the acknowledgement, and the deed records it.
	open := "deedglass: policy/v1\nname: open-door\ndefaults:\n  decision: allow\n"
	if _, _, err := w.svc.Create(ctx, caller, policy.PushRequest{Source: []byte(open)}); err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.Activate(ctx, caller, "open-door", policy.VersionRef{Number: 1}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "Open it."})
	wantKind(t, err, policy.KindValidation)
	od, err := w.svc.Activate(ctx, caller, "open-door", policy.VersionRef{Number: 1}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "Open it.",
		AcknowledgeWarnings: []string{"default_allow"}})
	if err != nil {
		t.Fatal(err)
	}
	var data string
	if err := w.e.DB.OrgReadTx(ctx, ids.MustParse[ids.OrgKind](org), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT data::text FROM deedglass.admin_events WHERE deed_id = $1`, od.Deeds[0]).Scan(&data)
	}); err != nil || !strings.Contains(data, `"acknowledged_warnings": ["default_allow"]`) {
		t.Fatalf("%v %s", err, data)
	}
	// Roll back to postgres-safe; the dry run reads the registry.
	if _, err := w.svc.Activate(ctx, caller, "postgres-safe", policy.VersionRef{Number: 1}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "Roll back."}); err != nil {
		t.Fatal(err)
	}
	res, err := w.svc.Evaluate(ctx, caller, policy.DryRunRequest{Input: map[string]any{"tool": "postgres.execute", "sql": "DELETE FROM public.orders"}})
	if err != nil || res.Decision != policy.Deny || *res.RuleID != "block-unbounded-writes" {
		t.Fatalf("%v %+v", err, res)
	}
	// Another org's agent is not found here (RLS).
	other, _ := w.org(t, "Globex")
	var agent string
	if err := w.e.DB.OrgTx(ctx, ids.MustParse[ids.OrgKind](other), func(ctx context.Context, tx pgx.Tx) error {
		a, err := store.CreateAgent(ctx, tx, ids.MustParse[ids.OrgKind](other), store.NewAgent{Name: "spy-bot", CreatedBy: "system"})
		agent = a.ID.String()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err = w.svc.Evaluate(ctx, caller, policy.DryRunRequest{Input: map[string]any{"tool": "postgres.query", "agent": map[string]any{"id": agent}}})
	wantKind(t, err, policy.KindNotFound)
	// The chain is intact after all of it.
	check, err := w.led.CheckChain(ctx, ids.MustParse[ids.OrgKind](org), ledger.Range{}, ledger.CheckOptions{})
	if err != nil || check.Status != "intact" {
		t.Fatalf("%v %+v", err, check)
	}
}

// Ten activations at once: one enforce at the end, ten activation rows, a gapless chain (S12 §6).
func TestConcurrentActivationsOnPostgres(t *testing.T) {
	w := newPWorld(t)
	org, caller := w.org(t, "Acme")
	base := string(source(t, "postgres-safe"))
	if _, _, err := w.svc.Create(ctx, caller, policy.PushRequest{Source: []byte(base)}); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 10; i++ {
		src := strings.Replace(base, "max_calls: 60", fmt.Sprintf("max_calls: %d", 60+i), 1)
		if _, _, err := w.svc.AddVersion(ctx, caller, "postgres-safe", policy.PushRequest{Source: []byte(src)}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 1; i <= 10; i++ {
		wg.Go(func() {
			_, err := w.svc.Activate(ctx, caller, "postgres-safe", policy.VersionRef{Number: i}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "Race it."})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var live, rows int
	if err := w.e.DB.OrgReadTx(ctx, ids.MustParse[ids.OrgKind](org), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE a.deactivated_at IS NULL), count(*) FILTER (WHERE p.name = 'postgres-safe')
		    FROM deedglass.policy_activations a JOIN deedglass.policies p ON p.id = a.policy_id`).Scan(&live, &rows)
	}); err != nil {
		t.Fatal(err)
	}
	if live != 1 || rows != 10 {
		t.Fatalf("live %d, postgres-safe activations %d", live, rows)
	}
	check, err := w.led.CheckChain(ctx, ids.MustParse[ids.OrgKind](org), ledger.Range{}, ledger.CheckOptions{})
	if err != nil || check.Status != "intact" {
		t.Fatalf("%v %+v", err, check)
	}
}

// Hot swap across two instances (two pools, two listeners): an activation committed through A is
// served by B within 2 s, 20 of 20 times; p50/p99 are logged (S12 §3.11).
func TestHotSwapTwoInstances(t *testing.T) {
	w := newPWorld(t)
	db2, err := store.Open(ctx, config.NewSecret(w.e.AppURL), store.Options{MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db2.Close)
	b := w.instance(t, db2)
	org, caller := w.org(t, "Acme")
	if _, _, err := w.svc.Create(ctx, caller, policy.PushRequest{Source: source(t, "postgres-safe")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.svc.Create(ctx, caller, policy.PushRequest{Source: source(t, "github-read-mostly")}); err != nil {
		t.Fatal(err)
	}
	names := []string{"postgres-safe", "github-read-mostly"}
	hashes := map[string]string{}
	for _, n := range names {
		v, _ := w.svc.GetVersion(ctx, caller, n, policy.VersionRef{Number: 1})
		hashes[n] = v.VersionHash
	}
	if _, err := b.reg.Active(ctx, org); err != nil { // B holds the org (warm cache)
		t.Fatal(err)
	}
	var times []time.Duration
	for i := 0; i < 20; i++ {
		name := names[i%2]
		for len(b.swap) > 0 {
			<-b.swap
		}
		if _, err := w.svc.Activate(ctx, caller, name, policy.VersionRef{Number: 1}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "Swap it."}); err != nil {
			t.Fatal(err)
		}
		committed := time.Now()
		deadline := time.After(2 * time.Second)
	wait:
		for {
			select {
			case s := <-b.swap:
				if s.org == org && s.hash == hashes[name] {
					times = append(times, s.at.Sub(committed))
					break wait
				}
			case <-deadline:
				t.Fatalf("run %d: B didn't serve %s within 2 s", i, name)
			}
		}
		a, _ := b.reg.Active(ctx, org)
		if a.Enforce.VersionHash != hashes[name] {
			t.Fatalf("run %d: B serves %s", i, a.Enforce.Name)
		}
	}
	slices.Sort(times)
	p50, p99, mx := times[len(times)/2], times[len(times)*99/100], times[len(times)-1]
	t.Logf("hot swap commit → instance B serving the new hash, 20 runs: p50 %v · p99 %v · max %v · all %v", p50, p99, mx, times)
	if out := os.Getenv("DEEDGLASS_IT_BENCH_OUT"); out != "" {
		b, _ := json.Marshal(map[string]any{"bench": "policy_hot_swap", "runs": len(times), "p50_ms": ms(p50), "p99_ms": ms(p99), "max_ms": ms(mx)})
		_ = os.WriteFile(out, append(b, '\n'), 0o644)
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// A killed listener connection: the instance reconnects, invalidates its cache, and converges; an
// activation made while it was away is caught (S12 §6, within 30 s).
func TestListenerKilled(t *testing.T) {
	w := newPWorld(t)
	db2, err := store.Open(ctx, config.NewSecret(w.e.AppURL), store.Options{MaxConns: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db2.Close)
	b := w.instance(t, db2, policy.WithPollEvery(time.Second))
	org, caller := w.org(t, "Acme")
	if _, _, err := w.svc.Create(ctx, caller, policy.PushRequest{Source: source(t, "postgres-safe")}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.reg.Active(ctx, org); err != nil {
		t.Fatal(err)
	}
	pid := b.keys.Listener().PID()
	if pid == 0 {
		t.Fatal("no listener connection")
	}
	start := time.Now()
	admin := w.e.Admin(t)
	if _, err := admin.Exec(ctx, `SELECT pg_catalog.pg_terminate_backend($1)`, int32(pid)); err != nil {
		t.Fatal(err)
	}
	// Change the policy while B's listener is down.
	if _, err := w.svc.Activate(ctx, caller, "postgres-safe", policy.VersionRef{Number: 1}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "While away."}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		a, _ := b.reg.Active(ctx, org)
		if a.Enforce.Name == "postgres-safe" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("B never converged after its listener was killed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	waitFor(t, func() bool {
		return b.keys.Listener().PID() != 0 && b.keys.Listener().PID() != pid && b.keys.Listener().Healthy()
	})
	t.Logf("converged %v after the kill; reconnects %d", time.Since(start), b.keys.Listener().Reconnects())
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// The tamper test (throwaway database only): a superuser disables the append-only trigger and edits
// the enforced version's YAML. The registry refuses it: policy_unavailable for every action, health
// degraded, never the older version. In normal operation the trigger is what prevents the edit.
func TestTamperFailsClosed(t *testing.T) {
	w := newPWorld(t)
	org, caller := w.org(t, "Acme")
	if _, _, err := w.svc.Create(ctx, caller, policy.PushRequest{Source: source(t, "postgres-safe")}); err != nil {
		t.Fatal(err)
	}
	act, err := w.svc.Activate(ctx, caller, "postgres-safe", policy.VersionRef{Number: 1}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "Guard orders."})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := w.a.reg.Active(ctx, org); a.Enforce.Unavailable != nil {
		t.Fatal(a.Enforce.Unavailable)
	}
	admin := w.e.Admin(t)
	// Loosen the policy in the database: block-destructive-sql now blocks nothing.
	for _, q := range []string{
		`ALTER TABLE deedglass.policy_versions DISABLE TRIGGER USER`,
		`UPDATE deedglass.policy_versions SET yaml = pg_catalog.replace(yaml, 'when: sql.kind in ["drop", "truncate"]', 'when: false') WHERE id = '` + act.Version.ID + `'`,
		`ALTER TABLE deedglass.policy_versions ENABLE TRIGGER USER`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	w.a.reg.Invalidate(org)
	a, _ := w.a.reg.Active(ctx, org)
	if a.Enforce.Unavailable == nil || a.Enforce.Policy() != nil {
		t.Fatal("a tampered version loaded")
	}
	t.Logf("the registry refused it: %v", a.Enforce.Unavailable)
	res, err := w.svc.Evaluate(ctx, caller, policy.DryRunRequest{Input: map[string]any{"tool": "postgres.execute", "sql": "DROP TABLE public.orders"}})
	if err != nil || res.Code == nil || *res.Code != "policy_unavailable" || res.Decision != policy.Deny {
		t.Fatalf("%v %+v", err, res)
	}
	if h := w.a.reg.Health(); h.Status != "degraded" || !slices.Contains(h.Unavailable, org) {
		t.Fatalf("%+v", h)
	}
	// With the trigger on, the same edit is refused outright.
	if _, err := admin.Exec(ctx, `UPDATE deedglass.policy_versions SET notes = 'x' WHERE id = '`+act.Version.ID+`'`); sqlstate(err) != store.CodeAppendOnly {
		t.Fatalf("the trigger must refuse edits: %v", err)
	}
}
