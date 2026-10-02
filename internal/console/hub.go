package console

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/ledger"
)

// The Console's event hub (S24 §3.11). It reads ledger.Bus, keeps a short ring of each watched
// org's events, hydrates them into the REST list shapes for the orgs someone is watching, and fans
// them out to the GET /v1/events streams of this instance.
//
//   - Isolation: rings and subscriber sets are per org; an event goes only to its own org's
//     subscribers, and an agent key's subscriber only sees its own agent's events, in the agent's
//     view.
//   - Order: one goroutine (Run) reads the bus in commit order and appends to the rings in that
//     order; each subscriber receives its events in that order.
//   - Honesty: whenever a subscriber may have missed something (a cursor the ring can't serve, a
//     bus Gap, a full buffer) it gets a resync instead.
//   - Nobody waits for a slow client: fan-out never blocks. A full buffer marks the subscriber
//     lagged, and its writer sends resync and closes.

// HubOptions configure a Hub. Zero values take the S24 defaults.
type HubOptions struct {
	// Instance names this process in stream cursors (<instance>.<seq>); default 6 random characters.
	Instance string
	// Ring is how many events each watched org keeps for resume (1024), none older than MaxAge (5 min).
	Ring   int
	MaxAge time.Duration
	// SubBuffer is each subscriber's queue (256 events).
	SubBuffer int
	// Heartbeat (15 s), MaxLifetime (30 min) and Revalidate (60 s) are the stream writer's timers.
	Heartbeat, MaxLifetime, Revalidate time.Duration
	// Coalesce batches events for hydration: 50 ms, or CoalesceIDs ids (100), whichever comes first.
	Coalesce    time.Duration
	CoalesceIDs int
	// MaxPerUser (8, per person or key) and MaxPerOrg (200 per instance) cap open streams.
	MaxPerUser, MaxPerOrg int
	// HydrateTimeout bounds one batch's reads (2 s); past it the events go out thin.
	HydrateTimeout time.Duration
	// Hydrator reads the list shapes; nil sends every event thin (hydrated:false).
	Hydrator Hydrator
	Log      *slog.Logger
	Now      func() time.Time
}

// S24 defaults.
const (
	DefaultRing           = 1024
	DefaultMaxAge         = 5 * time.Minute
	DefaultSubBuffer      = 256
	DefaultHeartbeat      = 15 * time.Second
	DefaultMaxLifetime    = 30 * time.Minute
	DefaultRevalidate     = 60 * time.Second
	DefaultCoalesce       = 50 * time.Millisecond
	DefaultCoalesceIDs    = 100
	DefaultMaxPerUser     = 8
	DefaultMaxPerOrg      = 200
	DefaultHydrateTimeout = 2 * time.Second
	// BusBuffer is the hub's queue on ledger.Bus: hydration may pause reading for a moment.
	BusBuffer = 4096
)

func (o HubOptions) withDefaults() HubOptions {
	set := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	setD := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	set(&o.Ring, DefaultRing)
	set(&o.SubBuffer, DefaultSubBuffer)
	set(&o.CoalesceIDs, DefaultCoalesceIDs)
	set(&o.MaxPerUser, DefaultMaxPerUser)
	set(&o.MaxPerOrg, DefaultMaxPerOrg)
	setD(&o.MaxAge, DefaultMaxAge)
	setD(&o.Heartbeat, DefaultHeartbeat)
	setD(&o.MaxLifetime, DefaultMaxLifetime)
	setD(&o.Revalidate, DefaultRevalidate)
	setD(&o.Coalesce, DefaultCoalesce)
	setD(&o.HydrateTimeout, DefaultHydrateTimeout)
	if o.Instance == "" {
		o.Instance = newInstance()
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// newInstance is 6 random characters of the id alphabet.
func newInstance() string {
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// Resync reasons (the resync event's reason).
const (
	ResyncCursorExpired  = "cursor_expired"  // the cursor is older than what the ring keeps
	ResyncOtherInstance  = "other_instance"  // the cursor came from another instance (or before a restart)
	ResyncLagged         = "lagged"          // this stream fell behind, or an event couldn't be attributed
	ResyncBusReconnected = "bus_reconnected" // the LISTEN connection reconnected: events may be missing
)

// Filter is what one stream receives.
type Filter struct {
	Org string
	// Agent narrows the stream to one agent's events. With AgentKey set (an agent key's stream) it is
	// the key's own agent, and the events are in the agent's view.
	Agent    string
	AgentKey bool
	// Types are exact event types or prefix wildcards (action.*); empty means every type.
	Types []string
	// Owner is who the stream counts against MaxPerUser: the person (usr_…) or the key (key_…).
	Owner string
}

// Envelope is one SSE message as the writer sends it: id: Cursor (when set), event: Type, data: Data.
type Envelope struct {
	Cursor string
	Type   string
	Data   []byte
	queued time.Time // when fan-out queued it (the bench's enqueue→write)
}

// Errors from Subscribe.
var (
	ErrTooManyForUser = errors.New("console: too many live connections for this person or key")
	ErrTooManyForOrg  = errors.New("console: too many live connections for this org on this instance")
	ErrHubClosed      = errors.New("console: the event hub is shut down")
)

// Hub is the process's event fan-out. Run must be running for events to flow.
type Hub struct {
	o    HubOptions
	mu   sync.Mutex
	seq  uint64 // one counter for every org: a cursor's seq is unique in this process
	orgs map[string]*orgState
	// owners counts open streams per Owner.
	owners map[string]int
	done   chan struct{}
	closed bool

	stats struct {
		events, hydrated, thin, hydrateErrors, hydrateCalls, lagged, gaps, resyncs atomic.Int64
	}
}

type orgState struct {
	id string
	// floor: a cursor below it can't be resumed (the ring started after it). gapAt: the position of
	// the last bus Gap (a cursor below it missed events). evicted: the newest seq dropped from the
	// ring.
	floor, gapAt, evicted uint64
	ring                  []*item // circular, len Ring once full
	head, n               int
	subs                  map[*Sub]struct{}
	idleSince             time.Time
	agents                map[string]string // act_… → agt_…, learned from hydration and approvals
}

// item is one event as kept in the ring: the bytes for org-wide streams, and for agent keys'
// streams (nil when agent keys can't see it).
type item struct {
	seq    uint64
	at     time.Time
	typ    string
	agent  string // the agent the event is about, "" when none or unknown
	full   []byte
	agentV []byte
}

// NewHub returns a hub; start Run with its bus subscription.
func NewHub(o HubOptions) *Hub {
	return &Hub{o: o.withDefaults(), orgs: map[string]*orgState{}, owners: map[string]int{}, done: make(chan struct{})}
}

// Instance is the instance part of this hub's cursors.
func (h *Hub) Instance() string { return h.o.Instance }

// Options returns the hub's options with defaults applied.
func (h *Hub) Options() HubOptions { return h.o }

// Sub is one stream's subscription.
type Sub struct {
	h      *Hub
	f      Filter
	org    *orgState
	ch     chan *Envelope
	lagged chan struct{}
	once   sync.Once
	closed atomic.Bool
}

// C delivers the stream's events (and in-band resyncs) in order.
func (s *Sub) C() <-chan *Envelope { return s.ch }

// Lagged is closed when the subscriber fell behind: send resync lagged and close the stream.
func (s *Sub) Lagged() <-chan struct{} { return s.lagged }

// Done is closed when the hub shuts down.
func (s *Sub) Done() <-chan struct{} { return s.h.done }

// Cursor is the stream's position now (the hello id).
func (s *Sub) cursor(seq uint64) string { return s.h.o.Instance + "." + strconv.FormatUint(seq, 10) }

// Close ends the subscription.
func (s *Sub) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	h := s.h
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(s)
}

func (h *Hub) removeLocked(s *Sub) {
	if _, ok := s.org.subs[s]; !ok {
		return
	}
	delete(s.org.subs, s)
	if len(s.org.subs) == 0 {
		s.org.idleSince = h.o.Now()
	}
	if h.owners[s.f.Owner]--; h.owners[s.f.Owner] <= 0 {
		delete(h.owners, s.f.Owner)
	}
}

// lag marks s lagged (once) and removes it from fan-out. Called with h.mu held.
func (s *Sub) lagLocked() {
	s.once.Do(func() {
		s.h.stats.lagged.Add(1)
		close(s.lagged)
		s.h.removeLocked(s)
	})
}

// ParseCursor splits <instance>.<seq>.
func ParseCursor(c string) (instance string, seq uint64, ok bool) {
	inst, n, found := strings.Cut(c, ".")
	if !found || inst == "" || len(inst) > 32 || len(n) == 0 || len(n) > 19 {
		return "", 0, false
	}
	for _, r := range inst {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') {
			return "", 0, false
		}
	}
	seq, err := strconv.ParseUint(n, 10, 64)
	if err != nil {
		return "", 0, false
	}
	return inst, seq, true
}

// Subscribe opens a stream for f. With a cursor it replays what the org's ring holds after it, or
// names why it can't (resync: cursor_expired, other_instance or bus_reconnected). position is the
// stream's cursor before any live event: the hello's id.
func (h *Hub) Subscribe(f Filter, cursor string) (s *Sub, replay []*Envelope, resync string, position string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, "", "", ErrHubClosed
	}
	if f.Owner != "" && h.owners[f.Owner] >= h.o.MaxPerUser {
		return nil, nil, "", "", ErrTooManyForUser
	}
	org := h.orgs[f.Org]
	if org != nil && len(org.subs) >= h.o.MaxPerOrg {
		return nil, nil, "", "", ErrTooManyForOrg
	}
	if org == nil {
		// A brand-new ring can serve a cursor taken now (h.seq), not one from before.
		org = &orgState{id: f.Org, floor: h.seq, ring: make([]*item, 0, min(h.o.Ring, 64)),
			subs: map[*Sub]struct{}{}, agents: map[string]string{}}
		h.orgs[f.Org] = org
	}
	h.pruneLocked(org, h.o.Now())
	s = &Sub{h: h, f: f, org: org, ch: make(chan *Envelope, h.o.SubBuffer), lagged: make(chan struct{})}
	if cursor != "" {
		inst, seq, ok := ParseCursor(cursor)
		switch {
		case !ok || inst != h.o.Instance:
			resync = ResyncOtherInstance
		case seq > h.seq:
			resync = ResyncCursorExpired
		case seq < org.gapAt:
			resync = ResyncBusReconnected
		case seq < org.floor || seq < org.evicted:
			resync = ResyncCursorExpired
		default:
			for i := range org.n {
				it := org.ring[(org.head+i)%len(org.ring)]
				if it.seq <= seq {
					continue
				}
				if s.unattributable(it) {
					replay, resync = nil, ResyncLagged
					break
				}
				if e := s.envelopeFor(it); e != nil {
					replay = append(replay, e)
				}
			}
		}
		if resync != "" {
			h.stats.resyncs.Add(1)
		}
	}
	org.subs[s] = struct{}{}
	h.owners[f.Owner]++
	return s, replay, resync, s.cursor(h.seq), nil
}

// pruneLocked drops ring entries older than MaxAge.
func (h *Hub) pruneLocked(org *orgState, now time.Time) {
	for org.n > 0 {
		it := org.ring[org.head]
		if now.Sub(it.at) <= h.o.MaxAge {
			return
		}
		org.evicted = it.seq
		org.ring[org.head] = nil
		org.head = (org.head + 1) % len(org.ring)
		org.n--
	}
}

// appendLocked adds it to the org's ring, evicting the oldest past Ring entries.
func (h *Hub) appendLocked(org *orgState, it *item) {
	if len(org.ring) < h.o.Ring && org.n == len(org.ring) && org.head == 0 {
		org.ring = append(org.ring, it)
		org.n++
		return
	}
	if org.n == len(org.ring) {
		if len(org.ring) < h.o.Ring {
			// Grow (the ring starts small): unroll into a bigger slice.
			grown := make([]*item, 0, min(h.o.Ring, 2*len(org.ring)))
			for i := range org.n {
				grown = append(grown, org.ring[(org.head+i)%len(org.ring)])
			}
			org.ring, org.head = append(grown, it), 0
			org.n++
			return
		}
		org.evicted = org.ring[org.head].seq
		org.ring[org.head] = it
		org.head = (org.head + 1) % len(org.ring)
		return
	}
	org.ring[(org.head+org.n)%len(org.ring)] = it
	org.n++
}

// match reports whether s receives it, and in which view.
func (s *Sub) envelopeFor(it *item) *Envelope {
	if !typeMatch(s.f.Types, it.typ) {
		return nil
	}
	data := it.full
	if s.f.AgentKey {
		if it.agentV == nil || it.agent == "" || it.agent != s.f.Agent {
			return nil
		}
		data = it.agentV
	} else if s.f.Agent != "" && it.agent != s.f.Agent {
		return nil
	}
	return &Envelope{Cursor: s.cursor(it.seq), Type: it.typ, Data: data}
}

// unattributable reports an event an agent key's stream might be allowed to see, but whose agent
// nobody could name (the read failed and nothing was known): the stream may be missing it, so it
// resyncs rather than guess.
func (s *Sub) unattributable(it *item) bool {
	return s.f.AgentKey && it.agentV != nil && it.agent == "" && typeMatch(s.f.Types, it.typ)
}

func typeMatch(types []string, typ string) bool {
	if len(types) == 0 {
		return true
	}
	for _, t := range types {
		if t == typ || (strings.HasSuffix(t, ".*") && strings.HasPrefix(typ, t[:len(t)-1])) {
			return true
		}
	}
	return false
}

// Close shuts the hub: every stream ends (serve calls it when it starts draining).
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		close(h.done)
	}
}

// Run reads the bus until ctx ends (or in closes), then shuts the hub.
func (h *Hub) Run(ctx context.Context, in <-chan ledger.BusMsg) error {
	defer h.Close()
	var (
		pending []*ledger.Event
		ids     int
		timer   = time.NewTimer(time.Hour)
		armed   bool
		sweep   = time.NewTicker(time.Minute)
	)
	timer.Stop()
	defer timer.Stop()
	defer sweep.Stop()
	flush := func() {
		if armed {
			timer.Stop()
			armed = false
		}
		if len(pending) > 0 {
			h.process(ctx, pending)
			clear(pending)
			pending, ids = pending[:0], 0
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-h.done:
			return nil
		case m, ok := <-in:
			if !ok {
				return nil
			}
			if m.Gap {
				flush()
				h.gap()
				continue
			}
			if m.Event == nil || !h.watched(m.Event.Org) {
				continue
			}
			pending = append(pending, m.Event)
			ids++
			if h.o.Hydrator == nil || ids >= h.o.CoalesceIDs {
				flush()
				continue
			}
			if !armed {
				timer.Reset(h.o.Coalesce)
				armed = true
			}
		case <-timer.C:
			armed = false
			flush()
		case <-sweep.C:
			h.sweep()
		}
	}
}

// watched reports whether org has a ring (a stream is open, or was within MaxAge).
func (h *Hub) watched(org string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.orgs[org]
	return ok
}

// sweep forgets orgs nobody has watched for MaxAge.
func (h *Hub) sweep() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.o.Now()
	for id, org := range h.orgs {
		if len(org.subs) == 0 && now.Sub(org.idleSince) > h.o.MaxAge {
			delete(h.orgs, id)
		}
	}
}

// gap handles a bus Gap: no cursor from before it can be resumed, and every stream gets resync
// bus_reconnected in band.
func (h *Hub) gap() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stats.gaps.Add(1)
	h.seq++ // a position with no event: cursors from before it can't be resumed
	for _, org := range h.orgs {
		org.gapAt = h.seq
		e := &Envelope{Cursor: h.o.Instance + "." + strconv.FormatUint(h.seq, 10), Type: "resync",
			Data: resyncData(ResyncBusReconnected), queued: h.o.Now()}
		for s := range org.subs {
			select {
			case s.ch <- e:
				h.stats.resyncs.Add(1)
			default:
				s.lagLocked()
			}
		}
	}
}

// resyncData is the resync event's data.
func resyncData(reason string) []byte {
	b, _ := json.Marshal(struct {
		Object string `json:"object"`
		Reason string `json:"reason"`
	}{"resync", reason})
	return b
}

// subscribed reports, per org, whether anyone is watching it now (hydration runs only for those).
func (h *Hub) subscribed(orgs map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range orgs {
		o := h.orgs[id]
		orgs[id] = o != nil && len(o.subs) > 0
	}
}

// knownAgents copies the org's action → agent map entries for actionIDs.
func (h *Hub) knownAgents(org string, actionIDs map[string]bool) map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]string{}
	if o := h.orgs[org]; o != nil {
		for id := range actionIDs {
			if a, ok := o.agents[id]; ok {
				out[id] = a
			}
		}
	}
	return out
}

// maxAgentMemo bounds each org's action → agent map; past it the map starts over (attribution then
// comes from hydration again).
const maxAgentMemo = 16384

// process hydrates a batch (for watched orgs), appends it to the rings in order and fans it out.
func (h *Hub) process(ctx context.Context, evs []*ledger.Event) {
	orgs := map[string]bool{}
	for _, ev := range evs {
		orgs[ev.Org] = false
	}
	h.subscribed(orgs)
	hyd := map[string]*hydrated{}
	memo := map[string]map[string]string{}
	for org, watching := range orgs {
		acts := map[string]bool{}
		for _, ev := range evs {
			if ev.Org == org && ev.Action != nil {
				acts[*ev.Action] = true
			}
		}
		memo[org] = h.knownAgents(org, acts)
		if watching && h.o.Hydrator != nil {
			hyd[org] = h.hydrate(ctx, org, evs, memo[org])
		}
	}
	now := h.o.Now()
	items := make([]*item, 0, len(evs))
	learned := map[string]map[string]string{}
	for _, ev := range evs {
		it, act, agent := buildItem(ev, hyd[ev.Org], memo[ev.Org])
		it.at = now
		items = append(items, it)
		if act != "" && agent != "" {
			if learned[ev.Org] == nil {
				learned[ev.Org] = map[string]string{}
			}
			learned[ev.Org][act] = agent
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for org, m := range learned {
		if o := h.orgs[org]; o != nil {
			if len(o.agents)+len(m) > maxAgentMemo {
				o.agents = map[string]string{}
			}
			for k, v := range m {
				o.agents[k] = v
			}
		}
	}
	for i, it := range items {
		org := h.orgs[evs[i].Org]
		if org == nil {
			continue // forgotten meanwhile
		}
		h.seq++
		it.seq = h.seq
		h.appendLocked(org, it)
		h.stats.events.Add(1)
		for s := range org.subs {
			if s.unattributable(it) {
				// An event an agent key might be allowed to see, but nobody could say whose it is:
				// the stream may be missing it, so it resyncs.
				s.lagLocked()
				continue
			}
			e := s.envelopeFor(it)
			if e == nil {
				continue
			}
			e.queued = now
			select {
			case s.ch <- e:
			default:
				s.lagLocked()
			}
		}
	}
}

// HubStats are the hub's counters and gauges.
type HubStats struct {
	Events, Hydrated, Thin, HydrateErrors, HydrateCalls, Lagged, Gaps, Resyncs int64
	Orgs, Streams                                                              int
}

// Stats returns the counters (tests, the bench and metrics).
func (h *Hub) Stats() HubStats {
	h.mu.Lock()
	orgs, streams := len(h.orgs), 0
	for _, o := range h.orgs {
		streams += len(o.subs)
	}
	h.mu.Unlock()
	st := &h.stats
	return HubStats{Events: st.events.Load(), Hydrated: st.hydrated.Load(), Thin: st.thin.Load(), HydrateErrors: st.hydrateErrors.Load(),
		HydrateCalls: st.hydrateCalls.Load(), Lagged: st.lagged.Load(), Gaps: st.gaps.Load(), Resyncs: st.resyncs.Load(),
		Orgs: orgs, Streams: streams}
}
