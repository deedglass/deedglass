package deed

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"
)

// Kind is what a deed records.
type Kind string

const (
	KindAction    Kind = "action"    // one agent action, sealed when it reaches a final state
	KindAdmin     Kind = "admin"     // one change a person or the system made (stop, invite, activate)
	KindAmendment Kind = "amendment" // a later witness verdict about an earlier action deed
)

// Version is deed format v1, which every builder writes and every issuer without a second seal
// key keeps sealing. VersionV2 (ADR-109) names its seals in the body (seal_keys) and carries one seal
// per key; the Sealer writes it only when it holds more than one key.
const (
	Version   = 1
	VersionV2 = 2
)

// Limits (DEED_SPEC.md §4). A deed holds digests, never payloads; these are the only free text.
const (
	MaxEnvelopeBytes   = 16 << 10 // JCS(envelope) of a v1 deed
	MaxEnvelopeBytesV2 = 24 << 10 // JCS(envelope) of a v2 deed (an ML-DSA-65 seal is 4,412 characters)
	MaxAgentName       = 64       // characters
	MaxToolBytes       = 200      // bytes (S13's qualified-name limit)
	MaxRefBytes        = 256      // on_behalf_of, session
	MaxMethod          = 64
	MaxErrorCode       = 64
	MaxRuleID          = 128
	MaxReason          = 280 // characters
)

// Body is Deed v1. Every key is always present (nil pointers encode as null). Never add omitempty:
// the closed schema depends on key presence.
type Body struct {
	V          int64             `json:"v"`            // always 1
	Kind       Kind              `json:"kind"`         //
	DeedID     string            `json:"deed_id"`      // deed_<ulid>
	Org        string            `json:"org"`          // org_<ulid>
	Seq        int64             `json:"seq"`          // 1.. per org, no gaps
	PrevHash   string            `json:"prev_hash"`    // sha256:<64 hex>
	IssuedAt   string            `json:"issued_at"`    // 2026-10-02T10:14:03.512Z (ms, UTC, "Z")
	Agent      *AgentRef         `json:"agent"`        //
	OnBehalfOf *string           `json:"on_behalf_of"` // opaque user ref from Deedglass-On-Behalf-Of
	Session    *string           `json:"session"`      // opaque session ref from Deedglass-Session
	Action     *ActionSection    `json:"action"`       //
	Policy     *PolicySection    `json:"policy"`       //
	Approval   *ApprovalSection  `json:"approval"`     //
	Execution  *ExecutionSection `json:"execution"`    //
	Witness    *WitnessSection   `json:"witness"`      //
	Cost       *Cost             `json:"cost"`         //
	Admin      *AdminSection     `json:"admin"`        //
	Amends     *string           `json:"amends"`       // deed_<ulid>, amendment only
	SealKeyID  string            `json:"seal_key_id"`  // sk_<ulid>; must equal Envelope.SealKeyID. v2: = SealKeys[0].ID, not serialized
	// SealKeys is v2 only: every key the deed carries a seal of, the primary first. A v1 body has
	// none (nil), and its JSON has no seal_keys member; a v2 body's JSON has seal_keys and no
	// seal_key_id (MarshalJSON).
	SealKeys []SealKeyRef `json:"seal_keys,omitempty"`
}

// SealKeyRef is one entry of a v2 body's seal_keys: an algorithm and the key that seals with it.
type SealKeyRef struct {
	Alg string `json:"alg"`
	ID  string `json:"id"`
}

// MarshalJSON writes a v2 body in its own shape (seal_keys, no seal_key_id), canonical. A v1 body
// marshals field by field as always.
func (b Body) MarshalJSON() ([]byte, error) {
	if b.V == VersionV2 {
		c, err := canonicalBody(&b)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	type plain Body
	return json.Marshal(plain(b))
}

// AgentRef names the agent. Name is informational; ID is what counts.
type AgentRef struct {
	ID   string `json:"id"`   // agt_…
	Name string `json:"name"` // 1–64 characters
}

// ActionSection describes the tool call. Arguments appear only as a digest.
type ActionSection struct {
	ID            string  `json:"id"`             // act_…
	Tool          string  `json:"tool"`           // "postgres.execute", ≤ 200 bytes
	Connector     *string `json:"connector"`      // con_… or null
	Mode          string  `json:"mode"`           // gateway | proxy
	RequestDigest string  `json:"request_digest"` // sha256:… of JCS(args)
	IntentHash    *string `json:"intent_hash"`    // sha256:… or null
	IntentSigned  bool    `json:"intent_signed"`  //
}

// PolicySection is the decision and the policy version that made it.
type PolicySection struct {
	ID          *string `json:"id"`           // pol_… (null when the org default decided)
	VersionID   *string `json:"version_id"`   // pv_…
	VersionHash *string `json:"version_hash"` // sha256:…
	Mode        string  `json:"mode"`         // enforce | shadow
	Decision    string  `json:"decision"`     // allow | deny | require_approval
	RuleID      *string `json:"rule_id"`      //
}

// ApprovalSection is the human decision (S22 fills it; the shape is fixed now).
type ApprovalSection struct {
	ID        string   `json:"id"`         // apr_…
	Outcome   string   `json:"outcome"`    // approved | denied | expired | cancelled
	DecidedBy []string `json:"decided_by"` // usr_…, sorted, never null (empty when expired)
	DecidedAt *string  `json:"decided_at"` //
	Deed      *string  `json:"deed"`       // the admin deed of the decision, if any
}

// ExecutionSection is what happened when the tool ran.
type ExecutionSection struct {
	Status       string  `json:"status"`        // succeeded | failed | unknown (contract §6)
	StartedAt    string  `json:"started_at"`    //
	FinishedAt   string  `json:"finished_at"`   //
	LatencyMS    int64   `json:"latency_ms"`    //
	ResultDigest *string `json:"result_digest"` //
	ErrorCode    *string `json:"error_code"`    // contract §8 codes, e.g. upstream_error
}

// WitnessSection is the Witness's verdict.
type WitnessSection struct {
	Level        string  `json:"level"`         // L0 | L1 | L2
	Verdict      string  `json:"verdict"`       // VERIFIED | MISMATCH | UNVERIFIABLE
	Method       string  `json:"method"`        // "response.schema", "readback.github.get_issue", "postgres.pgoutput"
	DetailDigest *string `json:"detail_digest"` // sha256:… of JCS(detail)
}

// Cost is money in integer minor units.
type Cost struct {
	Amount   int64  `json:"amount"`   // 0 ≤ amount ≤ 2^53−1
	Currency string `json:"currency"` // ISO 4217, lowercase
}

// Ref points at an actor or a target. Type matches ^[a-z][a-z_]{0,31}$ so later sessions can add
// kinds without breaking old verifiers. Known today: user, agent, api_key, system, org, membership,
// policy, policy_version, connector, approval, webhook, suggestion, anomaly, action. System actors
// use {system, deedglass} (jobs) or {system, operator} (database-direct CLI commands).
type Ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// AdminSection records an administrative change.
type AdminSection struct {
	Event      string  `json:"event"`       // adm_… (the admin_events row)
	Action     string  `json:"action"`      // "<object>.<past verb>": agent.stopped, member.invited
	Actor      Ref     `json:"actor"`       //
	Target     *Ref    `json:"target"`      //
	Reason     *string `json:"reason"`      // ≤ 280 characters
	DataDigest *string `json:"data_digest"` // sha256:… of JCS(event data)
}

// TimeFormat is the one time format inside a deed: RFC 3339, UTC, exactly 3 fractional digits.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// FormatTime renders t in TimeFormat (UTC, truncated to the millisecond).
func FormatTime(t time.Time) string { return t.UTC().Truncate(time.Millisecond).Format(TimeFormat) }

// ParseTime parses a deed time; anything not exactly TimeFormat is refused.
func ParseTime(s string) (time.Time, error) {
	if !reTime.MatchString(s) {
		return time.Time{}, fmt.Errorf("deed: time %q is not YYYY-MM-DDTHH:MM:SS.mmmZ", s)
	}
	t, err := time.Parse(TimeFormat, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("deed: time %q: %w", s, err)
	}
	return t, nil
}

// Patterns (DEED_SPEC.md §4).
var (
	reDigest    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	reTime      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
	reHeaderRef = regexp.MustCompile(`^[A-Za-z0-9._~:@/+=-]{1,256}$`) // the S09 Deedglass-On-Behalf-Of grammar
	reMethod    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	reErrorCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	reRuleID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	reAdminAct  = regexp.MustCompile(`^[a-z][a-z_]{0,30}\.[a-z][a-z_]{0,30}$`)
	reRefType   = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)
	reRefID     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	reCurrency  = regexp.MustCompile(`^[a-z]{3}$`)
	reID        = func() map[string]*regexp.Regexp {
		m := map[string]*regexp.Regexp{}
		for _, p := range idPrefixes {
			m[p] = regexp.MustCompile(IDPattern(p))
		}
		return m
	}()
)

// Id prefixes a deed refers to (contract §3). deed doesn't import internal/ids, which pulls in
// database/sql/driver; a test keeps IDPattern equal to ids.Pattern.
const (
	pDeed, pOrg, pAgent, pAction, pConnector, pPolicy = "deed", "org", "agt", "act", "con", "pol"
	pPolicyVersion, pApproval, pUser, pAdminEvent     = "pv", "apr", "usr", "adm"
	pAPIKey, pSealKey                                 = "key", "sk"
)

var idPrefixes = []string{pDeed, pOrg, pAgent, pAction, pConnector, pPolicy, pPolicyVersion, pApproval, pUser, pAdminEvent, pAPIKey, pSealKey}

// IDPattern is the contract §3 id pattern for prefix: "^<prefix>_[0-7][0-9a-hjkmnp-tv-z]{25}$".
func IDPattern(prefix string) string { return "^" + prefix + "_[0-7][0-9a-hjkmnp-tv-z]{25}$" }

func isID(prefix, s string) bool { return reID[prefix].MatchString(s) }

// Validate checks a complete body against the closed schema by kind (DEED_SPEC.md §5): enums,
// patterns, lengths and which sections must be null. The first failure wins, in field order.
func (b *Body) Validate() error { return b.validate(false) }

// validate with draft=true requires the Sealer-owned fields (deed_id, seq, prev_hash, issued_at,
// seal_key_id) to be unset instead of valid.
func (b *Body) validate(draft bool) error {
	if b.V != Version && b.V != VersionV2 {
		return errUnsupported(b.V)
	}
	switch b.Kind {
	case KindAction, KindAdmin, KindAmendment:
	default:
		return errSchemaField("kind", "must be action, admin or amendment")
	}
	if draft {
		switch {
		case b.DeedID != "":
			return errSchemaField("deed_id", "is set by the Sealer")
		case b.Seq != 0:
			return errSchemaField("seq", "is set by the Sealer")
		case b.PrevHash != "":
			return errSchemaField("prev_hash", "is set by the Sealer")
		case b.IssuedAt != "":
			return errSchemaField("issued_at", "is set by the Sealer")
		case b.SealKeyID != "":
			return errSchemaField("seal_key_id", "is set by the Sealer")
		case b.SealKeys != nil:
			return errSchemaField("seal_keys", "is set by the Sealer")
		}
	} else {
		if !isID(pDeed, b.DeedID) {
			return errSchemaField("deed_id", "must be a deed_ id")
		}
		if b.Seq < 1 || b.Seq > MaxSafeInteger {
			return errSchemaField("seq", "must be 1 to 2^53-1")
		}
		if !reDigest.MatchString(b.PrevHash) {
			return errSchemaField("prev_hash", "must be sha256:<64 hex>")
		}
		if _, err := ParseTime(b.IssuedAt); err != nil {
			return errSchemaField("issued_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
		}
		if b.V == Version {
			if !isID(pSealKey, b.SealKeyID) {
				return errSchemaField("seal_key_id", "must be an sk_ id")
			}
			if b.SealKeys != nil {
				return errSchemaField("seal_keys", "is not a v1 field")
			}
		} else if err := validateSealKeys(b.SealKeys, b.SealKeyID); err != nil {
			return err
		}
	}
	if !isID(pOrg, b.Org) {
		return errSchemaField("org", "must be an org_ id")
	}

	action, admin, amend := b.Kind == KindAction, b.Kind == KindAdmin, b.Kind == KindAmendment
	// agent: required for action and amendment, optional for admin.
	if b.Agent == nil {
		if !admin {
			return errSchemaField("agent", "is required for "+string(b.Kind)+" deeds")
		}
	} else if err := b.Agent.validate("agent"); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		v    *string
	}{{"on_behalf_of", b.OnBehalfOf}, {"session", b.Session}} {
		if f.v == nil {
			continue
		}
		if admin {
			return errSchemaField(f.name, "must be null in admin deeds")
		}
		if !reHeaderRef.MatchString(*f.v) {
			return errSchemaField(f.name, "must be 1-256 characters of A-Z a-z 0-9 . _ ~ : @ / + = -")
		}
	}
	// action: required for action and amendment.
	if b.Action == nil {
		if !admin {
			return errSchemaField("action", "is required for "+string(b.Kind)+" deeds")
		}
	} else {
		if admin {
			return errSchemaField("action", "must be null in admin deeds")
		}
		if err := b.Action.validate(); err != nil {
			return err
		}
	}
	// policy: action only, required.
	switch {
	case b.Policy == nil && action:
		return errSchemaField("policy", "is required for action deeds")
	case b.Policy != nil && !action:
		return errSchemaField("policy", "must be null in "+string(b.Kind)+" deeds")
	case b.Policy != nil:
		if err := b.Policy.validate(); err != nil {
			return err
		}
	}
	if b.Approval != nil {
		if !action {
			return errSchemaField("approval", "must be null in "+string(b.Kind)+" deeds")
		}
		if err := b.Approval.validate(); err != nil {
			return err
		}
	}
	if b.Execution != nil {
		if !action {
			return errSchemaField("execution", "must be null in "+string(b.Kind)+" deeds")
		}
		if err := b.Execution.validate(); err != nil {
			return err
		}
	}
	// witness: optional for action, required for amendment.
	switch {
	case b.Witness == nil && amend:
		return errSchemaField("witness", "is required for amendment deeds")
	case b.Witness != nil && admin:
		return errSchemaField("witness", "must be null in admin deeds")
	case b.Witness != nil:
		if err := b.Witness.validate(); err != nil {
			return err
		}
	}
	if b.Cost != nil {
		if !action {
			return errSchemaField("cost", "must be null in "+string(b.Kind)+" deeds")
		}
		if b.Cost.Amount < 0 || b.Cost.Amount > MaxSafeInteger {
			return errSchemaField("cost.amount", "must be 0 to 2^53-1 minor units")
		}
		if !reCurrency.MatchString(b.Cost.Currency) {
			return errSchemaField("cost.currency", "must be a lowercase ISO 4217 code")
		}
	}
	switch {
	case b.Admin == nil && admin:
		return errSchemaField("admin", "is required for admin deeds")
	case b.Admin != nil && !admin:
		return errSchemaField("admin", "must be null in "+string(b.Kind)+" deeds")
	case b.Admin != nil:
		if err := b.Admin.validate(); err != nil {
			return err
		}
	}
	switch {
	case b.Amends == nil && amend:
		return errSchemaField("amends", "is required for amendment deeds")
	case b.Amends != nil && !amend:
		return errSchemaField("amends", "must be null in "+string(b.Kind)+" deeds")
	case b.Amends != nil && !isID(pDeed, *b.Amends):
		return errSchemaField("amends", "must be a deed_ id")
	}
	return nil
}

func (a *AgentRef) validate(path string) error {
	if !isID(pAgent, a.ID) {
		return errSchemaField(path+".id", "must be an agt_ id")
	}
	if !plainText(a.Name, 1, MaxAgentName, false) {
		return errSchemaField(path+".name", "must be 1-64 characters of text")
	}
	return nil
}

func (a *ActionSection) validate() error {
	if !isID(pAction, a.ID) {
		return errSchemaField("action.id", "must be an act_ id")
	}
	if len(a.Tool) == 0 || len(a.Tool) > MaxToolBytes || !plainText(a.Tool, 1, MaxToolBytes, false) {
		return errSchemaField("action.tool", "must be 1-200 bytes of text")
	}
	if a.Connector != nil && !isID(pConnector, *a.Connector) {
		return errSchemaField("action.connector", "must be a con_ id or null")
	}
	if a.Mode != "gateway" && a.Mode != "proxy" {
		return errSchemaField("action.mode", "must be gateway or proxy")
	}
	if !reDigest.MatchString(a.RequestDigest) {
		return errSchemaField("action.request_digest", "must be sha256:<64 hex>")
	}
	if a.IntentHash != nil && !reDigest.MatchString(*a.IntentHash) {
		return errSchemaField("action.intent_hash", "must be sha256:<64 hex> or null")
	}
	if a.IntentSigned && a.IntentHash == nil {
		return errSchemaField("action.intent_hash", "is required when intent_signed is true")
	}
	return nil
}

func (p *PolicySection) validate() error {
	if p.ID != nil && !isID(pPolicy, *p.ID) {
		return errSchemaField("policy.id", "must be a pol_ id or null")
	}
	if p.VersionID != nil && !isID(pPolicyVersion, *p.VersionID) {
		return errSchemaField("policy.version_id", "must be a pv_ id or null")
	}
	if p.VersionHash != nil && !reDigest.MatchString(*p.VersionHash) {
		return errSchemaField("policy.version_hash", "must be sha256:<64 hex> or null")
	}
	if p.VersionID != nil && p.VersionHash == nil {
		return errSchemaField("policy.version_hash", "is required when version_id is set")
	}
	if p.Mode != "enforce" && p.Mode != "shadow" {
		return errSchemaField("policy.mode", "must be enforce or shadow")
	}
	switch p.Decision {
	case "allow", "deny", "require_approval":
	default:
		return errSchemaField("policy.decision", "must be allow, deny or require_approval")
	}
	if p.RuleID != nil && !reRuleID.MatchString(*p.RuleID) {
		return errSchemaField("policy.rule_id", "must be 1-128 characters of A-Z a-z 0-9 . _ : / -")
	}
	return nil
}

func (a *ApprovalSection) validate() error {
	if !isID(pApproval, a.ID) {
		return errSchemaField("approval.id", "must be an apr_ id")
	}
	switch a.Outcome {
	case "approved", "denied", "expired", "cancelled":
	default:
		return errSchemaField("approval.outcome", "must be approved, denied, expired or cancelled")
	}
	if a.DecidedBy == nil {
		return errSchemaField("approval.decided_by", "must be an array (empty when nobody decided)")
	}
	for i, u := range a.DecidedBy {
		if !isID(pUser, u) {
			return errSchemaField(fmt.Sprintf("approval.decided_by[%d]", i), "must be a usr_ id")
		}
		if i > 0 && a.DecidedBy[i-1] >= u {
			return errSchemaField(fmt.Sprintf("approval.decided_by[%d]", i), "must be sorted with no duplicates")
		}
	}
	if (a.Outcome == "approved" || a.Outcome == "denied") && len(a.DecidedBy) == 0 {
		return errSchemaField("approval.decided_by", "must name who decided")
	}
	if a.Outcome == "expired" && len(a.DecidedBy) != 0 {
		return errSchemaField("approval.decided_by", "must be empty when the approval expired")
	}
	if a.DecidedAt != nil {
		if _, err := ParseTime(*a.DecidedAt); err != nil {
			return errSchemaField("approval.decided_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ or null")
		}
	}
	if a.Deed != nil && !isID(pDeed, *a.Deed) {
		return errSchemaField("approval.deed", "must be a deed_ id or null")
	}
	return nil
}

func (e *ExecutionSection) validate() error {
	switch e.Status {
	case "succeeded", "failed", "unknown":
	default:
		return errSchemaField("execution.status", "must be succeeded, failed or unknown")
	}
	start, err := ParseTime(e.StartedAt)
	if err != nil {
		return errSchemaField("execution.started_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
	}
	end, err := ParseTime(e.FinishedAt)
	if err != nil {
		return errSchemaField("execution.finished_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
	}
	if end.Before(start) {
		return errSchemaField("execution.finished_at", "must not be before started_at")
	}
	if e.LatencyMS < 0 || e.LatencyMS > MaxSafeInteger {
		return errSchemaField("execution.latency_ms", "must be 0 to 2^53-1")
	}
	if e.ResultDigest != nil && !reDigest.MatchString(*e.ResultDigest) {
		return errSchemaField("execution.result_digest", "must be sha256:<64 hex> or null")
	}
	if e.ErrorCode != nil && !reErrorCode.MatchString(*e.ErrorCode) {
		return errSchemaField("execution.error_code", "must be a snake_case code of at most 64 characters, or null")
	}
	return nil
}

func (w *WitnessSection) validate() error {
	if !slices.Contains([]string{"L0", "L1", "L2"}, w.Level) {
		return errSchemaField("witness.level", "must be L0, L1 or L2")
	}
	if !slices.Contains([]string{"VERIFIED", "MISMATCH", "UNVERIFIABLE"}, w.Verdict) {
		return errSchemaField("witness.verdict", "must be VERIFIED, MISMATCH or UNVERIFIABLE")
	}
	if !reMethod.MatchString(w.Method) {
		return errSchemaField("witness.method", "must be 1-64 characters of a-z 0-9 . _ -")
	}
	if w.DetailDigest != nil && !reDigest.MatchString(*w.DetailDigest) {
		return errSchemaField("witness.detail_digest", "must be sha256:<64 hex> or null")
	}
	return nil
}

func (a *AdminSection) validate() error {
	if !isID(pAdminEvent, a.Event) {
		return errSchemaField("admin.event", "must be an adm_ id")
	}
	if !reAdminAct.MatchString(a.Action) {
		return errSchemaField("admin.action", "must be <object>.<past verb>, for example agent.stopped")
	}
	if err := a.Actor.validate("admin.actor"); err != nil {
		return err
	}
	if a.Target != nil {
		if err := a.Target.validate("admin.target"); err != nil {
			return err
		}
	}
	if a.Reason != nil && !plainText(*a.Reason, 1, MaxReason, true) {
		return errSchemaField("admin.reason", "must be 1-280 characters of text or null")
	}
	if a.DataDigest != nil && !reDigest.MatchString(*a.DataDigest) {
		return errSchemaField("admin.data_digest", "must be sha256:<64 hex> or null")
	}
	return nil
}

func (r *Ref) validate(path string) error {
	if !reRefType.MatchString(r.Type) {
		return errSchemaField(path+".type", "must match ^[a-z][a-z_]{0,31}$")
	}
	if !reRefID.MatchString(r.ID) {
		return errSchemaField(path+".id", "must be an id or a lowercase name of at most 64 characters")
	}
	return nil
}

// plainText reports whether s is valid UTF-8 of min..max characters with no control characters
// (newline allowed only when newline is true).
func plainText(s string, minChars, maxChars int, newline bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	n := 0
	for _, r := range s {
		n++
		if r < 0x20 && !(newline && r == '\n') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return n >= minChars && n <= maxChars
}

// validateSealKeys checks a v2 body's seal_keys: 1 to MaxSeals entries, each a well-formed alg
// name and an sk_ id, no alg and no key twice. primary (Body.SealKeyID) must be the first id.
func validateSealKeys(sk []SealKeyRef, primary string) error {
	if len(sk) < 1 || len(sk) > MaxSeals {
		return errSchemaField("seal_keys", fmt.Sprintf("must hold 1 to %d keys", MaxSeals))
	}
	for i, k := range sk {
		if !reAlg.MatchString(k.Alg) {
			return errSchemaField(fmt.Sprintf("seal_keys[%d].alg", i), "must match ^[A-Za-z0-9][A-Za-z0-9-]{0,31}$")
		}
		if !isID(pSealKey, k.ID) {
			return errSchemaField(fmt.Sprintf("seal_keys[%d].id", i), "must be an sk_ id")
		}
		for _, p := range sk[:i] {
			if p.Alg == k.Alg {
				return errSchemaField(fmt.Sprintf("seal_keys[%d].alg", i), "names an algorithm twice")
			}
			if p.ID == k.ID {
				return errSchemaField(fmt.Sprintf("seal_keys[%d].id", i), "names a key twice")
			}
		}
	}
	if primary != sk[0].ID {
		return errSchemaField("seal_keys", "the first key must be the primary seal key")
	}
	return nil
}
