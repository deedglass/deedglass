package caught

import (
	"strings"
	"testing"
	"time"
)

func ip(n int) *int { return &n }

var t0 = time.Date(2026, 10, 17, 14, 2, 0, 0, time.UTC)

func TestSessionDetectors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []Frame
		kinds  []string
		lines  []string
		undo   []bool
	}{
		{name: "an ordinary session: no banner", frames: []Frame{
			{ID: "dtx_1", Counts: map[string]Counts{"public.orders": {Update: 3}, "public.order_events": {Insert: 3}}},
			{ID: "dtx_2", Counts: map[string]Counts{"public.orders": {Insert: 1}}},
		}},
		{name: "the hero: 301 rows in one transaction", frames: []Frame{
			{ID: "dtx_1", Counts: map[string]Counts{"public.orders": {Update: 301}, "public.order_events": {Insert: 99}, "deedglass.ledger": {Insert: 500}}},
		}, kinds: []string{KindManyRows}, lines: []string{"Deedglass caught this: one transaction changed 301 rows in public.orders."}, undo: []bool{true}},
		{name: "a large delete", frames: []Frame{{ID: "dtx_1", Counts: map[string]Counts{"public.orders": {Delete: 1200}}}},
			kinds: []string{KindDeleteRows}, lines: []string{"Deedglass caught this: one transaction deleted 1,200 rows in public.orders."}, undo: []bool{true}},
		{name: "a truncate can't be undone from the record", frames: []Frame{{ID: "dtx_1", Counts: map[string]Counts{"public.order_events": {Truncate: 1}}}},
			kinds: []string{KindTruncate}, lines: []string{"Deedglass caught this: one transaction truncated public.order_events."}, undo: []bool{false}},
		{name: "just below the threshold", frames: []Frame{{ID: "dtx_1", Counts: map[string]Counts{"public.orders": {Update: 99}}}}},
		{name: "spread over transactions is not one transaction", frames: []Frame{
			{ID: "dtx_1", Counts: map[string]Counts{"public.orders": {Update: 60}}},
			{ID: "dtx_2", Counts: map[string]Counts{"public.orders": {Update: 60}}},
		}},
		{name: "an excluded transaction still shows", frames: []Frame{{ID: "dtx_1", Excluded: true, Counts: map[string]Counts{"public.orders": {Insert: 100}}}},
			kinds: []string{KindManyRows}, lines: []string{"Deedglass caught this: one transaction changed 100 rows in public.orders."}, undo: []bool{true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Session("obs_1", tc.frames, 100, true)
			if len(got) != len(tc.kinds) {
				t.Fatalf("got %d items %v, want %v", len(got), got, tc.kinds)
			}
			for i, it := range got {
				if it.Kind != tc.kinds[i] || it.Line != tc.lines[i] || it.UndoPossible != tc.undo[i] {
					t.Errorf("item %d = %+v", i, it)
				}
				if it.UndoFreePath != "deedglass observe show obs_1 --undo-sql" || it.Session != "obs_1" {
					t.Errorf("item %d free path %q", i, it.UndoFreePath)
				}
			}
		})
	}
	if got := Session("obs_1", []Frame{{ID: "dtx_1", Counts: map[string]Counts{"public.orders": {Update: 1}}}}, 0, false); len(got) != 1 || got[0].Line != Prefix+"one transaction changed 1 row in public.orders." || got[0].UndoPossible {
		t.Fatalf("a threshold below 1 counts as 1: %+v", got)
	}
}

func TestActionDetectors(t *testing.T) {
	acts := []Action{
		{ID: "act_ok", Agent: "support-bot", Tool: "pg.execute", Table: "public.orders", At: t0, Decision: "allow", Executed: true, Verdict: "VERIFIED", Level: "L2", Undoable: true},
		{ID: "act_mm", Agent: "support-bot", Tool: "pg.execute", Table: "public.orders", At: t0.Add(1 * time.Minute), Decision: "allow", Executed: true, Verdict: "MISMATCH", Level: "L2", Undoable: true},
		{ID: "act_l0", Agent: "support-bot", Tool: "rest.call", At: t0.Add(2 * time.Minute), Decision: "allow", Executed: true, Verdict: "MISMATCH", Level: "L0"},
		{ID: "act_drop", Agent: "ops-bot", Tool: "pg.execute", Table: "public.tmp", At: t0.Add(3 * time.Minute), Decision: "require_approval", Approved: true, Executed: true, Destructive: "drop"},
		{ID: "act_trunc_denied", Agent: "ops-bot", Tool: "pg.execute", Table: "public.tmp", At: t0.Add(4 * time.Minute), Decision: "deny", Destructive: "truncate"},
		{ID: "act_more", Agent: "billing-bot", Tool: "pg.execute", Table: "public.invoices", At: t0.Add(5 * time.Minute), Decision: "allow", Executed: true, ReportedRows: ip(1), ObservedRows: ip(40), Undoable: true},
		{ID: "act_fewer", Agent: "billing-bot", Tool: "pg.execute", Table: "public.invoices", At: t0.Add(6 * time.Minute), Decision: "allow", Executed: true, ReportedRows: ip(5), ObservedRows: ip(5)},
		{ID: "act_undone", Agent: "support-bot", Tool: "pg.execute", Table: "public.orders", At: t0.Add(7 * time.Minute), Decision: "allow", Executed: true, Verdict: "VERIFIED", Level: "L2", UndoneBy: "act_undo1", Undoable: true},
		{ID: "act_deny", Agent: "data-bot", Tool: "pg.execute", Table: "public.customers", At: t0.Add(8 * time.Minute), Decision: "deny"},
		{ID: "act_retry", Agent: "data-bot", Tool: "pg.call", Table: "public.customers", At: t0.Add(11 * time.Minute), Decision: "allow", Executed: true, Undoable: true},
		{ID: "act_deny_late", Agent: "late-bot", Tool: "pg.execute", Table: "public.x", At: t0.Add(20 * time.Minute), Decision: "deny"},
		{ID: "act_retry_late", Agent: "late-bot", Tool: "pg.call", Table: "public.x", At: t0.Add(31 * time.Minute), Decision: "allow", Executed: true},
		{ID: "act_deny_same_tool", Agent: "same-bot", Tool: "pg.execute", Table: "public.y", At: t0.Add(40 * time.Minute), Decision: "deny"},
		{ID: "act_same_tool", Agent: "same-bot", Tool: "pg.execute", Table: "public.y", At: t0.Add(41 * time.Minute), Decision: "allow", Executed: true},
	}
	got := Actions(acts)
	want := map[string]string{
		KindDoesntMatch:    "Deedglass caught this: the L2 check of pg.execute doesn't match what support-bot said.",
		KindDestructiveRan: "Deedglass caught this: a DROP of public.tmp ran through pg.execute.",
		KindMoreRows:       "Deedglass caught this: billing-bot reported 1 row; Postgres changed 40 rows.",
		KindUndoneLater:    "Deedglass caught this: a person undid pg.execute by support-bot.",
		KindRetryAfterDeny: "Deedglass caught this: data-bot was refused pg.execute on public.customers, then tried pg.call 3 minutes later.",
	}
	seen := map[string]int{}
	for _, it := range got {
		seen[it.Kind]++
		if w, ok := want[it.Kind]; !ok || it.Line != w {
			t.Errorf("unexpected %s: %q", it.Kind, it.Line)
		}
	}
	for k := range want {
		if seen[k] != 1 {
			t.Errorf("%s seen %d times", k, seen[k])
		}
	}
	// Newest first: the retry (11 min) comes before the mismatch (1 min).
	if got[0].Kind != KindRetryAfterDeny || got[0].UndoFreePath != FreeActionPath("act_retry") {
		t.Fatalf("first item %+v", got[0])
	}
	if n := Newest(got, 3); len(n) != 3 || Newest(got[:2], 3)[1].Line != got[1].Line {
		t.Fatal("Newest")
	}
	for _, it := range got {
		if it.Kind == KindUndoneLater && (it.UndoPossible || len(it.Actions) != 2) {
			t.Errorf("an undone action offers no second undo: %+v", it)
		}
	}
	if Actions(nil) != nil {
		t.Fatal("no actions, no items")
	}
}

func TestDestructiveWords(t *testing.T) {
	for k, w := range map[string]string{"drop": "a DROP of public.t", "truncate": "a TRUNCATE of public.t", "delete_all": "a DELETE without WHERE on public.t"} {
		if got := destructiveWords(k, "public.t"); got != w {
			t.Errorf("%s: %q", k, got)
		}
	}
	if got := destructiveWords("delete_all", ""); got != "a DELETE without WHERE on a table" {
		t.Error(got)
	}
	if minutes(10*time.Second) != "less than a minute" || minutes(time.Minute) != "1 minute" || thousands(-1234) != "-1,234" || thousands(1234567) != "1,234,567" {
		t.Error("words")
	}
}

// TestEveryItemHasFreePath: no caught item is ever without its free way back (S50 §7: "a caught
// banner without the free path" must never happen).
func TestEveryItemHasFreePath(t *testing.T) {
	items := append(Session("obs_9", []Frame{{ID: "dtx_1", Counts: map[string]Counts{"public.a": {Truncate: 1}, "public.b": {Delete: 500}, "public.c": {Update: 500}}}}, 100, false),
		Actions([]Action{{ID: "act_1", Agent: "a", Tool: "t", Table: "public.a", At: t0, Decision: "allow", Executed: true, Destructive: "delete_all"}})...)
	if len(items) != 4 {
		t.Fatalf("%d items", len(items))
	}
	for _, it := range items {
		if !strings.HasPrefix(it.UndoFreePath, "deedglass ") || !strings.HasPrefix(it.Line, Prefix) || !strings.HasSuffix(it.Line, ".") {
			t.Errorf("%+v", it)
		}
		if strings.Contains(it.Line, "—") || it.String() == "" {
			t.Errorf("copy: %q", it.Line)
		}
	}
}
