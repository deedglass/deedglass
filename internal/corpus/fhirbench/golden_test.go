package fhirbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestGoldenReplay replays the committed live run (corpus/fhir/results/latest.json) without a
// server: the facts are recomputed from the versions the runner read from the server's database,
// the baseline subjects' reports from the calls and answers recorded, every score from the facts
// and the scenarios' expected.json, the totals from the scores, and the markdown table from the
// file. A change to the facts, the scoring rules, a scenario's expected facts or the table that
// the committed run doesn't agree with fails here, in CI, where Docker isn't available. The live
// run itself is make fhir-bench.
func TestGoldenReplay(t *testing.T) {
	root := filepath.Join("..", "..", "..", "corpus", "fhir")
	f, err := Read(filepath.Join(root, "results", "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Object != "fhir_bench_run" || f.Version != 1 || f.Method != "corpus/fhir/BENCHMARK.md" {
		t.Fatalf("results header: %s v%d %s", f.Object, f.Version, f.Method)
	}
	scenarios, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*Scenario{}
	for _, s := range scenarios {
		byID[s.ID] = s
	}
	kinds := map[string]string{}
	for _, s := range f.Subjects {
		kinds[s.Name] = s.Kind
	}
	// Every scenario on every server it names was run by every subject in the file.
	for _, s := range scenarios {
		for _, srv := range s.Servers {
			for _, sub := range f.Subjects {
				if f.result(sub.Name, s.ID, srv) == nil {
					t.Errorf("%s on %s has no result for %s: run make fhir-bench OUT=corpus/fhir/results/latest.json", s.ID, srv, sub.Name)
				}
			}
		}
	}
	for _, si := range f.Scenarios {
		s := byID[si.ID]
		if s == nil {
			t.Errorf("the results name %s, which isn't in corpus/fhir/scenarios", si.ID)
			continue
		}
		if want := Describe(s, si.Server); !reflect.DeepEqual(si, want) {
			t.Errorf("%s: the scenario row is\n %+v\nand the scenario now says\n %+v", si.ID, si, want)
		}
	}
	for _, r := range f.Results {
		s := byID[r.Scenario]
		if s == nil {
			continue
		}
		if got := Facts(&r.Run); !Equal(got, r.Stored) {
			t.Errorf("%s/%s: the facts recomputed from the recorded versions are %s; the file says %s", r.Scenario, r.Subject, got.String(), r.Stored.String())
		}
		exp, ok := s.Expected[r.Server]
		if !ok {
			t.Errorf("%s has no expected facts for %s", r.Scenario, r.Server)
			continue
		}
		if kinds[r.Subject] != "gateway" && !Equal(r.Stored, exp.Facts) {
			t.Errorf("%s/%s stored %s, and expected.json says %s", r.Scenario, r.Subject, r.Stored.String(), exp.Facts.String())
		}
		var replay Report
		switch r.Subject {
		case "none":
			replay, _ = noneSession{s}.Finish(t.Context(), &r.Run)
		case "http-status":
			replay, _ = statusSession{}.Finish(t.Context(), &r.Run)
		}
		if replay.Flag != "" {
			a, _ := json.Marshal(replay)
			b, _ := json.Marshal(r.Report)
			if string(a) != string(b) {
				t.Errorf("%s/%s: the report replayed is\n %s\nand the file says\n %s", r.Scenario, r.Subject, a, b)
			}
		}
		if got := ScoreOf(!Equal(s.AgentReports.Effect, exp.Facts), r.Stored, r.Report); got != r.Score {
			t.Errorf("%s/%s: rescored %+v; the file says %+v", r.Scenario, r.Subject, got, r.Score)
		}
	}
	if got := Tally(f.Subjects, f.Results); !reflect.DeepEqual(got, f.Totals) {
		t.Errorf("totals: recomputed %+v; the file says %+v", got, f.Totals)
	}
	md, err := os.ReadFile(filepath.Join(root, "results", "latest.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(md) != f.Markdown() {
		t.Errorf("corpus/fhir/results/latest.md isn't what latest.json renders to: regenerate it with make fhir-bench, never by hand")
	}
}
