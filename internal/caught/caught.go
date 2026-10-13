// Package caught finds the moments worth a person's attention in what Deedglass already recorded
// (S50 §3.7): "Deedglass caught this". It is pure: it reads counts and facts the caller loaded from
// the Ledger and the decoder's frames, and returns items whose sentences are built from those
// records, never from numbers typed by anyone. It is free: nothing here reads a licence.
//
// Every item names its free way back (per-action Undo, or the undo SQL a person runs), and a view
// that shows an item must show that path too (TestEveryItemHasFreePath, and the CLI and Console
// tests that render items).
package caught

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kinds of item.
const (
	// Watched sessions.
	KindTruncate   = "truncate"    // a TRUNCATE of a watched table
	KindManyRows   = "many_rows"   // one transaction changed at least caught_rows rows in one table
	KindDeleteRows = "delete_rows" // one transaction deleted at least caught_rows rows in one table
	// Gate actions.
	KindDoesntMatch    = "doesnt_match"     // an L1 or L2 check read "Doesn't match"
	KindDestructiveRan = "destructive_ran"  // an allowed or approved DROP, TRUNCATE, or DELETE without WHERE ran
	KindMoreRows       = "more_rows"        // an UPDATE or DELETE changed more rows than the agent reported
	KindRetryAfterDeny = "retry_after_deny" // a deny, then the same agent tried the same table with another tool within 10 minutes
	KindUndoneLater    = "undone_later"     // a person later undid the action
)

// RetryWindow is how soon after a deny another tool on the same table counts as a retry.
const RetryWindow = 10 * time.Minute

// Counts are one table's row changes in one transaction (decoder.OpCounts' shape).
type Counts struct {
	Insert   int `json:"insert"`
	Update   int `json:"update"`
	Delete   int `json:"delete"`
	Truncate int `json:"truncate"`
}

// Rows is every changed row (truncates aside).
func (c Counts) Rows() int { return c.Insert + c.Update + c.Delete }

// Frame is one transaction of a watched session, as stored (no row values).
type Frame struct {
	ID       string
	Counts   map[string]Counts // "schema.table" → counts
	Excluded bool              // left out of the undo by a person; still shown, never hidden
}

// Item is one caught moment.
type Item struct {
	Kind         string   `json:"kind"`
	Session      string   `json:"session,omitempty"`
	Actions      []string `json:"actions,omitempty"`
	Frames       []string `json:"frames,omitempty"`
	Table        string   `json:"table,omitempty"`
	Rows         int      `json:"rows,omitempty"`
	Line         string   `json:"line"`
	UndoPossible bool     `json:"undo_possible"`
	UndoFreePath string   `json:"undo_free_path"`
}

// Prefix starts every item's sentence where a person reads it.
const Prefix = "Deedglass caught this: "

// FreeSessionPath is the free way back for a watched session (S50 §3.5).
func FreeSessionPath(session string) string {
	return "deedglass observe show " + session + " --undo-sql"
}

// FreeActionPath is the free way back for one Gate action (S23's per-action Undo).
func FreeActionPath(action string) string {
	return "deedglass undo preview " + action
}

// Session finds the caught moments of one watched session: every TRUNCATE, and every transaction
// that changed (or deleted) at least caughtRows rows in one table. Excluded transactions still
// count: leaving a transaction out of an undo doesn't change what Postgres committed. undoable is
// whether the session's undo can still be planned (frames present, not undone).
func Session(session string, frames []Frame, caughtRows int, undoable bool) []Item {
	if caughtRows < 1 {
		caughtRows = 1
	}
	var out []Item
	for _, f := range frames {
		for _, t := range sortedTables(f.Counts) {
			c := f.Counts[t]
			base := Item{Session: session, Frames: []string{f.ID}, Table: t, UndoPossible: undoable, UndoFreePath: FreeSessionPath(session)}
			switch {
			case c.Truncate > 0:
				it := base
				it.Kind, it.Line = KindTruncate, Prefix+"one transaction truncated "+t+"."
				it.UndoPossible = false // the change log holds no rows for a TRUNCATE
				out = append(out, it)
			case c.Delete >= caughtRows:
				it := base
				it.Kind, it.Rows = KindDeleteRows, c.Delete
				it.Line = Prefix + "one transaction deleted " + rows(c.Delete) + " in " + t + "."
				out = append(out, it)
			case c.Rows() >= caughtRows:
				it := base
				it.Kind, it.Rows = KindManyRows, c.Rows()
				it.Line = Prefix + "one transaction changed " + rows(c.Rows()) + " in " + t + "."
				out = append(out, it)
			}
		}
	}
	return out
}

// Action is what the Ledger says about one Gate action, for the action detectors.
type Action struct {
	ID            string
	Agent         string
	Tool          string
	Table         string // the table its statement named ("" when none)
	At            time.Time
	Decision      string // allow, deny, require_approval
	Approved      bool   // require_approval that a person approved
	Executed      bool   // the tool ran (execution succeeded or unknown)
	Verdict       string // VERIFIED, MISMATCH, UNVERIFIABLE ("" before the Witness)
	Level         string // L0, L1, L2
	Destructive   string // "drop", "truncate", "delete_all" when the statement was one ("" otherwise)
	ReportedRows  *int   // rows the agent reported, when it did
	ObservedRows  *int   // rows Postgres changed (L2), when known
	UndoneBy      string // the undo action that put it back, when one applied
	Undoable      bool   // a per-action undo preview could be planned (L2 evidence present)
	UndoAvailable bool
}

// Actions finds the caught moments among actions, newest first. The caller passes the org's recent
// actions; the retry detector compares each deny with the same agent's later actions.
func Actions(acts []Action) []Item {
	sorted := append([]Action(nil), acts...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.After(sorted[j].At) })
	var out []Item
	for i, a := range sorted {
		base := Item{Actions: []string{a.ID}, Table: a.Table, UndoPossible: a.Undoable && a.UndoneBy == "", UndoFreePath: FreeActionPath(a.ID)}
		switch {
		case a.Verdict == "MISMATCH" && (a.Level == "L1" || a.Level == "L2"):
			it := base
			it.Kind = KindDoesntMatch
			it.Line = Prefix + "the " + a.Level + " check of " + a.Tool + " doesn't match what " + a.Agent + " said."
			out = append(out, it)
		case a.Executed && a.Destructive != "" && (a.Decision == "allow" || a.Approved):
			it := base
			it.Kind = KindDestructiveRan
			it.Line = Prefix + destructiveWords(a.Destructive, a.Table) + " ran through " + a.Tool + "."
			out = append(out, it)
		case a.Executed && a.ReportedRows != nil && a.ObservedRows != nil && *a.ObservedRows > *a.ReportedRows:
			it := base
			it.Kind, it.Rows = KindMoreRows, *a.ObservedRows
			it.Line = Prefix + a.Agent + " reported " + rows(*a.ReportedRows) + "; Postgres changed " + rows(*a.ObservedRows) + "."
			out = append(out, it)
		}
		if a.UndoneBy != "" {
			it := base
			it.Kind, it.UndoPossible = KindUndoneLater, false
			it.Actions = []string{a.ID, a.UndoneBy}
			it.Line = Prefix + "a person undid " + a.Tool + " by " + a.Agent + "."
			out = append(out, it)
		}
		if a.Decision == "deny" && a.Table != "" {
			// A later action (newer, so earlier in sorted) by the same agent on the same table with
			// another tool, within the window.
			for j := i - 1; j >= 0; j-- {
				b := sorted[j]
				if b.At.Sub(a.At) > RetryWindow {
					break
				}
				if b.Agent == a.Agent && b.Table == a.Table && b.Tool != a.Tool && b.At.After(a.At) {
					it := base
					it.Kind = KindRetryAfterDeny
					it.Actions = []string{a.ID, b.ID}
					it.UndoPossible, it.UndoFreePath = b.Undoable && b.UndoneBy == "", FreeActionPath(b.ID)
					it.Line = Prefix + a.Agent + " was refused " + a.Tool + " on " + a.Table + ", then tried " + b.Tool + " " +
						minutes(b.At.Sub(a.At)) + " later."
					out = append(out, it)
					break
				}
			}
		}
	}
	return out
}

// Newest keeps at most n items (Home shows 3), in the order given.
func Newest(items []Item, n int) []Item {
	if len(items) <= n {
		return items
	}
	return items[:n]
}

func destructiveWords(kind, table string) string {
	t := table
	if t == "" {
		t = "a table"
	}
	switch kind {
	case "drop":
		return "a DROP of " + t
	case "truncate":
		return "a TRUNCATE of " + t
	}
	return "a DELETE without WHERE on " + t
}

func rows(n int) string {
	if n == 1 {
		return "1 row"
	}
	return thousands(n) + " rows"
}

func minutes(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	switch {
	case m < 1:
		return "less than a minute"
	case m == 1:
		return "1 minute"
	}
	return strconv.Itoa(m) + " minutes"
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func sortedTables(m map[string]Counts) []string {
	out := make([]string, 0, len(m))
	for t := range m {
		if t == "deedglass.ledger" {
			continue
		}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// String is the item as one log-safe line (ids and counts only).
func (it Item) String() string {
	return fmt.Sprintf("%s session=%s actions=%v table=%s rows=%d", it.Kind, it.Session, it.Actions, it.Table, it.Rows)
}
