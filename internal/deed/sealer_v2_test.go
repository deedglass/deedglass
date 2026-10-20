package deed_test

import (
	"errors"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// The Sealer with an ML-DSA-65 key beside Ed25519 (ADR-109): v1 until the PQ key is active, then
// every deed dual-sealed; and never a weaker deed when the key set wants a seal it can't make.
func TestSealerDualSeal(t *testing.T) {
	now := deedtest.QActivated.Add(-time.Second)
	m, k := memStore(t, func() time.Time { return now })
	q := deedtest.KeyQ()
	m.SetKey(q.KeyID(), deed.KeyStatus{Alg: deed.AlgMLDSA65, Status: deed.KeyActive, ActivatedAt: deedtest.QActivated})
	s := deed.NewSealer(k.Signer, deed.WithExtraSigners(q))
	org := deedtest.Org(5)

	before, err := sealOne(t, m, s, org, 1)
	if err != nil || before.Body.V != deed.Version {
		t.Fatalf("before the PQ key is active: v%d, %v (want v1)", before.Body.V, err)
	}
	now = deedtest.QActivated.Add(time.Second)
	after, err := sealOne(t, m, s, org, 2)
	if err != nil || after.Body.V != deed.VersionV2 || len(after.Seals) != 2 || after.Seals[1].Alg != deed.AlgMLDSA65 {
		t.Fatalf("after: %+v, %v (want v2 with an ML-DSA-65 seal)", after, err)
	}
	if after.Seal != after.Seals[0].Seal || after.SealKeyID != k.ID {
		t.Fatal("the primary seal is the Ed25519 one")
	}
	// The chain verifies across the change against the published key set.
	sum := verifyStored(t, m, org, deedtest.KeySetV2())
	if sum.Deeds != 2 {
		t.Fatalf("%+v", sum)
	}

	// An instance without the PQ key refuses to seal once it is active (key-set rule).
	plain := deed.NewSealer(k.Signer)
	if _, err := sealOne(t, m, plain, org, 3); !errors.Is(err, deed.ErrSealKeyNotHeld) || !errors.Is(err, deed.ErrSealKeyNotActive) {
		t.Fatalf("got %v, want ErrSealKeyNotHeld", err)
	}
	// A revoked PQ key refuses too, rather than sealing without it.
	at := now.Add(-time.Millisecond)
	m.SetKey(q.KeyID(), deed.KeyStatus{Alg: deed.AlgMLDSA65, Status: deed.KeyRevoked, ActivatedAt: deedtest.QActivated, RevokedAt: &at})
	if _, err := sealOne(t, m, s, org, 3); !errors.Is(err, deed.ErrSealKeyRetired) {
		t.Fatalf("revoked PQ key: %v", err)
	}
}
