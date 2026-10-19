package deed

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"time"
)

// Pipeline sizes (S05 §3.4). Memory is bounded by (queue + window) × batch × MaxInputBytes in the
// worst case (every line at the 64 KiB limit); typical deeds are under 2 KiB.
const (
	streamBatch  = 64 // lines per batch
	streamQueue  = 16 // batches waiting for a worker
	streamWindow = 32 // batches read but not yet chain-checked
)

// Anchor is the deed just before the first one checked: its seq and body_hash (verify --anchor).
type Anchor struct {
	Seq      int64
	BodyHash string
}

// StreamOptions tunes a Verifier.
type StreamOptions struct {
	Keys       *KeySet                // nil: no key set at all (keys_missing, cannot determine)
	KeySource  func(id string) string // where each key came from, for the report; nil means ""
	KeysPinned bool                   // the operator chose these keys (--keys or --pin)
	Org        string                 // optional: every deed must belong to this org
	Anchor     *Anchor                // optional: the deed before the first one given
	Strict     bool                   // require canonical bytes (non_canonical)
	All        bool                   // collect up to MaxFailures failures instead of stopping at the first
	Workers    int                    // default runtime.GOMAXPROCS(0)
	OnProgress func(n int64)          // called after each batch with the deeds read so far
}

// Record is one deed's bytes and where they came from.
type Record struct {
	Data  []byte
	Input string // file name, "-" for stdin
	Line  int64  // 1-based line in Input (1 for a single-deed file)

	tooLong bool // the line passed MaxInputBytes and was skipped
}

// Verifier checks deeds as they stream in: seals in parallel, the chain in order, with bounded
// memory. Feed it with Stream (JSONL, possibly several inputs that continue one chain), Records
// (a directory: one chain per org) or Loose (single deeds, no chain), then call Report.
type Verifier struct {
	opt     StreamOptions
	rep     *Report
	started time.Time

	mode     int // 0 unset, modeChain, modePerOrg, modeLoose
	checkers map[string]*ChainChecker
	single   *ChainChecker
	resync   bool
	stopped  bool
	firstOrg string
	orgs     map[string]bool
	tally    keyTally
	empty    *KeySet
	input    InputStats
}

// InputStats describes the intact deeds of the most recent Stream call (a bundle's manifest is
// checked against it).
type InputStats struct {
	Count             int64
	FirstSeq, LastSeq int64
	From, To          string
	FirstPrevHash     string
	Kinds             map[string]int64
	HeadSeq           int64
	HeadDeedID        string
	HeadBodyHash      string
}

// Input returns the stats of the most recent Stream call.
func (v *Verifier) Input() InputStats { return v.input }

const (
	modeChain = iota + 1
	modePerOrg
	modeLoose
)

// NewVerifier starts a verification.
func NewVerifier(opt StreamOptions) *Verifier {
	if opt.Workers <= 0 {
		opt.Workers = runtime.GOMAXPROCS(0)
	}
	v := &Verifier{opt: opt, rep: NewReport(), started: time.Now(), checkers: map[string]*ChainChecker{},
		orgs: map[string]bool{}, empty: &KeySet{byID: map[string]*keyEntry{}}}
	v.rep.KeysPinned = opt.KeysPinned
	return v
}

// VerifyStream verifies one JSONL stream as one org's chain.
func VerifyStream(ctx context.Context, r io.Reader, opt StreamOptions) (*Report, error) {
	v := NewVerifier(opt)
	if err := v.Stream(ctx, r, "-"); err != nil {
		return nil, err
	}
	return v.Report(), nil
}

// Stopped reports whether verification stopped at a failure (so later inputs are not read).
func (v *Verifier) Stopped() bool { return v.stopped }

// Fail records a failure found outside the deeds (a bundle's manifest, for example) and stops
// unless All is set.
func (v *Verifier) Fail(f Failure) {
	v.rep.AddFailure(f)
	if !v.opt.All || CannotDetermine(f.Code) {
		v.stopped = true
	}
}

// SetBundle records a bundle's manifest checks.
func (v *Verifier) SetBundle(b *BundleStatus) { v.rep.Bundle = b }

// Warn adds a warning to the report.
func (v *Verifier) Warn(code, message, next string) { v.rep.AddWarning(code, message, next) }

// Stream verifies r as JSONL (one envelope per line) continuing the chain of earlier Stream calls.
// It returns an error only when r cannot be read; every deed problem is in the report.
func (v *Verifier) Stream(ctx context.Context, r io.Reader, input string) error {
	if v.stopped {
		return nil
	}
	v.setMode(modeChain)
	v.input = InputStats{Kinds: map[string]int64{"action": 0, "admin": 0, "amendment": 0}}
	br := bufio.NewReaderSize(r, MaxInputBytes+1)
	var line int64
	var pending error
	next := func() (Record, bool, error) {
		for {
			if pending != nil {
				return Record{}, false, pending
			}
			line++
			raw, err := br.ReadSlice('\n')
			switch {
			case errors.Is(err, bufio.ErrBufferFull):
				// Too long: skip to the end of the line and report it.
				for errors.Is(err, bufio.ErrBufferFull) {
					_, err = br.ReadSlice('\n')
				}
				if err != nil && !errors.Is(err, io.EOF) {
					pending = err
				} else if errors.Is(err, io.EOF) {
					pending = io.EOF
				}
				return Record{Input: input, Line: line, tooLong: true}, true, nil
			case errors.Is(err, io.EOF):
				pending = io.EOF
				if len(raw) == 0 {
					return Record{}, false, nil
				}
			case err != nil:
				return Record{}, false, err
			}
			data := getBuf(len(raw))
			data = append(data, raw...)
			if n := len(data); n > 0 && data[n-1] == '\n' {
				data = data[:n-1]
			}
			return Record{Data: data, Input: input, Line: line}, true, nil
		}
	}
	err := v.run(ctx, func() (Record, bool, error) {
		rec, ok, err := next()
		if errors.Is(err, io.EOF) {
			return Record{}, false, nil
		}
		return rec, ok, err
	}, true)
	return err
}

// Records verifies deeds that are already in memory, in the order given, as one chain per org
// (a directory of single-deed files, sorted by org and seq by the caller).
func (v *Verifier) Records(ctx context.Context, recs []Record) error {
	if v.stopped {
		return nil
	}
	if v.opt.Org == "" {
		v.setMode(modePerOrg)
	} else {
		v.setMode(modeChain)
	}
	i := 0
	return v.run(ctx, func() (Record, bool, error) {
		if i >= len(recs) {
			return Record{}, false, nil
		}
		i++
		return recs[i-1], true, nil
	}, false)
}

// Loose verifies one deed on its own: its seal, and the genesis link when its seq is 1. The chain
// is not checked (the headline says so).
func (v *Verifier) Loose(raw []byte, input string) {
	if v.stopped {
		return
	}
	v.setMode(modeLoose)
	out := v.check(Record{Data: raw, Input: input, Line: 1})
	v.consume(&out, Record{Input: input, Line: 1})
}

// COSE verifies one COSE_Sign1 deed (DEED_SPEC.md §12) on its own, like Loose.
func (v *Verifier) COSE(b []byte, input string) {
	if v.stopped {
		return
	}
	v.setMode(modeLoose)
	v.rep.Deeds++
	ks := v.opt.Keys
	if ks == nil {
		ks = v.empty
	}
	res, err := VerifyCOSE(b, ks)
	if err != nil {
		ve := err.(*VerifyError)
		if v.opt.Keys == nil && ve.Code == CodeSealKeyUnknown {
			ve = &VerifyError{Code: CodeKeysMissing, Seq: ve.Seq, Key: ve.Key}
		}
		f := FailureFrom(ve)
		f.Input, f.Line = ptr(input), ptr(int64(1))
		v.Fail(f)
		return
	}
	b2 := *res.Body
	v.intact(&Result{Envelope: &Envelope{Body: b2, SealKeyID: res.KeyID}, Seq: b2.Seq, DeedID: b2.DeedID, Org: b2.Org, SealKeyID: res.KeyID})
}

func (v *Verifier) setMode(m int) {
	if v.mode == 0 {
		v.mode = m
	}
}

// lineOut is one deed's result from a worker.
type lineOut struct {
	res      *Result
	err      *VerifyError
	env      *Envelope // the parsed envelope of a deed that failed after parsing
	computed string    // body_hash_mismatch: the hash of the body as received
}

type batch struct {
	idx    int
	recs   []Record
	outs   []lineOut
	pooled bool
}

// run is the pipeline: next is called from one producer goroutine; W workers verify seals; the
// calling goroutine reorders and chain-checks.
func (v *Verifier) run(ctx context.Context, next func() (Record, bool, error), pooled bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan *batch, streamQueue)
	results := make(chan *batch, streamQueue)
	window := make(chan struct{}, streamWindow)
	var readErr error

	go func() { // producer
		defer close(jobs)
		idx := 0
		for {
			b := &batch{idx: idx, pooled: pooled, recs: make([]Record, 0, streamBatch)}
			for len(b.recs) < streamBatch {
				rec, ok, err := next()
				if err != nil {
					readErr = err
					break
				}
				if !ok {
					break
				}
				b.recs = append(b.recs, rec)
			}
			if len(b.recs) == 0 {
				return
			}
			select {
			case window <- struct{}{}:
			case <-ctx.Done():
				b.release()
				return
			}
			last := len(b.recs) < streamBatch || readErr != nil // read before b is handed off
			select {
			case jobs <- b:
			case <-ctx.Done():
				b.release()
				return
			}
			if last {
				return
			}
			idx++
		}
	}()

	var wg sync.WaitGroup
	for range v.opt.Workers {
		wg.Add(1)
		go func() { // workers
			defer wg.Done()
			for b := range jobs {
				b.outs = make([]lineOut, len(b.recs))
				if ctx.Err() == nil {
					for i, rec := range b.recs {
						b.outs[i] = v.check(rec)
					}
				}
				results <- b
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	held := map[int]*batch{}
	want := 0
	for b := range results {
		held[b.idx] = b
		for {
			cur, ok := held[want]
			if !ok {
				break
			}
			delete(held, want)
			want++
			if !v.stopped && ctx.Err() == nil {
				for i := range cur.recs {
					v.consume(&cur.outs[i], cur.recs[i])
					if v.stopped {
						cancel()
						break
					}
				}
				if v.opt.OnProgress != nil {
					v.opt.OnProgress(v.rep.Deeds)
				}
			}
			cur.release()
			<-window
		}
	}
	for _, b := range held { // only after a cancel
		b.release()
	}
	if readErr != nil {
		return readErr
	}
	if err := ctx.Err(); err != nil && !v.stopped {
		return err
	}
	return nil
}

func (b *batch) release() {
	if !b.pooled {
		return
	}
	for _, r := range b.recs {
		putBuf(r.Data)
	}
	b.recs = nil
}

// check runs every per-deed check (safe to call from many goroutines).
func (v *Verifier) check(rec Record) lineOut {
	if rec.tooLong {
		return lineOut{err: errMalformed(fmt.Sprintf("line %d is longer than %d KiB", rec.Line, MaxInputBytes>>10))}
	}
	if len(rec.Data) == 0 {
		return lineOut{err: errMalformed(fmt.Sprintf("line %d is blank", rec.Line))}
	}
	ks := v.opt.Keys
	if ks == nil {
		ks = v.empty
	}
	res, err := VerifyEnvelope(rec.Data, ks, VerifyOptions{Strict: v.opt.Strict})
	if err == nil {
		return lineOut{res: res}
	}
	ve := err.(*VerifyError)
	if v.opt.Keys == nil && ve.Code == CodeSealKeyUnknown {
		ve = &VerifyError{Code: CodeKeysMissing, Seq: ve.Seq, DeedID: ve.DeedID, Key: ve.Key}
	}
	out := lineOut{err: ve}
	if ve.Code != CodeMalformed {
		if p, perr := parseEnvelope(rec.Data); perr == nil {
			out.env = p.env
			if ve.Code == CodeBodyHashMismatch {
				out.computed = Digest(p.bodyJCS)
			}
		}
	}
	return out
}

// consume applies one deed's result in input order: counts, the chain, and the report.
func (v *Verifier) consume(o *lineOut, rec Record) {
	v.rep.Deeds++
	at := func(f Failure) Failure {
		f.Input, f.Line = ptr(rec.Input), ptr(rec.Line)
		return f
	}
	if o.err != nil {
		f := at(FailureFrom(o.err))
		if o.computed != "" && o.env != nil {
			f.Expected, f.Computed = ptr(o.env.BodyHash), ptr(o.computed)
		}
		v.Fail(f)
		if v.mode == modeLoose {
			return
		}
		if o.env != nil {
			// Keep checking links from the deed as it claims to be (verify --all).
			r := resultOf(o.env)
			v.checker(r.Org).Continue(r)
		} else {
			v.resync = true
		}
		return
	}
	r := o.res
	switch v.mode {
	case modeLoose:
		var ce *VerifyError
		switch {
		case v.opt.Org != "" && r.Org != v.opt.Org:
			ce = &VerifyError{Code: CodeChainOrgMismatch, Seq: r.Seq, DeedID: r.DeedID, Org: r.Org, WantOrg: v.opt.Org}
		case r.Seq == 1 && r.Envelope.Body.PrevHash != Genesis(r.Org): //deedglass:allow-compare a deed's public prev_hash
			ce = &VerifyError{Code: CodeChainGenesisMismatch, Seq: r.Seq, DeedID: r.DeedID}
		}
		if ce != nil {
			v.Fail(at(FailureFrom(ce)))
			return
		}
	default:
		cc := v.checker(r.Org)
		if v.resync {
			cc.Restart(r)
			v.resync = false
		}
		if err := cc.Next(r); err != nil {
			v.Fail(at(FailureFrom(err.(*VerifyError))))
			cc.Continue(r)
			return
		}
	}
	v.intact(r)
}

// checker returns the chain checker for org (one per org in a directory, else one).
func (v *Verifier) checker(org string) *ChainChecker {
	if v.mode != modePerOrg {
		if v.single == nil {
			co := ChainOptions{Org: v.opt.Org}
			if a := v.opt.Anchor; a != nil {
				co.Anchor = &Head{Seq: a.Seq, BodyHash: a.BodyHash}
			}
			v.single = NewChainChecker(co)
		}
		return v.single
	}
	cc, ok := v.checkers[org]
	if !ok {
		cc = NewChainChecker(ChainOptions{})
		v.checkers[org] = cc
	}
	return cc
}

func (v *Verifier) intact(r *Result) {
	rep := v.rep
	rep.Intact++
	if v.firstOrg == "" {
		v.firstOrg = r.Org
	}
	v.orgs[r.Org] = true
	if rep.FirstSeq == nil || r.Seq < *rep.FirstSeq {
		rep.FirstSeq = ptr(r.Seq)
	}
	if rep.LastSeq == nil || r.Seq > *rep.LastSeq {
		rep.LastSeq = ptr(r.Seq)
	}
	t := r.Envelope.Body.IssuedAt
	if rep.From == nil || t < *rep.From {
		rep.From = ptr(t)
	}
	if rep.To == nil || t > *rep.To {
		rep.To = ptr(t)
	}
	b := &r.Envelope.Body
	rep.Kinds[string(b.Kind)]++
	in := &v.input
	if in.Kinds != nil {
		if in.Count == 0 {
			in.FirstSeq, in.From, in.FirstPrevHash = r.Seq, t, b.PrevHash
		}
		in.Count++
		in.LastSeq, in.To = r.Seq, t
		in.Kinds[string(b.Kind)]++
		in.HeadSeq, in.HeadDeedID, in.HeadBodyHash = r.Seq, r.DeedID, r.BodyHash
	}
	verdict := "none"
	if b.Witness != nil {
		verdict = b.Witness.Verdict
	}
	rep.Recorded[verdict]++
	ids := []string{r.SealKeyID}
	if r.Envelope != nil && len(r.Envelope.Seals) > 1 {
		ids = ids[:0]
		for _, s := range r.Envelope.Seals { // a v2 deed counts for every key that sealed it
			ids = append(ids, s.SealKeyID)
		}
	}
	for _, id := range ids {
		tp := ""
		if k, _, ok := v.opt.Keys.Lookup(id); ok {
			tp = k.Thumbprint
		}
		src := ""
		if v.opt.KeySource != nil {
			src = v.opt.KeySource(id)
		}
		v.tally.add(id, tp, src, r.Seq)
	}
}

// Report finishes the verification and returns the report.
func (v *Verifier) Report() *Report {
	rep := v.rep
	rep.SealKeys = v.tally.list()
	if len(v.orgs) == 1 {
		rep.Org = ptr(v.firstOrg)
	} else if len(v.orgs) == 0 && v.opt.Org != "" {
		rep.Org = ptr(v.opt.Org)
	}
	switch v.mode {
	case modeChain, modePerOrg:
		rep.Chain.Status = ChainIntact
		switch {
		case v.opt.Anchor != nil:
			rep.Chain.Anchor = ChainAnchor{Kind: "deed", Seq: ptr(v.opt.Anchor.Seq), BodyHash: ptr(v.opt.Anchor.BodyHash)}
		case rep.Org != nil:
			rep.Chain.Anchor = ChainAnchor{Kind: "genesis", Seq: ptr(int64(0)), BodyHash: ptr(Genesis(*rep.Org))}
		default:
			rep.Chain.Anchor = ChainAnchor{Kind: "genesis", Seq: ptr(int64(0))}
		}
	}
	rep.sortFailures()
	rep.settle()
	if rep.Result == ResultCannotDetermine && rep.Chain.Status == ChainIntact {
		rep.Chain.Status = ChainNotChecked
	}
	if rep.Deeds == 0 && len(rep.Failures) == 0 {
		rep.Chain.Status = ChainNotChecked
	}
	for _, k := range rep.SealKeys {
		if IsTestThumbprint(k.Thumbprint) {
			msg, next := WarningMessage(WarnTestKeys, nil)
			rep.AddWarning(WarnTestKeys, msg, next)
			break
		}
	}
	rep.DurationMS = time.Since(v.started).Milliseconds()
	return rep
}

// resultOf builds a chain Result from an envelope that failed its own checks, using the values it
// claims (verify --all links the next deed to them).
func resultOf(env *Envelope) *Result {
	t, _ := ParseTime(env.Body.IssuedAt)
	return &Result{Envelope: env, BodyHash: env.BodyHash, Seq: env.Body.Seq, DeedID: env.Body.DeedID,
		Org: env.Body.Org, IssuedAt: t, SealKeyID: env.SealKeyID}
}

// Line buffers are pooled: a 100,000-deed stream reuses a few hundred of them.
var bufPool = sync.Pool{New: func() any { b := make([]byte, 0, 4096); return &b }}

func getBuf(n int) []byte {
	p := bufPool.Get().(*[]byte)
	if cap(*p) < n {
		return make([]byte, 0, n)
	}
	return (*p)[:0]
}

func putBuf(b []byte) {
	if b == nil || cap(b) > 2*MaxInputBytes {
		return
	}
	b = b[:0]
	bufPool.Put(&b)
}
