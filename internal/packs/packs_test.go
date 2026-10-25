package packs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

const testKeyID = "sk_01m4ppb8d5fkph9mevbjc6cv8z"

func testSigner(t *testing.T) (*deed.FileSigner, KeySet) {
	t.Helper()
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	s, err := deed.NewFileSigner(testKeyID, seed)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := KeySetJSON([]KeyRecord{Record(testKeyID, s.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	ks, err := ParseKeySet(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s, ks
}

func clone(s *Source) *Source {
	out := &Source{Origin: s.Origin, Files: map[string][]byte{}}
	for k, v := range s.Files {
		out.Files[k] = bytes.Clone(v)
	}
	return out
}

// signed returns the embedded snapshot sealed by the test key.
func signed(t *testing.T) (*Source, KeySet) {
	t.Helper()
	s, ks := testSigner(t)
	src := clone(Embedded())
	ix, err := ParseIndex(src.Files[IndexFile])
	if err != nil {
		t.Fatal(err)
	}
	index, seal, err := Sign(ix, s, time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	src.Files[IndexFile], src.Files[SealFile] = index, seal
	return src, ks
}

func TestEmbeddedSnapshot(t *testing.T) {
	c, err := Verify(Embedded(), KeySet{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sealed {
		t.Fatal("the snapshot is unsigned until the founder signs it")
	}
	names := map[string]string{}
	for _, e := range c.Index.Packs {
		names[e.Name] = e.Tier
		if e.Cases < MinCases {
			t.Errorf("%s has %d cases", e.Name, e.Cases)
		}
		r := Check(context.Background(), e, c.PackFiles(e))
		if !r.OK() {
			t.Errorf("%s: %+v", e.Name, r.Problems)
		}
		if r.Test.Coverage.Covered != r.Test.Coverage.Total {
			t.Errorf("%s leaves rules uncovered: %v", e.Name, r.Test.Coverage.Uncovered)
		}
	}
	want := map[string]string{"postgres-safe": "free", "postgres-check": "free", "supabase-mcp": "free", "stripe-refunds": "free", "github-agent": "free", "money-movement": "pro",
		"healthcare-fhir": "free", "india-health": "free"}
	if len(names) != len(want) {
		t.Fatalf("packs %v, want %v", names, want)
	}
	for n, tier := range want {
		if names[n] != tier {
			t.Errorf("%s tier %q, want %q", n, names[n], tier)
		}
	}
	if _, err := Verify(Embedded(), KeySet{}, true); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("an unsigned index must be refused when a seal is required: %v", err)
	}
	ks, err := EmbeddedKeys()
	if err != nil {
		t.Fatal(err)
	}
	if ks.Len() != 0 {
		t.Log("pack keys present: make sure the snapshot is signed by one of them")
	}
}

func TestSealRoundTrip(t *testing.T) {
	src, ks := signed(t)
	c, err := Verify(src, ks, true)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Sealed || c.KeyID != testKeyID || c.Index.SignedAt != "2026-10-12T09:00:00Z" {
		t.Fatalf("%+v", c)
	}
	if !strings.HasPrefix(string(SealInput(src.Files[IndexFile])), "deedglass/packs/v1\nsha256:") {
		t.Fatal("seal input tag")
	}
}

func TestVerifyRefusals(t *testing.T) {
	src, ks := signed(t)
	otherSeed := bytes.Repeat([]byte{9}, ed25519.SeedSize)
	other, _ := deed.NewFileSigner("sk_01m4ppb8d5fkph9mevbjc6cv8y", otherSeed)
	otherRaw, _ := KeySetJSON([]KeyRecord{Record(other.KeyID(), other.PublicKey())})
	otherKS, _ := ParseKeySet(otherRaw)

	cases := []struct {
		name  string
		edit  func(s *Source)
		ks    KeySet
		check func(error) bool
	}{
		{"a changed policy byte", func(s *Source) { s.Files["postgres-safe/postgres-safe.yaml"][0] ^= 1 }, ks,
			func(err error) bool {
				var m *MismatchError
				return errors.As(err, &m) && m.File == "postgres-safe.yaml"
			}},
		{"a missing cases file", func(s *Source) { delete(s.Files, "github-agent/github-agent.cases.yaml") }, ks,
			func(err error) bool { var m *MismatchError; return errors.As(err, &m) && m.Why == "is missing" }},
		{"a changed index", func(s *Source) {
			s.Files[IndexFile] = bytes.Replace(s.Files[IndexFile], []byte(`"cases":29`), []byte(`"cases":30`), 1)
		}, ks, func(err error) bool { return errors.Is(err, ErrSeal) }},
		{"an unknown key", func(*Source) {}, otherKS, func(err error) bool { return errors.Is(err, ErrKeyUnknown) }},
		{"no keys at all", func(*Source) {}, KeySet{}, func(err error) bool { return errors.Is(err, ErrKeyUnknown) }},
		{"a bad seal file", func(s *Source) { s.Files[SealFile] = []byte(`{"seal":"x","key_id":"sk_1"}`) }, ks,
			func(err error) bool { var f *FormatError; return errors.As(err, &f) && f.File == SealFile }},
		{"no index", func(s *Source) { delete(s.Files, IndexFile) }, ks, func(err error) bool { return errors.Is(err, ErrNoIndex) }},
		{"an index that isn't canonical", func(s *Source) { s.Files[IndexFile] = append([]byte(" "), s.Files[IndexFile]...) }, ks,
			func(err error) bool {
				var f *FormatError
				return errors.As(err, &f) && strings.Contains(f.Why, "canonical")
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := clone(src)
			tc.edit(s)
			_, err := Verify(s, tc.ks, true)
			if err == nil || !tc.check(err) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestMetaMustAgree(t *testing.T) {
	src := clone(Embedded())
	p := "supabase-mcp/pack.yaml"
	src.Files[p] = bytes.Replace(src.Files[p], []byte("tier: free"), []byte("tier: pro"), 1)
	// Re-hash the edited file so only the disagreement remains.
	ix, _ := ParseIndex(src.Files[IndexFile])
	for i, e := range ix.Packs {
		if e.Name == "supabase-mcp" {
			ix.Packs[i].Files[MetaFile] = deed.Digest(src.Files[p])
			ix.Packs[i].SHA256 = FilesDigest(ix.Packs[i].Files)
		}
	}
	src.Files[IndexFile], _ = Canonical(ix)
	var m *MismatchError
	if _, err := Verify(src, KeySet{}, false); !errors.As(err, &m) || m.File != MetaFile {
		t.Fatalf("got %v", err)
	}
}

func TestParseIndexRefusals(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":       `{`,
		"wrong object":   `{"object":"x","packs":[],"v":1}`,
		"unknown field":  `{"extra":1,"object":"pack_index","packs":[],"v":1}`,
		"key no time":    `{"key_id":"sk_01m4ppb8d5fkph9mevbjc6cv8z","object":"pack_index","packs":[],"v":1}`,
		"bad pack name":  `{"object":"pack_index","packs":[{"cases":12,"files":{},"id":"pck_01m4ppb8d5fkph9mevbjc6cv8d","name":"Bad Name","sha256":"x","summary":"s","tier":"free","version":"1.0.0"}],"v":1}`,
		"few cases":      `{"object":"pack_index","packs":[{"cases":3,"files":{},"id":"pck_01m4ppb8d5fkph9mevbjc6cv8d","name":"ok","sha256":"sha256:` + strings.Repeat("0", 64) + `","summary":"s","tier":"free","version":"1.0.0"}],"v":1}`,
		"missing files":  `{"object":"pack_index","packs":[{"cases":12,"files":{},"id":"pck_01m4ppb8d5fkph9mevbjc6cv8d","name":"ok","sha256":"sha256:` + strings.Repeat("0", 64) + `","summary":"s","tier":"free","version":"1.0.0"}],"v":1}`,
		"bad file hash":  `{"object":"pack_index","packs":[{"cases":12,"files":{"README.md":"x","ok.cases.yaml":"x","ok.yaml":"x","pack.yaml":"x"},"id":"pck_01m4ppb8d5fkph9mevbjc6cv8d","name":"ok","sha256":"sha256:` + strings.Repeat("0", 64) + `","summary":"s","tier":"free","version":"1.0.0"}],"v":1}`,
		"bad tier value": `{"object":"pack_index","packs":[{"cases":12,"files":{},"id":"pck_01m4ppb8d5fkph9mevbjc6cv8d","name":"ok","sha256":"sha256:` + strings.Repeat("0", 64) + `","summary":"s","tier":"gold","version":"1.0.0"}],"v":1}`,
	} {
		if _, err := ParseIndex([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dup := Embedded().Files[IndexFile]
	ix, _ := ParseIndex(dup)
	ix.Packs = append(ix.Packs, ix.Packs[0])
	b, _ := Canonical(ix)
	if _, err := ParseIndex(b); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a duplicate pack: %v", err)
	}
	if _, ok := ix.Find("postgres-safe", "9.9.9"); ok {
		t.Error("Find matched a version that isn't there")
	}
	if e, ok := ix.Find("postgres-safe", ""); !ok || e.Policy() != "postgres-safe.yaml" || e.CasesFile() != "postgres-safe.cases.yaml" {
		t.Error("Find")
	}
}

func TestParseMeta(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown key": "id: pck_01m4ppb8d5fkph9mevbjc6cv8d\nname: ok\nversion: 1.0.0\ntier: free\nsummary: s\ncolour: red\n",
		"no summary":  "id: pck_01m4ppb8d5fkph9mevbjc6cv8d\nname: ok\nversion: 1.0.0\ntier: free\n",
		"bad version": "id: pck_01m4ppb8d5fkph9mevbjc6cv8d\nname: ok\nversion: v1\ntier: free\nsummary: s\n",
		"bad id":      "id: pol_1\nname: ok\nversion: 1.0.0\ntier: free\nsummary: s\n",
	} {
		if _, err := ParseMeta([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseKeySetRefusals(t *testing.T) {
	_, ks := testSigner(t)
	if ks.Len() != 1 {
		t.Fatal("len")
	}
	good := Record(testKeyID, bytes.Repeat([]byte{1}, 32))
	for name, raw := range map[string]string{
		"not json":   `[`,
		"wrong obj":  `{"object":"licence_key_set","keys":[]}`,
		"bad id":     `{"object":"pack_key_set","keys":[{"id":"lk_1","alg":"Ed25519","public_key":"","thumbprint":""}]}`,
		"bad alg":    `{"object":"pack_key_set","keys":[{"id":"` + testKeyID + `","alg":"RSA","public_key":"","thumbprint":""}]}`,
		"bad b64":    `{"object":"pack_key_set","keys":[{"id":"` + testKeyID + `","alg":"Ed25519","public_key":"!!","thumbprint":""}]}`,
		"bad thumb":  `{"object":"pack_key_set","keys":[{"id":"` + testKeyID + `","alg":"Ed25519","public_key":"` + good.PublicKey + `","thumbprint":"sha256:00"}]}`,
		"small key":  `{"object":"pack_key_set","keys":[{"id":"` + testKeyID + `","alg":"Ed25519","public_key":"AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","thumbprint":""}]}`,
		"unknown kv": `{"object":"pack_key_set","keys":[],"x":1}`,
	} {
		if _, err := ParseKeySet([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	s, _ := testSigner(t)
	rec := Record(testKeyID, s.PublicKey())
	twice, _ := KeySetJSON([]KeyRecord{rec, rec})
	if _, err := ParseKeySet(twice); err == nil {
		t.Error("a key listed twice was accepted")
	}
	empty, _ := KeySetJSON(nil)
	if ks, err := ParseKeySet(empty); err != nil || ks.Len() != 0 {
		t.Error("empty set")
	}
}

func tarGz(t *testing.T, entries []tar.Header, bodies map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, h := range entries {
		b := bodies[h.Name]
		h.Size = int64(len(b))
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func archiveOf(t *testing.T, src *Source, prefix string) []byte {
	var hs []tar.Header
	bodies := map[string][]byte{}
	hs = append(hs, tar.Header{Name: prefix, Typeflag: tar.TypeDir})
	for p, b := range src.Files {
		hs = append(hs, tar.Header{Name: prefix + p, Typeflag: tar.TypeReg})
		bodies[prefix+p] = b
	}
	return tarGz(t, hs, bodies)
}

func TestFromTarGz(t *testing.T) {
	src, ks := signed(t)
	for _, prefix := range []string{"", "policy-packs-1.0.0/"} {
		got, err := FromTarGz(bytes.NewReader(archiveOf(t, src, prefix)), "test.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(got, ks, true); err != nil {
			t.Fatalf("prefix %q: %v", prefix, err)
		}
	}
	bad := map[string][]byte{
		"symlink":   tarGz(t, []tar.Header{{Name: "index.json", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, nil),
		"traversal": tarGz(t, []tar.Header{{Name: "../index.json", Typeflag: tar.TypeReg}}, map[string][]byte{"../index.json": []byte("{}")}),
		"absolute":  tarGz(t, []tar.Header{{Name: "/index.json", Typeflag: tar.TypeReg}}, map[string][]byte{"/index.json": []byte("{}")}),
		"too large": tarGz(t, []tar.Header{{Name: "big", Typeflag: tar.TypeReg}}, map[string][]byte{"big": make([]byte, MaxFileBytes+1)}),
		"duplicate": tarGz(t, []tar.Header{{Name: "a", Typeflag: tar.TypeReg}, {Name: "a", Typeflag: tar.TypeReg}}, map[string][]byte{"a": []byte("x")}),
		"not gzip":  []byte("plain"),
		"bad tar": func() []byte {
			var b bytes.Buffer
			z := gzip.NewWriter(&b)
			z.Write([]byte("not a tar archive at all, just text that is long enough"))
			z.Close()
			return b.Bytes()
		}(),
	}
	for name, raw := range bad {
		if _, err := FromTarGz(bytes.NewReader(raw), name); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Two top directories: nothing is stripped, so there is no index.
	two := tarGz(t, []tar.Header{{Name: "a/index.json", Typeflag: tar.TypeReg}, {Name: "b/x", Typeflag: tar.TypeReg}}, map[string][]byte{"a/index.json": []byte("{}"), "b/x": nil})
	s, err := FromTarGz(bytes.NewReader(two), "two")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Files[IndexFile]; ok {
		t.Error("stripped a top directory that wasn't shared")
	}
}

func TestFromDir(t *testing.T) {
	src, ks := signed(t)
	dir := t.TempDir()
	for p, b := range src.Files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref"), 0o644)
	os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("x"), 0o644)
	got, err := FromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(got, ks, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "link")); err != nil {
		t.Skip(err)
	}
	if _, err := FromDir(dir); err == nil || !strings.Contains(err.Error(), "links are refused") {
		t.Fatalf("a symlink: %v", err)
	}
	if _, err := FromDir(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing directory")
	}
}

func TestSourceLimits(t *testing.T) {
	s := &Source{Files: map[string][]byte{}}
	total := 0
	for i := range MaxFiles {
		if err := s.add(string(rune('a'+i%26))+strings.Repeat("x", i), nil, &total); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.add("one-more", nil, &total); err == nil {
		t.Error("file count limit")
	}
	s = &Source{Files: map[string][]byte{}}
	total = MaxTotalBytes - 1
	if err := s.add("big", []byte("xx"), &total); err == nil {
		t.Error("total limit")
	}
}

func TestFetch(t *testing.T) {
	src, ks := signed(t)
	archive := archiveOf(t, src, "policy-packs/")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write(archive)
		case "/evil":
			http.Redirect(w, r, "https://evil.example/x.tar.gz", http.StatusFound)
		case "/plain-github":
			http.Redirect(w, r, "http://github.com/x.tar.gz", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	got, err := Fetch(context.Background(), c, srv.URL+"/ok")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(got, ks, true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/evil", "/plain-github", "/missing"} {
		if _, err := Fetch(context.Background(), c, srv.URL+p); err == nil {
			t.Errorf("%s: accepted", p)
		}
	}
	if _, err := Fetch(context.Background(), c, "http://127.0.0.1:1/x"); err == nil {
		t.Error("an unreachable host")
	}
	for raw, want := range map[string]bool{
		"https://objects.githubusercontent.com/a": true, "https://github.com/a": true,
		"https://user@github.com/a": false, "https://github.com:8443/a": false, "https://github.com.evil/a": false,
	} {
		u, _ := url.Parse(raw)
		if allowedRedirect(u) != want {
			t.Errorf("%s: %v", raw, !want)
		}
	}
}

func TestBuildIndex(t *testing.T) {
	src := clone(Embedded())
	delete(src.Files, IndexFile)
	ix, err := BuildIndex(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Canonical(ix)
	if !bytes.Equal(b, Embedded().Files[IndexFile]) {
		t.Fatal("the committed index is stale: go run ./cmd/packgen")
	}
	edits := map[string]func(s *Source){
		"a failing case": func(s *Source) {
			p := "postgres-safe/postgres-safe.cases.yaml"
			s.Files[p] = bytes.Replace(s.Files[p], []byte("decision: allow"), []byte("decision: deny"), 1)
		},
		"a policy that doesn't compile": func(s *Source) {
			s.Files["postgres-safe/postgres-safe.yaml"] = []byte("deedglass: policy/v1\nrules: 3\n")
		},
		"a missing README": func(s *Source) { delete(s.Files, "github-agent/README.md") },
		"a name that differs": func(s *Source) {
			s.Files["github-agent/pack.yaml"] = bytes.Replace(s.Files["github-agent/pack.yaml"], []byte("name: github-agent"), []byte("name: github-other"), 1)
		},
		"bad pack.yaml": func(s *Source) { s.Files["github-agent/pack.yaml"] = []byte("x: 1\n") },
		"too few cases": func(s *Source) {
			p := "supabase-mcp/supabase-mcp.cases.yaml"
			lines := strings.Split(string(s.Files[p]), "\n  - name:")
			s.Files[p] = []byte(strings.Join(lines[:8], "\n  - name:") + "\n")
		},
		"nothing": func(s *Source) {
			for k := range s.Files {
				delete(s.Files, k)
			}
		},
	}
	for name, edit := range edits {
		s := clone(src)
		edit(s)
		if _, err := BuildIndex(context.Background(), s); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

func TestPlanFiles(t *testing.T) {
	c, err := Verify(Embedded(), KeySet{}, false)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := c.Index.Find("postgres-safe", "")
	files := c.PackFiles(e)
	disk := map[string][]byte{"policies/postgres-safe.yaml": files[e.Policy()]}
	plan := PlanFiles(e, files, "policies/", func(p string) ([]byte, bool) { b, ok := disk[p]; return b, ok })
	if len(plan) != 2 || plan[0].Path != "policies/postgres-safe.yaml" || !plan[0].Same || plan[1].Exists || plan[1].Diff.Added == 0 {
		t.Fatalf("%+v", plan)
	}
}

func TestNoneHasNothing(t *testing.T) {
	var e Entitlements = None{}
	if e.Has(FeaturePro) {
		t.Fatal("None has a feature")
	}
}
