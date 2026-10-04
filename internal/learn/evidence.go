package learn

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// Evidence is everything the six generators read (S28 §3.2): the enforced version and its source,
// fourteen days of decisions, and the facts of each action a person decided. It is built once
// per suggest pass from the store's rows; generators are pure functions of it.

// SuggestWindow is how far back suggestions look.
const SuggestWindow = 14 * 24 * time.Hour

// EvidenceRow is one decided action as the suggest pass reads it. Input is set only for actions
// that went to approval and for MISMATCH verdicts: the two places a generator needs derived facts.
type EvidenceRow struct {
	ActionID      string
	At            time.Time // evaluated_at (the decision time)
	AgentID       string
	AgentName     string
	Tool          string
	ConnectorType string // from the stored input's tool facts ("" when pruned)
	SideEffect    string // read, write, destructive, unknown ("" when pruned)
	Decision      string // allow | deny | require_approval
	Source        string // rule | default | budget | stop | builtin
	RuleID        string
	VersionID     string          // the enforced version that decided
	Approval      ApprovalOutcome // "" when the action never went to approval
	WaitSeconds   float64         // a decided approval's wait (-1 when there is none)
	Approvers     []string        // the people who approved (usr_…)
	Verdict       string          // VERIFIED | MISMATCH | UNVERIFIABLE | ""
	Level         string          // L0 | L1 | L2 | ""
	Input         json.RawMessage
}

// EpisodeRef is an anomaly episode as the budget generator reads it.
type EpisodeRef struct {
	ID        string
	AgentID   string
	Kind      string
	FirstSeen time.Time
	LastSeen  time.Time
}

// EvidenceStore is what BuildEvidence reads (store/learnstore.Store).
type EvidenceStore interface {
	EvidenceRows(ctx context.Context, org ids.OrgID, w Window) ([]EvidenceRow, error)
	EpisodesIn(ctx context.Context, org ids.OrgID, w Window) ([]EpisodeRef, error)
}

// Evidence is the input of every generator.
type Evidence struct {
	Org             string
	OrgName         string
	Loc             *time.Location
	PolicyID        string
	PolicyName      string
	BaseVersionID   string
	BaseVersion     int
	BaseVersionHash string
	Base            *policy.Compiled // the enforced version (never the builtin)
	Source          []byte           // its YAML
	Window          Window
	Rows            []EvidenceRow
	Rules           []RuleEvidence // per decision_rule_id of the base version, in the policy's order
	Budgets         []BudgetUse    // S27's day use of the base's budgets and the agents' budgets
	Episodes        []EpisodeRef
	// Facts holds the derived facts of every row with a stored input, by action id.
	Facts map[string]*ActionFacts
}

// RuleEvidence is one rule's decisions in the window, under the base version.
type RuleEvidence struct {
	RuleID     string
	Decision   string // the rule's own decision
	Actions    int64
	Approvals  ApprovalCounts
	WaitMedian float64 // seconds; 0 when no approval was decided
	Approvers  int     // distinct people who approved
	Tools      []string
	Agents     []string // names
	Verdicts   VerdictCounts
}

// ApprovalCounts are the approvals a rule asked for and what people did.
type ApprovalCounts struct {
	Requested int64 `json:"requested"`
	Approved  int64 `json:"approved"`
	Denied    int64 `json:"denied"`
	Expired   int64 `json:"expired"`
	Cancelled int64 `json:"cancelled,omitempty"`
	Pending   int64 `json:"pending"`
}

// ActionFacts are the facts a generator may build a pattern from: derived with policy.Prepare from
// the stored input, never from raw argument text. Numbers only for args.
type ActionFacts struct {
	SQL      *policy.SQLFacts // nil when the tool carried no SQL
	Amount   *int64           // args.amount when it is a whole number
	Currency string           // args.currency when it is a 3-letter string
}

// BuildEvidence reads the window's rows and derives the facts. now is the window's end.
func BuildEvidence(ctx context.Context, st EvidenceStore, org OrgInfo, base *ResolvedVersion, c *policy.Compiled, now time.Time) (*Evidence, error) {
	w := Window{From: now.Add(-SuggestWindow), To: now}
	rows, err := st.EvidenceRows(ctx, org.ID, w)
	if err != nil {
		return nil, err
	}
	eps, err := st.EpisodesIn(ctx, org.ID, w)
	if err != nil {
		return nil, err
	}
	return NewEvidence(org, base, c, w, rows, eps), nil
}

// NewEvidence assembles Evidence from rows (pure; fixtures call it directly).
func NewEvidence(org OrgInfo, base *ResolvedVersion, c *policy.Compiled, w Window, rows []EvidenceRow, eps []EpisodeRef) *Evidence {
	ev := &Evidence{Org: org.ID.String(), OrgName: org.Name, Loc: loadLoc(org.Timezone), PolicyID: base.PolicyID, PolicyName: base.Name,
		BaseVersionID: base.VersionID, BaseVersion: base.Version, BaseVersionHash: base.VersionHash, Base: c, Source: base.Source,
		Window: w, Rows: rows, Episodes: eps, Facts: map[string]*ActionFacts{}}
	for i := range rows {
		if f := deriveFacts(rows[i].Input); f != nil {
			ev.Facts[rows[i].ActionID] = f
		}
	}
	ev.Rules = ruleEvidence(c, rows, base.VersionID)
	return ev
}

// deriveFacts recomputes an input's derived facts. nil when there is no readable input.
func deriveFacts(raw json.RawMessage) *ActionFacts {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var in policy.Input
	if json.Unmarshal(raw, &in) != nil || in.V != policy.InputVersion {
		return nil
	}
	p, err := policy.Prepare(in)
	if err != nil {
		return nil
	}
	f := &ActionFacts{}
	if sq := p.Facts().SQL; sq.Present {
		s := sq
		f.SQL = &s
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(in.Args, &args) == nil {
		if a, ok := args["amount"]; ok {
			var n json.Number
			d := json.NewDecoder(strings.NewReader(string(a)))
			d.UseNumber()
			if d.Decode(&n) == nil {
				if v, err := n.Int64(); err == nil && v >= 0 {
					f.Amount = &v
				}
			}
		}
		if cur, ok := args["currency"]; ok {
			var s string
			if json.Unmarshal(cur, &s) == nil && len(s) == 3 && strings.ToLower(s) == s {
				f.Currency = s
			}
		}
	}
	return f
}

// ruleEvidence aggregates the base version's decisions per rule, in the policy's rule order.
func ruleEvidence(c *policy.Compiled, rows []EvidenceRow, baseVersionID string) []RuleEvidence {
	if c == nil {
		return nil
	}
	type acc struct {
		r         RuleEvidence
		waits     []float64
		approvers map[string]bool
		tools     map[string]bool
		agents    map[string]bool
	}
	by := map[string]*acc{}
	for _, r := range c.Rules {
		by[r.ID] = &acc{r: RuleEvidence{RuleID: r.ID, Decision: string(r.Decision)}, approvers: map[string]bool{}, tools: map[string]bool{}, agents: map[string]bool{}}
	}
	for i := range rows {
		x := &rows[i]
		if x.VersionID != baseVersionID || x.Source != policy.SourceRule {
			continue
		}
		a := by[x.RuleID]
		if a == nil {
			continue
		}
		a.r.Actions++
		a.tools[x.Tool] = true
		a.agents[x.AgentName] = true
		switch x.Verdict {
		case "VERIFIED":
			a.r.Verdicts.Verified++
		case "MISMATCH":
			a.r.Verdicts.Mismatch++
		case "UNVERIFIABLE":
			a.r.Verdicts.Unverifiable++
		}
		if x.Decision != "require_approval" {
			continue
		}
		a.r.Approvals.Requested++
		switch x.Approval {
		case ApprovalApproved:
			a.r.Approvals.Approved++
		case ApprovalDenied:
			a.r.Approvals.Denied++
		case ApprovalExpired:
			a.r.Approvals.Expired++
		case ApprovalCancelled:
			a.r.Approvals.Cancelled++
		default:
			a.r.Approvals.Pending++
		}
		if x.WaitSeconds >= 0 && (x.Approval == ApprovalApproved || x.Approval == ApprovalDenied) {
			a.waits = append(a.waits, x.WaitSeconds)
		}
		for _, p := range x.Approvers {
			a.approvers[p] = true
		}
	}
	out := make([]RuleEvidence, 0, len(c.Rules))
	for _, r := range c.Rules {
		a := by[r.ID]
		a.r.WaitMedian = median(a.waits)
		a.r.Approvers = len(a.approvers)
		a.r.Tools = sortedKeys(a.tools)
		a.r.Agents = sortedKeys(a.agents)
		out = append(out, a.r)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// median of xs (0 for none), the mean of the two middle values for an even count.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// percentile is the nearest-rank p-th percentile (0 < p ≤ 100) of xs.
func percentile(xs []int64, p float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	return s[min(max(rank, 1), len(s))-1]
}

// rule finds a rule of the base by id.
func (ev *Evidence) rule(id string) *policy.Rule {
	for i := range ev.Base.Rules {
		if ev.Base.Rules[i].ID == id {
			return &ev.Base.Rules[i]
		}
	}
	return nil
}

// ruleEvidence finds a rule's evidence by id.
func (ev *Evidence) ruleEv(id string) *RuleEvidence {
	for i := range ev.Rules {
		if ev.Rules[i].RuleID == id {
			return &ev.Rules[i]
		}
	}
	return nil
}

// effectiveVerify is the level a rule asks for (its own, else the default's).
func (ev *Evidence) effectiveVerify(r *policy.Rule) policy.Level {
	if r.Verify != "" {
		return r.Verify
	}
	if ev.Base.Defaults.Verify != "" {
		return ev.Base.Defaults.Verify
	}
	return policy.L0
}

// days is the window's length in whole days.
func (ev *Evidence) days() int { return int(math.Round(ev.Window.To.Sub(ev.Window.From).Hours() / 24)) }
