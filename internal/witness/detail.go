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

// LevelDetail is one level that ran.
type LevelDetail struct {
	Verdict  Verdict         `json:"verdict"`
	Method   string          `json:"method"`
	Readback *ReadbackDetail `json:"readback,omitempty"`
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
			det.Levels.L2 = ld
		}
	}
	b, err := canonical(det)
	if err != nil {
		return Outcome{}, err
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
