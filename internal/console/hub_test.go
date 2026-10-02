package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ledger"
)

// --- fixtures ---

func sp(s string) *string { return &s }

// evt builds an event as ledger.Publish would put it on deedglass_events.
func evt(n int, org, typ, id string, opts ...func(*ledger.Event)) *ledger.Event {
	ev := &ledger.Event{V: 1, Event: fmt.Sprintf("evt_%026d", n), Type: typ, Org: org, ID: id, At: "2026-10-02T10:00:00.000Z"}
	if strings.HasPrefix(typ, "action.") {
		ev.Action, ev.State = sp(id), sp("received")
	}
	for _, o := range opts {
		o(ev)
	}
	return ev
}

func withAction(a string) func(*ledger.Event) { return func(e *ledger.Event) { e.Action = &a } }
func withAgent(a string) func(*ledger.Event)  { return func(e *ledger.Event) { e.Agent = &a } }
func withKind(k string) func(*ledger.Event)   { return func(e *ledger.Event) { e.Kind = &k } }

// fakeHydrator answers actions from a fixed act → agent map, counting calls.
type fakeHydrator struct {
	mu       sync.Mutex
	agentOf  map[string]string
	calls    map[string]int
	idsSeen  map[string]int
	fail     atomic.Bool
	approval func(id string) any
}

func newFakeHydrator(agentOf map[string]string) *fakeHydrator {
	return &fakeHydrator{agentOf: agentOf, calls: map[string]int{}, idsSeen: map[string]int{}}
}

func (f *fakeHydrator) count(kind string, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[kind]++
	f.idsSeen[kind] += n
	if f.fail.Load() {
		return errors.New("database is down")
	}
	return nil
}

func (f *fakeHydrator) Actions(_ context.Context, org string, ids []string) (map[string]json.RawMessage, error) {
	if err := f.count("actions", len(ids)); err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, id := range ids {
		a := ledger.Action{Object: "action", ID: id, Agent: ledger.AgentRef{ID: f.agentOf[id], Name: "bot"}, Tool: "orders.execute_sql",
			State: "received", Status: ledger.StatusProcessing, Livemode: true}
		b, _ := json.Marshal(a)
		out[id] = b
	}
	return out, nil
}

func (f *fakeHydrator) Approvals(_ context.Context, org string, ids []string) (map[string]json.RawMessage, error) {
	if err := f.count("approvals", len(ids)); err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, id := range ids {
		var v any = approve.Approval{Object: "approval", ID: id, Status: approve.StatusPending, Approvers: []string{"user:priya@acme.test"},
			Context: json.RawMessage(`{"v":1,"args":{"secret":"s"}}`), You: &approve.You{Eligible: true}, Countersigned: []approve.Countersign{}}
		if f.approval != nil {
			v = f.approval(id)
		}
		b, _ := json.Marshal(v)
		out[id] = b
	}
	return out, nil
}

func (f *fakeHydrator) Agents(_ context.Context, org string, ids []string) (map[string]json.RawMessage, error) {
	if err := f.count("agents", len(ids)); err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	for _, id := range ids {
		b, _ := json.Marshal(identity.Agent{Object: "agent", ID: id, Name: "bot", Status: identity.AgentStopped})
		out[id] = b
	}
	return out, nil
}

// startHub runs a hub on its own bus channel and returns the send side.
func startHub(t *testing.T, o HubOptions) (*Hub, chan<- ledger.BusMsg) {
	t.Helper()
	h := NewHub(o)
	in := make(chan ledger.BusMsg, 1<<15)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.Run(ctx, in) }()
	t.Cleanup(func() { cancel(); <-done })
	return h, in
}

func mustSub(t *testing.T, h *Hub, f Filter, cursor string) (*Sub, []*Envelope, string, string) {
	t.Helper()
	s, replay, resync, pos, err := h.Subscribe(f, cursor)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, replay, resync, pos
}

// recv takes n envelopes from s, failing after a while.
func recv(t *testing.T, s *Sub, n int) []*Envelope {
	t.Helper()
	var out []*Envelope
	deadline := time.After(10 * time.Second)
	for len(out) < n {
		select {
		case e := <-s.C():
			out = append(out, e)
		case <-s.Lagged():
			t.Fatalf("lagged after %d of %d", len(out), n)
		case <-deadline:
			t.Fatalf("got %d of %d envelopes", len(out), n)
		}
	}
	return out
}

func quiet(t *testing.T, s *Sub, d time.Duration) {
	t.Helper()
	select {
	case e := <-s.C():
		t.Fatalf("unexpected %s %s", e.Type, e.Data)
	case <-time.After(d):
	}
}

func decode(t *testing.T, e *Envelope) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.Data, &m); err != nil {
		t.Fatalf("%s: %v", e.Data, err)
	}
	return m
}

func evtID(t *testing.T, e *Envelope) string {
	t.Helper()
	id, _ := decode(t, e)["id"].(string)
	return id
}

// --- isolation ---

// TestIsolationProperty: 5 orgs, 50 subscribers with assorted filters, 10,000 random events.
// Every subscriber receives exactly the events an independent oracle says it may see, in commit
// order, and nothing of another org.
func TestIsolationProperty(t *testing.T) {
	rng := rand.New(rand.NewPCG(24, 10))
	orgs := []string{"org_a", "org_b", "org_c", "org_d", "org_e"}
	agents := map[string][]string{}
	agentOf := map[string]string{}
	actions := map[string][]string{}
	for _, o := range orgs {
		for i := range 3 {
			agents[o] = append(agents[o], fmt.Sprintf("agt_%s_%d", o, i))
		}
		for i := range 40 {
			a := fmt.Sprintf("act_%s_%d", o, i)
			actions[o] = append(actions[o], a)
			agentOf[a] = agents[o][i%3]
		}
	}
	hyd := newFakeHydrator(agentOf)
	h, in := startHub(t, HubOptions{Instance: "prop1", SubBuffer: 1 << 14, Ring: 1 << 14, Hydrator: hyd, Coalesce: 5 * time.Millisecond, MaxPerUser: 100})

	type subscriber struct {
		f   Filter
		s   *Sub
		got []string
	}
	var subs []*subscriber
	for i := range 50 {
		o := orgs[i%5]
		f := Filter{Org: o, Owner: fmt.Sprintf("usr_%d", i)}
		switch i % 5 {
		case 1:
			f.Types = []string{"action.*"}
		case 2:
			f.Agent, f.AgentKey = agents[o][i%3], true
		case 3:
			f.Agent = agents[o][(i+1)%3]
		case 4:
			f.Types = []string{"deed.sealed", "approval.*", "org.stopped"}
		}
		s, _, _, _ := mustSub(t, h, f, "")
		subs = append(subs, &subscriber{f: f, s: s})
	}
	type sent struct {
		ev    *ledger.Event
		agent string
	}
	var all []sent
	for n := range 10_000 {
		o := orgs[rng.IntN(5)]
		act := actions[o][rng.IntN(40)]
		var ev *ledger.Event
		switch rng.IntN(7) {
		case 0, 1:
			ev = evt(n, o, ledger.EventActionCreated, act)
		case 2:
			ev = evt(n, o, ledger.EventActionWitnessed, act)
		case 3:
			ev = evt(n, o, ledger.EventDeedSealed, "deed_"+act, withAction(act), withKind("action"))
		case 4:
			ev = evt(n, o, ledger.EventApprovalRequested, "apr_"+act, withAction(act), withAgent(agentOf[act]))
		case 5:
			ag := agents[o][rng.IntN(3)]
			ev = evt(n, o, ledger.EventAgentStopped, ag)
			all = append(all, sent{ev, ag})
			in <- ledger.BusMsg{Event: ev}
			continue
		default:
			ev = evt(n, o, ledger.EventOrgStopped, o)
			all = append(all, sent{ev, ""})
			in <- ledger.BusMsg{Event: ev}
			continue
		}
		all = append(all, sent{ev, agentOf[act]})
		in <- ledger.BusMsg{Event: ev}
	}
	// The oracle.
	want := make([][]string, len(subs))
	for i, sb := range subs {
		for _, x := range all {
			ev := x.ev
			if ev.Org != sb.f.Org || !typeMatch(sb.f.Types, ev.Type) {
				continue
			}
			if sb.f.AgentKey {
				visible := strings.HasPrefix(ev.Type, "action.") || ev.Type == ledger.EventDeedSealed || strings.HasPrefix(ev.Type, "approval.")
				if !visible || x.agent != sb.f.Agent {
					continue
				}
			} else if sb.f.Agent != "" && x.agent != sb.f.Agent {
				continue
			}
			want[i] = append(want[i], ev.Event)
		}
	}
	for i, sb := range subs {
		for _, e := range recv(t, sb.s, len(want[i])) {
			m := decode(t, e)
			if m["org"] != sb.f.Org {
				t.Fatalf("subscriber %d (%s) received an event of %v", i, sb.f.Org, m["org"])
			}
			sb.got = append(sb.got, m["id"].(string))
		}
		quiet(t, sb.s, 0)
		if !slices.Equal(sb.got, want[i]) {
			t.Fatalf("subscriber %d (%+v): %d events, want %d (first difference matters: order is commit order)", i, sb.f, len(sb.got), len(want[i]))
		}
	}
	if st := h.Stats(); st.Events != 10_000 || st.Lagged != 0 {
		t.Fatalf("stats %+v", st)
	}
}

// --- resume ---

func TestResume(t *testing.T) {
	h, in := startHub(t, HubOptions{Instance: "inst1", Ring: 8})
	live, _, resync, pos := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_1"}, "")
	if resync != "" || pos != "inst1.0" {
		t.Fatalf("fresh: %q %q", resync, pos)
	}
	for n := range 5 {
		in <- ledger.BusMsg{Event: evt(n, "org_a", ledger.EventActionCreated, fmt.Sprintf("act_%d", n))}
		in <- ledger.BusMsg{Event: evt(100+n, "org_b", ledger.EventActionCreated, fmt.Sprintf("act_b%d", n))} // another org, other seqs
	}
	got := recv(t, live, 5)
	mid := got[1].Cursor

	t.Run("in ring: exact replay, no duplicates", func(t *testing.T) {
		s, replay, resync, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_2"}, mid)
		if resync != "" || len(replay) != 3 {
			t.Fatalf("resync %q, %d replayed", resync, len(replay))
		}
		for i, e := range replay {
			if e.Cursor != got[i+2].Cursor || !slices.Equal(e.Data, got[i+2].Data) {
				t.Fatalf("replay %d: %s", i, e.Cursor)
			}
		}
		in <- ledger.BusMsg{Event: evt(5, "org_a", ledger.EventActionCreated, "act_5")}
		next := recv(t, s, 1)[0]
		if evtID(t, next) != "evt_00000000000000000000000005" {
			t.Fatalf("then live: %s", next.Data)
		}
		recv(t, live, 1)
	})
	t.Run("latest cursor: nothing to replay", func(t *testing.T) {
		_, replay, resync, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_3"}, pos5(h))
		if resync != "" || len(replay) != 0 {
			t.Fatalf("resync %q, %d replayed", resync, len(replay))
		}
	})
	t.Run("other instance", func(t *testing.T) {
		_, replay, resync, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_4"}, "zzzzzz.3")
		if resync != ResyncOtherInstance || replay != nil {
			t.Fatalf("%q", resync)
		}
	})
	t.Run("expired: past the ring", func(t *testing.T) {
		for n := range 20 {
			in <- ledger.BusMsg{Event: evt(200+n, "org_a", ledger.EventActionCreated, fmt.Sprintf("act_x%d", n))}
		}
		recv(t, live, 20)
		_, replay, resync, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_5"}, mid)
		if resync != ResyncCursorExpired || replay != nil {
			t.Fatalf("%q %d", resync, len(replay))
		}
		_, _, resync, _ = mustSub(t, h, Filter{Org: "org_a", Owner: "usr_5"}, "inst1.99999")
		if resync != ResyncCursorExpired {
			t.Fatalf("a cursor from the future: %q", resync)
		}
	})
	t.Run("a new ring can't serve an old cursor", func(t *testing.T) {
		_, _, resync, _ := mustSub(t, h, Filter{Org: "org_new", Owner: "usr_6"}, "inst1.2")
		if resync != ResyncCursorExpired {
			t.Fatalf("%q", resync)
		}
	})
	t.Run("bus gap", func(t *testing.T) {
		other, _, _, _ := mustSub(t, h, Filter{Org: "org_b", Owner: "usr_7"}, "")
		before := pos5(h)
		in <- ledger.BusMsg{Gap: true}
		for _, s := range []*Sub{live, other} {
			e := recv(t, s, 1)[0]
			if e.Type != "resync" || decode(t, e)["reason"] != ResyncBusReconnected {
				t.Fatalf("a Gap reaches everyone as resync: %s %s", e.Type, e.Data)
			}
		}
		_, _, resync, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_8"}, before)
		if resync != ResyncBusReconnected {
			t.Fatalf("a cursor from before the gap: %q", resync)
		}
		after := pos5(h)
		_, replay, resync, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_8"}, after)
		if resync != "" || len(replay) != 0 {
			t.Fatalf("a cursor from after the gap resumes: %q", resync)
		}
	})
}

// pos5 is the hub's position now.
func pos5(h *Hub) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return fmt.Sprintf("%s.%d", h.o.Instance, h.seq)
}

func TestResumeExpiresByAge(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC).UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()) }
	h, in := startHub(t, HubOptions{Instance: "age1", Now: clock})
	live, _, _, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_1"}, "")
	in <- ledger.BusMsg{Event: evt(1, "org_a", ledger.EventActionCreated, "act_1")}
	in <- ledger.BusMsg{Event: evt(2, "org_a", ledger.EventActionCreated, "act_2")}
	got := recv(t, live, 2)
	_, replay, resync, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_2"}, got[0].Cursor)
	if resync != "" || len(replay) != 1 {
		t.Fatalf("within 5 minutes: %q %d", resync, len(replay))
	}
	now.Add(int64(6 * time.Minute))
	_, _, resync, _ = mustSub(t, h, Filter{Org: "org_a", Owner: "usr_3"}, got[0].Cursor)
	if resync != ResyncCursorExpired {
		t.Fatalf("after 5 minutes: %q", resync)
	}
}

// --- backpressure ---

func TestBackpressure(t *testing.T) {
	h, in := startHub(t, HubOptions{Instance: "bp1", SubBuffer: 4, MaxPerUser: 100})
	stalled, _, _, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_1"}, "")
	var fast []*Sub
	for i := range 5 {
		s, _, _, _ := mustSub(t, h, Filter{Org: "org_a", Owner: fmt.Sprintf("usr_f%d", i)}, "")
		fast = append(fast, s)
	}
	var wg sync.WaitGroup
	lat := make([][]time.Duration, len(fast))
	for i, s := range fast {
		wg.Go(func() {
			for range 200 {
				select {
				case e := <-s.C():
					lat[i] = append(lat[i], time.Since(e.queued))
				case <-s.Lagged():
					t.Errorf("fast subscriber %d lagged", i)
					return
				case <-time.After(10 * time.Second):
					t.Errorf("fast subscriber %d starved", i)
					return
				}
			}
		})
	}
	for n := range 200 {
		in <- ledger.BusMsg{Event: evt(n, "org_a", ledger.EventActionCreated, fmt.Sprintf("act_%d", n))}
		if n%4 == 3 {
			time.Sleep(time.Millisecond) // a paced producer: the fast readers keep up, the stalled one doesn't
		}
	}
	select {
	case <-stalled.Lagged():
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled subscriber is marked lagged")
	}
	wg.Wait()
	for i := range fast {
		sort.Slice(lat[i], func(a, b int) bool { return lat[i][a] < lat[i][b] })
		// Blocked behind the stalled one, they would wait seconds; the bound leaves room for a busy
		// machine running -race (make bench-events measures the real p99).
		if p99 := lat[i][len(lat[i])*99/100]; p99 > 250*time.Millisecond {
			t.Errorf("fast subscriber %d: p99 %v with a stalled neighbour", i, p99)
		}
	}
	if st := h.Stats(); st.Lagged != 1 || st.Streams != 5 {
		t.Fatalf("stats %+v", st)
	}
}

// --- limits ---

func TestLimits(t *testing.T) {
	h, _ := startHub(t, HubOptions{Instance: "lim1", MaxPerOrg: 10})
	var subs []*Sub
	for range 8 {
		s, _, _, _, err := h.Subscribe(Filter{Org: "org_a", Owner: "usr_1"}, "")
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, s)
	}
	if _, _, _, _, err := h.Subscribe(Filter{Org: "org_a", Owner: "usr_1"}, ""); !errors.Is(err, ErrTooManyForUser) {
		t.Fatalf("the 9th: %v", err)
	}
	subs[0].Close()
	subs[0].Close()
	s, _, _, _, err := h.Subscribe(Filter{Org: "org_a", Owner: "usr_1"}, "")
	if err != nil {
		t.Fatalf("after closing one: %v", err)
	}
	subs[0] = s
	for i := range 2 {
		if _, _, _, _, err := h.Subscribe(Filter{Org: "org_a", Owner: fmt.Sprintf("usr_%d", i+2)}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, _, err := h.Subscribe(Filter{Org: "org_a", Owner: "usr_9"}, ""); !errors.Is(err, ErrTooManyForOrg) {
		t.Fatalf("over the org cap: %v", err)
	}
	if _, _, _, _, err := h.Subscribe(Filter{Org: "org_b", Owner: "usr_9"}, ""); err != nil {
		t.Fatalf("another org isn't affected: %v", err)
	}
	h.Close()
	select {
	case <-subs[1].Done():
	default:
		t.Fatal("Close ends every stream")
	}
	if _, _, _, _, err := h.Subscribe(Filter{Org: "org_a", Owner: "usr_x"}, ""); !errors.Is(err, ErrHubClosed) {
		t.Fatal(err)
	}
}

// --- hydration ---

func TestHydrationBatches(t *testing.T) {
	agentOf := map[string]string{}
	for i := range 100 {
		agentOf[fmt.Sprintf("act_%d", i)] = "agt_1"
	}
	hyd := newFakeHydrator(agentOf)
	h := NewHub(HubOptions{Instance: "hyd1", Hydrator: hyd, Coalesce: 250 * time.Millisecond})
	s, _, _, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_1"}, "")
	in := make(chan ledger.BusMsg, 200)
	for i := range 100 { // 100 actions within one 50 ms window
		in <- ledger.BusMsg{Event: evt(i, "org_a", ledger.EventActionCreated, fmt.Sprintf("act_%d", i))}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.Run(ctx, in) }()
	defer func() { cancel(); <-done }()
	got := recv(t, s, 100)
	if hyd.calls["actions"] != 1 || hyd.idsSeen["actions"] != 100 {
		t.Fatalf("100 actions in one window → %d calls for %d ids, want 1 for 100", hyd.calls["actions"], hyd.idsSeen["actions"])
	}
	for _, e := range got {
		m := decode(t, e)
		if m["hydrated"] != true || m["livemode"] != true {
			t.Fatalf("hydrated: %s", e.Data)
		}
	}
	// A short burst waits for the window (250 ms here), then goes out in one call.
	for i := range 10 {
		in <- ledger.BusMsg{Event: evt(200+i, "org_a", ledger.EventActionExecuted, fmt.Sprintf("act_%d", i))}
	}
	recv(t, s, 10)
	if hyd.calls["actions"] != 2 {
		t.Fatalf("10 events in one window: %d calls", hyd.calls["actions"])
	}
	// The hydrator fails: the thin event goes out, hydrated:false.
	hyd.fail.Store(true)
	in <- ledger.BusMsg{Event: evt(300, "org_a", ledger.EventActionExecuted, "act_1")}
	m := decode(t, recv(t, s, 1)[0])
	obj := m["data"].(map[string]any)["object"].(map[string]any)
	if m["hydrated"] != false || obj["id"] != "act_1" || obj["object"] != "action" || obj["agent"] != nil {
		t.Fatalf("thin on error: %v", m)
	}
	if h.Stats().HydrateErrors != 1 {
		t.Fatal("errors are counted")
	}
}

// An org nobody watches is not hydrated.
func TestHydrationOnlyWatchedOrgs(t *testing.T) {
	hyd := newFakeHydrator(map[string]string{})
	h, in := startHub(t, HubOptions{Instance: "hyd2", Hydrator: hyd})
	s, _, _, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_1"}, "")
	s.Close() // org_a keeps its ring for resume, but nobody is watching
	in <- ledger.BusMsg{Event: evt(1, "org_a", ledger.EventActionCreated, "act_1")}
	in <- ledger.BusMsg{Event: evt(2, "org_z", ledger.EventActionCreated, "act_2")} // never watched: dropped
	time.Sleep(150 * time.Millisecond)
	if hyd.calls["actions"] != 0 {
		t.Fatalf("hydrated %d times with no subscribers", hyd.calls["actions"])
	}
	_, replay, resync, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_1"}, "hyd2.0")
	if resync != "" || len(replay) != 1 || decode(t, replay[0])["hydrated"] != false {
		t.Fatalf("the ring kept the thin event: %q %d", resync, len(replay))
	}
	if h.Stats().Events != 1 {
		t.Fatalf("an unwatched org's events aren't kept: %+v", h.Stats())
	}
}

// keysOf returns the top-level keys of a JSON object.
func keysOf(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// Hydrated objects never carry a field the REST list doesn't (S24 §3.11), and approvals never
// carry context, the approver spec or the per-person you.
func TestHydratedKeysSubsetOfREST(t *testing.T) {
	hyd := newFakeHydrator(map[string]string{"act_1": "agt_1"})
	// The full approval as a person's REST list shows it (context, approvers, you, countersigns).
	hyd.approval = func(id string) any {
		return approve.Approval{Object: "approval", ID: id, Agent: approve.AgentRef{ID: "agt_1", Name: "bot"}, Status: approve.StatusPending,
			Approvers: []string{"user:priya@acme.test"}, Context: json.RawMessage(`{"v":1}`), You: &approve.You{Eligible: true},
			Countersigned: []approve.Countersign{{User: approve.UserRef{ID: "usr_1", Name: "Priya"}}}, DeniedBy: &approve.UserRef{ID: "usr_2"},
			ActionStatus: "pending_approval"}
	}
	h, in := startHub(t, HubOptions{Instance: "keys1", Hydrator: hyd})
	s, _, _, _ := mustSub(t, h, Filter{Org: "org_a", Owner: "usr_1"}, "")
	agentSub, _, _, _ := mustSub(t, h, Filter{Org: "org_a", Agent: "agt_1", AgentKey: true, Owner: "key_1"}, "")
	in <- ledger.BusMsg{Event: evt(1, "org_a", ledger.EventActionCreated, "act_1")}
	in <- ledger.BusMsg{Event: evt(2, "org_a", ledger.EventApprovalRequested, "apr_1", withAction("act_1"), withAgent("agt_1"),
		func(e *ledger.Event) { e.Status = sp("pending"); n := 1; e.Required = &n; c := 0; e.Countersigned = &c })}
	in <- ledger.BusMsg{Event: evt(3, "org_a", ledger.EventAgentStopped, "agt_1")}
	in <- ledger.BusMsg{Event: evt(4, "org_a", ledger.EventDeedSealed, "deed_1", withAction("act_1"), withKind("action"),
		func(e *ledger.Event) { n := int64(7); e.Seq = &n })}
	got := recv(t, s, 4)
	rest := map[string]map[string]bool{
		"action":   keysOf(t, ledger.Action{Undo: &ledger.ActionUndo{}}),
		"approval": keysOf(t, approve.Approval{Approvers: []string{"x"}, Context: json.RawMessage(`{}`), You: &approve.You{}, DeniedBy: &approve.UserRef{}, ActionStatus: "x"}),
		"agent":    keysOf(t, identity.Agent{}),
	}
	envelopeKeys := keysOf(t, ledger.WebhookEnvelope{})
	for _, e := range got {
		m := decode(t, e)
		if m["hydrated"] != true {
			t.Fatalf("%s isn't hydrated", e.Type)
		}
		for k := range m {
			if k != "hydrated" && !envelopeKeys[k] {
				t.Errorf("%s: envelope key %q isn't in S22's envelope", e.Type, k)
			}
		}
		obj := m["data"].(map[string]any)["object"].(map[string]any)
		kind := obj["object"].(string)
		if kind == "deed_summary" {
			want := []string{"action", "id", "issued_at", "kind", "object", "seq"}
			var ks []string
			for k := range obj {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			if !reflect.DeepEqual(ks, want) || obj["kind"] != "action" || obj["seq"] != float64(7) {
				t.Errorf("deed_summary: %v", obj)
			}
			continue
		}
		for k := range obj {
			if !rest[kind][k] {
				t.Errorf("%s: %q isn't a field of the REST list's %s", e.Type, k, kind)
			}
		}
		if kind == "approval" {
			for _, hidden := range approvalHidden {
				if _, ok := obj[hidden]; ok {
					t.Errorf("approval carries %q", hidden)
				}
			}
			if strings.Contains(string(e.Data), "priya@acme.test") {
				t.Error("an email address in an event")
			}
		}
	}
	// The agent key: its action, its approval in the agent view (thin), its deed; never agent.*.
	ag := recv(t, agentSub, 3)
	if ag[0].Type != ledger.EventActionCreated || ag[1].Type != ledger.EventApprovalRequested || ag[2].Type != ledger.EventDeedSealed {
		t.Fatalf("agent key saw %s %s %s", ag[0].Type, ag[1].Type, ag[2].Type)
	}
	am := decode(t, ag[1])
	aobj := am["data"].(map[string]any)["object"].(map[string]any)
	if am["hydrated"] != false || aobj["countersigned"] != float64(0) || aobj["approvers"] != nil || aobj["context"] != nil {
		t.Fatalf("approval in the agent view: %s", ag[1].Data)
	}
	quiet(t, agentSub, 50*time.Millisecond)
}

// An agent key never sees another agent's events, an admin deed, or agent/org/policy events; if an
// action can't be attributed (the read failed and nothing is known), the stream resyncs instead of
// guessing.
func TestAgentKeyFilter(t *testing.T) {
	hyd := newFakeHydrator(map[string]string{"act_mine": "agt_me", "act_theirs": "agt_other"})
	h, in := startHub(t, HubOptions{Instance: "agk1", Hydrator: hyd})
	mine, _, _, _ := mustSub(t, h, Filter{Org: "org_a", Agent: "agt_me", AgentKey: true, Owner: "key_1"}, "")
	for i, ev := range []*ledger.Event{
		evt(1, "org_a", ledger.EventActionCreated, "act_theirs"),
		evt(2, "org_a", ledger.EventActionCreated, "act_mine"),
		evt(3, "org_a", ledger.EventDeedSealed, "deed_x", withKind("admin")),
		evt(4, "org_a", ledger.EventAgentStopped, "agt_me"),
		evt(5, "org_a", ledger.EventOrgStopped, "org_a"),
		evt(6, "org_a", ledger.EventPolicyActivated, "pa_1"),
		evt(7, "org_b", ledger.EventActionCreated, "act_mine"),
		evt(8, "org_a", ledger.EventApprovalRequested, "apr_t", withAction("act_theirs"), withAgent("agt_other")),
		evt(9, "org_a", ledger.EventDeedSealed, "deed_m", withAction("act_mine"), withKind("action")),
	} {
		_ = i
		in <- ledger.BusMsg{Event: ev}
	}
	got := recv(t, mine, 2)
	if evtID(t, got[0]) != "evt_00000000000000000000000002" || evtID(t, got[1]) != "evt_00000000000000000000000009" {
		t.Fatalf("agent key: %s, %s", got[0].Data, got[1].Data)
	}
	quiet(t, mine, 100*time.Millisecond)
	hyd.fail.Store(true)
	in <- ledger.BusMsg{Event: evt(10, "org_a", ledger.EventActionCreated, "act_unknown")}
	select {
	case <-mine.Lagged():
	case <-time.After(5 * time.Second):
		t.Fatal("an unattributable action resyncs the agent key's stream")
	}
}

func TestParseCursorAndTypes(t *testing.T) {
	for c, ok := range map[string]bool{"7f3k2m.1842": true, "a.0": true, "": false, "7f3k2m": false, "7F3K2M.1": false,
		"x.-1": false, "x.1.2": false, "x.18446744073709551616": false, ".1": false} {
		if _, _, got := ParseCursor(c); got != ok {
			t.Errorf("ParseCursor(%q) = %v", c, got)
		}
	}
	good := []string{"action.*", "deed.sealed", "action.created,approval.*", "decoder.alert", "policy.activated", "agent.*"}
	for _, g := range good {
		if _, err := parseTypes(g); err != nil {
			t.Errorf("%q: %v", g, err)
		}
	}
	for _, b := range []string{"*", "action", "action.nope", "nope.*", "webhook.test", "deed.*,", "ACTION.*", "a.b.c", strings.Repeat("action.*,", 33)} {
		if _, err := parseTypes(b); err == nil {
			t.Errorf("%q accepted", b)
		}
	}
	if !typeMatch([]string{"action.*"}, "action.created") || typeMatch([]string{"action.*"}, "actions.created") || !typeMatch(nil, "x") {
		t.Fatal("typeMatch")
	}
}
