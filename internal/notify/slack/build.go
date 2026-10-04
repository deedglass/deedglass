package slack

import (
	"encoding/json"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/store"
)

// snapshot is the subset of S22's context snapshot (v1) the card reads. The snapshot is what a
// person countersigns, so the card is built from it, never from the live action.
type snapshot struct {
	Action struct {
		Tool          string `json:"tool"`
		Connector     string `json:"connector"`
		ConnectorType string `json:"connector_type"`
		RequestDigest string `json:"request_digest"`
	} `json:"action"`
	Agent struct {
		Name string `json:"name"`
	} `json:"agent"`
	OnBehalfOf *string `json:"on_behalf_of"`
	Policy     struct {
		Name      *string  `json:"name"`
		Version   *int     `json:"version"`
		RuleID    string   `json:"rule_id"`
		Reason    string   `json:"reason"`
		Required  int      `json:"required"`
		Approvers []string `json:"approvers"`
	} `json:"policy"`
	Args      json.RawMessage `json:"args"`
	ExpiresAt string          `json:"expires_at"`
}

func parseSnapshot(b []byte) snapshot {
	var s snapshot
	_ = json.Unmarshal(b, &s)
	return s
}

// Summarizers finds a connector type's Summarizer (the registry's types).
type Summarizers interface {
	Type(t string) (connector.Connector, bool)
}

// viewOf builds the card's view of an approval as it is now.
func viewOf(r *store.ApprovalCardRow, sums Summarizers, baseURL string) ApprovalView {
	snap := parseSnapshot(r.Context)
	v := ApprovalView{ApprovalID: r.ApprovalID, ActionID: r.ActionID, ContextDigest: r.ContextDigest, Tool: r.Tool,
		Agent: firstNonEmpty(snap.Agent.Name, r.AgentName), RuleID: snap.Policy.RuleID, Reason: snap.Policy.Reason,
		ExpiresAt: r.ExpiresAt, Required: max(r.Required, snap.Policy.Required, 1), RequestDigest: snap.Action.RequestDigest,
		Args: snap.Args, ConsoleURL: strings.TrimSuffix(baseURL, "/") + "/app/approvals/" + r.ApprovalID}
	if snap.Policy.Name != nil {
		v.PolicyName = *snap.Policy.Name
	}
	if snap.Policy.Version != nil {
		v.PolicyVersion = *snap.Policy.Version
	}
	if snap.OnBehalfOf != nil {
		v.OnBehalfOf = *snap.OnBehalfOf
	}
	v.Summary = summaryOf(snap, r, sums, v.Agent)
	var lastDeny *store.CardVote
	for i, x := range r.Votes {
		switch x.Vote {
		case "approve":
			v.Approved++
			v.Countersigned = append(v.Countersigned, Countersign{Name: x.Name, At: x.At})
		case "deny":
			lastDeny = &r.Votes[i]
		}
	}
	switch r.Status {
	case "pending":
		v.State = StatePending
		if v.Approved > 0 {
			v.State = StatePartial
		}
	case "approved":
		v.State = StateApproved
		if n := len(v.Countersigned); n > 0 {
			v.DecidedBy, v.DecidedAt = v.Countersigned[n-1].Name, v.Countersigned[n-1].At
		}
		if r.DeedID != nil && r.ActionState == "sealed" {
			v.State, v.DeedID = StateSealed, *r.DeedID
			switch {
			case r.ExecutionStatus != nil && *r.ExecutionStatus == "failed":
				v.Failed = true
			case r.WitnessVerdict != nil:
				v.Verdict = *r.WitnessVerdict
				if r.WitnessLevel != nil {
					v.Level = *r.WitnessLevel
				}
				if v.Verdict != "VERIFIED" {
					v.Sentence = witnessSummary(r.WitnessDetail)
				}
			}
		}
	case "denied":
		v.State = StateDenied
		if r.DecisionReason != nil {
			v.DenyReason = *r.DecisionReason
		}
		if lastDeny != nil {
			v.DecidedBy, v.DecidedAt = lastDeny.Name, lastDeny.At
		} else {
			v.DecidedBy = "Deedglass"
			if r.DecidedAt != nil {
				v.DecidedAt = *r.DecidedAt
			}
		}
	case "expired":
		v.State = StateExpired
	case "cancelled":
		v.State = StateCancelled
	}
	if v.DecidedAt.IsZero() && r.DecidedAt != nil {
		v.DecidedAt = *r.DecidedAt
	}
	return v
}

// summaryOf asks the connector type for its sentence, with the enrich facts the policy saw.
func summaryOf(snap snapshot, r *store.ApprovalCardRow, sums Summarizers, agent string) connector.Summary {
	tool := firstNonEmpty(snap.Action.Tool, r.Tool)
	if sums != nil && snap.Action.ConnectorType != "" {
		if impl, ok := sums.Type(snap.Action.ConnectorType); ok {
			if s, ok := impl.(connector.Summarizer); ok {
				var facts map[string]any
				_ = json.Unmarshal(r.PolicyRequest, &facts)
				_, name, _ := connector.Split(tool)
				out := s.Summarize(connector.SummaryInput{Agent: agent, Qualified: tool, Tool: connector.ToolSpec{Name: name},
					Args: snap.Args, Facts: facts})
				if len(out) > 0 {
					return out
				}
			}
		}
	}
	return connector.FallbackSummary(agent, tool)
}

// witnessSummary is witness_detail.summary: the Witness's one line about the verdict.
func witnessSummary(detail json.RawMessage) string {
	var d struct {
		Summary string `json:"summary"`
	}
	_ = json.Unmarshal(detail, &d)
	return d.Summary
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// wantsSlack reports whether the rule that asked for this approval names slack in notify:.
func wantsSlack(notify []string) bool {
	for _, n := range notify {
		if n == "slack" || strings.HasPrefix(n, "slack:") {
			return true
		}
	}
	return false
}
