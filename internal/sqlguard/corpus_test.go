package sqlguard

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The unit corpus runner: every case in testdata/corpus.yaml against Analyze, with one readable
// line per field that differs. corpus_integration_test.go runs the same file against Postgres.

type corpusFile struct {
	Version int          `yaml:"version"`
	Setup   string       `yaml:"setup"`
	Cases   []corpusCase `yaml:"cases"`
}

type corpusCase struct {
	ID           string         `yaml:"id"`
	Family       string         `yaml:"family"`
	SQL          *string        `yaml:"sql"`
	Gen          string         `yaml:"gen"`
	Note         string         `yaml:"note"`
	Differential bool           `yaml:"differential"`
	MinPG        int            `yaml:"min_pg"`
	Expect       map[string]any `yaml:"expect"`
}

func loadCorpus(t testing.TB) corpusFile {
	t.Helper()
	f, err := os.Open("testdata/corpus.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var c corpusFile
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

// corpusSQL returns the case's SQL, generating it for gen cases.
func corpusSQL(c corpusCase) string {
	switch c.Gen {
	case "too_large":
		s := "SELECT 1 -- "
		return s + strings.Repeat("x", DefaultMaxBytes+1-len(s))
	case "too_deep":
		return "SELECT " + strings.Repeat("(", 300) + "1" + strings.Repeat(")", 300)
	case "too_many_statements":
		return strings.Repeat("SELECT 1;", DefaultMaxStatements+1)
	}
	return *c.SQL
}

// factsMap renders Facts by json name, the way the corpus spells expectations.
func factsMap(f Facts) map[string]any {
	m := map[string]any{}
	v := reflect.ValueOf(f)
	tp := v.Type()
	for i := 0; i < tp.NumField(); i++ {
		name := strings.Split(tp.Field(i).Tag.Get("json"), ",")[0]
		m[name] = v.Field(i).Interface()
	}
	return m
}

func sameValue(want, got any) bool {
	switch g := got.(type) {
	case []string:
		w, ok := want.([]any)
		if !ok {
			return false
		}
		if len(w) != len(g) {
			return false
		}
		for i := range w {
			if fmt.Sprint(w[i]) != g[i] {
				return false
			}
		}
		return true
	case int:
		w, ok := want.(int)
		return ok && w == g
	case bool:
		w, ok := want.(bool)
		return ok && w == g
	case string:
		w, ok := want.(string)
		return ok && w == g
	}
	return false
}

func TestCorpus(t *testing.T) {
	c := loadCorpus(t)
	if c.Version != 1 {
		t.Fatalf("corpus version %d", c.Version)
	}
	known := factsMap(Facts{})
	ids := map[string]bool{}
	families := map[string]int{}
	fails := 0
	for _, k := range c.Cases {
		if ids[k.ID] {
			t.Errorf("%s: duplicate id", k.ID)
		}
		ids[k.ID] = true
		families[k.Family]++
		if (k.SQL == nil) == (k.Gen == "") {
			t.Errorf("%s: set exactly one of sql and gen", k.ID)
			continue
		}
		for _, must := range []string{"kind", "category", "statements", "ambiguous"} {
			if _, ok := k.Expect[must]; !ok {
				t.Errorf("%s: expect.%s is required", k.ID, must)
			}
		}
		got := factsMap(Analyze(corpusSQL(k), Options{}))
		keys := make([]string, 0, len(k.Expect))
		for key := range k.Expect {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var diffs []string
		for _, key := range keys {
			if _, ok := known[key]; !ok {
				diffs = append(diffs, fmt.Sprintf("unknown fact %q", key))
				continue
			}
			if !sameValue(k.Expect[key], got[key]) {
				diffs = append(diffs, fmt.Sprintf("%s: want %v, got %v", key, k.Expect[key], got[key]))
			}
		}
		if len(diffs) > 0 {
			fails++
			sql := corpusSQL(k)
			if len(sql) > 120 {
				sql = sql[:120] + "…"
			}
			t.Errorf("%s (%s) %q\n    %s", k.ID, k.Family, sql, strings.Join(diffs, "\n    "))
		}
	}
	if len(c.Cases) < 90 {
		t.Errorf("the corpus has %d cases; S11 asks for at least 90", len(c.Cases))
	}
	names := make([]string, 0, len(families))
	for f := range families {
		names = append(names, f)
	}
	slices.Sort(names)
	var parts []string
	for _, f := range names {
		parts = append(parts, fmt.Sprintf("%s %d", f, families[f]))
	}
	t.Logf("corpus: %d cases (%s); %d differ", len(c.Cases), strings.Join(parts, ", "), fails)
}
