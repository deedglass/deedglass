package deed_test

import (
	"errors"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// check runs lines through VerifyEnvelope and a ChainChecker; it returns the first error.
func check(t testing.TB, ks *deed.KeySet, opt deed.ChainOptions, lines [][]byte) (deed.ChainSummary, error) {
	t.Helper()
	cc := deed.NewChainChecker(opt)
	for _, l := range lines {
		r, err := deed.VerifyEnvelope(l, ks, deed.VerifyOptions{Strict: true})
		if err != nil {
			return cc.Summary(), err
		}
		if err := cc.Next(r); err != nil {
			return cc.Summary(), err
		}
	}
	return cc.Summary(), nil
}

func TestChainIntact(t *testing.T) {
	c := deedtest.NewChain(20, deedtest.ChainOpts{})
	s, err := check(t, deedtest.KeySet(), deed.ChainOptions{Org: c.Org}, c.Lines)
	if err != nil {
		t.Fatal(err)
	}
	if s.Deeds != 20 || s.FirstSeq != 1 || s.LastSeq != 20 || s.Head != c.Deeds[19].BodyHash || len(s.SealKeys) != 2 || s.Org != c.Org {
		t.Fatalf("summary %+v", s)
	}
	if !s.FirstIssuedAt.Equal(deedtest.T0) || !s.LastIssuedAt.Equal(deedtest.T0.Add(19*time.Second)) {
		t.Fatalf("times %v %v", s.FirstIssuedAt, s.LastIssuedAt)
	}
}

func TestChainFailures(t *testing.T) {
	ks := deedtest.KeySet()
	c := deedtest.NewChain(6, deedtest.ChainOpts{})
	other := deedtest.NewChain(2, deedtest.ChainOpts{Org: deedtest.Org(3)})
	l := c.Lines
	// A chain whose deed 5 was sealed (validly) one second before deed 4.
	regress := deedtest.NewChain(5, deedtest.ChainOpts{Body: deedtest.CycleBody, Start: deedtest.T0})
	{
		e := deedtest.Reseal(regress.Deeds[4], deedtest.TestKey("a"), func(b *deed.Body) { b.IssuedAt = deed.FormatTime(deedtest.T0.Add(2 * time.Second)) })
		regress.Lines[4] = deedtest.Canonical(e)
	}
	// Seq 1 that links to another org's genesis value, sealed validly.
	wrongGenesis := deedtest.Canonical(deedtest.Reseal(c.Deeds[0], deedtest.TestKey("a"), func(b *deed.Body) { b.PrevHash = deed.Genesis(deedtest.Org(3)) }))
	// Deed 3 resealed with a prev_hash that isn't deed 2's body_hash.
	prevBreak := deedtest.Canonical(deedtest.Reseal(c.Deeds[2], deedtest.TestKey("a"), func(b *deed.Body) { b.PrevHash = c.Deeds[0].BodyHash }))

	cases := []struct {
		name  string
		lines [][]byte
		opt   deed.ChainOptions
		code  deed.Code
		seq   int64
		prev  int64
	}{
		{"seq gap (F03)", [][]byte{l[0], l[1], l[3]}, deed.ChainOptions{}, deed.CodeChainSeqGap, 4, 2},
		{"duplicate seq", [][]byte{l[0], l[1], l[1]}, deed.ChainOptions{}, deed.CodeChainSeqOutOfOrder, 2, 2},
		{"out of order", [][]byte{l[0], l[2], l[1]}, deed.ChainOptions{}, deed.CodeChainSeqGap, 3, 1},
		{"backwards", [][]byte{l[0], l[1], l[2], l[0]}, deed.ChainOptions{}, deed.CodeChainSeqOutOfOrder, 1, 3},
		{"prev break", [][]byte{l[0], l[1], prevBreak}, deed.ChainOptions{}, deed.CodeChainPrevHashMismatch, 3, 2},
		{"wrong genesis", [][]byte{wrongGenesis}, deed.ChainOptions{}, deed.CodeChainGenesisMismatch, 1, 0},
		{"mixed orgs (F04)", [][]byte{l[0], other.Lines[1]}, deed.ChainOptions{}, deed.CodeChainOrgMismatch, 2, 1},
		{"expected org", [][]byte{other.Lines[0]}, deed.ChainOptions{Org: c.Org}, deed.CodeChainOrgMismatch, 1, 0},
		{"starts mid-chain without an anchor", [][]byte{l[2], l[3]}, deed.ChainOptions{}, deed.CodeChainSeqGap, 3, 0},
		{"time regression", regress.Lines, deed.ChainOptions{}, deed.CodeChainTimeRegression, 5, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := check(t, ks, tc.opt, tc.lines)
			var ve *deed.VerifyError
			if !errors.As(err, &ve) || ve.Code != tc.code || ve.Seq != tc.seq || ve.Prev != tc.prev {
				t.Fatalf("got %+v, want %s at seq %d after %d", err, tc.code, tc.seq, tc.prev)
			}
			if s, n := deed.Message(ve); s == "" || n == "" {
				t.Fatal("empty message")
			}
		})
	}
}

func TestChainAnchor(t *testing.T) {
	c := deedtest.NewChain(8, deedtest.ChainOpts{})
	ks := deedtest.KeySet()
	at := c.Deeds[4]
	anchor := &deed.Head{Seq: 5, BodyHash: at.BodyHash, IssuedAt: deedtest.T0.Add(4 * time.Second)}
	if _, err := check(t, ks, deed.ChainOptions{Anchor: anchor}, c.Lines[5:]); err != nil {
		t.Fatalf("anchored tail: %v", err)
	}
	if _, err := check(t, ks, deed.ChainOptions{Anchor: anchor}, c.Lines[6:]); deed.CodeOf(err) != deed.CodeChainSeqGap {
		t.Fatalf("anchored gap: %v", err)
	}
	bad := &deed.Head{Seq: 5, BodyHash: c.Deeds[3].BodyHash, IssuedAt: anchor.IssuedAt}
	if _, err := check(t, ks, deed.ChainOptions{Anchor: bad}, c.Lines[5:]); deed.CodeOf(err) != deed.CodeChainPrevHashMismatch {
		t.Fatalf("wrong anchor: %v", err)
	}
}

func TestGenesisMatchesSQL(t *testing.T) {
	// The value the 0006_deeds.sql trigger computes for this org (checked in S02's integration
	// test; pinned here so the Go and SQL sides never drift).
	org := "org_01j9zq4m6e8r0t2v4x6z8b0d2f"
	want := "sha256:" + hexSHA256("deedglass/genesis\n"+org)
	if got := deed.Genesis(org); got != want {
		t.Fatalf("Genesis = %s, want %s", got, want)
	}
}
