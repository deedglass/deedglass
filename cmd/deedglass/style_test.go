package main

import (
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/cli/ui"
)

// CLI_STYLE.md: on a terminal with NO_COLOR, and through a pipe, the commands print no escape code,
// and no line they lay out is wider than 80 columns. With DEEDGLASS_ASCII the glyphs are ASCII.
func TestStyleNoColorAnd80Columns(t *testing.T) {
	for _, args := range [][]string{
		{"play", "--fast"},
		{"--help"},
		{"help", "keys"},
		{"verify", "testdata/no-such-file.jsonl"},
		{"actoins", "list"},
	} {
		for _, c := range []struct {
			name string
			env  map[string]string
			tty  bool
		}{
			{"NO_COLOR terminal", map[string]string{"NO_COLOR": "1", "DEEDGLASS_NO_BANNER": "1"}, true},
			{"pipe", nil, false},
		} {
			r := cli(t, c.env, c.tty, args...)
			out := r.stdout + r.stderr
			if strings.Contains(out, "\x1b") {
				t.Errorf("%v (%s): escape codes:\n%q", args, c.name, out)
			}
			for _, l := range strings.Split(out, "\n") {
				if ui.Width(l) > 80 && !strings.Contains(l, "no-such-file") {
					t.Errorf("%v (%s): %d columns: %q", args, c.name, ui.Width(l), l)
				}
			}
		}
	}
	r := cli(t, map[string]string{"DEEDGLASS_ASCII": "1", "NO_COLOR": "1"}, true, "play", "--fast")
	if strings.ContainsAny(r.stdout, "✓✕◐○•→…") || !strings.Contains(r.stdout, "+ allowed by allow-order-reads") {
		t.Errorf("DEEDGLASS_ASCII:\n%s", r.stdout)
	}
	if j := cli(t, map[string]string{"DEEDGLASS_ASCII": "1"}, true, "--json", "play", "--fast"); strings.Contains(j.stdout, "\x1b") || !strings.Contains(j.stdout, `"object": "play_result"`) {
		t.Errorf("--json is never rewritten:\n%s", j.stdout)
	}
}
