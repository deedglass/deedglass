package learn

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// Suggestions (S28): Deedglass proposes; people decide. A suggest pass reads fourteen days of
// decisions, runs the six generators, patches the enforced policy's text, compiles the result,
// replays it against the same fourteen days, drops anything its guard refuses, and stores what is
// left as open suggestions. Nothing here changes a policy: accepting (lifecycle.go) creates a
// version and runs it in shadow, and only a person promotes it to enforce.

// Suggestion statuses.
const (
	SugOpen       = "open"
	SugAccepting  = "accepting"
	SugAccepted   = "accepted"
	SugRejected   = "rejected"
	SugSuperseded = "superseded"
)

// Pass limits.
const (
	MaxOpenSuggestions = 10
	RejectionMemory    = 30 * 24 * time.Hour
	DiffContext        = 3
)

// Suggestion is the suggestion object.
type Suggestion struct {
	Object           string              `json:"object"` // suggestion
	ID               string              `json:"id"`
	Status           string              `json:"status"`
	Kind             string              `json:"kind"`
	Generator        string              `json:"generator"`
	Title            string              `json:"title"`
	Rationale        string              `json:"rationale"`
	Phrasing         Phrasing            `json:"phrasing"`
	Policy           SuggestionPolicy    `json:"policy"`
	TargetRuleID     *string             `json:"target_rule_id"`
	Evidence         json.RawMessage     `json:"evidence"`
	EvidenceDigest   string              `json:"evidence_digest"`
	Diff             *policy.Unified     `json:"diff"`
	Warnings         []policy.Diagnostic `json:"warnings"`
	Replay           *SuggestionReplay   `json:"replay"`
	RiskNote         string              `json:"risk_note"`
	Impact           int                 `json:"impact"`
	StaleReason      *string             `json:"stale_reason"`
	CreatedAt        time.Time           `json:"created_at"`
	DecidedBy        *Actor              `json:"decided_by"`
	DecidedAt        *time.Time          `json:"decided_at"`
	DecisionReason   *string             `json:"decision_reason"`
	ResultingVersion *VersionBrief       `json:"resulting_version"`
	DecisionDeed     *string             `json:"decision_deed"`
	// On an accept's answer only: the deeds sealed (policy.activated, suggestion.accepted), what
	// the shadow replaced, and a note when the source already existed as a version.
	Deeds          []string `json:"deeds,omitempty"`
	ReplacedShadow *int     `json:"replaced_shadow_version,omitempty"`
	Note           *string  `json:"note,omitempty"`
}

// SuggestionPolicy names the policy and the enforced version the suggestion was written for.
type SuggestionPolicy struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	BaseVersion     int    `json:"base_version"`
	BaseVersionID   string `json:"base_version_id"`
	BaseVersionHash string `json:"base_version_hash"`
}

// VersionBrief names a policy version.
type VersionBrief struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	VersionHash string `json:"version_hash"`
}

// SuggestionReplay is the suggestion's replay, summarized (GET /v1/replays/{id} has all of it).
type SuggestionReplay struct {
	ID           string        `json:"id"`
	Window       Window        `json:"window"`
	Summary      ReplaySummary `json:"summary"`
	Transitions  []Transition  `json:"transitions"`
	ResultDigest string        `json:"result_digest"`
}

// Phrasing says whose words the title and rationale are.
type Phrasing struct {
	Source         string  `json:"source"` // template | claude
	Model          string  `json:"model,omitempty"`
	Reason         string  `json:"reason,omitempty"` // why the template stands (validation:…, refusal, timeout, …)
	InputTokens    int     `json:"input_tokens,omitzero"`
	OutputTokens   int     `json:"output_tokens,omitzero"`
	CostUSDMicros  int64   `json:"cost_usd_micros,omitzero"`
	DurationMS     int64   `json:"duration_ms,omitzero"`
	At             *string `json:"at,omitempty"`
	TemplateTitle  string  `json:"template_title,omitempty"` // when worded by Claude: the template it replaced
	TemplateReason string  `json:"-"`
}

// SuggestionFilter is GET /v1/suggestions's filter.
type SuggestionFilter struct {
	Status string // "" = open and accepting; "all"
	Kind   string
}

// NewSuggestion is a row to insert.
type NewSuggestion struct {
	ID, Kind, Generator, Fingerprint                       string
	Title, Rationale, RiskNote                             string
	PolicyID, BaseVersionID, BaseVersionHash, TargetRuleID string
	CandidateSource                                        []byte
	CandidateSourceHash                                    string
	Diff                                                   string
	Evidence                                               []byte
	EvidenceDigest                                         string
	Window                                                 Window
	Impact                                                 int
	Warnings                                               []policy.Diagnostic
	Replay                                                 *ReplayResult // nil for an advisory
	ReplayID                                               string
	PolicyName                                             string
	PolicyVersion                                          int
	RequestedBy                                            Actor
	MainCount                                              int64
	PhrasingInput                                          *PhrasingInput // for the phraser after the insert (never stored)
	Target                                                 string         // the rule or budget it changes (learn_run only)
}

// SuggestionRow is a stored suggestion with what the accept saga needs.
type SuggestionRow struct {
	Suggestion
	Fingerprint     string
	CandidateSource []byte
	ReplayDigest    string
	DecidedByType   string
	DecidedByID     string
	AcceptReason    string
	ReplaceShadow   bool
	RequestID       string
	UpdatedAt       time.Time
}

// RejectedRef is a fingerprint's latest rejection.
type RejectedRef struct {
	At        time.Time
	MainCount int64
}

// SuggestStore is what suggestions read and write (store/learnstore.Store).
type SuggestStore interface {
	EvidenceStore
	Org(ctx context.Context, org ids.OrgID) (OrgInfo, error)
	EnforcedSource(ctx context.Context, org ids.OrgID) (*VersionInfo, []byte, error)
	ResolveVersion(ctx context.Context, org ids.OrgID, policyRef string, version int) (*ResolvedVersion, error)
	DayCounters(ctx context.Context, org ids.OrgID, from, to time.Time, tz string) ([]DayCounter, error)
	AgentBudgets(ctx context.Context, org ids.OrgID) ([]AgentBudgetRow, error)
	// LiveShadowActivation is the active shadow version and the deed of its activation (nil when none).
	LiveShadowActivation(ctx context.Context, org ids.OrgID) (*VersionInfo, string, error)

	SupersedeStale(ctx context.Context, org ids.OrgID, enforcedVersionID string) (int, error)
	OpenFingerprints(ctx context.Context, org ids.OrgID, enforcedVersionID string) (map[string]string, error)
	LastRejected(ctx context.Context, org ids.OrgID, fingerprints []string) (map[string]RejectedRef, error)
	InsertSuggestions(ctx context.Context, org ids.OrgID, rows []NewSuggestion) ([]string, error)
	ListSuggestions(ctx context.Context, org ids.OrgID, f SuggestionFilter, limit int, cursor string) ([]Suggestion, string, error)
	GetSuggestion(ctx context.Context, org ids.OrgID, id string) (*SuggestionRow, error)
	BeginAccept(ctx context.Context, org ids.OrgID, id string, by Actor, reason string, replaceShadow bool, requestID string) (*SuggestionRow, error)
	ReopenSuggestion(ctx context.Context, org ids.OrgID, id string) error
	SupersedeOne(ctx context.Context, org ids.OrgID, id string) error
	CompleteAccept(ctx context.Context, org ids.OrgID, id string, done AcceptDone) (string, error)
	RejectSuggestion(ctx context.Context, org ids.OrgID, id string, by Actor, reason, requestID string, now time.Time) (string, error)
	StuckAccepting(ctx context.Context, org ids.OrgID, before time.Time) ([]string, error)
	SetPhrasing(ctx context.Context, org ids.OrgID, id string, title, rationale *string, p Phrasing) error
	PhrasingUsage(ctx context.Context, org ids.OrgID, monthStart, dayStart time.Time) (costMicros int64, callsToday int, err error)
	PersonName(ctx context.Context, org ids.OrgID, a Actor) (string, error)

	ClaimRun(ctx context.Context, org ids.OrgID, kind, period string, at time.Time) (bool, error)
	FinishRun(ctx context.Context, org ids.OrgID, kind, period string, ok bool, stats map[string]any, errText string, at time.Time) error
}

// LearnRun is a suggest pass's answer (POST /v1/suggestions/generate).
type LearnRun struct {
	Object     string            `json:"object"` // learn_run
	Kind       string            `json:"kind"`   // suggest
	Period     string            `json:"period"`
	Policy     *SuggestionPolicy `json:"policy"`
	Window     Window            `json:"window"`
	Actions    int               `json:"actions"`
	Created    []SuggestionBrief `json:"created"`
	Superseded int               `json:"superseded"`
	Unchanged  int               `json:"unchanged"` // already open with the same fingerprint
	Waiting    int               `json:"waiting"`   // over the cap of 10 open; they come back next run
	Dropped    []Dropped         `json:"dropped"`
	Notice     *string           `json:"notice"`
	ElapsedMS  int64             `json:"elapsed_ms"`
}

// SuggestionBrief is one created suggestion in a learn_run.
type SuggestionBrief struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Generator    string `json:"generator"`
	TargetRuleID string `json:"target_rule_id,omitempty"`
	Target       string `json:"target,omitempty"` // the rule or budget it changes
	Title        string `json:"title"`
	Impact       int    `json:"impact"`
}

// Suggester runs suggest passes.
type Suggester struct {
	Store   SuggestStore
	Engine  *Engine
	Phraser Phraser // nil: template wording only
	Now     func() time.Time
	Log     *slog.Logger

	mu       sync.Mutex
	running  map[string]bool
	phrase   sync.WaitGroup
	phrasing sync.Map // org id → *sync.Mutex: one phrasing call per org at a time
}

func (s *Suggester) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Suggester) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ErrSuggestBusy is a pass already running for the org on this instance.
var ErrSuggestBusy = errors.New("learn: a suggest pass is already running for this org")

// WaitPhrasing waits for background phrasing calls (tests, shutdown).
func (s *Suggester) WaitPhrasing() { s.phrase.Wait() }

// Fingerprint is sha256(JCS{generator, policy_id, target, pattern}).
func Fingerprint(generator, policyID, target string, pattern map[string]any) string {
	d, err := deed.DigestValue(map[string]any{"generator": generator, "policy_id": policyID, "target": target, "pattern": jsonSafe(pattern)})
	if err != nil {
		return ""
	}
	return d
}

// jsonSafe round-trips v through JSON, so ints and string slices become the plain types JCS takes.
func jsonSafe(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	_ = d.Decode(&out)
	return normalizeNumbers(out)
}

func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		return x.String()
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeNumbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = normalizeNumbers(e)
		}
	}
	return v
}

// kindRank orders the cap: tighten before loosen (S28 §3.4), then the rest.
func kindRank(k string) int {
	switch k {
	case KindTighten:
		return 0
	case KindLoosen:
		return 1
	case KindBudget:
		return 2
	case KindExplicitRule:
		return 3
	}
	return 4
}

type candidate struct {
	p        Proposal
	row      NewSuggestion
	fp       string
	compiled *policy.Compiled
}

// Generate runs one suggest pass for org. period names the learn_runs row ("" records nothing).
func (s *Suggester) Generate(ctx context.Context, org ids.OrgID, by Actor) (*LearnRun, error) {
	key := org.String()
	s.mu.Lock()
	if s.running == nil {
		s.running = map[string]bool{}
	}
	if s.running[key] {
		s.mu.Unlock()
		return nil, ErrSuggestBusy
	}
	s.running[key] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, key)
		s.mu.Unlock()
	}()
	t0 := s.now()
	run, err := s.generate(ctx, org, by, t0)
	if run != nil {
		run.ElapsedMS = s.now().Sub(t0).Milliseconds()
	}
	return run, err
}

func (s *Suggester) generate(ctx context.Context, org ids.OrgID, by Actor, now time.Time) (*LearnRun, error) {
	run := &LearnRun{Object: "learn_run", Kind: "suggest", Created: []SuggestionBrief{}, Dropped: []Dropped{},
		Window: Window{From: now.Add(-SuggestWindow), To: now}}
	info, err := s.Store.Org(ctx, org)
	if err != nil {
		return nil, err
	}
	enf, _, err := s.Store.EnforcedSource(ctx, org)
	if err != nil {
		return nil, err
	}
	if enf == nil {
		n := MsgSuggestNoEnforced
		run.Notice = &n
		return run, nil
	}
	if run.Superseded, err = s.Store.SupersedeStale(ctx, org, enf.VersionID); err != nil {
		return nil, err
	}
	rv, err := s.Store.ResolveVersion(ctx, org, enf.PolicyID, enf.Version)
	if err != nil {
		return nil, err
	}
	if !rv.Found {
		return nil, fmt.Errorf("learn: the enforced version %s v%d is missing", enf.Name, enf.Version)
	}
	base, diags := policy.Compile(rv.Source, policy.Options{})
	if base == nil {
		return nil, errors.New("learn: the enforced version no longer compiles: " + firstDiag(diags))
	}
	run.Policy = &SuggestionPolicy{ID: rv.PolicyID, Name: rv.Name, BaseVersion: rv.Version, BaseVersionID: rv.VersionID, BaseVersionHash: rv.VersionHash}
	ev, err := BuildEvidence(ctx, s.Store, info, rv, base, now)
	if err != nil {
		return nil, err
	}
	counters, err := s.Store.DayCounters(ctx, org, ev.Window.From, ev.Window.To, info.Timezone)
	if err != nil {
		return nil, err
	}
	agents, err := s.Store.AgentBudgets(ctx, org)
	if err != nil {
		return nil, err
	}
	ev.Budgets = BudgetUses(counters, agents, base, ev.Loc)
	run.Actions = len(ev.Rows)

	var props []Proposal
	for _, g := range Generators {
		ps, ds := g.Fn(ev)
		props = append(props, ps...)
		run.Dropped = append(run.Dropped, ds...)
	}
	open, err := s.Store.OpenFingerprints(ctx, org, rv.VersionID)
	if err != nil {
		return nil, err
	}
	openCount := 0
	for _, st := range open {
		if st != SugAccepted {
			openCount++
		}
	}
	fps := make([]string, len(props))
	for i := range props {
		fps[i] = Fingerprint(props[i].Generator, rv.PolicyID, props[i].TargetRuleID, props[i].Pattern)
	}
	rejected, err := s.Store.LastRejected(ctx, org, fps)
	if err != nil {
		return nil, err
	}
	var cands []candidate
	for i, p := range props {
		fp := fps[i]
		switch open[fp] {
		case "":
		case SugAccepted:
			run.Dropped = append(run.Dropped, Dropped{Generator: p.Generator, Target: p.TargetRuleID,
				Reason: "already accepted for this version of the policy; promote or clear its shadow first"})
			continue
		default:
			run.Unchanged++
			continue
		}
		if r, ok := rejected[fp]; ok && now.Sub(r.At) < RejectionMemory && p.MainCount < 2*r.MainCount {
			run.Dropped = append(run.Dropped, Dropped{Generator: p.Generator, Target: p.TargetRuleID,
				Reason: "rejected on " + r.At.In(ev.Loc).Format("2006-01-02") + "; it comes back after 30 days, or sooner if the numbers double"})
			continue
		}
		c, why, err := s.build(ctx, org, ev, p, fp, by)
		if err != nil {
			return nil, err
		}
		if why != "" {
			run.Dropped = append(run.Dropped, Dropped{Generator: p.Generator, Target: p.TargetRuleID, Reason: why})
			continue
		}
		cands = append(cands, *c)
	}
	slices.SortStableFunc(cands, func(a, b candidate) int {
		if c := cmp.Compare(b.row.Impact, a.row.Impact); c != 0 {
			return c
		}
		return cmp.Compare(kindRank(a.row.Kind), kindRank(b.row.Kind))
	})
	room := max(0, MaxOpenSuggestions-openCount)
	if len(cands) > room {
		run.Waiting = len(cands) - room
		cands = cands[:room]
	}
	rows := make([]NewSuggestion, len(cands))
	for i := range cands {
		rows[i] = cands[i].row
	}
	if len(rows) > 0 {
		created, err := s.Store.InsertSuggestions(ctx, org, rows)
		if err != nil {
			return nil, err
		}
		inserted := map[string]bool{}
		for _, id := range created {
			inserted[id] = true
		}
		for _, r := range rows {
			if !inserted[r.ID] {
				run.Unchanged++ // another instance stored the same fingerprint first
				continue
			}
			run.Created = append(run.Created, SuggestionBrief{ID: r.ID, Kind: r.Kind, Generator: r.Generator, TargetRuleID: r.TargetRuleID,
				Target: r.Target, Title: r.Title, Impact: r.Impact})
		}
		s.phraseLater(org, info, rows, inserted)
	}
	return run, nil
}

// build turns a proposal into a stored row: patch, compile, check, replay, guard.
func (s *Suggester) build(ctx context.Context, org ids.OrgID, ev *Evidence, p Proposal, fp string, by Actor) (*candidate, string, error) {
	id := ids.New[ids.SuggestionKind]().String()
	evidence := mergeMaps(p.Evidence, map[string]any{"window": map[string]any{"from": ev.Window.From.Format(time.RFC3339), "to": ev.Window.To.Format(time.RFC3339)}})
	evJSON, err := json.Marshal(jsonSafe(evidence))
	if err != nil {
		return nil, "", err
	}
	evDigest, err := deed.DigestValue(jsonSafe(evidence))
	if err != nil {
		return nil, "", err
	}
	row := NewSuggestion{ID: id, Kind: p.Kind, Generator: p.Generator, Fingerprint: fp, Title: p.Title(), Rationale: p.Rationale(),
		RiskNote: p.RiskNote(), PolicyID: ev.PolicyID, BaseVersionID: ev.BaseVersionID, BaseVersionHash: ev.BaseVersionHash,
		TargetRuleID: p.TargetRuleID, Evidence: evJSON, EvidenceDigest: evDigest, Window: ev.Window, Warnings: []policy.Diagnostic{},
		PolicyName: ev.PolicyName, PolicyVersion: ev.BaseVersion, RequestedBy: by, MainCount: p.MainCount}
	row.PhrasingInput = BuildPhrasingInput(&p, ev.days())
	row.Target = p.TargetRuleID
	if b, ok := p.Pattern["budget"].(string); ok && row.Target == "" {
		row.Target = b
	} else if a, ok := p.Pattern["agent"].(string); ok && row.Target == "" {
		row.Target = a
	}
	c := &candidate{p: p, fp: fp}
	if len(p.Ops) == 0 {
		c.row = row
		return c, "", nil
	}
	// The comment above an inserted rule names the suggestion and its evidence, so the accepted
	// version reads back to where it came from.
	comment := "Accepted from Deedglass Insights suggestion " + id + " · evidence " + shortDigest(evDigest)
	ops := make([]policy.PatchOp, len(p.Ops))
	for i, op := range p.Ops {
		switch o := op.(type) {
		case policy.InsertRuleBefore:
			o.Rule.Comment = append([]string{comment}, o.Rule.Comment...)
			ops[i] = o
		case policy.AppendRule:
			o.Rule.Comment = append([]string{comment}, o.Rule.Comment...)
			ops[i] = o
		default:
			ops[i] = op
		}
	}
	src, err := policy.Patch(ev.Source, ops...)
	if err != nil {
		return nil, "the policy file can't be edited in place: " + strings.TrimPrefix(err.Error(), "policy: patch: "), nil
	}
	cand, diags := policy.Compile(src, policy.Options{})
	if cand == nil {
		return nil, "the changed policy doesn't compile: " + firstDiag(diags), nil
	}
	if err := policy.CheckPatch(ev.Base, cand, ops...); err != nil {
		return nil, "the changed policy moved more than the edit", nil
	}
	if cand.VersionHash == ev.Base.VersionHash {
		return nil, "the change doesn't change the policy", nil
	}
	if cand.Warnings != nil {
		row.Warnings = cand.Warnings
	}
	res, err := s.Engine.Run(ctx, org, Candidate{Compiled: cand, PolicyID: ev.PolicyID, SourceHash: cand.SourceHash, Label: ev.PolicyName + " (suggested)"},
		Baseline{Type: BaselineVersion, Compiled: ev.Base, VersionID: ev.BaseVersionID, Version: ev.BaseVersion}, ev.Window, nil)
	if errors.Is(err, ErrReplayTooLarge) {
		return nil, "its replay window holds more than 1,000,000 actions", nil
	}
	if err != nil {
		return nil, "", err
	}
	res.Candidate.Name = ev.PolicyName
	res.Candidate.PolicyID = ev.PolicyID
	res.Baseline.VersionHash = ev.BaseVersionHash
	if p.Guard != nil {
		if ok, why := p.Guard(res); !ok {
			return nil, why, nil
		}
	}
	row.CandidateSource, row.CandidateSourceHash = src, cand.SourceHash
	row.Diff = policy.Diff(ev.Source, src, DiffContext).Text
	row.Replay, row.ReplayID = res, ids.New[ids.ReplayKind]().String()
	row.Impact = int(res.Summary.Changed)
	c.row, c.compiled = row, cand
	return c, "", nil
}

func shortDigest(d string) string {
	h := strings.TrimPrefix(d, "sha256:")
	if len(h) < 8 {
		return d
	}
	return "sha256:" + h[:4] + "…" + h[len(h)-4:]
}

// --- reads -------------------------------------------------------------------------------------------

// SuggestionService is the API side of suggestions: list, get, generate, accept, reject.
type SuggestionService struct {
	Store     SuggestStore
	Suggester *Suggester
	Writer    PolicyWriter // nil: accept answers 503
	Now       func() time.Time
	Log       *slog.Logger
	// PhrasingKeySet reports whether the operator set DEEDGLASS_ANTHROPIC_API_KEY.
	PhrasingKeySet bool
}

func (s *SuggestionService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
