package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// Sentinels: no key may appear in any output, error, finding, report or log line.
const (
	testKey  = "rk_test_51dgSENTINELstripeKEY0000000000009f2c"
	liveKey  = "sk_live_51dgSENTINELliveKEY000000000000aaaa"
	secretSK = "sk_test_51dgSENTINELsecretKEY00000000000bbbb"
	actID    = "act_01j9zq7c4m8x2v6t0r3n5k1h7d"
	chargeID = "ch_3QxZ8f2LdemoA1"
	custID   = "cus_Q2demoCust01"
	subID    = "sub_1QdemoSub001"
	refundID = "re_3QxZ9aDemo01"
)

var (
	org   = ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvp")
	conID = "con_01j9zq7c4m8x2v6t0r3n5k1h7d"
)

type memLoader struct{ row store.Connector }

func (m memLoader) Head(_ context.Context, _ ids.OrgID, name string) (store.ConnectorHead, error) {
	if name != m.row.Name {
		return store.ConnectorHead{}, store.ErrNotFound
	}
	return store.ConnectorHead{ID: m.row.ID, Type: m.row.Type, Status: m.row.Status, ConfigHash: m.row.ConfigHash}, nil
}

func (m memLoader) Load(_ context.Context, _ ids.OrgID, id string) (store.Connector, error) {
	if id != m.row.ID {
		return store.Connector{}, store.ErrNotFound
	}
	return m.row, nil
}

type opener struct {
	mu  sync.Mutex
	key string
	err error
}

func (o *opener) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return nil, o.err
	}
	return vault.NewSecretForTest(vault.KindBearer, map[string]string{FieldToken: o.key}), nil
}

type breaker struct {
	trips   atomic.Int64
	reasons []string
	details []string
	mu      sync.Mutex
}

func (b *breaker) Trip(_ context.Context, _ *connector.Instance, reason, detail string) error {
	b.trips.Add(1)
	b.mu.Lock()
	b.reasons = append(b.reasons, reason)
	b.details = append(b.details, detail)
	b.mu.Unlock()
	return nil
}

type rig struct {
	t    *testing.T
	srv  *saastest.Server
	reg  *connector.Registry
	op   *opener
	br   *breaker
	row  store.Connector
	logs *bytes.Buffer
}

func newRig(t *testing.T, mutate func(map[string]any), xs ...saastest.Exchange) *rig {
	t.Helper()
	srv := saastest.New(t, xs...)
	cfg := map[string]any{"api_base_url": srv.URL}
	if mutate != nil {
		mutate(cfg)
	}
	raw, _ := json.Marshal(cfg)
	br := &breaker{}
	env := connector.Env{Name: egress.EnvDev, Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}, Breaker: br}
	norm, errs := New().Validate(context.Background(), env, raw)
	if len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	r := &rig{t: t, srv: srv, op: &opener{key: testKey}, br: br, logs: &bytes.Buffer{}}
	r.row = store.Connector{ID: conID, OrgID: org.String(), Type: Type, Name: "stripe", Config: norm, ConfigHash: "sha256:test", Status: "active"}
	r.reg = connector.New(connector.Options{Types: []connector.Connector{New()}, Loader: memLoader{r.row}, Opener: r.op, Env: env,
		Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
	old := logger
	logger = func() *slog.Logger { return slog.New(slog.NewTextHandler(r.logs, nil)) }
	t.Cleanup(func() { logger = old })
	return r
}

func (r *rig) inst() *connector.Instance {
	inst, err := r.reg.Instance(org, r.row)
	if err != nil {
		r.t.Fatal(err)
	}
	return inst
}

func (r *rig) resolve(tool string) (connector.Executor, connector.ToolSpec) {
	r.t.Helper()
	ex, spec, typ, err := r.reg.Resolve(context.Background(), org, "stripe."+tool)
	if err != nil || typ != Type {
		r.t.Fatalf("resolve stripe.%s: %v %s", tool, err, typ)
	}
	return ex, spec
}

func (r *rig) call(tool, args string) (connector.Result, error, connector.Call) {
	r.t.Helper()
	ex, spec := r.resolve(tool)
	c := connector.Call{ActionID: ids.MustParse[ids.ActionKind](actID), OrgID: org, Tool: spec, Qualified: "stripe." + tool,
		Args: json.RawMessage(args), IdempotencyKey: actID}
	res, err := ex.Execute(context.Background(), c)
	return res, err, c
}

func (r *rig) enrich(tool, args string) Facts {
	r.t.Helper()
	ex, spec := r.resolve(tool)
	m, err := ex.(connector.ArgsEnricher).EnrichArgs(context.Background(), spec, json.RawMessage(args))
	if err != nil {
		r.t.Fatal(err)
	}
	var f Facts
	b, _ := json.Marshal(m["stripe"])
	_ = json.Unmarshal(b, &f)
	return f
}

func (r *rig) readBack(c connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	ex, _ := r.resolve(c.Tool.Name)
	return ex.(connector.NativeReadBacking).NativeReadBack(context.Background(), c, res)
}

// sweep fails when a key appears anywhere the test can see.
func (r *rig) sweep(extra ...string) {
	r.t.Helper()
	hay := r.logs.String()
	for _, x := range extra {
		hay += x
	}
	for _, s := range []string{testKey, liveKey, secretSK, "dgSENTINEL"} {
		if bytes.Contains([]byte(hay), []byte(s)) {
			r.t.Errorf("sentinel %q leaked", s[:12])
		}
	}
}

// Fixture builders.

func ex(name, method, path string, status int, body string) saastest.Exchange {
	return saastest.Exchange{Name: name, Method: method, Path: path, Status: status, Body: json.RawMessage(body),
		Headers: map[string]string{"Request-Id": "req_demo" + name[:min(len(name), 8)], "Stripe-Version": APIVersion}}
}

func chargeBody(amount, refunded int64, cur string, disputed bool) string {
	b, _ := json.Marshal(map[string]any{"id": chargeID, "object": "charge", "amount": amount, "amount_refunded": refunded, "currency": cur,
		"customer": custID, "payment_intent": "pi_3QxZ8f2LdemoPI", "status": "succeeded", "disputed": disputed, "livemode": false,
		"created": now().Add(-48 * 3600e9).Unix()})
	return string(b)
}

func refundBody(amount int64, cur, status, action string) string {
	b, _ := json.Marshal(map[string]any{"id": refundID, "object": "refund", "amount": amount, "currency": cur, "status": status,
		"charge": chargeID, "payment_intent": "pi_3QxZ8f2LdemoPI", "metadata": map[string]string{"deedglass_action_id": action}, "livemode": false})
	return string(b)
}

var _ = http.StatusOK

var errUnusedVault = vaultErr("vault: not found")

type vaultErr string

func (e vaultErr) Error() string { return string(e) }
