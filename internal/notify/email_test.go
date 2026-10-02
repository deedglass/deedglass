package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mailer"
)

// fakeQueue runs every job at once against a recording mailer (a test sink: nothing is sent).
type fakeQueue struct{ sent []mailer.Message }

func (q *fakeQueue) Submit(kind, userID, requestID string, j mailer.Job) bool {
	_ = j(context.Background(), q)
	return true
}

func (q *fakeQueue) Send(_ context.Context, m mailer.Message) error {
	q.sent = append(q.sent, m)
	return nil
}

func notice() ApprovalNotice {
	return ApprovalNotice{OrgID: ids.New[ids.OrgKind](), OrgName: "Acme Demo", ApprovalID: "apr_01m3y2z9q4hk7c8vd5w3e6f2ab",
		AgentName: "claude-desktop", Tool: "postgres.execute", RuleID: "orders-writes-need-approval",
		Reason: "Writes to the orders database need a person to countersign.", Summary: "UPDATE about 1 row in public.orders",
		Estimated: true, TimeZone: "Asia/Kolkata", Required: 1, ExpiresAt: time.Date(2026, 10, 2, 5, 11, 0, 0, time.UTC),
		Status: "pending", RequestID: "req_x",
		Recipients: []Recipient{
			{UserID: ids.New[ids.UserKind](), Email: "priya@acme.test", Name: "Priya Raman", Link: "http://127.0.0.1:8080/approve#t=" + strings.Repeat("A", 43), LinkID: "atk_1"},
			{UserID: ids.New[ids.UserKind](), Email: "sam@deedglass.example.com", Name: "Sam", Link: "http://127.0.0.1:8080/approve#t=" + strings.Repeat("B", 43), LinkID: "atk_2", Self: true},
		}}
}

func TestEmailApprovalRequested(t *testing.T) {
	q := &fakeQueue{}
	e := &Email{Queue: q, Now: func() time.Time { return time.Date(2026, 10, 2, 4, 41, 0, 0, time.UTC) }}
	if e.Name() != "email" {
		t.Fatal(e.Name())
	}
	if err := e.ApprovalRequested(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	if len(q.sent) != 2 {
		t.Fatalf("sent %d", len(q.sent))
	}
	m := q.sent[0]
	if m.To != "priya@acme.test" || m.Kind != "approval" || m.IdempotencyKey != "approval/apr_01m3y2z9q4hk7c8vd5w3e6f2ab/atk_1" ||
		m.Subject != "claude-desktop is waiting for your countersign" || !strings.Contains(m.HTML, strings.Repeat("A", 43)) ||
		!strings.Contains(m.Text, "10:41 IST") {
		t.Errorf("message %+v", m)
	}
	if strings.Contains(q.sent[0].HTML, strings.Repeat("B", 43)) {
		t.Error("one person's link in another's email")
	}
	if !strings.Contains(q.sent[1].Text, "It runs on your behalf") {
		t.Error("self line missing")
	}
	if err := e.ApprovalResolved(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
}

func TestEmailSkipsReserved(t *testing.T) {
	q := &fakeQueue{}
	e := &Email{Queue: q, SkipReserved: true, Now: func() time.Time { return time.Date(2026, 10, 2, 4, 41, 0, 0, time.UTC) }}
	if err := e.ApprovalRequested(context.Background(), notice()); err != nil {
		t.Fatal(err)
	}
	if len(q.sent) != 0 {
		t.Fatalf("a reserved address got mail: %d", len(q.sent))
	}
	n := notice()
	n.Recipients[0].Email = "priya@acme.com"
	n.TimeZone = "Not/AZone" // falls back to UTC
	if err := e.ApprovalRequested(context.Background(), n); err != nil || len(q.sent) != 1 || !strings.Contains(q.sent[0].Text, "UTC") {
		t.Fatalf("%v %d", err, len(q.sent))
	}
	if err := (&Email{}).ApprovalRequested(context.Background(), n); err == nil {
		t.Fatal("no queue accepted")
	}
}

func TestReservedAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"a@example.com": true, "a@EXAMPLE.org": true, "a@example.net.": true, "a@sub.example.com": true,
		"a@acme.test": true, "a@x.example": true, "a@x.invalid": true, "a@localhost": true, "a@app.localhost": true,
		"no-at-sign": true, "a@acme.com": false, "a@example.co": false, "a@notexample.com": false, "a@gmail.com": false,
	} {
		if got := ReservedAddress(addr); got != want {
			t.Errorf("%s: %v", addr, got)
		}
	}
}
