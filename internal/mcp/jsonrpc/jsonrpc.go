// Package jsonrpc is the MCP stdio framing the wrapper relays (S14): one JSON-RPC 2.0 message per
// line, UTF-8, no embedded newlines. An Envelope keeps every top-level member as raw bytes, so the
// members the wrapper doesn't touch (params, result, _meta, extensions it has never heard of)
// round-trip byte for byte.
package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
)

// Error codes (JSON-RPC 2.0 and MCP 2026-07-28's reserved range).
const (
	CodeParseError         = -32700
	CodeInvalidRequest     = -32600
	CodeMethodNotFound     = -32601
	CodeInvalidParams      = -32602
	CodeInternal           = -32603
	CodeHeaderMismatch     = -32020
	CodeUnsupportedVersion = -32022
)

// MetaProtocolVersion and friends are the 2026-07-28 per-request _meta keys.
const (
	MetaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	MetaClientInfo         = "io.modelcontextprotocol/clientInfo"
	MetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
)

// Kind is what a message is.
type Kind int

// Message kinds.
const (
	Invalid Kind = iota
	Request
	Notification
	Response
)

func (k Kind) String() string {
	return [...]string{"invalid", "request", "notification", "response"}[k]
}

// Envelope is one message, member by member, as raw JSON.
type Envelope map[string]json.RawMessage

// Kind classifies the message: a method with an id is a request, a method without one a
// notification, and an id with result or error (and no method) a response.
func (e Envelope) Kind() Kind {
	_, hasID := e["id"]
	_, hasMethod := e["method"]
	_, hasResult := e["result"]
	_, hasError := e["error"]
	switch {
	case hasMethod && hasID:
		return Request
	case hasMethod:
		return Notification
	case hasID && (hasResult != hasError):
		return Response
	}
	return Invalid
}

// ID is the raw id, nil for notifications.
func (e Envelope) ID() json.RawMessage { return e["id"] }

// Method is the method name, "" when absent or not a string.
func (e Envelope) Method() string {
	var m string
	if raw, ok := e["method"]; ok && json.Unmarshal(raw, &m) == nil {
		return m
	}
	return ""
}

// Meta returns params._meta as raw members (nil when absent).
func (e Envelope) Meta() map[string]json.RawMessage {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if raw, ok := e["params"]; ok {
		_ = json.Unmarshal(raw, &p)
	}
	return p.Meta
}

// ModernVersion is params._meta["io.modelcontextprotocol/protocolVersion"]: the protocol version
// of a 2026-07-28-era request, "" for a legacy one.
func (e Envelope) ModernVersion() string {
	var v string
	if raw, ok := e.Meta()[MetaProtocolVersion]; ok && json.Unmarshal(raw, &v) == nil {
		return v
	}
	return ""
}

// order is the member order Marshal writes; other members follow, sorted.
var order = []string{"jsonrpc", "id", "method", "params", "result", "error"}

// Marshal writes the envelope as one line (no trailing newline). Raw member values are written
// verbatim; only the member order may differ from the input.
func (e Envelope) Marshal() []byte {
	var b bytes.Buffer
	b.Grow(e.size())
	b.WriteByte('{')
	first := true
	write := func(k string, v json.RawMessage) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.Write(quote(k))
		b.WriteByte(':')
		b.Write(v)
	}
	for _, k := range order {
		if v, ok := e[k]; ok {
			write(k, v)
		}
	}
	rest := make([]string, 0, len(e))
	for k := range e {
		if !isOrdered(k) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		write(k, e[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (e Envelope) size() int {
	n := 2
	for k, v := range e {
		n += len(k) + len(v) + 4
	}
	return n
}

func isOrdered(k string) bool {
	for _, o := range order {
		if o == k {
			return true
		}
	}
	return false
}

// quote is a JSON string without HTML escaping.
func quote(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// MarshalObject encodes members as one JSON object, raw values verbatim, keys sorted.
func MarshalObject(m map[string]json.RawMessage) json.RawMessage {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(quote(k))
		b.WriteByte(':')
		b.Write(m[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

// Value encodes v without HTML escaping and without a trailing newline.
func Value(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic("jsonrpc: " + err.Error()) // only called with plain values
	}
	return bytes.TrimRight(b.Bytes(), "\n")
}

// String is a JSON string value.
func String(s string) json.RawMessage { return quote(s) }

// Int is a JSON number value.
func Int(n int64) json.RawMessage { return json.RawMessage(strconv.FormatInt(n, 10)) }

// Null is the JSON null.
var Null = json.RawMessage("null")

var version = json.RawMessage(`"2.0"`)

// NewResponse is a result response for id.
func NewResponse(id, result json.RawMessage) Envelope {
	if id == nil {
		id = Null
	}
	return Envelope{"jsonrpc": version, "id": id, "result": result}
}

// ErrorObject is a JSON-RPC error.
type ErrorObject struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// NewError is an error response for id (null when id is nil).
func NewError(id json.RawMessage, code int, message string) Envelope {
	if id == nil {
		id = Null
	}
	return Envelope{"jsonrpc": version, "id": id, "error": Value(ErrorObject{Code: code, Message: message})}
}

// NewRequest is a request with an integer id.
func NewRequest(id int64, method string, params json.RawMessage) Envelope {
	e := Envelope{"jsonrpc": version, "id": Int(id), "method": String(method)}
	if params != nil {
		e["params"] = params
	}
	return e
}

// NewNotification is a notification.
func NewNotification(method string, params json.RawMessage) Envelope {
	e := Envelope{"jsonrpc": version, "method": String(method)}
	if params != nil {
		e["params"] = params
	}
	return e
}

// Framing errors. A Reader never returns a partial message: after ErrTooLarge it has skipped to
// the next newline.
var (
	ErrTooLarge = errors.New("jsonrpc: message larger than the line limit")
	ErrParse    = errors.New("jsonrpc: not valid JSON")
	ErrBatch    = errors.New("jsonrpc: batches are not supported")
	ErrTooDeep  = errors.New("jsonrpc: nested too deeply")
	ErrNotUTF8  = errors.New("jsonrpc: not valid UTF-8")
	ErrShape    = errors.New("jsonrpc: not a JSON-RPC 2.0 message")
)

// Reply is the JSON-RPC error a framing error deserves (code and message), for the sender.
func Reply(err error) (int, string) {
	switch {
	case errors.Is(err, ErrTooLarge):
		return CodeInvalidRequest, "The message is larger than 4 MiB."
	case errors.Is(err, ErrBatch):
		return CodeInvalidRequest, "Batches are not supported."
	case errors.Is(err, ErrTooDeep):
		return CodeInvalidRequest, "The message is nested more than 128 levels deep."
	case errors.Is(err, ErrShape):
		return CodeInvalidRequest, "The message isn't a JSON-RPC 2.0 request, notification or response."
	}
	return CodeParseError, "The message isn't valid JSON."
}
