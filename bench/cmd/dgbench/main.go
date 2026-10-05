// Command dgbench runs Deedglass's benchmark suites and reports them (S35).
//
//	dgbench check [--quick]        assert the six contract §12 targets on the newest results (make bench-check)
//	dgbench report                 print the table, write bench/results/SUMMARY.json and the docs block (make bench-report)
//	dgbench <suite> [--quick]      run one suite: policy, verify, quickstart (no Postgres); gate, stop, deed,
//	                               witness, wal, mcp, soak (need the throwaway cluster: build with -tags integration)
//
// Every suite writes bench/results/<date>-<host>-<suite>.json in the deedglass.bench/v1 shape. Numbers
// are never typed into docs: bench-report renders them from these files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/bench/internal/load"
	"github.com/deedglass/deedglass/internal/buildinfo"
)

// Opts are the flags every suite takes.
type Opts struct {
	Root  string
	Quick bool // 15 s per scenario, one run; …-quick.json (accepted by the RC's bench-check, never by docs)
}

// suites is filled by the files that implement them; the Postgres ones only in integration builds.
var suites = map[string]func(ctx context.Context, o Opts) (*load.File, error){}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("run dgbench inside the repository")
		}
		dir = parent
	}
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Println("Usage: dgbench check [--quick] | report | <suite> [--quick]")
		fmt.Println("Suites:", strings.Join(suiteNames(), ", "))
		return 0
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "✕ "+err.Error())
		return 2
	}
	fs := flag.NewFlagSet("dgbench "+args[0], flag.ContinueOnError)
	quick := fs.Bool("quick", false, "short runs for the release candidate")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	switch args[0] {
	case "check":
		return check(root, *quick)
	case "report":
		return report(root)
	}
	fn, ok := suites[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "✕ Unknown suite %q.\n  next  Use one of: %s (Postgres suites need: go run -tags integration ./bench/cmd/dgbench …)\n", args[0], strings.Join(suiteNames(), ", "))
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	start := time.Now()
	f, err := fn(ctx, Opts{Root: root, Quick: *quick})
	if err != nil {
		fmt.Fprintln(os.Stderr, "✕ "+args[0]+": "+err.Error())
		return 1
	}
	p, err := f.Write(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "✕ "+err.Error())
		return 1
	}
	for _, s := range f.Scenarios {
		sym := "·"
		switch s.Result {
		case "pass":
			sym = "✓"
		case "fail":
			sym = "✕"
		}
		fmt.Printf("%s %-28s %s\n", sym, s.Name, line(s))
	}
	rel, _ := filepath.Rel(root, p)
	fmt.Printf("  raw  %s · %s · %s power · %s\n", rel, f.Host.CPU, f.Host.Power, time.Since(start).Round(time.Second))
	if f.Host.Power != "ac" {
		fmt.Println("  note  This run was on " + f.Host.Power + " power, so bench-check won't accept it. Plug in and run it again.")
	}
	return 0
}

func line(s load.Scenario) string {
	if len(s.Runs) == 0 {
		return ""
	}
	r := s.Runs[len(s.Runs)/2]
	if len(r.Values) > 0 {
		keys := make([]string, 0, len(r.Values))
		for k := range r.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %v", k, r.Values[k]))
		}
		return strings.Join(parts, " · ")
	}
	return fmt.Sprintf("p50 %.3f ms · p99 %.3f ms · max %.1f ms · %d requests · %d errors · %.0f/s", r.P50ms, r.P99ms, r.MaxMs, r.Count, r.Errors, r.AchievedRate)
}

func suiteNames() []string {
	names := []string{"policy", "verify", "quickstart", "gate", "stop", "deed", "witness", "wal", "mcp", "soak"}
	return names
}

func check(root string, quick bool) int {
	s, problems, err := load.Check(load.Options{Root: root, AllowQuick: quick})
	if err != nil {
		fmt.Fprintln(os.Stderr, "✕ "+err.Error())
		return 2
	}
	fmt.Print(s.Text())
	for _, p := range problems {
		fmt.Println("  note  " + p)
	}
	if s.Missed > 0 {
		return 1
	}
	return 0
}

func report(root string) int {
	s, problems, err := load.Check(load.Options{Root: root})
	if err != nil {
		fmt.Fprintln(os.Stderr, "✕ "+err.Error())
		return 2
	}
	fmt.Print(s.Text())
	for _, p := range problems {
		fmt.Println("  note  " + p)
	}
	if err := load.WriteSummary(root, s); err != nil {
		fmt.Fprintln(os.Stderr, "✕ "+err.Error())
		return 1
	}
	fmt.Println("  wrote  bench/results/SUMMARY.json · docs/BENCHMARKS.md")
	return 0
}

func version() string { return buildinfo.Get().Version }
