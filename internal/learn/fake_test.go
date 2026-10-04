package learn

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// fakeStore is a Store for unit tests: each test fills what it needs; anything else panics through
// the nil embedded interface.
type fakeStore struct {
	Store
	mu sync.Mutex

	org      OrgInfo
	orgErr   error
	versions map[string]*ResolvedVersion // "ref|n"
	enforced *VersionInfo
	enfSrc   []byte
	shadow   *VersionInfo
	shSrc    []byte
	recent   int
	created  []NewReplay
	replays  map[string]*Replay
	cancelEr error

	shadowRows    []ShadowRow
	shadowSamples []ShadowSample

	groups   map[string][]StatsRow // by
	prev     map[string][]StatsRow // by, for the previous window
	series   []SeriesRow
	counters []DayCounter
	agentsB  []AgentBudgetRow

	anomalies []Anomaly
	active    int
	ages      []AgentInfo
	ackErr    error
	resErr    error
	settings  []Settings
	deedID    string

	factsSet   Settings
	factsEps   []ActiveEpisode
	factsLast  time.Time
	factsErr   error
	factsCalls int

	detectOrgs []ids.OrgID
	tx         *fakeTx
	locked     bool
}

func (f *fakeStore) Org(context.Context, ids.OrgID) (OrgInfo, error) { return f.org, f.orgErr }

func (f *fakeStore) ResolveVersion(_ context.Context, _ ids.OrgID, ref string, n int) (*ResolvedVersion, error) {
	if v, ok := f.versions[ref+"|"+itoa(n)]; ok {
		return v, nil
	}
	for k, v := range f.versions {
		if len(k) > len(ref) && k[:len(ref)] == ref {
			return &ResolvedVersion{VersionInfo: VersionInfo{PolicyID: v.PolicyID, Name: v.Name}, Latest: v.Latest}, nil
		}
	}
	return nil, ErrNotFound
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func (f *fakeStore) EnforcedSource(context.Context, ids.OrgID) (*VersionInfo, []byte, error) {
	return f.enforced, f.enfSrc, nil
}
func (f *fakeStore) ShadowSource(context.Context, ids.OrgID) (*VersionInfo, []byte, error) {
	return f.shadow, f.shSrc, nil
}
func (f *fakeStore) RecentReplays(context.Context, ids.OrgID, time.Time) (int, error) {
	return f.recent, nil
}
func (f *fakeStore) CreateReplay(_ context.Context, _ ids.OrgID, n NewReplay) (*Replay, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, n)
	r := &Replay{Object: "replay", ID: ids.New[ids.ReplayKind]().String(), Status: ReplayQueued, Window: n.Window}
	if f.replays == nil {
		f.replays = map[string]*Replay{}
	}
	f.replays[r.ID] = r
	return r, nil
}
func (f *fakeStore) GetReplay(_ context.Context, _ ids.OrgID, id string) (*Replay, error) {
	if r, ok := f.replays[id]; ok {
		return r, nil
	}
	return nil, ErrNotFound
}
func (f *fakeStore) CancelReplay(_ context.Context, _ ids.OrgID, id string, _ Actor, _ time.Time) (*Replay, error) {
	if f.cancelEr != nil {
		return nil, f.cancelEr
	}
	r, ok := f.replays[id]
	if !ok {
		return nil, ErrNotFound
	}
	r.Status = ReplayCancelled
	return r, nil
}
func (f *fakeStore) LiveShadow(context.Context, ids.OrgID) (*VersionInfo, error) {
	return f.shadow, nil
}
func (f *fakeStore) Version(_ context.Context, _ ids.OrgID, id string) (*VersionInfo, error) {
	if f.shadow != nil && f.shadow.VersionID == id {
		return f.shadow, nil
	}
	return nil, nil
}
func (f *fakeStore) VersionsByID(context.Context, ids.OrgID, []string) (map[string]VersionInfo, error) {
	m := map[string]VersionInfo{}
	if f.enforced != nil {
		m[f.enforced.VersionID] = *f.enforced
	}
	return m, nil
}
func (f *fakeStore) ShadowRows(context.Context, ids.OrgID, string, NullableWindow) ([]ShadowRow, error) {
	return f.shadowRows, nil
}
func (f *fakeStore) ShadowSamples(context.Context, ids.OrgID, string, NullableWindow, int) ([]ShadowSample, error) {
	return f.shadowSamples, nil
}
func (f *fakeStore) StatsGroups(_ context.Context, _ ids.OrgID, q StatsQuery, _ string) ([]StatsRow, StatsRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := f.groups
	if q.To.Before(now0) && f.prev != nil {
		src = f.prev
	}
	var total StatsRow
	if t := src[ByNone]; len(t) == 1 {
		total = t[0]
	} else {
		total = SumRows(src[q.By])
	}
	return src[q.By], total, nil
}
func (f *fakeStore) StatsSeries(context.Context, ids.OrgID, StatsQuery, string) ([]SeriesRow, error) {
	return f.series, nil
}
func (f *fakeStore) DayCounters(context.Context, ids.OrgID, time.Time, time.Time, string) ([]DayCounter, error) {
	return f.counters, nil
}
func (f *fakeStore) AgentBudgets(context.Context, ids.OrgID) ([]AgentBudgetRow, error) {
	return f.agentsB, nil
}
func (f *fakeStore) ListAnomalies(_ context.Context, _ ids.OrgID, _ AnomalyFilter, limit int, _ string, _ time.Time) ([]Anomaly, string, error) {
	if len(f.anomalies) > limit {
		return f.anomalies[:limit], "next", nil
	}
	return f.anomalies, "", nil
}
func (f *fakeStore) GetAnomaly(_ context.Context, _ ids.OrgID, id string, _ time.Time) (*Anomaly, error) {
	for i := range f.anomalies {
		if f.anomalies[i].ID == id {
			return &f.anomalies[i], nil
		}
	}
	return nil, ErrNotFound
}
func (f *fakeStore) CountActive(context.Context, ids.OrgID, time.Time) (int, error) {
	return f.active, nil
}
func (f *fakeStore) AgentAges(context.Context, ids.OrgID) ([]AgentInfo, error) { return f.ages, nil }
func (f *fakeStore) Acknowledge(_ context.Context, o ids.OrgID, id string, _ Actor, now time.Time) (*Anomaly, error) {
	if f.ackErr != nil {
		return nil, f.ackErr
	}
	return f.GetAnomaly(context.Background(), o, id, now)
}
func (f *fakeStore) Resolve(_ context.Context, o ids.OrgID, id string, _ Actor, _, _ string, now time.Time) (*Anomaly, error) {
	if f.resErr != nil {
		return nil, f.resErr
	}
	return f.GetAnomaly(context.Background(), o, id, now)
}
func (f *fakeStore) UpdateSettings(_ context.Context, _ ids.OrgID, _, after Settings, _ Actor, _, _ string) (string, error) {
	f.settings = append(f.settings, after)
	f.org.Settings = after
	return f.deedID, nil
}
func (f *fakeStore) FactsState(context.Context, ids.OrgID, time.Time) (Settings, []ActiveEpisode, time.Time, error) {
	f.mu.Lock()
	f.factsCalls++
	f.mu.Unlock()
	return f.factsSet, f.factsEps, f.factsLast, f.factsErr
}
func (f *fakeStore) DetectOrgs(context.Context, time.Duration) ([]ids.OrgID, error) {
	if f.detectOrgs == nil {
		return nil, errors.New("listing failed")
	}
	return f.detectOrgs, nil
}
func (f *fakeStore) WithDetectLock(ctx context.Context, _ ids.OrgID, fn func(ctx context.Context, tx DetectTx) error) (bool, error) {
	if !f.locked {
		return false, nil
	}
	return true, fn(ctx, f.tx)
}

// fakeTx is a DetectTx over fixed rows.
type fakeTx struct {
	agents  []AgentInfo
	buckets []BucketRow
	refs    []RefRow
	ups     []EpisodeInput
	seen    map[string]time.Time
	runs    []string
	closed  int
}

func (t *fakeTx) Agents(context.Context) ([]AgentInfo, error) { return t.agents, nil }
func (t *fakeTx) Buckets(_ context.Context, _ string, from, to time.Time) ([]BucketRow, error) {
	var out []BucketRow
	for _, b := range t.buckets {
		if !b.Start.Before(from) && b.Start.Before(to) {
			out = append(out, b)
		}
	}
	return out, nil
}
func (t *fakeTx) RefBuckets(_ context.Context, _ string, _, from, to time.Time) ([]RefRow, error) {
	var out []RefRow
	for _, r := range t.refs {
		if !r.Start.Before(from) && r.Start.Before(to) {
			out = append(out, r)
		}
	}
	return out, nil
}
func (t *fakeTx) Window(_ context.Context, from, to time.Time) ([]BucketRow, error) {
	var out []BucketRow
	for _, b := range t.buckets {
		if !b.Start.Before(from) && b.Start.Before(to) {
			b.Start = from
			out = append(out, b)
		}
	}
	return out, nil
}
func (t *fakeTx) WindowRefs(_ context.Context, _, from, to time.Time) ([]RefRow, error) {
	return t.RefBuckets(context.Background(), "", time.Time{}, from, to)
}
func (t *fakeTx) Samples(_ context.Context, agent string, _, _ time.Time, kind SampleKind, tool string, _ time.Time, n int) ([]string, error) {
	out := []string{}
	for i := range min(n, 3) {
		out = append(out, "act_"+string(kind)+"_"+tool+"_"+itoa(i))
	}
	return out, nil
}
func (t *fakeTx) UpsertEpisode(_ context.Context, in EpisodeInput) (EpisodeResult, error) {
	t.ups = append(t.ups, in)
	key := in.Agent + "|" + in.Finding.Kind + "|" + in.Finding.Tool
	if t.seen == nil {
		t.seen = map[string]time.Time{}
	}
	if end, ok := t.seen[key]; ok {
		if !in.Window.End.After(end) {
			return EpisodeResult{Skipped: true}, nil
		}
		t.seen[key] = in.Window.End
		return EpisodeResult{Extended: true}, nil
	}
	t.seen[key] = in.Window.End
	return EpisodeResult{Inserted: true, Published: true}, nil
}
func (t *fakeTx) CloseStale(context.Context, time.Time, time.Time) (int, error) {
	t.closed++
	return 0, nil
}
func (t *fakeTx) RecordRun(_ context.Context, kind, period string, _, _ time.Time, _ string, _ map[string]any, _ string) error {
	t.runs = append(t.runs, kind+":"+period)
	return nil
}
func (t *fakeTx) PruneRuns(context.Context, string, time.Time) error { return nil }
