//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/a2a"
	"github.com/deedglass/deedglass/internal/a2a/a2atest"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/mcp/gateclient"
)

// TestA2ALive is TY-07's done-when on a real server and database: a person asks planner, planner
// delegates to researcher through deedglass a2a proxy, and researcher delegates to analyst through
// its own proxy. Both delegations are actions sealed Claimed (L0, edge_reported), each carries the
// whole chain, actions show prints it, and the record binds it: the deed's request_digest is the
// digest of the arguments that hold the chain, and its on_behalf_of is the person. A CancelTask the
// policy denies never reaches the peer. The export verifies.
func TestA2ALive(t *testing.T) {
	m := newMCPLive(t)
	env := m.env
	if r := cli(t, env, false, "policy", "push", filepath.Join("testdata", "a2a", "a2a-demo.yaml")); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	if r := cli(t, env, false, "policy", "activate", "a2a-demo", "--version", "1", "--mode", "enforce", "--reason", "A2A test", "--yes"); r.code != 0 {
		t.Fatalf("%+v", r)
	}
	agentKey := func(name string) string {
		if r := cli(t, env, false, "--json", "agent", "create", name); r.code != 0 {
			t.Fatalf("%+v", r)
		}
		r := cli(t, env, false, "--json", "agent", "keys", "create", name)
		var k struct{ Secret string }
		if r.code != 0 || json.Unmarshal([]byte(r.stdout), &k) != nil {
			t.Fatalf("%+v", r)
		}
		return k.Secret
	}
	plannerKey, researcherKey := agentKey("planner"), agentKey("researcher")

	pol := &egress.Policy{Env: egress.EnvDev, AllowPrivate: true} // the peers are on loopback
	proxy := func(key, connector, obo string, peer *a2atest.Peer) (*a2a.Proxy, string) {
		u, _ := url.Parse(peer.URL())
		gc := gateclient.New(gateclient.Options{Server: m.base, APIKey: key})
		p, err := a2a.New(a2a.Config{Connector: connector, Peer: u, OnBehalfOf: obo, HTTP: pol.Client(egress.ClientOptions{MaxResponseBytes: 16 << 20}),
			Version: "test", InboundKey: key}, gc, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.LoadCard(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(p)
		t.Cleanup(srv.Close)
		return p, srv.URL + "/"
	}
	post := func(to, key, body string) string {
		req, _ := http.NewRequest(http.MethodPost, to, strings.NewReader(body))
		req.Header.Set("A2A-Version", "1.0")
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	send := func(id int, text, metadata string) string {
		meta := ""
		if metadata != "" && metadata != "null" {
			meta = `,"metadata":` + metadata
		}
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"SendMessage","params":{"message":{"messageId":"m-%d","role":"ROLE_USER","parts":[{"text":%q}]%s}}}`, id, id, text, meta)
	}

	analyst := a2atest.New("Analyst")
	defer analyst.Close()
	researcher := a2atest.New("Researcher")
	defer researcher.Close()
	rp, researcherProxy := proxy(researcherKey, "analyst", "", analyst)
	pp, plannerProxy := proxy(plannerKey, "researcher", "u_5780", researcher)

	var nested string
	researcher.OnSend = func(params json.RawMessage) {
		var p struct {
			Message struct {
				Metadata json.RawMessage `json:"metadata"`
			} `json:"message"`
		}
		_ = json.Unmarshal(params, &p)
		nested = post(researcherProxy, researcherKey, send(2, "complete the numbers for the summary", string(p.Message.Metadata)))
	}
	top := post(plannerProxy, plannerKey, send(1, "complete the market summary", ""))
	if !strings.Contains(top, "TASK_STATE_COMPLETED") || !strings.Contains(nested, "TASK_STATE_COMPLETED") {
		t.Fatalf("top %s\nnested %s", top, nested)
	}
	denied := post(plannerProxy, plannerKey, `{"jsonrpc":"2.0","id":3,"method":"CancelTask","params":{"id":"task-1"}}`)
	if !strings.Contains(denied, `"code":-31001`) || !strings.Contains(denied, "no-cancel-by-delegation") {
		t.Fatalf("cancel: %s", denied)
	}
	for _, g := range researcher.Got() {
		if g.Method == "CancelTask" {
			t.Fatal("a denied CancelTask reached the peer")
		}
	}
	pp.Wait()
	rp.Wait()

	// Both delegations seal as the peer's word (L0).
	list := cli(t, env, false, "--json", "actions", "list", "--limit", "10")
	var acts struct {
		Data []struct{ ID, Tool string } `json:"data"`
	}
	if list.code != 0 || json.Unmarshal([]byte(list.stdout), &acts) != nil {
		t.Fatalf("%+v", list)
	}
	byTool := map[string]string{}
	for _, a := range acts.Data {
		byTool[a.Tool] = a.ID
	}
	nestedID := byTool["analyst.SendMessage"]
	if byTool["researcher.SendMessage"] == "" || nestedID == "" || byTool["researcher.CancelTask"] == "" {
		t.Fatalf("actions: %s", list.stdout)
	}
	var shown result
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		shown = cli(t, env, false, "--json", "actions", "show", nestedID)
		if strings.Contains(shown.stdout, `"state": "sealed"`) {
			break
		}
	}
	var d struct {
		Tool          string          `json:"tool"`
		OnBehalfOf    string          `json:"on_behalf_of"`
		RequestDigest string          `json:"request_digest"`
		Request       json.RawMessage `json:"request"`
		Deed          string          `json:"deed"`
		Witness       struct{ Level, Verdict, Method string }
	}
	if json.Unmarshal([]byte(shown.stdout), &d) != nil || d.Witness.Level != "L0" || d.Witness.Verdict != "VERIFIED" ||
		d.Witness.Method != "edge_reported" || d.OnBehalfOf != "u_5780" {
		t.Fatalf("%+v", shown)
	}
	chain, ok := a2a.ChainFromRequest(d.Request)
	if !ok || a2a.FormatChain(chain) != "u_5780 → planner → researcher → Analyst" || chain[1].Kind != a2a.HopAgent || !strings.HasPrefix(chain[1].Ref, "agt_") {
		t.Fatalf("chain %v %v", chain, ok)
	}

	// actions show prints the chain.
	text := cli(t, env, false, "actions", "show", nestedID)
	got := regexp.MustCompile(` \d+ ms`).ReplaceAllString(text.stdout, " N ms")
	got = regexp.MustCompile(`pv_[0-9a-z]{8}…`).ReplaceAllString(got, "pv_…")
	got = regexp.MustCompile(`seq \d+`).ReplaceAllString(got, "seq N") // the two delegations seal in either order
	ledgerGolden(t, "a2a_actions_show", result{code: text.code, stdout: got, stderr: text.stderr})

	// The record binds the chain: request_digest is the digest of the arguments that hold it.
	want, err := gate.RequestDigest(d.Tool, d.Request, gate.ModeProxy, nil)
	if err != nil || want != d.RequestDigest {
		t.Fatalf("request_digest %s, recomputed %s (%v)", d.RequestDigest, want, err)
	}
	dr := cli(t, env, false, "deeds", "show", d.Deed, "--json")
	var env2 struct {
		Body struct {
			OnBehalfOf string `json:"on_behalf_of"`
			Action     struct {
				RequestDigest string `json:"request_digest"`
			} `json:"action"`
		} `json:"body"`
	}
	if dr.code != 0 || json.Unmarshal([]byte(dr.stdout), &env2) != nil || env2.Body.Action.RequestDigest != d.RequestDigest || env2.Body.OnBehalfOf != "u_5780" {
		t.Fatalf("deed: %+v", dr)
	}
	er := cli(t, env, false, "deeds", "export", "--since", "1h", "--format", "jsonl", "--out", "-")
	vr := cliIn(t, []byte(er.stdout), env, false, "verify", "-", "--keys", m.base+"/.well-known/deedglass/keys.json")
	if er.code != 0 || vr.code != 0 || !strings.Contains(vr.stdout, "chain intact") {
		t.Fatalf("verify: %+v %+v", er, vr)
	}
}
