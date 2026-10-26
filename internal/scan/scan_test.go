package scan

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/policy"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noConnect(t *testing.T) Connector {
	return func(context.Context, *pgx.ConnConfig) (*pgx.Conn, error) {
		t.Error("the file part opened a connection")
		return nil, errors.New("no network in the file part")
	}
}

func run(t *testing.T, o Options) *Report {
	t.Helper()
	if o.Connect == nil {
		o.Connect = noConnect(t)
	}
	if o.DirLabel == "" {
		o.DirLabel = "fixture"
	}
	r, err := Scan(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const npxArchived = `{"mcpServers":{"pg":{"command":"npx","args":["-y","%s"]}}}`

func TestConfigShapes(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	write(t, root, ".cursor/mcp.json", `{"mcpServers":{"pg":{"command":"npx","args":["-y","@modelcontextprotocol/server-postgres@0.6.2"],"env":{"DATABASE_URL":"postgres://x:canary@h/db"}}}}`)
	write(t, root, ".vscode/mcp.json", `{"servers":{"neon":{"command":"npx","args":["@neondatabase/mcp-server-neon"]}}}`)
	write(t, root, ".mcp.json", `{"mcpServers":{"cc":{"command":"uvx","args":["postgres-mcp","--access-mode=unrestricted"]}},"servers":{"vs":{"command":"docker","args":["run","-i","--rm","-e","DATABASE_URI","crystaldba/postgres-mcp:0.3.0"]}}}`)
	write(t, root, "sub/.mcp.json", `{"servers":{"only":{"url":"https://deedglass.example/mcp/native"}}}`)
	write(t, root, "claude_desktop_config.json", `{"mcpServers":{"w":{"command":"/usr/local/bin/deedglass","args":["mcp","wrap","--connector","orders","--","npx","-y","@supabase/mcp-server-supabase@0.13.0"]}}}`)
	write(t, home, ".cursor/mcp.json", `{"mcpServers":{"u":{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/tmp"]}}}`)
	write(t, home, ".codeium/windsurf/mcp_config.json", `{"mcpServers":{"old":{"command":"npx","args":["stripe"]}}}`)
	write(t, home, ".config/devin/mcp_config.json", `{"mcpServers":{"new":{"command":"npx","args":["-y","@stripe/mcp"]}}}`)
	write(t, home, "Library/Application Support/Claude/claude_desktop_config.json", `{"mcpServers":{"cd":{"command":"npx","args":["@modelcontextprotocol/server-postgres"]}}}`)
	r := run(t, Options{Dir: root, Home: home, UserConfigs: true, GOOS: "darwin"})
	got := map[string]string{}
	for _, c := range r.Configs {
		var s []string
		for _, sv := range c.Servers {
			s = append(s, fmt.Sprintf("%s=%s@%s/%s/routed=%v/env=%s", sv.Name, sv.Package, sv.Version, sv.Transport, sv.Routed, strings.Join(sv.EnvNames, "+")))
		}
		got[c.Path+" "+c.Client] = strings.Join(s, ";")
	}
	want := map[string]string{
		".cursor/mcp.json cursor":                      "pg=@modelcontextprotocol/server-postgres@0.6.2/stdio/routed=false/env=DATABASE_URL",
		".vscode/mcp.json vscode":                      "neon=@neondatabase/mcp-server-neon@/stdio/routed=false/env=",
		".mcp.json claude-code+vscode":                 "cc=postgres-mcp@/stdio/routed=false/env=;vs=crystaldba/postgres-mcp@0.3.0/stdio/routed=false/env=",
		"sub/.mcp.json vscode":                         "only=deedglass@/http/routed=true/env=",
		"claude_desktop_config.json claude-desktop":    "w=@supabase/mcp-server-supabase@0.13.0/stdio/routed=true/env=",
		"~/.cursor/mcp.json cursor":                    "u=@modelcontextprotocol/server-filesystem@/stdio/routed=false/env=",
		"~/.codeium/windsurf/mcp_config.json windsurf": "old=stripe@/stdio/routed=false/env=",
		"~/.config/devin/mcp_config.json windsurf":     "new=@stripe/mcp@/stdio/routed=false/env=",
		"~/Library/Application Support/Claude/claude_desktop_config.json claude-desktop": "cd=@modelcontextprotocol/server-postgres@/stdio/routed=false/env=",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s:\n got %q\nwant %q", k, got[k], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("%d configs: %v", len(got), got)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "canary") {
		t.Fatal("an env value reached the report")
	}
}

func TestArchivedEveryLocation(t *testing.T) {
	root := t.TempDir()
	for i, v := range []string{"@modelcontextprotocol/server-postgres@0.6.2", "@modelcontextprotocol/server-postgres@latest", "@modelcontextprotocol/server-postgres"} {
		write(t, root, fmt.Sprintf("p%d/.cursor/mcp.json", i), fmt.Sprintf(npxArchived, v))
	}
	write(t, root, "package.json", `{"devDependencies":{"@modelcontextprotocol/server-postgres":"*"}}`)
	r := run(t, Options{Dir: root})
	var paths []string
	for _, f := range r.Findings {
		if f.ID == "archived_postgres_mcp" {
			paths = append(paths, f.Paths...)
			if f.Severity != "medium" || f.Detail != ArchivedDetail || f.Fix[0] != ArchivedFix1 || f.Fix[1] != ArchivedFix2 || len(f.Sources) != 2 {
				t.Errorf("%+v", f)
			}
		}
	}
	if strings.Join(paths, ",") != "p0/.cursor/mcp.json,p1/.cursor/mcp.json,p2/.cursor/mcp.json,package.json" {
		t.Fatalf("%v", paths)
	}
}

func TestArchivedSeverityByRole(t *testing.T) {
	base := func(pg *Postgres) *Report {
		return &Report{Configs: []Config{{Path: ".cursor/mcp.json", Servers: []Server{{Name: "pg", Package: ArchivedPostgres}}}}, Postgres: pg}
	}
	writer := &Postgres{Role: "app_rw", EnvVar: "DEMO_DSN", WALLevel: "logical", Tables: []Table{{Schema: "public", Table: "orders", Can: []string{"UPDATE"}, HasPK: true}}}
	reader := &Postgres{Role: "app_ro", EnvVar: "DEMO_DSN", WALLevel: "logical"}
	for _, c := range []struct {
		pg   *Postgres
		want string
	}{{writer, "high"}, {nil, "medium"}, {reader, "low"}, {&Postgres{Role: "su", EnvVar: "X", WALLevel: "logical", RoleAttributes: RoleAttributes{Superuser: true}}, "high"}} {
		fs := findings(base(c.pg), Options{})
		var got string
		for _, f := range fs {
			if f.ID == "archived_postgres_mcp" {
				got = f.Severity
				if c.pg != nil && f.Checked != "Checked role "+c.pg.Role+" from "+c.pg.EnvVar+". If the server uses a different role, check that one." {
					t.Errorf("checked line %q", f.Checked)
				}
			}
		}
		if got != c.want {
			t.Errorf("%+v: %s, want %s", c.pg, got, c.want)
		}
	}
}

func TestEnvNamesNeverValues(t *testing.T) {
	root := t.TempDir()
	write(t, root, ".env", "DATABASE_URL=postgres://u:canary-env-1@h/db\nexport DIRECT_URL=postgres://x\nPGPASSWORD=canary-env-2\nOTHER=1\n# NEON_DATABASE_URL=commented\nSUPABASE_DB_URL = y\n")
	write(t, root, "app/.env.local", "PG_URI=z\n")
	r := run(t, Options{Dir: root})
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "canary-env") || strings.Contains(string(b), "postgres://") {
		t.Fatal("a value reached the report")
	}
	if fmt.Sprint(r.EnvNames) != "[{.env [DATABASE_URL PGPASSWORD SUPABASE_DB_URL]} {app/.env.local [PG_URI]}]" {
		t.Fatalf("%v", r.EnvNames)
	}
}

func TestDependencies(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"dependencies":{"stripe":"^23.0.0","left-pad":"1"},"devDependencies":{"@anthropic-ai/claude-agent-sdk":"^0.3.0"}}`)
	write(t, root, "pyproject.toml", "[project]\nname = \"x\"\ndependencies = [\n  \"langchain>=1.0\",\n  \"requests\",\n  'crewai==1.15',\n]\n[tool.poetry.dependencies]\npython = \"^3.11\"\nclaude_agent_sdk = \"^0.2\"\n")
	write(t, root, "requirements-dev.txt", "openai-agents==0.23.1  # agents\n-r base.txt\npostgres-mcp\n")
	r := run(t, Options{Dir: root})
	var got []string
	for _, d := range r.Dependencies {
		got = append(got, d.File+":"+d.Name+":"+d.CatalogID)
	}
	want := "package.json:@anthropic-ai/claude-agent-sdk:framework.claude-agent-sdk package.json:stripe:sdk.stripe pyproject.toml:claude-agent-sdk:framework.claude-agent-sdk pyproject.toml:crewai:framework.crewai pyproject.toml:langchain:framework.langchain requirements-dev.txt:openai-agents:framework.openai-agents requirements-dev.txt:postgres-mcp:mcp.postgres-mcp"
	if strings.Join(got, " ") != want {
		t.Fatalf("\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	var money int
	for _, tl := range r.Tools {
		if tl.SideEffect == "money" {
			money++
		}
	}
	if money != 2 {
		t.Fatalf("%+v", r.Tools)
	}
}

func TestWalkRules(t *testing.T) {
	root := t.TempDir()
	write(t, root, "node_modules/x/.cursor/mcp.json", fmt.Sprintf(npxArchived, ArchivedPostgres))
	write(t, root, ".git/claude_desktop_config.json", fmt.Sprintf(npxArchived, ArchivedPostgres))
	write(t, root, "big/package.json", `{"dependencies":{"stripe":"1"}}`+strings.Repeat(" ", maxFileBytes))
	outside := t.TempDir()
	write(t, outside, ".cursor/mcp.json", fmt.Sprintf(npxArchived, ArchivedPostgres))
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	r := run(t, Options{Dir: root})
	if len(r.Configs) != 0 || len(r.Dependencies) != 0 || r.Walk.SkippedBig != 1 {
		t.Fatalf("%+v %+v %+v", r.Configs, r.Dependencies, r.Walk)
	}
	for i := 0; i < 30; i++ {
		write(t, root, fmt.Sprintf("f/%d.txt", i), "x")
	}
	r = run(t, Options{Dir: root, MaxFiles: 10})
	if !r.Walk.Stopped || r.Walk.Files != 10 {
		t.Fatalf("%+v", r.Walk)
	}
}

func TestWalkBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("the 20,000-file walk")
	}
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		_ = os.MkdirAll(filepath.Join(root, fmt.Sprintf("d%03d", i)), 0o755)
	}
	for i := 0; i < 20000; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("d%03d", i%200), fmt.Sprintf("f%d.go", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Now()
	r := run(t, Options{Dir: root})
	d := time.Since(t0)
	t.Logf("20,000 files walked in %s", d)
	if r.Walk.Files != 20000 || d > 2*time.Second {
		t.Fatalf("%d files in %s (budget 2 s)", r.Walk.Files, d)
	}
}

func TestTableScore(t *testing.T) {
	trig := []Trigger{{Name: "t", Enabled: true}}
	for _, c := range []struct {
		t    Table
		want float64
	}{
		{Table{Table: "orders", Can: []string{"INSERT", "UPDATE", "DELETE"}, EstRows: 999_999, Triggers: trig}, 100},
		{Table{Table: "orders", Can: []string{"INSERT", "UPDATE", "DELETE"}, EstRows: -1, Triggers: trig}, 75},
		{Table{Table: "notes", Can: []string{"INSERT"}, EstRows: 0}, 5},
		{Table{Table: "notes", Can: []string{"TRUNCATE"}, EstRows: 9}, 40 * 0.5833333333333333},
		{Table{Table: "x", Can: []string{"UPDATE"}, CascadesTo: []string{"a"}, EstRows: 10_000_000}, 35},
	} {
		if got := TableScore(c.t); fmt.Sprintf("%.6f", got) != fmt.Sprintf("%.6f", c.want) {
			t.Errorf("%+v: %v, want %v", c.t, got, c.want)
		}
	}
	ts := []Table{{Schema: "b", Table: "x", Can: []string{"INSERT"}, EstRows: 0}, {Schema: "a", Table: "x", Can: []string{"INSERT"}, EstRows: 0},
		{Schema: "public", Table: "orders", Can: []string{"DELETE"}, EstRows: -1}}
	rank(ts)
	if ts[0].Name() != "public.orders" || ts[1].Name() != "a.x" || ts[2].Name() != "b.x" {
		t.Fatalf("%v", ts)
	}
}

func scoreOf(highs, mediums, paths int, su bool) Score {
	r := &Report{}
	for i := 0; i < highs; i++ {
		r.Findings = append(r.Findings, Finding{ID: fmt.Sprintf("h%d", i), Severity: "high", Title: "H"})
	}
	for i := 0; i < mediums; i++ {
		r.Findings = append(r.Findings, Finding{ID: fmt.Sprintf("m%d", i), Severity: "medium", Title: "M"})
	}
	r.Findings = append(r.Findings, Finding{ID: "l", Severity: "low"}, Finding{ID: "i", Severity: "info"})
	pg := &Postgres{Role: "r", RoleAttributes: RoleAttributes{BypassRLS: su}}
	for i := 0; i < paths; i++ {
		pg.Tables = append(pg.Tables, Table{Schema: "s", Table: fmt.Sprintf("t%02d", i), Can: []string{"DELETE"}})
	}
	r.Postgres = pg
	return score(r, false)
}

func TestScoreFormula(t *testing.T) {
	for _, c := range []struct {
		h, m, p int
		su      bool
		points  int
		grade   string
	}{
		{0, 0, 0, false, 100, "A"},
		{0, 0, 2, false, 92, "A"},
		{0, 0, 3, false, 88, "B"}, // 90/89 boundary: 92 A, 88 B
		{0, 2, 0, false, 84, "B"},
		{0, 0, 5, false, 80, "B"},
		{0, 2, 1, false, 80, "B"}, // 80/79
		{0, 2, 2, false, 76, "C"},
		{1, 2, 0, false, 64, "D"}, // 65/64
		{1, 1, 4, false, 56, "D"},
		{1, 0, 0, true, 45, "F"}, // −35 once, plus the high
		{2, 3, 5, false, 16, "F"},
		{3, 4, 6, false, 16, "F"}, // caps: 3 highs 40, 4 mediums 24, 6 paths 20
		{3, 4, 6, true, 0, "F"},   // floor at 0
	} {
		s := scoreOf(c.h, c.m, c.p, c.su)
		if s.Points != c.points || s.Grade != c.grade || s.FormulaVersion != 1 {
			t.Errorf("%+v: %d %s", c, s.Points, s.Grade)
		}
		var sum, zero int
		for _, d := range s.Deductions {
			sum += d.Points
			if d.Points == 0 {
				zero++
				if !strings.HasSuffix(d.Detail, "(over the cap)") {
					t.Errorf("%+v", d)
				}
			}
		}
		if 100-sum != s.Points && s.Points != 0 {
			t.Errorf("deductions %d don't explain %d points", sum, s.Points)
		}
	}
	if s := scoreOf(3, 4, 6, false); len(s.Deductions) != 13 {
		t.Fatalf("every capped item is listed: %d", len(s.Deductions))
	}
	if Grade(90) != "A" || Grade(89) != "B" || Grade(80) != "B" || Grade(79) != "C" || Grade(65) != "C" || Grade(64) != "D" || Grade(50) != "D" || Grade(49) != "F" {
		t.Fatal("grade boundaries")
	}
}

func demoReport() *Report {
	r := &Report{Object: "scan_report", Version: 1, Dir: "examples/demo", Footer: FooterLine, Configs: []Config{}, Dependencies: []Dependency{},
		EnvNames: []EnvNames{}, Tools: []Tool{}, Notes: []string{}}
	r.Postgres = &Postgres{Role: "canary_role_4k", Database: "acme", WALLevel: "logical", EnvVar: "DEMO_DSN", Tables: []Table{
		{Schema: "public", Table: "canary_tbl_9q", Can: []string{"UPDATE", "DELETE"}, EstRows: 1000, Triggers: []Trigger{{Name: "orders_ship_open_batch", Enabled: true}}, HasPK: true, UndoReady: true, L2Ready: true, CascadesTo: []string{}},
	}}
	r.Configs = []Config{{Path: "canary-dir-7x/.cursor/mcp.json", Client: "cursor", Servers: []Server{{Name: "canary-srv-2m", Package: ArchivedPostgres, Version: "0.6.2"}}}}
	r.Findings = findings(r, Options{})
	r.Score = score(r, false)
	r.Policies = suggest(r)
	r.Summary = summarize(r)
	return r
}

// TestBadgeShare (PRD-09): the payload a README badge may link to has the seven keys the /s page
// reads, with the grade, points and counts, and no finding or deduction id.
func TestBadgeShare(t *testing.T) {
	r := demoReport()
	full := NewShare(r)
	if len(full.F) == 0 || len(full.D) == 0 {
		t.Fatalf("the demo report needs findings and deductions: %+v", full)
	}
	link, err := ShareLink(BadgeShare(r))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, ShareBase))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"c":{"high":%d,"info":%d,"low":%d,"medium":%d},"d":[],"f":[],"fv":%d,"g":%q,"p":%d,"v":1}`,
		full.C["high"], full.C["info"], full.C["low"], full.C["medium"], full.FV, full.G, full.P)
	if string(raw) != want {
		t.Fatalf("%s, want %s", raw, want)
	}
	for _, id := range append(append([]string{}, full.F...), full.D[0].ID) {
		if strings.Contains(string(raw), id) {
			t.Errorf("the badge payload holds %q", id)
		}
	}
}

func TestShareCanary(t *testing.T) {
	r := demoReport()
	link, err := ShareLink(NewShare(r))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, ShareBase) || len(link) > 2000 {
		t.Fatalf("%s", link)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, ShareBase))
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"canary-dir-7x", "canary_tbl_9q", "canary_role_4k", "canary-srv-2m", "public.", "/", "0.6.2", "DEMO_DSN", "acme"} {
		if strings.Contains(string(raw), canary) || strings.Contains(link[len(ShareBase):], canary) {
			t.Errorf("the share payload holds %q: %s", canary, raw)
		}
	}
	var p SharePayload
	if err := json.Unmarshal(raw, &p); err != nil || p.G != r.Score.Grade || p.FV != 1 || p.C["high"] != 1 {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestEveryOutputCarriesScoreAndFooter(t *testing.T) {
	r := demoReport()
	h, err := RenderHTML(r)
	if err != nil {
		t.Fatal(err)
	}
	sarif, _, err := RenderSARIF(r)
	if err != nil {
		t.Fatal(err)
	}
	text := RenderText(r, Style{}, 100, "deedglass scan · x", "")
	md := RenderMarkdown(r)
	line := ScoreLine(r.Score)
	for name, out := range map[string]string{"text": text, "md": md, "html": h} {
		plain := htmlText(t, name, out)
		if !strings.Contains(plain, line) || !strings.Contains(plain, SmallPrint) || !strings.Contains(plain, "orders_ship_open_batch") {
			t.Errorf("%s lacks the score, the small print or a deduction", name)
		}
		if strings.Index(plain, line) > strings.Index(plain, "Found") {
			t.Errorf("%s: the score isn't first", name)
		}
		if !strings.Contains(plain, FooterLine) {
			t.Errorf("%s lacks the footer", name)
		}
		if strings.Contains(out, "deedglass.com/s#") {
			t.Errorf("%s holds a share link without --share", name)
		}
	}
	if !strings.Contains(string(sarif), `"deedglass_score"`) {
		t.Error("SARIF lacks the score")
	}
}

// htmlText is the text a reader sees: tags and the style block dropped, entities decoded.
func htmlText(t *testing.T, name, s string) string {
	t.Helper()
	if name != "html" {
		return s
	}
	s = regexp.MustCompile(`(?s)<style>.*?</style>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`<[^>]+>`).ReplaceAllString(s, " ")
	s = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'", "&quot;", `"`).Replace(s)
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.ReplaceAll(s, " ,", ",")
}

// TestHTMLLoadsNothing: no script, no element that loads, no src or href away from deedglass.com,
// no event handler, no url( or @import in the CSS (stdlib only: tags and attributes by pattern).
func TestHTMLLoadsNothing(t *testing.T) {
	h, err := RenderHTML(demoReport())
	if err != nil {
		t.Fatal(err)
	}
	if m := regexp.MustCompile(`(?i)<(script|iframe|img|link|object|embed|video|audio|source|frame)\b`).FindString(h); m != "" {
		t.Errorf("%s in the report", m)
	}
	for _, m := range regexp.MustCompile(`(?i)\s(src|href|srcset|action|formaction|poster|data)\s*=\s*"([^"]*)"`).FindAllStringSubmatch(h, -1) {
		if !strings.HasPrefix(m[2], "https://deedglass.com/") {
			t.Errorf("%s=%q points away", m[1], m[2])
		}
	}
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(h) {
		t.Error("an event handler")
	}
	if regexp.MustCompile(`url\(|@import`).MatchString(h) {
		t.Fatal("the CSS loads something")
	}
}

func TestSARIFShape(t *testing.T) {
	r := demoReport()
	r.Configs = append(r.Configs, Config{Path: "~/.cursor/mcp.json", User: true, Servers: []Server{{Name: "u", Package: ArchivedPostgres}}})
	r.Findings = findings(r, Options{})
	b, n, err := RenderSARIF(r)
	if err != nil {
		t.Fatal(err)
	}
	var log map[string]any
	if err := json.Unmarshal(b, &log); err != nil {
		t.Fatal(err)
	}
	// The SARIF 2.1.0 schema's required members, checked by hand (no schema validator dependency).
	if log["version"] != "2.1.0" || log["$schema"] != "https://json.schemastore.org/sarif-2.1.0.json" {
		t.Fatal(log["version"])
	}
	run := log["runs"].([]any)[0].(map[string]any)
	driver := run["tool"].(map[string]any)["driver"].(map[string]any)
	rules := driver["rules"].([]any)
	ids := map[string]bool{}
	for _, x := range rules {
		m := x.(map[string]any)
		ids[m["id"].(string)] = true
		if !strings.HasPrefix(m["helpUri"].(string), "https://docs.deedglass.com/scan#") || m["shortDescription"].(map[string]any)["text"] == "" {
			t.Errorf("%v", m)
		}
	}
	if driver["name"] != "deedglass" || len(rules) != n || len(ids) != n {
		t.Fatalf("%v", driver)
	}
	for _, x := range run["results"].([]any) {
		m := x.(map[string]any)
		if !ids[m["ruleId"].(string)] || m["message"].(map[string]any)["text"] == "" {
			t.Errorf("%v", m)
		}
		lv := m["level"].(string)
		if lv != "error" && lv != "warning" && lv != "note" {
			t.Errorf("level %s", lv)
		}
		locs, _ := m["locations"].([]any)
		for _, l := range locs {
			uri := l.(map[string]any)["physicalLocation"].(map[string]any)["artifactLocation"].(map[string]any)["uri"].(string)
			if strings.HasPrefix(uri, "~") || filepath.IsAbs(uri) {
				t.Errorf("a path outside --dir: %s", uri)
			}
		}
	}
	if SARIFLevel("high") != "error" || SARIFLevel("medium") != "warning" || SARIFLevel("low") != "note" || SARIFLevel("info") != "note" {
		t.Fatal("levels")
	}
}

func TestSuggestedPoliciesCompile(t *testing.T) {
	r := demoReport()
	r.Tools = []Tool{{Server: "s", Tool: "refunds.create", SideEffect: "money"}}
	all := append(suggest(r), suggest(demoReport())...)
	if len(all) != 6 {
		t.Fatalf("%d suggestions", len(all))
	}
	for _, s := range all {
		c, diags := policy.Compile([]byte(s.YAML), policy.Options{File: s.Rule + ".yaml"})
		if c == nil || len(diags) > 0 {
			t.Fatalf("%s:\n%s\n%+v", s.Rule, s.YAML, diags)
		}
		cases := fmt.Sprintf("policy: %s.yaml\ncases:\n  - name: DROP TABLE never runs\n    input: { tool: postgres.execute, sql: \"DROP TABLE public.orders\" }\n    expect: { decision: deny }\n", s.Rule)
		cf, probs := policy.ParseCasesYAML([]byte(cases), s.Rule+".cases.yaml")
		if cf == nil {
			t.Fatalf("%+v", probs)
		}
		res := policy.RunCases(context.Background(), c, cf, policy.TestOptions{})
		if res.Failed != 0 {
			t.Fatalf("%s lets DROP TABLE through: %+v", s.Rule, res)
		}
	}
	if !strings.Contains(all[1].YAML, `"public.canary_tbl_9q" in sql.tables`) {
		t.Fatalf("not narrowed to the top table:\n%s", all[1].YAML)
	}
}

func TestCatalogSources(t *testing.T) {
	c, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(c) < 15 {
		t.Fatalf("%d entries", len(c))
	}
	reDate := regexp.MustCompile(`^2026-\d{2}-\d{2}$`)
	for _, e := range c {
		if !strings.HasPrefix(e.Source, "https://") || !reDate.MatchString(e.Checked) || e.ID == "" || len(e.Match) == 0 {
			t.Errorf("%+v", e)
		}
	}
	for _, id := range []string{"mcp.server-postgres", "mcp.postgres-mcp", "mcp.supabase", "mcp.neon", "mcp.pgedge", "mcp.deedglass", "sdk.stripe", "sdk.stripe-agent-toolkit"} {
		if entryByID(id) == nil {
			t.Errorf("the catalog lacks %s", id)
		}
	}
}

func TestBadge(t *testing.T) {
	dir := t.TempDir()
	if r := ReadRemote(dir); r.OK || !strings.Contains(BadgeMarkdown(r, ""), "<owner>%2F<repo>%2F<branch>") {
		t.Fatal("placeholders without a remote")
	}
	write(t, dir, ".git/config", "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:acme/agents.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n")
	write(t, dir, ".git/HEAD", "ref: refs/heads/trunk\n")
	r := ReadRemote(dir)
	if BadgeMarkdown(r, "") != "[![agent blast radius](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Facme%2Fagents%2Ftrunk%2F.deedglass%2Fscore.json)](https://deedglass.com/score)" {
		t.Fatal(BadgeMarkdown(r, ""))
	}
	p, err := WriteBadge(dir, "D")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m["schemaVersion"] != 1.0 || m["label"] != "agent blast radius" || m["message"] != "D" || m["color"] != "9A5B00" || m["cacheSeconds"] != 3600.0 || len(m) != 5 {
		t.Fatalf("%s", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	for g, c := range map[string]string{"A": "0F766E", "B": "0F766E", "C": "3F4453", "D": "9A5B00", "F": "9A5B00"} {
		if NewBadge(g).Color != c {
			t.Errorf("%s", g)
		}
	}
	for _, c := range badgeColors {
		if strings.EqualFold(c, "red") || strings.HasPrefix(strings.ToUpper(c), "C8") || strings.HasPrefix(strings.ToUpper(c), "FF") {
			t.Errorf("a badge colour reads as red: %s", c)
		}
	}
	if GitignoreHidesBadge(dir) {
		t.Fatal("no .gitignore")
	}
	write(t, dir, ".gitignore", "node_modules/\n.deedglass/\n")
	if !GitignoreHidesBadge(dir) {
		t.Fatal("directory pattern hides score.json")
	}
	write(t, dir, ".gitignore", ".deedglass/*\n!.deedglass/score.json\n")
	if GitignoreHidesBadge(dir) {
		t.Fatal("the S47 block keeps score.json")
	}
}

func TestNoRetiredOffer(t *testing.T) {
	// deedglass.com/check was the retired paid offer; it is now the live free in-browser checker
	// (the protect badge links there), so only the retired price stays banned.
	re := regexp.MustCompile(`\$199`)
	for _, dir := range []string{".", "../../cmd/deedglass"} {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			if b, _ := os.ReadFile(p); re.Match(b) {
				t.Errorf("%s names the retired offer", p)
			}
			return nil
		})
	}
	if FooterLine != "What your grade means: deedglass.com/score" {
		t.Fatal(FooterLine)
	}
}

func TestReduceError(t *testing.T) {
	c, n := ReduceError(errors.New("dial tcp db.internal:5432: connect: connection refused (user canary)"))
	if c != "08001" || strings.Contains(n, "canary") {
		t.Fatal(c, n)
	}
}

func TestQueriesAreReadOnly(t *testing.T) {
	if len(Queries) != 6 {
		t.Fatal(len(Queries))
	}
	bad := regexp.MustCompile(`(?i)\b(insert|update|delete|truncate|create|alter|drop|grant|revoke|commit|set\s+role|copy)\b|;`)
	for _, q := range Queries {
		stripped := regexp.MustCompile(`has_table_privilege\(c\.oid, '[A-Z]+'\)|has_any_column_privilege\(c\.oid, 'UPDATE'\)|AS can_[a-z]+|'[A-Z]+'`).ReplaceAllString(q, "")
		if bad.MatchString(stripped) || !strings.HasPrefix(strings.TrimSpace(q), "SELECT") {
			t.Errorf("not a plain catalog read: %s", q)
		}
	}
	if RuntimeParams["default_transaction_read_only"] != "on" || RuntimeParams["application_name"] != "deedglass-scan" {
		t.Fatal(RuntimeParams)
	}
}

var updateGolden = flag.Bool("update", false, "rewrite internal/scan/testdata/*.golden.json")

// TestJSONGolden locks the scan_report v1 shape S37's renderer reads (times, dir and version masked).
func TestJSONGolden(t *testing.T) {
	r := run(t, Options{Dir: "testdata/archived-pg", DirLabel: "archived-pg", Version: "0.1.0",
		Now: func() time.Time { return time.Date(2026, 10, 11, 9, 12, 3, 0, time.UTC) }})
	r.TookMS = 0
	r.Walk.Files = 0
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const p = "testdata/archived-pg.golden.json"
	if *updateGolden {
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil || string(want) != string(b) {
		t.Fatalf("the scan_report golden drifted (go test ./internal/scan -run TestJSONGolden -update, then review):\n%s", b)
	}
}

// TestRoleCountsOnce: a superuser + BYPASSRLS role costs 35 once; neither finding is a high deduction.
func TestRoleCountsOnce(t *testing.T) {
	r := &Report{Postgres: &Postgres{Role: "app", RoleAttributes: RoleAttributes{Superuser: true, BypassRLS: true}}}
	r.Findings = []Finding{{ID: "role_superuser", Severity: "high", Title: "s"}, {ID: "role_bypassrls", Severity: "high", Title: "b"}}
	s := score(r, false)
	if s.Points != 65 {
		t.Fatalf("points = %d, want 65 (100 - 35)", s.Points)
	}
	for _, d := range s.Deductions {
		if d.ID == "high_finding" {
			t.Fatalf("role finding counted as high: %+v", d)
		}
	}
}

// TestShareCatalogClosed: every finding id the scan can produce is in the share catalog.
func TestShareCatalogClosed(t *testing.T) {
	var src []byte
	for _, f := range []string{"findings.go", "shell.go"} {
		b, _ := os.ReadFile(f)
		src = append(src, b...)
	}
	ids := regexp.MustCompile(`(?:ID: |role\()"([a-z_]{3,})"`).FindAllStringSubmatch(string(src), -1)
	if len(ids) < 13 {
		t.Fatalf("found %d finding ids; the pattern no longer matches the code", len(ids))
	}
	have := map[string]bool{}
	for _, e := range ShareCatalog()["findings"].([]ShareEntry) {
		have[e.ID] = true
	}
	for _, m := range ids {
		if !have[m[1]] {
			t.Errorf("finding %s is missing from ShareCatalog", m[1])
		}
	}
}

// TestPlayLine: a writable table with an enabled trigger (or a cascade) points to the 301 scene;
// a disabled trigger alone doesn't; the line sits above the footer in the text report.
func TestPlayLine(t *testing.T) {
	trig := Table{Schema: "public", Table: "orders", Can: []string{"UPDATE"}, Triggers: []Trigger{{Name: "orders_ship_open_batch", Enabled: true}}}
	off := Table{Schema: "public", Table: "notes", Can: []string{"UPDATE"}, Triggers: []Trigger{{Name: "old", Enabled: false}}}
	casc := Table{Schema: "public", Table: "customers", Can: []string{"DELETE"}, CascadesTo: []string{"public.orders"}}
	if l := PlayLine([]Table{off, casc, trig}); !strings.Contains(l, "A trigger on public.orders (orders_ship_open_batch)") || !strings.HasSuffix(l, PlayURL) {
		t.Fatalf("trigger line: %q", l)
	}
	if l := PlayLine([]Table{off, casc}); !strings.Contains(l, "Changes to public.customers cascade to public.orders") {
		t.Fatalf("cascade line: %q", l)
	}
	if l := PlayLine([]Table{off}); l != "" {
		t.Fatalf("a disabled trigger made a line: %q", l)
	}
	text := RenderText(demoReport(), Style{}, 100, "deedglass scan · x", "")
	i, j := strings.Index(text, PlayURL), strings.Index(text, FooterLine)
	if i < 0 || j < i {
		t.Fatalf("the play line is missing or below the footer:\n%s", text)
	}
}
