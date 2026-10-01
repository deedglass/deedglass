//go:build integration

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/mcp/wrap"
	"github.com/deedglass/deedglass/internal/store/storetest"
)

// TestMCPWrapLive is S14 §4 step 8 and §9 on a real server and database: the wrapper in front of
// the fake MCP server, the acme-demo policy enforced. tools/list hides what the policy always
// denies (POST /v1/tools/resolve); an allowed read is forwarded, reported and sealed L0 VERIFIED
// (edge_reported); a write outside /tmp/deedglass-demo/allowed is blocked and never forwarded; a
// hidden tool called by name is denied with a deed; actions list shows ✓ and ✕; the export
// verifies; and with the server stopped, a call fails closed.
// mcpLive is a real server with the acme-demo policy enforced and an agent key for claude-desktop.
type mcpLive struct {
	base, key string
	env       map[string]string
	s         *served
	stopped   bool
}

func (m *mcpLive) stop(t *testing.T) {
	if !m.stopped {
		m.stopped = true
		m.s.sigs <- syscall.SIGTERM
		m.s.wait(t)
	}
}

func newMCPLive(t *testing.T) *mcpLive {
	t.Helper()
	e := storetest.New(t, storetest.Options{NoMigrate: true})
	dir := t.TempDir()
	keyFile, _ := newKeyFile(t, dir, 9)
	env := map[string]string{"DEEDGLASS_DATABASE_URL": e.AppURL, "DEEDGLASS_MIGRATE_DATABASE_URL": e.MigratorURL,
		"DEEDGLASS_LISTEN": "127.0.0.1:0", "DEEDGLASS_LOG_LEVEL": "warn", "DEEDGLASS_SEAL_KEY_FILE": keyFile,
		"DEEDGLASS_API_KEY_PEPPER": key32, "DEEDGLASS_VAULT_KEY": key32}
	s := startServe(t, env, "--migrate")
	var base string
	select {
	case a := <-s.addr:
		base = "http://" + a.String()
	case c := <-s.code:
		t.Fatalf("exited %d\n%s%s", c, s.out, s.err)
	case <-time.After(30 * time.Second):
		t.Fatal("serve never listened")
	}
	m := &mcpLive{base: base, env: env, s: s}
	t.Cleanup(func() { m.stop(t) })
	env["DEEDGLASS_BASE_URL"], env["DEEDGLASS_SERVER"] = base, base
	r := cli(t, env, false, "org", "create", "--name", "Acme Robotics", "--slug", "acme", "--owner-email", "priya@acme.test", "--admin-key", "--json")
	var created struct {
		AdminKey struct{ Secret string } `json:"admin_key"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &created) != nil {
		t.Fatalf("org create: %+v", r)
	}
	env["DEEDGLASS_API_KEY"] = created.AdminKey.Secret
	if r = cli(t, env, false, "policy", "push", filepath.Join("..", "..", "examples", "demo", "policies", "acme-demo.yaml")); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = cli(t, env, false, "policy", "activate", "acme-demo", "--version", "1", "--mode", "enforce", "--reason", "MCP demo", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r = cli(t, env, false, "--json", "agent", "create", "claude-desktop"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	r = cli(t, env, false, "--json", "agent", "keys", "create", "claude-desktop")
	var k struct{ Secret string }
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &k) != nil || !strings.HasPrefix(k.Secret, "dg_") {
		t.Fatalf("%+v", r)
	}

	m.key = k.Secret
	return m
}

// mcpClient drives a wrapper over pipes, as an MCP client would.
type mcpClient struct {
	t    *testing.T
	in   *io.PipeWriter
	msgs chan jsonrpc.Envelope
	done chan error
	id   int
}

type mcpToolResult struct {
	Content []struct{ Text string }
	IsError bool              `json:"isError"`
	Meta    map[string]string `json:"_meta"`
}

func newMCPClient(t *testing.T, cfg wrap.Config) *mcpClient {
	t.Helper()
	w, err := wrap.New(cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &mcpClient{t: t, in: inW, msgs: make(chan jsonrpc.Envelope, 64), done: make(chan error, 1)}
	go func() { c.done <- w.Run(context.Background(), inR, outW); outW.Close() }()
	go func() {
		br := bufio.NewReaderSize(outR, 1<<20)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				e, perr := jsonrpc.Parse([]byte(strings.TrimRight(string(line), "\n")))
				if perr != nil {
					t.Errorf("stdout: %v %q", perr, line)
					continue
				}
				c.msgs <- e
			}
			if err != nil {
				close(c.msgs)
				return
			}
		}
	}()
	return c
}

func (c *mcpClient) rpc(method, params string) jsonrpc.Envelope {
	c.t.Helper()
	c.id++
	fmt.Fprintf(c.in, `{"jsonrpc":"2.0","id":%d,"method":%q,"params":{%s}}`+"\n", c.id, method, params)
	for {
		select {
		case e := <-c.msgs:
			if string(e.ID()) == fmt.Sprint(c.id) {
				return e
			}
		case <-time.After(60 * time.Second):
			c.t.Fatalf("no answer to %s", method)
		}
	}
}

func (c *mcpClient) call(name, args string) mcpToolResult {
	c.t.Helper()
	e := c.rpc("tools/call", fmt.Sprintf(`"name":%q,"arguments":%s`, name, args))
	var x mcpToolResult
	if json.Unmarshal(e["result"], &x) != nil || len(x.Content) == 0 {
		c.t.Fatalf("%s", e.Marshal())
	}
	return x
}

func (c *mcpClient) initialize() {
	c.rpc("initialize", `"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"claude-desktop","version":"1.0"}`)
	fmt.Fprintln(c.in, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
}

func (c *mcpClient) close() {
	c.t.Helper()
	c.in.Close()
	select {
	case err := <-c.done:
		if err != nil {
			c.t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		c.t.Fatal("the wrapper didn't exit")
	}
}

func TestMCPWrapLive(t *testing.T) {
	m := newMCPLive(t)
	base, env, k := m.base, m.env, struct{ Secret string }{m.key}
	dir := t.TempDir()
	// POST /v1/tools/resolve directly, checked against the spec.
	req, _ := http.NewRequest("POST", base+"/v1/tools/resolve", strings.NewReader(
		`{"connector":"scratch","tools":[{"name":"read_text_file"},{"name":"move_file"},{"name":"write_file"}]}`))
	req.Header.Set("Authorization", "Bearer "+k.Secret)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var vis gate.ToolVisibilityList
	if res.StatusCode != 200 || json.Unmarshal(body, &vis) != nil || len(vis.Data) != 3 || vis.Data[1].Visibility != "hidden" ||
		vis.Data[0].Visibility != "visible" || *vis.Data[0].RuleID != "allow-scratch-reads" || vis.PolicyVersion == nil {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	for _, err := range httpx.CheckRequired(apiSpec(), "ToolVisibilityList", body) {
		t.Error(err)
	}

	// The wrapper, in-process, in front of this test binary as the fake MCP server.
	calls := filepath.Join(dir, "calls.log")
	u, _ := url.Parse(base)
	c := newMCPClient(t, wrap.Config{Connector: "scratch", Server: u, APIKey: k.Secret, HideDenied: true, Version: "test",
		Upstream: []string{os.Args[0]}, Env: append(os.Environ(), "FAKE_MCP_MODE=legacy", "FAKE_MCP_LOG="+calls),
		ShutdownWait: time.Second, KillWait: time.Second})
	rpc, callTool := c.rpc, c.call
	c.initialize()
	list := rpc("tools/list", "")
	ls := string(list["result"])
	if !strings.Contains(ls, `"read_text_file"`) || !strings.Contains(ls, `"write_file"`) || strings.Contains(ls, `"move_file"`) ||
		strings.Contains(ls, `"echo"`) || !strings.Contains(ls, `"deedglass.check_approval"`) {
		t.Fatalf("tools/list: %s", ls)
	}
	read := callTool("read_text_file", `{"path":"/tmp/deedglass-demo/allowed/hello.txt"}`)
	act := read.Meta["com.deedglass/action"]
	if read.IsError || !strings.HasPrefix(act, "act_") {
		t.Fatalf("%+v", read)
	}
	deny := callTool("write_file", `{"path":"/tmp/deedglass-demo/outside.txt","content":"test"}`)
	if !deny.IsError || !strings.HasPrefix(deny.Content[0].Text, `Blocked by policy "scratch-writes-only-in-allowed"`) ||
		!strings.Contains(deny.Content[0].Text, "Nothing was run.") || !strings.HasPrefix(deny.Meta["com.deedglass/deed"], "deed_") {
		t.Fatalf("%+v", deny)
	}
	hidden := callTool("move_file", `{"source":"a","destination":"b"}`)
	if !hidden.IsError || !strings.HasPrefix(hidden.Meta["com.deedglass/deed"], "deed_") {
		t.Fatalf("%+v", hidden)
	}
	// The read's report lands and seals it L0 VERIFIED, as the edge's word.
	deadline := time.Now().Add(15 * time.Second)
	var shown result
	for time.Now().Before(deadline) {
		shown = cli(t, env, false, "--json", "actions", "show", act)
		if strings.Contains(shown.stdout, `"state": "sealed"`) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(shown.stdout, `"state": "sealed"`) || !strings.Contains(shown.stdout, `"verdict": "VERIFIED"`) ||
		!strings.Contains(shown.stdout, `"method": "edge_reported"`) {
		t.Fatalf("%+v", shown)
	}
	lr := cli(t, env, false, "actions", "list", "--limit", "5")
	if lr.code != 0 || !strings.Contains(lr.stdout, "✓") || !strings.Contains(lr.stdout, "✕") || !strings.Contains(lr.stdout, "scratch.write_file") {
		t.Fatalf("%+v", lr)
	}
	er := cli(t, env, false, "deeds", "export", "--since", "1h", "--format", "jsonl", "--out", "-")
	vr := cliIn(t, []byte(er.stdout), env, false, "verify", "-", "--keys", base+"/.well-known/deedglass/keys.json")
	if er.code != 0 || vr.code != 0 || !strings.Contains(vr.stdout, "chain intact") {
		t.Fatalf("verify: %+v %+v", er, vr)
	}
	if got := readCalls(calls); got != "read_text_file" {
		t.Fatalf("the server ran %q", got)
	}

	// The server stops: the same write fails closed with the §9 message; nothing reaches the server.
	m.stop(t)
	down := callTool("write_file", `{"path":"/tmp/deedglass-demo/allowed/a.txt","content":"x"}`)
	if !down.IsError || !strings.HasPrefix(down.Content[0].Text, "Deedglass could not be reached at "+base+", so this was not run.") {
		t.Fatalf("%+v", down)
	}
	if got := readCalls(calls); got != "read_text_file" {
		t.Fatalf("the server ran %q", got)
	}
	c.close()
}

func readCalls(p string) string {
	b, _ := os.ReadFile(p)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(l, "call ") {
			out = append(out, strings.TrimPrefix(l, "call "))
		}
	}
	return strings.Join(out, ",")
}

// filesystemServer is the @modelcontextprotocol/server-filesystem version S14 verified (npm,
// 2026-10-01). make mcp-it runs it through npx.
const filesystemServer = "@modelcontextprotocol/server-filesystem@2026.8.31"

// TestMCPWrapNodeLive is S14 §9's make mcp-it: the real filesystem server via npx behind the
// wrapper, the acme-demo policy enforced. It runs only with DEEDGLASS_IT_NODE=1 and node on PATH.
func TestMCPWrapNodeLive(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_NODE") != "1" {
		t.Skip("set DEEDGLASS_IT_NODE=1 (make mcp-it) to run the real filesystem server through npx")
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		t.Skip("npx isn't on PATH: install Node.js 24 LTS to run this test")
	}
	if err := os.MkdirAll("/tmp/deedglass-demo/allowed", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/deedglass-demo/allowed/hello.txt", []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat("/tmp/deedglass-demo/outside.txt")
	existedBefore := statErr == nil
	m := newMCPLive(t)
	u, _ := url.Parse(m.base)
	c := newMCPClient(t, wrap.Config{Connector: "scratch", Server: u, APIKey: m.key, HideDenied: true, Version: "test",
		Upstream: []string{npx, "-y", filesystemServer, "/tmp/deedglass-demo"}, Env: os.Environ(),
		ShutdownWait: 2 * time.Second, KillWait: time.Second})
	c.initialize()
	ls := string(c.rpc("tools/list", "")["result"])
	if !strings.Contains(ls, `"deedglass.check_approval"`) || !strings.Contains(ls, `"read_text_file"`) || strings.Contains(ls, `"move_file"`) {
		t.Fatalf("tools/list: %s", ls)
	}
	read := c.call("read_text_file", `{"path":"/tmp/deedglass-demo/allowed/hello.txt"}`)
	if read.IsError || !strings.Contains(read.Content[0].Text, "hello") {
		t.Fatalf("%+v", read)
	}
	deny := c.call("write_file", `{"path":"/tmp/deedglass-demo/outside.txt","content":"test"}`)
	if !deny.IsError || !strings.HasPrefix(deny.Content[0].Text, `Blocked by policy "scratch-writes-only-in-allowed"`) {
		t.Fatalf("%+v", deny)
	}
	if _, err := os.Stat("/tmp/deedglass-demo/outside.txt"); err == nil && !existedBefore {
		t.Fatal("outside.txt exists after a blocked write")
	}
	m.stop(t)
	down := c.call("write_file", `{"path":"/tmp/deedglass-demo/outside.txt","content":"test"}`)
	if !down.IsError || !strings.HasPrefix(down.Content[0].Text, "Deedglass could not be reached at "+m.base+", so this was not run.") {
		t.Fatalf("%+v", down)
	}
	if _, err := os.Stat("/tmp/deedglass-demo/outside.txt"); err == nil && !existedBefore {
		t.Fatal("outside.txt exists after a fail-closed write")
	}
	c.close()
}

// TestMCPBudget is S14 §3.10 (make bench-mcp): the wrapper's overhead on loopback, against a real
// Gate on a local Postgres 18 and the fake MCP server. Overheads are differences between the same
// request through the wrapper and straight to the server.
func TestMCPBudget(t *testing.T) {
	if os.Getenv("DEEDGLASS_IT_BENCH") != "1" {
		t.Skip("a budget run: make bench-mcp")
	}
	m := newMCPLive(t)
	u, _ := url.Parse(m.base)
	env := append(os.Environ(), "FAKE_MCP_MODE=legacy")
	// Straight to the server.
	direct := exec.Command(os.Args[0])
	direct.Env = env
	din, _ := direct.StdinPipe()
	dout, _ := direct.StdoutPipe()
	if err := direct.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { din.Close(); _ = direct.Wait() }()
	dr := bufio.NewReaderSize(dout, 1<<20)
	directRTT := func(method, params string, n int) []time.Duration {
		out := make([]time.Duration, 0, n)
		for i := 0; i < n; i++ {
			t0 := time.Now()
			fmt.Fprintf(din, `{"jsonrpc":"2.0","id":%d,"method":%q,"params":{%s}}`+"\n", i, method, params)
			if _, err := dr.ReadBytes('\n'); err != nil {
				t.Fatal(err)
			}
			out = append(out, time.Since(t0))
		}
		return out
	}
	c := newMCPClient(t, wrap.Config{Connector: "scratch", Server: u, APIKey: m.key, HideDenied: true, Version: "bench",
		Upstream: []string{os.Args[0]}, Env: env, ShutdownWait: time.Second, KillWait: time.Second})
	c.initialize()
	c.rpc("tools/list", "")
	wrapped := func(method, params string, n int) []time.Duration {
		out := make([]time.Duration, 0, n)
		for i := 0; i < n; i++ {
			t0 := time.Now()
			e := c.rpc(method, params)
			out = append(out, time.Since(t0))
			if method == "tools/call" && strings.Contains(string(e["result"]), `"isError":true`) {
				t.Fatalf("%s", e.Marshal())
			}
		}
		return out
	}
	q := func(d []time.Duration, p int) time.Duration {
		s := append([]time.Duration(nil), d...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		return s[len(s)*p/100]
	}
	const n = 1000
	directRTT("ping", "", 100)
	wrapped("ping", "", 100)
	dPing, wPing := directRTT("ping", "", n), wrapped("ping", "", n)
	call := `"name":"read_text_file","arguments":{"path":"/tmp/deedglass-demo/allowed/hello.txt"}`
	directRTT("tools/call", call, 50)
	wrapped("tools/call", call, 50)
	dCall, wCall := directRTT("tools/call", call, 500), wrapped("tools/call", call, 500)
	dList, wList := directRTT("tools/list", "", 300), wrapped("tools/list", "", 300)
	// Memory: the wrapper as its own process (this test binary running the CLI), 100 concurrent
	// calls with 64 KiB arguments, its peak RSS sampled with ps.
	rssMiB := wrapperRSS(t, m)
	sub := func(a, b time.Duration) float64 { return float64(max(a-b, 0).Microseconds()) / 1000 }
	perf := map[string]any{"benchmark": "BenchmarkMCPWrap", "host_cpus": runtime.NumCPU(),
		"relay_passthrough_overhead_p50_ms": sub(q(wPing, 50), q(dPing, 50)), "relay_passthrough_overhead_p99_ms": sub(q(wPing, 99), q(dPing, 99)),
		"tools_call_added_p50_ms": sub(q(wCall, 50), q(dCall, 50)), "tools_call_added_p99_ms": sub(q(wCall, 99), q(dCall, 99)),
		"tools_list_added_p50_ms":        sub(q(wList, 50), q(dList, 50)),
		"wrapper_peak_rss_mib_100x64kib": rssMiB,
		"target_relay_p99_ms":            1, "target_call_p50_ms": 8, "target_call_p99_ms": 30, "target_list_p50_ms": 10, "target_mem_mib": 64}
	b, _ := json.Marshal(perf)
	t.Logf("PERF %s", b)
	if out := os.Getenv("DEEDGLASS_MCP_BENCH_OUT"); out != "" {
		_ = os.WriteFile(out, append(b, '\n'), 0o644)
	}
	if sub(q(wPing, 99), q(dPing, 99)) > 1 || sub(q(wCall, 50), q(dCall, 50)) > 8 || sub(q(wCall, 99), q(dCall, 99)) > 30 ||
		sub(q(wList, 50), q(dList, 50)) > 10 || rssMiB > 64 {
		t.Errorf("over budget: %s", b)
	}
	c.close()
}

// wrapperRSS runs `deedglass mcp wrap` as a separate process (this test binary with
// DEEDGLASS_IT_CLI=1, which the wrapper strips before starting its own child), sends 100
// concurrent 64 KiB calls, and returns its peak resident set in MiB.
func wrapperRSS(t *testing.T, m *mcpLive) float64 {
	t.Helper()
	cmd := exec.Command(os.Args[0], "mcp", "wrap", "--connector", "scratch", "--no-log-file", "--", os.Args[0])
	cmd.Env = append(os.Environ(), "DEEDGLASS_IT_CLI=1", "FAKE_MCP_MODE=legacy", "DEEDGLASS_SERVER="+m.base, "DEEDGLASS_API_KEY="+m.key)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); _ = cmd.Wait() }()
	br := bufio.NewReaderSize(out, 1<<20)
	send := func(s string) { fmt.Fprintln(in, s) }
	read := func() string {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"bench","version":"1"}}}`)
	read()
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	read()
	var peak float64
	if o, err := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(cmd.Process.Pid)).Output(); err == nil {
		var kb float64
		fmt.Sscan(strings.TrimSpace(string(o)), &kb)
		t.Logf("wrapper idle RSS %.1f MiB", kb/1024)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if o, err := exec.Command("ps", "-o", "rss=", "-p", fmt.Sprint(cmd.Process.Pid)).Output(); err == nil {
				var kb float64
				fmt.Sscan(strings.TrimSpace(string(o)), &kb)
				peak = max(peak, kb/1024)
			}
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	big := strings.Repeat("x", 64<<10)
	for i := 0; i < 100; i++ {
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"read_text_file","arguments":{"path":"/tmp/deedglass-demo/allowed/hello.txt","pad":%q}}}`, 100+i, big))
	}
	for i := 0; i < 100; i++ {
		if l := read(); strings.Contains(l, `"isError":true`) {
			t.Fatalf("%.300s", l)
		}
	}
	close(stop)
	<-done
	return peak
}
