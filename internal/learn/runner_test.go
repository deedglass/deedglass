package learn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// fakeJobs is a JobStore over memory with one org.
type fakeJobs struct {
	JobStore
	mu       sync.Mutex
	queue    []ClaimedReplay
	stored   map[string]*StoredReplay
	renewErr error
	cancel   bool
	finished map[string]string // id → status
	results  map[string]*ReplayResult
	causes   map[string]*ReplayError
	loadErr  error
}

func (f *fakeJobs) ClaimReplays(context.Context, string, int, time.Duration) ([]ClaimedReplay, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return nil, nil
	}
	c := f.queue[0]
	f.queue = f.queue[1:]
	return []ClaimedReplay{c}, nil
}
func (f *fakeJobs) LoadReplay(_ context.Context, _ ids.OrgID, id string) (*StoredReplay, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.stored[id], nil
}
func (f *fakeJobs) RenewReplay(context.Context, ids.OrgID, string, string, int64, int64, time.Duration) (bool, error) {
	return f.cancel, f.renewErr
}
func (f *fakeJobs) FinishReplay(_ context.Context, _ ids.OrgID, id, _, status string, res *ReplayResult, cause *ReplayError) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished[id], f.results[id], f.causes[id] = status, res, cause
	return nil
}

func jobOf(id string, src string, w Window) *StoredReplay {
	c, _ := policy.Compile([]byte(src), policy.Options{})
	return &StoredReplay{Replay: Replay{ID: id, Window: w, Candidate: ReplayCandidate{VersionHash: c.VersionHash}, Baseline: ReplayBaseline{Type: BaselineRecorded}},
		Source: []byte(src)}
}

func newJobs() *fakeJobs {
	return &fakeJobs{stored: map[string]*StoredReplay{}, finished: map[string]string{}, results: map[string]*ReplayResult{}, causes: map[string]*ReplayError{}}
}

func TestRunner(t *testing.T) {
	w := window()
	jobs := newJobs()
	add := func(id string, j *StoredReplay) {
		jobs.stored[id] = j
		jobs.queue = append(jobs.queue, ClaimedReplay{Org: testOrg, ID: id, Attempts: 1})
	}
	add("ok", jobOf("ok", replayV3, w))
	bad := jobOf("bad", replayV3, w)
	bad.Source = []byte("not: a policy")
	add("bad", bad)
	hashed := jobOf("hash", replayV3, w)
	hashed.Candidate.VersionHash = "sha256:other"
	add("hash", hashed)
	vb := jobOf("vbase", replayV3, w)
	vb.Baseline = ReplayBaseline{Type: BaselineVersion, Version: 1}
	vb.BaselineSource = []byte(replayV1)
	add("vbase", vb)
	vbBad := jobOf("vbase-bad", replayV3, w)
	vbBad.Baseline = ReplayBaseline{Type: BaselineVersion, VersionHash: "sha256:x"}
	vbBad.BaselineSource = []byte(replayV1)
	add("vbase-bad", vbBad)
	vbBroken := jobOf("vbase-broken", replayV3, w)
	vbBroken.Baseline = ReplayBaseline{Type: BaselineVersion}
	vbBroken.BaselineSource = []byte("x: y")
	add("vbase-broken", vbBroken)
	long := jobOf("window", replayV3, Window{From: w.From, To: w.From.Add(100 * 24 * time.Hour)})
	add("window", long)
	r := &Runner{Engine: &Engine{Src: &memSource{rows: fixture(t, 300)}, Batch: 50}, Jobs: jobs, Worker: "w1"}
	n, err := r.RunOnce(bg)
	if err != nil || n != 7 {
		t.Fatalf("ran %d: %v", n, err)
	}
	want := map[string]string{"ok": ReplaySucceeded, "bad": ReplayFailed, "hash": ReplayFailed, "vbase": ReplaySucceeded, "vbase-bad": ReplayFailed,
		"vbase-broken": ReplayFailed, "window": ReplayFailed}
	for id, st := range want {
		if jobs.finished[id] != st {
			t.Errorf("%s: %s, want %s (%+v)", id, jobs.finished[id], st, jobs.causes[id])
		}
	}
	if jobs.results["ok"] == nil || jobs.causes["window"].Code != "replay_window_invalid" || jobs.causes["bad"].Code != "candidate_invalid" {
		t.Fatalf("results %+v causes %+v", jobs.results["ok"], jobs.causes)
	}
	// Too large, a scan error, a cancel, a lost lease and a killed runner.
	jobs2 := newJobs()
	jobs2.stored["big"], jobs2.queue = jobOf("big", replayV3, w), []ClaimedReplay{{Org: testOrg, ID: "big"}}
	r2 := &Runner{Engine: &Engine{Src: &memSource{count: MaxReplayActions + 1}}, Jobs: jobs2, Worker: "w"}
	r2.RunOnce(bg)
	if jobs2.causes["big"] == nil || jobs2.causes["big"].Code != "replay_too_large" {
		t.Fatalf("too large: %+v", jobs2.causes["big"])
	}
	jobs2.stored["scan"], jobs2.queue = jobOf("scan", replayV3, w), []ClaimedReplay{{Org: testOrg, ID: "scan"}}
	r2.Engine = &Engine{Src: &memSource{rows: fixture(t, 50), failAfter: 1}, Batch: 10}
	r2.RunOnce(bg)
	if jobs2.causes["scan"] == nil || jobs2.causes["scan"].Code != "internal_error" {
		t.Fatalf("scan error: %+v", jobs2.causes["scan"])
	}
	jobs2.cancel = true
	jobs2.stored["cancel"], jobs2.queue = jobOf("cancel", replayV3, w), []ClaimedReplay{{Org: testOrg, ID: "cancel"}}
	r2.Engine = &Engine{Src: &memSource{rows: fixture(t, 50)}, Batch: 10}
	r2.RunOnce(bg)
	if jobs2.finished["cancel"] != ReplayCancelled {
		t.Fatalf("cancel: %s", jobs2.finished["cancel"])
	}
	jobs2.cancel, jobs2.renewErr = false, ErrLeaseLost
	jobs2.stored["lost"], jobs2.queue = jobOf("lost", replayV3, w), []ClaimedReplay{{Org: testOrg, ID: "lost"}}
	r2.RunOnce(bg)
	if _, ok := jobs2.finished["lost"]; ok {
		t.Fatal("a lost lease must not finish the job")
	}
	jobs2.renewErr = errors.New("db hiccup")
	jobs2.stored["hiccup"], jobs2.queue = jobOf("hiccup", replayV3, w), []ClaimedReplay{{Org: testOrg, ID: "hiccup"}}
	r2.RunOnce(bg)
	if jobs2.finished["hiccup"] != ReplaySucceeded {
		t.Fatalf("a renew error is logged, not fatal: %s", jobs2.finished["hiccup"])
	}
	jobs2.renewErr = nil
	SetAfterBatch(r2, func(int64) bool { return false })
	jobs2.stored["killed"], jobs2.queue = jobOf("killed", replayV3, w), []ClaimedReplay{{Org: testOrg, ID: "killed"}}
	r2.RunOnce(bg)
	if _, ok := jobs2.finished["killed"]; ok {
		t.Fatal("a killed runner finishes nothing")
	}
	jobs2.loadErr = errors.New("gone")
	jobs2.queue = []ClaimedReplay{{Org: testOrg, ID: "x"}}
	r2.RunOnce(bg)
	// Run: claims until ctx ends, Max at once; Wake makes it look now.
	jobs3 := newJobs()
	for _, id := range []string{"a", "b", "c"} {
		jobs3.stored[id] = jobOf(id, replayV3, w)
		jobs3.queue = append(jobs3.queue, ClaimedReplay{Org: testOrg, ID: id})
	}
	r3 := &Runner{Engine: &Engine{Src: &memSource{rows: fixture(t, 100)}}, Jobs: jobs3, Worker: "w", Every: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(bg, 3*time.Second)
	go func() {
		for {
			jobs3.mu.Lock()
			done := len(jobs3.finished) == 3
			jobs3.mu.Unlock()
			if done {
				cancel()
				return
			}
			r3.Wake()
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := r3.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	if len(jobs3.finished) != 3 {
		t.Fatalf("Run finished %d", len(jobs3.finished))
	}
	if p := ProgressOf(5, nil); p.Percent != 0 {
		t.Fatal(p)
	}
	z := int64(0)
	if p := ProgressOf(0, &z); p.Percent != 100 {
		t.Fatal(p)
	}
	var rp Replay
	rp.WithResult(nil)
	rp.WithResult(&ReplayResult{ResultDigest: "sha256:x"})
	if rp.ResultDigest == nil || *rp.ResultDigest != "sha256:x" {
		t.Fatal("WithResult")
	}
}

func TestDetector(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Kolkata")
	// An agent with 10 days of steady traffic, then a 9× spike, a first github.merge_pr and three
	// mismatches in its last hour.
	end := time.Date(2026, 10, 9, 8, 30, 0, 0, time.UTC) // 14:00 IST
	start := end.Add(-10 * 24 * time.Hour)
	tx := &fakeTx{agents: []AgentInfo{{ID: "agt_g", Name: "growth-agent", CreatedAt: start}, {ID: "agt_s", Name: "console", Kind: "system", CreatedAt: start}}}
	for h := start; h.Before(end.Add(-time.Hour)); h = h.Add(time.Hour) {
		lh := h.In(loc).Hour()
		if lh >= 11 && lh < 20 {
			tx.buckets = append(tx.buckets, BucketRow{Agent: "agt_g", Start: h, Tool: "slack.post_message", Actions: 7, Writes: 7, SideRank: 3})
		}
	}
	last := end.Add(-time.Hour)
	tx.buckets = append(tx.buckets,
		BucketRow{Agent: "agt_g", Start: last, Tool: "slack.post_message", Actions: 80, Writes: 80, SideRank: 3, Mismatch: 3},
		BucketRow{Agent: "agt_g", Start: last, Tool: "github.merge_pr", Actions: 2, Writes: 2, SideRank: 3})
	tx.refs = []RefRow{{Agent: "agt_g", Start: last, NewRefs: 1}}
	f := &fakeStore{org: OrgInfo{Timezone: "Asia/Kolkata", Settings: DefaultSettings()}, tx: tx, locked: true, detectOrgs: []ids.OrgID{testOrg}}
	published := 0
	d := &Detector{Store: f, OnPublished: func(_ ids.OrgID, n int) { published += n }, Now: func() time.Time { return end }}
	if d.Health() != "ok" {
		t.Fatal("a fresh detector is ok")
	}
	st := d.Pass(bg, end)
	if st.Orgs != 1 || st.Inserted != 3 || st.Findings != 3 || published != 3 || d.Health() != "ok" || len(tx.runs) != 1 {
		t.Fatalf("pass %+v published %d runs %v", st, published, tx.runs)
	}
	kinds := map[string]bool{}
	for _, u := range tx.ups {
		kinds[u.Finding.Kind] = true
		if ids, _ := u.Finding.Evidence["action_ids"].([]string); len(ids) == 0 {
			t.Errorf("%s: no sample ids", u.Finding.Kind)
		}
	}
	if !kinds[AnomalyVolumeSpike] || !kinds[AnomalyNewTool] || !kinds[AnomalyMismatchSpike] {
		t.Fatalf("kinds %v", kinds)
	}
	// The same hour again: cached baseline, the windows already recorded.
	st = d.Pass(bg, end)
	if st.Inserted != 0 {
		t.Fatalf("second pass %+v", st)
	}
	// Backfill over the whole range: hour by hour, same findings, one run row.
	tx2 := &fakeTx{agents: tx.agents, buckets: tx.buckets, refs: tx.refs}
	f.tx = tx2
	bst, err := d.Backfill(bg, testOrg, start, end)
	if err != nil || bst.Windows != 240 || bst.Inserted != 3 || !strings.HasPrefix(tx2.runs[0], "detect:bf:") {
		t.Fatalf("backfill %+v %v %v", bst, err, tx2.runs)
	}
	if _, err := d.Backfill(bg, testOrg, end, start); err == nil {
		t.Fatal("a reversed range")
	}
	if _, err := d.Backfill(bg, testOrg, start.Add(-60*24*time.Hour), end); err == nil {
		t.Fatal("over 30 days")
	}
	// Another instance holds the lock.
	f.locked = false
	if st, err := d.DetectOrg(bg, testOrg, end); err != nil || st.Skipped != 1 {
		t.Fatalf("locked: %+v %v", st, err)
	}
	if _, err := d.Backfill(bg, testOrg, start, end); err == nil {
		t.Fatal("a backfill that can't lock says so")
	}
	// A failing org read and a failing org list make the detector degraded.
	f.orgErr = errors.New("down")
	if st := d.Pass(bg, end); st.Errors != 1 || d.Health() != "degraded" {
		t.Fatalf("org error %+v %s", st, d.Health())
	}
	f.orgErr = nil
	f.detectOrgs = nil
	if st := d.Pass(bg, end); st.Errors != 1 {
		t.Fatalf("list error %+v", st)
	}
	if _, err := d.Backfill(bg, testOrg, start, end); err != nil && !strings.Contains(err.Error(), "lock") {
		t.Fatal(err)
	}
	f.orgErr = errors.New("down")
	if _, err := d.Backfill(bg, testOrg, start, end); err == nil {
		t.Fatal("backfill org error")
	}
	// Run stops with its context.
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if err := (&Detector{Store: f, Every: time.Millisecond}).Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for r, s := range map[int]string{1: SideRead, 2: SideUnknown, 3: SideWrite, 4: SideDestructive} {
		if sideName(r) != s || sideRank(s) != r {
			t.Fatal(r, s)
		}
	}
	if loadLoc("") != time.UTC || loadLoc("Mars/Base") != time.UTC {
		t.Fatal("loadLoc")
	}
}

func TestFactsCache(t *testing.T) {
	agent := ids.New[ids.AgentKind]()
	now := time.Now()
	f := &fakeStore{factsSet: Settings{AnomalyFacts: true, AnomalyStaleAfterMinutes: 15}, factsLast: now,
		factsEps: []ActiveEpisode{{Agent: agent.String(), Kind: AnomalyVolumeSpike, Severity: SeverityWarn, ActiveUntil: now.Add(time.Hour)},
			{Agent: agent.String(), Kind: AnomalyNewTool, Severity: SeverityHigh, ActiveUntil: now.Add(time.Hour)},
			{Agent: agent.String(), Kind: AnomalyNewTool, Severity: SeverityInfo, ActiveUntil: now.Add(time.Hour)},
			{Agent: "agt_other", Kind: AnomalyOffHours, Severity: SeverityInfo, ActiveUntil: now.Add(time.Hour)}}}
	c := &FactsCache{Store: f}
	got, trace := c.Facts(bg, testOrg, agent, now)
	if !got.Active || got.Severity != SeverityHigh || strings.Join(got.Kinds, ",") != "new_tool,volume_spike" || trace != "anomaly facts: new_tool, volume_spike" {
		t.Fatalf("%+v %q", got, trace)
	}
	for range 50 {
		c.Facts(bg, testOrg, agent, now)
	}
	if c.Lookups() != 1 || f.factsCalls != 1 {
		t.Fatalf("lookups %d", c.Lookups())
	}
	c.Invalidate(testOrg.String())
	c.Facts(bg, testOrg, agent, now)
	c.Invalidate("not-an-id")
	c.Invalidate("")
	c.Facts(bg, testOrg, agent, now)
	if c.Lookups() != 3 {
		t.Fatalf("invalidations: %d lookups", c.Lookups())
	}
	if f, _ := FactsFor(f.factsEps, agent.String(), now.Add(2*time.Hour)); f.Active {
		t.Fatal("past active_until")
	}
	// Stale: the last run is older than the setting.
	c2 := &FactsCache{Store: f, Now: func() time.Time { return now.Add(20 * time.Minute) }}
	if got, trace := c2.Facts(bg, testOrg, agent, now); !got.Active || got.Kinds[0] != KindDetectorStale || !strings.Contains(trace, "last run 20 min ago") {
		t.Fatalf("stale %+v %q", got, trace)
	}
	// Off: inactive.
	f.factsSet.AnomalyFacts = false
	c3 := &FactsCache{Store: f}
	if got, _ := c3.Facts(bg, testOrg, agent, now); got.Active {
		t.Fatal("off")
	}
	// A lookup error: fail closed when the facts were on or unknown, quiet when they were known off.
	f.factsErr = errors.New("down")
	c4 := &FactsCache{Store: f, Log: slog.New(slog.DiscardHandler)}
	if got, trace := c4.Facts(bg, testOrg, agent, now); !got.Active || trace != AnomalyLookupFailedTrace {
		t.Fatalf("unknown opt-in: %+v", got)
	}
	f.factsErr = nil
	c3.Invalidate("")
	c3.Facts(bg, testOrg, agent, now)
	f.factsErr = errors.New("down")
	c3.TTL, c3.OffTTL = time.Nanosecond, time.Nanosecond
	time.Sleep(time.Millisecond)
	if got, _ := c3.Facts(bg, testOrg, agent, now); got.Active {
		t.Fatal("known off: a lookup error doesn't turn it on")
	}
	if severityRank("nope") != 0 {
		t.Fatal("rank")
	}
}

func TestAPIHandlers(t *testing.T) {
	anm := ids.New[ids.AnomalyKind]().String()
	f := &fakeStore{org: OrgInfo{Settings: DefaultSettings(), Timezone: "UTC"}, deedID: "deed_y",
		versions:  map[string]*ResolvedVersion{"acme-sim|3": {VersionInfo: VersionInfo{PolicyID: "pol_x", Name: "acme-sim", Version: 3}, Latest: 3, Found: true}},
		anomalies: []Anomaly{{ID: anm}}, groups: map[string][]StatsRow{ByNone: {{Actions: 1}}}}
	s := newSvc(f)
	s.Facts = &FactsCache{Store: f}
	routes := Routes(s)
	mux := http.NewServeMux()
	for _, rt := range routes {
		h := rt.Handler
		mux.HandleFunc(rt.Method+" "+rt.Pattern, func(w http.ResponseWriter, r *http.Request) {
			if err := h(w, r); err != nil {
				e := httpx.AsError(err)
				if e == nil {
					e = httpx.E(httpx.CodeInternalError)
				}
				w.WriteHeader(e.Status)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": e.Code, "message": e.Message})
			}
		})
	}
	admin := &identity.Principal{Kind: httpx.KindUser, OrgID: testOrg, UserID: ids.New[ids.UserKind](), Role: httpx.RoleAdmin}
	call := func(method, path, body string) (int, string) {
		var rd *bytes.Reader
		if body != "" {
			rd = bytes.NewReader([]byte(body))
		} else {
			rd = bytes.NewReader(nil)
		}
		r := httptest.NewRequest(method, path, rd)
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		r = r.WithContext(identity.WithPrincipal(r.Context(), admin))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var buf bytes.Buffer
		if json.Compact(&buf, w.Body.Bytes()) != nil {
			return w.Code, w.Body.String()
		}
		return w.Code, buf.String()
	}
	cases := []struct {
		method, path, body string
		status             int
		has                string
	}{
		{"POST", "/v1/policies/acme-sim/versions/3/replay", `{}`, 202, `"object":"replay"`},
		{"POST", "/v1/policies/acme-sim/versions/x/replay", ``, 400, "version is a number"},
		{"GET", "/v1/replays?status=nope", "", 400, "status must be"},
		{"GET", "/v1/replays?bogus=1", "", 400, "Unknown parameter"},
		{"GET", "/v1/replays/" + ids.New[ids.ReplayKind]().String(), "", 404, "No replay"},
		{"POST", "/v1/replays/nope/cancel", "", 404, "No replay"},
		{"GET", "/v1/insights/stats?window=7d", "", 200, `"object":"insights_stats"`},
		{"GET", "/v1/insights/stats?by=team", "", 400, "by must be"},
		{"GET", "/v1/insights/shadow", "", 404, "No shadow policy"},
		{"GET", "/v1/insights/shadow?from=nope", "", 400, "RFC 3339"},
		{"GET", "/v1/insights/shadow?from=2026-10-03T00:00:00Z&to=2026-10-01T00:00:00Z", "", 400, "The window must start before it ends"},
		{"GET", "/v1/insights/anomalies", "", 200, `"baseline_building":[]`},
		{"GET", "/v1/insights/anomalies?status=gone", "", 400, "status must be"},
		{"GET", "/v1/insights/anomalies?kind=weird", "", 400, "kind is one of"},
		{"GET", "/v1/insights/anomalies?since=yesterday", "", 400, "since is"},
		{"GET", "/v1/insights/anomalies/" + anm, "", 200, anm},
		{"POST", "/v1/insights/anomalies/" + anm + "/acknowledge", "", 200, anm},
		{"POST", "/v1/insights/anomalies/" + anm + "/resolve", `{"reason":"It was a planned launch."}`, 200, anm},
		{"POST", "/v1/insights/anomalies/" + anm + "/resolve", ``, 400, "A reason is required"},
		{"GET", "/v1/insights/settings", "", 200, `"object":"insights_settings"`},
		{"PATCH", "/v1/insights/settings", `{"anomaly_facts":true,"reason":"Require approval while an agent spikes"}`, 200, `"deed":"deed_y"`},
	}
	for _, c := range cases {
		code, body := call(c.method, c.path, c.body)
		if code != c.status || !strings.Contains(body, c.has) {
			t.Errorf("%s %s: %d %s", c.method, c.path, code, body)
		}
	}
	// No principal, and no service.
	r := httptest.NewRequest("GET", "/v1/insights/settings", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("no principal: %d", w.Code)
	}
	nilAPI := &api{}
	if _, _, err := nilAPI.who(r); codeOf(err) != httpx.CodeServiceUnavailable {
		t.Fatal(err)
	}
}
