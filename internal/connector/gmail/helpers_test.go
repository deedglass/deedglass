package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

const (
	refreshToken = "1//0dgSENTINELgmailREFRESH9x"
	clientSecret = "GOCSPX-dgSENTINELclientSECRET"
	accessTok    = "ya29.dgSENTINELaccessTOKEN"
	actID        = "act_01j9zq7c4m8x2v6t0r3n5k1h7d"
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
}

func (o *opener) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return vault.NewSecretForTest(vault.KindFields, o.fields), nil
}

type rig struct {
	t    *testing.T
	srv  *saastest.Server
	reg  *connector.Registry
	op   *opener
	row  store.Connector
	logs *bytes.Buffer
}

var hashN int

func newRig(t *testing.T, mutate func(map[string]any), xs ...saastest.Exchange) *rig {
	t.Helper()
	srv := saastest.New(t, xs...)
	cfg := map[string]any{"internal_domains": []any{"acme.com"}, "api_base_url": srv.URL, "token_url": srv.URL + "/token",
		"email": "arjun@acme.com", "scopes": []any{ScopeCompose}}
	if mutate != nil {
		mutate(cfg)
	}
	raw, _ := json.Marshal(cfg)
	env := connector.Env{Name: egress.EnvDev, Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
	norm, errs := New().Validate(context.Background(), env, raw)
	if len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	hashN++
	r := &rig{t: t, srv: srv, op: &opener{fields: map[string]string{FieldClientID: "123.apps.googleusercontent.com", FieldClientSecret: clientSecret, FieldRefreshToken: refreshToken}}, logs: &bytes.Buffer{}}
	r.row = store.Connector{ID: conID, OrgID: org.String(), Type: Type, Name: "gmail", Config: norm, ConfigHash: "sha256:test" + strings.Repeat("x", hashN), Status: "active"}
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

func (r *rig) call(tool, args string) (connector.Result, error, connector.Call) {
	r.t.Helper()
	ex, spec, _, err := r.reg.Resolve(context.Background(), org, "gmail."+tool)
	if err != nil {
		return connector.Result{}, err, connector.Call{}
	}
	c := connector.Call{ActionID: ids.MustParse[ids.ActionKind](actID), OrgID: org, Tool: spec, Qualified: "gmail." + tool, Args: json.RawMessage(args)}
	res, err := ex.Execute(context.Background(), c)
	return res, err, c
}

func (r *rig) readBack(c connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	ex, _, _, _ := r.reg.Resolve(context.Background(), org, c.Qualified)
	return ex.(connector.NativeReadBacking).NativeReadBack(context.Background(), c, res)
}

func (r *rig) sweep(extra ...string) {
	r.t.Helper()
	hay := r.logs.String() + strings.Join(extra, "")
	if strings.Contains(hay, "dgSENTINEL") {
		r.t.Errorf("a sentinel leaked")
	}
}

func tokenEx() saastest.Exchange {
	return saastest.Exchange{Name: "token", Method: "POST", Path: "/token", Status: 200, Repeat: true,
		BodySubset: map[string]any{"grant_type": "refresh_token", "refresh_token": refreshToken},
		Body:       json.RawMessage(`{"access_token":"` + accessTok + `","expires_in":3599,"token_type":"Bearer"}`)}
}

func gx(name, method, path string, status int, body string) saastest.Exchange {
	return saastest.Exchange{Name: name, Method: method, Path: path, Status: status, Body: json.RawMessage(body),
		RequireHeaders: map[string]string{"Authorization": "Bearer " + accessTok}}
}

var _ = saas.Segments

func mustCon() ids.ConnectorID { return ids.MustParse[ids.ConnectorKind](conID) }
