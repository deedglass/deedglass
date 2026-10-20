package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
)

// deedglass verify (contract §10, S05): check deeds offline. No database, no account, no telemetry;
// the network only for a --keys URL.

type verifyOpts struct {
	keys, pins       multiFlag
	timestamps       multiFlag // TY-03: timestamps.json files to compare the chain with
	scittKeys        multiFlag // TY-03: transparency-service keys (JWK) the reader trusts
	tsaRoots         string    // TY-03: PEM roots of the TSAs the reader trusts
	anchor           string
	file             string // S38: check this file against a file.sealed deed
	strict, all, q   bool
	maxBytes         int64
	maxBytesExplicit bool
}

var verifyFlagValues verifyOpts

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

func verifyFlags(fs *flag.FlagSet) {
	verifyFlagValues = verifyOpts{}
	bindVerifyFlags(fs, &verifyFlagValues)
}

func bindVerifyFlags(fs *flag.FlagSet, o *verifyOpts) {
	fs.Var(&o.keys, "keys", "seal key set: a keys.json file or an https URL (repeatable)")
	fs.Var(&o.pins, "pin", "accept only seal keys with this thumbprint, sha256:… (repeatable)")
	fs.StringVar(&o.anchor, "anchor", "", "<seq>:sha256:… the body_hash of the deed just before the first one given")
	fs.StringVar(&o.file, "file", "", "also check this file against the deed (a deed from deedglass deeds seal-file)")
	fs.Var(&o.timestamps, "timestamps", "a timestamps.json to compare the chain with (repeatable; a bundle's own is read too)")
	fs.StringVar(&o.tsaRoots, "tsa-roots", "", "PEM roots of the TSAs you trust (checks who signed each timestamp)")
	fs.Var(&o.scittKeys, "scitt-key", "a transparency service's public key as a JWK (repeatable)")
	fs.BoolVar(&o.strict, "strict", false, "require canonical bytes and pinned keys")
	fs.BoolVar(&o.all, "all", false, "list every failure (at most 100) instead of stopping at the first")
	fs.BoolVar(&o.q, "q", false, "print nothing; the exit code is the answer")
	fs.BoolVar(&o.q, "quiet", false, "print nothing; the exit code is the answer")
	fs.Int64Var(&o.maxBytes, "max-bytes", archive.DefaultLimits().Deeds, "the largest deeds.jsonl a bundle may hold")
}

// parseVerifyArgs reads flags before and after paths (the command has RawArgs: dispatch leaves
// parsing to it, so "--" still separates flags from paths).
func parseVerifyArgs(args []string) ([]string, error) {
	verifyFlagValues = verifyOpts{maxBytes: archive.DefaultLimits().Deeds}
	o := &verifyFlagValues
	var paths, after []string
	for i, a := range args { // everything after "--" is a path
		if a == "--" {
			args, after = args[:i], args[i+1:]
			break
		}
	}
	for len(args) > 0 {
		var more verifyOpts
		fs := flag.NewFlagSet("deedglass verify", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		bindVerifyFlags(fs, &more)
		if err := fs.Parse(args); err != nil {
			return nil, usageErr(fmt.Errorf("deedglass verify: %v", err), "Run deedglass help verify for its flags.")
		}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "anchor":
				o.anchor = more.anchor
			case "file":
				o.file = more.file
			case "tsa-roots":
				o.tsaRoots = more.tsaRoots
			case "strict":
				o.strict = o.strict || more.strict
			case "all":
				o.all = o.all || more.all
			case "q", "quiet":
				o.q = o.q || more.q
			case "max-bytes":
				o.maxBytes, o.maxBytesExplicit = more.maxBytes, true
			}
		})
		o.keys = append(o.keys, more.keys...)
		o.pins = append(o.pins, more.pins...)
		o.timestamps = append(o.timestamps, more.timestamps...)
		o.scittKeys = append(o.scittKeys, more.scittKeys...)
		args = fs.Args()
		if len(args) > 0 {
			paths = append(paths, args[0])
			args = args[1:]
		}
	}
	return append(paths, after...), nil
}

// Input kinds.
const (
	inJSONL  = "jsonl"  // one envelope per line, chained
	inJSON   = "json"   // one deed, checked on its own
	inBundle = "bundle" // export bundle v1
	inDir    = "dir"    // every *.json / *.jsonl, sorted by (org, seq), chained per org
	inCOSE   = "cose"   // COSE_Sign1, checked on its own
)

// vInput is one argument after detection.
type vInput struct {
	arg    string
	kind   string
	data   []byte // json, cose: the whole file
	reader io.Reader
	closer io.Closer
	bundle *archive.Reader
	err    error // bundle_invalid from archive.Open: reported as a failure in order
	// stampErr is a bundle's timestamps.json that doesn't parse (TY-03): reported after the
	// manifest, so a file that differs from the manifest is named as that first.
	stampErr error
}

const maxDirFiles = 100_000

var reAnchor = regexp.MustCompile(`^([1-9][0-9]{0,15}):(sha256:[0-9a-f]{64})$`)

func runVerify(ctx context.Context, e *Env, args []string) error {
	paths, err := parseVerifyArgs(args)
	if err != nil {
		return err
	}
	o := verifyFlagValues
	if len(paths) == 0 {
		return usageErr(errors.New("deedglass verify needs a file, a directory, or - for stdin"),
			"For example: deedglass verify deeds.jsonl --keys https://api.deedglass.com/.well-known/deedglass/keys.json")
	}
	var anchor *deed.Anchor
	if o.anchor != "" {
		m := reAnchor.FindStringSubmatch(o.anchor)
		if m == nil {
			return usageErr(fmt.Errorf("--anchor must be <seq>:sha256:<64 hex> (got %q)", clean(o.anchor)),
				"Use the seq and body_hash of the deed just before the first one you are checking.")
		}
		n, _ := strconv.ParseInt(m[1], 10, 64)
		anchor = &deed.Anchor{Seq: n, BodyHash: m[2]}
	}
	for _, p := range o.pins {
		if !rePin.MatchString(p) {
			return usageErr(fmt.Errorf("--pin must be a thumbprint, sha256:<64 hex> (got %q)", clean(p)),
				"Thumbprints are listed in keys.json and by deedglass keys list.")
		}
	}
	if o.maxBytes < 1 {
		return usageErr(fmt.Errorf("--max-bytes must be positive (got %d)", o.maxBytes), "For example --max-bytes 1073741824.")
	}
	lim := archive.DefaultLimits()
	lim.Deeds = o.maxBytes

	stamps, err := loadStampArgs(o)
	if err != nil {
		return verifyErr(e, o, err)
	}

	// Keys the operator chose.
	var chosen []keySource
	for _, k := range o.keys {
		s, err := loadKeyArg(ctx, k)
		if err != nil {
			return verifyErr(e, o, err)
		}
		chosen = append(chosen, s)
	}

	// Detect every input; open bundles now so their keys are known before the first deed.
	stdinUsed := false
	inputs := make([]*vInput, 0, len(paths))
	defer func() {
		for _, in := range inputs {
			if in.closer != nil {
				in.closer.Close()
			}
		}
	}()
	var bundleKeys []keySource
	issuer := ""
	for _, p := range paths {
		if p == "-" {
			if stdinUsed {
				return usageErr(errors.New("stdin (-) can be read only once"), "Pass - once.")
			}
			stdinUsed = true
		}
		in, err := detect(e, p, lim)
		if err != nil {
			return verifyErr(e, o, err)
		}
		inputs = append(inputs, in)
		if in.kind == inBundle && in.err == nil {
			ks, err := parseKeys(in.bundle.Keys(), clean(p)+" (keys.json)")
			if err != nil {
				return verifyErr(e, o, err)
			}
			bundleKeys = append(bundleKeys, keySource{ks, "bundle"})
			if issuer == "" {
				issuer = ks.Issuer
			}
			if ts := in.bundle.Timestamps(); ts != nil {
				if err := stamps.addBundle(ts, p); err != nil {
					in.stampErr = err
				}
			}
		}
	}
	stamps.prepare()

	if o.file != "" && (len(inputs) != 1 || inputs[0].kind != inJSON) {
		return usageErr(errors.New("--file checks one deed: pass the .deed.json that deedglass deeds seal-file wrote"),
			"For example: deedglass verify report.pdf.deed.json --file report.pdf --keys keys.json")
	}

	// Which keys: the operator's (pinned), else the bundles' own (not pinned), else none.
	var (
		ks      *deed.KeySet
		sources map[string]string
		pinned  bool
	)
	switch {
	case len(chosen) > 0:
		if ks, sources, err = mergeKeys(chosen); err != nil {
			return verifyErr(e, o, err)
		}
		pinned = true
	case len(bundleKeys) > 0:
		if ks, sources, err = mergeKeys(bundleKeys); err != nil {
			return verifyErr(e, o, err)
		}
	}
	if len(o.pins) > 0 && ks != nil {
		if ks, err = pinKeys(ks, o.pins); err != nil {
			return verifyErr(e, o, err)
		}
		pinned = true
	}

	opt := deed.StreamOptions{Keys: ks, KeysPinned: pinned, Org: e.Globals.Org, Anchor: anchor, Strict: o.strict, All: o.all,
		KeySource: func(id string) string { return sources[id] }}
	prog := newProgress(e, o)
	opt.OnProgress = prog.update
	if stamps.active() {
		opt.OnIntact = stamps.onIntact
	}
	v := deed.NewVerifier(opt)

	for _, in := range inputs {
		if v.Stopped() {
			break
		}
		if err := verifyInput(ctx, v, in, ks); err != nil {
			prog.clear()
			return verifyErr(e, o, err)
		}
	}
	prog.clear()
	var heads []deed.TimestampedHead
	if stamps.active() && !v.Stopped() {
		heads = stamps.check(v, ks)
	}
	rep := v.Report()
	rep.Timestamps = heads
	bi := buildInfo()
	rep.Verifier = "deedglass " + bi.Version + " (" + bi.Commit + ")"
	if ks != nil && !pinned && rep.Intact > 0 {
		pinAt := "<issuer>/.well-known/deedglass/keys.json"
		if issuer != "" {
			pinAt = strings.TrimSuffix(issuer, "/") + keysPath
		}
		// The trust level comes first: it changes what the ✓ means.
		msg, next := deed.WarningMessage(deed.WarnKeysNotPinned, map[string]string{"keys_url": pinAt})
		rep.Warnings = append([]deed.Warning{{Code: deed.WarnKeysNotPinned, Message: msg, Next: next}}, rep.Warnings...)
	}
	if rep.Result == deed.ResultValid && rep.Deeds == 0 {
		rep.Result = deed.ResultCannotDetermine
	}
	if o.strict && rep.Result == deed.ResultValid && !pinned {
		rep.Result = deed.ResultCannotDetermine // --strict: integrity without authenticity is not enough
	}

	code := rep.ExitCode()
	var fc *fileCheck
	if o.file != "" && code == 0 {
		if fc, err = checkSealedFile(bytes.TrimRight(inputs[0].data, "\r\n"), o.file); err != nil {
			return verifyErr(e, o, err)
		}
		if !fc.Matches {
			code = ExitFailure
		}
	}
	switch {
	case o.q:
	case e.Globals.JSON:
		enc := json.NewEncoder(e.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		var v any = rep
		if o.file != "" {
			v = struct {
				*deed.Report
				File *fileCheck `json:"file"`
			}{rep, fc}
		}
		if err := enc.Encode(v); err != nil {
			return err
		}
	default:
		printVerify(e, rep, o, len(inputs) > 1)
		if fc != nil {
			printFileCheck(e, fc)
		}
	}
	if code == 0 {
		return nil
	}
	if fc != nil && !fc.Matches {
		return &ExitError{Code: ExitFailure, ErrCode: "file_mismatch", Err: errors.New("The file doesn't match the deed"),
			Next: "Check that you have the file that was sealed. One changed byte gives a different sha256.", printed: true}
	}
	return &ExitError{Code: code, ErrCode: rep.Result, Err: errors.New(rep.Result), printed: true}
}

// detect classifies one argument by its bytes, not its name (S05 §3.2).
func detect(e *Env, p string, lim archive.Limits) (*vInput, error) {
	in := &vInput{arg: p}
	var r io.Reader
	if p == "-" {
		r = e.stdin()
	} else {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, inputErr("input_unreadable", fmt.Errorf("Can't read %s: %s", clean(p), reason(err)), "Check the path.")
		}
		if fi.IsDir() {
			in.kind = inDir
			return in, nil
		}
		if !fi.Mode().IsRegular() {
			return nil, inputErr("input_unreadable", fmt.Errorf("Can't read %s: not a regular file", clean(p)), "Pass a file, a directory or -.")
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, inputErr("input_unreadable", fmt.Errorf("Can't read %s: %s", clean(p), reason(err)), "Check the path.")
		}
		in.closer = f
		r = f
	}
	br := bufio.NewReaderSize(r, 2*deed.MaxInputBytes+2)
	head, _ := br.Peek(2)
	switch {
	case len(head) == 2 && head[0] == 0x1f && head[1] == 0x8b:
		in.kind = inBundle
		in.bundle, in.err = archive.Open(br, lim)
		return in, nil
	case strings.HasSuffix(p, ".cose") && len(head) > 0 && head[0] == 0xd2:
		in.kind = inCOSE
		b, err := io.ReadAll(io.LimitReader(br, deed.MaxInputBytes+1))
		if err != nil {
			return nil, inputErr("input_unreadable", fmt.Errorf("Can't read %s: %s", clean(p), reason(err)), "Check the path.")
		}
		in.data = b
		return in, nil
	}
	in.kind, in.reader = inJSONL, br
	if strings.HasSuffix(p, ".jsonl") {
		return in, nil
	}
	// .json, stdin or anything else: one deed, unless it holds several lines of compact deeds.
	peek, err := br.Peek(2*deed.MaxInputBytes + 2)
	if err == nil {
		return in, nil // too large for one deed: a stream
	}
	if !manyLines(peek) {
		in.kind, in.data, in.reader = inJSON, append([]byte(nil), peek...), nil
	}
	return in, nil
}

// manyLines reports whether b looks like JSONL: a first line that is a whole object, then more.
func manyLines(b []byte) bool {
	b = bytes.TrimSpace(b)
	i := bytes.IndexByte(b, '\n')
	if i < 0 {
		return false
	}
	first := bytes.TrimSpace(b[:i])
	rest := bytes.TrimSpace(b[i:])
	return len(first) > 1 && first[0] == '{' && first[len(first)-1] == '}' && len(rest) > 0 && rest[0] == '{'
}

// verifyInput feeds one input to the verifier. Errors are input problems (exit 2); deed and bundle
// problems go into the report.
func verifyInput(ctx context.Context, v *deed.Verifier, in *vInput, ks *deed.KeySet) error {
	name := in.arg
	readErr := func(err error) error {
		return inputErr("input_unreadable", fmt.Errorf("Can't read %s: %s", clean(name), reason(err)), "Check the file, then run verify again.")
	}
	switch in.kind {
	case inJSON:
		v.Loose(bytes.TrimRight(in.data, "\r\n"), name)
	case inCOSE:
		v.COSE(in.data, name)
	case inJSONL:
		if err := v.Stream(ctx, in.reader, name); err != nil {
			return readErr(err)
		}
	case inDir:
		recs, err := dirRecords(name)
		if err != nil {
			return err
		}
		return v.Records(ctx, recs)
	case inBundle:
		if in.err != nil {
			f := failureOf(in.err)
			f.Input = &name
			v.Fail(f)
			return nil
		}
		if err := v.Stream(ctx, in.bundle.Deeds(), name); err != nil {
			var ve *deed.VerifyError
			if errors.As(err, &ve) {
				f := failureOf(err)
				f.Input = &name
				v.Fail(f)
				return nil
			}
			return readErr(err)
		}
		if v.Stopped() {
			return nil // a deed failed: that is the answer
		}
		checkBundle(v, in, ks)
		if in.stampErr != nil && !v.Stopped() {
			f := failureOf(in.stampErr)
			f.Input = &name
			v.Fail(f)
		}
	}
	return nil
}

// checkBundle reads the manifest and its seal after the deeds, and compares them.
func checkBundle(v *deed.Verifier, in *vInput, ks *deed.KeySet) {
	name := in.arg
	st := &deed.BundleStatus{Manifest: "mismatch", ManifestSeal: "missing", Input: name}
	v.SetBundle(st)
	fail := func(err error) {
		f := failureOf(err)
		f.Input = &name
		v.Fail(f)
	}
	m, seal, err := in.bundle.Finish()
	if err != nil {
		fail(err)
		return
	}
	st.Files = 4
	if in.bundle.Timestamps() != nil {
		st.Files = 5 // TY-03: timestamps.json
	}
	got := v.Input()
	obs := archive.Observed{Count: got.Count, FirstSeq: got.FirstSeq, LastSeq: got.LastSeq, From: got.From, To: got.To,
		FirstPrevHash: got.FirstPrevHash, Kinds: got.Kinds,
		Head: archive.ChainHead{Seq: got.HeadSeq, DeedID: got.HeadDeedID, BodyHash: got.HeadBodyHash}}
	if err := m.Check(obs); err != nil {
		fail(err)
		return
	}
	st.Manifest = "ok"
	if seal == nil {
		msg, next := deed.WarningMessage(deed.WarnManifestUnsealed, nil)
		v.Warn(deed.WarnManifestUnsealed, msg, next)
		return
	}
	st.Files++
	st.SealedBy = &seal.SealKeyID
	created, _ := deed.ParseTime(m.CreatedAt)
	if ks == nil {
		ks = &deed.KeySet{}
	}
	if err := deed.VerifyBundleSeal(seal, in.bundle.ManifestBytes(), ks, created); err != nil {
		st.ManifestSeal = "invalid"
		fail(err)
		return
	}
	st.ManifestSeal = "ok"
}

func failureOf(err error) deed.Failure {
	var ve *deed.VerifyError
	if errors.As(err, &ve) {
		return deed.FailureFrom(ve)
	}
	return deed.FailureFrom(&deed.VerifyError{Code: deed.CodeBundleInvalid, Detail: "This bundle could not be read (" + lastPart(err) + ")."})
}

// dirRecords reads every *.json and *.jsonl in dir (no recursion, no symlinks) and sorts the deeds
// by (org, seq), unreadable ones last.
func dirRecords(dir string) ([]deed.Record, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, inputErr("input_unreadable", fmt.Errorf("Can't read %s: %s", clean(dir), reason(err)), "Check the path.")
	}
	type rec struct {
		r   deed.Record
		org string
		seq int64
		ok  bool
	}
	var recs []rec
	files := 0
	for _, de := range ents {
		n := de.Name()
		if !de.Type().IsRegular() || !(strings.HasSuffix(n, ".json") || strings.HasSuffix(n, ".jsonl")) {
			continue
		}
		if files++; files > maxDirFiles {
			return nil, inputErr("input_too_large", fmt.Errorf("%s holds more than 100,000 deed files", clean(dir)), "Export them as one deeds.jsonl or a bundle.")
		}
		p := filepath.Join(dir, n)
		max := int64(deed.MaxInputBytes)
		if strings.HasSuffix(n, ".jsonl") {
			max = 1 << 30
		}
		b, err := readCapped(p, max)
		if err != nil {
			return nil, inputErr("input_unreadable", fmt.Errorf("Can't read %s: %s", clean(p), err.Error()), "Check the file.")
		}
		var parts [][]byte
		if strings.HasSuffix(n, ".jsonl") || manyLines(b) {
			parts = bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
		} else {
			parts = [][]byte{bytes.TrimRight(b, "\r\n")}
		}
		for i, part := range parts {
			var f struct {
				Body struct {
					Org string `json:"org"`
					Seq int64  `json:"seq"`
				} `json:"body"`
			}
			ok := json.Unmarshal(part, &f) == nil && f.Body.Org != "" && f.Body.Seq > 0
			recs = append(recs, rec{deed.Record{Data: part, Input: p, Line: int64(i + 1)}, f.Body.Org, f.Body.Seq, ok})
		}
	}
	sort.SliceStable(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if a.ok != b.ok {
			return a.ok
		}
		if a.org != b.org {
			return a.org < b.org
		}
		return a.seq < b.seq
	})
	out := make([]deed.Record, len(recs))
	for i, r := range recs {
		out[i] = r.r
	}
	return out, nil
}

// verifyErr prints an input or key error in verify's layout (○ for "could not check", ✕ otherwise).
func verifyErr(e *Env, o verifyOpts, err error) error {
	var x *ExitError
	if !errors.As(err, &x) || e.Globals.JSON {
		return err
	}
	if o.q {
		x.printed = true
		return x
	}
	s := e.Style
	sym := s.Red(ui.Fail)
	if x.Plain {
		sym = s.Amber(ui.Unknown)
	}
	fmt.Fprintln(e.Stderr, sym+" "+sentence(x.Err.Error()))
	if x.Next != "" {
		fmt.Fprintln(e.Stderr, vrow("next", x.Next))
	}
	x.printed = true
	return x
}

// --- human output ---

func vrow(label, value string) string { return "  " + ui.Pad(label, 9) + "  " + value }

func printVerify(e *Env, rep *deed.Report, o verifyOpts, multi bool) {
	s := e.Style
	w := e.Stdout
	line := func(t string) { fmt.Fprintln(w, t) }
	if len(rep.Failures) > 0 {
		printFailures(e, rep, o, multi)
		return
	}
	if rep.Deeds == 0 {
		line(s.UnknownLine("No deeds were found in the input."))
		line(vrow("next", "Check the file: deeds.jsonl holds one deed per line."))
		return
	}
	parts := []string{count(rep.Intact, "deed", "deeds")}
	chained := rep.Chain.Status == deed.ChainIntact
	if chained {
		parts = append(parts, "chain intact")
		if rep.Chain.Anchor.Kind == "deed" {
			parts = append(parts, "anchored at seq "+deed.FormatCount(*rep.Chain.Anchor.Seq))
		}
	} else {
		parts = append(parts, "seals intact", "chain not checked")
	}
	parts = append(parts, count(int64(len(rep.SealKeys)), "seal key", "seal keys"))
	if chained && rep.From != nil {
		parts = append(parts, (*rep.From)[:10]+" → "+(*rep.To)[:10])
	}
	headline := strings.Join(parts, " "+ui.Sep+" ")
	if o.strict && !rep.KeysPinned {
		line(s.UnknownLine(headline + " " + ui.Sep + " keys not pinned (--strict)"))
	} else {
		line(s.OKLine(headline))
	}
	if e.Globals.Verbose > 0 {
		printVerbose(e, rep)
	}
	if b := rep.Bundle; b != nil && b.Manifest == "ok" {
		if b.SealedBy != nil {
			line(s.OKLine("Bundle manifest matches " + ui.Sep + " sealed by " + shortID(*b.SealedBy)))
		} else {
			line(s.OKLine("Bundle manifest matches"))
		}
	}
	if len(rep.Timestamps) > 0 {
		printTimestamps(e, rep.Timestamps)
	}
	for _, wn := range rep.Warnings {
		line(s.UnknownLine(clean(wn.Message)))
		if wn.Next != "" {
			line(vrow("next", clean(wn.Next)))
		}
	}
}

func printVerbose(e *Env, rep *deed.Report) {
	w := e.Stdout
	row := func(l, v string) { fmt.Fprintln(w, vrow(l, v)) }
	if rep.Org != nil {
		row("org", clean(*rep.Org))
	}
	from := "chain not checked"
	switch {
	case rep.Chain.Status != deed.ChainIntact:
	case rep.Chain.Anchor.Kind == "deed":
		from = "after seq " + deed.FormatCount(*rep.Chain.Anchor.Seq)
	default:
		from = "from genesis"
	}
	if rep.FirstSeq != nil {
		row("seq", deed.FormatCount(*rep.FirstSeq)+" → "+deed.FormatCount(*rep.LastSeq)+" ("+from+")")
	}
	var kinds []string
	for _, k := range []string{"action", "admin", "amendment"} {
		if n := rep.Kinds[k]; n > 0 {
			kinds = append(kinds, deed.FormatCount(n)+" "+k)
		}
	}
	row("kinds", strings.Join(kinds, " "+ui.Sep+" "))
	var rec []string
	for _, k := range []string{"VERIFIED", "MISMATCH", "UNVERIFIABLE", "none"} {
		if n := rep.Recorded[k]; n > 0 {
			label := k
			if k == "none" {
				label = "no witness"
			}
			rec = append(rec, deed.FormatCount(n)+" "+label)
		}
	}
	row("recorded", strings.Join(rec, " "+ui.Sep+" "))
	var keys, srcs []string
	seen := map[string]bool{}
	for _, k := range rep.SealKeys {
		keys = append(keys, clean(k.ID)+" ("+deed.FormatCount(k.FirstSeq)+" → "+deed.FormatCount(k.LastSeq)+")")
		if !seen[k.Source] {
			seen[k.Source] = true
			srcs = append(srcs, clean(k.Source))
		}
	}
	row("seal keys", strings.Join(keys, " "+ui.Sep+" "))
	trust := " (not pinned)"
	if rep.KeysPinned {
		trust = " (pinned)"
	}
	row("keys from", strings.Join(srcs, " "+ui.Sep+" ")+trust)
	row("took", fmt.Sprintf("%.2f s", float64(rep.DurationMS)/1000))
}

func printFailures(e *Env, rep *deed.Report, o verifyOpts, multi bool) {
	s := e.Style
	w := e.Stdout
	line := func(t string) { fmt.Fprintln(w, t) }
	if o.all {
		n := len(rep.Failures)
		head := count(int64(n), "break", "breaks") + " " + ui.Sep + " " + deed.FormatCount(rep.Intact) + " of " + deed.FormatCount(rep.Deeds) + " deeds intact"
		if n >= deed.MaxFailures {
			head += " " + ui.Sep + " showing the first 100"
		}
		line(s.FailLine(head))
	}
	for i, f := range rep.Failures {
		if !o.all && i > 0 {
			break
		}
		sym := s.Red(ui.Fail)
		if deed.CannotDetermine(f.Code) {
			sym = s.Amber(ui.Unknown)
		}
		line(sym + " " + clean(f.Message))
		cannot := deed.CannotDetermine(f.Code)
		if f.Deed != nil && f.Code != string(deed.CodeChainSeqGap) && !cannot {
			where := ""
			if f.Line != nil {
				where = " (line " + deed.FormatCount(*f.Line)
				if multi && f.Input != nil {
					where += " of " + clean(*f.Input)
				}
				where += ")"
			}
			line(vrow("deed", clean(*f.Deed)+where))
		}
		if f.Expected != nil && f.Computed != nil {
			line(vrow("sealed", "body_hash "+shortHash(*f.Expected)))
			line(vrow("computed", shortHash(*f.Computed)))
		}
		chained := rep.Chain.Status != deed.ChainNotChecked
		if !o.all && chained && breakCodes[f.Code] && rep.Intact > 0 {
			line(vrow("before it", count(rep.Intact, "deed", "deeds")+" intact "+ui.Sep+" stopped at the first break"))
		}
		next := clean(f.Next)
		if !o.all && chained && breakCodes[f.Code] {
			next += " Add --all to list every break."
		}
		line(vrow("next", next))
	}
}

// breakCodes are the failures after which later deeds may still be checked (verify --all).
var breakCodes = map[string]bool{
	string(deed.CodeBodyHashMismatch): true, string(deed.CodeSealInvalid): true, string(deed.CodeSealKeyMismatch): true,
	string(deed.CodeIDMismatch): true, string(deed.CodeSchemaInvalid): true, string(deed.CodeSealKeyNotValidAt): true,
	string(deed.CodeSealKeyRevoked): true, string(deed.CodeChainPrevHashMismatch): true, string(deed.CodeChainTimeRegression): true,
	string(deed.CodeChainSeqOutOfOrder): true, string(deed.CodeChainOrgMismatch): true, string(deed.CodeChainGenesisMismatch): true,
}

func count(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return deed.FormatCount(n) + " " + many
}

// shortID shows sk_01j9zr8k… (prefix and 8 characters).
func shortID(id string) string {
	i := strings.IndexByte(id, '_')
	if i < 0 || len(id) <= i+9 {
		return clean(id)
	}
	return clean(id[:i+9]) + "…"
}

// clean makes an untrusted string safe for a terminal: C0 and C1 controls, DEL, ESC and the bidi
// overrides and isolates (U+202A–U+202E, U+2066–U+2069) are shown as \uXXXX escapes, and invalid
// UTF-8 as \xXX. Nothing a deed holds can move the cursor, recolour a line or reorder text.
func clean(s string) string {
	ok := true
	for _, r := range s {
		if unsafeRune(r) {
			ok = false
			break
		}
	}
	if ok && utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n <= 1:
			fmt.Fprintf(&b, `\x%02x`, s[i])
		case unsafeRune(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

func unsafeRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// --- progress (TTY only, at most 10 times a second) ---

type progress struct {
	e     *Env
	on    bool
	last  time.Time
	drawn bool
}

func newProgress(e *Env, o verifyOpts) *progress {
	return &progress{e: e, on: e.IsTTY && !e.Globals.JSON && !o.q}
}

// progressMin is the deed count before the progress line appears (small checks finish first).
var progressMin int64 = 5000

func (p *progress) update(n int64) {
	if !p.on || time.Since(p.last) < 100*time.Millisecond || n < progressMin {
		return
	}
	p.last = time.Now()
	fmt.Fprintf(p.e.Stdout, "\r%s Checking %s deeds…\x1b[K", p.e.Style.Violet(ui.Pending), deed.FormatCount(n))
	p.drawn = true
}

func (p *progress) clear() {
	if p.drawn {
		fmt.Fprint(p.e.Stdout, "\r\x1b[K")
		p.drawn = false
	}
}
