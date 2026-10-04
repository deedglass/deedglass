package github

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/vault"
)

const (
	testRepo   = "arjun/deedglass-sandbox"
	testAction = "act_01j9zq3m8x2k7w4c5v6b7n8p9r"
	testMarker = "<!-- deedglass:" + testAction + " -->"
	testLogin  = "arjun-sandbox-bot"
)

var (
	testOrg  = ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvp")
	testConn = ids.MustParse[ids.ConnectorKind]("con_01j9zq7c4m8x2v6t0r3n5k1h7d")
	testAct  = ids.MustParse[ids.ActionKind](testAction)
	// testNow is the fixed clock: 2026-10-04 10:00:00 UTC (epoch 1791108000).
	testNow = time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
)

// sentinelToken is a fine-grained token shape built at run time; it is no credential.
func sentinelToken() string {
	return "github_pat_" + strings.Repeat("dgSENTINEL7Q2v", 4)
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

func patOpener(tok string) *opener {
	return &opener{kind: vault.KindBearer, fields: map[string]string{"token": tok}}
}

func devEnv() connector.Env {
	return connector.Env{Name: "dev", Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
}

func newConn() *Connector {
	return New(WithClock(func() time.Time { return testNow }))
}

// cfgJSON is a valid config against base with optional overrides merged in.
func cfgJSON(base string, extra map[string]any) json.RawMessage {
	m := map[string]any{"repos": []string{testRepo}, "api_base_url": base}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// instance validates cfg and binds it to an instance named "github".
func instance(t testing.TB, c *Connector, cfg json.RawMessage, op vault.Opener) *connector.Instance {
	t.Helper()
	norm, errs := c.Validate(context.Background(), devEnv(), cfg)
	if len(errs) > 0 {
		t.Fatalf("Validate: %v", errs)
	}
	return connector.NewInstance(testConn, testOrg, "github", Type, norm, "sha256:test", devEnv(), op)
}

// server serves testdata/<name>.json.
func server(t testing.TB, name string) *saastest.Server {
	t.Helper()
	return saastest.New(t, saastest.Load(t, "testdata/"+name+".json")...)
}

// setup is the common case: a fixture server and a PAT instance against it.
func setup(t testing.TB, fixture string, extra map[string]any) (*Connector, *connector.Instance, *saastest.Server) {
	t.Helper()
	srv := server(t, fixture)
	c := newConn()
	c.sleep = func(context.Context, time.Duration) bool { return true }
	return c, instance(t, c, cfgJSON(srv.URL, extra), patOpener(sentinelToken())), srv
}

// call builds a call of tool with args.
func call(t testing.TB, tool string, args string) connector.Call {
	t.Helper()
	spec, ok := specFor(tool)
	if !ok {
		t.Fatalf("no tool %s", tool)
	}
	return connector.Call{ActionID: testAct, OrgID: testOrg, Tool: spec, Qualified: "github." + tool, Args: json.RawMessage(args),
		IdempotencyKey: testAction}
}

func ctxWithRedactor() context.Context {
	return vault.WithRedactor(context.Background(), vault.NewRedactor())
}

// mustErr returns err as a *connector.Error.
func mustErr(t testing.TB, err error) *connector.Error {
	t.Helper()
	ce := connector.AsError(err)
	if ce == nil {
		t.Fatalf("err = %v, want a *connector.Error", err)
	}
	return ce
}

func output(t testing.TB, res connector.Result) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(res.Output, &m); err != nil {
		t.Fatalf("output %s: %v", res.Output, err)
	}
	return m
}

// allMatch reports whether every finding matches, and names the first that doesn't.
func allMatch(fs []connector.L1Finding) (bool, string) {
	for _, f := range fs {
		if !f.Match {
			return false, f.Field
		}
	}
	return len(fs) > 0, ""
}
