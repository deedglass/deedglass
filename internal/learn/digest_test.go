package learn

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mailer"
)

// S28 §6 "Digest": recipients per setting, the shadow line, the subject, the idempotency key, no
// payload strings, and exactly once per org and ISO week.

// fakeMail records queued messages.
type fakeMail struct {
	mu   sync.Mutex
	msgs []mailer.Message
	full bool
}

func (f *fakeMail) Enqueue(m mailer.Message) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.full {
		return false
	}
	f.msgs = append(f.msgs, m)
	return true
}

func (f *fakeMail) sent() []mailer.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mailer.Message(nil), f.msgs...)
}

// week41 is the Monday of ISO week 41 of 2026.
var week41 = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

type digestEnv struct {
	m    *memStore
	org  ids.OrgID
	mail *fakeMail
	g    *Digester
}

func newDigestEnv(t *testing.T, recipients string) *digestEnv {
	t.Helper()
	m := newMemStore()
	org := ids.New[ids.OrgKind]()
	o := m.addOrg(org, "Acme", "UTC")
	o.info.Settings.Digest.Recipients = recipients
	o.people = []memPerson{
		{DigestRecipient: DigestRecipient{UserID: "usr_owner", Email: "owner@acme.example", Role: "owner"}},
		{DigestRecipient: DigestRecipient{UserID: "usr_admin", Email: "admin@acme.example", Role: "admin"}},
		{DigestRecipient: DigestRecipient{UserID: "usr_viewer", Email: "viewer@acme.example", Role: "viewer"}},
	}
	o.numbers = &DigestNumbers{Actions: 5612, Verified: 4980, Witnessed: 5600, Denied: 311, Mismatches: 9,
		TopDenyRules:  []mailer.DigestRule{{Rule: "refund-limit", Count: 120}, {Rule: "block-destructive-sql", Count: 7}},
		AnomaliesOpen: 1, AnomaliesOpened: 2, AnomaliesResolved: 1, AnomalyTitle: "support-bot ran 4x its usual volume"}
	// Two open suggestions whose titles may show, with payload sentinels where a digest must never look.
	for i, title := range []string{"SENTINEL_TITLE_OK raise the refund limit", "Second title"} {
		m.putSuggestion(org, NewSuggestion{ID: ids.New[ids.SuggestionKind]().String(), Kind: KindLoosen, Generator: GenLoosenApprovalRate, Title: title,
			Rationale: "SENTINEL_ARG_7731 for cust_SENTINEL", RiskNote: "priya@acme.example", Evidence: []byte(`{"sample_actions":["act_sentinel"]}`),
			CandidateSource: []byte("SENTINEL_TABLE"), Fingerprint: "sha256:d" + title, Impact: 10 - i}, SugOpen)
	}
	mail := &fakeMail{}
	g := &Digester{Store: m, Mail: mail, BaseURL: "https://app.deedglass.com/"}
	return &digestEnv{m: m, org: org, mail: mail, g: g}
}

func TestDigestBuild(t *testing.T) {
	t.Parallel()
	e := newDigestEnv(t, DigestOwners)
	msgs, d, err := e.g.Build(bg, e.org, week41)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].To != "owner@acme.example" || msgs[0].UserID != "usr_owner" || msgs[0].Kind != "digest" {
		t.Fatalf("%+v", msgs)
	}
	if want := "digest/" + e.org.String() + "/2026-W41/usr_owner"; msgs[0].IdempotencyKey != want {
		t.Errorf("key %q, want %q", msgs[0].IdempotencyKey, want)
	}
	if d.Year != 2026 || d.Week != 41 || d.SuggestionsOpen != 2 || len(d.SuggestionTitles) != 2 || d.SuggestionTitles[0] != "SENTINEL_TITLE_OK raise the refund limit" ||
		d.Link != "https://app.deedglass.com/app/insights" || d.ShadowLine != "" {
		t.Fatalf("data %+v", d)
	}
	dd := *d
	dd.Email, dd.Role = "owner@acme.example", "owner"
	if msgs[0].Subject != mailer.DigestSubject(dd) || msgs[0].Subject != "Acme · week 41 · 5,612 agent actions, 2 suggestions to review" {
		t.Errorf("subject %q", msgs[0].Subject)
	}
	for _, body := range []string{msgs[0].HTML, msgs[0].Text} {
		if !strings.Contains(body, "SENTINEL_TITLE_OK") || !strings.Contains(body, "refund-limit") || !strings.Contains(body, "/app/insights") {
			t.Errorf("body misses the title, a rule or the link:\n%s", body)
		}
		for _, s := range []string{"SENTINEL_ARG_7731", "cust_SENTINEL", "priya@acme.example", "act_sentinel", "SENTINEL_TABLE", "admin@acme.example", "viewer@acme.example"} {
			if strings.Contains(body, s) {
				t.Errorf("the digest carries %q", s)
			}
		}
	}
	// admins: owners and admins, never viewers.
	e2 := newDigestEnv(t, DigestAdmins)
	msgs, _, _ = e2.g.Build(bg, e2.org, week41)
	if len(msgs) != 2 || msgs[0].To == msgs[1].To {
		t.Fatalf("admins: %+v", msgs)
	}
	for _, m := range msgs {
		if m.To == "viewer@acme.example" {
			t.Fatal("a viewer gets the digest")
		}
	}
	// off: nothing.
	e3 := newDigestEnv(t, DigestOff)
	if msgs, d, err := e3.g.Build(bg, e3.org, week41); msgs != nil || d != nil || err != nil {
		t.Fatalf("off: %v %v %v", msgs, d, err)
	}
	// An unknown org.
	if _, _, err := e.g.Build(bg, ids.New[ids.OrgKind](), week41); err == nil {
		t.Fatal("unknown org")
	}
}

func TestDigestShadowLine(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	rep := func(since time.Time, eval, dis int64) *ShadowReport {
		return &ShadowReport{Shadow: VersionInfo{Name: "acme-sim", Version: 3, Since: since}, Summary: ShadowSummary{Evaluated: eval, Disagree: dis}}
	}
	cases := []struct {
		r    *ShadowReport
		want string
	}{
		{rep(now.Add(-50*time.Hour), 1204, 24), "acme-sim v3 has run in shadow for 2 days and agrees on 1,180 of 1,204 actions."},
		{rep(now.Add(-25*time.Hour), 10, 0), "acme-sim v3 has run in shadow for 1 day and agrees on 10 of 10 actions."},
		{rep(now.Add(-time.Hour), 0, 0), "acme-sim v3 started running in shadow today and hasn't seen any actions yet."},
		{rep(time.Time{}, 5, 1), "acme-sim v3 started running in shadow today and agrees on 4 of 5 actions."},
	}
	for _, c := range cases {
		if got := DigestShadowLine(c.r, now); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
	// Through Build: the Insights service's report for the live shadow.
	e := newDigestEnv(t, DigestOwners)
	e.g.Insights = &Service{Store: &fakeStore{shadow: &VersionInfo{PolicyID: "pol_x", Name: "acme-sim", Version: 3, VersionID: "pv_3", Live: true, Since: week41.Add(-48 * time.Hour)},
		shadowRows: []ShadowRow{{Enforce: "allow", Shadow: "allow", Tool: "postgres.query", Count: 1180}, {Enforce: "require_approval", Shadow: "allow", Tool: "postgres.execute", Count: 24}}}}
	msgs, d, err := e.g.Build(bg, e.org, week41)
	if err != nil {
		t.Fatal(err)
	}
	if d.ShadowLine != "acme-sim v3 has run in shadow for 9 days and agrees on 1,180 of 1,204 actions." || !strings.Contains(msgs[0].Text, d.ShadowLine) {
		t.Fatalf("shadow line %q", d.ShadowLine)
	}
}

func TestDigestSendOnce(t *testing.T) {
	t.Parallel()
	e := newDigestEnv(t, DigestAdmins)
	n, err := e.g.Send(bg, e.m, e.org, week41, week41.AddDate(0, 0, 7).Add(9*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	n, err = e.g.Send(bg, e.m, e.org, week41, week41.AddDate(0, 0, 7).Add(10*time.Hour))
	if err != nil || n != 0 || len(e.mail.sent()) != 2 {
		t.Fatalf("second send: %d %v (%d queued)", n, err, len(e.mail.sent()))
	}
	if runs := e.m.runsOf(e.org, "digest"); len(runs) != 1 || runs[0] != "2026-W41=succeeded" {
		t.Fatalf("runs %v", runs)
	}
	// A full queue counts what was queued.
	e2 := newDigestEnv(t, DigestOwners)
	e2.mail.full = true
	if n, err := e2.g.Send(bg, e2.m, e2.org, week41, week41); n != 0 || err != nil {
		t.Fatalf("full queue: %d %v", n, err)
	}
	// No mailer: nothing queued, the week still recorded.
	e3 := newDigestEnv(t, DigestOwners)
	e3.g.Mail = nil
	if n, err := e3.g.Send(bg, e3.m, e3.org, week41, week41); n != 0 || err != nil || len(e3.m.runsOf(e3.org, "digest")) != 1 {
		t.Fatalf("no mailer: %d %v", n, err)
	}
	// A failed build records a failed run.
	e4 := newDigestEnv(t, DigestOwners)
	e4.g.BaseURL = "javascript:alert(1)"
	if _, err := e4.g.Send(bg, e4.m, e4.org, week41, week41); err == nil {
		t.Fatal("bad link accepted")
	}
	if runs := e4.m.runsOf(e4.org, "digest"); len(runs) != 1 || runs[0] != "2026-W41=failed" {
		t.Fatalf("runs %v", runs)
	}
}

// Two instances race for the same org and week: exactly one set of emails.
func TestDigestTwoInstances(t *testing.T) {
	t.Parallel()
	for range 20 {
		e := newDigestEnv(t, DigestAdmins)
		other := &Digester{Store: e.m, Mail: e.mail, BaseURL: e.g.BaseURL}
		var wg sync.WaitGroup
		for _, g := range []*Digester{e.g, other} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := g.Send(bg, e.m, e.org, week41, week41.AddDate(0, 0, 7)); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if got := e.mail.sent(); len(got) != 2 {
			t.Fatalf("%d emails queued, want 2", len(got))
		}
	}
}

func TestDigestPeriodAndWeekStart(t *testing.T) {
	t.Parallel()
	kolkata, _ := time.LoadLocation("Asia/Kolkata")
	la, _ := time.LoadLocation("America/Los_Angeles")
	cases := []struct {
		t      time.Time
		loc    *time.Location
		monday string
		period string
	}{
		{time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC), time.UTC, "2026-10-05", "2026-W41"},
		{time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), time.UTC, "2026-10-05", "2026-W41"},
		{time.Date(2026, 10, 4, 23, 59, 0, 0, time.UTC), time.UTC, "2026-09-28", "2026-W40"},
		// Sunday 20:00 UTC is Monday 01:30 in Kolkata.
		{time.Date(2026, 10, 4, 20, 0, 0, 0, time.UTC), kolkata, "2026-10-05", "2026-W41"},
		// Monday 03:00 UTC is still Sunday in Los Angeles.
		{time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC), la, "2026-09-28", "2026-W40"},
		// Year boundaries: 2026 has 53 ISO weeks; 2024-12-30 is in ISO week 1 of 2025.
		{time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC), time.UTC, "2026-12-28", "2026-W53"},
		{time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC), time.UTC, "2027-01-04", "2027-W01"},
		{time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC, "2024-12-30", "2025-W01"},
	}
	for _, c := range cases {
		m := weekStart(c.t, c.loc)
		y, w := m.ISOWeek()
		if m.Format("2006-01-02") != c.monday || m.Weekday() != time.Monday || m.Hour() != 0 || m.Location() != c.loc || DigestPeriod(y, w) != c.period {
			t.Errorf("weekStart(%s, %s) = %s (%s), want %s (%s)", c.t, c.loc, m, DigestPeriod(y, w), c.monday, c.period)
		}
	}
	if DigestPeriod(2026, 1) != "2026-W01" || DigestPeriod(2026, 41) != "2026-W41" {
		t.Fatal("DigestPeriod")
	}
}
