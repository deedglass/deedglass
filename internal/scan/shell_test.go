package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestBashRuleMatching: the rows of Claude Code's permissions page (wildcard patterns, the :*
// suffix, the bare tool, parameter rules), read 2026-10-18.
func TestBashRuleMatching(t *testing.T) {
	for _, c := range []struct {
		rule string
		yes  []string
		no   []string
	}{
		{"Bash(npm run build)", []string{"npm run build"}, []string{"npm run build --watch"}},
		{"Bash(npm run *)", []string{"npm run build", "npm run test --watch", "npm run"}, []string{"npm install"}},
		{"Bash(git log * main)", []string{"git log --oneline main", "git log -5 main"}, []string{"git log main", "git push origin main"}},
		{"Bash(git * main)", []string{"git merge main", "git push origin main"}, []string{"git log"}},
		{"Bash(* --version)", []string{"node --version"}, []string{"node -v"}},
		{"Bash(ls *)", []string{"ls -la", "ls"}, []string{"lsof"}},
		{"Bash(ls*)", []string{"ls -la", "lsof"}, nil},
		{"Bash(* --help *)", []string{"npm --help x"}, []string{"npm --help"}},
		{"Bash(ls:*)", []string{"ls -la", "ls"}, []string{"lsof"}},
		{"Bash(git:* push)", nil, []string{"git push", "git push origin"}},
		{"Bash", []string{"prisma migrate reset --force"}, nil},
		{"Bash(*)", []string{"rails db:drop"}, nil},
		{"Bash(run_in_background:true)", nil, []string{"run_in_background:true", "prisma db push --force-reset"}},
		{"Bash(npx prisma migrate:*)", []string{"npx prisma migrate reset --force", "npx prisma migrate"}, []string{"pnpm prisma migrate reset --force", "npx prisma db push --force-reset"}},
		{"Read(./.env)", nil, []string{"cat .env"}},
	} {
		for _, cmd := range c.yes {
			if !bashRule(c.rule, cmd, false) {
				t.Errorf("%s should match %q", c.rule, cmd)
			}
		}
		for _, cmd := range c.no {
			if bashRule(c.rule, cmd, false) {
				t.Errorf("%s shouldn't match %q", c.rule, cmd)
			}
		}
	}
	// A tool-name glob counts in deny and ask rules only.
	if !bashRule("*", "prisma db push", true) || bashRule("*", "prisma db push", false) || bashRule("mcp__*", "prisma db push", true) {
		t.Fatal("tool-name globs")
	}
}

func TestReadRuleMatching(t *testing.T) {
	home := "/h"
	proj := "/h/code/shop"
	user := &permSource{scope: "user", anchor: "/h/.claude"}
	project := &permSource{scope: "project", anchor: proj}
	for _, c := range []struct {
		src  *permSource
		rule string
		file string
		want bool
	}{
		{project, "Read(.env)", proj + "/.env", true},
		{project, "Read(.env)", proj + "/apps/api/.env", true},
		{project, "Read(**/.env)", proj + "/apps/api/.env", true},
		{project, "Read(.env)", "/h/code/.env", false}, // a parent directory
		{project, "Read(./.env)", proj + "/.env", true},
		{project, "Read(./.env)", proj + "/apps/api/.env", false}, // read as the current directory's .env only
		{project, "Read(./secrets/**)", proj + "/secrets/db/.env", true},
		{project, "Read(.env.*)", proj + "/apps/.env.local", true},
		{project, "Read(.env.*)", proj + "/.env", false},
		{project, "Read(*.env)", proj + "/x/prod.env", true},
		{project, "Read(//**/.env)", "/elsewhere/.env", true},
		{project, "Read(/.env)", proj + "/.env", true},
		{project, "Read(/.env)", proj + "/apps/.env", false},
		{project, "Read(/apps/**)", proj + "/apps/api/.env", true},
		{project, "Read(apps/api/.env)", proj + "/apps/api/.env", true},
		{project, "Read(apps/api/.env)", proj + "/x/apps/api/.env", false},
		{project, "Read(secrets/**)", proj + "/a/secrets/.env", true},
		{project, "Read(secrets)", proj + "/a/secrets/.env", true},
		{project, "Read(~/code/shop/.env)", proj + "/.env", true},
		{user, "Read(/.env)", proj + "/.env", false}, // anchors at ~/.claude, not the project
		{user, "Read(//h/code/**)", proj + "/.env", true},
		{project, "Read(!.env)", proj + "/.env", false},
		{project, "Read", proj + "/.env", true},
		{project, "*", proj + "/.env", true},
		{project, "Edit(.env)", proj + "/.env", false},
		{project, "Bash(cat .env)", proj + "/.env", false},
	} {
		s := *c.src
		s.deny = []string{c.rule}
		if got := readDenied([]*permSource{&s}, c.file, proj, home); got != c.want {
			t.Errorf("%s (%s) on %s: %v, want %v", c.rule, s.scope, c.file, got, c.want)
		}
	}
}

func TestEffectiveMode(t *testing.T) {
	src := func(scope, mode string) *permSource { return &permSource{scope: scope, mode: mode, label: scope} }
	for _, c := range []struct {
		srcs []*permSource
		want string
	}{
		{nil, ""},
		{[]*permSource{src("user", "bypassPermissions")}, "bypassPermissions"},
		{[]*permSource{src("project", "bypassPermissions"), src("user", "dontAsk")}, "default"}, // doesn't take effect in project files
		{[]*permSource{src("project", "auto"), src("user", "dontAsk")}, ""},                     // falls back to the built-in default
		{[]*permSource{src("local", "plan"), src("project", "acceptEdits")}, "plan"},
		{[]*permSource{src("project", "acceptEdits"), src("user", "bypassPermissions")}, "acceptEdits"},
		{[]*permSource{src("user", "bypassPermissions"), {scope: "project", noBypass: true}}, "default"},
		{[]*permSource{src("user", "auto"), {scope: "project", noAuto: true}}, "default"}, // disableAutoMode in any file
		{[]*permSource{{scope: "user", noAuto: true}}, "default"},                         // the built-in default becomes Manual
		{[]*permSource{src("user", "acceptEdits"), {scope: "project", noAuto: true}}, "acceptEdits"},
	} {
		if got := effectiveMode(c.srcs).mode; got != c.want {
			t.Errorf("%d sources: %q, want %q", len(c.srcs), got, c.want)
		}
	}
}

func TestDecidePrecedence(t *testing.T) {
	s := &permSource{label: "x", allow: []string{"Bash(npx prisma *)"}, ask: []string{"Bash(npx prisma migrate *)"}, deny: []string{"Bash(npx prisma migrate reset *)"}}
	srcs := []*permSource{s}
	for cmd, want := range map[string]int{
		"npx prisma migrate reset --force":  oDenied, // deny beats ask and allow
		"npx prisma migrate diff --x":       oAsks,   // ask beats allow
		"npx prisma db push --force-reset":  oRuns,
		"pnpm prisma db push --force-reset": oNone,
	} {
		if v := decide(cmd, srcs, modeInfo{}); v.o != want {
			t.Errorf("%s: %d, want %d", cmd, v.o, want)
		}
	}
	by := modeInfo{"bypassPermissions", s}
	if decide("pnpm prisma db push --force-reset", srcs, by).o != oRuns || decide("npx prisma migrate diff", srcs, by).o != oAsks {
		t.Fatal("bypass: unmatched runs, ask still asks")
	}
	if v := decide("pnpm prisma db push", srcs, modeInfo{"dontAsk", s}); v.o != oDenied || v.rule != "" {
		t.Fatal("dontAsk denies what no rule allows")
	}
}

func TestQuotable(t *testing.T) {
	for r, want := range map[string]bool{
		"Bash(npx prisma:*)":                                 true,
		"Bash(npm run *)":                                    true,
		"Bash(PGPASSWORD=hunter2 psql -c x)":                 false,
		"Bash(psql postgres://u:p@h/db)":                     false,
		"Bash(curl -H x ghp_abcdefghijklmnopqrstuvwxyz0123)": false,
		"Bash(" + strings.Repeat("a ", 50) + ")":             false,
		`Bash(echo "x")`:                                     false,
	} {
		if quotable(r) != want {
			t.Errorf("quotable(%q) = %v", r, !want)
		}
	}
}

// shellFixture writes a project tree and a home directory, then scans them.
func shellFixture(t *testing.T, label string, files, homeFiles map[string]string) *Report {
	t.Helper()
	root, home := t.TempDir(), t.TempDir()
	for p, b := range files {
		write(t, root, p, b)
	}
	for p, b := range homeFiles {
		write(t, home, p, b)
	}
	r := run(t, Options{Dir: root, DirLabel: label, Home: home, UserConfigs: true, GOOS: "linux", Version: "0.1.0",
		Now: func() time.Time { return time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC) }})
	r.TookMS, r.Walk.Files = 0, 0
	return r
}

var shellFixtures = map[string]struct {
	files, home map[string]string
}{
	// Every Bash command allowed: each destructive command runs without a prompt, and .env is readable.
	"shell-allow-all": {files: map[string]string{
		".claude/settings.json": `{"permissions":{"allow":["Bash","Read","Edit"]}}`,
		"package.json":          `{"name":"shop","scripts":{"db:sync":"prisma db push"},"dependencies":{"@prisma/client":"^6.19.0"},"devDependencies":{"prisma":"^6.19.0","drizzle-kit":"^0.31.0"}}`,
		".env":                  "DATABASE_URL=postgres://app:canary-allow-all@db.internal:5432/shop\nDIRECT_URL=postgres://app:canary-allow-all@db.internal:5432/shop\n",
		".env.example":          "DATABASE_URL=postgres://user:password@localhost:5432/shop\n",
	}},
	// claude-code#80868's rules: ask on two npx prefixes in user settings, auto mode. A script the
	// project's local settings allow runs the same Prisma command under another spelling.
	"shell-prefix-ask": {files: map[string]string{
		".claude/settings.local.json": `{"permissions":{"allow":["Bash(npm run *)","Bash(npx prisma generate)"]}}`,
		"package.json":                `{"name":"shop","scripts":{"db:diff":"prisma migrate diff --from-migrations prisma/migrations --to-schema-datamodel prisma/schema.prisma --shadow-database-url \"$DATABASE_URL\" --script","build":"next build"},"devDependencies":{"prisma":"^6.19.0"}}`,
		".env":                        "DATABASE_URL=postgres://app:canary-prefix-ask@prod.example:5432/shop\n",
	}, home: map[string]string{
		".claude/settings.json": `{"permissions":{"defaultMode":"auto","ask":["Bash(npx prisma db:*)","Bash(npx prisma migrate:*)"]}}`,
	}},
	// Deny rules on .env and on the downgrade, ask on prisma, each with a leading * so every runner
	// is covered: nothing to report.
	"shell-deny-env": {files: map[string]string{
		".claude/settings.json":       `{"permissions":{"defaultMode":"acceptEdits","allow":["Bash(npx prisma generate)","Bash(alembic upgrade head)"],"ask":["Bash(*prisma migrate*)","Bash(*prisma db push*)"],"deny":["Read(./.env)","Read(./.env.*)","Bash(*alembic downgrade*)"]}}`,
		"package.json":                `{"name":"shop","devDependencies":{"prisma":"^6.19.0"}}`,
		"services/api/pyproject.toml": "[project]\nname = \"api\"\ndependencies = [\n  \"alembic>=1.16\",\n  \"sqlalchemy>=2.0\",\n]\n",
		".env":                        "DATABASE_URL=postgres://app:canary-deny-env@db:5432/shop\n",
		".env.local":                  "DIRECT_URL=postgres://app:canary-deny-env@db:5432/shop\n",
	}},
}

// TestShellGoldens locks the three FEAT-05 cases end to end (go test ./internal/scan -run
// TestShellGoldens -update, then review): the scan_report v1 shape is unchanged apart from the new
// claude_settings list, and no .env value or rule text reaches the report.
func TestShellGoldens(t *testing.T) {
	want := map[string]struct {
		grade  string
		points int
		ids    string
	}{
		"shell-allow-all":  {"D", 52, "shell_destructive_unprompted shell_destructive_unprompted env_database_url_readable"},
		"shell-prefix-ask": {"D", 64, "shell_destructive_unprompted env_database_url_readable shell_destructive_no_rule"},
		"shell-deny-env":   {"A", 100, ""},
	}
	for name, fx := range shellFixtures {
		t.Run(name, func(t *testing.T) {
			r := shellFixture(t, name, fx.files, fx.home)
			var ids []string
			for _, f := range r.Findings {
				ids = append(ids, f.ID)
			}
			w := want[name]
			if r.Score.Grade != w.grade || r.Score.Points != w.points || strings.Join(ids, " ") != w.ids {
				t.Errorf("%s %d %v, want %s %d %s", r.Score.Grade, r.Score.Points, ids, w.grade, w.points, w.ids)
			}
			b, err := json.MarshalIndent(r, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			b = append(b, '\n')
			text := RenderText(r, Style{}, 100, "deedglass scan · "+name, "")
			h, _ := RenderHTML(r)
			for _, out := range []string{string(b), text, RenderMarkdown(r), h} {
				if strings.Contains(out, "canary") || strings.Contains(out, "postgres://") || strings.Contains(out, "next build") {
					t.Fatal("a .env value or a script body reached the report")
				}
			}
			p := filepath.Join("testdata", name+".golden.json")
			if *updateGolden {
				if err := os.WriteFile(p, b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			g, err := os.ReadFile(p)
			if err != nil || string(g) != string(b) {
				t.Fatalf("%s drifted (go test ./internal/scan -run TestShellGoldens -update, then review):\n%s", p, b)
			}
		})
	}
}

func TestShellFindingWords(t *testing.T) {
	r := shellFixture(t, "x", shellFixtures["shell-prefix-ask"].files, shellFixtures["shell-prefix-ask"].home)
	f := r.Findings[0]
	for _, s := range []string{"An agent can run `npm run db:diff` here without a prompt"} {
		if f.Title != s {
			t.Errorf("title %q", f.Title)
		}
	}
	for _, s := range []string{"The allow rule `Bash(npm run *)` in .claude/settings.local.json matches it",
		"The db:diff script runs `prisma migrate diff --shadow-database-url`",
		"A rule covers `npx prisma migrate diff --shadow-database-url`, not `npm run db:diff`", "In auto mode, Claude Code drops broad allow rules"} {
		if !strings.Contains(f.Detail, s) {
			t.Errorf("detail lacks %q:\n%s", s, f.Detail)
		}
	}
	if !strings.Contains(f.Checked, ".claude/settings.local.json and ~/.claude/settings.json") || !strings.HasPrefix(f.Fix[0], `Add "Bash(* db:diff*)" and "Bash(*prisma migrate diff*)" to permissions.ask.`) {
		t.Errorf("%q %q", f.Checked, f.Fix)
	}
	if !strings.HasPrefix(ScoreLine(r.Score), "Blast radius: D · 0 write paths with no check · fix this first: An agent can run `npm run db:diff`") {
		t.Errorf("%s", ScoreLine(r.Score))
	}
	env := r.Findings[1]
	if env.Title != "An agent can read DATABASE_URL in .env without a prompt" || !strings.Contains(env.Fix[0], `"Read(.env)"`) {
		t.Errorf("%+v", env)
	}
	// claude-code#80868's own rules: ask on the npx spelling, auto mode. The bare spelling goes to
	// the classifier: medium, since the settings chose auto.
	nr := r.Findings[2]
	if nr.ID != "shell_destructive_no_rule" || nr.Severity != "medium" || nr.Title != "No ask or deny rule covers `prisma db push --force-reset`" ||
		!strings.Contains(nr.Detail, "A rule covers `npx prisma db push --force-reset`, not `prisma db push --force-reset`") ||
		!strings.Contains(nr.Detail, "defaultMode auto in ~/.claude/settings.json sends it to a classifier") ||
		!strings.Contains(nr.Detail, "No rule covers `prisma migrate reset --force` either.") ||
		!strings.HasPrefix(nr.Fix[0], `Add "Bash(*prisma db push*)" and "Bash(*prisma migrate reset*)" to permissions.ask.`) ||
		strings.Join(nr.Paths, ",") != "package.json,~/.claude/settings.json" {
		t.Errorf("%+v", nr)
	}
}

// TestShellFixClears: the ask rules a finding suggests, added to the project's settings, leave no
// shell finding behind.
func TestShellFixClears(t *testing.T) {
	reRule := regexp.MustCompile(`"(Bash\([^"]*\))"`)
	for _, name := range []string{"shell-allow-all", "shell-prefix-ask"} {
		fx := shellFixtures[name]
		r := shellFixture(t, name, fx.files, fx.home)
		var rules []string
		for _, f := range r.Findings {
			if strings.HasPrefix(f.ID, "shell_") {
				for _, m := range reRule.FindAllStringSubmatch(f.Fix[0], -1) {
					rules = append(rules, m[1])
				}
			}
		}
		if len(rules) == 0 {
			t.Fatalf("%s: no rules suggested", name)
		}
		files := map[string]string{}
		for k, v := range fx.files {
			files[k] = v
		}
		b, _ := json.Marshal(map[string]any{"permissions": map[string]any{"ask": rules, "deny": []string{"Read(.env)", "Read(.env.*)"}}})
		key := ".claude/settings.json"
		if _, ok := files[key]; ok {
			key = ".claude/settings.local.json"
		}
		files[key] = string(b)
		fixed := shellFixture(t, name, files, fx.home)
		for _, f := range fixed.Findings {
			if strings.HasPrefix(f.ID, "shell_") || f.ID == "env_database_url_readable" {
				t.Errorf("%s with %v: %s remains: %s", name, rules, f.ID, f.Title)
			}
		}
		// The worked example on docs-site reference/scan-grade: with the suggested rules the
		// #80868 shape scores 100.
		if name == "shell-prefix-ask" && (fixed.Score.Points != 100 || fixed.Score.Grade != "A") {
			t.Errorf("%s fixed: %s %d, want A 100", name, fixed.Score.Grade, fixed.Score.Points)
		}
	}
}

// TestShellCases: no settings at all, bypass mode in user settings, nested project settings, an
// unquotable rule, Gemfile and pyproject dependencies.
func TestShellCases(t *testing.T) {
	ids := func(r *Report) string {
		var s []string
		for _, f := range r.Findings {
			s = append(s, f.Severity+":"+f.ID+":"+strings.Join(f.Paths, ","))
		}
		return strings.Join(s, " ")
	}
	// No Claude Code settings: Manual asks, auto lets a classifier decide; a low finding, no points.
	r := shellFixture(t, "x", map[string]string{"Gemfile": "source \"https://rubygems.org\"\ngem \"rails\", \"~> 8.0\"\ngem 'pg'\n"}, nil)
	if got := ids(r); got != "low:shell_destructive_no_rule:Gemfile" || r.Score.Points != 100 {
		t.Fatalf("%s %d", got, r.Score.Points)
	}
	if f := r.Findings[0]; f.Title != "No ask or deny rule covers `rails db:drop`" || !strings.Contains(f.Detail, "`rails db:reset` and `rails db:schema:load`") ||
		!strings.HasPrefix(f.Fix[0], `Add "Bash(* db:drop*)", "Bash(* db:reset*)" and "Bash(* db:schema:load*)" to permissions.ask.`) || !strings.HasPrefix(f.Checked, "No Claude Code settings file") {
		t.Fatalf("%+v", f)
	}
	// bypassPermissions in user settings: everything unmatched runs; ask rules still ask.
	r = shellFixture(t, "x", map[string]string{"requirements.txt": "alembic==1.16.5\n"},
		map[string]string{".claude/settings.json": `{"permissions":{"defaultMode":"bypassPermissions","ask":["Bash(alembic downgrade *)"]}}`})
	if f := r.Findings[0]; f.ID != "shell_destructive_unprompted" || f.Title != "An agent can run `uv run alembic downgrade base` here without a prompt" ||
		!strings.HasPrefix(f.Detail, "defaultMode bypassPermissions in ~/.claude/settings.json skips prompts") {
		t.Fatalf("%+v", f)
	}
	// The same mode in a project file doesn't take effect: Manual mode.
	r = shellFixture(t, "x", map[string]string{"requirements.txt": "alembic\n", ".claude/settings.json": `{"permissions":{"defaultMode":"bypassPermissions"}}`}, nil)
	if got := ids(r); got != "low:shell_destructive_no_rule:requirements.txt" {
		t.Fatalf("%s", got)
	}
	// Nested project settings govern their own directory; the root's deny doesn't reach them, and the
	// root's .env stays covered.
	r = shellFixture(t, "x", map[string]string{
		".claude/settings.json":              `{"permissions":{"deny":["Read(/.env)"]}}`,
		".env":                               "DATABASE_URL=x\n",
		"services/api/.claude/settings.json": `{"permissions":{"allow":["Bash(npx knex:*)"]}}`,
		"services/api/package.json":          `{"dependencies":{"knex":"^3.1.0"}}`,
		"services/api/.env":                  "PG_URL=y\n",
	}, nil)
	if got := ids(r); got != "high:shell_destructive_unprompted:services/api/package.json,services/api/.claude/settings.json medium:env_database_url_readable:services/api/.env" {
		t.Fatalf("%s", got)
	}
	// A long allow rule that matches is named by its file only.
	long := "Bash(npx prisma db push" + strings.Repeat("*", 70) + "--force-reset)"
	r = shellFixture(t, "x", map[string]string{"package.json": `{"devDependencies":{"prisma":"7"}}`,
		".claude/settings.local.json": `{"permissions":{"allow":["` + long + `","Bash(PGPASSWORD=canary-rule psql)"]}}`}, nil)
	if f := r.Findings[0]; !strings.HasPrefix(f.Detail, "An allow rule in .claude/settings.local.json matches it") {
		t.Fatalf("%+v", f)
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "canary-rule") || strings.Contains(string(b), "*****") {
		t.Fatal("a rule reached the report")
	}
	// dontAsk denies what no rule allows: nothing to report.
	r = shellFixture(t, "x", map[string]string{"package.json": `{"devDependencies":{"typeorm":"0.3"}}`, ".claude/settings.json": `{"permissions":{"defaultMode":"dontAsk"}}`}, nil)
	if len(r.Findings) != 0 || r.ClaudeSettings[0].DefaultMode != "dontAsk" {
		t.Fatalf("%s %+v", ids(r), r.ClaudeSettings)
	}
	// An invalid settings file is listed and ignored.
	r = shellFixture(t, "x", map[string]string{".claude/settings.json": `{"permissions":`}, nil)
	if len(r.ClaudeSettings) != 1 || r.ClaudeSettings[0].Error != "not valid JSON" {
		t.Fatalf("%+v", r.ClaudeSettings)
	}
	// One value of the wrong type costs only that key: the deny rules still count.
	r = shellFixture(t, "x", map[string]string{"package.json": `{"devDependencies":{"knex":"3"}}`,
		".claude/settings.json": `{"permissions":{"disableBypassPermissionsMode":true,"allow":"Bash","deny":["Bash(*knex migrate:rollback*)"]}}`}, nil)
	if c := r.ClaudeSettings[0]; c.Deny != 1 || c.Allow != 0 || c.Error != "permissions.allow, permissions.disableBypassPermissionsMode not in the shape Claude Code reads; left out" || len(r.Findings) != 0 {
		t.Fatalf("%+v %s", c, ids(r))
	}
	if !strings.Contains(RenderMarkdown(r), "0 allow, 0 ask, 1 deny · permissions.allow") {
		t.Fatal("the Found list drops the counts")
	}
	// disableAutoMode in project settings: auto in user settings starts in Manual, so the open
	// spelling is low and says why.
	r = shellFixture(t, "x", map[string]string{"package.json": `{"devDependencies":{"drizzle-kit":"0.31"}}`,
		".claude/settings.json": `{"permissions":{"disableAutoMode":"disable"}}`},
		map[string]string{".claude/settings.json": `{"permissions":{"defaultMode":"auto"}}`})
	if f := r.Findings[0]; f.Severity != "low" || !strings.Contains(f.Detail, "disableAutoMode in .claude/settings.json turns auto mode off, so Claude Code asks first.") {
		t.Fatalf("%+v", f)
	}
}

// TestDatabaseCLICatalog: every database_cli entry names commands, each with what it does and a
// public source; incidents are public links.
func TestDatabaseCLICatalog(t *testing.T) {
	n := 0
	for _, e := range catalog {
		if e.Kind != "database_cli" {
			continue
		}
		n++
		if len(e.Commands) == 0 || len(e.Tools) != 0 || e.Database {
			t.Errorf("%s: commands only", e.ID)
		}
		if _, ok := runners[ecosystem(&e)]; !ok {
			t.Errorf("%s: no runners for %s", e.ID, ecosystem(&e))
		}
		for _, c := range e.Commands {
			if c.Run == "" || c.Does == "" || !strings.HasPrefix(c.Source, "https://") || (c.NeedsArgs && c.Args == "") {
				t.Errorf("%s: %+v", e.ID, c)
			}
		}
		for _, s := range e.Incidents {
			if !regexp.MustCompile(`^https://github\.com/anthropics/claude-code/issues/\d+$`).MatchString(s) {
				t.Errorf("%s: incident %s", e.ID, s)
			}
		}
	}
	if n != 7 {
		t.Fatalf("%d database CLIs, want prisma, drizzle-kit, alembic, rails, knex, typeorm and sequelize", n)
	}
}
