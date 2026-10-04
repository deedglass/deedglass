package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

// Sentinels: neither may appear in any output, error, finding, report or log line.
const (
	botToken      = "xoxb-0000000000-dgSENTINELslackBOT7Q2vK9"
	signingSecret = "5e9d1c3b7a2f4e6d8c0b1a3f5e7d9c2b"
	actID         = "act_01j9zq7c4m8x2v6t0r3n5k1h7d"
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
	mu     sync.Mutex
	fields map[string]string
	err    error
	opens  atomic.Int64
}

func (o *opener) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	o.opens.Add(1)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return nil, o.err
	}
	return vault.NewSecretForTest(vault.KindFields, o.fields), nil
}

func (o *opener) set(fields map[string]string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.fields, o.err = fields, err
}

func goodFields() map[string]string {
	return map[string]string{FieldBotToken: botToken, FieldSigningSecret: signingSecret}
}

func devEnv() connector.Env {
	return connector.Env{Name: egress.EnvDev, Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
}

type rig struct {
	t    *testing.T
	srv  *saastest.Server
	reg  *connector.Registry
	op   *opener
	row  store.Connector
	logs *bytes.Buffer
}

// baseConfig is the example config; mutate edits it before validation.
func baseConfig(api string) map[string]any {
	return map[string]any{
		"team_id": "T0123ABCD", "app_id": "A0123ABCD",
		"channels": []any{"#agent-updates"}, "read_channels": []any{"C0AGENT01"}, "max_read_messages": 5,
		"approvals":    map[string]any{"enabled": true, "channel": "#deedglass-approvals", "require_email_domains": []any{"Example.COM"}},
		"api_base_url": api,
	}
}

func fixtures(t testing.TB, names ...string) []saastest.Exchange {
	t.Helper()
	var out []saastest.Exchange
	for _, n := range names {
		out = append(out, saastest.Load(t, filepath.Join("testdata", n+".json"))...)
	}
	return out
}

func newRig(t *testing.T, mutate func(map[string]any), xs ...saastest.Exchange) *rig {
	t.Helper()
	srv := saastest.New(t, xs...)
	cfg := baseConfig(srv.URL + "/api")
	if mutate != nil {
		mutate(cfg)
	}
	raw, _ := json.Marshal(cfg)
	norm, errs := New().Validate(context.Background(), devEnv(), raw)
	if len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	r := &rig{t: t, srv: srv, op: &opener{fields: goodFields()}, logs: &bytes.Buffer{}}
	r.row = store.Connector{ID: conID, OrgID: org.String(), Type: Type, Name: "slack", Config: norm, ConfigHash: "sha256:test", Status: "active"}
	r.reg = connector.New(connector.Options{Types: []connector.Connector{New()}, Loader: memLoader{r.row}, Opener: r.op, Env: devEnv(),
		Logger: slog.New(slog.NewTextHandler(r.logs, nil))})
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
	ex, spec, typ, err := r.reg.Resolve(context.Background(), org, "slack."+tool)
	if err != nil || typ != Type {
		r.t.Fatalf("resolve slack.%s: %v %s", tool, err, typ)
	}
	return ex, spec
}

func (r *rig) call(tool, args string) (connector.Result, error) {
	r.t.Helper()
	ex, spec := r.resolve(tool)
	return ex.Execute(context.Background(), r.callFor(spec, tool, args))
}

func (r *rig) callFor(spec connector.ToolSpec, tool, args string) connector.Call {
	return connector.Call{ActionID: ids.MustParse[ids.ActionKind](actID), OrgID: org, Tool: spec, Qualified: "slack." + tool,
		Args: json.RawMessage(args), Deadline: time.Now().Add(10 * time.Second)}
}

func (r *rig) readBack(tool, args string, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	r.t.Helper()
	ex, spec := r.resolve(tool)
	nr, ok := ex.(connector.NativeReadBacking)
	if !ok || !nr.CanNativeReadBack(spec) {
		r.t.Fatalf("slack.%s isn't read back natively", tool)
	}
	return nr.NativeReadBack(context.Background(), r.callFor(spec, tool, args), res)
}

func (r *rig) enrich(tool, args string) map[string]any {
	r.t.Helper()
	ex, spec := r.resolve(tool)
	m, err := ex.(connector.ArgsEnricher).EnrichArgs(context.Background(), spec, json.RawMessage(args))
	if err != nil {
		r.t.Fatalf("enrich: %v", err)
	}
	return m
}

// withClock moves the package clock for one test.
func withClock(t *testing.T, at *time.Time) {
	t.Helper()
	prev := now
	now = func() time.Time { return *at }
	t.Cleanup(func() { now = prev })
}

func ceOf(t *testing.T, err error) *connector.Error {
	t.Helper()
	ce := connector.AsError(err)
	if ce == nil {
		t.Fatalf("want a *connector.Error, got %v", err)
	}
	return ce
}
