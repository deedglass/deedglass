package witness

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// Code names a reason. Codes are snake_case and stable forever, like error codes; the catalog
// (catalog.go) is the only place a code gets its class and its sentence.
type Code string

// Reason is one piece of evidence, or one missing piece, behind a verdict.
type Reason struct {
	Code     Code           `json:"code"`
	Class    Class          `json:"class"` // from the catalog; callers can't set it
	Level    Level          `json:"level"`
	Check    string         `json:"check"`   // "status", "response.schema", "readback.expect", "postcondition", "row.xmin", …
	Message  string         `json:"message"` // rendered from the catalog template
	Diff     []FieldDiff    `json:"diff,omitempty"`
	Evidence map[string]any `json:"evidence,omitempty"` // small (≤ 8 KiB as JSON); never payloads or secrets
}

// FieldDiff is one compared field that didn't agree: what the tool's call implied, and what the
// target showed.
type FieldDiff struct {
	Path     string `json:"path"`
	Expected any    `json:"expected"`
	Observed any    `json:"observed"`
}

// MaxEvidenceBytes caps a reason's evidence; larger evidence is replaced by a note that it was cut.
const MaxEvidenceBytes = 8 << 10

// maxVarRunes caps each value placed into a message, so the sentence stays readable. {why} is
// Deedglass's own explanation (readback_skipped), never a value from the target, and gets more
// room so it isn't cut mid-sentence.
const (
	maxVarRunes = 60
	maxWhyRunes = 120
)

// R builds a Reason from the catalog: the class and the message come from the code. An unknown
// code panics under go test and becomes the gap internal_error otherwise: the witness fails closed.
func R(level Level, code Code, check string, vars map[string]any) Reason {
	e, ok := catalog[code]
	if !ok {
		if testing.Testing() {
			panic(fmt.Sprintf("witness: reason code %q isn't in the catalog", code))
		}
		e = catalog[CodeInternalError]
		code = CodeInternalError
		vars = nil
	}
	return Reason{Code: code, Class: e.Class, Level: level, Check: check, Message: render(e.Template, vars)}
}

// WithDiff returns r with its field diffs.
func (r Reason) WithDiff(d []FieldDiff) Reason {
	r.Diff = d
	return r
}

// WithEvidence returns r with evidence, cut to a note when it is over MaxEvidenceBytes.
func (r Reason) WithEvidence(ev map[string]any) Reason {
	if len(ev) == 0 {
		return r
	}
	if b, err := json.Marshal(ev); err != nil || len(b) > MaxEvidenceBytes {
		ev = map[string]any{"truncated": true}
	}
	r.Evidence = ev
	return r
}

// render fills {name} placeholders. Values named expected and observed are shown as values (strings
// in straight double quotes); everything else is shown as written. A missing value shows as
// "unknown", and every value is cut to 60 characters.
func render(tmpl string, vars map[string]any) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(tmpl, '{')
		if i < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		j := strings.IndexByte(tmpl[i:], '}')
		if j < 0 {
			b.WriteString(tmpl)
			return b.String()
		}
		b.WriteString(tmpl[:i])
		name := tmpl[i+1 : i+j]
		v, ok := vars[name]
		switch {
		case !ok:
			b.WriteString("unknown")
		case name == "expected" || name == "observed":
			b.WriteString(clipRunes(ShowValue(v), maxVarRunes))
		case name == "why" || name == "expr":
			b.WriteString(clipRunes(fmt.Sprint(v), maxWhyRunes))
		default:
			b.WriteString(clipRunes(fmt.Sprint(v), maxVarRunes))
		}
		tmpl = tmpl[i+j+1:]
	}
}

// ShowValue writes a JSON value for a person: strings in straight double quotes, numbers as
// written, null as null, objects and arrays as compact JSON.
func ShowValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return strconv.Quote(x)
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	case float64, int, int64:
		return fmt.Sprint(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func clipRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit-1]) + "…"
}
