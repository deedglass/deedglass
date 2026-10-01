package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

const sampleKey = "dg_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8"

func TestParseAPIKey(t *testing.T) {
	p, err := ParseAPIKey(sampleKey)
	if err != nil || p.Mode != ModeLive || p.Prefix != "jJbEEMWk" || p.Secret != "o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8" {
		t.Fatalf("%+v %v", p, err)
	}
	if p.Display() != "dg_live_jJbEEMWk_••••" {
		t.Fatal(p.Display())
	}
	if p, err := ParseAPIKey(strings.Replace(sampleKey, "live", "test", 1)); err != nil || p.Mode != ModeTest {
		t.Fatal(err)
	}
	bad := []string{
		"", "sk_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8", "dg_prod_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8",
		"dg_live_jJbEEMW_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8x", "dg_live_jJbEEMWkk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch",
		"dg_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch", "dg_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch88",
		"dg_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIc-8", " " + sampleKey, sampleKey + " ", sampleKey + "\n",
		"DG_LIVE_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8", "dg_live_jJbEEMWk-o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8",
		"dg_live_jJbEEMWé_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch", "dg_live_jJbEEMWk_o0Erxxf1kgtP2QjiJ79vzW31AjZqIch8\x00",
	}
	for _, b := range bad {
		if _, err := ParseAPIKey(b); !errors.Is(err, ErrMalformedKey) {
			t.Errorf("%q: %v", b, err)
		}
	}
}

func FuzzParseAPIKey(f *testing.F) {
	f.Add(sampleKey)
	f.Add("dg_test_" + strings.Repeat("a", 8) + "_" + strings.Repeat("Z", 32))
	f.Add("dg_live__")
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParseAPIKey(s)
		if err != nil {
			if len(s) >= 8 && strings.Contains(err.Error(), s) {
				t.Fatal("the parser echoed its input")
			}
			return
		}
		if DisplayPrefix(p.Mode, p.Prefix)+"_"+p.Secret != s || !keyRE.MatchString(s) {
			t.Fatalf("round trip: %q", s)
		}
	})
}

func TestNewAPIKeyUniqueAndUniform(t *testing.T) {
	seen := map[string]bool{}
	var counts [62]int
	idx := map[byte]int{}
	for i := 0; i < len(base62); i++ {
		idx[base62[i]] = i
	}
	const n = 10_000
	for range n {
		full, p, err := NewAPIKey(ModeLive)
		if err != nil {
			t.Fatal(err)
		}
		if len(full) != KeyLen || seen[full] || seen["p"+p.Prefix] {
			t.Fatalf("duplicate or wrong length: %s", full)
		}
		seen[full], seen["p"+p.Prefix] = true, true
		if _, err := ParseAPIKey(full); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(p.Secret); i++ {
			counts[idx[p.Secret[i]]]++
		}
	}
	// Chi-square over 62 symbols (61 degrees of freedom): the 0.999 quantile is about 100.9.
	exp := float64(n*KeySecretLen) / 62
	chi := 0.0
	for _, c := range counts {
		chi += math.Pow(float64(c)-exp, 2) / exp
	}
	if chi > 100.9 {
		t.Fatalf("chi-square %.1f: the characters aren't uniform", chi)
	}
	if _, _, err := NewAPIKey("prod"); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

func TestHashSecret(t *testing.T) {
	a := HashSecret([]byte("pepper-a"), "s")
	b := HashSecret([]byte("pepper-b"), "s")
	if subtle.ConstantTimeCompare(a[:], b[:]) == 1 {
		t.Fatal("the pepper must change the hash")
	}
}

// identityFiles returns the non-test Go files of this package.
func identityFiles(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		n := e.Name()
		if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			b, err := os.ReadFile(filepath.Clean(n))
			if err != nil {
				t.Fatal(err)
			}
			out[n] = string(b)
		}
	}
	return out
}

// Keys and tokens come from crypto/rand only; hashes are compared in constant time only.
func TestNoMathRandNoPlainHashCompare(t *testing.T) {
	plain := regexp.MustCompile(`bytes\.Equal\([^)]*(?i:hash)|(?i:hash)[A-Za-z\[\]:.]*\s*[!=]=\s*|[!=]=\s*[A-Za-z.]*(?i:hash)\b`)
	for name, src := range identityFiles(t) {
		if strings.Contains(src, `"math/rand`) {
			t.Errorf("%s imports math/rand", name)
		}
		for _, line := range strings.Split(src, "\n") {
			if l := strings.TrimSpace(line); strings.HasPrefix(l, "//") {
				continue
			}
			if plain.MatchString(line) && !strings.Contains(line, "len(") {
				t.Errorf("%s compares a hash without crypto/subtle: %s", name, strings.TrimSpace(line))
			}
		}
	}
}

func TestCacheEpochAndBypass(t *testing.T) {
	c := NewCache(nil)
	ke := KeyEntry{KeyID: ids.New[ids.APIKeyKind](), OrgID: ids.New[ids.OrgKind]()}
	if c.PutIfCurrent(c.Epoch(), Loaded{Prefix: "p", Key: &ke}) {
		t.Fatal("a new cache is bypassed until a listener is healthy")
	}
	c.SetBypass(false)
	e0 := c.Epoch()
	if !c.PutIfCurrent(e0, Loaded{Prefix: "p", Key: &ke}) {
		t.Fatal("put")
	}
	if _, ok := c.Key("p"); !ok {
		t.Fatal("miss")
	}
	// An invalidation between the read and the put wins.
	e1 := c.Epoch()
	c.InvalidateKey("p")
	if c.PutIfCurrent(e1, Loaded{Prefix: "p", Key: &ke}) {
		t.Fatal("a stale load was cached")
	}
	if _, ok := c.Key("p"); ok {
		t.Fatal("still cached")
	}
	ag := AgentEntry{ID: ids.New[ids.AgentKind]()}
	org := OrgEntry{ID: ke.OrgID}
	c.PutIfCurrent(c.Epoch(), Loaded{Agent: &ag, Org: &org})
	if _, ok := c.Agent(ag.ID); !ok {
		t.Fatal("agent")
	}
	c.InvalidateAgent(ag.ID)
	c.InvalidateOrg(org.ID)
	if _, ok := c.Agent(ag.ID); ok {
		t.Fatal("agent not evicted")
	}
	if _, ok := c.Org(org.ID); ok {
		t.Fatal("org not evicted")
	}
	// Bypass hides everything and refuses puts; leaving it flushes.
	c.PutIfCurrent(c.Epoch(), Loaded{Prefix: "p", Key: &ke})
	c.SetBypass(true)
	if _, ok := c.Key("p"); ok || c.PutIfCurrent(c.Epoch(), Loaded{Prefix: "q", Key: &ke}) {
		t.Fatal("bypass must hide and refuse")
	}
	c.SetBypass(false)
	if k, a, o := c.Len(); k+a+o != 0 {
		t.Fatalf("not flushed: %d %d %d", k, a, o)
	}
	c.Bump()
	c.Flush()
}

func TestCacheConcurrentInvalidation(t *testing.T) {
	c := NewCache(nil)
	c.SetBypass(false)
	ke := KeyEntry{KeyID: ids.New[ids.APIKeyKind]()}
	var wg sync.WaitGroup
	stale := 0
	var mu sync.Mutex
	for range 200 {
		wg.Go(func() {
			e0 := c.Epoch()
			c.InvalidateKey("p") // an invalidation after the read began
			if c.PutIfCurrent(e0, Loaded{Prefix: "p", Key: &ke}) {
				mu.Lock()
				stale++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if stale != 0 {
		t.Fatalf("%d stale puts survived an invalidation", stale)
	}
}

func TestLRU(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLRU[string, int](2, time.Minute, func() time.Time { return now })
	l.Put("a", 1)
	l.Put("b", 2)
	l.Get("a")
	l.Put("c", 3) // evicts b, the least recently used
	if _, ok := l.Get("b"); ok {
		t.Fatal("b should be evicted")
	}
	if v, ok := l.Get("a"); !ok || v != 1 {
		t.Fatal("a")
	}
	l.Put("a", 9)
	if v, _ := l.Get("a"); v != 9 {
		t.Fatal("update")
	}
	now = now.Add(time.Minute)
	if _, ok := l.Get("a"); ok {
		t.Fatal("the TTL must expire entries")
	}
	l.Remove("c")
	l.Clear()
	if l.Len() != 0 {
		t.Fatal(l.Len())
	}
}

func TestFailLimiterDoubles(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	f := newFailLimiter(func() time.Time { return now })
	for range 9 {
		f.fail("ip")
	}
	if ok, _ := f.check("ip"); !ok {
		t.Fatal("nine failures don't block")
	}
	f.fail("ip")
	ok, wait := f.check("ip")
	if ok || wait != time.Second {
		t.Fatalf("10th failure: %v %v", ok, wait)
	}
	now = now.Add(time.Second)
	f.fail("ip")
	if _, wait := f.check("ip"); wait != 2*time.Second {
		t.Fatalf("doubled: %v", wait)
	}
	for range 20 {
		now = now.Add(time.Minute)
		f.fail("ip")
	}
	if _, wait := f.check("ip"); wait > maxPenalty {
		t.Fatalf("capped: %v", wait)
	}
	if ok, _ := f.check("other"); !ok {
		t.Fatal("another address is unaffected")
	}
	now = now.Add(maxPenalty + cleanReset + time.Second)
	f.fail("ip") // after ten clean minutes the penalty starts over
	if ok, _ := f.check("ip"); !ok {
		t.Fatal("reset")
	}
}

func TestIdentityMsg(t *testing.T) {
	for _, m := range []identityMsg{
		{T: "agent", Org: ids.New[ids.OrgKind]().String(), ID: ids.New[ids.AgentKind]().String(), S: "stopped"},
		{T: "key", Org: ids.New[ids.OrgKind]().String(), ID: ids.New[ids.APIKeyKind]().String(), P: "jJbEEMWk", S: "revoked"},
		{T: "ping", From: "0123456789ab", N: math.MaxUint32},
	} {
		p, err := m.encode()
		if err != nil || len(p) > 200 {
			t.Fatalf("%q %v", p, err)
		}
		got, ok := decodeIdentityMsg(p)
		if !ok || got.T != m.T || got.ID != m.ID || got.P != m.P {
			t.Fatalf("%+v", got)
		}
	}
	for _, bad := range []string{"", "{}", `{"v":2,"t":"agent"}`, "not json", strings.Repeat("x", 300)} {
		if _, ok := decodeIdentityMsg(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
}

// The listener's handler, without a database: evictions, an unreadable payload flushes, and the
// instance's own ping makes it healthy.
func TestListenerHandle(t *testing.T) {
	k, err := NewKeyAuth(KeyAuthOptions{Pepper: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	l := k.listener
	l.pid.Store(42)
	ag := AgentEntry{ID: ids.New[ids.AgentKind]()}
	org := OrgEntry{ID: ids.New[ids.OrgKind]()}
	ke := KeyEntry{KeyID: ids.New[ids.APIKeyKind]()}
	ping, _ := identityMsg{T: "ping", From: l.instance, N: 1}.encode()
	l.handle(ping)
	if !l.Healthy() || k.cache.Bypassed() {
		t.Fatal("our own ping should make the listener healthy")
	}
	fill := func() {
		k.cache.PutIfCurrent(k.cache.Epoch(), Loaded{Prefix: "jJbEEMWk", Key: &ke, Agent: &ag, Org: &org})
	}
	fill()
	m, _ := identityMsg{T: "agent", Org: org.ID.String(), ID: ag.ID.String(), S: "stopped"}.encode()
	l.handle(m)
	if _, ok := k.cache.Agent(ag.ID); ok {
		t.Fatal("agent not evicted")
	}
	m, _ = identityMsg{T: "key", Org: org.ID.String(), ID: ke.KeyID.String(), P: "jJbEEMWk", S: "revoked"}.encode()
	l.handle(m)
	if _, ok := k.cache.Key("jJbEEMWk"); ok {
		t.Fatal("key not evicted")
	}
	fill()
	m, _ = identityMsg{T: "org", Org: org.ID.String(), ID: org.ID.String(), S: "stopped"}.encode()
	l.handle(m)
	if _, ok := k.cache.Org(org.ID); ok {
		t.Fatal("org not evicted")
	}
	fill()
	e := k.cache.Epoch()
	m, _ = identityMsg{T: "member", Org: org.ID.String()}.encode()
	l.handle(m)
	if k.cache.Epoch() == e {
		t.Fatal("a member change bumps the epoch")
	}
	l.handle("garbage")
	if n, _, _ := k.cache.Len(); n != 0 {
		t.Fatal("an unreadable payload must flush")
	}
	fill()
	l.SetStall(true)
	l.handle(m)
	l.SetStall(false)
	other, _ := identityMsg{T: "ping", From: "someone-else", N: 1}.encode()
	l.handle(other)
	l.markUnhealthy()
	if l.Healthy() || !k.cache.Bypassed() {
		t.Fatal("unhealthy")
	}
}

func TestStateError(t *testing.T) {
	stopped := time.Now()
	cases := []struct {
		st   agentState
		code httpx.Code
		root error
	}{
		{agentState{name: "a", status: AgentActive}, "", nil},
		{agentState{name: "a", status: AgentStopped}, httpx.CodeAgentStopped, ErrAgentStopped},
		{agentState{name: "a", status: AgentSuspended}, httpx.CodeAgentSuspended, ErrAgentSuspended},
		{agentState{name: "a", status: "weird"}, httpx.CodeAgentStopped, ErrAgentStopped},
		{agentState{name: "a", status: AgentStopped, orgName: "Acme", orgStopAt: &stopped}, httpx.CodeOrgStopped, ErrOrgStopped},
	}
	for _, c := range cases {
		err := stateError(c.st)
		if c.code == "" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if he := httpx.AsError(err); he == nil || he.Code != c.code || !errors.Is(err, c.root) {
			t.Errorf("%+v: %v", c.st, err)
		}
	}
}

func TestCheckLabelsAndBudgets(t *testing.T) {
	ok := map[string]string{"env": "prod", "team/owner": "support", "a.b_c-d": strings.Repeat("v", 128)}
	if err := CheckLabels(ok); err != nil {
		t.Fatal(err)
	}
	many := map[string]string{}
	for i := range 33 {
		many[string(rune('a'+i%26))+strings.Repeat("x", i)] = "v"
	}
	for _, bad := range []map[string]string{many, {"Env": "x"}, {"-x": "y"}, {"env": strings.Repeat("v", 129)}, {"env": "a\nb"}} {
		if CheckLabels(bad) == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	n := int64(0)
	big := int64(1 << 53)
	for _, b := range []Budgets{{CallsPerMinute: &n}, {CallsPerDay: &n}, {SpendPerDay: &httpx.Money{Amount: -1, Currency: "usd"}},
		{SpendPerDay: &httpx.Money{Amount: big, Currency: "usd"}}, {SpendPerDay: &httpx.Money{Amount: 1, Currency: "US"}}} {
		if CheckBudgets(b) == nil {
			t.Errorf("accepted %+v", b)
		}
	}
	for _, name := range []string{"support-bot", "a1", strings.Repeat("a", 63)} {
		if checkAgentName(name) != nil {
			t.Errorf("refused %s", name)
		}
	}
	for _, name := range []string{"a", "-a", "Support", "a_b", strings.Repeat("a", 64), "console", "deedglass", "system"} {
		if checkAgentName(name) == nil {
			t.Errorf("accepted %s", name)
		}
	}
	if _, err := checkReason("reason", "ok", MinStartReason); err == nil {
		t.Error("a 2-character start reason")
	}
	if _, err := checkReason("reason", "", 1); err == nil {
		t.Error("an empty start-all reason")
	}
	if _, err := checkReason("reason", strings.Repeat("x", 281), 0); err == nil {
		t.Error("281 characters")
	}
}

func TestActorOf(t *testing.T) {
	u := &Principal{Kind: KindUser, UserID: ids.New[ids.UserKind]()}
	if a, err := ActorOf(u); err != nil || a.Ref.Type != "user" || a.By != u.UserID.String() {
		t.Fatal(a, err)
	}
	k := &Principal{Kind: httpx.KindAdminKey, KeyID: ids.New[ids.APIKeyKind]()}
	if a, err := ActorOf(k); err != nil || a.Ref.Type != "api_key" {
		t.Fatal(a, err)
	}
	if _, err := ActorOf(&Principal{Kind: httpx.KindAgentKey, KeyID: ids.New[ids.APIKeyKind]()}); httpx.AsError(err) == nil ||
		httpx.AsError(err).Code != httpx.CodeInsufficientScope {
		t.Fatal("an agent key must never be an actor")
	}
	if _, err := ActorOf(nil); err == nil {
		t.Fatal("nil")
	}
}

// BenchmarkAuthWarm is authentication with a warm cache, in process (S08 §3.11: < 20 µs/op).
func BenchmarkAuthWarm(b *testing.B) {
	k, err := NewKeyAuth(KeyAuthOptions{Pepper: make([]byte, 32)})
	if err != nil {
		b.Fatal(err)
	}
	full, p, _ := NewAPIKey(ModeLive)
	org := OrgEntry{ID: ids.New[ids.OrgKind](), Name: "Acme", APIVersion: "2026-10-01"}
	ag := AgentEntry{ID: ids.New[ids.AgentKind](), OrgID: org.ID, Name: "support-bot", Status: AgentActive}
	ke := KeyEntry{KeyID: ids.New[ids.APIKeyKind](), OrgID: org.ID, AgentID: ag.ID, Mode: ModeLive, Scopes: httpx.DefaultAgentScopes,
		SecretHash: HashSecret(k.pepper, p.Secret)}
	k.cache.SetBypass(false)
	k.cache.PutIfCurrent(k.cache.Epoch(), Loaded{Prefix: p.Prefix, Key: &ke, Agent: &ag, Org: &org})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := k.AuthenticateKey(ctx, full); err != nil {
			b.Fatal(err)
		}
	}
}

func TestAuthWarmFromCache(t *testing.T) {
	k, _ := NewKeyAuth(KeyAuthOptions{Pepper: make([]byte, 32)})
	full, p, _ := NewAPIKey(ModeTest)
	org := OrgEntry{ID: ids.New[ids.OrgKind](), Name: "Acme"}
	ag := AgentEntry{ID: ids.New[ids.AgentKind](), OrgID: org.ID, Name: "support-bot", Status: AgentSuspended}
	ke := KeyEntry{KeyID: ids.New[ids.APIKeyKind](), OrgID: org.ID, AgentID: ag.ID, Mode: ModeTest, Scopes: httpx.DefaultAgentScopes,
		SecretHash: HashSecret(k.pepper, p.Secret)}
	k.cache.SetBypass(false)
	k.cache.PutIfCurrent(k.cache.Epoch(), Loaded{Prefix: p.Prefix, Key: &ke, Agent: &ag, Org: &org})
	pr, err := k.AuthenticateKey(context.Background(), full)
	if err != nil || pr.Kind != httpx.KindAgentKey || pr.Livemode || pr.AgentStatus != "suspended" || pr.AgentName != "support-bot" {
		t.Fatalf("%+v %v", pr, err)
	}
	// A mode that doesn't match the stored key is invalid.
	if _, err := k.AuthenticateKey(context.Background(), strings.Replace(full, "dg_test_", "dg_live_", 1)); httpx.AsError(err).Code != httpx.CodeInvalidAPIKey {
		t.Fatal(err)
	}
}
