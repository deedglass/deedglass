//go:build integration

package identity_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
)

// node is a second Deedglass instance on the same database: its own key authenticator (cache,
// listener, limiter) and HTTP server.
type node struct {
	keys *identity.KeyAuth
	ts   *httptest.Server
}

func (w *world) node(t testing.TB) *node {
	t.Helper()
	// The world's service peppers with 9s; the node must use the same pepper.
	k, err := identity.NewKeyAuth(identity.KeyAuthOptions{DB: w.e.DB, Pepper: pepper9(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	kctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); k.Run(kctx) }()
	t.Cleanup(func() { stop(); <-done })
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: w.svc, Keys: k,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	srv.Mount(w.svc.AgentRoutes()...)
	srv.Mount(ledger.Routes(w.led)...)
	n := &node{keys: k, ts: httptest.NewServer(srv)}
	t.Cleanup(n.ts.Close)
	return n
}

func pepper9() []byte {
	p := make([]byte, 32)
	for i := range p {
		p[i] = 9
	}
	return p
}

// status asks the node GET /v1/deeds?limit=1 with key and returns the status and error code.
func (n *node) status(key string) (int, string) {
	req, _ := http.NewRequest("GET", n.ts.URL+"/v1/deeds?limit=1", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer r.Body.Close()
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.NewDecoder(r.Body).Decode(&e)
	return r.StatusCode, e.Error.Code
}

func waitHealthy(t testing.TB, ks ...*identity.KeyAuth) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, k := range ks {
		for !k.Listener().Healthy() {
			if time.Now().After(deadline) {
				t.Fatal("the listener never became healthy")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// until polls the node every 2 ms until it answers code, and returns how long that took from
// since. More than 2 s fails the test (S08's "must never happen").
func (n *node) until(t testing.TB, key, code string, since time.Time) time.Duration {
	t.Helper()
	for {
		st, c := n.status(key)
		if c == code {
			return time.Since(since)
		}
		if st == 0 {
			t.Fatalf("node unreachable: %s", c)
		}
		if time.Since(since) > 2*time.Second {
			t.Fatalf("still %d %q %v after the change returned (want %s within 2 s)", st, c, time.Since(since), code)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func pctD(d []time.Duration, p int) time.Duration {
	s := slices.Clone(d)
	slices.Sort(s)
	i := (len(s)*p + 99) / 100
	return s[max(0, min(len(s)-1, i-1))]
}

type propResult struct {
	Name  string  `json:"name"`
	N     int     `json:"n"`
	P50MS float64 `json:"p50_ms"`
	P99MS float64 `json:"p99_ms"`
	MaxMS float64 `json:"max_ms"`
}

func summarize(name string, d []time.Duration) propResult {
	ms := func(x time.Duration) float64 { return float64(x.Microseconds()) / 1000 }
	return propResult{Name: name, N: len(d), P50MS: ms(pctD(d, 50)), P99MS: ms(pctD(d, 99)), MaxMS: ms(slices.Max(d))}
}

// TestStopPropagation runs two instances on one database: a change made through A must be refused
// by B, with a warm cache, in under 2 s; a killed listener and a silent stall are covered too.
func TestStopPropagation(t *testing.T) {
	w := newWorld(t)
	b := w.node(t)
	waitHealthy(t, w.keys, b.keys)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	key := *f.key(t, a.ID, "").Secret
	runs := 50
	if testing.Short() {
		runs = 5
	}
	warm := func(k string) {
		t.Helper()
		for range 3 {
			if st, c := b.status(k); st != 200 {
				t.Fatalf("warm: %d %s", st, c)
			}
		}
	}

	// Stop: timed from the moment A's 200 arrives (the "must never happen" bound) and from the
	// moment the stop request is sent (what an operator sees).
	var stops, stopsFromRequest []time.Duration
	for range runs {
		warm(key)
		sent := time.Now()
		expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""), 200, "")
		returned := time.Now()
		stops = append(stops, b.until(t, key, "agent_stopped", returned))
		stopsFromRequest = append(stopsFromRequest, returned.Sub(sent)+stops[len(stops)-1])
		expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/start", `{"reason":"next run"}`), 200, "")
		b.until(t, key, "", time.Now())
	}
	// Revoke.
	var revokes []time.Duration
	for range runs {
		k := f.key(t, a.ID, "")
		warm(*k.Secret)
		expect(t, w.call(t, f.admin, "POST", "/v1/keys/"+k.ID+"/revoke", ""), 200, "")
		revokes = append(revokes, b.until(t, *k.Secret, "api_key_revoked", time.Now()))
	}
	// Stop all.
	var all []time.Duration
	for range runs {
		warm(key)
		expect(t, w.call(t, f.admin, "POST", "/v1/org/stop-all", ""), 200, "")
		all = append(all, b.until(t, key, "org_stopped", time.Now()))
		expect(t, f.owner.do("POST", "/v1/org/start-all", `{"reason":"next run"}`), 200, "")
		b.until(t, key, "", time.Now())
	}

	// A killed listener: terminate B's LISTEN backend, then stop at once.
	warm(key)
	pid := b.keys.Listener().PID()
	if _, err := w.e.Admin(t).Exec(ctx, `SELECT pg_terminate_backend($1)`, int32(pid)); err != nil {
		t.Fatal(err)
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""), 200, "")
	killed := b.until(t, key, "agent_stopped", time.Now())
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/start", `{"reason":"after kill"}`), 200, "")
	waitHealthy(t, b.keys)
	if b.keys.Listener().PID() == pid || b.keys.Listener().Reconnects() < 1 {
		t.Fatal("the listener didn't reconnect")
	}
	b.until(t, key, "", time.Now())

	// A silent stall: B's listener receives nothing and reports no error.
	warm(key)
	b.keys.Listener().SetStall(true)
	stallStart := time.Now()
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""), 200, "")
	stalled := b.until(t, key, "agent_stopped", time.Now())
	bypassAfter := time.Duration(0)
	for !b.keys.Cache().Bypassed() {
		time.Sleep(5 * time.Millisecond)
	}
	bypassAfter = time.Since(stallStart)
	b.keys.Listener().SetStall(false)
	waitHealthy(t, b.keys)
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/start", `{"reason":"after stall"}`), 200, "")
	b.until(t, key, "", time.Now())

	res := []propResult{summarize("stop", stops), summarize("revoke", revokes), summarize("stop_all", all),
		summarize("stop_from_request", stopsFromRequest),
		summarize("killed_listener", []time.Duration{killed}), summarize("silent_stall", []time.Duration{stalled}),
		summarize("silent_stall_bypass_on", []time.Duration{bypassAfter})}
	for _, r := range res[:4] {
		if r.P99MS > 500 {
			t.Errorf("%s p99 %.1f ms > 500 ms locally (load: check uptime)", r.Name, r.P99MS)
		}
	}
	for _, r := range res {
		if r.MaxMS >= 2000 {
			t.Errorf("%s took %.1f ms, the bound is 2 s", r.Name, r.MaxMS)
		}
	}
	host, _ := os.Hostname()
	var pg string
	_ = w.e.Admin(t).QueryRow(ctx, `SHOW server_version`).Scan(&pg)
	out := map[string]any{"object": "stop_propagation", "date": time.Now().UTC().Format("2006-01-02"), "host": host,
		"goos": runtime.GOOS, "goarch": runtime.GOARCH, "postgres": pg, "instances": 2, "runs": runs, "results": res,
		"note": "Two Deedglass instances (separate caches and listeners) on one throwaway Postgres. stop, revoke, stop_all, killed_listener and silent_stall: time from the change's 200 on instance A to the first refusal on instance B, polled every 2 ms. stop_from_request: from sending the stop to that refusal. silent_stall_bypass_on: from the stall starting to B bypassing its cache."}
	b2, _ := json.MarshalIndent(out, "", "  ")
	t.Logf("PROPAGATION %s", b2)
	if p := os.Getenv("DEEDGLASS_IT_PROPAGATION_OUT"); p != "" {
		if err := os.WriteFile(p, append(b2, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestEpochGuard forces the race the epoch closes: B reads the agent (active) from Postgres, a stop
// commits and B's listener evicts, and only then does B's load try to cache what it read. The
// stale value must not be cached, so B's next request is refused.
func TestEpochGuard(t *testing.T) {
	w := newWorld(t)
	b := w.node(t)
	waitHealthy(t, w.keys, b.keys)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	key := *f.key(t, a.ID, "").Secret
	b.keys.Cache().Flush()

	paused, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	identity.SetAfterLoad(b.keys.Cache(), func() {
		once.Do(func() { close(paused); <-release })
	})
	t.Cleanup(func() { identity.SetAfterLoad(b.keys.Cache(), nil) })
	first := make(chan int, 1)
	go func() { st, _ := b.status(key); first <- st }()
	<-paused // B has read "active" and not cached it yet
	e0 := b.keys.Cache().Epoch()
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""), 200, "")
	for b.keys.Cache().Epoch() == e0 { // B's listener has seen the stop
		time.Sleep(time.Millisecond)
	}
	close(release)
	if st := <-first; st != 200 {
		t.Fatalf("the in-flight request read before the stop: %d", st) // it began before the stop
	}
	if st, c := b.status(key); st != 403 || c != "agent_stopped" {
		t.Fatalf("the stale value was cached: %d %s", st, c)
	}
}

// TestWatch: a long poll on B ends with ErrAgentStopped within 2 s of a stop through A, and with
// ErrOrgStopped for Stop all. CheckActive(fresh) refuses at once.
func TestWatch(t *testing.T) {
	w := newWorld(t)
	b := w.node(t)
	waitHealthy(t, w.keys, b.keys)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	agentID, _ := ids.Parse[ids.AgentKind](a.ID)
	org := f.org.Org.ID

	if err := b.keys.CheckActive(ctx, org, agentID, false); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := b.keys.Watch(ctx, org, agentID)
	defer cancel()
	time.Sleep(50 * time.Millisecond)
	if wctx.Err() != nil {
		t.Fatal("cancelled before the stop")
	}
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""), 200, "")
	start := time.Now()
	select {
	case <-wctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Watch wasn't cancelled within 2 s")
	}
	if c := context.Cause(wctx); c != identity.ErrAgentStopped {
		t.Fatalf("cause %v", c)
	}
	t.Logf("watch cancelled %v after the stop returned", time.Since(start))
	err := b.keys.CheckActive(ctx, org, agentID, true)
	if he := httpx.AsError(err); he == nil || he.Code != httpx.CodeAgentStopped {
		t.Fatalf("CheckActive(fresh): %v", err)
	}
	// A watcher opened after the stop is cancelled at once.
	late, cancel2 := b.keys.Watch(ctx, org, agentID)
	defer cancel2()
	select {
	case <-late.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a watcher on a stopped agent wasn't cancelled")
	}
	// Stop all.
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/start", `{"reason":"watch org"}`), 200, "")
	octx, cancel3 := b.keys.Watch(ctx, org, agentID)
	defer cancel3()
	time.Sleep(50 * time.Millisecond)
	expect(t, w.call(t, f.admin, "POST", "/v1/org/stop-all", ""), 200, "")
	select {
	case <-octx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Watch wasn't cancelled by Stop all")
	}
	if c := context.Cause(octx); c != identity.ErrOrgStopped {
		t.Fatalf("cause %v", c)
	}
	if n := identity.WatcherCount(b.keys); n != 0 {
		t.Fatalf("%d watchers left", n)
	}
}

// TestKeyAuthBudget: warm in-process authentication, cold (through Postgres), and the Stop API
// with its deed (S08 §3.11). It runs under make bench-stop only.
func TestKeyAuthBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-stop)")
	}
	w := newWorld(t)
	waitHealthy(t, w.keys)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	key := *f.key(t, a.ID, "").Secret
	if _, err := w.keys.AuthenticateKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	measure := func(name string, n int, fn func(i int)) time.Duration {
		d := make([]time.Duration, n)
		for i := range d {
			s := time.Now()
			fn(i)
			d[i] = time.Since(s)
		}
		r := summarize(name, d)
		t.Logf("PERF {\"name\":%q,\"n\":%d,\"p50_us\":%.1f,\"p99_us\":%.1f}", name, n, r.P50MS*1000, r.P99MS*1000)
		return pctD(d, 99)
	}
	// Warm: the mean over a tight loop is the per-op figure (BenchmarkAuthWarm's number).
	const warmN = 20000
	s := time.Now()
	for range warmN {
		if _, err := w.keys.AuthenticateKey(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	perOp := time.Since(s) / warmN
	t.Logf("PERF {\"name\":\"auth warm (mean per op)\",\"n\":%d,\"p50_us\":%.2f,\"p99_us\":%.2f}", warmN,
		float64(perOp.Nanoseconds())/1000, float64(perOp.Nanoseconds())/1000)
	cold := measure("auth cold (cache flushed, Postgres)", 1000, func(int) {
		w.keys.Cache().Flush()
		if _, err := w.keys.AuthenticateKey(ctx, key); err != nil {
			t.Fatal(err)
		}
	})
	stops := make([]time.Duration, 200)
	for i := range stops {
		s := time.Now()
		if r := w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/stop", ""); r.status != 200 {
			t.Fatal(r.status)
		}
		stops[i] = time.Since(s)
		w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/start", `{"reason":"bench run `+strconv.Itoa(i)+`"}`)
	}
	sr := summarize("stop API with its deed (HTTP)", stops)
	t.Logf("PERF {\"name\":%q,\"n\":%d,\"p50_us\":%.1f,\"p99_us\":%.1f}", sr.Name, sr.N, sr.P50MS*1000, sr.P99MS*1000)
	stop := pctD(stops, 99)
	if perOp > 20*time.Microsecond || cold > 2*time.Millisecond || stop > 50*time.Millisecond {
		t.Errorf("budget missed (load: check uptime): warm %v/op (20 µs), cold p99 %v (2 ms), stop p99 %v (50 ms)", perOp, cold, stop)
	}
}
