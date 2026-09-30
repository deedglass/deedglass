package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// Idempotency (contract §8): a POST with Idempotency-Key runs its handler at most once per
// (org, principal, key) in 24 hours. Every completed response is stored, 4xx and 5xx included,
// and replayed with Deedglass-Idempotent-Replayed: true.

// IdemTTL is how long a key is remembered.
const IdemTTL = 24 * time.Hour

// maxStoredBody is the largest response stored for replay (the table's CHECK).
const maxStoredBody = 1 << 20

// HeaderReplayed marks a replayed response.
const HeaderReplayed = "Deedglass-Idempotent-Replayed"

// IdemScope identifies one key. The key is scoped to (org, principal): two agents can use the
// same key string independently.
type IdemScope struct {
	OrgID     ids.OrgID
	Principal string // agt_…, usr_… or key_… (Principal.Ref)
	Key       string
	Method    string
	Route     string // the route pattern
}

// Outcome is what Begin decided.
type Outcome int

const (
	// Started: the caller owns the key and must Complete or Release it.
	Started Outcome = iota + 1
	// Replay: a stored response exists; send it.
	Replay
	// InProgress: another request holds the key.
	InProgress
	// Mismatch: the key was used with a different request.
	Mismatch
)

// StoredResponse is a completed response as stored for replay.
type StoredResponse struct {
	Status int
	Header map[string]string // the replayable headers only (storedHeaders)
	Body   []byte
}

// IdemStore stores keys. httpx defines it; internal/store implements it on Postgres
// (idempotency_keys, under RLS); cmd/deedglass/serve.go wires them together.
type IdemStore interface {
	// Begin claims the key, or reports why not. lockFor is how long the claim lasts before a retry
	// may take over (the route timeout plus 5 s). fp is the request fingerprint.
	Begin(ctx context.Context, s IdemScope, fp string, lockFor time.Duration) (Outcome, *StoredResponse, error)
	// Complete stores the response of a Started request.
	Complete(ctx context.Context, s IdemScope, r StoredResponse) error
	// Release forgets a Started request whose handler never ran to completion (409 in-use, 429,
	// 503) so a retry starts fresh.
	Release(ctx context.Context, s IdemScope) error
}

// storedHeaders are the response headers a replay repeats. Request ids and versions are the
// replaying request's own.
var storedHeaders = []string{"Content-Type", "Retry-After", "Allow", "Location", "WWW-Authenticate"}

// fingerprint is "sha256:" + hex(sha256(METHOD " " pattern "\n" path "\n" body)).
func fingerprint(method, pattern, path string, body []byte) string {
	h := sha256.New()
	io.WriteString(h, method+" "+pattern+"\n"+path+"\n")
	h.Write(body)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// ValidIdempotencyKey reports whether k is 1–255 printable ASCII characters.
func ValidIdempotencyKey(k string) bool { return len(k) >= 1 && len(k) <= 255 && printable(k) }

// idempotent decides whether the idempotency stage applies to this request.
func (s *Server) idempotent(rt Route, r *http.Request, ri *reqInfo) bool {
	if rt.Method != http.MethodPost || rt.Idempotency <= IdemNever {
		return false
	}
	_, has := r.Header["Idempotency-Key"]
	return has || rt.Idempotency == IdemRequired
}

func (s *Server) serveIdempotent(rt Route, w http.ResponseWriter, r *http.Request, ri *reqInfo) {
	rid := ri.requestID
	keys := r.Header.Values("Idempotency-Key")
	switch {
	case len(keys) == 0:
		writeError(w, rid, E(CodeIdempotencyKeyInvalid, Param("Idempotency-Key"), Msg("This endpoint needs an Idempotency-Key header. Send a new unique value, such as a UUID, with each new request.")))
		return
	case len(keys) > 1 || !ValidIdempotencyKey(keys[0]):
		writeError(w, rid, E(CodeIdempotencyKeyInvalid, Param("Idempotency-Key")))
		return
	}
	if ri.principal == nil || ri.principal.OrgID.IsZero() || ri.principal.Ref() == "" {
		// Public routes (IdemOptional) ignore a key: there is no principal to scope it to.
		s.invoke(rt, w, r)
		return
	}
	body, err := readBody(r)
	if err != nil {
		s.render(w, r, rt, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	scope := IdemScope{OrgID: ri.principal.OrgID, Principal: ri.principal.Ref(), Key: keys[0], Method: rt.Method, Route: rt.Pattern}
	fp := fingerprint(rt.Method, rt.Pattern, r.URL.Path, body)

	// The store is called with a context that survives the route timeout, so a slow handler's
	// response is still recorded.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	out, stored, err := s.o.Idempotency.Begin(sctx, scope, fp, rt.Timeout+5*time.Second)
	if err != nil {
		writeError(w, rid, s.internal(r, err))
		return
	}
	switch out {
	case Replay:
		ri.replayed = true
		h := w.Header()
		for k, v := range stored.Header {
			h.Set(k, v)
		}
		h.Set(HeaderReplayed, "true")
		h.Set("Content-Length", strconv.Itoa(len(stored.Body)))
		w.WriteHeader(stored.Status)
		_, _ = w.Write(stored.Body)
		return
	case InProgress:
		writeError(w, rid, E(CodeIdempotencyKeyInUse, WithHeader("Retry-After", "1")))
		return
	case Mismatch:
		writeError(w, rid, E(CodeIdempotencyConflict, Param("Idempotency-Key")))
		return
	case Started:
	default:
		writeError(w, rid, s.internal(r, errors.New("httpx: idempotency store returned no outcome")))
		return
	}

	cw := &captureWriter{header: http.Header{}}
	done := false
	defer func() {
		if done {
			return
		}
		p := recover()
		if p == nil || p == http.ErrAbortHandler { //nolint:errorlint // identity comparison
			_ = s.o.Idempotency.Release(sctx, scope)
			if p != nil {
				panic(p)
			}
			return
		}
		// A panic is stored as the same 500 the recoverer sends, then re-panicked for it.
		b := errorBody(E(CodeInternalError), rid)
		if err := s.o.Idempotency.Complete(sctx, scope, StoredResponse{Status: 500, Header: map[string]string{"Content-Type": ContentTypeJSON}, Body: b}); err != nil {
			s.log.ErrorContext(r.Context(), "idempotency: storing the response failed", "request_id", rid, "error", err.Error())
		}
		panic(p)
	}()
	// The handler writes to cw; the response is sent after it is stored.
	s.invoke(rt, cw, r)
	done = true

	status := cw.status
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable || cw.buf.Len() > maxStoredBody {
		if err := s.o.Idempotency.Release(sctx, scope); err != nil {
			s.log.ErrorContext(r.Context(), "idempotency: releasing the key failed", "request_id", rid, "error", err.Error())
		}
		if cw.buf.Len() > maxStoredBody {
			s.log.WarnContext(r.Context(), "idempotency: response too large to store; key released", "request_id", rid, "bytes", cw.buf.Len())
		}
	} else {
		sr := StoredResponse{Status: status, Header: map[string]string{}, Body: cw.buf.Bytes()}
		for _, k := range storedHeaders {
			if v := cw.header.Get(k); v != "" {
				sr.Header[k] = v
			}
		}
		if rt.Sensitive {
			sr.Body = redactJSON(sr.Body, rt.Redact)
		}
		if err := s.o.Idempotency.Complete(sctx, scope, sr); err != nil {
			s.log.ErrorContext(r.Context(), "idempotency: storing the response failed", "request_id", rid, "error", err.Error())
		}
	}
	h := w.Header()
	for k, vs := range cw.header {
		h[k] = vs
	}
	h.Set("Content-Length", strconv.Itoa(cw.buf.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(cw.buf.Bytes())
}

// redactJSON sets the named top-level members of a JSON object to null. A body that isn't an
// object is stored as a bare null, so a secret can never be stored by mistake.
func redactJSON(b []byte, fields []string) (out []byte) {
	defer func() {
		if recover() != nil {
			out = []byte("null\n") // never store an unredacted body
		}
	}()
	// Walk the object so member order and formatting stay as the handler wrote them.
	var buf bytes.Buffer
	dec := jsontext.NewDecoder(bytes.NewReader(b))
	enc := jsontext.NewEncoder(&buf, jsontext.WithIndent("  "))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return []byte("null\n")
	}
	_ = enc.WriteToken(jsontext.BeginObject)
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return []byte("null\n")
		}
		name := tok.String() // copy: the token is voided by the next read
		val, err := dec.ReadValue()
		if err != nil {
			return []byte("null\n")
		}
		_ = enc.WriteToken(jsontext.String(name))
		if contains(fields, name) {
			val = jsontext.Value("null")
		}
		if err := enc.WriteValue(val); err != nil {
			return []byte("null\n")
		}
	}
	_ = enc.WriteToken(jsontext.EndObject)
	return buf.Bytes()
}

// captureWriter buffers a response so it can be stored before it is sent.
type captureWriter struct {
	header http.Header
	status int
	wrote  bool
	buf    bytes.Buffer
}

func (c *captureWriter) Header() http.Header { return c.header }
func (c *captureWriter) WriteHeader(code int) {
	if c.wrote || (code >= 100 && code < 200) {
		return
	}
	c.status, c.wrote = code, true
}
func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	return c.buf.Write(b)
}
func (c *captureWriter) written() bool { return c.wrote }

// --- the in-memory store (tests, and servers without a database) ---

// MemoryStore is an IdemStore in memory. It has the Postgres store's semantics.
type MemoryStore struct {
	mu   sync.Mutex
	now  func() time.Time
	rows map[memKey]*memRow
}

type memKey struct{ org, principal, key string }

type memRow struct {
	fp          string
	complete    bool
	resp        StoredResponse
	lockedUntil time.Time
	expiresAt   time.Time
}

// NewMemoryStore returns an empty store. now defaults to time.Now.
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{now: now, rows: map[memKey]*memRow{}}
}

func mk(s IdemScope) memKey { return memKey{s.OrgID.String(), s.Principal, s.Key} }

// Begin implements IdemStore.
func (m *MemoryStore) Begin(_ context.Context, s IdemScope, fp string, lockFor time.Duration) (Outcome, *StoredResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	k := mk(s)
	row, ok := m.rows[k]
	switch {
	case !ok || !now.Before(row.expiresAt):
		m.rows[k] = &memRow{fp: fp, lockedUntil: now.Add(lockFor), expiresAt: now.Add(IdemTTL)}
		return Started, nil, nil
	case row.fp != fp:
		return Mismatch, nil, nil
	case row.complete:
		r := row.resp
		r.Body = append([]byte(nil), r.Body...)
		return Replay, &r, nil
	case now.Before(row.lockedUntil):
		return InProgress, nil, nil
	default: // the first attempt is presumed dead: take over
		row.lockedUntil = now.Add(lockFor)
		return Started, nil, nil
	}
}

// Complete implements IdemStore.
func (m *MemoryStore) Complete(_ context.Context, s IdemScope, r StoredResponse) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, ok := m.rows[mk(s)]; ok && !row.complete {
		r.Body = append([]byte(nil), r.Body...)
		row.resp, row.complete = r, true
	}
	return nil
}

// Release implements IdemStore.
func (m *MemoryStore) Release(_ context.Context, s IdemScope) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row, ok := m.rows[mk(s)]; ok && !row.complete {
		delete(m.rows, mk(s))
	}
	return nil
}

// Prune deletes expired keys and returns how many.
func (m *MemoryStore) Prune() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now, n := m.now(), 0
	for k, row := range m.rows {
		if !now.Before(row.expiresAt) {
			delete(m.rows, k)
			n++
		}
	}
	return n
}

// Stored returns the stored body for a key (tests: the secret never reaches the store).
func (m *MemoryStore) Stored(s IdemScope) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[mk(s)]
	if !ok || !row.complete {
		return nil, false
	}
	return append([]byte(nil), row.resp.Body...), true
}

var _ IdemStore = (*MemoryStore)(nil)
