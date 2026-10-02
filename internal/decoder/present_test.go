package decoder

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The Console copy follows the brand: calm, a period, no exclamation marks, no em dashes, no banned
// word.
func TestConsoleCopy(t *testing.T) {
	b, err := os.ReadFile("../../scripts/banned_words.txt")
	if err != nil {
		t.Fatal(err)
	}
	var banned []*regexp.Regexp
	for _, w := range strings.Split(string(b), "\n") {
		w = strings.TrimSpace(w)
		if w == "" || strings.HasPrefix(w, "#") {
			continue
		}
		banned = append(banned, regexp.MustCompile(`(?i)(^|[^a-z0-9_])`+regexp.QuoteMeta(w)+`($|[^a-z0-9_])`))
	}
	all := []string{PanelNotWitnessed, RowLine("healthy", "3.9 kB", "4s ago"), AlertToast("forged_marker", "postgres"),
		AlertToast("forged_marker_nontx", "postgres"), AlertToast("foreign_ledger", "postgres")}
	if got := RowLine("healthy", "3.9 kB", "4s ago"); got != "Witness · healthy · 3.9 kB behind · last commit 4s ago" {
		t.Fatalf("%q", got)
	}
	if got := AlertToast("forged_marker", "postgres"); got != "Forged marker on postgres: a transaction claimed to be a Deedglass action without a ledger row. Nothing was bound to it." {
		t.Fatalf("%q", got)
	}
	for _, s := range all {
		if strings.ContainsAny(s, "!—") {
			t.Errorf("%q", s)
		}
		for _, re := range banned {
			if re.MatchString(s) {
				t.Errorf("%q has a banned word (%s)", s, re)
			}
		}
	}
}
