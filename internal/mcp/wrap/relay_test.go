package wrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
)

func eras(t *testing.T, f func(t *testing.T, mode string)) {
	for _, m := range []string{"legacy", "modern"} {
		t.Run(m, func(t *testing.T) { f(t, m) })
	}
}

// TestHandshake: initialize (legacy) and server/discover (modern) are forwarded; the answer's
// instructions gain Deedglass's line and capabilities.tools is present; supportedVersions untouched.
func TestHandshake(t *testing.T) {
	eras(t, func(t *testing.T, mode string) {
		h := start(t, mode)
		e := h.handshake()
		var r struct {
			Instructions      string          `json:"instructions"`
			Capabilities      map[string]any  `json:"capabilities"`
			SupportedVersions json.RawMessage `json:"supportedVersions"`
		}
		if err := json.Unmarshal(e["result"], &r); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(r.Instructions, gate.MCPInstructions) || r.Capabilities["tools"] == nil {
			t.Fatalf("%s", e["result"])
		}
		if mode == "legacy" && !strings.HasPrefix(r.Instructions, "Fake server.") {
			t.Fatal(r.Instructions)
		}
		if mode == "modern" && string(r.SupportedVersions) != `["2026-07-28"]` {
			t.Fatalf("supportedVersions %s", r.SupportedVersions)
		}
	})
}

// TestToolsList: denied tools hidden, deedglass.* from the server hidden, check_approval once,
// cacheScope private and ttlMs capped for modern requests.
func TestToolsList(t *testing.T) {
	eras(t, func(t *testing.T, mode string) {
		h := start(t, mode)
		h.handshake()
		e := h.request("tools/list", "")
		names := toolNames(t, e)
		if slices.Contains(names, "move_file") || slices.Contains(names, "deedglass.evil") || !slices.Contains(names, "echo") {
			t.Fatalf("%v", names)
		}
		n := 0
		for _, x := range names {
			if x == "deedglass.check_approval" {
				n++
			}
		}
		if n != 1 || names[len(names)-1] != "deedglass.check_approval" {
			t.Fatalf("check_approval %d times: %v", n, names)
		}
		var r struct {
			CacheScope *string `json:"cacheScope"`
			TTL        *int64  `json:"ttlMs"`
		}
		_ = json.Unmarshal(e["result"], &r)
		if mode == "modern" && (r.CacheScope == nil || *r.CacheScope != "private" || r.TTL == nil || *r.TTL != 60000) {
			t.Fatalf("modern cache hints: %s", e["result"])
		}
		if mode == "legacy" && (r.CacheScope != nil || r.TTL != nil) {
			t.Fatalf("legacy gained cache hints: %s", e["result"])
		}
		// A page after the first doesn't repeat the synthetic tool.
		e = h.request("tools/list", `"cursor":"page-2"`)
		if slices.Contains(toolNames(t, e), "deedglass.check_approval") {
			t.Fatal("check_approval on a later page")
		}
		if !strings.Contains(h.stderr.String(), "◐ 1 tool hidden by policy · policy pv_01k6w4a3d5f7h9k1n3q5s7v9x1 · scratch.move_file") {
			t.Fatalf("log:\n%s", h.stderr)
		}
	})
}

// TestToolChanged: a definition that changes after the first listing is hidden and refused.
func TestToolChanged(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	h.request("tools/list", "")
	if r := result(t, h.call("changed", `{}`)); r.IsError {
		t.Fatalf("first call: %s", r.text())
	}
	e := h.request("tools/list", "")
	if slices.Contains(toolNames(t, e), "changed") {
		t.Fatal("changed tool still listed")
	}
	before := len(h.g.submits)
	r := result(t, h.call("changed", `{}`))
	if !r.IsError || r.text() != gate.MCPToolChangedText("scratch.changed") || len(h.g.submits) != before {
		t.Fatalf("%v %q", r.IsError, r.text())
	}
	if c := h.serverCalls(); len(c) != 1 {
		t.Fatalf("server calls %v", c)
	}
}

// TestAllowedCall: the result reaches the client unchanged except _meta, and the report lands.
func TestAllowedCall(t *testing.T) {
	eras(t, func(t *testing.T, mode string) {
		h := start(t, mode)
		h.handshake()
		e := h.call("echo", `{"message":"hi","n":1.50}`)
		r := result(t, e)
		if r.IsError || r.text() != `ran echo {"message":"hi","n":1.50}` {
			t.Fatalf("%s", e.Marshal())
		}
		act := r.Meta["com.deedglass/action"]
		if !strings.HasPrefix(act, "act_") {
			t.Fatalf("meta %v", r.Meta)
		}
		if mode == "modern" && r.ResultType != "complete" {
			t.Fatal("resultType")
		}
		rep := h.g.waitReport(t, act)
		if rep.Status != gate.ExecSucceeded || rep.ResultDigest == "" || len(rep.Result) == 0 || rep.StartedAt.IsZero() {
			t.Fatalf("report %+v", rep)
		}
		if c := h.serverCalls(); !slices.Equal(c, []string{"echo"}) {
			t.Fatalf("server calls %v", c)
		}
		if !strings.Contains(h.stderr.String(), "✓ scratch.echo · allowed · ") {
			t.Fatalf("log:\n%s", h.stderr)
		}
	})
}

// TestDeniedCall: not forwarded, the deny message, its _meta.
func TestDeniedCall(t *testing.T) {
	eras(t, func(t *testing.T, mode string) {
		h := start(t, mode)
		h.handshake()
		r := result(t, h.call("write_file", `{"path":"/tmp/deedglass-demo/outside.txt","content":"x"}`))
		want := denyMsg + " Nothing was run. Deedglass sealed a deed of this attempt (deed_01k6w4a3d5f7h9k1n3q5s7v9x1). If this should be allowed, a person can change the policy in Console › Policies."
		if !r.IsError || r.text() != want {
			t.Fatalf("%q", r.text())
		}
		if r.Meta["com.deedglass/decision"] != "deny" || r.Meta["com.deedglass/rule"] != "scratch-writes-only-in-allowed" || r.Meta["com.deedglass/deed"] == "" {
			t.Fatalf("%v", r.Meta)
		}
		if c := h.serverCalls(); len(c) != 0 {
			t.Fatalf("forwarded: %v", c)
		}
		if !strings.Contains(h.stderr.String(), "✕ scratch.write_file · denied · scratch-writes-only-in-allowed · act_") {
			t.Fatalf("log:\n%s", h.stderr)
		}
	})
}

// TestHiddenToolCalledByName: still goes to the Gate (recorded), never to the server.
func TestHiddenToolCalledByName(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	h.request("tools/list", "")
	r := result(t, h.call("move_file", `{"source":"a","destination":"b"}`))
	if !r.IsError || !strings.HasPrefix(r.text(), `Blocked by policy "acme-demo"`) || !slices.Contains(h.g.submits, "scratch.move_file") {
		t.Fatalf("%q %v", r.text(), h.g.submits)
	}
	if len(h.serverCalls()) != 0 {
		t.Fatal("forwarded")
	}
}

// TestUnknownAndReserved: an unknown tool is -32602 and not recorded; deedglass.* is reserved.
func TestUnknownAndReserved(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	for _, name := range []string{"nope", "deedglass.evil"} {
		e := h.call(name, `{}`)
		var er jsonrpc.ErrorObject
		if json.Unmarshal(e["error"], &er) != nil || er.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("%s: %s", name, e.Marshal())
		}
	}
	if len(h.g.submits) != 0 || len(h.serverCalls()) != 0 {
		t.Fatal("recorded or forwarded")
	}
}

// TestCallBeforeList: the wrapper lists the tools itself, then decides.
func TestCallBeforeList(t *testing.T) {
	eras(t, func(t *testing.T, mode string) {
		h := start(t, mode)
		h.handshake()
		if r := result(t, h.call("echo", `{}`)); r.IsError {
			t.Fatal(r.text())
		}
	})
}

// TestFailClosed: Gate down, a garbage 200, a 401 and a 500: nothing forwarded, calm messages.
func TestFailClosed(t *testing.T) {
	cases := []struct {
		name string
		mode string
		want string
	}{
		{"unexpected 200", "garbage", "unreachable"},
		{"401", "401", gate.MCPBadKey},
		{"500", "500", "unreachable"},
		{"down", "down", "unreachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, "legacy")
			h.handshake()
			h.request("tools/list", "")
			if c.mode == "down" {
				h.g.srv.Close()
			} else {
				h.g.set(func() { h.g.mode = c.mode })
			}
			r := result(t, h.call("write_file", `{"path":"/tmp/deedglass-demo/allowed/a.txt","content":"x"}`))
			want := c.want
			if want == "unreachable" {
				want = gate.UnreachableMessage(h.g.srv.URL)
			}
			if !r.IsError || r.text() != want {
				t.Fatalf("%q", r.text())
			}
			if len(h.serverCalls()) != 0 {
				t.Fatal("forwarded without a decision")
			}
		})
	}
}

// TestFailOpen: dev only, forwards with a warning per call and no report.
func TestFailOpen(t *testing.T) {
	g := newFakeGate(t)
	h := startWith(t, g, "legacy", func(c *Config) { c.FailOpen, c.DevEnv = true, true })
	h.handshake()
	h.request("tools/list", "")
	g.srv.Close()
	r := result(t, h.call("echo", `{}`))
	if r.IsError || len(h.serverCalls()) != 1 || !strings.Contains(h.stderr.String(), "✕ fail-open: Deedglass unreachable; scratch.echo ran without a decision or a deed") {
		t.Fatalf("%q\n%s", r.text(), h.stderr)
	}
	cfg := Config{Connector: "scratch", Server: mustURL("http://127.0.0.1:1"), FailOpen: true, Upstream: []string{"x"}}
	if err := cfg.Validate(); err != ErrFailOpenProd {
		t.Fatal(err)
	}
}

// TestPendingThenApproval: pending → check (still pending) → approve → check runs the original
// call exactly once → a second check says it already ran. Plus denied, expired and cancelled.
func TestPendingThenApproval(t *testing.T) {
	eras(t, func(t *testing.T, mode string) {
		h := start(t, mode)
		h.handshake()
		h.request("tools/list", "")
		r := result(t, h.call("write_file", `{"path":"/tmp/deedglass-demo/allowed/a.txt","approve":true}`))
		apr := r.Meta["com.deedglass/approval"]
		want := fmt.Sprintf(`Waiting for approval under policy "writes-need-approval": Writes wait for a human. Nothing has run yet. Approval %s expires at 10:42 UTC. To continue, call deedglass.check_approval with {"approval_id": "%s"}.`, apr, apr)
		if !r.IsError || r.text() != want {
			t.Fatalf("%q", r.text())
		}
		check := func() toolResult {
			return result(t, h.call("deedglass.check_approval", `{"approval_id":"`+apr+`"}`))
		}
		r = check()
		if r.IsError || !strings.HasPrefix(r.text(), "Still waiting for approval "+apr+". It expires at 10:42 UTC.") || r.Structured["status"] != "pending_approval" {
			t.Fatalf("%q %v", r.text(), r.Structured)
		}
		act := r.Meta["com.deedglass/action"]
		h.g.set(func() { h.g.actions[act].Status = "authorized" })
		r = check()
		if r.IsError || !strings.HasPrefix(r.text(), "Approved by a person. Ran scratch.write_file.\nran write_file") {
			t.Fatalf("%q", r.text())
		}
		h.g.waitReport(t, act)
		r = check()
		if r.IsError || !strings.HasPrefix(r.text(), "This action already ran (deed_") {
			t.Fatalf("%q", r.text())
		}
		if c := h.serverCalls(); !slices.Equal(c, []string{"write_file"}) {
			t.Fatalf("server calls %v", c)
		}
		for status, prefix := range map[string]string{"denied": gate.MCPApprovalDenied, "expired": "Approval apr_", "cancelled": "Approval apr_"} {
			r := result(t, h.call("write_file", `{"path":"/tmp/deedglass-demo/allowed/b.txt","approve":true}`))
			apr, act := r.Meta["com.deedglass/approval"], r.Meta["com.deedglass/action"]
			h.g.set(func() { h.g.actions[act].Status = status })
			r = result(t, h.call("deedglass.check_approval", `{"approval_id":"`+apr+`"}`))
			if !r.IsError || !strings.HasPrefix(r.text(), prefix) || !strings.Contains(r.text(), "Nothing was run.") {
				t.Fatalf("%s: %q", status, r.text())
			}
		}
		// Not from this session (the Gate doesn't know it for this agent), and a malformed id.
		r = result(t, h.call("deedglass.check_approval", `{"approval_id":"apr_01k6w4a3d5f7h9k1n3q5s7v999"}`))
		if !r.IsError || r.text() != gate.MCPApprovalUnknownText("apr_01k6w4a3d5f7h9k1n3q5s7v999") {
			t.Fatal(r.text())
		}
		r = result(t, h.call("deedglass.check_approval", `{"approval_id":"nope"}`))
		if !r.IsError || r.text() != gate.MCPApprovalIDBad {
			t.Fatal(r.text())
		}
		if c := h.serverCalls(); len(c) != 1 {
			t.Fatalf("server calls %v", c)
		}
	})
}

// TestProgressPassesThrough: three progress notifications reach the client before the result.
func TestProgressPassesThrough(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	e := h.request("tools/call", `"name":"progress","arguments":{},"_meta":{"progressToken":"tok-1"}`)
	if result(t, e).IsError {
		t.Fatal("error")
	}
	n := 0
	for _, o := range h.others {
		if o.Method() == "notifications/progress" && strings.Contains(string(o["params"]), `"progressToken":"tok-1"`) {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d progress notifications", n)
	}
}

// TestCancel: before the Gate answers, nothing is forwarded and the action is reported failed;
// after forwarding, the server gets notifications/cancelled with its own id and the report is unknown.
func TestCancel(t *testing.T) {
	t.Run("after forwarding", func(t *testing.T) {
		h := start(t, "legacy")
		h.handshake()
		h.request("tools/list", "")
		h.sendRequest(50, "tools/call", `"name":"slow","arguments":{}`)
		deadline := time.Now().Add(5 * time.Second)
		for len(h.serverCalls()) == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		h.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":50,"reason":"user"}}`)
		h.expectNone("50", 2500*time.Millisecond)
		var act string
		h.g.set(func() {
			for id, a := range h.g.actions {
				if a.Tool == "scratch.slow" {
					act = id
				}
			}
		})
		rep := h.g.waitReport(t, act)
		if rep.Status != gate.ExecUnknown || rep.ErrorCode != "client_cancelled" {
			t.Fatalf("%+v", rep)
		}
		log := h.serverLog()
		if !strings.Contains(log, `cancelled {"reason":"user","requestId":`) || strings.Contains(log, `"requestId":50`) {
			t.Fatalf("server log:\n%s", log)
		}
	})
	t.Run("before the Gate answers", func(t *testing.T) {
		g := newFakeGate(t)
		slow := make(chan struct{})
		inner := g.srv.Config.Handler
		g.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/v1/actions" {
				<-slow
			}
			inner.ServeHTTP(w, r)
		})
		h := startWith(t, g, "legacy")
		h.handshake()
		h.request("tools/list", "")
		h.sendRequest(60, "tools/call", `"name":"echo","arguments":{}`)
		time.Sleep(200 * time.Millisecond)
		h.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":60}}`)
		time.Sleep(100 * time.Millisecond)
		close(slow)
		h.expectNone("60", 500*time.Millisecond)
		var act string
		g.set(func() {
			for id := range g.actions {
				act = id
			}
		})
		rep := g.waitReport(t, act)
		if rep.Status != gate.ExecFailed || rep.ErrorCode != "client_cancelled" || len(h.serverCalls()) != 0 {
			t.Fatalf("%+v %v", rep, h.serverCalls())
		}
	})
}

// TestCrashAndRestart: the call that crashed the server is UNVERIFIABLE; the server restarts, the
// legacy initialize is replayed, and the next call works. Five crashes in a minute stop restarts.
func TestCrashAndRestart(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	h.request("tools/list", "")
	r := result(t, h.call("crash", `{}`))
	act := r.Meta["com.deedglass/action"]
	if !r.IsError || r.text() != gate.MCPCrashedText("scratch.crash", act) {
		t.Fatalf("%q", r.text())
	}
	if rep := h.g.waitReport(t, act); rep.Status != gate.ExecUnknown || rep.ErrorCode != "upstream_crashed" {
		t.Fatalf("%+v", rep)
	}
	if r := result(t, h.call("echo", `{}`)); r.IsError {
		t.Fatalf("after restart: %q", r.text())
	}
	if n := strings.Count(h.serverLog(), "initialize"); n != 2 {
		t.Fatalf("initialize %d times:\n%s", n, h.serverLog())
	}
	for i := 0; i < 4; i++ {
		r := result(t, h.call("crash", `{}`))
		if !r.IsError {
			t.Fatal("crash")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	r = result(t, h.call("echo", `{}`))
	if !r.IsError || r.text() != gate.MCPKeepsCrashing {
		t.Fatalf("%q\n%s", r.text(), h.stderr)
	}
	e := h.request("ping", "")
	var er jsonrpc.ErrorObject
	if json.Unmarshal(e["error"], &er) != nil || er.Message != gate.MCPKeepsCrashing {
		t.Fatalf("%s", e.Marshal())
	}
}

// TestInputRequired: an MRTR interim result passes through and is reported unknown.
func TestInputRequired(t *testing.T) {
	h := start(t, "modern")
	h.handshake()
	e := h.call("input_required", `{}`)
	r := result(t, e)
	if r.ResultType != "input_required" {
		t.Fatalf("%s", e.Marshal())
	}
	rep := h.g.waitReport(t, r.Meta["com.deedglass/action"])
	if rep.Status != gate.ExecUnknown || rep.ErrorCode != "input_required" {
		t.Fatalf("%+v", rep)
	}
}

// TestSampling: with --allow-server-requests sampling, a legacy server-to-client request passes to
// the client and the client's answer goes back with the server's own id.
func TestSampling(t *testing.T) {
	h := start(t, "legacy", func(c *Config) { c.AllowServerRequests = []string{"sampling"} })
	h.handshake()
	h.sendRequest(70, "tools/call", `"name":"sample","arguments":{}`)
	var srvReq jsonrpc.Envelope
	deadline := time.After(10 * time.Second)
	for srvReq == nil {
		select {
		case e := <-h.msgs:
			if e.Method() == "sampling/createMessage" {
				srvReq = e
			} else {
				h.others = append(h.others, e)
			}
		case <-deadline:
			t.Fatal("no sampling request")
		}
	}
	h.send(`{"jsonrpc":"2.0","id":` + string(srvReq.ID()) + `,"result":{"role":"assistant","content":{"type":"text","text":"ok"}}}`)
	if r := result(t, h.wait("70")); r.text() != "sampled" {
		t.Fatal(r.text())
	}
}

// TestSamplingRefused (S34, Q06): by default the wrapper answers sampling/createMessage itself with
// a JSON-RPC error, logs it, and the client never sees the request.
func TestSamplingRefused(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	h.sendRequest(71, "tools/call", `"name":"sample","arguments":{}`)
	e := h.wait("71") // the fake server answers the call once its request is answered (here: refused)
	if e == nil {
		t.Fatal("no answer")
	}
	for _, o := range h.others {
		if strings.HasPrefix(o.Method(), "sampling/") {
			t.Fatal("the sampling request reached the client")
		}
	}
	if !strings.Contains(h.stderr.String(), "sent sampling/createMessage; refused and not passed to the client") {
		t.Fatal(h.stderr)
	}
}

// TestGarbageAndBig: a non-JSON line from the server is dropped and logged; a 5 MiB answer is a
// -32603 for its id and the action is reported unknown.
func TestGarbageAndBig(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	if r := result(t, h.call("garbage", `{}`)); r.text() != "after garbage" {
		t.Fatal(r.text())
	}
	if !strings.Contains(h.stderr.String(), "wrote a line that isn't JSON-RPC; dropped") {
		t.Fatal(h.stderr)
	}
	e := h.call("big", `{}`)
	var er jsonrpc.ErrorObject
	if json.Unmarshal(e["error"], &er) != nil || er.Code != jsonrpc.CodeInternal || er.Message != gate.MCPTooLarge {
		t.Fatalf("%.300s", e.Marshal())
	}
	var act string
	h.g.set(func() {
		for id, a := range h.g.actions {
			if a.Tool == "scratch.big" {
				act = id
			}
		}
	})
	if rep := h.g.waitReport(t, act); rep.Status != gate.ExecUnknown || rep.ErrorCode != "result_too_large" {
		t.Fatalf("%+v", rep)
	}
}

// TestChildEnv: the server never sees DEEDGLASS_* (the agent's key).
func TestChildEnv(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	r := result(t, h.call("env", `{}`))
	if strings.Contains(r.text(), "DEEDGLASS_") || !strings.Contains(r.text(), "FAKE_MCP_MODE") {
		t.Fatalf("%q", r.text())
	}
	if strings.Contains(h.stderr.String(), "dg_test_wrapkey") {
		t.Fatal("key in the log")
	}
	b, _ := os.ReadFile(h.w.cfg.LogFile)
	if strings.Contains(string(b), "dg_test_wrapkey") || !strings.Contains(string(b), "✓ scratch.env") {
		t.Fatalf("log file:\n%s", b)
	}
	st, _ := os.Stat(h.w.cfg.LogFile)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v", st.Mode())
	}
}

// TestConcurrent: 100 calls in flight, 100 right answers.
func TestConcurrent(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	h.request("tools/list", "")
	for i := 0; i < 100; i++ {
		h.sendRequest(1000+i, "tools/call", fmt.Sprintf(`"name":"echo","arguments":{"i":%d}`, i))
	}
	got := map[string]bool{}
	deadline := time.After(30 * time.Second)
	for len(got) < 100 {
		select {
		case e := <-h.msgs:
			if e.Kind() != jsonrpc.Response {
				continue
			}
			var id int
			_ = json.Unmarshal(e.ID(), &id)
			r := result(t, e)
			if r.text() != fmt.Sprintf(`ran echo {"i":%d}`, id-1000) {
				t.Fatalf("id %d got %q", id, r.text())
			}
			got[string(e.ID())] = true
		case <-deadline:
			t.Fatalf("%d of 100", len(got))
		}
	}
	if len(h.g.submits) != 100 {
		t.Fatalf("%d submits", len(h.g.submits))
	}
}

// TestPassthroughAndFraming: unknown methods pass with only the id rewritten (string ids too);
// a framing error answers -32700/-32600 with a null id and the relay keeps going.
func TestPassthroughAndFraming(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	h.send(`{"jsonrpc":"2.0","id":"a-1","method":"resources/list","params":{}}`)
	e := h.wait(`"a-1"`)
	var er jsonrpc.ErrorObject
	if json.Unmarshal(e["error"], &er) != nil || er.Code != jsonrpc.CodeMethodNotFound {
		t.Fatalf("%s", e.Marshal())
	}
	h.send(`{"jsonrpc":"2.0",`)
	e = h.wait("null")
	if json.Unmarshal(e["error"], &er) != nil || er.Code != jsonrpc.CodeParseError {
		t.Fatalf("%s", e.Marshal())
	}
	h.send(`[{"jsonrpc":"2.0","id":9,"method":"ping"}]`)
	e = h.wait("null")
	if json.Unmarshal(e["error"], &er) != nil || er.Message != "Batches are not supported." {
		t.Fatalf("%s", e.Marshal())
	}
	if e := h.request("ping", ""); e["result"] == nil {
		t.Fatalf("%s", e.Marshal())
	}
}

// TestShutdown: closing stdin ends the child (SIGTERM if it lingers), flushes reports, exits 0.
func TestShutdown(t *testing.T) {
	h := start(t, "legacy")
	h.handshake()
	r := result(t, h.call("echo", `{}`))
	act := r.Meta["com.deedglass/action"]
	start := time.Now()
	h.close()
	if time.Since(start) > 5*time.Second {
		t.Fatal("slow shutdown")
	}
	if _, ok := h.g.reportFor(act); !ok {
		t.Fatal("report not flushed")
	}
}

// TestShutdownSignalsGroup: a server that ignores stdin EOF gets SIGTERM after the wait.
func TestShutdownSignalsGroup(t *testing.T) {
	h := start(t, "stubborn")
	h.handshake()
	h.w.up.mu.Lock()
	cmd := h.w.up.cmd
	h.w.up.mu.Unlock()
	t0 := time.Now()
	h.close()
	if d := time.Since(t0); d < 400*time.Millisecond || d > 5*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	if cmd.ProcessState == nil || cmd.ProcessState.Success() {
		t.Fatalf("child state %v", cmd.ProcessState)
	}
}

func TestValidate(t *testing.T) {
	base := func() Config {
		return Config{Connector: "scratch", Server: mustURL("http://127.0.0.1:8080"), Upstream: []string{"npx"}}
	}
	cases := []struct {
		name string
		mod  func(*Config)
		ok   bool
	}{
		{"ok", func(*Config) {}, true},
		{"reserved", func(c *Config) { c.Connector = "deedglass" }, false},
		{"bad name", func(c *Config) { c.Connector = "Scratch" }, false},
		{"no upstream", func(c *Config) { c.Upstream = nil }, false},
		{"http remote", func(c *Config) { c.Server = mustURL("http://api.example.com") }, false},
		{"http remote, dev + insecure", func(c *Config) { c.Server, c.DevEnv, c.InsecureHTTP = mustURL("http://10.0.0.2:8080"), true, true }, true},
		{"https remote", func(c *Config) { c.Server = mustURL("https://api.example.com") }, true},
		{"localhost", func(c *Config) { c.Server = mustURL("http://localhost:8080") }, true},
		{"ipv6 loopback", func(c *Config) { c.Server = mustURL("http://[::1]:8080") }, true},
		{"result mode", func(c *Config) { c.ResultMode = "some" }, false},
	}
	for _, k := range cases {
		c := base()
		k.mod(&c)
		if err := c.Validate(); (err == nil) != k.ok {
			t.Errorf("%s: %v", k.name, err)
		}
	}
	env := ChildEnv([]string{"PATH=/bin", "DEEDGLASS_API_KEY=x", "deedglass_server=y", "HOME=/h"})
	if !slices.Equal(env, []string{"PATH=/bin", "HOME=/h"}) {
		t.Fatal(env)
	}
}

func TestFindID(t *testing.T) {
	cases := []struct {
		head, tail string
		id         int64
		ok         bool
	}{
		{`{"jsonrpc":"2.0","id":57,"result":{"x":"`, `"}}`, 57, true},
		{`{"jsonrpc":"2.0","result":{"id":3,"x":"`, `xx"},"id":58}`, 58, true},
		{`{"result":{"x":"`, `xx"}}`, 0, false},
	}
	for _, c := range cases {
		id, ok := findID([]byte(c.head), []byte(c.tail))
		if id != c.id || ok != c.ok {
			t.Errorf("%s: %d %v", c.head, id, ok)
		}
	}
}

var _ = sync.Mutex{}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// TestFlaggedDescription (S34, Q01): a tool whose description carries instructions for the model
// is left out of tools/list, logged with the admin's sentence, and a call to it is an unknown tool
// that never reaches the server or the Gate.
func TestFlaggedDescription(t *testing.T) {
	h := start(t, "legacy", func(c *Config) { c.Env = append(c.Env, "FAKE_MCP_POISON=1") })
	h.handshake()
	h.sendRequest(80, "tools/list", "")
	if s := string(h.wait("80").Marshal()); strings.Contains(s, "cleanup") || !strings.Contains(s, "read_text_file") {
		t.Fatalf("%.400s", s)
	}
	if !strings.Contains(h.stderr.String(), `Hid tool "cleanup" from scratch: its description contains instructions aimed at the model`) {
		t.Fatal(h.stderr)
	}
	e := h.call("cleanup", `{}`)
	var er jsonrpc.ErrorObject
	if json.Unmarshal(e["error"], &er) != nil || er.Code != jsonrpc.CodeInvalidParams || strings.Contains(er.Message, "instruction") {
		t.Fatalf("%.300s", e.Marshal())
	}
	h.g.mu.Lock()
	n := len(h.g.submits)
	h.g.mu.Unlock()
	if n != 0 {
		t.Fatalf("the Gate saw %d submits", n)
	}
}
