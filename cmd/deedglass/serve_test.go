package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/mcp/httpgw"
)

// Every route serve mounts is documented in api/openapi.yaml (checks 1–6), from the real list.
func TestServeRoutesMatchOpenAPI(t *testing.T) {
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler)})
	api.Mount(serveRoutes(api, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)...)
	for _, err := range httpx.CheckSpec(apiSpec(), api.Routes()) {
		t.Error(err)
	}
	if len(api.Routes()) != 122 {
		t.Fatalf("serve mounts %d routes, want 122 (S03 + keys.json in S04 + the six Ledger routes in S06 + 16 in S07 + 17 in S08 + 9 in S09 + 12 in S12 + 3 in S13 + 1 in S14 + 1 in S15 + 9 in S16 + 1 in S17 + 2 in S20 + 15 in S22 + 2 in S23 + 3 in S24 + 1 in S25 + 12 in S27 + 5 in S28 + 1 in S29 + 1 in S30 + 1 in S38)", len(api.Routes()))
	}
}

func TestVersionAgainstServer(t *testing.T) {
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler)})
	api.Mount(serveRoutes(api, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)...)
	ts := httptest.NewServer(api)
	defer ts.Close()

	r := cli(t, nil, false, "version", "--server", ts.URL)
	if r.code != 0 || !strings.Contains(r.stdout, "client  deedglass 0.1.0-dev · commit 5495f7e (modified) · api 2026-10-01") ||
		!strings.Contains(r.stdout, "server  deedglass ") || !strings.Contains(r.stdout, ts.URL) {
		t.Fatalf("exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = cli(t, nil, false, "--json", "version", "--server", ts.URL+"/")
	if r.code != 0 || !strings.Contains(r.stdout, `"server":{"object":"version"`) || !strings.Contains(r.stdout, `"client":{"object":"version"`) {
		t.Fatalf("json: %d %s", r.code, r.stdout)
	}

	notUs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "<html>") }))
	defer notUs.Close()
	r = cli(t, nil, false, "version", "--server", notUs.URL)
	if r.code != 1 || !strings.Contains(r.stderr, "it answered 200, not like a Deedglass server") {
		t.Fatalf("not a Deedglass server: %d %s", r.code, r.stderr)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	start := time.Now()
	r = cli(t, nil, false, "version", "--server", slow.URL)
	if r.code != 1 || !strings.Contains(r.stderr, "no answer within 2 s") || time.Since(start) > 3*time.Second {
		t.Fatalf("slow: %d %s in %v", r.code, r.stderr, time.Since(start))
	}
}

// Graceful shutdown: an in-flight request finishes, health reports draining during the drain
// window, and new connections are refused afterwards.
func TestServeUntilStopped(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	api := httpx.New(httpx.Options{Logger: log})
	api.Mount(serveRoutes(api, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)...)
	entered, release := make(chan struct{}), make(chan struct{})
	api.Mount(httpx.Route{Method: "GET", Pattern: "/v1/_test/slow", OperationID: "slow", Auth: httpx.Public, Extension: "test",
		Handler: func(w http.ResponseWriter, r *http.Request) error {
			close(entered)
			<-release
			return httpx.WriteJSON(w, 200, map[string]string{"object": "done"})
		}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + ln.Addr().String()
	sigs := make(chan os.Signal, 2)
	type out struct {
		res stopResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := serveUntilStopped(context.Background(), newHTTPServer(api, log), api, ln, sigs, 300*time.Millisecond, log)
		done <- out{res, err}
	}()

	slowDone := make(chan int, 1)
	go func() {
		res, err := http.Get(base + "/v1/_test/slow")
		if err != nil {
			slowDone <- -1
			return
		}
		res.Body.Close()
		slowDone <- res.StatusCode
	}()
	<-entered
	sigs <- syscall.SIGTERM
	// During the drain window health says draining, and requests are still served.
	deadline := time.Now().Add(time.Second)
	for {
		res, err := http.Get(base + "/v1/health")
		if err == nil {
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode == 503 && strings.Contains(string(b), `"status": "draining"`) {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("health never reported draining")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(350 * time.Millisecond) // past the drain window: Shutdown has begun
	close(release)
	if code := <-slowDone; code != 200 {
		t.Fatalf("the in-flight request got %d, want 200", code)
	}
	o := <-done
	if o.err != nil || o.res.cutOff != 0 {
		t.Fatalf("result %+v %v", o.res, o.err)
	}
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
		t.Fatal("a new connection was accepted after shutdown")
	}
}

func TestSecondSignalExits(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	api := httpx.New(httpx.Options{Logger: log})
	api.Mount(serveRoutes(api, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)...)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	exited := make(chan int, 1)
	saved := serveExit
	serveExit = func(code int) { exited <- code }
	t.Cleanup(func() { serveExit = saved })
	sigs := make(chan os.Signal, 2)
	go serveUntilStopped(context.Background(), newHTTPServer(api, log), api, ln, sigs, 2*time.Second, log) //nolint:errcheck
	sigs <- syscall.SIGTERM
	sigs <- syscall.SIGINT
	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("the second signal didn't exit")
	}
}

func TestPGVersion(t *testing.T) {
	for n, want := range map[int]string{180006: "18.6", 170011: "17.11", 160015: "16.15", 0: "unknown"} {
		if got := pgVersion(n); got != want {
			t.Errorf("%d: %s", n, got)
		}
	}
}

// Every route an API key can call declares the scopes it needs (S08 §3.4), so no key route is
// open to any key by omission. GET /v1/whoami is the one exception: any key, stop checks skipped.
// POST /v1/actions (S13) and the two MCP gateway endpoints (S15) are the routes that let a stopped
// agent's key through, to record and seal the refused attempt; the gateway routes then send every
// tools/call through the Gate, which refuses it.
func TestKeyRoutesDeclareScopes(t *testing.T) {
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler)})
	api.Mount(serveRoutes(api, nil, nil, nil, nil, nil, nil, nil, nil, &mcpWiring{gw: &httpgw.Gateway{}, timeout: time.Minute}, nil, nil, nil, nil, nil)...)
	keyRoutes := 0
	for _, r := range api.Routes() {
		if r.Auth != httpx.AgentKey && r.Auth != httpx.SessionOrKey {
			if r.SkipStopChecks {
				t.Errorf("%s skips the stop checks but takes no key", r.Key())
			}
			continue
		}
		keyRoutes++
		switch {
		case r.Key() == "GET /v1/whoami":
			if !r.SkipStopChecks || len(r.Scopes) != 0 {
				t.Errorf("whoami must take any key and skip the stop checks")
			}
		case len(r.Scopes) == 0:
			t.Errorf("%s accepts API keys but declares no Scopes", r.Key())
		case r.SkipStopChecks:
			t.Errorf("%s skips the stop checks; only whoami may", r.Key())
		}
		// S13, S15: only POST /v1/actions and the MCP gateway record a stopped agent's attempt.
		records := r.Key() == "POST /v1/actions" || r.Key() == "POST /mcp/{connector}" || r.Key() == "POST /mcp/native"
		if r.RecordStopped != records {
			t.Errorf("%s: RecordStopped is %v; only POST /v1/actions and the MCP gateway record stopped agents", r.Key(), r.RecordStopped)
		}
	}
	if keyRoutes < 20 {
		t.Fatalf("only %d key routes: the check isn't seeing the route table", keyRoutes)
	}
}
