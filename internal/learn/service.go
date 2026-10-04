package learn

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// Store is every port Insights reads and writes (store/learnstore.Store implements it).
type Store interface {
	JobStore
	ShadowStore
	StatsStore
	AnomalyStore
	// ResolveVersion finds version n of a policy named by pol_ id or name. Found is false when the
	// policy exists but not that version (Latest says how many there are); ErrNotFound when the
	// policy doesn't exist.
	ResolveVersion(ctx context.Context, org ids.OrgID, policyRef string, version int) (*ResolvedVersion, error)
	// ShadowSource is the org's active shadow version and its source (nil when none).
	ShadowSource(ctx context.Context, org ids.OrgID) (*VersionInfo, []byte, error)
}

// ResolvedVersion is a stored policy version with its source.
type ResolvedVersion struct {
	VersionInfo
	VersionHash string
	Source      []byte
	Latest      int
	Found       bool
}

// Service is Insights' API-facing service (S27): replays, shadow, stats, anomalies and settings.
// Insights reads and reports; its only input into decisions is the opt-in anomaly facts.
type Service struct {
	Store  Store
	Inputs InputSource
	Runner *Runner // Wake on create; nil in tests that run jobs by hand
	Facts  *FactsCache
	Now    func() time.Time
	Log    *slog.Logger
	// StatsTTL caches stats answers per org and query (60 s; negative turns it off).
	StatsTTL time.Duration

	mu       sync.Mutex
	stats    map[string]statsEntry
	compiled map[string]*policy.Compiled // by version hash
}

type statsEntry struct {
	at  time.Time
	obj *StatsObject
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// compile compiles a stored source once per version hash.
func (s *Service) compile(src []byte) (*policy.Compiled, error) {
	c, diags := policy.Compile(src, policy.Options{})
	if c == nil {
		return nil, errors.New("learn: a stored version no longer compiles: " + firstDiag(diags))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.compiled == nil {
		s.compiled = map[string]*policy.Compiled{}
	}
	if x, ok := s.compiled[c.VersionHash]; ok {
		return x, nil
	}
	s.compiled[c.VersionHash] = c
	return c, nil
}

// ReplayRequest is POST /v1/policies/{id}/versions/{v}/replay's body.
type ReplayRequest struct {
	From     *time.Time `json:"from"`
	To       *time.Time `json:"to"`
	Baseline *struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
	} `json:"baseline"`
}

// CreateReplay queues a replay of version `version` of policyRef (S27 §3.4).
func (s *Service) CreateReplay(ctx context.Context, org ids.OrgID, by Actor, policyRef string, version int, req ReplayRequest) (*Replay, error) {
	now := s.now()
	w := Window{To: now, From: now.Add(-7 * 24 * time.Hour)}
	if req.To != nil {
		w.To = req.To.UTC()
	}
	if req.From != nil {
		w.From = req.From.UTC()
	} else if req.To != nil {
		w.From = w.To.Add(-7 * 24 * time.Hour)
	}
	if err := CheckWindow(w); err != nil {
		param := "from"
		if req.To != nil && req.From != nil && !req.From.Before(*req.To) {
			param = "to"
		}
		return nil, httpx.E(httpx.CodeReplayWindowInvalid, httpx.Param(param), httpx.Msg(MsgWindowInvalid))
	}
	rv, err := s.Store.ResolveVersion(ctx, org, policyRef, version)
	if errors.Is(err, ErrNotFound) {
		return nil, httpx.E(httpx.CodeNotFound, httpx.Msgf("No policy %s in this org. List them: deedglass policy list", clipID(policyRef)))
	}
	if err != nil {
		return nil, err
	}
	if !rv.Found {
		return nil, httpx.E(httpx.CodeNotFound, httpx.Param("version"), httpx.Msg(UnknownVersionMessage(rv.Name, version, rv.Latest)))
	}
	n := NewReplay{PolicyID: rv.PolicyID, VersionID: rv.VersionID, Version: rv.Version, Name: rv.Name, VersionHash: rv.VersionHash,
		Window: w, Purpose: "manual", RequestedBy: by, Baseline: ReplayBaseline{Type: BaselineRecorded}}
	if req.Baseline != nil {
		switch req.Baseline.Type {
		case "", BaselineRecorded:
		case BaselineVersion:
			if req.Baseline.Version < 1 {
				return nil, httpx.ErrValidation("baseline.version", MsgBaselineType)
			}
			bv, err := s.Store.ResolveVersion(ctx, org, rv.PolicyID, req.Baseline.Version)
			if err != nil {
				return nil, err
			}
			if !bv.Found {
				return nil, httpx.E(httpx.CodeNotFound, httpx.Param("baseline.version"), httpx.Msg(UnknownVersionMessage(rv.Name, req.Baseline.Version, bv.Latest)))
			}
			n.Baseline = ReplayBaseline{Type: BaselineVersion, VersionID: bv.VersionID, Version: bv.Version, VersionHash: bv.VersionHash}
		default:
			return nil, httpx.ErrValidation("baseline.type", MsgBaselineType)
		}
	}
	recent, err := s.Store.RecentReplays(ctx, org, now.Add(-time.Hour))
	if err != nil {
		return nil, err
	}
	if recent >= ReplaysPerHourPerOrg {
		e := httpx.ErrRateLimited(300)
		e.Message = MsgReplayRateLimited
		return nil, e
	}
	total, err := s.Inputs.Count(ctx, org, w)
	if err != nil {
		return nil, err
	}
	if total > MaxReplayActions {
		return nil, httpx.E(httpx.CodeReplayTooLarge, httpx.Msg(ReplayTooLargeMessage(total)))
	}
	n.Total = total
	r, err := s.Store.CreateReplay(ctx, org, n)
	if err != nil {
		return nil, err
	}
	if s.Runner != nil {
		s.Runner.Wake()
	}
	return r, nil
}

// GetReplay reads one replay.
func (s *Service) GetReplay(ctx context.Context, org ids.OrgID, id string) (*Replay, error) {
	if _, err := ids.Parse[ids.ReplayKind](id); err != nil {
		return nil, replayNotFound(id)
	}
	r, err := s.Store.GetReplay(ctx, org, id)
	if errors.Is(err, ErrNotFound) {
		return nil, replayNotFound(id)
	}
	return r, err
}

// CancelReplay asks a queued or running replay to stop (409 conflict once it finished).
func (s *Service) CancelReplay(ctx context.Context, org ids.OrgID, id string, by Actor) (*Replay, error) {
	if _, err := ids.Parse[ids.ReplayKind](id); err != nil {
		return nil, replayNotFound(id)
	}
	r, err := s.Store.CancelReplay(ctx, org, id, by, s.now())
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, replayNotFound(id)
	case errors.Is(err, ErrStateConflict):
		return nil, httpx.E(httpx.CodeConflict, httpx.Msg(MsgReplayFinished))
	}
	return r, err
}

func replayNotFound(id string) error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("No replay %s in this org. List recent replays: GET /v1/replays.", clipID(id)))
}

func clipID(s string) string {
	if utf8.RuneCountInString(s) > 64 {
		return string([]rune(s)[:64]) + "…"
	}
	return s
}

// Shadow is the shadow disagreement report (S27 §3.5).
func (s *Service) Shadow(ctx context.Context, org ids.OrgID, q ShadowQuery) (*ShadowReport, error) {
	var v *VersionInfo
	var err error
	if q.ShadowVersionID != "" {
		v, err = s.Store.Version(ctx, org, q.ShadowVersionID)
		if err == nil && v == nil {
			return nil, httpx.E(httpx.CodeNotFound, httpx.Param("shadow_version"), httpx.Msgf("No policy version %s in this org.", clipID(q.ShadowVersionID)))
		}
	} else {
		v, err = s.Store.LiveShadow(ctx, org)
		if err == nil && v == nil {
			return nil, httpx.E(httpx.CodeNotFound, httpx.Msg(MsgNoShadow))
		}
	}
	if err != nil {
		return nil, err
	}
	w := NullableWindow{From: q.From, To: q.To}
	rows, err := s.Store.ShadowRows(ctx, org, v.VersionID, w)
	if err != nil {
		return nil, err
	}
	samples, err := s.Store.ShadowSamples(ctx, org, v.VersionID, w, MaxSamples)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var vids []string
	for _, r := range rows {
		if r.EnforceVersionID != "" && !seen[r.EnforceVersionID] {
			seen[r.EnforceVersionID] = true
			vids = append(vids, r.EnforceVersionID)
		}
	}
	names, err := s.Store.VersionsByID(ctx, org, vids)
	if err != nil {
		return nil, err
	}
	return BuildShadowReport(*v, names, rows, samples, w), nil
}

// Stats computes the insights_stats object (S27 §3.6), cached 60 s per org and query.
func (s *Service) Stats(ctx context.Context, org ids.OrgID, q StatsQuery) (*StatsObject, error) {
	span := q.From.Format(time.RFC3339Nano) + "|" + q.To.Format(time.RFC3339Nano)
	if q.Relative != "" {
		span = "last " + q.Relative // a relative window: a repeat within the TTL shares the answer
	}
	key := org.String() + "|" + span + "|" + q.By + "|" + q.Agent + "|" + q.Tool + "|" + strconv.FormatBool(q.Compare)
	ttl := s.StatsTTL
	if ttl == 0 {
		ttl = time.Minute
	}
	now := s.now()
	if ttl > 0 {
		s.mu.Lock()
		e, ok := s.stats[key]
		s.mu.Unlock()
		if ok && now.Sub(e.at) < ttl {
			return e.obj, nil
		}
	}
	obj, err := s.computeStats(ctx, org, q, now)
	if err != nil {
		return nil, err
	}
	if ttl > 0 {
		s.mu.Lock()
		if s.stats == nil || len(s.stats) > 1000 {
			s.stats = map[string]statsEntry{}
		}
		s.stats[key] = statsEntry{at: now, obj: obj}
		s.mu.Unlock()
	}
	return obj, nil
}

func (s *Service) computeStats(ctx context.Context, org ids.OrgID, q StatsQuery, now time.Time) (*StatsObject, error) {
	info, err := s.Store.Org(ctx, org)
	if err != nil {
		return nil, err
	}
	loc := loadLoc(info.Timezone)
	// The reads are independent; they run side by side (the store bounds its own parallelism).
	var (
		rows, prows    []StatsRow
		total, prevTot StatsRow
		series         []SeriesRow
		counters       []DayCounter
		agents         []AgentBudgetRow
		enforcedSrc    []byte
		pq             = q
	)
	pq.To, pq.From = q.From, q.From.Add(-q.To.Sub(q.From))
	fns := []func() error{
		func() (err error) { rows, total, err = s.Store.StatsGroups(ctx, org, q, info.Timezone); return },
		func() (err error) { series, err = s.Store.StatsSeries(ctx, org, q, info.Timezone); return },
		func() (err error) { counters, err = s.Store.DayCounters(ctx, org, q.From, q.To, info.Timezone); return },
		func() (err error) { agents, err = s.Store.AgentBudgets(ctx, org); return },
		func() (err error) { _, enforcedSrc, err = s.Store.EnforcedSource(ctx, org); return },
	}
	if q.Compare {
		fns = append(fns, func() (err error) { prows, prevTot, err = s.Store.StatsGroups(ctx, org, pq, info.Timezone); return })
	}
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, f := range fns {
		wg.Go(func() { errs[i] = f() })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	obj := &StatsObject{Object: "insights_stats", Timezone: info.Timezone, By: q.By, GeneratedAt: now, Groups: []Group{},
		Window: StatsWindow{From: q.From.UTC(), To: q.To.UTC(), Days: round4(q.To.Sub(q.From).Hours() / 24)}}
	if q.Agent != "" {
		a := q.Agent
		obj.Filters.Agent = &a
	}
	if q.Tool != "" {
		t := q.Tool
		obj.Filters.Tool = &t
	}
	obj.Totals = BuildGroup(total, ByNone)
	var prev map[string]StatsRow
	if q.Compare {
		prev = map[string]StatsRow{}
		for _, r := range prows {
			prev[r.Key] = r
		}
		obj.Totals.Trend = TrendOf(obj.Totals, BuildGroup(prevTot, ByNone))
	}
	if q.By != ByNone {
		for _, r := range rows {
			g := BuildGroup(r, q.By)
			if q.Compare {
				g.Trend = TrendOf(g, BuildGroup(prev[r.Key], q.By))
			}
			obj.Groups = append(obj.Groups, g)
		}
	}
	obj.Series = series
	if obj.Series == nil {
		obj.Series = []SeriesRow{}
	}
	var enforced *policy.Compiled
	if enforcedSrc != nil {
		if enforced, err = s.compile(enforcedSrc); err != nil {
			s.log().Warn("learn: the enforced version doesn't compile for stats; budgets left out", "org_id", org.String(), "err", err)
		}
	}
	obj.Budgets = BudgetUses(counters, agents, enforced, loc)
	if obj.Totals.Actions == 0 {
		e := StatsEmpty(q.From, q.To, loc)
		obj.Empty = &e
	}
	return obj, nil
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Anomalies lists episodes, with the inbox's header facts.
func (s *Service) Anomalies(ctx context.Context, org ids.OrgID, f AnomalyFilter, limit int, cursor string) (*AnomalyList, error) {
	now := s.now()
	data, next, err := s.Store.ListAnomalies(ctx, org, f, limit, cursor, now)
	if err != nil {
		return nil, err
	}
	l := &AnomalyList{Object: "list", Data: data, HasMore: next != "", BaselineBuilding: []BaselineAgent{}}
	if l.Data == nil {
		l.Data = []Anomaly{}
	}
	if next != "" {
		l.NextCursor = &next
	}
	if l.ActiveNow, err = s.Store.CountActive(ctx, org, now); err != nil {
		return nil, err
	}
	so, err := s.SettingsOf(ctx, org)
	if err != nil {
		return nil, err
	}
	l.AnomalyFacts, l.Notice = so.AnomalyFacts, so.Notice
	agents, err := s.Store.AgentAges(ctx, org)
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		if a.Kind == "system" || a.CreatedAt.IsZero() {
			continue
		}
		age := now.Sub(a.CreatedAt)
		if age >= 0 && age < MinHistory {
			h := int(age.Hours())
			l.BaselineBuilding = append(l.BaselineBuilding, BaselineAgent{Agent: a.ID, Name: a.Name, Hours: h, Sentence: BaselineBuildingSentence(a.Name, h)})
		}
	}
	return l, nil
}

// Anomaly reads one episode.
func (s *Service) Anomaly(ctx context.Context, org ids.OrgID, id string) (*Anomaly, error) {
	if _, err := ids.Parse[ids.AnomalyKind](id); err != nil {
		return nil, anomalyNotFound(id)
	}
	a, err := s.Store.GetAnomaly(ctx, org, id, s.now())
	if errors.Is(err, ErrNotFound) {
		return nil, anomalyNotFound(id)
	}
	return a, err
}

// Acknowledge records who saw an episode, and changes nothing else.
func (s *Service) Acknowledge(ctx context.Context, org ids.OrgID, id string, by Actor) (*Anomaly, error) {
	if _, err := ids.Parse[ids.AnomalyKind](id); err != nil {
		return nil, anomalyNotFound(id)
	}
	a, err := s.Store.Acknowledge(ctx, org, id, by, s.now())
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, anomalyNotFound(id)
	case errors.Is(err, ErrStateConflict):
		return nil, httpx.E(httpx.CodeConflict, httpx.Msg(MsgAnomalyResolved))
	}
	return a, err
}

// Resolve closes an episode, ends any activity now and seals anomaly.resolved.
func (s *Service) Resolve(ctx context.Context, org ids.OrgID, id string, by Actor, reason, requestID string) (*Anomaly, error) {
	if err := checkReason(reason, MsgResolveReason); err != nil {
		return nil, err
	}
	if _, err := ids.Parse[ids.AnomalyKind](id); err != nil {
		return nil, anomalyNotFound(id)
	}
	a, err := s.Store.Resolve(ctx, org, id, by, trimReason(reason), requestID, s.now())
	switch {
	case errors.Is(err, ErrNotFound):
		return nil, anomalyNotFound(id)
	case errors.Is(err, ErrStateConflict):
		return nil, httpx.E(httpx.CodeConflict, httpx.Msg(MsgAnomalyResolved))
	case err != nil:
		return nil, err
	}
	if s.Facts != nil {
		s.Facts.Invalidate(org.String())
	}
	return a, nil
}

func anomalyNotFound(id string) error {
	return httpx.E(httpx.CodeNotFound, httpx.Msgf("No anomaly %s in this org. List them: GET /v1/insights/anomalies.", clipID(id)))
}

// SettingsOf is the insights_settings object.
func (s *Service) SettingsOf(ctx context.Context, org ids.OrgID) (*SettingsObject, error) {
	info, err := s.Store.Org(ctx, org)
	if err != nil {
		return nil, err
	}
	o := &SettingsObject{Object: "insights_settings", Settings: info.Settings}
	label, reads, err := s.policyReadsAnomaly(ctx, org)
	if err != nil {
		return nil, err
	}
	o.ActivePolicyReadsAnomaly = reads
	if reads && !info.Settings.AnomalyFacts {
		n := OptInOffNotice(label)
		o.Notice = &n
	}
	return o, nil
}

// policyReadsAnomaly reports whether the enforced or shadow version reads anomaly.*, and its label.
func (s *Service) policyReadsAnomaly(ctx context.Context, org ids.OrgID) (string, bool, error) {
	for _, get := range []func(context.Context, ids.OrgID) (*VersionInfo, []byte, error){s.Store.EnforcedSource, s.Store.ShadowSource} {
		v, src, err := get(ctx, org)
		if err != nil {
			return "", false, err
		}
		if src == nil {
			continue
		}
		c, err := s.compile(src)
		if err != nil {
			continue
		}
		if policy.ReadsAnomaly(c) {
			return v.Name + " v" + strconv.Itoa(v.Version), true, nil
		}
	}
	return "", false, nil
}

// PatchSettings changes Insights settings and seals insights.settings_changed (it changes what
// policies see).
func (s *Service) PatchSettings(ctx context.Context, org ids.OrgID, by Actor, p SettingsPatch, requestID string) (*SettingsObject, error) {
	if p.AnomalyActiveMinutes != nil && (*p.AnomalyActiveMinutes < MinActiveMinutes || *p.AnomalyActiveMinutes > MaxActiveMinutes) {
		return nil, httpx.ErrValidation("anomaly_active_minutes", MsgActiveMinutes)
	}
	if p.AnomalyStaleAfterMinutes != nil && (*p.AnomalyStaleAfterMinutes < MinStaleMinutes || *p.AnomalyStaleAfterMinutes > MaxStaleMinutes) {
		return nil, httpx.ErrValidation("anomaly_stale_after_minutes", MsgStaleMinutes)
	}
	if err := checkReason(p.Reason, MsgSettingsReason); err != nil {
		return nil, err
	}
	info, err := s.Store.Org(ctx, org)
	if err != nil {
		return nil, err
	}
	after := p.Apply(info.Settings)
	var deedID *string
	if after != info.Settings {
		d, err := s.Store.UpdateSettings(ctx, org, info.Settings, after, by, trimReason(p.Reason), requestID)
		if err != nil {
			return nil, err
		}
		deedID = &d
		if s.Facts != nil {
			s.Facts.Invalidate(org.String())
		}
	}
	o, err := s.SettingsOf(ctx, org)
	if err != nil {
		return nil, err
	}
	o.Deed = deedID
	return o, nil
}

// checkReason: required, 3 to 280 characters, no control characters.
func checkReason(r, missing string) error {
	t := trimReason(r)
	n := utf8.RuneCountInString(t)
	switch {
	case n == 0:
		return httpx.ErrValidation("reason", missing)
	case n < 3 || n > 280:
		return httpx.ErrValidation("reason", MsgReasonLength)
	}
	for _, c := range t {
		if c < 0x20 || c == 0x7f || (c >= 0x202a && c <= 0x202e) || (c >= 0x2066 && c <= 0x2069) {
			return httpx.ErrValidation("reason", MsgReasonLength)
		}
	}
	return nil
}

func trimReason(r string) string {
	for len(r) > 0 && (r[0] == ' ' || r[0] == '\t') {
		r = r[1:]
	}
	for len(r) > 0 && (r[len(r)-1] == ' ' || r[len(r)-1] == '\t') {
		r = r[:len(r)-1]
	}
	return r
}
