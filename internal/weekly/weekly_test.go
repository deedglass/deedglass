package weekly

// The goldens live in testdata/. After checking a copy change, regenerate them:
//
//	go test ./internal/weekly -update

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/caught"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mailer"
)

var update = flag.Bool("update", false, "rewrite the goldens")

func i64(n int64) *int64 { return &n }

// fixtureFacts is the fixture week: Acme, 12 to 18 Oct 2026, one Doesn't match and a caught session.
func fixtureFacts() Facts {
	p, err := ParsePeriod("2026-W42", time.UTC)
	if err != nil {
		panic(err)
	}
	at := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	return Facts{
		Org: Org{ID: "org_01k7acme0000000000000000000", Name: "Acme", Slug: "acme"}, Period: p, Now: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC),
		Decisions: []AgentDecision{
			{AgentID: "agt_01k7ops0000000000000000000", Name: "ops-agent", Decision: "allow", Count: 300},
			{AgentID: "agt_01k7ops0000000000000000000", Name: "ops-agent", Decision: "require_approval", Count: 22},
			{AgentID: "agt_01k7ops0000000000000000000", Name: "ops-agent", Decision: "deny", Count: 12},
			{AgentID: "agt_01k7sup0000000000000000000", Name: "support-agent", Decision: "allow", Count: 71},
			{AgentID: "agt_01k7sup0000000000000000000", Name: "support-agent", Decision: "deny", Count: 7},
		},
		Tools:    []Count{{Name: "postgres.execute", Count: 250}, {Name: "postgres.query", Count: 110}, {Name: "stripe.refund", Count: 52}},
		Verdicts: map[string]int64{"VERIFIED": 387, "MISMATCH": 1, "UNVERIFIABLE": 24},
		Previous: i64(380),
		Denials: []Denial{{Rule: "block-destructive-sql", Reason: "DROP is never allowed for agents.", Count: 12},
			{Rule: "refund-limit", Reason: "Refunds over $200 need a person.", Count: 5}, {Rule: "@default", Count: 2}, {Rule: "zzz", Count: 0}},
		Approvals: Approvals{Requested: 22, Approved: 20, Denied: 1, Expired: 1, MedianSeconds: i64(260)},
		Undos:     Undos{Actions: 1}, Stops: Stops{Agent: 0, All: 0}, Sessions: Sessions{Watched: 3, Caught: 1},
		SessionCaught: []caught.Item{{Kind: caught.KindManyRows, Session: "obs_01k7ses0000000000000000000", Frames: []string{"dtx_01k7f00000000000000000000"},
			Table: "public.orders", Rows: 301, Line: caught.Prefix + "one transaction changed 301 rows in public.orders.", UndoPossible: true,
			UndoFreePath: caught.FreeSessionPath("obs_01k7ses0000000000000000000")}},
		Actions: []caught.Action{{ID: "act_01k7mis0000000000000000000", Agent: "ops-agent", Tool: "postgres.execute", At: at, Decision: "allow",
			Executed: true, Verdict: "MISMATCH", Level: "L2", Undoable: true}},
		Chain: Chain{Status: ChainIntact, Checked: 640},
		Pack:  &Pack{BuiltAt: time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC), Folder: "security-pack-acme-2026-10-16"},
	}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run with -update)", name, err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden (run go test ./internal/weekly -update and review the diff)\n--- got ---\n%s", name, got)
	}
}

func TestGoldens(t *testing.T) {
	s := Build(fixtureFacts())
	b, err := JSON(s)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "week.json", string(b))
	golden(t, "week.md", Markdown(s, Options{}))
	h, err := HTML(s, Options{ConsoleURL: "https://deedglass.acme.internal/app"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "week.html", h)
	golden(t, "week.txt", strings.Join(TextLines(s, Options{ProUndoLine: true}), "\n")+"\n")
	m, err := Email(s, Options{ProUndoLine: true}, "priya@acme.example", "https://deedglass.acme.internal/app",
		"https://deedglass.acme.internal/weekly/off?t=x.y")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "email.txt", "Subject: "+m.Subject+"\n\n"+m.Text)
	golden(t, "email.html", m.HTML)
	// The empty week.
	e := Build(Facts{Org: Org{ID: "org_01k7acme0000000000000000000", Name: "Acme", Slug: "acme"}, Period: s.Period, Now: s.GeneratedAt,
		Chain: Chain{Status: ChainNoRecords}})
	golden(t, "empty.md", Markdown(e, Options{}))
	golden(t, "empty.txt", strings.Join(TextLines(e, Options{ProUndoLine: true}), "\n")+"\n")
}

func TestBuild(t *testing.T) {
	s := Build(fixtureFacts())
	if s.Actions.Total != 412 || s.Actions.Allowed != 371 || s.Actions.Waited != 22 || s.Actions.Blocked != 19 {
		t.Fatalf("actions %+v", s.Actions)
	}
	if s.NumbersLine() != "412 actions · 371 allowed · 22 waited for a person · 19 blocked by rules" {
		t.Fatal(s.NumbersLine())
	}
	if s.CheckLine() != "Check: 387 match · 1 don't match · 24 can't check" {
		t.Fatal(s.CheckLine())
	}
	if len(s.Denials) != 3 || s.Denials[0].Rule != "block-destructive-sql" || s.Denials[2].Rule != "@default" {
		t.Fatalf("denials %+v", s.Denials)
	}
	if s.CaughtTotal != 2 || s.Subject() != "What your agents did this week · 412 actions · 2 mistakes caught" {
		t.Fatalf("caught %d %q", s.CaughtTotal, s.Subject())
	}
	if s.Headline() != "What your agents did this week · acme · 12 to 18 Oct" {
		t.Fatal(s.Headline())
	}
	e := Build(Facts{Period: s.Period})
	if e.Subject() != "What your agents did this week · 0 actions · nothing caught" || e.CaughtHead() != "" || len(e.Caught) != 0 {
		t.Fatalf("empty %q", e.Subject())
	}
	if e.Chain.Status != ChainNotChecked {
		t.Fatal(e.Chain)
	}
}

// The caught block shows only when something was caught; the Pro line only on a caught session and
// only without undo_session.
func TestCaughtAndProLine(t *testing.T) {
	s := Build(fixtureFacts())
	free := strings.Join(TextLines(s, Options{}), "\n")
	pro := strings.Join(TextLines(s, Options{ProUndoLine: true}), "\n")
	if strings.Contains(free, ProLine) || strings.Count(pro, ProLine) != 1 {
		t.Fatalf("pro line: free %q pro %q", free, pro)
	}
	for _, out := range []string{free, pro, Markdown(s, Options{ProUndoLine: true})} {
		if !strings.Contains(out, "deedglass undo preview act_01k7mis0000000000000000000") || !strings.Contains(out, "deedglass observe show obs_01k7ses0000000000000000000 --undo-sql") {
			t.Fatalf("a caught view hides the free path:\n%s", out)
		}
	}
	quiet := fixtureFacts()
	quiet.SessionCaught, quiet.Actions = nil, nil
	q := Build(quiet)
	for _, out := range []string{strings.Join(TextLines(q, Options{ProUndoLine: true}), "\n"), Markdown(q, Options{ProUndoLine: true})} {
		if strings.Contains(out, "caught 1") || strings.Contains(out, ProLine) || !strings.Contains(out, "Nothing caught this week.") {
			t.Fatalf("quiet week:\n%s", out)
		}
	}
}

func TestPeriods(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	cases := []struct{ in, key, label string }{
		{"2026-W42", "2026-W42", "12 to 18 Oct 2026"},
		{"2026-W40", "2026-W40", "28 Sep to 4 Oct 2026"},
		{"2026-W53", "2026-W53", "28 Dec 2026 to 3 Jan 2027"},
		{"2026-10", "2026-10", "October 2026"},
	}
	for _, c := range cases {
		p, err := ParsePeriod(c.in, ist)
		if err != nil || p.Key != c.key || p.Label != c.label {
			t.Errorf("%s: %+v %v", c.in, p, err)
		}
	}
	for _, bad := range []string{"", "2026-W54", "2025-W53", "2026-13", "26-10", "2026-W4", "W42", "2026-10-01"} {
		if _, err := ParsePeriod(bad, time.UTC); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	p, _ := ParsePeriod("2026-W42", ist)
	if p.From != time.Date(2026, 10, 11, 18, 30, 0, 0, time.UTC) || p.To.Sub(p.From) != 7*24*time.Hour {
		t.Fatalf("IST bounds %v %v", p.From, p.To)
	}
	now := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)
	if LastWeek(now, time.UTC).Key != "2026-W42" || LastMonth(now, time.UTC).Key != "2026-09" {
		t.Fatal(LastWeek(now, time.UTC).Key, LastMonth(now, time.UTC).Key)
	}
	if p.ShortLabel() != "12 to 18 Oct" {
		t.Fatal(p.ShortLabel())
	}
}

func TestHTMLIsSelfContained(t *testing.T) {
	s := Build(fixtureFacts())
	s.Actions.ByAgent[0].Name = `<script>alert(1)</script>`
	h, err := HTML(s, Options{Brand: &Brand{Name: "Northwind <b>", ContactLine: "ops@northwind.example", LogoMIME: "image/png", Logo: []byte{0x89, 'P', 'N', 'G'}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script", "src=\"http", "href=\"http", "@import", "url("} {
		if strings.Contains(h, bad) {
			t.Errorf("report holds %q", bad)
		}
	}
	if !strings.Contains(h, "Prepared by <b>Northwind &lt;b&gt;</b> with Deedglass") || !strings.Contains(h, `src="data:image/png;base64,`) {
		t.Fatal("brand lines missing")
	}
	md := Markdown(s, Options{Brand: &Brand{Name: "Northwind"}})
	if !strings.Contains(md, "Prepared by Northwind with Deedglass.") || strings.Contains(md, "<script>") {
		t.Fatal(md)
	}
}

func TestOffToken(t *testing.T) {
	key := OffKeyFrom([]byte("pepper-0123456789abcdef"))
	org, user := ids.New[ids.OrgKind](), ids.New[ids.UserKind]()
	tok, err := OffToken(key, org, user)
	if err != nil {
		t.Fatal(err)
	}
	o, u, err := ParseOffToken(key, tok)
	if err != nil || o != org || u != user {
		t.Fatalf("round trip %v %v %v", o, u, err)
	}
	other := OffKeyFrom([]byte("another-pepper-0123456789"))
	for _, bad := range []string{"", tok + "x", strings.Replace(tok, ".", "", 1), "a.b", tok[:len(tok)-2] + "AA"} {
		if _, _, err := ParseOffToken(key, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, _, err := ParseOffToken(other, tok); err == nil {
		t.Fatal("another key accepted")
	}
	if _, err := OffToken(nil, org, user); err == nil {
		t.Fatal("no key accepted")
	}
}

// --- the Monday job over fakes ---

type fakeStore struct {
	orgs     []OrgInfo
	claimed  map[string]bool
	people   []Recipient
	finished []string
}

func (f *fakeStore) ActiveOrgs(context.Context) ([]OrgInfo, error) { return f.orgs, nil }
func (f *fakeStore) ClaimRun(_ context.Context, org ids.OrgID, kind, period string, _ time.Time) (bool, error) {
	k := org.String() + "/" + kind + "/" + period
	if f.claimed[k] {
		return false, nil
	}
	f.claimed[k] = true
	return true, nil
}
func (f *fakeStore) FinishRun(_ context.Context, _ ids.OrgID, _, period string, ok bool, _ map[string]any, _ string, _ time.Time) error {
	f.finished = append(f.finished, period)
	return nil
}
func (f *fakeStore) Recipients(context.Context, ids.OrgID) ([]Recipient, error) { return f.people, nil }
func (f *fakeStore) Summarize(_ context.Context, _ ids.OrgID, p Period, _ time.Time) (Summary, error) {
	ff := fixtureFacts()
	ff.Period = p
	return Build(ff), nil
}

type fakeQueue struct{ msgs []mailer.Message }

func (q *fakeQueue) Enqueue(m mailer.Message) bool { q.msgs = append(q.msgs, m); return true }

func TestMondayJob(t *testing.T) {
	org := ids.New[ids.OrgKind]()
	owner := Recipient{UserID: ids.New[ids.UserKind](), Email: "priya@acme.example"}
	st := &fakeStore{orgs: []OrgInfo{{ID: org, Timezone: "Asia/Kolkata"}}, claimed: map[string]bool{}, people: []Recipient{owner}}
	q := &fakeQueue{}
	key := OffKeyFrom([]byte("pepper-0123456789abcdef"))
	now := time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC) // 08:30 in Kolkata, Monday
	j := &Job{Store: st, Mail: q, BaseURL: "https://deedglass.acme.internal/", OffKey: key, ProUndoLine: func() bool { return false },
		Now: func() time.Time { return now }}
	j.Tick(context.Background())
	j.Tick(context.Background()) // insert-or-skip: once per org and week
	if len(q.msgs) != 1 || len(st.finished) != 1 || st.finished[0] != "2026-W42" {
		t.Fatalf("sent %d, finished %v", len(q.msgs), st.finished)
	}
	m := q.msgs[0]
	if m.Kind != "weekly" || m.To != owner.Email || m.IdempotencyKey != "weekly/"+org.String()+"/2026-W42/"+owner.UserID.String() ||
		!strings.Contains(m.Text, "https://deedglass.acme.internal/app") || strings.Contains(m.Text, ProLine) {
		t.Fatalf("message %+v", m)
	}
	i := strings.Index(m.Text, "/weekly/off?t=")
	tok := strings.TrimSpace(m.Text[i+len("/weekly/off?t="):])
	if o, u, err := ParseOffToken(key, tok); err != nil || o != org || u != owner.UserID {
		t.Fatalf("off link %q: %v", tok, err)
	}
	// Before 08:00 local, on another day, with no recipients or without a mailer: nothing.
	for _, c := range []struct {
		name string
		now  time.Time
		mail bool
	}{
		{"before eight", time.Date(2026, 10, 19, 2, 0, 0, 0, time.UTC), true},
		{"tuesday", time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC), true},
		{"no mailer", time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC), false},
	} {
		st2 := &fakeStore{orgs: st.orgs, claimed: map[string]bool{}, people: []Recipient{owner}}
		q2 := &fakeQueue{}
		j2 := &Job{Store: st2, BaseURL: "https://x.example", OffKey: key, Now: func() time.Time { return c.now }}
		if c.mail {
			j2.Mail = q2
		}
		j2.Tick(context.Background())
		if len(q2.msgs) != 0 || len(st2.claimed) != 0 {
			t.Errorf("%s: sent %d claimed %d", c.name, len(q2.msgs), len(st2.claimed))
		}
	}
	st3 := &fakeStore{orgs: st.orgs, claimed: map[string]bool{}}
	q3 := &fakeQueue{}
	(&Job{Store: st3, Mail: q3, BaseURL: "https://x.example", OffKey: key, Now: func() time.Time { return now }}).Tick(context.Background())
	if len(q3.msgs) != 0 || len(st3.finished) != 1 {
		t.Fatalf("no owners with the email on: sent %d finished %v", len(q3.msgs), st3.finished)
	}
}

func TestJSONShape(t *testing.T) {
	b, _ := JSON(Build(fixtureFacts()))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"object", "version", "org", "period", "actions", "check", "denials", "approvals", "undos", "stops", "sessions", "caught", "caught_total", "chain", "security_pack", "generated_at"} {
		if _, ok := m[k]; !ok {
			t.Errorf("no %s", k)
		}
	}
}

func TestMedianAndWords(t *testing.T) {
	if Median(nil) != nil || *Median([]int64{5, 1, 3}) != 3 || *Median([]int64{4, 1, 3, 2}) != 2 {
		t.Fatal("median")
	}
	for in, want := range map[int64]string{5: "5 seconds", 260: "4 minutes", 7200: "2 hours", 3 * 86400: "3 days"} {
		if got := humanSeconds(in); got != want {
			t.Errorf("%d: %q", in, got)
		}
	}
	if N(1234567) != "1,234,567" || N(-1200) != "-1,200" {
		t.Fatal(N(1234567))
	}
}
