// Command attack-report prints the last attack run and regenerates docs/ATTACK_SUITE.md from
// attack/attack_meta.json and attack/results/latest.json (S18). The page is generated, never
// edited by hand. With -check it exits 1 when the committed page differs from what it would
// generate, so CI catches a stale page.
//
//	go run ./attack/cmd/attack-report            # print the run, rewrite the page
//	go run ./attack/cmd/attack-report -check     # print the run, fail if the page is stale
//	go run ./attack/cmd/attack-report -quiet     # rewrite the page only
//	go run ./attack/cmd/attack-report -docs-json # print the docs site's data (docs-site/src/data/attack.json)
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // Asia/Kolkata on any machine
	"unicode/utf8"

	"github.com/deedglass/deedglass/attack/harness"
)

const docPath = "docs/ATTACK_SUITE.md"

func main() {
	check := flag.Bool("check", false, "fail when docs/ATTACK_SUITE.md differs from the generated page")
	quiet := flag.Bool("quiet", false, "don't print the run")
	root := flag.String("root", ".", "the repo root")
	docsJSON := flag.Bool("docs-json", false, "print the docs site's attack data as JSON and change nothing")
	flag.Parse()
	if *docsJSON {
		os.Exit(runDocsJSON(*root, os.Stdout, os.Stderr))
	}
	os.Exit(run(*root, *check, *quiet, os.Stdout, os.Stderr))
}

// runDocsJSON prints DocsData. A missing results file is an error: the docs never show stale or
// invented numbers (S33 §3.4).
func runDocsJSON(root string, stdout, stderr io.Writer) int {
	meta, err := harness.LoadMeta(filepath.Join(root, "attack", "attack_meta.json"))
	if err != nil {
		fmt.Fprintf(stderr, "✕ Can't read the attack meta: %v\n  Next: run from the repo root.\n", err)
		return 1
	}
	res, err := harness.ReadRun(filepath.Join(root, "attack", "results", "latest.json"))
	if err != nil {
		fmt.Fprintf(stderr, "✕ No attack results yet: %v\n  Next: Run make attack first.\n", err)
		return 1
	}
	b, err := json.MarshalIndent(DocsData(meta, res), "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "✕", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s\n", b)
	return 0
}

func run(root string, check, quiet bool, stdout, stderr io.Writer) int {
	meta, err := harness.LoadMeta(filepath.Join(root, "attack", "attack_meta.json"))
	if err != nil {
		fmt.Fprintf(stderr, "✕ Can't read the attack meta: %v\n  Next: run from the repo root.\n", err)
		return 1
	}
	res, err := harness.ReadRun(filepath.Join(root, "attack", "results", "latest.json"))
	if err != nil {
		fmt.Fprintf(stderr, "✕ No attack results yet: %v\n  Next: run make attack.\n", err)
		return 1
	}
	if !quiet {
		printRun(stdout, meta, res, colorOn(stdout))
	}
	page := Generate(meta, res)
	path := filepath.Join(root, docPath)
	if check {
		cur, _ := os.ReadFile(path)
		if !bytes.Equal(cur, page) {
			fmt.Fprintf(stderr, "✕ %s is stale: it differs from what attack-report generates.\n  Next: run make attack-report and commit the page.\n", docPath)
			return 1
		}
		return 0
	}
	if err := os.WriteFile(path, page, 0o644); err != nil {
		fmt.Fprintf(stderr, "✕ Can't write %s: %v\n", docPath, err)
		return 1
	}
	if res.Summary.Differs > 0 {
		return 1
	}
	return 0
}

// ---- the terminal output (brand §13.3: symbols with words, colour only on a TTY) ----

func colorOn(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func paint(on bool, code, s string) string {
	if !on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func symbol(status string, on bool) string {
	switch status {
	case harness.StatusMatched:
		return paint(on, "38;2;52;227;201", "✓")
	case harness.StatusDiffers:
		return paint(on, "38;2;255;122;112", "✕")
	}
	return paint(on, "38;2;247;185;85", "○")
}

func pad(s string, n int) string {
	if c := utf8.RuneCountInString(s); c < n {
		return s + strings.Repeat(" ", n-c)
	}
	return s
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func printRun(w io.Writer, meta *harness.Meta, res *harness.Run, on bool) {
	declared := 0
	for _, c := range meta.Cases {
		if strings.Contains(harness.V7Groups, c.Group) {
			declared++
		}
	}
	when := res.StartedAt
	if t, err := time.Parse(time.RFC3339, res.StartedAt); err == nil {
		if ist, err := time.LoadLocation("Asia/Kolkata"); err == nil {
			when = t.In(ist).Format("2006-01-02 15:04 MST")
		}
	}
	fmt.Fprintf(w, "Deedglass attack suite · %d declared · PostgreSQL %s · %s\n", declared, pgVersion(res.Postgres), when)
	for _, r := range res.Cases {
		mc := meta.Case(r.ID)
		title := r.Title
		if mc != nil {
			title = mc.Title
		}
		switch r.Status {
		case harness.StatusWaiting:
			fmt.Fprintf(w, "%s %s  %s  waiting on %s\n", symbol(r.Status, on), r.ID, pad(clip(title, 52), 52), r.Waiting)
		case harness.StatusMatched:
			fmt.Fprintf(w, "%s %s  %s  %s  %s\n", symbol(r.Status, on), r.ID, pad(clip(title, 52), 52), pad(r.Actual, 9), r.Line)
		default:
			exp := ""
			if mc != nil {
				exp = mc.ExpectedDetail
			}
			fmt.Fprintf(w, "%s %s  %s  expected %s · got %s\n", symbol(r.Status, on), r.ID, clip(title, 40), clip(exp, 60), r.Detail)
			for _, p := range r.Problems {
				fmt.Fprintf(w, "    %s\n", p)
			}
			fmt.Fprintln(w, "    Fix the code or the fixture; never the expected outcome. A wrongly declared case goes to the founder as an ADR.")
		}
	}
	s := res.Summary
	fmt.Fprintf(w, "%d matched · %d differ · %d waiting on a later session · chain %s · %d secrets seen in %s log lines\n",
		s.Matched, s.Differs, s.Waiting, res.Invariants.Chains, res.Invariants.SecretsSeen, thousands(res.Invariants.LogLines))
}

func pgVersion(v string) string {
	if i := strings.IndexByte(v, ' '); i > 0 {
		return v[:i]
	}
	return v
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
