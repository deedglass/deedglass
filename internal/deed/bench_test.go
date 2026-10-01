package deed_test

import (
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// BenchmarkSealBody: JCS + SHA-256 + Ed25519 sign. Budget ≤ 60 µs/op (S04 §3.11).
func BenchmarkSealBody(b *testing.B) {
	env, _ := base(b)
	k := deedtest.TestKey("a")
	body := env.Body
	b.ReportAllocs()
	for b.Loop() {
		if _, err := deed.SealBody(k.Signer, body); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkVerifyEnvelope: strict parse + schema + JCS + hash + Ed25519 verify, one core.
// Budget ≤ 120 µs/op.
func BenchmarkVerifyEnvelope(b *testing.B) {
	_, raw := base(b)
	ks := deedtest.KeySet()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := deed.VerifyEnvelope(raw, ks, deed.VerifyOptions{Strict: true}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSealerMemStore: the Sealer's own work around a store (no database).
func BenchmarkSealerMemStore(b *testing.B) {
	now := deedtest.T0.Add(time.Hour)
	m := deedtest.NewMemStore(func() time.Time { return now })
	k := deedtest.TestKey("b")
	m.SetKey(k.ID, deed.KeyStatus{Status: deed.KeyActive, ActivatedAt: deedtest.BActivated})
	s := deed.NewSealer(k.Signer)
	d := deed.Draft{Body: deedtest.AdminStopped(deedtest.Org(6), 1)}
	b.ReportAllocs()
	for b.Loop() {
		tx := m.Begin()
		if _, err := s.Seal(bg, tx, d); err != nil {
			b.Fatal(err)
		}
		tx.Commit()
	}
}

// BenchmarkChainCheck: VerifyEnvelope + ChainChecker over the 20-deed vector chain.
func BenchmarkChainCheck(b *testing.B) {
	c := deedtest.NewChain(20, deedtest.ChainOpts{})
	ks := deedtest.KeySet()
	b.ReportAllocs()
	for b.Loop() {
		cc := deed.NewChainChecker(deed.ChainOptions{})
		for _, l := range c.Lines {
			r, err := deed.VerifyEnvelope(l, ks, deed.VerifyOptions{})
			if err != nil {
				b.Fatal(err)
			}
			if err := cc.Next(r); err != nil {
				b.Fatal(err)
			}
		}
	}
}
