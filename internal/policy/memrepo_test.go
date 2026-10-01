package policy

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
	"github.com/deedglass/deedglass/internal/ids"
)

// memRepo is an in-memory Repo with Postgres-like semantics: one transaction per org at a time
// (the policy lock), all-or-nothing commits, append-only versions, the one-live-enforce rule, and
// real sealed admin deeds (deedtest.MemStore and a test seal key), so the registry's deed checks
// run for real in make ci.
type memRepo struct {
	mu     sync.Mutex
	orgs   map[string]*memOrg
	store  *deedtest.MemStore
	sealer *deed.Sealer
	keys   *deed.KeySet
	clock  func() time.Time

	// Hooks for tests.
	loadErr   error                          // LoadActive fails
	tamper    func(org string, r *LoadedRow) // edits what LoadActive returns
	loadCount map[string]int
	slowLoad  chan struct{} // when set, LoadActive reads the rows, then waits on it
}

type memOrg struct {
	lock        chan struct{}
	policies    []PolicyRow
	versions    []VersionRow
	activations []ActivationRow
	events      map[string][]byte // deed id → admin event data
	deeds       map[string][]byte // deed id → JCS envelope
	published   []PolicyEvent
	notified    int
	agents      map[string]AgentInput
	timezone    string
}

const testSealKey = "sk_01k6f6evxjk5zr0t0kntw9ehx5"

func newMemRepo() *memRepo {
	signer, err := deed.NewFileSigner(testSealKey, deedtest.Seed("policy"))
	if err != nil {
		panic(err)
	}
	clock := func() time.Time { return time.Now().UTC() }
	st := deedtest.NewMemStore(clock)
	act := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	st.SetKey(testSealKey, deed.KeyStatus{Status: deed.KeyActive, ActivatedAt: act})
	ks, err := deed.NewKeySet("https://deedglass.example", []deed.KeyRecord{{ID: testSealKey, PublicKey: signer.PublicKey(), Status: deed.KeyActive, ActivatedAt: act}})
	if err != nil {
		panic(err)
	}
	return &memRepo{orgs: map[string]*memOrg{}, store: st, sealer: deed.NewSealer(signer), keys: ks, clock: clock, loadCount: map[string]int{}}
}

func (r *memRepo) org(id string) *memOrg {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := r.orgs[id]
	if o == nil {
		o = &memOrg{lock: make(chan struct{}, 1), events: map[string][]byte{}, deeds: map[string][]byte{}, agents: map[string]AgentInput{}}
		r.orgs[id] = o
	}
	return o
}

func (o *memOrg) clone() *memOrg {
	c := *o
	c.policies = slices.Clone(o.policies)
	c.versions = slices.Clone(o.versions)
	c.activations = slices.Clone(o.activations)
	c.published = slices.Clone(o.published)
	c.events, c.deeds = map[string][]byte{}, map[string][]byte{}
	for k, v := range o.events {
		c.events[k] = v
	}
	for k, v := range o.deeds {
		c.deeds[k] = v
	}
	return &c
}

func (r *memRepo) InOrgTx(ctx context.Context, org string, fn func(Tx) error) error {
	o := r.org(org)
	tx := &memTx{r: r, org: org, o: o, dtx: r.store.Begin()}
	err := fn(tx)
	if tx.locked {
		defer func() { <-o.lock }()
	}
	if err != nil {
		if tx.snap != nil { // writes happen only under the lock, so the snapshot taken there is exact
			r.mu.Lock()
			lock := o.lock
			*o = *tx.snap
			o.lock = lock
			r.mu.Unlock()
		}
		tx.dtx.Rollback()
		return err
	}
	tx.dtx.Commit()
	return nil
}

func (r *memRepo) InOrgReadTx(ctx context.Context, org string, fn func(Tx) error) error {
	return r.InOrgTx(ctx, org, fn)
}

func (r *memRepo) LoadActive(ctx context.Context, org string) (*ActiveRows, error) {
	r.mu.Lock()
	r.loadCount[org]++
	r.mu.Unlock()
	if r.loadErr != nil {
		return nil, r.loadErr
	}
	o := r.org(org)
	r.mu.Lock()
	defer r.mu.Unlock()
	out := &ActiveRows{Keys: r.keys}
	for _, a := range o.activations {
		if a.DeactivatedAt != nil {
			continue
		}
		var v VersionRow
		for _, x := range o.versions {
			if x.ID == a.VersionID {
				v = x
			}
		}
		row := LoadedRow{Activation: a, Version: v, Deed: o.deeds[a.DeedID], DeedData: o.events[a.DeedID]}
		if r.tamper != nil {
			r.tamper(org, &row)
		}
		out.Rows = append(out.Rows, row)
	}
	if gate := r.slowLoad; gate != nil {
		r.mu.Unlock()
		<-gate
		r.mu.Lock()
	}
	return out, nil
}

func (r *memRepo) LiveActivationIDs(ctx context.Context, org string) ([]string, error) {
	o := r.org(org)
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, a := range o.activations {
		if a.DeactivatedAt == nil {
			out = append(out, a.ID)
		}
	}
	return out, nil
}

type memTx struct {
	r      *memRepo
	org    string
	o      *memOrg
	dtx    *deedtest.MemTx
	locked bool
	snap   *memOrg
}

func (t *memTx) LockOrgPolicies(ctx context.Context) error {
	if t.locked {
		return nil
	}
	select {
	case t.o.lock <- struct{}{}:
		t.locked = true
		t.r.mu.Lock()
		t.snap = t.o.clone()
		t.r.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *memTx) NewID(prefix string) string {
	switch prefix {
	case "pol":
		return ids.New[ids.PolicyKind]().String()
	case "pv":
		return ids.New[ids.PolicyVersionKind]().String()
	case "pa":
		return ids.New[ids.PolicyActivationKind]().String()
	}
	panic("unknown prefix " + prefix)
}

func (t *memTx) InsertPolicy(ctx context.Context, p PolicyRow) error {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	for _, x := range t.o.policies {
		if x.Name == p.Name {
			return errors.New("unique violation: policy name")
		}
	}
	now := t.r.clock()
	p.CreatedAt, p.UpdatedAt = now, now
	t.o.policies = append(t.o.policies, p)
	return nil
}

func (t *memTx) TouchPolicy(ctx context.Context, id, desc string) error {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	for i := range t.o.policies {
		if t.o.policies[i].ID == id {
			t.o.policies[i].UpdatedAt = t.r.clock()
			t.o.policies[i].Description = desc
		}
	}
	return nil
}

func (t *memTx) latest(id string) int {
	n := 0
	for _, v := range t.o.versions {
		if v.PolicyID == id && v.Version > n {
			n = v.Version
		}
	}
	return n
}

func (t *memTx) GetPolicy(ctx context.Context, ref string) (*PolicyRow, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	for _, p := range t.o.policies {
		if p.ID == ref || p.Name == ref {
			p.Latest = t.latest(p.ID)
			return &p, nil
		}
	}
	return nil, ErrNotFound
}

func (t *memTx) ListPolicies(ctx context.Context, after string, limit int) ([]PolicyRow, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	ps := slices.Clone(t.o.policies)
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	var out []PolicyRow
	for _, p := range ps {
		if p.Name > after && len(out) < limit {
			p.Latest = t.latest(p.ID)
			out = append(out, p)
		}
	}
	return out, nil
}

func (t *memTx) NextVersion(ctx context.Context, id string) (int, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	return t.latest(id) + 1, nil
}

func (t *memTx) InsertVersion(ctx context.Context, v VersionRow) error {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	for _, x := range t.o.versions {
		if x.PolicyID == v.PolicyID && (x.Version == v.Version || x.VersionHash == v.VersionHash) {
			return errors.New("unique violation: version")
		}
	}
	v.CreatedAt = t.r.clock()
	v.Name = ""
	t.o.versions = append(t.o.versions, v)
	return nil
}

func (t *memTx) LatestVersion(ctx context.Context, id string) (*VersionRow, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	n := t.latest(id)
	for _, v := range t.o.versions {
		if v.PolicyID == id && v.Version == n {
			return &v, nil
		}
	}
	return nil, ErrNotFound
}

func (t *memTx) GetVersion(ctx context.Context, policyID string, ref VersionRef) (*VersionRow, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	for _, v := range t.o.versions {
		if policyID != "" && v.PolicyID != policyID {
			continue
		}
		if (ref.Number > 0 && v.Version == ref.Number) || (ref.ID != "" && v.ID == ref.ID) || (ref.Hash != "" && v.VersionHash == ref.Hash) {
			return &v, nil
		}
	}
	return nil, ErrNotFound
}

func (t *memTx) ListVersions(ctx context.Context, id string, below, limit int) ([]VersionRow, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	var out []VersionRow
	for _, v := range t.o.versions {
		if v.PolicyID == id && (below == 0 || v.Version < below) {
			v.Source = nil
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (t *memTx) joined(a ActivationRow) ActivationRow {
	for _, v := range t.o.versions {
		if v.ID == a.VersionID {
			a.Version = v.Version
		}
	}
	for _, p := range t.o.policies {
		if p.ID == a.PolicyID {
			a.PolicyName = p.Name
		}
	}
	return a
}

func (t *memTx) LiveActivations(ctx context.Context) ([]ActivationRow, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	var out []ActivationRow
	for _, a := range t.o.activations {
		if a.DeactivatedAt == nil {
			out = append(out, t.joined(a))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mode < out[j].Mode })
	return out, nil
}

func (t *memTx) GetActivation(ctx context.Context, id string) (*ActivationRow, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	for _, a := range t.o.activations {
		if a.ID == id {
			a = t.joined(a)
			return &a, nil
		}
	}
	return nil, ErrNotFound
}

func (t *memTx) EndActivation(ctx context.Context, id string, by deed.Ref, deedID string) (time.Time, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	now := t.r.clock()
	for i := range t.o.activations {
		a := &t.o.activations[i]
		if a.ID == id {
			if a.DeactivatedAt != nil {
				return now, errors.New("DG001: already ended")
			}
			a.DeactivatedAt, a.DeactivatedBy, a.DeactivationDeedID = &now, refBy(by), deedID
			return now, nil
		}
	}
	return now, ErrNotFound
}

func refBy(r deed.Ref) string {
	if r.Type == "system" {
		return "system"
	}
	return r.ID
}

func (t *memTx) InsertActivation(ctx context.Context, a *ActivationRow) error {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	for _, x := range t.o.activations {
		if x.DeactivatedAt == nil && x.Mode == a.Mode {
			return errors.New("unique violation: policy_activations_active")
		}
	}
	a.ActivatedAt = t.r.clock()
	row := *a
	row.Version, row.PolicyName = 0, ""
	t.o.activations = append(t.o.activations, row)
	return nil
}

func (t *memTx) RecordAdmin(ctx context.Context, c AdminChange) (string, error) {
	data, err := json.Marshal(c.Data)
	if err != nil {
		return "", err
	}
	digest, err := deed.DigestJSON(data)
	if err != nil {
		return "", err
	}
	d, err := deed.ForAdmin(deed.AdminSnapshot{Org: t.org, Event: ids.New[ids.AdminEventKind]().String(), Action: c.Action,
		Actor: c.Actor, Target: &deed.Ref{Type: "policy_version", ID: c.VersionID}, Reason: &c.Reason, DataDigest: &digest})
	if err != nil {
		return "", err
	}
	env, err := t.r.sealer.Seal(ctx, t.dtx, d)
	if err != nil {
		return "", err
	}
	raw, err := env.MarshalCanonical()
	if err != nil {
		return "", err
	}
	t.r.mu.Lock()
	t.o.deeds[env.ID], t.o.events[env.ID] = raw, data
	t.r.mu.Unlock()
	return env.ID, nil
}

func (t *memTx) Publish(ctx context.Context, ev PolicyEvent) error {
	t.r.mu.Lock()
	t.o.published = append(t.o.published, ev)
	t.r.mu.Unlock()
	return nil
}

func (t *memTx) NotifyPolicyChanged(ctx context.Context) error {
	t.r.mu.Lock()
	t.o.notified++
	t.r.mu.Unlock()
	return nil
}

func (t *memTx) LoadAgent(ctx context.Context, id string) (*AgentInput, error) {
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	a, ok := t.o.agents[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &a, nil
}

func (t *memTx) LoadOrg(ctx context.Context) (*OrgInput, error) {
	return &OrgInput{OrgFacts: OrgFacts{ID: t.org, Timezone: t.o.timezone}}, nil
}

// deedsOf returns org's committed deeds in seq order.
func (r *memRepo) deedsOf(org string) []deed.Stored { return r.store.Deeds(org) }

// lastDeed parses the newest committed deed of org.
func (r *memRepo) lastDeed(org string) *deed.Envelope {
	ds := r.deedsOf(org)
	if len(ds) == 0 {
		return nil
	}
	return ds[len(ds)-1].Envelope
}

func hasPrefix(s, p string) bool { return strings.HasPrefix(s, p) }
