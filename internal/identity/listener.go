package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/store"
)

// channelRE is what Listen accepts: a deedglass_ channel name, safe to place in LISTEN.
var channelRE = regexp.MustCompile(`^deedglass_[a-z]{1,40}$`)

// Listener keeps the principal cache honest across instances (S08 §3.6).
//
// It holds one dedicated connection (not from the pool) running LISTEN deedglass_identity, and
// waits with a context that is never cancelled in normal operation: in pgx v5.11.0 a cancelled
// wait sets a read deadline on the socket (DeadlineContextWatcherHandler), so using timeouts as a
// heartbeat would be fragile. Liveness comes from self-pings instead: every 500 ms the instance
// sends a ping through the pool, and if its own ping hasn't come back for 1.5 s the cache is
// bypassed and every authentication reads Postgres. A connection that stays silent for 5 s is
// abandoned (its wait is cancelled, the connection closed) and a new one is made.
//
// On any listener error: bypass, close, reconnect with backoff (100 ms → 5 s), LISTEN, flush the
// cache, wait for the instance's own ping, then stop bypassing.
//
// S12 generalized it: other packages add channels with Listen (store.ChannelListener), and the
// same connection, self-ping liveness and reconnect logic carry them. deedglass_policy is the
// first: each payload goes to its handler, and every (re)connect runs the reset hooks, because a
// notification may have been missed while the connection was away.
type Listener struct {
	k        *KeyAuth
	instance string
	seq      atomic.Uint64
	lastPing atomic.Int64 // unix nanoseconds when our own ping last arrived
	healthy  atomic.Bool  // listening, and our own ping has arrived since the last reconnect
	pid      atomic.Uint32

	mu     sync.Mutex
	cancel context.CancelFunc // the current connection's wait

	// Tests: drop makes the listener ignore everything it receives (a silent stall without an
	// error); onEvent observes each handled notification.
	drop    atomic.Bool
	onEvent func(t string)

	reconnects atomic.Int64

	chMu     sync.Mutex
	channels map[string]*channelSub
}

type channelSub struct {
	onMsg   func(payload string)
	onReset func()
}

var _ store.ChannelListener = (*Listener)(nil)

// Listen adds a channel to the listener's connection. Call it before Run; a channel added later
// takes effect at the next reconnect, which this forces.
func (l *Listener) Listen(channel string, onMsg func(payload string), onReset func()) {
	if !channelRE.MatchString(channel) || channel == store.IdentityChannel {
		panic("identity: Listen needs a deedglass_* channel other than deedglass_identity, got " + channel)
	}
	l.chMu.Lock()
	if l.channels == nil {
		l.channels = map[string]*channelSub{}
	}
	l.channels[channel] = &channelSub{onMsg: onMsg, onReset: onReset}
	l.chMu.Unlock()
	if l.pid.Load() != 0 {
		l.mu.Lock()
		if l.cancel != nil {
			l.cancel()
		}
		l.mu.Unlock()
	}
}

func (l *Listener) subs() map[string]*channelSub {
	l.chMu.Lock()
	defer l.chMu.Unlock()
	out := make(map[string]*channelSub, len(l.channels))
	for k, v := range l.channels {
		out[k] = v
	}
	return out
}

// Liveness timing.
const (
	PingEvery       = 500 * time.Millisecond
	SilenceBypass   = 1500 * time.Millisecond
	silenceAbandon  = 5 * time.Second
	watchdogEvery   = 100 * time.Millisecond
	reconnectFirst  = 100 * time.Millisecond
	reconnectMax    = 5 * time.Second
	guardPollEvery  = 500 * time.Millisecond
	listenerConnect = 5 * time.Second
)

func newListener(k *KeyAuth) *Listener {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return &Listener{k: k, instance: hex.EncodeToString(b)}
}

// Healthy reports whether the cache is in use (not bypassed because of the listener).
func (l *Listener) Healthy() bool { return l.healthy.Load() }

// PID is the backend process id of the current LISTEN connection, or 0 (tests terminate it).
func (l *Listener) PID() uint32 { return l.pid.Load() }

// Reconnects counts connections made after the first.
func (l *Listener) Reconnects() int64 { return l.reconnects.Load() }

// SetStall makes the listener ignore every notification while on (tests: a silent stall).
func (l *Listener) SetStall(on bool) { l.drop.Store(on) }

func (l *Listener) run(ctx context.Context) {
	if l.k.db == nil {
		return
	}
	var wg sync.WaitGroup
	wg.Go(func() { l.pinger(ctx) })
	wg.Go(func() { l.watchdog(ctx) })
	backoff := reconnectFirst
	first := true
	for ctx.Err() == nil {
		err := l.listenOnce(ctx, first)
		first = false
		l.markUnhealthy()
		if ctx.Err() != nil {
			break
		}
		if err != nil {
			l.k.log.Warn("identity listener lost its connection; authenticating from Postgres until it's back", "error", err.Error())
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, reconnectMax)
		if err == nil {
			backoff = reconnectFirst
		}
	}
	wg.Wait()
}

// listenOnce connects, listens and handles notifications until the connection fails.
func (l *Listener) listenOnce(ctx context.Context, first bool) error {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	l.mu.Lock()
	l.cancel = cancel
	l.mu.Unlock()
	dctx, dcancel := context.WithTimeout(cctx, listenerConnect)
	conn, err := pgx.ConnectConfig(dctx, l.k.db.ListenConfig())
	dcancel()
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, c := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		_ = conn.Close(closeCtx)
		c()
		l.pid.Store(0)
	}()
	if _, err := conn.Exec(cctx, `LISTEN deedglass_identity`); err != nil {
		return err
	}
	subs := l.subs()
	for ch := range subs {
		// The channel name is checked against channelRE in Listen: a fixed identifier, never input.
		if _, err := conn.Exec(cctx, `LISTEN `+ch); err != nil {
			return err
		}
	}
	if !first {
		l.reconnects.Add(1)
	}
	l.pid.Store(conn.PgConn().PID())
	// Anything cached before this connection may have missed a notification.
	l.k.cache.Flush()
	for _, sub := range subs {
		if sub.onReset != nil {
			sub.onReset()
		}
	}
	l.lastPing.Store(time.Now().UnixNano()) // the watchdog measures silence from here
	for {
		n, err := conn.WaitForNotification(cctx)
		if err != nil {
			return err
		}
		if n.Channel != store.IdentityChannel {
			if sub := subs[n.Channel]; sub != nil && !l.drop.Load() {
				sub.onMsg(n.Payload)
			}
			continue
		}
		l.handle(n.Payload)
	}
}

func (l *Listener) markUnhealthy() {
	l.healthy.Store(false)
	l.k.cache.SetBypass(true)
}

func (l *Listener) handle(payload string) {
	if l.drop.Load() {
		return
	}
	m, ok := decodeIdentityMsg(payload)
	if !ok {
		l.k.cache.Flush() // a payload this build can't read: trust nothing cached
		return
	}
	switch m.T {
	case "ping":
		if m.From == l.instance {
			l.lastPing.Store(time.Now().UnixNano())
			if !l.healthy.Load() && l.pid.Load() != 0 {
				l.k.cache.SetBypass(false) // flushes, then trusts the cache again
				l.healthy.Store(true)
				l.k.watch.recheckAll()
			}
		}
	case "agent":
		l.k.cache.InvalidateAgent(parseAgent(m.ID))
		l.k.watch.recheck(m.Org, m.ID)
	case "key":
		l.k.cache.InvalidateKey(m.P)
	case "org":
		l.k.cache.InvalidateOrg(parseOrg(m.Org))
		l.k.watch.recheck(m.Org, "")
	case "member":
		l.k.cache.Bump()
	default:
		l.k.cache.Flush()
	}
	if l.onEvent != nil {
		l.onEvent(m.T)
	}
}

// pinger sends this instance's ping every 500 ms through the pool.
func (l *Listener) pinger(ctx context.Context) {
	t := time.NewTicker(PingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p, err := identityMsg{T: "ping", From: l.instance, N: l.seq.Add(1)}.encode()
			if err != nil {
				continue
			}
			pctx, cancel := context.WithTimeout(ctx, PingEvery)
			_ = l.k.db.NotifyIdentity(pctx, nil, p) // a failed ping shows up as silence
			cancel()
		}
	}
}

// watchdog bypasses the cache after 1.5 s without our own ping, abandons a connection silent for
// 5 s, and, while bypassed, re-checks watched agents every 500 ms.
func (l *Listener) watchdog(ctx context.Context) {
	t := time.NewTicker(watchdogEvery)
	defer t.Stop()
	lastPoll := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			silent := now.Sub(time.Unix(0, l.lastPing.Load()))
			if silent > SilenceBypass && l.healthy.Load() {
				l.k.log.Warn("identity listener is silent; authenticating from Postgres", "silent_ms", silent.Milliseconds())
				l.markUnhealthy()
			}
			if silent > silenceAbandon && l.pid.Load() != 0 {
				l.mu.Lock()
				if l.cancel != nil {
					l.cancel()
				}
				l.mu.Unlock()
				l.lastPing.Store(now.UnixNano())
			}
			if l.k.cache.Bypassed() && now.Sub(lastPoll) >= guardPollEvery {
				lastPoll = now
				l.k.watch.recheckAll()
			}
		}
	}
}
