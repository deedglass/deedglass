// Package harness runs Deedglass's V7 attack cases (S18) against a real `deedglass serve`
// process, a real Postgres and loopback upstreams, and checks the universal invariants after
// every case: the deed chains are intact, the target is unchanged when nothing should have run,
// every action is terminal, no secret appears anywhere, and forbidden listeners saw no traffic.
//
// Expected outcomes come only from attack/attack_meta.json. A Case has no expected field, so Go
// code can't move the goalposts: the runner compares what the case observed with the meta.
//
// The harness runs only on a throwaway cluster (DEEDGLASS_IT_THROWAWAY=1, DEEDGLASS_IT_ADMIN_URL),
// started by make attack. It never touches any other database.
package harness

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
)

// Meta is attack_meta.json, as much of it as the runner needs.
type Meta struct {
	Outcomes map[string]string `json:"outcomes"`
	Cases    []MetaCase        `json:"cases"`
}

// MetaCase is one declared case.
type MetaCase struct {
	ID             string   `json:"id"`
	Group          string   `json:"group"`
	Title          string   `json:"title"`
	Expected       string   `json:"expected"`
	ExpectedDetail string   `json:"expected_detail"`
	Priority       string   `json:"priority"`
	Demo           bool     `json:"demo"`
	OWASP          []string `json:"owasp"`
	Status         string   `json:"status"`
	Needs          []string `json:"needs,omitempty"`
	SupersededBy   string   `json:"superseded_by,omitempty"` // S34: a V0 case's V7 successor
	Retired        string   `json:"retired,omitempty"`       // S34: why a V0 case no longer applies
}

// LoadMeta reads the meta file.
func LoadMeta(path string) (*Meta, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// V7Groups are the groups make attack runs (S18 H, J, K; S21 W; S22 L; S23 U; S34 P, Q, R; S50 O;
// HC-P02 I, the FHIR connector).
const V7Groups = "HJKLUWPQROI"

// Case returns the declared case id, or nil.
func (m *Meta) Case(id string) *MetaCase {
	for i := range m.Cases {
		if m.Cases[i].ID == id {
			return &m.Cases[i]
		}
	}
	return nil
}

// Status of a case run.
const (
	StatusMatched = "matched"
	StatusDiffers = "differs"
	StatusWaiting = "waiting"
)

// Result is one case's row in latest.json.
type Result struct {
	ID       string         `json:"id"`
	Title    string         `json:"title"`
	Expected string         `json:"expected"`
	Actual   string         `json:"actual"`
	Detail   string         `json:"detail"`
	Status   string         `json:"status"`
	Waiting  string         `json:"waiting_on,omitempty"`
	Line     string         `json:"line,omitempty"`
	Problems []string       `json:"problems,omitempty"`
	MS       int64          `json:"ms"`
	Evidence map[string]any `json:"evidence,omitempty"`
}

// Run is one whole attack run: latest.json.
type Run struct {
	Object     string     `json:"object"` // "attack_run"
	StartedAt  string     `json:"started_at"`
	Commit     string     `json:"commit"`
	Version    string     `json:"version,omitempty"` // S35: the release tag the run belongs to (DEEDGLASS_IT_ATTACK_VERSION in release.yml)
	Postgres   string     `json:"postgres"`
	BootMS     int64      `json:"boot_ms"`
	Cases      []Result   `json:"cases"`
	Summary    Summary    `json:"summary"`
	Invariants Invariants `json:"invariants"`
}

// Summary counts the statuses.
type Summary struct {
	Matched int `json:"matched"`
	Differs int `json:"differs"`
	Waiting int `json:"waiting"`
}

// Invariants is the run-wide summary of the universal invariants.
type Invariants struct {
	Chains      string `json:"chains"` // intact | broken
	SecretsSeen int    `json:"secrets_seen"`
	LogLines    int    `json:"log_lines"`
	RowsScanned int    `json:"rows_scanned"`
	BodiesSeen  int    `json:"bodies_seen"`
}

// DetailMatches reports whether an observed detail head matches the declared detail: the
// declared text starts with it (S18 §3.3).
func DetailMatches(declared, observed string) bool {
	return observed != "" && strings.HasPrefix(declared, observed)
}

// Summarize counts the statuses.
func Summarize(rs []Result) Summary {
	var s Summary
	for _, r := range rs {
		switch r.Status {
		case StatusMatched:
			s.Matched++
		case StatusDiffers:
			s.Differs++
		case StatusWaiting:
			s.Waiting++
		}
	}
	return s
}

// WriteRun writes latest.json (indented, a trailing newline).
func WriteRun(path string, run Run) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(run); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// ReadRun reads latest.json.
func ReadRun(path string) (*Run, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
