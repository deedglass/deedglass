package learn

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
)

// memStore is an in-memory SuggestStore, DigestStore and ScheduleStore for S28's unit tests. The
// suggestion transitions follow store/learnstore/suggestions.go: compare-and-set on the status,
// ErrNotFound for an unknown id, ErrStateConflict for a status that moved on. Every transition that
// the real store seals (CompleteAccept, RejectSuggestion) seals one deed here, with a fresh deed_ id.
type memStore struct {
	mu   sync.Mutex
	orgs map[ids.OrgID]*memOrg
	sugs map[string]*memSug // by id
	runs map[string]*memRun // org|kind|period
	tick int64              // created_at clock: base + tick µs

	Base time.Time // the store's clock base (created_at, updated_at)

	deeds []memDeed

	// Counters and recorded calls.
	insertCalls, supersedeStaleCalls, phrasingUsageCalls int
	phrasings                                            []memPhrasing

	// Injectable errors.
	errOpenFingerprints, errInsert, errUsage, errEnforced error

	// onOrg runs at the start of every Org read, outside the lock (a seam to hold a pass mid-way).
	onOrg func()
}

type memOrg struct {
	info       OrgInfo
	enforced   *VersionInfo
	enfSrc     []byte
	versions   []*ResolvedVersion
	shadow     *VersionInfo
	shadowDeed string
	counters   []DayCounter
	agentB     []AgentBudgetRow
	rows       []EvidenceRow
	eps        []EpisodeRef
	people     []memPerson
	numbers    *DigestNumbers
	costMicros int64
	callsToday int
}

type memPerson struct {
	DigestRecipient
	Name string
}

type memSug struct {
	org       ids.OrgID
	row       SuggestionRow
	mainCount int64
}

type memRun struct {
	status string // running | succeeded | failed
	stats  map[string]any
	errTxt string
}

type memDeed struct {
	ID, Org, Action, Target string
	Actor                   Actor
}

type memPhrasing struct {
	ID               string
	Title, Rationale *string
	P                Phrasing
}

func newMemStore() *memStore {
	return &memStore{orgs: map[ids.OrgID]*memOrg{}, sugs: map[string]*memSug{}, runs: map[string]*memRun{},
		Base: time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)}
}

// addOrg registers an org with its name and time zone and the default settings.
func (m *memStore) addOrg(org ids.OrgID, name, tz string) *memOrg {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := &memOrg{info: OrgInfo{ID: org, Name: name, Slug: name, Timezone: tz, Settings: DefaultSettings()}}
	m.orgs[org] = o
	return o
}

// enforce makes src version n of the policy the org's enforced version (and a stored version).
func (m *memStore) enforce(org ids.OrgID, policyID, name string, n int, src []byte) *ResolvedVersion {
	rv := m.addVersion(org, policyID, name, n, src)
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.orgs[org]
	v := rv.VersionInfo
	o.enforced, o.enfSrc = &v, src
	return rv
}

// addVersion stores a version (compiled to get its hash).
func (m *memStore) addVersion(org ids.OrgID, policyID, name string, n int, src []byte) *ResolvedVersion {
	c, _ := policy.Compile(src, policy.Options{})
	hash := ""
	if c != nil {
		hash = c.VersionHash
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.orgs[org]
	rv := &ResolvedVersion{VersionInfo: VersionInfo{PolicyID: policyID, Name: name, Version: n, VersionID: ids.New[ids.PolicyVersionKind]().String()},
		VersionHash: hash, Source: src, Found: true}
	o.versions = append(o.versions, rv)
	for _, v := range o.versions {
		v.Latest = max(v.Latest, n)
	}
	return rv
}

func (m *memStore) org(org ids.OrgID) *memOrg { return m.orgs[org] }

func (m *memStore) now() time.Time {
	m.tick++
	return m.Base.Add(time.Duration(m.tick) * time.Microsecond)
}

func (m *memStore) seal(org ids.OrgID, action, target string, by Actor) string {
	id := ids.New[ids.DeedKind]().String()
	m.deeds = append(m.deeds, memDeed{ID: id, Org: org.String(), Action: action, Target: target, Actor: by})
	return id
}

// deedsOf counts the sealed deeds with action (and target, when not empty).
func (m *memStore) deedsOf(action, target string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, d := range m.deeds {
		if d.Action == action && (target == "" || d.Target == target) {
			n++
		}
	}
	return n
}

func (m *memStore) status(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sugs[id]; ok {
		return s.row.Status
	}
	return ""
}

func (m *memStore) find(org ids.OrgID, id string) (*memSug, error) {
	s, ok := m.sugs[id]
	if !ok || s.org != org {
		return nil, ErrNotFound
	}
	return s, nil
}

// --- EvidenceStore ----------------------------------------------------------------------------------

func (m *memStore) EvidenceRows(_ context.Context, org ids.OrgID, w Window) ([]EvidenceRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.orgs[org]
	if o == nil {
		return nil, nil
	}
	var out []EvidenceRow
	for _, r := range o.rows {
		if !r.At.Before(w.From) && r.At.Before(w.To) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) EpisodesIn(_ context.Context, org ids.OrgID, _ Window) ([]EpisodeRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o := m.orgs[org]; o != nil {
		return o.eps, nil
	}
	return nil, nil
}

// --- SuggestStore reads -----------------------------------------------------------------------------

func (m *memStore) Org(_ context.Context, org ids.OrgID) (OrgInfo, error) {
	if m.onOrg != nil {
		m.onOrg()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.orgs[org]
	if o == nil {
		return OrgInfo{}, ErrNotFound
	}
	return o.info, nil
}

func (m *memStore) EnforcedSource(_ context.Context, org ids.OrgID) (*VersionInfo, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errEnforced != nil {
		return nil, nil, m.errEnforced
	}
	o := m.orgs[org]
	if o == nil || o.enforced == nil {
		return nil, nil, nil
	}
	v := *o.enforced
	return &v, o.enfSrc, nil
}

func (m *memStore) ResolveVersion(_ context.Context, org ids.OrgID, ref string, n int) (*ResolvedVersion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.orgs[org]
	if o == nil {
		return nil, ErrNotFound
	}
	known := false
	for _, v := range o.versions {
		if v.PolicyID == ref || v.Name == ref {
			known = true
			if v.Version == n {
				c := *v
				return &c, nil
			}
		}
	}
	if !known {
		return nil, ErrNotFound
	}
	return &ResolvedVersion{Found: false}, nil
}

func (m *memStore) DayCounters(_ context.Context, org ids.OrgID, _, _ time.Time, _ string) ([]DayCounter, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orgs[org].counters, nil
}

func (m *memStore) AgentBudgets(_ context.Context, org ids.OrgID) ([]AgentBudgetRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.orgs[org].agentB, nil
}

func (m *memStore) LiveShadowActivation(_ context.Context, org ids.OrgID) (*VersionInfo, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.orgs[org]
	if o == nil || o.shadow == nil {
		return nil, "", nil
	}
	v := *o.shadow
	return &v, o.shadowDeed, nil
}

// --- suggestions --------------------------------------------------------------------------------------

func (m *memStore) SupersedeStale(_ context.Context, org ids.OrgID, enforcedVersionID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.supersedeStaleCalls++
	n := 0
	for _, s := range m.sugs {
		if s.org == org && s.row.Status == SugOpen && s.row.Policy.BaseVersionID != enforcedVersionID {
			s.row.Status = SugSuperseded
			r := "base_changed"
			s.row.StaleReason = &r
			n++
		}
	}
	return n, nil
}

func (m *memStore) OpenFingerprints(_ context.Context, org ids.OrgID, enforcedVersionID string) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.errOpenFingerprints != nil {
		return nil, m.errOpenFingerprints
	}
	out := map[string]string{}
	for _, s := range m.sugs {
		if s.org != org {
			continue
		}
		st := s.row.Status
		if st == SugOpen || st == SugAccepting || (st == SugAccepted && s.row.Policy.BaseVersionID == enforcedVersionID) {
			if out[s.row.Fingerprint] == "" || st != SugAccepted {
				out[s.row.Fingerprint] = st
			}
		}
	}
	return out, nil
}

func (m *memStore) LastRejected(_ context.Context, org ids.OrgID, fps []string) (map[string]RejectedRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]bool{}
	for _, f := range fps {
		want[f] = true
	}
	out := map[string]RejectedRef{}
	for _, s := range m.sugs {
		if s.org != org || s.row.Status != SugRejected || !want[s.row.Fingerprint] || s.row.DecidedAt == nil {
			continue
		}
		if cur, ok := out[s.row.Fingerprint]; !ok || s.row.DecidedAt.After(cur.At) {
			out[s.row.Fingerprint] = RejectedRef{At: *s.row.DecidedAt, MainCount: s.mainCount}
		}
	}
	return out, nil
}

func (m *memStore) InsertSuggestions(_ context.Context, org ids.OrgID, rows []NewSuggestion) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insertCalls++
	if m.errInsert != nil {
		return nil, m.errInsert
	}
	var created []string
	for _, r := range rows {
		dup := false
		for _, s := range m.sugs {
			if s.org == org && s.row.Fingerprint == r.Fingerprint && (s.row.Status == SugOpen || s.row.Status == SugAccepting) {
				dup = true
			}
		}
		if dup {
			continue
		}
		m.sugs[r.ID] = &memSug{org: org, row: rowFromNew(r, m.now()), mainCount: r.MainCount}
		created = append(created, r.ID)
	}
	return created, nil
}

// putSuggestion seeds a stored suggestion directly (tests that start from a given state).
func (m *memStore) putSuggestion(org ids.OrgID, r NewSuggestion, status string) *SuggestionRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &memSug{org: org, row: rowFromNew(r, m.now()), mainCount: r.MainCount}
	s.row.Status = status
	m.sugs[r.ID] = s
	return &s.row
}

func rowFromNew(r NewSuggestion, at time.Time) SuggestionRow {
	var target *string
	if r.TargetRuleID != "" {
		t := r.TargetRuleID
		target = &t
	}
	var diff *policy.Unified
	if r.Diff != "" {
		if u, err := policy.ParseUnified(r.Diff); err == nil {
			diff = &u
		}
	}
	var rep *SuggestionReplay
	digest := ""
	if r.Replay != nil {
		rep = &SuggestionReplay{ID: r.ReplayID, Window: r.Window, Summary: r.Replay.Summary, Transitions: r.Replay.Transitions, ResultDigest: r.Replay.ResultDigest}
		digest = r.Replay.ResultDigest
	}
	return SuggestionRow{
		Suggestion: Suggestion{Object: "suggestion", ID: r.ID, Status: SugOpen, Kind: r.Kind, Generator: r.Generator, Title: r.Title,
			Rationale: r.Rationale, Phrasing: Phrasing{Source: "template"},
			Policy:       SuggestionPolicy{ID: r.PolicyID, Name: r.PolicyName, BaseVersion: r.PolicyVersion, BaseVersionID: r.BaseVersionID, BaseVersionHash: r.BaseVersionHash},
			TargetRuleID: target, Evidence: r.Evidence, EvidenceDigest: r.EvidenceDigest, Diff: diff, Warnings: r.Warnings, Replay: rep,
			RiskNote: r.RiskNote, Impact: r.Impact, CreatedAt: at},
		Fingerprint: r.Fingerprint, CandidateSource: r.CandidateSource, ReplayDigest: digest, UpdatedAt: at,
	}
}

func (m *memStore) ListSuggestions(_ context.Context, org ids.OrgID, f SuggestionFilter, limit int, cursor string) ([]Suggestion, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Suggestion
	for _, s := range m.sugs {
		if s.org != org {
			continue
		}
		st := s.row.Status
		switch f.Status {
		case "":
			if st != SugOpen && st != SugAccepting {
				continue
			}
		case "all":
		default:
			if st != f.Status {
				continue
			}
		}
		if f.Kind != "" && s.row.Kind != f.Kind {
			continue
		}
		all = append(all, s.row.Suggestion)
	}
	slices.SortFunc(all, func(a, b Suggestion) int {
		if c := cmp.Compare(b.Impact, a.Impact); c != 0 {
			return c
		}
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	off := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil {
			return nil, "", errors.New("bad cursor")
		}
		off = n
	}
	if off > len(all) {
		off = len(all)
	}
	all = all[off:]
	if limit > 0 && len(all) > limit {
		return all[:limit], strconv.Itoa(off + limit), nil
	}
	return all, "", nil
}

func (m *memStore) GetSuggestion(_ context.Context, org ids.OrgID, id string) (*SuggestionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.find(org, id)
	if err != nil {
		return nil, err
	}
	c := s.row
	return &c, nil
}

func (m *memStore) BeginAccept(_ context.Context, org ids.OrgID, id string, by Actor, reason string, replace bool, requestID string) (*SuggestionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.find(org, id)
	if err != nil {
		return nil, err
	}
	if s.row.Status != SugOpen {
		return nil, ErrStateConflict
	}
	s.row.Status = SugAccepting
	s.row.DecidedByType, s.row.DecidedByID = by.Type, by.ID
	a := by
	s.row.DecidedBy = &a
	if reason != "" {
		r := reason
		s.row.DecisionReason = &r
	}
	s.row.AcceptReason, s.row.ReplaceShadow, s.row.RequestID = reason, replace, requestID
	s.row.UpdatedAt = m.now()
	c := s.row
	return &c, nil
}

func (m *memStore) ReopenSuggestion(_ context.Context, org ids.OrgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.find(org, id)
	if err != nil {
		return nil // the real UPDATE matches no row
	}
	if s.row.Status != SugAccepting {
		return nil
	}
	s.row.Status = SugOpen
	s.row.DecidedByType, s.row.DecidedByID, s.row.DecidedBy, s.row.DecisionReason = "", "", nil, nil
	s.row.AcceptReason, s.row.ReplaceShadow, s.row.RequestID = "", false, ""
	s.row.UpdatedAt = m.now()
	return nil
}

func (m *memStore) SupersedeOne(_ context.Context, org ids.OrgID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.find(org, id)
	if err != nil {
		return nil
	}
	if s.row.Status == SugOpen || s.row.Status == SugAccepting {
		s.row.Status = SugSuperseded
		r := "base_changed"
		s.row.StaleReason = &r
	}
	return nil
}

func (m *memStore) CompleteAccept(_ context.Context, org ids.OrgID, id string, d AcceptDone) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.find(org, id)
	if err != nil {
		return "", err
	}
	if s.row.Status != SugAccepting {
		return "", ErrStateConflict
	}
	deedID := m.seal(org, "suggestion.accepted", id, d.By)
	s.row.Status = SugAccepted
	s.row.ResultingVersion = &VersionBrief{ID: d.VersionID, Version: d.Version, VersionHash: d.VersionHash}
	at := d.At
	s.row.DecidedAt = &at
	s.row.DecisionDeed = &deedID
	return deedID, nil
}

func (m *memStore) RejectSuggestion(_ context.Context, org ids.OrgID, id string, by Actor, reason, requestID string, now time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.find(org, id)
	if err != nil {
		return "", err
	}
	if s.row.Status != SugOpen {
		return "", ErrStateConflict
	}
	deedID := m.seal(org, "suggestion.rejected", id, by)
	s.row.Status = SugRejected
	a := by
	s.row.DecidedBy = &a
	s.row.DecidedByType, s.row.DecidedByID = by.Type, by.ID
	at := now
	s.row.DecidedAt = &at
	r := reason
	s.row.DecisionReason = &r
	s.row.DecisionDeed = &deedID
	s.row.RequestID = requestID
	return deedID, nil
}

func (m *memStore) StuckAccepting(_ context.Context, org ids.OrgID, before time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for id, s := range m.sugs {
		if s.org == org && s.row.Status == SugAccepting && s.row.UpdatedAt.Before(before) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (m *memStore) SetPhrasing(_ context.Context, org ids.OrgID, id string, title, rationale *string, p Phrasing) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.phrasings = append(m.phrasings, memPhrasing{ID: id, Title: title, Rationale: rationale, P: p})
	s, err := m.find(org, id)
	if err != nil {
		return err
	}
	s.row.Phrasing = p
	if title != nil {
		s.row.Title = *title
	}
	if rationale != nil {
		s.row.Rationale = *rationale
	}
	if o := m.orgs[org]; o != nil {
		o.costMicros += p.CostUSDMicros
		if p.Model != "" && p.Model != "template" {
			o.callsToday++
		}
	}
	return nil
}

func (m *memStore) PhrasingUsage(_ context.Context, org ids.OrgID, _, _ time.Time) (int64, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.phrasingUsageCalls++
	if m.errUsage != nil {
		return 0, 0, m.errUsage
	}
	o := m.orgs[org]
	return o.costMicros, o.callsToday, nil
}

func (m *memStore) PersonName(_ context.Context, org ids.OrgID, a Actor) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.Type != "user" {
		return "an admin key", nil
	}
	for _, p := range m.orgs[org].people {
		if p.UserID == a.ID {
			return cmp.Or(p.Name, p.Email), nil
		}
	}
	return "", ErrNotFound
}

func (m *memStore) ClaimRun(_ context.Context, org ids.OrgID, kind, period string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := org.String() + "|" + kind + "|" + period
	if _, ok := m.runs[k]; ok {
		return false, nil
	}
	m.runs[k] = &memRun{status: "running"}
	return true, nil
}

func (m *memStore) FinishRun(_ context.Context, org ids.OrgID, kind, period string, ok bool, stats map[string]any, errText string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[org.String()+"|"+kind+"|"+period]
	if r == nil {
		return nil
	}
	r.status, r.stats, r.errTxt = "failed", stats, errText
	if ok {
		r.status = "succeeded"
	}
	return nil
}

// runsOf lists the runs of kind for org, as "period=status".
func (m *memStore) runsOf(org ids.OrgID, kind string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	p := org.String() + "|" + kind + "|"
	for k, r := range m.runs {
		if len(k) > len(p) && k[:len(p)] == p {
			out = append(out, k[len(p):]+"="+r.status)
		}
	}
	slices.Sort(out)
	return out
}

// --- DigestStore and ScheduleStore ---------------------------------------------------------------------

func (m *memStore) DigestRecipients(_ context.Context, org ids.OrgID, roles []string) ([]DigestRecipient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []DigestRecipient
	for _, p := range m.orgs[org].people {
		if slices.Contains(roles, p.Role) {
			out = append(out, p.DigestRecipient)
		}
	}
	return out, nil
}

func (m *memStore) DigestNumbers(_ context.Context, org ids.OrgID, _, _ time.Time) (*DigestNumbers, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n := m.orgs[org].numbers; n != nil {
		c := *n
		return &c, nil
	}
	return &DigestNumbers{}, nil
}

func (m *memStore) DetectOrgs(context.Context, time.Duration) ([]ids.OrgID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ids.OrgID
	for id := range m.orgs {
		out = append(out, id)
	}
	slices.SortFunc(out, func(a, b ids.OrgID) int { return cmp.Compare(a.String(), b.String()) })
	return out, nil
}

var (
	_ SuggestStore  = (*memStore)(nil)
	_ DigestStore   = (*memStore)(nil)
	_ ScheduleStore = (*memStore)(nil)
)

// --- the fake PolicyWriter ------------------------------------------------------------------------------

// fakeWriter is a PolicyWriter over a memStore: a new version per new source (identical source →
// the existing version, created false), a shadow activation that seals policy.activated and
// replaces the org's live shadow.
type fakeWriter struct {
	st *memStore

	mu       sync.Mutex
	adds     []writerAdd
	acts     []writerAct
	errAdd   error
	errAct   error
	afterAct func() error // runs after the shadow is live; its error is returned (a crash after step 5)
}

type writerAdd struct {
	P        Principal
	PolicyID string
	Src      []byte
	Sug      string
	Notes    string
	Created  bool
	Version  int
}

type writerAct struct {
	P        Principal
	PolicyID string
	Version  int
	Reason   string
}

func (w *fakeWriter) AddSuggestedVersion(_ context.Context, p Principal, policyID string, src []byte, sug string, notes string) (*policy.Version, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.errAdd != nil {
		return nil, false, w.errAdd
	}
	o := w.st.org(p.Org)
	w.st.mu.Lock()
	var found *ResolvedVersion
	latest, name := 0, ""
	for _, v := range o.versions {
		if v.PolicyID != policyID {
			continue
		}
		latest, name = max(latest, v.Version), v.Name
		if string(v.Source) == string(src) {
			found = v
		}
	}
	w.st.mu.Unlock()
	if found != nil {
		w.adds = append(w.adds, writerAdd{P: p, PolicyID: policyID, Src: src, Sug: sug, Notes: notes, Created: false, Version: found.Version})
		return &policy.Version{Object: "policy_version", ID: found.VersionID, Policy: policyID, Version: found.Version, Name: found.Name,
			VersionHash: found.VersionHash, Origin: "suggestion"}, false, nil
	}
	rv := w.st.addVersion(p.Org, policyID, name, latest+1, src)
	w.adds = append(w.adds, writerAdd{P: p, PolicyID: policyID, Src: src, Sug: sug, Notes: notes, Created: true, Version: rv.Version})
	return &policy.Version{Object: "policy_version", ID: rv.VersionID, Policy: policyID, Version: rv.Version, Name: rv.Name,
		VersionHash: rv.VersionHash, Origin: "suggestion"}, true, nil
}

func (w *fakeWriter) ActivateShadow(_ context.Context, p Principal, policyID string, version int, reason string) (*policy.Activation, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.errAct != nil {
		return nil, w.errAct
	}
	w.acts = append(w.acts, writerAct{P: p, PolicyID: policyID, Version: version, Reason: reason})
	o := w.st.org(p.Org)
	w.st.mu.Lock()
	var v *ResolvedVersion
	for _, x := range o.versions {
		if x.PolicyID == policyID && x.Version == version {
			v = x
		}
	}
	if v == nil {
		w.st.mu.Unlock()
		return nil, &policy.Error{}
	}
	d := w.st.seal(p.Org, "policy.activated", v.VersionID, p.Actor)
	vi := v.VersionInfo
	vi.Live = true
	o.shadow, o.shadowDeed = &vi, d
	w.st.mu.Unlock()
	a := &policy.Activation{Object: "policy_activation", Policy: policyID, Name: v.Name, Mode: policy.ModeShadow, Reason: reason, Deeds: []string{d}}
	if w.afterAct != nil {
		if err := w.afterAct(); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (w *fakeWriter) calls() ([]writerAdd, []writerAct) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.adds), slices.Clone(w.acts)
}

var _ PolicyWriter = (*fakeWriter)(nil)
