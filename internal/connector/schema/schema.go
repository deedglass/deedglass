// Package schema validates tool arguments against the JSON Schema subset native connectors use
// (S16 §4 step 8): type, properties, required, enum, pattern (RE2), minimum, maximum, maxLength,
// items and additionalProperties: false. Nothing more: Compile refuses any other keyword except
// the annotations title, description, default, examples and $schema, so a schema never means more
// than this package checks.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Schema is a compiled schema node.
type Schema struct {
	Type                 string
	Properties           map[string]*Schema
	Required             []string
	Enum                 []any
	Pattern              *regexp.Regexp
	Minimum, Maximum     *float64
	MaxLength            *int
	Items                *Schema
	AdditionalProperties *bool
}

// Error is a failed check: Path names the argument ("args.id"), What says what's wrong.
type Error struct {
	Path string
	What string
}

func (e *Error) Error() string { return e.Path + " " + e.What }

var types = []string{"object", "string", "integer", "number", "boolean", "array"}

var annotations = map[string]bool{"title": true, "description": true, "default": true, "examples": true, "$schema": true}

// MaxDepth bounds nesting.
const MaxDepth = 16

// Compile parses a schema. An empty schema accepts any object.
func Compile(raw json.RawMessage) (*Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return &Schema{Type: "object"}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("schema: not a JSON object: %w", err)
	}
	return compile(m, "schema", 0)
}

func compile(m map[string]json.RawMessage, at string, depth int) (*Schema, error) {
	if depth > MaxDepth {
		return nil, fmt.Errorf("%s: nested more than %d levels", at, MaxDepth)
	}
	s := &Schema{}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := m[k]
		var err error
		switch k {
		case "type":
			err = json.Unmarshal(v, &s.Type)
			if err == nil && !slices.Contains(types, s.Type) {
				err = fmt.Errorf("type %q isn't one of %s", s.Type, strings.Join(types, ", "))
			}
		case "properties":
			var ps map[string]map[string]json.RawMessage
			if err = json.Unmarshal(v, &ps); err == nil {
				s.Properties = map[string]*Schema{}
				for name, p := range ps {
					if s.Properties[name], err = compile(p, at+".properties."+name, depth+1); err != nil {
						return nil, err
					}
				}
			}
		case "required":
			err = json.Unmarshal(v, &s.Required)
		case "enum":
			dec := json.NewDecoder(bytes.NewReader(v))
			dec.UseNumber()
			err = dec.Decode(&s.Enum)
		case "pattern":
			var p string
			if err = json.Unmarshal(v, &p); err == nil {
				s.Pattern, err = regexp.Compile(p)
			}
		case "minimum":
			err = json.Unmarshal(v, &s.Minimum)
		case "maximum":
			err = json.Unmarshal(v, &s.Maximum)
		case "maxLength":
			err = json.Unmarshal(v, &s.MaxLength)
		case "items":
			var it map[string]json.RawMessage
			if err = json.Unmarshal(v, &it); err == nil {
				s.Items, err = compile(it, at+".items", depth+1)
			}
		case "additionalProperties":
			var b bool
			if err = json.Unmarshal(v, &b); err == nil {
				s.AdditionalProperties = &b
			} else {
				err = fmt.Errorf("additionalProperties must be true or false")
			}
		default:
			if !annotations[k] {
				return nil, fmt.Errorf("%s: the keyword %q isn't supported (type, properties, required, enum, pattern, minimum, maximum, maxLength, items, additionalProperties)", at, k)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", at, k, err)
		}
	}
	return s, nil
}

// Validate checks args (a JSON document) against s. The first failure is returned.
func (s *Schema) Validate(args json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return &Error{"args", "isn't valid JSON"}
	}
	return s.check("args", v)
}

func (s *Schema) check(path string, v any) error {
	if len(s.Enum) > 0 && !inEnum(s.Enum, v) {
		return &Error{path, "must be one of " + enumList(s.Enum)}
	}
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return &Error{path, "must be an object"}
		}
		for _, r := range s.Required {
			if _, ok := m[r]; !ok {
				return &Error{path + "." + r, "is required"}
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p, known := s.Properties[k]
			if !known {
				if s.AdditionalProperties != nil && !*s.AdditionalProperties {
					return &Error{path + "." + clip(k), "isn't an argument this tool takes"}
				}
				continue
			}
			if err := p.check(path+"."+k, m[k]); err != nil {
				return err
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok {
			return &Error{path, "must be an array"}
		}
		if s.Items != nil {
			for i, x := range a {
				if err := s.Items.check(path+"["+strconv.Itoa(i)+"]", x); err != nil {
					return err
				}
			}
		}
	case "string":
		str, ok := v.(string)
		if !ok {
			return &Error{path, "must be a string"}
		}
		if s.MaxLength != nil && utf8.RuneCountInString(str) > *s.MaxLength {
			return &Error{path, fmt.Sprintf("must be at most %d characters", *s.MaxLength)}
		}
		if s.Pattern != nil && !s.Pattern.MatchString(str) {
			return &Error{path, "doesn't have the expected format"}
		}
	case "integer", "number":
		num, ok := v.(json.Number)
		if !ok {
			return &Error{path, "must be a number"}
		}
		if s.Type == "integer" && strings.ContainsAny(num.String(), ".eE") {
			return &Error{path, "must be a whole number"}
		}
		f, err := num.Float64()
		if err != nil || math.IsInf(f, 0) {
			return &Error{path, "must be a number"}
		}
		if s.Minimum != nil && f < *s.Minimum {
			return &Error{path, "must be at least " + trimFloat(*s.Minimum)}
		}
		if s.Maximum != nil && f > *s.Maximum {
			return &Error{path, "must be at most " + trimFloat(*s.Maximum)}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return &Error{path, "must be true or false"}
		}
	}
	return nil
}

func inEnum(enum []any, v any) bool {
	for _, e := range enum {
		switch x := e.(type) {
		case json.Number:
			if n, ok := v.(json.Number); ok {
				a, err1 := x.Float64()
				b, err2 := n.Float64()
				if err1 == nil && err2 == nil && a == b {
					return true
				}
			}
		default:
			if e == v {
				return true
			}
		}
	}
	return false
}

func enumList(enum []any) string {
	parts := make([]string, 0, len(enum))
	for _, e := range enum {
		b, _ := json.Marshal(e)
		parts = append(parts, string(b))
	}
	return strings.Join(parts, ", ")
}

func clip(s string) string {
	if len(s) > 64 || !utf8.ValidString(s) {
		return "(an argument)"
	}
	return s
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
