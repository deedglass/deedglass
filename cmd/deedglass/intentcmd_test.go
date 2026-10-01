package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeIntentAPI answers the S09 routes the intent commands call.
func fakeIntentAPI(t *testing.T) *httptest.Server {
	t.Helper()
	agent := func(mode string) string {
		return `{"object":"agent","id":"agt_01k633rrmh3bcz2p39gg4vp46q","name":"support-bot","description":"","labels":{},"budgets":{},"status":"active",` +
			`"status_reason":null,"status_changed_at":null,"status_changed_by":null,"owner":null,"active_keys":1,"intent_mode":"` + mode + `",` +
			`"last_used_at":null,"created_at":"2026-10-03T10:02:11.000Z","updated_at":"2026-10-03T10:02:11.000Z"`
	}
	added := time.Now().Add(-2*time.Hour - time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	key := `{"object":"intent_key","kid":"21fe31dfa154a261626bf854046fd227","agent":"agt_01k633rrmh3bcz2p39gg4vp46q",` +
		`"public_key":"ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo","created_at":"` + added + `","created_by":"usr_01k6h7mzq3r5t8v2w4x6y8z0ab"`
	h := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/agents":
			io.WriteString(w, `{"object":"list","data":[`+agent("off")+`}],"has_more":false,"next_cursor":null}`)
		case "POST /v1/agents/agt_01k633rrmh3bcz2p39gg4vp46q/intent-keys":
			if strings.Contains(string(body), "AQAAAAAA") {
				w.WriteHeader(422)
				io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"validation_failed","message":"That public key is a small-order point, so a single signature would verify for every message. Generate a fresh key with deedglass agent intent keygen support-bot.","param":"public_key","request_id":"req_1","doc_url":"https://docs.deedglass.com/errors/validation_failed"}}`)
				return
			}
			w.WriteHeader(201)
			io.WriteString(w, key+`,"deed":"deed_01k6vrddq2atgb2023qvhhct3m"}`)
		case "GET /v1/agents/agt_01k633rrmh3bcz2p39gg4vp46q/intent-keys":
			io.WriteString(w, `{"object":"list","data":[`+key+`}],"has_more":false,"next_cursor":null}`)
		case "DELETE /v1/agents/agt_01k633rrmh3bcz2p39gg4vp46q/intent-keys/21fe31dfa154a261626bf854046fd227":
			io.WriteString(w, `{"object":"intent_key","kid":"21fe31dfa154a261626bf854046fd227","agent":"agt_01k633rrmh3bcz2p39gg4vp46q","deleted":true,"deed":"deed_01k6vrddq2atgb2023qvhhct3n"}`)
		case "PATCH /v1/agents/agt_01k633rrmh3bcz2p39gg4vp46q":
			if strings.Contains(string(body), "required") {
				io.WriteString(w, agent("required")+`,"deed":"deed_01k6vrddq2atgb2023qvhhct3p"}`)
				return
			}
			io.WriteString(w, agent("optional")+`,"deed":"deed_01k6vrddq2atgb2023qvhhct3q"}`)
		case "GET /v1/whoami":
			io.WriteString(w, `{"object":"whoami","org":{"id":"org_01k6h7kp6msmzrhdzwtcr3jnkp","name":"Acme","slug":"acme","stopped":false},`+
				`"agent":{"id":"agt_01k633rrmh3bcz2p39gg4vp46q","name":"support-bot","status":"active","labels":{}},`+
				`"key":{"id":"key_01k6jz1","prefix":"dg_live_agent000","mode":"live","scopes":["actions:execute"],"expires_at":null},"budgets":{}}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"not_found","message":"There's no endpoint here.","param":null,"request_id":"req_3","doc_url":"https://docs.deedglass.com/errors/not_found"}}`)
		}
	}
	s := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(s.Close)
	return s
}

// test1Key is the RFC 8032 TEST 1 key (public; tests only) as keygen would write it.
func test1Key() ([]byte, string, string, error) {
	seed, _ := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	priv := ed25519.NewKeyFromSeed(seed)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, "", "", err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), "ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo", "21fe31dfa154a261626bf854046fd227", nil
}

func TestIntentCommandGoldens(t *testing.T) {
	api := fakeIntentAPI(t)
	saved := newIntentKey
	newIntentKey = test1Key
	t.Cleanup(func() { newIntentKey = saved })
	tmp := t.TempDir()
	env := map[string]string{"DEEDGLASS_SERVER": api.URL, "DEEDGLASS_API_KEY": "adm", "NO_COLOR": "1"}
	noKey := map[string]string{"NO_COLOR": "1"}
	pemPath := "../../testdata/intents/keys/test1.pem"
	vector := []string{"dev", "sign-intent", "--key", pemPath, "--org", "org_01k6h7kp6msmzrhdzwtcr3jnkp", "--agent", "agt_01k633rrmh3bcz2p39gg4vp46q",
		"--tool", "stripe.refund", "--args", `{"charge":"ch_3QxZ8f2L","amount":1200}`, "--obo", "usr_ext_4821", "--session", "conv_9f2c",
		"--now", "2026-10-03T09:30:00Z", "--nonce", "05c18be68a24c056cd6c7c8910dc0729"}
	cases := []struct {
		name string
		env  map[string]string
		args []string
		code int
	}{
		{"intent/keygen", noKey, []string{"agent", "intent", "keygen", "support-bot", "--dir", filepath.Join(tmp, "intent")}, 0},
		{"intent/keygen_exists", noKey, []string{"agent", "intent", "keygen", "support-bot", "--dir", filepath.Join(tmp, "intent")}, 1},
		{"intent/keygen_bad_name", noKey, []string{"agent", "intent", "keygen", "Support Bot"}, 2},
		{"intent/add", env, []string{"agent", "intent", "add", "support-bot", "--public-key", "ed25519:11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"}, 0},
		{"intent/add_small_order", env, []string{"agent", "intent", "add", "support-bot", "--public-key", "ed25519:AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}, 1},
		{"intent/add_no_key", env, []string{"agent", "intent", "add", "support-bot"}, 2},
		{"intent/list", env, []string{"agent", "intent", "list", "support-bot"}, 0},
		{"intent/remove", env, []string{"agent", "intent", "remove", "support-bot", "21fe31dfa154a261626bf854046fd227"}, 0},
		{"intent/mode_required", env, []string{"agent", "intent", "mode", "support-bot", "required"}, 0},
		{"intent/mode_optional", env, []string{"agent", "intent", "mode", "support-bot", "optional"}, 0},
		{"intent/mode_bad", env, []string{"agent", "intent", "mode", "support-bot", "strict"}, 2},
		{"intent/sign_vector", noKey, vector, 0},
		{"intent/sign_explain", noKey, append(append([]string{}, vector...), "--explain"), 0},
		{"intent/sign_json", noKey, append([]string{"--json"}, vector...), 0},
		{"intent/sign_whoami", env, []string{"dev", "sign-intent", "--key", pemPath, "--tool", "stripe.refund", "--args", `{"amount":1200,"charge":"ch_3QxZ8f2L"}`,
			"--now", "2026-10-03T09:30:00Z", "--nonce", "05c18be68a24c056cd6c7c8910dc0729"}, 0},
		{"intent/sign_no_identity", noKey, []string{"dev", "sign-intent", "--key", pemPath, "--tool", "stripe.refund", "--args", `{}`}, 2},
		{"intent/sign_big_int", noKey, []string{"dev", "sign-intent", "--key", pemPath, "--org", "org_01k6h7kp6msmzrhdzwtcr3jnkp",
			"--agent", "agt_01k633rrmh3bcz2p39gg4vp46q", "--tool", "stripe.refund", "--args", `{"id":9007199254740993}`}, 1},
		{"intent/sign_bad_nonce", noKey, []string{"dev", "sign-intent", "--key", pemPath, "--tool", "stripe.refund", "--args", `{}`, "--nonce", "xyz"}, 2},
	}
	for _, c := range cases {
		r := cli(t, c.env, false, c.args...)
		if r.code != c.code {
			t.Errorf("%s: exit %d, want %d\n%s%s", c.name, r.code, c.code, r.stdout, r.stderr)
		}
		r.stdout = strings.ReplaceAll(r.stdout, tmp, "$TMP")
		r.stderr = strings.ReplaceAll(r.stderr, tmp, "$TMP")
		golden(t, c.name, r)
	}
	// The private key file is 0600 and the cross-check vector's header is exact.
	fi, err := os.Stat(filepath.Join(tmp, "intent", "support-bot.pem"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", err, fi.Mode())
	}
	if r := cli(t, noKey, false, vector...); !strings.HasSuffix(strings.TrimSpace(r.stdout), ".TQAgBevx6iwlDM2Z7px4mrRiCjdbrAX3GCacB_gMAQbvPagJqCsJUWYz7xS7VuQqyV2rOK71uyRHvym2nqjJCA") {
		t.Fatalf("%s", r.stdout)
	}
}
