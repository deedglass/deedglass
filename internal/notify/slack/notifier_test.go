package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/notify"
	"github.com/deedglass/deedglass/internal/store"
)

func newNotifier(st *fakeStore, api *fakeAPI) *Notifier {
	return &Notifier{Store: st, Conns: fakeConns{}, API: api, BaseURL: "https://deedglass.example.test/", Now: func() time.Time { return t0 },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func notice() notify.ApprovalNotice {
	return notify.ApprovalNotice{OrgID: orgA, ApprovalID: aprID, ActionID: actID}
}

func TestNotifierPostsOnce(t *testing.T) {
	st, api := newFakeStore(), newFakeAPI()
	n := newNotifier(st, api)
	if n.Name() != "slack" {
		t.Fatal(n.Name())
	}
	if err := n.ApprovalRequested(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	if api.posts != 1 || len(st.inserted) != 1 {
		t.Fatalf("posts %d inserted %d", api.posts, len(st.inserted))
	}
	m := st.inserted[0]
	if m.ChannelID != "C0APPROVALS" || m.TS != "1760069640.000100" || m.Version != StatePending.Rank() || m.ConnectorID != conA || m.ActionID != actID {
		t.Fatalf("%+v", m)
	}
	// Again (a retried notify job): the card is edited, not posted twice.
	if err := n.ApprovalRequested(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	if api.posts != 1 {
		t.Fatalf("posted %d times", api.posts)
	}
}

func TestNotifierSkips(t *testing.T) {
	// The rule didn't ask for Slack.
	st, api := newFakeStore(), newFakeAPI()
	st.card.Notify = []string{"email"}
	n := newNotifier(st, api)
	if err := n.ApprovalRequested(context.Background(), notice()); err != nil || api.posts != 0 {
		t.Fatalf("err %v posts %d", err, api.posts)
	}
	// No Slack connector with approvals on.
	st, api = newFakeStore(), newFakeAPI()
	st.has = false
	n = newNotifier(st, api)
	if err := n.ApprovalRequested(context.Background(), notice()); err != nil || api.posts != 0 {
		t.Fatalf("err %v posts %d", err, api.posts)
	}
	// Decided before the card could be posted: nothing to ask for.
	for _, status := range []string{"approved", "denied", "expired", "cancelled"} {
		st, api = newFakeStore(), newFakeAPI()
		st.card = cardRow(status, nil)
		n = newNotifier(st, api)
		if err := n.UpdateJob(context.Background(), orgA, ids.MustParse[ids.ActionKind](actID), 1); err != nil || api.posts != 0 {
			t.Fatalf("%s: err %v posts %d", status, err, api.posts)
		}
	}
	// The approval is gone.
	st, api = newFakeStore(), newFakeAPI()
	st.cardErr = store.ErrNotFound
	if err := newNotifier(st, api).ApprovalRequested(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	// Another action has no approval.
	if err := newNotifier(st, api).UpdateJob(context.Background(), orgA, ids.New[ids.ActionKind](), 1); err != nil {
		t.Fatal(err)
	}
}

func TestNotifierPostFailureRetries(t *testing.T) {
	st, api := newFakeStore(), newFakeAPI()
	api.postErr = &connector.Error{Kind: connector.KindRateLimited, Code: connector.CodeUpstreamRateLimited, AgentMessage: "Slack is limiting requests."}
	n := newNotifier(st, api)
	if err := n.ApprovalRequested(context.Background(), notice()); err == nil {
		t.Fatal("a failed post reported success")
	}
	if st.jobCount() != 1 || st.jobs[0] != actID {
		t.Fatalf("jobs %v", st.jobs)
	}
	// The job posts it once Slack answers.
	api.postErr = nil
	if err := n.UpdateJob(context.Background(), orgA, ids.MustParse[ids.ActionKind](actID), 2); err != nil || api.posts != 1 {
		t.Fatalf("err %v posts %d", err, api.posts)
	}
}

func TestNotifierUpdatesInPlace(t *testing.T) {
	st, api := newFakeStore(), newFakeAPI()
	n := newNotifier(st, api)
	if err := n.ApprovalRequested(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	// Approved: the job edits the card and its version moves to approved's rank.
	st.card = cardRow("approved", []store.CardVote{{UserID: "usr_a", Name: "Arjun", Vote: "approve", At: t0}})
	if err := n.ApprovalResolved(context.Background(), notice()); err != nil || st.jobCount() != 1 {
		t.Fatalf("err %v jobs %v", err, st.jobs)
	}
	if err := n.UpdateJob(context.Background(), orgA, ids.MustParse[ids.ActionKind](actID), 1); err != nil {
		t.Fatal(err)
	}
	if len(api.updates) != 1 || st.msgs[0].Version != StateApproved.Rank() {
		t.Fatalf("updates %v version %d", api.updates, st.msgs[0].Version)
	}
	// Sealed with a verdict.
	d, v, l := "deed_01k6vrddq2atgb2023qvhhct3h", "VERIFIED", "L1"
	st.card.ActionState, st.card.DeedID, st.card.WitnessVerdict, st.card.WitnessLevel = "sealed", &d, &v, &l
	if err := n.UpdateJob(context.Background(), orgA, ids.MustParse[ids.ActionKind](actID), 1); err != nil {
		t.Fatal(err)
	}
	if len(api.updates) != 2 || st.msgs[0].Version != StateSealed.Rank() {
		t.Fatalf("updates %v version %d", api.updates, st.msgs[0].Version)
	}
	// A stale job that reads an older state can't take the card back.
	st.card = cardRow("pending", nil)
	if err := n.UpdateJob(context.Background(), orgA, ids.MustParse[ids.ActionKind](actID), 1); err != nil {
		t.Fatal(err)
	}
	if len(api.updates) != 2 || st.msgs[0].Version != StateSealed.Rank() {
		t.Fatalf("a stale job edited the card: updates %v version %d", api.updates, st.msgs[0].Version)
	}
}

func TestNotifierPartialAndFailure(t *testing.T) {
	st, api := newFakeStore(), newFakeAPI()
	n := newNotifier(st, api)
	if err := n.ApprovalRequested(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	if err := n.ApprovalChanged(context.Background(), orgA, aprID, actID); err != nil || st.jobCount() != 1 {
		t.Fatalf("err %v jobs %d", err, st.jobCount())
	}
	// An edit that keeps failing: the job retries (an error) until the last attempt, then marks it.
	api.updateErr = &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, AgentMessage: "Slack failed (HTTP 500)."}
	st.card = cardRow("pending", []store.CardVote{{UserID: "usr_a", Name: "Arjun", Vote: "approve", At: t0}})
	st.card.Required = 2
	if err := n.UpdateJob(context.Background(), orgA, ids.MustParse[ids.ActionKind](actID), 1); err == nil {
		t.Fatal("a failed edit before the last attempt didn't ask for a retry")
	}
	if err := n.UpdateJob(context.Background(), orgA, ids.MustParse[ids.ActionKind](actID), MaxUpdateAttempts); err != nil {
		t.Fatal(err)
	}
	if len(st.failed) != 1 || !strings.HasPrefix(st.failed[0], "upstream_error: ") {
		t.Fatalf("failed %v", st.failed)
	}
	if failureText(io.EOF) != "upstream_error" {
		t.Fatal("untyped failure text")
	}
}

func TestNotifierWatch(t *testing.T) {
	st, api := newFakeStore(), newFakeAPI()
	n := newNotifier(st, api)
	if err := n.ApprovalRequested(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	bus := ledger.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); n.Watch(ctx, bus) }()
	for bus.Stats().Subscribers == 0 {
		time.Sleep(time.Millisecond)
	}
	ev := func(typ, kind string, action bool) string {
		m := map[string]any{"v": 1, "type": typ, "org": orgA.String(), "id": "x", "at": "2026-10-10T04:14:00.000Z"}
		if action {
			m["action"] = actID
		}
		if kind != "" {
			m["kind"] = kind
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	bus.Notify(ev("approval.decided", "", true))                                         // queued
	bus.Notify(ev("deed.sealed", "action", true))                                        // queued
	bus.Notify(ev("deed.sealed", "admin", true))                                         // an admin deed: ignored
	bus.Notify(ev("action.created", "", true))                                           // ignored
	bus.Notify(ev("approval.decided", "", false))                                        // no action: ignored
	bus.Notify(`{"v":1,"type":"approval.decided","org":"bad","action":"` + actID + `"}`) // bad org: ignored
	bus.Reset()
	deadline := time.Now().Add(2 * time.Second)
	for st.jobCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if st.jobCount() != 2 {
		t.Fatalf("jobs %v", st.jobs)
	}
}

func TestViewOf(t *testing.T) {
	base := "https://deedglass.example.test"
	// Pending, with the Summarizer's sentence.
	v := viewOf(cardRow("pending", nil), fakeConns{sums: map[string]connector.Connector{"github": summarizing{}}}, base)
	if v.State != StatePending || v.Agent != "support-bot" || v.RuleID != "pull-requests-to-main-need-approval" || v.PolicyName != "acme-demo" ||
		v.PolicyVersion != 7 || v.ConsoleURL != base+"/app/approvals/"+aprID || v.Summary.Plain() != "support-bot opens a pull request into main." {
		t.Fatalf("%+v", v)
	}
	// No Summarizer: the fallback.
	v = viewOf(cardRow("pending", nil), fakeConns{}, base)
	if v.Summary.Plain() != "support-bot wants to run github.create_pr." {
		t.Fatal(v.Summary.Plain())
	}
	// Four-eyes in progress.
	r := cardRow("pending", []store.CardVote{{Name: "Arjun", Vote: "approve", At: t0}})
	r.Required = 2
	if v = viewOf(r, nil, base); v.State != StatePartial || v.Approved != 1 || v.Required != 2 {
		t.Fatalf("%+v", v)
	}
	// Sealed: MISMATCH with the witness's sentence, and a failed tool.
	r = cardRow("approved", []store.CardVote{{Name: "Arjun", Vote: "approve", At: t0}})
	d, mm, l := "deed_01k6vrddq2atgb2023qvhhct3h", "MISMATCH", "L1"
	r.ActionState, r.DeedID, r.WitnessVerdict, r.WitnessLevel = "sealed", &d, &mm, &l
	r.WitnessDetail = json.RawMessage(`{"summary":"GitHub shows a different title."}`)
	if v = viewOf(r, nil, base); v.State != StateSealed || v.Verdict != "MISMATCH" || v.Sentence != "GitHub shows a different title." || v.DecidedBy != "Arjun" {
		t.Fatalf("%+v", v)
	}
	failed := "failed"
	r.ExecutionStatus = &failed
	if v = viewOf(r, nil, base); !v.Failed {
		t.Fatalf("%+v", v)
	}
	// Denied by a person, and by the system (unsatisfiable).
	reason := "Not during the release freeze."
	r = cardRow("denied", []store.CardVote{{Name: "Arjun", Vote: "deny", At: t0}})
	r.DecisionReason = &reason
	if v = viewOf(r, nil, base); v.State != StateDenied || v.DecidedBy != "Arjun" || v.DenyReason != reason {
		t.Fatalf("%+v", v)
	}
	at := t0
	r = cardRow("denied", nil)
	r.DecidedAt = &at
	if v = viewOf(r, nil, base); v.DecidedBy != "Deedglass" || !v.DecidedAt.Equal(t0) {
		t.Fatalf("%+v", v)
	}
	if viewOf(cardRow("expired", nil), nil, base).State != StateExpired || viewOf(cardRow("cancelled", nil), nil, base).State != StateCancelled {
		t.Fatal("final states")
	}
	if !wantsSlack([]string{"email", "slack:#ops"}) || wantsSlack([]string{"email"}) || wantsSlack(nil) {
		t.Fatal("wantsSlack")
	}
	if approverWords([]string{"role:owner"}) != "an owner" || approverWords([]string{"role:viewer"}) != "a member of this org" ||
		approverWords([]string{"user:a@b.c"}) != "a listed approver" || approverWords([]string{"role:admin", "role:owner"}) != "a listed approver" {
		t.Fatal("approverWords")
	}
	if MsgNotEligible("r", "") != `You can see this request, but policy "r" needs someone else to approve it.` {
		t.Fatal(MsgNotEligible("r", ""))
	}
	for _, s := range []State{StatePending, StatePartial, StateApproved, StateSealed, StateDenied, StateExpired, StateCancelled, State("x")} {
		_ = s.Rank()
	}
}

// summarizing is a connector type that writes its own sentence.
type summarizing struct{ connector.Connector }

func (summarizing) Summarize(in connector.SummaryInput) connector.Summary {
	var a struct{ Base string }
	_ = json.Unmarshal(in.Args, &a)
	return connector.Summary{{Text: in.Agent, Style: connector.SummaryStrong}, {Text: " opens a pull request into "}, {Text: a.Base, Style: connector.SummaryCode}, {Text: "."}}
}

var _ = bytes.MinRead
