package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

const (
	vec  = "../../testdata/deeds/"
	keys = vec + "keys/test-keys.json"
)

var noColor = map[string]string{"NO_COLOR": "1"}

// writeBundle writes deeds from..to of c as an export bundle at path.
func writeBundle(t testing.TB, path string, c *deedtest.Chain, from, to int, s deed.Signer, edit func(name string, b []byte) []byte) {
	t.Helper()
	ksJSON, err := deed.StrictCanonical(deedtest.KeySet())
	if err != nil {
		t.Fatal(err)
	}
	var size int64
	for _, l := range c.Lines[from-1 : to] {
		size += int64(len(l)) + 1
	}
	h := archive.Header{Org: c.Org, Issuer: "https://api.deedglass.example", CreatedAt: deedtest.T0.Add(time.Hour),
		Generator: archive.Generator{Name: "deedglass", Version: "0.1.0-dev", Commit: "4f2c1a9"},
		FirstSeq:  int64(from), LastSeq: int64(to), From: c.Deeds[from-1].Body.IssuedAt, To: c.Deeds[to-1].Body.IssuedAt}
	var out bytes.Buffer
	w, err := archive.NewWriter(&out, h, ksJSON, size, int64(to-from+1))
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
	b := out.Bytes()
	if edit != nil {
		b = rebundle(t, b, edit)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtures writes every generated input into dir and returns dir.
func fixtures(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	g := deedtest.NewChain(20, deedtest.ChainOpts{})
	b := deedtest.TestKey("b").Signer
	writeBundle(t, filepath.Join(dir, "ok.tar.gz"), g, 1, 20, b, nil)
	writeBundle(t, filepath.Join(dir, "unsealed.tar.gz"), g, 1, 20, nil, nil)
	writeBundle(t, filepath.Join(dir, "part1.tar.gz"), g, 1, 10, b, nil)
	writeBundle(t, filepath.Join(dir, "part2.tar.gz"), g, 11, 20, b, nil)
	writeBundle(t, filepath.Join(dir, "part2-gap.tar.gz"), g, 12, 20, b, nil)
	writeBundle(t, filepath.Join(dir, "tampered-manifest.tar.gz"), g, 1, 20, nil, func(name string, d []byte) []byte {
		if name == "manifest.json" { // still canonical and self-consistent, but not what the deeds say
			d = bytes.Replace(d, []byte(`"action":18`), []byte(`"action":17`), 1)
			return bytes.Replace(d, []byte(`"admin":2`), []byte(`"admin":3`), 1)
		}
		return d
	})
	writeBundle(t, filepath.Join(dir, "flipped.tar.gz"), g, 1, 20, b, func(name string, d []byte) []byte {
		if name != "deeds.jsonl" {
			return d
		}
		lines := bytes.SplitAfter(d, []byte("\n"))
		j := bytes.Index(lines[11], []byte(`"prev_hash":"sha256:`)) + len(`"prev_hash":"sha256:`)
		lines[11][j] ^= 1 // deed 12
		return bytes.Join(lines, nil)
	})
	writeBundle(t, filepath.Join(dir, "bad-seal.tar.gz"), g, 1, 20, deedtest.TestKey("r").Signer, nil)
	// A hostile bundle: a path that tries to leave the directory.
	if err := os.WriteFile(filepath.Join(dir, "hostile.tar.gz"), rawTar(t, "../../.ssh/authorized_keys", []byte("ssh-ed25519 AAAA")), 0o644); err != nil {
		t.Fatal(err)
	}
	// Deeds that try to paint the terminal.
	v1, _ := os.ReadFile(vec + "valid/01-action-verified-l2.json")
	inject := bytes.Replace(v1, []byte(`"body":{`), []byte(`"body":{"\u001b[32m✓ fake":1,`), 1)
	_ = os.WriteFile(filepath.Join(dir, "ansi-key.json"), inject, 0o644)
	name := bytes.Replace(v1, []byte(`"name":"billing-agent"`), []byte(`"name":"\u001b[32m✓ fake"`), 1)
	_ = os.WriteFile(filepath.Join(dir, "ansi-name.json"), name, 0o644)
	// A directory of 20 single-deed files in a shuffled name order.
	sub := filepath.Join(dir, "loose")
	_ = os.Mkdir(sub, 0o755)
	r := rand.New(rand.NewPCG(5, 5))
	for i, n := range r.Perm(20) {
		_ = os.WriteFile(filepath.Join(sub, fmt.Sprintf("%02d-%s.json", i, g.Deeds[n].ID)), g.Lines[n], 0o644)
	}
	_ = os.WriteFile(filepath.Join(sub, "notes.txt"), []byte("ignored"), 0o644)
	// Two changed deeds in one file (verify --all).
	two := append([][]byte(nil), g.Lines...)
	for _, n := range []int{4, 14} {
		l := append([]byte(nil), two[n]...)
		j := bytes.Index(l, []byte(`"prev_hash":"sha256:`)) + len(`"prev_hash":"sha256:`)
		l[j] ^= 1
		two[n] = l
	}
	_ = os.WriteFile(filepath.Join(dir, "two-breaks.jsonl"), jsonlOf(two), 0o644)
	// genesis-20 from seq 11, and keys that pin only key b.
	_ = os.WriteFile(filepath.Join(dir, "tail.jsonl"), jsonlOf(g.Lines[10:]), 0o644)
	return dir
}

func jsonlOf(lines [][]byte) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		b.Write(l)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

var (
	reDuration = regexp.MustCompile(`"duration_ms": [0-9]+`)
	reTook     = regexp.MustCompile(`took       [0-9.]+ s`)
)

// goldenVerify compares a run with cmd/deedglass/testdata/verify/<name>/{args,stdout,stderr,exit}.
func goldenVerify(t *testing.T, name string, args []string, r result, tmp string) {
	t.Helper()
	norm := func(s string) string {
		s = strings.ReplaceAll(s, tmp, "$TMP")
		s = reTook.ReplaceAllString(s, "took       0.00 s")
		return reDuration.ReplaceAllString(s, `"duration_ms": 0`)
	}
	got := map[string]string{
		"args":   norm(strings.Join(args, "\n")) + "\n",
		"stdout": norm(r.stdout),
		"stderr": norm(r.stderr),
		"exit":   fmt.Sprintf("%d\n", r.code),
	}
	dir := filepath.Join("testdata", "verify", name)
	if *update {
		_ = os.MkdirAll(dir, 0o755)
		for f, v := range got {
			if err := os.WriteFile(filepath.Join(dir, f), []byte(v), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for f, v := range got {
		want, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("%v (run go test ./cmd/deedglass -run TestVerifyGolden -update)", err)
		}
		if string(want) != v {
			t.Errorf("%s/%s differs (run with -update after checking)\n--- got\n%s--- want\n%s", name, f, v, want)
		}
	}
}

type vcase struct {
	name  string
	args  []string
	stdin []byte
}

func verifyCases(t *testing.T, tmp string) []vcase {
	var cs []vcase
	add := func(name string, args ...string) {
		cs = append(cs, vcase{name: name, args: append([]string{"verify"}, args...)})
	}
	var exp map[string]struct {
		StrictCode *string `json:"strict_code"`
	}
	raw, _ := os.ReadFile(vec + "expected.json")
	_ = json.Unmarshal(raw, &exp)
	var vecs []string
	for p := range exp {
		vecs = append(vecs, p)
	}
	sort.Strings(vecs)
	for _, p := range vecs {
		n := "vector-" + strings.NewReplacer("/", "-", ".json", "", ".jsonl", "", ".cose", "-cose").Replace(p)
		add(n, vec+p, "--keys", keys)
		if exp[p].StrictCode != nil {
			add(n+"-strict", vec+p, "--keys", keys, "--strict")
		}
	}
	dirs, _ := filepath.Glob(vec + "invalid/*")
	for _, d := range dirs {
		f, _ := filepath.Glob(d + "/deed*")
		k := keys
		if _, err := os.Stat(d + "/keys.json"); err == nil {
			k = d + "/keys.json"
		}
		add("invalid-"+filepath.Base(d), f[0], "--keys", k)
	}
	for _, n := range []string{"v2/valid/01-ed25519-only.json", "v2/valid/chain-v1-then-v2.jsonl", "v2/invalid/unsupported-algorithm/deed.json",
		"v2/invalid/seal-stripped/deed.json", "v2/valid/03-dual-sealed.json", "v2/valid/chain-across-pq-rotation.jsonl",
		"v2/invalid/pq-seal-stripped/deed.json", "v2/invalid/pq-downgrade-to-v1/deed.json", "v2/cose/03-dual-sealed.cose"} {
		add("v2-"+strings.NewReplacer("v2/", "", "/", "-", ".jsonl", "", ".json", "", ".cose", "-cose").Replace(n), vec+n, "--keys", vec+"v2/keys.json")
	}
	add("v2-invalid-ed25519-alone-2035-v1-deed", vec+"v2/invalid/ed25519-alone-2035-v1/deed.json", "--keys", vec+"v2/invalid/ed25519-alone-2035-v1/keys.json")
	add("valid-all-loose", vec+"valid/01-action-verified-l2.json", vec+"valid/02-action-denied.json", vec+"valid/03-admin-agent-stopped.json",
		vec+"valid/04-amendment-mismatch.json", vec+"valid/05-action-approved-then-executed.json", vec+"valid/06-unicode-agent-name.json", "--keys", keys)
	add("chain-verbose", vec+"chains/genesis-20.jsonl", "--keys", keys, "-v")
	add("keys-missing", vec+"chains/genesis-20.jsonl")
	add("bundle-ok", tmp+"/ok.tar.gz", "--keys", keys)
	add("bundle-ok-verbose", tmp+"/ok.tar.gz", "--keys", keys, "-v")
	add("bundle-keys-not-pinned", tmp+"/ok.tar.gz")
	add("bundle-keys-not-pinned-strict", tmp+"/ok.tar.gz", "--strict")
	add("bundle-missing-seal", tmp+"/unsealed.tar.gz", "--keys", keys)
	add("bundle-tampered-manifest", tmp+"/tampered-manifest.tar.gz", "--keys", keys)
	add("bundle-flipped-byte", tmp+"/flipped.tar.gz", "--keys", keys)
	add("bundle-seal-revoked-key", tmp+"/bad-seal.tar.gz", "--keys", keys)
	add("bundle-hostile", tmp+"/hostile.tar.gz", "--keys", keys)
	add("two-bundles-continuous", tmp+"/part1.tar.gz", tmp+"/part2.tar.gz", "--keys", keys)
	add("two-bundles-gap", tmp+"/part1.tar.gz", tmp+"/part2-gap.tar.gz", "--keys", keys)
	add("pin-excludes-sealing-key", vec+"chains/genesis-20.jsonl", "--keys", keys, "--pin", "sha256:3d3876ffac00eac374b0e6e5735718edfcc7637ace18ef40a3ccc492cf98bf60")
	add("pin-keeps-both", vec+"chains/genesis-20.jsonl", "--keys", keys, "--pin", "sha256:3d3876ffac00eac374b0e6e5735718edfcc7637ace18ef40a3ccc492cf98bf60",
		"--pin", "sha256:f6b1d24a4efdc39230905e0c217093f4d1d5e301ac52c9e763a773e864d93f33")
	add("anchor-ok", tmp+"/tail.jsonl", "--keys", keys, "--anchor", "10:"+deedtest.NewChain(10, deedtest.ChainOpts{}).Deeds[9].BodyHash)
	add("anchor-wrong", tmp+"/tail.jsonl", "--keys", keys, "--anchor", "10:"+deed.Genesis(deedtest.Org(2)))
	add("no-anchor-mid-chain", tmp+"/tail.jsonl", "--keys", keys)
	add("org-pin-mismatch", vec+"chains/genesis-20.jsonl", "--keys", keys, "--org", deedtest.Org(4))
	add("ansi-unknown-key", tmp+"/ansi-key.json", "--keys", keys)
	add("ansi-unknown-key-json", tmp+"/ansi-key.json", "--keys", keys, "--json")
	add("ansi-agent-name", tmp+"/ansi-name.json", "--keys", keys)
	add("json-valid", vec+"chains/genesis-20.jsonl", "--keys", keys, "--json")
	add("json-invalid", vec+"invalid/chain-seq-gap/deeds.jsonl", "--keys", keys, "--json")
	add("json-keys-missing", vec+"chains/genesis-20.jsonl", "--json")
	add("all-flag", tmp+"/two-breaks.jsonl", "--keys", keys, "--all")
	add("all-flag-json", tmp+"/two-breaks.jsonl", "--keys", keys, "--all", "--json")
	add("first-break", tmp+"/two-breaks.jsonl", "--keys", keys)
	add("flags-before-path", "--keys", keys, "--strict", vec+"lenient/key-order.json")
	add("double-dash", "--keys", keys, "--", vec+"chains/genesis-20.jsonl")
	add("directory", tmp+"/loose", "--keys", keys)
	add("quiet-invalid", vec+"invalid/body-edit/deed.json", "--keys", keys, "-q")
	add("no-paths", "--keys", keys)
	add("missing-file", tmp+"/nope.jsonl", "--keys", keys)
	add("bad-anchor", vec+"chains/genesis-20.jsonl", "--anchor", "x")
	add("bad-pin", vec+"chains/genesis-20.jsonl", "--pin", "abc")
	add("keys-not-a-key-set", vec+"chains/genesis-20.jsonl", "--keys", vec+"valid/01-action-verified-l2.json")
	add("keys-http-remote", vec+"chains/genesis-20.jsonl", "--keys", "http://example.com/keys.json")
	cs = append(cs, vcase{name: "stdin", args: []string{"verify", "-", "--keys", keys}, stdin: mustRead(t, vec+"chains/genesis-20.jsonl")})
	cs = append(cs, vcase{name: "stdin-one-deed", args: []string{"verify", "-", "--keys", keys}, stdin: mustRead(t, vec+"lenient/whitespace.json")})
	cs = append(cs, vcase{name: "stdin-bundle", args: []string{"verify", "-", "--keys", keys}, stdin: mustRead(t, tmp+"/ok.tar.gz")})
	return cs
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestVerifyGolden(t *testing.T) {
	tmp := fixtures(t)
	seen := map[string]bool{}
	for _, c := range verifyCases(t, tmp) {
		seen[c.name] = true
		t.Run(c.name, func(t *testing.T) {
			r := cliIn(t, c.stdin, noColor, false, c.args...)
			goldenVerify(t, c.name, c.args, r, tmp)
		})
	}
	// No stale golden directories.
	ents, _ := os.ReadDir(filepath.Join("testdata", "verify"))
	for _, e := range ents {
		if !seen[e.Name()] {
			t.Errorf("testdata/verify/%s has no case; delete it", e.Name())
		}
	}
}

// TestVerifyMatchesVectors checks every expected.json against the CLI's exit code and JSON code.
func TestVerifyMatchesVectors(t *testing.T) {
	type exp struct {
		Valid      bool    `json:"valid"`
		Code       *string `json:"code"`
		Seq        *int64  `json:"seq"`
		Field      *string `json:"field"`
		StrictCode *string `json:"strict_code"`
		Exit       int     `json:"exit"`
	}
	check := func(name string, args []string, want exp) {
		r := cli(t, noColor, false, append(append([]string{"verify"}, args...), "--json")...)
		if r.code != want.Exit {
			t.Errorf("%s: exit %d, want %d\n%s%s", name, r.code, want.Exit, r.stdout, r.stderr)
			return
		}
		var rep deed.Report
		if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
			// A key set refusal happens before any deed: the error envelope.
			if want.Code != nil && strings.Contains(r.stderr, `"code":"`+*want.Code+`"`) {
				return
			}
			t.Errorf("%s: %v\n%s%s", name, err, r.stdout, r.stderr)
			return
		}
		f := rep.FirstFailure()
		switch {
		case want.Valid && f != nil:
			t.Errorf("%s: %+v", name, f)
		case !want.Valid && (f == nil || f.Code != *want.Code || !eq(f.Seq, want.Seq) || !eqs(f.Field, want.Field)):
			t.Errorf("%s: got %+v, want %+v", name, f, want)
		}
	}
	var all map[string]exp
	_ = json.Unmarshal(mustRead(t, vec+"expected.json"), &all)
	for p, w := range all {
		check(p, []string{vec + p, "--keys", keys}, w)
		if w.StrictCode != nil {
			c := *w.StrictCode
			one := int64(1)
			check(p+" --strict", []string{vec + p, "--keys", keys, "--strict"}, exp{Code: &c, Seq: &one, Exit: 1})
		}
	}
	dirs, _ := filepath.Glob(vec + "invalid/*")
	if len(dirs) != 26 {
		t.Fatalf("%d invalid vectors", len(dirs))
	}
	for _, d := range dirs {
		var w exp
		_ = json.Unmarshal(mustRead(t, d+"/expected.json"), &w)
		f, _ := filepath.Glob(d + "/deed*")
		k := keys
		if _, err := os.Stat(d + "/keys.json"); err == nil {
			k = d + "/keys.json"
		}
		check(d, []string{f[0], "--keys", k}, w)
	}
	// Deed v2 (ADR-109): the same answers, exit codes included (unsupported_algorithm exits 2).
	var allV2 map[string]struct {
		exp
		Keys string `json:"keys"`
	}
	_ = json.Unmarshal(mustRead(t, vec+"v2/expected.json"), &allV2)
	for p, w := range allV2 {
		k := vec + "v2/keys.json"
		if w.Keys != "" {
			k = vec + w.Keys
		}
		check(p, []string{vec + p, "--keys", k}, w.exp)
	}
	dirsV2, _ := filepath.Glob(vec + "v2/invalid/*")
	if len(dirsV2) < 15 {
		t.Fatalf("%d v2 invalid vectors", len(dirsV2))
	}
	for _, d := range dirsV2 {
		var w exp
		_ = json.Unmarshal(mustRead(t, d+"/expected.json"), &w)
		f, _ := filepath.Glob(d + "/deed*")
		k := vec + "v2/keys.json"
		if _, err := os.Stat(d + "/keys.json"); err == nil {
			k = d + "/keys.json"
		}
		check(d, []string{f[0], "--keys", k}, w)
	}
}

func eq(a, b *int64) bool   { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }
func eqs(a, b *string) bool { return (a == nil && b == nil) || (a != nil && b != nil && *a == *b) }

func TestVerifyAcceptance(t *testing.T) {
	valid, _ := filepath.Glob(vec + "valid/*.json")
	r := cli(t, noColor, false, append(append([]string{"verify"}, valid...), "--keys", keys)...)
	if r.code != 0 || !strings.HasPrefix(r.stdout, "✓ 6 deeds · seals intact · chain not checked · 1 seal key\n") {
		t.Fatalf("%d %s", r.code, r.stdout)
	}
	r = cli(t, noColor, false, "verify", vec+"chains/genesis-20.jsonl", "--keys", keys)
	if r.code != 0 || !strings.HasPrefix(r.stdout, "✓ 20 deeds · chain intact · 2 seal keys · 2026-10-02\n") {
		t.Fatalf("%d %s", r.code, r.stdout)
	}
	r = cli(t, noColor, false, "verify", vec+"chains/genesis-20.jsonl")
	if r.code != 2 || !strings.HasPrefix(r.stdout, "○ Cannot check deed 1: no seal keys were given.\n") {
		t.Fatalf("%d %s", r.code, r.stdout)
	}
	tmp := fixtures(t)
	r = cli(t, noColor, false, "verify", tmp+"/flipped.tar.gz", "--keys", keys)
	if r.code != 1 || !strings.HasPrefix(r.stdout, "✕ Deed 12 was changed after it was sealed.\n") {
		t.Fatalf("%d %s", r.code, r.stdout)
	}
}

func TestVerifyColorAndProgress(t *testing.T) {
	g := deedtest.NewChain(6000, deedtest.ChainOpts{})
	saved := progressMin
	progressMin = 0
	t.Cleanup(func() { progressMin = saved })
	r := cliIn(t, jsonlOf(g.Lines), map[string]string{"TERM": "xterm"}, true, "verify", "-", "--keys", keys)
	if r.code != 0 || !strings.Contains(r.stdout, "\x1b[33m◐\x1b[0m Checking") || !strings.Contains(r.stdout, "\r\x1b[K\x1b[36m✓\x1b[0m 6,000 deeds") {
		t.Fatalf("%q", r.stdout)
	}
	r = cli(t, map[string]string{"TERM": "xterm"}, true, "verify", vec+"invalid/body-edit/deed.json", "--keys", keys)
	if !strings.HasPrefix(r.stdout, "\x1b[31m✕\x1b[0m Deed 1") {
		t.Fatalf("%q", r.stdout)
	}
}

func TestClean(t *testing.T) {
	bs := "\\" // one backslash; escapes in the expected text are built at run time
	u := func(hex string) string { return bs + "u" + hex }
	for in, want := range map[string]string{
		"plain":                          "plain",
		"\x1b[32m✓ fake":                 u("001b") + "[32m✓ fake",
		"a" + string(rune(0x202e)) + "b": "a" + u("202e") + "b",
		"a" + string(rune(0x2066)) + "b" + string(rune(0x2069)): "a" + u("2066") + "b" + u("2069"),
		"tab\there":               "tab" + u("0009") + "here",
		"c1" + string(rune(0x85)): "c1" + u("0085"),
		"del\x7f":                 "del" + u("007f"),
		"bad\xffutf8":             "bad" + bs + "xffutf8",
		"Ünïcödé 日本 ok":           "Ünïcödé 日本 ok",
		"new\nline\r":             "new" + u("000a") + "line" + u("000d"),
	} {
		if got := clean(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// --- --keys URLs ---

func keysServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	ts := httptest.NewTLSServer(h)
	t.Cleanup(ts.Close)
	saved := verifyTransport
	verifyTransport = ts.Client().Transport
	t.Cleanup(func() { verifyTransport = saved })
	return ts
}

func TestVerifyKeysURL(t *testing.T) {
	ks := mustRead(t, keys)
	chain := vec + "chains/genesis-20.jsonl"
	var (
		mu                          sync.Mutex
		gotUA, gotAccept, gotCookie string
	)
	ts := keysServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotUA, gotAccept, gotCookie = r.UserAgent(), r.Header.Get("Accept"), r.Header.Get("Cookie")
		mu.Unlock()
		switch r.URL.Path {
		case "/.well-known/deedglass/keys.json":
			http.SetCookie(w, &http.Cookie{Name: "track", Value: "1"})
			w.Write(ks)
		case "/moved":
			http.Redirect(w, r, "/.well-known/deedglass/keys.json", http.StatusFound)
		case "/elsewhere":
			http.Redirect(w, r, "https://evil.example/keys.json", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/big":
			w.Write(bytes.Repeat([]byte(" "), 2<<20))
		case "/slow":
			time.Sleep(300 * time.Millisecond)
			w.Write(ks)
		default:
			http.NotFound(w, r)
		}
	})
	host := strings.TrimPrefix(ts.URL, "https://")
	r := cli(t, noColor, false, "verify", chain, "--keys", ts.URL+"/.well-known/deedglass/keys.json", "-v")
	if r.code != 0 || !strings.Contains(r.stdout, "keys from  "+ts.URL+"/.well-known/deedglass/keys.json (pinned)") {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	mu.Lock()
	if !strings.HasPrefix(gotUA, "deedglass/") || gotAccept != "application/json" || gotCookie != "" {
		t.Fatalf("headers: %q %q %q", gotUA, gotAccept, gotCookie)
	}
	mu.Unlock()
	r = cli(t, noColor, false, "verify", chain, "--keys", ts.URL+"/moved")
	if r.code != 0 {
		t.Fatalf("same-host redirect: %d %s", r.code, r.stderr)
	}
	saved := keysFetchTimeout
	keysFetchTimeout = 100 * time.Millisecond
	t.Cleanup(func() { keysFetchTimeout = saved })
	for path, want := range map[string]string{
		"/elsewhere": "○ Could not fetch seal keys from " + host + " (it redirected to evil.example).",
		"/loop":      "(more than 3 redirects)",
		"/big":       "(the answer is larger than 1 MiB)",
		"/missing":   "(it answered 404)",
		"/slow":      "(timed out after 0.1 s)",
	} {
		r := cli(t, noColor, false, "verify", chain, "--keys", ts.URL+path)
		if r.code != 2 || !strings.Contains(r.stderr, want) || !strings.Contains(r.stderr, "next  Check the URL, or download keys.json") {
			t.Errorf("%s: %d %q", path, r.code, r.stderr)
		}
	}
	// Plain http only to this machine.
	r = cli(t, noColor, false, "verify", chain, "--keys", "http://10.0.0.1/keys.json")
	if r.code != 2 || !strings.Contains(r.stderr, "uses http to a host that is not this machine") {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(ks) }))
	defer plain.Close()
	verifyTransport = http.DefaultTransport
	if r := cli(t, noColor, false, "verify", chain, "--keys", plain.URL+"/keys.json"); r.code != 0 {
		t.Fatalf("loopback http: %d %s", r.code, r.stderr)
	}
}

func TestVerifyKeysMerge(t *testing.T) {
	dir := t.TempDir()
	ks := deedtest.KeySet()
	a, _ := json.Marshal(ks)
	// The same key id with another public key.
	other := *ks
	other.Keys = append([]deed.SealKey(nil), ks.Keys...)
	x := deedtest.TestKey("r").Signer.PublicKey()
	k := deed.NewSealKey(deed.KeyRecord{ID: ks.Keys[1].ID, PublicKey: x, Status: deed.KeyActive, ActivatedAt: deedtest.AActivated})
	other.Keys[1] = k
	b, _ := json.Marshal(other)
	_ = os.WriteFile(filepath.Join(dir, "a.json"), a, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b.json"), b, 0o644)
	chain := vec + "chains/genesis-20.jsonl"
	r := cli(t, noColor, false, "verify", chain, "--keys", dir+"/a.json", "--keys", dir+"/b.json")
	if r.code != 1 || !strings.Contains(r.stderr, "differs between") {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
	// The same set twice merges cleanly.
	if r := cli(t, noColor, false, "verify", chain, "--keys", dir+"/a.json", "--keys", keys); r.code != 0 {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
}

func TestVerifyNeverTouchesTheNetworkWithoutKeysURL(t *testing.T) {
	saved := verifyTransport
	verifyTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("network used")
		return nil, nil
	})
	t.Cleanup(func() { verifyTransport = saved })
	tmp := fixtures(t)
	for _, args := range [][]string{
		{"verify", vec + "chains/genesis-20.jsonl", "--keys", keys},
		{"verify", tmp + "/ok.tar.gz"},
		{"verify", tmp + "/loose", "--keys", keys, "--all"},
	} {
		cli(t, noColor, false, args...)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVerifyBundleNeverWritesToDisk(t *testing.T) {
	tmp := fixtures(t)
	before := listTree(t, tmp)
	cli(t, noColor, false, "verify", tmp+"/hostile.tar.gz", "--keys", keys)
	cli(t, noColor, false, "verify", tmp+"/ok.tar.gz")
	if after := listTree(t, tmp); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatal("verify wrote files")
	}
	if _, err := os.Stat(filepath.Join(tmp, "..", "..", ".ssh", "authorized_keys")); err == nil {
		t.Fatal("a file escaped")
	}
}

func listTree(t *testing.T, dir string) []string {
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestVerifyDirectoryErrors(t *testing.T) {
	if _, err := dirRecords("/nonexistent-dir"); err == nil {
		t.Fatal("missing dir")
	}
}

// rebundle re-encodes a bundle with each file passed through edit (not deterministic: test input).
func rebundle(t testing.TB, b []byte, edit func(name string, data []byte) []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	tw := tar.NewWriter(zw)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		d, _ := io.ReadAll(tr)
		d = edit(h.Name, d)
		h.Size = int64(len(d))
		_ = tw.WriteHeader(h)
		_, _ = tw.Write(d)
	}
	_ = tw.Close()
	_ = zw.Close()
	return out.Bytes()
}

// rawTar is a gzip-compressed tar with one entry.
func rawTar(t testing.TB, name string, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	tw := tar.NewWriter(zw)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(data)
	_ = tw.Close()
	_ = zw.Close()
	return out.Bytes()
}
