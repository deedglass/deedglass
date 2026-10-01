package deed_test

import (
	"os"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/deed"
)

// replaceBetween rewrites (with -update) or checks the text between two markers in a doc.
func replaceBetween(t *testing.T, path, begin, end, body, how string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	i := strings.Index(doc, begin)
	j := strings.Index(doc, end)
	if i < 0 || j < i {
		t.Fatalf("%s needs the markers %q and %q", path, begin, end)
	}
	start := i + len(begin)
	if doc[start:j] == body {
		return
	}
	if !*update {
		t.Fatalf("%s drifted from the code. Run: %s", path, how)
	}
	if err := os.WriteFile(path, []byte(doc[:start]+body+doc[j:]), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestVerifyingDocs keeps the failure table in docs/VERIFYING_DEEDS.md equal to messages.go.
func TestVerifyingDocs(t *testing.T) {
	var b strings.Builder
	b.WriteString("\n| Code | You see | Next step | Exit |\n|---|---|---|---|\n")
	esc := func(s string) string { return strings.ReplaceAll(s, "|", `\|`) }
	for _, c := range deed.Codes {
		m := deed.Messages[string(c)]
		exit := "1"
		if deed.CannotDetermine(string(c)) {
			exit = "2"
		}
		b.WriteString("| `" + string(c) + "` | " + esc(m.Sentence) + " | " + esc(m.Next) + " | " + exit + " |\n")
	}
	b.WriteString("\n`{Deed}` reads \"Deed 812\", or \"The deed\" when the deed is too damaged to read its seq. A missing\n" +
		"first deed reads: \"" + deed.Messages["chain_seq_gap.start"].Sentence + "\"\n")
	replaceBetween(t, "../../docs/VERIFYING_DEEDS.md",
		"<!-- codes:begin (generated from internal/deed/messages.go; go test ./internal/deed -run TestVerifyingDocs -update) -->\n",
		"<!-- codes:end -->", b.String(), "go test ./internal/deed -run TestVerifyingDocs -update")
}
