package deed

import (
	"context"
	"fmt"
	"time"
)

// ChainStore is the Sealer's view of one org-scoped transaction. internal/store implements it
// over pgx (store.DeedTx); deedtest has an in-memory one. Every method runs inside the caller's
// transaction, so the deed lands, or doesn't, with the state change it records.
type ChainStore interface {
	// LockChain blocks until this transaction owns the org's chain (pg_advisory_xact_lock). The
	// lock ends with the transaction.
	LockChain(ctx context.Context, org string) error
	// ReadState returns the chain head, the database clock and the seal key's row in one round
	// trip. Head.Seq is 0 for an empty chain.
	ReadState(ctx context.Context, org, sealKeyID string) (ChainState, error)
	// Insert writes the deeds row. A unique or chain-trigger violation returns an error that
	// wraps ErrChainConflict.
	Insert(ctx context.Context, d Stored) error
}

// ChainState is what the Sealer reads after taking the lock.
type ChainState struct {
	Head Head
	Now  time.Time // clock_timestamp()
	Key  KeyStatus // the primary seal key's row
	// Keys is every registered seal key, of every algorithm: the Sealer checks its extra keys and the
	// key-set rule (DEED_SPEC.md §11.4) against them.
	Keys []KeyStatus
}

// KeyStatus is a seal key's row.
type KeyStatus struct {
	ID          string // set in ChainState.Keys
	Alg         string // set in ChainState.Keys ("" means Ed25519)
	Found       bool
	Status      string // active | retired | revoked
	ActivatedAt time.Time
	RetiredAt   *time.Time
	RevokedAt   *time.Time
}

// Stored is one sealed deed as the store writes it.
type Stored struct {
	Envelope    *Envelope
	BodyJCS     []byte    // the bytes hashed into body_hash
	EnvelopeJCS []byte    // JCS(envelope), stored and served verbatim
	IssuedAt    time.Time // = body.issued_at
}

// RetireMargin: the Sealer refuses a key that retires within this long after issued_at, so an
// instance that missed a rotation errors instead of sealing right at the edge of the window.
const RetireMargin = 30 * time.Second

// Sealer seals drafts into an org's chain.
type Sealer struct {
	signer Signer
	extra  []AlgSigner // ADR-109: keys of other algorithms; with any, the Sealer writes deed v2
	newID  func(issued time.Time) string
}

// NewSealer returns a Sealer that seals with s.
func NewSealer(s Signer, opts ...SealerOption) *Sealer {
	r := &Sealer{signer: s, newID: NewDeedID}
	for _, o := range opts {
		o(r)
	}
	return r
}

// SealerOption tunes a Sealer.
type SealerOption func(*Sealer)

// WithDeedIDs replaces the deed id source (deterministic ids for test vectors).
func WithDeedIDs(next func() string) SealerOption {
	return func(s *Sealer) { s.newID = func(time.Time) string { return next() } }
}

// WithExtraSigners adds seal keys of other algorithms beside the Ed25519 key (ADR-109): the Sealer
// then seals every deed as v2, with one seal per key, the Ed25519 key first.
func WithExtraSigners(s ...AlgSigner) SealerOption {
	return func(r *Sealer) { r.extra = append(r.extra, s...) }
}

// ErrSealKeyNotHeld: the key set has a key of an algorithm active at issued_at that this Sealer
// doesn't hold, so any deed it sealed would fail the key-set rule. It wraps ErrSealKeyNotActive;
// the instance needs restarting with that key (DEEDGLASS_SEAL_PQ_KEY_FILE).
var ErrSealKeyNotHeld = fmt.Errorf("%w: a key of an algorithm active in the key set is not loaded", ErrSealKeyNotActive)

// KeyID is the seal key this Sealer uses.
func (s *Sealer) KeyID() string { return s.signer.KeyID() }

// Seal takes the org's chain lock, reads the head, the database clock and the key's status, fills
// deed_id, seq, prev_hash, issued_at and seal_key_id, seals, and inserts the deed, all inside
// the caller's transaction (DEED_SPEC.md §8, S04 §3.6). It returns ErrSealKeyRetired,
// ErrSealKeyNotActive or ErrChainConflict (wrapped) for the caller to act on; it never panics.
func (s *Sealer) Seal(ctx context.Context, cs ChainStore, d Draft) (*Envelope, error) {
	b := d.Body
	if err := b.validate(true); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDraft, err)
	}
	if err := cs.LockChain(ctx, b.Org); err != nil {
		return nil, fmt.Errorf("deed: chain lock: %w", err)
	}
	st, err := cs.ReadState(ctx, b.Org, s.signer.KeyID())
	if err != nil {
		return nil, fmt.Errorf("deed: chain state: %w", err)
	}
	// issued_at comes from the database clock under the lock and never goes backwards along seq,
	// even if the clock steps back.
	issued := st.Now.UTC().Truncate(time.Millisecond)
	if h := st.Head; h.Seq > 0 && issued.Before(h.IssuedAt) {
		issued = h.IssuedAt.UTC().Truncate(time.Millisecond)
	}
	if err := checkKey(st.Key, issued); err != nil {
		return nil, err
	}
	extra, err := s.checkExtra(st.Keys, issued)
	if err != nil {
		return nil, err
	}
	b.DeedID = s.newID(issued)
	b.Seq = st.Head.Seq + 1
	b.PrevHash = Genesis(b.Org)
	if st.Head.Seq > 0 {
		b.PrevHash = st.Head.BodyHash
	}
	b.IssuedAt = FormatTime(issued)
	var r *sealed
	if len(extra) == 0 {
		b.SealKeyID = s.signer.KeyID()
		r, err = sealBody(s.signer, b)
	} else {
		r, err = sealBodyV2(append([]AlgSigner{edAlgSigner{s.signer}}, extra...), b)
	}
	if err != nil {
		return nil, err
	}
	if err := cs.Insert(ctx, Stored{Envelope: r.env, BodyJCS: r.bodyJCS, EnvelopeJCS: r.envJCS, IssuedAt: issued}); err != nil {
		return nil, err
	}
	return r.env, nil
}

// checkExtra picks the extra keys to seal with at issued and applies the key-set rule. An extra key
// that isn't active yet is left out (the first ML-DSA-65 key activates after an overlap, so an
// instance restarted early keeps sealing v1 until then); one that is active must pass the key rule.
// Then every algorithm with a registered key active at issued must be one of the keys used, or the
// deed would fail verification (seal_missing): the Sealer errors instead of sealing a weaker deed.
func (s *Sealer) checkExtra(keys []KeyStatus, issued time.Time) ([]AlgSigner, error) {
	find := func(id string) KeyStatus {
		for _, k := range keys {
			if k.ID == id {
				return k
			}
		}
		return KeyStatus{}
	}
	held := map[string]bool{AlgEd25519: true}
	var use []AlgSigner
	for _, x := range s.extra {
		k := find(x.KeyID())
		if k.Found && k.RevokedAt == nil && issued.Before(k.ActivatedAt) {
			continue // not active yet
		}
		if err := checkKey(k, issued); err != nil {
			return nil, fmt.Errorf("%s key %s: %w", x.Alg(), x.KeyID(), err)
		}
		held[x.Alg()] = true
		use = append(use, x)
	}
	for _, k := range keys {
		alg := k.Alg
		if alg == "" {
			alg = AlgEd25519
		}
		active := k.RevokedAt == nil && !issued.Before(k.ActivatedAt) && (k.RetiredAt == nil || issued.Before(*k.RetiredAt))
		if active && !held[alg] {
			return nil, fmt.Errorf("%w: %s key %s is active", ErrSealKeyNotHeld, alg, k.ID)
		}
	}
	return use, nil
}

// checkKey applies the Sealer's key rule at issued: registered and activated, not revoked, and
// not retiring within RetireMargin. A key marked retired still seals inside its overlap window,
// so instances that haven't restarted yet keep working until then.
func checkKey(k KeyStatus, issued time.Time) error {
	switch {
	case !k.Found:
		return fmt.Errorf("%w: it is not registered", ErrSealKeyNotActive)
	case k.Status == KeyRevoked || k.RevokedAt != nil:
		return fmt.Errorf("%w: it was revoked", ErrSealKeyRetired)
	case issued.Before(k.ActivatedAt):
		return fmt.Errorf("%w: it activates at %s", ErrSealKeyNotActive, FormatTime(k.ActivatedAt))
	case k.RetiredAt != nil && k.RetiredAt.Sub(issued) < RetireMargin:
		return fmt.Errorf("%w: it retires at %s", ErrSealKeyRetired, FormatTime(*k.RetiredAt))
	case k.Status != KeyActive && k.Status != KeyRetired:
		return fmt.Errorf("%w: status %q", ErrSealKeyRetired, k.Status)
	}
	return nil
}
