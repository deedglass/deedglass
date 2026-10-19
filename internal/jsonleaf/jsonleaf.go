// Package jsonleaf keeps a JSON document as keyed digests of its leaves (ADR-108): a map from each
// leaf's JSON Pointer to an HMAC-SHA-256 of the pointer and the leaf's canonical value. The decoder
// stores a FHIR server's resource content this way, so a frame never holds a clinical value, and the
// connector's L2 judge compares what the agent sent leaf by leaf with the same key. A difference
// names the path; neither side's value can be read back from the digests without the key.
//
// Canonical values: strings "s:<text>", numbers "n:<exact rational>" (72, 72.0 and 7.2e1 are one
// value), true and false "b:true"/"b:false", null "z". An array also has a leaf at "<path>/#len"
// with its length, so two arrays of different lengths always differ.
package jsonleaf

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// MaxLeaves is the most leaves a document keeps; a larger one is refused (ErrTooLarge).
const MaxLeaves = 4096

// LenSegment is the pointer segment that holds an array's length.
const LenSegment = "#len"

// ErrTooLarge is a document with more than MaxLeaves leaves.
var ErrTooLarge = errors.New("jsonleaf: more than 4096 leaves")

// Leaves are a document's leaf digests by JSON Pointer.
type Leaves map[string]string

// Digest is one leaf's digest: base64url (no padding) of the first 12 bytes of
// HMAC-SHA-256(key, pointer 0x00 canonical).
func Digest(key []byte, pointer, canonical string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(pointer))
	m.Write([]byte{0})
	m.Write([]byte(canonical))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:12])
}

// Parse reads raw (a JSON value) with numbers kept exact and returns its leaves.
func Parse(key []byte, raw []byte) (Leaves, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("jsonleaf: trailing data after the JSON value")
	}
	return Of(key, v)
}

// Of returns the leaves of a decoded value (numbers as json.Number, or float64 and int).
func Of(key []byte, v any) (Leaves, error) {
	out := Leaves{}
	if err := walk(key, "", v, out); err != nil {
		return nil, err
	}
	return out, nil
}

func walk(key []byte, p string, v any, out Leaves) error {
	if len(out) > MaxLeaves {
		return ErrTooLarge
	}
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if err := walk(key, p+"/"+Escape(k), e, out); err != nil {
				return err
			}
		}
		return nil
	case []any:
		lp := p + "/" + LenSegment
		out[lp] = Digest(key, lp, "n:"+strconv.Itoa(len(x)))
		for i, e := range x {
			if err := walk(key, p+"/"+strconv.Itoa(i), e, out); err != nil {
				return err
			}
		}
		return nil
	}
	c, err := Canonical(v)
	if err != nil {
		return err
	}
	out[p] = Digest(key, p, c)
	return nil
}

// Canonical is a scalar's canonical form.
func Canonical(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "z", nil
	case bool:
		if x {
			return "b:true", nil
		}
		return "b:false", nil
	case string:
		return "s:" + x, nil
	case json.Number:
		return number(x.String())
	case float64:
		return number(strconv.FormatFloat(x, 'g', -1, 64))
	case int:
		return "n:" + strconv.Itoa(x), nil
	case int64:
		return "n:" + strconv.FormatInt(x, 10), nil
	}
	return "", errors.New("jsonleaf: not a JSON value")
}

func number(s string) (string, error) {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", errors.New("jsonleaf: not a number: " + s)
	}
	return "n:" + r.RatString(), nil
}

// Escape escapes one pointer segment (RFC 6901: ~ as ~0, / as ~1).
func Escape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// Segments splits a pointer into its unescaped segments.
func Segments(p string) []string {
	if p == "" {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return parts
}

// Under reports whether pointer p is prefix or inside it.
func Under(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// Sorted returns the pointers in order.
func (l Leaves) Sorted() []string {
	out := make([]string, 0, len(l))
	for p := range l {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Without returns l without the leaves under any of prefixes.
func (l Leaves) Without(prefixes ...string) Leaves {
	out := Leaves{}
	for p, d := range l {
		keep := true
		for _, x := range prefixes {
			if Under(p, x) {
				keep = false
				break
			}
		}
		if keep {
			out[p] = d
		}
	}
	return out
}

// Diff compares what was sent (want) with what was stored (got) the way the L1 read-back does
// (ADR-105 §6): objects field by field, so a field only got has is allowed; an array as a whole,
// so any difference inside it, or an extra element, is a difference at the array. It returns the
// differing fields as dotted paths ("status", "code.coding"), sorted and without repeats.
func Diff(want, got Leaves) []string {
	var arrays []string
	for p := range want {
		if strings.HasSuffix(p, "/"+LenSegment) {
			arrays = append(arrays, strings.TrimSuffix(p, "/"+LenSegment))
		}
	}
	sort.Strings(arrays)
	// The outermost arrays only: an array inside another is compared with it.
	var outer []string
	for _, a := range arrays {
		if len(outer) > 0 && Under(a, outer[len(outer)-1]) {
			continue
		}
		outer = append(outer, a)
	}
	report := func(p string) string {
		for _, a := range outer {
			if Under(p, a) {
				return dotted(a)
			}
		}
		return dotted(p)
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if d := report(p); !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for p, d := range want {
		if got[p] != d {
			add(p)
		}
	}
	for _, a := range outer {
		for p := range got {
			if Under(p, a) && p != a {
				if _, ok := want[p]; !ok {
					add(p)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// dotted is a pointer as a dotted field path ("/code/coding" → "code.coding"); the root is "$".
func dotted(p string) string {
	s := Segments(p)
	if len(s) == 0 {
		return "$"
	}
	return strings.Join(s, ".")
}
