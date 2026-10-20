//go:build integration

package gate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/config"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/policystore"
)

// --- the worker and a read-back that outlasts the inline budget (S19) ---

// readBackResolver wraps the world's resolver: fake.echo gets a read-back of itself, and its
// executor reads back through readBackExec.
type readBackResolver struct {
	inner        Resolver
	slow         atomic.Int32 // the next read-backs that wait out their budget
	resolveFails atomic.Bool  // the worker can't resolve the tool
	calls        atomic.Int32
	body         string
	expect       map[string]any
}

func (r *readBackResolver) Resolve(ctx context.Context, org ids.OrgID, q string) (Executor, connector.ToolSpec, string, error) {
	ex, spec, ctype, err := r.inner.Resolve(ctx, org, q)
	if err != nil || q != "fake.echo" {
		return ex, spec, ctype, err
	}
	if r.resolveFails.Load() {
		return nil, spec, "", errors.New("the registry is unreachable")
	}
	spec.ReadBack = &connector.ReadBackSpec{Tool: "echo", Expect: r.expect}
	return &readBackExec{Executor: ex, r: r}, spec, ctype, nil
}

type readBackExec struct {
	Executor
	r *readBackResolver
}

func (e *readBackExec) CanReadBack() bool { return true }

func (e *readBackExec) ReadBack(ctx context.Context, _ connector.Call, _ connector.Result) (connector.Observation, error) {
	e.r.calls.Add(1)
	if e.r.slow.Add(-1) >= 0 {
		select {
		case <-ctx.Done():
			return connector.Observation{}, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	body := e.r.body
	if body == "" {
		body = `{"status":"shipped"}`
	}
	return connector.Observation{Body: json.RawMessage(body), Status: 200}, nil
}

func runWorker(t *testing.T, w *world) (stop func()) {
	t.Helper()
	wk := w.p.NewWorker("test")
	wk.PollEvery, wk.SweepEvery, wk.Drain = 20*time.Millisecond, time.Hour, time.Second
	w.keys.Listener().Listen(store.JobsChannel, wk.Wake, func() { wk.Wake("") })
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { defer close(done); _ = wk.Run(ctx) }()
	return func() { cancel(); <-done }
}

func TestWorkerDeferredWitness(t *testing.T) {
	w := newWorld(t, worldOpts{cfg: Config{WorkerMaxAttempts: 2, Witness: WitnessConfig{InlineBudget: 100 * time.Millisecond}}})
	rr := &readBackResolver{inner: w.p.o.Resolver, expect: map[string]any{"body.status": "shipped"}}
	w.p.o.Resolver = rr
	// The read-back waits out the inline budget: the action stays witnessing (processing).
	rr.slow.Store(1)
	a := w.must(0, req("fake.echo", `{}`))
	if a.State != "witnessing" || a.Status != "processing" {
		t.Fatalf("%s", mustJSON(a))
	}
	var jobs int
	w.adminRow(`SELECT count(*) FROM deedglass.witness_jobs WHERE action_id = $1 AND kind = 'l1_readback'`, []any{a.ID}, &jobs)
	if jobs != 1 {
		t.Fatalf("%d l1_readback jobs", jobs)
	}
	stop := runWorker(t, w)
	defer stop()
	w.admin(`UPDATE deedglass.witness_jobs SET run_after = now() WHERE action_id = $1`, a.ID)
	if err := w.p.Wait(bg, w.org, a.ID, "", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	v, _ := w.p.Get(bg, w.pr(0), a.ID, 0)
	if v.State != "sealed" || v.Witness.Level != "L1" || v.Witness.Verdict != "VERIFIED" || *v.Witness.Method != "readback.fake.echo" {
		t.Fatalf("%s", mustJSON(v))
	}
	// A job that can't run (the tool can't be resolved): retried with backoff, then decided
	// without L1, with witness_gave_up listed as an L1 gap. Required L0 and a clean L0: L0 VERIFIED.
	rr.slow.Store(1)
	b := w.must(1, req("fake.echo", `{}`))
	rr.resolveFails.Store(true)
	deadline := time.Now().Add(15 * time.Second)
	for {
		// Pull the retry forward instead of waiting out the backoff.
		w.admin(`UPDATE deedglass.witness_jobs SET run_after = now() WHERE action_id = $1`, b.ID)
		v, _ = w.p.Get(bg, w.pr(1), b.ID, 0)
		if v.State == "sealed" || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if v.State != "sealed" || v.Witness.Verdict != "VERIFIED" || v.Witness.Level != "L0" || !slices.Contains(v.Witness.Codes(), ReasonWitnessGaveUp) {
		t.Fatalf("%s", mustJSON(v))
	}
	rr.resolveFails.Store(false)
	var b2 strings.Builder
	w.p.Metrics().Collect(&b2)
	if !strings.Contains(b2.String(), `deedglass_worker_jobs_total{kind="l1_readback",result="done"}`) ||
		!strings.Contains(b2.String(), `deedglass_worker_jobs_total{kind="l1_readback",result="retry"}`) {
		t.Fatalf("%s", b2.String())
	}
}

// TestCrashBetweenB1AndB2: an instance that stops after B1 leaves a job; the worker reads back and
// seals exactly once (S19 §3.7).
func TestCrashBetweenB1AndB2(t *testing.T) {
	w := newWorld(t, worldOpts{})
	rr := &readBackResolver{inner: w.p.o.Resolver, expect: map[string]any{"body.status": "shipped"}}
	w.p.o.Resolver = rr
	w.p.hooks.afterB1 = func(ids.ActionID) bool { return true }
	a := w.must(0, req("fake.echo", `{}`))
	if a.State != "witnessing" || rr.calls.Load() != 0 {
		t.Fatalf("%s", mustJSON(a))
	}
	w.p.hooks.afterB1 = nil
	stop := runWorker(t, w)
	defer stop()
	w.admin(`UPDATE deedglass.witness_jobs SET run_after = now() WHERE action_id = $1`, a.ID)
	if err := w.p.Wait(bg, w.org, a.ID, "", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	v, _ := w.p.Get(bg, w.pr(0), a.ID, 0)
	var deeds, jobs int
	w.adminRow(`SELECT count(*) FROM deedglass.deeds WHERE action_id = $1`, []any{a.ID}, &deeds)
	w.adminRow(`SELECT count(*) FROM deedglass.witness_jobs WHERE action_id = $1`, []any{a.ID}, &jobs)
	if v.State != "sealed" || v.Witness.Level != "L1" || deeds != 1 || jobs != 0 || rr.calls.Load() != 1 {
		t.Fatalf("deeds %d jobs %d calls %d: %s", deeds, jobs, rr.calls.Load(), mustJSON(v))
	}
}

// TestReadBackInline: the read-back finishes within the budget: one response, sealed, L1. The
// lying target (MISMATCH) and invariant 3 (no transaction open during the read-back).
func TestReadBackInline(t *testing.T) {
	w := newWorld(t, worldOpts{})
	rr := &readBackResolver{inner: w.p.o.Resolver, expect: map[string]any{"body.status": "shipped"}}
	w.p.o.Resolver = rr
	var acquired int32 = -1
	w.p.hooks.beforeReadBack = func(string) { acquired = w.e.DB.PoolForTest().Stat().AcquiredConns() }
	a := w.must(0, req("fake.echo", `{}`))
	if a.State != "sealed" || a.Witness.Level != "L1" || a.Witness.Verdict != "VERIFIED" || acquired != 0 {
		t.Fatalf("acquired %d: %s", acquired, mustJSON(a))
	}
	rr.body = `{"status":"pending"}`
	b := w.must(0, req("fake.echo", `{}`))
	if b.State != "sealed" || b.Witness.Level != "L1" || b.Witness.Verdict != "MISMATCH" || len(*b.Witness.Reasons) != 1 ||
		(*b.Witness.Reasons)[0].Diff[0].Path != "body.status" {
		t.Fatalf("%s", mustJSON(b))
	}
	// The detail digest is sha256(JCS(stored detail)), and the deed carries it.
	var detail []byte
	var digest string
	w.adminRow(`SELECT witness_detail::text, witness_detail_digest FROM deedglass.actions WHERE id = $1`, []any{b.ID}, &detail, &digest)
	if d, err := deed.DigestJSON(detail); err != nil || d != digest || b.Witness.DetailDigest == nil || *b.Witness.DetailDigest != digest {
		t.Fatalf("digest %s vs %s (%v)", d, digest, err)
	}
}

// --- HTTP, through the real handlers and the httpx pipeline ---

// agentKey stores a real test API key for agent i (the authenticator's pepper is 32 nines).
func (w *world) agentKey(i int) string {
	w.t.Helper()
	full, pk, err := identity.NewAPIKey(identity.ModeTest)
	if err != nil {
		w.t.Fatal(err)
	}
	h := identity.HashSecret(bytesOf(9, 32), pk.Secret)
	err = w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO deedglass.api_keys (id, org_id, agent_id, mode, prefix, secret_hash, scopes, created_by)
		    VALUES ($1, $2, $3, 'test', $4, $5, $6, 'operator')`, ids.New[ids.APIKeyKind](), w.org, w.agents[i].ID, pk.Prefix, h[:],
			httpx.DefaultAgentScopes)
		return err
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return full
}

func bytesOf(b byte, n int) []byte { return []byte(strings.Repeat(string(rune(b)), n)) }

func TestHTTPHandlers(t *testing.T) {
	w := newWorld(t, worldOpts{})
	key := w.agentKey(0)
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Keys: w.keys})
	srv.Mount(Routes(w.p)...)
	srv.Mount(ledger.RoutesWith(w.led, w.p.Wait)...)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	do := func(method, path, body string, hdr ...string) (int, http.Header, string) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		r, _ := http.NewRequest(method, ts.URL+path, rd)
		r.Header.Set("Authorization", "Bearer "+key)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Add(hdr[i], hdr[i+1])
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, string(b)
	}
	c, h, b := do("POST", "/v1/actions", `{"tool":"fake.echo","args":{"message":"hello"},"wait":5,"context":{"client":{"name":"t","version":"1"},"transport":"rest"}}`,
		"Idempotency-Key", "k1", "Deedglass-On-Behalf-Of", "u_1")
	if c != 200 || !strings.Contains(b, `"on_behalf_of": "u_1"`) || !strings.Contains(b, `"reasons": []`) {
		t.Fatalf("%d %s", c, b)
	}
	c2, h2, b2 := do("POST", "/v1/actions", `{"tool":"fake.echo","args":{"message":"hello"},"wait":5,"context":{"client":{"name":"t","version":"1"},"transport":"rest"}}`,
		"Idempotency-Key", "k1", "Deedglass-On-Behalf-Of", "u_1")
	if c2 != 200 || b2 != b || h2.Get("Deedglass-Idempotent-Replayed") != "true" || h.Get("Deedglass-Idempotent-Replayed") != "" {
		t.Fatalf("%d %s", c2, b2)
	}
	for _, x := range []struct {
		body, code, param string
		status            int
		hdr               []string
	}{
		{`{"args":{}}`, "validation_failed", "tool", 400, nil},
		{`{"tool":"fake.echo","args":{},"wait":30}`, "validation_failed", "wait", 400, nil},
		{`{"tool":"fake.echo","args":{},"nope":1}`, "validation_failed", "nope", 400, nil},
		{`{"tool":"fake.echo","args":{}}`, "idempotency_key_invalid", "Idempotency-Key", 400, []string{"Idempotency-Key", "tab\there"}},
		{`{"tool":"fake.echo","args":{}}`, "intent_invalid", "Deedglass-Intent", 403, []string{"Deedglass-Intent", "a.b", "Deedglass-Intent", "c.d"}},
		{`{"tool":"nope.echo","args":{}}`, "tool_unknown", "", 404, nil},
		{`{"tool":"fake.write","args":{"rows":301}}`, "policy_denied", "", 403, nil},
	} {
		c, _, b := do("POST", "/v1/actions", x.body, x.hdr...)
		if c != x.status || !strings.Contains(b, `"code": "`+x.code+`"`) || x.param != "" && !strings.Contains(b, `"param": "`+x.param+`"`) {
			t.Errorf("%s: %d %s", x.body, c, b)
		}
		if x.status == 403 && x.code == "policy_denied" && !strings.Contains(b, `"action": {`) {
			t.Errorf("no error.action: %s", b)
		}
	}
	// Pending → GET ?wait on it → cancel over HTTP.
	_, _, pb := do("POST", "/v1/actions", `{"tool":"fake.write","args":{"rows":1,"table":"approvals"}}`)
	var pa struct{ ID string }
	_ = json.Unmarshal([]byte(pb), &pa)
	start := time.Now()
	if c, _, b := do("GET", "/v1/actions/"+pa.ID+"?wait=0.3&expand[]=result", ""); c != 200 || !strings.Contains(b, `"status": "pending_approval"`) || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("a pending wait returned at once: %d %s", c, b)
	}
	if c, _, b := do("GET", "/v1/actions/"+pa.ID+"?expand[]=nope", ""); c != 400 {
		t.Fatalf("%d %s", c, b)
	}
	if c, _, b := do("POST", "/v1/actions/"+pa.ID+"/cancel", ""); c != 200 || !strings.Contains(b, `"status": "cancelled"`) {
		t.Fatalf("%d %s", c, b)
	}
	// Proxy over HTTP, a bad result body, then a good one.
	_, _, xb := do("POST", "/v1/actions", `{"tool":"scratch.write","args":{},"mode":"proxy"}`)
	var xa struct{ ID string }
	_ = json.Unmarshal([]byte(xb), &xa)
	if c, _, b := do("POST", "/v1/actions/"+xa.ID+"/result", `{"status":"done"}`); c != 400 {
		t.Fatalf("%d %s", c, b)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if c, _, b := do("POST", "/v1/actions/"+xa.ID+"/result", `{"status":"unknown","started_at":"`+now+`","finished_at":"`+now+`","error_code":"edge_crashed","error_message":"The edge restarted."}`); c != 200 ||
		!strings.Contains(b, `"verdict": "UNVERIFIABLE"`) {
		t.Fatalf("%d %s", c, b)
	}
	if c, _, _ := do("POST", "/v1/actions/act_01j9zq3m8x2k7w4c5v6b7n8p9r/result", `{"status":"succeeded","started_at":"`+now+`","finished_at":"`+now+`","result_digest":"sha256:`+strings.Repeat("0", 64)+`"}`); c != 404 {
		t.Fatalf("%d", c)
	}
}

// --- two instances race the sweepers ---

// TestTwoInstancesSweep: 200 actions stuck in executing past their deadline; two pipelines on two
// pools sweep at the same time. Each action is sealed exactly once and the chain stays intact.
func TestTwoInstancesSweep(t *testing.T) {
	w := newWorld(t, worldOpts{maxConns: 20})
	db2, err := store.Open(bg, config.NewSecret(w.e.AppURL), store.Options{MaxConns: 20})
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	led2 := ledger.New(db2, deed.NewSealer(w.signer), w.signer, ledger.WithDev(true), ledger.WithLogger(slog.New(slog.DiscardHandler)))
	p2, err := New(Options{DB: db2, Ledger: led2, Policies: w.reg, Usage: policystore.NewUsage(db2), Resolver: connector.NewRegistry(w.fake)})
	if err != nil {
		t.Fatal(err)
	}
	const n = 200
	var mu sync.Mutex
	var acts []string
	var wg sync.WaitGroup
	sem := make(chan struct{}, 20)
	for i := range n {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			r := proxyReq("scratch.write", fmt.Sprintf(`{"i":%d}`, i))
			a, err := w.submit(i%3, r)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			acts = append(acts, a.ID)
			mu.Unlock()
		})
	}
	wg.Wait()
	w.admin(`UPDATE deedglass.actions SET executing_deadline = now() - interval '1 second' WHERE state = 'executing'`)
	var r1, r2 SweepResult
	var sw sync.WaitGroup
	for range 3 { // a few passes each: one claim takes at most 100 of each kind
		sw.Go(func() { x := w.p.Sweep(bg); r1.CrashRecovered += x.CrashRecovered; r1.Errors += x.Errors })
		sw.Go(func() { x := p2.Sweep(bg); r2.CrashRecovered += x.CrashRecovered; r2.Errors += x.Errors })
		sw.Wait()
	}
	if r1.Errors+r2.Errors != 0 || r1.CrashRecovered+r2.CrashRecovered != n {
		t.Fatalf("instance 1 %+v, instance 2 %+v", r1, r2)
	}
	var sealed, deeds int
	_ = w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.actions WHERE state = 'sealed' AND mode = 'proxy'`).Scan(&sealed); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.deeds WHERE kind = 'action' AND action_id = ANY($1)`, acts).Scan(&deeds)
	})
	if sealed != n || deeds != n {
		t.Fatalf("%d sealed, %d deeds, want %d", sealed, deeds, n)
	}
	checkChain(t, w)
	t.Logf("instance 1 recovered %d, instance 2 %d", r1.CrashRecovered, r2.CrashRecovered)
}

func checkChain(t *testing.T, w *world) {
	t.Helper()
	rep, err := w.led.CheckChain(bg, w.org, ledger.Range{}, ledger.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "intact" {
		t.Fatalf("chain: %+v", rep)
	}
}

// --- load ---

// TestLoad1000: 1,000 actions from 10 agents, 50 at a time, fake.echo and fake.write with 10%
// denials. Every action is sealed with one deed, every event path is a path of the state machine,
// the chain is intact, the calls counters equal the non-denied actions, and spend equals the
// fake.write costs.
func TestLoad1000(t *testing.T) {
	// Load-aware (S32, DL-047): the correctness target never moves (every action sealed with one
	// deed, the paths, the counters and the chain). Only how long the test waits for a slow,
	// shared runner to finish is scaled to the speed measured here; a runner more than eight
	// times slower than the reference skips instead of passing on a relaxed check.
	factor := loadFactor(t)
	if factor > maxLoadFactor {
		t.Skipf("runner too loaded for the 1,000-action run (%.1fx slower than the reference); run it again on an idle machine", factor)
	}
	settle := time.Duration(float64(30*time.Second) * factor)
	w := newWorld(t, worldOpts{agents: 10, maxConns: 60})
	const n, conc = 1000, 50
	rng := rand.New(rand.NewPCG(13, 13))
	type job struct {
		agent int
		r     ActionRequest
		deny  bool
		cost  int64
	}
	jobs := make([]job, n)
	for i := range jobs {
		j := job{agent: i % 10}
		switch {
		case i%10 == 0:
			j.r, j.deny = req("fake.write", `{"rows":500,"table":"orders"}`), true
		case rng.IntN(2) == 0:
			rows := int64(rng.IntN(50) + 1)
			j.r, j.cost = req("fake.write", fmt.Sprintf(`{"rows":%d,"table":"orders"}`, rows)), rows
		default:
			j.r = req("fake.echo", fmt.Sprintf(`{"n":%d}`, i))
		}
		jobs[i] = j
	}
	var (
		wg        sync.WaitGroup
		sem       = make(chan struct{}, conc)
		denied    atomic.Int64
		spend     atomic.Int64
		mu        sync.Mutex
		ids       []string
		latencies []time.Duration
	)
	start := time.Now()
	for _, j := range jobs {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			t0 := time.Now()
			a, err := w.submit(j.agent, j.r)
			d := time.Since(t0)
			if j.deny {
				if httpx.AsError(err) == nil || a == nil || a.State != "sealed" {
					t.Errorf("deny: %v", err)
					return
				}
				denied.Add(1)
			} else {
				if err == nil && a != nil && a.State != "sealed" {
					// The 10 s wait ended first (a slow runner): wait for the seal, load-scaled.
					a, err = waitSealed(w, j.agent, a, settle)
				}
				if err != nil || a.State != "sealed" || a.Status != "executed" {
					t.Errorf("%v %v", err, a)
					if a != nil {
						w.explain(a.ID)
					}
					return
				}
			}
			spend.Add(j.cost)
			mu.Lock()
			ids = append(ids, a.ID)
			latencies = append(latencies, d)
			mu.Unlock()
		})
	}
	wg.Wait()
	elapsed := time.Since(start)
	if len(ids) != n || denied.Load() != n/10 {
		t.Fatalf("%d actions, %d denied", len(ids), denied.Load())
	}
	var deeds, calls, spent int64
	var bad []string
	_ = w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.deeds WHERE kind = 'action'`).Scan(&deeds); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(value), 0) FROM deedglass.usage_counters WHERE counter = 'calls' AND window_kind = 'day'`).Scan(&calls); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(value), 0) FROM deedglass.usage_counters WHERE counter = 'spend:usd' AND window_kind = 'day'`).Scan(&spent); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT action_id, array_agg(COALESCE(from_state, '') || '>' || to_state ORDER BY seq) FROM deedglass.action_events GROUP BY action_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var evs []string
			if err := rows.Scan(&id, &evs); err != nil {
				return err
			}
			var pairs [][2]string
			for _, e := range evs {
				f, to, _ := strings.Cut(e, ">")
				pairs = append(pairs, [2]string{f, to})
			}
			if err := path(pairs); err != nil || pairs[len(pairs)-1][1] != "sealed" {
				bad = append(bad, id)
			}
		}
		return rows.Err()
	})
	if deeds != n || calls != n-n/10 || spent != spend.Load() || len(bad) != 0 {
		t.Fatalf("deeds %d, calls %d (want %d), spend %d (want %d), bad paths %v", deeds, calls, n-n/10, spent, spend.Load(), bad)
	}
	checkChain(t, w)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("1,000 actions in %s (%.0f/s) · p50 %s · p99 %s at concurrency %d (race detector: %v)",
		elapsed.Round(time.Millisecond), float64(n)/elapsed.Seconds(), latencies[n/2], latencies[n*99/100], conc, raceEnabled)
	if f := os.Getenv("DEEDGLASS_IT_LOAD_OUT"); f != "" {
		_ = os.WriteFile(f, fmt.Appendf(nil, `{"actions":%d,"concurrency":%d,"seconds":%.3f,"p50_ms":%.3f,"p99_ms":%.3f,"race":%v}`+"\n",
			n, conc, elapsed.Seconds(), ms(latencies[n/2]), ms(latencies[n*99/100]), raceEnabled), 0o644)
	}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// waitSealed waits up to d for a to be sealed and returns it as last read. Pipeline.Wait returns at
// the action's next state change and holds for at most MaxWait, so one call isn't a wait for the
// seal: an action still authorized when the submit's wait ended returns from it at executing.
func waitSealed(w *world, agent int, a *Action, d time.Duration) (*Action, error) {
	deadline := time.Now().Add(d)
	for a.State != "sealed" {
		left := time.Until(deadline)
		if left <= 0 {
			return a, fmt.Errorf("%s not sealed in %s (state %s)", a.ID, d, a.State)
		}
		if err := w.p.Wait(bg, w.org, a.ID, "", left); err != nil {
			return a, err
		}
		v, err := w.p.Get(bg, w.pr(agent), a.ID, 0)
		if err != nil {
			return a, err
		}
		a = v
	}
	return a, nil
}

// maxLoadFactor is how much slower than the reference a runner may be before TestLoad1000 skips.
const maxLoadFactor = 8.0

// loadFactor measures this runner against the reference machine (an M-series laptop, idle: about
// 6 ms for the calibration below, 55 ms under the race detector) and returns how many times slower it is,
// never below 1. The race detector's own slowdown is part of the reference it is compared with.
// DEEDGLASS_IT_LOAD_FACTOR overrides the measurement (tests of this helper, odd runners).
func loadFactor(t *testing.T) float64 {
	t.Helper()
	if v := os.Getenv("DEEDGLASS_IT_LOAD_FACTOR"); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil && f > 0 {
			return max(f, 1)
		}
	}
	ref := 6 * time.Millisecond
	if raceEnabled {
		ref = 55 * time.Millisecond
	}
	best := time.Duration(1<<63 - 1)
	for range 3 {
		t0 := time.Now()
		calibrate()
		best = min(best, time.Since(t0))
	}
	f := max(float64(best)/float64(ref), 1)
	t.Logf("load factor %.2f (calibration %s, reference %s)", f, best.Round(time.Millisecond), ref)
	return f
}

// calibrate is a fixed CPU and allocation workload: hashing and JSON round trips, like the Gate.
func calibrate() {
	buf := make([]byte, 64<<10)
	for i := range buf {
		buf[i] = byte(i)
	}
	var sink [32]byte
	for range 120 {
		sink = sha256.Sum256(buf)
		buf[0] ^= sink[0]
	}
	for i := range 2000 {
		b, _ := json.Marshal(map[string]any{"n": i, "tool": "fake.echo", "args": map[string]any{"rows": i}})
		var v map[string]any
		_ = json.Unmarshal(b, &v)
	}
}

// explain logs an action's events and every Gate log line that names it, so a failure on a CI
// runner says why the action stopped where it did (the world's log otherwise stays in memory).
func (w *world) explain(id string) {
	w.t.Helper()
	w.t.Logf("%s events: %v", id, w.events(id))
	logMu.Lock()
	text := w.logs.String()
	logMu.Unlock()
	for l := range strings.SplitSeq(text, "\n") {
		if strings.Contains(l, id) {
			w.t.Logf("%s log: %s", id, l)
		}
	}
}

// TestRecordContention: Tx B meets the session's lock_timeout (a person holds the org's spend
// counter row past it, as 50 concurrent fake.write calls do on a slow runner). The outcome that came
// back is recorded on a retry, not dropped to crash recovery: sealed, executed, one deed, the cost
// counted once.
func TestRecordContention(t *testing.T) {
	w := newWorld(t, worldOpts{})
	// The first write creates the day's spend:usd row.
	if a := w.must(0, req("fake.write", `{"rows":2,"table":"orders"}`)); a.State != "sealed" {
		t.Fatalf("%s", mustJSON(a))
	}
	hold := w.e.Admin(t)
	tx, err := hold.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(bg, `SELECT count(*) FROM (SELECT 1 FROM deedglass.usage_counters WHERE org_id = $1 AND counter = 'spend:usd' FOR UPDATE) l`, w.org).Scan(&n); err != nil || n == 0 {
		t.Fatalf("%d spend rows locked: %v", n, err)
	}
	type res struct {
		a   *Action
		err error
	}
	got := make(chan res, 1)
	go func() {
		r := req("fake.write", `{"rows":3,"table":"orders"}`)
		r.Wait = MaxWait
		a, err := w.submit(0, r)
		got <- res{a, err}
	}()
	time.Sleep(6 * time.Second) // past lock_timeout (5 s): Tx B's first attempt is refused
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.err != nil || r.a.State != "sealed" || r.a.Status != "executed" {
		t.Fatalf("%v %v", r.err, r.a)
	}
	logMu.Lock()
	logs := w.logs.String()
	logMu.Unlock()
	if !strings.Contains(logs, "met contention; retrying") || strings.Contains(logs, "the sweeper will finish it") {
		w.explain(r.a.ID)
		t.Fatal("Tx B was not retried after the lock timeout")
	}
	var deeds, spent int64
	w.adminRow(`SELECT count(*) FROM deedglass.deeds WHERE action_id = $1`, []any{r.a.ID}, &deeds)
	w.adminRow(`SELECT COALESCE(sum(value), 0) FROM deedglass.usage_counters WHERE org_id = $1 AND counter = 'spend:usd' AND window_kind = 'day'`, []any{w.org}, &spent)
	if deeds != 1 || spent != 5 {
		t.Fatalf("deeds %d, spend %d (want 1, 5)", deeds, spent)
	}
	checkChain(t, w)
}
