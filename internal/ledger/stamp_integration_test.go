//go:build integration

package ledger_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/archive"
	"github.com/deedglass/deedglass/internal/deed/stamp"
	"github.com/deedglass/deedglass/internal/deed/stamp/fetch"
	"github.com/deedglass/deedglass/internal/deed/stamp/stamptest"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
)

type stampReport struct {
	bytes.Buffer
	tried bool
	err   error
}

func (s *stampReport) ExportStamp(err error) { s.tried, s.err = true, err }

// TY-03: with a stamper, Export timestamps its last deed and carries timestamps.json; the head
// matches the deed; a TSA that fails leaves the export without it (never without records).
func TestExportTimestamps(t *testing.T) {
	tsa1, tsa2 := stamptest.NewTSA("one"), stamptest.NewTSA("two")
	defer tsa1.Close()
	defer tsa2.Close()
	cfg := &fetch.Config{TSAs: []string{tsa1.URL, tsa2.URL}, MinTSA: 2, Client: tsa1.Client()}
	stamper := func(ctx context.Context, h stamp.Head) (stamp.Entry, error) { return fetch.Stamp(ctx, cfg, h) }
	pinned := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	en := newEnv(t, ledger.WithClock(func() time.Time { return pinned }), ledger.WithStamper(stamper))
	en.run(t, en.org, en.agents, 7, 30, fake.Allow, fake.Deny)

	h, err := en.led.Head(ctx, en.org, 0)
	if err != nil || h.Seq < 30 {
		t.Fatalf("head %+v %v", h, err)
	}
	if _, err := en.led.Head(ctx, en.org, h.Seq+1); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("a missing seq: %v", err)
	}

	var out stampReport
	m, err := en.led.Export(ctx, en.org, ledger.Range{}, &out)
	if err != nil || !out.tried || out.err != nil {
		t.Fatalf("export: %v (stamp tried %v, %v)", err, out.tried, out.err)
	}
	if _, ok := m.Files[archive.NameTimestamps]; !ok {
		t.Fatal("no timestamps.json in the manifest")
	}
	rep, _ := verifyBundle(t, en, out.Bytes())
	if rep.Result != deed.ResultValid {
		t.Fatalf("%+v", rep)
	}
	r, err := archive.Open(bytes.NewReader(out.Bytes()), archive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	f, err := stamp.ParseFile(r.Timestamps(), stamp.MaxBundleBytes)
	if err != nil || len(f.Heads) != 1 || f.Heads[0].Head.Seq != m.Range.LastSeq || f.Heads[0].Head.BodyHash != m.Chain.Head.BodyHash ||
		f.Heads[0].Head.DeedID != m.Chain.Head.DeedID || len(f.Heads[0].RFC3161) != 2 {
		t.Fatalf("timestamps %+v %v", f, err)
	}
	if _, probs, _ := stamp.Check(f, stamp.CheckOptions{}); len(probs) != 0 {
		t.Fatalf("%+v", probs)
	}

	// A TSA down: the export still happens, without timestamps.json, and says why.
	tsa2.SetMode(stamptest.TSARefuse)
	var out2 stampReport
	m, err = en.led.Export(ctx, en.org, ledger.Range{}, &out2)
	if err != nil || !out2.tried || out2.err == nil {
		t.Fatalf("export with a TSA down: %v (%v)", err, out2.err)
	}
	if _, ok := m.Files[archive.NameTimestamps]; ok {
		t.Fatal("timestamps.json with too few tokens")
	}
	if rep, _ := verifyBundle(t, en, out2.Bytes()); rep.Result != deed.ResultValid {
		t.Fatalf("%+v", rep)
	}
}
