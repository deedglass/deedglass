// Package saastest serves recorded HTTP exchanges for SaaS connector tests (S29). Exchanges come
// from internal/connector/<type>/testdata/*.json and are served in order: each request must match
// the next unconsumed exchange (or a repeatable one), otherwise the test fails with a diff of what
// arrived and what was expected. No live network call ever runs in make ci.
package saastest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exchange is one recorded request and its answer.
type Exchange struct {
	Name           string            `json:"name"`
	Method         string            `json:"method"`
	Path           string            `json:"path"`            // the decoded path ("/repos/a/b/contents/docs/x.md")
	Query          map[string]string `json:"query,omitempty"` // each must be present with this value
	RequireHeaders map[string]string `json:"require_headers,omitempty"`
	AbsentHeaders  []string          `json:"absent_headers,omitempty"` // must not be sent
	BodySubset     map[string]any    `json:"body_subset,omitempty"`    // JSON body keys (or form fields) that must match
	Status         int               `json:"status"`
	Headers        map[string]string `json:"headers,omitempty"`
	Body           json.RawMessage   `json:"body,omitempty"`
	DelayMS        int               `json:"delay_ms,omitempty"`
	// Repeat: the exchange answers any number of matching requests and is never consumed (a
	// cached lookup, a list a test calls twice).
	Repeat bool `json:"repeat,omitempty"`
	// Optional: the exchange may be skipped (a retry that may or may not happen).
	Optional bool `json:"optional,omitempty"`
}

// Recorded is one request the server received.
type Recorded struct {
	Name   string // the exchange it matched; "" when it matched nothing
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// Server is an httptest server over exchanges.
type Server struct {
	*httptest.Server
	t testing.TB

	mu        sync.Mutex
	exchanges []Exchange
	used      []bool
	requests  []Recorded
	failures  []string
}

// Load reads a JSON array of exchanges from path.
func Load(t testing.TB, path string) []Exchange {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("saastest: %v", err)
	}
	var xs []Exchange
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&xs); err != nil {
		t.Fatalf("saastest: %s: %v", path, err)
	}
	return xs
}

// New starts a server over exchanges. At cleanup it fails the test if a request matched nothing
// or a required exchange was never used.
func New(t testing.TB, exchanges ...Exchange) *Server {
	t.Helper()
	s := &Server{t: t, exchanges: exchanges, used: make([]bool, len(exchanges))}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		s.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, f := range s.failures {
			t.Errorf("saastest: %s", f)
		}
		for i, x := range s.exchanges {
			if !s.used[i] && !x.Repeat && !x.Optional {
				t.Errorf("saastest: exchange %q (%s %s) was never requested", x.Name, x.Method, x.Path)
			}
		}
	})
	return s
}

// Add appends exchanges (a test that learns an id from an earlier answer).
func (s *Server) Add(xs ...Exchange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exchanges = append(s.exchanges, xs...)
	s.used = append(s.used, make([]bool, len(xs))...)
}

// Count is how many requests matched the exchange named name.
func (s *Server) Count(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.requests {
		if r.Name == name {
			n++
		}
	}
	return n
}

// Requests returns every request received, in order.
func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Recorded(nil), s.requests...)
}

// Bodies returns every request body and header value received (the sentinel sweep).
func (s *Server) Bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.requests {
		out = append(out, string(r.Body))
	}
	return out
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	rec := Recorded{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body}
	s.mu.Lock()
	idx, why := s.match(r, body)
	if idx < 0 {
		s.failures = append(s.failures, fmt.Sprintf("unexpected request %s %s?%s\n%s", r.Method, r.URL.Path, r.URL.RawQuery, why))
		s.requests = append(s.requests, rec)
		s.mu.Unlock()
		http.Error(w, `{"error":"saastest: no exchange matches"}`, http.StatusTeapot)
		return
	}
	x := s.exchanges[idx]
	s.used[idx] = true
	rec.Name = x.Name
	s.requests = append(s.requests, rec)
	s.mu.Unlock()
	if x.DelayMS > 0 {
		select {
		case <-time.After(time.Duration(x.DelayMS) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	for k, v := range x.Headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	st := x.Status
	if st == 0 {
		st = 200
	}
	w.WriteHeader(st)
	_, _ = w.Write(x.Body)
}

// match finds the exchange for a request: the first unconsumed non-repeat one in order (optional
// ones may be skipped), else a repeatable one. why explains the nearest miss.
func (s *Server) match(r *http.Request, body []byte) (int, string) {
	var why []string
	for i, x := range s.exchanges {
		if x.Repeat || s.used[i] {
			continue
		}
		if d := diff(x, r, body); d == "" {
			return i, ""
		} else {
			why = append(why, fmt.Sprintf("  next %q: %s", x.Name, d))
		}
		if !x.Optional {
			break
		}
	}
	for i, x := range s.exchanges {
		if x.Repeat && diff(x, r, body) == "" {
			return i, ""
		}
	}
	if len(why) == 0 {
		why = append(why, "  no exchange is left")
	}
	return -1, strings.Join(why, "\n")
}

func diff(x Exchange, r *http.Request, body []byte) string {
	if !strings.EqualFold(x.Method, r.Method) {
		return fmt.Sprintf("method %s, want %s", r.Method, x.Method)
	}
	if x.Path != r.URL.Path {
		return fmt.Sprintf("path %s, want %s", r.URL.Path, x.Path)
	}
	q := r.URL.Query()
	keys := make([]string, 0, len(x.Query))
	for k := range x.Query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if got := q.Get(k); got != x.Query[k] {
			return fmt.Sprintf("query %s=%q, want %q", k, got, x.Query[k])
		}
	}
	for k, v := range x.RequireHeaders {
		if got := r.Header.Get(k); got != v {
			return fmt.Sprintf("header %s=%q, want %q", k, redactHeader(k, got), redactHeader(k, v))
		}
	}
	for _, k := range x.AbsentHeaders {
		if r.Header.Get(k) != "" {
			return "header " + k + " must not be sent"
		}
	}
	if len(x.BodySubset) > 0 {
		got := map[string]any{}
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			f, _ := url.ParseQuery(string(body))
			for k := range f {
				got[k] = f.Get(k)
			}
		} else if err := json.Unmarshal(body, &got); err != nil {
			return "body isn't a JSON object"
		}
		for k, want := range x.BodySubset {
			if !reflect.DeepEqual(normalize(got[k]), normalize(want)) {
				return fmt.Sprintf("body %s=%s, want %s", k, show(got[k]), show(want))
			}
		}
	}
	return ""
}

func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func show(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}

func redactHeader(k, v string) string {
	if strings.EqualFold(k, "Authorization") && len(v) > 12 {
		return v[:10] + "…"
	}
	return v
}
