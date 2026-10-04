package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
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

// registryFor binds a github connector row to a registry, the way serve does.
func registryFor(t *testing.T, c *Connector, cfg json.RawMessage, op vault.Opener, log *bytes.Buffer) *connector.Registry {
	t.Helper()
	norm, errs := c.Validate(context.Background(), devEnv(), cfg)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	row := store.Connector{ID: testConn.String(), OrgID: testOrg.String(), Type: Type, Name: "github", Config: norm, ConfigHash: "sha256:x", Status: "active"}
	return connector.New(connector.Options{Types: []connector.Connector{c}, Loader: memLoader{row}, Opener: op, Env: devEnv(),
		Logger: slog.New(slog.NewTextHandler(log, nil))})
}

// TestSentinelNeverLeaks: the token (and an App's JWT and installation token) never appears in an
// output, a meta value, an error, a finding, a test report, a request body or a log line.
func TestSentinelNeverLeaks(t *testing.T) {
	tok := sentinelToken()
	var seen []string
	note := func(vs ...any) {
		for _, v := range vs {
			b, _ := json.Marshal(v)
			seen = append(seen, string(b), fmt.Sprint(v))
		}
	}
	noteErr := func(err error) {
		if err == nil {
			return
		}
		seen = append(seen, err.Error())
		if ce := connector.AsError(err); ce != nil {
			seen = append(seen, ce.AgentMessage, ce.AdminDetail)
		}
	}
	var logs bytes.Buffer
	var bodies []string

	for _, fx := range []string{"create_issue", "perm_403", "ratelimit_403"} {
		srv := server(t, fx)
		c := newConn()
		reg := registryFor(t, c, cfgJSON(srv.URL, nil), patOpener(tok), &logs)
		ex, spec, _, err := reg.Resolve(context.Background(), testOrg, "github.create_issue")
		if err != nil {
			t.Fatal(err)
		}
		cl := connector.Call{ActionID: testAct, Tool: spec, Qualified: "github.create_issue",
			Args: json.RawMessage(`{"repo":"arjun/deedglass-sandbox","title":"Flaky checkout test","body":"Fails 1 in 20 runs on CI."}`)}
		res, err := ex.Execute(context.Background(), cl)
		if fx == "create_issue" && err != nil {
			t.Fatalf("execute: %v", err)
		}
		note(res.Output, res.Meta, res.Evidence)
		noteErr(err)
		if nr, ok := ex.(connector.NativeReadBacking); ok && err == nil {
			if !nr.CanNativeReadBack(spec) {
				t.Fatal("create_issue isn't read back natively")
			}
			obs, fs, err := nr.NativeReadBack(context.Background(), cl, res)
			note(obs, fs)
			noteErr(err)
			if ok, _ := allMatch(fs); !ok {
				t.Fatalf("findings %+v", fs)
			}
		}
		for _, r := range srv.Requests() {
			bodies = append(bodies, string(r.Body))
			if r.Header.Get("Authorization") != "Bearer "+tok {
				t.Fatal("the request wasn't authenticated")
			}
		}
	}
	// The test report.
	srv := server(t, "test_pat")
	c := newConn()
	rep, _ := c.Test(ctxWithRedactor(), instance(t, c, cfgJSON(srv.URL, nil), patOpener(tok)))
	note(rep)
	// An App: the JWT and the installation token stay out too.
	srv = saastest.New(t, saastest.Load(t, "testdata/app_execute.json")[:3]...)
	c = newConn()
	inst := instance(t, c, appCfg(srv.URL), appOpener())
	for i := 0; i < 2; i++ {
		res, err := c.Execute(ctxWithRedactor(), inst, call(t, "create_issue", `{"repo":"arjun/deedglass-sandbox","title":"Flaky checkout test"}`))
		note(res.Output, res.Meta)
		noteErr(err)
	}
	jwt, _ := appJWT(saastest.TestRSAKey(), "Iv23liAbC123", testNow)
	all := strings.Join(append(append(seen, logs.String()), bodies...), "\n")
	for name, secret := range map[string]string{"token": tok, "jwt": jwt, "installation token": "test-installation-token-0001", "pem": "PRIVATE KEY"} {
		if strings.Contains(all, secret) {
			t.Fatalf("the %s leaked", name)
		}
	}
	if len(seen) < 20 {
		t.Fatalf("only %d values swept", len(seen))
	}
}

// TestRegistryValidatesFirst: the registry's own argument check runs before the connector's.
func TestRegistryValidatesFirst(t *testing.T) {
	srv := saastest.New(t)
	var logs bytes.Buffer
	reg := registryFor(t, newConn(), cfgJSON(srv.URL, nil), patOpener(sentinelToken()), &logs)
	ex, spec, typ, err := reg.Resolve(context.Background(), testOrg, "github.merge_pr")
	if err != nil || typ != "github" || spec.L1 != L1MergePR {
		t.Fatalf("%v %s %+v", err, typ, spec)
	}
	_, err = ex.Execute(context.Background(), connector.Call{Tool: spec, Args: json.RawMessage(`{"repo":"arjun/deedglass-sandbox","number":7}`)})
	if ce := mustErr(t, err); ce.AgentMessage != msgMergeSHA {
		t.Fatalf("%+v", ce)
	}
	if ae, ok := ex.(connector.ArgsEnricher); !ok {
		t.Fatal("not an ArgsEnricher")
	} else if m, _ := ae.EnrichArgs(context.Background(), spec, json.RawMessage(`{"repo":"arjun/x","number":1}`)); m != nil {
		t.Fatal(m)
	}
	if len(srv.Requests()) != 0 {
		t.Fatal("a request was sent")
	}
}
