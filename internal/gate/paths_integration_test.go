//go:build integration

package gate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness"
)

// The remaining Resume paths, the unsealed sweeper, budget replays and edge-report validation.
func TestMorePaths(t *testing.T) {
	w := newWorld(t, worldOpts{policy: strings.Replace(testPolicy, "max_calls: 100000", "max_calls: 3", 1)})
	if w.p.Config().ExecuteTimeout != 30*time.Second {
		t.Fatal(w.p.Config())
	}
	// A person denies a pending approval: denied → sealed, the deed names the approval, nothing ran.
	a := w.must(0, req("fake.write", `{"rows":1,"table":"approvals"}`))
	_ = w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		by := w.owner.String()
		return store.DecideApproval(ctx, tx, w.org, a.Approval.ID, "denied", &by, nil)
	})
	r, err := w.p.Resume(bg, w.org, mustAction(a.ID))
	if err != nil || r.Status != "denied" || r.State != "sealed" || w.fake.Calls() != 0 {
		t.Fatalf("%v %s", err, mustJSON(r))
	}
	if b := w.deedOf(a.ID).Body; b.Approval.Outcome != "denied" || b.Policy.Decision != "require_approval" {
		t.Fatalf("%s", mustJSON(b))
	}
	// Resume on a sealed action changes nothing.
	if r2, err := w.p.Resume(bg, w.org, mustAction(a.ID)); err != nil || r2.State != "sealed" {
		t.Fatal(err)
	}
	// An org admin key cancels another agent's pending action.
	c := w.must(1, req("fake.write", `{"rows":1,"table":"approvals"}`))
	adminKey := principal{Kind: httpx.KindAdminKey, OrgID: w.org, KeyID: ids.New[ids.APIKeyKind]()}
	if x, err := w.p.Cancel(bg, adminKey, c.ID); err != nil || x.Status != "cancelled" {
		t.Fatalf("%v", err)
	}
	if _, err := w.p.Cancel(bg, adminKey, "act_nope"); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeNotFound {
		t.Fatalf("%v", err)
	}
	// Another agent can't read it, wait on it or see it.
	if _, err := w.p.Get(bg, w.pr(2), a.ID, 0); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeNotFound {
		t.Fatalf("%v", err)
	}
	if _, err := w.p.Get(bg, w.pr(2), a.ID, time.Second); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeNotFound {
		t.Fatalf("%v", err)
	}
	if v, err := w.p.Get(bg, principal{Kind: httpx.KindUser, OrgID: w.org, UserID: w.owner}, a.ID, time.Second); err != nil || v.ID != a.ID {
		t.Fatalf("%v", err)
	}
	// The budget (3 a minute for agent 2): the third is refused, and its replay is the same refusal
	// with a fresh Retry-After.
	w.must(2, req("fake.echo", `{}`))
	w.must(2, req("fake.echo", `{}`))
	w.must(2, req("fake.echo", `{}`))
	br := req("fake.echo", `{"n":4}`)
	br.IdempotencyKey = "budget-1"
	b1, err1 := w.submit(2, br)
	b2, err2 := w.submit(2, br)
	he1, he2 := httpx.AsError(err1), httpx.AsError(err2)
	if he1 == nil || he2 == nil || he2.Code != httpx.CodeBudgetExceeded || he2.Header.Get("Retry-After") == "" || b1.ID != b2.ID || !b2.Replayed() {
		t.Fatalf("%v %v", err1, err2)
	}
	// Verified but unsealed (a seal failure): the unsealed sweeper seals it after 30 s idle.
	n, _ := Normalize(req("fake.echo", `{}`), 0)
	n.ConnectorType = "fake"
	act, _ := w.reg.Active(bg, w.org.String())
	ex, spec, _, _ := w.p.o.Resolver.Resolve(bg, w.org, "fake.echo")
	adm, err := w.p.admit(bg, w.pr(0), n, req("fake.echo", `{}`), act, spec, ex, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := w.p.execute(bg, ex, adm.plan.call)
	_ = w.led.OrgTxRetry(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		wc := w.p.newWitnessCase(adm.plan.subject, e, adm.plan.exec, adm.plan.call, witness.L0, "", "")
		_, err := w.p.recordExecution(ctx, tx, adm.plan.subject, e, wc, nil, nil, "system", "", false)
		return err
	})
	w.admin(`ALTER TABLE deedglass.actions DISABLE TRIGGER actions_touch`)
	w.admin(`UPDATE deedglass.actions SET updated_at = now() - interval '1 minute' WHERE id = $1`, adm.view.ID)
	w.admin(`ALTER TABLE deedglass.actions ENABLE TRIGGER actions_touch`)
	if res := w.p.Sweep(bg); res.Sealed != 1 {
		t.Fatalf("%+v", res)
	}
	w.deedOf(adm.view.ID)
	// An action left in executed (a witness that crashed): Resume witnesses and seals it.
	adm2, err := w.p.admit(bg, w.pr(1), n, req("fake.echo", `{}`), act, spec, ex, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := "succeeded"
	_ = w.led.OrgTxRetry(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return w.led.Transition(ctx, tx, adm2.view.ID, Executing, Executed, ledgerEvent(), ledgerPatch(&status))
	})
	if r, err := w.p.Resume(bg, w.org, adm2.plan.subject.ID); err != nil || r.State != "sealed" || r.Witness.Verdict != "VERIFIED" {
		t.Fatalf("%v %s", err, mustJSON(r))
	}
	// Edge-report validation.
	x := w.must(0, proxyReq("scratch.x", `{}`))
	now := time.Now()
	for _, bad := range []EdgeResult{
		{Status: "done", StartedAt: now, FinishedAt: now},
		{Status: ExecSucceeded},
		{Status: ExecSucceeded, StartedAt: now, FinishedAt: now.Add(-time.Second)},
		{Status: ExecSucceeded, StartedAt: now, FinishedAt: now, ErrorCode: "Bad Code"},
		{Status: ExecSucceeded, StartedAt: now, FinishedAt: now},
		{Status: ExecSucceeded, StartedAt: now, FinishedAt: now, Result: json.RawMessage(`{x`)},
		{Status: ExecSucceeded, StartedAt: now, FinishedAt: now, ResultDigest: "md5:x"},
		{Status: ExecSucceeded, StartedAt: now.Add(-time.Hour), FinishedAt: now, ResultDigest: "sha256:" + strings.Repeat("a", 64)},
	} {
		if _, err := w.p.ReportResult(bg, w.pr(0), x.ID, bad); httpx.AsError(err) == nil || httpx.AsError(err).Status != 400 {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, err := w.p.ReportResult(bg, w.pr(0), a.ID, EdgeResult{Status: ExecSucceeded, StartedAt: now, FinishedAt: now, ResultDigest: "sha256:" + strings.Repeat("a", 64)}); httpx.AsError(err) == nil || httpx.AsError(err).Message != MsgResultNotProxy {
		t.Fatalf("%v", err)
	}
	pend := w.must(0, proxyReq("fake.write", `{"rows":1,"table":"approvals"}`))
	if _, err := w.p.ReportResult(bg, w.pr(0), pend.ID, EdgeResult{Status: ExecFailed, StartedAt: now, FinishedAt: now}); httpx.AsError(err) == nil || httpx.AsError(err).Message != MsgResultNotAuthorized {
		t.Fatalf("%v", err)
	}
	// A proxy action approved: authorized at the edge with a fresh deadline.
	_ = w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		by := w.owner.String()
		return store.DecideApproval(ctx, tx, w.org, pend.Approval.ID, "approved", &by, nil)
	})
	if r, err := w.p.Resume(bg, w.org, mustAction(pend.ID)); err != nil || r.Status != "authorized" || r.NextAction == nil {
		t.Fatalf("%v %s", err, mustJSON(r))
	}
	if r, err := w.p.ReportResult(bg, w.pr(0), pend.ID, EdgeResult{Status: ExecFailed, StartedAt: now, FinishedAt: now, ErrorCode: "edge_refused"}); err != nil || r.Status != "failed" {
		t.Fatalf("%v", err)
	}
}

func ledgerEvent() ledger.EventData { return ledger.EventData{} }

func ledgerPatch(status *string) ledger.Patch {
	now := time.Now()
	return ledger.Patch{ExecutionStatus: status, StartedAt: &now, FinishedAt: &now}
}
