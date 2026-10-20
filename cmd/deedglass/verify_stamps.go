package main

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/stamp"
)

// deedglass verify and head timestamps (TY-03, ADR-112): timestamps.json inside a bundle, or
// files passed with --timestamps, are checked offline after the deeds. A head inside the range must
// match the deed at its seq; a head past the last deed means the tail is missing.

// stampSet is every timestamps file of one verify run and what the deeds showed of them.
type stampSet struct {
	files  []stampFile
	wanted map[string]map[int64]bool
	seen   map[string]*orgSeen
	opt    stamp.CheckOptions
}

type stampFile struct {
	f     *stamp.File
	input string // the file it came from, for failures
}

// orgSeen is the intact deeds of one org: their range and the hashes at the wanted seqs.
type orgSeen struct {
	first, last int64
	hashes      map[int64]string
}

// loadStampArgs reads --timestamps, --tsa-roots and --scitt-key. Anything unreadable is an input
// error (exit 2): the reader passed it on purpose.
func loadStampArgs(o verifyOpts) (*stampSet, error) {
	s := &stampSet{seen: map[string]*orgSeen{}}
	for _, p := range o.timestamps {
		raw, err := readCapped(p, stamp.MaxFileBytes)
		if err == nil {
			var f *stamp.File
			if f, err = stamp.ParseFile(raw, stamp.MaxFileBytes); err == nil {
				s.files = append(s.files, stampFile{f, p})
				continue
			}
		}
		return nil, inputErr("input_unreadable", fmt.Errorf("Can't read timestamps from %s: %s", clean(p), err.Error()),
			"Pass a timestamps.json written by deedglass deeds timestamp or found in an export bundle.")
	}
	if o.tsaRoots != "" {
		raw, err := readCapped(o.tsaRoots, 1<<20)
		var pool *x509.CertPool
		if err == nil {
			pool, err = stamp.ParseRoots(raw)
		}
		if err != nil {
			return nil, inputErr("input_unreadable", fmt.Errorf("Can't read TSA roots from %s: %s", clean(o.tsaRoots), err.Error()),
				"Pass the PEM file your TSAs publish for their roots.")
		}
		s.opt.TSARoots = pool
	}
	for _, p := range o.scittKeys {
		raw, err := readCapped(p, 64<<10)
		var k stamp.JWK
		if err == nil {
			err = json.Unmarshal(raw, &k)
		}
		if err == nil {
			err = k.Validate()
		}
		if err != nil {
			return nil, inputErr("input_unreadable", fmt.Errorf("Can't read a transparency-service key from %s: %s", clean(p), reasonOf(err)),
				"Pass the service's public key as a JWK (EC P-256 or OKP Ed25519).")
		}
		s.opt.ServiceKeys = append(s.opt.ServiceKeys, k)
	}
	return s, nil
}

func reasonOf(err error) string {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return "not JSON"
	}
	return err.Error()
}

// addBundle adds a bundle's own timestamps.json. A file that doesn't parse is the bundle's
// failure (bundle_invalid), reported in order like any other.
func (s *stampSet) addBundle(raw []byte, input string) error {
	f, err := stamp.ParseFile(raw, stamp.MaxBundleBytes)
	if err != nil {
		return &deed.VerifyError{Code: deed.CodeBundleInvalid, Detail: "This bundle's timestamps.json is not valid (" + err.Error() + ")."}
	}
	s.files = append(s.files, stampFile{f, input})
	return nil
}

func (s *stampSet) active() bool { return s != nil && len(s.files) > 0 }

// prepare lists the seqs to capture; call it after every input is opened.
func (s *stampSet) prepare() {
	var fs []*stamp.File
	for _, f := range s.files {
		fs = append(fs, f.f)
	}
	s.wanted = stamp.Wanted(fs...)
}

// onIntact records the range of each org and the body hash at every wanted seq.
func (s *stampSet) onIntact(org string, seq int64, bodyHash string) {
	o := s.seen[org]
	if o == nil {
		o = &orgSeen{first: seq, last: seq, hashes: map[int64]string{}}
		s.seen[org] = o
	}
	if seq < o.first {
		o.first = seq
	}
	if seq > o.last {
		o.last = seq
	}
	if s.wanted[org][seq] {
		o.hashes[seq] = bodyHash
	}
}

// check verifies every file and compares its heads with the deeds. It returns the heads for the
// report.
func (s *stampSet) check(v *deed.Verifier, ks *deed.KeySet) []deed.TimestampedHead {
	s.opt.Keys = ks
	var out []deed.TimestampedHead
	warned := map[string]bool{}
	warn := func(w stamp.Warning) {
		if !warned[w.Code+w.Message] {
			warned[w.Code+w.Message] = true
			v.Warn(w.Code, w.Message, w.Next)
		}
	}
	fail := func(p stamp.Problem, input string) {
		seq := p.Seq
		in := input
		v.Fail(deed.Failure{Code: p.Code, Seq: &seq, Input: &in, Message: p.Message, Next: p.Next})
	}
	for _, sf := range s.files {
		if v.Stopped() {
			break
		}
		checked, probs, warns := stamp.Check(sf.f, s.opt)
		for _, p := range probs {
			fail(p, sf.input)
		}
		for _, w := range warns {
			warn(w)
		}
		o := s.seen[sf.f.Org]
		if o == nil {
			for _, c := range checked {
				out = append(out, row(c, "not_checked"))
			}
			continue
		}
		cmp, cwarns := stamp.Compare(checked, stamp.Observed{Org: sf.f.Org, FirstSeq: o.first, LastSeq: o.last, BodyHash: o.hashes})
		bad := map[int64]string{}
		for _, p := range cmp {
			fail(p, sf.input)
			switch p.Code {
			case stamp.CodeChainTailMissing:
				bad[p.Seq] = "missing"
			case stamp.CodeTimestampMismatch:
				bad[p.Seq] = "mismatch"
			}
		}
		for _, w := range cwarns {
			warn(w)
		}
		for _, c := range checked {
			st := "matches"
			switch {
			case bad[c.Head.Seq] != "":
				st = bad[c.Head.Seq]
			case c.Head.Seq < o.first:
				st = "before_range"
			case o.hashes[c.Head.Seq] == "":
				st = "not_checked"
			}
			out = append(out, row(c, st))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func row(c stamp.Checked, compared string) deed.TimestampedHead {
	h := deed.TimestampedHead{Seq: c.Head.Seq, BodyHash: c.Head.BodyHash, At: deed.FormatTime(c.Earliest), Compared: compared,
		Evidence: []deed.TimestampEvidence{}}
	for _, e := range c.Evidence {
		h.Evidence = append(h.Evidence, deed.TimestampEvidence{Kind: e.Kind, Source: e.Source, Time: deed.FormatTime(e.Time), Pinned: e.Pinned})
	}
	return h
}

// printTimestamps prints one line per timestamped head (the latest few) after a clean verify.
func printTimestamps(e *Env, heads []deed.TimestampedHead) {
	s := e.Style
	const show = 3
	start := 0
	if len(heads) > show {
		start = len(heads) - show
		fmt.Fprintln(e.Stdout, s.OKLine(fmt.Sprintf("%s timestamped heads verified %s showing the latest %d",
			deed.FormatCount(int64(len(heads))), ui.Sep, show)))
	}
	for _, h := range heads[start:] {
		var tsa, svc int
		for _, ev := range h.Evidence {
			if ev.Kind == "rfc3161" {
				tsa++
			} else {
				svc++
			}
		}
		var by []string
		if tsa > 0 {
			by = append(by, count(int64(tsa), "TSA", "TSAs"))
		}
		if svc > 0 {
			by = append(by, count(int64(svc), "transparency service", "transparency services"))
		}
		when := strings.Replace(strings.TrimSuffix(h.At, ".000Z"), "T", " ", 1)
		when = strings.TrimSuffix(when, "Z") + " UTC"
		line := fmt.Sprintf("Deed %s timestamped %s by %s", deed.FormatCount(h.Seq), when, strings.Join(by, " and "))
		switch h.Compared {
		case "matches":
			fmt.Fprintln(e.Stdout, s.OKLine(line+" "+ui.Sep+" matches the chain"))
		case "before_range", "not_checked":
			fmt.Fprintln(e.Stdout, s.UnknownLine(line+" "+ui.Sep+" not compared with these deeds"))
		}
	}
}
