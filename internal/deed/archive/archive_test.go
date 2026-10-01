package archive_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

var update = flag.Bool("update", false, "rewrite the bundle golden hash")

func keysJSON(t testing.TB) []byte {
	t.Helper()
	b, err := deed.StrictCanonical(deedtest.KeySet())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func header(c *deedtest.Chain, from, to int) archive.Header {
	return archive.Header{Org: c.Org, Issuer: "https://api.deedglass.example", CreatedAt: deedtest.T0.Add(time.Hour),
		Generator: archive.Generator{Name: "deedglass", Version: "0.1.0-dev", Commit: "4f2c1a9"},
		FirstSeq:  int64(from), LastSeq: int64(to),
		From: c.Deeds[from-1].Body.IssuedAt, To: c.Deeds[to-1].Body.IssuedAt}
}

// build writes deeds from..to (1-based, inclusive) of c as a bundle.
func build(t testing.TB, c *deedtest.Chain, from, to int, s deed.Signer) []byte {
	t.Helper()
	var size int64
	for _, l := range c.Lines[from-1 : to] {
		size += int64(len(l)) + 1
	}
	var out bytes.Buffer
	w, err := archive.NewWriter(&out, header(c, from, to), keysJSON(t), size, int64(to-from+1))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range c.Lines[from-1 : to] {
		if err := w.WriteDeed(l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Close(s); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// verify reads a bundle like `deedglass verify` does: deeds through the streaming verifier, then
// the manifest, its checks and its seal.
func verify(t testing.TB, b []byte) (*deed.Report, error) {
	t.Helper()
	r, err := archive.Open(bytes.NewReader(b), archive.Limits{})
	if err != nil {
		return nil, err
	}
	ks, err := deed.ParseKeySet(r.Keys())
	if err != nil {
		return nil, err
	}
	v := deed.NewVerifier(deed.StreamOptions{Keys: ks})
	if err := v.Stream(context.Background(), r.Deeds(), "deeds.jsonl"); err != nil {
		return nil, err
	}
	if v.Stopped() {
		return v.Report(), nil // a deed failed: that is the answer, before the manifest
	}
	m, seal, err := r.Finish()
	if err != nil {
		return nil, err
	}
	rep := v.Report()
	if rep.Result != deed.ResultValid {
		return rep, nil
	}
	obs := archive.Observed{Count: rep.Intact, FirstSeq: *rep.FirstSeq, LastSeq: *rep.LastSeq, From: *rep.From, To: *rep.To,
		Kinds: rep.Kinds, FirstPrevHash: m.Chain.Anchor.BodyHash, Head: m.Chain.Head}
	if err := m.Check(obs); err != nil {
		return nil, err
	}
	if seal == nil {
		return rep, errNoSeal
	}
	created, _ := deed.ParseTime(m.CreatedAt)
	return rep, deed.VerifyBundleSeal(seal, r.ManifestBytes(), ks, created)
}

var errNoSeal = errors.New("no seal")

func TestRoundTrip(t *testing.T) {
	c := deedtest.NewChain(20, deedtest.ChainOpts{})
	b := build(t, c, 1, 20, deedtest.TestKey("b").Signer)
	rep, err := verify(t, b)
	if err != nil || rep.Result != deed.ResultValid || rep.Intact != 20 {
		t.Fatalf("%v %+v", err, rep)
	}
	r, _ := archive.Open(bytes.NewReader(b), archive.DefaultLimits())
	if !strings.HasPrefix(string(r.README()), "Deedglass deed bundle\n\n20 deeds sealed by https://api.deedglass.example\n") {
		t.Fatalf("README:\n%s", r.README())
	}
	for _, l := range strings.Split(string(r.README()), "\n") {
		if len([]rune(l)) > 72 {
			t.Errorf("README line over 72 columns: %q", l)
		}
	}
	_, _ = io.Copy(io.Discard, r.Deeds())
	m, seal, err := r.Finish()
	if err != nil || seal == nil || m.Range.Count != 20 || m.Chain.Anchor.Kind != "genesis" || m.Chain.Head.Seq != 20 || m.Kinds["admin"] != 2 {
		t.Fatalf("%v %+v", err, m)
	}
	if *m.Files["deeds.jsonl"].Lines != 20 || m.Generator.Commit != "4f2c1a9" {
		t.Fatalf("%+v", m.Files)
	}

	// A partial range anchors on the deed before it.
	b2 := build(t, c, 11, 20, nil)
	r2, _ := archive.Open(bytes.NewReader(b2), archive.Limits{})
	_, _ = io.Copy(io.Discard, r2.Deeds())
	m2, seal2, err := r2.Finish()
	if err != nil || seal2 != nil || m2.Chain.Anchor.Kind != "deed" || m2.Chain.Anchor.Seq != 10 || m2.Chain.Anchor.BodyHash != c.Deeds[9].BodyHash {
		t.Fatalf("%v %+v %+v", err, m2.Chain, seal2)
	}
	if !strings.Contains(string(r2.README()), "seq 11 to 20") {
		t.Fatalf("%s", r2.README())
	}
	if archive.FileName(c.Org, 1, 1204) != "deedglass-"+c.Org+"-1-1204.tar.gz" {
		t.Fatal("file name")
	}
}

const goldenPath = "testdata/genesis-20.sha256"

func TestDeterministic(t *testing.T) {
	c := deedtest.NewChain(20, deedtest.ChainOpts{})
	a := build(t, c, 1, 20, deedtest.TestKey("b").Signer)
	b := build(t, c, 1, 20, deedtest.TestKey("b").Signer)
	if !bytes.Equal(a, b) {
		t.Fatal("two writes differ")
	}
	sum := sha256.Sum256(a)
	got := hex.EncodeToString(sum[:]) + "\n"
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil || string(want) != got {
		t.Fatalf("bundle SHA-256 %s, golden %s (run go test ./internal/deed/archive -update after checking the change)", got, want)
	}
	// Header and gzip fields are fixed.
	zr, _ := gzip.NewReader(bytes.NewReader(a))
	if zr.Name != "" || !zr.ModTime.IsZero() || zr.OS != 255 {
		t.Fatalf("gzip header %+v", zr.Header)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
		if h.Mode != 0o644 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || !h.ModTime.Equal(deedtest.T0.Add(time.Hour)) || h.Typeflag != tar.TypeReg {
			t.Fatalf("%s: %+v", h.Name, h)
		}
	}
	if strings.Join(names, " ") != "README.txt keys.json deeds.jsonl manifest.json manifest.seal.json" {
		t.Fatal(names)
	}
}

func TestTamperedDeedIsFound(t *testing.T) {
	c := deedtest.NewChain(20, deedtest.ChainOpts{})
	b := rewrite(t, build(t, c, 1, 20, deedtest.TestKey("b").Signer), func(name string, data []byte) []byte {
		if name != "deeds.jsonl" {
			return data
		}
		lines := bytes.SplitAfter(data, []byte("\n"))
		l := lines[6] // deed 7
		j := bytes.Index(l, []byte(`"prev_hash":"sha256:`)) + len(`"prev_hash":"sha256:`)
		l[j] ^= 1
		out := bytes.Join(lines, nil)
		return out
	})
	rep, err := verify(t, b)
	if err != nil {
		t.Fatal(err)
	}
	if f := rep.FirstFailure(); f == nil || f.Code != "body_hash_mismatch" || *f.Seq != 7 {
		t.Fatalf("%+v", rep.Failures)
	}
}

// entry is one raw tar entry for hostile bundles.
type entry struct {
	name string
	data []byte
	typ  byte
	size int64 // declared size when > 0 (data is then streamed as zeros)
	link string
}

func raw(t testing.TB, es ...entry) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	tw := tar.NewWriter(zw)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Typeflag: e.typ, Linkname: e.link}
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag != tar.TypeReg {
			h.Size = 0
		}
		if e.size > 0 {
			h.Size = e.size
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.size > 0 {
			_, _ = io.CopyN(tw, zeros{}, e.size)
		} else if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write(e.data)
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	return out.Bytes()
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// rewrite rebuilds a bundle with each file passed through edit (gzip and tar re-encoded).
func rewrite(t testing.TB, b []byte, edit func(name string, data []byte) []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var es []entry
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		d, _ := io.ReadAll(tr)
		if nd := edit(h.Name, d); nd != nil {
			es = append(es, entry{name: h.Name, data: nd})
		}
	}
	return raw(t, es...)
}

func files(t testing.TB) map[string][]byte {
	c := deedtest.NewChain(3, deedtest.ChainOpts{})
	b := build(t, c, 1, 3, deedtest.TestKey("a").Signer)
	out := map[string][]byte{}
	rewrite(t, b, func(name string, data []byte) []byte { out[name] = data; return data })
	return out
}

func code(err error) string {
	var ve *deed.VerifyError
	if errors.As(err, &ve) {
		return string(ve.Code) + ": " + ve.Detail
	}
	if err == nil {
		return "ok"
	}
	return "other: " + err.Error()
}

func TestReaderRules(t *testing.T) {
	f := files(t)
	std := func(extra ...entry) []entry {
		es := []entry{{name: "README.txt", data: f["README.txt"]}, {name: "keys.json", data: f["keys.json"]},
			{name: "deeds.jsonl", data: f["deeds.jsonl"]}, {name: "manifest.json", data: f["manifest.json"]},
			{name: "manifest.seal.json", data: f["manifest.seal.json"]}}
		return append(es, extra...)
	}
	badManifest := func(edit func(m map[string]any)) []byte {
		var m map[string]any
		_ = json.Unmarshal(f["manifest.json"], &m)
		edit(m)
		b, err := deed.StrictCanonical(m)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	replace := func(name string, data []byte) []entry {
		es := std()
		for i := range es {
			if es[i].name == name {
				es[i].data = data
			}
		}
		return es
	}
	cases := []struct {
		name  string
		input []byte
		want  string // substring of "code: detail"
	}{
		{"ok", raw(t, std()...), "ok"},
		{"not gzip", []byte("deeds"), "This file is not a gzip-compressed deed bundle."},
		{"unknown file", raw(t, append([]entry{{name: "notes.txt", data: []byte("x")}}, std()...)...), `entry named "notes.txt"`},
		{"traversal", raw(t, entry{name: "../../.ssh/authorized_keys", data: []byte("ssh-ed25519 AAAA")}), `This bundle contains an entry named "../../.ssh/authorized_keys".`},
		{"subdirectory", raw(t, entry{name: "x/README.txt", data: []byte("x")}), `entry named "x/README.txt"`},
		{"dot file", raw(t, entry{name: ".README.txt", data: []byte("x")}), `entry named ".README.txt"`},
		{"symlink", raw(t, entry{name: "README.txt", typ: tar.TypeSymlink, link: "/etc/passwd"}), `"README.txt", which is not a regular file`},
		{"directory", raw(t, entry{name: "README.txt", typ: tar.TypeDir}), "not a regular file"},
		{"duplicate", raw(t, entry{name: "README.txt", data: f["README.txt"]}, entry{name: "README.txt", data: f["README.txt"]}), `"README.txt" twice`},
		{"duplicate later", raw(t, append(std()[:4], entry{name: "manifest.json", data: f["manifest.json"]})...), `"manifest.json" twice`},
		{"six entries", raw(t, std(entry{name: "manifest.seal.json", data: f["manifest.seal.json"]})...), "twice"},
		{"out of order", raw(t, entry{name: "keys.json", data: f["keys.json"]}), `found "keys.json" where README.txt should be`},
		{"missing deeds", raw(t, std()[:2]...), "This bundle has no deeds.jsonl."},
		{"oversize keys", raw(t, entry{name: "README.txt", data: f["README.txt"]}, entry{name: "keys.json", data: bytes.Repeat([]byte(" "), 1<<20+1)}), "keys.json is larger than 1 MiB"},
		{"oversize declared", raw(t, entry{name: "README.txt", size: 1 << 20}), "README.txt is larger than 64 KiB"},
		{"manifest hash wrong", raw(t, replace("deeds.jsonl", bytes.Replace(f["deeds.jsonl"], []byte(`"seq":2`), []byte(`"seq":9`), 1))...), "does not match deeds.jsonl (sha256)"},
		{"deeds longer", raw(t, replace("deeds.jsonl", append(append([]byte(nil), f["deeds.jsonl"]...), f["deeds.jsonl"][:100]...))...), "does not match deeds.jsonl (size)"},
		{"README changed", raw(t, replace("README.txt", []byte("hello"))...), "does not match README.txt"},
		{"keys changed", raw(t, replace("keys.json", append(append([]byte(nil), f["keys.json"]...), ' '))...), "does not match keys.json"},
		{"line count wrong", raw(t, replace("manifest.json", badManifest(func(m map[string]any) {
			m["files"].(map[string]any)["deeds.jsonl"].(map[string]any)["lines"] = 2
		}))...), "line count"},
		{"manifest not canonical", raw(t, replace("manifest.json", append([]byte(" "), f["manifest.json"]...))...), "not canonical"},
		{"manifest unknown field", raw(t, replace("manifest.json", badManifest(func(m map[string]any) { m["note"] = "x" }))...), "unknown or mistyped field"},
		{"manifest version", raw(t, replace("manifest.json", badManifest(func(m map[string]any) { m["v"] = 2 }))...), "version 2"},
		{"manifest range", raw(t, replace("manifest.json", badManifest(func(m map[string]any) { m["range"].(map[string]any)["count"] = 9 }))...), "(range)"},
		{"manifest head", raw(t, replace("manifest.json", badManifest(func(m map[string]any) { m["chain"].(map[string]any)["head"].(map[string]any)["seq"] = 9 }))...), "(head)"},
		{"manifest bad JSON", raw(t, replace("manifest.json", []byte(`{"a":1.5}`))...), "not strict JSON"},
		{"seal not JSON", raw(t, replace("manifest.seal.json", []byte(`{}`))...), "manifest seal is not valid"},
		{"seal not canonical", raw(t, replace("manifest.seal.json", append([]byte(" "), f["manifest.seal.json"]...))...), "manifest seal is not canonical"},
		{"wrong after manifest", raw(t, append(std()[:4], entry{name: "keys.json", data: f["keys.json"]})...), `"keys.json" twice`},
		{"README after manifest", raw(t, append(std()[:4], entry{name: "README.txt", data: nil})...), "twice"},
		{"truncated", raw(t, std()...)[:300], "damaged"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := archive.Open(bytes.NewReader(tc.input), archive.Limits{})
			if err == nil {
				_, _ = io.Copy(io.Discard, r.Deeds())
				_, _, err = r.Finish()
			}
			if got := code(err); (tc.want == "ok") != (got == "ok") || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if tc.want != "ok" && !strings.HasPrefix(code(err), "bundle_invalid: ") {
				t.Fatalf("code %q", code(err))
			}
		})
	}
}

func TestGzipBomb(t *testing.T) {
	// 1 GiB of zeros, compressed on the fly: the reader stops after the first blocks.
	pr, pw := io.Pipe()
	go func() {
		zw := gzip.NewWriter(pw)
		_, err := io.CopyN(zw, zeros{}, 1<<30)
		if err == nil {
			err = zw.Close()
		}
		pw.CloseWithError(err)
	}()
	start := time.Now()
	_, err := archive.Open(pr, archive.Limits{})
	pr.Close()
	if !strings.Contains(code(err), "bundle_invalid") || time.Since(start) > 5*time.Second {
		t.Fatalf("%s after %s", code(err), time.Since(start))
	}
	// A deeds.jsonl that declares more than --max-bytes is refused from its header.
	lim := archive.DefaultLimits()
	lim.Deeds = 1 << 20
	f := files(t)
	b := raw(t, entry{name: "README.txt", data: f["README.txt"]}, entry{name: "keys.json", data: f["keys.json"]}, entry{name: "deeds.jsonl", size: 1 << 21})
	if _, err := archive.Open(bytes.NewReader(b), lim); !strings.Contains(code(err), "deeds.jsonl is larger than 1 MiB") {
		t.Fatal(code(err))
	}
	// The total cap stops a stream that expands past the sum of the limits.
	lim = archive.Limits{README: 1024, Keys: 1024, Deeds: 1024, Manifest: 1024, Seal: 1024}
	b = raw(t, entry{name: "README.txt", data: bytes.Repeat([]byte("a"), 1000)}, entry{name: "keys.json", size: 900},
		entry{name: "deeds.jsonl", size: 1000}, entry{name: "manifest.json", data: bytes.Repeat([]byte("{"), 20000)})
	r, err := archive.Open(bytes.NewReader(b), lim)
	if err == nil {
		_, _ = io.Copy(io.Discard, r.Deeds())
		_, _, err = r.Finish()
	}
	if !strings.Contains(code(err), "bundle_invalid") {
		t.Fatal(code(err))
	}
}

func TestSeal(t *testing.T) {
	c := deedtest.NewChain(5, deedtest.ChainOpts{})
	b := build(t, c, 1, 5, deedtest.TestKey("b").Signer)
	if _, err := verify(t, b); err != nil {
		t.Fatal(err)
	}
	// Unsealed.
	if _, err := verify(t, build(t, c, 1, 5, nil)); !errors.Is(err, errNoSeal) {
		t.Fatal(err)
	}
	// Sealed by the revoked key, by an unknown key, over other bytes.
	for name, s := range map[string]deed.Signer{"revoked": deedtest.TestKey("r").Signer} {
		if _, err := verify(t, build(t, c, 1, 5, s)); deed.CodeOf(err) != deed.CodeBundleSealInvalid {
			t.Fatalf("%s: %v", name, err)
		}
	}
	other, _ := deed.NewFileSigner(deedtest.ID("sk", 9), bytes.Repeat([]byte{7}, 32))
	if _, err := verify(t, build(t, c, 1, 5, other)); deed.CodeOf(err) != deed.CodeBundleSealInvalid {
		t.Fatal(err)
	}
	fs := files(t)
	seal := fs["manifest.seal.json"]
	var m map[string]any
	_ = json.Unmarshal(seal, &m)
	sig := []byte(m["seal"].(string))
	if sig[10] == 'A' {
		sig[10] = 'B'
	} else {
		sig[10] = 'A'
	}
	m["seal"] = string(sig)
	flipped, _ := deed.StrictCanonical(m)
	bad := rewrite(t, build(t, deedtest.NewChain(3, deedtest.ChainOpts{}), 1, 3, deedtest.TestKey("a").Signer), func(name string, d []byte) []byte {
		if name == "manifest.seal.json" {
			return flipped
		}
		return d
	})
	if _, err := verify(t, bad); deed.CodeOf(err) != deed.CodeBundleSealInvalid {
		t.Fatal(code(err))
	}
	// The seal of another manifest.
	m["manifest_hash"] = deed.Digest([]byte("x"))
	other2, _ := deed.StrictCanonical(m)
	bad = rewrite(t, build(t, deedtest.NewChain(3, deedtest.ChainOpts{}), 1, 3, deedtest.TestKey("a").Signer), func(name string, d []byte) []byte {
		if name == "manifest.seal.json" {
			return other2
		}
		return d
	})
	if _, err := verify(t, bad); deed.CodeOf(err) != deed.CodeBundleSealInvalid {
		t.Fatal(code(err))
	}
}

func TestManifestCheck(t *testing.T) {
	c := deedtest.NewChain(5, deedtest.ChainOpts{})
	r, _ := archive.Open(bytes.NewReader(build(t, c, 1, 5, nil)), archive.Limits{})
	_, _ = io.Copy(io.Discard, r.Deeds())
	m, _, _ := r.Finish()
	ok := archive.Observed{Count: 5, FirstSeq: 1, LastSeq: 5, From: m.Range.From, To: m.Range.To, FirstPrevHash: deed.Genesis(c.Org),
		Kinds: m.Kinds, Head: m.Chain.Head}
	if err := m.Check(ok); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(o *archive.Observed){
		"count":  func(o *archive.Observed) { o.Count = 4 },
		"range":  func(o *archive.Observed) { o.LastSeq = 4 },
		"dates":  func(o *archive.Observed) { o.To = "x" },
		"head":   func(o *archive.Observed) { o.Head.BodyHash = "x" },
		"anchor": func(o *archive.Observed) { o.FirstPrevHash = "x" },
		"kinds":  func(o *archive.Observed) { o.Kinds = map[string]int64{} },
	} {
		o := ok
		edit(&o)
		if err := m.Check(o); !strings.Contains(code(err), "("+name+")") {
			t.Errorf("%s: %s", name, code(err))
		}
	}
}

func TestWriterRefusals(t *testing.T) {
	c := deedtest.NewChain(5, deedtest.ChainOpts{})
	h := header(c, 1, 5)
	k := keysJSON(t)
	var size int64
	for _, l := range c.Lines {
		size += int64(len(l)) + 1
	}
	newW := func(h archive.Header, size, n int64) (*archive.Writer, error) {
		return archive.NewWriter(io.Discard, h, k, size, n)
	}
	noOrg := h
	noOrg.Org = ""
	for name, f := range map[string]func() error{
		"no org":    func() error { _, err := newW(noOrg, size, 5); return err },
		"bad range": func() error { _, err := newW(h, size, 4); return err },
		"tiny size": func() error { _, err := newW(h, 3, 5); return err },
		"bad keys":  func() error { _, err := archive.NewWriter(io.Discard, h, []byte("{}"), size, 5); return err },
		"huge keys": func() error { _, err := archive.NewWriter(io.Discard, h, make([]byte, 2<<20), size, 5); return err },
		"wrong seq": func() error { w, _ := newW(h, size, 5); return w.WriteDeed(c.Lines[1]) },
		"newline":   func() error { w, _ := newW(h, size, 5); return w.WriteDeed(append(c.Lines[0], '\n')) },
		"not JSON":  func() error { w, _ := newW(h, size, 5); return w.WriteDeed([]byte("x")) },
		"short": func() error {
			w, _ := newW(h, size, 5)
			_ = w.WriteDeed(c.Lines[0])
			_, err := w.Close(nil)
			return err
		},
		"wrong from": func() error {
			h2 := h
			h2.From = "2026-01-01T00:00:00.000Z"
			w, _ := newW(h2, size, 5)
			return w.WriteDeed(c.Lines[0])
		},
		"wrong to": func() error {
			h2 := h
			h2.To = "2027-01-01T00:00:00.000Z"
			w, _ := newW(h2, size, 5)
			for _, l := range c.Lines {
				_ = w.WriteDeed(l)
			}
			_, err := w.Close(nil)
			return err
		},
		"other org": func() error {
			o := deedtest.NewChain(1, deedtest.ChainOpts{Org: deedtest.Org(3)})
			w, _ := newW(h, size, 5)
			return w.WriteDeed(o.Lines[0])
		},
		"broken link": func() error {
			w, _ := newW(h, size, 5)
			_ = w.WriteDeed(c.Lines[0])
			x := deedtest.Canonical(deedtest.Reseal(c.Deeds[1], deedtest.TestKey("a"), func(b *deed.Body) { b.PrevHash = deed.Genesis(c.Org) }))
			return w.WriteDeed(x)
		},
		"too many bytes": func() error {
			w, _ := newW(h, size-10, 5)
			var err error
			for _, l := range c.Lines {
				if err = w.WriteDeed(l); err != nil {
					break
				}
			}
			return err
		},
	} {
		if err := f(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// After an error the writer stays failed.
	w, _ := newW(h, size, 5)
	_ = w.WriteDeed(c.Lines[2])
	if err := w.WriteDeed(c.Lines[0]); !errors.Is(err, archive.ErrWriter) {
		t.Fatal(err)
	}
	if _, err := w.Close(nil); err == nil {
		t.Fatal("closed after an error")
	}
}

func FuzzArchiveOpen(f *testing.F) {
	fs := files(f)
	f.Add(raw(f, entry{name: "README.txt", data: fs["README.txt"]}, entry{name: "keys.json", data: fs["keys.json"]},
		entry{name: "deeds.jsonl", data: fs["deeds.jsonl"]}, entry{name: "manifest.json", data: fs["manifest.json"]}))
	f.Add([]byte{0x1f, 0x8b})
	lim := archive.Limits{README: 4096, Keys: 8192, Deeds: 1 << 16, Manifest: 4096, Seal: 1024}
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := archive.Open(bytes.NewReader(b), lim)
		if err != nil {
			if deed.CodeOf(err) != deed.CodeBundleInvalid {
				t.Fatalf("unexpected error type: %v", err)
			}
			return
		}
		n, _ := io.Copy(io.Discard, r.Deeds())
		if n > lim.Deeds || int64(len(r.Keys())) > lim.Keys {
			t.Fatal("read past a limit")
		}
		if _, _, err := r.Finish(); err != nil && deed.CodeOf(err) != deed.CodeBundleInvalid {
			t.Fatalf("unexpected error type: %v", err)
		}
	})
}
