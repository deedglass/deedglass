package learn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

var (
	testOrg = ids.New[ids.OrgKind]()
	now0    = time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)
)

func codeOf(err error) httpx.Code {
	if e := httpx.AsError(err); e != nil {
		return e.Code
	}
	return ""
}

func newSvc(f *fakeStore) *Service {
	return &Service{Store: f, Inputs: &memSource{rows: fixture(&testing.T{}, 30)}, Now: func() time.Time { return now0 }, StatsTTL: -1}
}

func TestCreateReplay(t *testing.T) {
	f := &fakeStore{versions: map[string]*ResolvedVersion{
		"acme-sim|3": {VersionInfo: VersionInfo{PolicyID: "pol_x", Name: "acme-sim", Version: 3, VersionID: "pv_3"}, VersionHash: "sha256:3", Latest: 3, Found: true},
		"pol_x|1":    {VersionInfo: VersionInfo{PolicyID: "pol_x", Name: "acme-sim", Version: 1, VersionID: "pv_1"}, VersionHash: "sha256:1", Latest: 3, Found: true},
	}}
	s := newSvc(f)
	s.Runner = &Runner{}
	by := Actor{Type: "user", ID: "usr_x"}
	r, err := s.CreateReplay(bg, testOrg, by, "acme-sim", 3, ReplayRequest{})
	if err != nil || r.Status != ReplayQueued {
		t.Fatalf("%v %+v", err, r)
	}
	n := f.created[0]
	if !n.Window.To.Equal(now0) || n.Window.To.Sub(n.Window.From) != 7*24*time.Hour || n.Baseline.Type != BaselineRecorded || n.VersionHash != "sha256:3" {
		t.Fatalf("default window and baseline: %+v", n)
	}
	// A version baseline is resolved against the same policy.
	bl := &struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
	}{Type: BaselineVersion, Version: 1}
	if _, err := s.CreateReplay(bg, testOrg, by, "acme-sim", 3, ReplayRequest{Baseline: bl}); err != nil || f.created[1].Baseline.VersionID != "pv_1" {
		t.Fatalf("baseline v1: %v %+v", err, f.created[1].Baseline)
	}
	to := now0.Add(-time.Hour)
	from := to.Add(-time.Hour)
	if _, err := s.CreateReplay(bg, testOrg, by, "acme-sim", 3, ReplayRequest{To: &to}); err != nil || !f.created[2].Window.To.Equal(to) {
		t.Fatalf("to only: %v", err)
	}
	cases := []struct {
		name string
		ref  string
		v    int
		req  ReplayRequest
		code httpx.Code
		msg  string
	}{
		{"from after to", "acme-sim", 3, ReplayRequest{From: &to, To: &from}, httpx.CodeReplayWindowInvalid, MsgWindowInvalid},
		{"no policy", "nope", 3, ReplayRequest{}, httpx.CodeNotFound, "No policy nope"},
		{"no version", "acme-sim", 9, ReplayRequest{}, httpx.CodeNotFound, "No version 9 of acme-sim. Versions 1 to 3 exist."},
		{"bad baseline", "acme-sim", 3, ReplayRequest{Baseline: &struct {
			Type    string `json:"type"`
			Version int    `json:"version"`
		}{Type: "sideways"}}, httpx.CodeValidationFailed, MsgBaselineType},
		{"baseline without a version", "acme-sim", 3, ReplayRequest{Baseline: &struct {
			Type    string `json:"type"`
			Version int    `json:"version"`
		}{Type: BaselineVersion}}, httpx.CodeValidationFailed, MsgBaselineType},
		{"baseline version missing", "acme-sim", 3, ReplayRequest{Baseline: &struct {
			Type    string `json:"type"`
			Version int    `json:"version"`
		}{Type: BaselineVersion, Version: 7}}, httpx.CodeNotFound, "No version 7"},
	}
	for _, c := range cases {
		_, err := s.CreateReplay(bg, testOrg, by, c.ref, c.v, c.req)
		if codeOf(err) != c.code || !strings.Contains(httpx.AsError(err).Message, c.msg) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	f.recent = ReplaysPerHourPerOrg
	if _, err := s.CreateReplay(bg, testOrg, by, "acme-sim", 3, ReplayRequest{}); codeOf(err) != httpx.CodeRateLimited {
		t.Fatalf("rate limit: %v", err)
	}
	f.recent = 0
	s.Inputs = &memSource{count: MaxReplayActions + 5}
	_, err = s.CreateReplay(bg, testOrg, by, "acme-sim", 3, ReplayRequest{})
	if codeOf(err) != httpx.CodeReplayTooLarge || !strings.Contains(err.Error(), "1,000,005") {
		t.Fatalf("too large: %v", err)
	}
}

func TestGetAndCancelReplay(t *testing.T) {
	f := &fakeStore{}
	s := newSvc(f)
	r, _ := f.CreateReplay(bg, testOrg, NewReplay{})
	if _, err := s.GetReplay(bg, testOrg, "nope"); codeOf(err) != httpx.CodeNotFound {
		t.Fatal(err)
	}
	if _, err := s.GetReplay(bg, testOrg, ids.New[ids.ReplayKind]().String()); codeOf(err) != httpx.CodeNotFound {
		t.Fatal(err)
	}
	if got, err := s.GetReplay(bg, testOrg, r.ID); err != nil || got.ID != r.ID {
		t.Fatal(err)
	}
	if _, err := s.CancelReplay(bg, testOrg, "nope", Actor{}); codeOf(err) != httpx.CodeNotFound {
		t.Fatal(err)
	}
	if got, err := s.CancelReplay(bg, testOrg, r.ID, Actor{}); err != nil || got.Status != ReplayCancelled {
		t.Fatal(err)
	}
	f.cancelEr = ErrStateConflict
	if _, err := s.CancelReplay(bg, testOrg, r.ID, Actor{}); codeOf(err) != httpx.CodeConflict || !strings.Contains(err.Error(), "already finished") {
		t.Fatal(err)
	}
	f.cancelEr = ErrNotFound
	if _, err := s.CancelReplay(bg, testOrg, r.ID, Actor{}); codeOf(err) != httpx.CodeNotFound {
		t.Fatal(err)
	}
	if clipID(strings.Repeat("x", 80)) != strings.Repeat("x", 64)+"…" {
		t.Fatal("clipID")
	}
}

func TestShadowService(t *testing.T) {
	f := &fakeStore{}
	s := newSvc(f)
	if _, err := s.Shadow(bg, testOrg, ShadowQuery{}); codeOf(err) != httpx.CodeNotFound || !strings.Contains(err.Error(), "No shadow policy") {
		t.Fatalf("no shadow: %v", err)
	}
	if _, err := s.Shadow(bg, testOrg, ShadowQuery{ShadowVersionID: "pv_nope"}); codeOf(err) != httpx.CodeNotFound {
		t.Fatalf("unknown version: %v", err)
	}
	f.shadow = &VersionInfo{Name: "acme-sim", Version: 2, VersionID: "pv_2", Live: true}
	f.enforced = &VersionInfo{Name: "acme-sim", Version: 1, VersionID: "pv_1"}
	f.shadowRows = []ShadowRow{
		{Enforce: "allow", Shadow: "allow", Tool: "gmail.create_draft", AgentID: "agt_a", AgentName: "support-bot", EnforceVersionID: "pv_1", Count: 3161},
		{Enforce: "allow", Shadow: "require_approval", ShadowRule: "refund-approval-over-30", Tool: "stripe.create_refund", AgentID: "agt_a", AgentName: "support-bot", EnforceVersionID: "pv_1", Count: 41},
		{Enforce: "allow", Shadow: "deny", ShadowRule: "slack-allowlist", Tool: "slack.post_message", AgentID: "agt_g", AgentName: "growth-agent", EnforceVersionID: "pv_1", Count: 5},
		{Enforce: "require_approval", Shadow: "allow", ShadowRule: "x", Tool: "postgres.execute", AgentID: "agt_o", AgentName: "ops-agent", EnforceVersionID: "pv_1", Approval: ApprovalApproved, Count: 2},
		{Enforce: "require_approval", Shadow: "allow", ShadowRule: "x", Tool: "postgres.execute", AgentID: "agt_o", AgentName: "ops-agent", EnforceVersionID: "pv_1", Approval: ApprovalExpired, Count: 1},
	}
	f.shadowSamples = []ShadowSample{{Enforce: "allow", Shadow: "deny", ActionID: "act_1"}, {Enforce: "nope", Shadow: "x", ActionID: "act_2"}}
	r, err := s.Shadow(bg, testOrg, ShadowQuery{ShadowVersionID: "pv_2"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.Evaluated != 3210 || r.Summary.Disagree != 49 || r.Summary.Agreement != 0.9847 || len(r.Transitions) != 3 || r.Enforce[0].Actions != 3210 {
		t.Fatalf("%+v %+v", r.Summary, r.Enforce)
	}
	if r.Transitions[0].Count != 41 || r.Transitions[2].HumanOutcome == nil || r.Transitions[2].HumanOutcome.Approved != 2 || r.Transitions[2].HumanOutcome.Expired != 1 {
		t.Fatalf("transitions %+v", r.Transitions)
	}
	if !strings.Contains(r.Sentence, "Shadow acme-sim v2 would have sent 41 actions to approval, denied 5 and allowed 3") {
		t.Fatalf("sentence %q", r.Sentence)
	}
}

func TestShadowSentences(t *testing.T) {
	base := ShadowReport{Shadow: VersionInfo{Name: "acme-sim", Version: 2}, Enforce: []EnforceVersion{{Name: "acme-sim", Version: 1}}}
	r := base
	if got := ShadowSentence(&r); got != "Shadow acme-sim v2 hasn't seen any actions yet." {
		t.Fatal(got)
	}
	r.Summary = ShadowSummary{Evaluated: 3207, Agree: 3207}
	if got := ShadowSentence(&r); got != "Shadow acme-sim v2 agreed with v1 on all 3,207 actions." {
		t.Fatal(got)
	}
	r = base
	r.Summary = ShadowSummary{Evaluated: 3207, Agree: 3161, Disagree: 46}
	r.Transitions = []ShadowTransition{
		{Enforce: "allow", Shadow: "require_approval", Count: 41, ByRule: []RuleCount{{RuleID: "refund-approval-over-30", Count: 41}}},
		{Enforce: "allow", Shadow: "deny", Count: 5, ByRule: []RuleCount{{RuleID: "slack-allowlist", Count: 5}}},
	}
	want := "Shadow acme-sim v2 would have sent 41 actions to approval and denied 5 that v1 allowed (refund-approval-over-30: 41, slack-allowlist: 5)."
	if got := ShadowSentence(&r); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	r.Enforce = []EnforceVersion{{Name: "other", Version: 4}}
	if got := ShadowSentence(&r); !strings.Contains(got, "that other v4 allowed") {
		t.Fatal(got)
	}
	r.Enforce = nil
	r.Transitions[1].Enforce = "deny"
	if got := ShadowSentence(&r); strings.Contains(got, " that ") || !strings.Contains(got, "the enforced policy") && strings.Contains(got, "agreed") {
		t.Fatal(got)
	}
	for in, want := range map[string]string{"allow": "allowed", "deny": "denied", "require_approval": "sent to approval", "x": "decided"} {
		if pastTense(in) != want {
			t.Fatal(in)
		}
	}
	if joinAnd(nil) != "" || joinAnd([]string{"a"}) != "a" || joinAnd([]string{"a", "b", "c"}) != "a, b and c" {
		t.Fatal("joinAnd")
	}
}

func TestStatsService(t *testing.T) {
	p50, p95 := 1260.0, 6840.0
	l50, l95 := 41.0, 180.0
	row := StatsRow{Key: "approve-order-updates", Versions: []int{1}, Actions: 418, RequireApproval: 418, Approved: 401, Expired: 3, Pending: 14,
		WaitP50: &p50, WaitP95: &p95, Verified: 392, Mismatch: 3, Unverifiable: 6, NoVerdict: 17, L2: 401, Executed: 401, LatencyP50: &l50, LatencyP95: &l95}
	allow := StatsRow{Key: "allow-db-reads", Actions: 4000, Allow: 4000, Verified: 4000, Executed: 4000}
	total := SumRows([]StatsRow{row, allow})
	enforced := []byte(`deedglass: policy/v1
name: acme-sim
defaults: { decision: deny, reason: No rule allows this action. }
budgets:
  - id: refunds-per-day
    match: { tool: stripe.create_refund }
    per: org
    window: day
    max_spend: "500.00 usd"
    measure: 'has(args.amount) && type(args.amount) == int ? args.amount : 0'
  - id: calls-per-agent
    window: day
    max_calls: 100
  - id: per-minute
    window: minute
    max_calls: 10
`)
	f := &fakeStore{org: OrgInfo{Timezone: "Asia/Kolkata"}, enforced: &VersionInfo{Name: "acme-sim", Version: 1}, enfSrc: enforced,
		groups: map[string][]StatsRow{ByRule: {row, allow}, ByNone: {total}},
		prev:   map[string][]StatsRow{ByRule: {{Key: "approve-order-updates", Actions: 300, RequireApproval: 300, Approved: 300}}, ByNone: {{Actions: 300}}},
		series: []SeriesRow{{Date: "2026-10-02", Actions: 801}},
		counters: []DayCounter{
			{Scope: "org", Counter: "budget:refunds-per-day", Day: time.Date(2026, 10, 2, 18, 30, 0, 0, time.UTC), Value: 47500},
			{Scope: "org", Counter: "budget:refunds-per-day", Day: time.Date(2026, 10, 3, 18, 30, 0, 0, time.UTC), Value: 20000},
			{Scope: "agt_a", Counter: "budget:calls-per-agent", Day: time.Date(2026, 10, 3, 18, 30, 0, 0, time.UTC), Value: 90},
			{Scope: "agt_a", Counter: "calls", Day: time.Date(2026, 10, 3, 18, 30, 0, 0, time.UTC), Value: 50},
			{Scope: "agt_a", Counter: "spend:usd", Day: time.Date(2026, 10, 3, 18, 30, 0, 0, time.UTC), Value: 900},
		},
		agentsB: []AgentBudgetRow{{ID: "agt_a", Name: "support-bot", Budgets: json.RawMessage(`{"calls_per_day":100,"spend_per_day":{"amount":1000,"currency":"usd"}}`)},
			{ID: "agt_b", Name: "quiet"}, {ID: "agt_c", Name: "broken", Budgets: json.RawMessage(`{`)}}}
	s := newSvc(f)
	s.StatsTTL = time.Minute
	q := StatsQuery{From: now0.Add(-7 * 24 * time.Hour), To: now0, By: ByRule, Compare: true, Agent: "agt_a", Tool: "stripe.*"}
	obj, err := s.Stats(bg, testOrg, q)
	if err != nil {
		t.Fatal(err)
	}
	if obj.Totals.Actions != 4418 || len(obj.Groups) != 2 || obj.Window.Days != 7 || obj.Empty != nil || *obj.Filters.Agent != "agt_a" || *obj.Filters.Tool != "stripe.*" {
		t.Fatalf("%+v", obj)
	}
	g := obj.Groups[0]
	if *g.Key.RuleID != "approve-order-updates" || *g.Approval.Rate != 1 || *g.Approval.MedianSeconds != 1260 || g.Approval.Requested != 418 ||
		g.MismatchRate != 0.0075 || *g.LatencyMS.P95 != 180 || g.Trend["actions"].DeltaPct == nil || *g.Trend["actions"].DeltaPct != 39.3333 {
		t.Fatalf("group key %v rate %v median %v mismatch %v p95 %v delta %v", *g.Key.RuleID, *g.Approval.Rate, *g.Approval.MedianSeconds, g.MismatchRate, *g.LatencyMS.P95, *g.Trend["actions"].DeltaPct)
	}
	if obj.Totals.Trend == nil || obj.Totals.Trend["actions"].DeltaPct == nil {
		t.Fatalf("totals trend %+v", obj.Totals.Trend)
	}
	names := map[string]BudgetUse{}
	for _, b := range obj.Budgets {
		names[b.Budget+"|"+deref(b.AgentName)] = b
	}
	if b := names["refunds-per-day|"]; b.DaysOver80 != 1 || b.MaxUtilisation != 0.95 || *b.Limit.Amount != 50000 {
		t.Fatalf("refunds %+v", b)
	}
	if b := names["calls-per-agent|support-bot"]; b.DaysOver80 != 1 || b.MaxUtilisation != 0.9 {
		t.Fatalf("calls %+v", b)
	}
	if b := names["agent.calls_per_day|support-bot"]; b.MaxUtilisation != 0.5 || *b.Limit.Calls != 100 {
		t.Fatalf("agent calls %+v", b)
	}
	if b := names["agent.spend_per_day|support-bot"]; b.MaxUtilisation != 0.9 || b.DaysOver80 != 1 {
		t.Fatalf("agent spend %+v", b)
	}
	if _, ok := names["per-minute|"]; ok {
		t.Fatal("a minute budget has no daily picture")
	}
	// The cache answers the same query without the store.
	f.groups = nil
	again, err := s.Stats(bg, testOrg, q)
	if err != nil || again != obj {
		t.Fatalf("cached: %v", err)
	}
	// A relative window (window=7d) ends at each request's instant; repeats within the TTL share it.
	rq := q
	rq.Relative = "7d"
	first, err := s.Stats(bg, testOrg, rq)
	if err != nil {
		t.Fatal(err)
	}
	rq.From, rq.To = rq.From.Add(30*time.Second), rq.To.Add(30*time.Second)
	if second, err := s.Stats(bg, testOrg, rq); err != nil || second != first {
		t.Fatalf("relative window not cached: %v", err)
	}
	// An empty window says so; by=none has no groups.
	f.groups = map[string][]StatsRow{ByNone: {{}}}
	f.prev = nil
	empty, err := s.Stats(bg, testOrg, StatsQuery{From: now0.Add(-time.Hour), To: now0, By: ByNone})
	if err != nil || empty.Empty == nil || !strings.HasPrefix(*empty.Empty, "No actions between") || len(empty.Groups) != 0 {
		t.Fatalf("empty: %v %+v", err, empty)
	}
	f.orgErr = errors.New("db down")
	if _, err := s.Stats(bg, testOrg, StatsQuery{From: now0.Add(-2 * time.Hour), To: now0, By: ByTool}); err == nil {
		t.Fatal("an org read error must surface")
	}
}

func TestBuildGroupKeysAndTrend(t *testing.T) {
	for _, by := range []string{ByTool, ByAgent, ByNone} {
		g := BuildGroup(StatsRow{Key: "k", AgentName: "n", Actions: 30, Deny: 3, DenyNonStop: 2, StopDecided: 10}, by)
		switch by {
		case ByTool:
			if *g.Key.Tool != "k" {
				t.Fatal(by)
			}
		case ByAgent:
			if *g.Key.Agent != "k" || *g.Key.AgentName != "n" {
				t.Fatal(by)
			}
		case ByNone:
			if g.Key != nil {
				t.Fatal(by)
			}
		}
		if g.DenyRate != 0.1 || g.Cost == nil {
			t.Fatalf("%s: deny rate %v", by, g.DenyRate)
		}
	}
	tr := TrendOf(Group{Actions: 50}, Group{Actions: 10})
	if tr["actions"].DeltaPct != nil {
		t.Fatal("under 20 previous actions: no trend")
	}
	r1, r2 := 0.9, 0.8
	tr = TrendOf(Group{Actions: 50, DenyRate: 0.1, Approval: ApprovalStats{Rate: &r1}}, Group{Actions: 40, DenyRate: 0.05, Approval: ApprovalStats{Rate: &r2}})
	if *tr["actions"].DeltaPct != 25 || *tr["deny_rate"].DeltaPP != 5 || *tr["approval_rate"].DeltaPP != 10 {
		t.Fatalf("%+v", tr)
	}
	nan := 0.0 / zero()
	if roundPtr(&nan) != nil || roundPtr(nil) != nil {
		t.Fatal("roundPtr")
	}
	if Rate(1, 0) != 0 || Rate(1, 3) != 0.3333 {
		t.Fatal("Rate")
	}
	if ToolGlobSQL("stripe.*") != "stripe.%" || ToolGlobSQL(`a_b%c\`) != `a\_b\%c\\` || ToolGlobSQL("") != "" {
		t.Fatal("ToolGlobSQL")
	}
	tot := SumRows([]StatsRow{{Actions: 1, Costs: []CostRow{{Currency: "usd", Total: 5}}}, {Actions: 2, Costs: []CostRow{{Currency: "usd", Total: 7}}}})
	if tot.Actions != 3 {
		t.Fatal("SumRows")
	}
}

func zero() float64 { return 0 }

func TestAnomalyService(t *testing.T) {
	anm := ids.New[ids.AnomalyKind]().String()
	f := &fakeStore{org: OrgInfo{Settings: DefaultSettings()},
		enforced:  &VersionInfo{Name: "acme-sim", Version: 1},
		enfSrc:    []byte("deedglass: policy/v1\nname: acme-sim\ndefaults: { decision: deny, reason: No. }\nrules:\n  - id: r1\n    when: anomaly.active\n    decision: deny\n    reason: Unusual.\n"),
		anomalies: []Anomaly{{ID: anm, Kind: AnomalyVolumeSpike, Status: StatusOpen}, {ID: "anm_b"}},
		active:    1,
		ages: []AgentInfo{{ID: "agt_new", Name: "support-bot", CreatedAt: now0.Add(-41 * time.Hour)}, {ID: "agt_old", Name: "ops", CreatedAt: now0.Add(-100 * time.Hour)},
			{ID: "agt_sys", Name: "console", Kind: "system", CreatedAt: now0}},
	}
	s := newSvc(f)
	l, err := s.Anomalies(bg, testOrg, AnomalyFilter{}, 1, "")
	if err != nil || len(l.Data) != 1 || !l.HasMore || l.ActiveNow != 1 || l.AnomalyFacts || l.Notice == nil {
		t.Fatalf("%v %+v", err, l)
	}
	if !strings.Contains(*l.Notice, "acme-sim v1 reads anomaly.active") || len(l.BaselineBuilding) != 1 ||
		l.BaselineBuilding[0].Sentence != "support-bot has 41 hours of history. Volume and hours checks start after 72 hours." {
		t.Fatalf("%+v", l)
	}
	if _, err := s.Anomaly(bg, testOrg, "bad"); codeOf(err) != httpx.CodeNotFound {
		t.Fatal(err)
	}
	if got, err := s.Anomaly(bg, testOrg, anm); err != nil || got.ID != anm {
		t.Fatal(err)
	}
	if _, err := s.Anomaly(bg, testOrg, ids.New[ids.AnomalyKind]().String()); codeOf(err) != httpx.CodeNotFound {
		t.Fatal(err)
	}
	if _, err := s.Acknowledge(bg, testOrg, "bad", Actor{}); codeOf(err) != httpx.CodeNotFound {
		t.Fatal(err)
	}
	if _, err := s.Acknowledge(bg, testOrg, anm, Actor{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		err  error
		code httpx.Code
	}{{ErrStateConflict, httpx.CodeConflict}, {ErrNotFound, httpx.CodeNotFound}} {
		f.ackErr, f.resErr = c.err, c.err
		if _, err := s.Acknowledge(bg, testOrg, anm, Actor{}); codeOf(err) != c.code {
			t.Fatal(err)
		}
		if _, err := s.Resolve(bg, testOrg, anm, Actor{}, "a planned launch", ""); codeOf(err) != c.code {
			t.Fatal(err)
		}
	}
	f.ackErr, f.resErr = nil, nil
	for reason, want := range map[string]string{"": MsgResolveReason, "ab": MsgReasonLength, strings.Repeat("x", 281): MsgReasonLength, "bad\x01reason": MsgReasonLength} {
		if _, err := s.Resolve(bg, testOrg, anm, Actor{}, reason, ""); codeOf(err) != httpx.CodeValidationFailed || httpx.AsError(err).Message != want {
			t.Fatalf("reason %q: %v", reason, err)
		}
	}
	if _, err := s.Resolve(bg, testOrg, "bad", Actor{}, "a planned launch", ""); codeOf(err) != httpx.CodeNotFound {
		t.Fatal(err)
	}
	s.Facts = &FactsCache{Store: f}
	if _, err := s.Resolve(bg, testOrg, anm, Actor{}, "  a planned launch  ", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsService(t *testing.T) {
	f := &fakeStore{org: OrgInfo{Settings: DefaultSettings()}, deedID: "deed_x"}
	s := newSvc(f)
	s.Facts = &FactsCache{Store: f}
	o, err := s.SettingsOf(bg, testOrg)
	if err != nil || o.Object != "insights_settings" || o.ActivePolicyReadsAnomaly || o.Notice != nil {
		t.Fatalf("%v %+v", err, o)
	}
	on := true
	bad, huge := 1, 5000
	for _, p := range []SettingsPatch{{AnomalyActiveMinutes: &bad, Reason: "why not"}, {AnomalyStaleAfterMinutes: &huge, Reason: "why not"}, {AnomalyFacts: &on}} {
		if _, err := s.PatchSettings(bg, testOrg, Actor{}, p, ""); codeOf(err) != httpx.CodeValidationFailed {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	o, err = s.PatchSettings(bg, testOrg, Actor{}, SettingsPatch{AnomalyFacts: &on, Reason: "Require approval while an agent spikes"}, "")
	if err != nil || o.Deed == nil || *o.Deed != "deed_x" || !o.AnomalyFacts || len(f.settings) != 1 {
		t.Fatalf("%v %+v", err, o)
	}
	// The same values again: nothing to seal.
	o, err = s.PatchSettings(bg, testOrg, Actor{}, SettingsPatch{AnomalyFacts: &on, Reason: "again, the same"}, "")
	if err != nil || o.Deed != nil || len(f.settings) != 1 {
		t.Fatalf("no change: %v %+v", err, o)
	}
	ten, sixty := 10, 60
	if p := (SettingsPatch{AnomalyActiveMinutes: &sixty, AnomalyStaleAfterMinutes: &ten}).Apply(DefaultSettings()); p.AnomalyStaleAfterMinutes != 10 {
		t.Fatal("Apply")
	}
	for in, want := range map[string]Settings{
		``:   DefaultSettings(),
		`{`:  DefaultSettings(),
		`{}`: DefaultSettings(),
		`{"insights":{"anomaly_facts":true,"anomaly_active_minutes":30,"anomaly_stale_after_minutes":20}}`: {AnomalyFacts: true, AnomalyActiveMinutes: 30, AnomalyStaleAfterMinutes: 20,
			LLMMonthlyCapCents: DefaultLLMCapCents, Digest: DigestSettings{Recipients: DigestOwners}},
		`{"insights":{"llm_phrasing":true,"llm_monthly_cap_cents":500,"digest":{"recipients":"admins"}}}`: {AnomalyActiveMinutes: DefaultActiveMinutes,
			AnomalyStaleAfterMinutes: DefaultStaleMinutes, LLMPhrasing: true, LLMMonthlyCapCents: 500, Digest: DigestSettings{Recipients: DigestAdmins}},
		`{"insights":{"llm_monthly_cap_cents":-1,"digest":{"recipients":"everyone"}}}`: DefaultSettings(),
		`{"insights":{"anomaly_active_minutes":1,"anomaly_stale_after_minutes":9999}}`: DefaultSettings(),
	} {
		if got := ParseSettings([]byte(in)); got != want {
			t.Fatalf("%s: %+v", in, got)
		}
	}
	f.orgErr = errors.New("down")
	if _, err := s.SettingsOf(bg, testOrg); err == nil {
		t.Fatal("org error")
	}
	if _, err := s.PatchSettings(bg, testOrg, Actor{}, SettingsPatch{AnomalyFacts: &on, Reason: "a reason"}, ""); err == nil {
		t.Fatal("org error")
	}
}

func TestParseStatsQuery(t *testing.T) {
	ok := []struct {
		q     string
		by    string
		days  float64
		cmp   bool
		agent string
	}{
		{"", ByNone, 7, false, ""},
		{"window=30d&by=rule", ByRule, 30, false, ""},
		{"from=2026-10-01T00:00:00Z&to=2026-10-03T00:00:00Z&by=agent&compare=previous&agent=agt_x&tool=stripe.*", ByAgent, 2, true, "agt_x"},
	}
	for _, c := range ok {
		r := httptest.NewRequest("GET", "/v1/insights/stats?"+c.q, nil)
		q, err := ParseStatsQuery(r, now0)
		if err != nil || q.By != c.by || q.To.Sub(q.From).Hours()/24 != c.days || q.Compare != c.cmp || q.Agent != c.agent {
			t.Errorf("%s: %v %+v", c.q, err, q)
		}
	}
	for _, bad := range []string{"by=team", "window=1y", "window=7d&from=2026-10-01T00:00:00Z", "from=x&to=y", "from=2026-10-01T00:00:00Z",
		"from=2026-10-03T00:00:00Z&to=2026-10-01T00:00:00Z", "agent=bob", "tool=" + strings.Repeat("x", 201), "compare=next", "nope=1", "by=rule&by=tool"} {
		r := httptest.NewRequest("GET", "/v1/insights/stats?"+bad, nil)
		if _, err := ParseStatsQuery(r, now0); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
	r := httptest.NewRequest("GET", "/v1/replays?limit=0", nil)
	if _, _, err := pageOf(r); err == nil {
		t.Fatal("limit 0")
	}
	r = httptest.NewRequest("GET", "/v1/replays?limit=7&cursor=abc", nil)
	if n, c, err := pageOf(r); err != nil || n != 7 || c != "abc" {
		t.Fatal(n, c, err)
	}
}

func TestCopy(t *testing.T) {
	if got := ReplaySummaryLine(ReplaySummary{Changed: 408, RuleChanged: 4, Unchanged: 5200}); got != "408 decisions would change · 4 by a different rule · 5,200 unchanged · 0 unknown" {
		t.Fatal(got)
	}
	if got := ReplaySummaryLine(ReplaySummary{Changed: 1}); got != "1 decision would change · 0 unchanged · 0 unknown" {
		t.Fatal(got)
	}
	if HumanOutcomeLine(nil) != "" {
		t.Fatal("nil human outcome")
	}
	if got := HumanOutcomeLine(&HumanOutcome{Approved: 401, Expired: 3, Pending: 2, Cancelled: 1, NotRequested: 5}); got != "humans approved 401, expired 3, denied 0, pending 2, cancelled 1, never asked 5" {
		t.Fatal(got)
	}
	if got := UnknownLine(UnknownCounts{InputPruned: 12, InputMissing: 2, InputUnsupported: 1, InputUnreadable: 1, EvaluationTimedOut: 1}); got !=
		"12 inputs pruned by retention · 2 recorded before inputs were stored · 1 input in a newer format · 1 input that doesn't decode · 1 evaluation that hit its time limit" {
		t.Fatal(got)
	}
	if got := DetectorStaleNotice(22); !strings.HasPrefix(got, "Anomaly detection last ran 22 minutes ago.") {
		t.Fatal(got)
	}
	if got := DetectorStaleNotice(1); !strings.Contains(got, "1 minute ago") {
		t.Fatal(got)
	}
	if got := UnknownVersionMessage("acme-sim", 9, 0); !strings.HasPrefix(got, "No version 9 of acme-sim. List its versions") {
		t.Fatal(got)
	}
	if AnomalyTrace(nil) != "" || AnomalyTrace([]string{"a", "b"}) != "anomaly facts: a, b" {
		t.Fatal("trace")
	}
	if got := AnomalyStaleTrace(time.Time{}, now0); !strings.Contains(got, "hasn't run yet") {
		t.Fatal(got)
	}
	if got := StatsEmpty(now0.Add(-48*time.Hour), now0, time.UTC); got != `No actions between 2026-10-07 and 2026-10-09. Send one: deedglass act fake.echo --args '{"x":1}'` {
		t.Fatal(got)
	}
	for _, s := range []string{SimulatedBanner, DetectorNeverRanNotice, AnomalyFactsOffLine, AnomalyFactsOnLine, MsgNoShadow, ReplayTooLargeMessage(5)} {
		if strings.ContainsAny(s, "—–!") {
			t.Fatalf("dash or exclamation in %q", s)
		}
	}
	_ = policy.InputVersion
	_ = context.Background
}
