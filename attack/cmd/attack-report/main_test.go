package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/attack/harness"
)

func sampleRun(meta *harness.Meta) *harness.Run {
	run := &harness.Run{Object: "attack_run", StartedAt: "2026-10-02T00:10:00Z", Commit: "abc1234", Postgres: "18.6 (Homebrew)"}
	for _, c := range meta.Cases {
		if !strings.Contains("HJKLUW", c.Group) {
			continue
		}
		r := harness.Result{ID: c.ID, Title: c.Title, Expected: c.Expected, Actual: c.Expected, Detail: c.ExpectedDetail, Status: harness.StatusMatched, MS: 12}
		if len(c.Needs) > 0 {
			r.Status, r.Waiting, r.Actual = harness.StatusWaiting, c.Needs[0], "not run"
		}
		run.Cases = append(run.Cases, r)
	}
	run.Summary = harness.Summarize(run.Cases)
	run.Invariants = harness.Invariants{Chains: "intact", LogLines: 1234}
	return run
}

func TestGenerateDeterministic(t *testing.T) {
	meta, err := harness.LoadMeta(filepath.Join("..", "..", "attack_meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	run := sampleRun(meta)
	a := Generate(meta, run)
	run.Invariants.LogLines = 99999 // volatile numbers never reach the page
	for i := range run.Cases {
		run.Cases[i].MS = 999
	}
	b := Generate(meta, run)
	if !bytes.Equal(a, b) {
		t.Fatal("the page depends on timings or log counts")
	}
	page := string(a)
	for _, want := range []string{"Don't edit it by hand", "45 matched · 0 differ · 0 waiting on a later session", "| H01 |", "| W01 |", "| L01 |", "✓ matched",
		"What this suite doesn't cover", "| A01 |", "V0 suite", "expectedDigestV7"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "—") {
		t.Error("the page has an em dash")
	}
}

func TestCheckStale(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"attack/results", "docs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "attack_meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "attack", "attack_meta.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _ := harness.LoadMeta(filepath.Join(root, "attack", "attack_meta.json"))
	if err := harness.WriteRun(filepath.Join(root, "attack", "results", "latest.json"), *sampleRun(meta)); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	if code := run(root, true, true, &out, &errb); code != 1 {
		t.Fatalf("a missing page passed -check: %d", code)
	}
	if code := run(root, false, false, &out, &errb); code != 0 {
		t.Fatalf("generate: %d %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "45 matched · 0 differ · 0 waiting on a later session · chain intact · 0 secrets seen in 1,234 log lines") {
		t.Fatalf("summary line:\n%s", out.String())
	}
	if code := run(root, true, true, &out, &errb); code != 0 {
		t.Fatalf("a fresh page failed -check: %s", errb.String())
	}
}
