package connector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ValidateArgs checks args against the small JSON Schema subset the S13 tools use: type (object,
// string, integer, number, boolean), properties, required, additionalProperties: false, minimum,
// maximum and maxLength. Anything else in the schema is ignored. S16's connector/schema package
// brings the full subset validator and replaces this one; the error shape stays.
//
// The error is a *Error of kind invalid_request with code validation_failed and a sentence the
// agent can act on.
func ValidateArgs(schema, args json.RawMessage) error {
	if len(bytes.TrimSpace(schema)) == 0 {
		return nil
	}
	var s node
	if err := json.Unmarshal(schema, &s); err != nil {
		return &Error{Kind: KindConfig, Code: "connector_misconfigured", AgentMessage: "This tool's argument schema can't be read. An admin can check Console › Connectors.", Err: err}
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return invalid("args", "isn't valid JSON")
	}
	return s.check("args", v)
}

type node struct {
	Type                 string           `json:"type"`
	Properties           map[string]*node `json:"properties"`
	Required             []string         `json:"required"`
	AdditionalProperties *bool            `json:"additionalProperties"`
	Minimum              *float64         `json:"minimum"`
	Maximum              *float64         `json:"maximum"`
	MaxLength            *int             `json:"maxLength"`
}

func invalid(path, what string) error {
	return &Error{Kind: KindInvalidRequest, Code: "validation_failed",
		AgentMessage: fmt.Sprintf("%s %s. Check the tool's input schema and send the call again.", path, what)}
}

func (n *node) check(path string, v any) error {
	switch n.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return invalid(path, "must be an object")
		}
		for _, r := range n.Required {
			if _, ok := m[r]; !ok {
				return invalid(path+"."+r, "is required")
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p, known := n.Properties[k]
			if !known {
				if n.AdditionalProperties != nil && !*n.AdditionalProperties {
					return invalid(path+"."+k, "isn't an argument this tool takes")
				}
				continue
			}
			if err := p.check(path+"."+k, m[k]); err != nil {
				return err
			}
		}
	case "string":
		s, ok := v.(string)
		if !ok {
			return invalid(path, "must be a string")
		}
		if n.MaxLength != nil && len([]rune(s)) > *n.MaxLength {
			return invalid(path, fmt.Sprintf("must be at most %d characters", *n.MaxLength))
		}
	case "integer", "number":
		num, ok := v.(json.Number)
		if !ok {
			return invalid(path, "must be a number")
		}
		if n.Type == "integer" && strings.ContainsAny(num.String(), ".eE") {
			return invalid(path, "must be a whole number")
		}
		f, err := num.Float64()
		if err != nil {
			return invalid(path, "must be a number")
		}
		if n.Minimum != nil && f < *n.Minimum {
			return invalid(path, fmt.Sprintf("must be at least %s", trimFloat(*n.Minimum)))
		}
		if n.Maximum != nil && f > *n.Maximum {
			return invalid(path, fmt.Sprintf("must be at most %s", trimFloat(*n.Maximum)))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return invalid(path, "must be true or false")
		}
	}
	return nil
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%f", f), "0"), ".")
}
