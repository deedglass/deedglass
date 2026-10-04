package saas

import (
	"context"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/vault"
)

// TokenCache keeps short-lived tokens (GitHub App installation tokens, Google access tokens) in
// memory, keyed by the caller (connector id and credential version), and mints each one at most
// once at a time: concurrent callers for one key wait for the same mint (single flight). Tokens
// never leave the process and are never written anywhere.
type TokenCache struct {
	Now func() time.Time // nil: time.Now

	mu       sync.Mutex
	entries  map[string]*tokenEntry
	inflight map[string]*mintCall
	mints    map[string]int
}

type tokenEntry struct {
	tok *vault.Secret
	exp time.Time
}

type mintCall struct {
	done chan struct{}
	tok  *vault.Secret
	exp  time.Time
	err  error
}

func (t *TokenCache) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Get returns key's token while it has more than margin left, else mints a new one. The returned
// Secret belongs to the cache: callers read it and must not Destroy it.
func (t *TokenCache) Get(ctx context.Context, key string, margin time.Duration,
	mint func(ctx context.Context) (tok *vault.Secret, exp time.Time, err error)) (*vault.Secret, error) {
	t.mu.Lock()
	if t.entries == nil {
		t.entries, t.inflight, t.mints = map[string]*tokenEntry{}, map[string]*mintCall{}, map[string]int{}
	}
	if e, ok := t.entries[key]; ok && t.now().Add(margin).Before(e.exp) {
		t.mu.Unlock()
		return e.tok, nil
	}
	if c, ok := t.inflight[key]; ok {
		t.mu.Unlock()
		select {
		case <-c.done:
			return c.tok, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c := &mintCall{done: make(chan struct{})}
	t.inflight[key] = c
	t.mints[key]++
	t.mu.Unlock()

	c.tok, c.exp, c.err = mint(ctx)

	t.mu.Lock()
	delete(t.inflight, key)
	if c.err == nil && c.tok != nil {
		if old, ok := t.entries[key]; ok && old.tok != c.tok {
			old.tok.Destroy()
		}
		t.entries[key] = &tokenEntry{tok: c.tok, exp: c.exp}
	}
	t.mu.Unlock()
	close(c.done)
	return c.tok, c.err
}

// Forget drops key's token (a 401 with a cached token: the next call mints again).
func (t *TokenCache) Forget(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[key]; ok {
		delete(t.entries, key)
		e.tok.Destroy()
	}
}

// Mints is how many times key was minted (the "≤ 1 per connector per hour" budget).
func (t *TokenCache) Mints(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.mints[key]
}
