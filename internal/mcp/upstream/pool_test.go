package upstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mcp/mcptest"
	"github.com/deedglass/deedglass/internal/mcp/upstream"
	"github.com/deedglass/deedglass/internal/policy"
)

func pool(t *testing.T, u *mcptest.HTTPUpstream, timeout time.Duration) *upstream.Pool {
	t.Helper()
	ur, _ := url.Parse(u.URL())
	p, err := upstream.New(upstream.Options{AllowPrivate: true, Version: "test",
		Upstreams: []upstream.Config{{Slug: "everything", URL: ur, Timeout: timeout, ToolsTTL: time.Minute,
			HeadersFromEnv: map[string]string{"Authorization": "EVERYTHING_MCP_AUTH"}}},
		Getenv: func(k string) string {
			if k == "EVERYTHING_MCP_AUTH" {
				return "Bearer upstream-secret-1234"
			}
			return ""
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func call(name, args string) connector.Call {
	return connector.Call{ActionID: ids.New[ids.ActionKind](), Tool: connector.ToolSpec{Name: name}, Qualified: "everything." + name, Args: json.RawMessage(args)}
}

func TestPoolBothEras(t *testing.T) {
	for _, stateless := range []bool{true, false} {
		name := "legacy"
		if stateless {
			name = "modern"
		}
		t.Run(name, func(t *testing.T) {
			u := mcptest.NewHTTPUpstream(stateless)
			defer u.Close()
			p := pool(t, u, 5*time.Second)
			ctx := context.Background()
			tools, err := p.ListTools(ctx, "everything")
			if err != nil || len(tools) != 5 {
				t.Fatalf("%v %d", err, len(tools))
			}
			// cached
			before := len(u.Headers())
			if _, err := p.ListTools(ctx, "everything"); err != nil || len(u.Headers()) != before {
				t.Fatalf("not cached: %v", err)
			}
			ex, spec, ctype, err := p.Resolve(ctx, ids.OrgID{}, "everything.echo")
			if err != nil || ctype != "mcp_http" || spec.SideEffect != connector.SideEffectUnknown {
				t.Fatal(err, ctype, spec)
			}
			c := call("echo", `{"message":"hi"}`)
			res, err := ex.Execute(ctx, c)
			if err != nil || !strings.Contains(string(res.Output), "Echo: hi") {
				t.Fatalf("%v %s", err, res.Output)
			}
			if r, _, ok := p.Take(c.ActionID.String()); !ok || !strings.Contains(string(r), "Echo: hi") {
				t.Fatal("stash")
			}
			// fail → tool_error, failed
			_, err = ex.Execute(ctx, call("fail", `{}`))
			if ce := connector.AsError(err); ce == nil || ce.Kind != connector.KindToolError || ce.Code != "tool_error" {
				t.Fatalf("%v", err)
			}
			// hang with a deadline → timeout (unknown)
			hctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			_, err = ex.Execute(hctx, call("hang", `{}`))
			cancel()
			if ce := connector.AsError(err); ce == nil || ce.Kind != connector.KindTimeout {
				t.Fatalf("%v", err)
			}
			// an already-ended context never sends
			dctx, dcancel := context.WithCancel(ctx)
			dcancel()
			n := u.Count("echo")
			_, err = ex.Execute(dctx, call("echo", `{}`))
			if ce := connector.AsError(err); ce == nil || ce.Kind != connector.KindCancelled || u.Count("echo") != n {
				t.Fatalf("%v", err)
			}
			// The configured header reaches the upstream; nothing else credential-like does.
			if !u.SawHeader("", "upstream-secret-1234") {
				t.Fatal("configured header missing")
			}
			for _, h := range u.Headers() {
				for k := range h {
					if strings.HasPrefix(strings.ToLower(k), "deedglass-") || strings.EqualFold(k, "Cookie") {
						t.Fatalf("forwarded %s", k)
					}
				}
				if ua := h.Get("User-Agent"); ua != "deedglass/test" {
					t.Fatalf("User-Agent %q", ua)
				}
			}
			// The upstream stops: the next call is connector_unavailable (nothing ran).
			u.Close()
			_, err = ex.Execute(ctx, call("echo", `{}`))
			if ce := connector.AsError(err); ce == nil || ce.Code != "connector_unavailable" || ce.Kind != connector.KindConfig {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestPoolDown(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ur, _ := url.Parse("http://" + addr + "/mcp")
	p, err := upstream.New(upstream.Options{AllowPrivate: true, Upstreams: []upstream.Config{{Slug: "everything", URL: ur, Timeout: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.ListTools(context.Background(), "everything"); !errors.Is(err, upstream.ErrDown) {
		t.Fatalf("%v", err)
	}
	ex, _, _, _ := p.Resolve(context.Background(), ids.OrgID{}, "everything.echo")
	_, err = ex.Execute(context.Background(), call("echo", `{}`))
	if ce := connector.AsError(err); ce == nil || ce.Code != "connector_unavailable" {
		t.Fatalf("%v", err)
	}
}

func TestPoolRefusals(t *testing.T) {
	ur, _ := url.Parse("http://127.0.0.1:1/mcp")
	if _, err := upstream.New(upstream.Options{Upstreams: []upstream.Config{{Slug: "x", URL: ur}}}); err == nil {
		t.Fatal("http allowed outside dev")
	}
	us, _ := url.Parse("https://mcp.example.test/mcp")
	if _, err := upstream.New(upstream.Options{Upstreams: []upstream.Config{{Slug: "x", URL: us, HeadersFromEnv: map[string]string{"Authorization": "UNSET_VAR"}}},
		Getenv: func(string) string { return "" }}); err == nil {
		t.Fatal("empty header env accepted")
	}
	// S16: the pool dials through egress. Outside dev a loopback upstream is refused at start, and
	// a name that rebinds to loopback after the start check fails as destination_blocked.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	lu, _ := url.Parse("https://127.0.0.1:" + port + "/mcp")
	if _, err := upstream.New(upstream.Options{Upstreams: []upstream.Config{{Slug: "loop", URL: lu}}}); err == nil {
		t.Fatal("a loopback upstream was accepted in prod")
	}
	res := egress.StaticResolver(nil).Sequence("mcp.rebind.test", []netip.Addr{netip.MustParseAddr("93.184.216.34")}, []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	ru, _ := url.Parse("https://mcp.rebind.test:" + port + "/mcp")
	p, err := upstream.New(upstream.Options{Egress: &egress.Policy{Env: egress.EnvProd, Resolver: res},
		Upstreams: []upstream.Config{{Slug: "rebind", URL: ru, Timeout: time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	ex, _, _, _ := p.Resolve(context.Background(), ids.OrgID{}, "rebind.echo")
	_, err = ex.Execute(context.Background(), call("echo", `{}`))
	if ce := connector.AsError(err); ce == nil || ce.Code != "destination_blocked" {
		t.Fatalf("rebinding upstream: %v", err)
	}
	mu, _ := url.Parse("http://169.254.169.254/mcp")
	if _, err := upstream.New(upstream.Options{AllowPrivate: true, Upstreams: []upstream.Config{{Slug: "meta", URL: mu}}}); err == nil {
		t.Fatal("a metadata upstream was accepted in dev")
	}
}

type hideFail struct{}

func (hideFail) Visibility(_ context.Context, _ identity.Principal, ts []policy.ToolFacts) (*gate.ToolVisibilityList, error) {
	out := &gate.ToolVisibilityList{}
	for _, f := range ts {
		v := gate.VisibilityVisible
		if f.Name == "everything.fail" {
			v = gate.VisibilityHidden
		}
		if f.ConnectorType != "mcp_http" || f.SideEffect != "unknown" {
			return nil, errors.New("wrong facts")
		}
		out.Data = append(out.Data, gate.ToolVisibility{Name: f.Name, Visibility: v})
	}
	return out, nil
}

func TestSourceAndResolver(t *testing.T) {
	u := mcptest.NewHTTPUpstream(false)
	defer u.Close()
	p := pool(t, u, 5*time.Second)
	ctx := context.Background()
	src := p.Source("everything", hideFail{})
	ts, err := src.List(ctx, identity.Principal{})
	if err != nil || len(ts) != 5 || src.Label() != "everything" {
		t.Fatalf("%v %d", err, len(ts))
	}
	for _, x := range ts {
		if x.Gate != "everything."+x.Name || x.Digest == "" || (x.Name == "fail") != x.Hidden {
			t.Fatalf("%+v", x)
		}
	}
	if tl, ok, err := src.Lookup(ctx, identity.Principal{}, "echo"); !ok || err != nil || tl.Digest == "" {
		t.Fatal("lookup echo")
	}
	if _, ok, _ := src.Lookup(ctx, identity.Principal{}, "nope"); ok {
		t.Fatal("found nope")
	}
	if _, ok, _ := src.Lookup(ctx, identity.Principal{}, "bad name!"); ok {
		t.Fatal("found a bad name")
	}
	// Passthrough: the SDK fake has no resources or prompts, so the upstream answers an error.
	if _, rpcErr, err := src.Forward(ctx, "prompts/list", nil); err != nil && rpcErr == nil {
		t.Logf("prompts/list: %v", err)
	}
	if _, rpcErr, _ := src.Forward(ctx, "tasks/list", nil); rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatal("unknown passthrough method")
	}
	if _, rpcErr, _ := src.Forward(ctx, "resources/read", json.RawMessage(`{"uri":5}`)); rpcErr == nil {
		t.Fatal("bad params accepted")
	}
	// The composite resolver: upstream slugs to the pool, the rest to the native registry.
	r := upstream.Resolver{Native: connector.NewRegistry(fakeConn{}), Pool: p}
	if _, _, ct, err := r.Resolve(ctx, ids.OrgID{}, "everything.echo"); err != nil || ct != "mcp_http" {
		t.Fatal(err, ct)
	}
	if _, _, ct, err := r.Resolve(ctx, ids.OrgID{}, "fk.echo"); err != nil || ct != "fk" {
		t.Fatal(err, ct)
	}
	if _, _, _, err := r.Resolve(ctx, ids.OrgID{}, "nobody.echo"); !errors.Is(err, connector.ErrToolUnknown) {
		t.Fatal(err)
	}
	// Down: Lookup still returns the tool, so the call is recorded and fails at execution.
	u.Close()
	time.Sleep(10 * time.Millisecond)
	p2 := pool(t, u, time.Second)
	if tl, ok, err := p2.Source("everything", nil).Lookup(ctx, identity.Principal{}, "echo"); !ok || err != nil || tl.Digest != "" {
		t.Fatalf("down lookup %v %v %+v", ok, err, tl)
	}
	if _, err := p2.Source("everything", nil).List(ctx, identity.Principal{}); err == nil {
		t.Fatal("list while down")
	}
	if p.Slugs()[0] != "everything" || !p.Has("everything") || p.Has("x") {
		t.Fatal("slugs")
	}
}

type fakeConn struct{}

func (fakeConn) Type() string             { return "fk" }
func (fakeConn) Info() connector.TypeInfo { return connector.TypeInfo{Type: "fk"} }
func (fakeConn) Validate(context.Context, connector.Env, json.RawMessage) (json.RawMessage, []connector.FieldError) {
	return json.RawMessage(`{}`), nil
}
func (fakeConn) Tools(context.Context, *connector.Instance) ([]connector.ToolSpec, error) {
	return []connector.ToolSpec{{Name: "echo"}}, nil
}
func (fakeConn) Execute(context.Context, *connector.Instance, connector.Call) (connector.Result, error) {
	return connector.Result{}, nil
}
func (fakeConn) Test(context.Context, *connector.Instance) (connector.TestReport, error) {
	return connector.TestReport{}, nil
}
