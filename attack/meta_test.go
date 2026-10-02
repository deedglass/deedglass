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

	"github.com/deedglass/deedglass/internal/intent"
)

// The digests pin id|expected|expected_detail|priority, one line per case in file order, per
// group set (S18, ADR-040). If a test fails because an expected outcome changed, the change needs
// a founder-approved ADR (CLAUDE.md rule 2) before the constant is updated. Each later group set
// gets its own constant, so declaring new cases never changes an earlier pin.
const (
	// expectedDigest pins groups A–G (V0) in the Deedglass verdict words (ADR-021, applied in S18).
	expectedDigest = "c2213fdcfe94e54cd8c7be4bcef0337899b262d88e333728d2a2b76aef191b19"
	// expectedDigestV0Words is the frozen 2026-09-28 pin of groups A–G in the V0 words. ADR-021
	// renamed only words: TestADR021WordsOnly maps them back and must land on this value.
	expectedDigestV0Words = "503500269dfa2de0f6a52979922486e63e5894696b8446d5f8cdc3463f00c14b"
	// expectedDigestV7 pins groups H, J and K (S18; ADR-040, PROPOSED; the founder approves in chat).
	expectedDigestV7 = "b1583ab49a0ea3efdfc4bed2f4842a41e5d82a38c58f79b1e5d8d8c78f337428"
	// expectedDigestW pins group W, the L2 witness cases (S21; ADR-046, PROPOSED; the founder
	// approves in chat). Declared before the L2 code was written.
	expectedDigestW = "6662565a418a4a1f3f8218220d387b5df1176c35bca10e6d368213f53573c0ff"
	// expectedDigestL pins group L, the approval cases (S22; ADR-049, PROPOSED; the founder
	// approves in chat). Declared before the cases were implemented.
	expectedDigestL = "398129372b8ab6e0d2f87aa6b3a0cfa737de3329f484e277d892ccf69c6f36ec"
)

// groupSets maps each pinned digest to its groups. A later session adds its own row (S19 N,
// S21 W, S22 L, S23 U, S34 P/Q/R; docs/ATTACK_SUITE.md "Group letters").
var groupSets = []struct {
	groups, digest string
}{
	{"ABCDEFG", expectedDigest},
	{"HJK", expectedDigestV7},
	{"W", expectedDigestW},
	{"L", expectedDigestL},
}

type meta struct {
	Format     string            `json:"format"`
	Frozen     string            `json:"frozen"`
	Source     string            `json:"source"`
	Rule       string            `json:"rule"`
	Outcomes   map[string]string `json:"outcomes"`
	Priorities map[string]string `json:"priorities"`
	Pending    []json.RawMessage `json:"pending"`
	Cases      []caseMeta        `json:"cases"`
}

type caseMeta struct {
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
	Needs           []string `json:"needs,omitempty"` // sessions whose feature the case waits on; outside the digest
}

// digest is sha256 over "id|expected|expected_detail|priority\n" for the cases whose group is in
// groups, in file order. status and needs stay outside it.
func digest(cases []caseMeta, groups string) string {
	var b strings.Builder
	for _, c := range cases {
		if strings.Contains(groups, c.Group) {
			fmt.Fprintf(&b, "%s|%s|%s|%s\n", c.ID, c.Expected, c.ExpectedDetail, c.Priority)
		}
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
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
	}{{"A", 13}, {"B", 7}, {"C", 10}, {"D", 17}, {"E", 11}, {"F", 4}, {"G", 4}, {"H", 11}, {"J", 4}, {"K", 4}, {"L", 8}, {"W", 13}} {
		for i := 1; i <= g.n; i++ {
			ids = append(ids, fmt.Sprintf("%s%02d", g.p, i))
		}
	}
	return ids
}

func TestAttackMeta(t *testing.T) {
	m := load(t)
	if m.Format != "deedglass.attack_meta/1" {
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
		for _, n := range c.Needs {
			if !reNeeds.MatchString(n) {
				t.Errorf("%s: needs %q is not a session S19 to S37", c.ID, n)
			}
		}
		if strings.Contains("HJK", c.Group) {
			// S18: the V7 cases carry only the OWASP Agentic ids confirmed for our mappings (C-006).
			for _, o := range c.OWASP {
				if !reV7OWASP.MatchString(o) {
					t.Errorf("%s: owasp %q (V7 cases use only ASI01, ASI02, ASI03, ASI10)", c.ID, o)
				}
			}
			if c.Source != "prompts-v7/S18.md" || len(c.V3Ref) != 0 || len(c.ExpectedReasons) != 0 {
				t.Errorf("%s: source %q v3_ref %v reasons %v", c.ID, c.Source, c.V3Ref, c.ExpectedReasons)
			}
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
	if byID["A13"] != "BLOCKED" || pri["A13"] != "P0" || byID["E11"] != "VERIFIED" || pri["E11"] != "P0" {
		t.Errorf("A13/E11 drifted: A13=%s/%s E11=%s/%s", byID["A13"], pri["A13"], byID["E11"], pri["E11"])
	}
	// The demo set (spec §14.1, plan §0.3 #4) and its outcomes.
	for id, exp := range map[string]string{
		"A01": "VERIFIED", "B01": "BLOCKED", "C01": "BLOCKED", "A03": "DETECTED",
		"D05": "DETECTED", "D06": "UNVERIFIABLE", "G01": "OUTSIDE SCOPE",
		// S18's demo set.
		"H01": "DENIED", "H08": "MISMATCH", "K01": "BLOCKED",
		// S21's hero.
		"W01": "MISMATCH",
	} {
		if !demo[id] || byID[id] != exp || pri[id] != "P0" {
			t.Errorf("demo case %s: demo=%v expected=%s priority=%s", id, demo[id], byID[id], pri[id])
		}
	}
}

var (
	reNeeds   = regexp.MustCompile(`^S(19|2[0-9]|3[0-7])$`)
	reV7OWASP = regexp.MustCompile(`^ASI(01|02|03|10)$`)
)

func TestExpectedOutcomesPinned(t *testing.T) {
	m := load(t)
	cases := m.Cases
	covered := ""
	for _, g := range groupSets {
		covered += g.groups
		if got := digest(cases, g.groups); got != g.digest {
			t.Errorf("expected outcomes of groups %s changed (digest %s). This needs a founder-approved ADR before the pinned digest is updated.", g.groups, got)
		}
	}
	for _, c := range cases {
		if !strings.Contains(covered, c.Group) {
			t.Errorf("%s: group %s has no pinned digest; add a constant and a groupSets row under an ADR", c.ID, c.Group)
		}
	}
}

// TestADR021WordsOnly: ADR-021 changed words, not meaning. Mapping VERIFIED, MISMATCH and
// UNVERIFIABLE back to VALID, INVALID and INCONCLUSIVE in groups A–G gives the frozen V0 digest.
func TestADR021WordsOnly(t *testing.T) {
	m := load(t)
	// Whole words only: reason codes such as VALUE_MISMATCH keep their spelling.
	words := regexp.MustCompile(`\b(VERIFIED|MISMATCH|UNVERIFIABLE)\b`)
	old := map[string]string{"VERIFIED": "VALID", "MISMATCH": "INVALID", "UNVERIFIABLE": "INCONCLUSIVE"}
	back := func(s string) string { return words.ReplaceAllStringFunc(s, func(w string) string { return old[w] }) }
	var v0 []caseMeta
	for _, c := range m.Cases {
		if strings.Contains("ABCDEFG", c.Group) {
			c.Expected, c.ExpectedDetail = back(c.Expected), back(c.ExpectedDetail)
			v0 = append(v0, c)
		}
	}
	if got := digest(v0, "ABCDEFG"); got != expectedDigestV0Words {
		t.Fatalf("groups A–G differ from the frozen V0 outcomes by more than ADR-021's words (digest %s)", got)
	}
}
