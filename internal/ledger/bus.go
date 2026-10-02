package ledger

import (
	"encoding/json"
	"sync"
	"sync/atomic"
)

// Bus fans the instance's deedglass_events notifications out to everything in the process that
// follows them (S24 §3.11): the Gate's long-poll broker and the webhook worker's wake (inline
// handlers) and the Console's event hub (a channel subscriber).
//
// It opens no connection of its own. The process keeps one LISTEN connection (S08's identity
// listener, generalized in S12), which carries deedglass_events next to deedglass_identity: serve
// wires that listener's callbacks to Notify and Reset. The listener's liveness (its self-ping
// every 500 ms, the silence watchdog, reconnect with backoff) is therefore the bus's liveness, and
// every (re)connect, which may have missed notifications, reaches every subscriber as a Gap.
//
// Postgres delivers notifications only after COMMIT, in commit order, and drops them on
// ROLLBACK; the bus keeps that order for each subscriber.
type Bus struct {
	mu       sync.Mutex
	subs     map[*busSub]struct{}
	handlers []func(BusMsg)

	notified, malformed, dropped atomic.Int64
}

// BusMsg is one message on the bus: an event, or a Gap (the listener reconnected, or this
// subscriber fell behind: events may be missing).
type BusMsg struct {
	Event *Event // shared by every subscriber: read it, never change it
	Gap   bool
}

type busSub struct {
	ch  chan BusMsg
	gap bool // a message was dropped: deliver a Gap before the next event
}

// NewBus returns an empty bus.
func NewBus() *Bus { return &Bus{subs: map[*busSub]struct{}{}} }

// Handle adds an inline handler, called on the listener's goroutine for every message in order.
// It must not block (the Gate's broker and the webhook wake only signal a channel). Add handlers
// before the listener starts.
func (b *Bus) Handle(fn func(BusMsg)) {
	b.mu.Lock()
	b.handlers = append(b.handlers, fn)
	b.mu.Unlock()
}

// Subscribe returns a channel of messages with buf slots, and its cancel. Sends never block the
// listener: when the channel is full the message is dropped and the subscriber's next message is
// a Gap, so a slow subscriber learns it missed something instead of missing it silently.
func (b *Bus) Subscribe(buf int) (<-chan BusMsg, func()) {
	if buf < 1 {
		buf = 1
	}
	s := &busSub{ch: make(chan BusMsg, buf)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, s)
			b.mu.Unlock()
		})
	}
}

// Notify is the deedglass_events callback: it decodes the payload once and delivers it. A payload
// this build can't read is delivered as a Gap (something happened that nobody can name).
func (b *Bus) Notify(payload string) {
	ev := &Event{}
	if err := json.Unmarshal([]byte(payload), ev); err != nil || ev.Type == "" || ev.Org == "" {
		b.malformed.Add(1)
		b.deliver(BusMsg{Gap: true})
		return
	}
	b.notified.Add(1)
	b.deliver(BusMsg{Event: ev})
}

// Reset is the listener's reconnect callback: notifications may have been missed, so every
// subscriber gets a Gap.
func (b *Bus) Reset() { b.deliver(BusMsg{Gap: true}) }

func (b *Bus) deliver(m BusMsg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.handlers {
		h(m)
	}
	for s := range b.subs {
		if s.gap {
			select {
			case s.ch <- BusMsg{Gap: true}:
				s.gap = false
			default:
				b.dropped.Add(1)
				continue // still full: this message is lost too, and the Gap still owed
			}
			if m.Gap {
				continue // the Gap just sent covers this one
			}
		}
		select {
		case s.ch <- m:
		default:
			s.gap = true
			b.dropped.Add(1)
		}
	}
}

// BusStats are the bus's counters.
type BusStats struct {
	Notified, Malformed, Dropped int64
	Subscribers                  int
}

// Stats returns the counters (tests and metrics).
func (b *Bus) Stats() BusStats {
	b.mu.Lock()
	n := len(b.subs)
	b.mu.Unlock()
	return BusStats{Notified: b.notified.Load(), Malformed: b.malformed.Load(), Dropped: b.dropped.Load(), Subscribers: n}
}
