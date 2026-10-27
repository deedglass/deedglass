package play

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/cli/ui"
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
		"Deedglass play  offline · nothing leaves this machine",
		"rule     ✕ denied by block-destructive-sql",
		"DROP is never allowed for agents.",
		"rule     ✓ allowed by allow-order-status-updates, checked at Proven",
		"rule     ◐ waits for a person: undo-needs-approval",
		"verify   ✓ 4 deeds, chain intact, 1 seal key",
		"tamper   one byte changed in deed 1",
		"Deed 1 was changed after it was sealed.",
		"deedglass start",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q\n%s", s, out)
		}
	}
}

// Every row says where it comes from: the live policy and seal rows end with "live", the rows
// read from the recorded run with "replay", and no replayed row claims to be live.
func TestRunReplayIsLabelled(t *testing.T) {
	out, r := runPlay(t)
	if !strings.HasPrefix(r.Recording, "make demo-hero · commit ") {
		t.Fatalf("recording %q", r.Recording)
	}
	tagged := func(key, tag string) {
		t.Helper()
		n := 0
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimLeft(l, " "), key+" ") {
				n++
				if !strings.HasSuffix(l, " "+tag) {
					t.Errorf("%s row not labelled %s: %q", key, tag, l)
				}
			}
		}
		if n == 0 {
			t.Errorf("no %s row\n%s", key, out)
		}
	}
	tagged("rule", "live")
	tagged("verify", "live")
	tagged("records", "live")
	tagged("before", "replay")
	tagged("check", "replay")
	tagged("plan", "replay")
	tagged("person", "replay")
	for _, s := range []string{
		"check    ✕ Doesn't match: Postgres changed 301 rows in",
		"check    ✓ Matches: Postgres committed 1 row in public.orders",
		"✕ Mismatch at Proven (L2), 301 rows · act_01m4x8kv…",
		"✕ id 218  pending → shipped",
		"… 298 more rows",
		"person   ✓ countersigned by ops@example.test",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q\n%s", s, out)
		}
	}
	if strings.Contains(out, "│") {
		t.Error("the recorded lines print only with Verbose")
	}
}

// With Verbose, the recorded lines follow each replayed scene exactly as they printed.
func TestRunVerboseIsVerbatim(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Run(context.Background(), Options{Out: &buf, Verbose: true}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	c, _ := LoadCast()
	recorded := map[string]bool{}
	for _, l := range c.Lines {
		recorded[l.Text] = true
	}
	for _, l := range UndoReplay {
		recorded[l] = true
	}
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, bodyIndent+"│ ") {
			continue
		}
		n++
		if !recorded[strings.TrimPrefix(l, bodyIndent+"│ ")] {
			t.Errorf("replayed line not in the recording: %q", l)
		}
	}
	for _, s := range []string{"│ ✕ executed · postgres.execute · 11 ms", "│ ✓ L2 · MISMATCH · 301 rows", "│ ✓ countersigned by ops@example.test"} {
		if !strings.Contains(out, s) {
			t.Errorf("verbose output lacks %q", s)
		}
	}
	if n < 20 {
		t.Fatalf("%d recorded lines\n%s", n, out)
	}
}

// At 80 columns and narrower, no line is wider than the terminal; colour changes nothing else.
func TestRunWidths(t *testing.T) {
	for _, w := range []int{0, 60, 80, 120} {
		var plain, colour bytes.Buffer
		if _, err := Run(context.Background(), Options{Out: &plain, Width: w}); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(context.Background(), Options{Out: &colour, Width: w, Style: ui.Style{Color: true}}); err != nil {
			t.Fatal(err)
		}
		limit := ui.LayoutWidth(w)
		for _, l := range strings.Split(plain.String(), "\n") {
			if ui.Width(l) > limit {
				t.Errorf("width %d: %d columns: %q", w, ui.Width(l), l)
			}
		}
		if strings.Contains(plain.String(), "\x1b[") {
			t.Error("plain output has escape codes")
		}
		re := regexp.MustCompile(`sk_[0-9a-z]{8}…`)
		if re.ReplaceAllString(ui.StripANSI(colour.String()), "") != re.ReplaceAllString(plain.String(), "") {
			t.Errorf("width %d: colour changes the text", w)
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
	if _, err := Run(context.Background(), Options{Out: &out, Style: ui.Style{Color: true}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\x1b[31m✕\x1b[0m denied") {
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
