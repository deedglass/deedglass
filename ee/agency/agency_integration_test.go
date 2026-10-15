//go:build integration

package agency

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
	"github.com/deedglass/deedglass/internal/weekly"
	"github.com/deedglass/deedglass/internal/weekly/pgsource"
)

// spyDB wraps the store: every read must arrive through OrgReadTx, and inside it the transaction's
// own org setting must name that org (RLS applies to that org only).
type spyDB struct {
	db    *store.DB
	t     *testing.T
	mu    sync.Mutex
	calls map[ids.OrgID]int
}

func (s *spyDB) OrgReadTx(ctx context.Context, org ids.OrgID, fn store.TxFunc) error {
	s.mu.Lock()
	s.calls[org]++
	s.mu.Unlock()
	return s.db.OrgReadTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var set string
		if err := tx.QueryRow(ctx, `SELECT pg_catalog.current_setting('deedglass.org_id', true)`).Scan(&set); err != nil {
			return err
		}
		if set != org.String() {
			s.t.Errorf("a read for %s ran with deedglass.org_id %q", org, set)
		}
		return fn(ctx, tx)
	})
}

// sessionAuth signs every request in as one person in one org.
type sessionAuth struct {
	org  ids.OrgID
	user ids.UserID
}

func (a sessionAuth) AuthenticateSession(_ context.Context, r *http.Request) (httpx.Principal, error) {
	if _, err := r.Cookie("deedglass_session"); err != nil {
		return httpx.Principal{}, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return httpx.Principal{Kind: httpx.KindUser, OrgID: a.org, UserID: a.user, Role: httpx.RoleOwner, OrgName: "Agency", APIVersion: "2026-10-01"}, nil
}

func TestClientsLive(t *testing.T) {
	ctx := context.Background()
	e := storetest.New(t, storetest.Options{MaxConns: 8})
	seed, _ := deed.NewSeed(rand.Reader)
	signer, _ := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if _, _, err := e.DB.RegisterSealKey(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	led := ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(slog.New(slog.DiscardHandler)))
	const person = "ops@northwind.example.test"
	orgs := map[string]ids.OrgID{}
	var user ids.UserID
	for i, slug := range []string{"northwind", "acme", "globex", "initech", "umbrella"} {
		owner := person
		if slug == "umbrella" {
			owner = "someone@umbrella.example.test" // the person is only a viewer here
		}
		o, u, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: strings.ToUpper(slug[:1]) + slug[1:], Slug: slug, OwnerEmail: owner})
		if err != nil {
			t.Fatal(err)
		}
		orgs[slug] = o.ID
		if owner == person {
			user = u.ID
		}
		if slug == "acme" || slug == "globex" {
			var agents []store.Agent
			_ = e.DB.OrgTx(ctx, o.ID, func(ctx context.Context, tx pgx.Tx) error {
				a, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: "ops-bot", CreatedBy: "system"})
				agents = append(agents, a)
				return err
			})
			plans := fake.Generate(uint64(60+i), 12, []fake.Kind{fake.Allow}, 1)
			if slug == "acme" {
				plans = append(fake.Generate(uint64(70+i), 8, []fake.Kind{fake.Allow}, 1), fake.Generate(uint64(80+i), 4, []fake.Kind{fake.Mismatch}, 1)...)
			}
			if _, err := fake.Run(ctx, led, o.ID, agents, plans, fake.Options{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	c := e.Admin(t)
	if _, err := c.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at) VALUES ($1, $2, $3, 'viewer', 'active', now())`,
		ids.New[ids.MembershipKind](), orgs["umbrella"], user); err != nil {
		t.Fatal(err)
	}
	memberships := func() []Membership {
		u, err := e.DB.PreAuth().UserByEmail(ctx, person)
		if err != nil {
			t.Fatal(err)
		}
		var out []Membership
		for _, m := range u.Memberships {
			out = append(out, Membership{OrgID: m.OrgID, Role: m.Role, Status: m.Status, OrgStatus: m.OrgStatus})
		}
		return out
	}
	spy := &spyDB{db: e.DB, t: t, calls: map[ids.OrgID]int{}}
	now := time.Now().UTC()
	p := weekly.WeekOf(now, time.UTC)
	l, err := Clients(ctx, PG{DB: spy, Ledger: led}, user, memberships(), Options{Exclude: []string{"northwind"}, Period: p, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	slugs := []string{}
	for _, r := range l.Data {
		slugs = append(slugs, r.Slug)
	}
	if len(l.Data) != 3 || !(strings.Contains(strings.Join(slugs, ","), "acme") && strings.Contains(strings.Join(slugs, ","), "globex") && strings.Contains(strings.Join(slugs, ","), "initech")) {
		t.Fatalf("clients %v", slugs)
	}
	if spy.calls[orgs["umbrella"]] != 0 {
		t.Fatal("the viewer's org was read")
	}
	for slug, id := range orgs {
		if slug != "umbrella" && spy.calls[id] != 1 {
			t.Errorf("%s: %d org reads, want exactly 1", slug, spy.calls[id])
		}
	}
	t.Logf("store spy: %d OrgReadTx calls, each under its own deedglass.org_id: %v", len(spy.calls), spy.calls)
	for _, r := range l.Data {
		switch r.Slug {
		case "acme":
			if r.Actions != 12 || r.DoesntMatch == 0 || r.Caught == 0 || r.Chain != weekly.ChainIntact {
				t.Errorf("acme %+v", r)
			}
		case "initech":
			if r.Actions != 0 || r.LastActivity != nil || r.Chain != weekly.ChainNoRecords && r.Chain != weekly.ChainIntact {
				t.Errorf("initech %+v", r)
			}
		}
	}
	// The membership list still names globex, but the person was removed there (another owner keeps
	// the org): gone on this load.
	other := ids.New[ids.UserKind]()
	if _, err := c.Exec(ctx, `INSERT INTO deedglass.users (id, email, status) VALUES ($1, 'sam@globex.example.test', 'active')`, other); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `INSERT INTO deedglass.memberships (id, org_id, user_id, role, status, joined_at) VALUES ($1, $2, $3, 'owner', 'active', now())`,
		ids.New[ids.MembershipKind](), orgs["globex"], other); err != nil {
		t.Fatal(err)
	}
	stale := memberships()
	if _, err := c.Exec(ctx, `UPDATE deedglass.memberships SET status = 'removed', removed_at = now() WHERE org_id = $1 AND user_id = $2`, orgs["globex"], user); err != nil {
		t.Fatal(err)
	}
	l, err = Clients(ctx, PG{DB: e.DB, Ledger: led}, user, stale, Options{Exclude: []string{"northwind"}, Period: p, Now: now})
	if err != nil || len(l.Data) != 2 {
		t.Fatalf("after removal: %d rows %v", len(l.Data), err)
	}

	// GET /v1/clients: pro_required without agency, the list with it.
	allowed := false
	a := &API{Reader: PG{DB: e.DB, Ledger: led}, Allowed: func() bool { return allowed }, Now: func() time.Time { return now },
		Memberships: func(context.Context, ids.OrgID, ids.UserID) ([]Membership, error) { return memberships(), nil }}
	api := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Sessions: sessionAuth{org: orgs["northwind"], user: user}})
	api.Mount(Routes(a)...)
	get := func(q string) (int, string) {
		r := httptest.NewRequest("GET", "/v1/clients"+q, nil)
		r.AddCookie(&http.Cookie{Name: "deedglass_session", Value: "x"})
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	if code, body := get(""); code != 403 || !strings.Contains(body, `"pro_required"`) || !strings.Contains(body, "deedglass report --org") {
		t.Fatalf("no agency: %d %s", code, body)
	}
	allowed = true
	code, body := get("?exclude=northwind")
	var list List
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil || list.Object != "list" || len(list.Data) != 2 {
		t.Fatalf("clients: %d %s", code, body)
	}
	if code, _ := get("?limit=500"); code != 400 {
		t.Fatalf("limit: %d", code)
	}

	// Bulk reports equal each client's single-org report plus the brand lines.
	src := &pgsource.Source{DB: e.DB, Ledger: led}
	dir := t.TempDir()
	brand := &weekly.Brand{Name: "Northwind Automation", ContactLine: "ops@northwind.example.test"}
	res, err := WriteReports(ctx, dir, list.Data, p, []string{FormatMD, FormatJSON}, brand, func(ctx context.Context, r Row) (weekly.Summary, error) {
		id, _ := ids.Parse[ids.OrgKind](r.Org)
		return src.Summarize(ctx, id, p, now)
	}, weekly.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list.Data {
		id, _ := ids.Parse[ids.OrgKind](r.Org)
		single, _ := src.Summarize(ctx, id, p, now)
		got, _ := os.ReadFile(filepath.Join(dir, r.Slug+"-"+p.Key+".md"))
		want := weekly.Markdown(single, weekly.Options{})
		if strings.Replace(string(got), "Prepared by Northwind Automation with Deedglass.  \nops@northwind.example.test  \n\n", "", 1) != want {
			t.Fatalf("%s: the bulk report differs from the single-org report beyond the brand lines", r.Slug)
		}
	}
	t.Logf("bulk folder: %v + %s", res.Files, res.Index)
}
