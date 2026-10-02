package witness

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/gowebpki/jcs"

	"github.com/deedglass/deedglass/internal/deed"
)

// DetailVersion is witness_detail's version. From S19 on, changes to the shape are additive only:
// S21 fills levels.L2, and S25's Witness panel renders this shape.
const DetailVersion = 1

// DefaultMaxDetailBytes is witness.max_detail_bytes: the detail's JCS size cap.
const DefaultMaxDetailBytes = 64 << 10

// maxDiffsKept is how many field diffs a reason keeps when the detail is over its cap.
const maxDiffsKept = 8

// Claim is the verdict the deed carries.
type Claim struct {
	Level         Level   `json:"level"`
	Verdict       Verdict `json:"verdict"`
	Method        string  `json:"method"`
	RequiredLevel Level   `json:"required_level"`
}

// LevelDetail is one level that ran. L2 adds its evidence inline (S21 §3.7): the transaction,
// the expected footprint, the observed counts and the row diff.
type LevelDetail struct {
	Verdict  Verdict         `json:"verdict"`
	Method   string          `json:"method"`
	Readback *ReadbackDetail `json:"readback,omitempty"`
	*L2Evidence
}

// L2Evidence is what levels.L2 holds beyond its verdict and method (S21 §3.7). Every L2 verdict
// that read a frame names its transaction (system_id, timeline, commit_lsn); marker_lsn is
// evidence only and is never compared. Values are Postgres's text output; a sensitive column is
// "•••" and listed in redacted.
type L2Evidence struct {
	Transaction *L2Transaction `json:"transaction,omitempty"`
	Expected    *L2Expected    `json:"expected,omitempty"`
	Observed    *L2Observed    `json:"observed,omitempty"`
	Rows        []L2Row        `json:"rows"`
	RowsShown   int            `json:"rows_shown"`
	RowsMore    int            `json:"rows_more"`
	ColumnsMore int            `json:"columns_more"`
	Redacted    []string       `json:"redacted"`
	// Frames is how many transactions in the change log named the action (more than one is
	// multiple_transactions); FrameIDs are their decoded_txs ids, so a later look at the same
	// evidence is recognised and changes nothing.
	Frames   int      `json:"frames"`
	FrameIDs []string `json:"frame_ids,omitempty"`
}

// L2Transaction is the committed transaction the verdict is about.
type L2Transaction struct {
	SystemID      string `json:"system_id"`
	Timeline      int    `json:"timeline"`
	CommitLSN     string `json:"commit_lsn"`
	Xid           int64  `json:"xid"`
	CommittedAt   string `json:"committed_at"`
	MarkerLSN     string `json:"marker_lsn,omitempty"`
	ChangesDigest string `json:"changes_digest,omitempty"`
}

// L2Expected is the footprint as derived from the statement (l2.Derive).
type L2Expected struct {
	Derivation string            `json:"derivation"` // exact_keys | count_only | contract | explicit | unknown
	Why        string            `json:"why,omitempty"`
	Tables     []L2ExpectedTable `json:"tables"`
}

// L2ExpectedTable is one table of the footprint.
type L2ExpectedTable struct {
	Table   string              `json:"table"`
	Ops     []string            `json:"ops"`
	Rows    *int64              `json:"rows,omitempty"`
	MaxRows *int64              `json:"max_rows,omitempty"`
	Keys    []map[string]string `json:"keys,omitempty"`
	Columns []string            `json:"columns,omitempty"`
}

// L2Observed is what the transaction changed, per table, counted from the WAL (never estimated).
type L2Observed struct {
	Total  int               `json:"total"`
	Tables []L2ObservedTable `json:"tables"`
}

// L2ObservedTable is one table's exact counts.
type L2ObservedTable struct {
	Table    string `json:"table"`
	Insert   int    `json:"insert"`
	Update   int    `json:"update"`
	Delete   int    `json:"delete"`
	Truncate int    `json:"truncate,omitempty"`
}

// L2Row is one changed row in the diff: expected rows first, then unexpected ones in stream order.
type L2Row struct {
	Table    string            `json:"table"`
	Op       string            `json:"op"` // insert | update | delete
	Key      map[string]string `json:"key"`
	Expected bool              `json:"expected"`
	Cells    []L2Cell          `json:"cells"`
}

// L2Cell is one changed column: its value before and after, as Postgres prints it (null for SQL
// NULL or an image Postgres didn't send).
type L2Cell struct {
	Column string  `json:"column"`
	Before *string `json:"before"`
	After  *string `json:"after"`
}

// Extras is what Finish adds to the levels beyond their verdicts: L1's read-back and L2's evidence.
type Extras struct {
	Readback *ReadbackDetail
	L2       *L2Evidence
}

// LevelsDetail holds the three levels; a level that didn't run is null.
type LevelsDetail struct {
	L0 *LevelDetail `json:"L0"`
	L1 *LevelDetail `json:"L1"`
	L2 *LevelDetail `json:"L2"`
}

// Detail is witness_detail v1: what actions.witness_detail holds, and what the deed's
// witness.detail_digest is the digest of.
type Detail struct {
	Object      string       `json:"object"` // "witness_detail"
	Version     int          `json:"version"`
	Claim       Claim        `json:"claim"`
	Summary     string       `json:"summary"`
	Levels      LevelsDetail `json:"levels"`
	Reasons     []Reason     `json:"reasons"`
	WitnessedAt string       `json:"witnessed_at"`
	Truncated   int          `json:"truncated,omitempty"` // diff rows and evidence left out to stay under the cap
}

// Outcome is what the Gate records: the decision, its detail, the detail's bytes and its digest.
type Outcome struct {
	Decision Decision
	Detail   Detail
	JSON     json.RawMessage // JCS(Detail)
	Digest   string          // "sha256:" + hex(sha256(JCS(Detail)))
}

// Flags are the claim-level codes, for the action.witnessed event.
func (o Outcome) Flags() []string { return o.Decision.Flags() }

// Finish builds the detail of a decision: the summary, the levels that ran with their methods, and
// the reasons, cut to maxBytes (JCS) by dropping diff rows and evidence first.
func Finish(d Decision, methods map[Level]string, rb *ReadbackDetail, at time.Time, maxBytes int) (Outcome, error) {
	return FinishWith(d, methods, Extras{Readback: rb}, at, maxBytes)
}

// FinishWith is Finish with every level's extras (S21: L2's evidence). When the detail is over
// maxBytes, L2's rows are cut first (rows_more counts them), then diff rows and evidence.
func FinishWith(d Decision, methods map[Level]string, x Extras, at time.Time, maxBytes int) (Outcome, error) {
	rb := x.Readback
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDetailBytes
	}
	det := Detail{Object: "witness_detail", Version: DetailVersion,
		Claim:   Claim{Level: d.Level, Verdict: d.Verdict, Method: d.Method, RequiredLevel: d.Required},
		Summary: Summary(d), Reasons: d.Reasons, WitnessedAt: at.UTC().Format("2006-01-02T15:04:05.000Z")}
	for _, l := range Levels {
		ld := levelDetail(d, l, methods)
		if ld != nil && l == L1 {
			ld.Readback = rb
		}
		switch l {
		case L0:
			det.Levels.L0 = ld
		case L1:
			det.Levels.L1 = ld
		case L2:
			if ld == nil && x.L2 != nil && x.L2.Transaction != nil {
				// L2 read a frame but every L2 reason was a note: the level still names its evidence.
				m := methods[L2]
				if m == "" {
					m = defaultMethod(L2)
				}
				ld = &LevelDetail{Verdict: Unverifiable, Method: m}
			}
			if ld != nil && x.L2 != nil {
				ev := *x.L2
				if ev.Rows == nil {
					ev.Rows = []L2Row{}
				}
				if ev.Redacted == nil {
					ev.Redacted = []string{}
				}
				ld.L2Evidence = &ev
			}
			det.Levels.L2 = ld
		}
	}
	b, err := canonical(det)
	if err != nil {
		return Outcome{}, err
	}
	if len(b) > maxBytes && det.Levels.L2 != nil && det.Levels.L2.L2Evidence != nil {
		det, b, err = shrinkL2(det, maxBytes)
		if err != nil {
			return Outcome{}, err
		}
	}
	if len(b) > maxBytes {
		det, b, err = shrink(det, maxBytes)
		if err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Decision: d, Detail: det, JSON: b, Digest: deed.Digest(b)}, nil
}

// levelDetail is a level that ran: its own Precedence and method; nil when it produced no reason.
func levelDetail(d Decision, l Level, methods map[Level]string) *LevelDetail {
	var rs []Reason
	for _, r := range d.Reasons {
		if r.Level == l && r.Code != CodeLevelBelowRequired && r.Code != CodeNoChecks {
			rs = append(rs, r)
		}
	}
	counted := false
	for _, r := range rs {
		counted = counted || r.Class != Note
	}
	if !counted {
		return nil // a level with only notes (readback_skipped) didn't run a check
	}
	m := methods[l]
	if m == "" {
		m = defaultMethod(l)
	}
	return &LevelDetail{Verdict: Precedence(rs), Method: m}
}

func canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return jcs.Transform(bytes.TrimSpace(buf.Bytes()))
}

// shrinkL2 halves L2's shown rows until the detail fits (or none are left); rows_more counts what
// was cut, and Truncated says rows were left out.
func shrinkL2(det Detail, maxBytes int) (Detail, []byte, error) {
	l2 := *det.Levels.L2
	ev := *l2.L2Evidence
	l2.L2Evidence = &ev
	det.Levels.L2 = &l2
	for {
		b, err := canonical(det)
		if err != nil || len(b) <= maxBytes || len(ev.Rows) == 0 {
			return det, b, err
		}
		keep := len(ev.Rows) / 2
		cut := len(ev.Rows) - keep
		ev.Rows = ev.Rows[:keep]
		ev.RowsShown -= cut
		ev.RowsMore += cut
		det.Truncated += cut
	}
}

// shrink cuts diff rows, then evidence, until the detail fits; the count of what was cut is kept.
func shrink(det Detail, maxBytes int) (Detail, []byte, error) {
	rs := make([]Reason, len(det.Reasons))
	copy(rs, det.Reasons)
	for i := range rs {
		if len(rs[i].Diff) > maxDiffsKept {
			det.Truncated += len(rs[i].Diff) - maxDiffsKept
			rs[i].Diff = rs[i].Diff[:maxDiffsKept]
		}
	}
	det.Reasons = rs
	b, err := canonical(det)
	if err != nil || len(b) <= maxBytes {
		return det, b, err
	}
	for i := range rs {
		if rs[i].Evidence != nil {
			det.Truncated++
			rs[i].Evidence = nil
		}
		if len(rs[i].Diff) > 0 {
			det.Truncated += len(rs[i].Diff)
			rs[i].Diff = nil
		}
	}
	b, err = canonical(det)
	return det, b, err
}

// ParseDetail reads a stored witness_detail. S13's earlier shape ({"reasons": ["outcome_unknown"]})
// is read too: each code becomes a reason from the catalog (an unknown code becomes a gap with its
// code kept). ok is false when the bytes are neither.
func ParseDetail(b []byte) (Detail, bool) {
	if len(bytes.TrimSpace(b)) == 0 {
		return Detail{}, false
	}
	var probe struct {
		Object  string          `json:"object"`
		Reasons json.RawMessage `json:"reasons"`
	}
	if json.Unmarshal(b, &probe) != nil {
		return Detail{}, false
	}
	if probe.Object == "witness_detail" {
		var d Detail
		if json.Unmarshal(b, &d) != nil {
			return Detail{}, false
		}
		if d.Reasons == nil {
			d.Reasons = []Reason{}
		}
		return d, true
	}
	var codes []string
	if len(probe.Reasons) > 0 && json.Unmarshal(probe.Reasons, &codes) != nil {
		return Detail{}, false
	}
	d := Detail{Reasons: []Reason{}}
	for _, c := range codes {
		d.Reasons = append(d.Reasons, legacyReason(c))
	}
	return d, true
}

// s13Causes are S13's codes for an unknown outcome; S19 records them as outcome_unknown with the
// cause kept in the evidence, which the Gate reads to say what happened.
var s13Causes = map[string]bool{"timeout": true, "edge_lost": true, "client_cancelled": true, "outcome_unknown": true}

// legacyReason is an S13 reason code as a reason: the gate's short codes were all L0 gaps.
func legacyReason(code string) Reason {
	if s13Causes[code] {
		return R(L0, CodeOutcomeUnknown, "status", map[string]any{"cause": code}).WithEvidence(map[string]any{"cause": code})
	}
	if e, ok := catalog[Code(code)]; ok {
		vars := map[string]any{"cause": code}
		if e.Code == CodeWitnessGaveUp {
			vars = map[string]any{"attempts": "the maximum", "last_error": "see the server log"}
		}
		return Reason{Code: e.Code, Class: e.Class, Level: L0, Check: "status", Message: render(e.Template, vars)}
	}
	return Reason{Code: Code(code), Class: Gap, Level: L0, Check: "status",
		Message: render(catalog[CodeOutcomeUnknown].Template, map[string]any{"cause": code})}
}
