package policy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/deed/deedtest"
)

// fakeChanges is a Changes the test drives.
type fakeChanges struct {
	orgs  chan string
	recon chan struct{}
}

func newFakeChanges() *fakeChanges {
	return &fakeChanges{orgs: make(chan string, 16), recon: make(chan struct{}, 4)}
}

func (f *fakeChanges) Subscribe(ctx context.Context) (<-chan string, <-chan struct{}, error) {
	return f.orgs, f.recon, nil
}

func enforced(t *testing.T, w *world) *Activation {
	t.Helper()
	w.push("postgres-safe")
	return w.activate("postgres-safe", 1, ModeEnforce)
}

func TestBuiltinNoPolicy(t *testing.T) {
	if noPolicy() == nil {
		t.Fatal("the builtin no-active-policy must compile")
	}
	w := newWorld(t)
	a, err := w.reg.Active(bg, w.org)
	if err != nil || !a.Enforce.Builtin || a.Enforce.Name != BuiltinNoPolicy || a.Shadow != nil {
		t.Fatalf("%v %+v", err, a.Enforce)
	}
	d := Evaluate(bg, a.Enforce.Policy(), Input{Tool: ToolInput{ToolFacts: ToolFacts{Name: "x.y"}}, Time: DefaultCaseTime,
		Agent: AgentInput{AgentFacts: AgentFacts{ID: LocalAgentID, Status: StatusActive}}}, EvalOptions{})
	want := `Blocked by policy "no-active-policy": No policy is active for this org yet. An admin can activate one in Console › Policies.`
	if d.Outcome != Deny || d.Code != CodePolicyDenied || d.Message != want {
		t.Fatalf("%+v", d)
	}
}

func TestRegistryLoadsAndCaches(t *testing.T) {
	w := newWorld(t)
	act := enforced(t, w)
	w.reg.Invalidate(w.org)
	w.repo.loadCount[w.org] = 0
	// 50 concurrent cold loads: one load (single flight).
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Go(func() {
			a, err := w.reg.Active(bg, w.org)
			if err != nil || a.Enforce.ActivationID != act.ID {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := w.repo.loadCount[w.org]; n != 1 {
		t.Fatalf("want 1 load, got %d", n)
	}
	a, _ := w.reg.Active(bg, w.org)
	l := a.Enforce
	if l.Name != "postgres-safe" || l.Version != 1 || l.DeedID != act.Deeds[0] || l.Mode != ModeEnforce || l.Policy() == nil ||
		l.Compiled.VersionHash != l.VersionHash || a.Generation.IsZero() {
		t.Fatalf("%+v", l)
	}
	if h := w.reg.Health(); h.Status != "ok" {
		t.Fatalf("%+v", h)
	}
	if s := w.reg.Stats(); s.ActiveOrgs != 1 || s.Reloads == 0 || s.LoadFailures != 0 {
		t.Fatalf("%+v", s)
	}
}

// Every way a stored activation can disagree with its sealed record makes the org Unavailable,
// and never falls back to what was loaded before.
func TestRegistryFailsClosed(t *testing.T) {
	// The same seal key id with another public key: what a deed would look like re-sealed by
	// someone who doesn't hold the published key.
	otherSigner, _ := deed.NewFileSigner(testSealKey, deedtest.Seed("someone-else"))
	otherKeys, err := deed.NewKeySet("https://deedglass.example", []deed.KeyRecord{{ID: testSealKey, PublicKey: otherSigner.PublicKey(),
		Status: deed.KeyActive, ActivatedAt: time.Now().Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(org string, r *LoadedRow){
		"compile": func(_ string, r *LoadedRow) { r.Version.Source = []byte("deedglass: policy/v1\nname: x\n") },
		"version_hash": func(_ string, r *LoadedRow) {
			r.Version.Source = []byte(strings.Replace(string(r.Version.Source), "max_calls: 60", "max_calls: 6000", 1))
		},
		"source_hash": func(_ string, r *LoadedRow) {
			r.Version.Source = append([]byte("# a comment that changes nothing\n"), r.Version.Source...)
		},
		"deed": func(_ string, r *LoadedRow) {
			r.Deed = []byte(strings.Replace(string(r.Deed), `"seq":`, `"seq": `, 1) + "x")
		},
		"deed signature": func(_ string, r *LoadedRow) {
			env, _ := deed.ParseEnvelope(r.Deed)
			env.Seal = "ed25519:" + strings.Repeat("A", 86)
			r.Deed, _ = env.MarshalCanonical()
		},
		"deed_target": func(_ string, r *LoadedRow) { r.Version.ID = "pv_01k6f6evxjk5zr0t0kntw9ehx5" },
		"deed_data": func(_ string, r *LoadedRow) {
			r.DeedData = []byte(strings.Replace(string(r.DeedData), `"mode":"enforce"`, `"mode":"shadow"`, 1))
		},
		"deed_data mismatch": func(_ string, r *LoadedRow) { r.Activation.ID = "pa_01k6f6evxjk5zr0t0kntw9ehx5" },
		"wrong keys":         func(_ string, r *LoadedRow) {},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			enforced(t, w)
			before, _ := w.reg.Active(bg, w.org)
			if before.Enforce.Unavailable != nil {
				t.Fatal(before.Enforce.Unavailable)
			}
			w.repo.tamper = edit
			if name == "wrong keys" {
				w.repo.keys = otherKeys
			}
			w.reg.Invalidate(w.org)
			a, _ := w.reg.Active(bg, w.org)
			if a.Enforce.Unavailable == nil || a.Enforce.Policy() != nil {
				t.Fatalf("%s: loaded anyway", name)
			}
			var le *LoadError
			if !errors.As(a.Enforce.Unavailable, &le) {
				t.Fatalf("%T %v", a.Enforce.Unavailable, a.Enforce.Unavailable)
			}
			if strings.Contains(a.Enforce.Unavailable.Error(), "deedglass: policy/v1") {
				t.Fatal("the YAML must never appear in errors")
			}
			d := Evaluate(bg, a.Enforce.Policy(), Input{Tool: ToolInput{ToolFacts: ToolFacts{Name: "postgres.query"}}, Time: DefaultCaseTime,
				Agent: AgentInput{AgentFacts: AgentFacts{ID: LocalAgentID, Status: StatusActive}}}, EvalOptions{})
			if d.Code != CodePolicyUnavailable || d.Outcome != Deny {
				t.Fatalf("%+v", d)
			}
			if h := w.reg.Health(); h.Status != "degraded" || len(h.Unavailable) != 1 {
				t.Fatalf("%+v", h)
			}
			if w.reg.Stats().LoadFailures == 0 {
				t.Fatal("failures not counted")
			}
		})
	}
	t.Run("database down", func(t *testing.T) {
		w := newWorld(t)
		enforced(t, w)
		w.repo.loadErr = errors.New("connection refused")
		w.reg.Invalidate(w.org)
		a, _ := w.reg.Active(bg, w.org)
		if a.Enforce.Unavailable == nil {
			t.Fatal("a load error must fail closed")
		}
	})
	t.Run("a broken shadow never blocks", func(t *testing.T) {
		w := newWorld(t)
		enforced(t, w)
		w.push("gmail-drafts-only")
		w.activate("gmail-drafts-only", 1, ModeShadow)
		w.repo.tamper = func(_ string, r *LoadedRow) {
			if r.Activation.Mode == ModeShadow {
				r.DeedData = []byte(`{}`)
			}
		}
		w.reg.Invalidate(w.org)
		a, _ := w.reg.Active(bg, w.org)
		if a.Enforce.Unavailable != nil || a.Shadow != nil || a.Enforce.Name != "postgres-safe" {
			t.Fatalf("%+v %+v", a.Enforce, a.Shadow)
		}
	})
}

func TestRegistryHotSwap(t *testing.T) {
	w := newWorld(t)
	ch := newFakeChanges()
	var mu sync.Mutex
	swaps := map[string]int{}
	reg := NewRegistry(w.repo, ch, WithPollEvery(20*time.Millisecond), WithIdleEvict(time.Hour),
		WithSwapHook(func(org string, a *Active) { mu.Lock(); swaps[org]++; mu.Unlock() }))
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { defer close(done); _ = reg.Run(ctx) }()
	defer func() { cancel(); <-done }()

	enforced(t, w) // the service's own registry is w.reg; reg is "another instance"
	a, _ := reg.Active(bg, w.org)
	if a.Enforce.Name != "postgres-safe" {
		t.Fatal(a.Enforce.Name)
	}
	// A change on "the other instance", then the notification: reg swaps.
	w.push("default-deny")
	w.activate("default-deny", 1, ModeEnforce)
	ch.orgs <- w.org
	waitFor(t, func() bool { a, _ := reg.Active(bg, w.org); return a.Enforce.Name == "default-deny" })
	// A lost notification: the safety poll catches it.
	w.activate("postgres-safe", 1, ModeEnforce)
	waitFor(t, func() bool { a, _ := reg.Active(bg, w.org); return a.Enforce.Name == "postgres-safe" })
	// A reconnect invalidates everything.
	before := w.repo.loadCount[w.org]
	ch.recon <- struct{}{}
	waitFor(t, func() bool {
		reg.mu.RLock()
		defer reg.mu.RUnlock()
		return len(reg.orgs) == 0
	})
	if _, err := reg.Active(bg, w.org); err != nil || w.repo.loadCount[w.org] <= before {
		t.Fatal("the next Active after a reconnect loads again")
	}
	// A notification for an org this instance never loaded costs nothing.
	n := w.repo.loadCount["org_01k6f6evxjk5zr0t0kntw9ehx5"]
	ch.orgs <- "org_01k6f6evxjk5zr0t0kntw9ehx5"
	time.Sleep(50 * time.Millisecond)
	if w.repo.loadCount["org_01k6f6evxjk5zr0t0kntw9ehx5"] != n {
		t.Fatal("loaded an org nobody uses here")
	}
}

func TestRegistryEvictsIdleOrgs(t *testing.T) {
	w := newWorld(t)
	enforced(t, w)
	var clock time.Time = time.Now()
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	reg := NewRegistry(w.repo, nil, WithRegistryClock(now), WithIdleEvict(10*time.Minute))
	if _, err := reg.Active(bg, w.org); err != nil {
		t.Fatal(err)
	}
	reg.poll(bg)
	if reg.Stats().ActiveOrgs != 1 {
		t.Fatal("evicted too early")
	}
	mu.Lock()
	clock = clock.Add(11 * time.Minute)
	mu.Unlock()
	reg.poll(bg)
	if reg.Stats().ActiveOrgs != 0 {
		t.Fatal("not evicted after 10 minutes idle")
	}
}

func TestRegistryRetriesUnavailable(t *testing.T) {
	w := newWorld(t)
	enforced(t, w)
	var mu sync.Mutex
	clock := time.Now()
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	reg := NewRegistry(w.repo, nil, WithRegistryClock(now))
	w.repo.loadErr = errors.New("connection refused")
	a, _ := reg.Active(bg, w.org)
	if a.Enforce.Unavailable == nil {
		t.Fatal("want unavailable")
	}
	w.repo.loadErr = nil
	mu.Lock()
	clock = clock.Add(RetryUnavailable + time.Second)
	mu.Unlock()
	_, _ = reg.Active(bg, w.org) // still the old answer, and a background reload starts
	waitFor(t, func() bool { a, _ := reg.Active(bg, w.org); return a.Enforce.Unavailable == nil })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// A cache hit allocates nothing (S12 §3.11: < 1 µs, 0 allocs).
func TestRegistryHitAllocs(t *testing.T) {
	w := newWorld(t)
	enforced(t, w)
	if _, err := w.reg.Active(bg, w.org); err != nil {
		t.Fatal(err)
	}
	if n := testing.AllocsPerRun(1000, func() { _, _ = w.reg.Active(bg, w.org) }); n != 0 {
		t.Fatalf("a cache hit allocates %v times", n)
	}
}

func BenchmarkRegistryHit(b *testing.B) {
	t := &testing.T{}
	w := newWorld(t)
	w.t = t
	enforced(t, w)
	_, _ = w.reg.Active(bg, w.org)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = w.reg.Active(bg, w.org)
	}
}

func BenchmarkRegistryColdLoad(b *testing.B) {
	t := &testing.T{}
	w := newWorld(t)
	w.t = t
	enforced(t, w)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		w.reg.Invalidate(w.org)
		_, _ = w.reg.Active(bg, w.org)
	}
}

// A load that read the database before an activation must never store its older result after the
// activation's invalidation, and a later reload must not join it (S12 matrix run, PG 18: the dry
// run saw the previous policy).
func TestRegistryNoStaleStore(t *testing.T) {
	w := newWorld(t)
	w.push("postgres-safe")
	w.push("default-deny")
	w.activate("postgres-safe", 1, ModeEnforce)
	ch := newFakeChanges()
	reg := NewRegistry(w.repo, ch, WithPollEvery(time.Hour))
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { defer close(done); _ = reg.Run(ctx) }()
	defer func() { cancel(); <-done }()
	if a, _ := reg.Active(bg, w.org); a.Enforce.Name != "postgres-safe" {
		t.Fatal(a.Enforce.Name)
	}
	// A notification starts a reload that reads postgres-safe and then stalls.
	gate := make(chan struct{})
	w.repo.mu.Lock()
	w.repo.slowLoad = gate
	w.repo.mu.Unlock()
	ch.orgs <- w.org
	time.Sleep(50 * time.Millisecond)
	// Meanwhile default-deny is enforced; this instance learns of it (notification) and invalidates.
	w.repo.mu.Lock()
	w.repo.slowLoad = nil
	w.repo.mu.Unlock()
	w.activate("default-deny", 1, ModeEnforce)
	reg.Invalidate(w.org)
	ch.orgs <- w.org
	time.Sleep(20 * time.Millisecond)
	close(gate) // the stale load finishes now
	waitFor(t, func() bool { a, _ := reg.Active(bg, w.org); return a.Enforce.Name == "default-deny" })
	time.Sleep(50 * time.Millisecond)
	if a, _ := reg.Active(bg, w.org); a.Enforce.Name != "default-deny" {
		t.Fatalf("a stale load overwrote the newer policy: %s", a.Enforce.Name)
	}
}
