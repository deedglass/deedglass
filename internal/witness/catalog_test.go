package witness

import (
	"bufio"
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

var update = flag.Bool("update", false, "rewrite docs/witness/REASONS.md and the testdata goldens")

const catalogFile = "internal/witness/catalog.go"

// bannedMatcher reads scripts/banned_words.txt the way scripts/banned_words.sh does: whole words or
// phrases, case-insensitive, a space matching any whitespace, and .banned-words-allow entries for
// one path honoured.
func bannedMatcher(t testing.TB, path string) func(string) []string {
	t.Helper()
	root := moduleRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "scripts", "banned_words.txt"))
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	if a, err := os.ReadFile(filepath.Join(root, ".banned-words-allow")); err == nil {
		for _, line := range strings.Split(string(a), "\n") {
			parts := strings.SplitN(line, "::", 3)
			if len(parts) == 3 && parts[0] == path {
				allowed[strings.ToLower(parts[1])] = true
			}
		}
	}
	var phrases []string
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if p, ok := strings.CutPrefix(line, "@until-public "); ok {
			if os.Getenv("DEEDGLASS_REPO_PUBLIC") == "1" {
				continue
			}
			line = p
		}
		if !allowed[strings.ToLower(line)] {
			phrases = append(phrases, line)
		}
	}
	if len(phrases) < 20 {
		t.Fatalf("only %d banned phrases read", len(phrases))
	}
	sort.Slice(phrases, func(i, j int) bool { return len(phrases[i]) > len(phrases[j]) })
	alts := make([]string, len(phrases))
	for i, p := range phrases {
		alts[i] = strings.ReplaceAll(regexp.QuoteMeta(p), `\ `, `\s+`)
		alts[i] = strings.ReplaceAll(alts[i], " ", `\s+`)
	}
	re := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(` + strings.Join(alts, "|") + `)(?:$|[^A-Za-z0-9_])`)
	return func(s string) []string {
		var out []string
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			out = append(out, m[1])
		}
		return out
	}
}

var rePlaceholder = regexp.MustCompile(`\{([a-z_]+)\}`)

func TestCatalog(t *testing.T) {
	banned := bannedMatcher(t, catalogFile)
	// Two banned words, assembled at run time so the repo scan doesn't see them here.
	if hits := banned("This is " + "guaran" + "teed and " + "seam" + "less."); len(hits) != 2 {
		t.Fatalf("the banned-word matcher is broken: %v", hits)
	}
	if hits := banned("asynchronous receipts_table"); len(hits) != 0 {
		t.Fatalf("the banned-word matcher matches inside words: %v", hits)
	}
	seen := map[Code]bool{}
	for _, e := range Catalog() {
		t.Run(string(e.Code), func(t *testing.T) {
			if seen[e.Code] {
				t.Fatal("duplicate code")
			}
			seen[e.Code] = true
			if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(string(e.Code)) {
				t.Errorf("code isn't snake_case")
			}
			switch e.Class {
			case Pass, Violation, Gap, Note:
			default:
				t.Errorf("class %q", e.Class)
			}
			switch e.Levels {
			case "L0", "L1", "L2", "L1, L2", "any", "none":
			default:
				t.Errorf("levels %q", e.Levels)
			}
			if e.Next == "" || !strings.HasSuffix(e.Next, ".") {
				t.Errorf("next step %q must be a sentence", e.Next)
			}
			used := map[string]bool{}
			for _, m := range rePlaceholder.FindAllStringSubmatch(e.Template, -1) {
				used[m[1]] = true
				if _, ok := e.Sample[m[1]]; !ok {
					t.Errorf("placeholder {%s} has no sample value", m[1])
				}
			}
			for k := range e.Sample {
				if !used[k] {
					t.Errorf("sample value %q isn't a placeholder", k)
				}
			}
			r := R(L1, e.Code, "test", e.Sample)
			if r.Class != e.Class || r.Code != e.Code || r.Level != L1 || r.Check != "test" {
				t.Errorf("R gave %+v", r)
			}
			msg := r.Message
			if n := utf8.RuneCountInString(msg); n > 200 {
				t.Errorf("%d characters: %s", n, msg)
			}
			if strings.ContainsAny(msg, "{}") {
				t.Errorf("unrendered placeholder: %s", msg)
			}
			if !strings.HasSuffix(msg, ".") {
				t.Errorf("doesn't end with a period: %s", msg)
			}
			if strings.Contains(msg+e.Next, "!") {
				t.Errorf("exclamation mark: %s", msg)
			}
			if strings.ContainsAny(msg+e.Next, "—–") {
				t.Errorf("dash: %s", msg)
			}
			// Sentence case, unless the sentence opens with a name ({tool}, {readback}).
			if first, _ := utf8.DecodeRuneInString(e.Template); first != '{' && !unicode.IsUpper(first) {
				t.Errorf("not sentence case: %s", msg)
			}
			if n := strings.Count(msg, ". ") + 1; n > 2 {
				t.Errorf("%d sentences: %s", n, msg)
			}
			for _, s := range []string{e.Template, msg, e.Next} {
				if hits := banned(s); len(hits) > 0 {
					t.Errorf("banned words %v in %q", hits, s)
				}
			}
		})
	}
	if len(seen) != len(entries) {
		t.Errorf("Catalog() has %d codes, entries %d", len(seen), len(entries))
	}
	for _, c := range Reserved {
		if _, ok := Lookup(c); ok {
			t.Errorf("reserved code %s is registered", c)
		}
	}
	if _, ok := Lookup(CodeReadbackMatched); !ok {
		t.Error("Lookup misses readback_matched")
	}
	cs := Catalog()
	if !sort.SliceIsSorted(cs, func(i, j int) bool { return cs[i].Code < cs[j].Code }) {
		t.Error("Catalog() isn't sorted")
	}
}

func TestRUnknownCodePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("R accepted an unknown code under go test")
		}
	}()
	R(L0, "not_a_code", "status", nil)
}

func TestRender(t *testing.T) {
	cases := []struct {
		tmpl string
		vars map[string]any
		want string
	}{
		{"{path} is {observed}, not {expected}.", map[string]any{"path": "status", "observed": "pending", "expected": "shipped"}, `status is "pending", not "shipped".`},
		{"{observed} {expected}", map[string]any{"observed": 42, "expected": nil}, `42 null`},
		{"missing {x}.", nil, "missing unknown."},
		{"open {brace", nil, "open {brace"},
		{"{name}", map[string]any{"name": strings.Repeat("a", 80)}, strings.Repeat("a", 59) + "…"},
	}
	for _, c := range cases {
		if got := render(c.tmpl, c.vars); got != c.want {
			t.Errorf("render(%q) = %q, want %q", c.tmpl, got, c.want)
		}
	}
}

func TestReasonsDoc(t *testing.T) {
	path := filepath.Join(moduleRoot(t), "docs", "witness", "REASONS.md")
	want := reasonsDoc()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; regenerate it with go generate ./internal/witness", err)
	}
	if string(got) != want {
		t.Fatal("docs/witness/REASONS.md is out of date with internal/witness/catalog.go; regenerate it with go generate ./internal/witness")
	}
	if strings.ContainsAny(want, "—–") {
		t.Error("REASONS.md has a dash")
	}
	if hits := bannedMatcher(t, "docs/witness/REASONS.md")(want); len(hits) > 0 {
		t.Errorf("REASONS.md has banned words: %v", hits)
	}
}

func reasonsDoc() string {
	var b strings.Builder
	b.WriteString("# Witness reasons\n\n")
	b.WriteString("Every Witness verdict lists its reasons: what each check found, or what it couldn't find. ")
	b.WriteString("This page is generated from `internal/witness/catalog.go` by `go generate ./internal/witness`; ")
	b.WriteString("never edit it by hand. A pass agrees with what the tool reported, a violation contradicts it, ")
	b.WriteString("a gap is evidence Deedglass needed and couldn't get, and a note is context that never changes a verdict. ")
	b.WriteString("The messages below are shown with sample values.\n\n")
	b.WriteString("| Code | Class | Level | Message | What to do next |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, e := range Catalog() {
		msg := render(e.Template, e.Sample)
		b.WriteString("| `" + string(e.Code) + "` | " + string(e.Class) + " | " + e.Levels + " | `" + mdCell(msg) + "` | " + mdCell(e.Next) + " |\n")
	}
	b.WriteString("\n## Reserved\n\n")
	b.WriteString("These codes were set aside before L2 chose its names. They stay unused, so they never mean something else.\n\n")
	for _, c := range Reserved {
		b.WriteString("- `" + string(c) + "`\n")
	}
	return b.String()
}

func mdCell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
