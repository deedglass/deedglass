package attack

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/a2a"
	"github.com/deedglass/deedglass/internal/a2a/a2atest"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/deed/stamp"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/ledger"
	"github.com/deedglass/deedglass/internal/mcp/gateclient"
)

// Group T, this week's new surfaces (ADR-115, after the 2026-10-21 audit's M-02 and M-03): the A2A
// proxy, the Debezium witness, the shell guard, head timestamps. Library- and CLI-level cases run
// by plain go test, like group Y. Expected outcomes are in attack_meta.json, declared before the
// proxy's browser checks and the Debezium input cap were written, and never change here. T12
// (independent verifiers) and T13 (connect) are declared and not implemented yet.

// --- the A2A proxy against a recording Gate ---

const t0Key = "dg_attack_t_agent_key_0000000000000000"

type tGate struct {
	mu   sync.Mutex
	keys []string // idempotency keys, one per submit
	args []json.RawMessage
	n    int
}

func (g *tGate) Submit(_ context.Context, r gateclient.SubmitRequest) gateclient.Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	g.keys = append(g.keys, r.IdempotencyKey)
	g.args = append(g.args, r.Args)
	return gateclient.Decision{Kind: gateclient.Authorized, Action: &ledger.Action{ID: fmt.Sprintf("act_01k6w4a3d5f7h9k1n3q5s7t%03d", g.n), Tool: r.Tool, Status: ledger.StatusAuthorized}}
}
func (g *tGate) Report(context.Context, string, gate.EdgeResult) error { return nil }
func (g *tGate) GetActionWait(context.Context, string, time.Duration) (*ledger.Action, error) {
	return nil, errors.New("not used")
}
func (g *tGate) Whoami(context.Context) (*gateclient.Whoami, error) {
	w := &gateclient.Whoami{}
	w.Agent = &struct{ ID, Name, Status string }{ID: "agt_01k6w4a3d5f7h9k1n3q5s70001", Name: "planner", Status: "active"}
	return w, nil
}
func (g *tGate) Server() string { return "http://127.0.0.1:8080" }

func (g *tGate) submits() int { g.mu.Lock(); defer g.mu.Unlock(); return g.n }

type tProxy struct {
	gate  *tGate
	peer  *a2atest.Peer
	proxy *a2a.Proxy
	url   string
}

func newTProxy(t *testing.T, peerName string) *tProxy {
	t.Helper()
	peer := a2atest.New(peerName)
	t.Cleanup(peer.Close)
	u, _ := url.Parse(peer.URL())
	g := &tGate{}
	p, err := a2a.New(a2a.Config{Connector: "researcher", Peer: u, OnBehalfOf: "u_5780", Version: "attack", InboundKey: t0Key,
		HTTP: (&egress.Policy{Env: egress.EnvDev, AllowPrivate: true}).Client(egress.ClientOptions{MaxResponseBytes: 16 << 20})}, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.LoadCard(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return &tProxy{gate: g, peer: peer, proxy: p, url: srv.URL}
}

type tAnswer struct {
	status int
	header http.Header
	body   string
}

func (tp *tProxy) do(t *testing.T, method, path, host string, hdr map[string]string, body string) tAnswer {
	t.Helper()
	req, _ := http.NewRequest(method, tp.url+path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if host != "" {
		req.Host = host
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	tp.proxy.Wait()
	return tAnswer{res.StatusCode, res.Header, string(b)}
}

func sendMessage(id int) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"SendMessage","params":{"message":{"messageId":"m-%d","role":"ROLE_USER","parts":[{"text":"complete the market summary"}]}}}`, id, id)
}

func client() map[string]string {
	return map[string]string{"Content-Type": "application/json", "A2A-Version": "1.0", "Authorization": "Bearer " + t0Key}
}

// refusedNothingForwarded checks the headline every refused A2A case shares.
func (tp *tProxy) refusedNothingForwarded(t *testing.T, a tAnswer, status int, reason string) {
	t.Helper()
	if a.status != status || !strings.Contains(strings.ToLower(a.body), reason) {
		t.Errorf("HTTP %d %s, want %d naming %s", a.status, a.body, status, reason)
	}
	if n, got := tp.gate.submits(), len(tp.peer.Got()); n != 0 || got != 0 {
		t.Errorf("the Gate saw %d submits and the peer %d requests; want none", n, got)
	}
	for k := range a.header {
		if strings.HasPrefix(strings.ToLower(k), "access-control-") {
			t.Errorf("CORS header %s", k)
		}
	}
	if strings.Contains(a.body, t0Key) {
		t.Error("the answer echoes the key")
	}
}

// T01 · A simple cross-site POST from a web page → REJECTED (browser_origin, HTTP 403).
func TestT01BrowserSimplePost(t *testing.T) {
	tp := newTProxy(t, "Researcher")
	a := tp.do(t, http.MethodPost, "/?A2A-Version=1.0", "", map[string]string{
		"Content-Type": "text/plain;charset=UTF-8", "Origin": "https://pages.attack.test", "Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors",
	}, sendMessage(1))
	tp.refusedNothingForwarded(t, a, http.StatusForbidden, "browser_origin")
	// The same body with the key but the version only in the query is still not forwarded.
	h := client()
	delete(h, "A2A-Version")
	a = tp.do(t, http.MethodPost, "/?A2A-Version=1.0", "", h, sendMessage(2))
	if !strings.Contains(a.body, "-32009") || tp.gate.submits() != 0 || len(tp.peer.Got()) != 0 {
		t.Errorf("query-string version was read: %s", a.body)
	}
	t.Log("T01: REJECTED (browser_origin, HTTP 403: nothing reaches the Gate or the peer; the query-string version is never read)")
}

// T02 · DNS rebinding → REJECTED (host_not_loopback, HTTP 421), even with the right key.
func TestT02DNSRebinding(t *testing.T) {
	tp := newTProxy(t, "Researcher")
	for _, host := range []string{"rebind.attack.test:7420", "rebind.attack.test", "localhost.attack.test:7420", "0.0.0.0:7420", "127.0.0.1.nip.io:7420"} {
		a := tp.do(t, http.MethodPost, "/", host, client(), sendMessage(1))
		tp.refusedNothingForwarded(t, a, http.StatusMisdirectedRequest, "host_not_loopback")
		c := tp.do(t, http.MethodGet, "/.well-known/agent-card.json", host, nil, "")
		if c.status != http.StatusMisdirectedRequest {
			t.Errorf("%s: the card answered HTTP %d to a rebinding name", host, c.status)
		}
	}
	t.Log("T02: REJECTED (host_not_loopback, HTTP 421: nothing reaches the Gate or the peer, even with the right key)")
}

// T03 · No key, another key, Basic → REJECTED (key_required, HTTP 401).
func TestT03NoKey(t *testing.T) {
	tp := newTProxy(t, "Researcher")
	for name, auth := range map[string]string{"none": "", "another agent's key": "Bearer dg_attack_t_other_key_00000000000000000",
		"a prefix of the key": "Bearer " + t0Key[:len(t0Key)-1], "Basic": "Basic " + t0Key, "empty bearer": "Bearer "} {
		h := client()
		if auth == "" {
			delete(h, "Authorization")
		} else {
			h["Authorization"] = auth
		}
		a := tp.do(t, http.MethodPost, "/", "", h, sendMessage(1))
		if a.status != http.StatusUnauthorized || !strings.Contains(a.body, "KEY_REQUIRED") {
			t.Errorf("%s: HTTP %d %s", name, a.status, a.body)
		}
		tp.refusedNothingForwarded(t, a, http.StatusUnauthorized, "key_required")
	}
	t.Log("T03: REJECTED (key_required, HTTP 401: nothing reaches the Gate or the peer)")
}

// T04 · CORS preflight → REJECTED (cors_refused, HTTP 403), no Access-Control-* anywhere.
func TestT04CORS(t *testing.T) {
	tp := newTProxy(t, "Researcher")
	a := tp.do(t, http.MethodOptions, "/", "", map[string]string{"Origin": "https://pages.attack.test",
		"Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type,a2a-version,authorization"}, "")
	if a.status != http.StatusForbidden {
		t.Errorf("preflight HTTP %d", a.status)
	}
	b := tp.do(t, http.MethodOptions, "/", "", nil, "")
	if b.status != http.StatusForbidden || !strings.Contains(b.body, "CORS") {
		t.Errorf("preflight without Origin HTTP %d %s", b.status, b.body)
	}
	// Answers a page might read: none carries a CORS header, including a success.
	ok := tp.do(t, http.MethodPost, "/", "", client(), sendMessage(1))
	card := tp.do(t, http.MethodGet, "/.well-known/agent-card.json", "", nil, "")
	for _, x := range []tAnswer{a, b, ok, card} {
		for k := range x.header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("CORS header %s on HTTP %d", k, x.status)
			}
		}
	}
	if ok.status != http.StatusOK || card.status != http.StatusOK {
		t.Fatalf("controls: send HTTP %d, card HTTP %d", ok.status, card.status)
	}
	t.Log("T04: REJECTED (cors_refused, HTTP 403; no answer from the proxy carries an Access-Control-* header)")
}

// T05 · A replayed SendMessage → OUTSIDE SCOPE: each replay is a new action the Gate decides.
func TestT05Replay(t *testing.T) {
	tp := newTProxy(t, "Researcher")
	body := sendMessage(7)
	for i := 0; i < 3; i++ {
		if a := tp.do(t, http.MethodPost, "/", "", client(), body); a.status != http.StatusOK {
			t.Fatalf("send %d: HTTP %d %s", i, a.status, a.body)
		}
	}
	tp.gate.mu.Lock()
	keys := append([]string(nil), tp.gate.keys...)
	tp.gate.mu.Unlock()
	if len(keys) != 3 || keys[0] == keys[1] || keys[1] == keys[2] || keys[0] == keys[2] {
		t.Fatalf("each replay must be its own Gate decision: %q", keys)
	}
	t.Log("T05: OUTSIDE SCOPE (each replay is a new action that the Gate decides and records; the proxy keeps no replay table, LIMITATIONS)")
}

// T06 · The peer's card claims another name → OUTSIDE SCOPE: the ref is the operator's URL.
func TestT06CardNameClaimed(t *testing.T) {
	tp := newTProxy(t, "Deedglass Admin Console")
	if a := tp.do(t, http.MethodPost, "/", "", client(), sendMessage(1)); a.status != http.StatusOK {
		t.Fatalf("HTTP %d %s", a.status, a.body)
	}
	tp.gate.mu.Lock()
	args := append([]json.RawMessage(nil), tp.gate.args...)
	tp.gate.mu.Unlock()
	if len(args) != 1 {
		t.Fatalf("gate submits %d", len(args))
	}
	var m struct {
		Peer struct {
			URL, Name string
		} `json:"peer"`
		Chain []a2a.Hop `json:"delegation"`
	}
	if err := json.Unmarshal(args[0], &m); err != nil {
		t.Fatal(err)
	}
	chain := m.Chain
	if len(chain) == 0 {
		t.Fatalf("no chain in %s", args[0])
	}
	last := chain[len(chain)-1]
	if last.Ref != tp.peer.URL() || last.Name != "Deedglass Admin Console" || m.Peer.URL != tp.peer.URL() {
		t.Fatalf("peer hop %+v: the ref must be the --peer URL and the name the card's claim", last)
	}
	t.Log("T06: OUTSIDE SCOPE (the hop's ref is the operator's --peer URL; the name is the card's own claim, recorded as given)")
}

// --- the Debezium witness, through the CLI ---

func debeziumTrigger(t *testing.T) []string {
	t.Helper()
	fh, err := os.Open("../internal/witness/feed/debezium/testdata/mysql-trigger.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	zr, err := gzip.NewReader(fh)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

var debeziumArgs = []string{"witness", "debezium", "--schema", "shop", "--pk", "shop.orders=id", "--pk", "shop.shipments=order_id",
	"--sql", "UPDATE orders SET status = 'shipped' WHERE id = 42", "--reported", "1", "--action", "act_01k82m4v9q5n3d7f8g2h6j0kxp"}

// T07 · One change event dropped from the action's transaction → UNVERIFIABLE (feed_incomplete).
func TestT07DebeziumEventDropped(t *testing.T) {
	lines := debeziumTrigger(t)
	if !strings.Contains(lines[0], `"BEGIN"`) || !strings.Contains(lines[2], `"op":"`) {
		t.Fatalf("fixture changed: %.80s / %.80s", lines[0], lines[2])
	}
	cut := append(append([]string(nil), lines[:2]...), lines[3:]...)
	path := writeLines(t, "cut.jsonl", toBytes(cut))
	cmd := exec.Command(deedglassBin(t), append(append([]string{"--json"}, debeziumArgs...), "--events", path)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 {
		t.Fatalf("exit %v\n%s", err, out.String())
	}
	var r struct {
		Verdict string   `json:"verdict"`
		Codes   []string `json:"reason_codes"`
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "UNVERIFIABLE" || len(r.Codes) == 0 || r.Codes[0] != "feed_incomplete" {
		t.Fatalf("verdict %s codes %v", r.Verdict, r.Codes)
	}
	t.Log("T07: UNVERIFIABLE (feed_incomplete: a transaction with an event missing is never compared; never VERIFIED and never MISMATCH)")
}

func toBytes(ls []string) [][]byte {
	out := make([][]byte, len(ls))
	for i, l := range ls {
		out[i] = []byte(l)
	}
	return out
}

// T08 · A feed larger than 1 GiB (an endless stdin) → REJECTED (feed_too_large, exit 2), and
// reading stops at the cap.
func TestT08DebeziumTooLarge(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 1 GiB")
	}
	cmd := exec.Command(deedglassBin(t), append(append([]string{}, debeziumArgs...), "--events", "-")...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line := append(bytes.Repeat([]byte{' '}, 1<<20-1), '\n') // blank lines: parsed as nothing
	var sent int64
	go func() {
		for sent < 3<<30 {
			n, err := stdin.Write(line)
			sent += int64(n)
			if err != nil {
				break
			}
		}
		stdin.Close()
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(2 * time.Minute):
		_ = cmd.Process.Kill()
		t.Fatal("the CLI kept reading")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(out.String(), "larger than 1024 MiB") {
		t.Fatalf("exit %v\n%s", err, out.String())
	}
	t.Log("T08: REJECTED (feed_too_large, exit 2: reading stops at the cap; nothing is compared)")
}

// --- the shell guard ---

// T09 · Input that isn't a hook's JSON → OUTSIDE SCOPE: fails open (exit 1, nothing on stdout).
func TestT09ShellGuardFailsOpen(t *testing.T) {
	for _, in := range []string{"not json", "", `{"hook_event_name":`, `[]`} {
		cmd := exec.Command(deedglassBin(t), "guard", "shell")
		cmd.Stdin = strings.NewReader(in)
		cmd.Env = append(os.Environ(), "NO_COLOR=1")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 1 || stdout.Len() != 0 {
			t.Fatalf("input %q: exit %v, stdout %q", in, err, stdout.String())
		}
	}
	t.Log("T09: OUTSIDE SCOPE (fails open by design: exit 1 with nothing on stdout, so the agent's own permission rules decide; LIMITATIONS)")
}

// --- head timestamps ---

func opensslToken(t *testing.T, alg string) (tok, imprint, root []byte) {
	t.Helper()
	dir := "../internal/deed/stamp/testdata/openssl/" + alg + "/"
	head, err1 := os.ReadFile(dir + "head.json")
	reply, err2 := os.ReadFile(dir + "reply.tsr")
	root, err3 := os.ReadFile(dir + "root.pem")
	if err := errors.Join(err1, err2, err3); err != nil {
		t.Fatal(err)
	}
	tok, err := stamp.ParseResponse(reply)
	if err != nil {
		t.Fatal(err)
	}
	imp := sha256.Sum256(head)
	return tok, imp[:], root
}

// T10 · A token with its time edited, and a token from a TSA outside --tsa-roots → DETECTED.
func TestT10TimestampForged(t *testing.T) {
	tok, imp, rsaRoot := opensslToken(t, "rsa")
	_, _, ecRoot := opensslToken(t, "ec")
	trusted, err := stamp.ParseRoots(rsaRoot)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := stamp.VerifyToken(tok, imp, trusted); err != nil || !info.Pinned {
		t.Fatalf("control: %v %+v", err, info)
	}
	// The genTime is a GeneralizedTime (tag 0x18, 15 bytes, "2026…Z"): move it a year back.
	i := bytes.Index(tok, []byte{0x18, 0x0f, '2', '0', '2', '6'})
	if i < 0 {
		t.Fatal("no genTime in the token")
	}
	edited := bytes.Clone(tok)
	edited[i+5] = '5'
	if _, err := stamp.VerifyToken(edited, imp, trusted); err == nil {
		t.Fatal("a token with its time edited was accepted")
	}
	other, err := stamp.ParseRoots(ecRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stamp.VerifyToken(tok, imp, other); err == nil || !strings.Contains(err.Error(), "not from a TSA you trust") {
		t.Fatalf("a token from a TSA outside --tsa-roots: %v", err)
	}
	t.Log("T10: DETECTED (the edited token fails its signature; the other is refused as not from a TSA you trust)")
}

// T11 · A token checked without --tsa-roots → OUTSIDE SCOPE: intact but unpinned (tsa_not_pinned).
func TestT11TimestampUnpinned(t *testing.T) {
	tok, imp, _ := opensslToken(t, "rsa")
	info, err := stamp.VerifyToken(tok, imp, nil)
	if err != nil || info.Pinned {
		t.Fatalf("without roots: %v %+v", err, info)
	}
	if stamp.WarnTSANotPinned != "tsa_not_pinned" {
		t.Fatalf("warning code %q", stamp.WarnTSANotPinned)
	}
	t.Log("T11: OUTSIDE SCOPE (checked against the chain it carries: accepted as intact but unpinned, and verify warns tsa_not_pinned)")
}
