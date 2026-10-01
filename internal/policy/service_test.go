package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/deedglass/deedglass/examples/policies"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
)

var bg = context.Background()

func starter(t *testing.T, name string) []byte {
	t.Helper()
	b, err := policies.Source(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type world struct {
	t    *testing.T
	repo *memRepo
	reg  *Registry
	svc  *Service
	org  string
	who  Caller
}

func newWorld(t *testing.T) *world {
	repo := newMemRepo()
	reg := NewRegistry(repo, nil)
	org := ids.New[ids.OrgKind]().String()
	return &world{t: t, repo: repo, reg: reg, svc: NewService(repo, WithRegistry(reg)), org: org,
		who: Caller{Org: org, Actor: deed.Ref{Type: "user", ID: ids.New[ids.UserKind]().String()}, RequestID: "req_test"}}
}

func (w *world) push(name string) *Version {
	w.t.Helper()
	_, v, err := w.svc.Create(bg, w.who, PushRequest{Source: starter(w.t, name)})
	if err != nil {
		w.t.Fatal(err)
	}
	return v
}

func (w *world) activate(policy string, n int, mode Mode, acks ...string) *Activation {
	w.t.Helper()
	a, err := w.svc.Activate(bg, w.who, policy, VersionRef{Number: n}, ActivateRequest{Mode: mode, Reason: "Testing the change.", AcknowledgeWarnings: acks})
	if err != nil {
		w.t.Fatal(err)
	}
	return a
}

func wantKind(t *testing.T, err error, kind ErrKind, contains string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Kind != kind || !strings.Contains(e.Message, contains) {
		t.Fatalf("want %s containing %q, got %v", kind, contains, err)
	}
}

// checkDeed verifies a deed the service sealed: an admin deed with a null policy block, the
// action, the version as target, and data whose digest is in the deed.
func (w *world) checkDeed(id, action, versionID string, mode Mode) ActivationData {
	w.t.Helper()
	o := w.repo.org(w.org)
	raw := o.deeds[id]
	res, err := deed.VerifyEnvelope(raw, w.repo.keys, deed.VerifyOptions{Strict: true})
	if err != nil {
		w.t.Fatalf("deed %s: %v", id, err)
	}
	b := res.Envelope.Body
	if b.Kind != deed.KindAdmin || b.Policy != nil || b.Admin.Action != action || b.Admin.Target.ID != versionID ||
		b.Admin.Target.Type != "policy_version" || *b.Admin.Reason != "Testing the change." {
		w.t.Fatalf("deed %s body: %+v %+v", id, b, b.Admin)
	}
	var data ActivationData
	if err := json.Unmarshal(o.events[id], &data); err != nil {
		w.t.Fatal(err)
	}
	if dg, _ := deed.DigestJSON(o.events[id]); dg != *b.Admin.DataDigest {
		w.t.Fatal("data digest")
	}
	if data.Mode != mode || data.VersionID != versionID {
		w.t.Fatalf("data %+v", data)
	}
	if strings.Contains(string(raw), "deedglass: policy/v1") {
		w.t.Fatal("the deed must never contain the YAML")
	}
	return data
}

func TestCreateAndPush(t *testing.T) {
	w := newWorld(t)
	p, v, err := w.svc.Create(bg, w.who, PushRequest{Source: starter(t, "postgres-safe"), Notes: "Orders DB, first cut"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusInactive || p.LatestVersion != 1 || p.ActiveVersion != nil || p.Name != "postgres-safe" ||
		p.Description != "Reads run freely. Writes wait for a human. Schema changes never run." {
		t.Fatalf("%+v", p)
	}
	if v.Version != 1 || v.Rules != 12 || v.Budgets != 2 || v.Origin != OriginManual || v.Author.Object != "user" || *v.Notes != "Orders DB, first cut" ||
		!strings.HasPrefix(v.ID, "pv_") || !strings.HasPrefix(v.VersionHash, "sha256:") || v.Source != nil || v.Lang != "deedglass.policy/v1" {
		t.Fatalf("%+v", v)
	}
	if n := len(w.repo.deedsOf(w.org)); n != 0 {
		t.Fatalf("a push seals nothing; got %d deeds", n)
	}
	// Identical content: the same version, nothing saved.
	same, created, err := w.svc.AddVersion(bg, w.who, "postgres-safe", PushRequest{Source: starter(t, "postgres-safe")})
	if err != nil || created || same.ID != v.ID || same.Name != "postgres-safe" {
		t.Fatalf("%v %v %+v", err, created, same)
	}
	// Re-indented content is the same policy (the hash is over the normalized form).
	reindented := strings.Replace(string(starter(t, "postgres-safe")), "description: Reads run freely.", "description:    Reads run freely.", 1)
	if same, created, err = w.svc.AddVersion(bg, w.who, p.ID, PushRequest{Source: []byte(reindented)}); err != nil || created || same.ID != v.ID {
		t.Fatalf("%v %v", err, created)
	}
	// A real change is v2.
	changed := strings.Replace(string(starter(t, "postgres-safe")), "max_calls: 60", "max_calls: 30", 1)
	v2, created, err := w.svc.AddVersion(bg, w.who, "postgres-safe", PushRequest{Source: []byte(changed)})
	if err != nil || !created || v2.Version != 2 || v2.Name != "postgres-safe" {
		t.Fatalf("%v %v %+v", err, created, v2)
	}
	// Pushing v1's content again returns v1: versions are never duplicated.
	again, created, err := w.svc.AddVersion(bg, w.who, "postgres-safe", PushRequest{Source: starter(t, "postgres-safe")})
	if err != nil || created || again.Version != 1 {
		t.Fatalf("%v %v %+v", err, created, again)
	}
	// Another name to an existing id.
	_, _, err = w.svc.AddVersion(bg, w.who, p.ID, PushRequest{Source: starter(t, "stripe-refunds")})
	wantKind(t, err, KindValidation, `This YAML is named "stripe-refunds", but `+p.ID+` is "postgres-safe". Push it as a new policy, or change the name.`)
	// The same name twice.
	_, _, err = w.svc.Create(bg, w.who, PushRequest{Source: starter(t, "postgres-safe")})
	wantKind(t, err, KindConflict, `A policy named "postgres-safe" already exists`)
	// A bad source.
	_, _, err = w.svc.Create(bg, w.who, PushRequest{Source: []byte("deedglass: policy/v1\nname: demo\nrules:\n  - id: a\n    desicion: deny\n")})
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindInvalid || e.Param != "source" || len(e.Diagnostics) == 0 {
		t.Fatalf("%v", err)
	}
	found := false
	for _, d := range e.Diagnostics {
		found = found || (d.Code == "unknown_key" && d.Line == 5 && d.Hint == `Did you mean "decision"?`)
	}
	if !found || !strings.HasPrefix(e.Message, "The policy has ") {
		t.Fatalf("%s %+v", e.Message, e.Diagnostics)
	}
	_, _, err = w.svc.Create(bg, w.who, PushRequest{Source: []byte("  ")})
	wantKind(t, err, KindValidation, "source is required")
	_, _, err = w.svc.Create(bg, w.who, PushRequest{Source: starter(t, "gmail-drafts-only"), Notes: strings.Repeat("n", MaxNotes+1)})
	wantKind(t, err, KindValidation, "Notes are at most 2,000 characters")
	// Lists and gets.
	w.push("gmail-drafts-only")
	list, err := w.svc.List(bg, w.who, Page{Limit: 1})
	if err != nil || len(list.Data) != 1 || !list.HasMore || list.NextCursor == nil || list.Data[0].Name != "gmail-drafts-only" {
		t.Fatalf("%v %+v", err, list)
	}
	rest, err := w.svc.List(bg, w.who, Page{Limit: 1, Cursor: *list.NextCursor})
	if err != nil || len(rest.Data) != 1 || rest.HasMore || rest.Data[0].Name != "postgres-safe" || rest.Data[0].LatestVersion != 2 {
		t.Fatalf("%v %+v", err, rest)
	}
	vs, err := w.svc.ListVersions(bg, w.who, "postgres-safe", Page{Limit: 1})
	if err != nil || len(vs.Data) != 1 || vs.Data[0].Version != 2 || !vs.HasMore || vs.Data[0].Source != nil {
		t.Fatalf("%v %+v", err, vs)
	}
	vs2, err := w.svc.ListVersions(bg, w.who, "postgres-safe", Page{Limit: 5, Cursor: *vs.NextCursor})
	if err != nil || len(vs2.Data) != 1 || vs2.Data[0].Version != 1 {
		t.Fatalf("%v %+v", err, vs2)
	}
	got, err := w.svc.GetVersion(bg, w.who, "postgres-safe", VersionRef{ID: v.ID})
	if err != nil || got.Source == nil || *got.Source != string(starter(t, "postgres-safe")) || got.Name != "postgres-safe" {
		t.Fatalf("%v", err)
	}
	_, err = w.svc.GetVersion(bg, w.who, "postgres-safe", VersionRef{Number: 9})
	wantKind(t, err, KindNotFound, "postgres-safe has no version 9")
	_, err = w.svc.Get(bg, w.who, "nope")
	wantKind(t, err, KindNotFound, "There's no policy nope")
	_, err = w.svc.List(bg, w.who, Page{Cursor: "garbage!"})
	wantKind(t, err, KindValidation, "cursor")
}

func TestActivationEdges(t *testing.T) {
	w := newWorld(t)
	// Onboarding: default-deny enforced, actor system.
	o := Caller{Org: w.org}
	var boot *Activation
	if err := w.repo.InOrgTx(bg, w.org, func(tx Tx) error {
		var err error
		boot, err = Bootstrap(bg, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if boot.Mode != ModeEnforce || boot.ActivatedBy.Object != "system" || len(boot.Deeds) != 1 || boot.Reason != BootstrapReason {
		t.Fatalf("%+v", boot)
	}
	env := w.repo.lastDeed(w.org)
	if env.Body.Admin.Actor != SystemActor || env.Body.Admin.Action != ActionActivated {
		t.Fatalf("%+v", env.Body.Admin)
	}
	// Bootstrap again does nothing.
	if err := w.repo.InOrgTx(bg, w.org, func(tx Tx) error {
		a, err := Bootstrap(bg, tx)
		if a != nil {
			t.Fatal("second bootstrap activated something")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	set, err := w.svc.Active(bg, o)
	if err != nil || set.Enforce == nil || set.Enforce.Name != "default-deny" || set.Shadow != nil {
		t.Fatalf("%v %+v", err, set)
	}
	active, _ := w.reg.Active(bg, w.org)
	if active.Enforce.Name != "default-deny" || active.Enforce.Unavailable != nil {
		t.Fatalf("%+v", active.Enforce)
	}

	v1 := w.push("postgres-safe")
	// activate(v, shadow): one deed; default-deny stays enforced.
	sh := w.activate("postgres-safe", 1, ModeShadow)
	if len(sh.Deeds) != 1 || sh.Previous != nil || sh.PromotedFromShadow || !strings.HasPrefix(sh.ID, "pa_") {
		t.Fatalf("%+v", sh)
	}
	w.checkDeed(sh.Deeds[0], ActionActivated, v1.ID, ModeShadow)
	active, _ = w.reg.Active(bg, w.org)
	if active.Shadow == nil || active.Shadow.VersionHash != v1.VersionHash || active.Enforce.Name != "default-deny" {
		t.Fatalf("%+v %+v", active.Enforce, active.Shadow)
	}
	// Shadow the same version again: conflict.
	_, err = w.svc.Activate(bg, w.who, "postgres-safe", VersionRef{Number: 1}, ActivateRequest{Mode: ModeShadow, Reason: "Again."})
	wantKind(t, err, KindConflict, "postgres-safe v1 is already the shadow policy. Nothing changed.")
	// activate(shadow's v, enforce): promote, two deeds, shadow cleared.
	pr := w.activate("postgres-safe", 1, ModeEnforce)
	if len(pr.Deeds) != 2 || !pr.PromotedFromShadow || pr.Previous == nil || pr.Previous.Name != "default-deny" || pr.Previous.Version != 1 {
		t.Fatalf("%+v", pr)
	}
	w.checkDeed(pr.Deeds[0], ActionDeactivated, v1.ID, ModeShadow)
	data := w.checkDeed(pr.Deeds[1], ActionActivated, v1.ID, ModeEnforce)
	if data.PreviousVersionID == nil || *data.PreviousVersionID != set.Enforce.VersionID || data.ActivationID != pr.ID {
		t.Fatalf("%+v", data)
	}
	set, _ = w.svc.Active(bg, o)
	if set.Shadow != nil || set.Enforce.Name != "postgres-safe" || set.Enforce.Deed != pr.Deeds[1] {
		t.Fatalf("%+v", set)
	}
	active, _ = w.reg.Active(bg, w.org)
	if active.Enforce.VersionHash != v1.VersionHash || active.Shadow != nil {
		t.Fatal("the registry didn't swap")
	}
	// Already enforced.
	_, err = w.svc.Activate(bg, w.who, "postgres-safe", VersionRef{Number: 1}, ActivateRequest{Mode: ModeEnforce, Reason: "Again."})
	wantKind(t, err, KindConflict, "postgres-safe v1 is already the enforced policy. Nothing changed.")
	// Shadow the enforced version.
	_, err = w.svc.Activate(bg, w.who, "postgres-safe", VersionRef{Number: 1}, ActivateRequest{Mode: ModeShadow, Reason: "Shadow it."})
	wantKind(t, err, KindConflict, "is the enforced policy, so it can't also run in shadow")
	// Deactivate enforce.
	_, err = w.svc.Deactivate(bg, w.who, "postgres-safe", VersionRef{Number: 1}, DeactivateRequest{Reason: "Off."})
	wantKind(t, err, KindConflict, "The enforced policy can't be switched off. Activate another version or default-deny instead.")
	// deactivate(v, shadow): one deed.
	w.push("github-read-mostly")
	w.activate("github-read-mostly", 1, ModeShadow)
	da, err := w.svc.Deactivate(bg, w.who, "github-read-mostly", VersionRef{Number: 1}, DeactivateRequest{Reason: "Testing the change."})
	if err != nil || len(da.Deeds) != 1 || da.DeactivatedAt == nil || da.DeactivatedBy == nil {
		t.Fatalf("%v %+v", err, da)
	}
	_, err = w.svc.Deactivate(bg, w.who, "github-read-mostly", VersionRef{Number: 1}, DeactivateRequest{Reason: "Again."})
	wantKind(t, err, KindConflict, "isn't the shadow policy")
	// A plain enforce replacement (no shadow): one deed, previous named.
	rb := w.activate("default-deny", 1, ModeEnforce)
	if len(rb.Deeds) != 1 || rb.Previous == nil || rb.Previous.Name != "postgres-safe" || rb.PromotedFromShadow {
		t.Fatalf("%+v", rb)
	}
	// Roll back = activate an older version.
	changed := strings.Replace(string(starter(t, "postgres-safe")), "max_calls: 60", "max_calls: 30", 1)
	if _, _, err := w.svc.AddVersion(bg, w.who, "postgres-safe", PushRequest{Source: []byte(changed)}); err != nil {
		t.Fatal(err)
	}
	w.activate("postgres-safe", 2, ModeEnforce)
	back := w.activate("postgres-safe", 1, ModeEnforce)
	if back.Previous == nil || back.Previous.Version != 2 {
		t.Fatalf("%+v", back)
	}
	// Reasons and modes.
	for _, r := range []string{"", "  ", "ab", strings.Repeat("x", 281), "line\nbreak"} {
		_, err = w.svc.Activate(bg, w.who, "postgres-safe", VersionRef{Number: 2}, ActivateRequest{Mode: ModeEnforce, Reason: r})
		wantKind(t, err, KindValidation, "reason")
	}
	_, err = w.svc.Activate(bg, w.who, "postgres-safe", VersionRef{Number: 2}, ActivateRequest{Mode: "both", Reason: "Fine."})
	wantKind(t, err, KindValidation, "mode must be enforce or shadow")
	// Every change sealed one deed per edge; the chain is gapless.
	ds := w.repo.deedsOf(w.org)
	for i, d := range ds {
		if d.Envelope.Body.Seq != int64(i+1) {
			t.Fatalf("seq gap at %d", i)
		}
	}
	// Every change notified.
	if w.repo.org(w.org).notified == 0 || len(w.repo.org(w.org).published) == 0 {
		t.Fatal("no notifications")
	}
}

func TestDefaultAllowNeedsAcknowledgement(t *testing.T) {
	w := newWorld(t)
	src := "deedglass: policy/v1\nname: open-door\ndefaults:\n  decision: allow\n"
	if _, _, err := w.svc.Create(bg, w.who, PushRequest{Source: []byte(src)}); err != nil {
		t.Fatal(err)
	}
	_, err := w.svc.Activate(bg, w.who, "open-door", VersionRef{Number: 1}, ActivateRequest{Mode: ModeEnforce, Reason: "Open it."})
	var e *Error
	if !errors.As(err, &e) || e.Param != "acknowledge_warnings" || e.Message != msgAckDefaultAllow {
		t.Fatalf("%v", err)
	}
	_, err = w.svc.Activate(bg, w.who, "open-door", VersionRef{Number: 1}, ActivateRequest{Mode: ModeEnforce, Reason: "Open it.", AcknowledgeWarnings: []string{"nope"}})
	wantKind(t, err, KindValidation, `This version has no "nope" warning`)
	// Shadow needs no acknowledgement: it decides nothing.
	if _, err := w.svc.Activate(bg, w.who, "open-door", VersionRef{Number: 1}, ActivateRequest{Mode: ModeShadow, Reason: "Watch it."}); err != nil {
		t.Fatal(err)
	}
	a := w.activate("open-door", 1, ModeEnforce, "default_allow")
	o := w.repo.org(w.org)
	var data ActivationData
	_ = json.Unmarshal(o.events[a.Deeds[len(a.Deeds)-1]], &data)
	if len(data.AcknowledgedWarnings) != 1 || data.AcknowledgedWarnings[0] != "default_allow" {
		t.Fatalf("the acknowledgement goes into the deed's data: %+v", data)
	}
}

// Ten activations at once: the org lock serializes them; one enforce at the end, ten rows, a
// gapless chain.
func TestConcurrentActivations(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 10; i++ {
		src := strings.Replace(string(starter(t, "default-deny")), "max_calls: 60", fmt.Sprintf("max_calls: %d", 50+i), 1)
		if i == 0 {
			w.push("default-deny")
			continue
		}
		if _, _, err := w.svc.AddVersion(bg, w.who, "default-deny", PushRequest{Source: []byte(src)}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 1; i <= 10; i++ {
		wg.Go(func() {
			_, err := w.svc.Activate(bg, w.who, "default-deny", VersionRef{Number: i}, ActivateRequest{Mode: ModeEnforce, Reason: "Race it."})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	o := w.repo.org(w.org)
	live := 0
	for _, a := range o.activations {
		if a.DeactivatedAt == nil {
			live++
		}
	}
	if live != 1 || len(o.activations) != 10 || len(w.repo.deedsOf(w.org)) != 10 {
		t.Fatalf("live %d, rows %d, deeds %d", live, len(o.activations), len(w.repo.deedsOf(w.org)))
	}
}

func TestDryRunAndTest(t *testing.T) {
	w := newWorld(t)
	w.push("postgres-safe")
	w.activate("postgres-safe", 1, ModeEnforce)
	agent := ids.New[ids.AgentKind]().String()
	w.repo.org(w.org).agents[agent] = AgentInput{AgentFacts: AgentFacts{ID: agent, Name: "support-bot", Status: StatusActive, Labels: map[string]string{}}}
	in := map[string]any{"tool": "postgres.execute", "args": map[string]any{"sql": "DELETE FROM public.orders"}, "agent": map[string]any{"id": agent},
		"time": "2026-10-04T10:00:00Z"}
	res, err := w.svc.Evaluate(bg, w.who, DryRunRequest{Policy: json.RawMessage(`"active"`), Input: in, Explain: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision != Deny || *res.RuleID != "block-unbounded-writes" || *res.Code != "policy_denied" || res.Policy.Name != "postgres-safe" ||
		res.Policy.Mode != ModeEnforce || *res.Policy.Version != 1 || len(res.Trace) == 0 || !res.Facts.SQL.UnboundedWrite || res.Approval != nil {
		t.Fatalf("%s", MarshalIndent(res))
	}
	// Another org's agent is not found here.
	in["agent"] = map[string]any{"id": ids.New[ids.AgentKind]().String()}
	_, err = w.svc.Evaluate(bg, w.who, DryRunRequest{Input: in})
	wantKind(t, err, KindNotFound, "There's no agent with id")
	// A source dry run, and a bad one.
	src, _ := json.Marshal(map[string]string{"source": string(starter(t, "gmail-drafts-only"))})
	res, err = w.svc.Evaluate(bg, w.who, DryRunRequest{Policy: src, Input: map[string]any{"tool": "gmail.send_email"}})
	if err != nil || res.Decision != Deny || *res.RuleID != "block-sending" || res.Policy.ID != nil {
		t.Fatalf("%v %s", err, MarshalIndent(res))
	}
	_, err = w.svc.Evaluate(bg, w.who, DryRunRequest{Policy: json.RawMessage(`{"source":"nope: 1"}`), Input: map[string]any{"tool": "x.y"}})
	wantKind(t, err, KindInvalid, "")
	_, err = w.svc.Evaluate(bg, w.who, DryRunRequest{Policy: json.RawMessage(`{"id":"postgres-safe","version":1}`), Input: map[string]any{"tool": "x.y", "agnet": 1}})
	wantKind(t, err, KindValidation, `Unknown key "agnet" in the input. Did you mean "agent"?`)
	_, err = w.svc.Evaluate(bg, w.who, DryRunRequest{Policy: json.RawMessage(`"latest"`), Input: map[string]any{"tool": "x.y"}})
	wantKind(t, err, KindValidation, "policy must be")
	// The server test runner.
	cases, _ := policies.Cases("postgres-safe")
	tr, err := w.svc.Test(bg, w.who, TestRequest{PolicyRef: "postgres-safe", Version: VersionRef{Number: 1}, Cases: string(cases)})
	if err != nil || !tr.OK || tr.Passed != 28 || tr.Coverage.Covered != 12 {
		t.Fatalf("%v %+v", err, tr)
	}
	_, err = w.svc.Test(bg, w.who, TestRequest{Source: string(starter(t, "postgres-safe")), Cases: []any{map[string]any{"name": "x", "input": map[string]any{"tool": "a.b"}}}})
	wantKind(t, err, KindValidation, "Case \"x\" has no expect.")
}
