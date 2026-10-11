package learn

import (
	"fmt"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// Every sentence Insights prints lives here (and the detector titles in anomaly_copy.go), so the
// CLI, the API and S28's Console use the same words. The voice: numbers first, no adjectives, no
// alarm; anomalies are signals, not verdicts. No em or en dashes, no exclamation marks.

// Messages of the API's refusals (internal/httpx carries the codes; these are the sentences).
const (
	MsgWindowInvalid      = "The window must start before it ends, and it can be at most 90 days long. Send from before to."
	MsgResolveReason      = "A reason is required to resolve an anomaly. It goes into the sealed record."
	MsgReasonLength       = "The reason must be 3 to 280 characters."
	MsgSettingsReason     = "A reason is required to change Insights settings. It goes into the sealed record."
	MsgReplayFinished     = "This replay already finished, so it can't be cancelled. Start a new one to replay again."
	MsgAnomalyResolved    = "This anomaly is already resolved. Resolved anomalies stay in the history; a new finding opens a new one."
	MsgAgentKey           = "Agent keys can't read Insights. Use the Console or an org admin key."
	MsgReplayRateLimited  = "This org started 10 replays in the last hour. Try again later, or narrow the window of the ones running."
	MsgStatsBy            = "by must be rule, tool, agent or none."
	MsgStatsWindow        = "window must be 7d or 30d, or send from and to (RFC 3339)."
	MsgStatsCompare       = "compare must be previous."
	MsgActiveMinutes      = "anomaly_active_minutes must be 5 to 1,440."
	MsgStaleMinutes       = "anomaly_stale_after_minutes must be 10 to 240."
	MsgBaselineType       = "baseline.type must be recorded or version; a version baseline needs baseline.version."
	MsgAnomalyStatus      = "status must be open, acknowledged, resolved or all."
	MsgNoShadow           = "No shadow policy is active in this org. Run one: deedglass policy activate <name> --version <n> --mode shadow --reason \"…\""
	MsgReplayVersionShape = "The version is a number, like 3."
)

// ReplayTooLargeMessage is replay_too_large's sentence.
func ReplayTooLargeMessage(actions int64) string {
	return fmt.Sprintf("This replay covers %s actions; the limit is %s. Narrow the window with from and to.",
		deed.FormatCount(actions), deed.FormatCount(MaxReplayActions))
}

// UnknownVersionMessage is the sentence for a replay of a version that doesn't exist.
func UnknownVersionMessage(name string, version, latest int) string {
	if latest < 1 {
		return fmt.Sprintf("No version %d of %s. List its versions: deedglass policy versions %s", version, name, name)
	}
	return fmt.Sprintf("No version %d of %s. Versions 1 to %d exist. List them: deedglass policy versions %s", version, name, latest, name)
}

// OptInOffNotice is the sentence when a policy reads anomaly.* but the org's facts are off.
func OptInOffNotice(policyLabel string) string {
	return policyLabel + " reads anomaly.active, but anomaly facts are off for this org, so it always sees false. Turn them on in Console › Insights › Settings."
}

// DetectorStaleNotice is the sentence when detection is behind for an opted-in org.
func DetectorStaleNotice(minutes int) string {
	return fmt.Sprintf("Anomaly detection last ran %d %s ago. Until it catches up, policies that read anomaly.active treat every agent as anomalous.",
		minutes, plural(minutes, "minute", "minutes"))
}

// DetectorNeverRanNotice is the sentence when detection never ran for an opted-in org.
const DetectorNeverRanNotice = "Anomaly detection hasn't run for this org yet. Until it does, policies that read anomaly.active treat every agent as anomalous."

// SimulatedBanner is the text S28 shows on every Console screen for a simulated org.
const SimulatedBanner = "Simulated history. These actions came from deedglass dev simulate. Their records were sealed when the simulation ran, and their check method is simulated."

// AnomalyTrace is the decision trace's line about the anomaly facts a policy saw.
func AnomalyTrace(kinds []string) string {
	if len(kinds) == 0 {
		return ""
	}
	return "anomaly facts: " + strings.Join(kinds, ", ")
}

// AnomalyStaleTrace is the trace line of a fail-closed decision.
func AnomalyStaleTrace(lastRun time.Time, now time.Time) string {
	if lastRun.IsZero() {
		return "anomaly facts: detector_stale (detection hasn't run yet)"
	}
	m := int(now.Sub(lastRun).Minutes())
	return fmt.Sprintf("anomaly facts: detector_stale (last run %d min ago)", m)
}

// AnomalyLookupFailedTrace is the trace line when the facts couldn't be read (fail closed).
const AnomalyLookupFailedTrace = "anomaly facts: detector_stale (the anomaly facts couldn't be read)"

// StatsEmpty is the sentence for a window with no actions.
func StatsEmpty(from, to time.Time, loc *time.Location) string {
	return fmt.Sprintf("No actions between %s and %s. Send one: deedglass act fake.echo --args '{\"x\":1}'",
		from.In(loc).Format("2006-01-02"), to.In(loc).Format("2006-01-02"))
}

// ReplaySummaryLine is the CLI's and Console's one-line result.
func ReplaySummaryLine(s ReplaySummary) string {
	parts := []string{fmt.Sprintf("%s %s would change", deed.FormatCount(s.Changed), plural(int(s.Changed), "decision", "decisions"))}
	if s.RuleChanged > 0 {
		parts = append(parts, deed.FormatCount(s.RuleChanged)+" by a different rule")
	}
	parts = append(parts, deed.FormatCount(s.Unchanged)+" unchanged", deed.FormatCount(s.Unknown)+" unknown")
	return strings.Join(parts, " · ")
}

// HumanOutcomeLine is "humans approved 401, expired 3, denied 0" for a transition out of approval.
func HumanOutcomeLine(h *HumanOutcome) string {
	if h == nil {
		return ""
	}
	s := fmt.Sprintf("humans approved %s, expired %s, denied %s", deed.FormatCount(h.Approved), deed.FormatCount(h.Expired), deed.FormatCount(h.Denied))
	if h.Pending > 0 {
		s += ", pending " + deed.FormatCount(h.Pending)
	}
	if h.Cancelled > 0 {
		s += ", cancelled " + deed.FormatCount(h.Cancelled)
	}
	if h.NotRequested > 0 {
		s += ", never asked " + deed.FormatCount(h.NotRequested)
	}
	return s
}

// UnknownLine explains the unknowns on their own line ("12 inputs pruned by retention · 2
// recorded before inputs were stored").
func UnknownLine(u UnknownCounts) string {
	var parts []string
	add := func(n int64, one, many string) {
		if n > 0 {
			parts = append(parts, deed.FormatCount(n)+" "+plural(int(n), one, many))
		}
	}
	add(u.InputPruned, "input pruned by retention", "inputs pruned by retention")
	add(u.InputMissing, "recorded before inputs were stored", "recorded before inputs were stored")
	add(u.InputUnsupported, "input in a newer format", "inputs in a newer format")
	add(u.InputUnreadable, "input that doesn't decode", "inputs that don't decode")
	add(u.EvaluationTimedOut, "evaluation that hit its time limit", "evaluations that hit their time limit")
	return strings.Join(parts, " · ")
}

// ShadowSentence is the shadow report's one sentence: "Shadow acme-sim v2 would have sent 41
// actions to approval and denied 5 that v1 allowed (refund-approval-over-30: 41, slack-allowlist: 5)."
func ShadowSentence(r *ShadowReport) string {
	label := r.Shadow.Name
	if r.Shadow.Version > 0 {
		label += fmt.Sprintf(" v%d", r.Shadow.Version)
	}
	enforce := "the enforced policy"
	if len(r.Enforce) == 1 && r.Enforce[0].Version > 0 {
		if r.Enforce[0].Name == r.Shadow.Name {
			enforce = fmt.Sprintf("v%d", r.Enforce[0].Version)
		} else {
			enforce = fmt.Sprintf("%s v%d", r.Enforce[0].Name, r.Enforce[0].Version)
		}
	}
	if r.Summary.Evaluated == 0 {
		return fmt.Sprintf("Shadow %s hasn't seen any actions yet.", label)
	}
	if r.Summary.Disagree == 0 {
		return fmt.Sprintf("Shadow %s agreed with %s on all %s %s.", label, enforce, deed.FormatCount(r.Summary.Evaluated),
			plural(int(r.Summary.Evaluated), "action", "actions"))
	}
	byShadow := map[string]int64{}
	from := map[string]bool{}
	rules := map[string]int64{}
	for _, t := range r.Transitions {
		byShadow[t.Shadow] += t.Count
		from[t.Enforce] = true
		for _, br := range t.ByRule {
			rules[br.RuleID] += br.Count
		}
	}
	var verbs []string
	if n := byShadow["require_approval"]; n > 0 {
		verbs = append(verbs, fmt.Sprintf("sent %s %s to approval", deed.FormatCount(n), plural(int(n), "action", "actions")))
	}
	if n := byShadow["deny"]; n > 0 {
		verbs = append(verbs, "denied "+deed.FormatCount(n))
	}
	if n := byShadow["allow"]; n > 0 {
		verbs = append(verbs, "allowed "+deed.FormatCount(n))
	}
	s := "Shadow " + label + " would have " + joinAnd(verbs)
	if len(from) == 1 {
		for f := range from {
			s += " that " + enforce + " " + pastTense(f)
		}
	}
	keys := sortedCounts(rules)
	if len(keys) > 3 {
		keys = keys[:3]
	}
	var rs []string
	for _, k := range keys {
		if k.Key == "" {
			continue
		}
		rs = append(rs, fmt.Sprintf("%s: %s", k.Key, deed.FormatCount(k.Count)))
	}
	if len(rs) > 0 {
		s += " (" + strings.Join(rs, ", ") + ")"
	}
	return s + "."
}

func pastTense(decision string) string {
	switch decision {
	case "allow":
		return "allowed"
	case "deny":
		return "denied"
	case "require_approval":
		return "sent to approval"
	}
	return "decided"
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// The anomaly inbox's last line: whether this org's policies see anomaly facts.
const (
	AnomalyFactsOffLine = `Policies don't read anomalies in this org. Turn it on: deedglass insights settings --anomaly-facts on --reason "…"`
	AnomalyFactsOnLine  = "Policies in this org see anomaly facts: an active anomaly sets anomaly.active for its agent."
)
