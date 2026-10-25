//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mcp/bridge"
	"github.com/deedglass/deedglass/internal/mcp/httpgw"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/mcp/mcptest"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// gatewayPolicy is the S15 gateway test policy: upstream tools run, delete_all never runs,
// approve_me waits for a person, the second agent may not call fail (so its list differs), and
// of the fake connector only echo runs (so GET /v1/tools hides the rest).
const gatewayPolicy = `deedglass: policy/v1
name: gateway-test
description: The S15 gateway test policy.
defaults:
  decision: deny
  reason: No rule allows this action. Add a rule in Console › Policies to allow it.
rules:
  - id: second-never-fails
    match: { tool: [everything.fail], agent: [second-agent] }
    decision: deny
    reason: The second agent may not call fail.
  - id: no-deletes
    match: { tool: [everything.delete_all, legacy.delete_all] }
    decision: deny
    reason: Agents never delete everything.
  - id: approve-first
    match: { tool: everything.approve_me }
    decision: require_approval
    reason: A person approves this first.
    approval: { approvers: [role:admin], required: 1, expires_in: 30m }
  - id: allow-upstreams
    match: { tool: ["everything.*", "legacy.*"] }
    decision: allow
  - id: allow-fake-echo
    match: { tool: fake.echo }
    decision: allow
`

type gwLive struct {
	base, admin, key1, key2, agent2 string
	env                             map[string]string
	s                               *served
	e                               *storetest.Env
	gp                              *gate.Pipeline
	modern, legacy                  *mcptest.HTTPUpstream
	org                             ids.OrgID
}

const upstreamSecret = "Bearer upstream-secret-7f3k"

func newGatewayLive(t *testing.T) *gwLive {
	t.Helper()
	g := &gwLive{modern: mcptest.NewHTTPUpstream(true), legacy: mcptest.NewHTTPUpstream(false)}
	t.Cleanup(g.modern.Close)
	t.Cleanup(g.legacy.Close)
	g.e = storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 9)
	yaml := fmt.Sprintf("gate:\n  execute_timeout: 2s\nmcp:\n  upstreams:\n    everything:\n      url: %s\n      headers_from_env: {Authorization: EVERYTHING_MCP_AUTH}\n    legacy:\n      url: %s\n",
		g.modern.URL(), g.legacy.URL())
	g.env = map[string]string{"DEEDGLASS_DATABASE_URL": g.e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": g.e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "info", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32, "DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "1",
		"DEEDGLASS_IT_YAML": yaml, "EVERYTHING_MCP_AUTH": upstreamSecret}
	saved := serveGate
	gpc := make(chan *gate.Pipeline, 1)
	serveGate = func(p *gate.Pipeline) { gpc <- p }
	t.Cleanup(func() { serveGate = saved })
	g.s = startServe(t, g.env, "--migrate")
	select {
	case a := <-g.s.addr:
		g.base = "http://" + a.String()
	case c := <-g.s.code:
		t.Fatalf("exited %d\n%s%s", c, g.s.out, g.s.err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve never listened")
	}
	g.gp = <-gpc
	t.Cleanup(func() { g.s.sigs <- os.Interrupt; g.s.wait(t) })
	g.env["DEEDGLASS_BASE_URL"], g.env["DEEDGLASS_SERVER"] = g.base, g.base
	r := cli(t, g.env, false, "org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		Org      struct{ ID string }     `json:"org"`
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	g.admin = created.AdminKey.Secret
	g.env["DEEDGLASS_API_KEY"] = g.admin
	pol := filepath.Join(dir, "gateway-test.yaml")
	if err := os.WriteFile(pol, []byte(gatewayPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"policy", "push", pol}, {"policy", "activate", "gateway-test", "--version", "1", "--mode", "enforce", "--reason", "S15 test", "--yes"}} {
		if r = cli(t, g.env, false, args...); r.code != 0 {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	mk := func(name string) (string, string) {
		r := cli(t, g.env, false, "--json", "agent", "create", name)
		var a struct{ ID string }
		if r.code != 0 || json.Unmarshal([]byte(r.stdout), &a) != nil {
			t.Fatalf("%+v", r)
		}
		r = cli(t, g.env, false, "--json", "agent", "keys", "create", name)
		var k struct{ Secret string }
		if r.code != 0 || json.Unmarshal([]byte(r.stdout), &k) != nil {
			t.Fatalf("%+v", r)
		}
		return a.ID, k.Secret
	}
	_, g.key1 = mk("claude-desktop")
	g.agent2, g.key2 = mk("second-agent")
	org, err := ids.Parse[ids.OrgKind](created.Org.ID)
	if err != nil {
		t.Fatalf("org id %q: %v", created.Org.ID, err)
	}
	g.org = org
	return g
}

// authRT adds the agent key to every request an SDK client sends.
type authRT struct{ key string }

func (a authRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.key)
	r.Header.Set("Cookie", "session=client-cookie-should-not-travel")
	r.Header.Set("Deedglass-Session", "sess-should-not-travel")
	return http.DefaultTransport.RoundTrip(r)
}

func sdkSession(t *testing.T, base, path, key, version string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "sdk-test", Version: "1"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: base + path, HTTPClient: &http.Client{Transport: authRT{key}}, MaxRetries: -1}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := c.Connect(ctx, tr, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("connect %s at %s: %v", path, version, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func toolNames(t *testing.T, s *mcp.ClientSession) string {
	t.Helper()
	res, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, x := range res.Tools {
		n = append(n, x.Name)
	}
	return strings.Join(n, ",")
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// rawCall posts one modern tools/call with the right headers.
func rawCall(t *testing.T, ctx context.Context, base, path, key, tool, args string) (int, map[string]json.RawMessage) {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":%q,"arguments":%s,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"raw","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, tool, args)
	req, _ := http.NewRequestWithContext(ctx, "POST", base+path, strings.NewReader(body))
	for k, v := range map[string]string{"Authorization": "Bearer " + key, "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
		"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": tool} {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer res.Body.Close()
	var out struct{ Result map[string]json.RawMessage }
	b, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(b, &out)
	if out.Result == nil {
		t.Logf("raw %s: %d %s", tool, res.StatusCode, b)
	}
	return res.StatusCode, out.Result
}

func resultText(r map[string]json.RawMessage) string {
	var c []struct{ Text string }
	_ = json.Unmarshal(r["content"], &c)
	var b strings.Builder
	for _, x := range c {
		b.WriteString(x.Text)
	}
	return b.String()
}

func metaOf(r map[string]json.RawMessage) map[string]string {
	m := map[string]string{}
	_ = json.Unmarshal(r["_meta"], &m)
	return m
}

func (g *gwLive) action(t *testing.T, key, id string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest("GET", g.base+"/v1/actions/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var m map[string]any
	b, _ := io.ReadAll(res.Body)
	if json.Unmarshal(b, &m) != nil {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	if bytes.Contains(b, []byte("upstream-secret")) {
		t.Fatalf("the upstream credential is in the action: %s", b)
	}
	return m
}

func path(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// TestMCPGatewayLive is S15 §6 "Gateway behaviour" and "Native and bridge" on a real server and
// database, with the official SDK as both the clients and the upstream servers (stateless
// 2026-07-28 and stateful 2025-11-25).
func TestMCPGatewayLive(t *testing.T) {
	g := newGatewayLive(t)
	bg := context.Background()

	// 1. SDK clients in both eras, on both upstream eras: list and an allowed call.
	for _, v := range []string{"2026-07-28", "2025-11-25"} {
		for _, up := range []string{"/mcp/everything", "/mcp/legacy"} {
			t.Run("sdk "+v+" "+up, func(t *testing.T) {
				s := sdkSession(t, g.base, up, g.key1, v)
				names := toolNames(t, s)
				if names != "approve_me,echo,fail,hang,deedglass_check_approval" {
					t.Fatalf("tools: %s", names)
				}
				res, err := s.CallTool(bg, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": "hi"}})
				if err != nil || res.IsError || text(res) != "Echo: hi" {
					t.Fatalf("%v %+v", err, res)
				}
				act, _ := res.Meta["com.deedglass/action"].(string)
				deedID, _ := res.Meta["com.deedglass/deed"].(string)
				if !strings.HasPrefix(act, "act_") || !strings.HasPrefix(deedID, "deed_") {
					t.Fatalf("meta %v", res.Meta)
				}
				a := g.action(t, g.key1, act)
				if a["mode"] != "gateway" || path(a, "witness", "verdict") != "VERIFIED" || a["state"] != "sealed" {
					t.Fatalf("%v", a)
				}
			})
		}
	}

	// 2. A denied call never reaches the upstream, and is sealed.
	before := g.modern.Count("delete_all")
	st, r := rawCall(t, bg, g.base, "/mcp/everything", g.key1, "delete_all", `{}`)
	txt := resultText(r)
	if st != 200 || string(r["isError"]) != "true" || !strings.HasPrefix(txt, `Blocked by policy "no-deletes": Agents never delete everything.`) || g.modern.Count("delete_all") != before {
		t.Fatalf("%d %s", st, txt)
	}
	if m := metaOf(r); !strings.HasPrefix(m["com.deedglass/deed"], "deed_") || m["com.deedglass/rule"] != "no-deletes" {
		t.Fatalf("%v", m)
	}

	// 3. Pending → approve (in process) → check_approval returns the result; the upstream ran once.
	_, r = rawCall(t, bg, g.base, "/mcp/everything", g.key1, "approve_me", `{"message":"ship it"}`)
	apr := metaOf(r)["com.deedglass/approval"]
	if !strings.HasPrefix(apr, "apr_") || !strings.Contains(resultText(r), "Nothing has run yet") || g.modern.Count("approve_me") != 0 {
		t.Fatalf("%s %v", resultText(r), metaOf(r))
	}
	_, r = rawCall(t, bg, g.base, "/mcp/everything", g.key1, "deedglass_check_approval", `{"approval_id":"`+apr+`"}`)
	if !strings.Contains(resultText(r), "Still waiting for approval "+apr) || string(r["isError"]) != "false" {
		t.Fatalf("%s", resultText(r))
	}
	actID := metaOf(r)["com.deedglass/action"]
	if err := g.e.DB.OrgTx(bg, g.org, func(ctx context.Context, tx pgx.Tx) error {
		return store.DecideApproval(ctx, tx, g.org, apr, "approved", nil, nil, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	aid, _ := ids.Parse[ids.ActionKind](actID)
	if _, err := g.gp.Resume(bg, g.org, aid); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, r = rawCall(t, bg, g.base, "/mcp/everything", g.key1, "deedglass.check_approval", `{"approval_id":"`+apr+`"}`)
		if txt := resultText(r); !strings.Contains(txt, "Approved by a person. Ran everything.approve_me.") || !strings.Contains(txt, "approved: ship it") {
			t.Fatalf("check %d: %s", i, txt)
		}
	}
	if g.modern.Count("approve_me") != 1 {
		t.Fatalf("approve_me ran %d times", g.modern.Count("approve_me"))
	}
	// Another agent can't check this agent's approval.
	_, r = rawCall(t, bg, g.base, "/mcp/everything", g.key2, "deedglass.check_approval", `{"approval_id":"`+apr+`"}`)
	if !strings.Contains(resultText(r), "No approval "+apr+" for this agent") {
		t.Fatalf("%s", resultText(r))
	}

	// 4. The tool reports an error: failed, the upstream's own result.
	_, r = rawCall(t, bg, g.base, "/mcp/everything", g.key1, "fail", `{}`)
	if string(r["isError"]) != "true" || !strings.Contains(resultText(r), "the tool failed") {
		t.Fatalf("%s", resultText(r))
	}
	if a := g.action(t, g.key1, metaOf(r)["com.deedglass/action"]); path(a, "execution", "status") != "failed" || path(a, "execution", "error_code") != "tool_error" {
		t.Fatalf("%v", a)
	}

	// 5. The upstream hangs past gate.execute_timeout (2 s): unknown, UNVERIFIABLE (timeout).
	_, r = rawCall(t, bg, g.base, "/mcp/everything", g.key1, "hang", `{}`)
	if !strings.Contains(resultText(r), "didn't answer everything.hang within 2 s. It may have run.") {
		t.Fatalf("%s", resultText(r))
	}
	if a := g.action(t, g.key1, metaOf(r)["com.deedglass/action"]); path(a, "witness", "verdict") != "UNVERIFIABLE" || path(a, "execution", "status") != "unknown" {
		t.Fatalf("%v", a)
	}

	// 6. The client disconnects mid-call: the call is cancelled, UNVERIFIABLE (client_cancelled).
	for len(g.legacy.Started) > 0 {
		<-g.legacy.Started
	}
	cctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { rawCall(t, cctx, g.base, "/mcp/legacy", g.key1, "hang", `{}`); close(done) }()
	select {
	case name := <-g.legacy.Started:
		if name != "hang" {
			t.Fatalf("started %s", name)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hang never started")
	}
	cancel()
	<-done
	deadline := time.Now().Add(10 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", g.base+"/v1/actions?limit=1", nil)
		req.Header.Set("Authorization", "Bearer "+g.key1)
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			var l struct{ Data []map[string]any }
			_ = json.NewDecoder(res.Body).Decode(&l)
			res.Body.Close()
			if len(l.Data) == 1 && l.Data[0]["state"] == "sealed" && l.Data[0]["tool"] == "legacy.hang" {
				last = g.action(t, g.key1, l.Data[0]["id"].(string))
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if last == nil || path(last, "witness", "verdict") != "UNVERIFIABLE" || path(last, "execution", "error_code") != "client_cancelled" {
		t.Fatalf("after disconnect: %v", last)
	}

	// 7. Two agents, one policy, different lists; each owns its deeds.
	s2 := sdkSession(t, g.base, "/mcp/everything", g.key2, "2026-07-28")
	if names := toolNames(t, s2); names != "approve_me,echo,hang,deedglass_check_approval" {
		t.Fatalf("second agent's tools: %s", names)
	}
	res2, err := s2.CallTool(bg, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"message": "two"}})
	if err != nil || text(res2) != "Echo: two" {
		t.Fatalf("%v %+v", err, res2)
	}
	other, _ := res2.Meta["com.deedglass/action"].(string)
	req, _ := http.NewRequest("GET", g.base+"/v1/actions/"+other, nil)
	req.Header.Set("Authorization", "Bearer "+g.key1)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 404 {
		t.Fatalf("agent 1 reads agent 2's action: %v %v", err, res.StatusCode)
	}

	// 8. Stop the second agent: its call is refused with the stop message and sealed.
	if r := cli(t, g.env, false, "agent", "stop", g.agent2, "--reason", "S15 test"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	n := g.modern.Count("echo")
	_, r = rawCall(t, bg, g.base, "/mcp/everything", g.key2, "echo", `{"message":"stopped"}`)
	if string(r["isError"]) != "true" || !strings.Contains(resultText(r), "stopped") || !strings.HasPrefix(metaOf(r)["com.deedglass/deed"], "deed_") || g.modern.Count("echo") != n {
		t.Fatalf("%s %v", resultText(r), metaOf(r))
	}

	// 9. /mcp/native and GET /v1/tools: fake.echo runs; the policy hides the other fake tools.
	_, r = rawCall(t, bg, g.base, "/mcp/native", g.key1, "fake.echo", `{"message":"native"}`)
	if string(r["isError"]) != "false" || !strings.Contains(resultText(r), "native") || !strings.HasPrefix(metaOf(r)["com.deedglass/deed"], "deed_") {
		t.Fatalf("%s", resultText(r))
	}
	// The API-safe name tools/list shows reaches the same tool, rule and deed as the dotted one.
	dotted := g.action(t, g.admin, metaOf(r)["com.deedglass/action"])
	_, r = rawCall(t, bg, g.base, "/mcp/native", g.key1, "fake_echo", `{"message":"native"}`)
	safe := g.action(t, g.admin, metaOf(r)["com.deedglass/action"])
	if string(r["isError"]) != "false" || safe["tool"] != "fake.echo" || dotted["tool"] != "fake.echo" ||
		fmt.Sprint(harnessGet(safe, "decision", "rule_id")) != fmt.Sprint(harnessGet(dotted, "decision", "rule_id")) {
		t.Fatalf("safe %v\ndotted %v", safe, dotted)
	}
	for _, a := range []map[string]any{safe, dotted} {
		req, _ := http.NewRequest("GET", g.base+"/v1/deeds/"+fmt.Sprint(a["deed"]), nil)
		req.Header.Set("Authorization", "Bearer "+g.admin)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 || !bytes.Contains(b, []byte(`"tool":"fake.echo"`)) {
			t.Fatalf("deed %v: %d %s", a["deed"], res.StatusCode, b)
		}
	}
	for _, c := range []struct {
		q    string
		want string
	}{{"", "fake.echo"}, {"?include_hidden=true", "fake.echo,fake.fail,fake.slow,fake.write"}} {
		req, _ := http.NewRequest("GET", g.base+"/v1/tools"+c.q, nil)
		req.Header.Set("Authorization", "Bearer "+g.key1)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var l struct {
			Object        string
			PolicyVersion string `json:"policy_version"`
			Data          []struct{ Name, Visibility string }
		}
		_ = json.NewDecoder(res.Body).Decode(&l)
		res.Body.Close()
		var names []string
		for _, d := range l.Data {
			names = append(names, d.Name)
		}
		if res.StatusCode != 200 || strings.Join(names, ",") != c.want || !strings.HasPrefix(l.PolicyVersion, "pv_") {
			t.Fatalf("%s: %d %v %s", c.q, res.StatusCode, names, l.PolicyVersion)
		}
	}

	// 9b. deedglass mcp serve: an SDK client over stdio, in both eras, through the bridge to
	// /mcp/native.
	for _, v := range []string{"2026-07-28", "2025-11-25"} {
		t.Run("mcp serve "+v, func(t *testing.T) {
			u, _ := url.Parse(g.base)
			b, err := bridge.New(bridge.Config{Server: u, APIKey: g.key1, Version: "test"})
			if err != nil {
				t.Fatal(err)
			}
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			done := make(chan error, 1)
			go func() { done <- b.Run(bg, inR, outW); outW.Close() }()
			c := mcp.NewClient(&mcp.Implementation{Name: "sdk-stdio", Version: "1"}, nil)
			s, err := c.Connect(bg, &mcp.IOTransport{Reader: outR, Writer: inW}, &mcp.ClientSessionOptions{ProtocolVersion: v})
			if err != nil {
				t.Fatal(err)
			}
			if names := toolNames(t, s); names != "fake_echo,deedglass_check_approval" {
				t.Fatalf("tools: %s", names)
			}
			res, err := s.CallTool(bg, &mcp.CallToolParams{Name: "fake_echo", Arguments: map[string]any{"message": "bridged"}})
			if err != nil || res.IsError || !strings.Contains(text(res), "bridged") {
				t.Fatalf("%v %+v", err, res)
			}
			_ = s.Close()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the bridge didn't exit on stdin EOF")
			}
		})
	}

	// 10. The upstream goes away: connector_unavailable, failed, nothing ran.
	g.legacy.Close()
	_, r = rawCall(t, bg, g.base, "/mcp/legacy", g.key1, "echo", `{"message":"down"}`)
	if !strings.Contains(resultText(r), `The "legacy" MCP server did not answer, so nothing was run.`) {
		t.Fatalf("%s", resultText(r))
	}
	if id := metaOf(r)["com.deedglass/action"]; id != "" {
		if a := g.action(t, g.key1, id); path(a, "execution", "status") != "failed" || path(a, "execution", "error_code") != "connector_unavailable" {
			t.Fatalf("%v", a)
		}
	}

	// 11. Credentials: the configured header reached the upstream; nothing the clients sent did,
	// and the value appears in no log line.
	if !g.modern.SawHeader("", "upstream-secret-7f3k") {
		t.Fatal("the configured upstream header never arrived")
	}
	for _, up := range []*mcptest.HTTPUpstream{g.modern, g.legacy} {
		for _, h := range up.Headers() {
			if h.Get("Cookie") != "" || strings.Contains(h.Get("Authorization"), "dg_") {
				t.Fatalf("forwarded a client credential: %v", h)
			}
			for k := range h {
				if strings.HasPrefix(strings.ToLower(k), "deedglass-") {
					t.Fatalf("forwarded %s", k)
				}
			}
		}
	}
	if strings.Contains(g.s.out.String()+g.s.err.String(), "upstream-secret") {
		t.Fatal("the upstream credential is in the server's output")
	}

	// 12. Sessionless: GET answers 405.
	req, _ = http.NewRequest("GET", g.base+"/mcp/native", nil)
	req.Header.Set("Authorization", "Bearer "+g.key1)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 405 || res.Header.Get("Allow") != "POST" {
		t.Fatalf("GET: %v %v", err, res)
	}
}

// TestGatewayBudget is S15 §6's benchmarks (make bench-gateway): /mcp/native fake.echo end to end
// on loopback, the gateway's added latency over calling an in-process upstream directly, and
// header validation plus envelope parsing of a 2 KiB body.
func TestGatewayBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("a budget run: make bench-gateway")
	}
	g := newGatewayLive(t)
	bg := context.Background()
	direct := func() *mcp.ClientSession {
		c := mcp.NewClient(&mcp.Implementation{Name: "bench", Version: "1"}, nil)
		s, err := c.Connect(bg, &mcp.StreamableClientTransport{Endpoint: g.modern.URL(), MaxRetries: -1}, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}()
	viaGate := sdkSession(t, g.base, "/mcp/everything", g.key1, "2026-07-28")
	native := sdkSession(t, g.base, "/mcp/native", g.key1, "2026-07-28")
	run := func(s *mcp.ClientSession, name string, n int) []time.Duration {
		out := make([]time.Duration, 0, n)
		for i := 0; i < n; i++ {
			t0 := time.Now()
			r, err := s.CallTool(bg, &mcp.CallToolParams{Name: name, Arguments: map[string]any{"message": "bench"}})
			out = append(out, time.Since(t0))
			if err != nil || r.IsError {
				t.Fatalf("%s: %v %+v", name, err, r)
			}
		}
		return out
	}
	q := func(d []time.Duration, p int) time.Duration {
		s := append([]time.Duration(nil), d...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return s[len(s)*p/100]
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	const n = 500
	run(direct, "echo", 50)
	run(viaGate, "echo", 50)
	run(native, "fake.echo", 50)
	dCall, gCall, nCall := run(direct, "echo", n), run(viaGate, "echo", n), run(native, "fake.echo", n)
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"message":%q},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`, strings.Repeat("x", 1900))
	hdr := http.Header{"Mcp-Protocol-Version": {"2026-07-28"}, "Mcp-Method": {"tools/call"}, "Mcp-Name": {"echo"}}
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			e, err := jsonrpc.Parse([]byte(body))
			if err != nil {
				b.Fatal(err)
			}
			if v := httpgw.Validate(hdr, e, func(string) json.RawMessage { return nil }); v.Err != nil {
				b.Fatal(string(v.Err.Marshal()))
			}
		}
	})
	validateUS := float64(res.NsPerOp()) / 1000
	add := func(p int) float64 { return ms(max(q(gCall, p)-q(dCall, p), 0)) }
	perf := map[string]any{"benchmark": "BenchmarkMCPGateway", "host_cpus": runtime.NumCPU(), "calls": n,
		"native_fake_echo_p50_ms": ms(q(nCall, 50)), "native_fake_echo_p99_ms": ms(q(nCall, 99)),
		"gateway_added_p50_ms": add(50), "gateway_added_p99_ms": add(99),
		"direct_p50_ms": ms(q(dCall, 50)), "gateway_p50_ms": ms(q(gCall, 50)),
		"validate_parse_2kib_us": validateUS,
		"target_native_p50_ms":   7, "target_native_p99_ms": 30, "target_added_p50_ms": 10, "target_added_p99_ms": 40, "target_validate_us": 50}
	b, _ := json.Marshal(perf)
	t.Logf("PERF %s", b)
	if out := os.Getenv("DEEDGLASS_GATEWAY_BENCH_OUT"); out != "" {
		_ = os.WriteFile(out, append(b, '\n'), 0o644)
	}
	if ms(q(nCall, 50)) > 7 || ms(q(nCall, 99)) > 30 || add(50) > 10 || add(99) > 40 || validateUS > 50 {
		t.Errorf("over budget: %s", b)
	}
}

// harnessGet walks nested JSON objects by key.
func harnessGet(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[k]
	}
	return v
}
