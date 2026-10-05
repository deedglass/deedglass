// Package toolscan flags MCP tool definitions whose text is aimed at the model rather than at the
// person reading it (S34, attack case Q01): phrases that tell a model to drop its instructions or
// hide something from the user, instruction tags such as <IMPORTANT>, and characters people can't
// see (Unicode tag characters, bidirectional overrides, zero-width characters).
//
// It is a heuristic. It flags; it doesn't understand intent, so a reworded instruction can pass and
// a harmless description can be flagged (docs/LIMITATIONS.md). Callers hide a flagged tool from
// clients and record why; they never show the reason to an agent.
//
// The package is pure: no I/O, no clock, no logging.
package toolscan

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Reasons a tool is hidden (the admin deed's data.reason, contract §8, S34).
const (
	ReasonDescription = "description_flagged" // the definition's text carries instructions or hidden characters
	ReasonChanged     = "definition_changed"  // the definition changed after it was first listed (a rug pull)
)

// Finding is why a definition was flagged.
type Finding struct {
	Field   string // "description", "title", "inputSchema.properties.path.description", …
	Kind    string // "instruction" | "tag" | "hidden_characters"
	Excerpt string // the matched text, at most 40 characters, control and hidden characters shown as \u escapes
}

// maxScan bounds the text read from one definition (a definition is at most a few KiB in practice;
// a hostile one can be larger, and is flagged by what the first MiB holds).
const maxScan = 1 << 20

// phrases are matched case-insensitively after whitespace is collapsed. Each one is an instruction to
// a model, not a description of a tool. The list is short on purpose: every entry costs a false
// positive somewhere.
var phrases = regexp.MustCompile(`(?i)\b(` +
	`ignore (?:all |any |the )?(?:previous|prior|above|earlier|preceding) (?:instructions|prompts?|messages|rules)` +
	`|disregard (?:all |any |the )?(?:previous|prior|above|earlier|preceding)` +
	`|forget (?:all |any |the |your )?(?:previous|prior|earlier) instructions` +
	`|do not (?:tell|inform|mention (?:this |it )?to|show (?:this |it )?to) the user` +
	`|don'?t (?:tell|inform) the user` +
	`|without (?:telling|informing|asking) the user` +
	`|(?:new|updated|override) (?:system )?instructions\s*:` +
	`|you are now (?:a|an|in)\b` +
	`|your (?:new |real )?instructions are` +
	`|reveal (?:your|the) (?:system )?prompt` +
	`)`)

// tags are markup that some tool-poisoning payloads use to make text look authoritative to a model.
var tags = regexp.MustCompile(`(?i)<\s*/?\s*(important|system|instructions?|secret|admin|assistant|human)\s*>|\[\s*/?\s*(system|inst)\s*\]|\{\{\s*system`)

// hidden reports a character a person can't see in a description: Unicode tag characters
// (U+E0000–U+E007F, used to smuggle ASCII), bidirectional controls, zero-width characters and the
// byte-order mark, and C0/C1 controls other than tab and newline.
func hidden(r rune) bool {
	switch {
	case r >= 0xE0000 && r <= 0xE007F:
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	case r >= 0x200B && r <= 0x200D, r == 0x2060, r == 0xFEFF, r == 0x180E:
		return true
	case r < 0x20 && r != '\t' && r != '\n' && r != '\r', r >= 0x7F && r <= 0x9F:
		return true
	}
	return false
}

// Scan reads one tool definition (the JSON object from tools/list) and returns its findings, in a
// stable order. An empty result means nothing was flagged. Definitions that aren't JSON objects
// return nothing: the callers already drop them.
func Scan(def json.RawMessage) []Finding {
	var v any
	if len(def) > maxScan {
		def = def[:maxScan]
	}
	if json.Unmarshal(def, &v) != nil {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var out []Finding
	walk("", m, 0, &out)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// Flagged reports whether def has a finding and returns the first one.
func Flagged(def json.RawMessage) (Finding, bool) {
	f := Scan(def)
	if len(f) == 0 {
		return Finding{}, false
	}
	return f[0], true
}

// textFields are the members whose string values a client shows to its model.
var textFields = map[string]bool{"description": true, "title": true}

func walk(path string, v any, depth int, out *[]Finding) {
	if depth > 32 {
		return
	}
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if s, ok := x[k].(string); ok && textFields[k] {
				check(p, s, out)
				continue
			}
			walk(p, x[k], depth+1, out)
		}
	case []any:
		for _, e := range x {
			walk(path, e, depth+1, out)
		}
	}
}

func check(field, s string, out *[]Finding) {
	for _, r := range s {
		if hidden(r) {
			*out = append(*out, Finding{Field: field, Kind: "hidden_characters", Excerpt: excerpt(s)})
			return
		}
	}
	flat := strings.Join(strings.Fields(s), " ")
	if loc := tags.FindStringIndex(flat); loc != nil {
		*out = append(*out, Finding{Field: field, Kind: "tag", Excerpt: clip(flat[loc[0]:], 40)})
		return
	}
	if loc := phrases.FindStringIndex(flat); loc != nil {
		*out = append(*out, Finding{Field: field, Kind: "instruction", Excerpt: clip(flat[loc[0]:], 40)})
	}
}

// excerpt shows the start of s with every hidden character as an escape.
func excerpt(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if hidden(r) {
			b.WriteString(escape(r))
			n += 6
		} else {
			b.WriteRune(r)
			n++
		}
		if n >= 40 {
			break
		}
	}
	return clip(b.String(), 40)
}

func escape(r rune) string {
	const hex = "0123456789ABCDEF"
	if r > 0xFFFF {
		return `\U` + string([]byte{hex[r>>28&15], hex[r>>24&15], hex[r>>20&15], hex[r>>16&15], hex[r>>12&15], hex[r>>8&15], hex[r>>4&15], hex[r&15]})
	}
	return `\u` + string([]byte{hex[r>>12&15], hex[r>>8&15], hex[r>>4&15], hex[r&15]})
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Sentence is the admin-facing line for a hidden tool (S34 §5): the tool, the connector, the
// reason and the excerpt, quoted and clipped. It never goes to an agent.
func Sentence(tool, connector string, f Finding) string {
	tool, connector = clip(tool, 64), clip(connector, 64)
	switch f.Kind {
	case "hidden_characters":
		return "Hid tool \"" + tool + "\" from " + connector + ": its " + f.Field + " contains characters people can't see (\"" + f.Excerpt + "\"). Review it in Console › Connectors before relying on it."
	case "changed":
		return "Hid tool \"" + tool + "\" from " + connector + ": its definition changed after it was first listed. Review the new definition in Console › Connectors; it stays hidden until Deedglass restarts."
	}
	return "Hid tool \"" + tool + "\" from " + connector + ": its " + f.Field + " contains instructions aimed at the model (\"" + f.Excerpt + "\"). Review it in Console › Connectors before relying on it."
}
