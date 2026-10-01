package httpgw

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/mcp/front"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
)

// Base64 sentinel (MCP 2026-07-28 "Value Encoding"): =?base64?<standard base64 of UTF-8>?=, the
// markers lowercase and exact.
const (
	sentinelPrefix = "=?base64?"
	sentinelSuffix = "?="
)

// DecodeHeader reads a mirrored header value: plain visible ASCII (with inner spaces and tabs),
// or the base64 sentinel, which must decode to valid UTF-8. ok=false for anything else.
func DecodeHeader(v string) (string, bool) {
	if strings.HasPrefix(v, sentinelPrefix) && strings.HasSuffix(v, sentinelSuffix) && len(v) >= len(sentinelPrefix)+len(sentinelSuffix) {
		b, err := base64.StdEncoding.Strict().DecodeString(v[len(sentinelPrefix) : len(v)-len(sentinelSuffix)])
		if err != nil || !utf8.Valid(b) {
			return "", false
		}
		return string(b), true
	}
	if v != strings.TrimSpace(v) {
		return "", false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c != '\t' && (c < 0x20 || c > 0x7e) {
			return "", false
		}
	}
	return v, true
}

// EncodeHeader is DecodeHeader's inverse: plain when the value is safe, else the sentinel. A
// plain value that looks like a sentinel is encoded too, so it can't be misread.
func EncodeHeader(v string) string {
	safe := v == strings.TrimSpace(v) && !(strings.HasPrefix(v, sentinelPrefix) && strings.HasSuffix(v, sentinelSuffix))
	for i := 0; safe && i < len(v); i++ {
		if c := v[i]; c != '\t' && (c < 0x20 || c > 0x7e) {
			safe = false
		}
	}
	if safe {
		return v
	}
	return sentinelPrefix + base64.StdEncoding.EncodeToString([]byte(v)) + sentinelSuffix
}

// checkParams validates Mcp-Param-* for a tools/call: every annotated argument that is present
// (and not null) needs its header with an equal value; a header without its argument is a
// mismatch too. It returns the -32020 message, or nil.
func checkParams(h http.Header, env jsonrpc.Envelope, schema func(string) json.RawMessage) *string {
	cp, ok := front.ParseCall(env["params"])
	if !ok {
		return nil
	}
	s := schema(cp.Name)
	if len(s) == 0 {
		return nil
	}
	anns, valid := front.ParamHeaders(s)
	if !valid {
		return nil // the front refuses the tool; nothing to compare
	}
	var args map[string]json.RawMessage
	_ = json.Unmarshal(cp.Arguments, &args)
	for _, a := range anns {
		header := HParamPrefix + a.Name
		val, present := lookupPath(args, a.Path)
		raw, hasH := single(h, header)
		if !present {
			if hasH {
				m := "Header mismatch: " + header + " is sent, but arguments has no " + strings.Join(a.Path, ".") + ". Leave the header out when the argument is absent."
				return &m
			}
			continue
		}
		bodyText := scalarText(val)
		if !hasH {
			m := gate.MCPHeaderMissingText(header, bodyText)
			return &m
		}
		got, ok := DecodeHeader(raw)
		if !ok {
			m := gate.MCPHeaderInvalidText(header)
			return &m
		}
		if !sameValue(got, val, a.Type) {
			m := gate.MCPHeaderMismatchText(header, got, strings.Join(a.Path, "."), bodyText)
			return &m
		}
	}
	return nil
}

// lookupPath reads args at a properties path; null counts as absent.
func lookupPath(args map[string]json.RawMessage, path []string) (json.RawMessage, bool) {
	cur := args
	for i, k := range path {
		v, ok := cur[k]
		if !ok || string(v) == "null" {
			return nil, false
		}
		if i == len(path)-1 {
			return v, true
		}
		if json.Unmarshal(v, &cur) != nil {
			return nil, false
		}
	}
	return nil, false
}

func scalarText(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return string(v)
}

// sameValue compares a decoded header with the body value: strings exactly, integers
// numerically (42 and 42.0 are equal), booleans as lowercase true/false.
func sameValue(header string, body json.RawMessage, typ string) bool {
	switch typ {
	case "integer":
		hn, err1 := strconv.ParseFloat(header, 64)
		var bn float64
		err2 := json.Unmarshal(body, &bn)
		return err1 == nil && err2 == nil && !math.IsNaN(hn) && hn == bn
	case "boolean":
		var b bool
		return json.Unmarshal(body, &b) == nil && header == strconv.FormatBool(b)
	}
	var s string
	if json.Unmarshal(body, &s) != nil {
		return false
	}
	return header == s
}
