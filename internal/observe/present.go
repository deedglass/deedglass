package observe

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/caught"
)

// Every sentence a person reads about a watched session lives here (API, CLI, docs; the Console's
// copy is web/src/lib/copy.ts and a test keeps the two equal).
const (
	NoteObserved    = "Observed: what Postgres committed. Nothing was compared with what the agent said."
	NoteAttribution = "Changes from any writer during this window appear here. Leave out anything that wasn't the agent before you undo."
	NoteCantKnow    = "Postgres's change log doesn't say which database role or program made each change, and unwatched tables aren't recorded."
	NoteExpired     = "Frames expired: undo no longer possible. The sealed summary stays."
	NoteOverlap     = "Another watched session on this connector holds some of the same transactions."
	NoteBehind      = "The decoder hadn't caught up 30 seconds after the close, so late transactions were attached for up to 10 minutes."
	NoteMaxSession  = "This session closed itself after witness.observe.max_session."
	ProSheet        = "Undo a whole session in one step; every feature is free while we grow. Or copy the undo SQL and run it yourself; that's free and always will be."
)

// Headline is the session's header line: "Session obs_01k… · nightly agent · 14:02–14:03 · pg-demo · sealed".
func Headline(x *Session, loc *time.Location) string {
	parts := []string{"Session " + x.Row.ID}
	if x.Row.Label != nil {
		parts = append(parts, *x.Row.Label)
	}
	parts = append(parts, WindowText(x.Row.OpenedAt, x.Row.ClosedAt, loc), x.Connector.Name, x.Row.State)
	return strings.Join(parts, " · ")
}

// WindowText is "14:02–14:03" (UTC unless loc), with the date when the window spans days.
func WindowText(opened time.Time, closed *time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	o := opened.In(loc)
	if closed == nil {
		return o.Format("15:04") + "–now"
	}
	c := closed.In(loc)
	if o.YearDay() != c.YearDay() || o.Year() != c.Year() {
		return o.Format("2 Jan 15:04") + "–" + c.Format("2 Jan 15:04")
	}
	return o.Format("15:04") + "–" + c.Format("15:04")
}

// CountsLine is "3 transactions · public.orders 301 updated · public.order_events 301 inserted".
func CountsLine(s Summary) string {
	parts := []string{plural(s.Transactions, "transaction")}
	for _, t := range TableLines(s) {
		parts = append(parts, t[0]+" "+t[1])
	}
	return strings.Join(parts, " · ")
}

// TableLines are one [table, "301 updated"] pair per table, sorted by table.
func TableLines(s Summary) [][2]string {
	tables := make([]string, 0, len(s.Tables))
	for t := range s.Tables {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	var out [][2]string
	for _, t := range tables {
		c := s.Tables[t]
		var ops []string
		for _, x := range []struct {
			n int
			w string
		}{{c.Update, "updated"}, {c.Insert, "inserted"}, {c.Delete, "deleted"}, {c.Truncate, "truncated"}} {
			if x.n > 0 {
				if x.w == "truncated" {
					ops = append(ops, fmt.Sprintf("truncated %s", times(x.n)))
					continue
				}
				ops = append(ops, thousands(x.n)+" "+x.w)
			}
		}
		if len(ops) == 0 {
			continue
		}
		out = append(out, [2]string{t, strings.Join(ops, ", ")})
	}
	return out
}

// CaughtLines are the caught items' sentences.
func CaughtLines(items []caught.Item) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Line)
	}
	return out
}

// FlagNotes are the notes a session's flags add.
func FlagNotes(flags []string) []string {
	var out []string
	for _, f := range flags {
		switch f {
		case FlagOverlap:
			out = append(out, NoteOverlap)
		case FlagDecoderBehind:
			out = append(out, NoteBehind)
		case FlagMaxSession:
			out = append(out, NoteMaxSession)
		case FlagFramesExpired:
			out = append(out, NoteExpired)
		}
	}
	return out
}

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return thousands(n) + " " + w + "s"
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return thousands(n) + " times"
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
