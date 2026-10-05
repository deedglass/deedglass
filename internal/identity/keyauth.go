package identity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// KeyAuth authenticates bearer API keys (S08 §3.5) and is the Guard the Gate, MCP and approvals
// use (§3.7). It owns the principal cache, the deedglass_identity listener, the failed-auth
// limiter and the batched last_used_at writes. Start its background work with Run.
type KeyAuth struct {
	db     *store.DB
	pre    store.PreAuth
	cache  *Cache
	pepper []byte
	now    func() time.Time
	log    *slog.Logger

	ipFail     *failLimiter
	prefixFail *httpx.Limiter
	used       *lastUsed

	watch    *watchers
	listener *Listener
}

// Failed-auth limits (S08 §3.9).
const (
	FailuresPerIPMinute     = 10
	FailuresPerPrefixMinute = 10
	maxPenalty              = 15 * time.Minute
	cleanReset              = 10 * time.Minute
	LastUsedFlushEvery      = 30 * time.Second
)

// KeyAuthOptions configure KeyAuth.
type KeyAuthOptions struct {
	DB     *store.DB
	Pepper []byte // DEEDGLASS_API_KEY_PEPPER, decoded
	Logger *slog.Logger
	Now    func() time.Time
}

// NewKeyAuth returns a key authenticator. Until Run has a healthy listener, its cache is bypassed
// and every authentication reads Postgres.
func NewKeyAuth(o KeyAuthOptions) (*KeyAuth, error) {
	if len(o.Pepper) < 32 {
		return nil, errors.New("identity: the pepper must be at least 32 bytes")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	k := &KeyAuth{db: o.DB, cache: NewCache(o.Now), pepper: o.Pepper, now: o.Now, log: o.Logger,
		ipFail:     newFailLimiter(o.Now),
		prefixFail: httpx.NewLimiter(FailuresPerPrefixMinute, time.Minute, o.Now),
		used:       &lastUsed{m: map[ids.APIKeyID]usedAt{}},
	}
	if o.DB != nil {
		k.pre = o.DB.PreAuth()
	}
	k.watch = newWatchers(k)
	k.listener = newListener(k)
	return k, nil
}

// Cache returns the principal cache (tests and metrics).
func (k *KeyAuth) Cache() *Cache { return k.cache }

// Listener returns the deedglass_identity listener (tests).
func (k *KeyAuth) Listener() *Listener { return k.listener }

// Run starts the listener, its self-ping and watchdog, and the last_used_at flusher. It returns when
// ctx is done, after a final flush.
func (k *KeyAuth) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { k.listener.run(ctx) })
	wg.Go(func() {
		t := time.NewTicker(LastUsedFlushEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_, _ = k.FlushLastUsed(fctx)
				cancel()
				return
			case <-t.C:
				if _, err := k.FlushLastUsed(ctx); err != nil && ctx.Err() == nil {
					k.log.Warn("writing API key last_used_at failed", "error", err.Error())
				}
			}
		}
	})
	wg.Wait()
}

// dummyHash is compared against when a prefix is unknown, so an unknown prefix and a wrong secret
// take the same path and give the same answer.
var dummyHash = sha256.Sum256([]byte("deedglass/api-key/dummy\n"))

func invalidKey() error { return httpx.E(httpx.CodeInvalidAPIKey) }

// AuthenticateKey is §3.5 steps 2–5. Steps 6–9 (stop checks and scopes) run in httpx with the
// statuses this fills into the principal.
func (k *KeyAuth) AuthenticateKey(ctx context.Context, bearer string) (Principal, error) {
	ip := httpx.ClientIP(ctx)
	if ok, wait := k.ipFail.check(ip); !ok {
		return Principal{}, httpx.ErrKeyRateLimited(ceilSeconds(wait))
	}
	pk, err := ParseAPIKey(bearer)
	if err != nil {
		h := HashSecret(k.pepper, "")
		subtle.ConstantTimeCompare(h[:], dummyHash[:])
		k.ipFail.fail(ip)
		return Principal{}, invalidKey()
	}
	if ok, wait := k.prefixFail.Check(pk.Prefix); !ok {
		return Principal{}, httpx.ErrKeyRateLimited(ceilSeconds(wait))
	}
	failed := func(err error) (Principal, error) {
		k.ipFail.fail(ip)
		k.prefixFail.Allow(pk.Prefix)
		return Principal{}, err
	}
	ke, ag, org, err := k.lookup(ctx, pk.Prefix)
	h := HashSecret(k.pepper, pk.Secret)
	if errors.Is(err, store.ErrNotFound) {
		subtle.ConstantTimeCompare(h[:], dummyHash[:])
		return failed(invalidKey())
	}
	if err != nil {
		return Principal{}, httpx.E(httpx.CodeServiceUnavailable,
			httpx.Msg("Deedglass can't check API keys right now because its database is unreachable. Retry in a few seconds."), httpx.Cause(err))
	}
	match := subtle.ConstantTimeCompare(h[:], ke.SecretHash[:]) == 1
	if !match || ke.Mode != pk.Mode || org.Disabled {
		return failed(invalidKey())
	}
	// Revoked and expired keys passed the hash check: whoever sent them holds a real secret, so
	// they don't count toward the limiter (which is for guessing), and the caller keeps getting
	// the useful answer instead of 429.
	now := k.now()
	if !ke.RevokedAt.IsZero() {
		return Principal{}, httpx.E(httpx.CodeAPIKeyRevoked)
	}
	if !ke.ExpiresAt.IsZero() && !now.Before(ke.ExpiresAt) {
		return Principal{}, httpx.ErrAPIKeyExpired(ke.ExpiresAt)
	}
	p := Principal{OrgID: ke.OrgID, KeyID: ke.KeyID, Scopes: ke.Scopes, Livemode: ke.Mode == ModeLive,
		APIVersion: org.APIVersion, OrgName: org.Name, OrgStopped: !org.StoppedAt.IsZero()}
	if ke.AgentID.IsZero() {
		p.Kind, p.Role = httpx.KindAdminKey, RoleAdmin
	} else {
		p.Kind, p.AgentID, p.AgentName, p.AgentStatus = httpx.KindAgentKey, ag.ID, ag.Name, string(ag.Status)
	}
	k.used.mark(ke.KeyID, ke.OrgID, now)
	return p, nil
}

func ceilSeconds(d time.Duration) int { return max(1, int(math.Ceil(d.Seconds()))) }

// lookup reads the key, its agent and its org from the cache, or from Postgres in one round trip
// (store.PreAuth.APIKey). The fresh value is always returned; it is cached only if no invalidation
// arrived while it was being read (the epoch guard).
func (k *KeyAuth) lookup(ctx context.Context, prefix string) (KeyEntry, AgentEntry, OrgEntry, error) {
	if ke, ok := k.cache.Key(prefix); ok {
		org, okOrg := k.cache.Org(ke.OrgID)
		ag, okAgent := AgentEntry{}, ke.AgentID.IsZero()
		if !okAgent {
			ag, okAgent = k.cache.Agent(ke.AgentID)
		}
		if okOrg && okAgent {
			return ke, ag, org, nil
		}
	}
	e0 := k.cache.Epoch()
	if !k.pre.Ready() {
		// S34 (SR-008): no database (route tests); fail closed instead of dereferencing nil.
		return KeyEntry{}, AgentEntry{}, OrgEntry{}, httpx.E(httpx.CodeServiceUnavailable)
	}
	row, err := k.pre.APIKey(ctx, prefix)
	if err != nil {
		return KeyEntry{}, AgentEntry{}, OrgEntry{}, err
	}
	ke, ag, org, err := entriesFrom(row)
	if err != nil {
		return KeyEntry{}, AgentEntry{}, OrgEntry{}, err
	}
	l := Loaded{Prefix: prefix, Key: &ke, Org: &org}
	if !ke.AgentID.IsZero() {
		l.Agent = &ag
	}
	k.cache.PutIfCurrent(e0, l)
	return ke, ag, org, nil
}

func entriesFrom(r store.PreAuthKey) (KeyEntry, AgentEntry, OrgEntry, error) {
	if len(r.SecretHash) != 32 {
		return KeyEntry{}, AgentEntry{}, OrgEntry{}, errors.New("identity: stored key hash isn't 32 bytes")
	}
	ke := KeyEntry{KeyID: r.ID, OrgID: r.OrgID, Mode: Mode(r.Mode), Scopes: r.Scopes}
	copy(ke.SecretHash[:], r.SecretHash)
	if r.ExpiresAt != nil {
		ke.ExpiresAt = *r.ExpiresAt
	}
	if r.RevokedAt != nil {
		ke.RevokedAt = *r.RevokedAt
	}
	org := OrgEntry{ID: r.OrgID, Name: r.OrgName, Disabled: r.OrgStatus != "active", APIVersion: r.OrgVersion}
	if r.OrgStoppedAt != nil {
		org.StoppedAt = *r.OrgStoppedAt
	}
	var ag AgentEntry
	if r.AgentID != nil {
		if r.AgentName == nil || r.AgentStatus == nil {
			return KeyEntry{}, AgentEntry{}, OrgEntry{}, errors.New("identity: an agent key without its agent")
		}
		ke.AgentID = *r.AgentID
		ag = AgentEntry{ID: *r.AgentID, OrgID: r.OrgID, Name: *r.AgentName, Status: AgentStatus(*r.AgentStatus)}
		if r.IntentMode != nil {
			keys, err := parseIntentKeys(r.IntentKeys)
			if err != nil {
				return KeyEntry{}, AgentEntry{}, OrgEntry{}, err
			}
			ag.IntentLoaded, ag.IntentMode, ag.IntentKeys = true, IntentMode(*r.IntentMode), keys
		}
	}
	return ke, ag, org, nil
}

// --- the failed-auth limiter (per IP, with a doubling penalty) ---

// failLimiter counts step-3 failures (invalid_api_key) only. It allows 10 a minute per address. The failure that reaches the limit starts a
// penalty of 1 s; each further failure while the address is over the limit doubles it (2 s, 4 s,
// … up to 15 minutes). Ten minutes without a failure resets the penalty. Successes never count.
type failLimiter struct {
	mu        sync.Mutex
	now       func() time.Time
	m         map[string]*failState
	lastSweep time.Time
}

type failState struct {
	windowStart time.Time
	count       int
	penalty     time.Duration
	until       time.Time
	lastFail    time.Time
}

func newFailLimiter(now func() time.Time) *failLimiter {
	return &failLimiter{now: now, m: map[string]*failState{}}
}

func (f *failLimiter) check(ip string) (bool, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.m[ip]
	now := f.now()
	if st == nil || !now.Before(st.until) {
		return true, 0
	}
	return false, st.until.Sub(now)
}

func (f *failLimiter) fail(ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	if now.Sub(f.lastSweep) > time.Minute {
		for k, st := range f.m {
			if now.Sub(st.lastFail) > cleanReset && !now.Before(st.until) {
				delete(f.m, k)
			}
		}
		f.lastSweep = now
	}
	st := f.m[ip]
	if st == nil || now.Sub(st.lastFail) >= cleanReset {
		st = &failState{windowStart: now}
		f.m[ip] = st
	}
	if now.Sub(st.windowStart) >= time.Minute {
		st.windowStart, st.count = now, 0
	}
	st.count++
	st.lastFail = now
	if st.count >= FailuresPerIPMinute {
		switch {
		case st.penalty == 0:
			st.penalty = time.Second
		default:
			st.penalty = min(st.penalty*2, maxPenalty)
		}
		st.until = now.Add(st.penalty)
	}
}

// --- last_used_at, batched off the request path ---

type usedAt struct {
	org ids.OrgID
	at  time.Time
}

type lastUsed struct {
	mu sync.Mutex
	m  map[ids.APIKeyID]usedAt
}

func (u *lastUsed) mark(id ids.APIKeyID, org ids.OrgID, at time.Time) {
	u.mu.Lock()
	if cur, ok := u.m[id]; !ok || at.After(cur.at) {
		u.m[id] = usedAt{org: org, at: at}
	}
	u.mu.Unlock()
}

func (u *lastUsed) take() map[ids.APIKeyID]usedAt {
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.m
	u.m = map[ids.APIKeyID]usedAt{}
	return m
}

// FlushLastUsed writes the remembered last-use times: one UPDATE per org, in that org's
// transaction. It returns the number of UPDATE statements run. Times that fail to write are kept
// for the next flush.
func (k *KeyAuth) FlushLastUsed(ctx context.Context) (int, error) {
	pending := k.used.take()
	byOrg := map[ids.OrgID][]ids.APIKeyID{}
	for id, u := range pending {
		byOrg[u.org] = append(byOrg[u.org], id)
	}
	n := 0
	var firstErr error
	for org, keys := range byOrg {
		idList := make([]string, len(keys))
		times := make([]time.Time, len(keys))
		for i, id := range keys {
			idList[i], times[i] = id.String(), pending[id].at
		}
		err := k.db.OrgTx(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE deedglass.api_keys AS k SET last_used_at = u.t
			    FROM ROWS FROM (pg_catalog.unnest($2::text[]), pg_catalog.unnest($3::timestamptz[])) AS u(id, t)
			    WHERE k.org_id = $1 AND k.id = u.id AND (k.last_used_at IS NULL OR k.last_used_at < u.t)`, org, idList, times)
			return err
		})
		n++
		if err != nil {
			for _, id := range keys {
				k.used.mark(id, org, pending[id].at)
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return n, firstErr
}
