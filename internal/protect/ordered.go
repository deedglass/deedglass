package protect

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// A small ordered JSON tree, so a settings file keeps its keys in the order the person wrote them
// when protect adds a hook to it. Objects are *object; arrays []any; numbers json.Number (so
// 10000 stays 10000); strings, bools and nil as encoding/json decodes them.

type object struct {
	keys []string
	vals map[string]any
}

func newObject() *object { return &object{vals: map[string]any{}} }

func (o *object) get(k string) (any, bool) { v, ok := o.vals[k]; return v, ok }

func (o *object) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) del(k string) {
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

var errNotJSON = errors.New("not one JSON value")

// parse reads exactly one JSON value; anything after it (a comment, a second value) is an error.
func parse(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := parseValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errNotJSON
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
			o := newObject()
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errNotJSON
				}
				v, err := parseValue(d)
				if err != nil {
					return nil, err
				}
				o.set(k, v)
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
		return nil, errNotJSON
	default:
		return t, nil
	}
}

// render writes v with indent per level and a final newline.
func render(v any, indent string) []byte {
	var b bytes.Buffer
	write(&b, v, indent, 0)
	b.WriteByte('\n')
	return b.Bytes()
}

func write(b *bytes.Buffer, v any, indent string, depth int) {
	pad := func(n int) { b.WriteString(strings.Repeat(indent, n)) }
	switch v := v.(type) {
	case *object:
		if len(v.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range v.keys {
			pad(depth + 1)
			b.WriteString(quote(k))
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
		b.WriteString(quote(v))
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

func quote(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// indentOf is the indent the file already uses (its first indented line), or two spaces.
func indentOf(b []byte) string {
	for _, l := range strings.Split(string(b), "\n")[1:] {
		t := strings.TrimLeft(l, " \t")
		if t == "" || len(t) == len(l) {
			continue
		}
		return l[:len(l)-len(t)]
	}
	return "  "
}

// strings in v, depth first.
func eachString(v any, f func(string)) {
	switch v := v.(type) {
	case *object:
		for _, k := range v.keys {
			eachString(v.vals[k], f)
		}
	case []any:
		for _, x := range v {
			eachString(x, f)
		}
	case string:
		f(v)
	}
}

// mapStrings returns a copy of v with f applied to every string value (not keys).
func mapStrings(v any, f func(string) string) any {
	switch v := v.(type) {
	case *object:
		o := newObject()
		for _, k := range v.keys {
			o.set(k, mapStrings(v.vals[k], f))
		}
		return o
	case []any:
		a := make([]any, len(v))
		for i, x := range v {
			a[i] = mapStrings(x, f)
		}
		return a
	case string:
		return f(v)
	}
	return v
}
