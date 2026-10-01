package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// The registry (S12 §3.5): every instance's view of each org's live policies. The gate (S13) calls
// Active on every action; a cache hit takes a read lock and an atomic load, and allocates nothing.
//
// Loading an org (cold, on NOTIFY, or on the safety poll) reads the live activations with their
// versions, deeds and deed data, then for each one:
//
//  1. compiles the stored YAML;
//  2. checks the recompiled version_hash and source_hash against the stored ones;
//  3. checks the activation deed with deed.VerifyEnvelope against the published seal keys: an
//     admin deed of this org, action policy.activated, target the version, null policy block;
//  4. checks that the deed's data_digest is the digest of the stored event data, and that the data
//     names exactly this activation (policy, version, version_hash, source_hash, mode, id).
//
// Any failure makes the org Unavailable: every decision is S11's policy_unavailable (fail closed),
// the failure is logged at error level (never the YAML) and counted, and health says degraded. It
// never falls back to the version that was loaded before: after an emergency tightening, serving
// the older, looser policy would be the worst outcome.

// Registry timing.
const (
	SafetyPollEvery  = 30 * time.Second
	IdleEvictAfter   = 10 * time.Minute
	RetryUnavailable = 5 * time.Second // an Unavailable entry is reloaded in the background this often
)

// BuiltinNoPolicy is the decision for an org with no live activation (an old dev org).
const BuiltinNoPolicy = "no-active-policy"

var noPolicySource = []byte("deedglass: policy/v1\nname: " + BuiltinNoPolicy + `
description: The decision for an org with no active policy.
defaults:
  decision: deny
  reason: No policy is active for this org yet. An admin can activate one in Console › Policies.
`)

// noPolicy is compiled once; it can't fail (TestBuiltinNoPolicy).
var noPolicy = sync1(func() *Compiled {
	c, _ := Compile(noPolicySource, Options{File: "builtin"})
	return c
})

// Active is an org's live policies as the gate sees them.
type Active struct {
	Enforce    *Loaded   // never nil: a loaded version, the builtin no-active-policy, or Unavailable
	Shadow     *Loaded   // nil when there is no shadow (or it failed to load: shadow never blocks)
	Generation time.Time // the latest activated_at seen for this org
	LoadedAt   time.Time
	signature  string // the live activation ids, for the safety poll
}

// Loaded is one live policy.
type Loaded struct {
	Compiled     *Compiled
	PolicyID     string
	Name         string
	VersionID    string
	VersionHash  string
	ActivationID string
	DeedID       string
	Version      int
	Mode         Mode
	ActivatedAt  time.Time
	Builtin      bool  // the builtin no-active-policy
	Unavailable  error // non-nil: decide with Unavailable(...) (S11's policy_unavailable)
}

// Policy returns what Evaluate needs: the compiled policy, or nil when it is unavailable (Evaluate
// then returns S11's policy_unavailable decision).
func (l *Loaded) Policy() *Compiled {
	if l == nil || l.Unavailable != nil {
		return nil
	}
	return l.Compiled
}

// RegistryHealth is what /v1/health shows.
type RegistryHealth struct {
	Status      string   // "ok" | "degraded"
	Unavailable []string // org ids whose policies failed to load
}

// RegistryStats are the registry's counters (deedglass_policy_* metrics).
type RegistryStats struct {
	Reloads      uint64
	LoadFailures uint64
	ActiveOrgs   int
}

type regEntry struct {
	active   atomic.Pointer[Active]
	lastUsed atomic.Int64 // unix nanoseconds
}

// Registry caches each org's live policies.
type Registry struct {
	repo    Repo
	changes Changes
	log     *slog.Logger
	now     func() time.Time

	mu    sync.RWMutex
	orgs  map[string]*regEntry
	gens  map[string]uint64 // per-org change generation (Invalidate, notifications, the poll)
	epoch uint64            // bumped when everything is invalidated (a reconnect)

	flight flight

	reloads, failures atomic.Uint64
	pollEvery         time.Duration
	idle              time.Duration

	// Tests: onSwap observes each stored Active.
	onSwap func(org string, a *Active)
}

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// WithRegistryLogger sets the logger.
func WithRegistryLogger(l *slog.Logger) RegistryOption { return func(r *Registry) { r.log = l } }

// WithRegistryClock sets the clock (tests).
func WithRegistryClock(now func() time.Time) RegistryOption { return func(r *Registry) { r.now = now } }

// WithPollEvery sets the safety-poll and eviction interval (tests).
func WithPollEvery(d time.Duration) RegistryOption { return func(r *Registry) { r.pollEvery = d } }

// WithIdleEvict sets how long an unused org stays cached (tests).
func WithIdleEvict(d time.Duration) RegistryOption { return func(r *Registry) { r.idle = d } }

// WithSwapHook observes every swap (tests and benchmarks).
func WithSwapHook(f func(org string, a *Active)) RegistryOption {
	return func(r *Registry) { r.onSwap = f }
}

// NewRegistry returns a registry over repo. changes may be nil (no hot swap: the safety poll only).
func NewRegistry(repo Repo, changes Changes, opts ...RegistryOption) *Registry {
	r := &Registry{repo: repo, changes: changes, log: slog.New(slog.DiscardHandler), now: time.Now, orgs: map[string]*regEntry{}, gens: map[string]uint64{},
		pollEvery: SafetyPollEvery, idle: IdleEvictAfter}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Active returns org's live policies: from the cache when it is there, else loaded (one load per
// org at a time, however many callers wait). The result is never nil unless ctx ends first.
func (r *Registry) Active(ctx context.Context, org string) (*Active, error) {
	r.mu.RLock()
	e := r.orgs[org]
	r.mu.RUnlock()
	if e != nil {
		if a := e.active.Load(); a != nil {
			now := r.now()
			e.lastUsed.Store(now.UnixNano())
			if a.Enforce.Unavailable != nil && now.Sub(a.LoadedAt) > RetryUnavailable {
				go r.reload(context.WithoutCancel(ctx), org)
			}
			return a, nil
		}
	}
	return r.load(ctx, org)
}

// Invalidate drops org from the cache; the next Active loads it again. A load that started before
// the call never stores its (older) result.
func (r *Registry) Invalidate(org string) {
	r.mu.Lock()
	delete(r.orgs, org)
	r.gens[org]++
	r.mu.Unlock()
}

func (r *Registry) invalidateAll() {
	r.mu.Lock()
	r.orgs = map[string]*regEntry{}
	r.epoch++
	r.mu.Unlock()
}

// changed records that org's live policies may have changed (a notification, or the safety poll
// saw different ids): loads that started earlier must not store their result.
func (r *Registry) changed(org string) {
	r.mu.Lock()
	r.gens[org]++
	r.mu.Unlock()
}

// genToken is org's change generation: a load stores its result only if no change signal arrived
// while it ran, and loads of different generations never share a single flight.
type genToken struct{ epoch, gen uint64 }

func (r *Registry) token(org string) genToken {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return genToken{r.epoch, r.gens[org]}
}

func flightKey(org string, t genToken) string {
	return org + "#" + strconv.FormatUint(t.epoch, 10) + "." + strconv.FormatUint(t.gen, 10)
}

// load loads org under single flight and stores the result.
func (r *Registry) load(ctx context.Context, org string) (*Active, error) {
	t := r.token(org)
	v, err := r.flight.do(ctx, flightKey(org, t), func() *Active {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		a := r.build(lctx, org)
		r.store(org, a, t)
		return a
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// reload loads org again only when it is still cached (a notification for an org nobody uses
// here costs nothing).
func (r *Registry) reload(ctx context.Context, org string) {
	r.mu.RLock()
	_, cached := r.orgs[org]
	t := genToken{r.epoch, r.gens[org]}
	r.mu.RUnlock()
	if !cached {
		return
	}
	_, _ = r.flight.do(ctx, flightKey(org, t), func() *Active {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		a := r.build(lctx, org)
		r.store(org, a, t)
		return a
	})
}

// store keeps a only if org's generation is still the one the load started under; otherwise a
// newer change arrived meanwhile and its own load decides.
func (r *Registry) store(org string, a *Active, t genToken) {
	r.mu.Lock()
	if r.epoch != t.epoch || r.gens[org] != t.gen {
		r.mu.Unlock()
		return
	}
	e := r.orgs[org]
	if e == nil {
		e = &regEntry{}
		e.lastUsed.Store(r.now().UnixNano())
		r.orgs[org] = e
	}
	e.active.Store(a)
	r.mu.Unlock()
	r.reloads.Add(1)
	if r.onSwap != nil {
		r.onSwap(org, a)
	}
}

// build loads and checks one org. It never returns nil.
func (r *Registry) build(ctx context.Context, org string) *Active {
	now := r.now()
	a := &Active{LoadedAt: now}
	rows, err := r.repo.LoadActive(ctx, org)
	if err != nil {
		return r.fail(org, a, fmt.Errorf("loading the active policies: %w", err))
	}
	ids := make([]string, 0, len(rows.Rows))
	for i := range rows.Rows {
		row := &rows.Rows[i]
		ids = append(ids, row.Activation.ID)
		if row.Activation.ActivatedAt.After(a.Generation) {
			a.Generation = row.Activation.ActivatedAt
		}
		l, err := checkLoaded(org, row, rows.Keys)
		switch {
		case err != nil && row.Activation.Mode == ModeEnforce:
			a.signature = signature(ids)
			return r.fail(org, a, err)
		case err != nil:
			// A shadow that fails to load is logged and dropped: it never decides anything.
			r.failures.Add(1)
			r.log.Error("policy: the shadow policy failed its checks and is not running", "org_id", org,
				"activation", row.Activation.ID, "error", err.Error())
		case l.Mode == ModeEnforce:
			a.Enforce = l
		default:
			a.Shadow = l
		}
	}
	slices.Sort(ids)
	a.signature = signature(ids)
	if a.Enforce == nil {
		a.Enforce = &Loaded{Compiled: noPolicy(), Name: BuiltinNoPolicy, Mode: ModeEnforce, Builtin: true, VersionHash: noPolicy().VersionHash}
	}
	return a
}

func signature(ids []string) string {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	return strings.Join(ids, ",")
}

func (r *Registry) fail(org string, a *Active, err error) *Active {
	r.failures.Add(1)
	r.log.Error("policy: the enforced policy failed its checks; every action in this org is refused with policy_unavailable",
		"org_id", org, "error", err.Error())
	a.Enforce = &Loaded{Mode: ModeEnforce, Unavailable: err}
	a.Shadow = nil
	return a
}

// LoadError says which check a version failed (never the YAML).
type LoadError struct {
	Activation string
	Check      string // compile | version_hash | source_hash | deed | deed_target | deed_data
	Detail     string
}

func (e *LoadError) Error() string {
	return fmt.Sprintf("activation %s failed the %s check: %s", e.Activation, e.Check, e.Detail)
}

// checkLoaded runs the four load checks on one live activation.
func checkLoaded(org string, row *LoadedRow, keys *deed.KeySet) (*Loaded, error) {
	act, ver := &row.Activation, &row.Version
	fail := func(check, format string, a ...any) error {
		return &LoadError{Activation: act.ID, Check: check, Detail: fmt.Sprintf(format, a...)}
	}
	c, diags := Compile(ver.Source, Options{})
	if c == nil {
		code := "unknown"
		if len(diags) > 0 {
			code = diags[0].Code
		}
		return nil, fail("compile", "the stored version %s no longer compiles (%s)", ver.ID, code)
	}
	if c.VersionHash != ver.VersionHash {
		return nil, fail("version_hash", "version %s recompiles to %s, not the stored %s", ver.ID, ShortHash(c.VersionHash), ShortHash(ver.VersionHash))
	}
	if c.SourceHash != ver.SourceHash {
		return nil, fail("source_hash", "version %s's source hashes to %s, not the stored %s", ver.ID, ShortHash(c.SourceHash), ShortHash(ver.SourceHash))
	}
	if keys == nil {
		return nil, fail("deed", "no seal keys are published")
	}
	res, err := deed.VerifyEnvelope(row.Deed, keys, deed.VerifyOptions{})
	if err != nil {
		return nil, fail("deed", "the activation deed %s doesn't verify: %v", act.DeedID, err)
	}
	b := &res.Envelope.Body
	switch {
	case res.DeedID != act.DeedID:
		return nil, fail("deed_target", "the stored deed is %s, the activation names %s", res.DeedID, act.DeedID)
	case b.Org != org:
		return nil, fail("deed_target", "the deed belongs to %s", b.Org)
	case b.Kind != deed.KindAdmin || b.Admin == nil:
		return nil, fail("deed_target", "the deed isn't an admin deed")
	case b.Admin.Action != ActionActivated:
		return nil, fail("deed_target", "the deed records %s, not %s", b.Admin.Action, ActionActivated)
	case b.Admin.Target == nil || b.Admin.Target.Type != "policy_version" || b.Admin.Target.ID != ver.ID:
		return nil, fail("deed_target", "the deed's target isn't version %s", ver.ID)
	case b.Policy != nil:
		return nil, fail("deed_target", "the admin deed carries a policy block")
	case b.Admin.DataDigest == nil:
		return nil, fail("deed_data", "the deed has no data_digest")
	}
	digest, err := deed.DigestJSON(row.DeedData)
	if err != nil || digest != *b.Admin.DataDigest {
		return nil, fail("deed_data", "the stored event data doesn't match the deed's data_digest")
	}
	var data ActivationData
	if err := json.Unmarshal(row.DeedData, &data); err != nil {
		return nil, fail("deed_data", "the event data isn't readable")
	}
	if data.PolicyID != act.PolicyID || data.VersionID != ver.ID || data.Version != ver.Version || data.VersionHash != ver.VersionHash ||
		data.SourceHash != ver.SourceHash || data.Mode != act.Mode || data.ActivationID != act.ID {
		return nil, fail("deed_data", "the deed's data names a different activation")
	}
	return &Loaded{Compiled: c, PolicyID: act.PolicyID, Name: c.Name, VersionID: ver.ID, VersionHash: ver.VersionHash,
		ActivationID: act.ID, DeedID: act.DeedID, Version: ver.Version, Mode: act.Mode, ActivatedAt: act.ActivatedAt}, nil
}

// Run consumes the change feed and runs the safety poll until ctx ends.
func (r *Registry) Run(ctx context.Context) error {
	var orgs <-chan string
	var reconnected <-chan struct{}
	if r.changes != nil {
		var err error
		if orgs, reconnected, err = r.changes.Subscribe(ctx); err != nil {
			return err
		}
	}
	t := time.NewTicker(r.pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case org, ok := <-orgs:
			if !ok {
				orgs = nil
				continue
			}
			r.changed(org)
			go r.reload(ctx, org)
		case _, ok := <-reconnected:
			if !ok {
				reconnected = nil
				continue
			}
			// Anything cached may have missed a notification while the listener was away.
			r.invalidateAll()
		case <-t.C:
			r.poll(ctx)
		}
	}
}

// poll compares each cached org's live activation ids with the database (a lost NOTIFY), and
// evicts orgs nobody used for IdleEvictAfter.
func (r *Registry) poll(ctx context.Context) {
	now := r.now()
	type item struct {
		org string
		e   *regEntry
	}
	r.mu.RLock()
	items := make([]item, 0, len(r.orgs))
	for org, e := range r.orgs {
		items = append(items, item{org, e})
	}
	r.mu.RUnlock()
	for _, it := range items {
		if now.Sub(time.Unix(0, it.e.lastUsed.Load())) > r.idle {
			r.mu.Lock()
			if r.orgs[it.org] == it.e {
				delete(r.orgs, it.org)
			}
			r.mu.Unlock()
			continue
		}
		a := it.e.active.Load()
		if a == nil {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ids, err := r.repo.LiveActivationIDs(pctx, it.org)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				r.log.Warn("policy: safety poll failed", "org_id", it.org, "error", err.Error())
			}
			continue
		}
		if signature(ids) != a.signature || a.Enforce.Unavailable != nil {
			r.changed(it.org)
			r.reload(ctx, it.org)
		}
	}
}

// Health is ok unless a cached org's enforced policy is unavailable.
func (r *Registry) Health() RegistryHealth {
	h := RegistryHealth{Status: "ok"}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for org, e := range r.orgs {
		if a := e.active.Load(); a != nil && a.Enforce.Unavailable != nil {
			h.Unavailable = append(h.Unavailable, org)
		}
	}
	if len(h.Unavailable) > 0 {
		slices.Sort(h.Unavailable)
		h.Status = "degraded"
	}
	return h
}

// Stats returns the counters for the metrics endpoint.
func (r *Registry) Stats() RegistryStats {
	r.mu.RLock()
	n := len(r.orgs)
	r.mu.RUnlock()
	return RegistryStats{Reloads: r.reloads.Load(), LoadFailures: r.failures.Load(), ActiveOrgs: n}
}

// Unavailable is S11's policy_unavailable decision for a Loaded that failed its checks (the gate
// may also just call Evaluate with Loaded.Policy(), which is nil then).
func Unavailable(why string) Decision { return unavailable(why) }

// --- single flight -------------------------------------------------------------------------------

// flight runs one function per key at a time; other callers for the key wait for its result
// (golang.org/x/sync/singleflight in thirty lines, without the dependency).
type flight struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done chan struct{}
	val  *Active
}

func (f *flight) do(ctx context.Context, key string, fn func() *Active) (*Active, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]*flightCall{}
	}
	if c, ok := f.calls[key]; ok {
		f.mu.Unlock()
		select {
		case <-c.done:
			return c.val, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := &flightCall{done: make(chan struct{})}
	f.calls[key] = c
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.calls, key)
		f.mu.Unlock()
		close(c.done)
	}()
	c.val = fn()
	return c.val, nil
}
