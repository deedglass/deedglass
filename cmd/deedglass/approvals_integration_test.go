//go:build integration

package main

import (
	"encoding/json"
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

	"github.com/deedglass/deedglass/internal/mcp/wrap"
	"github.com/deedglass/deedglass/pkg/deedglass/webhook"
)

// approvalRule pauses scratch writes under approve/ for a person (inserted into acme-demo).
const approvalRule = `  - id: scratch-approve-writes
    match: { tool: [scratch.write_file] }
    when: has(args.path) && args.path.startsWith("/tmp/deedglass-demo/allowed/approve/")
    decision: require_approval
    reason: Writes under approve/ need a person to countersign.
    approval: { approvers: [role:admin], required: 1, expires_in: 30m }
`

var reLink = regexp.MustCompile(`http://127\.0\.0\.1:\d+/approve#t=([A-Za-z0-9_-]{43})`)

// TestApprovalsLive is S22 §9 on a real server: the wrapper's approve-then-run (DL-028), the deny
// reason reaching the MCP client, the emailed link (dev mail log) deciding once, the page's
// headers, and a signed webhook to a local receiver.
func TestApprovalsLive(t *testing.T) {
	m := newMCPLiveWith(t, map[string]string{"DEEDGLASS_ALLOW_PRIVATE_NETWORKS": "true"})
	env := m.env
	src, err := os.ReadFile(filepath.Join("..", "..", "examples", "demo", "policies", "acme-demo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	pol := strings.Replace(string(src), "  - id: scratch-writes-only-in-allowed\n", approvalRule+"  - id: scratch-writes-only-in-allowed\n", 1)
	pf := filepath.Join(t.TempDir(), "acme-demo.yaml")
	if err := os.WriteFile(pf, []byte(pol), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := cli(t, env, false, "policy", "push", pf); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := cli(t, env, false, "policy", "activate", "acme-demo", "--version", "2", "--mode", "enforce", "--reason", "approvals", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}

	// A webhook to a local receiver, created with the CLI.
	var mu sync.Mutex
	var hits []http.Header
	var bodies [][]byte
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits, bodies = append(hits, r.Header.Clone()), append(bodies, b)
		mu.Unlock()
	}))
	defer recv.Close()
	r := cli(t, env, false, "--json", "webhooks", "create", "--url", recv.URL+"/hook", "--events", "approval.requested,approval.decided")
	var wh struct{ ID, Secret string }
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &wh) != nil || !strings.HasPrefix(wh.Secret, "whsec_") {
		t.Fatalf("webhooks create: %+v", r)
	}
	if r := cli(t, env, false, "webhooks", "test", wh.ID); r.code != 0 || !strings.Contains(r.stdout, "Delivered webhook.test · 200 in ") {
		t.Fatalf("webhooks test: %+v", r)
	}

	// The wrapper in front of the fake MCP server.
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	u, _ := url.Parse(m.base)
	c := newMCPClient(t, wrap.Config{Connector: "scratch", Server: u, APIKey: m.key, HideDenied: true, Version: "test",
		Upstream: []string{os.Args[0]}, Env: append(os.Environ(), "FAKE_MCP_MODE=legacy", "FAKE_MCP_LOG="+calls),
		ShutdownWait: time.Second, KillWait: time.Second})
	c.initialize()

	// 1. Approve, then check: the call runs once, through the wrapper.
	p := c.call("write_file", `{"path":"/tmp/deedglass-demo/allowed/approve/a.txt","content":"hi"}`)
	apr := p.Meta["com.deedglass/approval"]
	if !strings.HasPrefix(apr, "apr_") || !strings.Contains(p.Content[0].Text, "Nothing has run yet") || readCalls(calls) != "" {
		t.Fatalf("pending: %+v", p)
	}
	if r := cli(t, env, false, "approvals", "list"); r.code != 0 || !strings.Contains(r.stdout, "claude-desktop") || !strings.Contains(r.stdout, "scratch.write_file") {
		t.Fatalf("approvals list: %+v", r)
	}
	if r := cli(t, env, false, "approvals", "show", apr); r.code != 0 || !strings.Contains(r.stdout, "scratch-approve-writes") {
		t.Fatalf("approvals show: %+v", r)
	}
	r = cli(t, env, false, "approve", apr, "--as", "priya@acme.test", "--reason", "Looks right")
	if r.code != 0 || !strings.Contains(r.stdout, "Countersigned · "+apr[:12]+"… · priya@acme.test · 1 of 1") ||
		!strings.Contains(r.stdout, "Deed sealed · approval.decided") {
		t.Fatalf("approve: %+v", r)
	}
	ok := c.call("deedglass.check_approval", `{"approval_id":"`+apr+`","wait_seconds":10}`)
	if ok.IsError || !strings.HasPrefix(ok.Content[0].Text, "Approved by a person. Ran scratch.write_file.") || readCalls(calls) != "write_file" {
		t.Fatalf("check after approval: %+v, calls %q", ok, readCalls(calls))
	}
	again := c.call("deedglass.check_approval", `{"approval_id":"`+apr+`"}`)
	if !strings.HasPrefix(again.Content[0].Text, "This action already ran") || readCalls(calls) != "write_file" {
		t.Fatalf("second check: %+v", again)
	}

	// 2. Deny with a reason: the reason reaches the MCP client (DL-028), nothing runs.
	p = c.call("write_file", `{"path":"/tmp/deedglass-demo/allowed/approve/b.txt","content":"no"}`)
	apr2 := p.Meta["com.deedglass/approval"]
	if r := cli(t, env, false, "deny", apr2, "--as", "priya@acme.test"); r.code != 2 || !strings.Contains(r.stderr, "A reason is required to deny.") {
		t.Fatalf("deny without a reason: %+v", r)
	}
	if r := cli(t, env, false, "deny", apr2, "--as", "priya@acme.test", "--reason", `Order 42 hasn't been "paid" yet`); r.code != 0 {
		t.Fatalf("deny: %+v", r)
	}
	no := c.call("deedglass.check_approval", `{"approval_id":"`+apr2+`","wait_seconds":0}`)
	if !no.IsError || no.Content[0].Text != `A person denied this action. Nothing was run. Reason: "Order 42 hasn't been 'paid' yet."` ||
		readCalls(calls) != "write_file" {
		t.Fatalf("denied: %q", no.Content[0].Text)
	}

	// 3. The emailed link (the dev mail log on serve's stderr): preview, decide once, then 401.
	time.Sleep(500 * time.Millisecond) // the first two approvals' emails are out
	before := len(reLink.FindAllStringSubmatch(m.s.err.String(), -1))
	p = c.call("write_file", `{"path":"/tmp/deedglass-demo/allowed/approve/c.txt","content":"link"}`)
	apr3 := p.Meta["com.deedglass/approval"]
	var tok string
	for deadline := time.Now().Add(10 * time.Second); tok == ""; {
		if ms := reLink.FindAllStringSubmatch(m.s.err.String(), -1); len(ms) > before {
			tok = ms[len(ms)-1][1] // the org has one approver: one email per approval
		}
		if time.Now().After(deadline) {
			t.Fatalf("no approval email in the mail log:\n%s", m.s.err)
		}
		if tok == "" {
			time.Sleep(100 * time.Millisecond)
		}
	}
	post := func(path, body string) (int, map[string]any) {
		res, err := http.Post(m.base+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	st, prev := post("/v1/approval-links/preview", `{"token":"`+tok+`"}`)
	ap, _ := prev["approval"].(map[string]any)
	if st != 200 || ap["id"] != apr3 {
		t.Fatalf("preview %d %v", st, prev)
	}
	dg := ap["context_digest"].(string)
	st, dec := post("/v1/approval-links/decide", `{"token":"`+tok+`","vote":"approve","context_digest":"`+dg+`"}`)
	if st != 200 || dec["status"] != "approved" || dec["action_status"] != "processing" {
		t.Fatalf("decide %d %v", st, dec)
	}
	st, again2 := post("/v1/approval-links/decide", `{"token":"`+tok+`","vote":"approve","context_digest":"`+dg+`"}`)
	if e, _ := again2["error"].(map[string]any); st != 401 || e["code"] != "approval_link_invalid" {
		t.Fatalf("used link: %d %v", st, again2)
	}
	// The page: rendered with nothing consumed, strict headers.
	res, err := http.Get(m.base + "/approve?t=" + tok)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
		res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("page %d %v", res.StatusCode, res.Header)
	}
	// The link's approval ran through the wrapper's own check.
	got := c.call("deedglass.check_approval", `{"approval_id":"`+apr3+`","wait_seconds":10}`)
	if got.IsError || !strings.HasPrefix(got.Content[0].Text, "Approved by a person.") {
		t.Fatalf("check after the link: %+v", got)
	}
	c.close()

	// The webhook saw the approvals, every delivery signed.
	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(hits)
		mu.Unlock()
		if n >= 7 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	types := map[string]int{}
	for i, h := range hits {
		types[h.Get("Deedglass-Event-Type")]++
		if err := webhook.Verify(wh.Secret, h.Get(webhook.Header), bodies[i], 0); err != nil {
			t.Errorf("delivery %d: %v", i, err)
		}
		if strings.Contains(string(bodies[i]), "@") || strings.Contains(string(bodies[i]), `"args"`) {
			t.Errorf("payload leaks: %s", bodies[i])
		}
	}
	if types["approval.requested"] != 3 || types["approval.decided"] != 3 || types["webhook.test"] != 1 {
		t.Errorf("deliveries by type %v", types)
	}
	// Nothing secret in the server's own log lines (the dev mail log is the one place a link goes).
	logs := m.s.err.String()
	if i := strings.Index(logs, "──── deedglass mail"); i >= 0 {
		logs = logs[:i]
	}
	if strings.Contains(logs, wh.Secret) {
		t.Error("the webhook secret reached the server log")
	}
}
