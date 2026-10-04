package learn

import (
	"context"
	"encoding/json"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// Anomaly episodes (S27 §3.7). An episode is the unresolved series of findings for one
// agent|kind|tool. Two separate ideas:
//   - Active (active_until > now) is what anomaly.active sees. It expires on its own,
//     anomaly_active_minutes after the last finding.
//   - Status (open → acknowledged → resolved) is the review inbox. Only a person moves it, except
//     that an episode with no new occurrence for 14 days closes itself (resolved by system, no
//     deed, because nothing was decided).

// Anomaly statuses.
const (
	StatusOpen         = "open"
	StatusAcknowledged = "acknowledged"
	StatusResolved     = "resolved"
)

// EpisodeCloseAfter is how long an episode stays open with no new occurrence.
const EpisodeCloseAfter = 14 * 24 * time.Hour

// MaxEvidencePoints caps the hourly points an episode keeps.
const MaxEvidencePoints = 48

// Anomaly is the anomaly object.
type Anomaly struct {
	Object          string          `json:"object"` // anomaly
	ID              string          `json:"id"`
	Agent           AgentRef        `json:"agent"`
	Kind            string          `json:"kind"`
	Tool            *string         `json:"tool"`
	Severity        string          `json:"severity"`
	Score           float64         `json:"score"`
	Title           string          `json:"title"`
	Status          string          `json:"status"`
	Active          bool            `json:"active"`
	ActiveUntil     time.Time       `json:"active_until"`
	Window          AnomalyWindow   `json:"window"`
	Occurrences     int             `json:"occurrences"`
	Evidence        json.RawMessage `json:"evidence"`
	DetectorVersion string          `json:"detector_version"`
	Acknowledged    *ReviewMark     `json:"acknowledged"`
	Resolved        *Resolution     `json:"resolved"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// AgentRef names an agent.
type AgentRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AnomalyWindow is when the episode's findings happened (decision time).
type AnomalyWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// ReviewMark is who acknowledged and when.
type ReviewMark struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// Resolution is who resolved, why, and the sealed deed (none for the system's 14-day close).
type Resolution struct {
	By     string    `json:"by"`
	At     time.Time `json:"at"`
	Reason *string   `json:"reason"`
	Deed   *string   `json:"deed"`
}

// AnomalyFilter filters GET /v1/insights/anomalies.
type AnomalyFilter struct {
	Status string // open | acknowledged | resolved | all ("" = open and acknowledged)
	Agent  string
	Kind   string
	Since  time.Time // window_end ≥ since (the CLI's last 14 days)
}

// AnomalyList is GET /v1/insights/anomalies: the list envelope plus what the inbox says about the
// org (additive fields).
type AnomalyList struct {
	Object           string          `json:"object"` // list
	Data             []Anomaly       `json:"data"`
	HasMore          bool            `json:"has_more"`
	NextCursor       *string         `json:"next_cursor"`
	ActiveNow        int             `json:"active_now"`
	AnomalyFacts     bool            `json:"anomaly_facts"`
	Notice           *string         `json:"notice"`
	BaselineBuilding []BaselineAgent `json:"baseline_building"`
}

// BaselineAgent is an agent with under 72 hours of history: only new_tool can fire for it.
type BaselineAgent struct {
	Agent    string `json:"agent"`
	Name     string `json:"name"`
	Hours    int    `json:"hours"`
	Sentence string `json:"sentence"`
}

// Finding plus where it happened: what UpsertEpisode records.
type EpisodeInput struct {
	Agent, AgentName string
	Finding          Finding
	Window           DetectWindow
	ActiveMinutes    int
	Now              time.Time // the pass's now (virtual during a backfill)
}

// EpisodeResult says what UpsertEpisode did.
type EpisodeResult struct {
	ID        string
	Inserted  bool // a new episode
	Extended  bool // an existing one got this window
	Published bool // anomaly.detected went out (a new episode or a new occurrence)
	Skipped   bool // this window was already recorded (a re-run)
}

// AgentInfo is an agent as detection reads it.
type AgentInfo struct {
	ID, Name, Kind string
	CreatedAt      time.Time
}

// OrgInfo is the org as Insights reads it.
type OrgInfo struct {
	ID        ids.OrgID
	Name      string
	Slug      string
	Timezone  string
	Settings  Settings
	Simulated bool
}

// BucketRow is one (agent, org-local hour, tool) aggregate of decided actions.
type BucketRow struct {
	Agent            string
	Start            time.Time // the org-local hour's start, as an instant
	Tool             string
	Actions, Denied  int
	Mismatch, Writes int
	SideRank         int // 1 read, 2 unknown, 3 write, 4 destructive (the highest seen)
}

// RefRow counts on-behalf-of refs an agent used for the first time (within the look-back) in one
// org-local hour.
type RefRow struct {
	Agent   string
	Start   time.Time
	NewRefs int
}

// SampleKind picks which action ids a finding's evidence lists.
type SampleKind string

// Sample kinds.
const (
	SampleAll      SampleKind = "all"
	SampleDenied   SampleKind = "denied"
	SampleMismatch SampleKind = "mismatch"
	SampleNewRefs  SampleKind = "new_refs"
	SampleTool     SampleKind = "tool"
)

// ActiveEpisode is an episode active at some time: what anomaly facts read.
type ActiveEpisode struct {
	Agent, Kind, Severity string
	ActiveUntil           time.Time
}

// DetectTx is one org's detect pass, inside one transaction holding the org's advisory lock.
type DetectTx interface {
	Agents(ctx context.Context) ([]AgentInfo, error)
	Buckets(ctx context.Context, tz string, from, to time.Time) ([]BucketRow, error)
	// RefBuckets counts first uses of refs per agent and hour in [from, to), a ref counting as
	// new when the agent hadn't used it since lookback.
	RefBuckets(ctx context.Context, tz string, lookback, from, to time.Time) ([]RefRow, error)
	// Window aggregates [from, to) per (agent, tool) into rows whose Start is from.
	Window(ctx context.Context, from, to time.Time) ([]BucketRow, error)
	WindowRefs(ctx context.Context, lookback, from, to time.Time) ([]RefRow, error)
	Samples(ctx context.Context, agent string, from, to time.Time, kind SampleKind, tool string, lookback time.Time, n int) ([]string, error)
	UpsertEpisode(ctx context.Context, in EpisodeInput) (EpisodeResult, error)
	CloseStale(ctx context.Context, before, now time.Time) (int, error)
	RecordRun(ctx context.Context, kind, period string, started, finished time.Time, status string, stats map[string]any, errMsg string) error
	PruneRuns(ctx context.Context, kind string, before time.Time) error
}

// AnomalyStore is what anomalies need (store/learnstore.Store).
type AnomalyStore interface {
	DetectOrgs(ctx context.Context, recent time.Duration) ([]ids.OrgID, error)
	Org(ctx context.Context, org ids.OrgID) (OrgInfo, error)
	// WithDetectLock runs fn in one org transaction holding pg_try_advisory_xact_lock for the
	// org's detection; locked is false (and fn doesn't run) when another pass holds it.
	WithDetectLock(ctx context.Context, org ids.OrgID, fn func(ctx context.Context, tx DetectTx) error) (locked bool, err error)
	ListAnomalies(ctx context.Context, org ids.OrgID, f AnomalyFilter, limit int, cursor string, now time.Time) ([]Anomaly, string, error)
	GetAnomaly(ctx context.Context, org ids.OrgID, id string, now time.Time) (*Anomaly, error)
	CountActive(ctx context.Context, org ids.OrgID, now time.Time) (int, error)
	Acknowledge(ctx context.Context, org ids.OrgID, id string, by Actor, now time.Time) (*Anomaly, error)
	Resolve(ctx context.Context, org ids.OrgID, id string, by Actor, reason, requestID string, now time.Time) (*Anomaly, error)
	// FactsState is what FactsCache loads: the settings, the episodes active at or after now, and
	// the last successful live detect run.
	FactsState(ctx context.Context, org ids.OrgID, now time.Time) (Settings, []ActiveEpisode, time.Time, error)
	UpdateSettings(ctx context.Context, org ids.OrgID, before, after Settings, by Actor, reason, requestID string) (deed string, err error)
	AgentAges(ctx context.Context, org ids.OrgID) ([]AgentInfo, error)
}

// ErrNotFound is the stores' not-found (the API maps it to not_found).
var ErrNotFound = errNotFound{}

type errNotFound struct{}

func (errNotFound) Error() string { return "learn: not found" }

// ErrStateConflict: the anomaly or replay isn't in a state that allows the change.
var ErrStateConflict = errStateConflict{}

type errStateConflict struct{}

func (errStateConflict) Error() string { return "learn: state conflict" }
