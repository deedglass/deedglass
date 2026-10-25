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
	// expectedDigestU pins group U, the undo cases (S23; ADR-052, PROPOSED; the founder approves
	// in chat). Declared before the cases were implemented.
	expectedDigestU = "ece59deebe94a2d2fd70f1da3e9f0f70c55debfebbb0644620764cf84073bf58"
	// expectedDigestS34 pins groups P, Q and R (S34; ADR-065, PROPOSED; the founder approves in
	// chat). Declared before any of their case code existed. P01 and P09 were not declared: S22's
	// L01 and S23's U01 already pin those attacks.
	expectedDigestS34 = "4bfe69608375ef9023f35bed231e0a15489993b0a7dab497584a370e1965bd42"
	// expectedDigestO pins group O, observe mode (S50; ADR-094, PROPOSED; the orchestrator accepts
	// under DL-089). Declared before the case code ran.
	expectedDigestO = "9381eebc213562073fa5cbafb57edaae2a7a9a5950cc4de0123434e0c0f61550"
	// expectedDigestI pins group I, the FHIR connector cases (healthcare phase 1; ADR-105, PROPOSED;
	// the orchestrator accepts under DL-107). Declared before any FHIR connector code exists; the
	// healthcare tickets call them F01-F12.
	expectedDigestI = "5916b959bccd6e860e3246f0534e7c29107ef93d9668649ca94d04908ba2a0dd"
	// expectedDigestY pins group Y, crypto agility: seal algorithm versioning and the ML-DSA-65
	// dual seal (TY-01, TY-02; ADR-109, PROPOSED; the orchestrator accepts under DL-107). Declared
	// before any deed v2 code existed.
	expectedDigestY = "206311510fa9be60f424a0f4c1e38f0322442a9c7303c1541b7efd107122f7e1"
	// expectedDigestT pins group T, this week's new surfaces: the A2A proxy, the Debezium witness,
	// the shell guard, head timestamps, the independent verifiers and connect (security hardening
	// after the 2026-10-21 audit, M-02 and M-03; ADR-116, PROPOSED; the orchestrator accepts under
	// DL-107). Declared before the A2A proxy's browser checks and the Debezium input cap existed.
	expectedDigestT = "d53ab26aea6fa7cf2897e1d081d7cd2217c2b1cad34ad5eb653eebe3bcaa12cf"
	// expectedDigestM pins group M, lockdown mode: the one switch for the strictest setup (ADR-117,
	// PROPOSED; the orchestrator accepts under DL-107). Declared before the Gate applied lockdown.
	expectedDigestM = "2c0a5749836dc379365bf8af244ea476f4ce0d49754d6268c2afbd97560bcbdd"
	// expectedDigestS pins group S, India's national profiles: ABDM records and NHCX bundles under
	// the india-health and healthcare-fhir packs (HC-P15; ADR-119, PROPOSED; the orchestrator
	// accepts under DL-107). Declared before the cases were implemented.
	expectedDigestS = "db28a9b44e60d7021e5bb1b727be95e8a5a182f5bffdb664369b10801fc8e33c"
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
	{"U", expectedDigestU},
	{"PQR", expectedDigestS34},
	{"O", expectedDigestO},
	{"I", expectedDigestI},
	{"Y", expectedDigestY},
	{"T", expectedDigestT},
	{"M", expectedDigestM},
	{"S", expectedDigestS},
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
	// S34, outside the digest: a V0 case's V7 successor, or why it no longer applies as an attack
	// case (with the test that checks the V7 control instead).
	SupersededBy string `json:"superseded_by,omitempty"`
	Retired      string `json:"retired,omitempty"`
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
	}{{"A", 13}, {"B", 7}, {"C", 10}, {"D", 17}, {"E", 11}, {"F", 4}, {"G", 4}, {"H", 11}, {"I", 12}, {"J", 4}, {"K", 4}, {"L", 8}, {"M", 14}, {"O", 2}, {"S", 12}, {"U", 5}, {"T", 13}, {"W", 13}, {"Y", 12}} {
		for i := 1; i <= g.n; i++ {
			ids = append(ids, fmt.Sprintf("%s%02d", g.p, i))
		}
	}
	// S34: P02–P08 and P10 (P01 is S22's L01 and P09 is S23's U01), Q01–Q08, R01–R07.
	for _, n := range []int{2, 3, 4, 5, 6, 7, 8, 10} {
		ids = append(ids, fmt.Sprintf("P%02d", n))
	}
	for i := 1; i <= 8; i++ {
		ids = append(ids, fmt.Sprintf("Q%02d", i))
	}
	for i := 1; i <= 7; i++ {
		ids = append(ids, fmt.Sprintf("R%02d", i))
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
		switch c.Status {
		case "not implemented", "implemented":
			if c.SupersededBy != "" || c.Retired != "" {
				t.Errorf("%s: status %q with superseded_by %q retired %q", c.ID, c.Status, c.SupersededBy, c.Retired)
			}
		case "superseded":
			// S34: a V0 case whose attack a V7 case runs; it must name that case.
			if !strings.Contains("ABCDEFG", c.Group) || c.SupersededBy == "" || c.Retired != "" {
				t.Errorf("%s: superseded needs a V0 case and superseded_by", c.ID)
			}
		case "retired":
			// S34: a V0 case that no longer applies to the V7 architecture, with the reason and the
			// test that checks the V7 control.
			if !strings.Contains("ABCDEFG", c.Group) || c.SupersededBy != "" || len(strings.TrimSpace(c.Retired)) < 20 {
				t.Errorf("%s: retired needs a V0 case and a reason", c.ID)
			}
		default:
			t.Errorf("%s: status %q", c.ID, c.Status)
		}
		for _, n := range c.Needs {
			if !reNeeds.MatchString(n) {
				t.Errorf("%s: needs %q is not a session S19 to S37", c.ID, n)
			}
		}
		if strings.Contains("PQR", c.Group) {
			// S34: only the confirmed OWASP mappings (C-006), declared from the session file.
			for _, o := range c.OWASP {
				if !reS34OWASP.MatchString(o) {
					t.Errorf("%s: owasp %q (S34 cases use only ASI01, ASI02, ASI03, ASI10 and LLM06)", c.ID, o)
				}
			}
			if c.Source != "prompts-v7/S34.md" || len(c.OWASP) == 0 || len(c.V3Ref) != 0 || len(c.ExpectedReasons) != 0 {
				t.Errorf("%s: source %q owasp %v v3_ref %v reasons %v", c.ID, c.Source, c.OWASP, c.V3Ref, c.ExpectedReasons)
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
	for _, c := range m.Cases {
		if c.SupersededBy != "" && !seen[c.SupersededBy] {
			t.Errorf("%s: superseded_by %q isn't a declared case", c.ID, c.SupersededBy)
		}
		if c.SupersededBy != "" && strings.Contains("ABCDEFG", c.SupersededBy[:1]) {
			t.Errorf("%s: superseded_by %q must be a V7 case", c.ID, c.SupersededBy)
		}
	}
	want := wantIDs()
	sort.Strings(want)
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
	reNeeds    = regexp.MustCompile(`^S(19|2[0-9]|3[0-7])$`)
	reV7OWASP  = regexp.MustCompile(`^ASI(01|02|03|10)$`)
	reS34OWASP = regexp.MustCompile(`^(ASI(01|02|03|10)|LLM06)$`)
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
