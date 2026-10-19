//go:build integration

package store_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// sealEnv is a fresh database with a registered seal key and n orgs that have no deeds yet.
type sealEnv struct {
	e      *storetest.Env
	signer *deed.FileSigner
	sealer *deed.Sealer
	orgs   []ids.OrgID
}

func newSealEnv(t *testing.T, n int, maxConns int32) *sealEnv {
	t.Helper()
	e := storetest.New(t, storetest.Options{MaxConns: maxConns})
	s, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), randomSeed(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := e.DB.RegisterSealKey(ctx, s.KeyID(), s.PublicKey()); err != nil || !created {
		t.Fatalf("register: %v %v", created, err)
	}
	se := &sealEnv{e: e, signer: s, sealer: deed.NewSealer(s)}
	for i := range n {
		o, _, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: fmt.Sprintf("Seal %d", i), Slug: fmt.Sprintf("seal-%d", i), OwnerEmail: fmt.Sprintf("o%d@seal.example.test", i)})
		if err != nil {
			t.Fatal(err)
		}
		se.orgs = append(se.orgs, o.ID)
	}
	return se
}

func randomSeed(t *testing.T) []byte {
	s, err := deed.NewSeed(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func adminDraft(org ids.OrgID) deed.Draft {
	d, err := deed.ForAdmin(deed.AdminSnapshot{Org: org.String(), Event: ids.New[ids.AdminEventKind]().String(),
		Action: "agent.stopped", Actor: deed.Ref{Type: "system", ID: "operator"}})
	if err != nil {
		panic(err)
	}
	return d
}

func (se *sealEnv) seal(org ids.OrgID) (*deed.Envelope, error) {
	var env *deed.Envelope
	err := se.e.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		env, err = se.sealer.Seal(ctx, store.DeedTx(tx), adminDraft(org))
		return err
	})
	return env, err
}

// chainOf reads an org's stored envelopes in seq order and checks them as a chain.
func (se *sealEnv) chainOf(t *testing.T, org ids.OrgID) deed.ChainSummary {
	t.Helper()
	var envs [][]byte
	tx(t, se.e.DB, org, func(tx pgx.Tx) {
		rows, err := tx.Query(ctx, `SELECT envelope FROM deedglass.deeds WHERE org_id = $1 ORDER BY seq`, org)
		if err != nil {
			t.Fatal(err)
		}
		envs, err = pgx.CollectRows(rows, pgx.RowTo[[]byte])
		if err != nil {
			t.Fatal(err)
		}
	})
	rows, err := se.e.DB.SealKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var recs []deed.KeyRecord
	for _, r := range rows {
		recs = append(recs, r.Record())
	}
	ks, err := deed.NewKeySet("http://127.0.0.1:8080", recs)
	if err != nil {
		t.Fatal(err)
	}
	cc := deed.NewChainChecker(deed.ChainOptions{Org: org.String()})
	for _, b := range envs {
		r, err := deed.VerifyEnvelope(b, ks, deed.VerifyOptions{Strict: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := cc.Next(r); err != nil {
			t.Fatal(err)
		}
	}
	return cc.Summary()
}

func TestSealConcurrency(t *testing.T) {
	se := newSealEnv(t, 1, 60)
	org := se.orgs[0]
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := se.seal(org); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	s := se.chainOf(t, org)
	if s.Deeds != 50 || s.FirstSeq != 1 || s.LastSeq != 50 {
		t.Fatalf("%+v", s)
	}
	t.Logf("✓ %d deeds · chain intact · %d seal key", s.Deeds, len(s.SealKeys))
}

// Holding org A's chain lock never blocks org B. The test has no wall-clock bound on org B's
// speed (S19's 200 ms bound tripped on a slow CI runner, DL-033): org A keeps its lock until org B
// has sealed all 20 deeds, so if B's seals waited on A's lock they could never finish and the
// 60 s deadline would fail the test. Org B's seals use their own context with that deadline, and
// org A's transaction must still hold its lock (still open) when B is done.
func TestSealOrgsIndependent(t *testing.T) {
	se := newSealEnv(t, 2, 10)
	a, b := se.orgs[0], se.orgs[1]
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- se.e.DB.OrgTx(ctx, a, func(ctx context.Context, tx pgx.Tx) error {
			if err := store.DeedTx(tx).LockChain(ctx, a.String()); err != nil {
				return err
			}
			close(held)
			select {
			case <-release:
				return nil
			case <-time.After(90 * time.Second):
				return errors.New("org A's lock was never released")
			}
		})
	}()
	<-held
	bctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	start := time.Now()
	for i := range 20 {
		err := se.e.DB.OrgTx(bctx, b, func(ctx context.Context, tx pgx.Tx) error {
			_, err := se.sealer.Seal(ctx, store.DeedTx(tx), adminDraft(b))
			return err
		})
		if err != nil {
			close(release)
			<-done
			t.Fatalf("seal %d on org B while org A held its chain lock: %v", i+1, err)
		}
	}
	took := time.Since(start)
	select {
	case err := <-done:
		t.Fatalf("org A's transaction ended before org B finished: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := se.chainOf(t, b); s.Deeds != 20 {
		t.Fatalf("org B: %+v", s)
	}
	t.Logf("20 seals on org B in %v while org A held its chain lock", took)
}

func TestSealRollbackBurnsNoSeq(t *testing.T) {
	se := newSealEnv(t, 1, 4)
	org := se.orgs[0]
	rollback := errors.New("roll back")
	var first *deed.Envelope
	err := se.e.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if first, err = se.sealer.Seal(ctx, store.DeedTx(tx), adminDraft(org)); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) || first.Body.Seq != 1 {
		t.Fatalf("%v", err)
	}
	again, err := se.seal(org)
	if err != nil || again.Body.Seq != 1 || again.ID == first.ID {
		t.Fatalf("after rollback: %v seq %d", err, again.Body.Seq)
	}
}

// interloper runs after ReadState: a writer that bypassed the chain lock.
type interloper struct {
	deed.ChainStore
	after func()
}

func (i interloper) ReadState(ctx context.Context, org, key string) (deed.ChainState, error) {
	st, err := i.ChainStore.ReadState(ctx, org, key)
	i.after()
	return st, err
}

func TestSealForcedConflict(t *testing.T) {
	se := newSealEnv(t, 1, 4)
	org := se.orgs[0]
	admin := se.e.Admin(t)
	sneak := func() {
		// A superuser on the test database inserts seq 1 behind the Sealer's back.
		c := deedtest.NewChain(1, deedtest.ChainOpts{Org: org.String(), Start: time.Now().UTC(),
			KeyFor: func(int64) deedtest.Key { return deedtest.Key{ID: se.signer.KeyID(), Signer: se.signer} },
			Body:   func(org string, seq int64) deed.Body { return deedtest.AdminStopped(org, int(seq)) }})
		env := c.Deeds[0]
		bodyJCS, _ := deed.StrictCanonical(&env.Body)
		if _, err := admin.Exec(ctx, `INSERT INTO deedglass.deeds (org_id, seq, id, kind, issued_at, body, body_jcs, body_hash, prev_hash, seal, seal_key_id, envelope)
		    VALUES ($1, 1, $2, 'admin', now(), $3::jsonb, $4, $5, $6, $7, $8, $9)`,
			org, env.ID, string(bodyJCS), bodyJCS, env.BodyHash, env.Body.PrevHash, env.Seal, env.SealKeyID, c.Lines[0]); err != nil {
			t.Errorf("sneak insert: %v", err)
		}
	}
	err := se.e.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := se.sealer.Seal(ctx, interloper{store.DeedTx(tx), sneak}, adminDraft(org))
		return err
	})
	if !errors.Is(err, deed.ErrChainConflict) {
		t.Fatalf("got %v, want ErrChainConflict", err)
	}
	env, err := se.seal(org) // the retry, in a new transaction
	if err != nil || env.Body.Seq != 2 {
		t.Fatalf("retry: %v", err)
	}
	if s := se.chainOf(t, org); s.Deeds != 2 {
		t.Fatalf("%+v", s)
	}
}

func TestSealRLSAndAppendOnly(t *testing.T) {
	se := newSealEnv(t, 2, 4)
	a, b := se.orgs[0], se.orgs[1]
	env, err := se.seal(a)
	if err != nil {
		t.Fatal(err)
	}
	tx(t, se.e.DB, b, func(tx pgx.Tx) {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.deeds WHERE org_id = $1`, a).Scan(&n); err != nil || n != 0 {
			t.Fatalf("org B sees %d of org A's deeds (%v)", n, err)
		}
	})
	// The app role has no UPDATE or DELETE on deeds; the owner hits the append-only trigger.
	for _, q := range []string{`UPDATE deedglass.deeds SET seal = seal`, `DELETE FROM deedglass.deeds`} {
		err := se.e.DB.OrgTx(ctx, a, func(ctx context.Context, tx pgx.Tx) error { _, err := tx.Exec(ctx, q); return err })
		expectCode(t, "app: "+q, err, store.CodeInsufficientPriv)
		_, err = se.e.Migrator(t).Exec(ctx, q)
		expectCode(t, "owner: "+q, err, store.CodeAppendOnly)
	}
	// The stored bytes are exactly JCS(envelope), and body_jcs hashes to body_hash.
	tx(t, se.e.DB, a, func(tx pgx.Tx) {
		var envBytes, bodyJCS []byte
		if err := tx.QueryRow(ctx, `SELECT envelope, body_jcs FROM deedglass.deeds WHERE id = $1`, env.ID).Scan(&envBytes, &bodyJCS); err != nil {
			t.Fatal(err)
		}
		want, _ := env.MarshalCanonical()
		if string(envBytes) != string(want) || deed.Digest(bodyJCS) != env.BodyHash {
			t.Fatal("stored bytes differ from the sealed deed")
		}
	})
}

func TestSealKeyLifecycle(t *testing.T) {
	se := newSealEnv(t, 1, 4)
	org := se.orgs[0]
	if _, err := se.seal(org); err != nil {
		t.Fatal(err)
	}
	// Registering the same key again is a no-op; the same public key under another id is refused.
	if _, created, err := se.e.DB.RegisterSealKey(ctx, se.signer.KeyID(), se.signer.PublicKey()); err != nil || created {
		t.Fatalf("re-register: %v %v", created, err)
	}
	if _, _, err := se.e.DB.RegisterSealKey(ctx, ids.New[ids.SealKeyKind]().String(), se.signer.PublicKey()); !errors.Is(err, store.ErrSealKeyConflict) {
		t.Fatalf("same public key, new id: %v", err)
	}

	next, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), randomSeed(t))
	if err != nil {
		t.Fatal(err)
	}
	newRow, retired, err := se.e.DB.RotateSealKey(ctx, next.KeyID(), next.PublicKey(), time.Second)
	if err != nil || len(retired) != 1 || retired[0].ID != se.signer.KeyID() || newRow.Status != deed.KeyActive {
		t.Fatalf("rotate: %v %+v", err, retired)
	}
	if retired[0].Status != deed.KeyRetired || retired[0].RetiredAt == nil || !retired[0].RetiredAt.After(newRow.ActivatedAt) {
		t.Fatalf("retired row %+v", retired[0])
	}
	// Inside the overlap the old key is within 30 s of retiring, so the Sealer refuses it at once.
	if _, err := se.seal(org); !errors.Is(err, deed.ErrSealKeyRetired) {
		t.Fatalf("old key inside a 1 s overlap: %v", err)
	}
	time.Sleep(2 * time.Second)
	if _, err := se.seal(org); !errors.Is(err, deed.ErrSealKeyRetired) {
		t.Fatalf("old key after retirement: %v", err)
	}
	se.sealer = deed.NewSealer(next)
	if _, err := se.seal(org); err != nil {
		t.Fatalf("new key: %v", err)
	}
	keys, err := se.e.DB.SealKeys(ctx)
	if err != nil || len(keys) != 2 || keys[0].Status != deed.KeyRetired || keys[1].Status != deed.KeyActive {
		t.Fatalf("keys %+v %v", keys, err)
	}
	if s := se.chainOf(t, org); s.Deeds != 2 || len(s.SealKeys) != 2 {
		t.Fatalf("%+v", s)
	}
}

// TestSealerBudget measures Sealer.Seal (excluding COMMIT) over 1,000 sequential seals. It runs
// with DEEDGLASS_IT_BENCH=1 (make bench-deed) and logs one PERF line.
func TestSealerBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-deed)")
	}
	se := newSealEnv(t, 1, 4)
	org := se.orgs[0]
	const n = 1000
	ds := make([]time.Duration, 0, n)
	for i := 0; i < n+100; i++ {
		var d time.Duration
		err := se.e.DB.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			start := time.Now()
			_, err := se.sealer.Seal(ctx, store.DeedTx(tx), adminDraft(org))
			d = time.Since(start)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if i >= 100 { // warm-up
			ds = append(ds, d)
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	msf := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	var ver string
	_ = se.e.Admin(t).QueryRow(ctx, "SELECT pg_catalog.current_setting('server_version')").Scan(&ver)
	r := perf{Name: "Sealer.Seal in Postgres, excluding COMMIT", N: n, P50ms: msf(ds[n/2]), P99ms: msf(ds[n*99/100]), BudP50: 1, BudP99: 3}
	r.Within = r.P50ms <= 1 && r.P99ms <= 3
	b, _ := json.Marshal(struct {
		perf
		Server string `json:"server"`
	}{r, "PostgreSQL " + ver})
	t.Logf("PERF %s", b)
	if !r.Within {
		t.Errorf("p50 %.3f ms, p99 %.3f ms; budget 1 / 3 ms", r.P50ms, r.P99ms)
	}
	if s := se.chainOf(t, org); s.Deeds != n+100 {
		t.Fatalf("%+v", s)
	}
}

// TestSealDualOnPostgres (ADR-109, TY-02): an ML-DSA-65 key added beside Ed25519 with an overlap;
// v1 deeds until it activates, v2 dual-sealed after, the chain verifies against the published key
// set, an instance without the key refuses to seal once it is active, and migration 0033 takes the
// 1,952-byte key and the larger envelope.
func TestSealDualOnPostgres(t *testing.T) {
	se := newSealEnv(t, 1, 4)
	org := se.orgs[0]
	seed := randomSeed(t)
	pq, err := deed.NewMLDSASigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	row, retired, err := se.e.DB.AddPQSealKey(ctx, pq.KeyID(), pq.PublicKeyBytes(), 2*time.Second)
	if err != nil || len(retired) != 0 || row.Alg != deed.AlgMLDSA65 || !row.ActivatedAt.After(time.Now().Add(-time.Second)) {
		t.Fatalf("add: %v %+v %+v", err, row, retired)
	}
	se.sealer = deed.NewSealer(se.signer, deed.WithExtraSigners(pq))
	before, err := se.seal(org)
	if err != nil || before.Body.V != deed.Version {
		t.Fatalf("before activation: %v %+v", err, before)
	}
	time.Sleep(2500 * time.Millisecond)
	after, err := se.seal(org)
	if err != nil || after.Body.V != deed.VersionV2 || len(after.Seals) != 2 {
		t.Fatalf("after activation: %v %+v", err, after)
	}
	plain := deed.NewSealer(se.signer)
	se.sealer, plain = plain, se.sealer
	if _, err := se.seal(org); !errors.Is(err, deed.ErrSealKeyNotHeld) {
		t.Fatalf("instance without the PQ key: %v", err)
	}
	se.sealer = plain
	if s := se.chainOf(t, org); s.Deeds != 2 {
		t.Fatalf("%+v", s)
	}
	// A second ML-DSA-65 key activates at once and retires the first after the overlap.
	pq2, _ := deed.NewMLDSASigner(ids.New[ids.SealKeyKind]().String(), randomSeed(t))
	row2, retired2, err := se.e.DB.AddPQSealKey(ctx, pq2.KeyID(), pq2.PublicKeyBytes(), time.Minute)
	if err != nil || len(retired2) != 1 || retired2[0].ID != pq.KeyID() || row2.ActivatedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("second: %v %+v %+v", err, row2, retired2)
	}
}
