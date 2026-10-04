package learn

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// The detect runner (S27 §3.7): every 5 minutes, for each org with actions decided in the last 2
// hours or with anomaly facts on, it takes the org's advisory lock, reads the trailing 60 minutes
// and the hourly baseline (cached for the hour), runs the six pure detectors, and upserts
// episodes. Backfill walks a past range hour by hour with `now` as an argument, so simulated
// history gets its anomalies at the right virtual times.

// Detect timing.
const (
	DetectEvery     = 5 * time.Minute
	DetectRecent    = 2 * time.Hour
	DetectWindowLen = time.Hour
	BaselineHours   = 336 // 14 days
	ToolsLookback   = 30 * 24 * time.Hour
	RefLookback     = 30 * 24 * time.Hour
	RunsKeep        = 7 * 24 * time.Hour
	MaxBackfill     = 30 * 24 * time.Hour
)

// Detector runs anomaly detection.
type Detector struct {
	Store AnomalyStore
	Every time.Duration
	Log   *slog.Logger
	Now   func() time.Time
	// OnPublished is called after an org's pass that published anomaly.detected (tests, metrics).
	OnPublished func(org ids.OrgID, n int)

	mu      sync.Mutex
	cache   map[ids.OrgID]*baselineCache
	lastOK  time.Time
	lastErr error
}

// baselineCache is one org's bucket rows for one org-local hour.
type baselineCache struct {
	hour    time.Time
	from    time.Time
	buckets []BucketRow
	refs    []RefRow
}

// PassStats counts one pass (or backfill) over one or more orgs.
type PassStats struct {
	Orgs      int `json:"orgs"`
	Windows   int `json:"windows"`
	Findings  int `json:"findings"`
	Inserted  int `json:"inserted"`
	Extended  int `json:"extended"`
	Published int `json:"published"`
	Closed    int `json:"closed"`
	Skipped   int `json:"skipped"` // orgs another instance was already passing
	Errors    int `json:"errors"`
}

func (d *Detector) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d *Detector) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// Run passes every Every (5 minutes) until ctx ends.
func (d *Detector) Run(ctx context.Context) error {
	every := d.Every
	if every <= 0 {
		every = DetectEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		d.Pass(ctx, d.now())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

// Health is "ok", or "degraded" when the last pass failed or is older than two periods. An
// instance that never passed yet (just started) is ok.
func (d *Detector) Health() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	every := d.Every
	if every <= 0 {
		every = DetectEvery
	}
	if d.lastErr != nil || (!d.lastOK.IsZero() && d.now().Sub(d.lastOK) > 2*every+time.Minute) {
		return "degraded"
	}
	return "ok"
}

// Pass runs detection for every org that needs it, ending at now.
func (d *Detector) Pass(ctx context.Context, now time.Time) PassStats {
	var st PassStats
	orgs, err := d.Store.DetectOrgs(ctx, DetectRecent)
	if err != nil {
		d.mu.Lock()
		d.lastErr = err
		d.mu.Unlock()
		if ctx.Err() == nil {
			d.log().Warn("learn: listing orgs for anomaly detection failed", "err", err)
		}
		st.Errors++
		return st
	}
	var firstErr error
	for _, org := range orgs {
		s, err := d.DetectOrg(ctx, org, now)
		add(&st, s)
		if err != nil {
			st.Errors++
			if firstErr == nil {
				firstErr = err
			}
			if ctx.Err() == nil {
				d.log().Warn("learn: anomaly detection failed for an org", "org_id", org.String(), "err", err)
			}
		}
	}
	d.mu.Lock()
	d.lastErr = firstErr
	if firstErr == nil {
		d.lastOK = d.now()
	}
	d.mu.Unlock()
	return st
}

func add(a *PassStats, b PassStats) {
	a.Orgs += b.Orgs
	a.Windows += b.Windows
	a.Findings += b.Findings
	a.Inserted += b.Inserted
	a.Extended += b.Extended
	a.Published += b.Published
	a.Closed += b.Closed
	a.Skipped += b.Skipped
	a.Errors += b.Errors
}

// DetectOrg runs one live pass for one org: the trailing 60 minutes ending at now.
func (d *Detector) DetectOrg(ctx context.Context, org ids.OrgID, now time.Time) (PassStats, error) {
	info, err := d.Store.Org(ctx, org)
	if err != nil {
		return PassStats{}, err
	}
	loc := loadLoc(info.Timezone)
	started := d.now()
	var st PassStats
	locked, err := d.Store.WithDetectLock(ctx, org, func(ctx context.Context, tx DetectTx) error {
		st = PassStats{Orgs: 1}
		agents, err := tx.Agents(ctx)
		if err != nil {
			return err
		}
		hour := floorLocalHour(now.Add(-DetectWindowLen), loc)
		base, err := d.baseline(ctx, org, tx, info.Timezone, hour)
		if err != nil {
			return err
		}
		ws := now.Add(-DetectWindowLen)
		cur, err := tx.Window(ctx, ws, now)
		if err != nil {
			return err
		}
		curRefs, err := tx.WindowRefs(ctx, ws.Add(-RefLookback), ws, now)
		if err != nil {
			return err
		}
		if err := d.windowPass(ctx, tx, &st, info, loc, agents, base.buckets, base.refs, cur, curRefs, ws, now, hour); err != nil {
			return err
		}
		n, err := tx.CloseStale(ctx, now.Add(-EpisodeCloseAfter), now)
		if err != nil {
			return err
		}
		st.Closed = n
		if err := tx.PruneRuns(ctx, "detect", now.Add(-RunsKeep)); err != nil {
			return err
		}
		return tx.RecordRun(ctx, "detect", now.Truncate(DetectEvery).Format("2006-01-02T15:04Z"), started, d.now(), "succeeded", statsMap(st), "")
	})
	if err != nil {
		return st, err
	}
	if !locked {
		return PassStats{Skipped: 1}, nil
	}
	if st.Published > 0 && d.OnPublished != nil {
		d.OnPublished(org, st.Published)
	}
	return st, nil
}

// Backfill walks [from, to) one org-local hour at a time, each hour judged as if it were now
// (dev: simulated history). It is idempotent: a window already recorded is skipped.
func (d *Detector) Backfill(ctx context.Context, org ids.OrgID, from, to time.Time) (PassStats, error) {
	if !from.Before(to) || to.Sub(from) > MaxBackfill {
		return PassStats{}, errors.New("learn: a backfill covers 1 hour to 30 days")
	}
	info, err := d.Store.Org(ctx, org)
	if err != nil {
		return PassStats{}, err
	}
	loc := loadLoc(info.Timezone)
	started := d.now()
	start := floorLocalHour(from, loc)
	var st PassStats
	locked, err := d.Store.WithDetectLock(ctx, org, func(ctx context.Context, tx DetectTx) error {
		st = PassStats{Orgs: 1}
		agents, err := tx.Agents(ctx)
		if err != nil {
			return err
		}
		lookback := start.Add(-max(ToolsLookback, RefLookback))
		buckets, err := tx.Buckets(ctx, info.Timezone, lookback, to)
		if err != nil {
			return err
		}
		refs, err := tx.RefBuckets(ctx, info.Timezone, lookback, start.Add(-BaselineHours*time.Hour), to)
		if err != nil {
			return err
		}
		for h := start; h.Before(to); h = nextLocalHour(h, loc) {
			end := nextLocalHour(h, loc)
			var cur []BucketRow
			for _, b := range buckets {
				if b.Start.Equal(h) {
					cur = append(cur, b)
				}
			}
			var curRefs []RefRow
			for _, r := range refs {
				if r.Start.Equal(h) {
					curRefs = append(curRefs, r)
				}
			}
			if err := d.windowPass(ctx, tx, &st, info, loc, agents, buckets, refs, cur, curRefs, h, end, h); err != nil {
				return err
			}
		}
		n, err := tx.CloseStale(ctx, to.Add(-EpisodeCloseAfter), to)
		if err != nil {
			return err
		}
		st.Closed = n
		return tx.RecordRun(ctx, "detect", "bf:"+to.UTC().Format("2006-01-02T15:04Z"), started, d.now(), "succeeded", statsMap(st), "")
	})
	if err != nil {
		return st, err
	}
	if !locked {
		return PassStats{Skipped: 1}, errors.New("learn: another detection pass holds this org's lock; try again in a minute")
	}
	return st, nil
}

// baseline returns the org's bucket rows for the 30 days before hour, cached for the hour.
func (d *Detector) baseline(ctx context.Context, org ids.OrgID, tx DetectTx, tz string, hour time.Time) (*baselineCache, error) {
	d.mu.Lock()
	if d.cache == nil {
		d.cache = map[ids.OrgID]*baselineCache{}
	}
	c := d.cache[org]
	d.mu.Unlock()
	if c != nil && c.hour.Equal(hour) {
		return c, nil
	}
	from := hour.Add(-max(ToolsLookback, RefLookback))
	buckets, err := tx.Buckets(ctx, tz, from, hour)
	if err != nil {
		return nil, err
	}
	refs, err := tx.RefBuckets(ctx, tz, from, hour.Add(-BaselineHours*time.Hour), hour)
	if err != nil {
		return nil, err
	}
	c = &baselineCache{hour: hour, from: from, buckets: buckets, refs: refs}
	d.mu.Lock()
	d.cache[org] = c
	d.mu.Unlock()
	return c, nil
}

// windowPass judges one window [ws, we) for every agent and records what fires. hour is the
// org-local hour the baseline ends at (exclusive).
func (d *Detector) windowPass(ctx context.Context, tx DetectTx, st *PassStats, info OrgInfo, loc *time.Location, agents []AgentInfo,
	buckets []BucketRow, refs []RefRow, cur []BucketRow, curRefs []RefRow, ws, we, hour time.Time) error {
	st.Windows++
	for _, a := range agents {
		if a.Kind == "system" {
			continue
		}
		h := buildHistory(a, buckets, refs, hour, loc)
		w := DetectWindow{Start: ws, End: we, Current: currentBucket(a.ID, ws, cur, curRefs)}
		if w.Current.Actions == 0 {
			continue
		}
		fs := Detect(h, w, loc)
		if len(fs) == 0 {
			continue
		}
		// Evidence lists up to 10 action ids: read them only when something fired, then decide again
		// with the samples (the detectors are pure, so the findings are the same).
		if err := fillSamples(ctx, tx, &w, a.ID, fs, ws.Add(-RefLookback)); err != nil {
			return err
		}
		fs = Detect(h, w, loc)
		for _, f := range fs {
			st.Findings++
			res, err := tx.UpsertEpisode(ctx, EpisodeInput{Agent: a.ID, AgentName: a.Name, Finding: f, Window: w,
				ActiveMinutes: info.Settings.AnomalyActiveMinutes, Now: we})
			if err != nil {
				return err
			}
			switch {
			case res.Inserted:
				st.Inserted++
			case res.Extended:
				st.Extended++
			}
			if res.Published {
				st.Published++
			}
		}
	}
	return nil
}

// fillSamples reads the action ids the findings' evidence will list.
func fillSamples(ctx context.Context, tx DetectTx, w *DetectWindow, agent string, fs []Finding, lookback time.Time) error {
	s := &w.Current.Samples
	need := map[SampleKind]bool{}
	tools := map[string]bool{}
	for _, f := range fs {
		switch f.Kind {
		case AnomalyNewTool:
			tools[f.Tool] = true
		case AnomalyDenySpike:
			need[SampleDenied] = true
		case AnomalyMismatchSpike:
			need[SampleMismatch] = true
		case AnomalyUserRefBurst:
			need[SampleNewRefs] = true
		default:
			need[SampleAll] = true
		}
	}
	read := func(k SampleKind, tool string) ([]string, error) {
		return tx.Samples(ctx, agent, w.Start, w.End, k, tool, lookback, MaxEvidenceActionIDs)
	}
	var err error
	if need[SampleAll] {
		if s.All, err = read(SampleAll, ""); err != nil {
			return err
		}
	}
	if need[SampleDenied] {
		if s.Denied, err = read(SampleDenied, ""); err != nil {
			return err
		}
	}
	if need[SampleMismatch] {
		if s.Mismatch, err = read(SampleMismatch, ""); err != nil {
			return err
		}
	}
	if need[SampleNewRefs] {
		if s.NewUserRefs, err = read(SampleNewRefs, ""); err != nil {
			return err
		}
	}
	if len(tools) > 0 {
		s.ByTool = map[string][]string{}
		names := make([]string, 0, len(tools))
		for t := range tools {
			names = append(names, t)
		}
		sort.Strings(names)
		for _, t := range names {
			ids, err := read(SampleTool, t)
			if err != nil {
				return err
			}
			s.ByTool[t] = ids
		}
	}
	return nil
}

// sideName maps a side-effect rank back to its word.
func sideName(rank int) string {
	switch rank {
	case 1:
		return SideRead
	case 3:
		return SideWrite
	case 4:
		return SideDestructive
	}
	return SideUnknown
}

// currentBucket folds the window's rows of one agent.
func currentBucket(agent string, start time.Time, rows []BucketRow, refs []RefRow) HourBucket {
	b := HourBucket{Start: start, Tools: map[string]int{}, ToolSide: map[string]string{}}
	for _, r := range rows {
		if r.Agent != agent {
			continue
		}
		addRow(&b, r)
	}
	for _, r := range refs {
		if r.Agent == agent {
			b.NewUserRefs += r.NewRefs
		}
	}
	return b
}

func addRow(b *HourBucket, r BucketRow) {
	b.Actions += r.Actions
	b.Denied += r.Denied
	b.Mismatch += r.Mismatch
	b.Writes += r.Writes
	b.Tools[r.Tool] += r.Actions
	if prev, ok := b.ToolSide[r.Tool]; !ok || sideRank(prev) < r.SideRank {
		b.ToolSide[r.Tool] = sideName(r.SideRank)
	}
}

func sideRank(s string) int {
	switch s {
	case SideRead:
		return 1
	case SideWrite:
		return 3
	case SideDestructive:
		return 4
	}
	return 2
}

// buildHistory is one agent's 14 days of hourly buckets before hour, zero-filled, plus the
// 30-day tool set and the hour-of-day shares.
func buildHistory(a AgentInfo, rows []BucketRow, refs []RefRow, hour time.Time, loc *time.Location) AgentHistory {
	h := AgentHistory{Agent: a.ID, Name: a.Name, CreatedAt: a.CreatedAt, ToolsSeen30d: map[string]bool{}}
	starts := make([]time.Time, 0, BaselineHours)
	t := hour
	for range BaselineHours {
		t = prevLocalHour(t, loc)
		starts = append(starts, t)
	}
	// oldest first
	for i, j := 0, len(starts)-1; i < j; i, j = i+1, j-1 {
		starts[i], starts[j] = starts[j], starts[i]
	}
	index := make(map[int64]int, len(starts))
	h.Hours = make([]HourBucket, len(starts))
	for i, s := range starts {
		h.Hours[i] = HourBucket{Start: s, Tools: map[string]int{}, ToolSide: map[string]string{}}
		index[s.Unix()] = i
	}
	first := starts[0]
	toolsFrom := hour.Add(-ToolsLookback)
	days := map[string]bool{}
	var perHour [24]int
	for _, r := range rows {
		if r.Agent != a.ID || !r.Start.Before(hour) {
			continue
		}
		if !r.Start.Before(toolsFrom) {
			h.ToolsSeen30d[r.Tool] = true
		}
		if r.Start.Before(first) {
			continue
		}
		i, ok := index[r.Start.Unix()]
		if !ok {
			continue
		}
		addRow(&h.Hours[i], r)
		h.Total += r.Actions
		lt := r.Start.In(loc)
		perHour[lt.Hour()] += r.Actions
		if r.Actions > 0 {
			days[lt.Format("2006-01-02")] = true
		}
	}
	for _, r := range refs {
		if r.Agent != a.ID {
			continue
		}
		if i, ok := index[r.Start.Unix()]; ok {
			h.Hours[i].NewUserRefs += r.NewRefs
		}
	}
	h.ActiveDays = len(days)
	if h.Total > 0 {
		for i := range perHour {
			h.HourOfDayShare[i] = float64(perHour[i]) / float64(h.Total)
		}
	}
	return h
}

// floorLocalHour is the start of the org-local hour that contains t.
func floorLocalHour(t time.Time, loc *time.Location) time.Time {
	lt := t.In(loc)
	return time.Date(lt.Year(), lt.Month(), lt.Day(), lt.Hour(), 0, 0, 0, loc).UTC()
}

// nextLocalHour is the start of the next org-local hour (an hour later, also across DST).
func nextLocalHour(t time.Time, loc *time.Location) time.Time {
	return floorLocalHour(t.Add(time.Hour), loc)
}

// prevLocalHour is the start of the org-local hour before the one starting at t.
func prevLocalHour(t time.Time, loc *time.Location) time.Time {
	return floorLocalHour(t.Add(-time.Minute), loc)
}

func loadLoc(tz string) *time.Location {
	if tz == "" {
		return time.UTC
	}
	l, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return l
}

func statsMap(st PassStats) map[string]any {
	return map[string]any{"windows": st.Windows, "findings": st.Findings, "inserted": st.Inserted, "extended": st.Extended,
		"published": st.Published, "closed": st.Closed}
}
