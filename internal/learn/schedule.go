package learn

import (
	"context"
	"log/slog"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// The schedule loop (S28 §3.4, §3.9) runs in serve on every instance: the daily suggest pass at
// 03:00 org-local, the Monday 09:00 digest, and the accept sweeper. learn_runs makes each daily
// pass and each weekly digest happen exactly once per org across instances.

// Schedule times (org-local) and cadence.
const (
	SuggestHour   = 3
	DigestHour    = 9
	ScheduleEvery = 5 * time.Minute
	// ScheduleFirst is the wait before the first tick after start, so a restarting fleet settles
	// first (and a person's own first generate isn't raced by the daily pass).
	ScheduleFirst = 2 * time.Minute
	// ScheduleRecent: orgs with a decision in this long get the loop (the detect loop's org list).
	ScheduleRecent = 15 * 24 * time.Hour
)

// ScheduleStore is what the loop reads.
type ScheduleStore interface {
	SuggestStore
	DetectOrgs(ctx context.Context, recent time.Duration) ([]ids.OrgID, error)
}

// Scheduler is the loop.
type Scheduler struct {
	Store       ScheduleStore
	Suggestions *SuggestionService
	Digest      *Digester
	Now         func() time.Time
	Every       time.Duration
	First       time.Duration // default ScheduleFirst; negative runs the first tick at once
	Log         *slog.Logger
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Scheduler) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run ticks until ctx ends.
func (s *Scheduler) Run(ctx context.Context) error {
	every := s.Every
	if every <= 0 {
		every = ScheduleEvery
	}
	first := s.First
	if first == 0 {
		first = ScheduleFirst
	}
	if first > 0 {
		w := time.NewTimer(first)
		select {
		case <-ctx.Done():
			w.Stop()
			return ctx.Err()
		case <-w.C:
		}
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		s.Tick(ctx, s.now())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Tick runs what is due at now for every active org.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	orgs, err := s.Store.DetectOrgs(ctx, ScheduleRecent)
	if err != nil {
		if ctx.Err() == nil {
			s.log().Warn("learn: listing orgs for suggestions failed", "err", err)
		}
		return
	}
	for _, org := range orgs {
		if ctx.Err() != nil {
			return
		}
		s.TickOrg(ctx, org, now)
	}
}

// TickOrg runs the sweeper, then the daily pass and the weekly digest when due.
func (s *Scheduler) TickOrg(ctx context.Context, org ids.OrgID, now time.Time) {
	if s.Suggestions != nil {
		if _, err := s.Suggestions.Sweep(ctx, org); err != nil && ctx.Err() == nil {
			s.log().Warn("learn: finishing stuck accepts failed", "org_id", org.String(), "err", err)
		}
	}
	info, err := s.Store.Org(ctx, org)
	if err != nil {
		return
	}
	local := now.In(loadLoc(info.Timezone))
	if s.Suggestions != nil && local.Hour() >= SuggestHour {
		period := local.Format("2006-01-02")
		if ok, err := s.Store.ClaimRun(ctx, org, "suggest", period, now); err == nil && ok {
			run, err := s.Suggestions.Suggester.Generate(ctx, org, Actor{Type: "system", ID: "deedglass"})
			if err != nil {
				_ = s.Store.FinishRun(context.WithoutCancel(ctx), org, "suggest", period, false, nil, err.Error(), s.now())
				if ctx.Err() == nil {
					s.log().Warn("learn: the daily suggest pass failed", "org_id", org.String(), "err", err)
				}
			} else {
				_ = s.Store.FinishRun(ctx, org, "suggest", period, true, runStats(run), "", s.now())
			}
		}
	}
	if s.Digest != nil && local.Weekday() == time.Monday && local.Hour() >= DigestHour {
		// The digest covers the ISO week that ended this Monday at 00:00.
		monday := weekStart(local, local.Location()).AddDate(0, 0, -7)
		if _, err := s.Digest.Send(ctx, s.Store, org, monday, now); err != nil && ctx.Err() == nil {
			s.log().Warn("learn: the weekly digest failed", "org_id", org.String(), "err", err)
		}
	}
}
