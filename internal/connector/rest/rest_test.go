package rest_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/rest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

const sentinel = "dgSENTINEL7Q2vK9xT4mB8nR3"

var (
	org   = ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvp")
	conID = "con_01j9zq7c4m8x2v6t0r3n5k1h7d"
)

type memLoader struct{ rows map[string]store.Connector }

func (m memLoader) Head(_ context.Context, _ ids.OrgID, name string) (store.ConnectorHead, error) {
	for _, r := range m.rows {
		if r.Name == name {
			return store.ConnectorHead{ID: r.ID, Type: r.Type, Status: r.Status, ConfigHash: r.ConfigHash}, nil
		}
	}
	return store.ConnectorHead{}, store.ErrNotFound
}

func (m memLoader) Load(_ context.Context, _ ids.OrgID, id string) (store.Connector, error) {
	r, ok := m.rows[id]
	if !ok {
		return store.Connector{}, store.ErrNotFound
	}
	return r, nil
}

type opener struct {
	kind   string
	fields map[string]string
	err    error
	opens  atomic.Int64
}

func (o *opener) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	o.opens.Add(1)
	if o.err != nil {
		return nil, o.err
	}
	return vault.NewSecretForTest(o.kind, o.fields), nil
}

func devEnv() connector.Env {
	return connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
}

const ordersTools = `"tools": [
  {"name": "get", "method": "GET", "path": "/v1/orders/{id}", "args": {"id": {"in": "path", "type": "string", "pattern": "^[0-9]{1,12}$", "required": true}}},
  {"name": "raw_get", "method": "GET", "path": "/v1/orders/{id}", "args": {"id": {"in": "path", "type": "string"}}},
  {"name": "update_status", "description": "Set an order's status.", "method": "PATCH", "path": "/v1/orders/{id}", "idempotency_header": "Idempotency-Key",
   "args": {"id": {"in": "path", "type": "string", "pattern": "^[0-9]{1,12}$", "required": true},
            "status": {"in": "body", "type": "string", "enum": ["pending", "shipped", "cancelled"], "required": true}},
   "readback": {"tool": "get", "args": {"id": "{{args.id}}"}, "expect": {"body.status": "{{args.status}}"}}, "postcondition": "observation.body.status == args.status"},
  {"name": "patch_plain", "method": "PATCH", "path": "/v1/orders/{id}", "args": {"id": {"in": "path", "type": "string"}}},
  {"name": "search", "method": "GET", "path": "/v1/orders", "args": {"q": {"in": "query", "type": "string"}, "src": {"in": "header", "header": "X-Source", "type": "string"},
   "card": {"in": "query", "type": "string", "sensitive": true}}}
]`

func cfgFor(base string, extra string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"base_url": %q, "auth": {"kind": "bearer"}, "allowed_methods": ["GET", "PATCH"],
	  "allowed_path_prefixes": ["/v1/orders"], "timeout_ms": 1000, "max_response_bytes": 4194304, "redirects": "same_host",
	  "test": {"method": "GET", "path": "/v1/health"}%s, %s}`, base, extra, ordersTools))
}

type rig struct {
	reg    *connector.Registry
	opener *opener
	hits   atomic.Int64
}

func newRig(t testing.TB, h http.HandlerFunc, extra string) (*rig, *httptest.Server) {
	t.Helper()
	r := &rig{opener: &opener{kind: "bearer", fields: map[string]string{"token": sentinel}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		h(w, req)
	}))
	t.Cleanup(srv.Close)
	c := rest.New()
	norm, errs := c.Validate(context.Background(), devEnv(), cfgFor(srv.URL, extra))
	if len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	hash, _ := rest.Hash(norm)
	row := store.Connector{ID: conID, OrgID: org.String(), Type: "rest", Name: "orders", Config: norm, ConfigHash: hash, Status: "active"}
	r.reg = connector.New(connector.Options{Types: []connector.Connector{c}, Loader: memLoader{map[string]store.Connector{conID: row}},
		Opener: r.opener, Env: devEnv()})
	return r, srv
}

func (r *rig) call(t *testing.T, tool, args string) (connector.Result, error) {
	t.Helper()
	ex, spec, ctype, err := r.reg.Resolve(context.Background(), org, "orders."+tool)
	if err != nil || ctype != "rest" {
		t.Fatalf("resolve orders.%s: %v %s", tool, err, ctype)
	}
	act := ids.New[ids.ActionKind]()
	return ex.Execute(context.Background(), connector.Call{ActionID: act, OrgID: org, Tool: spec, Qualified: "orders." + tool,
		Args: json.RawMessage(args), Deadline: time.Now().Add(10 * time.Second)})
}

func TestGetPassesThrough(t *testing.T) {
	var auth, actionID string
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		auth, actionID = req.Header.Get("Authorization"), req.Header.Get("Deedglass-Action-Id")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=abc")
		w.Header().Set("ETag", `"v1"`)
		_, _ = fmt.Fprintf(w, `{"id": 42, "status": "pending", "path": %q}`, req.URL.Path)
	}, "")
	res, err := r.call(t, "get", `{"id":"42"}`)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer "+sentinel || !strings.HasPrefix(actionID, "act_") {
		t.Fatalf("upstream saw auth %q, action %q", auth, actionID)
	}
	var out struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    map[string]any    `json:"body"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil || out.Status != 200 || out.Body["status"] != "pending" || out.Body["path"] != "/v1/orders/42" {
		t.Fatalf("output %s", res.Output)
	}
	if _, ok := out.Headers["set-cookie"]; ok || out.Headers["etag"] != `"v1"` {
		t.Fatalf("headers %v", out.Headers)
	}
	if res.Meta["http_status"] != 200 || res.Meta["path"] != "/v1/orders/{id}" || res.Meta["attempts"] != 1 {
		t.Fatalf("meta %v", res.Meta)
	}
}

func TestPathTraversalRefused(t *testing.T) {
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {}, "")
	for _, id := range []string{"../admin", "42/../../x", "%2e%2e", "..", ".", "a b", "42?x=1", strings.Repeat("9", 257)} {
		for _, tool := range []string{"get", "raw_get"} {
			args, _ := json.Marshal(map[string]string{"id": id})
			_, err := r.call(t, tool, string(args))
			ce := connector.AsError(err)
			if ce == nil || ce.Code != "validation_failed" {
				t.Errorf("%s %q: %v", tool, id, err)
			}
		}
	}
	if r.hits.Load() != 0 {
		t.Fatalf("%d requests reached the upstream", r.hits.Load())
	}
}

func TestAuthFailureRedacted(t *testing.T) {
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_, _ = fmt.Fprintf(w, `{"error": "bad token %s", "echo": %q}`, sentinel, req.Header.Get("Authorization"))
	}, "")
	res, err := r.call(t, "get", `{"id":"42"}`)
	ce := connector.AsError(err)
	if ce == nil || ce.Code != "upstream_auth_failed" {
		t.Fatalf("err %v", err)
	}
	if bytes.Contains(res.Output, []byte(sentinel)) || !bytes.Contains(res.Output, []byte(vault.RedactedText)) {
		t.Fatalf("output %s", res.Output)
	}
	if strings.Contains(ce.AgentMessage+ce.AdminDetail, sentinel) {
		t.Fatal("the message holds the token")
	}
}

func TestRetries(t *testing.T) {
	var n atomic.Int64
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}, "")
	if res, err := r.call(t, "get", `{"id":"42"}`); err != nil || res.Meta["attempts"] != 2 {
		t.Fatalf("503 then 200: %v %v", err, res.Meta)
	}
	// A PATCH without an idempotency header: one attempt.
	var m atomic.Int64
	r2, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) { m.Add(1); w.WriteHeader(503) }, "")
	_, err := r2.call(t, "patch_plain", `{"id":"42"}`)
	if ce := connector.AsError(err); ce == nil || ce.Code != "upstream_error" || m.Load() != 1 {
		t.Fatalf("PATCH 503: %v after %d attempts", err, m.Load())
	}
	// A PATCH with the idempotency header retries, with the same key.
	var keys []string
	var k atomic.Int64
	r3, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		keys = append(keys, req.Header.Get("Idempotency-Key"))
		if k.Add(1) == 1 {
			w.WriteHeader(502)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}, "")
	if _, err := r3.call(t, "update_status", `{"id":"42","status":"shipped"}`); err != nil || len(keys) != 2 || keys[0] != keys[1] || !strings.HasPrefix(keys[0], "act_") {
		t.Fatalf("idempotent PATCH: %v %v", err, keys)
	}
}

func TestRetryAfter(t *testing.T) {
	var n atomic.Int64
	var first time.Time
	var gap time.Duration
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		if n.Add(1) == 1 {
			first = time.Now()
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		gap = time.Since(first)
		_, _ = w.Write([]byte(`{}`))
	}, "")
	if _, err := r.call(t, "get", `{"id":"42"}`); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 2 || gap < 900*time.Millisecond || gap > 3*time.Second {
		t.Fatalf("%d attempts, gap %s", n.Load(), gap)
	}
}

func TestResponseCaps(t *testing.T) {
	var bomb bytes.Buffer
	gz := gzip.NewWriter(&bomb)
	z := make([]byte, 1<<20)
	for range 100 {
		_, _ = gz.Write(z)
	}
	_ = gz.Close()
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/orders/1" {
			_, _ = w.Write(bytes.Repeat([]byte("a"), 5<<20))
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(bomb.Bytes())
	}, "")
	for _, id := range []string{"1", "2"} {
		_, err := r.call(t, "get", `{"id":"`+id+`"}`)
		if ce := connector.AsError(err); ce == nil || ce.Code != "response_too_large" || ce.Kind != connector.KindTooLarge {
			t.Errorf("id %s: %v", id, err)
		}
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Set-Cookie", "a=b")
		select {
		case <-release:
		case <-time.After(20 * time.Second):
		}
	}, "")
	defer close(release)
	t0 := time.Now()
	res, err := r.call(t, "patch_plain", `{"id":"42"}`)
	if ce := connector.AsError(err); ce == nil || ce.Code != "timeout" || ce.Kind != connector.KindTimeout {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(t0); d < 900*time.Millisecond || d > 3*time.Second {
		t.Fatalf("timed out after %s", d)
	}
	if bytes.Contains(res.Output, []byte("a=b")) {
		t.Fatal("Set-Cookie in the output")
	}
}

func TestQueryHeaderAndSensitive(t *testing.T) {
	var got string
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		got = req.URL.RawQuery + " " + req.Header.Get("X-Source")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintf(w, "you sent card 4242424242424242 and %s", req.URL.RawQuery)
	}, "")
	res, err := r.call(t, "search", `{"q":"late orders","src":"agent","card":"4242424242424242"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "q=late+orders") || !strings.HasSuffix(got, " agent") {
		t.Fatalf("upstream saw %q", got)
	}
	if bytes.Contains(res.Output, []byte("4242424242424242")) {
		t.Fatalf("a sensitive arg came back: %s", res.Output)
	}
}

func TestDestinationBlockedAtExecute(t *testing.T) {
	// A config validated in dev, executed by a prod egress policy: blocked at dial, nothing sent.
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {}, "")
	reg := connector.New(connector.Options{Types: []connector.Connector{rest.New()}, Loader: memLoaderFrom(t, r), Opener: r.opener,
		Env: connector.Env{Name: "prod", Egress: &egress.Policy{Env: egress.EnvProd}}})
	ex, spec, _, err := reg.Resolve(context.Background(), org, "orders.get")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ex.Execute(context.Background(), connector.Call{Tool: spec, Args: json.RawMessage(`{"id":"1"}`), Deadline: time.Now().Add(5 * time.Second)})
	ce := connector.AsError(err)
	if ce == nil || ce.Code != "destination_blocked" || strings.Contains(ce.AgentMessage, "127.0.0.1") || !strings.Contains(ce.AdminDetail, "127.0.0.1") {
		t.Fatalf("err %v (%+v)", err, ce)
	}
	if r.hits.Load() != 0 {
		t.Fatal("the upstream was reached")
	}
}

// memLoaderFrom is a loader with the rig's connector row, for a second registry.
func memLoaderFrom(t *testing.T, r *rig) connector.Loader {
	ex, _, _, err := r.reg.Resolve(context.Background(), org, "orders.get")
	if err != nil {
		t.Fatal(err)
	}
	inst := ex.(interface{ Instance() *connector.Instance }).Instance()
	return memLoader{map[string]store.Connector{conID: {ID: conID, OrgID: org.String(), Type: "rest", Name: "orders", Config: inst.Config,
		ConfigHash: inst.ConfigHash, Status: "active"}}}
}

func TestCredentialsMissingAndVault(t *testing.T) {
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {}, "")
	r.opener.err = vault.ErrNotFound
	_, err := r.call(t, "get", `{"id":"42"}`)
	if ce := connector.AsError(err); ce == nil || ce.Code != "credentials_missing" ||
		ce.AgentMessage != `The connector "orders" has no credentials yet. Run deedglass connector credentials set orders --from-env ORDERS_TOKEN.` {
		t.Fatalf("missing: %v", err)
	}
	r.opener.err = vault.ErrIntegrity
	_, err = r.call(t, "get", `{"id":"42"}`)
	if ce := connector.AsError(err); ce == nil || ce.Code != "vault_unavailable" || ce.AgentMessage != connector.VaultAgentMessage {
		t.Fatalf("vault: %v", err)
	}
	if r.hits.Load() != 0 {
		t.Fatal("a call was sent without auth")
	}
}

func TestEnrich(t *testing.T) {
	r, srv := newRig(t, func(w http.ResponseWriter, req *http.Request) {}, "")
	ex, spec, _, _ := r.reg.Resolve(context.Background(), org, "orders.update_status")
	m, err := ex.(connector.ArgsEnricher).EnrichArgs(context.Background(), spec, json.RawMessage(`{"id":"42","status":"shipped"}`))
	if err != nil {
		t.Fatal(err)
	}
	h := m["http"].(map[string]string)
	if h["method"] != "PATCH" || h["path"] != "/v1/orders/42" || h["host"] != strings.TrimPrefix(srv.URL, "http://") {
		t.Fatalf("http %v", h)
	}
	if spec.SideEffect != connector.SideEffectWrite || !spec.Idempotent || spec.ReadBack == nil || spec.Postcondition == "" {
		t.Fatalf("spec %+v", spec)
	}
}

func TestConfigRefusals(t *testing.T) {
	base := func(tools string) json.RawMessage {
		return json.RawMessage(`{"base_url": "http://127.0.0.1:8090", "auth": {"kind": "bearer"}, "allowed_methods": ["GET", "PATCH", "DELETE"],
		  "allowed_path_prefixes": ["/v1/orders"], "tools": [` + tools + `]}`)
	}
	cases := map[string]struct {
		cfg   json.RawMessage
		param string
		msg   string
	}{
		"delete declared read": {base(`{"name":"a","method":"GET","path":"/v1/orders"},{"name":"b","method":"GET","path":"/v1/orders"},{"name":"rm","method":"DELETE","path":"/v1/orders/{id}","side_effect":"read","args":{"id":{"in":"path"}}}`),
			"config.tools[2].side_effect", `tools[2] is a DELETE, so its side effect can't be "read". Use "destructive", or leave side_effect out.`},
		"authorization header arg": {base(`{"name":"a","method":"GET","path":"/v1/orders","args":{"auth":{"in":"header","header":"Authorization"}}}`),
			"config.tools[0].args.auth.header", ""},
		"prefix boundary":         {base(`{"name":"a","method":"GET","path":"/v1/orders-admin"}`), "config.tools[0].path", ""},
		"method not allowed":      {base(`{"name":"a","method":"POST","path":"/v1/orders"}`), "config.tools[0].method", ""},
		"placeholder without arg": {base(`{"name":"a","method":"GET","path":"/v1/orders/{id}"}`), "config.tools[0].path", ""},
		"metadata base": {json.RawMessage(`{"base_url": "http://169.254.169.254", "allowed_methods": ["GET"], "allowed_path_prefixes": ["/"], "tools": [{"name":"a","method":"GET","path":"/x"}]}`),
			"config.base_url", "Deedglass never connects to cloud metadata addresses"},
		"forbidden default header": {json.RawMessage(`{"base_url": "http://127.0.0.1:8090", "allowed_methods": ["GET"], "allowed_path_prefixes": ["/"], "headers": {"Cookie": "x"}, "tools": [{"name":"a","method":"GET","path":"/x"}]}`),
			"config.headers.Cookie", ""},
		"unknown key": {json.RawMessage(`{"base_url": "http://127.0.0.1:8090", "verify_tls": false}`), "config", ""},
		"write test request": {json.RawMessage(`{"base_url": "http://127.0.0.1:8090", "allowed_methods": ["GET"], "allowed_path_prefixes": ["/"], "test": {"method": "POST", "path": "/x"}, "tools": [{"name":"a","method":"GET","path":"/x"}]}`),
			"config.test.method", ""},
	}
	c := rest.New()
	for name, tc := range cases {
		_, errs := c.Validate(context.Background(), devEnv(), tc.cfg)
		found := false
		for _, e := range errs {
			if e.Param == tc.param && strings.Contains(e.Message, tc.msg) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: %v", name, errs)
		}
	}
	// The dev override never reaches metadata, and prod refuses http and loopback.
	prod := connector.Env{Name: "prod", Egress: &egress.Policy{Env: egress.EnvProd}}
	if _, errs := c.Validate(context.Background(), prod, base(`{"name":"a","method":"GET","path":"/v1/orders"}`)); len(errs) == 0 {
		t.Error("prod accepted http://127.0.0.1")
	}
}

func TestConfigNormalizes(t *testing.T) {
	c := rest.New()
	a, errs := c.Validate(context.Background(), devEnv(), json.RawMessage(`{"base_url": "HTTP://LOCALHOST:8090/", "allowed_methods": ["get"], "allowed_path_prefixes": ["/v1/orders/"],
	  "tools": [{"name":"get","method":"get","path":"/v1/orders/{id}","side_effect":"read","args":{"id":{"in":"path"}}}]}`))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	b, _ := c.Validate(context.Background(), devEnv(), a)
	if !bytes.Equal(a, b) {
		t.Fatalf("not idempotent:\n%s\n%s", a, b)
	}
	if !strings.Contains(string(a), `"base_url":"http://localhost:8090"`) || !strings.Contains(string(a), `"allowed_path_prefixes":["/v1/orders"]`) ||
		strings.Contains(string(a), `"side_effect"`) || !strings.Contains(string(a), `"send_action_id":true`) {
		t.Fatalf("normalized %s", a)
	}
	h1, _ := rest.Hash(a)
	h2, _ := rest.Hash(b)
	if h1 != h2 || !strings.HasPrefix(h1, "sha256:") {
		t.Fatal(h1, h2)
	}
}

func TestTestChecks(t *testing.T) {
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v1/health" && req.Header.Get("Authorization") == "Bearer "+sentinel {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(401)
	}, "")
	ex, _, _, _ := r.reg.Resolve(context.Background(), org, "orders.get")
	inst := ex.(interface{ Instance() *connector.Instance }).Instance()
	rep, err := rest.New().Test(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, c := range rep.Checks {
		ids = append(ids, c.ID+":"+string(c.Status))
	}
	want := "config:pass credentials:pass destination:pass tls:skip auth:pass"
	if strings.Join(ids, " ") != want || rep.Status != connector.CheckPass {
		t.Fatalf("checks %v (%s)", ids, rep.Status)
	}
	r.opener.fields = map[string]string{"token": "wrong-token-value"}
	rep, _ = rest.New().Test(context.Background(), inst)
	if rep.Status != connector.CheckFail || rep.Checks[4].Status != connector.CheckFail || !strings.Contains(rep.Checks[4].Detail, "→ 401") {
		t.Fatalf("wrong token: %+v", rep.Checks)
	}
	r.opener.err = vault.ErrNotFound
	rep, _ = rest.New().Test(context.Background(), inst)
	if rep.Checks[1].Status != connector.CheckFail || !strings.Contains(rep.Checks[1].Fix, "--from-env ORDERS_TOKEN") {
		t.Fatalf("missing: %+v", rep.Checks[1])
	}
	for _, c := range rep.Checks {
		if strings.Contains(c.Detail+c.Fix, sentinel) {
			t.Fatal("a check shows the token")
		}
	}
}

func TestPanicRecovered(t *testing.T) {
	r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {}, "")
	reg := connector.New(connector.Options{Types: []connector.Connector{panicky{rest.New()}}, Loader: memLoaderFrom(t, r), Opener: r.opener, Env: devEnv()})
	ex, spec, _, err := reg.Resolve(context.Background(), org, "orders.get")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ex.Execute(context.Background(), connector.Call{Tool: spec, Args: json.RawMessage(`{"id":"1"}`)})
	if ce := connector.AsError(err); ce == nil || ce.Code != "upstream_error" {
		t.Fatalf("panic: %v", err)
	}
}

type panicky struct{ *rest.Connector }

func (panicky) Execute(context.Context, *connector.Instance, connector.Call) (connector.Result, error) {
	panic("boom " + sentinel)
}

// BenchmarkExecuteLoopback is one governed REST call end to end through the registry (schema
// check, credential, egress, an httptest server on loopback, redaction): an upper bound on the
// connector's overhead, since it includes the loopback round trip.
func BenchmarkExecuteLoopback(b *testing.B) {
	r, _ := newRig(b, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"status":"pending"}`))
	}, "")
	ex, spec, _, err := r.reg.Resolve(context.Background(), org, "orders.get")
	if err != nil {
		b.Fatal(err)
	}
	call := connector.Call{Tool: spec, Args: json.RawMessage(`{"id":"42"}`)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ex.Execute(context.Background(), call); err != nil {
			b.Fatal(err)
		}
	}
}

// TestRedirectRefusals (S18, attack K02): a read tool's redirect to metadata or to another host
// is never followed; both fail as destination_blocked (nothing was sent there), not as an unknown.
func TestRedirectRefusals(t *testing.T) {
	var other atomic.Int64
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { other.Add(1) }))
	defer elsewhere.Close()
	for _, to := range []string{"http://169.254.169.254/latest/meta-data/iam/", elsewhere.URL + "/v1/orders/1"} {
		r, _ := newRig(t, func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, to, http.StatusFound)
		}, "")
		_, err := r.call(t, "get", `{"id":"42"}`)
		ce := connector.AsError(err)
		if ce == nil || ce.Code != connector.CodeDestinationBlocked || ce.Kind != connector.KindDestinationBlocked {
			t.Fatalf("redirect to %s: %v", to, err)
		}
		if strings.Contains(ce.AgentMessage, "169.254") || strings.Contains(ce.AgentMessage, "127.0.0.1") {
			t.Fatalf("the agent message names the address: %q", ce.AgentMessage)
		}
	}
	if other.Load() != 0 {
		t.Fatalf("the other host was hit %d times", other.Load())
	}
}
