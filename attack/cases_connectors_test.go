//go:build attack

package attack

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/attack/harness"
)

// Connectors, egress and secrets: K01–K04 (S18 §3.4). A hostile config, a hostile upstream, and a
// careless one. The harness runs in dev with the private-network override (so it can reach its
// own loopback mocks): metadata and cross-host targets must stay blocked even then.

func restGet(base, redirects string, tools ...[2]string) map[string]any {
	ts := []any{}
	for _, t := range tools {
		tool := map[string]any{"name": t[0], "method": "GET", "path": t[1]}
		if strings.Contains(t[1], "{id}") {
			tool["args"] = map[string]any{"id": map[string]any{"in": "path", "type": "string", "pattern": "^[a-z0-9-]{1,32}$", "required": true}}
		}
		ts = append(ts, tool)
	}
	cfg := map[string]any{"base_url": base, "auth": map[string]any{"kind": "bearer"}, "allowed_methods": []string{"GET"},
		"allowed_path_prefixes": []string{"/"}, "tools": ts, "timeout_ms": 3000}
	if redirects != "" {
		cfg["redirects"] = redirects
	}
	return cfg
}

func init() {
	register(
		harness.Case{ID: "K01", Run: k01},
		harness.Case{ID: "K02", Run: k02},
		harness.Case{ID: "K03", Run: k03},
		harness.Case{ID: "K04", Run: k04},
	)
}

// K01: SSRF to cloud metadata. (a) an admin's config naming it is refused at create; (b) a host
// that resolved to a public address at create and to metadata at call time is refused at dial.
func k01(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: []string{"orders", "other", "redirector", "secret-echo", "mcp"}}
	tok := map[string]string{"token": h.Sentinel}
	configOK := true
	for i, c := range []struct{ typ, name, url string }{
		{"rest", "meta", "http://metadata.attack.test"},
		{"rest", "meta", "http://169.254.169.254"},
		{"mcp_http", "metamcp", "http://169.254.169.254/mcp"},
	} {
		var cfg map[string]any
		if c.typ == "rest" {
			cfg = restGet(c.url, "", [2]string{"get", "/latest/meta-data/{id}"})
		} else {
			cfg = map[string]any{"url": c.url, "auth": map[string]any{"kind": "bearer"}}
		}
		r := h.Env.AddConnector(h.Acme, c.typ, c.name, cfg, "bearer", tok)
		obs.Evidence[fmt.Sprintf("config_%d", i+1)] = map[string]any{"url": c.url, "type": c.typ, "status": r.Status, "error.code": r.ErrorCode(),
			"param": harness.Str(r.Map(), "error", "param")}
		if r.Status != 400 || r.ErrorCode() != "destination_blocked" {
			configOK = false
		}
	}
	// (b) DNS rebinding: public at create, metadata at call time.
	h.Env.SetHost("rebind.attack.test", "93.184.216.34")
	r := h.Env.AddConnector(h.Acme, "rest", "rebind", restGet("http://rebind.attack.test", "", [2]string{"get", "/latest/meta-data/{id}"}), "bearer", tok)
	created := r.Status == 201
	h.Env.SetHost("rebind.attack.test", "169.254.169.254")
	resp, id := h.Act(h.Acme.Agents["attacker"], "rebind.get", map[string]any{"id": "iam"})
	if id != "" {
		obs.ActionIDs = append(obs.ActionIDs, id)
	}
	dial := h.FailedWith(h.Acme, id, "destination_blocked")
	a := h.Action(h.Acme, id)
	obs.Evidence["rebind"] = map[string]any{"create_status": r.Status, "call_status": resp.Status, "action": id, "differs": dial,
		"error_message": harness.Str(a, "execution", "error_message")}
	switch {
	case configOK && created && dial == "":
		obs.Outcome, obs.Detail = "BLOCKED", "BLOCKED (destination_blocked at config and at dial; 0 packets)"
		obs.Line = "destination_blocked · config and dial · 0 packets"
	case !configOK:
		obs.Outcome, obs.Detail = "CREATED", "a metadata destination was accepted at config"
	default:
		obs.Outcome, obs.Detail = "NOT BLOCKED AT DIAL", dial
	}
	return obs
}

// K02: the upstream answers 302 to cloud metadata, then 302 to another host. Neither is followed.
func k02(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: []string{"other", "orders", "secret-echo", "mcp"}}
	r := h.Env.AddConnector(h.Acme, "rest", "redirect", restGet(h.Up.Redirector.URL, "same_host",
		[2]string{"meta", "/hop/meta"}, [2]string{"cross", "/hop/cross"}), "bearer", map[string]string{"token": h.Sentinel})
	if r.Status != 201 {
		panic(fmt.Sprintf("create redirect: HTTP %d %s", r.Status, r.Body))
	}
	all := true
	for _, tool := range []string{"redirect.meta", "redirect.cross"} {
		_, id := h.Act(h.Acme.Agents["attacker"], tool, map[string]any{})
		obs.ActionIDs = append(obs.ActionIDs, id)
		p := h.FailedWith(h.Acme, id, "destination_blocked")
		a := h.Action(h.Acme, id)
		obs.Evidence[tool] = map[string]any{"action": id, "differs": p, "error_code": harness.Str(a, "execution", "error_code"),
			"error_message": harness.Str(a, "execution", "error_message")}
		if p != "" {
			all = false
		}
	}
	obs.Evidence["redirector_hits"], obs.Evidence["other_hits"] = h.HitsSince("redirector"), h.HitsSince("other")
	if all && h.HitsSince("other") == 0 {
		obs.Outcome, obs.Detail = "BLOCKED", "BLOCKED (redirect refused; 0 hits on the target)"
		obs.Line = "destination_blocked · 0 hits on the target"
	} else {
		obs.Outcome, obs.Detail = "FOLLOWED?", fmt.Sprintf("other hits %d", h.HitsSince("other"))
	}
	return obs
}

// K03: an upstream echoes the credential in its error body and in WWW-Authenticate; a Postgres DSN
// carries the sentinel as its password. The sentinel must appear nowhere.
func k03(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: []string{"other", "orders", "mcp"}}
	r := h.Env.AddConnector(h.Acme, "rest", "secrets", restGet(h.Up.SecretEcho.URL, "", [2]string{"get", "/v1/whoami/{id}"}), "bearer",
		map[string]string{"token": h.Sentinel})
	if r.Status != 201 {
		panic(fmt.Sprintf("create secrets: HTTP %d %s", r.Status, r.Body))
	}
	_, id := h.Act(h.Acme.Agents["attacker"], "secrets.get", map[string]any{"id": "1"})
	obs.ActionIDs = append(obs.ActionIDs, id)
	a := h.WaitSealed(h.Acme, id)
	obs.Evidence["rest"] = map[string]any{"action": id, "execution": harness.Str(a, "execution", "status"), "error_code": harness.Str(a, "execution", "error_code")}
	test := h.Acme.Admin.Do("POST", "/v1/connectors/"+h.Acme.Conns["secrets"]+"/test", nil)
	obs.Evidence["rest_test_status"] = harness.Str(test.Map(), "status")
	// A Postgres connector whose password is the sentinel.
	u, err := url.Parse(h.Target.DSN)
	if err != nil {
		panic(err)
	}
	u.User = url.UserPassword(u.User.Username(), h.Sentinel)
	cfg := map[string]any{"mode": "sql", "allowed_schemas": []string{"public"}, "throwaway": true}
	pr := h.Env.AddConnector(h.Acme, "postgres", "pgsecret", cfg, "dsn", map[string]string{"dsn": u.String()})
	obs.Evidence["pgsecret_create"] = pr.Status
	_, pid := h.Act(h.Acme.Agents["attacker"], "pgsecret.query", map[string]any{"sql": "SELECT 1"})
	if pid != "" {
		obs.ActionIDs = append(obs.ActionIDs, pid)
	}
	pa := h.WaitSealed(h.Acme, pid)
	obs.Evidence["postgres"] = map[string]any{"action": pid, "execution": harness.Str(pa, "execution", "status"), "error_code": harness.Str(pa, "execution", "error_code")}
	ptest := h.Acme.Admin.Do("POST", "/v1/connectors/"+h.Acme.Conns["pgsecret"]+"/test", nil)
	obs.Evidence["pg_test_status"] = harness.Str(ptest.Map(), "status")
	n, where := h.Env.SentinelHits()
	obs.Evidence["sentinel_hits"], obs.Evidence["where"] = n, where
	obs.Evidence["upstream_saw_sentinel"] = h.Up.SecretEchoSawSentinel.Load()
	switch {
	case !h.Up.SecretEchoSawSentinel.Load():
		obs.Outcome, obs.Detail = "VACUOUS", "the upstream never received the sentinel"
	case harness.Str(a, "execution", "status") != "failed" || harness.Str(pa, "execution", "status") != "failed":
		obs.Outcome, obs.Detail = "UNEXPECTED", "a call with a bad credential didn't fail"
	case n == 0:
		obs.Outcome, obs.Detail = "REDACTED", "REDACTED (0 sentinel hits in responses, logs, events, deeds)"
		obs.Line = fmt.Sprintf("0 sentinel hits · %s, %s", harness.Str(a, "execution", "error_code"), harness.Str(pa, "execution", "error_code"))
	default:
		obs.Outcome, obs.Detail = "LEAKED", fmt.Sprintf("%d sentinel hits (%s)", n, where)
	}
	return obs
}

// K04: an upstream MCP server (an mcp_http connector, credential in the vault) lists read_file,
// then renames its tools to look-alikes.
func k04(h *harness.H) harness.Observed {
	obs := harness.Observed{Evidence: map[string]any{}, Forbidden: []string{"other", "orders", "secret-echo"}}
	ag := h.Acme.Agents["attacker"]
	if r := ag.API.MCPInit("fs"); r.Status != 200 {
		panic(fmt.Sprintf("MCP initialize on /mcp/fs: HTTP %d %s", r.Status, r.Body))
	}
	names := func() []string {
		_, list := ag.API.MCP("fs", "tools/list", 1, map[string]any{})
		var out []string
		ts, _ := harness.Get(list, "result", "tools").([]any)
		for _, t := range ts {
			if n := harness.Str(t, "name"); !strings.HasPrefix(n, "deedglass.") {
				out = append(out, n)
			}
		}
		return out
	}
	before := names()
	_, res := ag.API.MCP("fs", "tools/call", 2, map[string]any{"name": "read_file", "arguments": map[string]any{"path": "notes.txt"}})
	firstAction := harness.Str(res, "result", "_meta", "com.deedglass/action")
	if firstAction != "" {
		obs.ActionIDs = append(obs.ActionIDs, firstAction)
	}
	ranFirst := h.Up.MCPCalls("read_file") == 1
	h.Up.RenameMCP()
	time.Sleep(400 * time.Millisecond) // the connector's tools_ttl_ms is 100
	after := names()
	joined := "|" + strings.Join(after, "|") + "|"
	// The non-ASCII names are hidden by the charset (and logged); the ASCII look-alikes are hidden
	// because the policy always denies them (tool visibility), yet stay callable, and are denied.
	hidden := !strings.Contains(joined, "|"+harness.K04Cyrillic+"|") && !strings.Contains(joined, "|read_file |")
	policyHidden := !strings.Contains(joined, "|read_fiIe|") && !strings.Contains(joined, "|read_files|")
	logged := strings.Count(string(h.Env.Logs.Bytes()), "from fs: the name isn't plain ASCII") >= 2
	obs.Evidence["tools_before"], obs.Evidence["tools_after"], obs.Evidence["hidden_logged"] = before, after, logged
	// The ASCII look-alikes are known to nobody's policy: denied by the default, with a deed.
	deniedOK := true
	for _, name := range []string{"read_fiIe", "read_files"} {
		_, res := ag.API.MCP("fs", "tools/call", 3, map[string]any{"name": name, "arguments": map[string]any{"path": "notes.txt"}})
		id := harness.Str(res, "result", "_meta", "com.deedglass/action")
		if id != "" {
			obs.ActionIDs = append(obs.ActionIDs, id)
		}
		p := h.DeniedBy(h.Acme, id, "default")
		isErr, _ := harness.Get(res, "result", "isError").(bool)
		obs.Evidence["call "+name] = map[string]any{"action": id, "isError": isErr, "differs": p, "ran": h.Up.MCPCalls(name)}
		if p != "" || !isErr || h.Up.MCPCalls(name) != 0 {
			deniedOK = false
		}
	}
	// The non-ASCII names: not callable over MCP, and tool_unknown through the API.
	rejectedOK := true
	for _, name := range []string{harness.K04Cyrillic, "read_file "} {
		r, res := ag.API.MCP("fs", "tools/call", 4, map[string]any{"name": name, "arguments": map[string]any{"path": "notes.txt"}})
		isErr, _ := harness.Get(res, "result", "isError").(bool)
		rpcErr := harness.Get(res, "error") != nil
		api, id := h.Act(ag, "fs."+name, map[string]any{"path": "notes.txt"})
		if id != "" {
			obs.ActionIDs = append(obs.ActionIDs, id)
		}
		obs.Evidence[fmt.Sprintf("call %q", name)] = map[string]any{"mcp_status": r.Status, "mcp_error": rpcErr, "mcp_isError": isErr,
			"api_status": api.Status, "api_code": api.ErrorCode(), "ran": h.Up.MCPCalls(name)}
		apiOK := api.ErrorCode() == "tool_unknown" || (api.Status == 400 && api.ErrorCode() == "validation_failed")
		if !(rpcErr || isErr) || !apiOK || h.Up.MCPCalls(name) != 0 {
			rejectedOK = false
		}
	}
	switch {
	case !ranFirst || len(before) != 1 || before[0] != "read_file":
		obs.Outcome, obs.Detail = "SETUP", fmt.Sprintf("the first listing was %v and read_file ran %t", before, ranFirst)
	case deniedOK && hidden && policyHidden && logged && rejectedOK:
		obs.Outcome, obs.Detail = "DENIED", "DENIED (default) for ASCII look-alikes; non-ASCII names hidden and REJECTED (tool_unknown)"
		obs.Line = "default deny · 2 look-alikes hidden · tool_unknown"
	default:
		obs.Outcome = "DENIED?"
		obs.Detail = fmt.Sprintf("denied %t, hidden %t, policy-hidden %t, logged %t, rejected %t", deniedOK, hidden, policyHidden, logged, rejectedOK)
	}
	return obs
}
