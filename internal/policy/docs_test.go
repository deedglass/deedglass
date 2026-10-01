package policy

import (
	"os"
	"strings"
	"testing"
)

// TestErrorDocExamples re-lints every example in docs/POLICY_ERRORS.md: in each section, the first
// ```yaml block followed by a ```text block that starts with ✕ or ○ must produce exactly the
// printed lines (header, message, hint) for every diagnostic the block shows.
func TestErrorDocExamples(t *testing.T) {
	b, err := os.ReadFile("../../docs/POLICY_ERRORS.md")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, sec := range strings.Split(string(b), "\n## ")[1:] {
		code := strings.SplitN(sec, "\n", 2)[0]
		blocks := fences(sec)
		for i := 0; i+1 < len(blocks); i++ {
			if blocks[i].lang != "yaml" || blocks[i+1].lang != "text" {
				continue
			}
			want := blocks[i+1].body
			if !strings.HasPrefix(want, "✕ ") && !strings.HasPrefix(want, "○ ") {
				continue
			}
			got := compact(Lint([]byte(blocks[i].body), Options{File: "policy.yaml", Voice: code == "reason_style"}))
			for _, diag := range strings.Split(strings.TrimSpace(want), "\n\n") {
				if !strings.Contains(got, strings.TrimSpace(diag)+"\n") {
					t.Errorf("## %s: the example prints\n%s\nbut the compiler prints\n%s", code, diag, got)
				}
			}
			checked++
			break
		}
	}
	if checked < 40 {
		t.Errorf("only %d examples checked; the doc should have one per code where a file can show it", checked)
	}
}

type fence struct{ lang, body string }

func fences(s string) []fence {
	var out []fence
	lines := strings.Split(s, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "```") || len(lines[i]) == 3 {
			continue
		}
		f := fence{lang: strings.TrimPrefix(lines[i], "```")}
		var body []string
		for i++; i < len(lines) && lines[i] != "```"; i++ {
			body = append(body, lines[i])
		}
		f.body = strings.Join(body, "\n") + "\n"
		out = append(out, f)
	}
	return out
}

// compact renders each diagnostic as its header, message and hint, without the excerpt.
func compact(r *LintResult) string {
	var b strings.Builder
	for _, d := range ordered(r) {
		sym := "✕"
		if d.Severity == SevWarning {
			sym = "○"
		}
		loc := "policy.yaml"
		if d.Line > 0 {
			loc = "policy.yaml:" + itoa(d.Line) + ":" + itoa(d.Col)
		}
		b.WriteString(sym + " " + loc + " · " + d.Code + "\n  " + PlainText(d.Message) + "\n")
		if d.Hint != "" {
			b.WriteString("  hint: " + PlainText(d.Hint) + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}
