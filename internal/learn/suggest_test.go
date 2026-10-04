package learn

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// S28 §6 "Pipeline": Suggester.Generate over memStore and a replay Engine fed with the fixture's
// own inputs.

// sugEnv is one org with acme-sim v1 enforced and a fixture of decided rows.
type sugEnv struct {
	t   *testing.T
	m   *memStore
	org ids.OrgID
	rv  *ResolvedVersion
	f   *evFixture
	src *memSource
	sg  *Suggester
}

func newSugEnv(t *testing.T, fill func(f *evFixture)) *sugEnv {
	t.Helper()
	m := newMemStore()
	org := ids.New[ids.OrgKind]()
	m.addOrg(org, "Acme", "UTC")
	src := simSource(t, 1)
	rv := m.enforce(org, ids.New[ids.PolicyKind]().String(), "acme-sim", 1, src)
	f := newEvFixture(t, src)
	f.org.ID = org
	f.rv = rv
	if fill != nil {
		fill(f)
	}
	m.org(org).rows = f.rows
	ms := &memSource{rows: f.inputRows()}
	e := &sugEnv{t: t, m: m, org: org, rv: rv, f: f, src: ms}
	e.sg = &Suggester{Store: m, Engine: &Engine{Src: ms, Workers: 2}, Now: func() time.Time { return sugNow }}
	return e
}

// refresh feeds the store and the replay source the fixture's rows again (after more adds).
func (e *sugEnv) refresh() {
	e.m.mu.Lock()
	e.m.orgs[e.org].rows = e.f.rows
	e.m.mu.Unlock()
	e.src.mu.Lock()
	e.src.rows = e.f.inputRows()
	e.src.mu.Unlock()
}

func (e *sugEnv) generate() *LearnRun {
	e.t.Helper()
	run, err := e.sg.Generate(bg, e.org, Actor{Type: "system", ID: "deedglass"})
	if err != nil {
		e.t.Fatal(err)
	}
	return run
}

func (e *sugEnv) byGenerator(gen string) *SuggestionRow {
	e.m.mu.Lock()
	defer e.m.mu.Unlock()
	for _, s := range e.m.sugs {
		if s.org == e.org && s.row.Generator == gen && (s.row.Status == SugOpen || s.row.Status == SugAccepting) {
			c := s.row
			return &c
		}
	}
	return nil
}

// standardRows: 401 approved and 3 expired order updates (loosen), 14 default denies of
// github.merge_pr on 3 days (explicit rule), 3 MISMATCH inventory writes (tighten).
func standardRows(f *evFixture) {
	f.addOrderApprovals(401, 3, 0)
	for i := range 14 {
		f.add(rowSpec{at: sugNow.Add(-time.Duration(1+i%3) * 24 * time.Hour).Add(-time.Duration(i) * time.Minute), tool: "github.merge_pr", ct: "github", args: map[string]any{"pr": i}})
	}
	inv := map[string]any{"sql": "UPDATE public.inventory SET reserved = reserved + 1 WHERE sku = 'A1'"}
	for range 3 {
		f.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: inv, verdict: "MISMATCH", level: "L2"})
	}
}

func dropped(run *LearnRun, gen, contains string) bool {
	for _, d := range run.Dropped {
		if d.Generator == gen && strings.Contains(d.Reason, contains) {
			return true
		}
	}
	return false
}

func TestGenerateCreatesWithReplay(t *testing.T) {
	t.Parallel()
	e := newSugEnv(t, standardRows)
	run := e.generate()
	if run.Object != "learn_run" || run.Kind != "suggest" || run.Policy == nil || run.Policy.BaseVersionID != e.rv.VersionID || run.Actions != len(e.f.rows) {
		t.Fatalf("run %+v", run)
	}
	gens := map[string]SuggestionBrief{}
	for _, c := range run.Created {
		gens[c.Generator] = c
	}
	if len(run.Created) != 3 || gens[GenLoosenApprovalRate].ID == "" || gens[GenExplicitDefaultHit].ID == "" || gens[GenTightenMismatch].ID == "" {
		t.Fatalf("created %+v dropped %+v", run.Created, run.Dropped)
	}
	// Ordered by impact.
	if run.Created[0].Generator != GenLoosenApprovalRate || run.Created[0].Impact != 404 {
		t.Errorf("first %+v", run.Created[0])
	}
	if gens[GenExplicitDefaultHit].Impact != 0 || gens[GenTightenMismatch].Impact != 3 {
		t.Errorf("impacts %+v", gens)
	}
	if e.m.supersedeStaleCalls != 1 {
		t.Errorf("SupersedeStale calls %d", e.m.supersedeStaleCalls)
	}
	lo := e.byGenerator(GenLoosenApprovalRate)
	if lo.Replay == nil || !strings.HasPrefix(lo.Replay.ID, "rpl_") || lo.Replay.Summary.Changed != 404 || len(lo.Replay.Transitions) != 1 {
		t.Fatalf("replay %+v", lo.Replay)
	}
	tr := lo.Replay.Transitions[0]
	if tr.From != "require_approval" || tr.To != "allow" || tr.HumanOutcome == nil || tr.HumanOutcome.Approved != 401 || tr.HumanOutcome.Expired != 3 || tr.HumanOutcome.Denied != 0 {
		t.Errorf("transition %+v %+v", tr, tr.HumanOutcome)
	}
	if !strings.HasPrefix(lo.ReplayDigest, "sha256:") || !strings.HasPrefix(lo.EvidenceDigest, "sha256:") || !strings.HasPrefix(lo.Fingerprint, "sha256:") {
		t.Errorf("digests %q %q %q", lo.ReplayDigest, lo.EvidenceDigest, lo.Fingerprint)
	}
	if lo.Title != "Allow updates to public.orders without approval" || lo.Policy.Name != "acme-sim" || lo.Policy.BaseVersion != 1 || *lo.TargetRuleID != "approve-order-updates" {
		t.Errorf("row %+v", lo.Suggestion)
	}
	// The candidate source compiles, moved only what the op says, and names the suggestion.
	cand, diags := policy.Compile(lo.CandidateSource, policy.Options{})
	if cand == nil {
		t.Fatalf("candidate does not compile: %v", diags)
	}
	f := newEvFixture(t, simSource(t, 1))
	f.rv = e.rv
	f.addOrderApprovals(401, 3, 0)
	ps, _ := GenLoosenFn(f.evidence())
	if err := policy.CheckPatch(e.f.base, cand, ps[0].Ops...); err != nil {
		t.Errorf("CheckPatch: %v", err)
	}
	if !strings.Contains(string(lo.CandidateSource), "# Accepted from Deedglass Insights suggestion "+lo.ID+" · evidence sha256:") {
		t.Errorf("candidate comment missing:\n%s", lo.CandidateSource)
	}
	if lo.Diff == nil || !strings.Contains(lo.Diff.Text, "+  - id: allow-order-updates") {
		t.Errorf("diff %+v", lo.Diff)
	}
	// The explicit rule's replay changed no decision.
	if ex := e.byGenerator(GenExplicitDefaultHit); ex.Replay == nil || ex.Replay.Summary.Changed != 0 || ex.Replay.Summary.RuleChanged != 14 {
		t.Errorf("explicit replay %+v", ex.Replay)
	}
	// Advisories (none here) carry no replay; the evidence carries the window.
	if !strings.Contains(string(lo.Evidence), `"window":{"from":"2026-09-25T13:00:00Z","to":"2026-10-09T13:00:00Z"}`) {
		t.Errorf("evidence %s", lo.Evidence)
	}

	// A second pass over the same evidence creates nothing.
	run2 := e.generate()
	if len(run2.Created) != 0 || run2.Unchanged != 3 {
		t.Fatalf("second run: created %d unchanged %d dropped %v", len(run2.Created), run2.Unchanged, run2.Dropped)
	}
}

func TestGenerateDedupeAccepted(t *testing.T) {
	t.Parallel()
	e := newSugEnv(t, func(f *evFixture) { f.addOrderApprovals(401, 3, 0) })
	e.generate()
	lo := e.byGenerator(GenLoosenApprovalRate)
	e.m.mu.Lock()
	e.m.sugs[lo.ID].row.Status = SugAccepted
	e.m.mu.Unlock()
	run := e.generate()
	if len(run.Created) != 0 || !dropped(run, GenLoosenApprovalRate, "already accepted for this version") {
		t.Fatalf("created %v dropped %v", run.Created, run.Dropped)
	}
	// Accepted for an older base: no longer remembered.
	e.m.mu.Lock()
	e.m.sugs[lo.ID].row.Policy.BaseVersionID = "pv_old"
	e.m.mu.Unlock()
	if run := e.generate(); len(run.Created) != 1 {
		t.Fatalf("after the base moved: %+v", run)
	}
}

func TestGenerateGuardDropsHumanDenied(t *testing.T) {
	t.Parallel()
	e := newSugEnv(t, func(f *evFixture) {
		f.addOrderApprovals(401, 3, 0)
		// A person denied this same kind of update under an older enforced version: the rule's
		// counts under the base don't see it, the replay does.
		r := f.add(rowSpec{tool: "postgres.execute", ct: "postgres", args: orderUpdate(9999), approval: ApprovalDenied, wait: 30})
		r.VersionID = "pv_older"
	})
	if re := e.f.evidence().ruleEv("approve-order-updates"); re.Approvals.Denied != 0 {
		t.Fatalf("fixture: the base's counts see the denial")
	}
	run := e.generate()
	if len(run.Created) != 0 || !dropped(run, GenLoosenApprovalRate, "its replay would allow 1 action a person denied") {
		t.Fatalf("created %v dropped %+v", run.Created, run.Dropped)
	}
}

func TestGenerateRejectedMemory(t *testing.T) {
	t.Parallel()
	e := newSugEnv(t, func(f *evFixture) { f.addOrderApprovals(401, 3, 0) })
	e.generate()
	lo := e.byGenerator(GenLoosenApprovalRate)
	if _, err := e.m.RejectSuggestion(bg, e.org, lo.ID, Actor{Type: "user", ID: "usr_p"}, "Not yet.", "", sugNow.Add(-5*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	run := e.generate()
	if len(run.Created) != 0 || !dropped(run, GenLoosenApprovalRate, "rejected on 2026-10-04; it comes back after 30 days") {
		t.Fatalf("within 30 days: created %v dropped %+v", run.Created, run.Dropped)
	}
	// The numbers doubled since (it was rejected at 200): it comes back.
	e.m.mu.Lock()
	e.m.sugs[lo.ID].mainCount = 200
	e.m.mu.Unlock()
	if run := e.generate(); len(run.Created) != 1 {
		t.Fatalf("doubled: %+v", run)
	}
	// Rejected 31 days ago: it comes back too.
	e2 := newSugEnv(t, func(f *evFixture) { f.addOrderApprovals(401, 3, 0) })
	e2.generate()
	lo2 := e2.byGenerator(GenLoosenApprovalRate)
	if _, err := e2.m.RejectSuggestion(bg, e2.org, lo2.ID, Actor{Type: "user", ID: "usr_p"}, "Not yet.", "", sugNow.Add(-31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if run := e2.generate(); len(run.Created) != 1 {
		t.Fatalf("after 30 days: %+v", run)
	}
}

func TestGenerateSupersedesOnBaseChange(t *testing.T) {
	t.Parallel()
	e := newSugEnv(t, func(f *evFixture) { f.addOrderApprovals(401, 3, 0) })
	old := e.m.putSuggestion(e.org, NewSuggestion{ID: ids.New[ids.SuggestionKind]().String(), Kind: KindLoosen, Generator: GenLoosenApprovalRate,
		Fingerprint: "sha256:old", PolicyID: e.rv.PolicyID, BaseVersionID: "pv_was_enforced", Impact: 5}, SugOpen)
	run := e.generate()
	if run.Superseded != 1 || e.m.status(old.ID) != SugSuperseded || e.m.supersedeStaleCalls != 1 {
		t.Fatalf("superseded %d status %s", run.Superseded, e.m.status(old.ID))
	}
	got, _ := e.m.GetSuggestion(bg, e.org, old.ID)
	if got.StaleReason == nil || *got.StaleReason != "base_changed" {
		t.Errorf("stale reason %v", got.StaleReason)
	}
}

func TestGenerateCap(t *testing.T) {
	t.Parallel()
	e := newSugEnv(t, func(f *evFixture) {
		f.addOrderApprovals(401, 3, 0)
		for i := range 14 {
			f.add(rowSpec{at: sugNow.Add(-time.Duration(1+i%3) * 24 * time.Hour).Add(-time.Duration(i) * time.Minute), tool: "github.merge_pr", ct: "github"})
		}
	})
	for i := range 9 {
		e.m.putSuggestion(e.org, NewSuggestion{ID: ids.New[ids.SuggestionKind]().String(), Kind: KindAdvisory, Generator: GenAdvisoryDenyHeavy,
			Fingerprint: "sha256:seed" + string(rune('a'+i)), PolicyID: e.rv.PolicyID, BaseVersionID: e.rv.VersionID, Impact: i}, SugOpen)
	}
	// An accepted one doesn't take a slot.
	e.m.putSuggestion(e.org, NewSuggestion{ID: ids.New[ids.SuggestionKind]().String(), Kind: KindLoosen, Generator: GenLoosenApprovalRate,
		Fingerprint: "sha256:acc", PolicyID: e.rv.PolicyID, BaseVersionID: e.rv.VersionID}, SugAccepted)
	run := e.generate()
	if len(run.Created) != 1 || run.Waiting != 1 || run.Created[0].Generator != GenLoosenApprovalRate {
		t.Fatalf("created %+v waiting %d", run.Created, run.Waiting)
	}
	// Full: everything waits.
	if run := e.generate(); len(run.Created) != 0 || run.Waiting != 1 || run.Unchanged != 1 {
		t.Fatalf("full: %+v", run)
	}
}

func TestGenerateBusy(t *testing.T) {
	t.Parallel()
	e := newSugEnv(t, func(f *evFixture) { f.addOrderApprovals(31, 0, 0) })
	entered := make(chan struct{})
	release := make(chan struct{})
	var held atomic.Bool
	e.m.onOrg = func() {
		if held.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.sg.Generate(bg, e.org, Actor{Type: "system", ID: "deedglass"})
		done <- err
	}()
	<-entered
	if _, err := e.sg.Generate(bg, e.org, Actor{Type: "system", ID: "deedglass"}); !errors.Is(err, ErrSuggestBusy) {
		t.Errorf("concurrent pass: %v, want ErrSuggestBusy", err)
	}
	// Another org isn't held up.
	other := ids.New[ids.OrgKind]()
	e.m.addOrg(other, "Other", "UTC")
	if run, err := e.sg.Generate(bg, other, Actor{Type: "system", ID: "deedglass"}); err != nil || run.Notice == nil {
		t.Errorf("another org: %v %+v", err, run)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Free again afterwards.
	if _, err := e.sg.Generate(bg, e.org, Actor{Type: "system", ID: "deedglass"}); err != nil {
		t.Fatal(err)
	}
	// The service maps busy to 409 and records the run as failed.
	svc := &SuggestionService{Store: e.m, Suggester: e.sg, Now: func() time.Time { return sugNow }}
	e.sg.mu.Lock()
	e.sg.running[e.org.String()] = true
	e.sg.mu.Unlock()
	if _, err := svc.Generate(bg, e.org, Actor{Type: "user", ID: "usr_a"}); codeOf(err) != httpx.CodeConflict || !strings.Contains(err.Error(), MsgSuggestNotNow) {
		t.Fatalf("busy service: %v", err)
	}
	if runs := e.m.runsOf(e.org, "suggest"); len(runs) != 1 || !strings.HasSuffix(runs[0], "=failed") {
		t.Fatalf("runs %v", runs)
	}
	// Still running: 409 again, each manual pass with its own learn_runs row.
	if _, err := svc.Generate(bg, e.org, Actor{Type: "user", ID: "usr_a"}); codeOf(err) != httpx.CodeConflict {
		t.Fatalf("same period: %v", err)
	}
}

func TestGenerateNoticeAndErrors(t *testing.T) {
	t.Parallel()
	m := newMemStore()
	org := ids.New[ids.OrgKind]()
	m.addOrg(org, "Acme", "UTC")
	sg := &Suggester{Store: m, Engine: &Engine{Src: &memSource{}}}
	run, err := sg.Generate(bg, org, Actor{Type: "system", ID: "deedglass"})
	if err != nil || run.Notice == nil || *run.Notice != MsgSuggestNoEnforced || len(run.Created) != 0 || m.supersedeStaleCalls != 0 {
		t.Fatalf("no enforced: %v %+v", err, run)
	}
	if _, err := sg.Generate(bg, ids.New[ids.OrgKind](), Actor{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown org: %v", err)
	}
	// The enforced version missing from the versions.
	m.orgs[org].enforced = &VersionInfo{PolicyID: "pol_gone", Name: "gone", Version: 4, VersionID: "pv_gone"}
	if _, err := sg.Generate(bg, org, Actor{}); err == nil {
		t.Fatal("missing enforced version: no error")
	}
	// An enforced version that no longer compiles.
	rv := m.enforce(org, "pol_bad", "bad", 1, []byte("deedglass: policy/v1\nname: bad\nrules: [\n"))
	if _, err := sg.Generate(bg, org, Actor{}); err == nil || !strings.Contains(err.Error(), "no longer compiles") {
		t.Fatalf("broken enforced: %v (%s)", err, rv.VersionID)
	}
	m.enforce(org, "pol_ok", "acme-sim", 1, simSource(t, 1))
	m.errOpenFingerprints = errors.New("db down")
	if _, err := sg.Generate(bg, org, Actor{}); err == nil {
		t.Fatal("OpenFingerprints error swallowed")
	}
	m.errOpenFingerprints = nil
	m.errEnforced = errors.New("db down")
	if _, err := sg.Generate(bg, org, Actor{}); err == nil {
		t.Fatal("EnforcedSource error swallowed")
	}
	m.errEnforced = nil
	// The service records a failed run.
	svc := &SuggestionService{Store: m, Suggester: sg, Now: func() time.Time { return sugNow }}
	m.errOpenFingerprints = errors.New("db down")
	if _, err := svc.Generate(bg, org, Actor{}); err == nil {
		t.Fatal("service swallowed the error")
	}
	m.errOpenFingerprints = nil
	if runs := m.runsOf(org, "suggest"); len(runs) != 1 || !strings.HasSuffix(runs[0], "=failed") {
		t.Fatalf("runs %v", runs)
	}
}

func TestGenerateInsertRace(t *testing.T) {
	t.Parallel()
	// Another instance stored the same fingerprint between the read and the insert.
	e := newSugEnv(t, func(f *evFixture) { f.addOrderApprovals(401, 3, 0) })
	e2 := &Suggester{Store: e.m, Engine: e.sg.Engine, Now: e.sg.Now}
	var wg sync.WaitGroup
	runs := make([]*LearnRun, 2)
	for i, s := range []*Suggester{e.sg, e2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Generate(bg, e.org, Actor{Type: "system", ID: "deedglass"})
			if err != nil {
				t.Error(err)
			}
			runs[i] = r
		}()
	}
	wg.Wait()
	if c := len(runs[0].Created) + len(runs[1].Created); c != 1 {
		t.Fatalf("two instances created %d", c)
	}
	n := 0
	for _, s := range e.m.sugs {
		if s.row.Status == SugOpen {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d open", n)
	}
}

func TestSuggestionServiceGenerateAndRead(t *testing.T) {
	t.Parallel()
	e := newSugEnv(t, standardRows)
	svc := &SuggestionService{Store: e.m, Suggester: e.sg, Now: func() time.Time { return sugNow }}
	run, err := svc.Generate(bg, e.org, Actor{Type: "user", ID: "usr_a"})
	if err != nil || len(run.Created) != 3 || !strings.HasPrefix(run.Period, "m-") {
		t.Fatalf("%v %+v", err, run)
	}
	if runs := e.m.runsOf(e.org, "suggest"); len(runs) != 1 || !strings.HasSuffix(runs[0], "=succeeded") {
		t.Fatalf("runs %v", runs)
	}
	l, err := svc.List(bg, e.org, SuggestionFilter{}, 2, "")
	if err != nil || len(l.Data) != 2 || !l.HasMore || l.NextCursor == nil || l.Data[0].Impact < l.Data[1].Impact {
		t.Fatalf("list %v %+v", err, l)
	}
	l2, _ := svc.List(bg, e.org, SuggestionFilter{}, 2, *l.NextCursor)
	if len(l2.Data) != 1 || l2.HasMore || l2.NextCursor != nil {
		t.Fatalf("page 2 %+v", l2)
	}
	if l3, _ := svc.List(bg, e.org, SuggestionFilter{Status: SugRejected}, 10, ""); l3.Data == nil || len(l3.Data) != 0 {
		t.Fatalf("empty list must be [] %+v", l3)
	}
	if l4, _ := svc.List(bg, e.org, SuggestionFilter{Kind: KindTighten}, 10, ""); len(l4.Data) != 1 {
		t.Fatalf("kind filter %+v", l4)
	}
	if _, err := svc.List(bg, e.org, SuggestionFilter{}, 2, "bogus"); err == nil {
		t.Fatal("bad cursor")
	}
	got, err := svc.Get(bg, e.org, l.Data[0].ID)
	if err != nil || got.ID != l.Data[0].ID {
		t.Fatal(err)
	}
	for _, id := range []string{"nope", ids.New[ids.SuggestionKind]().String()} {
		if _, err := svc.Get(bg, e.org, id); codeOf(err) != httpx.CodeNotFound {
			t.Errorf("get %s: %v", id, err)
		}
	}
	// Another org never sees it.
	other := ids.New[ids.OrgKind]()
	e.m.addOrg(other, "Other", "UTC")
	if _, err := svc.Get(bg, other, l.Data[0].ID); codeOf(err) != httpx.CodeNotFound {
		t.Errorf("cross-org get: %v", err)
	}
	st := runStats(run)
	if st["created"] != 3 || st["actions"] != run.Actions {
		t.Errorf("stats %v", st)
	}
}

// The suggestion object as the API writes it (httpx's encoder): template wording is just
// {"source":"template"} (S28 §3.5), with no zero token or cost fields.
func TestSuggestionObjectPhrasingJSON(t *testing.T) {
	t.Parallel()
	enc := func(v Suggestion) string {
		b, err := httpx.MarshalJSON(v)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, b); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if got := enc(Suggestion{Object: "suggestion", Phrasing: Phrasing{Source: "template"}}); !strings.Contains(got, `"phrasing":{"source":"template"}`) {
		t.Errorf("BUG: Phrasing's int fields use omitempty, which httpx's encoding/json/v2 encoder doesn't apply to 0 (use omitzero); the object reads %s", got)
	}
	got := enc(Suggestion{Object: "suggestion", Phrasing: Phrasing{Source: "claude", Model: "claude-sonnet-5", InputTokens: 900}})
	if !strings.Contains(got, `"input_tokens":900`) || strings.Contains(got, `"deeds"`) || strings.Contains(got, `"note"`) {
		t.Errorf("%s", got)
	}
}
