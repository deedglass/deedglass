package learn

import (
	"context"
	"math"
	"slices"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// Shadow disagreements (S27 §3.5): where the shadow policy and the enforced one disagree on live
// traffic, grouped by rule, tool and agent. Rows are selected by shadow_version_id, never by
// activation time (appended simulated history is in the past).

// ShadowQuery picks the shadow version (default: the live one) and an optional window.
type ShadowQuery struct {
	ShadowVersionID string
	From, To        *time.Time
}

// VersionInfo names one policy version.
type VersionInfo struct {
	PolicyID  string    `json:"policy"`
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	VersionID string    `json:"version_id"`
	Live      bool      `json:"live"`           // the active shadow now
	Since     time.Time `json:"since,omitzero"` // when it became the shadow (its activation)
}

// ShadowRow is one aggregate row: actions with this (enforced decision, shadow decision, shadow
// rule, tool, agent, enforcing version, approval status).
type ShadowRow struct {
	Enforce, Shadow, ShadowRule, Tool, AgentID, AgentName, EnforceVersionID string
	Approval                                                                ApprovalOutcome
	Count                                                                   int64
}

// ShadowSample is one disagreeing action, in decision-time order.
type ShadowSample struct {
	Enforce, Shadow, ActionID string
}

// ShadowReport is the shadow_report object.
type ShadowReport struct {
	Object      string             `json:"object"` // shadow_report
	Shadow      VersionInfo        `json:"shadow"`
	Enforce     []EnforceVersion   `json:"enforce"`
	Window      NullableWindow     `json:"window"`
	Summary     ShadowSummary      `json:"summary"`
	Transitions []ShadowTransition `json:"transitions"`
	Sentence    string             `json:"sentence"`
}

// NullableWindow is a window whose ends may be open.
type NullableWindow struct {
	From *time.Time `json:"from"`
	To   *time.Time `json:"to"`
}

// EnforceVersion is an enforced version that decided some of the report's actions.
type EnforceVersion struct {
	Name      string `json:"name"`
	Version   int    `json:"version"`
	VersionID string `json:"version_id"`
	Actions   int64  `json:"actions"`
}

// ShadowSummary: agreement = agree / evaluated, rounded to 4 decimals.
type ShadowSummary struct {
	Evaluated int64   `json:"evaluated"`
	Agree     int64   `json:"agree"`
	Disagree  int64   `json:"disagree"`
	Agreement float64 `json:"agreement"`
}

// ShadowTransition is every action the shadow would have decided differently.
type ShadowTransition struct {
	Enforce      string        `json:"enforce"`
	Shadow       string        `json:"shadow"`
	Count        int64         `json:"count"`
	HumanOutcome *HumanOutcome `json:"human_outcome,omitempty"`
	ByRule       []RuleCount   `json:"by_rule"`
	ByTool       []ToolCount   `json:"by_tool"`
	ByAgent      []AgentCount  `json:"by_agent"`
	Samples      []string      `json:"samples"`
}

// BuildShadowReport folds the aggregate rows and samples into the report (pure).
func BuildShadowReport(shadow VersionInfo, enforceNames map[string]VersionInfo, rows []ShadowRow, samples []ShadowSample, w NullableWindow) *ShadowReport {
	r := &ShadowReport{Object: "shadow_report", Shadow: shadow, Window: w, Enforce: []EnforceVersion{}, Transitions: []ShadowTransition{}}
	type acc struct {
		t                    ShadowTransition
		rules, tools, agents map[string]int64
		agentNames           map[string]string
		human                HumanOutcome
	}
	trans := map[[2]string]*acc{}
	enforce := map[string]int64{}
	for _, row := range rows {
		r.Summary.Evaluated += row.Count
		enforce[row.EnforceVersionID] += row.Count
		if row.Enforce == row.Shadow {
			r.Summary.Agree += row.Count
			continue
		}
		r.Summary.Disagree += row.Count
		k := [2]string{row.Enforce, row.Shadow}
		a := trans[k]
		if a == nil {
			a = &acc{t: ShadowTransition{Enforce: row.Enforce, Shadow: row.Shadow, Samples: []string{}}, rules: map[string]int64{},
				tools: map[string]int64{}, agents: map[string]int64{}, agentNames: map[string]string{}}
			trans[k] = a
		}
		a.t.Count += row.Count
		a.rules[row.ShadowRule] += row.Count
		a.tools[row.Tool] += row.Count
		a.agents[row.AgentID] += row.Count
		a.agentNames[row.AgentID] = row.AgentName
		if row.Enforce == "require_approval" {
			switch row.Approval {
			case ApprovalApproved:
				a.human.Approved += row.Count
			case ApprovalDenied:
				a.human.Denied += row.Count
			case ApprovalExpired:
				a.human.Expired += row.Count
			case ApprovalCancelled:
				a.human.Cancelled += row.Count
			default:
				a.human.Pending += row.Count
			}
		}
	}
	for _, s := range samples {
		if a := trans[[2]string{s.Enforce, s.Shadow}]; a != nil && len(a.t.Samples) < MaxSamples {
			a.t.Samples = append(a.t.Samples, s.ActionID)
		}
	}
	for _, a := range trans {
		t := a.t
		for _, kc := range sortedCounts(a.rules) {
			t.ByRule = append(t.ByRule, RuleCount{RuleID: kc.Key, Count: kc.Count})
		}
		for _, kc := range sortedCounts(a.tools) {
			t.ByTool = append(t.ByTool, ToolCount{Tool: kc.Key, Count: kc.Count})
		}
		for _, kc := range sortedCounts(a.agents) {
			t.ByAgent = append(t.ByAgent, AgentCount{Agent: kc.Key, Name: a.agentNames[kc.Key], Count: kc.Count})
		}
		if t.Enforce == "require_approval" {
			h := a.human
			t.HumanOutcome = &h
		}
		r.Transitions = append(r.Transitions, t)
	}
	slices.SortFunc(r.Transitions, func(x, y ShadowTransition) int {
		if x.Count != y.Count {
			return int(y.Count - x.Count)
		}
		if x.Enforce != y.Enforce {
			return compareStrings(x.Enforce, y.Enforce)
		}
		return compareStrings(x.Shadow, y.Shadow)
	})
	for _, kc := range sortedCounts(enforce) {
		v := enforceNames[kc.Key]
		r.Enforce = append(r.Enforce, EnforceVersion{Name: v.Name, Version: v.Version, VersionID: kc.Key, Actions: kc.Count})
	}
	if r.Summary.Evaluated > 0 {
		r.Summary.Agreement = round4(float64(r.Summary.Agree) / float64(r.Summary.Evaluated))
	}
	r.Sentence = ShadowSentence(r)
	return r
}

func round4(x float64) float64 { return math.Round(x*10000) / 10000 }

// ShadowStore is what the shadow report reads (store/learnstore.Store).
type ShadowStore interface {
	// LiveShadow is the org's active shadow version, or nil.
	LiveShadow(ctx context.Context, org ids.OrgID) (*VersionInfo, error)
	// Version names one version by id (nil when it isn't the org's).
	Version(ctx context.Context, org ids.OrgID, versionID string) (*VersionInfo, error)
	// VersionsByID names several versions.
	VersionsByID(ctx context.Context, org ids.OrgID, versionIDs []string) (map[string]VersionInfo, error)
	// ShadowRows aggregates the actions decided with this shadow version.
	ShadowRows(ctx context.Context, org ids.OrgID, shadowVersionID string, w NullableWindow) ([]ShadowRow, error)
	// ShadowSamples returns up to n disagreeing actions per (enforce, shadow) pair, by decision time.
	ShadowSamples(ctx context.Context, org ids.OrgID, shadowVersionID string, w NullableWindow, n int) ([]ShadowSample, error)
}
