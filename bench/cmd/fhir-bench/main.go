//go:build integration

// Command fhir-bench plays the FHIR write-integrity benchmark (corpus/fhir/) through each subject
// and scores it (corpus/fhir/BENCHMARK.md). scripts/fhir_bench.sh (make fhir-bench) starts a
// throwaway Medplum on PostgreSQL 18.6 in Docker and calls it:
//
//	go run -tags integration ./bench/cmd/fhir-bench -subjects none,http-status,deedglass
//
// Subjects: none (the baseline), http-status (trusts 2xx), deedglass (a real deedglass serve
// built from this tree, its Gate in front of every agent call) and cmd:<name>=<command>. It
// refuses to run outside a throwaway stack (DEEDGLASS_IT_THROWAWAY=1).
package main

//deedglass:allow-exec benchmark tooling: reads the commit with git for the results file

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/corpus/fhirbench"
)

// types are the resource types the scenarios write; the witness publication covers their
// history tables.
var types = []string{"MedicationRequest", "Observation", "Patient"}

func main() {
	var (
		dir      = flag.String("dir", "corpus/fhir", "the benchmark directory")
		only     = flag.String("run", "", "run only the scenarios whose id contains this")
		subjects = flag.String("subjects", "none,http-status,deedglass", "comma-separated subjects: none, http-status, deedglass, cmd:<name>=<command>")
		out      = flag.String("out", "", "write the results file here (and the markdown table next to it, .md)")
		write    = flag.Bool("write-expected", false, "record each scenario's expected.json facts from the baseline run")
	)
	flag.Parse()
	env := func(k string) string { return os.Getenv(k) }
	if env("DEEDGLASS_IT_THROWAWAY") != "1" || env("FHIRBENCH_MEDPLUM_URL") == "" {
		fmt.Fprintln(os.Stderr, "fhir-bench: refusing: runs only on a throwaway stack (make fhir-bench starts one and sets DEEDGLASS_IT_THROWAWAY=1 and FHIRBENCH_MEDPLUM_*)")
		os.Exit(2)
	}
	scenarios, err := fhirbench.Load(*dir)
	if err != nil {
		fail(err)
	}
	var picked []*fhirbench.Scenario
	for _, s := range scenarios {
		if (*only == "" || strings.Contains(s.ID, *only)) && runsOn(s, "medplum") {
			picked = append(picked, s)
		}
	}
	if len(picked) == 0 {
		fail(fmt.Errorf("no scenario matches -run %q on medplum", *only))
	}
	root, err := filepath.Abs(".")
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	srv, err := newMedplum(ctx, env("FHIRBENCH_MEDPLUM_URL"), env("FHIRBENCH_MEDPLUM_ADMIN"), env("FHIRBENCH_MEDPLUM_PASSWORD"),
		env("FHIRBENCH_MEDPLUM_DSN"), env("FHIRBENCH_MEDPLUM_WITNESS_DSN"), env("FHIRBENCH_MEDPLUM_VERSION"), types)
	if err != nil {
		fail(err)
	}
	defer srv.close()

	if *write {
		for _, s := range picked {
			r, err := fhirbench.PlayUnscored(ctx, srv, fhirbench.None{}, s, nonce())
			if err != nil {
				fail(err)
			}
			s.Expected["medplum"] = fhirbench.Expected{Facts: r.Stored, Deedglass: s.Expected["medplum"].Deedglass}
			b, _ := json.MarshalIndent(s.Expected, "", "  ")
			if err := os.WriteFile(filepath.Join(s.Dir, "expected.json"), append(b, '\n'), 0o644); err != nil {
				fail(err)
			}
			fmt.Printf("  %-32s stored %s\n", s.ID, r.Stored.String())
		}
		fmt.Println("wrote expected.json for each scenario; review the diff")
		return
	}

	f := &fhirbench.File{Object: "fhir_bench_run", Version: 1, StartedAt: time.Now().UTC().Format(time.RFC3339), Commit: commit(root),
		Method:  "corpus/fhir/BENCHMARK.md",
		Servers: []fhirbench.ServerInfo{{Name: "medplum", Version: srv.Version(), Postgres: pgVersion(ctx, env("FHIRBENCH_MEDPLUM_DSN")), Image: env("FHIRBENCH_MEDPLUM_IMAGE")}}}
	for _, s := range picked {
		f.Scenarios = append(f.Scenarios, fhirbench.Describe(s, "medplum"))
	}
	for _, name := range strings.Split(*subjects, ",") {
		subj, err := open(strings.TrimSpace(name), root)
		if err != nil {
			fail(err)
		}
		info := subj.Info()
		f.Subjects = append(f.Subjects, info)
		fmt.Printf("· %s\n", info.Name)
		for _, s := range picked {
			r, err := fhirbench.Play(ctx, srv, subj, s, nonce())
			if err != nil {
				_ = subj.Close()
				fail(err)
			}
			// A subject that doesn't stand in front of the calls must see exactly the expected facts:
			// otherwise the server's behaviour changed, and the scenario needs a look.
			if info.Kind != "gateway" && !fhirbench.Equal(r.Stored, s.Expected["medplum"].Facts) {
				_ = subj.Close()
				fail(fmt.Errorf("%s with %s stored %s, and expected.json says %s", s.ID, info.Name, r.Stored.String(), s.Expected["medplum"].Facts.String()))
			}
			f.Results = append(f.Results, r)
			fmt.Printf("  %-32s flag %-6s caught %-5t effect %-5t record %-5t  %s\n", s.ID, r.Score.Flag, r.Score.Caught, r.Score.Effect, r.Score.Record, strings.Join(r.Report.Notes, "; "))
		}
		if err := subj.Close(); err != nil {
			fail(err)
		}
	}
	f.Totals = fhirbench.Tally(f.Subjects, f.Results)
	for _, t := range f.Totals {
		fmt.Printf("%s: caught %d of %d (%d before the write, %d after), %d false alarms, true effect %d of %d, checkable record %d of %d\n",
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

func runsOn(s *fhirbench.Scenario, server string) bool {
	for _, x := range s.Servers {
		if x == server {
			return true
		}
	}
	return false
}

func open(name, root string) (fhirbench.Subject, error) {
	switch {
	case name == "none":
		return fhirbench.None{}, nil
	case name == "http-status":
		return fhirbench.HTTPStatus{}, nil
	case name == "deedglass":
		return startDeedglass(root, types)
	case strings.HasPrefix(name, "cmd:"):
		n, c, ok := strings.Cut(strings.TrimPrefix(name, "cmd:"), "=")
		if !ok || n == "" || c == "" {
			return nil, fmt.Errorf("a command subject is cmd:<name>=<command>, not %q", name)
		}
		return fhirbench.Command{Name: n, Cmd: c}, nil
	}
	return nil, fmt.Errorf("unknown subject %q (none, http-status, deedglass, cmd:<name>=<command>)", name)
}

func nonce() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// commit is HEAD, with "-dirty" when the tree has changes outside the results.
func commit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	c := strings.TrimSpace(string(out))
	if st, err := exec.Command("git", "-C", root, "status", "--porcelain", "--", ".", ":!corpus/fhir/results", ":!.tmp").Output(); err != nil || len(strings.TrimSpace(string(st))) > 0 {
		c += "-dirty"
	}
	return c
}

func pgVersion(ctx context.Context, dsn string) string {
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
	fmt.Fprintln(os.Stderr, "fhir-bench:", err)
	os.Exit(1)
}
