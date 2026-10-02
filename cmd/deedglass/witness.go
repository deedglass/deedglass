package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/internal/witness/l2"
)

// The witness block (S19 §5): the claim on one line, "<level> · <VERDICT> · <summary>", then the
// reasons that matter, each with its symbol and level, and a field/expected/observed table under a
// reason that compared fields. Symbols and words carry the meaning; colour only repeats it.

// verdictSymbol is the brand §13.3 symbol of a verdict, styled.
func verdictSymbol(st ui.Style, verdict string) string {
	switch verdict {
	case gate.VerdictVerified:
		return st.Teal(ui.OK)
	case gate.VerdictMismatch:
		return st.Red(ui.Fail)
	}
	return st.Amber(ui.Unknown)
}

// classSymbol is a reason's symbol: ✓ pass, ✕ violation, ○ gap, · note.
func classSymbol(st ui.Style, c witness.Class) string {
	switch c {
	case witness.Pass:
		return st.Teal(ui.OK)
	case witness.Violation:
		return st.Red(ui.Fail)
	case witness.Gap:
		return st.Amber(ui.Unknown)
	}
	return st.Dim(ui.Sep)
}

// verdictWord is the verdict in its colour (red only for a MISMATCH).
func verdictWord(st ui.Style, v string) string {
	switch v {
	case gate.VerdictVerified:
		return st.Teal(v)
	case gate.VerdictMismatch:
		return st.Red(v)
	}
	return st.Amber(v)
}

// witnessClaim is the claim line's value: "L1 · MISMATCH · <summary>" (or the method when there is
// no summary, as in lists).
func witnessClaim(st ui.Style, w *ledger.ActionWitness, method bool) string {
	line := w.Level + " " + ui.Sep + " " + verdictWord(st, w.Verdict)
	switch {
	case method && w.Method != nil:
		line += " " + ui.Sep + " " + clean(*w.Method)
	case w.Summary != nil && *w.Summary != "":
		line += " " + ui.Sep + " " + clean(*w.Summary)
	case w.Method != nil:
		line += " " + ui.Sep + " " + clean(*w.Method)
	}
	return line
}

// witnessReasons lists the reasons under the claim, indented by pad. With all, every reason of the
// detail (passes and notes too, as actions show prints them); otherwise the violations and gaps,
// leaving out the one the claim line already says (its field table still prints).
func witnessReasons(st ui.Style, w *ledger.ActionWitness, all bool, pad string) []string {
	var rs []witness.Reason
	if all {
		if d, ok := witness.ParseDetail(w.Detail); ok {
			rs = d.Reasons
		}
	}
	if rs == nil && w.Reasons != nil {
		rs = *w.Reasons
	}
	summary := ""
	if w.Summary != nil {
		summary = *w.Summary
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range rs {
		if !all && seen[r.Message] {
			continue // two checks that disagree the same way (expect and a postcondition) print once
		}
		seen[r.Message] = true
		said := !all && r.Message == summary
		if !said {
			out = append(out, pad+classSymbol(st, r.Class)+" "+string(r.Level)+"  "+clean(r.Message))
		}
		if len(r.Diff) > 0 {
			inner := pad
			if !said {
				inner += "      "
			}
			out = append(out, diffTable(st, r.Diff, inner)...)
		}
	}
	return out
}

// diffTable is brand §12's two aligned columns, expected and observed, under a field column.
func diffTable(st ui.Style, diffs []witness.FieldDiff, pad string) []string {
	const maxRows = 8
	fw, ew := len("field"), len("expected")
	rows := make([][3]string, 0, len(diffs))
	for i, d := range diffs {
		if i == maxRows {
			break
		}
		r := [3]string{clip(clean(d.Path), 40), clip(clean(cellValue(d.Expected)), 40), clip(clean(cellValue(d.Observed)), 40)}
		fw, ew = max(fw, ui.Width(r[0])), max(ew, ui.Width(r[1]))
		rows = append(rows, r)
	}
	out := []string{pad + st.Dim(ui.Pad("field", fw+4)+ui.Pad("expected", ew+4)+"observed")}
	for _, r := range rows {
		out = append(out, pad+ui.Pad(r[0], fw+4)+ui.Pad(r[1], ew+4)+r[2])
	}
	if len(diffs) > maxRows {
		out = append(out, pad+st.Dim(fmt.Sprintf("and %d more fields", len(diffs)-maxRows)))
	}
	return out
}

// cellValue shows a value in a diff table: strings bare, everything else as JSON.
func cellValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return witness.ShowValue(v)
}

// witnessNext is the next step after a verdict that isn't VERIFIED, or "".
func witnessNext(a *ledger.Action) string {
	w := a.Witness
	if w == nil {
		return ""
	}
	switch w.Verdict {
	case gate.VerdictMismatch:
		return "Next: deedglass actions show " + a.ID + " " + ui.Sep + " https://docs.deedglass.com/witness/mismatch"
	case gate.VerdictUnverifiable:
		if hasL2Reason(w) {
			return "Next: deedglass decoder status " + ui.Sep + " deedglass actions show " + a.ID
		}
		for _, c := range w.Codes() {
			if c == string(witness.CodeReadbackFailed) || c == string(witness.CodeReadbackTimeout) {
				conn, _, _ := strings.Cut(a.Tool, ".")
				return "Next: check the " + clean(conn) + " connector (deedglass connector test " + clean(conn) + "), then deedglass actions show " + a.ID
			}
		}
	}
	return ""
}

// l2Lines is the L2 diff under a witness claim (S21 §0): the expected and observed pair, the
// rows and the "Witnessed by Postgres" footer, from a stored witness_detail. nil when the detail
// has no L2 evidence. With footerOnly, only the footer (a VERIFIED claim needs no diff).
func l2Lines(e *Env, detail json.RawMessage, pad string, footerOnly bool) []string {
	d, ok := witness.ParseDetail(detail)
	if !ok || d.Levels.L2 == nil || d.Levels.L2.L2Evidence == nil {
		return nil
	}
	if footerOnly {
		if t := d.Levels.L2.Transaction; t != nil {
			return []string{pad + e.Style.Dim(l2.Footer(t))}
		}
		return nil
	}
	w := e.Width
	if w <= 0 {
		w = 120
	}
	return l2.Render(d, l2.RenderOptions{Width: w, Style: e.Style, Indent: pad})
}

// historyLines are the witness sections sealed about an action, oldest first (S21 §5):
// "L1 · UNVERIFIABLE · sealed 13:20:11 · l2_timeout", then each amendment.
func historyLines(st ui.Style, w *ledger.ActionWitness, summary string) []string {
	if w == nil || len(w.History) < 2 {
		return nil
	}
	var out []string
	for i, h := range w.History {
		at := h.At
		if t, err := time.Parse("2006-01-02T15:04:05.000Z", h.At); err == nil {
			at = t.UTC().Format("15:04:05")
		}
		line := h.Level + " " + ui.Sep + " " + verdictWord(st, h.Verdict) + " " + ui.Sep
		if h.Kind == "amendment" {
			line += " amendment " + shortID(h.Deed) + " " + ui.Sep + " " + at
		} else {
			line += " sealed " + at
		}
		if h.Reason != nil {
			line += " " + ui.Sep + " " + reasonWords(*h.Reason)
		}
		if i == len(w.History)-1 && summary != "" {
			line += " " + ui.Sep + " " + clean(summary)
		}
		out = append(out, line)
	}
	return out
}

// reasonWords says a history entry's reason code briefly.
func reasonWords(code string) string {
	switch witness.Code(code) {
	case witness.CodeL2Timeout:
		return "L2 timed out"
	case witness.CodeSlotDropped:
		return "the slot was dropped"
	case witness.CodeDecoderHalted:
		return "the decoder had stopped"
	}
	return strings.ReplaceAll(code, "_", " ")
}
