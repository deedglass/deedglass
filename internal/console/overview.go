package console

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/store/policystore"
)

// GET /v1/overview (S25 §3.2): the Overview screen's numbers for the caller's org. A Console
// session (viewer and up) or an org admin key; agent keys get 403 insufficient_scope. Answers are
// cached for 10 s per org and window, and concurrent misses share one read (a small single
// flight). The chain_broken item comes from a background check of the last 24 h of deeds, at most
// one every 5 minutes per org; the Overview never waits for it. docs/OVERVIEW_METRICS.md.

// The Overview's copy (rule 17: the route's own catalog, like the stream's).
const (
	MsgOverviewWindow   = "window must be 24h or 7d, sent once."
	MsgOverviewParam    = "This endpoint takes only window (24h or 7d)."
	MsgOverviewAgentKey = "The Overview is a Console screen, so agent keys can't read it. Use a Console session or an org admin key."
	MsgOverviewDown     = "The Overview isn't available on this server right now. Try again in a few seconds."
)

// Overview cache and background check timings.
const (
	OverviewTTL        = 10 * time.Second // one answer per org and window
	overviewEvict      = time.Minute      // cache entries older than this are dropped
	ChainCheckEvery    = 5 * time.Minute  // at most one background chain check per org
	chainCheckLookback = 24 * time.Hour
	chainStateKeep     = time.Hour // a quiet org's last check is forgotten after this
	maxChainChecks     = 2         // background checks running at once on this instance
	overviewReadLimit  = 5 * time.Second
)

// OverviewOptions wire the route.
type OverviewOptions struct {
	Ledger *ledger.Ledger // nil mounts the route answering 503 (route-table tests)
	// Usage reads agents' counters (UsageFromStore); nil shows no usage.
	Usage ledger.UsageReader
	// SlotLagAlertBytes is DEEDGLASS_SLOT_LAG_ALERT_MB in bytes.
	SlotLagAlertBytes int64
	Now               func() time.Time
	Log               *slog.Logger
	// TTL overrides OverviewTTL (tests); a negative TTL turns the cache off.
	TTL time.Duration
	// load and check replace the Ledger's reads (unit tests).
	load  func(ctx context.Context, org ids.OrgID, o ledger.OverviewOptions) (*ledger.Overview, error)
	check func(ctx context.Context, org ids.OrgID, since time.Time) (*ledger.ChainCheck, error)
}

// Overview serves GET /v1/overview.
type Overview struct {
	o OverviewOptions

	mu        sync.Mutex
	cache     map[overviewKey]*overviewEntry
	lastSweep time.Time
	chains    map[ids.OrgID]*chainState
	running   int
	wg        sync.WaitGroup
}

type overviewKey struct {
	org    ids.OrgID
	window string
}

// overviewEntry is one cached answer, or a read in flight (done open).
type overviewEntry struct {
	done chan struct{}
	at   time.Time
	val  *ledger.Overview
	err  error
}

// chainState is an org's latest background chain check.
type chainState struct {
	started time.Time
	running bool
	res     *ledger.ChainCheck
}

// NewOverview returns the route's handler.
func NewOverview(o OverviewOptions) *Overview {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.TTL == 0 {
		o.TTL = OverviewTTL
	}
	if o.Ledger != nil {
		led := o.Ledger
		if o.load == nil {
			o.load = led.Overview
		}
		if o.check == nil {
			o.check = func(ctx context.Context, org ids.OrgID, since time.Time) (*ledger.ChainCheck, error) {
				return led.CheckChain(ctx, org, ledger.Range{IssuedGTE: &since}, ledger.CheckOptions{})
			}
		}
	}
	return &Overview{o: o, cache: map[overviewKey]*overviewEntry{}, chains: map[ids.OrgID]*chainState{}}
}

// Routes is GET /v1/overview.
func (v *Overview) Routes() []httpx.Route {
	return []httpx.Route{{Method: "GET", Pattern: "/v1/overview", OperationID: "getOverview", Auth: httpx.SessionOrKey,
		MinRole: httpx.RoleViewer, Scopes: []string{httpx.ScopeAdmin}, AgentKeyMessage: MsgOverviewAgentKey,
		Purpose: "reading the overview", Handler: v.serve}}
}

// ParseOverviewQuery validates GET /v1/overview's query: window=24h (the default) or 7d, once.
func ParseOverviewQuery(r *http.Request) (ledger.OverviewWindow, error) {
	w := ledger.Window24h
	for k, vs := range r.URL.Query() {
		if k != "window" {
			return w, httpx.ErrValidation(k, MsgOverviewParam)
		}
		var ok bool
		if w, ok = ledger.OverviewWindowOf(vs[0]); !ok || len(vs) != 1 {
			return w, httpx.ErrValidation("window", MsgOverviewWindow)
		}
	}
	return w, nil
}

func (v *Overview) serve(w http.ResponseWriter, r *http.Request) error {
	p, ok := httpx.PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return httpx.E(httpx.CodeAuthenticationRequired)
	}
	win, err := ParseOverviewQuery(r)
	if err != nil {
		return err
	}
	if v.o.load == nil {
		return httpx.E(httpx.CodeServiceUnavailable, httpx.Msg(MsgOverviewDown))
	}
	httpx.Annotate(r.Context(), p.OrgID.String(), "", "")
	ov, err := v.Get(r.Context(), p.OrgID, win)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "private, no-store")
	return httpx.WriteJSON(w, http.StatusOK, ov)
}

// Get returns org's overview for the window: the cached answer when it is under 10 s old, else
// one fresh read shared by every caller that asks meanwhile.
func (v *Overview) Get(ctx context.Context, org ids.OrgID, win ledger.OverviewWindow) (*ledger.Overview, error) {
	if v.o.TTL < 0 {
		return v.Read(ctx, org, win)
	}
	k := overviewKey{org: org, window: win.Name}
	v.mu.Lock()
	now := v.o.Now()
	v.sweepLocked(now)
	e := v.cache[k]
	switch {
	case e == nil:
	case !e.at.IsZero() && now.Sub(e.at) < v.o.TTL:
		v.mu.Unlock()
		return e.val, nil
	case e.at.IsZero(): // in flight: wait for it
		v.mu.Unlock()
		select {
		case <-e.done:
			return e.val, e.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e = &overviewEntry{done: make(chan struct{})}
	v.cache[k] = e
	v.mu.Unlock()

	// The read outlives one caller's cancellation, so the others waiting on it still get it.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), overviewReadLimit)
	val, err := v.Read(rctx, org, win)
	cancel()
	v.mu.Lock()
	e.val, e.err = val, err
	if err != nil {
		delete(v.cache, k) // errors are never cached
	} else {
		e.at = v.o.Now()
	}
	close(e.done)
	v.mu.Unlock()
	return val, err
}

// Read is one uncached overview (the bench measures it).
func (v *Overview) Read(ctx context.Context, org ids.OrgID, win ledger.OverviewWindow) (*ledger.Overview, error) {
	if v.o.load == nil {
		return nil, errors.New("console: the overview has no ledger")
	}
	return v.o.load(ctx, org, ledger.OverviewOptions{Window: win, Now: v.o.Now(), Usage: v.o.Usage,
		SlotLagAlertBytes: v.o.SlotLagAlertBytes, Chain: v.chain(org)})
}

// sweepLocked drops cache entries older than a minute and forgotten chain checks, at most once
// per TTL.
func (v *Overview) sweepLocked(now time.Time) {
	if now.Sub(v.lastSweep) < v.o.TTL {
		return
	}
	v.lastSweep = now
	for k, e := range v.cache {
		if !e.at.IsZero() && now.Sub(e.at) > overviewEvict {
			delete(v.cache, k)
		}
	}
	for org, st := range v.chains {
		if !st.running && now.Sub(st.started) > chainStateKeep {
			delete(v.chains, org)
		}
	}
}

// chain returns org's latest finished chain check (nil before the first one finishes) and starts a
// new one in the background when the last started 5 minutes ago or more. It never waits.
func (v *Overview) chain(org ids.OrgID) *ledger.ChainCheck {
	if v.o.check == nil {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.o.Now()
	st := v.chains[org]
	if st == nil {
		st = &chainState{}
		v.chains[org] = st
	}
	if !st.running && (st.started.IsZero() || now.Sub(st.started) >= ChainCheckEvery) && v.running < maxChainChecks {
		st.started, st.running = now, true
		v.running++
		v.wg.Add(1)
		go v.runCheck(org, st, now.Add(-chainCheckLookback))
	}
	return st.res
}

func (v *Overview) runCheck(org ids.OrgID, st *chainState, since time.Time) {
	defer v.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), ledger.CheckBudget+5*time.Second)
	defer cancel()
	res, err := v.o.check(ctx, org, since)
	switch {
	case errors.Is(err, ledger.ErrEmptyRange):
		res, err = nil, nil // no deeds in the last 24 h: nothing to break
	case err != nil:
		v.o.Log.Warn("overview: the background chain check failed", "org", org.String(), "err", err)
	case res != nil && res.Status == ledger.CheckBroken && res.Break != nil:
		v.o.Log.Error("overview: the deed chain is broken", "org", org.String(), "code", res.Break.Code)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	st.running = false
	v.running--
	if err == nil {
		st.res = res
	}
}

// Wait blocks until every background chain check has finished (tests and shutdown).
func (v *Overview) Wait() { v.wg.Wait() }

// UsageFromStore reads agents' counters through S11's Postgres usage store, in the org's time zone
// (an unknown zone reads as UTC, as the Gate does).
func UsageFromStore(u *policystore.Usage) ledger.UsageReader {
	return func(ctx context.Context, tx pgx.Tx, org ids.OrgID, tz string, at time.Time, reqs []ledger.AgentUsageRequest) ([]ledger.AgentUsage, error) {
		loc, err := policy.LoadLocation(tz)
		if err != nil {
			loc = time.UTC
		}
		agents, curs := make([]string, len(reqs)), make([]string, len(reqs))
		for i, r := range reqs {
			agents[i], curs[i] = r.Agent, r.Currency
		}
		snaps, err := u.AgentsTx(ctx, tx, org, loc, at, agents, curs)
		if err != nil {
			return nil, err
		}
		out := make([]ledger.AgentUsage, len(snaps))
		for i, s := range snaps {
			out[i] = ledger.AgentUsage{CallsMinute: s.CallsMinute, CallsHour: s.CallsHour, CallsToday: s.CallsToday,
				SpendToday: s.SpendToday, Currency: s.Currency}
		}
		return out, nil
	}
}
