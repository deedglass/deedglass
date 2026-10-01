package policy

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/internal/ids"
)

const base = `deedglass: policy/v1
name: refunds
description: Refunds for the support agents.
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
budgets:
  - id: agent-rate
    window: minute
    max_calls: 60
rules:
  - id: small-refunds
    match: { tool: stripe.create_refund, agent: [support-*] }
    when: args.amount <= minor("50.00 usd")
    decision: allow
  - id: large-refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds over $50.00 wait for a human.
`

const reindented = `deedglass: policy/v1
name: refunds
description: Refunds for the support agents.
defaults:
    decision: deny
    reason: No rule allows this action. Add a rule in Console › Policies to allow it.
budgets:
-   id: agent-rate
    window: minute
    max_calls: 60
rules:
-   id: small-refunds
    match: { tool: stripe.create_refund, agent: [support-*] }
    when: args.amount <= minor("50.00 usd")
    decision: allow
-   id: large-refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds over $50.00 wait for a human.
`

func mustHash(t *testing.T, src string) string {
	t.Helper()
	c, d := Compile([]byte(src), Options{})
	if c == nil {
		t.Fatalf("did not compile: %+v\n%s", d, src)
	}
	return c.VersionHash
}

func TestHashStability(t *testing.T) {
	h := mustHash(t, base)
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(h) {
		t.Fatalf("hash shape: %s", h)
	}
	same := map[string]string{
		"re-indented":       reindented,
		"keys reordered":    strings.Replace(base, "    window: minute\n    max_calls: 60\n", "    max_calls: 60\n    window: minute\n", 1),
		"flow to block":     strings.Replace(base, "    match: { tool: stripe.create_refund }\n", "    match:\n      tool: stripe.create_refund\n", 1),
		"comments":          "# Acme refunds\n" + strings.Replace(base, "rules:\n", "rules: # first match wins\n  # small ones run\n", 1),
		"quoting":           strings.Replace(base, "tool: stripe.create_refund }\n    decision: require", "tool: \"stripe.create_refund\" }\n    decision: require", 1),
		"when spaces":       strings.Replace(base, `args.amount <= minor("50.00 usd")`, `args.amount   <=   minor( "50.00 usd" )`, 1),
		"when quotes":       strings.Replace(base, `args.amount <= minor("50.00 usd")`, `'args.amount <= minor("50.00 usd")'`, 1),
		"when folded":       strings.Replace(base, `when: args.amount <= minor("50.00 usd")`, "when: >-\n      args.amount <=\n      minor(\"50.00 usd\")", 1),
		"crlf":              strings.ReplaceAll(base, "\n", "\r\n"),
		"bom":               "\ufeff" + base,
		"list of one":       strings.Replace(base, "agent: [support-*]", "agent: support-*", 1),
		"duplicates":        strings.Replace(base, "agent: [support-*]", "agent: [support-*, support-*]", 1),
		"explicit defaults": strings.Replace(base, "    decision: allow\n", "    decision: allow\n    verify: L0\n", 1),
	}
	for name, src := range same {
		if got := mustHash(t, src); got != h {
			t.Errorf("%s: hash moved: %s != %s", name, got, h)
		}
	}
	diff := map[string]string{
		"rules swapped": strings.Replace(base, `  - id: small-refunds
    match: { tool: stripe.create_refund, agent: [support-*] }
    when: args.amount <= minor("50.00 usd")
    decision: allow
  - id: large-refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds over $50.00 wait for a human.
`, `  - id: large-refunds
    match: { tool: stripe.create_refund }
    decision: require_approval
    reason: Refunds over $50.00 wait for a human.
  - id: small-refunds
    match: { tool: stripe.create_refund, agent: [support-*] }
    when: args.amount <= minor("50.00 usd")
    decision: allow
`, 1),
		"reason edited": strings.Replace(base, "wait for a human.", "wait for a person.", 1),
		"60 to 61":      strings.Replace(base, "max_calls: 60", "max_calls: 61", 1),
		"verify L1":     strings.Replace(base, "    decision: allow\n", "    decision: allow\n    verify: L1\n", 1),
		"description":   strings.Replace(base, "Refunds for the support agents.", "Refunds for support.", 1),
		"when changed":  strings.Replace(base, `minor("50.00 usd")`, `minor("50.01 usd")`, 1),
	}
	seen := map[string]string{h: "base"}
	for name, src := range diff {
		got := mustHash(t, src)
		if prev, ok := seen[got]; ok {
			t.Errorf("%s: same hash as %s", name, prev)
		}
		seen[got] = name
	}
}

func TestDeterminism(t *testing.T) {
	src := []byte(base + "  - id: bad\n    decision: block\n")
	_, want := Compile(src, Options{})
	good, _ := Compile([]byte(base), Options{})
	var wg sync.WaitGroup
	errs := make(chan string, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, got := Compile(src, Options{})
			if !reflect.DeepEqual(got, want) {
				errs <- "diagnostics differ"
			}
			c, _ := Compile([]byte(base), Options{})
			if !bytes.Equal(c.Normalized, good.Normalized) {
				errs <- "normalized bytes differ"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

func TestFmtProperties(t *testing.T) {
	var srcs [][]byte
	for _, p := range lintFixtures(t) {
		if strings.Contains(p, "/valid/") {
			b, _ := os.ReadFile(p)
			srcs = append(srcs, b)
		}
	}
	srcs = append(srcs, []byte(base))
	srcs = append(srcs, generated(500)...)
	for i, src := range srcs {
		c, _ := Compile(src, Options{})
		if c == nil {
			t.Fatalf("source %d doesn't compile", i)
		}
		once, err := Format(src)
		if err != nil {
			t.Fatalf("source %d: %v\n%s", i, err, src)
		}
		twice, err := Format(once)
		if err != nil || !bytes.Equal(once, twice) {
			t.Fatalf("source %d: fmt is not idempotent: %v\n--- once ---\n%s\n--- twice ---\n%s", i, err, once, twice)
		}
		c2, _ := Compile(once, Options{})
		if c2 == nil || c2.VersionHash != c.VersionHash {
			t.Fatalf("source %d: fmt moved the hash", i)
		}
	}
	if _, err := Format([]byte("deedglass: policy/v1\n")); err != ErrNotFormatted {
		t.Fatalf("fmt must refuse an invalid policy, got %v", err)
	}
}

func TestFmtLayout(t *testing.T) {
	src := "rules:\n  - decision: allow\n    id: bb\n    when: 'args.x == \"a: b\"'\n  - id: cc\n    decision: deny\n    reason: No.\n    when: " +
		strings.Repeat("args.a == 1 || ", 8) + "args.b == 2\n" +
		"defaults: {reason: Nope., decision: deny}\nbudgets:\n  - {window: day, id: spend, max_spend: \"5.00 USD\"}\nname: x-y\ndeedglass: policy/v1\n"
	out, err := Format([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"deedglass: policy/v1\nname: x-y\n", "max_spend: \"5.00 usd\"", "  - id: bb\n    when: 'args.x == \"a: b\"'\n    decision: allow",
		"when: >-\n      args.a == 1 ||", "\nrules:\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("fmt output lacks %q:\n%s", want, s)
		}
	}
}

func TestSuggest(t *testing.T) {
	cases := map[string]string{
		"desicion": "decision", "decison": "decision", "reasn": "reason", "resaon": "reason", "mtach": "match",
		"wehn": "when", "verfiy": "verify", "notfiy": "notify", "aproval": "approval", "descripton": "description",
		"budget": "budgets", "rule": "rules", "defualts": "defaults", "nmae": "name", "requst": "request",
		"tol": "tool", "agnet": "agent", "lables": "labels", "sesion": "session", "usgae": "usage",
	}
	cands := append(append(append([]string{}, topKeys...), ruleKeys...), append(append([]string{}, Variables...), matchKeys...)...)
	for typo, want := range cases {
		if got := suggest(typo, cands); got != want {
			t.Errorf("suggest(%q) = %q, want %q", typo, got, want)
		}
	}
	if got := suggest("zzzzzz", cands); got != "" {
		t.Errorf("no suggestion expected, got %q", got)
	}
	if d := damerau("ab", "ba"); d != 1 {
		t.Errorf("transposition costs 1, got %d", d)
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		p, s string
		ok   bool
	}{
		{"*", "", true}, {"*", "anything.at.all", true}, {"**", "x", true},
		{"postgres.*", "postgres.execute", true}, {"postgres.*", "postgres.a.b", true}, {"postgres.*", "postgres", false},
		{"postgres.*", "postgresql.execute", false}, {"postgres.*", "Postgres.execute", false},
		{"*.execute", "postgres.execute", true}, {"*.execute", "postgres.executes", false},
		{"stripe.create_?efund", "stripe.create_refund", true}, {"stripe.create_?efund", "stripe.create_refunds", false},
		{"?", "", false}, {"?", "a", true}, {"?", "ab", false}, {"a?c", "abc", true}, {"a?c", "ac", false},
		{"a*b*c", "abc", true}, {"a*b*c", "aXXbYYc", true}, {"a*b*c", "aXXbYY", false}, {"*a*", "banana", true},
		{"exact", "exact", true}, {"exact", "exac", false}, {"exact", "exactly", false},
		{"github.*.merge", "github.repo.merge", true}, {"github.*.merge", "github.merge", false},
		{"mcp:tool-Name", "mcp:tool-Name", true}, {"?*", "", false}, {"?*", "x", true}, {"*?", "é", true},
	}
	for _, c := range cases {
		g, ok := CompileGlob(c.p)
		if !ok {
			t.Fatalf("CompileGlob(%q) refused", c.p)
		}
		if got := g.Match(c.s); got != c.ok {
			t.Errorf("glob %q on %q = %v, want %v", c.p, c.s, got, c.ok)
		}
	}
	for _, bad := range []string{"postgres.[x]", "a b", "a/b", "é", "a{b}"} {
		if _, ok := CompileGlob(bad); ok {
			t.Errorf("CompileGlob(%q) accepted", bad)
		}
	}
	a, _ := CompileGlob("stripe.*")
	b, _ := CompileGlob("stripe.create_refund")
	c, _ := CompileGlob("stripe.create_*")
	d, _ := CompileGlob("*.refund")
	if !a.covers(b) || !a.covers(c) || c.covers(a) || a.covers(d) || d.covers(b) {
		t.Error("covers is wrong")
	}
	if n := testing.AllocsPerRun(100, func() { a.Match("stripe.create_refund"); d.Match("x.refund"); c.Match("stripe.x") }); n != 0 {
		t.Errorf("glob matching allocates %v times", n)
	}
}

// TestDeclaredFields pins the CEL-visible facts: every field has an explicit cel tag, and the
// list only changes on purpose (S11 fills exactly these).
func TestDeclaredFields(t *testing.T) {
	var got []string
	for _, name := range Variables {
		typ, ok := factTypes[name]
		if !ok {
			got = append(got, name+": map(string, dyn)")
			continue
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Tag.Get("cel") == "" {
				t.Errorf("%s.%s has no cel tag", typ.Name(), f.Name)
			}
		}
		got = append(got, name+": "+strings.Join(celFields(typ), " "))
	}
	want := []string{
		"tool: name connector connector_type side_effect type op", // S16: type, op
		"args: map(string, dyn)",
		"request: mode bytes intent_signed digest http", // S16: http {method, path, host}
		"sql: present kind category kinds statements multi writes write_kinds has_where unbounded_write where_trivial has_returning cte_write select_into copy_program explain_analyze locks_rows tables tables_complete functions dangerous_functions write_functions unknown_functions ambiguous reasons bytes",
		"agent: id name labels status",
		"user: ref sub iss",
		"session: ref",
		"org: id timezone",
		"time: now hour minute weekday date",
		"usage: calls_minute calls_hour calls_today spend_today currency budgets",
		"anomaly: active",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("declared fields changed:\n got %q\nwant %q", got, want)
	}
	env, err := Env()
	if err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{"tool.name", "sql.tables", "agent.labels", "time.now", "usage.budgets", "anomaly.active", "args.x"} {
		if _, iss := env.Compile(expr); iss.Err() != nil {
			t.Errorf("%s: %v", expr, iss.Err())
		}
	}
}

func TestAgentIDPattern(t *testing.T) {
	if agentIDRE.String() != ids.Pattern("agt") {
		t.Fatalf("agentIDRE %s != ids.Pattern(agt) %s", agentIDRE, ids.Pattern("agt"))
	}
}

func TestBannedWordsCopy(t *testing.T) {
	want, err := os.ReadFile("../../scripts/banned_words.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != bannedWords {
		t.Fatal("internal/policy/bannedwords.txt must equal scripts/banned_words.txt byte for byte: cp scripts/banned_words.txt internal/policy/bannedwords.txt")
	}
}

// TestSourceMap checks that CEL positions map to the right file position in every YAML style.
func TestSourceMap(t *testing.T) {
	cases := []struct{ name, when, token string }{
		{"plain", "when: args.x == requst.y", "requst"},
		{"single", "when: 'args.x == requst.y'", "requst"},
		{"single escape", "when: 'args.x == ''a'' && requst.y'", "requst"},
		{"double", `when: "args.x == \"a\" && requst.y"`, "requst"},
		{"literal", "when: |\n      args.x == 1 &&\n        requst.y", "requst"},
		{"folded", "when: >-\n      args.x == 1 &&\n      requst.y", "requst"},
		{"plain multi-line", "when: args.x == 1 &&\n      requst.y", "requst"},
	}
	for _, c := range cases {
		src := "deedglass: policy/v1\nname: x-y\ndefaults: {decision: deny, reason: No.}\nrules:\n  - id: rr\n    decision: allow\n    " + c.when + "\n"
		_, ds := Compile([]byte(src), Options{})
		if len(ds) != 1 || ds[0].Code != "cel_undeclared" {
			t.Errorf("%s: want one cel_undeclared, got %+v", c.name, ds)
			continue
		}
		lines := strings.Split(src, "\n")
		line := lines[ds[0].Line-1]
		col := ds[0].Col - 1
		if col < 0 || col+len(c.token) > len(line) || line[col:col+len(c.token)] != c.token {
			t.Errorf("%s: %d:%d points at %q, not %q", c.name, ds[0].Line, ds[0].Col, line, c.token)
		}
	}
}

func TestMatcher(t *testing.T) {
	src := "deedglass: policy/v1\nname: x-y\ndefaults: {decision: deny, reason: No.}\nrules:\n  - id: rr\n    decision: allow\n    match:\n      tool: [stripe.*]\n      connector_type: stripe\n      side_effect: [write]\n      agent: [support-*, agt_01k67c04kxevyex3pjyqe0t45d]\n      labels: {env: prod}\n"
	c, ds := Compile([]byte(src), Options{})
	if c == nil {
		t.Fatal(ds)
	}
	m := c.Rules[0].Match
	tool := ToolFacts{Name: "stripe.create_refund", ConnectorType: "stripe", SideEffect: "write"}
	agent := AgentFacts{ID: "agt_x", Name: "support-bot", Labels: map[string]string{"env": "prod", "team": "a"}}
	if !m.Match(tool, agent) {
		t.Error("should match")
	}
	agent2 := agent
	agent2.Name, agent2.ID = "billing", "agt_01k67c04kxevyex3pjyqe0t45d"
	if !m.Match(tool, agent2) {
		t.Error("should match by id")
	}
	for _, f := range []func(*ToolFacts, *AgentFacts){
		func(t *ToolFacts, _ *AgentFacts) { t.Name = "github.merge" },
		func(t *ToolFacts, _ *AgentFacts) { t.ConnectorType = "rest" },
		func(t *ToolFacts, _ *AgentFacts) { t.SideEffect = "read" },
		func(_ *ToolFacts, a *AgentFacts) { a.Name = "billing" },
		func(_ *ToolFacts, a *AgentFacts) { a.Labels = map[string]string{"env": "dev"} },
	} {
		tt, aa := tool, agent
		f(&tt, &aa)
		if m.Match(tt, aa) {
			t.Errorf("should not match: %+v %+v", tt, aa)
		}
	}
	if n := testing.AllocsPerRun(100, func() { m.Match(tool, agent) }); n != 0 {
		t.Errorf("Match allocates %v times", n)
	}
}

// TestErrorDocs: every code has a section in docs/POLICY_ERRORS.md (the doc_url target).
func TestErrorDocs(t *testing.T) {
	b, err := os.ReadFile("../../docs/POLICY_ERRORS.md")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "## ") {
			have[strings.TrimSpace(strings.TrimPrefix(l, "## "))] = true
		}
	}
	seen := map[string]bool{}
	for _, c := range Codes {
		if seen[c.Code] {
			t.Errorf("code %s listed twice", c.Code)
		}
		seen[c.Code] = true
		if !have[c.Code] {
			t.Errorf("docs/POLICY_ERRORS.md has no section for %s", c.Code)
		}
	}
	// Every code the compiler can emit is in the catalog.
	srcs, _ := os.ReadDir(".")
	re := regexp.MustCompile(`"([a-z]+(?:_[a-z]+)+)"`)
	for _, f := range srcs {
		if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f.Name())
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if strings.Contains(string(b), `errorf("`+m[1]+`"`) || strings.Contains(string(b), `warnf("`+m[1]+`"`) || strings.Contains(string(b), `dgIssue("`+m[1]+`"`) || strings.Contains(string(b), `c.err("`+m[1]+`"`) {
				if !seen[m[1]] {
					t.Errorf("%s emits %s, which is not in Codes", f.Name(), m[1])
				}
			}
		}
	}
}

func TestDiagnosticCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("deedglass: policy/v1\nname: x-y\ndefaults: {decision: deny, reason: No.}\nrules:\n")
	for i := 0; i < 70; i++ {
		b.WriteString("  - id: r" + itoa(i) + "x\n    decision: nope\n")
	}
	_, ds := Compile([]byte(b.String()), Options{})
	if len(ds) != MaxDiagnostics+1 || ds[MaxDiagnostics].Code != "more_diagnostics" || !strings.Contains(ds[MaxDiagnostics].Message, "And 20 more (20 errors, 0 warnings).") {
		t.Fatalf("cap: %d diagnostics, last %+v", len(ds), ds[len(ds)-1])
	}
}

func TestRenderers(t *testing.T) {
	src := []byte("deedglass: policy/v1\nname: x-y\ndefaults: {decision: deny, reason: No.}\nrules:\n  - id: rr\n    desicion: allow\n")
	r := Lint(src, Options{File: "a,b.yaml"})
	var gh bytes.Buffer
	RenderGitHub(&gh, r, "a,b.yaml")
	if !strings.HasPrefix(gh.String(), "::error file=a%2Cb.yaml,line=6,col=5::unknown_key: ") {
		t.Errorf("github: %q", gh.String())
	}
	if ShortHash("sha256:3f9a00000000c21e") != "sha256:3f9a…c21e" || ShortHash("x") != "x" {
		t.Error("ShortHash")
	}
	if PlainText("Quote it: `when: '!x'`.") != "Quote it: when: '!x'" || PlainText("It has no `when`.") != "It has no when." {
		t.Error("PlainText")
	}
	if !strings.Contains(string(r.JSON()), `"object": "policy_lint_result"`) {
		t.Error("JSON object")
	}
	var txt bytes.Buffer
	long := []byte("deedglass: policy/v1\nname: x-y\ndefaults: {decision: deny, reason: No.}\nrules:\n  - id: rr\n    decision: allow\n    when: " + strings.Repeat("args.a == 1 && ", 20) + "requst.x\n")
	RenderText(&txt, Lint(long, Options{}), long, "long.yaml", Plain{})
	if !strings.Contains(txt.String(), "…") {
		t.Errorf("long lines are windowed:\n%s", txt.String())
	}
}

// generated returns n small valid policies with varied shapes, for the fmt properties.
func generated(n int) [][]byte {
	tools := []string{"stripe.create_refund", "postgres.execute", "github.*", "slack.post_message", "*"}
	whens := []string{"", "args.amount <= 5000", `sql.kind in ["select", "show"]`, `'!sql.multi'`, `'has(args.x) && args.x == "a: b"'`,
		strings.Repeat("args.a == 1 || ", 7) + "args.b == 2"}
	decs := []string{"allow", "deny", "require_approval"}
	var out [][]byte
	for i := 0; i < n; i++ {
		var b strings.Builder
		b.WriteString("# generated\ndeedglass: policy/v1\nname: gen-" + itoa(i) + "\n")
		if i%3 == 0 {
			b.WriteString("description: Generated policy " + itoa(i) + ".\n")
		}
		b.WriteString("defaults: {decision: deny, reason: No rule allows this.}\n")
		if i%4 == 0 {
			b.WriteString("budgets:\n  - id: b" + itoa(i) + "\n    window: day\n    max_spend: \"" + itoa(i+1) + ".00 USD\"\n")
		}
		b.WriteString("rules:\n")
		for j := 0; j < 1+i%5; j++ {
			d := decs[(i+j)%3]
			b.WriteString("  - id: r" + itoa(j) + "x\n")
			b.WriteString("    match: {tool: \"" + tools[(i*7+j)%len(tools)] + "-" + itoa(j) + "\"}\n")
			if w := whens[(i+j)%len(whens)]; w != "" {
				b.WriteString("    when: " + w + "\n")
			}
			b.WriteString("    decision: " + d + "\n")
			if d != "allow" {
				b.WriteString("    reason: Rule " + itoa(j) + " decides this.\n")
			}
			if d == "require_approval" && j%2 == 0 {
				b.WriteString("    approval:\n      approvers: [\"user:priya@acme.com\", role:admin]\n      expires_in: 2h\n")
			}
		}
		out = append(out, []byte(b.String()))
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }

var _ = sort.Strings
var _ = yaml.Node{}
