package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

func vecAgentWithKey() (intentAgent, ed25519.PrivateKey) {
	k := seedKey(seedTest1)
	pub := k.Public().(ed25519.PublicKey)
	return intentAgent{org: mustOrg(vecOrg), id: mustAgent(vecAgent), name: vecName, mode: IntentOptional,
		keys: []IntentKey{{KID: testKID(k), PublicKey: FormatIntentPublicKey(pub), pub: pub}}}, k
}

func vecSigner(t testing.TB) *deedglass.IntentSigner {
	t.Helper()
	der := pemOfB(seedKey(seedTest1))
	s, err := deedglass.LoadIntentSigner(der, vecOrg, vecAgent)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func pemOfB(k ed25519.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestParseOBO(t *testing.T) {
	for _, c := range []struct {
		name string
		h    http.Header
		want OBO
		bad  string
	}{
		{"none", http.Header{}, OBO{}, ""},
		{"both", http.Header{"Deedglass-On-Behalf-Of": {"usr_ext_4821"}, "Deedglass-Session": {"conv_9f2c"}}, OBO{"usr_ext_4821", "conv_9f2c"}, ""},
		{"every character", http.Header{"Deedglass-On-Behalf-Of": {"AZaz09._~:@/+=-"}}, OBO{OnBehalfOf: "AZaz09._~:@/+=-"}, ""},
		{"256", http.Header{"Deedglass-Session": {strings.Repeat("s", 256)}}, OBO{Session: strings.Repeat("s", 256)}, ""},
		{"257", http.Header{"Deedglass-Session": {strings.Repeat("s", 257)}}, OBO{}, "Deedglass-Session"},
		{"empty", http.Header{"Deedglass-On-Behalf-Of": {""}}, OBO{}, "Deedglass-On-Behalf-Of"},
		{"space", http.Header{"Deedglass-On-Behalf-Of": {"Priya Raman"}}, OBO{}, "Deedglass-On-Behalf-Of"},
		{"non-ASCII", http.Header{"Deedglass-On-Behalf-Of": {"usr_é"}}, OBO{}, "Deedglass-On-Behalf-Of"},
		{"comma", http.Header{"Deedglass-On-Behalf-Of": {"a,b"}}, OBO{}, "Deedglass-On-Behalf-Of"},
		{"twice", http.Header{"Deedglass-Session": {"a", "b"}}, OBO{}, "Deedglass-Session"},
	} {
		got, err := ParseOBO(c.h)
		if c.bad == "" {
			if err != nil || got != c.want {
				t.Errorf("%s: %+v %v", c.name, got, err)
			}
			continue
		}
		e := httpx.AsError(err)
		if e == nil || e.Status != 422 || e.Code != httpx.CodeValidationFailed || e.Param != c.bad {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestParseIntentPublicKey(t *testing.T) {
	_, kid, err := ParseIntentPublicKey("ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo")
	if err != nil || kid != "21fe31dfa154a261626bf854046fd227" {
		t.Fatalf("%s %v", kid, err)
	}
	for _, bad := range []string{
		"ed25519:AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // identity
		"ed25519:JuiVj8KyJ7BFw_SJ8u-Y8NXfrAXTxjM5sTgCiG1T_AU", // order 8
		"ed25519:7f_______________________________________38", // y = p
		"ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURp", // non-zero trailing bits
		"ED25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo",
		"11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo",
		"",
	} {
		if _, _, err := ParseIntentPublicKey(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// Modes with no header, and the off mode, never touch the nonce ledger (no database here).
func TestModesWithoutHeader(t *testing.T) {
	a, _ := vecAgentWithKey()
	for _, m := range []IntentMode{IntentOff, IntentOptional, IntentRequired} {
		a.mode = m
		_, err := verifyIntent(a, IntentInput{Header: "x", Tool: "stripe.refund", Args: json.RawMessage(`{}`), Now: time.Now()})
		if IntentReason(err) != "syntax" {
			t.Errorf("%s: %v", m, err)
		}
	}
	if !IntentOff.Valid() || IntentMode("strict").Valid() || IntentMode("").Valid() {
		t.Fatal("Valid")
	}
}

// Every intent copy entry reads as the brand asks: a full stop, no dashes, no unfilled placeholder
// once filled, and every refusal is 403.
func TestIntentCopy(t *testing.T) {
	for key, tpl := range httpx.IntentCopy {
		if !strings.HasSuffix(tpl, ".") || strings.ContainsAny(tpl, "–—!") {
			t.Errorf("%s: %q", key, tpl)
		}
		e := httpx.ErrIntent(key, "", "agent", "support-bot", "kid", "21fe…d227", "time", "09:30:02", "detail", "x", "field", "nonce",
			"want", "w", "got", "g", "intent_org", "o", "org", "o2", "intent_agent", "a", "agent_id", "a2", "ago", "3 minutes", "ahead", "4 minutes",
			"ms", "1", "intent_tool", "t", "tool", "t2", "header", "h", "intent", "i", "name", "Deedglass-Session")
		if e.Status != 403 || strings.Contains(e.Message, "{") || (e.Param == "" && !strings.HasPrefix(key, "schema")) {
			t.Errorf("%s: %d %q param %q", key, e.Status, e.Message, e.Param)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "less than a second", time.Second: "1 second", 45 * time.Second: "45 seconds",
		61 * time.Second: "1 minute", 3 * time.Minute: "3 minutes", 90 * time.Minute: "1 hour", 5 * time.Hour: "5 hours", 72 * time.Hour: "3 days"} {
		if got := humanDuration(d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}

// The expired and not-yet-valid messages name the time and the distance.
func TestClockMessages(t *testing.T) {
	a, _ := vecAgentWithKey()
	s := vecSigner(t)
	at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	h, _, _ := s.Sign("stripe.refund", json.RawMessage(`{}`), deedglass.SignOptions{Now: func() time.Time { return at }})
	_, err := verifyIntent(a, IntentInput{Header: h, Tool: "stripe.refund", Args: json.RawMessage(`{}`), Now: at.Add(4 * time.Minute)})
	if e := httpx.AsError(err); e == nil || e.Message != "The signed intent expired at 09:31:00 UTC, 3 minutes ago. Sign a fresh intent for each call; an intent lasts at most 5 minutes." {
		t.Fatalf("%v", err)
	}
	_, err = verifyIntent(a, IntentInput{Header: h, Tool: "stripe.refund", Args: json.RawMessage(`{}`), Now: at.Add(-4 * time.Minute)})
	if e := httpx.AsError(err); e == nil || e.Message != "The signed intent was issued 4 minutes in the future. Check the agent's clock; Deedglass allows 60 seconds of skew." {
		t.Fatalf("%v", err)
	}
}

// Property: random arguments (nested, unicode, fractions in range) sign and check.
func TestIntentRoundTripProperty(t *testing.T) {
	a, _ := vecAgentWithKey()
	s := vecSigner(t)
	r := rand.New(rand.NewPCG(9, 3))
	now := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	for i := range 1000 {
		args, _ := json.Marshal(randomValue(r, 0))
		obo := ""
		if i%3 == 0 {
			obo = fmt.Sprintf("usr_%d", i)
		}
		h, hash, err := s.Sign("pg.public.orders", args, deedglass.SignOptions{OnBehalfOf: obo, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		res, err := verifyIntent(a, IntentInput{Header: h, Tool: "pg.public.orders", Args: args, OBO: OBO{OnBehalfOf: obo}, Now: now.Add(time.Second)})
		if err != nil || !res.Signed || res.IntentHash != hash || res.OBO.OnBehalfOf != obo {
			t.Fatalf("%s: %v %+v", args, err, res)
		}
	}
}

func randomValue(r *rand.Rand, depth int) any {
	switch k := r.IntN(7); {
	case depth > 4 || k == 0:
		return r.Int64N(1<<53) - 1<<52
	case k == 1:
		return r.Float64()*1e6 - 5e5
	case k == 2:
		return []string{"café", "東京", "a\"b\\c", " ", "😀", ""}[r.IntN(6)]
	case k == 3:
		return r.IntN(2) == 0
	case k == 4:
		return nil
	case k == 5:
		out := make([]any, r.IntN(4))
		for i := range out {
			out[i] = randomValue(r, depth+1)
		}
		return out
	default:
		m := map[string]any{}
		for range r.IntN(5) {
			m[fmt.Sprintf("k%dé%d", r.IntN(50), depth)] = randomValue(r, depth+1)
		}
		return m
	}
}

// FuzzIntentHeader: no panic, and every refusal carries a reason.
func FuzzIntentHeader(f *testing.F) {
	s := vecSigner(f)
	at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	h, _, _ := s.Sign("stripe.refund", json.RawMessage(`{"amount":1200}`), deedglass.SignOptions{Now: func() time.Time { return at }})
	for _, seed := range []string{h, "", ".", "a.b", h + ".x", strings.Replace(h, "e", "f", 1)} {
		f.Add(seed)
	}
	a, _ := vecAgentWithKey()
	f.Fuzz(func(t *testing.T, header string) {
		res, err := verifyIntent(a, IntentInput{Header: header, Tool: "stripe.refund", Args: json.RawMessage(`{"amount":1200}`), Now: at.Add(time.Second)})
		if err == nil {
			if !res.Signed || res.IntentHash == "" {
				t.Fatalf("accepted without a hash: %+v", res)
			}
			return
		}
		if IntentReason(err) == "" || httpx.AsError(err) == nil || httpx.AsError(err).Status != 403 {
			t.Fatalf("a refusal without a reason: %v", err)
		}
	})
}

// BenchmarkIntentCheck is checks 1–12 without the database, for args of about 4 KiB.
func BenchmarkIntentCheck(b *testing.B) {
	a, _ := vecAgentWithKey()
	s := vecSigner(b)
	args, _ := json.Marshal(map[string]any{"charge": "ch_3QxZ8f2L", "amount": 1200, "note": strings.Repeat("x", 4000)})
	at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	h, _, err := s.Sign("stripe.refund", args, deedglass.SignOptions{Now: func() time.Time { return at }})
	if err != nil {
		b.Fatal(err)
	}
	in := IntentInput{Header: h, Tool: "stripe.refund", Args: args, Now: at.Add(time.Second)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := verifyIntent(a, in); err != nil {
			b.Fatal(err)
		}
	}
}

// The p99 of checks 1–12 stays under 150 µs for 4 KiB of arguments (S09 §3.13).
func TestIntentCheckBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" || raceEnabled {
		t.Skip("timing budget: set DEEDGLASS_IT_BENCH=1 (make bench-intents), not under -race")
	}
	a, _ := vecAgentWithKey()
	s := vecSigner(t)
	args, _ := json.Marshal(map[string]any{"charge": "ch_3QxZ8f2L", "amount": 1200, "note": strings.Repeat("x", 4000)})
	at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	h, _, _ := s.Sign("stripe.refund", args, deedglass.SignOptions{Now: func() time.Time { return at }})
	in := IntentInput{Header: h, Tool: "stripe.refund", Args: args, Now: at.Add(time.Second)}
	d := make([]time.Duration, 5000)
	for i := range d {
		st := time.Now()
		if _, err := verifyIntent(a, in); err != nil {
			t.Fatal(err)
		}
		d[i] = time.Since(st)
	}
	slices.Sort(d)
	p50, p99 := d[len(d)/2], d[len(d)*99/100]
	t.Logf("PERF {\"name\":\"intent checks 1-12, 4 KiB args (no DB)\",\"n\":%d,\"p50_us\":%.1f,\"p99_us\":%.1f}", len(d),
		float64(p50.Nanoseconds())/1000, float64(p99.Nanoseconds())/1000)
	if p99 > 150*time.Microsecond {
		t.Errorf("p99 %v, budget 150 µs (machine load?)", p99)
	}
}

// --- MCP readiness (no database) ---

type fakeKeys struct{}

func (fakeKeys) AuthenticateKey(_ context.Context, bearer string) (Principal, error) {
	switch bearer {
	case "agent":
		return Principal{Kind: httpx.KindAgentKey, OrgID: mustOrg(vecOrg), AgentID: mustAgent(vecAgent), AgentName: vecName,
			AgentStatus: "active", Scopes: httpx.DefaultAgentScopes}, nil
	case "stopped":
		return Principal{Kind: httpx.KindAgentKey, OrgID: mustOrg(vecOrg), AgentID: mustAgent(vecAgent), AgentName: vecName,
			AgentStatus: "stopped", Scopes: httpx.DefaultAgentScopes}, nil
	case "admin":
		return Principal{Kind: httpx.KindAdminKey, OrgID: mustOrg(vecOrg), Scopes: []string{httpx.ScopeAdmin}, Role: RoleAdmin}, nil
	}
	return Principal{}, httpx.E(httpx.CodeAPIKeyRevoked)
}

func mcpServer(t *testing.T, base string) *httptest.Server {
	t.Helper()
	u, _ := url.Parse(base)
	srv := httpx.New(httpx.Options{Logger: slog.New(slog.DiscardHandler), Keys: fakeKeys{}, BaseURL: u})
	srv.Mount(NewMCPAuth(u).MCPRoutes()...)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func doReq(t *testing.T, ts *httptest.Server, method, path, key, body string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestProtectedResourceMetadata(t *testing.T) {
	ts := mcpServer(t, "HTTPS://API.Deedglass.com:443/")
	res, body := doReq(t, ts, "GET", "/.well-known/oauth-protected-resource", "", "")
	if res.StatusCode != 200 || compactJSON(body) != `{"resource":"https://api.deedglass.com","resource_name":"Deedglass","bearer_methods_supported":["header"],"scopes_supported":["actions:execute","actions:read","deeds:read"],"resource_documentation":"https://docs.deedglass.com/mcp/authorization"}` {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if res.Header.Get("Cache-Control") != "public, max-age=3600" || res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%v", res.Header)
	}
	res, body = doReq(t, ts, "GET", "/.well-known/oauth-protected-resource/mcp/con_01k67c04kxevyex3pjyqe0t45d", "", "")
	if res.StatusCode != 200 || compactJSON(body) != `{"resource":"https://api.deedglass.com/mcp/con_01k67c04kxevyex3pjyqe0t45d","resource_name":"DeedglassGate","bearer_methods_supported":["header"],"scopes_supported":["actions:execute"],"resource_documentation":"https://docs.deedglass.com/mcp/authorization"}` {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	if strings.Contains(body, "authorization_servers") {
		t.Fatal("authorization_servers must stay absent in v1")
	}
	// Any well-formed id gets the document (this server has no database at all); malformed ids are 404.
	res, _ = doReq(t, ts, "GET", "/.well-known/oauth-protected-resource/mcp/con_7zzzzzzzzzzzzzzzzzzzzzzzzz", "", "")
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	res, body = doReq(t, ts, "GET", "/.well-known/oauth-protected-resource/mcp/native", "", "")
	if res.StatusCode != 200 || !strings.Contains(body, `"resource": "https://api.deedglass.com/mcp/native"`) {
		t.Fatalf("%s", body)
	}
	for _, p := range []string{"con_", "con_8zzzzzzzzzzzzzzzzzzzzzzzzz", "agt_01k633rrmh3bcz2p39gg4vp46q", "CON_01K67C04KXEVYEX3PJYQE0T45D"} {
		if res, _ := doReq(t, ts, "GET", "/.well-known/oauth-protected-resource/mcp/"+p, "", ""); res.StatusCode != 404 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}
}

// compactJSON drops the indentation (the values compared here have no spaces).
func compactJSON(s string) string { return strings.Join(strings.Fields(s), "") }

func TestCanonicalResource(t *testing.T) {
	for in, want := range map[string]string{
		"HTTPS://API.Deedglass.com:443/":  "https://api.deedglass.com",
		"https://api.deedglass.com":       "https://api.deedglass.com",
		"https://api.deedglass.com:8443/": "https://api.deedglass.com:8443",
		"http://127.0.0.1:8080":           "http://127.0.0.1:8080",
		"http://localhost:80/":            "http://localhost",
		"https://x.example/base/":         "https://x.example/base",
		"https://x.example/base?q=1#frag": "https://x.example/base",
		"http://[::1]:8080/":              "http://[::1]:8080",
		"http://[::1]:80":                 "http://[::1]",
	} {
		u, _ := url.Parse(in)
		if got := CanonicalResource(u); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestMCPChallengesAndOrigin(t *testing.T) {
	ts := mcpServer(t, "https://api.deedglass.com")
	con := "/mcp/con_01k67c04kxevyex3pjyqe0t45d"
	meta := "https://api.deedglass.com/.well-known/oauth-protected-resource" + con
	rpc := `{"jsonrpc":"2.0","id":"a-1","method":"tools/list"}`
	res, _ := doReq(t, ts, "POST", con, "", rpc)
	if res.StatusCode != 401 || res.Header.Get("WWW-Authenticate") != `Bearer realm="deedglass", resource_metadata="`+meta+`", scope="actions:execute"` {
		t.Fatalf("%d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	res, _ = doReq(t, ts, "POST", con, "revoked", rpc)
	if res.StatusCode != 401 || res.Header.Get("WWW-Authenticate") != `Bearer realm="deedglass", error="invalid_token", error_description="The API key has been revoked. Create a new key in Console > Agents.", resource_metadata="`+meta+`", scope="actions:execute"` {
		t.Fatalf("%q", res.Header.Get("WWW-Authenticate"))
	}
	res, _ = doReq(t, ts, "POST", con, "admin", rpc)
	if res.StatusCode != 403 || res.Header.Get("WWW-Authenticate") != `Bearer realm="deedglass", error="insufficient_scope", scope="actions:execute", resource_metadata="`+meta+`"` {
		t.Fatalf("%q", res.Header.Get("WWW-Authenticate"))
	}
	res, _ = doReq(t, ts, "POST", con, "stopped", rpc)
	if res.StatusCode != 403 || res.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("%d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
	}
	res, body := doReq(t, ts, "POST", con, "agent", rpc)
	if res.StatusCode != 404 || strings.Join(strings.Fields(body), "") != `{"jsonrpc":"2.0","id":"a-1","error":{"code":-32601,"message":"TheMCPgatewayisn'tenabledonthisserver."}}` {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	// Origin: other origins and malformed values are refused before auth; the base URL's origin passes.
	for _, o := range []string{"https://evil.example", "null", "https://api.deedglass.com.evil.example", "http://api.deedglass.com", "https://api.deedglass.com/x"} {
		res, body := doReq(t, ts, "POST", con, "agent", rpc, "Origin", o)
		if res.StatusCode != 403 || strings.Contains(body, `"id"`) || !strings.Contains(body, "-32600") {
			t.Errorf("%s: %d %s", o, res.StatusCode, body)
		}
	}
	for _, o := range []string{"https://api.deedglass.com", "HTTPS://API.DEEDGLASS.COM:443"} {
		if res, _ := doReq(t, ts, "POST", con, "agent", rpc, "Origin", o); res.StatusCode != 404 {
			t.Errorf("%s: %d", o, res.StatusCode)
		}
	}
	for _, m := range []string{"GET", "DELETE"} {
		res, _ := doReq(t, ts, m, con, "agent", "")
		if res.StatusCode != 405 || res.Header.Get("Allow") != "POST" {
			t.Errorf("%s: %d %v", m, res.StatusCode, res.Header)
		}
	}
}

func TestStripInboundCredentials(t *testing.T) {
	h := http.Header{}
	for _, k := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Deedglass-Intent", "Deedglass-On-Behalf-Of", "Deedglass-Session",
		"Deedglass-Version", "Content-Type", "Mcp-Method", "Mcp-Name", "Mcp-Protocol-Version", "Accept"} {
		h.Set(k, "v")
	}
	h["deedglass-csrf"] = []string{"raw, non-canonical key"}
	StripInboundCredentials(h)
	var kept []string
	for k := range h {
		kept = append(kept, k)
	}
	slices.Sort(kept)
	if strings.Join(kept, ",") != "Accept,Content-Type,Mcp-Method,Mcp-Name,Mcp-Protocol-Version" {
		t.Fatalf("kept %v", kept)
	}
}
