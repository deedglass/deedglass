package stamp

import (
	"crypto/x509"
	"fmt"
	"sort"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// Failure codes of timestamp checks (spec/v2/BUNDLE_FORMAT.md §9.4). They are verification
// results, like bundle_invalid: exit 1, never "cannot decide".
const (
	// CodeTimestampInvalid: timestamps.json is malformed, or a token, Signed Statement or inclusion
	// proof in it doesn't verify. One good piece of evidence never covers a bad one.
	CodeTimestampInvalid = "timestamp_invalid"
	// CodeTimestampMismatch: the deed at a timestamped seq has another body_hash: the chain was
	// rewritten after the time the head was timestamped.
	CodeTimestampMismatch = "timestamp_mismatch"
	// CodeChainTailMissing: a head was timestamped at a seq past the last deed given: deeds were
	// removed from the end of the chain (or the export stops early).
	CodeChainTailMissing = "chain_tail_missing"
)

// Warning codes.
const (
	WarnTSANotPinned     = "tsa_not_pinned"
	WarnServiceNotPinned = "service_not_pinned"
	WarnBeforeRange      = "timestamp_before_range"
)

// CheckOptions are what the reader trusts.
type CheckOptions struct {
	Keys        *deed.KeySet   // the issuer's seal keys (Signed Statements)
	TSARoots    *x509.CertPool // nil: TSA chains are checked for integrity only
	ServiceKeys []JWK          // empty: each registration's own service_key is used, unpinned
}

// Evidence is one verified piece of evidence for a head.
type Evidence struct {
	Kind   string    `json:"kind"`   // rfc3161 | scitt
	Source string    `json:"source"` // the TSA or service URL, as stored
	Time   time.Time `json:"time"`   // genTime (rfc3161) or the Signed Statement's iat (scitt)
	Pinned bool      `json:"pinned"`
}

// Checked is one head whose evidence all verified.
type Checked struct {
	Head     Head
	Evidence []Evidence
	Earliest time.Time // the earliest time any piece of evidence gives
}

// Problem is one failed check, with what a person needs to read it.
type Problem struct {
	Code    string
	Seq     int64
	Message string
	Next    string
}

// Warning is something the reader should know that doesn't fail the check.
type Warning struct {
	Code, Message, Next string
}

// Check verifies every piece of evidence in f. It returns the heads that verified, the problems
// (in seq order) and the warnings. Comparing the heads with the chain is Compare's job.
func Check(f *File, opt CheckOptions) ([]Checked, []Problem, []Warning) {
	var (
		out      []Checked
		probs    []Problem
		unpinTSA bool
		unpinSvc bool
	)
	for _, e := range f.Heads {
		h := e.Head
		fail := func(format string, a ...any) {
			probs = append(probs, Problem{Code: CodeTimestampInvalid, Seq: h.Seq,
				Message: fmt.Sprintf("The timestamp of deed %s doesn't verify: %s.", deed.FormatCount(h.Seq), fmt.Sprintf(format, a...)),
				Next:    "Treat this timestamp as unusable, and ask the issuer for the timestamps.json it wrote."})
		}
		imprint, err := h.Imprint()
		if err != nil {
			fail("the head is not valid (%v)", err)
			continue
		}
		c := Checked{Head: h}
		ok := true
		for _, t := range e.RFC3161 {
			der, _ := b64.DecodeString(t.Token)
			info, err := VerifyToken(der, imprint, opt.TSARoots)
			if err != nil {
				fail("the token from %s: %v", t.TSA, err)
				ok = false
				continue
			}
			unpinTSA = unpinTSA || !info.Pinned
			c.Evidence = append(c.Evidence, Evidence{Kind: "rfc3161", Source: t.TSA, Time: info.GenTime, Pinned: info.Pinned})
		}
		for _, r := range e.SCITT {
			stmt, _ := b64.DecodeString(r.Statement)
			si, err := VerifyStatement(stmt, h, opt.Keys)
			if err != nil {
				fail("the registration with %s: %v", r.Service, err)
				ok = false
				continue
			}
			key, pinned := r.ServiceKey, false
			if len(opt.ServiceKeys) > 0 {
				found := false
				for _, k := range opt.ServiceKeys {
					found = found || k == r.ServiceKey
				}
				if !found {
					fail("the registration with %s is signed by a service key you didn't pass (--scitt-key)", r.Service)
					ok = false
					continue
				}
				pinned = true
			}
			incl, _ := b64.DecodeString(r.Inclusion)
			if _, err := VerifyInclusion(incl, stmt, key); err != nil {
				fail("the registration with %s: %v", r.Service, err)
				ok = false
				continue
			}
			unpinSvc = unpinSvc || !pinned
			c.Evidence = append(c.Evidence, Evidence{Kind: "scitt", Source: r.Service, Time: si.IssuedAt, Pinned: pinned})
		}
		if !ok {
			continue
		}
		for i, ev := range c.Evidence {
			if i == 0 || ev.Time.Before(c.Earliest) {
				c.Earliest = ev.Time
			}
		}
		out = append(out, c)
	}
	var warns []Warning
	if unpinTSA {
		warns = append(warns, Warning{Code: WarnTSANotPinned,
			Message: "The TSA chains were checked against themselves, not against roots you trust.",
			Next:    "Pass the TSAs' roots with --tsa-roots roots.pem to check who signed each timestamp."})
	}
	if unpinSvc {
		warns = append(warns, Warning{Code: WarnServiceNotPinned,
			Message: "The transparency service's key came from the timestamps file, not from you.",
			Next:    "Pass the service's published key with --scitt-key key.jwk to check who signed the inclusion proof."})
	}
	return out, probs, warns
}

// Observed is what the verifier saw of one org's chain: the intact deeds' range and the body
// hashes at the seqs the heads name.
type Observed struct {
	Org               string
	FirstSeq, LastSeq int64
	BodyHash          map[int64]string // seq → body_hash, for the seqs Wanted listed
}

// Wanted lists the seqs whose body hashes Compare needs, for each org.
func Wanted(files ...*File) map[string]map[int64]bool {
	out := map[string]map[int64]bool{}
	for _, f := range files {
		if f == nil {
			continue
		}
		m := out[f.Org]
		if m == nil {
			m = map[int64]bool{}
			out[f.Org] = m
		}
		for _, e := range f.Heads {
			m[e.Head.Seq] = true
		}
	}
	return out
}

// Compare checks verified heads against the chain: a head inside the range must match the deed at
// its seq (timestamp_mismatch otherwise), a head past the last deed means the tail is missing
// (chain_tail_missing), and a head before the first deed can't be compared (a warning).
func Compare(heads []Checked, o Observed) ([]Problem, []Warning) {
	sort.SliceStable(heads, func(i, j int) bool { return heads[i].Head.Seq < heads[j].Head.Seq })
	var probs []Problem
	before := 0
	for _, c := range heads {
		h := c.Head
		when := c.Earliest.UTC().Format("2006-01-02 15:04:05") + " UTC"
		switch {
		case h.Org != o.Org:
			probs = append(probs, Problem{Code: CodeTimestampInvalid, Seq: h.Seq,
				Message: fmt.Sprintf("A timestamped head belongs to %s, not to the org of these deeds (%s).", h.Org, o.Org),
				Next:    "Pass the timestamps of this org's chain."})
		case h.Seq > o.LastSeq:
			probs = append(probs, Problem{Code: CodeChainTailMissing, Seq: h.Seq,
				Message: fmt.Sprintf("Deeds after %s are missing: deed %s was timestamped at %s.", deed.FormatCount(o.LastSeq), deed.FormatCount(h.Seq), when),
				Next:    fmt.Sprintf("Ask the issuer for the chain up to deed %s. If they can't give it, treat the chain as cut short after that time.", deed.FormatCount(h.Seq))})
		case h.Seq < o.FirstSeq:
			before++
		default:
			got, ok := o.BodyHash[h.Seq]
			if !ok {
				continue // the deed failed on its own; that failure is the answer
			}
			if got != h.BodyHash { //deedglass:allow-compare a deed's public body hash
				probs = append(probs, Problem{Code: CodeTimestampMismatch, Seq: h.Seq,
					Message: fmt.Sprintf("Deed %s doesn't match the head timestamped at %s: the chain was changed after that time.", deed.FormatCount(h.Seq), when),
					Next:    fmt.Sprintf("Treat deed %s and every later deed as altered, and ask the issuer why.", deed.FormatCount(h.Seq))})
			}
		}
	}
	var warns []Warning
	if before > 0 {
		warns = append(warns, Warning{Code: WarnBeforeRange,
			Message: fmt.Sprintf("%s before deed %s could not be compared with these deeds.", plural(before, "1 timestamped head", deed.FormatCount(int64(before))+" timestamped heads"), deed.FormatCount(o.FirstSeq)),
			Next:    "Verify the earlier deeds together with these to compare them."})
	}
	return probs, warns
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
