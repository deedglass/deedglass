//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/ee/agency"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/ledger/fake"
	"github.com/deedglass/deedglass/internal/licence"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
	"github.com/deedglass/deedglass/internal/weekly"
)

const reportCanary = "canary-s52-cli-argument-2207"

// TestReportAndClientsLive is S52's CLI acceptance on a throwaway cluster: the free single-org
// report (md, html, json; refuses to overwrite; no canary), pro_required for clients, --all-clients
// and --brand without the agency feature, then with a test agency licence: clients lists exactly
// the person's client orgs, and --all-clients --brand writes one report per client that equals the
// single-org report plus the brand lines.
func TestReportAndClientsLive(t *testing.T) {
	ctx := context.Background()
	e := storetest.New(t, storetest.Options{MaxConns: 8})
	seed, _ := deed.NewSeed(rand.Reader)
	signer, _ := deed.NewFileSigner(ids.New[ids.SealKeyKind]().String(), seed)
	if _, _, err := e.DB.RegisterSealKey(ctx, signer.KeyID(), signer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	led := ledger.New(e.DB, deed.NewSealer(signer), signer, ledger.WithDev(true), ledger.WithLogger(slog.New(slog.DiscardHandler)))
	const person = "ops@northwind.example.test"
	for i, slug := range []string{"northwind", "acme", "globex", "initech"} {
		o, _, _, err := e.DB.CreateOrg(ctx, store.NewOrg{Name: strings.ToUpper(slug[:1]) + slug[1:], Slug: slug, OwnerEmail: person})
		if err != nil {
			t.Fatal(err)
		}
		if slug == "initech" {
			continue
		}
		var agents []store.Agent
		_ = e.DB.OrgTx(ctx, o.ID, func(ctx context.Context, tx pgx.Tx) error {
			a, err := store.CreateAgent(ctx, tx, o.ID, store.NewAgent{Name: "ops-bot", CreatedBy: "system"})
			agents = append(agents, a)
			return err
		})
		plans := fake.Generate(uint64(90+i), 10, []fake.Kind{fake.Allow, fake.Deny}, 1)
		for j := range plans {
			plans[j].Args = json.RawMessage(`{"q":"` + reportCanary + `"}`)
		}
		if _, err := fake.Run(ctx, led, o.ID, agents, plans, fake.Options{}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	saved := reportNow
	reportNow = func() time.Time { return now }
	t.Cleanup(func() { reportNow = saved })
	week := weekly.WeekOf(now, time.UTC).Key
	month := weekly.MonthOf(now, time.UTC).Key
	env := map[string]string{"NO_COLOR": "1", "DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_ENV": "dev"}
	run := func(env map[string]string, args ...string) result {
		t.Helper()
		return cli(t, env, false, args...)
	}

	// 1. The free single-org report.
	r := run(env, "--org", "acme", "report", "--week", week)
	if r.code != 0 || !strings.Contains(r.stdout, "# What your agents did · Acme · ") || !strings.Contains(r.stdout, "10 actions") {
		t.Fatalf("report md: %+v", r)
	}
	t.Logf("deedglass --org acme report --week %s\n%s", week, r.stdout)
	all := r.stdout
	for _, f := range []string{"html", "json"} {
		r = run(env, "--org", "acme", "report", "--week", week, "--format", f)
		if r.code != 0 {
			t.Fatalf("report %s: %+v", f, r)
		}
		all += r.stdout
	}
	if strings.Contains(all, reportCanary) {
		t.Fatal("a report holds the argument canary")
	}
	out := filepath.Join(t.TempDir(), "acme.html")
	if r = run(env, "--org", "acme", "report", "--format", "html", "--out", out); r.code != 0 {
		t.Fatalf("--out: %+v", r)
	}
	if r = run(env, "--org", "acme", "report", "--format", "html", "--out", out); r.code == 0 || !strings.Contains(r.stderr, "already exists") {
		t.Fatalf("overwrite: %+v", r)
	}
	if r = run(env, "--org", "acme", "report", "--week", "2026-W99"); r.code != ExitUsage {
		t.Fatalf("bad week: %+v", r)
	}

	// 2. Without the agency feature: pro_required and the free line.
	_, lenv, _ := licenceSeams(t)
	for k, v := range env {
		lenv[k] = v
	}
	brand := filepath.Join(t.TempDir(), "brand.yaml")
	_ = os.WriteFile(brand, []byte("name: Northwind Automation\ncontact_line: ops@northwind.example.test\n"), 0o600)
	for _, args := range [][]string{{"clients", "--as", person}, {"report", "--all-clients", "--out", t.TempDir(), "--as", person},
		{"--org", "acme", "report", "--brand", brand}} {
		r = run(lenv, args...)
		if r.code != 1 || !strings.Contains(r.stderr, "Agency plan") || !strings.Contains(r.stderr, "deedglass report --org") {
			t.Fatalf("%v without agency: %+v", args, r)
		}
	}
	t.Logf("without agency:\n%s", r.stderr)

	// 3. A Pro licence is not enough: agency comes with the agency_monthly sku only.
	f, lenv2, _ := licenceSeams(t)
	for k, v := range env {
		lenv2[k] = v
	}
	f.plan = licence.PlanPro
	if r = run(lenv2, "licence", "activate", "DG-PRO-S52-0001"); r.code != 0 {
		t.Fatalf("activate pro: %+v", r)
	}
	if r = run(lenv2, "clients", "--as", person); r.code != 1 || !strings.Contains(r.stderr, "Agency plan") {
		t.Fatalf("plain Pro: %+v", r)
	}
	f.sku = licence.SkuAgencyMonthly
	if r = run(lenv2, "licence", "refresh"); r.code != 0 {
		t.Fatalf("refresh agency: %+v", r)
	}
	r = run(lenv2, "clients", "--as", person, "--exclude", "northwind")
	if r.code != 0 || !strings.Contains(r.stdout, "Clients · 3 · this week") || !strings.Contains(r.stdout, "acme") ||
		!strings.Contains(r.stdout, "initech") || !strings.Contains(r.stdout, "no activity this week") || strings.Contains(r.stdout, "northwind ") {
		t.Fatalf("clients: %+v", r)
	}
	t.Logf("deedglass clients --exclude northwind\n%s", r.stdout)
	r = run(lenv2, "--json", "clients", "--as", person)
	var l agency.List
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &l) != nil || len(l.Data) != 4 {
		t.Fatalf("clients --json: %+v", r)
	}

	// 4. Every client's monthly report, branded; each equals its single-org report plus the brand lines.
	dir := filepath.Join(t.TempDir(), "reports")
	r = run(lenv2, "report", "--all-clients", "--month", month, "--brand", brand, "--out", dir, "--as", person, "--exclude", "northwind")
	if r.code != 0 || !strings.Contains(r.stdout, "3 client reports") {
		t.Fatalf("--all-clients: %+v", r)
	}
	t.Logf("%s", r.stdout)
	files, _ := os.ReadDir(dir)
	var names []string
	for _, fi := range files {
		names = append(names, fi.Name())
	}
	t.Logf("bulk folder: %v", names)
	if len(names) != 10 {
		t.Fatalf("files %v", names)
	}
	for _, slug := range []string{"acme", "globex", "initech"} {
		single := run(env, "--org", slug, "report", "--month", month)
		got, _ := os.ReadFile(filepath.Join(dir, slug+"-"+month+".md"))
		if strings.Replace(string(got), "Prepared by Northwind Automation with Deedglass.  \nops@northwind.example.test  \n\n", "", 1) != single.stdout {
			t.Fatalf("%s: the bulk report differs from deedglass report --org %s beyond the brand lines", slug, slug)
		}
	}
	bad := filepath.Join(filepath.Dir(brand), "bad.yaml")
	_ = os.WriteFile(filepath.Join(filepath.Dir(brand), "logo.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>x()</script></svg>`), 0o600)
	_ = os.WriteFile(bad, []byte("name: N\nlogo_file: logo.svg\n"), 0o600)
	if r = run(lenv2, "--org", "acme", "report", "--brand", bad); r.code != ExitUsage || !strings.Contains(r.stderr, "a script") {
		t.Fatalf("svg with a script: %+v", r)
	}
}
