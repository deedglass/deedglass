package front

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// ParamHeader is one x-mcp-header annotation: the header name part and the property path.
type ParamHeader struct {
	Name string   // "Region" → Mcp-Param-Region
	Path []string // properties chain from the schema root
	Type string   // string, integer or boolean
}

// ParamHeaders lists a tool's valid x-mcp-header annotations, statically reachable through
// properties only (the spec's constraint). ok=false when an annotation is invalid: a conforming
// client excludes such a tool, so the gateway doesn't serve it either.
func ParamHeaders(schema json.RawMessage) (out []ParamHeader, ok bool) {
	if len(schema) == 0 {
		return nil, true
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(schema, &root) != nil {
		return nil, true
	}
	seen := map[string]bool{}
	ok = walkProps(root, nil, &out, seen, 0)
	if !ok {
		return nil, false
	}
	if hasStrayAnnotation(schema, len(out)) {
		return nil, false
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, true
}

func walkProps(node map[string]json.RawMessage, path []string, out *[]ParamHeader, seen map[string]bool, depth int) bool {
	if depth > 32 {
		return false
	}
	var props map[string]json.RawMessage
	if json.Unmarshal(node["properties"], &props) != nil {
		return true
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var child map[string]json.RawMessage
		if json.Unmarshal(props[k], &child) != nil {
			continue
		}
		p := append(append([]string{}, path...), k)
		if raw, has := child["x-mcp-header"]; has {
			var name, typ string
			if json.Unmarshal(raw, &name) != nil || !isToken(name) || seen[strings.ToLower(name)] {
				return false
			}
			_ = json.Unmarshal(child["type"], &typ)
			if typ != "string" && typ != "integer" && typ != "boolean" {
				return false
			}
			seen[strings.ToLower(name)] = true
			*out = append(*out, ParamHeader{Name: name, Path: p, Type: typ})
		}
		if !walkProps(child, p, out, seen, depth+1) {
			return false
		}
	}
	return true
}

// hasStrayAnnotation: an x-mcp-header anywhere the properties walk doesn't reach (items, oneOf,
// $ref targets, …) makes the tool definition invalid.
func hasStrayAnnotation(schema json.RawMessage, reached int) bool {
	return bytes.Count(schema, []byte(`"x-mcp-header"`)) > reached
}

// isToken is RFC 9110's 1*tchar.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
