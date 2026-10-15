package play

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The embedded recording is a byte-for-byte copy of the README's recording.
func TestCastMatchesDocsAsset(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "docs", "assets", "demo-hero.cast.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, castJSON) {
		t.Fatal("internal/play/hero.cast.json differs from docs/assets/demo-hero.cast.json; copy it: cp docs/assets/demo-hero.cast.json internal/play/hero.cast.json")
	}
	c, err := LoadCast()
	if err != nil {
		t.Fatal(err)
	}
	if c.Commit == "" || c.Command != "make demo-hero" || c.RecordedAt == "" {
		t.Fatalf("recording identity %+v", c)
	}
}

// The undo lines are exactly what make demo printed (launch.golden, compared on every demo run).
func TestUndoReplayInGolden(t *testing.T) {
	g, err := os.ReadFile(filepath.Join("..", "..", "examples", "demo", "testdata", "launch.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(g), strings.Join(UndoReplay, "\n")+"\n") {
		t.Fatal("UndoReplay is not a contiguous block of examples/demo/testdata/launch.golden")
	}
}

func runPlay(t *testing.T) (string, *Result) {
	t.Helper()
	var out bytes.Buffer
	r, err := Run(context.Background(), Options{Out: &out, Now: func() time.Time { return time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	return out.String(), r
}

func TestRunLiveParts(t *testing.T) {
	out, r := runPlay(t)
	want := map[string]Decision{
		"read": {Outcome: "allow", Rule: "allow-order-reads"},
		"drop": {Outcome: "deny", Rule: "block-destructive-sql"},
		"ship": {Outcome: "allow", Rule: "allow-order-status-updates"},
		"undo": {Outcome: "require_approval", Rule: "undo-needs-approval"},
	}
	for k, w := range want {
		g := r.Decisions[k]
		if g.Outcome != w.Outcome || g.Rule != w.Rule {
			t.Errorf("%s: got %s/%s, want %s/%s", k, g.Outcome, g.Rule, w.Outcome, w.Rule)
		}
	}
	if len(r.Checks) != 2 || r.Checks["ship"] != "L2 MISMATCH" || r.Checks["undo"] != "L2 VERIFIED" {
		t.Errorf("record checks %v (want the replayed run's: ship L2 MISMATCH, undo L2 VERIFIED)", r.Checks)
	}
	if r.Records != 4 || !r.ChainOK || !r.TamperSeen || r.TamperSeq != 1 {
		t.Fatalf("records %+v", r)
	}
	for _, s := range []string{
		"Deedglass play · offline · no Docker · nothing leaves this machine",
		`✕ denied · Blocked by policy "block-destructive-sql": DROP is never allowed for agents.  [live]`,
		"✓ allowed · rule allow-order-status-updates · check Proven (L2)  [live]",
		"◐ needs a person · Waiting for approval under policy \"undo-needs-approval\"",
		"✓ 4 deeds · chain intact · 1 seal key  [live]",
		"· one byte changed in deed 1",
		"Deed 1 was changed after it was sealed.",
		"npx deedglass scan",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q\n%s", s, out)
		}
	}
}

func TestRunReplayIsLabelled(t *testing.T) {
	out, r := runPlay(t)
	if !strings.HasPrefix(r.Recording, "make demo-hero · commit ") {
		t.Fatalf("recording %q", r.Recording)
	}
	// Every replayed witness line is indented under a "│" and preceded by a [replay] label.
	if n := strings.Count(out, "[replay]"); n < 4 {
		t.Fatalf("%d [replay] labels\n%s", n, out)
	}
	for _, s := range []string{
		"│ ✕ executed · postgres.execute · 11 ms",
		"│   check    Proven · Doesn't match · Postgres changed 301 rows in public.orders. The agent reported 1.",
		"│   check    Proven · Matches · Postgres committed 1 row in public.orders, as the statement said.",
		"more recorded rows not shown here",
		"│ ✓ L2 · MISMATCH · 301 rows",
		"✓ countersigned by ops@example.test",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q", s)
		}
	}
	// A replayed line never claims to be live.
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "│") && strings.Contains(l, "[live]") {
			t.Errorf("replayed line marked live: %q", l)
		}
	}
	// Every recorded line in the printed sections came from the recording, unchanged.
	c, _ := LoadCast()
	recorded := map[string]bool{}
	for _, l := range c.Lines {
		recorded[l.Text] = true
	}
	for _, l := range UndoReplay {
		recorded[l] = true
	}
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "  │ ") {
			continue
		}
		if !recorded[strings.TrimPrefix(l, "  │ ")] {
			t.Errorf("replayed line not in the recording: %q", l)
		}
	}
}

func TestRunNoPauseWithoutPace(t *testing.T) {
	start := time.Now()
	runPlay(t)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s without a pace function", d)
	}
}

func TestRunPacesAndStops(t *testing.T) {
	var total time.Duration
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	_, err := Run(ctx, Options{Out: &out, Pace: func(ctx context.Context, d time.Duration) error {
		total += d
		calls++
		if calls == 20 {
			cancel()
		}
		return ctx.Err()
	}})
	if err == nil || calls != 20 || total <= 0 {
		t.Fatalf("err %v calls %d total %s", err, calls, total)
	}
}

func TestRunColour(t *testing.T) {
	var out bytes.Buffer
	red := func(s string) string { return "<r>" + s + "</r>" }
	if _, err := Run(context.Background(), Options{Out: &out, Style: Style{Red: red}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "<r>✕</r> denied") {
		t.Fatal("deny mark not coloured")
	}
}

func TestTamperChangesOneByte(t *testing.T) {
	in := []sealedLine{{1, []byte(`{"a":1,"latency_ms":17}`)}, {2, []byte(`{"latency_ms":9}`)}}
	got, seq, ok := tamper(in)
	if !ok || seq != 1 || string(got) != "{\"a\":1,\"latency_ms\":18}\n{\"latency_ms\":9}\n" {
		t.Fatalf("%q %d %v", got, seq, ok)
	}
	if _, _, ok := tamper([]sealedLine{{1, []byte(`{}`)}}); ok {
		t.Fatal("tampered a record with no latency")
	}
}
