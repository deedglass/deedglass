package attack

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/testamur/testamur/internal/intent"
)

// expectedDigest pins id|expected|expected_detail|priority for every case.
// If this test fails because an expected outcome changed, the change needs a
// founder-approved ADR (CLAUDE.md rule 2) before this constant is updated.
const expectedDigest = "503500269dfa2de0f6a52979922486e63e5894696b8446d5f8cdc3463f00c14b"

type meta struct {
	Format     string            `json:"format"`
	Frozen     string            `json:"frozen"`
	Source     string            `json:"source"`
	Rule       string            `json:"rule"`
	Outcomes   map[string]string `json:"outcomes"`
	Priorities map[string]string `json:"priorities"`
	Pending    []json.RawMessage `json:"pending"`
	Cases      []struct {
		ID              string   `json:"id"`
		Group           string   `json:"group"`
		Title           string   `json:"title"`
		Expected        string   `json:"expected"`
		ExpectedReasons []string `json:"expected_reasons"`
		ExpectedDetail  string   `json:"expected_detail"`
		Priority        string   `json:"priority"`
		Demo            bool     `json:"demo"`
		V3Ref           []int    `json:"v3_ref"`
		OWASP           []string `json:"owasp"`
		Source          string   `json:"source"`
		Status          string   `json:"status"`
		Note            string   `json:"note,omitempty"`
	} `json:"cases"`
}

func load(t *testing.T) meta {
	t.Helper()
	raw, err := os.ReadFile("attack_meta.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := intent.ParseStrict(raw); err != nil {
		t.Fatalf("attack_meta.json must satisfy the strict JSON profile (no duplicate ids as keys, integers only): %v", err)
	}
	var m meta
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func wantIDs() []string {
	var ids []string
	for _, g := range []struct {
		p string
		n int
	}{{"A", 13}, {"B", 7}, {"C", 10}, {"D", 17}, {"E", 11}, {"F", 4}, {"G", 4}} {
		for i := 1; i <= g.n; i++ {
			ids = append(ids, fmt.Sprintf("%s%02d", g.p, i))
		}
	}
	return ids
}

func TestAttackMeta(t *testing.T) {
	m := load(t)
	if m.Format != "txv.attack_meta/1" {
		t.Fatalf("format %q", m.Format)
	}
	reasonRe := regexp.MustCompile(`^[A-Z][A-Z0-9_]+$`)
	owaspRe := regexp.MustCompile(`^(ASI(0[1-9]|10)|LLM06)$`)
	var got []string
	seen := map[string]bool{}
	for _, c := range m.Cases {
		if seen[c.ID] {
			t.Errorf("duplicate id %s", c.ID)
		}
		seen[c.ID] = true
		got = append(got, c.ID)
		if c.Group != c.ID[:1] {
			t.Errorf("%s: group %q", c.ID, c.Group)
		}
		if _, ok := m.Outcomes[c.Expected]; !ok {
			t.Errorf("%s: expected %q is not a declared outcome", c.ID, c.Expected)
		}
		if _, ok := m.Priorities[c.Priority]; !ok {
			t.Errorf("%s: priority %q is not declared", c.ID, c.Priority)
		}
		if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.ExpectedDetail) == "" {
			t.Errorf("%s: title and expected_detail are required", c.ID)
		}
		for _, r := range c.ExpectedReasons {
			if !reasonRe.MatchString(r) {
				t.Errorf("%s: reason %q", c.ID, r)
			}
		}
		for _, o := range c.OWASP {
			if !owaspRe.MatchString(o) {
				t.Errorf("%s: owasp %q", c.ID, o)
			}
		}
		if c.Status != "not implemented" && c.Status != "implemented" {
			t.Errorf("%s: status %q", c.ID, c.Status)
		}
	}
	want := wantIDs()
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("case ids:\n got %v\nwant %v", got, want)
	}

	byID := map[string]string{}
	pri := map[string]string{}
	demo := map[string]bool{}
	for _, c := range m.Cases {
		byID[c.ID], pri[c.ID], demo[c.ID] = c.Expected, c.Priority, c.Demo
	}
	// Spike-driven additions (plan §3.0, §3.8) are P0.
	if byID["A13"] != "BLOCKED" || pri["A13"] != "P0" || byID["E11"] != "VALID" || pri["E11"] != "P0" {
		t.Errorf("A13/E11 drifted: A13=%s/%s E11=%s/%s", byID["A13"], pri["A13"], byID["E11"], pri["E11"])
	}
	// The demo set (spec §14.1, plan §0.3 #4) and its outcomes.
	for id, exp := range map[string]string{
		"A01": "VALID", "B01": "BLOCKED", "C01": "BLOCKED", "A03": "DETECTED",
		"D05": "DETECTED", "D06": "INCONCLUSIVE", "G01": "OUTSIDE SCOPE",
	} {
		if !demo[id] || byID[id] != exp || pri[id] != "P0" {
			t.Errorf("demo case %s: demo=%v expected=%s priority=%s", id, demo[id], byID[id], pri[id])
		}
	}
}

func TestExpectedOutcomesPinned(t *testing.T) {
	m := load(t)
	var b strings.Builder
	for _, c := range m.Cases {
		fmt.Fprintf(&b, "%s|%s|%s|%s\n", c.ID, c.Expected, c.ExpectedDetail, c.Priority)
	}
	h := sha256.Sum256([]byte(b.String()))
	if got := hex.EncodeToString(h[:]); got != expectedDigest {
		t.Fatalf("expected outcomes changed (digest %s). This needs a founder-approved ADR before expectedDigest is updated.", got)
	}
}
