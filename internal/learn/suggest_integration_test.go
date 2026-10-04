//go:build integration

package learn_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/learn"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
)

// shadowWriter is cmd/deedglass's adapter (serve_insights.go), copied: the mode is fixed to
// shadow. TestNeverEnforceBehaviour below checks what it, the saga and the schedule do together.
type shadowWriter struct{ svc *policy.Service }

func callerOf(p learn.Principal) policy.Caller {
	return policy.Caller{Org: p.Org.String(), Actor: deed.Ref{Type: p.Actor.Type, ID: p.Actor.ID}, RequestID: p.RequestID}
}

func (w shadowWriter) AddSuggestedVersion(ctx context.Context, p learn.Principal, policyID string, src []byte, sug, notes string) (*policy.Version, bool, error) {
	return w.svc.AddVersion(ctx, callerOf(p), policyID, policy.PushRequest{Source: src, Notes: notes, Origin: policy.OriginSuggestion, SuggestionID: sug})
}

func (w shadowWriter) ActivateShadow(ctx context.Context, p learn.Principal, policyID string, version int, reason string) (*policy.Activation, error) {
	return w.svc.Activate(ctx, callerOf(p), policyID, policy.VersionRef{Number: version}, policy.ActivateRequest{Mode: policy.ModeShadow, Reason: reason})
}

// crashingWriter activates, then fails: a crash between the saga's steps 5 and 6.
type crashingWriter struct{ shadowWriter }

func (w crashingWriter) ActivateShadow(ctx context.Context, p learn.Principal, policyID string, version int, reason string) (*policy.Activation, error) {
	if _, err := w.shadowWriter.ActivateShadow(ctx, p, policyID, version, reason); err != nil {
		return nil, err
	}
	return nil, errors.New("the process died after activating the shadow")
}

type suggestEnv struct {
	*env
	org   ids.OrgID
	priya learn.Actor
	now   time.Time
	svc   *learn.SuggestionService
}

func newSuggestEnv(t *testing.T) *suggestEnv {
	t.Helper()
	v := newEnv(t)
	res := v.simulate("acme-sim")
	org := res.Org
	// The scenario's five incidents, as the detector finds them.
	det := &learn.Detector{Store: v.ls}
	if _, err := det.Backfill(bg, org, res.Window.Start, res.Window.End.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var priya string
	c := v.e.Admin(t)
	if err := c.QueryRow(bg, `SELECT u.id FROM deedglass.users AS u JOIN deedglass.memberships AS m ON m.user_id = u.id
	    WHERE m.org_id = $1 AND u.email = 'priya@example.test'`, org).Scan(&priya); err != nil {
		t.Fatal(err)
	}
	now := simEnd.Add(time.Minute)
	clock := func() time.Time { return now }
	sug := &learn.Suggester{Store: v.ls, Engine: &learn.Engine{Src: v.ls.Inputs}, Now: clock}
	svc := &learn.SuggestionService{Store: v.ls, Suggester: sug, Writer: shadowWriter{svc: v.policySvc()}, Now: clock}
	return &suggestEnv{env: v, org: org, priya: learn.Actor{Type: "user", ID: priya}, now: now, svc: svc}
}

func (s *suggestEnv) byKind(t *testing.T, kind string) *learn.Suggestion {
	t.Helper()
	l, err := s.svc.List(bg, s.org, learn.SuggestionFilter{Kind: kind}, 100, "")
	if err != nil || len(l.Data) == 0 {
		t.Fatalf("no open %s suggestion: %v", kind, err)
	}
	return &l.Data[0]
}

func codeOf(err error) httpx.Code {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Code
	}
	return ""
}

// TestSuggestionsLive is S28 §6 on Postgres: the seed-7 generate pass, dedupe, the accept saga
// (shadow slot, replace, double accept, two at once, a crash between steps 5 and 6), reject,
// a stale base, and the deed chain.
func TestSuggestionsLive(t *testing.T) {
	if testing.Short() {
		t.Skip("a week of simulated traffic")
	}
	s := newSuggestEnv(t)
	t0 := time.Now()
	run, err := s.svc.Generate(bg, s.org, s.priya)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(t0)
	t.Logf("generate: %d created, %d dropped in %s", len(run.Created), len(run.Dropped), took)
	for _, c := range run.Created {
		t.Logf("  %-9s %-24s %-60s %d", c.Kind, c.Target, c.Title, c.Impact)
	}
	for _, d := range run.Dropped {
		t.Logf("  dropped %s on %s: %s", d.Generator, d.Target, d.Reason)
	}
	if took > 5*time.Second {
		t.Errorf("the pass took %s; the budget is 5 s", took)
	}
	kinds := map[string]bool{}
	for _, c := range run.Created {
		kinds[c.Kind] = true
	}
	for _, k := range []string{learn.KindLoosen, learn.KindTighten, learn.KindBudget} {
		if !kinds[k] {
			t.Errorf("no %s suggestion on seed 7", k)
		}
	}
	l, err := s.svc.List(bg, s.org, learn.SuggestionFilter{}, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, sg := range l.Data {
		if sg.Kind != learn.KindAdvisory && (sg.Replay == nil || sg.Diff == nil || sg.Replay.ResultDigest == "") {
			t.Errorf("%s (%s) has no replay or diff", sg.ID, sg.Kind)
		}
		if sg.Phrasing.Source != "template" {
			t.Errorf("%s: phrasing %q without a key", sg.ID, sg.Phrasing.Source)
		}
		var replayed int64
		if sg.Replay != nil {
			r, err := s.service().GetReplay(bg, s.org, sg.Replay.ID)
			if err != nil || r.Purpose != "suggestion" {
				t.Errorf("GET /v1/replays/%s: %v %+v", sg.Replay.ID, err, r)
			}
			replayed = sg.Replay.Summary.Changed
		}
		if int64(sg.Impact) != replayed {
			t.Errorf("%s: impact %d, replay changed %d", sg.ID, sg.Impact, replayed)
		}
	}
	// The second pass creates nothing.
	again, err := s.svc.Generate(bg, s.org, s.priya)
	if err != nil || len(again.Created) != 0 || again.Unchanged != len(run.Created) {
		t.Fatalf("second run: %v %+v", err, again)
	}

	loosen := s.byKind(t, learn.KindLoosen)
	if loosen.Evidence == nil || loosen.Replay.Transitions[0].From != "require_approval" || loosen.Replay.Transitions[0].To != "allow" ||
		loosen.Replay.Transitions[0].HumanOutcome == nil || loosen.Replay.Transitions[0].HumanOutcome.Denied != 0 {
		t.Fatalf("loosen: %+v", loosen.Replay)
	}
	t.Run("shadow slot taken", func(t *testing.T) {
		_, err := s.svc.Accept(bg, s.org, s.priya, loosen.ID, learn.AcceptRequest{Reason: "Ops approves these"}, "")
		if codeOf(err) != httpx.CodeShadowSlotTaken {
			t.Fatalf("got %v, want shadow_slot_taken (v2 runs in shadow)", err)
		}
		if g, _ := s.svc.Get(bg, s.org, loosen.ID); g.Status != learn.SugOpen {
			t.Fatalf("status %s, want open again", g.Status)
		}
	})
	var accepted *learn.Suggestion
	t.Run("accept replaces the shadow", func(t *testing.T) {
		accepted, err = s.svc.Accept(bg, s.org, s.priya, loosen.ID, learn.AcceptRequest{Reason: "Ops approves these every time", ReplaceShadow: true}, "")
		if err != nil {
			t.Fatal(err)
		}
		if accepted.Status != learn.SugAccepted || accepted.ResultingVersion == nil || accepted.ResultingVersion.Version != 3 ||
			len(accepted.Deeds) != 2 || accepted.ReplacedShadow == nil || *accepted.ReplacedShadow != 2 {
			t.Fatalf("%+v", accepted)
		}
		if n := s.scalar(`SELECT count(*) FROM deedglass.policy_versions WHERE org_id = $1 AND version = 3 AND origin = 'suggestion' AND suggestion_id = $2`,
			s.org, loosen.ID); n != 1 {
			t.Fatal("v3 isn't origin suggestion with suggestion_id")
		}
		if n := s.scalar(`SELECT count(*) FROM deedglass.policy_activations a JOIN deedglass.policy_versions v ON v.id = a.version_id
		    WHERE a.org_id = $1 AND a.mode = 'shadow' AND a.deactivated_at IS NULL AND v.version = 3`, s.org); n != 1 {
			t.Fatal("v3 isn't the live shadow")
		}
		if n := s.scalar(`SELECT count(*) FROM deedglass.admin_events WHERE org_id = $1 AND action = 'suggestion.accepted' AND target_id = $2`, s.org, loosen.ID); n != 1 {
			t.Fatalf("%d suggestion.accepted events", n)
		}
		_, err := s.svc.Accept(bg, s.org, s.priya, loosen.ID, learn.AcceptRequest{ReplaceShadow: true}, "")
		if codeOf(err) != httpx.CodeSuggestionDecided {
			t.Fatalf("second accept: %v", err)
		}
	})
	t.Run("an accepted change isn't proposed again", func(t *testing.T) {
		r, err := s.svc.Generate(bg, s.org, s.priya)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range r.Created {
			if c.Kind == learn.KindLoosen {
				t.Fatalf("the accepted loosen came back: %+v", r)
			}
		}
	})
	t.Run("two accepts at once", func(t *testing.T) {
		tighten := s.byKind(t, learn.KindTighten)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Go(func() {
				_, errs[i] = s.svc.Accept(bg, s.org, s.priya, tighten.ID, learn.AcceptRequest{ReplaceShadow: true}, "")
			})
		}
		wg.Wait()
		ok, decided := 0, 0
		for _, e := range errs {
			switch {
			case e == nil:
				ok++
			case codeOf(e) == httpx.CodeSuggestionDecided:
				decided++
			default:
				t.Fatalf("unexpected: %v", e)
			}
		}
		if ok != 1 || decided != 1 {
			t.Fatalf("ok %d decided %d", ok, decided)
		}
		if n := s.scalar(`SELECT count(*) FROM deedglass.admin_events WHERE org_id = $1 AND action = 'suggestion.accepted' AND target_id = $2`, s.org, tighten.ID); n != 1 {
			t.Fatalf("%d suggestion.accepted deeds for one acceptance", n)
		}
	})
	t.Run("a crash between steps 5 and 6", func(t *testing.T) {
		budget := s.byKind(t, learn.KindBudget)
		crash := &learn.SuggestionService{Store: s.ls, Suggester: s.svc.Suggester, Writer: crashingWriter{shadowWriter{svc: s.policySvc()}}, Now: s.svc.Now}
		if _, err := crash.Accept(bg, s.org, s.priya, budget.ID, learn.AcceptRequest{Reason: "Raise it", ReplaceShadow: true}, ""); err == nil {
			t.Fatal("the crash didn't surface")
		}
		if g, _ := s.svc.Get(bg, s.org, budget.ID); g.Status != learn.SugAccepting {
			t.Fatalf("status %s, want accepting (the sweeper finishes it)", g.Status)
		}
		// Not yet stuck: the sweeper leaves it.
		if n, err := s.svc.Sweep(bg, s.org); err != nil || n != 0 {
			t.Fatalf("early sweep: %d %v", n, err)
		}
		// An hour later (updated_at is the database's; the sweeper compares it with its own clock).
		late := &learn.SuggestionService{Store: s.ls, Suggester: s.svc.Suggester, Writer: s.svc.Writer,
			Now: func() time.Time { return time.Now().Add(time.Hour) }}
		if n, err := late.Sweep(bg, s.org); err != nil || n != 1 {
			t.Fatalf("sweep: %d %v", n, err)
		}
		if n, err := late.Sweep(bg, s.org); err != nil || n != 0 {
			t.Fatalf("second sweep: %d %v", n, err)
		}
		g, _ := s.svc.Get(bg, s.org, budget.ID)
		if g.Status != learn.SugAccepted || g.DecidedBy == nil || g.DecidedBy.ID != s.priya.ID {
			t.Fatalf("after the sweep: %+v", g)
		}
		if n := s.scalar(`SELECT count(*) FROM deedglass.admin_events WHERE org_id = $1 AND action = 'suggestion.accepted' AND target_id = $2`, s.org, budget.ID); n != 1 {
			t.Fatalf("%d deeds", n)
		}
		if n := s.scalar(`SELECT count(*) FROM deedglass.admin_events WHERE org_id = $1 AND action = 'policy.activated'
		    AND data ->> 'version_id' = (SELECT resulting_version_id FROM deedglass.suggestions WHERE id = $2)`, s.org, budget.ID); n != 1 {
			t.Fatalf("%d activation deeds for the swept accept", n)
		}
	})
	t.Run("a stale base", func(t *testing.T) {
		svc := s.policySvc()
		p := policy.Caller{Org: s.org.String(), Actor: deed.Ref{Type: "user", ID: s.priya.ID}}
		if _, err := svc.Activate(bg, p, "acme-sim", policy.VersionRef{Number: 3}, policy.ActivateRequest{Mode: policy.ModeEnforce, Reason: "Promoted after the shadow agreed"}); err != nil {
			t.Fatal(err)
		}
		// An open suggestion written for v1 is now stale.
		rows, _ := s.svc.List(bg, s.org, learn.SuggestionFilter{Status: learn.SugOpen}, 100, "")
		if len(rows.Data) > 0 {
			_, err := s.svc.Accept(bg, s.org, s.priya, rows.Data[0].ID, learn.AcceptRequest{ReplaceShadow: true}, "")
			if codeOf(err) != httpx.CodeSuggestionStale {
				t.Fatalf("got %v, want suggestion_stale", err)
			}
		}
		r, err := s.svc.Generate(bg, s.org, s.priya)
		if err != nil {
			t.Fatal(err)
		}
		if r.Policy == nil || r.Policy.BaseVersion != 3 {
			t.Fatalf("the pass didn't read v3: %+v", r.Policy)
		}
	})
	t.Run("reject", func(t *testing.T) {
		gen, err := s.svc.Generate(bg, s.org, s.priya)
		if err != nil {
			t.Fatal(err)
		}
		_ = gen
		l, _ := s.svc.List(bg, s.org, learn.SuggestionFilter{Status: learn.SugOpen}, 100, "")
		if len(l.Data) == 0 {
			t.Fatal("nothing open to reject")
		}
		id := l.Data[0].ID
		if _, err := s.svc.Reject(bg, s.org, s.priya, id, learn.RejectRequest{}, ""); codeOf(err) != httpx.CodeValidationFailed {
			t.Fatalf("no reason: %v", err)
		}
		r, err := s.svc.Reject(bg, s.org, s.priya, id, learn.RejectRequest{Reason: "We review these by hand"}, "")
		if err != nil || r.Status != learn.SugRejected || len(r.Deeds) != 1 {
			t.Fatalf("%v %+v", err, r)
		}
	})
	chk, err := s.led.CheckChain(bg, s.org, ledger.Range{}, ledger.CheckOptions{})
	if err != nil || chk.Status != ledger.CheckIntact {
		t.Fatalf("chain: %v %+v", err, chk)
	}
}

// TestNeverEnforceBehaviour (S28 §3.6, way 3): thirty days of the schedule (a suggest pass a day,
// the sweeper, nobody accepting anything) never enforces a policy; every enforce activation in
// the org was a person's, or the onboarding bootstrap's.
func TestNeverEnforceBehaviour(t *testing.T) {
	if testing.Short() {
		t.Skip("a week of simulated traffic")
	}
	s := newSuggestEnv(t)
	now := simEnd
	sch := &learn.Scheduler{Store: s.ls, Suggestions: s.svc, Now: func() time.Time { return now }}
	for d := 0; d < 30; d++ {
		now = simEnd.Add(time.Duration(d)*24*time.Hour + 4*time.Hour) // 16:00 IST: after the 03:00 pass time
		sch.TickOrg(bg, s.org, now)
	}
	if n := s.scalar(`SELECT count(*) FROM deedglass.learn_runs WHERE org_id = $1 AND kind = 'suggest'`, s.org); n < 30 {
		t.Fatalf("only %d suggest runs in 30 days", n)
	}
	c := s.e.Admin(t)
	rows, err := c.Query(bg, `SELECT e.actor_type, COALESCE(e.actor_id, ''), COALESCE(v.origin, '')
	    FROM deedglass.admin_events AS e LEFT JOIN deedglass.policy_versions AS v ON v.id = e.data ->> 'version_id'
	    WHERE e.org_id = $1 AND e.action = 'policy.activated' AND e.data ->> 'mode' = 'enforce'`, s.org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var typ, id, origin string
		if err := rows.Scan(&typ, &id, &origin); err != nil {
			t.Fatal(err)
		}
		seen++
		if typ == "system" && origin != "onboarding" && origin != "starter" {
			t.Errorf("an enforce activation by the system (origin %q)", origin)
		}
		if origin == "suggestion" {
			t.Errorf("a suggested version was enforced by %s %s with nobody promoting it", typ, id)
		}
	}
	if seen == 0 {
		t.Fatal("no enforce activations at all; the query is wrong")
	}
	if n := s.scalar(`SELECT count(*) FROM deedglass.suggestions WHERE org_id = $1 AND status IN ('accepting', 'accepted')`, s.org); n != 0 {
		t.Fatalf("%d suggestions were accepted with nobody accepting them", n)
	}
	var ids []string
	r2, _ := c.Query(bg, `SELECT DISTINCT kind FROM deedglass.suggestions WHERE org_id = $1`, s.org)
	for r2.Next() {
		var k string
		_ = r2.Scan(&k)
		ids = append(ids, k)
	}
	r2.Close()
	if !slices.Contains(ids, learn.KindLoosen) {
		t.Fatalf("the schedule never suggested anything: %v", ids)
	}
}
