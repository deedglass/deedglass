package deedglass

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	vOrg   = "org_01k6h7kp6msmzrhdzwtcr3jnkp"
	vAgent = "agt_01k633rrmh3bcz2p39gg4vp46q"
)

func test1PEM(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/intents/keys/test1.pem")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The S09 §3.2 cross-check vector, from the signer alone.
func TestSignerCrossCheckVector(t *testing.T) {
	s, err := LoadIntentSigner(test1PEM(t), vOrg, vAgent)
	if err != nil {
		t.Fatal(err)
	}
	if s.KID() != "21fe31dfa154a261626bf854046fd227" || s.PublicKey() != "ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo" {
		t.Fatalf("%s %s", s.KID(), s.PublicKey())
	}
	at := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	sg, err := s.SignDetailed("stripe.refund", json.RawMessage(`{"charge":"ch_3QxZ8f2L","amount":1200}`),
		SignOptions{OnBehalfOf: "usr_ext_4821", Session: "conv_9f2c", Now: func() time.Time { return at }, Nonce: "05c18be68a24c056cd6c7c8910dc0729"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(sg.Header, ".TQAgBevx6iwlDM2Z7px4mrRiCjdbrAX3GCacB_gMAQbvPagJqCsJUWYz7xS7VuQqyV2rOK71uyRHvym2nqjJCA") ||
		sg.IntentHash != "sha256:87c73e450a53f0269e33f7adb8430a6f89714606f9aae38bc732536fef7e3089" || len(sg.Intent) != 393 ||
		sg.ArgsDigest != "sha256:f7411a7cf4706333c9ba80c85aab7d8b92363ad3077a288dadc26bef42d89d36" ||
		string(sg.Args) != `{"amount":1200,"charge":"ch_3QxZ8f2L"}` || !sg.ExpiresAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("%+v", sg)
	}
	h, hash, err := s.Sign("stripe.refund", json.RawMessage(`{"amount":1200,"charge":"ch_3QxZ8f2L"}`),
		SignOptions{OnBehalfOf: "usr_ext_4821", Session: "conv_9f2c", Now: func() time.Time { return at }, Nonce: "05c18be68a24c056cd6c7c8910dc0729"})
	if err != nil || h != sg.Header || hash != sg.IntentHash {
		t.Fatal("key order in args changed the header")
	}
}

func TestGenerateIntentKey(t *testing.T) {
	priv, pub, kid, err := GenerateIntentKey()
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadIntentSigner(priv, vOrg, vAgent)
	if err != nil || s.KID() != kid || s.PublicKey() != pub || !strings.HasPrefix(string(priv), "-----BEGIN PRIVATE KEY-----") {
		t.Fatalf("%v %s %s", err, s.KID(), kid)
	}
	// Fresh nonces: two signatures of the same call differ.
	a, _, _ := s.Sign("stripe.refund", json.RawMessage(`{}`), SignOptions{})
	b, _, _ := s.Sign("stripe.refund", json.RawMessage(`{}`), SignOptions{})
	if a == b {
		t.Fatal("two signatures share a nonce")
	}
	_, _, kid2, _ := GenerateIntentKey()
	if kid2 == kid {
		t.Fatal("two keys share a kid")
	}
}

func TestLoadIntentSignerRefuses(t *testing.T) {
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(ec)
	// (A small-order public key can't come from a PKCS#8 file: it stores only the seed, and every
	// seed gives a full-order key. LoadIntentSigner checks anyway.)
	for name, b := range map[string][]byte{
		"not PEM":     []byte("nope"),
		"wrong block": pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}),
		"bad DER":     pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2, 3}}),
		"P-256":       pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
	} {
		if _, err := LoadIntentSigner(b, vOrg, vAgent); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSignRefuses(t *testing.T) {
	s, err := LoadIntentSigner(test1PEM(t), vOrg, vAgent)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		tool, args string
		o          SignOptions
	}{
		"ttl too long":   {"stripe.refund", `{}`, SignOptions{TTL: 5*time.Minute + time.Millisecond}},
		"ttl negative":   {"stripe.refund", `{}`, SignOptions{TTL: -time.Second}},
		"bad tool":       {"Stripe refund", `{}`, SignOptions{}},
		"big integer":    {"stripe.refund", `{"id":9007199254740993}`, SignOptions{}},
		"duplicate key":  {"stripe.refund", `{"a":1,"a":2}`, SignOptions{}},
		"bad obo":        {"stripe.refund", `{}`, SignOptions{OnBehalfOf: "Priya Raman"}},
		"bad nonce":      {"stripe.refund", `{}`, SignOptions{Nonce: "XYZ"}},
		"bad org":        {"stripe.refund", `{}`, SignOptions{}},
		"not JSON":       {"stripe.refund", `{`, SignOptions{}},
		"trailing value": {"stripe.refund", `{} 1`, SignOptions{}},
	} {
		signer := s
		if name == "bad org" {
			signer = &IntentSigner{key: s.key, kid: s.kid, org: "acme", agent: vAgent}
		}
		if _, _, err := signer.Sign(c.tool, json.RawMessage(c.args), c.o); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
	var nilSigner *IntentSigner
	if _, _, err := nilSigner.Sign("stripe.refund", json.RawMessage(`{}`), SignOptions{}); err == nil {
		t.Error("a nil signer signed")
	}
	if d, err := ArgsDigest(json.RawMessage(`{}`)); err != nil || d != "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a" {
		t.Errorf("%s %v", d, err)
	}
}

func TestVerifyIntentClient(t *testing.T) {
	var seen http.Header
	var body []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, body = r.Header.Clone(), nil
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Deedglass-Intent") == "replayed" {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"type":"permission_error","code":"intent_replay","message":"This signed intent was already used at 09:30:02 UTC. Sign a new intent, with a fresh nonce, for every call.","param":"nonce","request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/intent_replay"}}`)
			return
		}
		io.WriteString(w, `{"object":"intent_check","signed":true,"intent_hash":"sha256:87c7","kid":"21fe","agent":"`+vAgent+`","tool":"stripe.refund",`+
			`"args_digest":"sha256:f741","on_behalf_of":"usr_ext_4821","session":null,"expires_at":"2026-10-03T09:31:00.000Z","nonce_consumed":true,"intent_mode":"optional"}`)
	}))
	defer ts.Close()
	c := NewClient("dg_test_x", ts.URL)
	ic, err := c.VerifyIntent(context.Background(), "stripe.refund", json.RawMessage(`{"amount":1200}`), "hdr", WithOnBehalfOf("usr_ext_4821"), WithSession("conv_9f2c"))
	if err != nil || ic.Object != "intent_check" || !ic.Signed || *ic.OnBehalfOf != "usr_ext_4821" || ic.Session != nil || !ic.NonceConsumed {
		t.Fatalf("%v %+v", err, ic)
	}
	if seen.Get("Deedglass-Intent") != "hdr" || seen.Get("Deedglass-On-Behalf-Of") != "usr_ext_4821" || seen.Get("Deedglass-Session") != "conv_9f2c" ||
		string(body) != `{"tool":"stripe.refund","args":{"amount":1200}}` {
		t.Fatalf("%v %s", seen, body)
	}
	_, err = c.VerifyIntent(context.Background(), "stripe.refund", json.RawMessage(`{}`), "replayed")
	var e *Error
	if !errorsAs(err, &e) || e.Status != 403 || e.Code != "intent_replay" || e.Param != "nonce" {
		t.Fatalf("%v", err)
	}
}

func errorsAs(err error, e **Error) bool {
	x, ok := err.(*Error)
	if ok {
		*e = x
	}
	return ok
}
