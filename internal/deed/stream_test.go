package deed_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

func chainBytes(c *deedtest.Chain) []byte { return jsonl(c.Lines...) }

func streamOpts() deed.StreamOptions {
	return deed.StreamOptions{Keys: deedtest.KeySet(), KeysPinned: true, KeySource: func(string) string { return "test-keys.json" }}
}

func codes(r *deed.Report) []string {
	var out []string
	for _, f := range r.Failures {
		out = append(out, f.Code)
	}
	return out
}

func TestStreamValid(t *testing.T) {
	c := deedtest.NewChain(300, deedtest.ChainOpts{})
	var progress atomic.Int64
	opt := streamOpts()
	opt.Workers = 4
	opt.OnProgress = func(n int64) { progress.Store(n) }
	rep, err := deed.VerifyStream(context.Background(), bytes.NewReader(chainBytes(c)), opt)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Result != deed.ResultValid || rep.Deeds != 300 || rep.Intact != 300 || rep.Chain.Status != deed.ChainIntact || rep.ExitCode() != 0 {
		t.Fatalf("report: %+v", rep)
	}
	if *rep.FirstSeq != 1 || *rep.LastSeq != 300 || *rep.Org != c.Org || rep.Chain.Anchor.Kind != "genesis" || *rep.Chain.Anchor.BodyHash != deed.Genesis(c.Org) {
		t.Fatalf("range: %+v", rep)
	}
	if len(rep.SealKeys) != 2 || rep.SealKeys[0].FirstSeq != 1 || rep.SealKeys[0].LastSeq != 10 || rep.SealKeys[1].Deeds != 290 || rep.SealKeys[0].Source != "test-keys.json" {
		t.Fatalf("keys: %+v", rep.SealKeys)
	}
	if progress.Load() != 300 {
		t.Fatalf("progress %d", progress.Load())
	}
	if rep.Kinds["admin"] == 0 || rep.Kinds["action"] == 0 || rep.Recorded["VERIFIED"] == 0 || rep.Recorded["none"] == 0 {
		t.Fatalf("tallies: %v %v", rep.Kinds, rep.Recorded)
	}
	if len(rep.Warnings) != 1 || rep.Warnings[0].Code != deed.WarnTestKeys {
		t.Fatalf("warnings: %+v", rep.Warnings)
	}
	js, _ := json.Marshal(rep)
	for _, k := range []string{`"object":"verification_report"`, `"failures":[]`, `"bundle":null`, `"verdicts_recorded":`} {
		if !strings.Contains(string(js), k) {
			t.Errorf("JSON lacks %s", k)
		}
	}
}

// tampered returns the chain with deed seq's body changed after sealing: one hex digit of its
// prev_hash is replaced, so the body no longer matches body_hash.
func tampered(t *testing.T, c *deedtest.Chain, seqs ...int) [][]byte {
	t.Helper()
	lines := append([][]byte(nil), c.Lines...)
	for _, s := range seqs {
		l := lines[s-1]
		i := bytes.Index(l, []byte(`"prev_hash":"sha256:`))
		if i < 0 {
			t.Fatalf("deed %d has no prev_hash", s)
		}
		out := append([]byte(nil), l...)
		j := i + len(`"prev_hash":"sha256:`)
		if out[j] == '0' {
			out[j] = '1'
		} else {
			out[j] = '0'
		}
		lines[s-1] = out
	}
	return lines
}

func TestStreamFirstFailure(t *testing.T) {
	c := deedtest.NewChain(1000, deedtest.ChainOpts{})
	lines := tampered(t, c, 812)
	rep, err := deed.VerifyStream(context.Background(), bytes.NewReader(jsonl(lines...)), streamOpts())
	if err != nil {
		t.Fatal(err)
	}
	f := rep.FirstFailure()
	if rep.Result != deed.ResultInvalid || rep.ExitCode() != 1 || len(rep.Failures) != 1 || f.Code != "body_hash_mismatch" || *f.Seq != 812 || *f.Line != 812 {
		t.Fatalf("report: %+v", rep.Failures)
	}
	if f.Message != "Deed 812 was changed after it was sealed." || f.Next != "Get a fresh export from the issuer and compare deed 812." {
		t.Fatalf("copy: %q / %q", f.Message, f.Next)
	}
	if f.Expected == nil || f.Computed == nil || *f.Expected == *f.Computed || *f.Expected != c.Deeds[811].BodyHash {
		t.Fatalf("hashes: %v %v", f.Expected, f.Computed)
	}
	if rep.Intact != 811 || rep.Deeds != 812 || rep.Chain.Status != deed.ChainBroken {
		t.Fatalf("intact %d deeds %d chain %s", rep.Intact, rep.Deeds, rep.Chain.Status)
	}
}

func TestStreamAllListsEveryBreakInOrder(t *testing.T) {
	c := deedtest.NewChain(200, deedtest.ChainOpts{})
	lines := tampered(t, c, 20, 150)
	lines = append(lines[:99], lines[100:]...) // deed 100 removed
	opt := streamOpts()
	opt.All = true
	opt.Workers = 8
	rep, err := deed.VerifyStream(context.Background(), bytes.NewReader(jsonl(lines...)), opt)
	if err != nil {
		t.Fatal(err)
	}
	got := codes(rep)
	want := []string{"body_hash_mismatch", "chain_seq_gap", "body_hash_mismatch"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("failures %v, want %v", got, want)
	}
	if *rep.Failures[0].Seq != 20 || *rep.Failures[1].Seq != 101 || *rep.Failures[2].Seq != 150 {
		t.Fatalf("seqs: %+v", rep.Failures)
	}
	if rep.Deeds != 199 || rep.Intact != 196 {
		t.Fatalf("deeds %d intact %d", rep.Deeds, rep.Intact)
	}
}

func TestStreamAllIsBounded(t *testing.T) {
	c := deedtest.NewChain(300, deedtest.ChainOpts{})
	var odd [][]byte
	for i, l := range c.Lines {
		if i%2 == 0 {
			odd = append(odd, l)
		}
	}
	opt := streamOpts()
	opt.All = true
	rep, _ := deed.VerifyStream(context.Background(), bytes.NewReader(jsonl(odd...)), opt)
	if len(rep.Failures) != deed.MaxFailures || rep.Result != deed.ResultInvalid {
		t.Fatalf("%d failures", len(rep.Failures))
	}
}

func TestStreamCancelLeaksNothing(t *testing.T) {
	c := deedtest.NewChain(3000, deedtest.ChainOpts{})
	data := chainBytes(c)
	runtime.GC()
	before := runtime.NumGoroutine()
	for range 5 {
		ctx, cancel := context.WithCancel(context.Background())
		opt := streamOpts()
		var n atomic.Int64
		opt.OnProgress = func(x int64) {
			if n.Add(1) == 2 {
				cancel()
			}
		}
		_, err := deed.VerifyStream(ctx, bytes.NewReader(data), opt)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
		cancel()
		// A first failure also stops the pipeline early.
		lines := tampered(t, c, 70)
		if rep, err := deed.VerifyStream(context.Background(), bytes.NewReader(jsonl(lines...)), streamOpts()); err != nil || rep.Deeds != 70 {
			t.Fatalf("stop: %v %+v", err, rep)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("goroutines: %d before, %d after", before, after)
	}
}

func TestStreamKeysMissing(t *testing.T) {
	c := deedtest.NewChain(20, deedtest.ChainOpts{})
	opt := streamOpts()
	opt.Keys, opt.KeysPinned, opt.All = nil, false, true
	rep, err := deed.VerifyStream(context.Background(), bytes.NewReader(chainBytes(c)), opt)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Result != deed.ResultCannotDetermine || rep.ExitCode() != 2 || len(rep.Failures) != 1 || rep.Failures[0].Code != "keys_missing" {
		t.Fatalf("%+v", rep)
	}
	if rep.Failures[0].Message != "Cannot check deed 1: no seal keys were given." || rep.Chain.Status != deed.ChainNotChecked {
		t.Fatalf("%q %s", rep.Failures[0].Message, rep.Chain.Status)
	}
	// A changed deed is changed with or without keys.
	opt.All = false
	rep, _ = deed.VerifyStream(context.Background(), bytes.NewReader(jsonl(tampered(t, c, 1)...)), opt)
	if rep.ExitCode() != 1 || rep.Failures[0].Code != "body_hash_mismatch" {
		t.Fatalf("%+v", rep.Failures)
	}
}

func TestStreamLines(t *testing.T) {
	c := deedtest.NewChain(5, deedtest.ChainOpts{})
	long := bytes.Repeat([]byte("x"), deed.MaxInputBytes+10)
	cases := []struct {
		name string
		data []byte
		code string
		line int64
	}{
		{"blank line", jsonl(c.Lines[0], nil, c.Lines[1]), "malformed", 2},
		{"long line", jsonl(c.Lines[0], long, c.Lines[1]), "malformed", 2},
		{"long last line", append(jsonl(c.Lines[0]), long...), "malformed", 2},
		{"no final newline", bytes.TrimSuffix(chainBytes(c), []byte("\n")), "", 0},
		{"empty input", nil, "", 0},
	}
	for _, tc := range cases {
		opt := streamOpts()
		opt.All = true
		rep, err := deed.VerifyStream(context.Background(), bytes.NewReader(tc.data), opt)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.code == "" {
			if rep.Result != deed.ResultValid {
				t.Errorf("%s: %+v", tc.name, rep.Failures)
			}
			continue
		}
		f := rep.FirstFailure()
		if f == nil || f.Code != tc.code || *f.Line != tc.line {
			t.Errorf("%s: %+v", tc.name, rep.Failures)
			continue
		}
		if len(rep.Failures) != 1 {
			t.Errorf("%s: an unreadable line must not cascade: %v", tc.name, codes(rep))
		}
	}
}

type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n > 0 {
		r.n--
		return copy(p, "{"), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func TestStreamReadError(t *testing.T) {
	if _, err := deed.VerifyStream(context.Background(), &failingReader{n: 3}, streamOpts()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err %v", err)
	}
}

func TestStreamAnchorAndOrg(t *testing.T) {
	c := deedtest.NewChain(40, deedtest.ChainOpts{})
	tail := jsonl(c.Lines[19:]...)
	opt := streamOpts()
	opt.Anchor = &deed.Anchor{Seq: 19, BodyHash: c.Deeds[18].BodyHash}
	rep, _ := deed.VerifyStream(context.Background(), bytes.NewReader(tail), opt)
	if rep.Result != deed.ResultValid || rep.Chain.Anchor.Kind != "deed" || *rep.Chain.Anchor.Seq != 19 {
		t.Fatalf("anchored: %+v", rep.Failures)
	}
	opt.Anchor = &deed.Anchor{Seq: 19, BodyHash: c.Deeds[17].BodyHash}
	rep, _ = deed.VerifyStream(context.Background(), bytes.NewReader(tail), opt)
	if f := rep.FirstFailure(); f == nil || f.Code != "chain_prev_hash_mismatch" || *f.Seq != 20 {
		t.Fatalf("wrong anchor: %+v", rep.Failures)
	}
	rep, _ = deed.VerifyStream(context.Background(), bytes.NewReader(tail), streamOpts())
	if f := rep.FirstFailure(); f == nil || f.Code != "chain_seq_gap" || !strings.HasPrefix(f.Message, "The deeds start at deed 20") {
		t.Fatalf("no anchor: %+v", rep.Failures)
	}
	opt = streamOpts()
	opt.Org = deedtest.Org(5)
	rep, _ = deed.VerifyStream(context.Background(), bytes.NewReader(chainBytes(c)), opt)
	if f := rep.FirstFailure(); f == nil || f.Code != "chain_org_mismatch" {
		t.Fatalf("org: %+v", rep.Failures)
	}
}

func TestVerifierLooseAndDirectory(t *testing.T) {
	c := deedtest.NewChain(6, deedtest.ChainOpts{})
	v := deed.NewVerifier(streamOpts())
	for _, i := range []int{4, 0, 2} {
		v.Loose(c.Lines[i], "deed.json")
	}
	rep := v.Report()
	if rep.Result != deed.ResultValid || rep.Deeds != 3 || rep.Chain.Status != deed.ChainNotChecked || rep.Chain.Anchor.Kind != "none" {
		t.Fatalf("loose: %+v", rep)
	}
	// Loose deed 1 with the wrong genesis.
	other := deedtest.NewChain(1, deedtest.ChainOpts{Org: deedtest.Org(3)})
	wrong := deedtest.Canonical(deedtest.Reseal(other.Deeds[0], deedtest.TestKey("a"), func(b *deed.Body) { b.PrevHash = deed.Genesis(c.Org) }))
	v = deed.NewVerifier(streamOpts())
	v.Loose(wrong, "x.json")
	if f := v.Report().FirstFailure(); f == nil || f.Code != "chain_genesis_mismatch" {
		t.Fatal("loose genesis")
	}
	opt := streamOpts()
	opt.Org = deedtest.Org(7)
	v = deed.NewVerifier(opt)
	v.Loose(c.Lines[0], "x.json")
	if f := v.Report().FirstFailure(); f == nil || f.Code != "chain_org_mismatch" {
		t.Fatal("loose org")
	}
	v = deed.NewVerifier(streamOpts())
	v.Loose([]byte("{"), "x.json")
	v.Loose(c.Lines[0], "y.json") // stopped: not read
	if r := v.Report(); r.Deeds != 1 || r.FirstFailure().Code != "malformed" || !v.Stopped() {
		t.Fatalf("loose malformed: %+v", r)
	}

	// A directory: two orgs, each its own chain.
	recs := []deed.Record{}
	for _, l := range c.Lines {
		recs = append(recs, deed.Record{Data: l, Input: "a.json", Line: 1})
	}
	o3 := deedtest.NewChain(3, deedtest.ChainOpts{Org: deedtest.Org(3)})
	for _, l := range o3.Lines {
		recs = append(recs, deed.Record{Data: l, Input: "b.json", Line: 1})
	}
	v = deed.NewVerifier(streamOpts())
	if err := v.Records(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
	if r := v.Report(); r.Result != deed.ResultValid || r.Deeds != 9 || r.Org != nil || r.Chain.Status != deed.ChainIntact {
		t.Fatalf("directory: %+v", r)
	}
}

func TestVerifierMultipleInputsAreOneChain(t *testing.T) {
	c := deedtest.NewChain(30, deedtest.ChainOpts{})
	v := deed.NewVerifier(streamOpts())
	_ = v.Stream(context.Background(), bytes.NewReader(jsonl(c.Lines[:15]...)), "part1")
	_ = v.Stream(context.Background(), bytes.NewReader(jsonl(c.Lines[15:]...)), "part2")
	if r := v.Report(); r.Result != deed.ResultValid || r.Deeds != 30 {
		t.Fatalf("%+v", r.Failures)
	}
	v = deed.NewVerifier(streamOpts())
	_ = v.Stream(context.Background(), bytes.NewReader(jsonl(c.Lines[:15]...)), "part1")
	_ = v.Stream(context.Background(), bytes.NewReader(jsonl(c.Lines[16:]...)), "part2")
	r := v.Report()
	if f := r.FirstFailure(); f == nil || f.Code != "chain_seq_gap" || *f.Seq != 17 || *f.Input != "part2" || *f.Line != 1 {
		t.Fatalf("%+v", r.Failures)
	}
	v.Fail(deed.Failure{Code: "bundle_invalid"})
	v.SetBundle(&deed.BundleStatus{Manifest: "mismatch"})
	v.Warn(deed.WarnManifestUnsealed, "m", "n")
	v.Warn(deed.WarnManifestUnsealed, "m", "n")
	if r := v.Report(); len(r.Warnings) != 2 || r.Bundle == nil {
		t.Fatalf("%+v", r.Warnings)
	}
}

func TestFormatCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 1204: "1,204", 100000: "100,000", 1234567: "1,234,567", -1204: "-1,204"} {
		if got := deed.FormatCount(n); got != want {
			t.Errorf("%d: %s", n, got)
		}
	}
}

func TestVerifierInputStatsAndCOSE(t *testing.T) {
	c := deedtest.NewChain(12, deedtest.ChainOpts{})
	v := deed.NewVerifier(streamOpts())
	_ = v.Stream(context.Background(), bytes.NewReader(jsonl(c.Lines[:5]...)), "a")
	_ = v.Stream(context.Background(), bytes.NewReader(jsonl(c.Lines[5:]...)), "b")
	in := v.Input()
	if in.Count != 7 || in.FirstSeq != 6 || in.LastSeq != 12 || in.FirstPrevHash != c.Deeds[4].BodyHash || in.HeadBodyHash != c.Deeds[11].BodyHash || in.HeadDeedID != c.Deeds[11].ID {
		t.Fatalf("%+v", in)
	}
	cose, err := deed.EncodeCOSE(c.Deeds[0], deedtest.TestKey("b").Signer, deedtest.Issuer, deedtest.T0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	v = deed.NewVerifier(streamOpts())
	v.COSE(cose, "x.cose")
	if r := v.Report(); r.Result != deed.ResultValid || r.Intact != 1 {
		t.Fatalf("%+v", r.Failures)
	}
	opt := streamOpts()
	opt.Keys = nil
	v = deed.NewVerifier(opt)
	v.COSE(cose, "x.cose")
	if f := v.Report().FirstFailure(); f == nil || f.Code != "keys_missing" {
		t.Fatal("cose without keys")
	}
	v = deed.NewVerifier(streamOpts())
	v.COSE([]byte{1, 2}, "x.cose")
	v.COSE(cose, "x.cose")
	if f := v.Report().FirstFailure(); f == nil || f.Code != "malformed" || v.Report().Deeds != 1 {
		t.Fatal("bad cose")
	}
}

// FuzzVerifyStream mutates lines of a chain: the verifier never panics, never reports valid for
// bytes that differ from the chain, and always ends with a consistent report.
func FuzzVerifyStream(f *testing.F) {
	c := deedtest.NewChain(6, deedtest.ChainOpts{})
	f.Add(chainBytes(c), uint16(0), byte(0))
	f.Add(chainBytes(c), uint16(500), byte(1))
	f.Add([]byte("{}\n\n{"), uint16(1), byte(0x20))
	orig := chainBytes(c)
	ks := deedtest.KeySet()
	f.Fuzz(func(t *testing.T, data []byte, pos uint16, flip byte) {
		if len(data) > 0 && flip != 0 {
			data = bytes.Clone(data)
			data[int(pos)%len(data)] ^= flip
		}
		for _, all := range []bool{false, true} {
			rep, err := deed.VerifyStream(context.Background(), bytes.NewReader(data), deed.StreamOptions{Keys: ks, All: all, Workers: 2})
			if err != nil {
				t.Fatal(err)
			}
			if rep.Result == deed.ResultValid && len(rep.Failures) != 0 {
				t.Fatal("valid with failures")
			}
			// Six intact deeds from changed bytes is possible only through JSON whitespace (the
			// lenient mode allows it); any other change must fail.
			if rep.Result == deed.ResultValid && rep.Intact == 6 && !bytes.Equal(bytes.Join(bytes.Fields(data), nil), bytes.Join(bytes.Fields(orig), nil)) {
				t.Fatalf("changed bytes verified: %q", data)
			}
			if rep.Intact > rep.Deeds || len(rep.Failures) > deed.MaxFailures {
				t.Fatalf("counts: %+v", rep)
			}
		}
	})
}
