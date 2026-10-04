//go:build attack

package attack

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/deedglass/deedglass/attack/harness"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/pkg/deedglass"
)

// TestOrgIsolation (S18 §3.7): a curious org neighbour. Globex holds a valid agent key, an admin
// key and an owner session of its own; it calls every route that takes an id with Acme's ids.
// Every answer must be 404 not_found (never 403, which would confirm the id exists) and must name
// no Acme id; every list and export must show none of Acme's ids; and no mutation may change any
// of Acme's rows. A route that takes an id and has no fixture here fails the test: future
// sessions extend this table, never weaken it.

type spec struct {
	Paths map[string]map[string]struct {
		Auth        string `yaml:"x-deedglass-auth"`
		RequestBody struct {
			Content map[string]struct {
				Example any `yaml:"example"`
			} `yaml:"content"`
		} `yaml:"requestBody"`
	} `yaml:"paths"`
}

// acmeRows are the Acme rows whose digests must not change, and how to read them stably.
var acmeRows = []string{"agents", "api_keys", "connectors", "credentials", "policies", "policy_versions",
	"policy_activations", "actions", "memberships", "orgs"}

func acmeDigest(e *harness.Env, org string) map[string]string {
	out := map[string]string{}
	for _, t := range acmeRows {
		col := "org_id"
		if t == "orgs" {
			col = "id"
		}
		q := fmt.Sprintf(`SELECT coalesce(md5(string_agg((to_jsonb(t) - 'last_used_at')::text, ',' ORDER BY (to_jsonb(t)->>'id'))), '') FROM deedglass.%s AS t WHERE %s = $1`, t, col)
		r := e.CPQuery(q, org)
		if len(r) == 1 {
			out[t] = r[0][0]
		}
	}
	return out
}

func TestOrgIsolation(t *testing.T) {
	e := boot(t)
	acme, globex := e.Orgs["acme"], e.Orgs["globex"]
	if globex.Session == nil || acme.Session == nil {
		t.Fatal("the orgs have no owner sessions; the sign-in link flow changed")
	}
	// Acme's fixtures: one of everything (TestAttack may not have run).
	attacker := acme.Agents["attacker"]
	r := attacker.API.Do("POST", "/v1/actions", map[string]any{"tool": "fake.echo", "args": map[string]any{"message": "fixture"}, "mode": "gateway", "wait": 5})
	act := harness.Str(r.Map(), "id")
	deed := harness.Str(acme.Admin.Do("GET", "/v1/actions/"+act+"?wait=5", nil).Map(), "deed")
	ib := acme.Agents["intent-bot"]
	kid := firstKID(e, acme, ib.ID)
	member := e.CPQuery("SELECT id FROM deedglass.memberships WHERE org_id = $1 LIMIT 1", acme.ID)[0][0]
	// S22: a pending approval (approval-bot meets the attack-baseline's approval rule; the owner can
	// countersign) and a webhook to a loopback listener.
	if acme.Agents["approval-bot"] == nil {
		e.AddAgent(acme, "approval-bot", nil)
	}
	pr := acme.Agents["approval-bot"].API.Do("POST", "/v1/actions", map[string]any{"tool": "fake.echo", "args": map[string]any{"message": "fixture"},
		"mode": "gateway", "wait": 0})
	apr := harness.Str(pr.Map(), "approval", "id")
	wh := harness.Str(acme.Admin.Do("POST", "/v1/webhooks", map[string]any{"url": e.Up.Orders.URL + "/hook",
		"events": []string{"approval.decided"}}).Map(), "id")
	// S27: a replay of Acme's policy, and an anomaly episode (written as the superuser: detection
	// would need a week of history).
	rpl := harness.Str(acme.Admin.Do("POST", "/v1/policies/"+acme.Policy+"/versions/1/replay", map[string]any{}).Map(), "id")
	anm := e.CPQuery(`INSERT INTO deedglass.anomalies (id, org_id, agent_id, kind, severity, window_start, window_end, score, episode_key,
	    active_until, detector_version, title)
	  VALUES ($1, $2, $3, 'volume_spike', 'warn', now() - interval '1 hour', now(), 4.2, $4, now() - interval '1 minute', '1',
	    'fixture') RETURNING id`, ids.New[ids.AnomalyKind]().String(), acme.ID, attacker.ID, attacker.ID+"|volume_spike|")[0][0]
	// S28: a suggestion (advice, written as the superuser: a suggest pass needs 14 days of history).
	sug := e.CPQuery(`INSERT INTO deedglass.suggestions (id, org_id, kind, title, generator, generator_version, fingerprint, policy_id,
	    base_version_id, base_version_hash, evidence_digest, window_from, window_to)
	  SELECT $1::text, $2::text, 'advisory', 'fixture', 'advisory.deny_heavy', '1', 'sha256:' || repeat('a', 64), v.policy_id, v.id, v.version_hash,
	    'sha256:' || repeat('b', 64), now() - interval '14 days', now()
	  FROM deedglass.policy_versions AS v WHERE v.org_id = $2::text AND v.id = $3::text RETURNING id`, ids.New[ids.SuggestionKind]().String(), acme.ID, acme.PolicyV)[0][0]
	acmeIDs := []string{acme.ID, attacker.ID, attacker.KeyID, act, deed, acme.Policy, acme.PolicyV, acme.Conns["orders"], acme.Conns["fs"], member, kid,
		apr, wh, rpl, anm, sug}
	for _, id := range acmeIDs {
		if id == "" {
			t.Fatalf("an Acme fixture is missing: %v", acmeIDs)
		}
	}
	// path parameter → Acme's value, per route prefix.
	fixtures := map[string]map[string]string{
		"/v1/actions/{id}":                                      {"id": act},
		"/v1/actions/{id}/cancel":                               {"id": act},
		"/v1/actions/{id}/result":                               {"id": act},
		"/v1/actions/{id}/undo":                                 {"id": act},
		"/v1/actions/{id}/undo/preview":                         {"id": act},
		"/v1/deeds/{id}":                                        {"id": deed},
		"/v1/org/members/{id}":                                  {"id": member},
		"/v1/agents/{id}":                                       {"id": attacker.ID},
		"/v1/agents/{id}/suspend":                               {"id": attacker.ID},
		"/v1/agents/{id}/resume":                                {"id": attacker.ID},
		"/v1/agents/{id}/stop":                                  {"id": attacker.ID},
		"/v1/agents/{id}/start":                                 {"id": attacker.ID},
		"/v1/agents/{id}/keys":                                  {"id": attacker.ID},
		"/v1/agents/{id}/intent-keys":                           {"id": ib.ID},
		"/v1/agents/{id}/intent-keys/{kid}":                     {"id": ib.ID, "kid": kid},
		"/v1/keys/{id}/roll":                                    {"id": attacker.KeyID},
		"/v1/keys/{id}/revoke":                                  {"id": attacker.KeyID},
		"/v1/policies/{id}":                                     {"id": acme.Policy},
		"/v1/policies/{id}/versions":                            {"id": acme.Policy},
		"/v1/policies/{id}/versions/{v}":                        {"id": acme.Policy, "v": "1"},
		"/v1/policies/{id}/versions/{v}/activate":               {"id": acme.Policy, "v": "1"},
		"/v1/policies/{id}/versions/{v}/deactivate":             {"id": acme.Policy, "v": "1"},
		"/v1/connectors/{id}":                                   {"id": acme.Conns["orders"]},
		"/v1/connectors/{id}/credentials":                       {"id": acme.Conns["orders"]},
		"/v1/connectors/{id}/test":                              {"id": acme.Conns["orders"]},
		"/v1/connectors/{id}/initialized":                       {"id": acme.Conns["postgres"]},
		"/v1/connectors/{id}/decoder":                           {"id": acme.Conns["postgres"]},
		"/v1/approvals/{id}":                                    {"id": apr},
		"/v1/approvals/{id}/approve":                            {"id": apr},
		"/v1/approvals/{id}/deny":                               {"id": apr},
		"/v1/webhooks/{id}":                                     {"id": wh},
		"/v1/webhooks/{id}/deliveries":                          {"id": wh},
		"/v1/webhooks/{id}/test":                                {"id": wh},
		"/v1/policies/{id}/versions/{v}/replay":                 {"id": acme.Policy, "v": "1"},
		"/v1/replays/{id}":                                      {"id": rpl},
		"/v1/replays/{id}/cancel":                               {"id": rpl},
		"/v1/insights/anomalies/{id}":                           {"id": anm},
		"/v1/insights/anomalies/{id}/acknowledge":               {"id": anm},
		"/v1/insights/anomalies/{id}/resolve":                   {"id": anm},
		"/v1/suggestions/{id}":                                  {"id": sug},
		"/v1/suggestions/{id}/accept":                           {"id": sug},
		"/v1/suggestions/{id}/reject":                           {"id": sug},
		"/mcp/{connector}":                                      {"connector": "fs"},
		"/.well-known/oauth-protected-resource/mcp/{connector}": nil, // public metadata: no org data
		"/auth/assets/{file}":                                   nil, // public static file
		"/approve/assets/{file}":                                nil, // public static file (S22's approval page)
		"/app/{path...}":                                        nil, // S24's Console pages: public static files with no org data
		"/v1/connector-types/{type}":                            nil, // S30: the deployment-wide catalog; a type name, no org data
	}
	raw, err := os.ReadFile(filepath.Join(e.Root, "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var sp spec
	if err := yaml.Unmarshal(raw, &sp); err != nil {
		t.Fatal(err)
	}
	before := acmeDigest(e, acme.ID)
	var paths []string
	for p := range sp.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	covered, lists, notFound, refusedEarly := 0, 0, 0, 0
	agentKey := globex.Agents["globex-bot"].API
	for _, p := range paths {
		methods := sp.Paths[p]
		var ms []string
		for m := range methods {
			ms = append(ms, m)
		}
		sort.Strings(ms)
		for _, m := range ms {
			op := methods[m]
			method := strings.ToUpper(m)
			if !strings.Contains(p, "{") {
				if method == "GET" && op.Auth != "public" {
					lists++
					for _, c := range clientsFor(op.Auth, agentKey, globex) {
						var r harness.Response
						if p == "/v1/events" {
							r = c.Peek(p, 2*time.Second) // S24: a stream; read what it sends for a moment
						} else {
							r = c.Do("GET", p, nil)
						}
						if leaked := leaks(r.Body, acmeIDs); leaked != "" {
							t.Errorf("GET %s as globex shows Acme's %s", p, leaked)
						}
					}
				}
				continue
			}
			fx, ok := fixtures[p]
			if !ok {
				t.Errorf("Add an isolation fixture for %s %s", method, p)
				continue
			}
			if fx == nil || op.Auth == "public" {
				continue
			}
			covered++
			path := p
			for k, v := range fx {
				path = strings.ReplaceAll(path, "{"+k+"}", v)
			}
			var body any
			if c, ok := op.RequestBody.Content["application/json"]; ok {
				body = c.Example
				if body == nil {
					body = map[string]any{}
				}
			}
			// The control: the same request with a fresh id of the same kind that exists nowhere.
			// Globex must get the same answer for Acme's id as for an id that doesn't exist, so
			// nothing confirms that Acme's id exists.
			control := p
			sent := map[string]bool{}
			for k, v := range fx {
				control = strings.ReplaceAll(control, "{"+k+"}", unknownLike(v))
				sent[v] = true
			}
			for _, c := range clientsFor(op.Auth, agentKey, globex) {
				r := c.Do(method, path, body)
				ctl := c.Do(method, control, body)
				code, ctlCode := r.ErrorCode(), ctl.ErrorCode()
				if strings.HasPrefix(p, "/mcp/") {
					code, ctlCode = harness.Str(r.Map(), "error", "data", "code"), harness.Str(ctl.Map(), "error", "data", "code")
				}
				switch {
				case r.Status < 400 || r.Status == 403 || r.Status >= 500:
					t.Errorf("%s %s as globex (%s): HTTP %d %s; want 404 not_found", method, p, op.Auth, r.Status, code)
				case r.Status != ctl.Status || code != ctlCode:
					t.Errorf("%s %s as globex (%s): HTTP %d %s for Acme's id but HTTP %d %s for an unknown one (existence leaks)",
						method, p, op.Auth, r.Status, code, ctl.Status, ctlCode)
				case r.Status == 404:
					notFound++
				default:
					refusedEarly++ // validation runs before the lookup: the same answer for any id
				}
				var others []string
				for _, id := range acmeIDs {
					if !sent[id] {
						others = append(others, id)
					}
				}
				if leaked := leaks(r.Body, others); leaked != "" {
					t.Errorf("%s %s as globex names Acme's %s", method, p, leaked)
				}
			}
		}
	}
	// S25: the Overview, both windows and the agents' usage expansion, as globex: none of Acme's
	// ids (agents, actions, approvals, connectors) and 200 for globex's own credentials; an agent
	// key is refused before any read.
	for _, p := range []string{"/v1/overview", "/v1/overview?window=7d", "/v1/agents?expand[]=usage"} {
		for _, c := range []*harness.Client{globex.Admin, globex.Session} {
			r := c.Do("GET", p, nil)
			if r.Status != 200 {
				t.Errorf("GET %s as globex: HTTP %d %s", p, r.Status, r.ErrorCode())
			}
			if leaked := leaks(r.Body, acmeIDs); leaked != "" {
				t.Errorf("GET %s as globex shows Acme's %s", p, leaked)
			}
		}
	}
	if r := agentKey.Do("GET", "/v1/overview", nil); r.Status != 403 || r.ErrorCode() != "insufficient_scope" {
		t.Errorf("GET /v1/overview with globex's agent key: HTTP %d %s, want 403 insufficient_scope", r.Status, r.ErrorCode())
	}
	// Exports: globex's deed export holds none of Acme's ids.
	exp := globex.Admin.Do("GET", "/v1/deeds/export", nil)
	if leaked := leaks(exp.Body, acmeIDs); leaked != "" {
		t.Errorf("globex's export holds Acme's %s", leaked)
	}
	after := acmeDigest(e, acme.ID)
	for k, v := range before {
		if after[k] != v {
			t.Errorf("Acme's %s rows changed during globex's sweep", k)
		}
	}
	t.Logf("org isolation: %d id-bearing routes swept with each credential kind they accept (%d answers 404 not_found, %d refused by validation before the lookup, the same as for an unknown id), %d list routes, %d Acme ids watched, %d Acme tables digested",
		covered, notFound, refusedEarly, lists, len(acmeIDs), len(before))
	if covered < 20 {
		t.Errorf("only %d id-bearing routes were swept; the spec walk is broken", covered)
	}
}

// clientsFor returns globex's credentials of the kinds the route accepts.
func clientsFor(auth string, agentKey *harness.Client, g *harness.Org) []*harness.Client {
	switch auth {
	case "agent_key":
		return []*harness.Client{agentKey}
	case "session":
		return []*harness.Client{g.Session}
	case "session_or_key":
		return []*harness.Client{g.Admin, g.Session}
	}
	return nil
}

func leaks(body []byte, ids []string) string {
	for _, id := range ids {
		if id != "" && strings.Contains(string(body), id) {
			return id
		}
	}
	return ""
}

func firstKID(e *harness.Env, o *harness.Org, agent string) string {
	ks := o.Admin.Do("GET", "/v1/agents/"+agent+"/intent-keys", nil).Map()
	if data, _ := ks["data"].([]any); len(data) > 0 {
		return harness.Str(data[0], "kid")
	}
	// TestAttack's J03 registers one; when this test runs alone, register one now.
	_, pub, _, err := deedglass.GenerateIntentKey()
	if err != nil {
		panic(err)
	}
	_ = e
	return harness.Str(o.Admin.Do("POST", "/v1/agents/"+agent+"/intent-keys", map[string]string{"public_key": pub}).Map(), "kid")
}

// unknownLike returns an id of the same kind that exists nowhere: the same prefix and a fresh
// ULID, or for names and numbers a value nobody uses.
func unknownLike(v string) string {
	if pfx, _, ok := strings.Cut(v, "_"); ok && len(v) == len(pfx)+27 {
		if id, err := ids.NewString(pfx); err == nil {
			return id
		}
	}
	switch v {
	case "1":
		return "999"
	}
	if len(v) == 32 { // an intent key id (hex)
		return strings.Repeat("0", 32)
	}
	return "nobody-" + v
}
