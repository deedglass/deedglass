package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/a2a/a2atest"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/gateclient"
)

var update = flag.Bool("update", false, "rewrite the goldens in testdata/golden")

// --- a fake Gate: decides on the arguments, records submits and reports ---

type submit struct {
	Tool, OnBehalfOf, ConnectorType, Transport string
	Args                                       json.RawMessage
}

type fakeGate struct {
	mu       sync.Mutex
	agent    string // the agent's name; its id is derived from it
	down     bool
	n        int
	submits  []submit
	reports  map[string]gate.EdgeResult
	actions  map[string]*ledger.Action
	approved bool // pending actions read as authorized once true
}

func newFakeGate(agent string) *fakeGate {
	return &fakeGate{agent: agent, reports: map[string]gate.EdgeResult{}, actions: map[string]*ledger.Action{}}
}

func (g *fakeGate) agentID() string {
	return "agt_01k6w4a3d5f7h9k1n3q5s7" + fmt.Sprintf("%04d", len(g.agent))
}

func (g *fakeGate) Server() string { return "http://127.0.0.1:8080" }

func (g *fakeGate) Whoami(context.Context) (*gateclient.Whoami, error) {
	if g.down {
		return nil, fmt.Errorf("connection refused")
	}
	w := &gateclient.Whoami{}
	w.Agent = &struct{ ID, Name, Status string }{ID: g.agentID(), Name: g.agent, Status: "active"}
	return w, nil
}

func str(s string) *string { return &s }

func (g *fakeGate) Submit(_ context.Context, r gateclient.SubmitRequest) gateclient.Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.down {
		return gateclient.Decision{Kind: gateclient.Unreachable}
	}
	g.n++
	g.submits = append(g.submits, submit{Tool: r.Tool, OnBehalfOf: r.OnBehalfOf, ConnectorType: r.ConnectorType, Transport: r.Transport, Args: r.Args})
	id := fmt.Sprintf("act_01k6w4a3d5f7h9k1n3q5s7v%03d", g.n)
	a := &ledger.Action{ID: id, Tool: r.Tool, Status: ledger.StatusAuthorized}
	g.actions[id] = a
	text := string(r.Args)
	switch {
	case strings.Contains(text, "delete everything"):
		a.Status = ledger.StatusDenied
		a.Decision = &ledger.ActionDecision{Outcome: "deny", RuleID: str("no-deletes-by-delegation")}
		a.Deed = str("deed_01k6w4a3d5f7h9k1n3q5s7v001")
		return gateclient.Decision{Kind: gateclient.Denied, Action: a, Message: `Blocked by policy "no-deletes-by-delegation": Peers may not delete.`}
	case strings.Contains(text, "refund"):
		a.Status = ledger.StatusPendingApproval
		a.Approval = &ledger.ActionApproval{ID: "apr_01k6w4a3d5f7h9k1n3q5s7v001", Status: "pending"}
		a.Decision = &ledger.ActionDecision{Outcome: "require_approval", Message: str(`Waiting for approval under policy "refunds-need-a-person": Refunds wait for a person.`)}
		return gateclient.Decision{Kind: gateclient.Pending, Action: a}
	}
	return gateclient.Decision{Kind: gateclient.Authorized, Action: a}
}

func (g *fakeGate) GetActionWait(_ context.Context, id string, _ time.Duration) (*ledger.Action, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	a, ok := g.actions[id]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	if g.approved && a.Status == ledger.StatusPendingApproval {
		a.Status = ledger.StatusAuthorized
		a.Approval.Status = "approved"
	}
	c := *a
	return &c, nil
}

func (g *fakeGate) Report(_ context.Context, id string, r gate.EdgeResult) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reports[id] = r
	return nil
}

// --- harness ---

type harness struct {
	t     *testing.T
	gate  *fakeGate
	peer  *a2atest.Peer
	proxy *Proxy
	srv   *httptest.Server
}

func peerClient() *http.Client {
	return (&egress.Policy{Env: egress.EnvDev, AllowPrivate: true}).Client(egress.ClientOptions{MaxResponseBytes: 16 << 20})
}

func newHarness(t *testing.T, agent, connector, obo string, peer *a2atest.Peer) *harness {
	t.Helper()
	h := &harness{t: t, gate: newFakeGate(agent), peer: peer}
	u, _ := url.Parse(peer.URL())
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer peer-token-from-the-operator")
	var err error
	h.proxy, err = New(Config{Connector: connector, Peer: u, PeerHeaders: hdr, OnBehalfOf: obo, HTTP: peerClient(), Version: "test",
		Now: func() time.Time { return time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC) }}, h.gate, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.srv = httptest.NewServer(h.proxy)
	h.proxy.cfg.PublicURL = h.srv.URL + "/"
	t.Cleanup(h.srv.Close)
	if err := h.proxy.LoadCard(context.Background()); err != nil {
		t.Fatal(err)
	}
	return h
}

type exchange struct {
	status int
	ctype  string
	body   string
}

func (h *harness) post(body string, hdr map[string]string) exchange {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	h.proxy.Wait()
	return exchange{status: res.StatusCode, ctype: res.Header.Get("Content-Type"), body: string(b)}
}

var v1 = map[string]string{"A2A-Version": "1.0", "Authorization": "Bearer the-callers-own-secret"}

func sendBody(id int, method, text, metadata string) string {
	m := ""
	if metadata != "" {
		m = `,"metadata":` + metadata
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":{"message":{"messageId":"msg-%d","role":"ROLE_USER","parts":[{"text":%q}]%s}}}`,
		id, method, id, text, m)
}

func pretty(b []byte) string {
	var buf bytes.Buffer
	if json.Indent(&buf, bytes.TrimSpace(b), "", "  ") != nil {
		return string(b)
	}
	return buf.String()
}

var rePort = regexp.MustCompile(`127\.0\.0\.1:\d+`)

// transcript is what one scenario did, end to end, for the golden.
func (h *harness) transcript(start int, ex exchange, request string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "== caller → proxy\n%s\n", pretty([]byte(request)))
	h.gate.mu.Lock()
	subs := append([]submit(nil), h.gate.submits...)
	reps := map[string]gate.EdgeResult{}
	for k, v := range h.gate.reports {
		reps[k] = v
	}
	h.gate.mu.Unlock()
	if len(subs) == 0 {
		b.WriteString("== gate: no action\n")
	}
	for i, s := range subs {
		fmt.Fprintf(&b, "== gate submit %d: tool %s · connector_type %s · transport %s · on_behalf_of %q\n%s\n", i+1, s.Tool, s.ConnectorType, s.Transport, s.OnBehalfOf, pretty(s.Args))
	}
	got := h.peer.Got()[start:]
	if len(got) == 0 {
		b.WriteString("== peer: nothing received\n")
	}
	for _, g := range got {
		fmt.Fprintf(&b, "== peer received %s · A2A-Version %q · A2A-Extensions %q · Authorization %q\n%s\n", g.Method, g.Version, g.Extensions, g.Authorization, pretty(g.Body))
	}
	fmt.Fprintf(&b, "== proxy → caller: HTTP %d %s\n", ex.status, ex.ctype)
	if strings.HasPrefix(ex.ctype, "text/event-stream") {
		b.WriteString(ex.body)
	} else {
		b.WriteString(pretty([]byte(ex.body)) + "\n")
	}
	for i := range subs {
		id := fmt.Sprintf("act_01k6w4a3d5f7h9k1n3q5s7v%03d", i+1)
		if r, ok := reps[id]; ok {
			fmt.Fprintf(&b, "== report %s: %s · error_code %q · result_digest %s\n", id, r.Status, r.ErrorCode, r.ResultDigest)
			if len(r.Result) > 0 {
				b.WriteString(pretty(r.Result) + "\n")
			}
		}
	}
	return rePort.ReplaceAllString(b.String(), "127.0.0.1:PORT")
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from the golden (run with -update and review the diff):\n%s", name, got)
	}
}

// TestGoldens runs each row against a fresh fake Gate and peer and compares the whole exchange.
func TestGoldens(t *testing.T) {
	chain := `{"` + ExtensionURI + `":{"chain":[{"kind":"person","ref":"u_5780"},{"kind":"agent","ref":"agt_01k6w4a3d5f7h9k1n3q5s70007","name":"planner"}]}}`
	rows := []struct {
		name, obo, body string
		hdr             map[string]string
		down            bool
	}{
		{"send_completed", "u_5780", sendBody(1, MethodSendMessage, "complete the market summary", ""), v1, false},
		{"send_chain_from_caller", "", sendBody(2, MethodSendMessage, "complete the market summary", chain), v1, false},
		{"send_reply_message", "u_5780", sendBody(3, MethodSendMessage, "reply with a greeting", ""), v1, false},
		{"send_task_failed", "u_5780", sendBody(4, MethodSendMessage, "fail on purpose", ""), v1, false},
		{"send_task_working", "u_5780", sendBody(5, MethodSendMessage, "work in the background", ""), v1, false},
		{"send_peer_error", "u_5780", sendBody(6, MethodSendMessage, "refuse politely", ""), v1, false},
		{"send_denied", "u_5780", sendBody(7, MethodSendMessage, "delete everything in the archive", ""), v1, false},
		{"send_streaming", "u_5780", sendBody(8, MethodSendStreamingMessage, "complete it while streaming", ""),
			map[string]string{"A2A-Version": "1.0", "Accept": "text/event-stream"}, false},
		{"cancel_task", "u_5780", `{"jsonrpc":"2.0","id":9,"method":"CancelTask","params":{"id":"task-1"}}`, v1, false},
		{"read_get_task", "u_5780", `{"jsonrpc":"2.0","id":10,"method":"GetTask","params":{"id":"task-1"}}`, v1, false},
		{"refused_unknown_method", "u_5780", `{"jsonrpc":"2.0","id":11,"method":"message/send","params":{}}`, v1, false},
		{"refused_no_version", "u_5780", sendBody(12, MethodSendMessage, "complete", ""), nil, false},
		{"refused_batch", "u_5780", `[` + sendBody(13, MethodSendMessage, "complete", "") + `]`, v1, false},
		{"refused_gate_down", "u_5780", sendBody(14, MethodSendMessage, "complete", ""), v1, true},
		{"refused_two_people", "u_9999", sendBody(15, MethodSendMessage, "complete", chain), v1, false},
		{"refused_bad_chain", "", sendBody(16, MethodSendMessage, "complete", `{"`+ExtensionURI+`":{"chain":[{"kind":"robot","ref":"x"}]}}`), v1, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			peer := a2atest.New("Researcher")
			defer peer.Close()
			h := newHarness(t, "planner", "researcher", r.obo, peer)
			h.gate.down = r.down
			ex := h.post(r.body, r.hdr)
			golden(t, r.name, h.transcript(0, ex, r.body))
			for _, g := range peer.Got() {
				if strings.Contains(g.Authorization, "callers-own-secret") || strings.Contains(string(g.Body), "callers-own-secret") {
					t.Error("the caller's credential reached the peer")
				}
			}
		})
	}
}

// TestApprovalRunsOnce: a call that waits for a person is answered CodePending; the same request
// sent again before the decision waits again; after approval it runs once, on the same action, and
// the same request after that is a new action that waits for its own approval.
func TestApprovalRunsOnce(t *testing.T) {
	peer := a2atest.New("Payments")
	defer peer.Close()
	h := newHarness(t, "support-bot", "payments", "u_5780", peer)
	body := sendBody(1, MethodSendMessage, "complete the refund of order 4928", "")
	first := h.post(body, v1)
	second := h.post(body, v1)
	h.gate.approved = true
	third := h.post(body, v1)
	fourth := h.post(body, v1)
	var b strings.Builder
	for i, ex := range []exchange{first, second, third, fourth} {
		fmt.Fprintf(&b, "== call %d: HTTP %d\n%s\n", i+1, ex.status, pretty([]byte(ex.body)))
	}
	fmt.Fprintf(&b, "== submits %d · peer calls %d\n", len(h.gate.submits), len(peer.Got()))
	golden(t, "approval_runs_once", b.String())
	if len(h.gate.submits) != 2 || len(peer.Got()) != 1 {
		t.Errorf("submits %d, peer calls %d; want 2 and 1 (the approved action runs once; the same request after it is a new action)", len(h.gate.submits), len(peer.Got()))
	}
}

// TestCard: the proxy's card points its JSONRPC interface here and drops the rest.
func TestCard(t *testing.T) {
	peer := a2atest.New("Researcher")
	defer peer.Close()
	h := newHarness(t, "planner", "researcher", "", peer)
	res, err := http.Get(h.srv.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	golden(t, "agent_card", rePort.ReplaceAllString(pretty(b), "127.0.0.1:PORT")+"\n")
}

// TestTwoAgentDelegation: a person asks planner, planner delegates to researcher through its proxy,
// and researcher delegates to analyst through its own. Each action carries the whole chain.
func TestTwoAgentDelegation(t *testing.T) {
	analyst := a2atest.New("Analyst")
	defer analyst.Close()
	researcherAgent := a2atest.New("Researcher")
	defer researcherAgent.Close()
	hr := newHarness(t, "researcher", "analyst", "", analyst) // the researcher's own proxy
	hp := newHarness(t, "planner", "researcher", "u_5780", researcherAgent)

	// The researcher agent passes the chain it received on to the analyst, as A2A metadata.
	var nested exchange
	researcherAgent.OnSend = func(params json.RawMessage) {
		var p struct {
			Message struct {
				Metadata json.RawMessage `json:"metadata"`
			} `json:"message"`
		}
		_ = json.Unmarshal(params, &p)
		nested = hr.post(sendBody(2, MethodSendMessage, "complete the numbers", string(p.Message.Metadata)), v1)
	}
	top := hp.post(sendBody(1, MethodSendMessage, "complete the market summary", ""), v1)
	if top.status != 200 || nested.status != 200 {
		t.Fatalf("top %d, nested %d", top.status, nested.status)
	}
	var lines []string
	for _, h := range []*harness{hp, hr} {
		for _, s := range h.gate.submits {
			c, ok := ChainFromRequest(s.Args)
			if !ok {
				t.Fatalf("no chain in %s", s.Args)
			}
			lines = append(lines, fmt.Sprintf("%s on behalf of %s: %s", s.Tool, s.OnBehalfOf, FormatChain(c)))
		}
	}
	got := strings.Join(lines, "\n")
	want := "researcher.SendMessage on behalf of u_5780: u_5780 → planner → Researcher\n" +
		"analyst.SendMessage on behalf of u_5780: u_5780 → planner → researcher → Analyst"
	if got != want {
		t.Errorf("chains:\n%s\nwant:\n%s", got, want)
	}
}

func TestCheckChain(t *testing.T) {
	agent := Hop{Kind: HopAgent, Ref: "agt_x"}
	long := make([]Hop, MaxHops+1)
	for i := range long {
		long[i] = agent
	}
	for _, c := range []struct {
		chain []Hop
		bad   bool
	}{
		{[]Hop{{Kind: HopPerson, Ref: "u_1"}, agent}, false},
		{[]Hop{agent}, false},
		{nil, true},
		{long, true},
		{[]Hop{agent, {Kind: HopPerson, Ref: "u_1"}}, true},
		{[]Hop{{Kind: HopAgent, Ref: "has space"}}, true},
		{[]Hop{{Kind: HopAgent, Ref: "a", Name: "line\nbreak"}}, true},
		{[]Hop{{Kind: HopAgent, Ref: "a", Name: strings.Repeat("n", 65)}}, true},
	} {
		if err := CheckChain(c.chain); (err != nil) != c.bad {
			t.Errorf("%v: err %v, want bad=%v", c.chain, err, c.bad)
		}
	}
	if _, err := ChainFromMetadata(json.RawMessage(`{"` + ExtensionURI + `":{"chain":[{"kind":"agent","ref":"a","extra":1}]}}`)); err == nil {
		t.Error("an unknown member in a hop was accepted")
	}
	if c, err := ChainFromMetadata(json.RawMessage(`{"other":1}`)); c != nil || err != nil {
		t.Errorf("no extension entry: %v %v", c, err)
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		method, body string
		status       gate.ExecStatus
		code         string
	}{
		{MethodSendMessage, `{"result":{"task":{"id":"t","status":{"state":"TASK_STATE_COMPLETED"}}}}`, gate.ExecSucceeded, ""},
		{MethodSendMessage, `{"result":{"task":{"id":"t","status":{"state":"TASK_STATE_REJECTED"}}}}`, gate.ExecFailed, "task_rejected"},
		{MethodSendMessage, `{"result":{"task":{"id":"t","status":{"state":"TASK_STATE_CANCELED"}}}}`, gate.ExecFailed, "task_canceled"},
		{MethodSendMessage, `{"result":{"task":{"id":"t","status":{"state":"TASK_STATE_INPUT_REQUIRED"}}}}`, gate.ExecUnknown, "task_input_required"},
		{MethodSendMessage, `{"result":{"task":{"id":"t","status":{"state":"weird; drop table"}}}}`, gate.ExecUnknown, "task_state_unknown"},
		{MethodSendMessage, `{"result":{"task":{"id":"t"}}}`, gate.ExecUnknown, "task_state_unknown"},
		{MethodSendMessage, `{"result":{"message":{"messageId":"m"}}}`, gate.ExecSucceeded, ""},
		{MethodSendMessage, `{"error":{"code":-32603,"message":"boom"}}`, gate.ExecUnknown, "peer_internal_error"},
		{MethodSendMessage, `{"error":{"code":-32001,"message":"no task"}}`, gate.ExecFailed, "task_not_found"},
		{MethodSendMessage, `{"error":{"code":12,"message":"x"}}`, gate.ExecFailed, "peer_error"},
		{MethodSendMessage, `not json`, gate.ExecUnknown, "peer_answer_unreadable"},
		{MethodSendStreamingMessage, `{"result":{"statusUpdate":{"status":{"state":"TASK_STATE_FAILED"}}}}`, gate.ExecFailed, "task_failed"},
		{MethodSendStreamingMessage, `{"result":{"artifactUpdate":{"taskId":"t"}}}`, gate.ExecUnknown, "task_state_unknown"},
		{MethodCancelTask, `{"result":{"id":"t","status":{"state":"TASK_STATE_CANCELED"}}}`, gate.ExecSucceeded, ""},
		{MethodCancelTask, `{"result":{"id":"t","status":{"state":"TASK_STATE_WORKING"}}}`, gate.ExecUnknown, "task_working"},
		{MethodCreatePushConfig, `{"result":{"id":"cfg"}}`, gate.ExecSucceeded, ""},
	} {
		o := Classify(c.method, []byte(c.body))
		if o.Status != c.status || o.Code != c.code {
			t.Errorf("%s %s: %s %q, want %s %q", c.method, c.body, o.Status, o.Code, c.status, c.code)
		}
	}
}

func TestChainFromRequest(t *testing.T) {
	if _, ok := ChainFromRequest(json.RawMessage(`{"sql":"select 1"}`)); ok {
		t.Error("a non-a2a request read as a chain")
	}
	c, ok := ChainFromRequest(json.RawMessage(`{"peer":{"url":"https://r.example/a2a"},"delegation":[{"kind":"person","ref":"u_1"},{"kind":"agent","ref":"agt_1","name":"planner"},{"kind":"agent","ref":"https://r.example/a2a"}],"request":{}}`))
	if !ok || FormatChain(c) != "u_1 → planner → https://r.example/a2a" {
		t.Errorf("%v %v", c, ok)
	}
}
