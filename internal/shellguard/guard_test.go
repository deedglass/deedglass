package shellguard

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// tcase is one command from a testdata file, with the catalog id it must produce ("" for none).
type tcase struct {
	line int
	want string
	cmd  string
}

// readEntries reads a testdata file: entries separated by blank lines; a "# want: <id>" line before
// a command names the id it must produce; any other line starting "# " outside a command is a comment.
func readEntries(t *testing.T, name string) []tcase {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []tcase
	var cur *tcase
	var lines []string
	flush := func() {
		if cur != nil && len(lines) > 0 {
			cur.cmd = strings.Join(lines, "\n")
			out = append(out, *cur)
		}
		cur, lines = nil, nil
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		l := sc.Text()
		switch {
		case strings.TrimSpace(l) == "":
			flush()
		case len(lines) == 0 && strings.HasPrefix(l, "# want: "):
			id, _, _ := strings.Cut(strings.TrimPrefix(l, "# want: "), " ")
			cur = &tcase{line: n, want: id}
		case len(lines) == 0 && (strings.HasPrefix(l, "# ") || l == "#"):
		default:
			if cur == nil {
				cur = &tcase{line: n}
			}
			lines = append(lines, l)
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func ids(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}

func has(fs []Finding, id string) bool {
	for _, f := range fs {
		if f.ID == id {
			return true
		}
	}
	return false
}

// TestIncidents: the commands from the public Claude Code issues each produce an ask (FEAT-07's
// done line: #80868, #36183, #33183 and #26913, plus #46684).
func TestIncidents(t *testing.T) {
	es := readEntries(t, "incidents.txt")
	issues := map[string]bool{}
	for _, e := range es {
		fs := Check(e.cmd, Options{})
		if !has(fs, e.want) {
			t.Errorf("incidents.txt:%d: want %s, got %v\n%s", e.line, e.want, ids(fs), e.cmd)
			continue
		}
		if r := Reason(fs, false); !strings.HasPrefix(r, "Deedglass asks first: ") {
			t.Errorf("incidents.txt:%d: reason %q", e.line, r)
		}
	}
	b, _ := os.ReadFile(filepath.Join("testdata", "incidents.txt"))
	for _, m := range regexp.MustCompile(`claude-code#(\d+)`).FindAllStringSubmatch(string(b), -1) {
		issues[m[1]] = true
	}
	for _, n := range []string{"80868", "36183", "33183", "26913"} {
		if !issues[n] {
			t.Errorf("incidents.txt has no command from claude-code#%s", n)
		}
	}
}

// TestAsks: every catalog shape, through wrappers and compound forms, produces its id.
func TestAsks(t *testing.T) {
	es := readEntries(t, "asks.txt")
	if len(es) < 100 {
		t.Fatalf("asks.txt has %d entries, want at least 100", len(es))
	}
	covered := map[string]bool{}
	for _, e := range es {
		if e.want == "" {
			t.Errorf("asks.txt:%d: entry without a want line", e.line)
			continue
		}
		entry(e.want) // panics on an unknown id
		covered[e.want] = true
		if fs := Check(e.cmd, Options{}); !has(fs, e.want) {
			t.Errorf("asks.txt:%d: want %s, got %v\n%s", e.line, e.want, ids(fs), e.cmd)
		}
	}
	for _, c := range Catalog {
		switch c.ID {
		case "too-long", "too-nested": // TestLimitsAsk
			continue
		}
		if !covered[c.ID] {
			t.Errorf("catalog entry %s has no case in asks.txt", c.ID)
		}
	}
}

// TestOrdinary: 200 ordinary development commands pass untouched (the false-positive budget is 0).
func TestOrdinary(t *testing.T) {
	es := readEntries(t, "ordinary.txt")
	if len(es) < 200 {
		t.Fatalf("ordinary.txt has %d entries, want at least 200", len(es))
	}
	o := Options{Dir: "/repo", ReadFile: fakeFS(map[string]string{
		"/repo/package.json":       `{"scripts":{"build":"tsc -p .","dev":"next dev","test":"vitest run","lint":"eslint .","worker":"node worker.js"}}`,
		"/repo/queries/report.sql": "SELECT customer_id, sum(total) FROM orders GROUP BY 1;\n",
		"/repo/prisma/seed.sql":    "INSERT INTO flags (name) VALUES ('beta');\n",
		"/repo/tables.txt":         "users\norders\n",
	})}
	for _, e := range es {
		if e.want != "" {
			t.Errorf("ordinary.txt:%d: has a want line", e.line)
		}
		if fs := Check(e.cmd, o); len(fs) > 0 {
			t.Errorf("ordinary.txt:%d: want nothing, got %v (%s)\n%s", e.line, ids(fs), Reason(fs, false), e.cmd)
		}
	}
}

// TestLimits pins what the guard does not see, so docs/SHELL_GUARD.md stays true: each of these
// would destroy data and passes untouched. A change that starts catching one moves it to asks.txt.
func TestLimits(t *testing.T) {
	es := readEntries(t, "limits.txt")
	if len(es) == 0 {
		t.Fatal("limits.txt is empty")
	}
	for _, e := range es {
		if fs := Check(e.cmd, Options{}); len(fs) > 0 {
			t.Errorf("limits.txt:%d: now caught as %v: move it to asks.txt and update docs/SHELL_GUARD.md\n%s", e.line, ids(fs), e.cmd)
		}
	}
}

func TestLimitsAsk(t *testing.T) {
	long := "echo " + strings.Repeat("a", MaxBytes)
	if fs := Check(long, Options{}); len(fs) != 1 || fs[0].ID != "too-long" {
		t.Fatalf("over MaxBytes: got %v", ids(fs))
	}
	deep := strings.Repeat("$(", maxDepth+2) + "echo hi" + strings.Repeat(")", maxDepth+2)
	if fs := Check(deep, Options{}); !has(fs, "too-nested") {
		t.Fatalf("nested past maxDepth: got %v", ids(fs))
	}
	ok := strings.Repeat("$(", maxDepth-1) + "echo hi" + strings.Repeat(")", maxDepth-1)
	if fs := Check(ok, Options{}); len(fs) != 0 {
		t.Fatalf("nested within maxDepth: got %v", ids(fs))
	}
}

func fakeFS(files map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		if s, ok := files[filepath.ToSlash(name)]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}
}

// TestFiles: package.json scripts, shell scripts and SQL files are read from the working
// directory, each at most once, and named in the reason.
func TestFiles(t *testing.T) {
	read := map[string]int{}
	files := map[string]string{
		"/app/package.json":           `{"scripts":{"db:reset":"prisma migrate reset --force","fresh":"npm run db:reset && npm run seed","seed":"node seed.js","loop":"npm run loop","test":"vitest"}}`,
		"/app/api/package.json":       `{"scripts":{"wipe":"psql -c 'TRUNCATE users'"}}`,
		"/app/scripts/reset.sh":       "#!/bin/sh\nset -e\ndropdb app_dev\ncreatedb app_dev\n",
		"/app/scripts/self.sh":        "sh scripts/self.sh\n",
		"/app/migrations/0005.sql":    "BEGIN;\nDROP TABLE legacy_orders;\nCOMMIT;\n",
		"/app/migrations/0006.sql":    "UPDATE users SET plan = 'free';\n",
		"/app/migrations/0007_ok.sql": "CREATE INDEX ON orders (created_at);\n",
		"/abs/drop.sql":               "DROP SCHEMA app CASCADE;",
	}
	rf := func(name string) ([]byte, error) {
		read[filepath.ToSlash(name)]++
		return fakeFS(files)(name)
	}
	o := Options{Dir: "/app", ReadFile: rf}
	cases := []struct {
		cmd, want, reason string
	}{
		{"npm run db:reset", "prisma-migrate-reset", "Deedglass asks first: prisma migrate reset (in npm run db:reset) drops"},
		{"pnpm db:reset", "prisma-migrate-reset", "(in pnpm run db:reset)"},
		{"yarn fresh", "prisma-migrate-reset", "(in yarn run fresh)"},
		{"cd api && npm run wipe", "sql-truncate", "psql (in npm run wipe) would run TRUNCATE"},
		{"bash scripts/reset.sh", "dropdb", "dropdb (in scripts/reset.sh) drops"},
		{"./scripts/reset.sh", "dropdb", ""},
		{"sh -e scripts/reset.sh", "dropdb", ""},
		{"psql app -f migrations/0005.sql", "sql-drop", "psql reading migrations/0005.sql would run DROP TABLE"},
		{"psql \"$DATABASE_URL\" < migrations/0006.sql", "sql-update-all", "psql reading migrations/0006.sql would run an UPDATE with no WHERE clause"},
		{"npx prisma db execute --file migrations/0005.sql", "sql-drop", "prisma db execute reading migrations/0005.sql"},
		{"psql --file=/abs/drop.sql", "sql-drop", ""},
		{"mysql shop < migrations/0006.sql", "sql-update-all", ""},
		{"cat migrations/0005.sql | psql app", "sql-drop", "psql reading migrations/0005.sql would run DROP TABLE"},
		{"cat scripts/reset.sh | bash", "dropdb", "dropdb (in scripts/reset.sh) drops"},
		{"cat migrations/0007_ok.sql | psql", "", ""},
		{"npm run loop", "", ""},
		{"sh scripts/self.sh", "", ""},
		{"npm test", "", ""},
		{"psql -f migrations/0007_ok.sql", "", ""},
		{"psql -f missing.sql", "", ""},
		{"psql -f \"$FILE\"", "", ""},
	}
	for _, c := range cases {
		for k := range read {
			delete(read, k)
		}
		fs := Check(c.cmd, o)
		if c.want == "" {
			if len(fs) > 0 {
				t.Errorf("%s: want nothing, got %v", c.cmd, ids(fs))
			}
		} else if !has(fs, c.want) {
			t.Errorf("%s: want %s, got %v", c.cmd, c.want, ids(fs))
		} else if r := Reason(fs, false); !strings.Contains(r, c.reason) {
			t.Errorf("%s: reason %q, want it to contain %q", c.cmd, r, c.reason)
		}
		for name, n := range read {
			if n > 1 {
				t.Errorf("%s: read %s %d times", c.cmd, name, n)
			}
		}
	}
	// Without ReadFile, or without a working directory, nothing is read.
	if fs := Check("npm run db:reset", Options{}); len(fs) > 0 {
		t.Errorf("no ReadFile: got %v", ids(fs))
	}
	if fs := Check("npm run db:reset", Options{ReadFile: rf}); len(fs) > 0 {
		t.Errorf("no Dir: got %v", ids(fs))
	}
	// A file over MaxBytes is not read.
	big := Options{Dir: "/b", ReadFile: fakeFS(map[string]string{"/b/x.sql": "DROP TABLE t;" + strings.Repeat(" ", MaxBytes)})}
	if fs := Check("psql -f x.sql", big); len(fs) > 0 {
		t.Errorf("big file: got %v", ids(fs))
	}
}

func TestReason(t *testing.T) {
	fs := Check("npx prisma migrate reset --force && psql -c 'DROP TABLE users'", Options{})
	if len(fs) != 2 {
		t.Fatalf("got %v", ids(fs))
	}
	ask := Reason(fs, false)
	want := "Deedglass asks first: prisma migrate reset drops the database, or every table in it, and applies your migrations again, so its data is gone (and 1 more in this command)."
	if ask != want {
		t.Errorf("ask:\n got %q\nwant %q", ask, want)
	}
	deny := Reason(fs[1:], true)
	want = "Deedglass denied this command because psql would run DROP TABLE, which deletes the table and every row in it; if it is intended, ask the user to run it themselves."
	if deny != want {
		t.Errorf("deny:\n got %q\nwant %q", deny, want)
	}
	if Reason(nil, false) != "" {
		t.Error("no findings: want an empty reason")
	}
	// Duplicates collapse.
	if fs := Check("dropdb a; dropdb a", Options{}); len(fs) != 1 {
		t.Errorf("duplicates: %v", ids(fs))
	}
}

// overclaims reports a reason sentence that uses a word from scripts/banned_words.txt or says more
// than a second check can.
func overclaims(t *testing.T, s string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "banned_words.txt"))
	if err != nil {
		t.Fatal(err)
	}
	words := []string{"sandboxed", "blocks all", "always safe"}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimPrefix(strings.TrimSpace(l), "@until-public "); l != "" && !strings.HasPrefix(l, "#") {
			words = append(words, l)
		}
	}
	for _, w := range words {
		if regexp.MustCompile(`(?i)(^|[^a-z])` + regexp.QuoteMeta(w) + `([^a-z]|$)`).MatchString(s) {
			return true
		}
	}
	return false
}

// TestCatalog: every row has an id, a shape, a why that completes the sentence, a primary https
// source and the date it was checked; ids are unique.
func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Catalog {
		if seen[c.ID] {
			t.Errorf("%s: duplicate id", c.ID)
		}
		seen[c.ID] = true
		if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(c.ID) {
			t.Errorf("%s: id is not kebab-case", c.ID)
		}
		if c.Shape == "" || c.Why == "" {
			t.Errorf("%s: shape or why missing", c.ID)
		}
		if strings.HasSuffix(c.Why, ".") || strings.ContainsAny(c.Why, "!—") || c.Why != strings.TrimSpace(c.Why) {
			t.Errorf("%s: why %q must be a clause: no final period, no exclamation mark or em-dash", c.ID, c.Why)
		}
		if overclaims(t, c.Why) {
			t.Errorf("%s: why %q overclaims", c.ID, c.Why)
		}
		if !strings.HasPrefix(c.Source, "https://") {
			t.Errorf("%s: source %q is not an https URL", c.ID, c.Source)
		}
		if _, err := time.Parse("2006-01-02", c.Checked); err != nil {
			t.Errorf("%s: checked %q is not a date", c.ID, c.Checked)
		}
		if strings.HasPrefix(c.ID, "sql-") || c.ID == "pg-dump-clean-pipe" || strings.HasPrefix(c.ID, "too-") {
			if len(c.progs) > 0 {
				t.Errorf("%s: SQL and limit rows are matched in code, not by program", c.ID)
			}
		} else if len(c.progs) == 0 {
			t.Errorf("%s: no program", c.ID)
		}
	}
}

func TestEntryPanicsOnUnknown(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("entry of an unknown id: want a panic")
		}
	}()
	entry("no-such-entry")
}

// TestOnlyAdds: appending anything to a command that asks never removes a finding.
func TestOnlyAdds(t *testing.T) {
	tails := []string{"", " # comment", "; true", " || true", " && echo ok", " | cat", " &", "\nexit 0", " 2>/dev/null", " )", " '", " \"", " `", " $(", " <<EOF"}
	for _, e := range readEntries(t, "asks.txt") {
		for _, tail := range tails {
			cmd := e.cmd + tail
			if strings.HasSuffix(e.cmd, "\n") || strings.Contains(e.cmd, "<<") {
				continue // a heredoc's terminator must stay last
			}
			if !has(Check(cmd, Options{}), e.want) {
				t.Errorf("asks.txt:%d with tail %q: lost %s", e.line, tail, e.want)
			}
		}
	}
}

func TestReadFileErrorsIgnored(t *testing.T) {
	o := Options{Dir: "/x", ReadFile: func(string) ([]byte, error) { return nil, errors.New("permission denied") }}
	if fs := Check("npm run x; bash a.sh; psql -f a.sql", o); len(fs) > 0 {
		t.Fatalf("got %v", ids(fs))
	}
}

func FuzzCheck(f *testing.F) {
	for _, name := range []string{"asks.txt", "ordinary.txt", "incidents.txt", "limits.txt"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		for _, s := range strings.Split(string(b), "\n\n") {
			f.Add(s)
		}
	}
	files := fakeFS(map[string]string{"/w/package.json": `{"scripts":{"a":"npm run b","b":"npm run a && dropdb x"}}`, "/w/x.sh": "bash x.sh"})
	f.Fuzz(func(t *testing.T, s string) {
		start := time.Now()
		fs := Check(s, Options{Dir: "/w", ReadFile: files})
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("took %s", d)
		}
		for _, x := range fs {
			entry(x.ID)
			if x.What == "" {
				t.Fatal("empty finding")
			}
		}
		if len(fs) > 0 && !strings.HasPrefix(Reason(fs, false), "Deedglass asks first: ") {
			t.Fatal("reason")
		}
	})
}

// TestLargeInputs: pathological commands near MaxBytes finish quickly and keep at most
// MaxFindings findings.
func TestLargeInputs(t *testing.T) {
	n := func(s string) int { return (MaxBytes - 64) / len(s) }
	cases := map[string]string{
		"dropdb":     strings.Repeat("dropdb a;", n("dropdb a;")),
		"distinct":   "",
		"sql":        "psql -c '" + strings.Repeat("DROP TABLE t; ", n("DROP TABLE t; ")) + "'",
		"quotes":     strings.Repeat("'\"", n("'\"")),
		"backticks":  strings.Repeat("`a ", n("`a ")),
		"subst":      strings.Repeat("$(a) ", n("$(a) ")),
		"subshells":  strings.Repeat("( a ) ", n("( a ) ")),
		"heredocs":   strings.Repeat("a <<E\n", n("a <<E\n")),
		"expansions": strings.Repeat("${a:-$(b)}", n("${a:-$(b)}")),
		"files":      strings.Repeat("psql -f a.sql ", n("psql -f a.sql ")),
		"pipes":      strings.Repeat("echo DROP TABLE t | psql;", n("echo DROP TABLE t | psql;")),
	}
	var b strings.Builder
	for i := 0; b.Len() < MaxBytes-64; i++ {
		b.WriteString("dropdb a" + strconvItoa(i) + ";")
	}
	cases["distinct"] = b.String()
	o := Options{Dir: "/w", ReadFile: fakeFS(map[string]string{})}
	for name, cmd := range cases {
		start := time.Now()
		fs := Check(cmd, o)
		if d := time.Since(start); d > 3*time.Second || testing.Verbose() {
			t.Logf("%s: took %s", name, d)
		}
		if len(fs) > MaxFindings {
			t.Errorf("%s: %d findings", name, len(fs))
		}
	}
	k := &checker{}
	for i := 0; i < 3*MaxFindings; i++ {
		k.addWhy("dropdb", "dropdb a"+strconvItoa(i), "", "drops a whole Postgres database")
	}
	if len(k.out) != MaxFindings || !strings.Contains(Reason(k.out, false), "(and at least 31 more in this command)") {
		t.Errorf("cap: %d findings, %q", len(k.out), Reason(k.out, false))
	}
}

func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	var d []byte
	for ; i > 0; i /= 10 {
		d = append([]byte{byte('0' + i%10)}, d...)
	}
	return string(d)
}

// TestDocListsCatalog: docs/SHELL_GUARD.md lists every catalog row with its source, and keeps the
// wording that says what the guard is.
func TestDocListsCatalog(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "SHELL_GUARD.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, c := range Catalog {
		if !strings.Contains(doc, "| `"+c.ID+"` |") || !strings.Contains(doc, "("+c.Source+")") {
			t.Errorf("docs/SHELL_GUARD.md: no row for %s with its source (deedglass guard shell --list --json prints them)", c.ID)
		}
	}
	if !strings.Contains(doc, "a second check, not a sandbox") {
		t.Error(`docs/SHELL_GUARD.md must say "a second check, not a sandbox"`)
	}
}
