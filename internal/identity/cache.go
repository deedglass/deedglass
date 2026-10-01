package identity

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/ids"
)

// The principal cache (S08 §3.6). Authentication reads keys, agents and orgs from here when it can,
// and from Postgres otherwise. Correctness never depends on the TTL: every change to who may act
// sends a NOTIFY inside its transaction, the listener evicts the entry, and an epoch guard stops a
// slow load from caching a value that was already stale when it arrived. While the listener is
// unhealthy the cache is bypassed entirely.

// KeyEntry is an API key as authentication needs it.
type KeyEntry struct {
	KeyID      ids.APIKeyID
	OrgID      ids.OrgID
	AgentID    ids.AgentID // zero for an org admin key
	SecretHash [32]byte
	Mode       Mode
	Scopes     []string
	ExpiresAt  time.Time // zero: none
	RevokedAt  time.Time // zero: not revoked
}

// AgentEntry is an agent's identity and status, and its signed-intent settings (S09). The guard's
// own reads don't load the intent settings: IntentLoaded says whether IntentMode and IntentKeys are
// real, and the intent checker never reads an entry without them (an empty mode is not "off").
type AgentEntry struct {
	ID     ids.AgentID
	OrgID  ids.OrgID
	Name   string
	Status AgentStatus

	IntentLoaded bool
	IntentMode   IntentMode
	IntentKeys   []IntentKey
}

// OrgEntry is an org's name, pinned API version and stop overlay.
type OrgEntry struct {
	ID         ids.OrgID
	Name       string
	Disabled   bool
	APIVersion string
	StoppedAt  time.Time // zero: not stopped
}

// Cache sizes and the hygiene TTL.
const (
	keyCacheSize   = 10_000
	agentCacheSize = 10_000
	orgCacheSize   = 1_000
	cacheTTL       = 60 * time.Second
)

// Cache holds the three LRUs, the epoch and the bypass flag.
type Cache struct {
	keys   *lru[string, KeyEntry] // by prefix
	agents *lru[ids.AgentID, AgentEntry]
	orgs   *lru[ids.OrgID, OrgEntry]

	// mu orders writes: a guarded Put checks the epoch and bypass under it, and an invalidation
	// bumps the epoch and evicts under it, so no Put can slip between the two.
	mu     sync.Mutex
	epoch  atomic.Uint64
	bypass atomic.Bool

	// afterLoad, when set (tests), runs between a load's database read and its guarded Put.
	afterLoad func()
}

// NewCache returns an empty cache. It starts bypassed: nothing is cached until a listener has
// shown it is healthy (Listener clears bypass after its first ping round trip).
func NewCache(now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	c := &Cache{
		keys:   newLRU[string, KeyEntry](keyCacheSize, cacheTTL, now),
		agents: newLRU[ids.AgentID, AgentEntry](agentCacheSize, cacheTTL, now),
		orgs:   newLRU[ids.OrgID, OrgEntry](orgCacheSize, cacheTTL, now),
	}
	c.bypass.Store(true)
	return c
}

// Epoch is the current epoch; a load reads it before its database read.
func (c *Cache) Epoch() uint64 { return c.epoch.Load() }

// Bypassed reports whether every read must go to Postgres.
func (c *Cache) Bypassed() bool { return c.bypass.Load() }

// SetBypass turns bypass on or off. Turning it off flushes everything first, so nothing cached
// before (or during) an outage is trusted afterwards.
func (c *Cache) SetBypass(on bool) {
	if on {
		c.mu.Lock()
		c.bypass.Store(true)
		c.epoch.Add(1)
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	c.flushLocked()
	c.bypass.Store(false)
	c.mu.Unlock()
}

// Flush empties all three caches and bumps the epoch.
func (c *Cache) Flush() {
	c.mu.Lock()
	c.flushLocked()
	c.mu.Unlock()
}

func (c *Cache) flushLocked() {
	c.epoch.Add(1)
	c.keys.Clear()
	c.agents.Clear()
	c.orgs.Clear()
}

// Key returns the cached key for prefix. Always a miss while bypassed.
func (c *Cache) Key(prefix string) (KeyEntry, bool) {
	if c.bypass.Load() {
		return KeyEntry{}, false
	}
	return c.keys.Get(prefix)
}

// Agent returns the cached agent. Always a miss while bypassed.
func (c *Cache) Agent(id ids.AgentID) (AgentEntry, bool) {
	if c.bypass.Load() {
		return AgentEntry{}, false
	}
	return c.agents.Get(id)
}

// Org returns the cached org. Always a miss while bypassed.
func (c *Cache) Org(id ids.OrgID) (OrgEntry, bool) {
	if c.bypass.Load() {
		return OrgEntry{}, false
	}
	return c.orgs.Get(id)
}

// Loaded is one database read: the key (when the read was by prefix), its agent and its org.
type Loaded struct {
	Prefix string
	Key    *KeyEntry
	Agent  *AgentEntry
	Org    *OrgEntry
}

// PutIfCurrent caches a load that started at epoch e0, but only if nothing was invalidated since
// and the cache isn't bypassed. The caller always uses the fresh value, cached or not.
func (c *Cache) PutIfCurrent(e0 uint64, l Loaded) bool {
	if c.afterLoad != nil {
		c.afterLoad()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bypass.Load() || c.epoch.Load() != e0 {
		return false
	}
	if l.Key != nil && l.Prefix != "" {
		c.keys.Put(l.Prefix, *l.Key)
	}
	if l.Agent != nil {
		c.agents.Put(l.Agent.ID, *l.Agent)
	}
	if l.Org != nil {
		c.orgs.Put(l.Org.ID, *l.Org)
	}
	return true
}

// InvalidateKey bumps the epoch and evicts a key by prefix.
func (c *Cache) InvalidateKey(prefix string) {
	c.mu.Lock()
	c.epoch.Add(1)
	c.keys.Remove(prefix)
	c.mu.Unlock()
}

// InvalidateAgent bumps the epoch and evicts an agent.
func (c *Cache) InvalidateAgent(id ids.AgentID) {
	c.mu.Lock()
	c.epoch.Add(1)
	c.agents.Remove(id)
	c.mu.Unlock()
}

// InvalidateOrg bumps the epoch and evicts an org.
func (c *Cache) InvalidateOrg(id ids.OrgID) {
	c.mu.Lock()
	c.epoch.Add(1)
	c.orgs.Remove(id)
	c.mu.Unlock()
}

// Bump bumps the epoch without evicting anything (a notification this build doesn't cache, such as
// a membership change).
func (c *Cache) Bump() {
	c.mu.Lock()
	c.epoch.Add(1)
	c.mu.Unlock()
}

// Len reports the entries held (tests and metrics).
func (c *Cache) Len() (keys, agents, orgs int) { return c.keys.Len(), c.agents.Len(), c.orgs.Len() }

// --- a small generic LRU: container/list plus a map, one mutex, no dependency ---

type lru[K comparable, V any] struct {
	mu    sync.Mutex
	cap   int
	ttl   time.Duration
	now   func() time.Time
	ll    *list.List
	items map[K]*list.Element
}

type lruItem[K comparable, V any] struct {
	k  K
	v  V
	at time.Time
}

func newLRU[K comparable, V any](capacity int, ttl time.Duration, now func() time.Time) *lru[K, V] {
	return &lru[K, V]{cap: capacity, ttl: ttl, now: now, ll: list.New(), items: make(map[K]*list.Element, capacity)}
}

func (l *lru[K, V]) Get(k K) (V, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var zero V
	e, ok := l.items[k]
	if !ok {
		return zero, false
	}
	it := e.Value.(*lruItem[K, V])
	if l.ttl > 0 && l.now().Sub(it.at) >= l.ttl {
		l.ll.Remove(e)
		delete(l.items, k)
		return zero, false
	}
	l.ll.MoveToFront(e)
	return it.v, true
}

func (l *lru[K, V]) Put(k K, v V) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.items[k]; ok {
		it := e.Value.(*lruItem[K, V])
		it.v, it.at = v, l.now()
		l.ll.MoveToFront(e)
		return
	}
	l.items[k] = l.ll.PushFront(&lruItem[K, V]{k: k, v: v, at: l.now()})
	for l.ll.Len() > l.cap {
		last := l.ll.Back()
		l.ll.Remove(last)
		delete(l.items, last.Value.(*lruItem[K, V]).k)
	}
}

func (l *lru[K, V]) Remove(k K) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.items[k]; ok {
		l.ll.Remove(e)
		delete(l.items, k)
	}
}

func (l *lru[K, V]) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ll.Init()
	clear(l.items)
}

func (l *lru[K, V]) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ll.Len()
}
