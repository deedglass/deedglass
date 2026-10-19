package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/catalogdocs"
	"github.com/deedglass/deedglass/internal/httpx"
)

func TestStripeLiveKeyRefusedBeforeAnyCall(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	env := map[string]string{"STRIPE_KEY": "sk_live_0000000000000000", "DEEDGLASS_SERVER": srv.URL, "DEEDGLASS_API_KEY": "dg_test_abcdefgh_" + strings.Repeat("a", 32)}
	r := cli(t, env, false, "connector", "credentials", "set", "stripe", "--from-env", "STRIPE_KEY")
	if r.code != 1 {
		t.Fatalf("exit %d\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	want := "✕ Refused · stripe · this is a live-mode key (sk_live_…)\n  Deedglass connects to Stripe sandboxes only. Create a restricted key that starts with rk_test_ in your sandbox:\n  https://dashboard.stripe.com/test/apikeys\n"
	if r.stderr+r.stdout != want && !strings.Contains(r.stderr, "this is a live-mode key (sk_live_…)") {
		t.Errorf("got %q", r.stderr+r.stdout)
	}
	if strings.Contains(r.stdout+r.stderr, "0000000000000000") {
		t.Error("the key was echoed")
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests reached the server", hits.Load())
	}
	r = cli(t, env, false, "--json", "connector", "credentials", "set", "stripe", "--from-env", "STRIPE_KEY")
	if r.code != 1 || !strings.Contains(r.stdout+r.stderr, `"code":"stripe_live_key_refused"`) {
		t.Errorf("json: %d %s %s", r.code, r.stdout, r.stderr)
	}
}

func TestConnectorTypesCLI(t *testing.T) {
	infos := []connector.TypeInfo{}
	m := catalogdocs.Infos()
	for _, k := range catalogdocs.Sorted(m) {
		infos = append(infos, m[k])
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/v1/connector-types/") {
			_ = json.NewEncoder(w).Encode(m[strings.TrimPrefix(r.URL.Path, "/v1/connector-types/")])
			return
		}
		_ = json.NewEncoder(w).Encode(httpx.NewList(infos, false, nil))
	}))
	defer srv.Close()
	env := map[string]string{"DEEDGLASS_SERVER": srv.URL, "DEEDGLASS_API_KEY": "dg_test_abcdefgh_" + strings.Repeat("a", 32)}
	r := cli(t, env, false, "connector", "types")
	if r.code != 0 {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
	norm := map[string]bool{}
	for _, l := range strings.Split(r.stdout, "\n") {
		norm[strings.Join(strings.Fields(l), " ")] = true
	}
	for _, line := range []string{"stripe Stripe L0 L1 10 sandbox only", "gmail Gmail L0 L1 5 drafts only",
		"postgres PostgreSQL L0 L1 L2 built-in", "rest REST API L0 L1 from config", "mcp_http Remote MCP server L0 from server"} {
		if !norm[line] {
			t.Errorf("missing %q in\n%s", line, r.stdout)
		}
	}
	golden(t, "connector_types", r)
	r = cli(t, env, false, "connector", "types", "--json")
	var out []connector.TypeInfo
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || len(out) != 10 {
		t.Fatalf("--json: %v %d", err, len(out))
	}
	r = cli(t, env, false, "connector", "types", "--type", "stripe")
	if r.code != 0 || !strings.Contains(r.stdout, "stripe.create_refund") || !strings.Contains(r.stdout, "What it doesn't do") {
		t.Errorf("%s", r.stdout)
	}
	golden(t, "connector_types_stripe", r)
}

func TestStripeFixturesCLI(t *testing.T) {
	dir := t.TempDir()
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		id := map[string]string{"/v1/customers": "cus_Q2demoCust01", "/v1/products": "prod_1", "/v1/prices": "price_1", "/v1/subscriptions": "sub_1QdemoSub001"}[r.URL.Path]
		body := map[string]any{"id": id, "livemode": false}
		if r.URL.Path == "/v1/payment_intents" {
			body = map[string]any{"id": "pi_" + r.Header.Get("Idempotency-Key")[len(r.Header.Get("Idempotency-Key"))-1:], "latest_charge": "ch_1QdemoCharge" + r.Header.Get("Idempotency-Key")[len(r.Header.Get("Idempotency-Key"))-1:], "livemode": false}
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	out := filepath.Join(dir, "fx.json")
	env := map[string]string{"STRIPE_TEST_KEY": "rk_test_51dgSENTINELfixturesKEY00000000", "DEEDGLASS_DEV_STRIPE": srv.URL}
	r := cli(t, env, false, "dev", "stripe-fixtures", "--out", out)
	if r.code != 0 || !strings.Contains(r.stdout, "charge        ch_1QdemoCharge2 · $149.00") {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", err, fi)
	}
	if len(keys) != 7 || !strings.HasPrefix(keys[0], "fixtures-") || !strings.HasSuffix(keys[6], "-7") {
		t.Errorf("keys %v", keys)
	}
	if strings.Contains(r.stdout+r.stderr, "dgSENTINEL") {
		t.Error("the key was echoed")
	}
	env["STRIPE_TEST_KEY"] = "sk_live_0000000000000000"
	r = cli(t, env, false, "dev", "stripe-fixtures", "--out", out)
	if r.code != 1 || len(keys) != 7 {
		t.Errorf("live key: %d, %d requests", r.code, len(keys))
	}
}
