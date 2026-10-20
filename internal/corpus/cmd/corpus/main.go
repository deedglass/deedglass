// Command corpus runs the intent-vs-effect corpus (corpus/) against a throwaway Postgres and
// compares what committed with each scenario's expected.json. scripts/corpus.sh (make corpus)
// starts the Postgres and calls it; run it directly against your own throwaway server with
//
//	go run ./internal/corpus/cmd/corpus -dsn 'postgres://postgres@127.0.0.1:5433/postgres'
//
// The server needs wal_level=logical and a superuser: each scenario gets its own database and
// logical slot, both dropped at the end (-keep leaves them).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/deedglass/deedglass/internal/corpus"
)

func main() {
	var (
		dsn  = flag.String("dsn", os.Getenv("CORPUS_PG_DSN"), "a superuser DSN for a throwaway server with wal_level=logical")
		dir  = flag.String("dir", "corpus", "the corpus directory")
		only = flag.String("run", "", "run only the scenarios whose id contains this")
		out  = flag.String("json", "", "also write what each scenario saw to this file")
		keep = flag.Bool("keep", false, "leave each scenario's database and slot in place")
	)
	flag.Parse()
	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "corpus: -dsn (or CORPUS_PG_DSN) is required. make corpus starts a throwaway Postgres for you.")
		os.Exit(2)
	}
	scenarios, err := corpus.Load(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx := context.Background()
	opt := corpus.Options{DSN: *dsn, Keep: *keep, Tool: os.Getenv("CORPUS_TOOL")}
	var (
		results []*corpus.Observed
		failed  int
		ran     int
	)
	for _, s := range scenarios {
		if *only != "" && !strings.Contains(s.ID, *only) {
			continue
		}
		ran++
		o, err := corpus.Run(ctx, s, opt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "✕ %s: %v\n", s.ID, err)
			os.Exit(2)
		}
		results = append(results, o)
		diff := corpus.Diff(s, o)
		mark := "✓"
		if len(diff) > 0 {
			mark, failed = "✕", failed+1
		}
		fmt.Printf("%s %s  %s\n", mark, s.ID, s.Title)
		fmt.Printf("    agent reports  %s: %s\n", s.Expected.AgentReports.Summary, corpus.Describe(s.Expected.AgentReports.Changes))
		fmt.Printf("    agent saw      %s\n", strings.Join(flatten(o.AgentSaw), ", "))
		fmt.Printf("    committed      %s\n", committed(o))
		fmt.Printf("    Deedglass      %s\n", deedglass(s.Expected.Deedglass))
		if o.ToolSaid != "" {
			fmt.Printf("    your tool      %s\n", o.ToolSaid)
		}
		for _, d := range diff {
			fmt.Printf("    differs        %s\n", d)
		}
	}
	if *out != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(*out, append(b, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	if ran == 0 {
		fmt.Fprintln(os.Stderr, "corpus: no scenario matches -run")
		os.Exit(2)
	}
	if failed > 0 {
		fmt.Printf("✕ %d of %d scenarios committed something other than expected.json says\n", failed, ran)
		os.Exit(1)
	}
	fmt.Printf("✓ %d %s · every database fact as expected.json says\n", ran, plural(ran, "scenario", "scenarios"))
}

func flatten(saw [][]string) []string {
	var out []string
	for _, s := range saw {
		out = append(out, s...)
	}
	return out
}

func committed(o *corpus.Observed) string {
	var parts []string
	for _, s := range []string{"agent", "other"} {
		if c := corpus.Sum(o.Commits.Transactions, s); len(c) > 0 {
			n := 0
			for _, t := range o.Commits.Transactions {
				if t.Session == s {
					n++
				}
			}
			parts = append(parts, fmt.Sprintf("%s (%s, %d %s)", corpus.Describe(c), s, n, plural(n, "transaction", "transactions")))
		}
	}
	for t, c := range o.Commits.State {
		if c.Before != c.After {
			parts = append(parts, fmt.Sprintf("%s rows %d → %d", t, c.Before, c.After))
		}
	}
	for ch, n := range o.Commits.Notifications {
		parts = append(parts, fmt.Sprintf("%d %s on NOTIFY channel %s", n, plural(n, "message", "messages"), ch))
	}
	if len(parts) == 0 {
		return "no row changes"
	}
	return strings.Join(parts, "; ")
}

func deedglass(d corpus.Deedglass) string {
	var parts []string
	for _, a := range d.Actions {
		switch {
		case a.Verdict == "none" && a.Decision == "deny":
			parts = append(parts, fmt.Sprintf("%s: denied by %s, not run", a.Statement, a.Rule))
		case a.Verdict == "none":
			parts = append(parts, fmt.Sprintf("%s: execution %s, nothing to check", a.Statement, a.Execution))
		default:
			parts = append(parts, fmt.Sprintf("%s: %s at %s (%s)", a.Statement, a.Verdict, a.Level, strings.Join(a.Reasons, ", ")))
		}
	}
	return strings.Join(parts, "; ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
