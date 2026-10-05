package deed

import (
	"sort"
	"time"
)

// Genesis returns the prev_hash of an org's first deed:
// "sha256:" + lowercase hex(SHA-256("deedglass/genesis\n" + org)). The database's chain trigger
// computes the same value (0006_deeds.sql).
func Genesis(org string) string { return Digest([]byte(TagGenesis + org)) }

// Head is the last deed of an org's chain. Seq 0 means the chain is empty.
type Head struct {
	Seq      int64
	BodyHash string
	IssuedAt time.Time
}

// ChainOptions tunes a ChainChecker.
type ChainOptions struct {
	// Org, when set, is the org every deed must belong to.
	Org string
	// Anchor, when set, is the deed just before the first one checked (a partial export whose
	// head the reader already holds). Without it the chain must start at seq 1 with the genesis
	// value.
	Anchor *Head
}

// ChainChecker checks a stream of verified deeds, in input order, as one org's chain: seq
// continuity, links and time order. It holds O(number of seal keys) memory, so S05 can stream
// 100,000 deeds through it.
type ChainChecker struct {
	opt  ChainOptions
	prev *Result
	sum  ChainSummary
	keys map[string]bool
}

// ChainSummary describes a checked chain.
type ChainSummary struct {
	Deeds         int
	Org           string
	FirstSeq      int64
	LastSeq       int64
	Head          string // body_hash of the last deed: keep it, it is what detects tail deletion
	FirstIssuedAt time.Time
	LastIssuedAt  time.Time
	SealKeys      []string // distinct seal key ids, sorted
}

// NewChainChecker starts a chain check.
func NewChainChecker(opt ChainOptions) *ChainChecker {
	return &ChainChecker{opt: opt, keys: map[string]bool{}}
}

// Next checks r against the previous deed. Call it in input order with each VerifyEnvelope result.
// After an error the checker keeps its last good deed, so a caller may report and stop.
func (c *ChainChecker) Next(r *Result) error {
	b := &r.Envelope.Body
	fail := func(code Code) *VerifyError {
		e := &VerifyError{Code: code, Seq: r.Seq, DeedID: r.DeedID}
		if c.prev != nil {
			e.Prev = c.prev.Seq
		}
		return e
	}
	wantOrg := c.opt.Org
	if c.prev != nil {
		wantOrg = c.prev.Org
	}
	if wantOrg != "" && r.Org != wantOrg {
		e := fail(CodeChainOrgMismatch)
		e.Org, e.WantOrg = r.Org, wantOrg
		return e
	}
	switch {
	case c.prev == nil && c.opt.Anchor == nil:
		if r.Seq != 1 {
			e := fail(CodeChainSeqGap)
			e.Want = 1
			return e
		}
		if b.PrevHash != Genesis(r.Org) { //deedglass:allow-compare a deed's public prev_hash
			return fail(CodeChainGenesisMismatch)
		}
	default:
		prevSeq, prevHash, prevTime := c.anchorOrPrev()
		switch {
		case r.Seq <= prevSeq:
			e := fail(CodeChainSeqOutOfOrder)
			e.Prev = prevSeq
			return e
		case r.Seq > prevSeq+1:
			e := fail(CodeChainSeqGap)
			e.Prev, e.Want = prevSeq, prevSeq+1
			return e
		}
		if b.PrevHash != prevHash { //deedglass:allow-compare a deed's public prev_hash
			e := fail(CodeChainPrevHashMismatch)
			e.Prev = prevSeq
			return e
		}
		if r.IssuedAt.Before(prevTime) {
			e := fail(CodeChainTimeRegression)
			e.Prev = prevSeq
			return e
		}
	}
	c.prev = r
	if c.sum.Deeds == 0 {
		c.sum.Org, c.sum.FirstSeq, c.sum.FirstIssuedAt = r.Org, r.Seq, r.IssuedAt
	}
	c.sum.Deeds++
	c.sum.LastSeq, c.sum.Head, c.sum.LastIssuedAt = r.Seq, r.BodyHash, r.IssuedAt
	c.keys[r.SealKeyID] = true
	return nil
}

func (c *ChainChecker) anchorOrPrev() (int64, string, time.Time) {
	if c.prev != nil {
		return c.prev.Seq, c.prev.BodyHash, c.prev.IssuedAt
	}
	a := c.opt.Anchor
	return a.Seq, a.BodyHash, a.IssuedAt
}

// Summary describes the deeds accepted so far.
func (c *ChainChecker) Summary() ChainSummary {
	s := c.sum
	s.SealKeys = make([]string, 0, len(c.keys))
	for k := range c.keys {
		s.SealKeys = append(s.SealKeys, k)
	}
	sort.Strings(s.SealKeys)
	return s
}

// Continue makes r the previous deed without checking it, so a verifier that lists every failure
// (verify --all) checks the next deed against r instead of reporting the same break again. r may
// be a deed that failed its own checks; its declared body_hash is what the next deed links to.
// Continue never adds r to the summary.
func (c *ChainChecker) Continue(r *Result) { c.prev = r }

// Restart forgets the previous deed, so the next deed is accepted as a new starting point (after an
// unreadable line, verify --all keeps checking links from the next readable deed). The caller has
// already reported the break.
func (c *ChainChecker) Restart(next *Result) {
	c.prev = nil
	c.opt.Anchor = &Head{Seq: next.Seq - 1, BodyHash: next.Envelope.Body.PrevHash}
}
