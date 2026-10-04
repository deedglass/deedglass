package learn

import (
	"math"
	"sort"
	"time"
)

// This file holds the six anomaly detectors (S27 §3.7). They are pure: no I/O and no
// clock. The caller passes the agent's hourly history, the trailing 60-minute window and
// the org's location; Detect returns findings in a deterministic order. Findings are
// signals for a person to look at, never verdicts, and nothing here blocks an action.

// DetectorVersion names the thresholds below. Bump it when any of them changes.
const DetectorVersion = "1"

// MinHistory is the history an agent needs before the baseline checks run. A cold agent
// (younger than this) only gets new_tool; the API marks it baseline_building.
const MinHistory = 72 * time.Hour

// The anomaly kinds, in the order Detect reports them.
const (
	AnomalyNewTool       = "new_tool"
	AnomalyVolumeSpike   = "volume_spike"
	AnomalyOffHours      = "off_hours"
	AnomalyDenySpike     = "deny_spike"
	AnomalyMismatchSpike = "mismatch_spike"
	AnomalyUserRefBurst  = "user_ref_burst"
)

// AnomalyKinds lists every kind in report order.
var AnomalyKinds = []string{
	AnomalyNewTool, AnomalyVolumeSpike, AnomalyOffHours,
	AnomalyDenySpike, AnomalyMismatchSpike, AnomalyUserRefBurst,
}

// Finding severities. Anomalies are signals: the Console renders them amber or neutral.
const (
	SeverityInfo = "info"
	SeverityWarn = "warn"
	SeverityHigh = "high"
)

// Side effects as recorded per tool in HourBucket.ToolSide.
const (
	SideRead        = "read"
	SideWrite       = "write"
	SideDestructive = "destructive"
	SideUnknown     = "unknown"
)

// EvidenceKeys is the allow-list of keys a Finding's Evidence may hold: numbers, hours,
// tool names and up to 10 action ids. Never args, refs, SQL or results.
var EvidenceKeys = []string{
	"window_start", "window_end", "hour", "timezone", "count",
	"baseline_mean", "baseline_stddev", "factor", "deny_rate", "baseline_rate",
	"mismatches", "new_refs", "writes", "tool", "side_effect",
	"history_hours", "share", "action_ids",
}

// MaxEvidenceActionIDs caps the action ids one finding carries.
const MaxEvidenceActionIDs = 10

// Detector thresholds (DetectorVersion "1").
const (
	newToolMinAge      = 24 * time.Hour
	newToolMinTotal    = 20
	volumeMinCount     = 20
	volumeFactor       = 3.0
	zThreshold         = 3.0
	volumeHighScore    = 10.0
	offHoursMaxShare   = 0.01
	offHoursMinCount   = 5
	offHoursMinTotal   = 200
	offHoursMinDays    = 5
	denyMinCount       = 20
	denyMinDenied      = 5
	mismatchMinCount   = 2
	mismatchQuietMean  = 0.25
	mismatchMinZCount  = 3
	userRefMinCount    = 10
	userRefFactor      = 3.0
	evidenceTimeLayout = time.RFC3339
)

// HourBucket is one hour of an agent's activity.
type HourBucket struct {
	Start       time.Time         // UTC, on the hour
	Actions     int               // every action
	Denied      int               // decision deny, excluding decision_source = stop
	Mismatch    int               // MISMATCH verdicts
	NewUserRefs int               // on_behalf_of refs this agent had not used in the 30 days before
	Writes      int               // actions whose side effect is write or destructive
	Tools       map[string]int    // calls per tool name, e.g. "github.merge_pr"
	ToolSide    map[string]string // the highest side effect seen per tool: read|write|destructive|unknown
	Samples     Samples           // up to 10 action ids each, in time order
}

// Samples holds up to 10 action ids per list, in time order.
type Samples struct {
	All, Denied, Mismatch, NewUserRefs []string
	ByTool                             map[string][]string
}

// AgentHistory is the baseline for one agent: the 14 days before the window.
type AgentHistory struct {
	Agent          string          // agt_ id
	Name           string          // agent name, e.g. "growth-agent"
	CreatedAt      time.Time       // when the agent was created
	Hours          []HourBucket    // the 336 hourly buckets before the window, oldest first, zero-filled
	ToolsSeen30d   map[string]bool // tools this agent used in the 30 days before the window
	HourOfDayShare [24]float64     // share of the 14-day volume per org-local hour of day (sums to 1, or all 0)
	ActiveDays     int             // distinct org-local days with at least one action in the 14 days
	Total          int             // actions in the 14 days
}

// DetectWindow is the trailing 60 minutes (End is now).
type DetectWindow struct {
	Start, End time.Time
	Current    HourBucket
}

// Finding is one detector result for one agent and window.
type Finding struct {
	Kind, Tool, Severity string // severity: info | warn | high
	Score                float64
	Evidence             map[string]any
	Title                string
}

// HistoryAge is now minus the agent's creation time, or 0 when CreatedAt is zero or after now.
func HistoryAge(h AgentHistory, now time.Time) time.Duration {
	if h.CreatedAt.IsZero() || h.CreatedAt.After(now) {
		return 0
	}
	return now.Sub(h.CreatedAt)
}

// BaselineBuilding reports whether the agent has less than MinHistory of history at now.
func BaselineBuilding(h AgentHistory, now time.Time) bool {
	return HistoryAge(h, now) < MinHistory
}

// Detect runs the six detectors for one agent over one window. The result is ordered by
// kind (AnomalyKinds order), then by tool. A cold agent only gets new_tool.
func Detect(h AgentHistory, w DetectWindow, loc *time.Location) []Finding {
	if loc == nil {
		loc = time.UTC
	}
	d := detection{h: h, w: w, loc: loc}
	out := d.newTools()
	if BaselineBuilding(h, w.End) {
		return out
	}
	for _, f := range []func() (Finding, bool){d.volumeSpike, d.offHours, d.denySpike, d.mismatchSpike, d.userRefBurst} {
		if found, ok := f(); ok {
			out = append(out, found)
		}
	}
	return out
}

type detection struct {
	h   AgentHistory
	w   DetectWindow
	loc *time.Location
}

// span is the window as the titles write it, e.g. "14:00 to 15:00 IST".
func (d detection) span() string { return AnomalySpan(d.w.Start, d.w.End, d.loc) }

// evidence starts every finding's evidence with the window, the org-local hour and the history.
func (d detection) evidence(ids []string) map[string]any {
	local := d.w.Start.In(d.loc)
	return map[string]any{
		"window_start":  d.w.Start.UTC().Format(evidenceTimeLayout),
		"window_end":    d.w.End.UTC().Format(evidenceTimeLayout),
		"hour":          anmHourLabel(local.Hour()),
		"timezone":      local.Format("MST"),
		"history_hours": int(HistoryAge(d.h, d.w.End) / time.Hour),
		"action_ids":    anmCapIDs(ids),
	}
}

// baseline returns the population mean and standard deviation of one counter over the
// prior hourly buckets that start at or after the agent's creation hour.
func (d detection) baseline(field func(HourBucket) int) (mean, sd float64) {
	from := d.h.CreatedAt.Truncate(time.Hour)
	var vals []float64
	for _, b := range d.h.Hours {
		if b.Start.Before(from) {
			continue
		}
		vals = append(vals, float64(field(b)))
	}
	if len(vals) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	mean = sum / float64(len(vals))
	var sq float64
	for _, v := range vals {
		sq += (v - mean) * (v - mean)
	}
	return mean, math.Sqrt(sq / float64(len(vals)))
}

// sigmaPrime is max(σ, √μ, 1): a floor so a flat baseline does not make every bump a spike.
func sigmaPrime(mean, sd float64) float64 {
	return math.Max(sd, math.Max(math.Sqrt(mean), 1))
}

func (d detection) newTools() []Finding {
	out := []Finding{}
	if HistoryAge(d.h, d.w.End) <= newToolMinAge || d.h.Total < newToolMinTotal {
		return out
	}
	tools := make([]string, 0, len(d.w.Current.Tools))
	for tool, n := range d.w.Current.Tools {
		if n > 0 && !d.seenBefore(tool) {
			tools = append(tools, tool)
		}
	}
	sort.Strings(tools)
	for _, tool := range tools {
		side, severity := d.w.Current.ToolSide[tool], SeverityWarn
		switch side {
		case SideRead:
			severity = SeverityInfo
		case SideDestructive:
			severity = SeverityHigh
		case SideWrite:
		default:
			side = SideUnknown
		}
		n := d.w.Current.Tools[tool]
		ev := d.evidence(d.w.Current.Samples.ByTool[tool])
		ev["tool"] = tool
		ev["side_effect"] = side
		ev["count"] = n
		out = append(out, Finding{
			Kind: AnomalyNewTool, Tool: tool, Severity: severity,
			Score: anmRound(float64(n), 2), Evidence: ev,
			Title: TitleNewTool(tool, side, d.span()),
		})
	}
	return out
}

func (d detection) seenBefore(tool string) bool {
	if d.h.ToolsSeen30d[tool] {
		return true
	}
	for _, b := range d.h.Hours {
		if b.Tools[tool] > 0 {
			return true
		}
	}
	return false
}

func (d detection) volumeSpike() (Finding, bool) {
	n := d.w.Current.Actions
	mean, sd := d.baseline(func(b HourBucket) int { return b.Actions })
	sp := sigmaPrime(mean, sd)
	fn := float64(n)
	if n < volumeMinCount || fn < mean+zThreshold*sp || fn < volumeFactor*mean {
		return Finding{}, false
	}
	score := anmRound((fn-mean)/sp, 2)
	severity := SeverityWarn
	if score >= volumeHighScore {
		severity = SeverityHigh
	}
	ev := d.evidence(d.w.Current.Samples.All)
	ev["count"] = n
	ev["writes"] = d.w.Current.Writes
	anmBaselineEvidence(ev, fn, mean, sp)
	return Finding{
		Kind: AnomalyVolumeSpike, Severity: severity, Score: score, Evidence: ev,
		Title: TitleVolumeSpike(n, mean, d.span()),
	}, true
}

func (d detection) offHours() (Finding, bool) {
	n := d.w.Current.Actions
	share := d.h.HourOfDayShare[d.w.Start.In(d.loc).Hour()]
	if share >= offHoursMaxShare || n < offHoursMinCount || d.h.Total < offHoursMinTotal || d.h.ActiveDays < offHoursMinDays {
		return Finding{}, false
	}
	severity := SeverityInfo
	if d.w.Current.Writes > 0 {
		severity = SeverityWarn
	}
	ev := d.evidence(d.w.Current.Samples.All)
	ev["count"] = n
	ev["writes"] = d.w.Current.Writes
	ev["share"] = anmRound(share, 4)
	return Finding{
		Kind: AnomalyOffHours, Severity: severity, Score: anmRound(float64(n), 2), Evidence: ev,
		Title: TitleOffHours(n, share, d.span()),
	}, true
}

// denyBaseline is p = (D+1)/(N+2) over the 14 days: the deny rate with one prior deny and
// one prior allow, so an agent with no history starts at 0.5, not 0.
func denyBaseline(h AgentHistory) float64 {
	var denied, total int
	for _, b := range h.Hours {
		denied += b.Denied
		total += b.Actions
	}
	return float64(denied+1) / float64(total+2)
}

func (d detection) denySpike() (Finding, bool) {
	n, denied := d.w.Current.Actions, d.w.Current.Denied
	if n < denyMinCount || denied < denyMinDenied {
		return Finding{}, false
	}
	p := denyBaseline(d.h)
	se := math.Sqrt(p * (1 - p) / float64(n))
	rate := float64(denied) / float64(n)
	if rate < p+zThreshold*se {
		return Finding{}, false
	}
	ev := d.evidence(d.w.Current.Samples.Denied)
	ev["count"] = n
	ev["deny_rate"] = anmRound(rate, 4)
	ev["baseline_rate"] = anmRound(p, 4)
	return Finding{
		Kind: AnomalyDenySpike, Severity: SeverityWarn, Score: anmRound((rate-p)/se, 2), Evidence: ev,
		Title: TitleDenySpike(denied, n, p, d.span()),
	}, true
}

func (d detection) mismatchSpike() (Finding, bool) {
	m := d.w.Current.Mismatch
	mean, sd := d.baseline(func(b HourBucket) int { return b.Mismatch })
	sp := sigmaPrime(mean, sd)
	fm := float64(m)
	var score float64
	switch {
	case m >= mismatchMinCount && mean < mismatchQuietMean:
		score = fm
	case m >= mismatchMinZCount && fm >= mean+zThreshold*sp:
		score = (fm - mean) / sp
	default:
		return Finding{}, false
	}
	ev := d.evidence(d.w.Current.Samples.Mismatch)
	ev["count"] = d.w.Current.Actions
	ev["mismatches"] = m
	ev["baseline_mean"] = anmRound(mean, 2)
	ev["baseline_stddev"] = anmRound(sp, 2)
	return Finding{
		Kind: AnomalyMismatchSpike, Severity: SeverityHigh, Score: anmRound(score, 2), Evidence: ev,
		Title: TitleMismatchSpike(m, mean, d.span()),
	}, true
}

func (d detection) userRefBurst() (Finding, bool) {
	r := d.w.Current.NewUserRefs
	mean, sd := d.baseline(func(b HourBucket) int { return b.NewUserRefs })
	sp := sigmaPrime(mean, sd)
	fr := float64(r)
	if r < userRefMinCount || fr < mean+zThreshold*sp || fr < userRefFactor*mean {
		return Finding{}, false
	}
	ev := d.evidence(d.w.Current.Samples.NewUserRefs)
	ev["count"] = d.w.Current.Actions
	ev["new_refs"] = r
	anmBaselineEvidence(ev, fr, mean, sp)
	return Finding{
		Kind: AnomalyUserRefBurst, Severity: SeverityWarn, Score: anmRound((fr-mean)/sp, 2), Evidence: ev,
		Title: TitleUserRefBurst(r, mean, d.span()),
	}, true
}

// anmBaselineEvidence adds the mean, σ' and, when the mean is above 0, the factor n/μ.
func anmBaselineEvidence(ev map[string]any, n, mean, sp float64) {
	ev["baseline_mean"] = anmRound(mean, 2)
	ev["baseline_stddev"] = anmRound(sp, 2)
	if mean > 0 {
		ev["factor"] = anmRound(n/mean, 1)
	}
}

func anmCapIDs(ids []string) []string {
	if len(ids) > MaxEvidenceActionIDs {
		ids = ids[:MaxEvidenceActionIDs]
	}
	return append([]string{}, ids...)
}

func anmRound(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
