//go:build integration

package learn_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	simfiles "github.com/deedglass/deedglass/examples/sim"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/fake"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/learn/sim"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/learnstore"
	"github.com/deedglass/deedglass/internal/store/policystore"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

var (
	bg     = context.Background()
	update = flag.Bool("update-learn", false, "rewrite internal/learn/testdata/*.golden.json")
	// simEnd is the seed-7 window's end: 2026-10-04 12:00 IST.
	simEnd = time.Date(2026, 10, 4, 6, 30, 0, 0, time.UTC)
)

type env struct {
	t      testing.TB
	e      *storetest.Env
	led    *ledger.Ledger
	people *identity.Service
	ls     *learnstore.Store
	keys   *identity.KeyAuth
	logs   *bytes.Buffer
}

func newEnv(t testing.TB) *env {
	t.Helper()
	e := storetest.New(t, storetest.Options{MaxConns: 16, AsyncCommit: true})
	seed, err := deed.NewSeed(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(bg, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	v := &env{t: t, e: e, logs: &bytes.Buffer{}}
	log := slog.New(slog.NewJSONHandler(&lockedBuf{b: v.logs}, nil))
	v.led = ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(log))
	base, _ := url.Parse("http://127.0.0.1:8080")
	pepper := bytes.Repeat([]byte{7}, 32)
	if v.people, err = identity.New(identity.Options{DB: e.DB, Ledger: v.led, Pepper: pepper, BaseURL: base, Logger: log}); err != nil {
		t.Fatal(err)
	}
	if v.keys, err = identity.NewKeyAuth(identity.KeyAuthOptions{DB: e.DB, Pepper: pepper, Logger: log}); err != nil {
		t.Fatal(err)
	}
	v.ls = learnstore.New(e.DB, v.led)
	return v
}

type lockedBuf struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (v *env) simulate(slug string) *sim.Result {
	v.t.Helper()
	res, err := sim.Run(bg, sim.Env{DB: v.e.DB, Ledger: v.led, People: v.people}, sim.Options{Seed: 7, Days: 7, End: simEnd, OrgSlug: slug})
	if err != nil {
		v.t.Fatalf("simulate: %v", err)
	}
	if !res.ChainIntact {
		v.t.Fatalf("the chain didn't check out: %s", res.ChainStatus)
	}
	return res
}

func (v *env) scalar(q string, args ...any) int64 {
	v.t.Helper()
	c := v.e.Admin(v.t)
	var n int64
	if err := c.QueryRow(bg, q, args...).Scan(&n); err != nil {
		v.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (v *env) policySvc() *policy.Service {
	return policy.NewService(policystore.NewRepo(v.e.DB, v.led))
}

// push saves src as the next version of its policy and returns the version number.
func (v *env) push(org ids.OrgID, src []byte) int {
	v.t.Helper()
	svc := v.policySvc()
	caller := policy.Caller{Org: org.String(), Actor: policy.SystemActor}
	c, _ := policy.Compile(src, policy.Options{})
	ver, _, err := svc.AddVersion(bg, caller, c.Name, policy.PushRequest{Source: src, Origin: policy.OriginManual})
	if err != nil {
		v.t.Fatal(err)
	}
	return ver.Version
}

func (v *env) service() *learn.Service {
	return &learn.Service{Store: v.ls, Inputs: v.ls.Inputs, StatsTTL: -1}
}

// TestSeed7 runs the default scenario once and checks everything S27 §6 asks of it.
func TestSeed7(t *testing.T) {
	if testing.Short() {
		t.Skip("a week of simulated traffic")
	}
	v := newEnv(t)
	start := time.Now()
	res := v.simulate("acme-sim")
	t.Logf("simulated %d actions, %d deeds in %s", res.Submitted, res.Deeds, time.Since(start))
	org := res.Org
	w := learn.Window{From: res.Window.Start, To: res.Window.End}

	t.Run("honest", func(t *testing.T) {
		if n := v.scalar(`SELECT count(*) FROM deedglass.actions WHERE org_id = $1 AND witness_verdict IS NOT NULL AND witness_method <> 'simulated'`, org); n != 0 {
			t.Fatalf("%d verdicts aren't marked simulated", n)
		}
		if n := v.scalar(`SELECT count(*) FROM deedglass.actions WHERE org_id = $1 AND decision IS NOT NULL
		    AND (policy_input ->> 'time')::timestamptz <> evaluated_at`, org); n != 0 {
			t.Fatalf("%d actions' evaluated_at differs from policy_input.time", n)
		}
		if n := v.scalar(`SELECT count(*) FROM deedglass.actions AS a JOIN deedglass.deeds AS d ON d.id = a.deed_id
		    WHERE a.org_id = $1 AND (d.body ->> 'issued_at')::timestamptz <= a.evaluated_at`, org); n != 0 {
			t.Fatalf("%d deeds were issued before their (virtual) decision", n)
		}
		if n := v.scalar(`SELECT count(*) FROM deedglass.orgs WHERE id = $1 AND settings ->> 'simulated' = 'true'`, org); n != 1 {
			t.Fatal("the org isn't flagged simulated")
		}
		if res.Approvals.Requested < 300 || res.Counts.Mismatch != 3 || res.Submitted != res.Planned {
			t.Fatalf("summary: %+v %+v submitted %d planned %d", res.Approvals, res.Counts, res.Submitted, res.Planned)
		}
	})

	v1 := compileFile(t, simfiles.AcmeSimV1)
	v3src := simfiles.AcmeSimV3
	v3n := v.push(org, v3src)
	v3 := compileFile(t, v3src)

	var oracle map[[2]string]int64
	t.Run("replay oracle", func(t *testing.T) {
		oracle = map[[2]string]int64{}
		var after learn.Cursor
		var rows []learn.InputRow
		for {
			b, next, err := v.ls.Inputs.Scan(bg, org, w, after, 1000)
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, b...)
			if len(b) < 1000 {
				break
			}
			after = next
		}
		for _, r := range rows {
			if r.Recorded.Source == policy.SourceBuiltin && r.Recorded.Code == string(policy.CodePolicyUnavailable) {
				t.Fatalf("%s was decided by policy_unavailable", r.ActionID)
			}
			var in policy.Input
			if err := json.Unmarshal(r.Input, &in); err != nil {
				t.Fatal(err)
			}
			d := policy.Evaluate(bg, v3, in, policy.EvalOptions{Mode: policy.ModeShadow})
			if string(d.Outcome) != r.Recorded.Decision {
				oracle[[2]string{r.Recorded.Decision, string(d.Outcome)}]++
			}
		}
		e := &learn.Engine{Src: v.ls.Inputs}
		got, err := e.Run(bg, org, learn.Candidate{Compiled: v3, Version: v3n}, learn.Baseline{}, w, nil)
		if err != nil {
			t.Fatal(err)
		}
		m := map[[2]string]int64{}
		for _, tr := range got.Transitions {
			m[[2]string{tr.From, tr.To}] = tr.Count
		}
		if !mapsEqual(m, oracle) || got.Summary.Unknown != 0 || got.Summary.Actions != int64(len(rows)) {
			t.Fatalf("engine %v, oracle %v (summary %+v)", m, oracle, got.Summary)
		}
		t.Logf("replay v3: %s · %s", learn.ReplaySummaryLine(got.Summary), got.ResultDigest)
		writeGolden(t, "replay_seed7_v3.golden.json", normalizeReplay(got))
		// Determinism: any worker count, any batch size, twice.
		for _, cfg := range []struct{ batch, workers int }{{1, 1}, {7, 8}, {1000, 8}, {1000, 1}} {
			e := &learn.Engine{Src: v.ls.Inputs, Batch: cfg.batch, Workers: cfg.workers}
			again, err := e.Run(bg, org, learn.Candidate{Compiled: v3, Version: v3n}, learn.Baseline{}, w, nil)
			if err != nil || again.ResultDigest != got.ResultDigest {
				t.Fatalf("batch %d workers %d: %v %s vs %s", cfg.batch, cfg.workers, err, again.ResultDigest, got.ResultDigest)
			}
		}
		prev := runtime.GOMAXPROCS(1)
		again, err := e.Run(bg, org, learn.Candidate{Compiled: v3, Version: v3n}, learn.Baseline{}, w, nil)
		runtime.GOMAXPROCS(prev)
		if err != nil || again.ResultDigest != got.ResultDigest {
			t.Fatalf("GOMAXPROCS 1: %v", err)
		}
		// The recorded version against itself changes nothing; so does a version baseline equal to the candidate.
		self, err := e.Run(bg, org, learn.Candidate{Compiled: v1, Version: 1}, learn.Baseline{}, w, nil)
		if err != nil || self.Summary.Changed != 0 || self.Summary.RuleChanged != 0 {
			t.Fatalf("v1 vs recorded: %v %+v", err, self.Summary)
		}
		same, err := e.Run(bg, org, learn.Candidate{Compiled: v3}, learn.Baseline{Type: learn.BaselineVersion, Compiled: v3, Version: v3n}, w, nil)
		if err != nil || same.Summary.Changed != 0 || same.Summary.RuleChanged != 0 {
			t.Fatalf("baseline = candidate: %v %+v", err, same.Summary)
		}
	})

	t.Run("replay job lost and reclaimed", func(t *testing.T) {
		svc := v.service()
		r, err := svc.CreateReplay(bg, org, learn.Actor{Type: "system", ID: "deedglass"}, "acme-sim", v3n, learn.ReplayRequest{From: &w.From, To: &w.To})
		if err != nil {
			t.Fatal(err)
		}
		killed := &learn.Runner{Engine: &learn.Engine{Src: v.ls.Inputs, Batch: 1000}, Jobs: v.ls, Worker: "killed", Lease: time.Second}
		learn.SetAfterBatch(killed, func(int64) bool { return false })
		if n, err := killed.RunOnce(bg); err != nil || n != 1 {
			t.Fatalf("killed runner: %d %v", n, err)
		}
		time.Sleep(1200 * time.Millisecond) // the lease runs out
		ok := &learn.Runner{Engine: &learn.Engine{Src: v.ls.Inputs}, Jobs: v.ls, Worker: "second", Lease: 30 * time.Second}
		if n, err := ok.RunOnce(bg); err != nil || n != 1 {
			t.Fatalf("second runner: %d %v", n, err)
		}
		got, err := svc.GetReplay(bg, org, r.ID)
		if err != nil || got.Status != learn.ReplaySucceeded || got.Attempts != 2 || got.ResultDigest == nil {
			t.Fatalf("%v %+v", err, got)
		}
		e := &learn.Engine{Src: v.ls.Inputs}
		direct, _ := e.Run(bg, org, learn.Candidate{Compiled: v3, Version: v3n, VersionID: got.Candidate.VersionID, PolicyID: got.Candidate.PolicyID},
			learn.Baseline{}, w, nil)
		if *got.ResultDigest != direct.ResultDigest {
			t.Fatalf("the job's digest %s differs from a direct run's %s", *got.ResultDigest, direct.ResultDigest)
		}
		// A finished replay can't be cancelled; a window over 90 days and an unknown version are refused.
		if _, err := svc.CancelReplay(bg, org, r.ID, learn.Actor{Type: "system", ID: "deedglass"}); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeConflict {
			t.Fatalf("cancel a finished replay: %v", err)
		}
		long := w.To.Add(-91 * 24 * time.Hour)
		if _, err := svc.CreateReplay(bg, org, learn.Actor{Type: "system", ID: "x"}, "acme-sim", v3n, learn.ReplayRequest{From: &long, To: &w.To}); httpx.AsError(err) == nil ||
			httpx.AsError(err).Code != httpx.CodeReplayWindowInvalid {
			t.Fatalf("91 days: %v", err)
		}
		if _, err := svc.CreateReplay(bg, org, learn.Actor{Type: "system", ID: "x"}, "acme-sim", 99, learn.ReplayRequest{}); httpx.AsError(err) == nil ||
			!strings.Contains(err.Error(), "Versions 1 to") {
			t.Fatalf("unknown version: %v", err)
		}
		// A queued replay cancels at once.
		q, err := svc.CreateReplay(bg, org, learn.Actor{Type: "system", ID: "x"}, "acme-sim", 1, learn.ReplayRequest{})
		if err != nil {
			t.Fatal(err)
		}
		c, err := svc.CancelReplay(bg, org, q.ID, learn.Actor{Type: "system", ID: "x"})
		if err != nil || c.Status != learn.ReplayCancelled {
			t.Fatalf("cancel queued: %v %+v", err, c)
		}
	})

	t.Run("shadow", func(t *testing.T) {
		rep, err := v.service().Shadow(bg, org, learn.ShadowQuery{})
		if err != nil {
			t.Fatal(err)
		}
		vid := rep.Shadow.VersionID
		agree := v.scalar(`SELECT count(*) FROM deedglass.actions WHERE org_id = $1 AND shadow_version_id = $2 AND shadow_decision = decision`, org, vid)
		disagree := v.scalar(`SELECT count(*) FROM deedglass.actions WHERE org_id = $1 AND shadow_version_id = $2 AND shadow_decision IS DISTINCT FROM decision
		    AND shadow_decision IS NOT NULL`, org, vid)
		if rep.Summary.Agree != agree || rep.Summary.Disagree != disagree || disagree == 0 {
			t.Fatalf("report %+v, SQL agree %d disagree %d", rep.Summary, agree, disagree)
		}
		for _, tr := range rep.Transitions {
			n := v.scalar(`SELECT count(*) FROM deedglass.actions WHERE org_id = $1 AND shadow_version_id = $2 AND decision = $3 AND shadow_decision = $4`,
				org, vid, tr.Enforce, tr.Shadow)
			if n != tr.Count {
				t.Fatalf("%s → %s: report %d, SQL %d", tr.Enforce, tr.Shadow, tr.Count, n)
			}
		}
		if !strings.HasPrefix(rep.Sentence, "Shadow acme-sim v2 would have") {
			t.Fatalf("sentence %q", rep.Sentence)
		}
		t.Log(rep.Sentence)
	})

	var stats *learn.StatsObject
	t.Run("stats", func(t *testing.T) {
		var err error
		stats, err = v.service().Stats(bg, org, learn.StatsQuery{From: w.From, To: w.To, By: learn.ByRule})
		if err != nil {
			t.Fatal(err)
		}
		var ord *learn.Group
		for i := range stats.Groups {
			if g := stats.Groups[i]; g.Key != nil && g.Key.RuleID != nil && *g.Key.RuleID == "approve-order-updates" {
				ord = &stats.Groups[i]
			}
		}
		if ord == nil || ord.Approval.Rate == nil || *ord.Approval.Rate < 0.95 {
			t.Fatalf("approve-order-updates: %+v", ord)
		}
		var pressure *learn.BudgetUse
		for i := range stats.Budgets {
			if stats.Budgets[i].Budget == "refunds-per-day" {
				pressure = &stats.Budgets[i]
			}
		}
		if pressure == nil || pressure.DaysOver80 != 3 {
			t.Fatalf("refunds-per-day: %+v", pressure)
		}
		if stats.Totals.Actions != v.scalar(`SELECT count(*) FROM deedglass.actions WHERE org_id = $1 AND evaluated_at >= $2 AND evaluated_at < $3`, org, w.From, w.To) {
			t.Fatal("totals don't match the actions in the window")
		}
		writeGolden(t, "stats_seed7.golden.json", normalizeStats(stats))
	})

	t.Run("backfill", func(t *testing.T) {
		lc, err := pgx.Connect(bg, v.e.AppURL)
		if err != nil {
			t.Fatal(err)
		}
		defer lc.Close(bg)
		if _, err := lc.Exec(bg, "LISTEN deedglass_events"); err != nil {
			t.Fatal(err)
		}
		det := &learn.Detector{Store: v.ls}
		st, err := det.Backfill(bg, org, w.From, w.To.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		got := episodes(t, v, org)
		want := []string{"mismatch_spike ops-agent ", "new_tool ops-agent github.merge_pr", "off_hours growth-agent ",
			"user_ref_burst support-bot ", "volume_spike growth-agent "}
		if !slices.Equal(got, want) {
			t.Fatalf("episodes %q, want exactly %q (stats %+v)", got, want, st)
		}
		// anomaly.detected went out on deedglass_events (so SSE and webhooks carry it).
		seen := 0
		ctx, cancel := context.WithTimeout(bg, 3*time.Second)
		defer cancel()
		for seen < len(want) {
			n, err := lc.WaitForNotification(ctx)
			if err != nil {
				break
			}
			if strings.Contains(n.Payload, `"type":"anomaly.detected"`) {
				seen++
			}
		}
		if seen != len(want) {
			t.Fatalf("%d anomaly.detected events, want %d", seen, len(want))
		}
		// A second backfill finds the same windows already recorded: nothing new.
		again, err := det.Backfill(bg, org, w.From, w.To.Add(time.Hour))
		if err != nil || again.Inserted != 0 || again.Published != 0 {
			t.Fatalf("second backfill: %v %+v", err, again)
		}
		// After a backfill nothing is active, and every episode is open.
		n := v.scalar(`SELECT count(*) FROM deedglass.anomalies WHERE org_id = $1 AND (active_until > now() OR status <> 'open')`, org)
		if n != 0 {
			t.Fatalf("%d episodes active or not open", n)
		}
		evidenceAllowList(t, v, org)
	})

	t.Run("two orgs", func(t *testing.T) {
		b, err := identity.CreateOrg(bg, v.e.DB, v.led, "Globex", "globex", "owner@globex.example.test")
		if err != nil {
			t.Fatal(err)
		}
		src := &learnstore.Inputs{DB: v.e.DB}
		r, err := (&learn.Engine{Src: src}).Run(bg, b.Org.ID, learn.Candidate{Compiled: v3}, learn.Baseline{}, w, nil)
		if err != nil || r.Summary.Actions != 0 || src.Scanned.Load() != 0 {
			t.Fatalf("org B saw %d actions (%d scanned): %v", r.Summary.Actions, src.Scanned.Load(), err)
		}
	})

	t.Run("a second run gives the same numbers", func(t *testing.T) {
		res2 := v.simulate("acme-sim")
		if res2.Slug != "acme-sim-2" || res2.PlanDigest != res.PlanDigest {
			t.Fatalf("second run %s %s vs %s", res2.Slug, res2.PlanDigest, res.PlanDigest)
		}
		s2, err := v.service().Stats(bg, res2.Org, learn.StatsQuery{From: w.From, To: w.To, By: learn.ByRule})
		if err != nil {
			t.Fatal(err)
		}
		if a, b := mustJSON(normalizeStats(stats)), mustJSON(normalizeStats(s2)); a != b {
			t.Fatalf("stats differ between two runs:\n%s\n%s", a, b)
		}
		v.push(res2.Org, v3src)
		r1, _ := (&learn.Engine{Src: v.ls.Inputs}).Run(bg, org, learn.Candidate{Compiled: v3}, learn.Baseline{}, w, nil)
		r2, _ := (&learn.Engine{Src: v.ls.Inputs}).Run(bg, res2.Org, learn.Candidate{Compiled: v3}, learn.Baseline{}, w, nil)
		if a, b := mustJSON(normalizeReplay(r1)), mustJSON(normalizeReplay(r2)); a != b {
			t.Fatalf("replays differ between two runs:\n%s\n%s", a, b)
		}
	})
}

func compileFile(t testing.TB, src []byte) *policy.Compiled {
	t.Helper()
	c, d := policy.Compile(src, policy.Options{})
	if c == nil {
		t.Fatal(d)
	}
	return c
}

func mapsEqual(a, b map[[2]string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func episodes(t testing.TB, v *env, org ids.OrgID) []string {
	t.Helper()
	c := v.e.Admin(t)
	rows, err := c.Query(bg, `SELECT n.kind || ' ' || g.name || ' ' || COALESCE(n.tool, '') FROM deedglass.anomalies n
	    JOIN deedglass.agents g ON g.id = n.agent_id WHERE n.org_id = $1 ORDER BY 1`, org)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// evidenceAllowList: an episode's evidence holds only the detector's keys and its hourly points.
func evidenceAllowList(t testing.TB, v *env, org ids.OrgID) {
	t.Helper()
	c := v.e.Admin(t)
	rows, err := c.Query(bg, `SELECT evidence FROM deedglass.anomalies WHERE org_id = $1`, org)
	if err != nil {
		t.Fatal(err)
	}
	evs, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatal(err)
	}
	allowed := append(slices.Clone(learn.EvidenceKeys), "points")
	for _, raw := range evs {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		for k := range m {
			if !slices.Contains(allowed, k) {
				t.Fatalf("evidence key %q isn't allowed: %s", k, raw)
			}
		}
		if ids, _ := m["action_ids"].([]any); len(ids) > 10 {
			t.Fatalf("%d action ids", len(ids))
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// normalizeStats drops what differs between runs: the generation time, the window's instant and
// agent ids.
func normalizeStats(s *learn.StatsObject) any {
	var m map[string]any
	_ = json.Unmarshal([]byte(mustJSON(s)), &m)
	delete(m, "generated_at")
	for _, b := range m["budgets"].([]any) {
		bm := b.(map[string]any)
		if bm["agent"] != nil {
			bm["agent"] = "agt_…"
		}
	}
	return m
}

// normalizeReplay drops ids (samples, version ids) and timing.
func normalizeReplay(r *learn.ReplayResult) any {
	var m map[string]any
	_ = json.Unmarshal([]byte(mustJSON(r)), &m)
	for _, k := range []string{"elapsed_ms", "result_digest"} {
		delete(m, k)
	}
	if c, ok := m["candidate"].(map[string]any); ok {
		delete(c, "policy")
		delete(c, "version_id")
	}
	for _, list := range []string{"transitions", "rule_changes"} {
		if ts, ok := m[list].([]any); ok {
			for _, x := range ts {
				x.(map[string]any)["samples"] = "…"
			}
		}
	}
	return m
}

func writeGolden(t testing.TB, name string, v any) {
	t.Helper()
	path := filepath.Join("testdata", name)
	got := mustJSON(v) + "\n"
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -args -update-learn to write it)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs from the golden (run with -args -update-learn after checking the change):\n%s", name, got)
	}
}

// --- anomaly facts (S27 §6, the opt-in) ---

const recipe17 = `deedglass: policy/v1
name: anomaly-recipe
description: S27 recipe 17 with fake tools.
defaults: { decision: deny, reason: No rule allows this action. }
rules:
  - id: approve-while-anomalous
    match: { side_effect: [write, destructive] }
    when: anomaly.active && anomaly.severity in ["warn", "high"]
    decision: require_approval
    reason: This agent is behaving unusually right now. A person approves writes until the anomaly is resolved.
    approval: { approvers: [role:admin], required: 1, expires_in: 1h }
  - id: allow-fake
    match: { tool: ["fake.*"] }
    decision: allow
`

type failingFacts struct{ learn.Store }

func (failingFacts) FactsState(context.Context, ids.OrgID, time.Time) (learn.Settings, []learn.ActiveEpisode, time.Time, error) {
	return learn.Settings{}, nil, time.Time{}, context.DeadlineExceeded
}

func TestAnomalyFacts(t *testing.T) {
	v := newEnv(t)
	c, err := identity.CreateOrg(bg, v.e.DB, v.led, "Acme", "acme", "owner@acme.example.test")
	if err != nil {
		t.Fatal(err)
	}
	org := c.Org.ID
	owner := learn.Actor{Type: "user", ID: c.Owner.ID.String()}
	agent, err := v.people.CreateAgent(bg, identity.Operator, org, identity.CreateAgentInput{Name: "growth-agent"})
	if err != nil {
		t.Fatal(err)
	}
	aid := ids.MustParse[ids.AgentKind](agent.ID)
	svc := policy.NewService(policystore.NewRepo(v.e.DB, v.led))
	caller := policy.Caller{Org: org.String(), Actor: policy.SystemActor}
	_, ver, err := svc.Create(bg, caller, policy.PushRequest{Source: []byte(recipe17)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Activate(bg, caller, "anomaly-recipe", policy.VersionRef{Number: ver.Version}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "S27 test"}); err != nil {
		t.Fatal(err)
	}
	facts := &learn.FactsCache{Store: v.ls, TTL: time.Minute, OffTTL: time.Minute}
	v.keys.Listener().Listen(store.LearnChannel, facts.Invalidate, func() { facts.Invalidate("") })
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	go v.keys.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for !v.keys.Listener().Healthy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ls := learn.Service{Store: v.ls, Inputs: v.ls.Inputs, Facts: facts}
	now := time.Now().UTC()
	// An active high episode, as the detect runner records it.
	open := func(kind string) string {
		var id string
		if _, err := v.ls.WithDetectLock(bg, org, func(ctx context.Context, tx learn.DetectTx) error {
			r, err := tx.UpsertEpisode(ctx, learn.EpisodeInput{Agent: agent.ID, AgentName: "growth-agent",
				Finding:       learn.Finding{Kind: kind, Severity: learn.SeverityWarn, Score: 9.4, Title: "212 calls", Evidence: map[string]any{"count": 212}},
				Window:        learn.DetectWindow{Start: now.Add(-time.Hour), End: now},
				ActiveMinutes: 60, Now: now})
			id = r.ID
			if err != nil {
				return err
			}
			return tx.RecordRun(ctx, "detect", now.Truncate(5*time.Minute).Format("2006-01-02T15:04Z"), now, now, "succeeded", map[string]any{}, "")
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	anm := open(learn.AnomalyVolumeSpike)
	// Off (the default): inactive even with an active episode, with no lookup after the first.
	if f, _ := facts.Facts(bg, org, aid, now); f.Active {
		t.Fatal("facts are off but an anomaly was active")
	}
	n0 := facts.Lookups()
	for range 100 {
		facts.Facts(bg, org, aid, now)
	}
	if facts.Lookups() != n0 {
		t.Fatalf("off: %d lookups for 100 decisions", facts.Lookups()-n0)
	}
	// The settings read the notice: the enforced policy reads anomaly.active while the facts are off.
	so, err := ls.SettingsOf(bg, org)
	if err != nil || !so.ActivePolicyReadsAnomaly || so.Notice == nil || !strings.Contains(*so.Notice, "anomaly-recipe v1 reads anomaly.active") {
		t.Fatalf("settings %+v %v", so, err)
	}
	// On: active within 2 s (NOTIFY deedglass_learn), and the switch is sealed.
	on := true
	so, err = ls.PatchSettings(bg, org, owner, learn.SettingsPatch{AnomalyFacts: &on, Reason: "Require approval while an agent spikes"}, "")
	if err != nil || so.Deed == nil || so.Notice != nil {
		t.Fatalf("patch: %v %+v", err, so)
	}
	waitFor(t, "active after the opt-in", func() bool { f, _ := facts.Facts(bg, org, aid, time.Now()); return f.Active })
	f, trace := facts.Facts(bg, org, aid, time.Now())
	if f.Severity != learn.SeverityWarn || !slices.Equal(f.Kinds, []string{learn.AnomalyVolumeSpike}) || trace != "anomaly facts: volume_spike" {
		t.Fatalf("facts %+v %q", f, trace)
	}
	// The Gate: recipe 17 sends the agent's next write to approval, with the trace line.
	reg := policy.NewRegistry(policystore.NewRepo(v.e.DB, nil), nil)
	gp, err := gate.New(gate.Options{DB: v.e.DB, Ledger: v.led, Policies: reg, Usage: policystore.NewUsage(v.e.DB),
		Resolver: connector.NewRegistry(fake.New()), Anomalies: facts})
	if err != nil {
		t.Fatal(err)
	}
	pr := identity.Principal{Kind: httpx.KindAgentKey, OrgID: org, AgentID: aid, AgentName: "growth-agent", AgentStatus: "active", Scopes: httpx.DefaultAgentScopes}
	a, err := gp.Submit(bg, pr, gate.ActionRequest{Tool: "fake.write", Args: json.RawMessage(`{"table":"posts","rows":1}`), WaitSet: true, Wait: 5 * time.Second})
	if err != nil || a.Status != ledger.StatusPendingApproval || a.Decision == nil || a.Decision.Anomaly == nil || *a.Decision.Anomaly != "anomaly facts: volume_spike" {
		t.Fatalf("anomalous write: %v %+v", err, a)
	}
	// active_until passes: inactive, and the episode stays open.
	if f, _ := facts.Facts(bg, org, aid, time.Now().Add(2*time.Hour)); f.Active {
		t.Fatal("still active after active_until")
	}
	got, err := ls.Anomaly(bg, org, anm)
	if err != nil || got.Status != learn.StatusOpen {
		t.Fatalf("%v %+v", err, got)
	}
	// Acknowledge changes nothing but the mark; resolve ends the activity and seals the deed.
	if _, err := ls.Acknowledge(bg, org, anm, owner); err != nil {
		t.Fatal(err)
	}
	if f, _ := facts.Facts(bg, org, aid, time.Now()); !f.Active {
		t.Fatal("acknowledging ended the activity")
	}
	if _, err := ls.Resolve(bg, org, anm, owner, "", ""); httpx.AsError(err) == nil || httpx.AsError(err).Param != "reason" {
		t.Fatalf("resolve without a reason: %v", err)
	}
	res, err := ls.Resolve(bg, org, anm, owner, "The spike was a planned launch.", "")
	if err != nil || res.Status != learn.StatusResolved || res.Resolved == nil || res.Resolved.Deed == nil {
		t.Fatalf("resolve: %v %+v", err, res)
	}
	waitFor(t, "inactive after the resolve", func() bool { f, _ := facts.Facts(bg, org, aid, time.Now()); return !f.Active })
	chk, err := v.led.CheckChain(bg, org, ledger.Range{}, ledger.CheckOptions{})
	if err != nil || chk.Status != ledger.CheckIntact {
		t.Fatalf("chain: %v %+v", err, chk)
	}
	// The detector stops for 16 minutes: every agent is anomalous (detector_stale), and recipe 17 applies.
	open(learn.AnomalyOffHours)
	late := &learn.FactsCache{Store: v.ls, Now: func() time.Time { return time.Now().Add(16 * time.Minute) }}
	f, trace = late.Facts(bg, org, aid, time.Now())
	if !f.Active || !slices.Equal(f.Kinds, []string{learn.KindDetectorStale}) || !strings.HasPrefix(trace, "anomaly facts: detector_stale (last run 16 min ago") {
		t.Fatalf("stale: %+v %q", f, trace)
	}
	// A lookup that fails: active (fail closed).
	broken := &learn.FactsCache{Store: failingFacts{v.ls}}
	if f, trace := broken.Facts(bg, org, aid, time.Now()); !f.Active || trace != learn.AnomalyLookupFailedTrace {
		t.Fatalf("lookup error: %+v %q", f, trace)
	}
}

func waitFor(t testing.TB, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within 2 s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
