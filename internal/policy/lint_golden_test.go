package policy

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the lint goldens, hashes.json and costs.json")

// lintFixtures lists every fixture under testdata/lint (valid ones live in valid/).
func lintFixtures(t testing.TB) []string {
	var out []string
	err := filepath.WalkDir("testdata/lint", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".golden.yaml") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func fixtureOptions(p string) Options {
	return Options{File: filepath.Base(p), Voice: strings.Contains(filepath.Base(p), "voice")}
}

// TestLintGolden compiles every fixture and compares the text and JSON output with its goldens.
// Valid fixtures also pin their version_hash (hashes.json) and each rule's estimated cost
// (costs.json), so any change to the normalizer or the cost model shows up as a diff.
func TestLintGolden(t *testing.T) {
	files := lintFixtures(t)
	if len(files) < 60 {
		t.Errorf("want at least 60 lint fixtures, have %d", len(files))
	}
	hashes := map[string]string{}
	costs := map[string]map[string]uint64{}
	for _, p := range files {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		opts := fixtureOptions(p)
		r := Lint(src, opts)
		var txt bytes.Buffer
		RenderText(&txt, r, src, opts.File, Plain{})
		golden(t, strings.TrimSuffix(p, ".yaml")+".golden.txt", txt.Bytes())
		golden(t, strings.TrimSuffix(p, ".yaml")+".golden.json", r.JSON())
		rel, _ := filepath.Rel("testdata/lint", p)
		if strings.HasPrefix(rel, "valid"+string(filepath.Separator)) {
			if !r.Valid || len(r.Errors) > 0 || len(r.Warnings) > 0 {
				t.Errorf("%s: valid fixtures must have no diagnostics: %+v %+v", p, r.Errors, r.Warnings)
				continue
			}
			hashes[filepath.ToSlash(rel)] = *r.VersionHash
			c := r.Compiled()
			m := map[string]uint64{}
			for _, ru := range c.Rules {
				if ru.When != nil {
					m[ru.ID] = ru.WhenCostMax
				}
			}
			for _, b := range c.Budgets {
				if b.Measure != nil {
					m[b.ID] = b.MeasureCostMax
				}
			}
			if len(m) > 0 {
				costs[filepath.ToSlash(rel)] = m
			}
		}
	}
	goldenJSON(t, "testdata/lint/hashes.json", hashes)
	goldenJSON(t, "testdata/lint/costs.json", costs)
}

func goldenJSON(t *testing.T, path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, path, append(b, '\n'))
}

func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("%s: missing golden (run go test ./internal/policy -run TestLintGolden -update): %v", path, err)
		return
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from its golden.\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
