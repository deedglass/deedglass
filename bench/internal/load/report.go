package load

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Headline is one row of the report: a contract §12 target and the suite file that measures it.
type Headline struct {
	Suite    string // results suite name
	Scenario string // the scenario holding the targets
	Label    string // "Gate · allow, no witness"
	Show     func(s Scenario) string
}

// Headlines are the six §12 rows bench-check asserts, in report order.
var Headlines = []Headline{
	{"gate", "actions.allow", "Gate · allow, no witness", func(s Scenario) string {
		return fmt.Sprintf("p50 %s ms · p99 %s ms", num(med(s, "p50_ms")), num(med(s, "p99_ms")))
	}},
	{"policy", "evaluate.50_rules", "Policy · 50 rules", func(s Scenario) string {
		return fmt.Sprintf("p99 %s µs", num(med(s, "p99_ms")*1000))
	}},
	{"witness", "l2.100_per_s", "Witness · L2 at 100 writes/s", func(s Scenario) string {
		return fmt.Sprintf("p50 %s ms · p99 %s ms", num(med(s, "p50_ms")), num(med(s, "p99_ms")))
	}},
	{"verify", "verify.100k_jsonl", "Verify · 100,000 deeds", func(s Scenario) string {
		return fmt.Sprintf("%s s · %s MiB", num(med(s, "wall_s")), num(med(s, "peak_rss_mib")))
	}},
	{"stop", "stop.across_instances", "Stop · across 2 instances", func(s Scenario) string {
		return fmt.Sprintf("max %s ms over %d trials", num(med(s, "max_ms")), int(med(s, "trials")))
	}},
	{"quickstart", "quickstart.clean_dir", "Quickstart · clean machine", func(s Scenario) string {
		w := med(s, "wall_s")
		return fmt.Sprintf("%d min %d s", int(w)/60, int(w)%60)
	}},
}

func med(s Scenario, metric string) float64 {
	var vs []float64
	for _, r := range s.Runs {
		if v, ok := MetricOf(r, metric); ok {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return 0
	}
	sort.Float64s(vs)
	return vs[len(vs)/2]
}

// num prints a number calmly: three significant figures, thousands separated.
func num(v float64) string {
	switch {
	case v >= 100:
		return thousands(strconv.FormatFloat(v, 'f', 0, 64))
	case v >= 10:
		return strconv.FormatFloat(v, 'f', 1, 64)
	default:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
}

func thousands(s string) string {
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func targetLabel(ts []Goal) string {
	var parts []string
	for _, t := range ts {
		parts = append(parts, t.Label)
	}
	return strings.Join(parts, " · ")
}

// Row is one checked headline.
type Row struct {
	Suite    string `json:"suite"`
	Label    string `json:"label"`
	Measured string `json:"measured"`
	Target   string `json:"target"`
	Targets  []Goal `json:"targets"`
	Result   string `json:"result"` // "met", "missed", "refused", "missing"
	Problem  string `json:"problem,omitempty"`
	File     string `json:"file,omitempty"`
	Quick    bool   `json:"quick,omitempty"`
}

// Summary is bench/results/SUMMARY.json, the one file the docs and the site read.
type Summary struct {
	Schema   string            `json:"schema"`
	Version  string            `json:"version"`
	Host     Host              `json:"host"`
	Go       string            `json:"go"`
	Postgres *Postgres         `json:"postgres,omitempty"`
	Rows     []Row             `json:"rows"`
	Met      int               `json:"met"`
	Missed   int               `json:"missed"`
	Files    []string          `json:"files"`
	Reported []ReportedSummary `json:"reported"`
}

// ReportedSummary is a scenario outside the six headlines (deny, approval, MCP, deed, WAL, soak, …).
type ReportedSummary struct {
	Suite    string `json:"suite"`
	Scenario string `json:"scenario"`
	Measured string `json:"measured"`
	Result   string `json:"result"`
	File     string `json:"file"`
}

// Options for Check.
type Options struct {
	Root       string
	AllowQuick bool // the RC accepts …-quick.json files (never for docs)
}

// latest returns, per suite, the newest results file in dir (by started_at, then name).
func latest(dir string, allowQuick bool) (map[string]*File, map[string]string, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, nil, err
	}
	files, names := map[string]*File{}, map[string]string{}
	var problems []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "SUMMARY.json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || !bytes.Contains(b, []byte(`"schema": "`+Schema+`"`)) && !bytes.Contains(b, []byte(`"schema":"`+Schema+`"`)) {
			continue // an older session's raw file; not in this schema
		}
		f, err := Read(filepath.Join(dir, e.Name()))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if f.Quick && !allowQuick {
			continue
		}
		cur, ok := files[f.Suite]
		if !ok || f.StartedAt > cur.StartedAt || (f.StartedAt == cur.StartedAt && e.Name() > names[f.Suite]) {
			files[f.Suite], names[f.Suite] = f, e.Name()
		}
	}
	return files, names, problems, nil
}

// Check judges the six headlines against the newest results. It refuses battery and unknown-power
// runs and any file whose source_hash differs from the working tree's.
func Check(o Options) (*Summary, []string, error) {
	dir := filepath.Join(o.Root, "bench", "results")
	files, names, problems, err := latest(dir, o.AllowQuick)
	if err != nil {
		return nil, nil, err
	}
	current, err := SourceHash(o.Root)
	if err != nil {
		return nil, nil, err
	}
	sum := &Summary{Schema: "deedglass.bench.summary/v1", Rows: []Row{}, Files: []string{}, Reported: []ReportedSummary{}}
	for _, h := range Headlines {
		row := Row{Suite: h.Suite, Label: h.Label}
		f := files[h.Suite]
		if f == nil {
			row.Result, row.Problem = "missing", "No results file for this suite. Run make bench."
			sum.Missed++
			sum.Rows = append(sum.Rows, row)
			continue
		}
		row.File, row.Quick = names[h.Suite], f.Quick
		var sc *Scenario
		for i := range f.Scenarios {
			if f.Scenarios[i].Name == h.Scenario {
				sc = &f.Scenarios[i]
			}
		}
		switch {
		case sc == nil:
			row.Result, row.Problem = "missing", "The file has no "+h.Scenario+" scenario."
		case f.Host.Power != "ac":
			row.Result, row.Problem = "refused", "Measured on "+f.Host.Power+" power. Plug in and run it again."
		case f.SourceHash != current: //deedglass:allow-compare a public digest of source files, not a secret
			row.Result, row.Problem = "refused", "Stale: the code it measured has changed since. Run it again."
		}
		if sc != nil {
			row.Measured, row.Targets, row.Target = h.Show(*sc), sc.Targets, targetLabel(sc.Targets)
			if row.Result == "" {
				if sc.Met() && len(sc.Targets) > 0 {
					row.Result = "met"
				} else {
					row.Result = "missed"
				}
			}
		}
		if row.Result == "met" {
			sum.Met++
		} else {
			sum.Missed++
		}
		sum.Rows = append(sum.Rows, row)
	}
	suites := make([]string, 0, len(files))
	for s := range files {
		suites = append(suites, s)
	}
	sort.Strings(suites)
	for _, s := range suites {
		f := files[s]
		sum.Files = append(sum.Files, names[s])
		if sum.Version == "" || f.Suite == "gate" {
			sum.Version, sum.Host, sum.Go = f.Version, f.Host, f.Go
		}
		if f.Postgres != nil && (sum.Postgres == nil || f.Suite == "gate") {
			sum.Postgres = f.Postgres
		}
		for _, sc := range f.Scenarios {
			if !isHeadline(s, sc.Name) {
				sum.Reported = append(sum.Reported, ReportedSummary{Suite: s, Scenario: sc.Name, Measured: describe(sc), Result: sc.Result, File: names[s]})
			}
		}
	}
	return sum, problems, nil
}

func isHeadline(suite, scenario string) bool {
	for _, h := range Headlines {
		if h.Suite == suite && h.Scenario == scenario {
			return true
		}
	}
	return false
}

// describe prints a reported scenario's medians.
func describe(s Scenario) string {
	if len(s.Runs) == 0 {
		return ""
	}
	if len(s.Runs[0].Values) > 0 {
		keys := make([]string, 0, len(s.Runs[0].Values))
		for k := range s.Runs[0].Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			if _, ok := MetricOf(s.Runs[0], k); ok {
				parts = append(parts, k+" "+num(med(s, k)))
			}
		}
		return strings.Join(parts, " · ")
	}
	return fmt.Sprintf("p50 %s ms · p99 %s ms", num(med(s, "p50_ms")), num(med(s, "p99_ms")))
}

// Header is the report's first line.
func (s *Summary) Header() string {
	if s.Host.CPU == "" {
		return "Deedglass benchmarks · no results yet"
	}
	pg := ""
	if s.Postgres != nil {
		pg = " · PostgreSQL " + s.Postgres.Version + " (" + s.Postgres.Mode + ")"
	}
	power := "on AC power"
	if s.Host.Power != "ac" {
		power = "on " + s.Host.Power + " power"
	}
	return fmt.Sprintf("Deedglass benchmarks · %s · %s, %d cores, %d GB%s · %s", s.Version, s.Host.CPU, s.Host.Cores, s.Host.MemGB, pg, power)
}

// Text renders the terminal report (symbols from the brand doc §13.3; color is the caller's).
func (s *Summary) Text() string {
	var b strings.Builder
	b.WriteString(s.Header() + "\n")
	for _, r := range s.Rows {
		sym := "✓"
		if r.Result != "met" {
			sym = "✕"
		}
		target := "target " + r.Target
		if r.Target == "" {
			target = ""
		}
		fmt.Fprintf(&b, "%s %-30s %-30s %-28s %s\n", sym, r.Label, r.Measured, target, r.File)
		if r.Problem != "" {
			fmt.Fprintf(&b, "  next  %s\n", r.Problem)
		} else if r.Result == "missed" {
			if r.Suite == "gate" {
				b.WriteString("  next  Profile it with make bench-gate PROFILE=1 (a CPU profile of the in-process Gate); explain the cause before the target is discussed.\n")
			} else {
				fmt.Fprintf(&b, "  next  Profile the %s suite's test with go test -cpuprofile; explain the cause before the target is discussed.\n", r.Suite)
			}
		}
	}
	fmt.Fprintf(&b, "%d targets met · %d missed · raw files in bench/results/\n", s.Met, s.Missed)
	return b.String()
}

// Markdown renders the block docs/BENCHMARKS.md carries between the markers.
func (s *Summary) Markdown() string {
	var b strings.Builder
	b.WriteString("<!-- Generated by make bench-report from bench/results/SUMMARY.json. Don't edit by hand. -->\n\n")
	b.WriteString("**" + strings.TrimPrefix(s.Header(), "Deedglass benchmarks · ") + "**\n\n")
	b.WriteString("| Target (contract §12) | Measured (median of runs) | Target | Result | Raw file |\n|---|---|---|---|---|\n")
	for _, r := range s.Rows {
		res := map[string]string{"met": "✓ met", "missed": "✕ missed", "refused": "✕ refused", "missing": "✕ missing"}[r.Result]
		if r.Problem != "" {
			res += ": " + r.Problem
		}
		file := "none"
		if r.File != "" {
			file = "`bench/results/" + r.File + "`"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", r.Label, orDash(r.Measured), orDash(r.Target), res, file)
	}
	fmt.Fprintf(&b, "\n%d targets met · %d missed.\n", s.Met, s.Missed)
	if len(s.Reported) > 0 {
		b.WriteString("\n| Reported, no target | Measured (median of runs) | Raw file |\n|---|---|---|\n")
		for _, r := range s.Reported {
			fmt.Fprintf(&b, "| %s · %s | %s | `bench/results/%s` |\n", r.Suite, r.Scenario, r.Measured, r.File)
		}
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "not measured"
	}
	return s
}

// Markers delimit the generated block in docs/BENCHMARKS.md.
const (
	BeginMarker = "<!-- bench:begin -->"
	EndMarker   = "<!-- bench:end -->"
)

// Splice replaces the text between the markers.
func Splice(doc, block string) (string, error) {
	i, j := strings.Index(doc, BeginMarker), strings.Index(doc, EndMarker)
	if i < 0 || j < i {
		return "", errors.New("docs/BENCHMARKS.md has no " + BeginMarker + " … " + EndMarker + " block")
	}
	return doc[:i+len(BeginMarker)] + "\n" + block + doc[j:], nil
}

// WriteSummary writes SUMMARY.json and splices the docs block. It is deterministic: the same
// results give the same bytes.
func WriteSummary(root string, s *Summary) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "bench", "results", "SUMMARY.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	docPath := filepath.Join(root, "docs", "BENCHMARKS.md")
	doc, err := os.ReadFile(docPath)
	if err != nil {
		return err
	}
	out, err := Splice(string(doc), s.Markdown())
	if err != nil {
		return err
	}
	return os.WriteFile(docPath, []byte(out), 0o644)
}

// DocFresh reports whether doc's generated block equals SUMMARY.json rendered (a hand edit fails).
func DocFresh(root, doc string) error {
	s, err := ReadSummary(root)
	if err != nil {
		return err
	}
	want, err := Splice(doc, s.Markdown())
	if err != nil {
		return err
	}
	if want != doc {
		return errors.New("docs/BENCHMARKS.md's generated block differs from bench/results/SUMMARY.json; run make bench-report and never edit the block by hand")
	}
	return nil
}

// ReadSummary reads SUMMARY.json.
func ReadSummary(root string) (*Summary, error) {
	b, err := os.ReadFile(filepath.Join(root, "bench", "results", "SUMMARY.json"))
	if err != nil {
		return nil, err
	}
	var s Summary
	return &s, json.Unmarshal(b, &s)
}
