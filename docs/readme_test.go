package docs_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestReadme (S37): the README's quickstart is the tested docs page's commands, in order; its
// numbers equal bench/results/SUMMARY.json; its attack count equals attack/results/latest.json.
func TestReadme(t *testing.T) {
	readme := read(t, "../README.md")
	block := between(t, readme, "<!-- quickstart:begin", "<!-- quickstart:end -->")
	var cmds []string
	for _, l := range strings.Split(block, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "```") && !strings.HasPrefix(l, "(") && !strings.Contains(l, "quickstart:begin") {
			cmds = append(cmds, l)
		}
	}
	page := read(t, "../docs-site/src/content/docs/quickstart.mdx")
	var docs []string
	for _, l := range strings.Split(page, "\n") {
		if strings.HasPrefix(l, "$ ") {
			docs = append(docs, strings.TrimPrefix(l, "$ "))
		}
	}
	i := 0
	for _, c := range cmds {
		for i < len(docs) && docs[i] != c {
			i++
		}
		if i == len(docs) {
			t.Fatalf("README quickstart line %q isn't a command of quickstart.mdx (in this order): %q", c, docs)
		}
	}
	if len(cmds) != 4 {
		t.Fatalf("the README quickstart has %d commands, want 4", len(cmds))
	}
	var sum struct {
		Rows []struct{ Label, Measured string } `json:"rows"`
	}
	if err := json.Unmarshal([]byte(read(t, "../bench/results/SUMMARY.json")), &sum); err != nil {
		t.Fatal(err)
	}
	nums := between(t, readme, "<!-- numbers:begin", "<!-- numbers:end -->")
	for _, l := range strings.Split(nums, "\n") {
		cells := strings.Split(strings.Trim(strings.TrimSpace(l), "|"), "|")
		if len(cells) < 3 || strings.TrimSpace(cells[0]) == "What" || strings.HasPrefix(strings.TrimSpace(cells[0]), "---") {
			continue
		}
		label, measured := strings.TrimSpace(cells[0]), strings.TrimSpace(cells[1])
		found := false
		for _, r := range sum.Rows {
			if r.Label == label {
				found = true
				if r.Measured != measured {
					t.Errorf("%s: README %q, SUMMARY.json %q", label, measured, r.Measured)
				}
			}
		}
		if !found {
			t.Errorf("README row %q isn't in SUMMARY.json", label)
		}
	}
	var att struct {
		Summary struct{ Matched, Differs int } `json:"summary"`
	}
	if err := json.Unmarshal([]byte(read(t, "../attack/results/latest.json")), &att); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`runs (\d+) cases[\s\S]*?(\d+) matched, (\d+) differ`).FindStringSubmatch(readme)
	if m == nil || m[2] != itoa(att.Summary.Matched) || m[3] != itoa(att.Summary.Differs) || m[1] != itoa(att.Summary.Matched+att.Summary.Differs) {
		t.Fatalf("README attack line %v vs latest.json %+v", m, att.Summary)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func between(t *testing.T, s, a, b string) string {
	t.Helper()
	i := strings.Index(s, a)
	j := strings.Index(s, b)
	if i < 0 || j < i {
		t.Fatalf("markers %q … %q missing", a, b)
	}
	return s[i:j]
}

func itoa(n int) string { return strconv.Itoa(n) }
