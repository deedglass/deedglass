package agency

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/weekly"
)

// fakeReader answers from a table and records every org it was asked about.
type fakeReader struct {
	rows  map[ids.OrgID]Row
	gone  map[ids.OrgID]bool // the person lost access since the membership list was read
	asked []ids.OrgID
}

func (f *fakeReader) Row(_ context.Context, org ids.OrgID, _ ids.UserID, _ weekly.Period, _ time.Time) (Row, bool, error) {
	f.asked = append(f.asked, org)
	if f.gone[org] {
		return Row{}, false, nil
	}
	r, ok := f.rows[org]
	return r, ok, nil
}

func newOrgs(n int) []ids.OrgID {
	out := make([]ids.OrgID, n)
	for i := range out {
		out[i] = ids.New[ids.OrgKind]()
	}
	return out
}

func TestClients(t *testing.T) {
	orgs := newOrgs(5)
	user := ids.New[ids.UserKind]()
	slugs := []string{"acme", "globex", "initech", "umbrella", "agency"}
	r := &fakeReader{rows: map[ids.OrgID]Row{}, gone: map[ids.OrgID]bool{}}
	for i, o := range orgs {
		r.rows[o] = Row{Object: "client", Org: o.String(), Slug: slugs[i], Name: strings.ToUpper(slugs[i][:1]) + slugs[i][1:]}
	}
	ms := []Membership{
		{OrgID: orgs[0], Role: "owner", Status: "active", OrgStatus: "active"},
		{OrgID: orgs[1], Role: "admin", Status: "active", OrgStatus: "active"},
		{OrgID: orgs[2], Role: "owner", Status: "active", OrgStatus: "active"},
		{OrgID: orgs[3], Role: "viewer", Status: "active", OrgStatus: "active"}, // a viewer isn't a client
		{OrgID: orgs[4], Role: "owner", Status: "removed", OrgStatus: "active"}, // removed
	}
	l, err := Clients(context.Background(), r, user, ms, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Data) != 3 || l.Total != 3 || l.HasMore || len(r.asked) != 3 {
		t.Fatalf("got %d rows (total %d), asked %d", len(l.Data), l.Total, len(r.asked))
	}
	for _, o := range r.asked {
		if o == orgs[3] || o == orgs[4] {
			t.Fatal("read an org the person isn't a client of")
		}
	}
	// The person lost access to one since: it disappears on this load.
	r.gone[orgs[1]] = true
	l, _ = Clients(context.Background(), r, user, ms, Options{})
	if len(l.Data) != 2 {
		t.Fatalf("lost access: %d rows", len(l.Data))
	}
	delete(r.gone, orgs[1])
	// --exclude by slug or id.
	l, _ = Clients(context.Background(), r, user, ms, Options{Exclude: []string{"acme", orgs[2].String()}})
	if len(l.Data) != 1 || l.Data[0].Slug != "globex" {
		t.Fatalf("exclude: %+v", l.Data)
	}
}

func TestClientsPaginated(t *testing.T) {
	orgs := newOrgs(201)
	user := ids.New[ids.UserKind]()
	r := &fakeReader{rows: map[ids.OrgID]Row{}}
	var ms []Membership
	for i, o := range orgs {
		r.rows[o] = Row{Org: o.String(), Slug: fmt.Sprintf("client-%03d", i)}
		ms = append(ms, Membership{OrgID: o, Role: "admin", Status: "active", OrgStatus: "active"})
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		l, err := Clients(context.Background(), r, user, ms, Options{Limit: 200, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, d := range l.Data {
			if seen[d.Org] {
				t.Fatal("a client listed twice")
			}
			seen[d.Org] = true
		}
		if !l.HasMore {
			break
		}
		cursor = *l.Next
	}
	if len(seen) != 201 || pages != 2 {
		t.Fatalf("%d clients over %d pages", len(seen), pages)
	}
	if _, err := Clients(context.Background(), r, user, ms, Options{Cursor: ids.New[ids.OrgKind]().String()}); !errors.Is(err, ErrCursor) {
		t.Fatal("a foreign cursor accepted")
	}
	l, _ := Clients(context.Background(), r, user, ms, Options{Limit: 5000})
	if len(l.Data) != MaxClients {
		t.Fatalf("limit not capped: %d", len(l.Data))
	}
}

func TestCheckLogo(t *testing.T) {
	png := append(append([]byte(nil), pngMagic...), bytes.Repeat([]byte{0}, 64)...)
	if m, err := CheckLogo(png); err != nil || m != "image/png" {
		t.Fatalf("png: %s %v", m, err)
	}
	clean := `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><defs><linearGradient id="g"/></defs><rect fill="url(#g)" width="10" height="10"/><use href="#g"/></svg>`
	if m, err := CheckLogo([]byte(clean)); err != nil || m != "image/svg+xml" {
		t.Fatalf("clean svg: %s %v", m, err)
	}
	for name, bad := range map[string]string{
		"script":        `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"handler":       `<svg xmlns="http://www.w3.org/2000/svg" onload="x()"><rect/></svg>`,
		"external href": `<svg xmlns="http://www.w3.org/2000/svg"><image href="https://evil.example/x.png"/></svg>`,
		"xlink href":    `<svg xmlns="http://www.w3.org/2000/svg"><a xlink:href="https://evil.example">x</a></svg>`,
		"css url":       `<svg xmlns="http://www.w3.org/2000/svg"><rect style="fill:url(https://evil.example/f)"/></svg>`,
		"import":        `<svg xmlns="http://www.w3.org/2000/svg"><style>@import 'x.css';</style></svg>`,
		"entity":        `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg>&x;</svg>`,
		"foreign":       `<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><div/></foreignObject></svg>`,
		"not an image":  `GIF89a`,
		"html":          `<html><svg></svg></html>`,
	} {
		if _, err := CheckLogo([]byte(bad)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	big := append(append([]byte(nil), pngMagic...), bytes.Repeat([]byte{1}, MaxLogoBytes)...)
	if _, err := CheckLogo(big); err == nil || !strings.Contains(err.Error(), "200 KB") {
		t.Fatalf("over 200 KB: %v", err)
	}
}

func TestLoadBrand(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("logo.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><circle r="4" cx="4" cy="4"/></svg>`)
	b, err := LoadBrand(write("brand.yaml", "name: Northwind Automation\ncontact_line: ops@northwind.example\nlogo_file: logo.svg\n"))
	if err != nil || b.Name != "Northwind Automation" || b.LogoMIME != "image/svg+xml" || len(b.Logo) == 0 {
		t.Fatalf("brand %+v %v", b, err)
	}
	write("bad.svg", `<svg><script/></svg>`)
	for name, body := range map[string]string{
		"no name":       "contact_line: x\n",
		"unknown key":   "name: N\ncolour: red\n",
		"script logo":   "name: N\nlogo_file: bad.svg\n",
		"missing logo":  "name: N\nlogo_file: nope.png\n",
		"long name":     "name: " + strings.Repeat("n", 81) + "\n",
		"control chars": "name: \"N\\u0007\"\n",
	} {
		if _, err := LoadBrand(write(name+".yaml", body)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func fixtureSummary(org Row) weekly.Summary {
	p, _ := weekly.ParsePeriod("2026-10", time.UTC)
	return weekly.Build(weekly.Facts{Org: weekly.Org{ID: org.Org, Name: org.Name, Slug: org.Slug}, Period: p,
		Now:       time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
		Decisions: []weekly.AgentDecision{{AgentID: "agt_x", Name: "ops-agent", Decision: "allow", Count: 40}},
		Verdicts:  map[string]int64{"VERIFIED": 30}, Chain: weekly.Chain{Status: weekly.ChainIntact}})
}

// TestBulkEqualsSingle: a bulk report is the client's single-org report plus the brand lines.
func TestBulkEqualsSingle(t *testing.T) {
	dir := t.TempDir()
	clients := []Row{{Org: "org_a", Name: "Acme", Slug: "acme"}, {Org: "org_g", Name: "Globex", Slug: "globex"}}
	p, _ := weekly.ParsePeriod("2026-10", time.UTC)
	brand := &weekly.Brand{Name: "Northwind Automation", ContactLine: "ops@northwind.example"}
	res, err := WriteReports(context.Background(), dir, clients, p, nil, brand, func(_ context.Context, c Row) (weekly.Summary, error) {
		return fixtureSummary(c), nil
	}, weekly.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 6 || res.Index != "index.md" {
		t.Fatalf("files %v", res.Files)
	}
	for _, c := range clients {
		single := fixtureSummary(c)
		md, _ := os.ReadFile(filepath.Join(dir, c.Slug+"-2026-10.md"))
		got := strings.Replace(string(md), "Prepared by Northwind Automation with Deedglass.  \nops@northwind.example  \n\n", "", 1)
		if got != weekly.Markdown(single, weekly.Options{}) {
			t.Fatalf("%s: md differs beyond the brand lines\n%s", c.Slug, md)
		}
		h, _ := os.ReadFile(filepath.Join(dir, c.Slug+"-2026-10.html"))
		want, _ := weekly.HTML(single, weekly.Options{})
		brandLine := regexp.MustCompile(`(?s)\s*<p class="brand">.*?</p>`)
		if brandLine.ReplaceAllString(string(h), "") != brandLine.ReplaceAllString(want, "") || !brandLine.Match(h) {
			t.Fatalf("%s: html differs beyond the brand lines", c.Slug)
		}
		j, _ := os.ReadFile(filepath.Join(dir, c.Slug+"-2026-10.json"))
		wj, _ := weekly.JSON(single)
		if !bytes.Equal(j, wj) {
			t.Fatalf("%s: json differs", c.Slug)
		}
	}
	idx, _ := os.ReadFile(filepath.Join(dir, "index.md"))
	if !strings.Contains(string(idx), "Prepared by Northwind Automation with Deedglass.") || !strings.Contains(string(idx), "acme-2026-10.html") {
		t.Fatal(string(idx))
	}
	// Never overwrites.
	if _, err := WriteReports(context.Background(), dir, clients, p, []string{"md"}, nil, func(_ context.Context, c Row) (weekly.Summary, error) {
		return fixtureSummary(c), nil
	}, weekly.Options{}); !errors.Is(err, ErrExists) {
		t.Fatalf("overwrite: %v", err)
	}
}

// TestNoCrossOrgReads keeps ee/agency to org-scoped reads: no system transaction, no cross-org
// function, no pre-auth lookup.
func TestNoCrossOrgReads(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	bad := regexp.MustCompile(`store\.Sys|SystemTx|SECURITY DEFINER|preauth_|claim_[a-z_]+\(|maint_[a-z_]+\(|OrgIDs\(|PreAuth\(|set_config\('deedglass\.org_id'`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if m := bad.Find(b); m != nil {
			t.Errorf("%s uses %q: the Clients view reads each org in its own OrgReadTx only", f, m)
		}
	}
}
