package gate

import (
	"encoding/json"
	"sync"

	"github.com/deedglass/deedglass/internal/ledger"
)

// Broker wakes long polls (GET /v1/actions/{id}?wait=, and POST /v1/actions waiting on an
// in-flight duplicate) when an action changes. It is fed by the instance's one LISTEN connection
// on deedglass_events (S06's payload: ids and states only) and by the pipeline itself after each
// commit, so a wait on this instance doesn't depend on the round trip. A waiter also re-reads on a
// timer, so a lost notification costs latency, never correctness. Since S24 it is an inline
// handler on ledger.Bus (OnBus), next to the Console's event hub.
type Broker struct {
	mu   sync.Mutex
	subs map[string]map[chan struct{}]struct{}
}

// NewBroker returns an empty broker.
func NewBroker() *Broker { return &Broker{subs: map[string]map[chan struct{}]struct{}{}} }

// Subscribe returns a channel that receives (without blocking the sender) when action id changes,
// and its cancel.
func (b *Broker) Subscribe(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	m := b.subs[id]
	if m == nil {
		m = map[chan struct{}]struct{}{}
		b.subs[id] = m
	}
	m[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs[id], ch)
		if len(b.subs[id]) == 0 {
			delete(b.subs, id)
		}
		b.mu.Unlock()
	}
}

// Notify wakes every waiter on action id.
func (b *Broker) Notify(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// OnEvent is the deedglass_events handler: it wakes the waiters of the event's action.
func (b *Broker) OnEvent(payload string) {
	var ev struct {
		Action *string `json:"action"`
	}
	if json.Unmarshal([]byte(payload), &ev) == nil && ev.Action != nil {
		b.Notify(*ev.Action)
	}
}

// OnBus is the ledger.Bus handler: an event wakes its action's waiters, a Gap wakes everyone.
func (b *Broker) OnBus(m ledger.BusMsg) {
	switch {
	case m.Gap:
		b.Reset()
	case m.Event != nil && m.Event.Action != nil:
		b.Notify(*m.Event.Action)
	}
}

// Reset wakes every waiter (the listener reconnected; anything may have changed meanwhile).
func (b *Broker) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, m := range b.subs {
		for ch := range m {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

// Waiters is the number of open subscriptions (a gauge for tests and metrics).
func (b *Broker) Waiters() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, m := range b.subs {
		n += len(m)
	}
	return n
}
