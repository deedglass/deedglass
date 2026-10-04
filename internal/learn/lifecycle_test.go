package learn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

// S28 §6 "Lifecycle": accept, reject and the sweeper through SuggestionService, over memStore and
// a fake PolicyWriter that seals policy.activated.

var lcAdmin = Actor{Type: "user", ID: "usr_01jbq000000000000000priya"}

type lcEnv struct {
	*sugEnv
	w   *fakeWriter
	svc *SuggestionService
	id  string // the loosen suggestion

	mu  sync.Mutex
	now time.Time
}

func newLC(t *testing.T) *lcEnv {
	t.Helper()
	e := newSugEnv(t, func(f *evFixture) { f.addOrderApprovals(401, 3, 0) })
	e.m.org(e.org).people = []memPerson{{DigestRecipient: DigestRecipient{UserID: lcAdmin.ID, Email: "priya@acme.example", Role: "admin"}}}
	run := e.generate()
	if len(run.Created) != 1 {
		t.Fatalf("setup: %+v", run)
	}
	l := &lcEnv{sugEnv: e, w: &fakeWriter{st: e.m}, id: run.Created[0].ID, now: sugNow}
	l.svc = &SuggestionService{Store: e.m, Suggester: e.sg, Writer: l.w, Now: l.clock}
	return l
}

func (l *lcEnv) clock() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.now
}

func (l *lcEnv) advance(d time.Duration) {
	l.mu.Lock()
	l.now = l.now.Add(d)
	l.mu.Unlock()
}

func (l *lcEnv) accept(req AcceptRequest) (*Suggestion, error) {
	return l.svc.Accept(bg, l.org, lcAdmin, l.id, req, "req_01jbq000000000000000000001")
}

// shadowV2 puts acme-sim v2 in shadow, as if someone activated it from Policies.
func (l *lcEnv) shadowV2() *ResolvedVersion {
	rv := l.m.addVersion(l.org, l.rv.PolicyID, "acme-sim", 2, simSource(l.t, 2))
	l.m.mu.Lock()
	vi := rv.VersionInfo
	vi.Live = true
	l.m.orgs[l.org].shadow, l.m.orgs[l.org].shadowDeed = &vi, "deed_v2shadow"
	l.m.mu.Unlock()
	return rv
}

func httpErr(t *testing.T, err error, code httpx.Code) *httpx.Error {
	t.Helper()
	e := httpx.AsError(err)
	if e == nil || e.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
	return e
}

func TestAcceptNoShadow(t *testing.T) {
	t.Parallel()
	l := newLC(t)
	row, _ := l.m.GetSuggestion(bg, l.org, l.id)
	got, err := l.accept(AcceptRequest{Reason: "Every one of these was routine."})
	if err != nil {
		t.Fatal(err)
	}
	adds, acts := l.w.calls()
	if len(adds) != 1 || adds[0].Sug != l.id || !bytes.Equal(adds[0].Src, row.CandidateSource) || !adds[0].Created || adds[0].PolicyID != l.rv.PolicyID ||
		adds[0].P.Actor != lcAdmin || adds[0].P.Org != l.org || adds[0].P.RequestID != "req_01jbq000000000000000000001" ||
		adds[0].Notes != "Created from Deedglass Insights suggestion "+l.id+"." {
		t.Fatalf("AddSuggestedVersion calls %+v", adds)
	}
	if len(acts) != 1 || acts[0].Version != 2 || acts[0].Reason != "Accepted suggestion "+l.id+": Every one of these was routine." {
		t.Fatalf("ActivateShadow calls %+v", acts)
	}
	if got.Status != SugAccepted || got.ResultingVersion == nil || got.ResultingVersion.Version != 2 || got.Note != nil || got.ReplacedShadow != nil {
		t.Fatalf("answer %+v", got)
	}
	if len(got.Deeds) != 2 || l.m.deedsOf("policy.activated", "") != 1 || l.m.deedsOf("suggestion.accepted", l.id) != 1 {
		t.Fatalf("deeds %v (%+v)", got.Deeds, l.m.deeds)
	}
	for _, d := range got.Deeds {
		if _, err := ids.Parse[ids.DeedKind](d); err != nil {
			t.Errorf("deed id %q: %v", d, err)
		}
	}
	if got.DecisionDeed == nil || *got.DecisionDeed != got.Deeds[1] {
		t.Errorf("decision deed %v", got.DecisionDeed)
	}
	if l.m.status(l.id) != SugAccepted {
		t.Fatal(l.m.status(l.id))
	}
	// The new version runs in shadow; the enforced one is untouched.
	sh, _, _ := l.m.LiveShadowActivation(bg, l.org)
	enf, _, _ := l.m.EnforcedSource(bg, l.org)
	if sh == nil || sh.Version != 2 || enf.Version != 1 {
		t.Fatalf("shadow %+v enforced %+v", sh, enf)
	}
	// Accepting again: suggestion_decided with who and what.
	_, err = l.accept(AcceptRequest{})
	if e := httpErr(t, err, httpx.CodeSuggestionDecided); !strings.Contains(e.Message, "already accepted on 2026-10-09 by priya@acme.example. It created acme-sim v2.") {
		t.Fatalf("message %q", e.Message)
	}
	// Rejecting an accepted one: decided too.
	_, err = l.svc.Reject(bg, l.org, lcAdmin, l.id, RejectRequest{Reason: "Changed my mind."}, "")
	httpErr(t, err, httpx.CodeSuggestionDecided)
}

func TestAcceptShadowSlot(t *testing.T) {
	t.Parallel()
	l := newLC(t)
	l.shadowV2()
	_, err := l.accept(AcceptRequest{Reason: "Looks right."})
	e := httpErr(t, err, httpx.CodeShadowSlotTaken)
	if e.Param != "replace_shadow" || e.Status != 409 || !strings.Contains(e.Message, "acme-sim v2 is already running in shadow") {
		t.Fatalf("%+v", e)
	}
	if l.m.status(l.id) != SugOpen {
		t.Fatalf("status %s, want open again", l.m.status(l.id))
	}
	if adds, acts := l.w.calls(); len(adds) != 0 || len(acts) != 0 {
		t.Fatalf("writer called: %v %v", adds, acts)
	}
	got, err := l.accept(AcceptRequest{Reason: "Replace the v2 trial.", ReplaceShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.ReplacedShadow == nil || *got.ReplacedShadow != 2 || got.ResultingVersion.Version != 3 || got.Status != SugAccepted {
		t.Fatalf("%+v", got)
	}
	sh, _, _ := l.m.LiveShadowActivation(bg, l.org)
	if sh.Version != 3 {
		t.Fatalf("shadow v%d", sh.Version)
	}
}

func TestAcceptStale(t *testing.T) {
	t.Parallel()
	l := newLC(t)
	l.m.enforce(l.org, l.rv.PolicyID, "acme-sim", 2, simSource(t, 2))
	_, err := l.accept(AcceptRequest{})
	e := httpErr(t, err, httpx.CodeSuggestionStale)
	if e.Message != "acme-sim v2 has been enforced since this suggestion was written for v1. Generate suggestions again: POST /v1/suggestions/generate." {
		t.Fatalf("message %q", e.Message)
	}
	if l.m.status(l.id) != SugSuperseded {
		t.Fatal(l.m.status(l.id))
	}
	if adds, _ := l.w.calls(); len(adds) != 0 {
		t.Fatal("a stale accept reached the writer")
	}
	// No enforced policy at all: stale too.
	l2 := newLC(t)
	l2.m.mu.Lock()
	l2.m.orgs[l2.org].enforced = nil
	l2.m.mu.Unlock()
	_, err = l2.accept(AcceptRequest{})
	if e := httpErr(t, err, httpx.CodeSuggestionStale); !strings.Contains(e.Message, "v0") {
		t.Fatalf("%q", e.Message)
	}
}

func TestAcceptConcurrent(t *testing.T) {
	t.Parallel()
	l := newLC(t)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = l.accept(AcceptRequest{Reason: "Race."})
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case codeOf(err) != httpx.CodeSuggestionDecided:
			t.Errorf("loser: %v, want suggestion_decided", err)
		}
	}
	if wins != 1 || l.m.deedsOf("suggestion.accepted", l.id) != 1 || l.m.deedsOf("policy.activated", "") != 1 {
		t.Fatalf("wins %d, accepted deeds %d, activations %d", wins, l.m.deedsOf("suggestion.accepted", l.id), l.m.deedsOf("policy.activated", ""))
	}
	if adds, _ := l.w.calls(); len(adds) != 1 {
		t.Fatalf("%d versions added", len(adds))
	}
}

func TestAcceptCrashThenSweep(t *testing.T) {
	t.Parallel()
	l := newLC(t)
	crashed := false
	l.w.afterAct = func() error {
		if crashed {
			return nil
		}
		crashed = true
		return errors.New("connection reset by peer")
	}
	_, err := l.accept(AcceptRequest{Reason: "Accept it."})
	if err == nil || httpx.AsError(err) != nil {
		t.Fatalf("crash: %v", err)
	}
	if l.m.status(l.id) != SugAccepting || l.m.deedsOf("suggestion.accepted", "") != 0 || l.m.deedsOf("policy.activated", "") != 1 {
		t.Fatalf("after the crash: %s, %d accepted deeds", l.m.status(l.id), l.m.deedsOf("suggestion.accepted", ""))
	}
	// Not stuck long enough yet.
	if n, err := l.svc.Sweep(bg, l.org); n != 0 || err != nil {
		t.Fatalf("early sweep: %d %v", n, err)
	}
	// A retry by a person meanwhile: decided ("someone is accepting").
	_, err = l.accept(AcceptRequest{})
	if e := httpErr(t, err, httpx.CodeSuggestionDecided); !strings.Contains(e.Message, "accepting this suggestion right now") {
		t.Fatalf("%q", e.Message)
	}
	l.advance(StuckAfter + time.Second)
	if n, err := l.svc.Sweep(bg, l.org); n != 1 || err != nil {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if l.m.status(l.id) != SugAccepted || l.m.deedsOf("suggestion.accepted", l.id) != 1 || l.m.deedsOf("policy.activated", "") != 1 {
		t.Fatalf("after the sweep: %s, accepted deeds %d, activations %d", l.m.status(l.id), l.m.deedsOf("suggestion.accepted", l.id), l.m.deedsOf("policy.activated", ""))
	}
	adds, acts := l.w.calls()
	if len(adds) != 2 || adds[1].Created || len(acts) != 1 || adds[1].P.Actor != lcAdmin {
		t.Fatalf("the sweeper re-ran step 4 only, as the accepting person: %+v %+v", adds, acts)
	}
	got, _ := l.m.GetSuggestion(bg, l.org, l.id)
	if got.ResultingVersion == nil || got.ResultingVersion.Version != 2 {
		t.Fatalf("%+v", got.ResultingVersion)
	}
	l.advance(time.Hour)
	if n, err := l.svc.Sweep(bg, l.org); n != 0 || err != nil || l.m.deedsOf("suggestion.accepted", "") != 1 {
		t.Fatalf("second sweep: %d %v", n, err)
	}
}

func TestSweepEdges(t *testing.T) {
	t.Parallel()
	// The base moved while it was stuck: superseded.
	l := newLC(t)
	l.w.afterAct = func() error { return errors.New("crash") }
	_, _ = l.accept(AcceptRequest{})
	l.m.enforce(l.org, l.rv.PolicyID, "acme-sim", 2, simSource(t, 2))
	l.advance(StuckAfter + time.Second)
	if n, err := l.svc.Sweep(bg, l.org); n != 0 || err != nil || l.m.status(l.id) != SugSuperseded {
		t.Fatalf("%d %v %s", n, err, l.m.status(l.id))
	}
	// The policy service refuses on retry: back to open.
	l2 := newLC(t)
	l2.w.afterAct = func() error { return errors.New("crash") }
	_, _ = l2.accept(AcceptRequest{})
	l2.w.afterAct = nil
	l2.w.errAdd = &policy.Error{Kind: "conflict", Message: "stored version tampered"}
	l2.advance(StuckAfter + time.Second)
	if n, err := l2.svc.Sweep(bg, l2.org); n != 0 || err != nil || l2.m.status(l2.id) != SugOpen {
		t.Fatalf("%d %v %s", n, err, l2.m.status(l2.id))
	}
	// Another error stops the sweep with it.
	l3 := newLC(t)
	l3.w.afterAct = func() error { return errors.New("crash") }
	_, _ = l3.accept(AcceptRequest{})
	l3.w.errAdd = errors.New("db down")
	l3.advance(StuckAfter + time.Second)
	if _, err := l3.svc.Sweep(bg, l3.org); err == nil {
		t.Fatal("sweep swallowed the error")
	}
	// No writer: nothing to do.
	if n, err := (&SuggestionService{Store: l3.m}).Sweep(bg, l3.org); n != 0 || err != nil {
		t.Fatal(n, err)
	}
}

func TestAcceptIdenticalSource(t *testing.T) {
	t.Parallel()
	l := newLC(t)
	row, _ := l.m.GetSuggestion(bg, l.org, l.id)
	l.m.addVersion(l.org, l.rv.PolicyID, "acme-sim", 2, row.CandidateSource)
	got, err := l.accept(AcceptRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Note == nil || *got.Note != "This change already existed as acme-sim v2, so that version runs in shadow; no new version was created." {
		t.Fatalf("note %v", got.Note)
	}
	if adds, _ := l.w.calls(); len(adds) != 1 || adds[0].Created || got.ResultingVersion.Version != 2 {
		t.Fatalf("%+v", adds)
	}
	_, acts := l.w.calls()
	if acts[0].Reason != "Accepted suggestion "+l.id+"." {
		t.Errorf("reason without a person's reason: %q", acts[0].Reason)
	}
}

func TestAcceptRefusals(t *testing.T) {
	t.Parallel()
	l := newLC(t)
	// No writer: 503.
	none := &SuggestionService{Store: l.m}
	if _, err := none.Accept(bg, l.org, lcAdmin, l.id, AcceptRequest{}, ""); codeOf(err) != httpx.CodeServiceUnavailable {
		t.Fatalf("no writer: %v", err)
	}
	// Bad ids: 404.
	for _, id := range []string{"nope", ids.New[ids.SuggestionKind]().String(), ids.New[ids.ReplayKind]().String()} {
		if _, err := l.svc.Accept(bg, l.org, lcAdmin, id, AcceptRequest{}, ""); codeOf(err) != httpx.CodeNotFound {
			t.Errorf("accept %s: %v", id, err)
		}
		if _, err := l.svc.Reject(bg, l.org, lcAdmin, id, RejectRequest{Reason: "Not needed."}, ""); codeOf(err) != httpx.CodeNotFound {
			t.Errorf("reject %s: %v", id, err)
		}
	}
	// Another org's id: 404.
	other := ids.New[ids.OrgKind]()
	l.m.addOrg(other, "Other", "UTC")
	if _, err := l.svc.Accept(bg, other, lcAdmin, l.id, AcceptRequest{}, ""); codeOf(err) != httpx.CodeNotFound {
		t.Errorf("cross-org accept: %v", err)
	}
	// A reason that is too long or has a line break.
	for _, r := range []string{strings.Repeat("x", 281), "two\nlines"} {
		_, err := l.accept(AcceptRequest{Reason: r})
		if e := httpErr(t, err, httpx.CodeValidationFailed); e.Param != "reason" {
			t.Errorf("param %q", e.Param)
		}
	}
	if l.m.status(l.id) != SugOpen {
		t.Fatal("a refused accept moved the status")
	}
	// An advisory has nothing to accept.
	adv := l.m.putSuggestion(l.org, NewSuggestion{ID: ids.New[ids.SuggestionKind]().String(), Kind: KindAdvisory, Generator: GenAdvisoryDenyHeavy,
		Fingerprint: "sha256:adv", PolicyID: l.rv.PolicyID, BaseVersionID: l.rv.VersionID}, SugOpen)
	_, err := l.svc.Accept(bg, l.org, lcAdmin, adv.ID, AcceptRequest{}, "")
	if e := httpErr(t, err, httpx.CodeConflict); e.Message != MsgAcceptAdvisory {
		t.Fatalf("%q", e.Message)
	}
	// The policy service refuses the version: back to open, its error returned.
	l.w.errAdd = &policy.Error{Kind: "conflict", Message: "refused"}
	if _, err := l.accept(AcceptRequest{}); err == nil || l.m.status(l.id) != SugOpen {
		t.Fatalf("refused: %v %s", err, l.m.status(l.id))
	}
	l.w.errAdd = nil
	// Reading the enforced policy fails: back to open.
	l.m.errEnforced = errors.New("db down")
	if _, err := l.accept(AcceptRequest{}); err == nil || l.m.status(l.id) != SugOpen {
		t.Fatalf("enforced unreadable: %v %s", err, l.m.status(l.id))
	}
	l.m.errEnforced = nil
	// ActivateShadow fails with a plain error: left in accepting for the sweeper.
	l.w.errAct = errors.New("timeout")
	if _, err := l.accept(AcceptRequest{}); err == nil || l.m.status(l.id) != SugAccepting {
		t.Fatalf("activate failed: %v %s", err, l.m.status(l.id))
	}
}

func TestReject(t *testing.T) {
	t.Parallel()
	l := newLC(t)
	for _, r := range []string{"", "  ", "no", strings.Repeat("y", 281), "bad\x01char"} {
		_, err := l.svc.Reject(bg, l.org, lcAdmin, l.id, RejectRequest{Reason: r}, "")
		e := httpErr(t, err, httpx.CodeValidationFailed)
		if e.Param != "reason" || e.Status != 400 {
			t.Errorf("%q: %+v", r, e)
		}
		if strings.TrimSpace(r) == "" && e.Message != MsgRejectReason {
			t.Errorf("missing reason message %q", e.Message)
		}
	}
	if l.m.deedsOf("suggestion.rejected", "") != 0 {
		t.Fatal("a refused reject sealed")
	}
	got, err := l.svc.Reject(bg, l.org, lcAdmin, l.id, RejectRequest{Reason: "  We want a person on every order change.  "}, "req_x")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != SugRejected || len(got.Deeds) != 1 || l.m.deedsOf("suggestion.rejected", l.id) != 1 || *got.DecisionReason != "We want a person on every order change." {
		t.Fatalf("%+v", got)
	}
	_, err = l.svc.Reject(bg, l.org, lcAdmin, l.id, RejectRequest{Reason: "Again."}, "")
	if e := httpErr(t, err, httpx.CodeSuggestionDecided); !strings.Contains(e.Message, "already rejected on 2026-10-09 by priya@acme.example") {
		t.Fatalf("%q", e.Message)
	}
	if _, err := l.accept(AcceptRequest{}); codeOf(err) != httpx.CodeSuggestionDecided {
		t.Fatalf("accept after reject: %v", err)
	}
}

func TestActivationReason(t *testing.T) {
	t.Parallel()
	if got := activationReason("sug_1", ""); got != "Accepted suggestion sug_1." {
		t.Error(got)
	}
	long := activationReason("sug_1", strings.Repeat("é", 300))
	if n := len([]rune(long)); n != 280 || !strings.HasSuffix(long, "…") {
		t.Errorf("clipped to %d runes", n)
	}
}

// --- the routes ------------------------------------------------------------------------------------

// sugRoleAuth authenticates every request as one fixed principal (api_test.go's roleAuth, here in
// package learn).
type sugRoleAuth struct{ p httpx.Principal }

func (a sugRoleAuth) AuthenticateSession(context.Context, *http.Request) (httpx.Principal, error) {
	if a.p.Kind != httpx.KindUser {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return a.p, nil
}

func (a sugRoleAuth) AuthenticateKey(context.Context, string) (httpx.Principal, error) {
	if a.p.Kind == httpx.KindUser || a.p.Kind == "" {
		return httpx.Principal{}, httpx.E(httpx.CodeInvalidAPIKey)
	}
	return a.p, nil
}

// The route × role matrix for the five suggestion routes: viewers read, admins (and org admin
// keys) change, agent keys get 403 everywhere, no credentials 401. With a nil service every
// handler answers 503, so a 503 means the request passed authentication and the role check.
func TestSuggestionRouteRoleMatrix(t *testing.T) {
	t.Parallel()
	routes := SuggestionRoutes(nil)
	if len(routes) != 5 {
		t.Fatalf("%d routes", len(routes))
	}
	org := ids.New[ids.OrgKind]()
	user := func(r httpx.Role) httpx.Principal {
		return httpx.Principal{Kind: httpx.KindUser, OrgID: org, UserID: ids.New[ids.UserKind](), Role: r, OrgName: "Acme"}
	}
	ps := map[string]httpx.Principal{
		"owner":     user(httpx.RoleOwner),
		"admin":     user(httpx.RoleAdmin),
		"viewer":    user(httpx.RoleViewer),
		"admin key": {Kind: httpx.KindAdminKey, OrgID: org, KeyID: ids.New[ids.APIKeyKind](), Role: httpx.RoleAdmin, Scopes: []string{httpx.ScopeAdmin}},
		"agent key": {Kind: httpx.KindAgentKey, OrgID: org, KeyID: ids.New[ids.APIKeyKind](), AgentID: ids.New[ids.AgentKind](),
			Scopes: []string{"actions:execute", "actions:read", "deeds:read"}, AgentStatus: "active", AgentName: "support-bot"},
		"none": {},
	}
	bodies := map[string]string{"acceptSuggestion": `{"reason":"Fine."}`, "rejectSuggestion": `{"reason":"Not now."}`}
	sid := ids.New[ids.SuggestionKind]().String()
	for _, name := range []string{"owner", "admin", "viewer", "admin key", "agent key", "none"} {
		p := ps[name]
		srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: sugRoleAuth{p}, Keys: sugRoleAuth{p}})
		srv.Mount(routes...)
		ts := httptest.NewServer(srv)
		for _, rt := range routes {
			path := strings.Replace(rt.Pattern, "{id}", sid, 1)
			var body io.Reader
			if b, ok := bodies[rt.OperationID]; ok {
				body = strings.NewReader(b)
			}
			req, _ := http.NewRequest(rt.Method, ts.URL+path, body)
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			if p.Kind != httpx.KindUser && p.Kind != "" {
				req.Header.Set("Authorization", "Bearer dg_test_AAAAAAAA_"+strings.Repeat("B", 32))
			}
			r, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			got := string(b)
			key := name + " · " + rt.Method + " " + rt.Pattern
			write := rt.Method != "GET"
			if write != (rt.MinRole == httpx.RoleAdmin) {
				t.Errorf("%s: MinRole %s", key, rt.MinRole)
			}
			passed := r.StatusCode == http.StatusServiceUnavailable && strings.Contains(got, `"service_unavailable"`)
			switch {
			case name == "none":
				if r.StatusCode != http.StatusUnauthorized {
					t.Errorf("%s: %d %s, want 401", key, r.StatusCode, got)
				}
			case name == "agent key":
				if r.StatusCode != http.StatusForbidden || !strings.Contains(got, `"insufficient_scope"`) || !strings.Contains(got, MsgAgentKey) {
					t.Errorf("%s: %d %s, want 403 insufficient_scope", key, r.StatusCode, got)
				}
			case name == "viewer" && write:
				if r.StatusCode != http.StatusForbidden || !strings.Contains(got, `"insufficient_role"`) {
					t.Errorf("%s: %d %s, want 403 insufficient_role", key, r.StatusCode, got)
				}
			default:
				if !passed {
					t.Errorf("%s: %d %s, want 503 (past auth)", key, r.StatusCode, got)
				}
			}
		}
		ts.Close()
	}
}

// The handlers over a real service, past authentication.
func TestSuggestionHandlers(t *testing.T) {
	t.Parallel()
	l := newLC(t)
	adv := l.m.putSuggestion(l.org, NewSuggestion{ID: ids.New[ids.SuggestionKind]().String(), Kind: KindAdvisory, Generator: GenAdvisoryDenyHeavy,
		Fingerprint: "sha256:adv", PolicyID: l.rv.PolicyID, BaseVersionID: l.rv.VersionID}, SugOpen)
	mux := http.NewServeMux()
	for _, rt := range SuggestionRoutes(l.svc) {
		h := rt.Handler
		mux.HandleFunc(rt.Method+" "+rt.Pattern, func(w http.ResponseWriter, r *http.Request) {
			if err := h(w, r); err != nil {
				e := httpx.AsError(err)
				if e == nil {
					e = httpx.E(httpx.CodeInternalError)
				}
				w.WriteHeader(e.Status)
				_ = json.NewEncoder(w).Encode(map[string]any{"code": e.Code, "message": e.Message, "param": e.Param})
			}
		})
	}
	admin := &identity.Principal{Kind: httpx.KindUser, OrgID: l.org, UserID: ids.New[ids.UserKind](), Role: httpx.RoleAdmin}
	call := func(method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
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
		{"GET", "/v1/suggestions", "", 200, `"object":"list"`},
		{"GET", "/v1/suggestions?status=all&kind=loosen", "", 200, l.id},
		{"GET", "/v1/suggestions?status=weird", "", 400, MsgSuggestionStatus},
		{"GET", "/v1/suggestions?kind=weird", "", 400, MsgSuggestionKind},
		{"GET", "/v1/suggestions?bogus=1", "", 400, "Unknown parameter"},
		{"GET", "/v1/suggestions?limit=0", "", 400, "limit"},
		{"GET", "/v1/suggestions/" + l.id, "", 200, `"object":"suggestion"`},
		{"GET", "/v1/suggestions/" + l.id + "?x=1", "", 400, "Unknown parameter"},
		{"GET", "/v1/suggestions/nope", "", 404, "No suggestion nope"},
		{"POST", "/v1/suggestions/" + adv.ID + "/accept", "", 409, "advice"},
		{"POST", "/v1/suggestions/" + adv.ID + "/reject", `{}`, 400, "A reason is required"},
		{"POST", "/v1/suggestions/" + adv.ID + "/reject", `{"reason":"Known, it's the refunds week."}`, 200, `"status":"rejected"`},
		{"POST", "/v1/suggestions/" + l.id + "/accept", `{"reason":"ok","replace_shadow":false}`, 200, `"status":"accepted"`},
		{"POST", "/v1/suggestions/" + l.id + "/accept", `{"bogus":1}`, 400, ""},
		{"POST", "/v1/suggestions/generate", "", 200, `"object":"learn_run"`},
	}
	for _, c := range cases {
		code, body := call(c.method, c.path, c.body)
		if code != c.status || !strings.Contains(body, c.has) {
			t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, code, body)
		}
	}
	// No principal.
	r := httptest.NewRequest("GET", "/v1/suggestions", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("no principal: %d", w.Code)
	}
}
