package httpx

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"strconv"
	"time"
)

// Content types.
const (
	ContentTypeJSON = "application/json; charset=utf-8"
	ContentTypeYAML = "application/yaml" // RFC 9512
)

// encodeOpts: 2-space indentation, nil slices as [], no HTML escaping, deterministic member order
// (struct order; maps sorted).
var encodeOpts = json.JoinOptions(jsontext.WithIndent("  "), json.Deterministic(true))

// MarshalJSON encodes v the way every response does, with a trailing newline.
func MarshalJSON(v any) ([]byte, error) {
	b, err := json.Marshal(v, encodeOpts)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// WriteJSON writes v with status. An encoding failure is a programming error: it is logged by the
// caller's pipeline as internal_error.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	b, err := MarshalJSON(v)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", ContentTypeJSON)
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, err = w.Write(b)
	return err
}

// envelope is the error body, exactly: {"error":{type, code, message, param, request_id, doc_url}}.
type envelope struct {
	Error envelopeError `json:"error"`
}

type envelopeError struct {
	Type      ErrType `json:"type"`
	Code      Code    `json:"code"`
	Message   string  `json:"message"`
	Param     *string `json:"param"`
	RequestID string  `json:"request_id"`
	DocURL    string  `json:"doc_url"`
	// Diagnostics is policy_invalid's additive field (S12); absent on every other code.
	Diagnostics any `json:"diagnostics,omitempty"`
	// Action is the sealed action on POST /v1/actions policy, permission and rate-limit errors
	// (S13, Stripe-style): the refusal was recorded, and this is its record.
	Action any `json:"action,omitempty"`
	// Checks is undo_unavailable's additive field (S23): every check, with its fix.
	Checks any `json:"checks,omitempty"`
}

// errorBody returns the envelope bytes for e under request id rid.
func errorBody(e *Error, rid string) []byte {
	env := envelope{Error: envelopeError{
		Type: e.Type, Code: e.Code, Message: e.Message, RequestID: rid, DocURL: DocURLPrefix + string(e.Code), Diagnostics: e.Diagnostics,
		Action: e.Action, Checks: e.Checks,
	}}
	if e.Param != "" {
		p := e.Param
		env.Error.Param = &p
	}
	b, err := MarshalJSON(env)
	if err != nil { // cannot happen: every field is a plain string or plain data
		return []byte(`{"error":{"type":"api_error","code":"internal_error"}}` + "\n")
	}
	return b
}

// writeError renders e. The caller has already logged any cause.
func writeError(w http.ResponseWriter, rid string, e *Error) {
	h := w.Header()
	for k, vs := range e.Header {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	// Every 401 names the scheme (RFC 7235): a client learns it can retry with a bearer key.
	if e.Status == http.StatusUnauthorized && h.Get("WWW-Authenticate") == "" {
		h.Set("WWW-Authenticate", `Bearer realm="deedglass"`)
	}
	b := errorBody(e, rid)
	h.Set("Content-Type", ContentTypeJSON)
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Del("Content-Encoding")
	w.WriteHeader(e.Status)
	_, _ = w.Write(b)
}

// Time is a timestamp as the API shows it: RFC 3339 UTC with milliseconds
// ("2026-10-01T09:30:00.123Z"). Nullable fields use *Time.
type Time time.Time

// TimeLayout is the API's timestamp layout.
const TimeLayout = "2006-01-02T15:04:05.000Z07:00"

// MarshalText implements encoding.TextMarshaler.
func (t Time) MarshalText() ([]byte, error) {
	return []byte(time.Time(t).UTC().Format(TimeLayout)), nil
}

// UnmarshalText parses RFC 3339 (any precision) and keeps it in UTC.
func (t *Time) UnmarshalText(b []byte) error {
	v, err := time.Parse(time.RFC3339Nano, string(b))
	if err != nil {
		return err
	}
	*t = Time(v.UTC())
	return nil
}

// Money is an amount in integer minor units plus a lowercase ISO 4217 currency.
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// List is every list response: {"object":"list","data":[…],"has_more":bool,"next_cursor":"…"|null}.
type List[T any] struct {
	Object     string  `json:"object"`
	Data       []T     `json:"data"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}

// NewList builds a list page. data is never rendered as null.
func NewList[T any](data []T, hasMore bool, next *Cursor) List[T] {
	if data == nil {
		data = []T{}
	}
	l := List[T]{Object: "list", Data: data, HasMore: hasMore}
	if next != nil && hasMore {
		s := next.Encode()
		l.NextCursor = &s
	}
	return l
}

// Cursor is an opaque position in a list: the sort key of the last row plus its id. Cursors are
// unsigned on purpose: row-level security scopes every query, so a crafted cursor can only move
// within the caller's own org (docs/API.md).
type Cursor struct {
	V  int              `json:"v"`
	K  []jsontext.Value `json:"k"`
	ID string           `json:"id"`
}

// NewCursor builds a cursor from sort-key values (strings, numbers, Time) and the row id.
func NewCursor(id string, key ...any) (*Cursor, error) {
	c := &Cursor{V: 1, ID: id, K: make([]jsontext.Value, 0, len(key))}
	for _, k := range key {
		b, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		c.K = append(c.K, b)
	}
	return c, nil
}

// Encode returns the cursor as base64url without padding.
func (c *Cursor) Encode() string {
	b, _ := json.Marshal(c) // plain values, cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// Key decodes sort-key element i into v.
func (c *Cursor) Key(i int, v any) error {
	if i < 0 || i >= len(c.K) {
		return errBadCursor()
	}
	if err := json.Unmarshal(c.K[i], v); err != nil {
		return errBadCursor()
	}
	return nil
}

func errBadCursor() *Error {
	return ErrValidation("cursor", "`cursor` isn't a cursor this API issued. Pass the next_cursor value from the previous page unchanged.")
}

// DecodeCursor parses a cursor from a request. Anything malformed is validation_failed on "cursor".
func DecodeCursor(s string) (*Cursor, error) {
	if len(s) == 0 || len(s) > 1024 {
		return nil, errBadCursor()
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errBadCursor()
	}
	var c Cursor
	if err := json.Unmarshal(b, &c, json.RejectUnknownMembers(true)); err != nil || c.V != 1 || c.ID == "" {
		return nil, errBadCursor()
	}
	return &c, nil
}

// ListParams are the list query parameters.
type ListParams struct {
	Limit  int     // 1–100, default 20
	Cursor *Cursor // nil on the first page
}

// DefaultLimit and MaxLimit bound ?limit=.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// ParseListParams reads ?limit= and ?cursor=.
func ParseListParams(r *http.Request) (ListParams, error) {
	q := r.URL.Query()
	p := ListParams{Limit: DefaultLimit}
	if v := q.Get("limit"); v != "" || q.Has("limit") {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxLimit {
			return p, ErrValidation("limit", "`limit` must be between 1 and 100.")
		}
		p.Limit = n
	}
	if q.Has("cursor") {
		c, err := DecodeCursor(q.Get("cursor"))
		if err != nil {
			return p, err
		}
		p.Cursor = c
	}
	return p, nil
}
