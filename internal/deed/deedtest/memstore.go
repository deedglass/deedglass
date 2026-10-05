package deedtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/deed"
)

// MemStore is an in-memory deed.ChainStore with Postgres-like transactions: LockChain blocks
// until the transaction owns the org's chain, and inserts become visible at Commit. The Sealer's
// concurrency tests run against it in make ci, and against Postgres in make integration.
type MemStore struct {
	mu     sync.Mutex
	chains map[string]*memChain
	keys   map[string]deed.KeyStatus
	clock  func() time.Time
}

type memChain struct {
	lock  chan struct{} // capacity 1: a held token is the advisory lock
	deeds []deed.Stored
}

// NewMemStore returns an empty store whose database clock is clock (time.Now when nil).
func NewMemStore(clock func() time.Time) *MemStore {
	if clock == nil {
		clock = time.Now
	}
	return &MemStore{chains: map[string]*memChain{}, keys: map[string]deed.KeyStatus{}, clock: clock}
}

// SetKey registers or replaces a seal key's row.
func (m *MemStore) SetKey(id string, k deed.KeyStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k.Found = true
	m.keys[id] = k
}

// Deeds returns the committed deeds of org, in seq order.
func (m *MemStore) Deeds(org string) []deed.Stored {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.chains[org]
	if c == nil {
		return nil
	}
	return append([]deed.Stored(nil), c.deeds...)
}

func (m *MemStore) chain(org string) *memChain {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.chains[org]
	if c == nil {
		c = &memChain{lock: make(chan struct{}, 1)}
		m.chains[org] = c
	}
	return c
}

// Begin starts a transaction.
func (m *MemStore) Begin() *MemTx { return &MemTx{m: m, held: map[string]*memChain{}} }

// MemTx is one transaction; it implements deed.ChainStore.
type MemTx struct {
	m       *MemStore
	held    map[string]*memChain
	pending []deed.Stored
	done    bool
}

// LockChain implements deed.ChainStore.
func (t *MemTx) LockChain(ctx context.Context, org string) error {
	if _, ok := t.held[org]; ok {
		return nil
	}
	c := t.m.chain(org)
	select {
	case c.lock <- struct{}{}:
		t.held[org] = c
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReadState implements deed.ChainStore.
func (t *MemTx) ReadState(_ context.Context, org, sealKeyID string) (deed.ChainState, error) {
	t.m.mu.Lock()
	defer t.m.mu.Unlock()
	st := deed.ChainState{Now: t.m.clock(), Key: t.m.keys[sealKeyID]}
	all := t.visible(org)
	if n := len(all); n > 0 {
		last := all[n-1]
		st.Head = deed.Head{Seq: last.Envelope.Body.Seq, BodyHash: last.Envelope.BodyHash, IssuedAt: last.IssuedAt}
	}
	return st, nil
}

// visible is the committed chain plus this transaction's inserts (mu held).
func (t *MemTx) visible(org string) []deed.Stored {
	var out []deed.Stored
	if c := t.m.chains[org]; c != nil {
		out = append(out, c.deeds...)
	}
	for _, d := range t.pending {
		if d.Envelope.Body.Org == org {
			out = append(out, d)
		}
	}
	return out
}

// Insert implements deed.ChainStore with the database's constraints: seq must follow the head and
// prev_hash must link to it.
func (t *MemTx) Insert(_ context.Context, d deed.Stored) error {
	t.m.mu.Lock()
	defer t.m.mu.Unlock()
	b := d.Envelope.Body
	all := t.visible(b.Org)
	want, prev := int64(1), deed.Genesis(b.Org)
	if n := len(all); n > 0 {
		want, prev = all[n-1].Envelope.Body.Seq+1, all[n-1].Envelope.BodyHash
	}
	if b.Seq != want || b.PrevHash != prev { //deedglass:allow-compare a deed's public prev_hash
		return fmt.Errorf("%w: seq %d, want %d", deed.ErrChainConflict, b.Seq, want)
	}
	t.pending = append(t.pending, d)
	return nil
}

// InsertRaw appends a deed behind the Sealer's back (the forced-conflict test).
func (m *MemStore) InsertRaw(d deed.Stored) {
	c := m.chain(d.Envelope.Body.Org)
	m.mu.Lock()
	defer m.mu.Unlock()
	c.deeds = append(c.deeds, d)
}

// Commit makes the inserts visible and releases the locks.
func (t *MemTx) Commit() {
	if t.done {
		return
	}
	t.m.mu.Lock()
	for _, d := range t.pending {
		c := t.m.chains[d.Envelope.Body.Org]
		if c == nil {
			c = &memChain{lock: make(chan struct{}, 1)}
			t.m.chains[d.Envelope.Body.Org] = c
		}
		c.deeds = append(c.deeds, d)
	}
	t.m.mu.Unlock()
	t.release()
}

// Rollback drops the inserts and releases the locks.
func (t *MemTx) Rollback() {
	if t.done {
		return
	}
	t.pending = nil
	t.release()
}

func (t *MemTx) release() {
	t.done = true
	for _, c := range t.held {
		<-c.lock
	}
	t.held = nil
}
