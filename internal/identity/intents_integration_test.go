//go:build integration

package identity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// S09: signed intents, on-behalf-of and MCP readiness against a real Postgres.

// intentAgent is an agent with an API key and a registered intent key.
type intentAgent struct {
	a      identity.AgentResult
	key    string // the agent's API key secret
	signer *deedglass.IntentSigner
}

func (f *fleet) intentAgent(t testing.TB, name string, register bool) intentAgent {
	t.Helper()
	a := f.agent(t, name)
	k := f.key(t, a.ID, "")
	pemBytes, pub, kid, err := deedglass.GenerateIntentKey()
	if err != nil {
		t.Fatal(err)
	}
	s, err := deedglass.LoadIntentSigner(pemBytes, f.org.Org.ID.String(), a.ID)
	if err != nil || s.KID() != kid {
		t.Fatal(err)
	}
	if register {
		r := f.w.call(t, f.admin, "POST", "/v1/agents/"+a.ID+"/intent-keys", `{"public_key":"`+pub+`"}`)
		expect(t, r, 201, "")
	}
	return intentAgent{a: a, key: *k.Secret, signer: s}
}

func (ia intentAgent) principal(f *fleet) *identity.Principal {
	id, _ := ids.Parse[ids.AgentKind](ia.a.ID)
	return &identity.Principal{Kind: httpx.KindAgentKey, OrgID: f.org.Org.ID, AgentID: id, AgentName: ia.a.Name}
}

func (ia intentAgent) sign(t testing.TB, w *world, args string, o deedglass.SignOptions) string {
	t.Helper()
	if o.Now == nil {
		o.Now = w.clock.Now
	}
	h, _, err := ia.signer.Sign("stripe.refund", json.RawMessage(args), o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (f *fleet) mode(t testing.TB, agent, mode string, status int) {
	t.Helper()
	r := f.w.call(t, f.admin, "PATCH", "/v1/agents/"+agent, `{"intent_mode":"`+mode+`"}`)
	if r.status != status {
		t.Fatalf("mode %s: %d %s", mode, r.status, r.body)
	}
}

const refundArgs = `{"charge":"ch_3QxZ8f2L","amount":1200}`

func verifyBody(args string) string { return `{"tool":"stripe.refund","args":` + args + `}` }

// The S09 demo: keygen → add → sign → verify → replay refused → changed args refused.
func TestIntentRoundTrip(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	ia := f.intentAgent(t, "support-bot", true)
	h := ia.sign(t, w, refundArgs, deedglass.SignOptions{OnBehalfOf: "usr_ext_4821", Session: "conv_9f2c"})

	r := w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(refundArgs), "Deedglass-Intent", h)
	expect(t, r, 200, "")
	var ic identity.IntentCheck
	r.json(t, &ic)
	if ic.Object != "intent_check" || !ic.Signed || !ic.NonceConsumed || ic.Agent != ia.a.ID || ic.Tool != "stripe.refund" ||
		*ic.OnBehalfOf != "usr_ext_4821" || *ic.Session != "conv_9f2c" || *ic.KID != ia.signer.KID() || ic.Mode != identity.IntentOff ||
		ic.ArgsDigest != "sha256:f7411a7cf4706333c9ba80c85aab7d8b92363ad3077a288dadc26bef42d89d36" {
		t.Fatalf("%s", r.body)
	}
	// The same request again: refused, naming when it was first used.
	r = w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(refundArgs), "Deedglass-Intent", h)
	expect(t, r, 403, "intent_replay")
	if !strings.HasPrefix(r.message(), "This signed intent was already used at ") || !strings.Contains(flat(r), `"param":"nonce"`) {
		t.Fatalf("%s", r.body)
	}
	// A fresh intent, but the body says 120000.
	h2 := ia.sign(t, w, refundArgs, deedglass.SignOptions{})
	r = w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(`{"charge":"ch_3QxZ8f2L","amount":120000}`), "Deedglass-Intent", h2)
	expect(t, r, 403, "intent_invalid")
	if r.message() != "The signed intent's args_digest doesn't match the arguments sent. Sign the exact arguments you send, canonicalized with RFC 8785." {
		t.Fatalf("%s", r.body)
	}
	// Intent errors are never 401: an OAuth-capable client would start an authorization flow.
	if r.header.Get("WWW-Authenticate") != "" {
		t.Fatalf("challenge on an intent error: %v", r.header)
	}
	// No header: 422 until the agent requires intents, then 403 intent_required.
	expect(t, w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(refundArgs)), 422, "validation_failed")
	f.mode(t, ia.a.ID, "required", 200)
	r = w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(refundArgs))
	expect(t, r, 403, "intent_required")
	if r.message() != `Agent "support-bot" requires a signed intent for every call. Send a Deedglass-Intent header; see docs.deedglass.com/intents.` {
		t.Fatal(r.message())
	}
	// Admin keys can't call the conformance check: it is for agents.
	expect(t, w.call(t, f.admin, "POST", "/v1/intents/verify", verifyBody(refundArgs), "Deedglass-Intent", h2), 403, "insufficient_scope")
}

// §3.5, every cell, through the IntentChecker S13 will call.
func TestIntentModes(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	ia := f.intentAgent(t, "support-bot", true)
	p := ia.principal(f)
	for _, mode := range []string{"off", "optional", "required"} {
		f.mode(t, ia.a.ID, mode, 200)
		cases := []struct {
			name   string
			header func() string
			signed bool
			code   string
		}{
			{"no header", func() string { return "" }, false, map[string]string{"required": "intent_required"}[mode]},
			{"valid", func() string { return ia.sign(t, w, refundArgs, deedglass.SignOptions{}) }, mode != "off", ""},
			{"invalid", func() string { return ia.sign(t, w, `{"amount":1}`, deedglass.SignOptions{}) }, false,
				map[string]string{"optional": "intent_invalid", "required": "intent_invalid"}[mode]},
		}
		for _, c := range cases {
			res, err := waitIntent(t, w, identity.IntentInput{Principal: p, Header: c.header(), Tool: "stripe.refund", Args: json.RawMessage(refundArgs)}, mode)
			code := ""
			if e := httpx.AsError(err); e != nil {
				code = string(e.Code)
				if e.Status != 403 {
					t.Errorf("%s/%s: status %d", mode, c.name, e.Status)
				}
			} else if err != nil {
				t.Fatalf("%s/%s: %v", mode, c.name, err)
			}
			if code != c.code || res.Signed != c.signed {
				t.Errorf("%s/%s: signed %v code %q, want %v %q", mode, c.name, res.Signed, code, c.signed, c.code)
			}
		}
	}
}

// waitIntent checks in, retrying briefly until the checker sees mode (the cache drops the agent on
// the NOTIFY the PATCH sent, within the listener's latency).
func waitIntent(t testing.TB, w *world, in identity.IntentInput, mode string) (identity.IntentResult, error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		probe := in
		probe.Header = ""
		r, _ := w.intents.Check(ctx, probe)
		if string(r.Mode) == mode || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return w.intents.Check(ctx, in)
}

func TestIntentKeyRules(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	a := f.agent(t, "support-bot")
	path := "/v1/agents/" + a.ID + "/intent-keys"
	add := func(pub string) resp { return w.call(t, f.admin, "POST", path, `{"public_key":"`+pub+`"}`) }
	// Small-order and non-canonical encodings (internal/keys) are 422 with param public_key.
	for _, bad := range []struct{ pub, want string }{
		{"ed25519:AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "small-order"}, // y = 1, the identity
		{"ed25519:JuiVj8KyJ7BFw_SJ8u-Y8NXfrAXTxjM5sTgCiG1T_AU", "small-order"}, // a point of order 8
		{"ed25519:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "small-order"}, // y = 0, order 4
		{"ed25519:7f_______________________________________38", "canonical"},   // y = p
		{"ed25519:AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAIA", "canonical"},   // x = 0 with the sign bit
		{"x25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo", "ed25519:"},     // wrong prefix
		{"ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo=", "ed25519:"},   // padding
		{"ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHUR", "ed25519:"},     // 31 bytes
	} {
		r := add(bad.pub)
		expect(t, r, 422, "validation_failed")
		if !strings.Contains(flat(r), `"param":"public_key"`) || (bad.want != "" && !strings.Contains(r.message(), bad.want)) {
			t.Errorf("%s: %s", bad.pub, r.body)
		}
	}
	if r := add("ed25519:AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); r.message() != "That public key is a small-order point, so a single signature would verify for every message. Generate a fresh key with deedglass agent intent keygen support-bot." {
		t.Fatal(r.message())
	}
	// Required with no keys: 409.
	f.mode(t, a.ID, "required", 409)
	_, pub1, kid1, _ := deedglass.GenerateIntentKey()
	_, pub2, kid2, _ := deedglass.GenerateIntentKey()
	_, pub3, _, _ := deedglass.GenerateIntentKey()
	r := add(pub1)
	expect(t, r, 201, "")
	var k identity.IntentKeyObject
	r.json(t, &k)
	if k.Object != "intent_key" || k.KID != kid1 || k.Agent != a.ID || k.PublicKey != pub1 || k.Deed == nil || k.CreatedBy == "" {
		t.Fatalf("%s", r.body)
	}
	expect(t, add(pub1), 409, "conflict") // the same kid
	expect(t, add(pub2), 201, "")
	r = add(pub3)
	expect(t, r, 409, "conflict")
	if r.message() != "support-bot already has two intent keys. Remove one first; two lets you rotate without downtime." {
		t.Fatal(r.message())
	}
	// Listing: viewers and admin keys; agent keys never manage agents.
	r = w.call(t, f.admin, "GET", path, "")
	expect(t, r, 200, "")
	if !strings.Contains(flat(r), kid1) || !strings.Contains(flat(r), kid2) {
		t.Fatalf("%s", r.body)
	}
	agentKey := *f.key(t, a.ID, "").Secret
	expect(t, w.call(t, agentKey, "GET", path, ""), 403, "insufficient_scope")
	expect(t, w.call(t, agentKey, "POST", path, `{"public_key":"`+pub3+`"}`), 403, "insufficient_scope")
	// Required, then remove down to one key; the last one can't go.
	f.mode(t, a.ID, "required", 200)
	expect(t, w.call(t, f.admin, "DELETE", path+"/"+kid2, ""), 200, "")
	r = w.call(t, f.admin, "DELETE", path+"/"+kid1, "")
	expect(t, r, 409, "conflict")
	expect(t, w.call(t, f.admin, "DELETE", path+"/"+kid2, ""), 404, "not_found")
	// A bad mode is 400; the same mode is a no-op with no deed.
	expect(t, w.call(t, f.admin, "PATCH", "/v1/agents/"+a.ID, `{"intent_mode":"strict"}`), 400, "validation_failed")
	r = w.call(t, f.admin, "PATCH", "/v1/agents/"+a.ID, `{"intent_mode":"required"}`)
	expect(t, r, 200, "")
	if !strings.Contains(flat(r), `"deed":null`) || !strings.Contains(flat(r), `"intent_mode":"required"`) {
		t.Fatalf("%s", r.body)
	}
	f.mode(t, a.ID, "optional", 200)
	expect(t, w.call(t, f.admin, "DELETE", path+"/"+kid1, ""), 200, "")
	// Every change was sealed, and the chain is intact.
	for action, n := range map[string]int{ledger.AdminAgentIntentKeyAdded: 2, ledger.AdminAgentIntentKeyRemoved: 2, ledger.AdminAgentIntentMode: 2} {
		if got := f.adminEvents(t, action, a.ID); got != n {
			t.Errorf("%s: %d sealed, want %d", action, got, n)
		}
	}
	res, err := w.led.CheckChain(ctx, f.org.Org.ID, ledger.Range{}, ledger.CheckOptions{})
	if err != nil || res.Status != ledger.CheckIntact {
		t.Fatalf("chain %v %+v", err, res)
	}
}

// §3.7: 100 concurrent uses of one header give exactly one success; the same nonce for two agents
// is fine.
func TestNonceConcurrency(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	ia := f.intentAgent(t, "support-bot", true)
	ib := f.intentAgent(t, "refund-agent", true)
	f.mode(t, ia.a.ID, "optional", 200)
	f.mode(t, ib.a.ID, "optional", 200)
	nonce := "05c18be68a24c056cd6c7c8910dc0729"
	h := ia.sign(t, w, refundArgs, deedglass.SignOptions{Nonce: nonce})
	var ok, replay atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			res, err := w.intents.Check(ctx, identity.IntentInput{Principal: ia.principal(f), Header: h, Tool: "stripe.refund", Args: json.RawMessage(refundArgs)})
			switch {
			case err == nil && res.Signed:
				ok.Add(1)
			case identity.IntentReason(err) == "replay":
				replay.Add(1)
			default:
				t.Errorf("%v %+v", err, res)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 1 || replay.Load() != 99 {
		t.Fatalf("ok %d, replay %d", ok.Load(), replay.Load())
	}
	hb := ib.sign(t, w, refundArgs, deedglass.SignOptions{Nonce: nonce})
	if res, err := w.intents.Check(ctx, identity.IntentInput{Principal: ib.principal(f), Header: hb, Tool: "stripe.refund", Args: json.RawMessage(refundArgs)}); err != nil || !res.Signed {
		t.Fatalf("the same nonce for another agent: %v", err)
	}
}

// §3.7: a pruned nonce is never accepted again, because its intent is refused as expired first.
func TestNoncePruneInvariant(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	ia := f.intentAgent(t, "support-bot", true)
	f.mode(t, ia.a.ID, "optional", 200)
	signedAt := time.Now().Add(-10 * time.Minute).Truncate(time.Millisecond)
	h := ia.sign(t, w, refundArgs, deedglass.SignOptions{Now: func() time.Time { return signedAt }})
	in := identity.IntentInput{Principal: ia.principal(f), Header: h, Tool: "stripe.refund", Args: json.RawMessage(refundArgs), Now: signedAt.Add(10 * time.Second)}
	if _, err := w.intents.Check(ctx, in); err != nil {
		t.Fatal(err)
	}
	n, err := w.e.DB.PreAuth().PruneIntentNonces(ctx, 10000)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
	if got := countRows(t, w, f, "intent_nonces"); got != 0 {
		t.Fatalf("%d nonces left", got)
	}
	in.Now = time.Now()
	if _, err := w.intents.Check(ctx, in); identity.IntentReason(err) != "expired" {
		t.Fatalf("resent after prune: %v", err)
	}
	// A nonce inside its window is kept.
	h2 := ia.sign(t, w, refundArgs, deedglass.SignOptions{Now: time.Now})
	if _, err := w.intents.Check(ctx, identity.IntentInput{Principal: ia.principal(f), Header: h2, Tool: "stripe.refund", Args: json.RawMessage(refundArgs), Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if n, _ := w.e.DB.PreAuth().PruneIntentNonces(ctx, 10000); n != 0 {
		t.Fatalf("pruned a live nonce")
	}
}

// RLS isolates nonces between orgs.
func TestNonceRLS(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	g := newFleet(t, w, "Globex", "hank@globex.com")
	ia := f.intentAgent(t, "support-bot", true)
	f.mode(t, ia.a.ID, "optional", 200)
	if _, err := w.intents.Check(ctx, identity.IntentInput{Principal: ia.principal(f), Header: ia.sign(t, w, refundArgs, deedglass.SignOptions{}),
		Tool: "stripe.refund", Args: json.RawMessage(refundArgs)}); err != nil {
		t.Fatal(err)
	}
	seen := func(org ids.OrgID) int {
		var n int
		if err := w.e.DB.OrgReadTx(ctx, org, func(ctx2 context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx2, `SELECT count(*) FROM deedglass.intent_nonces`).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if seen(f.org.Org.ID) != 1 || seen(g.org.Org.ID) != 0 {
		t.Fatalf("acme %d, globex %d", seen(f.org.Org.ID), seen(g.org.Org.ID))
	}
}

// Adding a key reaches a warm cache: the agent's next call can use it at once (the NOTIFY evicts
// the cached agent).
func TestIntentKeyReachesWarmCache(t *testing.T) {
	w := newWorld(t)
	waitHealthy(t, w.keys)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	ia := f.intentAgent(t, "support-bot", false)
	f.mode(t, ia.a.ID, "optional", 200)
	// Warm the cache with the agent (no keys yet).
	expect(t, w.call(t, ia.key, "GET", "/v1/whoami", ""), 200, "")
	r := w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(refundArgs), "Deedglass-Intent", ia.sign(t, w, refundArgs, deedglass.SignOptions{}))
	expect(t, r, 403, "intent_invalid")
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+ia.a.ID+"/intent-keys", `{"public_key":"`+ia.signer.PublicKey()+`"}`), 201, "")
	deadline := time.Now().Add(2 * time.Second)
	for {
		r = w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(refundArgs), "Deedglass-Intent", ia.sign(t, w, refundArgs, deedglass.SignOptions{}))
		if r.status == 200 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	expect(t, r, 200, "")
}

// §3.9: OBO headers on every key route, and their binding to a signed intent.
func TestOnBehalfOf(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	ia := f.intentAgent(t, "support-bot", true)
	for _, c := range []struct {
		name, value string
		status      int
	}{
		{"opaque id", "usr_ext_4821", 200},
		{"every allowed character", "Az09._~:@/+=-", 200},
		{"256 characters", strings.Repeat("a", 256), 200},
		{"257 characters", strings.Repeat("a", 257), 422},
		{"a space", "Priya Raman", 422},
		{"non-ASCII", "usr_café", 422},
		{"an email", "priya@acme.com", 200}, // allowed by the grammar; the docs say don't
	} {
		r := w.call(t, ia.key, "GET", "/v1/whoami", "", "Deedglass-On-Behalf-Of", c.value)
		if r.status != c.status {
			t.Errorf("%s: %d %s", c.name, r.status, r.body)
		}
		if c.status == 422 && (!strings.Contains(flat(r), `"param":"Deedglass-On-Behalf-Of"`) || r.code() != "validation_failed") {
			t.Errorf("%s: %s", c.name, r.body)
		}
	}
	// Two headers.
	req, _ := http.NewRequest("GET", w.ts.URL+"/v1/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+ia.key)
	req.Header.Add("Deedglass-Session", "a")
	req.Header.Add("Deedglass-Session", "b")
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 422 {
		t.Fatalf("two Deedglass-Session headers: %v %v", err, res.StatusCode)
	}
	res.Body.Close()
	// Intent only: the effective values come from the intent.
	h := ia.sign(t, w, refundArgs, deedglass.SignOptions{OnBehalfOf: "usr_ext_4821", Session: "conv_9f2c"})
	r := w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(refundArgs), "Deedglass-Intent", h)
	expect(t, r, 200, "")
	if !strings.Contains(flat(r), `"on_behalf_of":"usr_ext_4821","session":"conv_9f2c"`) {
		t.Fatalf("%s", r.body)
	}
	// A header that differs, and a header the intent doesn't cover.
	h = ia.sign(t, w, refundArgs, deedglass.SignOptions{OnBehalfOf: "usr_ext_4821"})
	r = w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(refundArgs), "Deedglass-Intent", h, "Deedglass-On-Behalf-Of", "usr_ext_9")
	expect(t, r, 403, "intent_invalid")
	if r.message() != "Deedglass-On-Behalf-Of says usr_ext_9, but the signed intent says usr_ext_4821. When you sign an intent, send the same value in both, or only in the intent." {
		t.Fatal(r.message())
	}
	r = w.call(t, ia.key, "POST", "/v1/intents/verify", verifyBody(refundArgs), "Deedglass-Intent", h, "Deedglass-Session", "conv_1")
	expect(t, r, 403, "intent_invalid")
	if !strings.Contains(flat(r), `"param":"Deedglass-Session"`) {
		t.Fatalf("%s", r.body)
	}
}

// §3.10 against the real key pipeline: the challenges, the Origin rule, the stub and the methods.
func TestMCPReadiness(t *testing.T) {
	w := newWorld(t)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	ia := f.intentAgent(t, "support-bot", false)
	con := "/mcp/con_01k67c04kxevyex3pjyqe0t45d"
	meta := w.base.String() + "/.well-known/oauth-protected-resource" + con
	rpc := `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`

	r := w.call(t, "", "POST", con, rpc)
	expect(t, r, 401, "authentication_required")
	if got := r.header.Get("WWW-Authenticate"); got != `Bearer realm="deedglass", resource_metadata="`+meta+`", scope="actions:execute"` {
		t.Fatalf("challenge %q", got)
	}
	revoked := *f.key(t, ia.a.ID, "").Secret
	var kl struct{ Data []identity.APIKey }
	w.call(t, f.admin, "GET", "/v1/agents/"+ia.a.ID+"/keys", "").json(t, &kl)
	for _, k := range kl.Data {
		if strings.HasPrefix(revoked, k.Prefix) {
			expect(t, w.call(t, f.admin, "POST", "/v1/keys/"+k.ID+"/revoke", ""), 200, "")
		}
	}
	r = w.call(t, revoked, "POST", con, rpc)
	expect(t, r, 401, "api_key_revoked")
	ch := parseChallenge(t, r.header.Get("WWW-Authenticate"))
	if ch["error"] != "invalid_token" || ch["resource_metadata"] != meta || ch["scope"] != "actions:execute" || ch["error_description"] == "" {
		t.Fatalf("%v", ch)
	}
	r = w.call(t, f.admin, "POST", con, rpc)
	expect(t, r, 403, "insufficient_scope")
	ch = parseChallenge(t, r.header.Get("WWW-Authenticate"))
	if ch["error"] != "insufficient_scope" || ch["scope"] != "actions:execute" || ch["resource_metadata"] != meta {
		t.Fatalf("%v", ch)
	}
	// An authenticated agent reaches the stub: 404, JSON-RPC -32601, the id echoed.
	r = w.call(t, ia.key, "POST", con, rpc)
	if r.status != 404 || flat(r) != `{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"The MCP gateway isn't enabled on this server."}}` {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r = w.call(t, ia.key, "POST", "/mcp/native", `{"jsonrpc":"2.0","method":"tools/list"}`)
	if r.status != 404 || !strings.Contains(flat(r), `"id":null`) {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// Stop: 403 without a Bearer error (it isn't a token problem).
	expect(t, w.call(t, f.admin, "POST", "/v1/agents/"+ia.a.ID+"/stop", `{"reason":"test"}`), 200, "")
	r = w.call(t, ia.key, "POST", con, rpc)
	expect(t, r, 403, "agent_stopped")
	if strings.Contains(r.header.Get("WWW-Authenticate"), "error=") {
		t.Fatalf("stop carries a Bearer error: %v", r.header)
	}
	// Origin.
	r = w.call(t, ia.key, "POST", con, rpc, "Origin", "https://evil.example")
	if r.status != 403 || flat(r) != `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Deedglass refuses browser requests from this origin, to protect MCP endpoints from DNS rebinding. Call the Gate from your MCP client, not from a web page."}}` {
		t.Fatalf("%d %s", r.status, r.body)
	}
	expect(t, w.call(t, "", "POST", con, rpc, "Origin", w.base.String()), 401, "authentication_required")
	// Methods.
	for _, m := range []string{"GET", "DELETE"} {
		r = w.call(t, ia.key, m, con, "")
		if r.status != 405 || r.header.Get("Allow") != "POST" {
			t.Fatalf("%s: %d %v", m, r.status, r.header)
		}
	}
	// The metadata, served without a key.
	r = w.call(t, "", "GET", "/.well-known/oauth-protected-resource"+con, "")
	expect(t, r, 200, "")
	if r.header.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(flat(r), `"resource":"`+w.base.String()+con+`"`) {
		t.Fatalf("%v %s", r.header, r.body)
	}
	expect(t, w.call(t, "", "GET", "/.well-known/oauth-protected-resource/mcp/con_nope", ""), 404, "not_found")
}

// parseChallenge reads Bearer auth-params (RFC 9110 §11.2): token=token or token=quoted-string,
// comma separated. The quoted strings here never contain a quote or backslash (RFC 6750 §3).
func parseChallenge(t testing.TB, v string) map[string]string {
	t.Helper()
	rest, ok := strings.CutPrefix(v, "Bearer ")
	if !ok {
		t.Fatalf("not a Bearer challenge: %q", v)
	}
	out := map[string]string{}
	for rest != "" {
		k, after, ok := strings.Cut(rest, "=")
		if !ok {
			t.Fatalf("bad auth-param in %q", v)
		}
		k = strings.TrimSpace(k)
		var val string
		if strings.HasPrefix(after, `"`) {
			end := strings.IndexByte(after[1:], '"')
			if end < 0 {
				t.Fatalf("unterminated quoted-string in %q", v)
			}
			val, rest = after[1:1+end], after[2+end:]
		} else {
			val, rest, _ = strings.Cut(after, ",")
			rest = "," + rest
		}
		if _, dup := out[k]; dup {
			t.Fatalf("duplicate %s in %q", k, v)
		}
		out[k] = val
		rest = strings.TrimPrefix(strings.TrimSpace(rest), ",")
		rest = strings.TrimSpace(rest)
	}
	return out
}

// §3.13: checks 1–12 without the database, and Check with the nonce insert (local PG 18).
func TestIntentBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("set DEEDGLASS_IT_BENCH=1 (make bench-intents)")
	}
	w := newWorld(t)
	waitHealthy(t, w.keys)
	f := newFleet(t, w, "Acme", "priya@acme.com")
	ia := f.intentAgent(t, "support-bot", true)
	f.mode(t, ia.a.ID, "optional", 200)
	p := ia.principal(f)
	const n = 2000
	headers := make([]string, n)
	for i := range headers {
		headers[i] = ia.sign(t, w, refundArgs, deedglass.SignOptions{Now: time.Now})
	}
	// Warm the cache.
	if _, err := w.intents.Check(ctx, identity.IntentInput{Principal: p, Tool: "stripe.refund", Args: json.RawMessage(refundArgs)}); err != nil {
		t.Fatal(err)
	}
	d := make([]time.Duration, n)
	for i := range d {
		s := time.Now()
		if _, err := w.intents.Check(ctx, identity.IntentInput{Principal: p, Header: headers[i], Tool: "stripe.refund", Args: json.RawMessage(refundArgs)}); err != nil {
			t.Fatal(err)
		}
		d[i] = time.Since(s)
	}
	r := summarize("intent check with the nonce insert (Postgres)", d)
	t.Logf("PERF {\"name\":%q,\"n\":%d,\"p50_us\":%.1f,\"p99_us\":%.1f}", r.Name, r.N, r.P50MS*1000, r.P99MS*1000)
	if p99 := pctD(d, 99); p99 > 1500*time.Microsecond {
		t.Errorf("budget missed (load: check uptime): p99 %v (1.5 ms)", p99)
	}
}
