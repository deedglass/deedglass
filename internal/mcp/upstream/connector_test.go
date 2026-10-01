package upstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mcp/mcptest"
	"github.com/deedglass/deedglass/internal/mcp/upstream"
	"github.com/deedglass/deedglass/internal/vault"
)

// stubVault hands out one credential per org and connector; token may change between calls.
type stubVault struct {
	kind  string
	token atomic.Value // string; "" means no credential
	opens atomic.Int64
}

func (s *stubVault) Open(_ context.Context, _ vault.Ref) (*vault.Secret, error) {
	s.opens.Add(1)
	v, _ := s.token.Load().(string)
	if v == "" {
		return nil, vault.ErrNotFound
	}
	field := "token"
	if s.kind == "header" {
		field = "value"
	}
	return vault.NewSecretForTest(s.kind, map[string]string{field: v}), nil
}

func devEnv() connector.Env {
	return connector.Env{Name: egress.EnvDev, Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
}

func rowInstance(t *testing.T, typ *upstream.Type, env connector.Env, cfg string, v *stubVault) *connector.Instance {
	t.Helper()
	norm, errs := typ.Validate(context.Background(), env, json.RawMessage(cfg))
	if len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	var op interface {
		Open(context.Context, vault.Ref) (*vault.Secret, error)
	}
	if v != nil {
		op = v
	}
	inst := connector.NewInstance(ids.New[ids.ConnectorKind](), ids.New[ids.OrgKind](), "fs", upstream.ConnectorType, norm, "sha256:test", env, op)
	return inst
}

func TestRowValidate(t *testing.T) {
	p, _ := upstream.New(upstream.Options{})
	typ := upstream.NewType(p)
	ctx := context.Background()
	norm, errs := typ.Validate(ctx, devEnv(), json.RawMessage(`{"url":"http://127.0.0.1:9/mcp"}`))
	if len(errs) > 0 || !strings.Contains(string(norm), `"kind":"bearer"`) || !strings.Contains(string(norm), `"timeout_ms":30000`) {
		t.Fatalf("defaults: %s %v", norm, errs)
	}
	prod := connector.Env{Name: egress.EnvProd, Egress: &egress.Policy{Env: egress.EnvProd}}
	for cfg, want := range map[string]string{
		`{}`:                                     "url is required",
		`{"url":"https://x.test/mcp","x":1}`:     "can't be read",
		`{"url":"http://169.254.169.254/mcp"}`:   "destination_blocked",
		`{"url":"http://mcp.example.com/mcp"}`:   "destination_blocked", // http in prod
		`{"url":"https://u:p@mcp.example.com/"}`: "url",
		`{"url":"https://mcp.example.com/","auth":{"kind":"header","header":"Cookie"}}`:      "auth.header",
		`{"url":"https://mcp.example.com/","auth":{"kind":"header","header":"Mcp-Param-x"}}`: "auth.header",
		`{"url":"https://mcp.example.com/","auth":{"kind":"basic"}}`:                         "auth.kind",
		`{"url":"https://mcp.example.com/","timeout_ms":5}`:                                  "timeout_ms",
	} {
		_, errs := typ.Validate(ctx, prod, json.RawMessage(cfg))
		got := ""
		for _, e := range errs {
			got += e.Param + " " + e.Message + " " + e.Code + "\n"
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s: want %q in %q", cfg, want, got)
		}
	}
	if typ.Info().Type != "mcp_http" || len(typ.Info().CredentialKinds) != 2 {
		t.Fatal(typ.Info())
	}
}

func TestRowCredentialFromVault(t *testing.T) {
	u := mcptest.NewHTTPUpstream(true)
	defer u.Close()
	p, _ := upstream.New(upstream.Options{Version: "test"})
	defer p.Close()
	typ := upstream.NewType(p)
	v := &stubVault{kind: "bearer"}
	v.token.Store("vault-token-one-1234567890abcdef")
	inst := rowInstance(t, typ, devEnv(), `{"url":"`+u.URL()+`"}`, v)
	ctx := context.Background()

	tools, err := typ.Tools(ctx, inst)
	if err != nil || len(tools) != 5 || tools[0].SideEffect != connector.SideEffectUnknown {
		t.Fatalf("%v %v", err, tools)
	}
	if !u.SawHeader("", "Bearer vault-token-one-1234567890abcdef") {
		t.Fatal("the vault credential didn't reach the upstream")
	}
	res, err := typ.Execute(ctx, inst, call("echo", `{"message":"hi"}`))
	if err != nil || !strings.Contains(string(res.Output), "Echo: hi") {
		t.Fatalf("%v %s", err, res.Output)
	}
	// A replaced credential opens a new session with the new value.
	v.token.Store("vault-token-two-1234567890abcdef")
	if _, err := typ.Execute(ctx, inst, call("echo", `{"message":"again"}`)); err != nil {
		t.Fatal(err)
	}
	if !u.SawHeader("", "Bearer vault-token-two-1234567890abcdef") {
		t.Fatal("the replaced credential wasn't used")
	}
	// No credential: credentials_missing, and nothing is sent.
	v.token.Store("")
	before := u.Count("echo")
	_, err = typ.Execute(ctx, inst, call("echo", `{}`))
	var ce *connector.Error
	if !errors.As(err, &ce) || ce.Code != connector.CodeCredentialsMissing || u.Count("echo") != before {
		t.Fatalf("%v", err)
	}
	// No opener at all: the same.
	bare := rowInstance(t, typ, devEnv(), `{"url":"`+u.URL()+`"}`, nil)
	if _, err := typ.Tools(ctx, bare); !errors.As(err, &ce) || ce.Code != connector.CodeCredentialsMissing {
		t.Fatalf("%v", err)
	}
	// auth none needs no credential.
	none := rowInstance(t, typ, devEnv(), `{"url":"`+u.URL()+`","auth":{"kind":"none"}}`, nil)
	if ts, err := typ.Tools(ctx, none); err != nil || len(ts) != 5 {
		t.Fatalf("%v", err)
	}
	// A credential of the wrong kind is treated as missing.
	hv := &stubVault{kind: "header"}
	hv.token.Store("header-value-1234567890abcdef")
	wrong := rowInstance(t, typ, devEnv(), `{"url":"`+u.URL()+`"}`, hv)
	if _, err := typ.Tools(ctx, wrong); !errors.As(err, &ce) || ce.Code != connector.CodeCredentialsMissing {
		t.Fatalf("%v", err)
	}
	// header auth sends the configured header.
	hdr := rowInstance(t, typ, devEnv(), `{"url":"`+u.URL()+`","auth":{"kind":"header","header":"x-api-key"}}`, hv)
	if _, err := typ.Tools(ctx, hdr); err != nil || !u.SawHeader("", "header-value-1234567890abcdef") {
		t.Fatalf("header auth: %v", err)
	}
	rep, err := typ.Test(ctx, inst)
	if err != nil || rep.Status != connector.CheckFail { // the credential is gone now
		t.Fatalf("%v %+v", err, rep)
	}
	v.token.Store("vault-token-one-1234567890abcdef")
	if rep, _ := typ.Test(ctx, inst); rep.Status != connector.CheckPass {
		t.Fatalf("%+v", rep)
	}
}
