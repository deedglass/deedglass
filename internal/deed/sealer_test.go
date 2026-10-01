package deed_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

var bg = context.Background()

func memStore(t testing.TB, clock func() time.Time) (*deedtest.MemStore, deedtest.Key) {
	t.Helper()
	if clock == nil {
		clock = func() time.Time { return deedtest.T0.Add(time.Hour) }
	}
	m := deedtest.NewMemStore(clock)
	k := deedtest.TestKey("b")
	m.SetKey(k.ID, deed.KeyStatus{Status: deed.KeyActive, ActivatedAt: deedtest.BActivated})
	return m, k
}

func sealOne(t testing.TB, m *deedtest.MemStore, s *deed.Sealer, org string, n int) (*deed.Envelope, error) {
	t.Helper()
	tx := m.Begin()
	env, err := s.Seal(bg, tx, deed.Draft{Body: deedtest.AdminStopped(org, n)})
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	tx.Commit()
	return env, nil
}

func verifyStored(t testing.TB, m *deedtest.MemStore, org string, ks *deed.KeySet) deed.ChainSummary {
	t.Helper()
	cc := deed.NewChainChecker(deed.ChainOptions{Org: org})
	for _, d := range m.Deeds(org) {
		r, err := deed.VerifyEnvelope(d.EnvelopeJCS, ks, deed.VerifyOptions{Strict: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := cc.Next(r); err != nil {
			t.Fatal(err)
		}
	}
	return cc.Summary()
}

func TestSealerConcurrency(t *testing.T) {
	now := deedtest.T0.Add(time.Hour)
	m, k := memStore(t, func() time.Time { return now })
	s := deed.NewSealer(k.Signer)
	org := deedtest.Org(5)
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := sealOne(t, m, s, org, i+1); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	sum := verifyStored(t, m, org, deedtest.KeySet())
	if sum.Deeds != 50 || sum.LastSeq != 50 {
		t.Fatalf("%+v", sum)
	}
	t.Logf("✓ %d deeds · chain intact · %d seal key", sum.Deeds, len(sum.SealKeys))
}

func TestSealerRollbackBurnsNoSeq(t *testing.T) {
	m, k := memStore(t, nil)
	s := deed.NewSealer(k.Signer)
	org := deedtest.Org(5)
	tx := m.Begin()
	first, err := s.Seal(bg, tx, deed.Draft{Body: deedtest.AdminStopped(org, 1)})
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	again, err := sealOne(t, m, s, org, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Body.Seq != 1 || again.Body.Seq != 1 || again.Body.PrevHash != deed.Genesis(org) {
		t.Fatalf("seq %d then %d", first.Body.Seq, again.Body.Seq)
	}
}

// hooked runs after once ReadState returns: a writer that bypassed the chain lock.
type hooked struct {
	*deedtest.MemTx
	after func()
}

func (h hooked) ReadState(ctx context.Context, org, key string) (deed.ChainState, error) {
	st, err := h.MemTx.ReadState(ctx, org, key)
	if h.after != nil {
		h.after()
	}
	return st, err
}

func TestSealerConflict(t *testing.T) {
	m, k := memStore(t, nil)
	s := deed.NewSealer(k.Signer)
	org := deedtest.Org(5)
	c := deedtest.NewChain(1, deedtest.ChainOpts{Org: org, KeyFor: func(int64) deedtest.Key { return k }, Start: deedtest.BActivated})
	tx := m.Begin()
	_, err := s.Seal(bg, hooked{tx, func() {
		m.InsertRaw(deed.Stored{Envelope: c.Deeds[0], EnvelopeJCS: c.Lines[0], IssuedAt: deedtest.BActivated})
	}}, deed.Draft{Body: deedtest.AdminStopped(org, 2)})
	tx.Rollback()
	if !errors.Is(err, deed.ErrChainConflict) {
		t.Fatalf("got %v, want ErrChainConflict", err)
	}
	env, err := sealOne(t, m, s, org, 3) // the retry, in a new transaction
	if err != nil || env.Body.Seq != 2 || env.Body.PrevHash != c.Deeds[0].BodyHash {
		t.Fatalf("retry: %v", err)
	}
}

func TestSealerKeyRules(t *testing.T) {
	org := deedtest.Org(5)
	now := deedtest.T0.Add(time.Hour)
	at := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	cases := []struct {
		name string
		key  *deed.KeyStatus
		want error
	}{
		{"active", &deed.KeyStatus{Status: deed.KeyActive, ActivatedAt: deedtest.BActivated}, nil},
		{"retiring later", &deed.KeyStatus{Status: deed.KeyRetired, ActivatedAt: deedtest.BActivated, RetiredAt: at(31 * time.Second)}, nil},
		{"retiring in 29 s", &deed.KeyStatus{Status: deed.KeyRetired, ActivatedAt: deedtest.BActivated, RetiredAt: at(29 * time.Second)}, deed.ErrSealKeyRetired},
		{"retired", &deed.KeyStatus{Status: deed.KeyRetired, ActivatedAt: deedtest.BActivated, RetiredAt: at(-time.Second)}, deed.ErrSealKeyRetired},
		{"revoked", &deed.KeyStatus{Status: deed.KeyRevoked, ActivatedAt: deedtest.BActivated, RevokedAt: at(-time.Second)}, deed.ErrSealKeyRetired},
		{"not yet active", &deed.KeyStatus{Status: deed.KeyActive, ActivatedAt: now.Add(time.Second)}, deed.ErrSealKeyNotActive},
		{"unregistered", nil, deed.ErrSealKeyNotActive},
	}
	for _, tc := range cases {
		m := deedtest.NewMemStore(func() time.Time { return now })
		k := deedtest.TestKey("b")
		if tc.key != nil {
			m.SetKey(k.ID, *tc.key)
		}
		_, err := sealOne(t, m, deed.NewSealer(k.Signer), org, 1)
		if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
			t.Errorf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
}

// issued_at never goes backwards along seq, even when the database clock steps back.
func TestSealerClockStepsBack(t *testing.T) {
	now := deedtest.T0.Add(time.Hour)
	m, k := memStore(t, func() time.Time { return now })
	s := deed.NewSealer(k.Signer)
	org := deedtest.Org(5)
	e1, err := sealOne(t, m, s, org, 1)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Minute)
	e2, err := sealOne(t, m, s, org, 2)
	if err != nil {
		t.Fatal(err)
	}
	if e2.Body.IssuedAt != e1.Body.IssuedAt {
		t.Fatalf("issued_at %s then %s", e1.Body.IssuedAt, e2.Body.IssuedAt)
	}
	verifyStored(t, m, org, deedtest.KeySet())
}

func TestSealerRefusesBadDrafts(t *testing.T) {
	m, k := memStore(t, nil)
	s := deed.NewSealer(k.Signer)
	b := deedtest.AdminStopped(deedtest.Org(5), 1)
	b.Seq = 4
	if _, err := s.Seal(bg, m.Begin(), deed.Draft{Body: b}); !errors.Is(err, deed.ErrDraft) {
		t.Fatalf("got %v", err)
	}
	if s.KeyID() != k.ID {
		t.Fatal("KeyID")
	}
}

func TestSealerDeterministicIDs(t *testing.T) {
	m, k := memStore(t, nil)
	n := 0
	s := deed.NewSealer(k.Signer, deed.WithDeedIDs(func() string { n++; return deedtest.ID("deed", 900+n) }))
	env, err := sealOne(t, m, s, deedtest.Org(5), 1)
	if err != nil || env.ID != deedtest.ID("deed", 901) {
		t.Fatalf("%v %s", err, env.ID)
	}
}
