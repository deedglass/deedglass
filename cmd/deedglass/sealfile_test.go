package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/ledger"
)

const testKeysFile = "../../testdata/deeds/keys/test-keys.json"

// fakeSealServer answers POST /v1/deeds/files like the server: it seals file.sealed with the test
// key a, the label as admin.reason and data_digest over the four fields. It records every body.
type fakeSealServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newFakeSealServer(t *testing.T) *fakeSealServer {
	t.Helper()
	f := &fakeSealServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, raw)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "POST" || r.URL.Path != "/v1/deeds/files" || r.Header.Get("Authorization") != "Bearer adm" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":{"type":"permission_error","code":"insufficient_scope","message":"This is an agent key, and managing agents needs an admin key. Create one with deedglass org keys create on the server.","param":null,"request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/insufficient_scope"}}`)
			return
		}
		var in ledger.SealFileBody
		if err := json.Unmarshal(raw, &in); err != nil || ledger.CheckSealFile(in) != nil {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"validation_failed","message":"label must be 1 to 120 characters.","param":"label","request_id":"req_2","doc_url":"https://docs.deedglass.com/errors/validation_failed"}}`)
			return
		}
		env := sealFileDeed(t, ledger.FileSealedData{SHA256: in.SHA256, Bytes: *in.Bytes, MediaType: in.MediaType, Label: in.Label})
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(ledger.SealedFile{Object: "sealed_file", SHA256: in.SHA256, Bytes: *in.Bytes, MediaType: in.MediaType, Label: in.Label, Deed: env})
	}))
	t.Cleanup(f.Close)
	return f
}

// sealFileDeed seals a file.sealed deed as seq 1 of the test org's chain.
func sealFileDeed(t *testing.T, d ledger.FileSealedData) *deed.Envelope {
	t.Helper()
	digest, err := ledger.FileDataDigest(d)
	if err != nil {
		t.Fatal(err)
	}
	label := d.Label
	c := deedtest.NewChain(1, deedtest.ChainOpts{Body: func(org string, seq int64) deed.Body {
		dr, err := deed.ForAdmin(deed.AdminSnapshot{Org: org, Event: deedtest.ID("adm", 1), Action: ledger.AdminFileSealed,
			Actor: deed.Ref{Type: "api_key", ID: deedtest.ID("key", 1)}, Target: &deed.Ref{Type: "org", ID: org},
			Reason: &label, DataDigest: &digest})
		if err != nil {
			t.Fatal(err)
		}
		return dr.Body
	}})
	return c.Deeds[0]
}

func TestSealFileRoundTrip(t *testing.T) {
	srv := newFakeSealServer(t)
	dir := t.TempDir()
	pdf := filepath.Join(dir, "sample-report.pdf")
	content := []byte("%PDF-1.7\nsample report, never sent to the server\n%%EOF\n")
	if err := os.WriteFile(pdf, content, 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DEEDGLASS_SERVER": srv.URL, "DEEDGLASS_API_KEY": "adm", "NO_COLOR": "1"}
	label := "Full scan report · sample · Acme demo"

	r := cli(t, env, false, "deeds", "seal-file", pdf, "--label", label)
	t.Log("\n" + r.stdout)
	if r.code != 0 {
		t.Fatalf("seal-file exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{"✓ Sealed sample-report.pdf · " + deed.FormatCount(int64(len(content))) + " bytes · sha256:",
		"written to " + pdf + ".deed.json", "Footer: Sealed as deed_", "· check it with deedglass verify"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("seal-file output lacks %q:\n%s", want, r.stdout)
		}
	}
	// Only the four fields went to the server; the file's bytes never did.
	if len(srv.bodies) != 1 {
		t.Fatalf("%d requests", len(srv.bodies))
	}
	var sent map[string]any
	if err := json.Unmarshal(srv.bodies[0], &sent); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range sent {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"bytes", "label", "media_type", "sha256"}) || sent["media_type"] != "application/pdf" {
		t.Fatalf("sent %v", sent)
	}
	if bytes.Contains(srv.bodies[0], []byte("never sent")) || bytes.Contains(srv.bodies[0], []byte("%PDF")) {
		t.Fatal("the file's contents reached the server")
	}

	// verify --file: the deed checks, the file matches.
	deedPath := pdf + ".deed.json"
	r = cli(t, env, false, "verify", deedPath, "--file", pdf, "--keys", testKeysFile)
	t.Log("\n" + r.stdout)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ 1 deed · seals intact") || !strings.Contains(r.stdout, "✓ File matches · sample-report.pdf · ") ||
		!strings.Contains(r.stdout, label) {
		t.Fatalf("verify --file exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	// The plain verifier reads the same file (it is the deed envelope, JCS).
	if r := cli(t, env, false, "verify", deedPath, "--keys", testKeysFile); r.code != 0 {
		t.Fatalf("plain verify exit %d\n%s", r.code, r.stdout)
	}

	// One changed byte: exit 1 with the mismatch line.
	edited := filepath.Join(dir, "edited.pdf")
	changed := slices.Clone(content)
	changed[3] ^= 1
	if err := os.WriteFile(edited, changed, 0o644); err != nil {
		t.Fatal(err)
	}
	r = cli(t, env, false, "verify", deedPath, "--file", edited, "--keys", testKeysFile)
	t.Log("\n" + r.stdout)
	if r.code != 1 || !strings.Contains(r.stdout, "✕ edited.pdf doesn't match the deed. Its sha256 is sha256:") ||
		!strings.Contains(r.stdout, "next") {
		t.Fatalf("edited: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = cli(t, env, false, "--json", "verify", deedPath, "--file", edited, "--keys", testKeysFile)
	var rep struct {
		Result string `json:"result"`
		File   struct {
			Matches bool   `json:"matches"`
			Label   string `json:"label"`
		} `json:"file"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || r.code != 1 || rep.Result != "valid" || rep.File.Matches || rep.File.Label != label {
		t.Fatalf("edited --json: exit %d %v\n%s", r.code, err, r.stdout)
	}

	// A tampered deed fails as a deed first; the file isn't checked.
	raw, _ := os.ReadFile(deedPath)
	bad := bytes.Replace(raw, []byte("Acme demo"), []byte("Acme dema"), 1)
	badPath := filepath.Join(dir, "bad.deed.json")
	os.WriteFile(badPath, bad, 0o644)
	if r := cli(t, env, false, "verify", badPath, "--file", pdf, "--keys", testKeysFile); r.code != 1 || strings.Contains(r.stdout, "File matches") {
		t.Fatalf("tampered deed: exit %d\n%s", r.code, r.stdout)
	}
}

func TestSealFileRefusals(t *testing.T) {
	srv := newFakeSealServer(t)
	dir := t.TempDir()
	pdf := filepath.Join(dir, "r.pdf")
	os.WriteFile(pdf, []byte("%PDF"), 0o644)
	noext := filepath.Join(dir, "notes")
	os.WriteFile(noext, []byte("x"), 0o644)
	env := func(key string) map[string]string {
		return map[string]string{"DEEDGLASS_SERVER": srv.URL, "DEEDGLASS_API_KEY": key, "NO_COLOR": "1"}
	}
	cases := []struct {
		name string
		key  string
		args []string
		code int
		want string
	}{
		{"no label", "adm", []string{"deeds", "seal-file", pdf}, 2, "--label is required"},
		{"long label", "adm", []string{"deeds", "seal-file", pdf, "--label", strings.Repeat("x", 121)}, 2, "1 to 120 characters"},
		{"newline label", "adm", []string{"deeds", "seal-file", pdf, "--label", "a\nb"}, 2, "no line breaks"},
		{"bad media type", "adm", []string{"deeds", "seal-file", pdf, "--label", "x", "--media-type", "image/png"}, 2, "isn't a media type"},
		{"no extension", "adm", []string{"deeds", "seal-file", noext, "--label", "x"}, 2, "has no extension"},
		{"two files", "adm", []string{"deeds", "seal-file", pdf, noext, "--label", "x"}, 2, "takes one file"},
		{"missing file", "adm", []string{"deeds", "seal-file", filepath.Join(dir, "nope.pdf"), "--label", "x"}, 2, "Can't read"},
		{"agent key", "agentkey", []string{"deeds", "seal-file", pdf, "--label", "x"}, 1, "admin key"},
		{"no key", "", []string{"deeds", "seal-file", pdf, "--label", "x"}, 3, "admin API key"},
		{"explicit media type", "adm", []string{"deeds", "seal-file", noext, "--label", "Notes", "--media-type", "text/plain", "--out", filepath.Join(dir, "n.json")}, 0, "✓ Sealed notes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := cli(t, env(c.key), false, c.args...)
			if r.code != c.code || !strings.Contains(r.stdout+r.stderr, c.want) {
				t.Fatalf("exit %d, want %d with %q\n%s%s", r.code, c.code, c.want, r.stdout, r.stderr)
			}
		})
	}
	// --file with a deed that isn't a sealed file: exit 2.
	chain := deedtest.NewChain(1, deedtest.ChainOpts{})
	other := filepath.Join(dir, "action.deed.json")
	os.WriteFile(other, chain.Lines[0], 0o644)
	if r := cli(t, env("adm"), false, "verify", other, "--file", pdf, "--keys", testKeysFile); r.code != 2 || !strings.Contains(r.stdout+r.stderr, "isn't a sealed file") {
		t.Fatalf("not a sealed file: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	// --file with a stream: exit 2.
	if r := cli(t, env("adm"), false, "verify", "../../testdata/deeds/chains/genesis-20.jsonl", "--file", pdf, "--keys", testKeysFile); r.code != 2 {
		t.Fatalf("stream with --file: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}

// TestFileDataDigestGolden pins file.sealed's data_digest: the server and every verifier compute it
// the same way (JCS of the four fields).
func TestFileDataDigestGolden(t *testing.T) {
	d, err := ledger.FileDataDigest(ledger.FileSealedData{Bytes: 214388, Label: "Full scan report · sample · Acme demo",
		MediaType: "application/pdf", SHA256: "sha256:" + strings.Repeat("3f9a", 16)})
	if err != nil {
		t.Fatal(err)
	}
	const want = "sha256:a2071fb6e56a75989de245e593187ca2fab35e96f98c47ecc01a9624f1e6cea3"
	if d != want {
		t.Fatalf("data_digest %s, want %s", d, want)
	}
}
