//go:build integration

// Command corpus-bench plays the intent-vs-effect corpus (corpus/) through each subject and scores
// it (corpus/BENCHMARK.md). scripts/corpus_bench.sh (make corpus-bench) starts a throwaway
// PostgreSQL 18.6 in Docker and calls it:
//
//	go run -tags integration ./bench/cmd/corpus-bench -dsn <superuser DSN> -subjects deedglass,none
//
// Subjects: deedglass (a real deedglass serve built from this tree, its Gate in front of every
// agent statement, each Postgres connector witnessed at L2), none (the baseline), and
// cmd:<name>=<command> (an observer driven by a shell command, internal/corpus/bench.Command).
// It refuses to run outside a throwaway cluster (DEEDGLASS_IT_THROWAWAY=1).
package main

//deedglass:allow-exec benchmark tooling: reads the commit with git for the results file

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/corpus"
	"github.com/deedglass/deedglass/internal/corpus/bench"
)

func main() {
	var (
		dsn      = flag.String("dsn", os.Getenv("DEEDGLASS_IT_ADMIN_URL"), "a superuser DSN for a throwaway server with wal_level=logical")
		dir      = flag.String("dir", "corpus", "the corpus directory")
		only     = flag.String("run", "", "run only the scenarios whose id contains this")
		subjects = flag.String("subjects", "deedglass,none", "comma-separated subjects: deedglass, none, cmd:<name>=<command>")
		out      = flag.String("out", "", "write the results file here (and the markdown table next to it, .md)")
		keep     = flag.Bool("keep", false, "leave each scenario's database in place")
	)
	flag.Parse()
	if os.Getenv("DEEDGLASS_IT_THROWAWAY") != "1" || *dsn == "" {
		fmt.Fprintln(os.Stderr, "corpus-bench: refusing: runs only on a throwaway cluster (make corpus-bench starts one and sets DEEDGLASS_IT_THROWAWAY=1 and the DSN)")
		os.Exit(2)
	}
	scenarios, err := corpus.Load(*dir)
	if err != nil {
		fail(err)
	}
	var picked []*corpus.Scenario
	for _, s := range scenarios {
		if *only == "" || strings.Contains(s.ID, *only) {
			picked = append(picked, s)
		}
	}
	if len(picked) == 0 {
		fail(fmt.Errorf("no scenario matches -run %q", *only))
	}
	root, err := filepath.Abs(".")
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	f := &bench.File{Object: "corpus_bench_run", Version: 1, StartedAt: time.Now().UTC().Format(time.RFC3339),
		Commit: commit(root), Postgres: serverVersion(ctx, *dsn), Method: "corpus/BENCHMARK.md"}
	for _, s := range picked {
		f.Scenarios = append(f.Scenarios, bench.Describe(s))
	}
	for _, name := range strings.Split(*subjects, ",") {
		subj, err := open(ctx, strings.TrimSpace(name), root)
		if err != nil {
			fail(err)
		}
		f.Subjects = append(f.Subjects, subj.Info())
		fmt.Printf("· %s\n", subj.Info().Name)
		for _, s := range picked {
			r, err := bench.Play(ctx, subj, s, bench.RunOptions{DSN: *dsn, Keep: *keep})
			if err != nil {
				_ = subj.Close()
				fail(err)
			}
			f.Results = append(f.Results, r)
			fmt.Printf("  %-22s flag %-6s caught %-5t effect %-5t record %-5t  %s\n", s.ID, r.Score.Flag, r.Score.Caught, r.Score.Effect, r.Score.Record, strings.Join(r.Report.Notes, "; "))
		}
		if err := subj.Close(); err != nil {
			fail(err)
		}
	}
	f.Totals = bench.Tally(f.Subjects, f.Results)
	for _, t := range f.Totals {
		fmt.Printf("%s: caught %d of %d (%d before commit, %d after), %d false alarms, true effect %d of %d, checkable record %d of %d\n",
			t.Subject, t.Caught, t.Diverging, t.CaughtBefore, t.CaughtAfter, t.FalseAlarms, t.Effect, t.Scenarios, t.Record, t.Scenarios)
	}
	if *out != "" {
		if err := f.Write(*out); err != nil {
			fail(err)
		}
		md := strings.TrimSuffix(*out, filepath.Ext(*out)) + ".md"
		if err := os.WriteFile(md, []byte(f.Markdown()), 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("wrote %s and %s\n", *out, md)
	}
}

func open(ctx context.Context, name, root string) (bench.Subject, error) {
	switch {
	case name == "none":
		return bench.None{}, nil
	case name == "deedglass":
		return startDeedglass(ctx, root)
	case strings.HasPrefix(name, "cmd:"):
		n, c, ok := strings.Cut(strings.TrimPrefix(name, "cmd:"), "=")
		if !ok || n == "" || c == "" {
			return nil, fmt.Errorf("a command subject is cmd:<name>=<command>, not %q", name)
		}
		return bench.Command{Name: n, Cmd: c}, nil
	}
	return nil, fmt.Errorf("unknown subject %q (deedglass, none, cmd:<name>=<command>)", name)
}

// commit is HEAD, with "-dirty" when the tree has changes outside corpus/results.
func commit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	c := strings.TrimSpace(string(out))
	if st, err := exec.Command("git", "-C", root, "status", "--porcelain", "--", ".", ":!corpus/results", ":!.tmp").Output(); err != nil || len(strings.TrimSpace(string(st))) > 0 {
		c += "-dirty"
	}
	return c
}

func serverVersion(ctx context.Context, dsn string) string {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return "unknown"
	}
	defer c.Close(ctx)
	var v string
	if c.QueryRow(ctx, "SELECT pg_catalog.current_setting('server_version')").Scan(&v) != nil {
		return "unknown"
	}
	return v
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "corpus-bench:", err)
	os.Exit(1)
}
