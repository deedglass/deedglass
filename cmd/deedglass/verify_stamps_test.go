package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/deed/stamp"
	"github.com/deedglass/deedglass/internal/deed/stamp/fetch"
	"github.com/deedglass/deedglass/internal/deed/stamp/stamptest"
)

// stampFixture is a 20-deed chain whose head (deed 20) is timestamped by two fake TSAs and
// registered with a fake transparency service, all on loopback.
type stampFixture struct {
	dir   string
	chain *deedtest.Chain
	entry stamp.Entry
}

func newStampFixture(t *testing.T) *stampFixture {
	t.Helper()
	tsa1, tsa2, svc := stamptest.NewTSA("one"), stamptest.NewTSA("two"), stamptest.NewService()
	t.Cleanup(func() { tsa1.Close(); tsa2.Close(); svc.Close() })
	f := &stampFixture{dir: t.TempDir(), chain: deedtest.NewChain(20, deedtest.ChainOpts{})}
	d := f.chain.Deeds[19]
	key := svc.Key
	c := &fetch.Config{TSAs: []string{tsa1.URL, tsa2.URL}, MinTSA: 2, Service: svc.URL, ServiceKey: &key,
		Signer: deedtest.TestKey("b").Signer, Issuer: deedtest.Issuer, Client: tsa1.Client(),
		Now: func() time.Time { return time.Date(2026, 10, 20, 9, 31, 0, 0, time.UTC) }}
	e, err := fetch.Stamp(context.Background(), c, stamp.NewHead(f.chain.Org, d.Body.Seq, d.Body.DeedID, d.BodyHash, d.Body.IssuedAt))
	if err != nil {
		t.Fatal(err)
	}
	f.entry = e
	file := stamp.NewFile(f.chain.Org)
	if err := file.Add(e); err != nil {
		t.Fatal(err)
	}
	raw, err := file.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	jwk, _ := json.Marshal(svc.Key)
	for name, b := range map[string][]byte{
		"timestamps.json": raw,
		"roots.pem":       append(tsa1.RootPEM(), tsa2.RootPEM()...),
		"service.jwk":     jwk,
		"full.jsonl":      jsonlOf(f.chain.Lines),
		"cut.jsonl":       jsonlOf(f.chain.Lines[:17]),
	} {
		if err := os.WriteFile(filepath.Join(f.dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Deed 20 rewritten and sealed again with the same key: its seal and link are fine, its body
	// is not the one that was timestamped.
	re := deedtest.Reseal(f.chain.Deeds[19], deedtest.TestKey("b"), func(b *deed.Body) { b.IssuedAt = deed.FormatTime(deedtest.T0.Add(30 * time.Second)) })
	lines := append(append([][]byte(nil), f.chain.Lines[:19]...), deedtest.Canonical(re))
	if err := os.WriteFile(filepath.Join(f.dir, "rewritten.jsonl"), jsonlOf(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *stampFixture) path(name string) string { return filepath.Join(f.dir, name) }

func TestVerifyTimestamps(t *testing.T) {
	f := newStampFixture(t)
	pinned := []string{"--timestamps", f.path("timestamps.json"), "--tsa-roots", f.path("roots.pem"), "--scitt-key", f.path("service.jwk")}
	const head = "✓ Deed 20 timestamped 2026-10-20 09:30:00 UTC by 2 TSAs and 1 transparency service · matches the chain\n"

	r := cli(t, noColor, false, append([]string{"verify", f.path("full.jsonl"), "--keys", keys}, pinned...)...)
	if r.code != 0 || !strings.Contains(r.stdout, "chain intact · 2 seal keys · 2026-10-02 → 2026-10-02\n"+head) || strings.Contains(r.stdout, "not pinned") {
		t.Fatalf("full chain: %d\n%s%s", r.code, r.stdout, r.stderr)
	}

	// Unpinned: the same answer, with the two trust warnings.
	r = cli(t, noColor, false, "verify", f.path("full.jsonl"), "--keys", keys, "--timestamps", f.path("timestamps.json"))
	if r.code != 0 || !strings.Contains(r.stdout, head) || !strings.Contains(r.stdout, "○ The TSA chains were checked against themselves") ||
		!strings.Contains(r.stdout, "○ The transparency service's key came from the timestamps file") {
		t.Fatalf("unpinned: %d\n%s", r.code, r.stdout)
	}

	// TY-03's acceptance: a chain cut short fails against its timestamped head, offline.
	r = cli(t, noColor, false, append([]string{"verify", f.path("cut.jsonl"), "--keys", keys}, pinned...)...)
	if r.code != 1 || !strings.HasPrefix(r.stdout, "✕ Deeds after 17 are missing: deed 20 was timestamped at 2026-10-20 09:30:00 UTC.\n") {
		t.Fatalf("cut: %d\n%s", r.code, r.stdout)
	}

	// A rewritten head fails too, though its own seal and link verify.
	r = cli(t, noColor, false, append([]string{"verify", f.path("rewritten.jsonl"), "--keys", keys}, pinned...)...)
	if r.code != 1 || !strings.HasPrefix(r.stdout, "✕ Deed 20 doesn't match the head timestamped at 2026-10-20 09:30:00 UTC: the chain was changed after that time.\n") {
		t.Fatalf("rewritten: %d\n%s", r.code, r.stdout)
	}
	r = cli(t, noColor, false, "verify", f.path("rewritten.jsonl"), "--keys", keys)
	if r.code != 0 {
		t.Fatalf("without the timestamps the rewrite is invisible, as LIMITATIONS.md says: %d\n%s", r.code, r.stdout)
	}

	// JSON: the head, its evidence and the comparison.
	r = cli(t, noColor, false, append([]string{"--json", "verify", f.path("full.jsonl"), "--keys", keys}, pinned...)...)
	var rep deed.Report
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || r.code != 0 {
		t.Fatalf("json: %v %d %s", err, r.code, r.stdout)
	}
	if len(rep.Timestamps) != 1 || rep.Timestamps[0].Compared != "matches" || len(rep.Timestamps[0].Evidence) != 3 || !rep.Timestamps[0].Evidence[0].Pinned {
		t.Fatalf("json timestamps: %+v", rep.Timestamps)
	}
	r = cli(t, noColor, false, "--json", "verify", f.path("cut.jsonl"), "--keys", keys, "--timestamps", f.path("timestamps.json"))
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil || len(rep.Failures) != 1 || rep.Failures[0].Code != "chain_tail_missing" || *rep.Failures[0].Seq != 20 {
		t.Fatalf("json cut: %s", r.stdout)
	}

	// Inputs the reader passed that can't be read are input errors (exit 2).
	_ = os.WriteFile(f.path("junk.json"), []byte(`{"object":"head_timestamps"}`), 0o644)
	for _, args := range [][]string{
		{"--timestamps", f.path("junk.json")},
		{"--timestamps", f.path("missing.json")},
		{"--tsa-roots", f.path("service.jwk")},
		{"--scitt-key", f.path("roots.pem")},
	} {
		r = cli(t, noColor, false, append([]string{"verify", f.path("full.jsonl"), "--keys", keys}, args...)...)
		if r.code != 2 {
			t.Fatalf("%v: %d %s%s", args, r.code, r.stdout, r.stderr)
		}
	}

	// A key the reader pins that isn't the service's: the registration fails.
	other := stamptest.NewService()
	defer other.Close()
	ojwk, _ := json.Marshal(other.Key)
	_ = os.WriteFile(f.path("other.jwk"), ojwk, 0o644)
	r = cli(t, noColor, false, "verify", f.path("full.jsonl"), "--keys", keys, "--timestamps", f.path("timestamps.json"), "--scitt-key", f.path("other.jwk"))
	if r.code != 1 || !strings.Contains(r.stdout, "signed by a service key you didn't pass") {
		t.Fatalf("other key: %d %s", r.code, r.stdout)
	}
}

func TestVerifyBundleTimestamps(t *testing.T) {
	f := newStampFixture(t)
	file := stamp.NewFile(f.chain.Org)
	_ = file.Add(f.entry)
	ts, _ := file.Marshal()
	p := f.path("stamped.tar.gz")
	writeStampedBundle(t, p, f.chain, 20, ts)
	r := cli(t, noColor, false, "verify", p, "--keys", keys, "--tsa-roots", f.path("roots.pem"), "--scitt-key", f.path("service.jwk"))
	want := "✓ 20 deeds · chain intact · 2 seal keys · 2026-10-02 → 2026-10-02\n" +
		"✓ Bundle manifest matches · sealed by sk_01m3xxf0…\n" +
		"✓ Deed 20 timestamped 2026-10-20 09:30:00 UTC by 2 TSAs and 1 transparency service · matches the chain\n"
	if r.code != 0 || !strings.HasPrefix(r.stdout, want) || strings.Contains(r.stdout, "TSA chains") {
		t.Fatalf("%d\n%s%s", r.code, r.stdout, r.stderr)
	}
	// A bundle cut short and exported again carries a fresh timestamp of its new head, but the
	// timestamp kept from before shows what is missing.
	writeBundle(t, f.path("cut.tar.gz"), f.chain, 1, 17, deedtest.TestKey("b").Signer, nil)
	r = cli(t, noColor, false, "verify", f.path("cut.tar.gz"), "--keys", keys, "--timestamps", f.path("timestamps.json"))
	if r.code != 1 || !strings.HasPrefix(r.stdout, "✕ Deeds after 17 are missing") {
		t.Fatalf("cut bundle: %d %s", r.code, r.stdout)
	}
}

var updateTimestamps = flag.Bool("update-timestamps", false, "rewrite testdata/timestamps (then update testdata/SHA256SUMS)")

// TestTimestampFixture keeps testdata/timestamps verifying: a 20-record bundle whose head is
// timestamped by two fake TSAs and registered with a fake transparency service (test keys only).
// The independent verifiers read the same bundle (verifiers/crosscheck.py). Regenerate with
// go test ./cmd/deedglass -run TestTimestampFixture -update-timestamps.
func TestTimestampFixture(t *testing.T) {
	const dir = "../../testdata/timestamps/"
	if *updateTimestamps {
		f := newStampFixture(t)
		file := stamp.NewFile(f.chain.Org)
		_ = file.Add(f.entry)
		ts, err := file.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		writeStampedBundle(t, dir+"stamped.tar.gz", f.chain, 20, ts)
		writeBundle(t, dir+"cut.tar.gz", f.chain, 1, 17, deedtest.TestKey("b").Signer, nil)
		for _, n := range []string{"timestamps.json", "roots.pem", "service.jwk"} {
			b, _ := os.ReadFile(f.path(n))
			out := map[string]string{"timestamps.json": "timestamps.json", "roots.pem": "tsa-roots.pem", "service.jwk": "scitt-key.jwk"}[n]
			if err := os.WriteFile(dir+out, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	pins := []string{"--keys", keys, "--tsa-roots", dir + "tsa-roots.pem", "--scitt-key", dir + "scitt-key.jwk"}
	r := cli(t, noColor, false, append([]string{"verify", dir + "stamped.tar.gz"}, pins...)...)
	if r.code != 0 || !strings.Contains(r.stdout, "✓ Deed 20 timestamped 2026-10-20 09:30:00 UTC by 2 TSAs and 1 transparency service · matches the chain\n") {
		t.Fatalf("stamped: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = cli(t, noColor, false, append([]string{"verify", dir + "cut.tar.gz", "--timestamps", dir + "timestamps.json"}, pins...)...)
	if r.code != 1 || !strings.HasPrefix(r.stdout, "✕ Deeds after 17 are missing: deed 20 was timestamped at 2026-10-20 09:30:00 UTC.\n") {
		t.Fatalf("cut: %d\n%s", r.code, r.stdout)
	}
}

func writeStampedBundle(t *testing.T, path string, c *deedtest.Chain, n int, ts []byte) {
	t.Helper()
	ksJSON, _ := deed.StrictCanonical(deedtest.KeySet())
	var size int64
	for _, l := range c.Lines[:n] {
		size += int64(len(l)) + 1
	}
	h := archive.Header{Org: c.Org, Issuer: "https://api.deedglass.example", CreatedAt: deedtest.T0.Add(time.Hour),
		Generator: archive.Generator{Name: "deedglass", Version: "0.1.0-dev", Commit: "4f2c1a9"},
		FirstSeq:  1, LastSeq: int64(n), From: c.Deeds[0].Body.IssuedAt, To: c.Deeds[n-1].Body.IssuedAt, Timestamps: ts}
	var out bytes.Buffer
	w, err := archive.NewWriter(&out, h, ksJSON, size, int64(n))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range c.Lines[:n] {
		if err := w.WriteDeed(l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Close(deedtest.TestKey("b").Signer); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
