package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/deed/stamp"
	"github.com/deedglass/deedglass/internal/deed/stamp/stamptest"
)

func TestStamperForAndStampOnce(t *testing.T) {
	saved := stampTransport
	stampTransport = http.DefaultTransport // loopback fakes; production goes through the egress door
	t.Cleanup(func() { stampTransport = saved })

	if stamperFor(&config.Config{}, nil, nil) != nil {
		t.Fatal("a stamper with nothing configured")
	}
	tsa1, tsa2, svc := stamptest.NewTSA("one"), stamptest.NewTSA("two"), stamptest.NewService()
	defer tsa1.Close()
	defer tsa2.Close()
	defer svc.Close()
	dir := t.TempDir()
	jwk, _ := json.Marshal(svc.Key)
	keyPath := filepath.Join(dir, "svc.jwk")
	_ = os.WriteFile(keyPath, jwk, 0o600)
	base, _ := url.Parse("https://api.deedglass.example")
	cfg := &config.Config{BaseURL: base, Timestamps: config.TimestampSettings{TSAURLs: []string{tsa1.URL, tsa2.URL}, MinTSA: 2,
		SCITTURL: svc.URL, SCITTKey: keyPath, AtExport: true, Timeout: 5 * time.Second}}
	st := stamperFor(cfg, deedtest.TestKey("b").Signer, nil)
	if st == nil {
		t.Fatal("no stamper")
	}
	c := deedtest.NewChain(3, deedtest.ChainOpts{})
	head := func(i int) stamp.Head {
		d := c.Deeds[i-1]
		return stamp.NewHead(c.Org, d.Body.Seq, d.Body.DeedID, d.BodyHash, d.Body.IssuedAt)
	}
	var out, errb bytes.Buffer
	e := &Env{Stdout: &out, Stderr: &errb}
	path := filepath.Join(dir, "timestamps.json")
	for _, i := range []int{2, 3} {
		if err := stampOnce(context.Background(), e, st, head(i), path); err != nil {
			t.Fatalf("%v %s", err, errb.String())
		}
	}
	if !strings.Contains(out.String(), "✓ Record 3 timestamped by 2 TSAs and 1 transparency service · "+path) {
		t.Fatalf("%q", out.String())
	}
	raw, _ := os.ReadFile(path)
	f, err := stamp.ParseFile(raw, stamp.MaxFileBytes)
	if err != nil || len(f.Heads) != 2 || f.Heads[1].Head.Seq != 3 || len(f.Heads[1].SCITT) != 1 {
		t.Fatalf("%+v %v", f, err)
	}
	if _, probs, _ := stamp.Check(f, stamp.CheckOptions{Keys: deedtest.KeySet()}); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}
	// Another org's file is refused; a failing TSA fails the stamp and leaves the file as it was.
	other := deedtest.NewChain(1, deedtest.ChainOpts{Org: deedtest.Org(3)})
	d := other.Deeds[0]
	if err := stampOnce(context.Background(), e, st, stamp.NewHead(other.Org, 1, d.Body.DeedID, d.BodyHash, d.Body.IssuedAt), path); err == nil {
		t.Fatal("another org's head went into the file")
	}
	tsa2.SetMode(stamptest.TSARefuse)
	errb.Reset()
	if err := stampOnce(context.Background(), e, st, head(1), path); err == nil || !strings.Contains(errb.String(), "Record 1 was not timestamped: ") {
		t.Fatalf("%v %s", err, errb.String())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, raw) {
		t.Fatal("a failed stamp changed the file")
	}
	// A missing service key file fails the stamp, not the process.
	cfg.Timestamps.SCITTKey = filepath.Join(dir, "missing.jwk")
	tsa2.SetMode(stamptest.TSAOK)
	if _, err := stamperFor(cfg, deedtest.TestKey("b").Signer, nil)(context.Background(), head(1)); err == nil {
		t.Fatal("a missing service key was not reported")
	}
}

func TestDeedsTimestampUsage(t *testing.T) {
	for _, args := range [][]string{
		{"deeds", "timestamp", "extra"},
		{"deeds", "timestamp", "--every", "30s"},
		{"deeds", "timestamp", "--every", "15m", "--seq", "3"},
	} {
		if r := cli(t, noColor, false, args...); r.code != 2 {
			t.Fatalf("%v: %d %s%s", args, r.code, r.stdout, r.stderr)
		}
	}
}
