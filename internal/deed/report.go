package deed

import (
	"sort"
	"strconv"
)

// Report results.
const (
	ResultValid           = "valid"
	ResultInvalid         = "invalid"
	ResultCannotDetermine = "cannot_determine"
)

// Chain statuses.
const (
	ChainIntact     = "intact"
	ChainBroken     = "broken"
	ChainNotChecked = "not_checked"
)

// MaxFailures bounds Report.Failures (verify --all).
const MaxFailures = 100

// Report is a verification_report: what `deedglass verify` prints with --json, what `ledger check`
// (S06) returns, and what the browser verifier builds with the same field names.
type Report struct {
	Object     string           `json:"object"` // "verification_report"
	Result     string           `json:"result"` // valid | invalid | cannot_determine
	Deeds      int64            `json:"deeds"`  // deeds read (stops at the first failure without --all)
	Intact     int64            `json:"intact"` // deeds that passed every check
	Org        *string          `json:"org"`
	FirstSeq   *int64           `json:"first_seq"`
	LastSeq    *int64           `json:"last_seq"`
	From       *string          `json:"from"` // issued_at of the first intact deed
	To         *string          `json:"to"`   // issued_at of the last intact deed
	Chain      ChainStatus      `json:"chain"`
	SealKeys   []KeyUse         `json:"seal_keys"`
	KeysPinned bool             `json:"keys_pinned"`
	Kinds      map[string]int64 `json:"kinds"`             // action, admin, amendment
	Recorded   map[string]int64 `json:"verdicts_recorded"` // VERIFIED, MISMATCH, UNVERIFIABLE, none: as written in the deeds
	Bundle     *BundleStatus    `json:"bundle"`
	Failures   []Failure        `json:"failures"`
	Warnings   []Warning        `json:"warnings"`
	DurationMS int64            `json:"duration_ms"`
	Verifier   string           `json:"verifier"`
}

// ChainStatus says whether the deeds were checked as a chain, and from where.
type ChainStatus struct {
	Status string      `json:"status"` // intact | broken | not_checked
	Anchor ChainAnchor `json:"anchor"`
}

// ChainAnchor is what the first deed links to: the org's genesis value, a deed the reader already
// holds (--anchor, or a partial export), or nothing (loose deeds).
type ChainAnchor struct {
	Kind     string  `json:"kind"` // genesis | deed | none
	Seq      *int64  `json:"seq"`
	BodyHash *string `json:"body_hash"`
}

// KeyUse is one seal key and the deeds it sealed.
type KeyUse struct {
	ID         string `json:"id"`
	Thumbprint string `json:"thumbprint"`
	Deeds      int64  `json:"deeds"`
	FirstSeq   int64  `json:"first_seq"`
	LastSeq    int64  `json:"last_seq"`
	Source     string `json:"source"` // the file or URL the key came from, or "bundle"
}

// BundleStatus describes an export bundle's manifest checks.
type BundleStatus struct {
	Manifest     string  `json:"manifest"`      // ok | mismatch
	ManifestSeal string  `json:"manifest_seal"` // ok | missing | invalid
	SealedBy     *string `json:"sealed_by"`     // the manifest seal's key id
	Files        int     `json:"files"`
	Input        string  `json:"input"`
}

// Failure is one failed check. Message and Next are the copy from messages.go.
type Failure struct {
	Code     string  `json:"code"`
	Seq      *int64  `json:"seq"`
	Deed     *string `json:"deed"`
	Input    *string `json:"input"` // the file it came from ("-" for stdin)
	Line     *int64  `json:"line"`
	Field    *string `json:"field"`
	Expected *string `json:"expected"` // body_hash_mismatch: the sealed body_hash
	Computed *string `json:"computed"` // body_hash_mismatch: sha256(JCS(body))
	Message  string  `json:"message"`
	Next     string  `json:"next"`
}

// Warning is something the reader should know that does not fail verification.
type Warning struct {
	Code    string `json:"code"` // keys_not_pinned | manifest_unsealed | test_keys | …
	Message string `json:"message"`
	Next    string `json:"next"`
}

// Warning codes.
const (
	WarnKeysNotPinned    = "keys_not_pinned"
	WarnManifestUnsealed = "manifest_unsealed"
	WarnTestKeys         = "test_keys"
)

// CannotDetermine reports whether code means "cannot decide" rather than "failed": the verifier
// had no keys, or the deed uses a newer format. Exit 2 instead of 1.
func CannotDetermine(code string) bool {
	return code == string(CodeKeysMissing) || code == string(CodeUnsupportedVersion)
}

// NewReport returns an empty report with every list and map non-nil (JSON shows [] and {}).
func NewReport() *Report {
	return &Report{Object: "verification_report", Result: ResultValid,
		Chain:    ChainStatus{Status: ChainNotChecked, Anchor: ChainAnchor{Kind: "none"}},
		SealKeys: []KeyUse{}, Kinds: map[string]int64{}, Recorded: map[string]int64{},
		Failures: []Failure{}, Warnings: []Warning{}}
}

// FailureFrom turns a verification error into a Failure with its copy.
func FailureFrom(e *VerifyError) Failure {
	msg, next := Message(e)
	f := Failure{Code: string(e.Code), Message: msg, Next: next}
	if e.Seq > 0 {
		f.Seq = ptr(e.Seq)
	}
	if e.DeedID != "" {
		f.Deed = ptr(e.DeedID)
	}
	if e.Field != "" {
		f.Field = ptr(e.Field)
	}
	return f
}

// AddFailure records f (at most MaxFailures) and sets the result.
func (r *Report) AddFailure(f Failure) {
	if len(r.Failures) < MaxFailures {
		r.Failures = append(r.Failures, f)
	}
	r.settle()
}

// AddWarning records w once per code.
func (r *Report) AddWarning(code, message, next string) {
	for _, w := range r.Warnings {
		if w.Code == code {
			return
		}
	}
	r.Warnings = append(r.Warnings, Warning{Code: code, Message: message, Next: next})
}

// settle derives Result and the chain status from the failures.
func (r *Report) settle() {
	r.Result = ResultValid
	for _, f := range r.Failures {
		if CannotDetermine(f.Code) {
			if r.Result == ResultValid {
				r.Result = ResultCannotDetermine
			}
			continue
		}
		r.Result = ResultInvalid
	}
	if r.Result == ResultInvalid && r.Chain.Status == ChainIntact {
		r.Chain.Status = ChainBroken
	}
}

// ExitCode is the contract §10 exit code for the report: 0 valid, 1 invalid, 2 cannot determine.
func (r *Report) ExitCode() int {
	switch r.Result {
	case ResultValid:
		return 0
	case ResultInvalid:
		return 1
	}
	return 2
}

// FirstFailure returns the first failure in seq order, or nil.
func (r *Report) FirstFailure() *Failure {
	if len(r.Failures) == 0 {
		return nil
	}
	return &r.Failures[0]
}

// sortFailures orders failures by input order (line), which is seq order for a chain.
func (r *Report) sortFailures() {
	sort.SliceStable(r.Failures, func(i, j int) bool {
		a, b := r.Failures[i], r.Failures[j]
		ai, bi := deref(a.Input), deref(b.Input)
		if ai != bi {
			return false // keep input order across files
		}
		return deref64(a.Line) < deref64(b.Line)
	})
}

// keyTally accumulates KeyUse rows in first-use order.
type keyTally struct {
	order []string
	rows  map[string]*KeyUse
}

func (t *keyTally) add(id, thumbprint, source string, seq int64) {
	if t.rows == nil {
		t.rows = map[string]*KeyUse{}
	}
	k, ok := t.rows[id]
	if !ok {
		k = &KeyUse{ID: id, Thumbprint: thumbprint, FirstSeq: seq, LastSeq: seq, Source: source}
		t.rows[id] = k
		t.order = append(t.order, id)
	}
	k.Deeds++
	k.FirstSeq = min(k.FirstSeq, seq)
	k.LastSeq = max(k.LastSeq, seq)
}

func (t *keyTally) list() []KeyUse {
	out := make([]KeyUse, 0, len(t.order))
	for _, id := range t.order {
		out = append(out, *t.rows[id])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].FirstSeq < out[j].FirstSeq })
	return out
}

func ptr[T any](v T) *T { return &v }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func deref64(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

// FormatCount writes n with thousands separators: 1,204.
func FormatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := n < 0
	if neg {
		s = s[1:]
	}
	var b []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, c)
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
