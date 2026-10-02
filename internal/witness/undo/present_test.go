package undo

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSummaryAndHeadline(t *testing.T) {
	_, p := basePlan(t, defaultCatalog())
	if got := Summary(p, actID); got != "Put back 3 rows in public.order_items and public.orders to the values Postgres recorded before act_01m3x71p…" {
		t.Errorf("Summary = %q", got)
	}
	if got := Summary(nil, actID); got != "Nothing to put back for act_01m3x71p…" {
		t.Errorf("Summary(nil) = %q", got)
	}
	if got := Summary(&Plan{}, "act_x"); got != "Nothing to put back for act_x." {
		t.Errorf("Summary(empty) = %q", got)
	}
	if got := Headline(p); got != "3 rows in public.order_items and public.orders · 1 update · 1 insert · 1 delete" {
		t.Errorf("Headline = %q", got)
	}
	if got := Headline(nil); got != "" {
		t.Errorf("Headline(nil) = %q", got)
	}
	big := &Plan{Rows: 1301, Counts: Counts{Update: 1300, Delete: 1}, Tables: []PlanTable{{Schema: "public", Name: "orders"}}}
	if got := Headline(big); got != "1,301 rows in public.orders · 1,300 updates · 1 delete" {
		t.Errorf("Headline(big) = %q", got)
	}
	one := &Plan{Rows: 1, Counts: Counts{Insert: 2}, Tables: []PlanTable{{Schema: "a", Name: "x"}, {Schema: "a", Name: "y"}, {Schema: "a", Name: "z"}}}
	if got := Headline(one); got != "1 row in a.x, a.y and a.z · 2 inserts" {
		t.Errorf("Headline(one) = %q", got)
	}
	if got := Headline(&Plan{}); got != "0 rows in no table" {
		t.Errorf("Headline(empty) = %q", got)
	}
}

func TestApprovalAndResult(t *testing.T) {
	_, p := basePlan(t, defaultCatalog())
	tests := []struct{ got, want string }{
		{ApprovalSummary("console", "Arjun", p, actID),
			"console (for Arjun) wants to put back 3 rows in public.order_items and public.orders, undoing act_01m3x71p…"},
		{ApprovalSummary("console", "", p, actID), "console wants to put back 3 rows in public.order_items and public.orders, undoing act_01m3x71p…"},
		{ApprovalSummary("console", "Arjun", nil, actID), "console (for Arjun) wants to undo act_01m3x71p…"},
		{ResultText(301, []string{"public.orders"}), "301 rows restored in public.orders"},
		{ResultText(1, []string{"public.orders", "public.order_items"}), "1 row restored in public.orders and public.order_items"},
		{WitnessText(1301, []string{"public.orders"}), "Restored 1,301 rows in public.orders exactly as planned."},
		{MsgUnavailable(actID, []string{"triggers"}),
			"Undo isn't available for act_01m3x71p…: 1 check failed (triggers). Run deedglass undo preview " + actID + " for the fix."},
		{MsgUnavailable(actID, []string{"age", "unchanged"}),
			"Undo isn't available for act_01m3x71p…: 2 checks failed (age, unchanged). Run deedglass undo preview " + actID + " for the fix."},
		{MsgAlreadyUndone(actID, "act_01j9zx8kqqqq", "10:14 IST"), "act_01m3x71p… was already undone by act_01j9zx8k… at 10:14 IST."},
		{MsgAlreadyWaiting(actID, "act_01j9zx8kqqqq"), "An undo of act_01m3x71p… is already waiting for approval: act_01j9zx8k…"},
		{KeyText(Key{"order_id": "7", "line": "2"}, []string{"order_id", "line"}), "order_id=7 line=2"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("got  %q\nwant %q", tc.got, tc.want)
		}
	}
}

func TestShortID(t *testing.T) {
	for in, want := range map[string]string{
		"act_01m3x71p6wesj7xewec6bwbwxm": "act_01m3x71p…",
		"act_01m3x71p":                   "act_01m3x71p",
		"act_short":                      "act_short",
		"noprefix0123456789":             "noprefix0123456789",
		"":                               "",
	} {
		if got := ShortID(in); got != want {
			t.Errorf("ShortID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestThousands(t *testing.T) {
	for in, want := range map[int]string{0: "0", 7: "7", 999: "999", 1000: "1,000", 1204: "1,204", 12345: "12,345", 1234567: "1,234,567",
		-1: "-1", -1000: "-1,000", -123456: "-123,456"} {
		if got := Thousands(in); got != want {
			t.Errorf("Thousands(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDurationWords(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0 s"}, {59 * time.Second, "59 s"}, {time.Minute, "1 min"}, {6*time.Minute + 30*time.Second, "6 min"}, {59 * time.Minute, "59 min"},
		{time.Hour, "1 hour"}, {90 * time.Minute, "1 hour"}, {2 * time.Hour, "2 hours"}, {47 * time.Hour, "47 hours"},
		{48 * time.Hour, "2 days"}, {7 * 24 * time.Hour, "7 days"}, {9*24*time.Hour + 5*time.Hour, "9 days"},
	} {
		if got := DurationWords(tc.d); got != tc.want {
			t.Errorf("DurationWords(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestProblemSentences(t *testing.T) {
	tests := []struct{ got, want string }{
		{ProblemNotWitnessedAt("").Text, ProblemNoFrame.Text},
		{ProblemNotWitnessedAt("L0").Text, "Undo needs L2 evidence from Postgres. This action was witnessed at L0, so Deedglass doesn't know every row it changed."},
		{ProblemNotBound("forged_marker").Text, "Postgres's change log shows a marker without a ledger row for this action, so it can't be undone from that evidence."},
		{ProblemNotBound("marker_count").Text, "Postgres's change log shows more than one marker or ledger row for this action, so it can't be undone from that evidence."},
		{ProblemNotBound("marker_mismatch").Text, "Postgres's change log shows a marker and a ledger row that disagree for this action, so it can't be undone from that evidence."},
		{ProblemNotBound("publication_gap").Text, "Postgres's change log shows a change log with a gap for this action, so it can't be undone from that evidence."},
		{ProblemNotBound("something_new").Text, "Postgres's change log shows a transaction Deedglass can't attribute for this action, so it can't be undone from that evidence."},
		{ProblemManyFrames(3).Text, "3 transactions name this action, so Deedglass can't tell which one to undo."},
		{ProblemImagesTruncated(12000, 1000).Text, "The transaction changed 12,000 rows and Deedglass kept the images of 1,000, so it can't put every row back."},
		{ProblemOldImage("public.orders", "n").FixSQL, "ALTER TABLE public.orders REPLICA IDENTITY FULL;"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("got  %q\nwant %q", tc.got, tc.want)
		}
	}
	for _, id := range CheckOrder {
		if CheckTitles[id] == "" || cliLabels[id] == "" {
			t.Errorf("check %s has no title or CLI label", id)
		}
	}
	if len(CheckOrder) != 12 || len(CheckTitles) != 12 || len(cliLabels) != 12 {
		t.Errorf("%d checks, %d titles, %d labels", len(CheckOrder), len(CheckTitles), len(cliLabels))
	}
}

// TestCopyRules reads every string literal in the files that hold user-visible copy (present.go,
// checks.go, plan.go) and refuses an em dash, an exclamation mark, and the words "rollback" and
// "revert" (S23 §5: undo, put back, restore; a rollback is what Postgres does to a failed
// transaction).
func TestCopyRules(t *testing.T) {
	fset := token.NewFileSet()
	n := 0
	for _, file := range []string{"present.go", "checks.go", "plan.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: %v", fset.Position(lit.Pos()), err)
			}
			n++
			lower := strings.ToLower(s)
			switch {
			case strings.ContainsRune(s, '—'):
				t.Errorf("%s: an em dash in %q", fset.Position(lit.Pos()), s)
			case strings.Contains(s, "!"):
				t.Errorf("%s: an exclamation mark in %q", fset.Position(lit.Pos()), s)
			case strings.Contains(lower, "rollback"), strings.Contains(lower, "revert"):
				t.Errorf("%s: %q says rollback or revert", fset.Position(lit.Pos()), s)
			}
			return true
		})
	}
	if n < 100 {
		t.Fatalf("only %d string literals read; the scan is broken", n)
	}
}
