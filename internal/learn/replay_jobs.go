package learn

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// Replay jobs (S27 §3.4): POST …/versions/{v}/replay queues a job; a runner on any instance claims
// it (one running job per org, a 60 s lease renewed every batch), runs the
// engine and stores the result. A lost lease restarts from zero, which is safe because the engine
// is deterministic. Cancel sets cancel_requested_at; the runner checks it between batches.

// Replay statuses.
const (
	ReplayQueued    = "queued"
	ReplayRunning   = "running"
	ReplaySucceeded = "succeeded"
	ReplayFailed    = "failed"
	ReplayCancelled = "cancelled"
)

// Replay job limits.
const (
	ReplayLease          = 60 * time.Second
	ReplaysPerInstance   = 2
	ReplaysPerHourPerOrg = 10
)

// Replay is the replay object: the job, its progress, and its result once it succeeded (the
// result fields are flattened into it).
type Replay struct {
	Object      string          `json:"object"` // replay
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	Purpose     string          `json:"purpose"` // manual | suggestion
	Candidate   ReplayCandidate `json:"candidate"`
	Baseline    ReplayBaseline  `json:"baseline"`
	Window      Window          `json:"window"`
	Progress    ReplayProgress  `json:"progress"`
	RequestedBy Actor           `json:"requested_by"`
	Attempts    int             `json:"attempts"`
	CreatedAt   time.Time       `json:"created_at"`
	StartedAt   *time.Time      `json:"started_at"`
	FinishedAt  *time.Time      `json:"finished_at"`
	Error       *ReplayError    `json:"error"`

	// The result, when status is succeeded.
	Summary      *ReplaySummary `json:"summary,omitempty"`
	Transitions  []Transition   `json:"transitions,omitempty"`
	RuleChanges  []RuleChange   `json:"rule_changes,omitempty"`
	Unknown      *UnknownCounts `json:"unknown,omitempty"`
	SQLGuard     *SQLGuardDrift `json:"sqlguard,omitempty"`
	ResultDigest *string        `json:"result_digest,omitempty"`
	ElapsedMS    *int64         `json:"elapsed_ms,omitempty"`
}

// ReplayProgress: percent = processed / total, an integer.
type ReplayProgress struct {
	Processed int64  `json:"processed"`
	Total     *int64 `json:"total"`
	Percent   int    `json:"percent"`
}

// ReplayError is a failed replay's cause.
type ReplayError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WithResult fills the replay's result fields from r.
func (p *Replay) WithResult(r *ReplayResult) {
	if r == nil {
		return
	}
	s, u, g, d, e := r.Summary, r.Unknown, r.SQLGuard, r.ResultDigest, r.ElapsedMS
	p.Summary, p.Transitions, p.RuleChanges, p.Unknown, p.SQLGuard, p.ResultDigest, p.ElapsedMS = &s, r.Transitions, r.RuleChanges, &u, &g, &d, &e
}

// NewReplay is what POST …/replay asks for, resolved.
type NewReplay struct {
	PolicyID, VersionID string
	Version             int
	Name, VersionHash   string
	Source              []byte // a draft (S28); nil for a stored version
	SourceHash          string
	Baseline            ReplayBaseline
	Window              Window
	Purpose             string
	RequestedBy         Actor
	Total               int64
}

// ClaimedReplay is a job a runner leased.
type ClaimedReplay struct {
	Org      ids.OrgID
	ID       string
	Attempts int
}

// StoredReplay is a claimed job with what the runner needs to rebuild the candidate.
type StoredReplay struct {
	Replay
	Org            ids.OrgID
	Source         []byte // the candidate's source (the version's, or the draft)
	BaselineSource []byte // the baseline version's source, when baseline.type is version
}

// ErrLeaseLost: another runner holds the job now (this one was too slow to renew).
var ErrLeaseLost = errors.New("learn: the replay's lease moved to another runner")

// JobStore is what replay jobs need (store/learnstore.Store).
type JobStore interface {
	CreateReplay(ctx context.Context, org ids.OrgID, n NewReplay) (*Replay, error)
	GetReplay(ctx context.Context, org ids.OrgID, id string) (*Replay, error)
	ListReplays(ctx context.Context, org ids.OrgID, f ReplayFilter, limit int, cursor string) ([]Replay, string, error)
	CancelReplay(ctx context.Context, org ids.OrgID, id string, by Actor, now time.Time) (*Replay, error)
	ClaimReplays(ctx context.Context, worker string, n int, lease time.Duration) ([]ClaimedReplay, error)
	LoadReplay(ctx context.Context, org ids.OrgID, id string) (*StoredReplay, error)
	// RenewReplay stores progress and extends the lease; it reports whether a cancel was asked
	// for, and ErrLeaseLost when the job isn't this worker's any more.
	RenewReplay(ctx context.Context, org ids.OrgID, id, worker string, processed, total int64, lease time.Duration) (cancel bool, err error)
	FinishReplay(ctx context.Context, org ids.OrgID, id, worker, status string, res *ReplayResult, cause *ReplayError) error
	// RecentReplays counts the org's manual replays created since t (the rate limit).
	RecentReplays(ctx context.Context, org ids.OrgID, since time.Time) (int, error)
}

// ReplayFilter filters GET /v1/replays.
type ReplayFilter struct {
	PolicyID string
	Status   string
}

// Runner claims and runs replay jobs.
type Runner struct {
	Engine *Engine
	Jobs   JobStore
	Worker string        // this instance's name (locked_by)
	Lease  time.Duration // ReplayLease by default
	Max    int           // jobs at once on this instance (ReplaysPerInstance)
	Every  time.Duration // how often it looks for work (2 s)
	Log    *slog.Logger
	// afterBatch is a test seam: return false to stop like a killed runner (no finish, the lease
	// left to expire).
	afterBatch func(processed int64) bool

	mu      sync.Mutex
	running int
	wake    chan struct{}
	once    sync.Once
}

// defaults fills the zero settings once (Run and Wake call it from different goroutines).
func (r *Runner) defaults() { r.once.Do(r.setDefaults) }

func (r *Runner) setDefaults() {
	if r.Lease <= 0 {
		r.Lease = ReplayLease
	}
	if r.Max <= 0 {
		r.Max = ReplaysPerInstance
	}
	if r.Every <= 0 {
		r.Every = 2 * time.Second
	}
	if r.Log == nil {
		r.Log = slog.Default()
	}
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
}

// Wake makes the runner look for work now (a replay was just queued on this instance).
func (r *Runner) Wake() {
	r.defaults()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run claims and runs jobs until ctx ends, at most Max at once.
func (r *Runner) Run(ctx context.Context) error {
	r.defaults()
	var wg sync.WaitGroup
	defer wg.Wait()
	t := time.NewTicker(r.Every)
	defer t.Stop()
	for {
		r.mu.Lock()
		free := r.Max - r.running
		r.mu.Unlock()
		if free > 0 {
			jobs, err := r.Jobs.ClaimReplays(ctx, r.Worker, free, r.Lease)
			if err != nil && ctx.Err() == nil {
				r.Log.Warn("learn: claiming replay jobs failed", "err", err)
			}
			for _, j := range jobs {
				r.mu.Lock()
				r.running++
				r.mu.Unlock()
				wg.Go(func() {
					defer func() { r.mu.Lock(); r.running--; r.mu.Unlock(); r.Wake() }()
					r.runOne(context.WithoutCancel(ctx), j)
				})
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		case <-r.wake:
		}
	}
}

// RunOnce claims and runs what it can now, one at a time (tests and dev tooling).
func (r *Runner) RunOnce(ctx context.Context) (int, error) {
	r.defaults()
	n := 0
	for {
		jobs, err := r.Jobs.ClaimReplays(ctx, r.Worker, 1, r.Lease)
		if err != nil || len(jobs) == 0 {
			return n, err
		}
		r.runOne(ctx, jobs[0])
		n++
	}
}

// runOne runs one claimed job to its end (or until its lease is lost or a cancel is asked for).
func (r *Runner) runOne(ctx context.Context, j ClaimedReplay) {
	log := r.Log.With("org_id", j.Org.String(), "replay_id", j.ID)
	job, err := r.Jobs.LoadReplay(ctx, j.Org, j.ID)
	if err != nil {
		log.Warn("learn: loading a replay job failed", "err", err)
		return
	}
	finish := func(status string, res *ReplayResult, cause *ReplayError) {
		if err := r.Jobs.FinishReplay(ctx, j.Org, j.ID, r.Worker, status, res, cause); err != nil && !errors.Is(err, ErrLeaseLost) {
			log.Warn("learn: recording a replay's end failed", "err", err)
		}
	}
	cand, base, cerr := candidateOf(job)
	if cerr != nil {
		finish(ReplayFailed, nil, &ReplayError{Code: "candidate_invalid", Message: cerr.Error()})
		return
	}
	jctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stopped := false
	progress := func(processed, total int64) {
		if r.afterBatch != nil && !r.afterBatch(processed) {
			stopped = true
			cancel(errKilled)
			return
		}
		c, err := r.Jobs.RenewReplay(ctx, j.Org, j.ID, r.Worker, processed, total, r.Lease)
		switch {
		case errors.Is(err, ErrLeaseLost):
			cancel(ErrLeaseLost)
		case err != nil:
			log.Warn("learn: renewing a replay's lease failed", "err", err)
		case c:
			cancel(errCancelRequested)
		}
	}
	res, err := r.Engine.Run(jctx, j.Org, cand, base, job.Window, progress)
	switch cause := context.Cause(jctx); {
	case stopped:
		return // a killed runner: the lease expires and another claims it
	case errors.Is(cause, ErrLeaseLost):
		return
	case errors.Is(cause, errCancelRequested):
		finish(ReplayCancelled, nil, nil)
		return
	}
	var tl *TooLargeError
	switch {
	case errors.As(err, &tl):
		finish(ReplayFailed, nil, &ReplayError{Code: "replay_too_large", Message: ReplayTooLargeMessage(tl.Actions)})
	case errors.Is(err, ErrWindowInvalid):
		finish(ReplayFailed, nil, &ReplayError{Code: "replay_window_invalid", Message: MsgWindowInvalid})
	case err != nil:
		log.Warn("learn: a replay failed", "err", err)
		finish(ReplayFailed, nil, &ReplayError{Code: "internal_error", Message: "The replay stopped on a server error. Start it again; the result will be the same."})
	default:
		finish(ReplaySucceeded, res, nil)
	}
}

var (
	errKilled          = errors.New("learn: stopped by a test seam")
	errCancelRequested = errors.New("learn: a cancel was requested")
)

// candidateOf compiles the job's candidate (and baseline) and checks them against what was
// stored when the job was created: a version whose hash differs fails closed.
func candidateOf(j *StoredReplay) (Candidate, Baseline, error) {
	c, diags := policy.Compile(j.Source, policy.Options{})
	if c == nil {
		return Candidate{}, Baseline{}, errors.New("the candidate no longer compiles: " + firstDiag(diags))
	}
	if j.Candidate.VersionHash != "" && c.VersionHash != j.Candidate.VersionHash { //deedglass:allow-compare a policy version's public digest
		return Candidate{}, Baseline{}, errors.New("the candidate's version hash changed; replay a stored version again")
	}
	cand := Candidate{Compiled: c, PolicyID: j.Candidate.PolicyID, VersionID: j.Candidate.VersionID, Version: j.Candidate.Version,
		SourceHash: j.Candidate.SourceHash}
	base := Baseline{Type: j.Baseline.Type}
	if base.Type == BaselineVersion {
		bc, bd := policy.Compile(j.BaselineSource, policy.Options{})
		if bc == nil {
			return Candidate{}, Baseline{}, errors.New("the baseline version no longer compiles: " + firstDiag(bd))
		}
		if j.Baseline.VersionHash != "" && bc.VersionHash != j.Baseline.VersionHash { //deedglass:allow-compare a policy version's public digest
			return Candidate{}, Baseline{}, errors.New("the baseline version's hash changed")
		}
		base.Compiled, base.VersionID, base.Version = bc, j.Baseline.VersionID, j.Baseline.Version
	}
	return cand, base, nil
}

func firstDiag(ds []policy.Diagnostic) string {
	for _, d := range ds {
		if d.Severity == policy.SevError {
			return d.Message
		}
	}
	return "unknown"
}

// ProgressOf fills a replay's progress.
func ProgressOf(processed int64, total *int64) ReplayProgress {
	p := ReplayProgress{Processed: processed, Total: total}
	if total != nil && *total > 0 {
		p.Percent = int(min(100, processed*100 / *total))
	} else if total != nil {
		p.Percent = 100
	}
	return p
}

// MarshalResult is the stored result column.
func MarshalResult(r *ReplayResult) ([]byte, error) { return json.Marshal(r) }
