//go:build integration

package pgsource_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
	"github.com/deedglass/deedglass/internal/mailer"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
	"github.com/deedglass/deedglass/internal/weekly"
	"github.com/deedglass/deedglass/internal/weekly/pgsource"
	"github.com/deedglass/deedglass/internal/weeklyapi"
)

// The canaries: an argument, a result value and a member's email. None may appear in a summary
// or any rendering of it.
const (
	canaryArg    = "canary-s52-argument-7731"
	canaryResult = "canary-s52-result-4410"
	canaryEmail  = "canary-s52-member@acme.example.test"
)

type world struct {
	e    *storetest.Env
	led  *ledger.Ledger
	acme ids.OrgID
	glob ids.OrgID
	src  *pgsource.Source
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	e := storetest.New(t, storetest.Options{MaxConns: 8})
	seed, err := deed.NewSeed(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.DB.RegisterSealKey(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	led := ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(slog.New(slog.DiscardHandler)))
	w := &world{e: e, led: led, src: &pgsource.Source{DB: e.DB, Ledger: led}}
	newOrg := func(slug string, kinds []fake.Kind, n int) ids.OrgID {
		o, _, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: "Org " + slug, Slug: slug, OwnerEmail: "priya@" + slug + ".example.test"})
		if err != nil {
			t.Fatal(err)
		}
		var agents []store.Agent
		if err := e.DB.OrgTx(ctx, o.ID, func(ctx context.Context, tx pgx.Tx) error {
			for _, name := range fake.AgentNames[:2] {
				a, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: name, CreatedBy: "system"})
				if err != nil {
					return err
				}
				agents = append(agents, a)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		plans := fake.Generate(52, n, kinds, 2)
		for i := range plans {
			plans[i].Args = json.RawMessage(`{"message":"` + canaryArg + `"}`)
			if plans[i].Result != nil {
				plans[i].Result = json.RawMessage(`{"echo":"` + canaryResult + `"}`)
			}
		}
		if _, err := fake.Run(ctx, led, o.ID, agents, plans, fake.Options{}); err != nil {
			t.Fatal(err)
		}
		return o.ID
	}
	w.acme = newOrg("acme", []fake.Kind{fake.Allow, fake.Deny, fake.Mismatch, fake.Unverifiable}, 40)
	w.glob = newOrg("globex", []fake.Kind{fake.Allow}, 7)
	// A second owner of acme whose email is a canary.
	c := e.Admin(t)
	u := ids.New[ids.UserKind]()
	if _, err := c.Exec(ctx, `INSERT INTO deedglass.users (id, email, status) VALUES ($1, $2, 'active')`, u, canaryEmail); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at) VALUES ($1, $2, $3, 'owner', 'active', now())`,
		ids.New[ids.MembershipKind](), w.acme, u); err != nil {
		t.Fatal(err)
	}
	return w
}

// TestSummaryLive: the summary of a real week, every rendering free of the canaries, and the
// other org's actions never counted.
func TestSummaryLive(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	now := time.Now().UTC()
	p := weekly.WeekOf(now, time.UTC)
	s, err := w.src.Summarize(ctx, w.acme, p, now)
	if err != nil {
		t.Fatal(err)
	}
	if s.Actions.Total != 40 || s.Org.Slug != "acme" || s.Check.DoesntMatch == 0 || s.Actions.Blocked == 0 {
		t.Fatalf("summary %+v", s.Actions)
	}
	if s.CaughtTotal == 0 || s.Chain.Status != weekly.ChainIntact {
		t.Fatalf("caught %d chain %+v", s.CaughtTotal, s.Chain)
	}
	g, err := w.src.Summarize(ctx, w.glob, p, now)
	if err != nil || g.Actions.Total != 7 || g.CaughtTotal != 0 {
		t.Fatalf("globex %+v %v", g.Actions, err)
	}
	js, _ := weekly.JSON(s)
	h, _ := weekly.HTML(s, weekly.Options{ProUndoLine: true})
	m, err := weekly.Email(s, weekly.Options{ProUndoLine: true}, "priya@acme.example.test", "https://x.example/app", "https://x.example/weekly/off?t=a.b")
	if err != nil {
		t.Fatal(err)
	}
	all := string(js) + h + weekly.Markdown(s, weekly.Options{}) + strings.Join(weekly.TextLines(s, weekly.Options{}), "\n") + m.Text + m.HTML + m.Subject
	for _, c := range []string{canaryArg, canaryResult, canaryEmail} {
		if strings.Contains(all, c) {
			t.Fatalf("a rendering holds the canary %q", c)
		}
	}
	t.Logf("canary: 0 hits across json, html, md, text and the email\n%s", strings.Join(weekly.TextLines(s, weekly.Options{ProUndoLine: true}), "\n"))
	// An empty week before anything happened.
	old := weekly.WeekOf(now.AddDate(0, 0, -70), time.UTC)
	o, err := w.src.Summarize(ctx, w.acme, old, now)
	if err != nil || o.Actions.Total != 0 || o.CaughtTotal != 0 {
		t.Fatalf("empty week %+v %v", o.Actions, err)
	}
}

type captureQueue struct{ q *mailer.Queue }

func (c captureQueue) Enqueue(m mailer.Message) bool { return c.q.Enqueue(m) }

// TestMondayJobLive: the job sends the week to acme's owners through the log mailer, once; the
// one-click page turns one owner's email off; next week that owner gets nothing.
func TestMondayJobLive(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	var buf bytes.Buffer
	lm, err := mailer.NewLog(&buf, false)
	if err != nil {
		t.Fatal(err)
	}
	q := mailer.NewQueue(lm, mailer.QueueOptions{Logger: slog.New(slog.DiscardHandler)})
	q.Start(ctx)
	key := weekly.OffKeyFrom([]byte("pepper-for-the-s52-integration-test"))
	// Next Monday 09:00 UTC: the job covers the week in which the fake actions were made.
	now := time.Now().UTC()
	this := weekly.WeekOf(now, time.UTC)
	monday := this.To.Add(9 * time.Hour)
	j := &weekly.Job{Store: w.src, Mail: captureQueue{q}, BaseURL: "https://deedglass.acme.internal", OffKey: key,
		ProUndoLine: func() bool { return true }, Now: func() time.Time { return monday }, Log: slog.New(slog.DiscardHandler)}
	j.Tick(ctx)
	j.Tick(ctx)
	q.Close()
	out := buf.String()
	if n := strings.Count(out, "What your agents did this week · 40 actions"); n != 2 { // two acme owners, once each
		t.Fatalf("acme emails: %d\n%s", n, out)
	}
	if !strings.Contains(out, "What your agents did this week · 7 actions · nothing caught") {
		t.Fatalf("globex email missing\n%s", out)
	}
	for _, c := range []string{canaryArg, canaryResult} {
		if strings.Contains(out, c) {
			t.Fatalf("an email holds %q", c)
		}
	}
	var runs int
	_ = w.e.DB.OrgReadTx(ctx, w.acme, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.learn_runs WHERE org_id = $1 AND kind = 'weekly' AND status = 'succeeded'`, w.acme).Scan(&runs)
	})
	if runs != 1 {
		t.Fatalf("weekly runs %d", runs)
	}
	// The off link of priya's email, through the real routes.
	i := strings.Index(out, "Stop these emails: https://deedglass.acme.internal/weekly/off?t=")
	if i < 0 {
		t.Fatal("no off link")
	}
	link := strings.Fields(out[i+len("Stop these emails: "):])[0]
	u, _ := url.Parse(link)
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler)})
	api.Mount(weeklyapi.Routes(&weeklyapi.API{Source: w.src, Mailer: true, OffKey: key})...)
	r := httptest.NewRequest("GET", u.RequestURI(), nil)
	rw := httptest.NewRecorder()
	api.ServeHTTP(rw, r)
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), "Stop these emails") || !strings.Contains(rw.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("page %d %s", rw.Code, rw.Body)
	}
	org, user, _ := weekly.ParseOffToken(key, u.Query().Get("t"))
	if on, _, _ := w.src.EmailSetting(ctx, org, user); !on {
		t.Fatal("opening the page changed the setting")
	}
	r = httptest.NewRequest("POST", "/weekly/off", strings.NewReader(url.Values{"t": {u.Query().Get("t")}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rw = httptest.NewRecorder()
	api.ServeHTTP(rw, r)
	if rw.Code != 200 || !strings.Contains(rw.Body.String(), "Done") {
		t.Fatalf("off %d %s", rw.Code, rw.Body)
	}
	if on, _, _ := w.src.EmailSetting(ctx, org, user); on {
		t.Fatal("the setting is still on")
	}
	// A changed token is refused.
	r = httptest.NewRequest("POST", "/weekly/off", strings.NewReader("t="+u.Query().Get("t")+"x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rw = httptest.NewRecorder()
	api.ServeHTTP(rw, r)
	if rw.Code != 400 {
		t.Fatalf("bad token %d", rw.Code)
	}
	// The next week: that owner is skipped.
	people, err := w.src.Recipients(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range people {
		if p.UserID == user {
			t.Fatal("an owner who turned the email off is still a recipient")
		}
	}
	// A viewer never receives it.
	if err := w.e.DB.OrgTx(ctx, w.glob, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE deedglass.memberships SET weekly_email = true WHERE org_id = $1`, w.glob)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if ps, _ := w.src.Recipients(ctx, w.glob); len(ps) != 1 {
		t.Fatalf("globex recipients %d", len(ps))
	}
}

// TestNoMailerNoSend: without a mailer nothing is claimed or sent.
func TestNoMailerNoSend(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	monday := weekly.WeekOf(time.Now().UTC(), time.UTC).To.Add(9 * time.Hour)
	(&weekly.Job{Store: w.src, BaseURL: "https://x.example", OffKey: weekly.OffKeyFrom([]byte("p-0123456789abcdef")),
		Now: func() time.Time { return monday }}).Tick(ctx)
	var runs int
	_ = w.e.DB.OrgReadTx(ctx, w.acme, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM deedglass.learn_runs WHERE kind = 'weekly'`).Scan(&runs)
	})
	if runs != 0 {
		t.Fatalf("runs %d without a mailer", runs)
	}
	_ = http.StatusOK
}
