package archive_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/deed/stamp"
	"github.com/deedglass/deedglass/internal/deed/stamp/stamptest"
)

// stampsFor returns timestamps.json with one TSA token over deed seq of c.
func stampsFor(t *testing.T, c *deedtest.Chain, seqs ...int) []byte {
	t.Helper()
	tsa := stamptest.NewTSA("archive")
	defer tsa.Close()
	f := stamp.NewFile(c.Org)
	for _, seq := range seqs {
		d := c.Deeds[seq-1]
		h := stamp.NewHead(c.Org, d.Body.Seq, d.Body.DeedID, d.BodyHash, d.Body.IssuedAt)
		imp, err := h.Imprint()
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Add(stamp.Entry{Head: h, RFC3161: []stamp.Token{{TSA: tsa.URL, Token: stamp.EncodeB64(tsa.Token(imp, deedtest.T0.Add(2*3600e9)))}}}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func buildWith(t *testing.T, c *deedtest.Chain, from, to int, ts []byte) []byte {
	t.Helper()
	var size int64
	for _, l := range c.Lines[from-1 : to] {
		size += int64(len(l)) + 1
	}
	h := header(c, from, to)
	h.Timestamps = ts
	var out bytes.Buffer
	w, err := archive.NewWriter(&out, h, keysJSON(t), size, int64(to-from+1))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range c.Lines[from-1 : to] {
		if err := w.WriteDeed(l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Close(deedtest.TestKey("b").Signer); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestTimestampsEntry(t *testing.T) {
	c := deedtest.NewChain(3, deedtest.ChainOpts{})
	ts := stampsFor(t, c, 2, 3)
	b := buildWith(t, c, 1, 3, ts)
	if _, err := verify(t, b); err != nil {
		t.Fatalf("a bundle with timestamps.json: %v", err)
	}
	r, err := archive.Open(bytes.NewReader(b), archive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.Timestamps(), ts) {
		t.Fatal("Timestamps() is not the file")
	}
	if !strings.Contains(string(r.README()), "timestamps.json      independent timestamps of the chain head") {
		t.Fatal("README.txt doesn't list timestamps.json")
	}
	m, _, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Files[archive.NameTimestamps]; !ok || len(m.Files) != 4 {
		t.Fatalf("manifest files %v", m.Files)
	}
	// Without timestamps the bundle is byte-identical to one written before TY-03.
	if !bytes.Equal(buildWith(t, c, 1, 3, nil), build(t, c, 1, 3, deedtest.TestKey("b").Signer)) {
		t.Fatal("a bundle without timestamps changed")
	}
	plain := build(t, c, 1, 3, deedtest.TestKey("b").Signer)
	if r, err := archive.Open(bytes.NewReader(plain), archive.Limits{}); err != nil || r.Timestamps() != nil {
		t.Fatalf("plain bundle: %v", err)
	}

	f := map[string][]byte{}
	rewrite(t, b, func(name string, data []byte) []byte { f[name] = data; return data })
	cases := []struct {
		name string
		es   []entry
		want string
	}{
		{"after the deeds", []entry{{name: "README.txt", data: f["README.txt"]}, {name: "keys.json", data: f["keys.json"]},
			{name: "deeds.jsonl", data: f["deeds.jsonl"]}, {name: "timestamps.json", data: f["timestamps.json"]}},
			`found "timestamps.json" where manifest.json should be`},
		{"before keys", []entry{{name: "README.txt", data: f["README.txt"]}, {name: "timestamps.json", data: f["timestamps.json"]}},
			`found "timestamps.json" where keys.json should be`},
		{"twice", []entry{{name: "README.txt", data: f["README.txt"]}, {name: "keys.json", data: f["keys.json"]},
			{name: "timestamps.json", data: f["timestamps.json"]}, {name: "timestamps.json", data: f["timestamps.json"]}},
			`contains "timestamps.json" twice`},
		{"too large", []entry{{name: "README.txt", data: f["README.txt"]}, {name: "keys.json", data: f["keys.json"]},
			{name: "timestamps.json", size: 1<<20 + 1}}, "timestamps.json is larger than 1 MiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := verify(t, raw(t, tc.es...))
			if got := code(err); !strings.Contains(got, tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
	// Removed, or edited: the manifest (and so its seal) no longer matches.
	gone := rewrite(t, b, func(name string, data []byte) []byte {
		if name == "timestamps.json" {
			return nil
		}
		return data
	})
	if _, err := verify(t, gone); !strings.Contains(code(err), "has no timestamps.json, but its manifest lists one") {
		t.Fatalf("removed: %s", code(err))
	}
	edited := rewrite(t, b, func(name string, data []byte) []byte {
		if name == "timestamps.json" {
			return bytes.Replace(data, []byte(`"v":1}`), []byte(`"v":1 }`), 1)
		}
		return data
	})
	if _, err := verify(t, edited); !strings.Contains(code(err), "manifest does not match timestamps.json") {
		t.Fatalf("edited: %s", code(err))
	}
}

func TestWriterTimestampsRefusals(t *testing.T) {
	c := deedtest.NewChain(5, deedtest.ChainOpts{})
	other := deedtest.NewChain(5, deedtest.ChainOpts{Org: deedtest.Org(3)})
	for name, ts := range map[string][]byte{
		"not canonical": append([]byte(" "), stampsFor(t, c, 2)...),
		"another org":   stampsFor(t, other, 2),
		"after the end": stampsFor(t, c, 5),
	} {
		t.Run(name, func(t *testing.T) {
			h := header(c, 1, 4)
			h.Timestamps = ts
			var size int64
			for _, l := range c.Lines[:4] {
				size += int64(len(l)) + 1
			}
			if _, err := archive.NewWriter(&bytes.Buffer{}, h, keysJSON(t), size, 4); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
