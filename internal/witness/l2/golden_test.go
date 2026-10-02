package l2

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/witness"
)

var update = flag.Bool("update", false, "rewrite the goldens in testdata/")

// ansi is a Styler with the CLI's codes (cmd/deedglass/internal/ui), for the TTY goldens.
type ansi struct{}

func (ansi) Teal(s string) string  { return "\x1b[36m" + s + "\x1b[0m" }
func (ansi) Red(s string) string   { return "\x1b[31m" + s + "\x1b[0m" }
func (ansi) Amber(s string) string { return "\x1b[33m" + s + "\x1b[0m" }
func (ansi) Dim(s string) string   { return "\x1b[2m" + s + "\x1b[0m" }

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./internal/witness/l2 -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// detailOf is the whole witness_detail for an L2 result, as the Gate builds it.
func detailOf(t *testing.T, r Result) witness.Detail {
	t.Helper()
	d := decideHero(t, r, witness.L2)
	out, err := witness.FinishWith(d, map[witness.Level]string{witness.L1: "postgres.row", witness.L2: Method},
		witness.Extras{L2: r.Evidence}, time.Date(2026, 10, 7, 13, 12, 9, 331e6, time.UTC), 0)
	if err != nil {
		t.Fatal(err)
	}
	var back witness.Detail
	if err := json.Unmarshal(out.JSON, &back); err != nil {
		t.Fatal(err)
	}
	return back
}

func render(t *testing.T, r Result, o RenderOptions) []byte {
	t.Helper()
	d := detailOf(t, r)
	lines := append([]string{"witness  " + string(d.Claim.Level) + " · " + string(d.Claim.Verdict) + " · " + d.Summary}, Render(d, o)...)
	return []byte(strings.Join(lines, "\n") + "\n")
}

func TestRenderGoldens(t *testing.T) {
	hero := Evaluate(heroInput(heroFrame()))
	golden(t, "render_hero_80.golden", render(t, hero, RenderOptions{Width: 80, Style: ansi{}, Indent: "  "}))
	golden(t, "render_hero_120.golden", render(t, hero, RenderOptions{Width: 120, Style: ansi{}, Indent: "  "}))
	golden(t, "render_hero_nocolor.golden", render(t, hero, RenderOptions{Width: 120, Indent: "  "}))
	clean := Evaluate(heroInput(frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}}))))
	golden(t, "render_clean.golden", render(t, clean, RenderOptions{Width: 120, Indent: "  "}))
	// A sensitive column: the customer's email, changed by the statement.
	sens := frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}}))
	sens.Changes[0].Old["status"], sens.Changes[0].New["status"] = Value{D: "sha256:aa"}, Value{D: "sha256:bb"}
	golden(t, "render_redacted.golden", render(t, Evaluate(heroInput(sens)), RenderOptions{Width: 120, Indent: "  "}))
	// A trigger that touches six columns: four shown, two counted.
	many := frame(upd(1, 42, map[string][2]string{"status": {"paid", "shipped"}}))
	wide := map[string][2]string{"status": {"paid", "shipped"}, "customer_id": {"7", "9"}, "total_cents": {"1200", "0"},
		"updated_at": {"2026-10-01 09:00:00+00", "2026-10-07 13:12:09+00"}}
	many.Changes[0] = upd(1, 42, wide)
	many.Tables["public.orders"] = TableInfo{RelID: 16400, PK: []string{"id"}, Identity: "f",
		Columns:  []string{"id", "customer_id", "status", "total_cents", "updated_at", "note", "flag"},
		TypeOIDs: []uint32{oidInt4, oidInt4, oidText, oidInt4, 1184, oidText, oidBool}}
	many.Changes[0].Old["note"], many.Changes[0].New["note"] = txt(""), txt("bulk")
	many.Changes[0].Old["flag"], many.Changes[0].New["flag"] = txt("f"), txt("t")
	many.Changes[0].Changed = []string{"customer_id", "status", "total_cents", "updated_at", "note", "flag"}
	golden(t, "render_many_columns.golden", render(t, Evaluate(heroInput(many)), RenderOptions{Width: 120, Indent: "  "}))
	// The hero's witness_detail as JSON (S21 §3.7).
	b, err := json.MarshalIndent(detailOf(t, hero).Levels.L2, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "detail_hero.json", append(b, '\n'))
}

// TestRenderNoColorIsPlain: the NO_COLOR golden holds no escape codes, and every line of the
// 80-column one fits after the colour codes are removed.
func TestRenderNoColorIsPlain(t *testing.T) {
	hero := Evaluate(heroInput(heroFrame()))
	if bytes.Contains(render(t, hero, RenderOptions{Width: 120, Indent: "  "}), []byte("\x1b[")) {
		t.Fatal("escape codes without a Styler")
	}
	for _, l := range strings.Split(string(render(t, hero, RenderOptions{Width: 80, Indent: "  "})), "\n")[1:] {
		if n := len([]rune(l)); n > 80 {
			t.Errorf("%d columns: %s", n, l)
		}
	}
}
