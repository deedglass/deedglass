package learn

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// FactsCache gives the Gate the anomaly facts a policy sees (S27 §3.8; it implements
// gate.AnomalySource). Opt-in off (the default): {active: false}, from memory. On: a per-org map of
// agent → facts, loaded with one indexed query, dropped on NOTIFY deedglass_learn (an episode
// inserted, extended or resolved; settings changed) and on a TTL safety net.
//
// Fail closed: when the lookup errors, or the last successful detect run for the org is older than
// anomaly_stale_after_minutes, every agent of an opted-in org is
// {active: true, kinds: [detector_stale], severity: warn}. An org that opted in has said "treat
// unusual as risky", and unknown is not usual.
type FactsCache struct {
	Store AnomalyStore
	Now   func() time.Time
	Log   *slog.Logger
	// TTL is the safety net when the facts are on (10 s); OffTTL when they are off (60 s; a
	// settings change also arrives by NOTIFY).
	TTL, OffTTL time.Duration

	mu   sync.Mutex
	orgs map[ids.OrgID]*factsEntry
	// lookups counts store loads (tests: a cache hit does no database work).
	lookups int
}

type factsEntry struct {
	loaded   time.Time
	settings Settings
	known    bool // settings come from a successful load (this one or the one before a failure)
	episodes []ActiveEpisode
	lastRun  time.Time
	err      error
}

// Kind and severity of the fail-closed facts.
const KindDetectorStale = "detector_stale"

func (c *FactsCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Invalidate drops an org's entry (NOTIFY deedglass_learn, payload the org id). An empty org
// drops every entry (the listener reconnected and may have missed notifications).
func (c *FactsCache) Invalidate(org string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if org == "" {
		c.orgs = nil
		return
	}
	if id, err := ids.Parse[ids.OrgKind](org); err == nil {
		delete(c.orgs, id)
	}
}

// Lookups is how many times the cache read the store.
func (c *FactsCache) Lookups() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lookups
}

// Facts are agent's anomaly facts at decision time `at`, and the decision trace's line.
func (c *FactsCache) Facts(ctx context.Context, org ids.OrgID, agent ids.AgentID, at time.Time) (policy.AnomalyFacts, string) {
	e := c.entry(ctx, org)
	if e.err != nil {
		// Fail closed unless this org is known to have the facts off.
		if e.settings.AnomalyFacts || !e.known {
			return staleFacts(), AnomalyLookupFailedTrace
		}
		return policy.AnomalyFacts{}, ""
	}
	if !e.settings.AnomalyFacts {
		return policy.AnomalyFacts{}, ""
	}
	now := c.now()
	stale := time.Duration(e.settings.AnomalyStaleAfterMinutes) * time.Minute
	if e.lastRun.IsZero() || now.Sub(e.lastRun) > stale {
		return staleFacts(), AnomalyStaleTrace(e.lastRun, now)
	}
	return FactsFor(e.episodes, agent.String(), at)
}

func staleFacts() policy.AnomalyFacts {
	return policy.AnomalyFacts{Active: true, Kinds: []string{KindDetectorStale}, Severity: SeverityWarn}
}

// FactsFor folds the episodes active for agent at `at` into facts (pure).
func FactsFor(eps []ActiveEpisode, agent string, at time.Time) (policy.AnomalyFacts, string) {
	var f policy.AnomalyFacts
	for _, e := range eps {
		if e.Agent != agent || !e.ActiveUntil.After(at) {
			continue
		}
		f.Active = true
		if !slices.Contains(f.Kinds, e.Kind) {
			f.Kinds = append(f.Kinds, e.Kind)
		}
		if severityRank(e.Severity) > severityRank(f.Severity) {
			f.Severity = e.Severity
		}
	}
	if !f.Active {
		return policy.AnomalyFacts{}, ""
	}
	slices.Sort(f.Kinds)
	return f, AnomalyTrace(f.Kinds)
}

func severityRank(s string) int {
	switch s {
	case SeverityInfo:
		return 1
	case SeverityWarn:
		return 2
	case SeverityHigh:
		return 3
	}
	return 0
}

// entry returns the org's cached state, loading it when missing or past its TTL.
func (c *FactsCache) entry(ctx context.Context, org ids.OrgID) *factsEntry {
	ttl, offTTL := c.TTL, c.OffTTL
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	if offTTL <= 0 {
		offTTL = time.Minute
	}
	now := c.now()
	c.mu.Lock()
	e := c.orgs[org]
	c.mu.Unlock()
	if e != nil {
		age := now.Sub(e.loaded)
		if e.err == nil && (e.settings.AnomalyFacts && age < ttl || !e.settings.AnomalyFacts && age < offTTL) {
			return e
		}
		if e.err != nil && age < time.Second { // don't hammer a failing database
			return e
		}
	}
	s, eps, last, err := c.Store.FactsState(ctx, org, now)
	n := &factsEntry{loaded: now, settings: s, episodes: eps, lastRun: last, err: err, known: err == nil}
	if err != nil {
		if c.Log != nil {
			c.Log.Warn("learn: reading anomaly facts failed; opted-in orgs fail closed (detector_stale)", "org_id", org.String(), "err", err)
		}
		if e != nil && e.known {
			n.settings, n.known = e.settings, true
		} else {
			n.settings = Settings{AnomalyFacts: true} // unknown opt-in: fail closed
		}
	}
	c.mu.Lock()
	if c.orgs == nil {
		c.orgs = map[ids.OrgID]*factsEntry{}
	}
	c.orgs[org] = n
	c.lookups++
	c.mu.Unlock()
	return n
}
