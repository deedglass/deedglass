package deedtest

import (
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// ChainOpts tunes NewChain. The zero value is the vector chain: org 2, from T0 one second apart,
// key a for seq 1–10 and key b from seq 11.
type ChainOpts struct {
	Org    string
	Start  time.Time
	Step   time.Duration
	KeyFor func(seq int64) Key
	Body   func(org string, seq int64) deed.Body // a draft body; the default cycles kinds
}

// Chain is a sealed chain.
type Chain struct {
	Org   string
	Deeds []*deed.Envelope
	Lines [][]byte // JCS(envelope) per deed, no newline
}

// NewChain seals n deeds into one org's chain with SealBody (no store, no clock).
func NewChain(n int, o ChainOpts) *Chain {
	if o.Org == "" {
		o.Org = Org(2)
	}
	if o.Start.IsZero() {
		o.Start = T0
	}
	if o.Step == 0 {
		o.Step = time.Second
	}
	if o.KeyFor == nil {
		a, b := TestKey("a"), TestKey("b")
		o.KeyFor = func(seq int64) Key {
			if seq < 11 {
				return a
			}
			return b
		}
	}
	if o.Body == nil {
		o.Body = CycleBody
	}
	c := &Chain{Org: o.Org}
	prev := deed.Genesis(o.Org)
	for i := 1; i <= n; i++ {
		seq := int64(i)
		b := o.Body(o.Org, seq)
		k := o.KeyFor(seq)
		b.DeedID = ID("deed", 100000*orgNum(o.Org)+i)
		b.Seq = seq
		b.PrevHash = prev
		b.IssuedAt = deed.FormatTime(o.Start.Add(time.Duration(i-1) * o.Step))
		b.SealKeyID = k.ID
		env, err := deed.SealBody(k.Signer, b)
		if err != nil {
			panic(err)
		}
		line, err := env.MarshalCanonical()
		if err != nil {
			panic(err)
		}
		c.Deeds = append(c.Deeds, env)
		c.Lines = append(c.Lines, line)
		prev = env.BodyHash
	}
	return c
}

// orgNum keeps deed ids distinct across test orgs.
func orgNum(org string) int {
	for n := 1; n <= 9; n++ {
		if Org(n) == org {
			return n
		}
	}
	return 0
}

// CycleBody gives a mix of kinds along a chain: mostly verified actions, a deny every 5th, an
// admin deed every 7th.
func CycleBody(org string, seq int64) deed.Body {
	n := int(seq)
	switch {
	case seq%7 == 0:
		return AdminStopped(org, n)
	case seq%5 == 0:
		return ActionDenied(org, n)
	}
	return ActionVerified(org, n)
}

// Reseal seals a modified copy of env with key k, keeping its chain fields (tamper vectors that
// carry a valid seal over the wrong content). edit may be nil. With signer set, it signs instead
// of k.Signer (k.ID still names the key).
func Reseal(env *deed.Envelope, k Key, edit func(*deed.Body), signer ...deed.Signer) *deed.Envelope {
	b := env.Body
	if edit != nil {
		edit(&b)
	}
	b.SealKeyID = k.ID
	var s deed.Signer = k.Signer
	if len(signer) > 0 {
		s = signer[0]
	}
	out, err := deed.SealBody(s, b)
	if err != nil {
		panic(err)
	}
	return out
}

// Canonical returns JCS(env).
func Canonical(env *deed.Envelope) []byte {
	b, err := env.MarshalCanonical()
	if err != nil {
		panic(err)
	}
	return b
}
