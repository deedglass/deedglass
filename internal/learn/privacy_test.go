package learn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/policy"
)

// S28 §6 "Privacy": the phrasing request carries the allow-listed PhrasingInput and nothing an
// org chose: no args, SQL, user refs, emails, agent names, rule ids, org or policy names, or
// action ids.

// sentinelPolicy is a policy whose every org-chosen name is a sentinel.
const sentinelPolicy = `deedglass: policy/v1
name: sentinel-policy-name
defaults: { decision: deny, reason: No rule allows this action. }
rules:
  - id: sentinel-rule-approve
    match: { tool: postgres.execute }
    decision: require_approval
    reason: Waits for a person.
`

// sentinels are the strings that must never leave.
var sentinels = []string{"SENTINEL_ARG_7731", "SENTINEL_TABLE", "sentinel_table", "cust_SENTINEL", "priya@acme.example", "acme-secret-bot",
	"agt_sentinel", "sentinel-rule-approve", "sentinel-policy-name", "Acme Sentinel Org", "act_sentinel", "usr_sentinel"}

// sentinelEvidence builds 40 approved updates full of sentinels and returns the evidence.
func sentinelEvidence(t testing.TB) *Evidence {
	t.Helper()
	f := newEvFixture(t, []byte(sentinelPolicy))
	f.org.Name = "Acme Sentinel Org"
	for i := range 40 {
		r := f.add(rowSpec{agentID: "agt_sentinel", agent: "acme-secret-bot", tool: "postgres.execute", ct: "postgres",
			args: map[string]any{
				"sql":      fmt.Sprintf("UPDATE public.SENTINEL_TABLE SET note = 'SENTINEL_ARG_7731 for cust_SENTINEL' WHERE id = %d", i),
				"customer": "cust_SENTINEL", "notify": "priya@acme.example",
			},
			approval: ApprovalApproved, wait: 1260, approvers: []string{"usr_sentinel", "priya@acme.example"}})
		r.ActionID = fmt.Sprintf("act_sentinel%04d", i)
	}
	return f.evidence()
}

func noSentinel(t *testing.T, where string, body []byte, extra ...string) {
	t.Helper()
	low := strings.ToLower(string(body))
	for _, s := range append(slices.Clone(sentinels), extra...) {
		if strings.Contains(low, strings.ToLower(s)) {
			t.Errorf("%s leaks %q:\n%s", where, s, body)
		}
	}
}

// checkBody asserts the request's shape: the top-level keys, one user message whose JSON keys are
// a subset of PhrasingInputKeys, refs that are kinds only, and the size.
func checkBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	if len(body) >= 8<<10 {
		t.Errorf("body is %d bytes, want < 8 KiB", len(body))
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	var top []string
	for k := range req {
		top = append(top, k)
	}
	slices.Sort(top)
	if want := []string{"max_tokens", "messages", "model", "output_config", "system"}; !slices.Equal(top, want) {
		t.Errorf("request keys %v, want %v", top, want)
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["role"] != "user" {
		t.Fatalf("messages %v", msgs)
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(msgs[0].(map[string]any)["content"].(string)), &in); err != nil {
		t.Fatal(err)
	}
	for k := range in {
		if !slices.Contains(PhrasingInputKeys, k) {
			t.Errorf("user message key %q is not allow-listed", k)
		}
	}
	for p, kind := range in["refs"].(map[string]any) {
		if !placeholderRE.MatchString(p) || !slices.Contains([]string{"agent", "rule", "table", "budget", "policy", "tool"}, kind.(string)) {
			t.Errorf("ref %q → %v", p, kind)
		}
	}
	for _, n := range in["numbers"].([]any) {
		for k := range n.(map[string]any) {
			if k != "key" && k != "value" && k != "unit" {
				t.Errorf("number key %q", k)
			}
		}
	}
	return in
}

func TestPhrasingInputKeysMatchStruct(t *testing.T) {
	t.Parallel()
	var tags []string
	typ := reflect.TypeOf(PhrasingInput{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		tags = append(tags, strings.Split(f.Tag.Get("json"), ",")[0])
	}
	if !slices.Equal(tags, PhrasingInputKeys) {
		t.Fatalf("json tags %v != PhrasingInputKeys %v", tags, PhrasingInputKeys)
	}
	// The local values never serialize.
	in := &PhrasingInput{values: map[string]string{"{agent_1}": "acme-secret-bot"}}
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "acme-secret-bot") {
		t.Fatal("values serialized")
	}
}

func TestPrivacySentinels(t *testing.T) {
	t.Parallel()
	ev := sentinelEvidence(t)
	ps, ds := GenLoosenFn(ev)
	if len(ps) != 1 {
		t.Fatalf("fixture: %d proposals, dropped %v", len(ps), ds)
	}
	p := ps[0]
	// The template text itself names them (locally).
	if !strings.Contains(p.Rationale(), "acme-secret-bot") || !strings.Contains(p.Title(), "public.sentinel_table") {
		t.Fatalf("fixture text %q %q", p.Title(), p.Rationale())
	}
	in := BuildPhrasingInput(&p, ev.days())
	if in.Values()["{agent_1}"] != "acme-secret-bot" || in.Values()["{rule_1}"] != "sentinel-rule-approve" {
		t.Fatalf("values %v", in.Values())
	}
	// Capture what actually goes over the wire.
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		writeClaude(w, "end_turn", in.TemplateTitle, in.TemplateRationale)
	}))
	defer srv.Close()
	c := &ClaudePhraser{APIKey: "dg-test-key", HTTP: srv.Client(), BaseURL: srv.URL}
	res, err := c.Phrase(bg, in)
	if err != nil || res.Title != in.TemplateTitle {
		t.Fatalf("%v %+v", err, res)
	}
	if len(bodies) != 1 {
		t.Fatalf("%d requests", len(bodies))
	}
	want, _ := c.RequestBody(in)
	if string(bodies[0]) != string(want) {
		t.Fatal("the wire body differs from RequestBody")
	}
	noSentinel(t, "request body", bodies[0])
	got := checkBody(t, bodies[0])
	if got["tool"] != "postgres.execute" || got["template_title"] != "Allow updates to {table_1} without approval" {
		t.Errorf("content %v", got)
	}
	// The reply is resolved locally.
	if r := in.resolve("{agent_1} on {table_1} and {nope_1}"); r != "acme-secret-bot on public.sentinel_table and {nope_1}" {
		t.Errorf("resolve %q", r)
	}
}

// randName is an org-chosen name: distinctive lowercase letters.
func randName(r *rand.Rand, prefix string) string {
	const letters = "bcdfghjklmnpqrstvwxz"
	b := []byte(prefix)
	for range 10 {
		b = append(b, letters[r.IntN(len(letters))])
	}
	return string(b)
}

// TestPrivacyProperty: 500 random evidence sets with random org-chosen names, through every
// generator; no phrasing request ever carries one of them.
func TestPrivacyProperty(t *testing.T) {
	t.Parallel()
	n := 500
	if testing.Short() {
		n = 60
	}
	const shards = 5 // 5 × 100 seeded iterations, in parallel
	for sh := range shards {
		t.Run(fmt.Sprintf("seed-%d", sh), func(t *testing.T) {
			t.Parallel()
			privacyIterations(t, rand.New(rand.NewPCG(7, uint64(28+sh))), n/shards)
		})
	}
}

func privacyIterations(t *testing.T, r *rand.Rand, n int) {
	c := &ClaudePhraser{APIKey: "dg-test-key"}
	generators := map[string]int{}
	for iter := range n {
		names := map[string]string{
			"policy": randName(r, "zqp"), "deny": randName(r, "zqd"), "approve": randName(r, "zqa"), "payapprove": randName(r, "zqy"),
			"allowwrite": randName(r, "zqw"), "budget": randName(r, "zqb"), "paytool": randName(r, "zqt") + "." + randName(r, "zqo"),
			"othertool": randName(r, "zqu") + "." + randName(r, "zqv"), "table": randName(r, "zqs"), "table2": randName(r, "zqr"),
			"agentA": randName(r, "zqg"), "agentB": randName(r, "zqh"), "agentC": randName(r, "zqj"), "org": randName(r, "zqk"),
			"cust": "cust_" + randName(r, "zqm"), "email": randName(r, "zqn") + "@" + randName(r, "zqx") + ".example", "note": randName(r, "zqz"),
		}
		src := fmt.Sprintf(`deedglass: policy/v1
name: %[1]s
defaults: { decision: deny, reason: No rule allows this action. }
budgets:
  - id: %[6]s
    match: { tool: "%[7]s" }
    per: org
    window: day
    max_calls: 100
rules:
  - id: %[2]s
    match: { tool: "%[7]s" }
    when: 'has(args.amount) && type(args.amount) == int && args.amount > 9000'
    decision: deny
    reason: Too large.
  - id: %[5]s
    match: { tool: postgres.execute }
    when: sql.kind == "insert"
    decision: allow
  - id: %[3]s
    match: { tool: postgres.execute }
    decision: require_approval
    reason: Waits for a person.
  - id: %[4]s
    match: { tool: "%[7]s" }
    decision: require_approval
    reason: Waits for a person.
`, names["policy"], names["deny"], names["approve"], names["payapprove"], names["allowwrite"], names["budget"], names["paytool"])
		f := newEvFixture(t, []byte(src))
		f.org.Name = names["org"]
		args := func(extra map[string]any) map[string]any {
			m := map[string]any{"customer": names["cust"], "email": names["email"], "note": names["note"]}
			for k, v := range extra {
				m[k] = v
			}
			return m
		}
		var orgStrings []string
		for _, v := range names {
			orgStrings = append(orgStrings, v)
		}
		add := func(s rowSpec) {
			row := f.add(s)
			row.ActionID = "act_" + randName(r, "zqa")
			orgStrings = append(orgStrings, row.ActionID, row.AgentID)
		}
		for i := range 30 + r.IntN(5) {
			add(rowSpec{agentID: "agt_" + names["agentA"], agent: names["agentA"], tool: "postgres.execute", ct: "postgres",
				args:     args(map[string]any{"sql": fmt.Sprintf("UPDATE public.%s SET note = '%s' WHERE id = %d", names["table"], names["note"], i)}),
				approval: ApprovalApproved, wait: float64(60 + r.IntN(4000)), approvers: []string{"usr_" + names["agentC"]},
				rule: names["approve"], decision: "require_approval", source: "rule"})
		}
		for i := range 1 + r.IntN(4) {
			add(rowSpec{agentID: "agt_" + names["agentB"], agent: names["agentB"], tool: "postgres.execute", ct: "postgres",
				args: args(map[string]any{"sql": fmt.Sprintf("INSERT INTO public.%s (id) VALUES (%d)", names["table2"], i)}), verdict: "MISMATCH",
				rule: names["allowwrite"], decision: "allow", source: "rule"})
		}
		for range 30 + r.IntN(5) {
			add(rowSpec{agentID: "agt_" + names["agentB"], agent: names["agentB"], tool: names["paytool"], ct: strings.Split(names["paytool"], ".")[0],
				args: args(map[string]any{"amount": 100 + r.IntN(3000), "currency": "usd"}), approval: ApprovalApproved, wait: 600,
				rule: names["payapprove"], decision: "require_approval", source: "rule"})
		}
		for i := range 10 + r.IntN(5) {
			add(rowSpec{at: sugNow.Add(-time.Duration(1+i%3) * 24 * time.Hour), agentID: "agt_" + names["agentA"], agent: names["agentA"], tool: names["othertool"], ct: "zq", args: args(nil),
				rule: "-", decision: "deny", source: "default"})
			f.rows[len(f.rows)-1].RuleID = ""
		}
		for i := range 60 {
			amt := 50
			if i < 25 {
				amt = 9500
			}
			rule, dec := names["payapprove"], "require_approval"
			if amt > 9000 {
				rule, dec = names["deny"], "deny"
			}
			add(rowSpec{agentID: "agt_" + names["agentC"], agent: names["agentC"], tool: names["paytool"], ct: "zq", args: args(map[string]any{"amount": amt}),
				rule: rule, decision: dec, source: "rule"})
		}
		ev := f.evidence()
		var counters []DayCounter
		for d := range 7 {
			counters = append(counters, DayCounter{Scope: policy.ScopeOrg, Counter: policy.BudgetCounter(names["budget"]), Day: sugNow.AddDate(0, 0, -d-1), Value: int64(91 + r.IntN(9))})
		}
		ev.Budgets = BudgetUses(counters, nil, f.base, time.UTC)
		var all []Proposal
		for _, g := range Generators {
			ps, _ := g.Fn(ev)
			all = append(all, ps...)
		}
		if len(all) < 5 {
			t.Fatalf("iteration %d: only %d proposals", iter, len(all))
		}
		for i := range all {
			generators[all[i].Generator]++
			in := BuildPhrasingInput(&all[i], ev.days())
			body, err := c.RequestBody(in)
			if err != nil {
				t.Fatal(err)
			}
			low := strings.ToLower(string(body))
			for _, s := range orgStrings {
				if strings.Contains(low, strings.ToLower(s)) {
					t.Fatalf("iteration %d, %s: the request leaks %q:\n%s", iter, all[i].Generator, s, body)
				}
			}
			if len(body) >= 8<<10 {
				t.Fatalf("iteration %d: %d bytes", iter, len(body))
			}
			// Every placeholder in the template resolves locally.
			for _, p := range placeholderRE.FindAllString(in.TemplateTitle+in.TemplateRationale, -1) {
				if _, ok := in.Values()[p]; !ok {
					t.Fatalf("iteration %d: %s has no value", iter, p)
				}
			}
		}
	}
	for _, g := range []string{GenLoosenApprovalRate, GenTightenMismatch, GenBudgetNearLimit, GenExplicitDefaultHit, GenAdvisoryDenyHeavy} {
		if generators[g] < n {
			t.Errorf("%s ran %d times over %d iterations", g, generators[g], n)
		}
	}
}

// writeClaude answers like the Messages API.
func writeClaude(w http.ResponseWriter, stop, title, rationale string) {
	text, _ := json.Marshal(map[string]string{"title": title, "rationale": rationale})
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "stop_reason": stop,
		"content": []any{map[string]any{"type": "text", "text": string(text)}}, "usage": map[string]any{"input_tokens": 900, "output_tokens": 120}})
}

// TestHostileEnv: every Anthropic SDK and proxy variable set to an attacker's values changes
// nothing: one request, to the pinned server, with the Deedglass key only. No t.Parallel (Setenv).
func TestHostileEnv(t *testing.T) {
	var attacker atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attacker.Add(1)
		w.WriteHeader(500)
	}))
	defer evil.Close()
	for k, v := range map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-attacker", "ANTHROPIC_AUTH_TOKEN": "attacker-token", "ANTHROPIC_BASE_URL": evil.URL,
		"ANTHROPIC_PROFILE": "attacker", "ANTHROPIC_FEDERATION_RULE_ID": "fdrl_attacker", "ANTHROPIC_IDENTITY_TOKEN": "attacker-jwt",
		"ANTHROPIC_IDENTITY_TOKEN_FILE": "/tmp/attacker", "ANTHROPIC_ORGANIZATION_ID": "org-attacker", "ANTHROPIC_SERVICE_ACCOUNT_ID": "svc-attacker",
		"ANTHROPIC_CONFIG_DIR": "/tmp/attacker", "ANTHROPIC_SCOPE": "attacker", "HTTPS_PROXY": evil.URL, "HTTP_PROXY": evil.URL, "https_proxy": evil.URL,
		"http_proxy": evil.URL, "NO_PROXY": "", "no_proxy": "",
	} {
		t.Setenv(k, v)
	}
	var mu sync.Mutex
	var reqs []*http.Request
	pinned := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Clone(context.Background()))
		mu.Unlock()
		writeClaude(w, "end_turn", "Allow updates to {table_1} without approval", "People approved 401 updates to {table_1}.")
	}))
	defer pinned.Close()
	in := &PhrasingInput{Kind: KindLoosen, Refs: map[string]string{"{table_1}": "table"}, Numbers: []Number{{Key: "approved", Value: 401, Unit: "count"}},
		TemplateTitle: "Allow updates to {table_1} without approval", TemplateRationale: "People approved 401 updates to {table_1}."}
	hc := egress.Wrap(pinned.Client().Transport, egress.ClientOptions{Timeout: 5 * time.Second, MaxResponseBytes: 64 << 10})
	c := &ClaudePhraser{APIKey: "dg-deedglass-key", HTTP: hc, BaseURL: pinned.URL}
	if _, err := c.Phrase(bg, in); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || attacker.Load() != 0 {
		t.Fatalf("%d requests to the pinned server, %d to the attacker", len(reqs), attacker.Load())
	}
	r := reqs[0]
	if r.Header.Get("x-api-key") != "dg-deedglass-key" || r.Header.Get("Authorization") != "" || len(r.Header.Values("x-api-key")) != 1 {
		t.Fatalf("headers %v", r.Header)
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			if strings.Contains(v, "attacker") {
				t.Errorf("header %s carries %q", k, v)
			}
		}
	}
	if r.URL.Path != "/v1/messages" || r.Host != strings.TrimPrefix(pinned.URL, "http://") {
		t.Errorf("request went to %s%s", r.Host, r.URL.Path)
	}
	// With no Deedglass key: zero requests.
	reqs = nil
	for _, empty := range []*ClaudePhraser{{HTTP: hc, BaseURL: pinned.URL}, {APIKey: "dg-deedglass-key", BaseURL: pinned.URL}} {
		_, err := empty.Phrase(bg, in)
		var pe *PhraseError
		if !asPhraseError(err, &pe) || pe.Reason != "not_configured" {
			t.Fatalf("no key: %v", err)
		}
	}
	if len(reqs) != 0 || attacker.Load() != 0 {
		t.Fatalf("no key made %d requests (%d to the attacker)", len(reqs), attacker.Load())
	}
	// The pinned host when BaseURL is empty: api.anthropic.com, never the environment's.
	var host string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		host = r.URL.Scheme + "://" + r.URL.Host
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
	})
	_, _ = (&ClaudePhraser{APIKey: "k", HTTP: &http.Client{Transport: rt}}).Phrase(bg, in)
	if host != AnthropicBaseURL || AnthropicBaseURL != "https://api.anthropic.com" {
		t.Fatalf("host %q", host)
	}
	// egress.FirstParty doesn't read proxies unless asked.
	fp := egress.FirstParty(egress.FirstPartyOptions{Timeout: time.Second})
	if p := transportProxy(t, fp); !p.IsValid() || !p.IsNil() {
		t.Fatalf("FirstParty without Proxy has a proxy func")
	}
	if p := transportProxy(t, egress.FirstParty(egress.FirstPartyOptions{Proxy: true})); !p.IsValid() || p.IsNil() {
		t.Fatalf("FirstParty with Proxy has no proxy func")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// transportProxy reaches the *http.Transport inside an egress client (its unexported wrapper's
// next field) and returns its Proxy field, for inspection only.
func transportProxy(t *testing.T, c *http.Client) reflect.Value {
	t.Helper()
	v := reflect.ValueOf(c.Transport)
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if tr, ok := v.Interface().(*http.Transport); ok {
			return reflect.ValueOf(tr).Elem().FieldByName("Proxy")
		}
		v = v.Elem()
	}
	next := v.FieldByName("next")
	if !next.IsValid() {
		t.Skip("egress client layout changed; the proxy check can't reach the transport")
	}
	for next.Kind() == reflect.Interface || next.Kind() == reflect.Pointer {
		if next.Kind() == reflect.Pointer && next.Type() == reflect.TypeOf(&http.Transport{}) {
			return next.Elem().FieldByName("Proxy")
		}
		next = next.Elem()
	}
	t.Skip("egress client layout changed")
	return reflect.Value{}
}

func asPhraseError(err error, pe **PhraseError) bool {
	e, ok := err.(*PhraseError)
	if ok {
		*pe = e
	}
	return ok
}
