package witness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Templates are whole values only: "{{args.<path>}}" or "{{result.body.<path>}}". Anything else is
// a literal. There is no string interpolation, so a template can't build a path or a query. An
// expect path's root (body, status, meta) is lowercase; later segments may use any case (FHIR's
// body.valueQuantity.value, HC-P00).
var (
	reTemplate   = regexp.MustCompile(`^\{\{\s*((?:args|result\.body)(?:\.[A-Za-z0-9_]+|\[[0-9]+\])*)\s*\}\}$`)
	reExpectPath = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[A-Za-z0-9_]+|\[[0-9]+\])*$`)
	reDigits     = regexp.MustCompile(`^[0-9]+$`)
)

// templateRef returns the reference inside a whole-value template, or "" for a literal.
func templateRef(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	m := reTemplate.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

// decode reads JSON with UseNumber; nil for empty or invalid input.
func decode(raw []byte) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}

// templateEnv is what templates resolve against: the call's arguments and the tool's response body.
type templateEnv struct {
	args any
	body any
}

func newTemplateEnv(s Subject) templateEnv {
	env := templateEnv{args: decode(s.Args)}
	if out, ok := decode(s.Result.Output).(map[string]any); ok {
		env.body = out["body"]
	}
	return env
}

// resolve gives a template's value (missing → nil, found false), or a literal as itself.
func (e templateEnv) resolve(v any) (any, bool) {
	ref := templateRef(v)
	if ref == "" {
		return v, true
	}
	if rest, ok := strings.CutPrefix(ref, "args"); ok {
		return lookup(e.args, rest)
	}
	rest := strings.TrimPrefix(ref, "result.body")
	return lookup(e.body, rest)
}

// lookup walks ".name" and "[n]" segments from v.
func lookup(v any, path string) (any, bool) {
	for path != "" {
		switch path[0] {
		case '.':
			end := strings.IndexAny(path[1:], ".[")
			name := path[1:]
			if end >= 0 {
				name = path[1 : end+1]
				path = path[end+1:]
			} else {
				path = ""
			}
			m, ok := v.(map[string]any)
			if !ok {
				return nil, false
			}
			if v, ok = m[name]; !ok {
				return nil, false
			}
		case '[':
			end := strings.IndexByte(path, ']')
			if end < 0 {
				return nil, false
			}
			i, err := strconv.Atoi(path[1:end])
			path = path[end+1:]
			a, ok := v.([]any)
			if err != nil || !ok || i < 0 || i >= len(a) {
				return nil, false
			}
			v = a[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// observationRoot is what expect paths and the CEL observation variable see.
func observationRoot(o Observation) map[string]any {
	meta := o.Meta
	if meta == nil {
		meta = map[string]any{}
	}
	return map[string]any{"body": o.Body, "status": json.Number(strconv.Itoa(o.Status)), "meta": meta}
}

// Equal is JSON equality after UseNumber: numbers by value (1.0 equals 1), strings exactly, objects
// and arrays member by member. One documented convenience: a string of decimal digits equals a
// number written with the same digits ("42" equals 42; "042" doesn't).
func Equal(a, b any) bool {
	a, b = jsonNumber(a), jsonNumber(b)
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		switch y := b.(type) {
		case string:
			return x == y
		case json.Number:
			return reDigits.MatchString(x) && y.String() == x
		}
		return false
	case json.Number:
		switch y := b.(type) {
		case json.Number:
			return numEqual(x, y)
		case string:
			return reDigits.MatchString(y) && x.String() == y
		}
		return false
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !Equal(v, w) {
				return false
			}
		}
		return true
	}
	return false
}

// jsonNumber writes a Go number (an expect literal from the config, a CEL value) as the
// json.Number a decoded body holds, so Equal is symmetric whichever side it came from.
func jsonNumber(v any) any {
	switch x := v.(type) {
	case float64:
		return json.Number(strconv.FormatFloat(x, 'g', -1, 64))
	case float32:
		return json.Number(strconv.FormatFloat(float64(x), 'g', -1, 32))
	case int:
		return json.Number(strconv.Itoa(x))
	case int32:
		return json.Number(strconv.FormatInt(int64(x), 10))
	case int64:
		return json.Number(strconv.FormatInt(x, 10))
	case uint64:
		return json.Number(strconv.FormatUint(x, 10))
	}
	return v
}

func numEqual(a, b json.Number) bool {
	if a == b {
		return true
	}
	ra, ok1 := new(big.Rat).SetString(string(a))
	rb, ok2 := new(big.Rat).SetString(string(b))
	return ok1 && ok2 && ra.Cmp(rb) == 0
}

// expectResult is one expect run: the fields that agreed and the ones that didn't.
type expectResult struct {
	matched []FieldDiff // Expected == Observed
	diffs   []FieldDiff
}

// runExpect compares every expect path of the spec with the observation, in path order.
func runExpect(spec map[string]any, env templateEnv, o Observation) expectResult {
	root := observationRoot(o)
	paths := make([]string, 0, len(spec))
	for p := range spec {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var r expectResult
	for _, p := range paths {
		want, _ := env.resolve(spec[p])
		got, _ := lookup(root, "."+p)
		fd := FieldDiff{Path: p, Expected: want, Observed: got}
		if Equal(want, got) {
			r.matched = append(r.matched, fd)
		} else {
			r.diffs = append(r.diffs, fd)
		}
	}
	return r
}

// displayPath is how a message names a field: the read-back body's fields without "body.".
func displayPath(p string) string {
	if s, ok := strings.CutPrefix(p, "body."); ok {
		return s
	}
	return p
}

// fieldsPhrase is readback_matched's list: `status is "shipped"`, or `status is "shipped" and 2
// more fields agree`.
func fieldsPhrase(m []FieldDiff) string {
	if len(m) == 0 {
		return "the read-back answered"
	}
	first := fmt.Sprintf("%s is %s", displayPath(m[0].Path), ShowValue(m[0].Observed))
	switch len(m) {
	case 1:
		return first
	case 2:
		return first + fmt.Sprintf(" and %s is %s", displayPath(m[1].Path), ShowValue(m[1].Observed))
	}
	return first + fmt.Sprintf(" and %d more fields agree", len(m)-1)
}
