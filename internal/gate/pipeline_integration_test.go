//go:build integration

package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/witness"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// --- helpers ---

func (w *world) admin(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.e.Admin(w.t).Exec(bg, sql, args...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
}

// adminRow scans one row of an admin (superuser) query.
func (w *world) adminRow(sql string, args []any, dest ...any) {
	w.t.Helper()
	if err := w.e.Admin(w.t).QueryRow(bg, sql, args...).Scan(dest...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
}

func refusal(t *testing.T, a *Action, err error, code httpx.Code, status int) *httpx.Error {
	t.Helper()
	he := httpx.AsError(err)
	if he == nil || he.Code != code || he.Status != status {
		t.Fatalf("want %d %s, got %v", status, code, err)
	}
	if a == nil || a.State != string(Sealed) || a.Deed == nil || he.Action == nil {
		t.Fatalf("the refusal isn't recorded and sealed: %+v", a)
	}
	if a.Refusal() != he {
		t.Fatal("the action's refusal isn't the returned error")
	}
	return he
}

func path(evs [][2]string) error {
	if len(evs) == 0 || evs[0][0] != "" || evs[0][1] != string(Received) {
		return fmt.Errorf("first event %v", evs)
	}
	for i := 1; i < len(evs); i++ {
		if evs[i][0] != evs[i-1][1] || !CanTransition(State(evs[i][0]), State(evs[i][1])) {
			return fmt.Errorf("event %d: %v after %v", i, evs[i], evs[i-1])
		}
	}
	return nil
}

func body(t *testing.T, raw []byte) deed.Body {
	t.Helper()
	env, err := deed.ParseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	return env.Body
}

// --- paths ---

func TestAllowSealsL0Verified(t *testing.T) {
	w := newWorld(t, worldOpts{})
	a := w.must(0, req("fake.echo", `{"message":"hello"}`))
	if a.State != "sealed" || a.Status != "executed" || a.HTTPStatus() != 200 || a.Refusal() != nil {
		t.Fatalf("%s", mustJSON(a))
	}
	if a.Witness == nil || a.Witness.Verdict != "VERIFIED" || a.Witness.Level != "L0" || *a.Witness.Method != "reported" ||
		a.Witness.Reasons == nil || len(*a.Witness.Reasons) != 0 {
		t.Fatalf("witness %+v", a.Witness)
	}
	if string(a.Execution.Result) != `{"echo": {"message": "hello"}}` && string(a.Execution.Result) != `{"echo":{"message":"hello"}}` {
		t.Fatalf("result %s", a.Execution.Result)
	}
	if err := path(w.events(a.ID)); err != nil || len(w.events(a.ID)) != 8 {
		t.Fatalf("%v %v", err, w.events(a.ID))
	}
	env := w.deedOf(a.ID)
	b := env.Body
	if b.Policy.Decision != "allow" || b.Execution.Status != "succeeded" || b.Witness.Verdict != "VERIFIED" ||
		b.Witness.Method != "reported" || b.Action.Mode != "gateway" || b.Policy.VersionID == nil || *b.Policy.RuleID != "allow-fake" {
		t.Fatalf("deed body %s", mustJSON(b))
	}
	if w.fake.CallsFor(a.ID) != 1 {
		t.Fatal("the tool didn't run exactly once")
	}
	if w.p.Metrics().ActionsTotal(ModeGateway, "executed") != 1 {
		t.Fatal("deedglass_gate_actions_total")
	}
}

func TestDenyNeverCallsConnector(t *testing.T) {
	w := newWorld(t, worldOpts{})
	a, err := w.submit(0, req("fake.write", `{"rows":301,"table":"orders"}`))
	he := refusal(t, a, err, httpx.CodePolicyDenied, 403)
	if !strings.HasPrefix(he.Message, `Blocked by policy "`) || !strings.Contains(he.Message, "writes over 100 rows need a person") {
		t.Fatalf("message %q", he.Message)
	}
	if a.Status != "denied" || a.Decision.Outcome != "deny" || *a.Decision.RuleID != "no-bulk-writes" || *a.Decision.Source != "rule" {
		t.Fatalf("%s", mustJSON(a))
	}
	if w.fake.Calls() != 0 {
		t.Fatal("a denied action called the connector")
	}
	b := w.deedOf(a.ID).Body
	if b.Policy.Decision != "deny" || b.Execution != nil || b.Witness != nil {
		t.Fatalf("deed %s", mustJSON(b))
	}
	if err := path(w.events(a.ID)); err != nil {
		t.Fatal(err)
	}
	// The envelope carries the sealed action.
	raw, _ := json.Marshal(he.Action)
	if !strings.Contains(string(raw), a.ID) || !strings.Contains(string(raw), `"deed":"deed_`) {
		t.Fatalf("error.action %s", raw)
	}
}

func TestStopSuspendAndOrgStopAreRecorded(t *testing.T) {
	w := newWorld(t, worldOpts{})
	w.admin(`UPDATE deedglass.agents SET status = 'stopped' WHERE id = $1`, w.agents[0].ID)
	w.admin(`UPDATE deedglass.agents SET status = 'suspended' WHERE id = $1`, w.agents[1].ID)
	a, err := w.submit(0, req("fake.echo", `{}`))
	he := refusal(t, a, err, httpx.CodeAgentStopped, 403)
	if *a.Decision.Source != "stop" || !strings.Contains(he.Message, `Agent "agent-a" is stopped`) {
		t.Fatalf("%s %q", mustJSON(a.Decision), he.Message)
	}
	if b := w.deedOf(a.ID).Body; b.Policy.Decision != "deny" || b.Policy.RuleID != nil {
		t.Fatalf("%s", mustJSON(b.Policy))
	}
	a, err = w.submit(1, req("fake.echo", `{}`))
	refusal(t, a, err, httpx.CodeAgentSuspended, 403)
	w.admin(`UPDATE deedglass.orgs SET stopped_at = now() WHERE id = $1`, w.org)
	a, err = w.submit(2, req("fake.echo", `{}`))
	refusal(t, a, err, httpx.CodeOrgStopped, 403)
	if w.fake.Calls() != 0 {
		t.Fatal("a stopped agent's call ran")
	}
}

func TestBudgetExceeded(t *testing.T) {
	w := newWorld(t, worldOpts{policy: strings.Replace(testPolicy, "max_calls: 100000", "max_calls: 2", 1)})
	for range 2 {
		w.must(0, req("fake.echo", `{}`))
	}
	a, err := w.submit(0, req("fake.echo", `{}`))
	he := refusal(t, a, err, httpx.CodeBudgetExceeded, 429)
	if he.Header.Get("Retry-After") == "" || !strings.Contains(he.Message, `Budget "agent-rate" reached`) {
		t.Fatalf("%v %q", he.Header, he.Message)
	}
	if *a.Decision.Source != "budget" || w.fake.Calls() != 2 {
		t.Fatalf("%s calls %d", mustJSON(a.Decision), w.fake.Calls())
	}
	// Another agent has its own budget (per agent).
	w.must(1, req("fake.echo", `{}`))
}

func TestApprovalApprovedRuns(t *testing.T) {
	w := newWorld(t, worldOpts{})
	a := w.must(0, req("fake.write", `{"rows":1,"table":"approvals"}`))
	if a.Status != "pending_approval" || a.HTTPStatus() != 202 || a.NextAction == nil || a.NextAction.Type != "await_approval" ||
		a.Approval == nil || a.Approval.Status != "pending" || w.fake.Calls() != 0 {
		t.Fatalf("%s", mustJSON(a))
	}
	if *a.NextAction.PollURL != "/v1/actions/"+a.ID+"?wait=25" {
		t.Fatalf("poll url %q", *a.NextAction.PollURL)
	}
	// A wait on a pending action waits for its next change (a person's decision).
	waitDone := make(chan error, 1)
	go func() { waitDone <- w.p.Wait(bg, w.org, a.ID, w.agents[0].ID.String(), 10*time.Second) }()
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-waitDone:
		t.Fatalf("the wait returned before anyone decided: %v", err)
	default:
	}
	err := w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		by := w.owner.String()
		return store.DecideApproval(ctx, tx, w.org, a.Approval.ID, "approved", &by, nil, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := w.p.Resume(bg, w.org, mustAction(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "sealed" || r.Status != "executed" || w.fake.CallsFor(a.ID) != 1 {
		t.Fatalf("%s", mustJSON(r))
	}
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait didn't end when the action changed")
	}
	b := w.deedOf(a.ID).Body
	if b.Approval == nil || b.Approval.Outcome != "approved" || len(b.Approval.DecidedBy) != 1 || b.Approval.DecidedBy[0] != w.owner.String() ||
		b.Policy.Decision != "require_approval" || b.Execution.Status != "succeeded" {
		t.Fatalf("%s", mustJSON(b))
	}
	if err := path(w.events(a.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalStoppedAgentNeverRuns(t *testing.T) {
	w := newWorld(t, worldOpts{})
	a := w.must(0, req("fake.write", `{"rows":1,"table":"approvals"}`))
	_ = w.e.DB.OrgTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		by := w.owner.String()
		return store.DecideApproval(ctx, tx, w.org, a.Approval.ID, "approved", &by, nil, time.Now())
	})
	w.admin(`UPDATE deedglass.agents SET status = 'stopped' WHERE id = $1`, w.agents[0].ID)
	r, err := w.p.Resume(bg, w.org, mustAction(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "denied" || r.State != "sealed" || *r.Decision.Code != "agent_stopped" || w.fake.Calls() != 0 {
		t.Fatalf("%s", mustJSON(r))
	}
}

func TestCancelPending(t *testing.T) {
	w := newWorld(t, worldOpts{})
	a := w.must(0, req("fake.write", `{"rows":1,"table":"approvals"}`))
	// Another agent can't see it.
	if _, err := w.p.Cancel(bg, w.pr(1), a.ID); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeNotFound {
		t.Fatalf("other agent: %v", err)
	}
	c, err := w.p.Cancel(bg, w.pr(0), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "cancelled" || c.State != "sealed" || c.Approval.Status != "cancelled" {
		t.Fatalf("%s", mustJSON(c))
	}
	if b := w.deedOf(a.ID).Body; b.Approval == nil || b.Approval.Outcome != "cancelled" || b.Execution != nil {
		t.Fatalf("%s", mustJSON(b))
	}
	_, err = w.p.Cancel(bg, w.pr(0), a.ID)
	if he := httpx.AsError(err); he == nil || he.Code != httpx.CodeConflict || he.Message != MsgCancelDone {
		t.Fatalf("second cancel: %v", err)
	}
	e := w.must(0, req("fake.echo", `{}`))
	_, err = w.p.Cancel(bg, w.pr(0), e.ID)
	if he := httpx.AsError(err); he == nil || he.Code != httpx.CodeConflict {
		t.Fatalf("cancel a sealed action: %v", err)
	}
	// A person (owner session) may cancel any agent's pending action.
	b := w.must(1, req("fake.write", `{"rows":1,"table":"approvals"}`))
	if _, err := w.p.Cancel(bg, principal{Kind: httpx.KindUser, OrgID: w.org, UserID: w.owner}, b.ID); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalExpiresAndReleases(t *testing.T) {
	w := newWorld(t, worldOpts{policy: strings.Replace(testPolicy, "max_calls: 100000", "max_calls: 1", 1)})
	a := w.must(0, req("fake.write", `{"rows":1,"table":"approvals"}`))
	// The reservation holds the budget while it waits.
	if _, err := w.submit(0, req("fake.echo", `{}`)); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeBudgetExceeded {
		t.Fatalf("budget not held: %v", err)
	}
	// The approvals guard (S22, DG006) keeps an approval's request, expiry included, immutable; as a
	// superuser with triggers off for this one transaction, the test moves the expiry into the past
	// so the sweeper (which compares with the database's now()) ends it.
	tx, err := w.e.Admin(t).Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(bg, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(bg, `UPDATE deedglass.approvals SET expires_at = now() - interval '1 second' WHERE id = $1`, a.Approval.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	r := w.p.Sweep(bg)
	if r.Expired != 1 || r.Errors != 0 {
		t.Fatalf("%+v", r)
	}
	v, err := w.p.Get(bg, w.pr(0), a.ID, 0)
	if err != nil || v.Status != "expired" || v.State != "sealed" || v.Approval.Status != "expired" {
		t.Fatalf("%v %s", err, mustJSON(v))
	}
	// Released: the budget has room again.
	w.must(0, req("fake.echo", `{}`))
}

func TestFailedToolError(t *testing.T) {
	w := newWorld(t, worldOpts{})
	a := w.must(0, req("fake.fail", `{"_fake":{"code":"card_declined","message":"The card was declined. Nothing was charged."}}`))
	if a.Status != "failed" || a.State != "sealed" || *a.Execution.Status != "failed" || *a.Execution.ErrorCode != "card_declined" ||
		a.Witness != nil || *a.Execution.ErrorMessage != "The card was declined. Nothing was charged." {
		t.Fatalf("%s", mustJSON(a))
	}
	if b := w.deedOf(a.ID).Body; b.Execution.Status != "failed" || *b.Execution.ErrorCode != "card_declined" || b.Witness != nil {
		t.Fatalf("%s", mustJSON(b))
	}
	// A tool that refused its arguments never reached the upstream.
	v := w.must(0, req("fake.write", `{"rows":1,"extra":true}`))
	if v.Status != "failed" || *v.Execution.ErrorCode != "validation_failed" {
		t.Fatalf("%s", mustJSON(v))
	}
}

func TestTimeoutIsUnverifiable(t *testing.T) {
	w := newWorld(t, worldOpts{cfg: Config{ExecuteTimeout: 50 * time.Millisecond}})
	a := w.must(0, req("fake.slow", `{"_fake":{"latency_ms":5000}}`))
	if a.Status != "executed" || a.State != "sealed" || *a.Execution.Status != "unknown" || *a.Execution.ErrorCode != "timeout" ||
		a.Witness.Verdict != "UNVERIFIABLE" || a.Witness.Cause() != ReasonTimeout {
		t.Fatalf("%s", mustJSON(a))
	}
	if UnknownOutcomeMessage(a, 30*time.Second) != "The tool did not answer within 30 s. It may have run." {
		t.Fatal(UnknownOutcomeMessage(a, 30*time.Second))
	}
	b := w.deedOf(a.ID).Body
	if b.Execution.Status != "unknown" || b.Witness.Verdict != "UNVERIFIABLE" {
		t.Fatalf("%s", mustJSON(b))
	}
}

func TestPanicIsUnknown(t *testing.T) {
	w := newWorld(t, worldOpts{})
	a := w.must(0, req("fake.echo", `{"_fake":{"panic":true}}`))
	if *a.Execution.Status != "unknown" || *a.Execution.ErrorCode != "internal_error" || a.Witness.Verdict != "UNVERIFIABLE" {
		t.Fatalf("%s", mustJSON(a))
	}
}

func TestWaitZeroAnswersProcessing(t *testing.T) {
	w := newWorld(t, worldOpts{})
	r := req("fake.slow", `{"_fake":{"latency_ms":300}}`)
	r.Wait = 0
	a := w.must(0, r)
	if a.Status != "processing" || a.HTTPStatus() != 202 || a.State != "executing" {
		t.Fatalf("%s", mustJSON(a))
	}
	if err := w.p.Wait(bg, w.org, a.ID, "", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	v, _ := w.p.Get(bg, w.pr(0), a.ID, 0)
	if v.State != "sealed" || v.Status != "executed" {
		t.Fatalf("%s", mustJSON(v))
	}
}

// --- tool names ---

func TestToolUnknownIsRecorded(t *testing.T) {
	w := newWorld(t, worldOpts{})
	for _, tool := range []string{"nope.echo", "fake.nope", "fаke.echo"} {
		a, err := w.submit(0, req(tool, `{}`))
		he := refusal(t, a, err, httpx.CodeToolUnknown, 404)
		if *a.Decision.Code != "tool_unknown" || *a.Decision.Source != "builtin" || *a.Decision.RuleID != RuleToolUnknown ||
			he.Message != ToolUnknownMessage(tool) {
			t.Fatalf("%q: %s %q", tool, mustJSON(a.Decision), he.Message)
		}
		w.deedOf(a.ID)
	}
	for _, tool := range []string{"Fake.echo", "fake", ".echo", strings.Repeat("a", 300)} {
		_, err := w.submit(0, req(tool, `{}`))
		if he := httpx.AsError(err); he == nil || he.Code != httpx.CodeValidationFailed || he.Param != "tool" {
			t.Fatalf("%q: %v", tool, err)
		}
	}
	if w.fake.Calls() != 0 {
		t.Fatal("an unknown tool ran")
	}
}

// --- proxy mode ---

func proxyReq(tool, args string) ActionRequest {
	r := req(tool, args)
	r.Mode = ModeProxy
	return r
}

func TestProxyRoundTrip(t *testing.T) {
	w := newWorld(t, worldOpts{})
	a := w.must(0, proxyReq("scratch.readTextFile", `{"path":"/tmp/x"}`))
	if a.Status != "authorized" || a.State != "executing" || a.NextAction == nil || a.NextAction.Type != "execute_at_edge" ||
		*a.NextAction.ReportURL != "/v1/actions/"+a.ID+"/result" || a.NextAction.Deadline == nil {
		t.Fatalf("%s", mustJSON(a))
	}
	now := time.Now()
	res := EdgeResult{Status: ExecSucceeded, StartedAt: now.Add(-40 * time.Millisecond), FinishedAt: now,
		Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}],"isError":false}`)}
	// Another agent: 404, so ids can't be probed.
	if _, err := w.p.ReportResult(bg, w.pr(1), a.ID, res); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeNotFound {
		t.Fatalf("other agent: %v", err)
	}
	// A digest that doesn't match the result.
	bad := res
	bad.ResultDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := w.p.ReportResult(bg, w.pr(0), a.ID, bad); httpx.AsError(err) == nil || httpx.AsError(err).Param != "result_digest" {
		t.Fatalf("digest mismatch: %v", err)
	}
	r, err := w.p.ReportResult(bg, w.pr(0), a.ID, res)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "sealed" || r.Status != "executed" || *r.Witness.Method != MethodEdgeReported || *r.Execution.LatencyMS != 40 {
		t.Fatalf("%s", mustJSON(r))
	}
	if _, err := w.p.ReportResult(bg, w.pr(0), a.ID, res); httpx.AsError(err) == nil || httpx.AsError(err).Message != MsgResultReported {
		t.Fatalf("double report: %v", err)
	}
	b := w.deedOf(a.ID).Body
	if b.Action.Mode != "proxy" || b.Witness.Method != "edge_reported" || b.Execution.LatencyMS != 40 {
		t.Fatalf("%s", mustJSON(b))
	}
}

func TestProxyEdgeLost(t *testing.T) {
	w := newWorld(t, worldOpts{})
	a := w.must(0, proxyReq("scratch.write", `{}`))
	w.admin(`UPDATE deedglass.actions SET executing_deadline = now() - interval '1 second' WHERE id = $1`, a.ID)
	if r := w.p.Sweep(bg); r.CrashRecovered != 1 {
		t.Fatalf("%+v", r)
	}
	v, _ := w.p.Get(bg, w.pr(0), a.ID, 0)
	if v.State != "sealed" || *v.Execution.Status != "unknown" || *v.Execution.ErrorCode != ReasonEdgeLost || v.Witness.Verdict != "UNVERIFIABLE" {
		t.Fatalf("%s", mustJSON(v))
	}
	now := time.Now()
	_, err := w.p.ReportResult(bg, w.pr(0), a.ID, EdgeResult{Status: ExecSucceeded, StartedAt: now, FinishedAt: now, Result: json.RawMessage(`{}`)})
	if he := httpx.AsError(err); he == nil || he.Code != httpx.CodeConflict || he.Message != MsgResultLate {
		t.Fatalf("late report: %v", err)
	}
}

func TestProxyPostgresFacts(t *testing.T) {
	w := newWorld(t, worldOpts{})
	r := proxyReq("pg.query", `{"sql":"DROP TABLE refunds"}`)
	r.ConnectorType = "postgres"
	a, err := w.submit(0, r)
	refusal(t, a, err, httpx.CodePolicyDenied, 403)
	var detail []byte
	_ = w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT decision_detail FROM deedglass.actions WHERE id = $1`, a.ID).Scan(&detail)
	})
	if !strings.Contains(string(detail), `"kind": "drop"`) || !strings.Contains(string(detail), `"category": "ddl"`) {
		t.Fatalf("facts %s", detail)
	}
	// Without the declared type, the edge is mcp and the SQL isn't classified.
	a = w.must(0, proxyReq("pg.query", `{"sql":"DROP TABLE refunds"}`))
	if a.Status != "authorized" {
		t.Fatalf("%s", mustJSON(a))
	}
	// Gateway calls can't claim a connector type.
	g := req("fake.echo", `{}`)
	g.ConnectorType = "postgres"
	if _, err := w.submit(0, g); httpx.AsError(err) == nil || httpx.AsError(err).Param != "connector_type" {
		t.Fatalf("%v", err)
	}
}

// --- idempotency ---

func TestIdempotency(t *testing.T) {
	w := newWorld(t, worldOpts{})
	r := req("fake.echo", `{"message":"hello"}`)
	r.IdempotencyKey = "demo-1"
	a := w.must(0, r)
	b := w.must(0, r)
	if !b.Replayed() || a.Replayed() || mustJSON(a.Action) != mustJSON(b.Action) || w.fake.CallsFor(a.ID) != 1 {
		t.Fatalf("replay:\n%s\n%s", mustJSON(a.Action), mustJSON(b.Action))
	}
	// Keys reordered: the same request.
	r2 := r
	r2.Args = json.RawMessage(`{ "message" : "hello" }`)
	if c := w.must(0, r2); c.ID != a.ID {
		t.Fatal("the same request with whitespace is a new action")
	}
	// Different args: 422.
	r3 := r
	r3.Args = json.RawMessage(`{"message":"bye"}`)
	_, err := w.submit(0, r3)
	if he := httpx.AsError(err); he == nil || he.Code != httpx.CodeIdempotencyConflict || he.Status != 422 || he.Message != MsgIdempotencyConflict {
		t.Fatalf("%v", err)
	}
	// A different session: a different request.
	r4 := r
	r4.Session = "chat_2"
	if _, err := w.submit(0, r4); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeIdempotencyConflict {
		t.Fatalf("%v", err)
	}
	// Another agent: independent.
	if c := w.must(1, r); c.ID == a.ID || c.Replayed() {
		t.Fatal("keys are per agent")
	}
	// A denial replays as a denial.
	d := req("fake.write", `{"rows":301}`)
	d.IdempotencyKey = "deny-1"
	a1, err1 := w.submit(0, d)
	a2, err2 := w.submit(0, d)
	if httpx.AsError(err1) == nil || httpx.AsError(err2) == nil || a1.ID != a2.ID || !a2.Replayed() || httpx.AsError(err2).Code != httpx.CodePolicyDenied {
		t.Fatalf("%v %v", err1, err2)
	}
	// After 24 hours the key is free.
	w.admin(`UPDATE deedglass.actions SET created_at = now() - interval '25 hours' WHERE id = $1`, a.ID)
	if c := w.must(0, r); c.ID == a.ID || c.Replayed() {
		t.Fatal("an expired key replayed")
	}
}

func TestIdempotencyInFlight(t *testing.T) {
	w := newWorld(t, worldOpts{})
	r := req("fake.slow", `{"_fake":{"latency_ms":400}}`)
	r.IdempotencyKey = "slow-1"
	var wg sync.WaitGroup
	out := make([]*Action, 5)
	for i := range out {
		wg.Go(func() {
			if i > 0 {
				time.Sleep(time.Duration(i) * 30 * time.Millisecond)
			}
			a, err := w.submit(0, r)
			if err != nil {
				t.Error(err)
				return
			}
			out[i] = a
		})
	}
	wg.Wait()
	for _, a := range out {
		if a == nil || a.ID != out[0].ID || a.State != "sealed" {
			t.Fatalf("%v", a)
		}
	}
	if w.fake.Calls() != 1 {
		t.Fatalf("the tool ran %d times", w.fake.Calls())
	}
}

func TestKeyReleaseSweeper(t *testing.T) {
	w := newWorld(t, worldOpts{})
	r := req("fake.echo", `{}`)
	r.IdempotencyKey = "old"
	a := w.must(0, r)
	w.admin(`UPDATE deedglass.actions SET created_at = now() - interval '25 hours' WHERE id = $1`, a.ID)
	if res := w.p.Sweep(bg); res.KeysReleased != 1 {
		t.Fatalf("%+v", res)
	}
	var key *string
	_ = w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT idempotency_key FROM deedglass.actions WHERE id = $1`, a.ID).Scan(&key)
	})
	if key != nil {
		t.Fatal("the key wasn't released")
	}
}

// --- intents and on-behalf-of ---

func TestIntentModes(t *testing.T) {
	w := newWorld(t, worldOpts{})
	priv, pub, kid, err := deedglass.GenerateIntentKey()
	if err != nil {
		t.Fatal(err)
	}
	keys := fmt.Sprintf(`[{"kid":%q,"public_key":%q,"created_at":"2026-10-01T00:00:00Z","created_by":"operator"}]`, kid, pub)
	w.admin(`UPDATE deedglass.agents SET intent_mode = 'optional', intent_keys = $2::jsonb WHERE id = $1`, w.agents[0].ID, keys)
	signer, err := deedglass.LoadIntentSigner(priv, w.org.String(), w.agents[0].ID.String())
	if err != nil {
		t.Fatal(err)
	}
	// optional, unsigned: fine.
	w.must(0, req("fake.echo", `{}`))
	// optional, signed: recorded with its hash, the intent's on-behalf-of wins.
	args := json.RawMessage(`{"message":"signed"}`)
	h, hash, err := signer.Sign("fake.echo", args, deedglass.SignOptions{OnBehalfOf: "u_4821"})
	if err != nil {
		t.Fatal(err)
	}
	r := req("fake.echo", string(args))
	r.Intent = h
	a := w.must(0, r)
	b := w.deedOf(a.ID).Body
	if !b.Action.IntentSigned || *b.Action.IntentHash != hash || *b.OnBehalfOf != "u_4821" || *a.OnBehalfOf != "u_4821" {
		t.Fatalf("%s", mustJSON(b))
	}
	// The same intent again: intent_replay, and nothing is recorded.
	var before int
	_ = w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.actions`).Scan(&before)
	})
	if _, err := w.submit(0, r); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeIntentReplay {
		t.Fatalf("replay: %v", err)
	}
	// required: an unsigned call is refused, and an MCP call is refused with the MCP sentence.
	w.admin(`UPDATE deedglass.agents SET intent_mode = 'required' WHERE id = $1`, w.agents[0].ID)
	if _, err := w.submit(0, req("fake.echo", `{}`)); httpx.AsError(err) == nil || httpx.AsError(err).Code != httpx.CodeIntentRequired {
		t.Fatalf("required: %v", err)
	}
	h2, _, _ := signer.Sign("fake.echo", args, deedglass.SignOptions{})
	m := req("fake.echo", string(args))
	m.Intent, m.Context = h2, &RequestContext{Transport: TransportMCPStdio}
	_, err = w.submit(0, m)
	if he := httpx.AsError(err); he == nil || he.Code != httpx.CodeIntentRequired || !strings.Contains(he.Message, "MCP clients can't sign intents yet") {
		t.Fatalf("mcp: %v", err)
	}
	var after int
	_ = w.e.DB.OrgReadTx(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.actions`).Scan(&after)
	})
	if after != before {
		t.Fatalf("identity failures were recorded: %d → %d", before, after)
	}
	// required, signed, REST: runs.
	r.Intent = h2
	if a := w.must(0, r); a.Status != "executed" {
		t.Fatalf("%s", mustJSON(a))
	}
}

func TestOnBehalfOfAndSession(t *testing.T) {
	w := newWorld(t, worldOpts{})
	r := req("fake.echo", `{}`)
	r.OnBehalfOf, r.Session = "u_7", "chat_77"
	a := w.must(0, r)
	b := w.deedOf(a.ID).Body
	if *a.OnBehalfOf != "u_7" || *a.Session != "chat_77" || *b.OnBehalfOf != "u_7" || *b.Session != "chat_77" {
		t.Fatalf("%s", mustJSON(b))
	}
}

// --- shadow, events, invariants ---

func TestShadowIsRecorded(t *testing.T) {
	w := newWorld(t, worldOpts{})
	caller := policyCaller(w)
	shadowSrc := strings.Replace(strings.Replace(testPolicy, "name: gate-test", "name: gate-shadow", 1), "id: allow-fake", "id: allow-fake-shadow", 1)
	pol, ver, err := w.svc.Create(bg, caller, pushReq(shadowSrc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Activate(bg, caller, pol.ID, versionRef(ver.Version), activateShadow()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		act, _ := w.reg.Active(bg, w.org.String())
		return act != nil && act.Shadow != nil
	})
	a := w.must(0, req("fake.echo", `{}`))
	if a.Decision.Shadow == nil || a.Decision.Shadow.Outcome != "allow" || *a.Decision.Shadow.RuleID != "allow-fake-shadow" ||
		*a.Decision.Shadow.PolicyVersion != ver.ID {
		t.Fatalf("%s", mustJSON(a.Decision))
	}
}

func TestEventsInOrder(t *testing.T) {
	w := newWorld(t, worldOpts{})
	c := w.e.App(t)
	if _, err := c.Exec(bg, `LISTEN deedglass_events`); err != nil {
		t.Fatal(err)
	}
	a := w.must(0, req("fake.echo", `{}`))
	var types []string
	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	for len(types) < 5 {
		n, err := c.WaitForNotification(ctx)
		if err != nil {
			t.Fatalf("%v after %v", err, types)
		}
		var ev ledger.Event
		if err := json.Unmarshal([]byte(n.Payload), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Action != nil && *ev.Action == a.ID {
			types = append(types, ev.Type)
		}
	}
	want := []string{"action.created", "action.decided", "action.executed", "action.witnessed", "deed.sealed"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("%v", types)
	}
}

func TestNoTransactionWhileExecuting(t *testing.T) {
	w := newWorld(t, worldOpts{})
	var acquired int32 = -1
	w.p.hooks.beforeExecute = func(connector.Call) { acquired = w.e.DB.PoolForTest().Stat().AcquiredConns() }
	w.must(0, req("fake.echo", `{}`))
	if acquired != 0 {
		t.Fatalf("%d pooled connections were held while the tool ran", acquired)
	}
}

func TestCrashBetweenTxAAndTxB(t *testing.T) {
	w := newWorld(t, worldOpts{})
	// Admit an action, then "die" before running it: the plan is dropped.
	pr := w.pr(0)
	n, err := Normalize(req("fake.echo", `{}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	n.ConnectorType = "fake"
	act, _ := w.reg.Active(bg, w.org.String())
	ex, spec, _, _ := connector.NewRegistry(w.fake).Resolve(bg, w.org, "fake.echo")
	adm, err := w.p.admit(bg, pr, n, req("fake.echo", `{}`), act, anomalyIn{}, spec, ex, false, nil, nil)
	if err != nil || adm.plan == nil {
		t.Fatalf("%v %+v", err, adm)
	}
	id := adm.view.ID
	if r := w.p.Sweep(bg); r.CrashRecovered != 0 {
		t.Fatal("an action inside its deadline was recovered")
	}
	w.admin(`UPDATE deedglass.actions SET executing_deadline = now() - interval '1 second' WHERE id = $1`, id)
	if r := w.p.Sweep(bg); r.CrashRecovered != 1 {
		t.Fatalf("%+v", r)
	}
	b := w.deedOf(id).Body
	if b.Execution.Status != "unknown" || b.Witness.Verdict != "UNVERIFIABLE" || *b.Execution.ErrorCode != "timeout" {
		t.Fatalf("%s", mustJSON(b))
	}
	if w.fake.Calls() != 0 {
		t.Fatal("recovery ran the tool")
	}
	// A crash between the outcome and the seal: the action is verified but unsealed, and Resume
	// (the unsealed sweeper) seals it.
	adm2, err := w.p.admit(bg, pr, n, req("fake.echo", `{}`), act, anomalyIn{}, spec, ex, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := w.p.execute(bg, ex, adm2.plan.call)
	err = w.led.OrgTxRetry(bg, w.org, func(ctx context.Context, tx pgx.Tx) error {
		wc := w.p.newWitnessCase(adm2.plan.subject, e, adm2.plan.exec, adm2.plan.call, witness.L0, "", "")
		_, err := w.p.recordExecution(ctx, tx, adm2.plan.subject, e, wc, nil, nil, "system", "", false)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	v, _ := w.p.Get(bg, pr, adm2.view.ID, 0)
	if v.State != "verified" || v.Deed != nil {
		t.Fatalf("%s", mustJSON(v))
	}
	r, err := w.p.Resume(bg, w.org, adm2.plan.subject.ID)
	if err != nil || r.State != "sealed" || r.Deed == nil {
		t.Fatalf("%v %s", err, mustJSON(r))
	}
	w.deedOf(r.ID)
}

// policy helpers for the shadow test.
func policyCaller(w *world) policy.Caller {
	return policy.Caller{Org: w.org.String(), Actor: deed.Ref{Type: "user", ID: w.owner.String()}}
}

func pushReq(src string) policy.PushRequest { return policy.PushRequest{Source: []byte(src)} }
func versionRef(n int) policy.VersionRef    { return policy.VersionRef{Number: n} }
func activateShadow() policy.ActivateRequest {
	return policy.ActivateRequest{Mode: policy.ModeShadow, Reason: "Watch first"}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
