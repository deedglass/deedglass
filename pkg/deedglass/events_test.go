package deedglass

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestEventStream: SSE parsing (comments, multi-line data, CRLF), Last-Event-ID on reconnect, the
// backoff schedule reset after a live connection, 5xx and 429 retried, other 4xx final,
// session_ended final.
func TestEventStream(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	conn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conn++
		n := conn
		seen = append(seen, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer dg_test_k" || r.URL.Query().Get("types") != "action.*" || r.URL.Query().Get("agent") != "agt_1" {
			w.WriteHeader(400)
			return
		}
		switch n {
		case 1:
			fmt.Fprint(w, "retry: 3000\r\n\r\nid: i.0\r\nevent: hello\r\ndata: {\"resumed\":false}\r\n\r\n: ping\n\nid: i.1\nevent: action.created\ndata: {\"a\":\ndata: 1}\n\n")
		case 2:
			w.WriteHeader(503)
		case 3:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
		case 4:
			fmt.Fprint(w, "id: i.1\nevent: hello\ndata: {\"resumed\":true}\n\nevent: session_ended\ndata: {\"code\":\"session_expired\",\"message\":\"Sign in again.\"}\n\n")
		}
	}))
	defer srv.Close()
	var waits []time.Duration
	var got []StreamEvent
	s := &EventStream{Client: &Client{BaseURL: srv.URL, APIKey: "dg_test_k"}, Types: []string{"action.*"}, Agent: "agt_1",
		Jitter: func() float64 { return 0.5 }, Sleep: func(context.Context, time.Duration) error { return nil },
		OnReconnect: func(d time.Duration, _ error) { waits = append(waits, d) }}
	err := s.Run(context.Background(), func(ev StreamEvent) error { got = append(got, ev); return nil })
	var se *SessionEndedError
	if !errors.As(err, &se) || se.Code != "session_expired" {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 4 || got[1].Type != "action.created" || string(got[1].Data) != "{\"a\":\n1}" || got[1].ID != "i.1" || got[3].Type != "session_ended" {
		t.Fatalf("events: %+v", got)
	}
	if fmt.Sprint(seen) != "[ i.1 i.1 i.1]" {
		t.Fatalf("Last-Event-ID: %q", seen)
	}
	if fmt.Sprint(waits) != "[1s 2s 4s]" {
		t.Fatalf("waits: %v", waits)
	}

	// A 4xx (other than 429) ends Run with the API error.
	bad := &EventStream{Client: &Client{BaseURL: srv.URL, APIKey: "nope"}, Sleep: func(context.Context, time.Duration) error { return nil }}
	var ae *Error
	if err := bad.Run(context.Background(), func(StreamEvent) error { return nil }); !errors.As(err, &ae) || ae.Status != 400 {
		t.Fatalf("400: %v", err)
	}
}

// A silent connection is dropped after IdleTimeout and the stream reconnects; ctx ends Run with nil.
func TestEventStreamIdle(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "event: hello\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	reconnects := 0
	s := &EventStream{Client: &Client{BaseURL: srv.URL}, IdleTimeout: 50 * time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil },
		OnReconnect: func(time.Duration, error) {
			if reconnects++; reconnects == 2 {
				cancel()
			}
		}}
	if err := s.Run(ctx, func(StreamEvent) error { return nil }); err != nil || reconnects != 2 {
		t.Fatalf("%v after %d reconnects", err, reconnects)
	}
}
