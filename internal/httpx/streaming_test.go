package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// S24: a Streaming route has no route timeout (the handler owns its lifetime), must be a GET, and
// takes no body.
func TestStreamingRoute(t *testing.T) {
	mustPanic(t, "a Streaming route must be a GET", func() {
		New(Options{}).Mount(Route{Method: "POST", Pattern: "/v1/x", OperationID: "x", Auth: Public, Idempotency: IdemNever,
			Extension: "test", Streaming: true, Handler: ok})
	})
	mustPanic(t, "takes no request body", func() {
		New(Options{}).Mount(Route{Method: "GET", Pattern: "/v1/x", OperationID: "x", Auth: Public, MaxBody: 10,
			Extension: "test", Streaming: true, Handler: ok})
	})
	s := New(Options{})
	deadline := map[string]bool{}
	probe := func(name string) HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) error {
			_, deadline[name] = r.Context().Deadline()
			return nil
		}
	}
	s.Mount(
		Route{Method: "GET", Pattern: "/v1/stream", OperationID: "s", Auth: Public, Extension: "test", Streaming: true, Handler: probe("stream")},
		Route{Method: "GET", Pattern: "/v1/plain", OperationID: "p", Auth: Public, Extension: "test", Handler: probe("plain")},
	)
	for _, p := range []string{"/v1/stream", "/v1/plain"} {
		s.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	if deadline["stream"] || !deadline["plain"] {
		t.Fatalf("deadlines: %v", deadline)
	}
}

// flipKeys accepts goodKey until revoked.
type flipKeys struct{ revoked atomic.Bool }

func (f *flipKeys) AuthenticateKey(ctx context.Context, bearer string) (Principal, error) {
	if f.revoked.Load() {
		return Principal{}, E(CodeAPIKeyRevoked)
	}
	return fakeKeys{}.AuthenticateKey(ctx, bearer)
}

// Recheck re-runs the route's authentication with live answers (S24: the stream's 60 s re-check).
func TestRecheck(t *testing.T) {
	keys := &flipKeys{}
	s := New(Options{Keys: keys})
	var first, second error
	s.Mount(Route{Method: "GET", Pattern: "/v1/stream", OperationID: "s", Auth: AgentKey, Scopes: []string{"actions:read"},
		Extension: "test", Streaming: true, Handler: func(w http.ResponseWriter, r *http.Request) error {
			first = s.Recheck(r)
			keys.revoked.Store(true)
			second = s.Recheck(r)
			return nil
		}})
	r := httptest.NewRequest("GET", "/v1/stream", nil)
	r.Header.Set("Authorization", "Bearer "+goodKey)
	s.ServeHTTP(httptest.NewRecorder(), r)
	if first != nil {
		t.Fatalf("a valid key: %v", first)
	}
	if e := AsError(second); e == nil || e.Code != CodeAPIKeyRevoked {
		t.Fatalf("a revoked key: %v", second)
	}
	if e := AsError(s.Recheck(httptest.NewRequest("GET", "/v1/stream", nil))); e == nil || e.Code != CodeAuthenticationRequired {
		t.Fatal("a request the pipeline never saw")
	}
}
