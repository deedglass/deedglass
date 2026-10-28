// Package orderedjson is a small ordered JSON tree, so a settings file keeps its keys in the order
// the person wrote them when Deedglass adds an entry to it (internal/protect, internal/mcpinstall).
// Objects are *Object; arrays []any; numbers json.Number (so 10000 stays 10000); strings, bools and
// nil as encoding/json decodes them.
package orderedjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Object is a JSON object whose keys keep their order.
type Object struct {
	keys []string
	vals map[string]any
}

// New is an empty object.
func New() *Object { return &Object{vals: map[string]any{}} }

// Keys are the object's keys in order. Don't change the slice.
func (o *Object) Keys() []string { return o.keys }

// Get is the value under k.
func (o *Object) Get(k string) (any, bool) { v, ok := o.vals[k]; return v, ok }

// Set sets k, keeping its place when it is already there and appending it when it isn't.
func (o *Object) Set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Del removes k.
func (o *Object) Del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Len is the number of keys.
func (o *Object) Len() int { return len(o.keys) }

// ErrNotJSON is a file that isn't exactly one JSON value (a comment, a second value, a trailing comma).
var ErrNotJSON = errors.New("not one JSON value")

// Parse reads exactly one JSON value; anything after it (a comment, a second value) is an error.
func Parse(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := parseValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrNotJSON
	}
	return v, nil
}

func parseValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := New()
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, ErrNotJSON
				}
				v, err := parseValue(d)
				if err != nil {
					return nil, err
				}
				o.Set(k, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return o, nil
		case '[':
			a := []any{}
			for d.More() {
				v, err := parseValue(d)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			if _, err := d.Token(); err != nil {
				return nil, err
			}
			return a, nil
		}
		return nil, ErrNotJSON
	default:
		return t, nil
	}
}

// Render writes v with indent per level and a final newline.
func Render(v any, indent string) []byte {
	var b bytes.Buffer
	write(&b, v, indent, 0)
	b.WriteByte('\n')
	return b.Bytes()
}

func write(b *bytes.Buffer, v any, indent string, depth int) {
	pad := func(n int) { b.WriteString(strings.Repeat(indent, n)) }
	switch v := v.(type) {
	case *Object:
		if len(v.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range v.keys {
			pad(depth + 1)
			b.WriteString(Quote(k))
			b.WriteString(": ")
			write(b, v.vals[k], indent, depth+1)
			if i < len(v.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		pad(depth)
		b.WriteByte('}')
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, x := range v {
			pad(depth + 1)
			write(b, x, indent, depth+1)
			if i < len(v)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		pad(depth)
		b.WriteByte(']')
	case string:
		b.WriteString(Quote(v))
	case json.Number:
		b.WriteString(v.String())
	case bool:
		if v {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	}
}

// Quote is s as a JSON string, without HTML escaping.
func Quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// IndentOf is the indent the file already uses (its first indented line), or two spaces.
func IndentOf(b []byte) string {
	for _, l := range strings.Split(string(b), "\n")[1:] {
		t := strings.TrimLeft(l, " \t")
		if t == "" || len(t) == len(l) {
			continue
		}
		return l[:len(l)-len(t)]
	}
	return "  "
}

// EachString calls f on every string value in v, depth first.
func EachString(v any, f func(string)) {
	switch v := v.(type) {
	case *Object:
		for _, k := range v.keys {
			EachString(v.vals[k], f)
		}
	case []any:
		for _, x := range v {
			EachString(x, f)
		}
	case string:
		f(v)
	}
}

// MapStrings returns a copy of v with f applied to every string value (not keys).
func MapStrings(v any, f func(string) string) any {
	switch v := v.(type) {
	case *Object:
		o := New()
		for _, k := range v.keys {
			o.Set(k, MapStrings(v.vals[k], f))
		}
		return o
	case []any:
		a := make([]any, len(v))
		for i, x := range v {
			a[i] = MapStrings(x, f)
		}
		return a
	case string:
		return f(v)
	}
	return v
}

// FromValue converts a plain Go value (what json.Unmarshal into any gives, or maps and slices a
// caller builds) into the ordered tree. Map keys are sorted, so the output is stable.
func FromValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		o := New()
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			o.Set(k, FromValue(v[k]))
		}
		return o
	case []any:
		a := make([]any, len(v))
		for i, x := range v {
			a[i] = FromValue(x)
		}
		return a
	case []string:
		a := make([]any, len(v))
		for i, x := range v {
			a[i] = x
		}
		return a
	case int:
		return json.Number(strconv.Itoa(v))
	}
	return v
}
