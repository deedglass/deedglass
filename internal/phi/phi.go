// Package phi is PHI mode's projection (HC-P09): it turns a stored request, result or witness
// detail into a copy that keeps only the paths a caller names and replaces everything else by a
// digest. It is pure (no database, no connector code), so the core stays protocol-agnostic
// (DL-109): connectors say which paths are identifiers, this package never guesses.
//
// The rule is fail-closed. A subtree with no kept path below it becomes one digest string, keys
// included (JSON keys can carry data too). A kept path keeps its value only when it is a short
// identifier: a string of at most 256 characters with no whitespace or control characters, an
// integer, or a boolean. Anything else at a kept path is digested as well.
package phi

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/deed"
)

// MaxKept is the longest string a kept path may keep.
const MaxKept = 256

// Paths is a set of kept paths: dot-separated object keys, "*" for any key and "[]" for every item
// of an array ("status", "headers.etag", "levels.*.method", "reasons[].diff[].path").
type Paths []string

// node is the compiled form of Paths: a tree of segments. leaf means the path ends here.
type node struct {
	next map[string]*node
	leaf bool
}

func compile(ps Paths) *node {
	root := &node{next: map[string]*node{}}
	for _, p := range ps {
		n := root
		for _, seg := range split(p) {
			c, ok := n.next[seg]
			if !ok {
				c = &node{next: map[string]*node{}}
				n.next[seg] = c
			}
			n = c
		}
		n.leaf = true
	}
	return root
}

// split turns "reasons[].diff[].path" into ["reasons", "[]", "diff", "[]", "path"].
func split(p string) []string {
	var out []string
	for _, part := range strings.Split(p, ".") {
		for strings.HasSuffix(part, "[]") {
			base := strings.TrimSuffix(part, "[]")
			if base != "" {
				out = append(out, base)
			}
			out = append(out, "[]")
			part = ""
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// child is the node for key under n ("*" matches any key), or nil.
func (n *node) child(key string) *node {
	if n == nil {
		return nil
	}
	if c, ok := n.next[key]; ok {
		return c
	}
	return n.next["*"]
}

// Digest is "sha256:" and the hex SHA-256 of a JSON value's canonical form (the same digest the
// deeds use), or of its raw bytes when it isn't JSON.
func Digest(raw []byte) string {
	if d, err := deed.DigestJSON(raw); err == nil {
		return d
	}
	b, _ := json.Marshal(string(raw))
	d, _ := deed.DigestJSON(b)
	return d
}

func digestValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return Digest([]byte("null"))
	}
	return Digest(b)
}

// Minimize returns raw with only the kept paths' identifier values; every other value, and every
// subtree with no kept path below it, is a digest string. Empty input stays empty; input that isn't
// JSON becomes one digest string.
func Minimize(raw json.RawMessage, keep Paths) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		out, _ := json.Marshal(Digest(raw))
		return out
	}
	out, err := json.Marshal(walk(v, compile(keep)))
	if err != nil {
		b, _ := json.Marshal(Digest(raw))
		return b
	}
	return out
}

func walk(v any, n *node) any {
	if n == nil {
		return digestValue(v)
	}
	switch x := v.(type) {
	case map[string]any:
		if len(n.next) == 0 {
			return digestValue(v)
		}
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = walk(val, n.child(k))
		}
		return out
	case []any:
		c := n.next["[]"]
		if c == nil {
			return digestValue(v)
		}
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = walk(val, c)
		}
		return out
	case nil:
		return nil // null carries nothing
	default:
		if n.leaf && identifier(v) {
			return v
		}
		return digestValue(v)
	}
}

// identifier reports whether a scalar may be kept as it is: null, a boolean, an integer, or a short
// string with no whitespace or control characters.
func identifier(v any) bool {
	switch x := v.(type) {
	case nil, bool:
		return true
	case json.Number:
		return !strings.ContainsAny(x.String(), ".eE")
	case string:
		if x == "" || len(x) > MaxKept || !utf8.ValidString(x) {
			return x == ""
		}
		for _, r := range x {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				return false
			}
		}
		return true
	}
	return false
}

// Text is a free-text value (an error message) as PHI mode stores it: its digest.
func Text(s string) string {
	b, _ := json.Marshal(s)
	return Digest(b)
}

// WitnessDetail is what PHI mode keeps of a witness_detail: the verdicts, levels, methods, reason
// codes and classes, the compared paths, the read-back's digests and counts. Summaries, messages,
// targets and compared values become digests.
var WitnessDetail = Paths{
	"object", "version", "witnessed_at", "truncated",
	"claim.level", "claim.verdict", "claim.method", "claim.required_level",
	"levels.*.verdict", "levels.*.method",
	"levels.*.readback.tool", "levels.*.readback.request_digest", "levels.*.readback.status",
	"levels.*.readback.response_digest", "levels.*.readback.attempts", "levels.*.readback.latency_ms",
	"reasons[].code", "reasons[].class", "reasons[].level", "reasons[].check",
	"reasons[].diff[].path",
}

// MinimizeArgs is Minimize for a tool's arguments: a JSON object keeps its argument names, the
// values of the named identifier arguments (a connector's, such as a REST tool's path args) when
// they are short identifiers, and a digest of every other value. Arguments that aren't an object
// become one digest string.
func MinimizeArgs(raw json.RawMessage, ids []string) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil || dec.More() {
		out, _ := json.Marshal(Digest(raw))
		return out
	}
	keep := make(map[string]bool, len(ids))
	for _, n := range ids {
		keep[n] = true
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if keep[k] && identifier(v) {
			out[k] = v
		} else {
			out[k] = digestValue(v)
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		b, _ = json.Marshal(Digest(raw))
	}
	return b
}
