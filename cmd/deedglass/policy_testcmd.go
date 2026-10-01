package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/cmd/deedglass/internal/ui"
	"github.com/deedglass/deedglass/internal/policy"
)

// `deedglass policy test` (S12 §3.8): run a policy's cases like unit tests, with rule coverage.
//
//	deedglass policy test examples/policies/postgres-safe.yaml       pairs postgres-safe.cases.yaml
//	deedglass policy test examples/policies/postgres-safe.cases.yaml  reads its policy: key
//	deedglass policy test --dir examples/policies                     every policy, each with cases
//
// Exit 0 when every case passes (and, with --require-coverage, every rule decides a case; with
// --voice, no reason_style warning), 1 otherwise, 2 on a usage error.

type policyTestOpts struct {
	cases, dir, run    string
	json, cover, voice bool
	re                 *regexp.Regexp
}

// policyTestRun is one policy and its cases.
type policyTestRun struct {
	Display   string              `json:"-"`
	Policy    string              `json:"policy"`
	File      string              `json:"file"`
	CasesFile string              `json:"cases_file"`
	Result    *policy.TestResult  `json:"result"`
	Lint      *policy.LintResult  `json:"lint,omitempty"`     // when the policy didn't compile or --voice found problems
	Problems  []policy.Diagnostic `json:"problems,omitempty"` // cases-file problems
	Missing   bool                `json:"missing_cases,omitempty"`
	OK        bool                `json:"ok"`
	compiled  *policy.Compiled
	extra     map[string]any // the sources, for excerpts
}

func runPolicyTest(ctx context.Context, e *Env, args []string) error {
	var o policyTestOpts
	paths, err := parseInterleaved("deedglass policy test", args, func(fs *flag.FlagSet) {
		fs.StringVar(&o.cases, "cases", o.cases, "the cases file (default: <name>.cases.yaml next to the policy)")
		fs.StringVar(&o.dir, "dir", o.dir, "test every policy in a directory")
		fs.StringVar(&o.run, "run", o.run, "only cases whose name matches this regular expression")
		fs.BoolVar(&o.json, "json", o.json, "print JSON")
		fs.BoolVar(&o.cover, "require-coverage", o.cover, "fail when a rule decides no case")
		fs.BoolVar(&o.voice, "voice", o.voice, "also fail on reason_style warnings")
	})
	if err != nil {
		return err
	}
	jsonOut := o.json || e.Globals.JSON
	if o.run != "" {
		if o.re, err = regexp.Compile(o.run); err != nil {
			return usageErr(fmt.Errorf("--run %q isn't a regular expression", clean(o.run)), "For example: --run 'DROP|TRUNCATE'")
		}
	}
	var pairs [][2]string // policy file, cases file ("" to find it)
	switch {
	case o.dir != "" && (len(paths) > 0 || o.cases != ""):
		return usageErr(errors.New("--dir tests a whole directory; leave out file names and --cases"), "deedglass policy test --dir examples/policies")
	case o.dir != "":
		fi, err := os.Stat(o.dir)
		if err != nil || !fi.IsDir() {
			return usageErr(fmt.Errorf("can't read the directory %s", o.dir), "Check the path and try again.")
		}
		ents, err := os.ReadDir(o.dir)
		if err != nil {
			return usageErr(fmt.Errorf("can't read the directory %s: %s", o.dir, plainErr(err)), "Check the path and try again.")
		}
		for _, ent := range ents {
			n := ent.Name()
			if ent.IsDir() || strings.HasSuffix(n, ".cases.yaml") || strings.HasSuffix(n, ".cases.yml") ||
				!(strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml")) {
				continue
			}
			pairs = append(pairs, [2]string{filepath.Join(o.dir, n), ""})
		}
		if len(pairs) == 0 {
			return usageErr(fmt.Errorf("%s has no policy files", o.dir), "Policies are *.yaml files next to their *.cases.yaml.")
		}
	case len(paths) == 0:
		return usageErr(errors.New("deedglass policy test needs a policy file or --dir"),
			"For example: deedglass policy test examples/policies/postgres-safe.yaml")
	case len(paths) > 1 && o.cases != "":
		return usageErr(errors.New("--cases goes with one policy file"), "Test one policy at a time with --cases, or use --dir.")
	default:
		for _, p := range paths {
			if isCasesFile(p) {
				pairs = append(pairs, [2]string{"", p})
				continue
			}
			pairs = append(pairs, [2]string{p, o.cases})
		}
	}
	start := time.Now()
	var runs []*policyTestRun
	for _, pr := range pairs {
		r, err := testOnePolicy(ctx, e, pr[0], pr[1], &o)
		if err != nil {
			return err
		}
		runs = append(runs, r)
	}
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].Policy < runs[j].Policy })
	elapsed := time.Since(start)
	allOK := true
	for _, r := range runs {
		allOK = allOK && r.OK
	}
	if jsonOut {
		total, passed, failed := 0, 0, 0
		for _, r := range runs {
			if r.Result != nil {
				passed += r.Result.Passed
				failed += r.Result.Failed
				total += r.Result.Passed + r.Result.Failed
			}
		}
		out := map[string]any{"object": "policy_test_run", "ok": allOK, "policies": runs, "cases": total, "passed": passed, "failed": failed,
			"elapsed_ms": elapsed.Milliseconds()}
		if err := writeJSON(e, out); err != nil {
			return err
		}
	} else {
		printTestRuns(e, runs, elapsed, &o)
	}
	if !allOK {
		return &ExitError{Code: ExitFailure, ErrCode: "policy_test_failed", Err: errors.New("policy tests failed"), printed: true}
	}
	return nil
}

func isCasesFile(p string) bool {
	return strings.HasSuffix(p, ".cases.yaml") || strings.HasSuffix(p, ".cases.yml")
}

// casesFileFor is <dir>/<name>.cases.yaml for <dir>/<name>.yaml.
func casesFileFor(policyFile string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(policyFile, ".yaml"), ".yml")
	for _, ext := range []string{".cases.yaml", ".cases.yml"} {
		if _, err := os.Stat(base + ext); err == nil {
			return base + ext
		}
	}
	return base + ".cases.yaml"
}

func testOnePolicy(ctx context.Context, e *Env, policyFile, casesFile string, o *policyTestOpts) (*policyTestRun, error) {
	var cf *policy.CasesFile
	var probs []policy.Diagnostic
	var casesSrc []byte
	if policyFile == "" { // a cases file given directly: its policy: key names the policy
		b, err := readPolicy(e, policyInput{path: casesFile, display: casesFile})
		if err != nil {
			return nil, err
		}
		casesSrc = b
		cf, probs = policy.ParseCasesYAML(b, casesFile)
		if cf == nil {
			return &policyTestRun{Display: casesFile, Policy: strings.TrimSuffix(filepath.Base(casesFile), ".cases.yaml"), CasesFile: casesFile,
				Problems: probs, extra: map[string]any{"src": casesSrc}}, nil
		}
		if cf.Policy == "" {
			return nil, usageErr(fmt.Errorf("%s has no policy: key, so Deedglass doesn't know which policy to test", casesFile),
				"Add policy: <file>.yaml at the top of the cases file, or pass the policy file instead.")
		}
		policyFile = filepath.Join(filepath.Dir(casesFile), cf.Policy)
	}
	run := &policyTestRun{Display: policyFile, File: policyFile, Policy: strings.TrimSuffix(strings.TrimSuffix(filepath.Base(policyFile), ".yaml"), ".yml")}
	src, err := readPolicy(e, policyInput{path: policyFile, display: policyFile})
	if err != nil {
		return nil, err
	}
	lr := policy.Lint(src, policy.Options{File: policyFile, Voice: o.voice})
	c := lr.Compiled()
	if c == nil {
		run.Lint = lr
		run.extra = map[string]any{"policy_src": src}
		return run, nil
	}
	run.Policy, run.compiled = c.Name, c
	if o.voice {
		for _, w := range lr.Warnings {
			if w.Code == "reason_style" {
				run.Lint = lr
				run.extra = map[string]any{"policy_src": src}
				break
			}
		}
	}
	if cf == nil {
		if casesFile == "" {
			casesFile = casesFileFor(policyFile)
		}
		run.CasesFile = casesFile
		b, err := os.ReadFile(casesFile)
		if errors.Is(err, os.ErrNotExist) {
			run.Missing = true
			return run, nil
		}
		if err != nil {
			return nil, usageErr(fmt.Errorf("can't read %s: %s", casesFile, plainErr(err)), "Check the path and try again.")
		}
		casesSrc = b
		cf, probs = policy.ParseCasesYAML(b, casesFile)
	} else {
		run.CasesFile = casesFile
	}
	if cf == nil {
		run.Problems = probs
		if run.extra == nil {
			run.extra = map[string]any{}
		}
		run.extra["src"] = casesSrc
		return run, nil
	}
	if cf.Policy != "" && filepath.Base(filepath.Join(filepath.Dir(run.CasesFile), cf.Policy)) != filepath.Base(policyFile) {
		return nil, usageErr(fmt.Errorf("%s is for %s, not %s", run.CasesFile, cf.Policy, filepath.Base(policyFile)),
			"Pass the matching policy, or fix the policy: key in the cases file.")
	}
	res := policy.RunCases(ctx, c, cf, policy.TestOptions{Run: o.re})
	res.File = policyFile
	run.Result = res
	run.OK = res.OK && run.Lint == nil && (!o.cover || len(res.Coverage.Uncovered) == 0)
	return run, nil
}

// printTestRuns renders the §5 layout: failures first (case by case), then one line per policy and
// a summary line.
func printTestRuns(e *Env, runs []*policyTestRun, elapsed time.Duration, o *policyTestOpts) {
	s := e.Style
	w := e.Stdout
	width := 0
	for _, r := range runs {
		width = max(width, ui.Width(r.Policy))
	}
	// Problems first: lint results, cases-file problems, failed cases.
	for _, r := range runs {
		switch {
		case r.Lint != nil && !r.Lint.Valid:
			var buf bytes.Buffer
			policy.RenderText(&buf, r.Lint, r.extra["policy_src"].([]byte), r.Display, s)
			_, _ = w.Write(buf.Bytes())
			fmt.Fprintln(w)
		case r.Lint != nil:
			var buf bytes.Buffer
			var voice []policy.Diagnostic
			for _, d := range r.Lint.Warnings {
				if d.Code == "reason_style" {
					voice = append(voice, d)
				}
			}
			policy.RenderDiagnostics(&buf, voice, r.extra["policy_src"].([]byte), r.Display, s)
			_, _ = w.Write(buf.Bytes())
		}
		if len(r.Problems) > 0 {
			var buf bytes.Buffer
			src, _ := r.extra["src"].([]byte)
			policy.RenderDiagnostics(&buf, r.Problems, src, r.CasesFile, s)
			_, _ = w.Write(buf.Bytes())
		}
		if r.Result == nil {
			continue
		}
		for _, c := range r.Result.Cases {
			if c.Pass {
				continue
			}
			loc := filepath.Base(r.CasesFile)
			if c.Line > 0 {
				loc = fmt.Sprintf("%s:%d", loc, c.Line)
			}
			fmt.Fprintln(w, s.FailLine(fmt.Sprintf("%s %s %s", c.Name, ui.Sep, loc)))
			exp := string(c.Expected.Decision)
			if c.Expected.Rule != nil {
				exp += " " + ui.Sep + " " + *c.Expected.Rule
			}
			fmt.Fprintf(w, "    %s  %s\n", s.Dim("expected"), exp)
			fmt.Fprintf(w, "    %s       %s %s %s\n", s.Dim("got"), c.Got.Decision, ui.Sep, c.Got.Rule)
			for _, p := range c.Problems {
				if strings.HasPrefix(p, "decision:") || strings.HasPrefix(p, "rule:") {
					continue
				}
				fmt.Fprintf(w, "    %s\n", s.Dim(p))
			}
		}
	}
	total, passed, failed := 0, 0, 0
	var uncovered []string
	for _, r := range runs {
		name := ui.Pad(r.Policy, width)
		switch {
		case r.Lint != nil && !r.Lint.Valid:
			fmt.Fprintln(w, s.FailLine(fmt.Sprintf("%s   the policy was not accepted", name)))
			continue
		case r.Missing:
			fmt.Fprintln(w, s.FailLine(fmt.Sprintf("%s   no cases file (%s)", name, filepath.Base(r.CasesFile))))
			continue
		case r.Result == nil:
			fmt.Fprintln(w, s.FailLine(fmt.Sprintf("%s   the cases file was not accepted", name)))
			continue
		}
		res := r.Result
		n := res.Passed + res.Failed
		total += n
		passed += res.Passed
		failed += res.Failed
		for _, u := range res.Coverage.Uncovered {
			uncovered = append(uncovered, r.Policy+"/"+u)
		}
		parts := []string{pluralN(n, "case", "cases")}
		if res.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d failed", res.Failed))
		}
		parts = append(parts, coverageWords(res, r.compiled))
		line := name + "   " + strings.Join(parts, " "+ui.Sep+" ")
		if r.OK {
			fmt.Fprintln(w, s.OKLine(line))
		} else {
			fmt.Fprintln(w, s.FailLine(line))
		}
	}
	allOK := true
	for _, r := range runs {
		allOK = allOK && r.OK
	}
	secs := fmt.Sprintf("%.1f s", elapsed.Seconds())
	skipped := 0
	for _, r := range runs {
		if r.Result != nil {
			skipped += r.Result.Skipped
		}
	}
	if total == 0 && !allOK {
		fmt.Fprintln(w, s.FailLine("no cases ran "+ui.Sep+" fix the problems above, then run it again"))
		return
	}
	if allOK {
		cov := "every rule covered"
		switch {
		case o.re != nil && !o.cover:
			cov = fmt.Sprintf("%d skipped by --run", skipped)
		case len(uncovered) > 0:
			cov = "uncovered: " + strings.Join(uncoveredNames(uncovered, len(runs) > 1), ", ")
		}
		fmt.Fprintln(w, s.OKLine(fmt.Sprintf("%s passed %s %s %s %s %s %s", pluralN(total, "case", "cases"), ui.Sep,
			pluralN(len(runs), "policy", "policies"), ui.Sep, cov, ui.Sep, secs)))
		return
	}
	parts := []string{fmt.Sprintf("%d passed", passed), fmt.Sprintf("%d failed", failed)}
	if len(runs) > 1 {
		parts = append(parts, pluralN(len(runs), "policy", "policies"))
	}
	if len(uncovered) > 0 {
		parts = append(parts, "uncovered: "+strings.Join(uncoveredNames(uncovered, len(runs) > 1), ", "))
	}
	if o.voice {
		for _, r := range runs {
			if r.Lint != nil && r.Lint.Valid {
				parts = append(parts, "reason_style warnings in "+r.Policy)
			}
		}
	}
	fmt.Fprintln(w, s.FailLine(strings.Join(parts, " "+ui.Sep+" ")))
}

// uncoveredNames drops the policy prefix when only one policy ran.
func uncoveredNames(list []string, many bool) []string {
	if many {
		return list
	}
	out := make([]string, len(list))
	for i, u := range list {
		_, out[i], _ = strings.Cut(u, "/")
	}
	return out
}

// coverageWords is "12 of 12 rules", or for a policy without rules "default and 1 budget covered".
func coverageWords(r *policy.TestResult, c *policy.Compiled) string {
	if r.Coverage.Total > 0 {
		return fmt.Sprintf("%d of %d rules", r.Coverage.Covered, r.Coverage.Total)
	}
	var parts []string
	if r.Coverage.Default > 0 {
		parts = append(parts, "default")
	}
	if c != nil && len(c.Budgets) > 0 {
		parts = append(parts, pluralN(r.BudgetsCovered(c), "budget", "budgets"))
	}
	if len(parts) == 0 {
		return "nothing covered"
	}
	return strings.Join(parts, " and ") + " covered"
}
