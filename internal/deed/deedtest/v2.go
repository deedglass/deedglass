package deedtest

import (
	"crypto/sha256"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// Deed v2 test keys (ADR-109). Key x stands for an algorithm no Deedglass verifier implements
// ("SLH-DSA-SHA2-128s" here): its "signatures" are SHA-256 of the seed and the message, which no
// verifier can or may check. It exists to show that a key set may list it (attack case Y07) and
// that a deed sealed with it is unsupported_algorithm (Y06). Never trust any of these keys.

// AlgUnknown is key x's algorithm name.
const AlgUnknown = "SLH-DSA-SHA2-128s"

// XActivated is when key x became active (with a and r, before the chain began).
var XActivated = T0.Add(-time.Hour)

// UnknownSigner is key x.
type UnknownSigner struct {
	ID   string
	Seed []byte
}

func (u UnknownSigner) KeyID() string { return u.ID }
func (u UnknownSigner) Alg() string   { return AlgUnknown }
func (u UnknownSigner) PublicKeyBytes() []byte {
	s := sha256.Sum256(append([]byte("pub:"), u.Seed...))
	return s[:]
}
func (u UnknownSigner) SealTag() string    { return "deedglass/deed/v2/slh-dsa-sha2-128s\n" }
func (u UnknownSigner) SealPrefix() string { return "slh-dsa-sha2-128s:" }
func (u UnknownSigner) Sign(msg []byte) ([]byte, error) {
	s := sha256.Sum256(append(append([]byte{}, u.Seed...), msg...))
	return s[:], nil
}

// KeyX returns key x.
func KeyX() UnknownSigner { return UnknownSigner{ID: ID("sk", 5), Seed: Seed("x")} }

// QActivated is when ML-DSA-65 key q became active, beside Ed25519 key b (the rotation that adds a
// PQ key, ADR-109): from then on the key-set rule wants an ML-DSA-65 seal on every deed.
var QActivated = T0.Add(20 * time.Second)

// KeyQ returns ML-DSA-65 test key q, signing deterministically so the vectors are reproducible.
func KeyQ() *deed.MLDSASigner {
	s, err := deed.NewMLDSASigner(ID("sk", 4), Seed("q"))
	if err != nil {
		panic(err)
	}
	return s.Deterministic()
}

// KeyRecordQ is key q as the server stores it; status and times can be changed by the caller.
func KeyRecordQ() deed.KeyRecord {
	q := KeyQ()
	return deed.KeyRecord{ID: q.KeyID(), Alg: deed.AlgMLDSA65, PublicKey: q.PublicKeyBytes(), Status: deed.KeyActive, ActivatedAt: QActivated}
}

// KeyRecordsV2 is the v2 vectors' key set: a, b and r as in v1, plus ML-DSA-65 key q and key x.
func KeyRecordsV2() []deed.KeyRecord {
	x := KeyX()
	return append(KeyRecords(), KeyRecordQ(),
		deed.KeyRecord{ID: x.ID, Alg: AlgUnknown, PublicKey: x.PublicKeyBytes(), Status: deed.KeyActive, ActivatedAt: XActivated})
}

// KeySetV2 is the published v2 test key set.
func KeySetV2() *deed.KeySet {
	ks, err := deed.NewKeySet(Issuer, KeyRecordsV2())
	if err != nil {
		panic(err)
	}
	return ks
}

// ResealV2 seals a modified copy of env as deed v2 with signers, keeping its chain fields.
func ResealV2(env *deed.Envelope, signers []deed.AlgSigner, edit func(*deed.Body)) *deed.Envelope {
	b := env.Body
	b.SealKeys, b.SealKeyID = nil, ""
	if edit != nil {
		edit(&b)
	}
	out, err := deed.SealBodyV2(signers, b)
	if err != nil {
		panic(err)
	}
	return out
}

// V2ChainOpts tunes NewChainV2: the deeds whose SignersFor returns nil are sealed as v1 with
// KeyFor, the others as v2 with those signers.
type V2ChainOpts struct {
	ChainOpts
	SignersFor func(seq int64) []deed.AlgSigner
}

// NewChainV2 seals n deeds into one org's chain, mixing v1 and v2 deeds.
func NewChainV2(n int, o V2ChainOpts) *Chain {
	if o.SignersFor == nil {
		o.SignersFor = func(int64) []deed.AlgSigner { return nil }
	}
	c := &Chain{Org: o.Org}
	if c.Org == "" {
		c.Org = Org(2)
	}
	start, step := o.Start, o.Step
	if start.IsZero() {
		start = T0
	}
	if step == 0 {
		step = time.Second
	}
	body := o.Body
	if body == nil {
		body = CycleBody
	}
	keyFor := o.KeyFor
	if keyFor == nil {
		a, b := TestKey("a"), TestKey("b")
		keyFor = func(seq int64) Key {
			if seq < 11 {
				return a
			}
			return b
		}
	}
	prev := deed.Genesis(c.Org)
	for i := 1; i <= n; i++ {
		seq := int64(i)
		b := body(c.Org, seq)
		b.DeedID = ID("deed", 100000*orgNum(c.Org)+i)
		b.Seq = seq
		b.PrevHash = prev
		b.IssuedAt = deed.FormatTime(start.Add(time.Duration(i-1) * step))
		var (
			env *deed.Envelope
			err error
		)
		if s := o.SignersFor(seq); s != nil {
			env, err = deed.SealBodyV2(s, b)
		} else {
			k := keyFor(seq)
			b.SealKeyID = k.ID
			env, err = deed.SealBody(k.Signer, b)
		}
		if err != nil {
			panic(err)
		}
		c.Deeds = append(c.Deeds, env)
		c.Lines = append(c.Lines, Canonical(env))
		prev = env.BodyHash
	}
	return c
}
