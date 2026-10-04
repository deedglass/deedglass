package learn

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// S28 §3.4, §3.9: the schedule loop. The daily pass at 03:00 org-local, once a day; the digest on
// Monday from 09:00 org-local, once for the previous ISO week; the sweeper on every tick.

type schedEnv struct {
	*sugEnv
	mail *fakeMail
	s    *Scheduler
	svc  *SuggestionService
	w    *fakeWriter
}

func newSchedEnv(t *testing.T, tz string) *schedEnv {
	t.Helper()
	e := newSugEnv(t, func(f *evFixture) { f.addOrderApprovals(401, 3, 0) })
	o := e.m.org(e.org)
	o.info.Timezone = tz
	o.people = []memPerson{{DigestRecipient: DigestRecipient{UserID: "usr_owner", Email: "owner@acme.example", Role: "owner"}}}
	o.numbers = &DigestNumbers{Actions: 12}
	mail := &fakeMail{}
	w := &fakeWriter{st: e.m}
	svc := &SuggestionService{Store: e.m, Suggester: e.sg, Writer: w, Now: func() time.Time { return sugNow }}
	s := &Scheduler{Store: e.m, Suggestions: svc, Digest: &Digester{Store: e.m, Mail: mail, BaseURL: "https://app.deedglass.com"}}
	return &schedEnv{sugEnv: e, mail: mail, s: s, svc: svc, w: w}
}

func ist(t *testing.T, y int, m time.Month, d, h, min int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(y, m, d, h, min, 0, 0, loc).UTC()
}

func TestTickOrgDailyPass(t *testing.T) {
	t.Parallel()
	e := newSchedEnv(t, "Asia/Kolkata")
	// Monday 12 Oct 2026, 02:59 in Kolkata (Sunday 21:29 UTC): nothing yet.
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 12, 2, 59))
	if runs := e.m.runsOf(e.org, "suggest"); len(runs) != 0 || e.m.insertCalls != 0 {
		t.Fatalf("before 03:00: %v", runs)
	}
	// 03:05: one pass, as the system.
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 12, 3, 5))
	if runs := e.m.runsOf(e.org, "suggest"); len(runs) != 1 || runs[0] != "2026-10-12=succeeded" {
		t.Fatalf("after 03:00: %v", runs)
	}
	if e.byGenerator(GenLoosenApprovalRate) == nil {
		t.Fatal("the pass created nothing")
	}
	// Later the same day: nothing more.
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 12, 4, 0))
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 12, 23, 59))
	if runs := e.m.runsOf(e.org, "suggest"); len(runs) != 1 {
		t.Fatalf("same day: %v", runs)
	}
	// The next day: a second pass.
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 13, 3, 0))
	if runs := e.m.runsOf(e.org, "suggest"); len(runs) != 2 || runs[1] != "2026-10-13=succeeded" {
		t.Fatalf("next day: %v", runs)
	}
	// A failing pass is recorded as failed.
	e.m.errOpenFingerprints = errors.New("db down")
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 14, 3, 0))
	if runs := e.m.runsOf(e.org, "suggest"); len(runs) != 3 || runs[2] != "2026-10-14=failed" {
		t.Fatalf("failed pass: %v", runs)
	}
}

func TestTickOrgDigest(t *testing.T) {
	t.Parallel()
	e := newSchedEnv(t, "Asia/Kolkata")
	// Monday 08:59: no digest yet.
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 12, 8, 59))
	if len(e.mail.sent()) != 0 {
		t.Fatal("digest before 09:00")
	}
	// Monday 09:00 in Kolkata (03:30 UTC): the digest for ISO week 41 (5 to 11 October).
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 12, 9, 0))
	got := e.mail.sent()
	if len(got) != 1 || got[0].IdempotencyKey != "digest/"+e.org.String()+"/2026-W41/usr_owner" || !strings.Contains(got[0].Subject, "week 41") {
		t.Fatalf("digest %+v", got)
	}
	// Later on Monday, and on Tuesday: nothing more.
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 12, 17, 0))
	e.s.TickOrg(bg, e.org, ist(t, 2026, 10, 13, 9, 0))
	if len(e.mail.sent()) != 1 {
		t.Fatalf("%d digests", len(e.mail.sent()))
	}
	if runs := e.m.runsOf(e.org, "digest"); len(runs) != 1 || runs[0] != "2026-W41=succeeded" {
		t.Fatalf("runs %v", runs)
	}
	// Across the year boundary: Monday 4 January 2027 sends ISO week 53 of 2026.
	e.s.TickOrg(bg, e.org, ist(t, 2027, 1, 4, 9, 30))
	if got := e.mail.sent(); len(got) != 2 || got[1].IdempotencyKey != "digest/"+e.org.String()+"/2026-W53/usr_owner" {
		t.Fatalf("year boundary: %+v", got[len(got)-1].IdempotencyKey)
	}
	// Two instances ticking the same Monday: one digest.
	other := &Scheduler{Store: e.m, Digest: &Digester{Store: e.m, Mail: e.mail, BaseURL: "https://app.deedglass.com"}}
	other.TickOrg(bg, e.org, ist(t, 2027, 1, 4, 10, 0))
	e.s.TickOrg(bg, e.org, ist(t, 2027, 1, 4, 10, 5))
	if len(e.mail.sent()) != 2 {
		t.Fatalf("%d digests after the second instance", len(e.mail.sent()))
	}
}

func TestTickOrgSweeps(t *testing.T) {
	t.Parallel()
	e := newSchedEnv(t, "UTC")
	e.generate()
	lo := e.byGenerator(GenLoosenApprovalRate)
	e.w.afterAct = func() error { return errors.New("crash") }
	if _, err := e.svc.Accept(bg, e.org, Actor{Type: "user", ID: "usr_owner"}, lo.ID, AcceptRequest{}, ""); err == nil {
		t.Fatal("no crash")
	}
	e.w.afterAct = nil
	e.svc.Now = func() time.Time { return sugNow.Add(StuckAfter + time.Minute) }
	// 01:00 UTC: no pass is due, but the sweeper runs.
	e.s.TickOrg(bg, e.org, time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC))
	if e.m.status(lo.ID) != SugAccepted || e.m.deedsOf("suggestion.accepted", lo.ID) != 1 {
		t.Fatalf("status %s", e.m.status(lo.ID))
	}
	// An unknown org: the sweep finds nothing and the tick stops at the org read.
	e.s.TickOrg(bg, ids.New[ids.OrgKind](), time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
	// Nothing configured: a tick does nothing.
	(&Scheduler{Store: e.m}).TickOrg(bg, e.org, time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC))
}

// errOrgs is a ScheduleStore whose org listing fails.
type errOrgs struct{ *memStore }

func (errOrgs) DetectOrgs(context.Context, time.Duration) ([]ids.OrgID, error) {
	return nil, errors.New("listing failed")
}

// countOrgs counts the listing calls.
type countOrgs struct {
	*memStore
	n *atomic.Int32
}

func (c countOrgs) DetectOrgs(ctx context.Context, d time.Duration) ([]ids.OrgID, error) {
	c.n.Add(1)
	return c.memStore.DetectOrgs(ctx, d)
}

func TestSchedulerTickAndRun(t *testing.T) {
	t.Parallel()
	e := newSchedEnv(t, "UTC")
	other := ids.New[ids.OrgKind]()
	e.m.addOrg(other, "Other", "UTC")
	// Tick runs every org the store lists.
	e.s.Tick(bg, time.Date(2026, 10, 10, 4, 0, 0, 0, time.UTC))
	if len(e.m.runsOf(e.org, "suggest")) != 1 || len(e.m.runsOf(other, "suggest")) != 1 {
		t.Fatalf("runs %v %v", e.m.runsOf(e.org, "suggest"), e.m.runsOf(other, "suggest"))
	}
	// A failed listing: nothing, no panic.
	(&Scheduler{Store: errOrgs{e.m}}).Tick(bg, time.Now())
	// A cancelled context stops between orgs.
	ctx, cancel := context.WithCancel(bg)
	cancel()
	(&Scheduler{Store: e.m}).Tick(ctx, time.Now())

	// Run: the first tick at once (First < 0), then every Every, until the context ends.
	var n atomic.Int32
	s := &Scheduler{Store: countOrgs{e.m, &n}, Every: 5 * time.Millisecond, First: -1, Now: func() time.Time { return time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC) }}
	ctx, cancel = context.WithTimeout(bg, 60*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run: %v", err)
	}
	if n.Load() < 2 {
		t.Fatalf("%d ticks", n.Load())
	}
	// The first wait honours the context.
	s2 := &Scheduler{Store: e.m, First: time.Hour}
	ctx2, cancel2 := context.WithTimeout(bg, 10*time.Millisecond)
	defer cancel2()
	if err := s2.Run(ctx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first wait: %v", err)
	}
	if (&Scheduler{}).now().IsZero() || (&Scheduler{}).log() == nil {
		t.Fatal("defaults")
	}
}
