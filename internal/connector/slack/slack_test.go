package slack

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/policy"
	"github.com/deedglass/deedglass/internal/vault"
	"go.yaml.in/yaml/v3"
)

func TestInfo(t *testing.T) {
	c := New()
	info := c.Info()
	if c.Type() != "slack" || info.Type != "slack" || info.Title != "Slack" || info.DocsURL != "https://docs.deedglass.com/connectors/slack" {
		t.Fatalf("info = %+v", info)
	}
	if strings.Join(info.Levels, ",") != "L0,L1" {
		t.Fatalf("levels = %v", info.Levels)
	}
	if len(info.CredentialKinds) != 1 || info.CredentialKinds[0].Kind != "fields" || len(info.CredentialKinds[0].Fields) != 2 ||
		info.CredentialKinds[0].Fields[0].Name != "bot_token" || !info.CredentialKinds[0].Fields[0].Secret ||
		info.CredentialKinds[0].Fields[0].PrefixHint != "xoxb-" ||
		info.CredentialKinds[0].Fields[1].Name != "signing_secret" || !info.CredentialKinds[0].Fields[1].Secret {
		t.Fatalf("credential kinds = %+v", info.CredentialKinds)
	}
	if !json.Valid(info.ConfigSchema) {
		t.Fatal("config schema isn't JSON")
	}
}

func TestToolsAreClosedAndDeclared(t *testing.T) {
	specs := toolSpecs(7)
	want := map[string]connector.SideEffect{ToolListChannels: "read", ToolReadChannel: "read", ToolPostMessage: "write", ToolAddReaction: "write"}
	if len(specs) != 4 {
		t.Fatalf("%d tools", len(specs))
	}
	for _, s := range specs {
		if want[s.Name] != s.SideEffect {
			t.Errorf("%s side effect %s", s.Name, s.SideEffect)
		}
		var sch map[string]any
		if err := json.Unmarshal(s.InputSchema, &sch); err != nil || sch["additionalProperties"] != false {
			t.Errorf("%s schema isn't closed: %s", s.Name, s.InputSchema)
		}
		if err := connector.ValidateArgs(s.InputSchema, json.RawMessage(`{}`)); s.Name == ToolListChannels && err != nil {
			t.Errorf("list_channels refuses {}: %v", err)
		}
		if err := connector.ValidateArgs(s.InputSchema, json.RawMessage(`{"extra":1}`)); err == nil {
			t.Errorf("%s accepts an unknown argument", s.Name)
		}
	}
	if specs[2].L1 != L1PostMessage || specs[3].L1 != L1AddReaction || specs[0].L1 != "" || specs[1].L1 != "" {
		t.Fatal("L1 sentences are on the wrong tools")
	}
	if err := connector.ValidateArgs(specs[1].InputSchema, json.RawMessage(`{"channel":"C0AGENT01","limit":8}`)); err == nil {
		t.Fatal("read_channel accepts a limit above max_read_messages")
	}
	if err := connector.ValidateArgs(specs[2].InputSchema, json.RawMessage(`{"channel":"#a","text":"`+strings.Repeat("é", 4001)+`"}`)); err == nil {
		t.Fatal("post_message accepts 4,001 characters")
	}
}

func TestValidateNormalizes(t *testing.T) {
	raw := `{"team_id":"T0123ABCD","app_id":"A0123ABCD","channels":["#Agent-Updates","C0AGENT01","#agent-updates"],
	  "approvals":{"enabled":true,"channel":"#Deedglass-Approvals","require_email_domains":["@Acme.COM"," acme.com","example.org"]}}`
	out, errs := New().Validate(context.Background(), connector.Env{Name: "prod"}, json.RawMessage(raw))
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var cfg Config
	_ = json.Unmarshal(out, &cfg)
	if strings.Join(cfg.Channels, ",") != "#agent-updates,C0AGENT01" || len(cfg.ReadChannels) != 0 || cfg.MaxReadMessages != 20 ||
		cfg.APIBaseURL != "https://slack.com/api" || cfg.Approvals.Channel != "#deedglass-approvals" ||
		strings.Join(cfg.Approvals.RequireEmailDomains, ",") != "acme.com,example.org" {
		t.Fatalf("normalized = %s", out)
	}
	// Normalizing twice changes nothing.
	again, errs := New().Validate(context.Background(), connector.Env{Name: "prod"}, out)
	if len(errs) > 0 || string(again) != string(out) {
		t.Fatalf("not idempotent: %s vs %s %v", again, out, errs)
	}
}

func TestValidateRefusals(t *testing.T) {
	base := `"team_id":"T0123ABCD","app_id":"A0123ABCD"`
	cases := []struct {
		name, raw, param string
		dev              bool
	}{
		{"unknown key", `{` + base + `,"channel":"#x"}`, "config.channel", false},
		{"unknown approvals key", `{` + base + `,"approvals":{"enabled":true,"channel":"#a","notify":true}}`, "config.approvals.notify", false},
		{"team id", `{"team_id":"t0123","app_id":"A0123ABCD"}`, "config.team_id", false},
		{"app id", `{"team_id":"T0123ABCD","app_id":"B0123"}`, "config.app_id", false},
		{"star with others", `{` + base + `,"channels":["*","#a"]}`, "config.channels[0]", false},
		{"bad channel", `{` + base + `,"read_channels":["agent updates"]}`, "config.read_channels[0]", false},
		{"max read", `{` + base + `,"max_read_messages":101}`, "config.max_read_messages", false},
		{"approvals without channel", `{` + base + `,"approvals":{"enabled":true}}`, "config.approvals.channel", false},
		{"approvals channel", `{` + base + `,"approvals":{"channel":"*"}}`, "config.approvals.channel", false},
		{"domain", `{` + base + `,"approvals":{"require_email_domains":["not a domain"]}}`, "config.approvals.require_email_domains[0]", false},
		{"http outside dev", `{` + base + `,"api_base_url":"http://slack.com/api"}`, "config.api_base_url", false},
		{"other host outside dev", `{` + base + `,"api_base_url":"https://slack.example.com/api"}`, "config.api_base_url", false},
		{"ftp in dev", `{` + base + `,"api_base_url":"ftp://127.0.0.1/api"}`, "config.api_base_url", true},
		{"userinfo", `{` + base + `,"api_base_url":"https://u:p@slack.com/api"}`, "config.api_base_url", true},
		{"not an object", `[]`, "config", false},
		{"wrong type", `{` + base + `,"channels":"#a"}`, "config", false},
		{"too large", `{` + base + `,"channels":["` + strings.Repeat("a", 17<<10) + `"]}`, "config", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := connector.Env{Name: "prod"}
			if c.dev {
				env = devEnv()
			}
			out, errs := New().Validate(context.Background(), env, json.RawMessage(c.raw))
			if out != nil || len(errs) == 0 {
				t.Fatalf("accepted: %s", out)
			}
			if errs[0].Param != c.param {
				t.Fatalf("param %q, want %q (%v)", errs[0].Param, c.param, errs)
			}
			if !strings.HasSuffix(errs[0].Message, ".") {
				t.Fatalf("message without a period: %q", errs[0].Message)
			}
		})
	}
	// In dev any http(s) URL works (tests reach httptest).
	if _, errs := New().Validate(context.Background(), devEnv(), json.RawMessage(`{`+base+`,"api_base_url":"http://127.0.0.1:9/api"}`)); len(errs) > 0 {
		t.Fatal(errs)
	}
	if _, errs := New().Validate(context.Background(), connector.Env{Name: "prod"}, json.RawMessage(`{`+base+`,"api_base_url":"https://slack.com/api/"}`)); len(errs) > 0 {
		t.Fatal(errs)
	}
}

func TestPostMessageVerified(t *testing.T) {
	r := newRig(t, nil, fixtures(t, "channels", "post_message")...)
	args := `{"channel":"#agent-updates","text":"a & b <c>"}`
	res, err := r.call(ToolPostMessage, args)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.Output) != `{"channel":"C0AGENT01","ts":"1760000000.000100"}` {
		t.Fatalf("output %s", res.Output)
	}
	if r.srv.Count("chat.postMessage") != 1 {
		t.Fatal("want exactly one POST")
	}
	if res.Evidence["channel"] != "C0AGENT01" || res.Evidence["ts"] != "1760000000.000100" {
		t.Fatalf("evidence %v", res.Evidence)
	}
	obs, fs, err := r.readBack(ToolPostMessage, args, res)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Target != "the message in #agent-updates on Slack" || obs.Status != 200 || obs.Body != nil {
		t.Fatalf("observation %+v", obs)
	}
	if len(fs) != 3 {
		t.Fatalf("findings %+v", fs)
	}
	for _, f := range fs {
		if !f.Match {
			t.Fatalf("finding %+v doesn't match", f)
		}
	}
	if fs[0].Field != "text" || fs[0].Observed != "a & b <c>" || fs[2].Field != "metadata.action_id" || fs[2].Observed != actID {
		t.Fatalf("findings %+v", fs)
	}
}

func TestPostMessageInThread(t *testing.T) {
	r := newRig(t, func(c map[string]any) { c["channels"] = []any{"C0AGENT01"} },
		saastest.Exchange{Name: "post", Method: "POST", Path: "/api/chat.postMessage", BodySubset: map[string]any{"thread_ts": "1760000000.000001"},
			Status: 200, Body: json.RawMessage(`{"ok":true,"channel":"C0AGENT01","ts":"1760000000.000300"}`)},
		saastest.Exchange{Name: "history", Method: "GET", Path: "/api/conversations.history", Status: 200,
			Body: json.RawMessage(`{"ok":true,"messages":[{"ts":"1760000000.000300","text":"hi","thread_ts":"1760000000.000001","metadata":{"event_type":"other","event_payload":{"action_id":"` + actID + `"}}}]}`)})
	args := `{"channel":"C0AGENT01","text":"hi","thread_ts":"1760000000.000001"}`
	res, err := r.call(ToolPostMessage, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), `"thread_ts":"1760000000.000001"`) {
		t.Fatalf("output %s", res.Output)
	}
	_, fs, err := r.readBack(ToolPostMessage, args, res)
	if err != nil {
		t.Fatal(err)
	}
	// Another app's metadata type doesn't count as this action's id.
	if len(fs) != 4 || fs[2].Match || !fs[3].Match {
		t.Fatalf("findings %+v", fs)
	}
}

func TestReadBackMismatch(t *testing.T) {
	r := newRig(t, nil, fixtures(t, "channels", "readback_mismatch")...)
	res := connector.Result{Evidence: map[string]string{"channel": "C0AGENT01", "ts": "1760000000.000100"}}
	_, fs, err := r.readBack(ToolPostMessage, `{"channel":"#agent-updates","text":"a & b <c>"}`, res)
	if err != nil {
		t.Fatal(err)
	}
	if fs[0].Field != "text" || fs[0].Match || fs[0].Observed != "something else entirely" || !fs[2].Match {
		t.Fatalf("findings %+v", fs)
	}
}

func TestReadBackRateLimitedIsAGap(t *testing.T) {
	r := newRig(t, nil, fixtures(t, "readback_429")...)
	// The output is the fallback when Evidence is gone (a deferred witness).
	res := connector.Result{Output: json.RawMessage(`{"channel":"C0AGENT01","ts":"1760000000.000100"}`)}
	_, fs, err := r.readBack(ToolPostMessage, `{"channel":"C0AGENT01","text":"x"}`, res)
	ce := ceOf(t, err)
	if fs != nil || ce.Kind != connector.KindRateLimited || ce.Code != "upstream_rate_limited" || ce.AgentMessage != "Slack rate-limited the read-back." ||
		ce.RetryAfter != 30*time.Second {
		t.Fatalf("err %+v findings %v", ce, fs)
	}
	if r.srv.Count("readback history (429)") != 1 {
		t.Fatal("a read-back reads once per call")
	}
}

func TestReadBackMissingIsNotFound(t *testing.T) {
	r := newRig(t, nil, fixtures(t, "readback_missing")...)
	res := connector.Result{Evidence: map[string]string{"channel": "C0AGENT01", "ts": "1760000000.000100"}}
	_, _, err := r.readBack(ToolPostMessage, `{"channel":"C0AGENT01","text":"x"}`, res)
	if ce := ceOf(t, err); ce.Kind != connector.KindNotFound {
		t.Fatalf("kind %s", ce.Kind)
	}
}

func TestReadBackSkipped(t *testing.T) {
	r := newRig(t, nil)
	_, _, err := r.readBack(ToolPostMessage, `{"channel":"C0AGENT01","text":"x"}`, connector.Result{})
	var sk *connector.ErrReadBackSkipped
	if !errors.As(err, &sk) {
		t.Fatalf("err %v", err)
	}
	if _, _, err := New().NativeReadBack(context.Background(), r.inst(), connector.Call{Tool: connector.ToolSpec{Name: ToolListChannels}}, connector.Result{}); !errors.As(err, &sk) {
		t.Fatalf("err %v", err)
	}
}

func TestChannelOutsideTheListSendsNothing(t *testing.T) {
	r := newRig(t, nil, fixtures(t, "channels")...)
	for _, ch := range []string{"#random", "C0RANDOM1", "G0PRIV001"} {
		_, err := r.call(ToolPostMessage, `{"channel":"`+ch+`","text":"hi"}`)
		ce := ceOf(t, err)
		if ce.Code != "validation_failed" || !strings.Contains(ce.AgentMessage, "isn't in this connector's channels") {
			t.Fatalf("%s: %+v", ch, ce)
		}
	}
	_, err := r.call(ToolPostMessage, `{"channel":"#no-such-channel","text":"hi"}`)
	if ce := ceOf(t, err); ce.Code != "validation_failed" || !strings.Contains(ce.AgentMessage, "#no-such-channel") {
		t.Fatalf("%+v", ce)
	}
	_, err = r.call(ToolAddReaction, `{"channel":"#random","timestamp":"1760000000.000100","name":"eyes"}`)
	if ce := ceOf(t, err); ce.Code != "validation_failed" {
		t.Fatalf("%+v", ce)
	}
	_, err = r.call(ToolReadChannel, `{"channel":"#random"}`)
	if ce := ceOf(t, err); ce.Code != "validation_failed" {
		t.Fatalf("%+v", ce)
	}
	_, err = r.call(ToolPostMessage, `{"channel":"#agent-updates","text":"   "}`)
	if ce := ceOf(t, err); ce.Code != "validation_failed" {
		t.Fatalf("%+v", ce)
	}
	if n := len(r.srv.Requests()); n != r.srv.Count("conversations.list") {
		t.Fatalf("%d requests besides conversations.list", n)
	}
}

func TestRenamedChannelComparedByID(t *testing.T) {
	xs := fixtures(t, "channels_renamed")
	xs = append(xs, saastest.Exchange{Name: "chat.postMessage", Method: "POST", Path: "/api/chat.postMessage",
		BodySubset: map[string]any{"channel": "C0AGENT01"}, Status: 200, Body: json.RawMessage(`{"ok":true,"channel":"C0AGENT01","ts":"1760000000.000100"}`)})
	r := newRig(t, func(c map[string]any) { c["channels"] = []any{"C0AGENT01"} }, xs...)
	// #agent-updates was renamed to #agent-news: the id still matches.
	if _, err := r.call(ToolPostMessage, `{"channel":"#agent-news","text":"hi"}`); err != nil {
		t.Fatal(err)
	}
	// A new channel took the old name: it has another id, so it can't slip through.
	_, err := r.call(ToolPostMessage, `{"channel":"#agent-updates","text":"hi"}`)
	if ce := ceOf(t, err); ce.Code != "validation_failed" {
		t.Fatalf("%+v", ce)
	}
	if r.srv.Count("chat.postMessage") != 1 {
		t.Fatal("the impostor got a post")
	}
}

func TestStarMeansChannelsTheBotIsIn(t *testing.T) {
	xs := fixtures(t, "channels")
	xs = append(xs, saastest.Exchange{Name: "post", Method: "POST", Path: "/api/chat.postMessage", Repeat: true, Status: 200,
		Body: json.RawMessage(`{"ok":true,"channel":"G0PRIV001","ts":"1760000000.000100"}`)})
	r := newRig(t, func(c map[string]any) { c["channels"] = []any{"*"} }, xs...)
	for _, ch := range []string{"#random", "G0PRIV001", "agent-updates"} {
		if _, err := r.call(ToolPostMessage, `{"channel":"`+ch+`","text":"hi"}`); err != nil {
			t.Fatalf("%s: %v", ch, err)
		}
	}
	for _, ch := range []string{"#deedglass-approvals", "C0UNKNOWN1"} {
		_, err := r.call(ToolPostMessage, `{"channel":"`+ch+`","text":"hi"}`)
		if ce := ceOf(t, err); ce.Code != "validation_failed" {
			t.Fatalf("%s: %+v", ch, ce)
		}
	}
}

func TestChannelListPagesAndCache(t *testing.T) {
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	withClock(t, &at)
	xs := fixtures(t, "channels_paged")
	xs = append(xs, saastest.Exchange{Name: "post", Method: "POST", Path: "/api/chat.postMessage", Repeat: true, Status: 200,
		Body: json.RawMessage(`{"ok":true,"channel":"C0LATER01","ts":"1760000000.000100"}`)})
	r := newRig(t, func(c map[string]any) { c["channels"] = []any{"#later"} }, xs...)
	if _, err := r.call(ToolPostMessage, `{"channel":"#later","text":"hi"}`); err != nil {
		t.Fatal(err)
	}
	// Within 5 minutes the cache answers: no new conversations.list.
	at = at.Add(4 * time.Minute)
	if _, err := r.call(ToolPostMessage, `{"channel":"#later","text":"hi"}`); err != nil {
		t.Fatal(err)
	}
	if r.srv.Count("conversations.list page 1") != 1 || r.srv.Count("conversations.list page 2") != 1 {
		t.Fatal("the list wasn't cached")
	}
	// After 5 minutes it is fetched again.
	at = at.Add(2 * time.Minute)
	r.srv.Add(saastest.Exchange{Name: "list again", Method: "GET", Path: "/api/conversations.list", Status: 200,
		Body: json.RawMessage(`{"ok":true,"channels":[{"id":"C0LATER01","name":"later","is_member":true}]}`)})
	if _, err := r.call(ToolPostMessage, `{"channel":"#later","text":"hi"}`); err != nil {
		t.Fatal(err)
	}
	if r.srv.Count("list again") != 1 {
		t.Fatal("a stale cache was used")
	}
}

func TestCredentialsRefused(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]string
		err    error
		want   string
		kind   connector.Kind
	}{
		{"user token", map[string]string{FieldBotToken: "xoxp-1111111111-2222222222", FieldSigningSecret: signingSecret}, nil, MsgUserToken, connector.KindConfig},
		{"app token", map[string]string{FieldBotToken: "xapp-1-A0123-1111-abcdef", FieldSigningSecret: signingSecret}, nil, MsgAppToken, connector.KindConfig},
		{"not a token", map[string]string{FieldBotToken: "hello there", FieldSigningSecret: signingSecret}, nil, MsgNotBotToken, connector.KindConfig},
		{"no token", map[string]string{FieldSigningSecret: signingSecret}, nil, "no bot_token field", connector.KindConfig},
		{"missing", nil, vault.ErrNotFound, "has no credentials yet", connector.KindCredentialsMissing},
		{"vault", nil, vault.ErrIntegrity, "", connector.KindVault},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, nil)
			r.op.set(c.fields, c.err)
			_, err := r.call(ToolPostMessage, `{"channel":"#agent-updates","text":"hi"}`)
			ce := ceOf(t, err)
			if ce.Kind != c.kind || !strings.Contains(ce.AdminDetail+ce.AgentMessage, c.want) {
				t.Fatalf("%+v", ce)
			}
			if c.kind == connector.KindConfig && strings.Contains(ce.AgentMessage, "xox") {
				t.Fatalf("agent message names the token: %q", ce.AgentMessage)
			}
			if len(r.srv.Requests()) != 0 {
				t.Fatal("a request was sent")
			}
		})
	}
	if checkSigningSecret("5E9D1C3B7A2F4E6D8C0B1A3F5E7D9C2B") != MsgSigningSecret || checkSigningSecret(signingSecret) != "" {
		t.Fatal("signing secret check")
	}
}

func TestReactionVerified(t *testing.T) {
	r := newRig(t, nil, fixtures(t, "channels", "reaction")...)
	args := `{"channel":"#agent-updates","timestamp":"1760000000.000100","name":":eyes:"}`
	res, err := r.call(ToolAddReaction, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), `"already_reacted":false`) {
		t.Fatalf("output %s", res.Output)
	}
	obs, fs, err := r.readBack(ToolAddReaction, args, res)
	if err != nil {
		t.Fatal(err)
	}
	if obs.Target != "the :eyes: reaction in #agent-updates on Slack" || len(fs) != 1 || !fs[0].Match || fs[0].Field != "reaction" {
		t.Fatalf("%+v %+v", obs, fs)
	}
}

func TestAlreadyReactedIsSuccess(t *testing.T) {
	xs := fixtures(t, "channels", "reaction_already")
	xs = append(xs, saastest.Exchange{Name: "auth.test", Method: "GET", Path: "/api/auth.test", Status: 200,
		Body: json.RawMessage(`{"ok":true,"team_id":"T0123ABCD","user":"deedglass","user_id":"U0BOT0001","bot_id":"B0BOT0001"}`)})
	// auth.test comes before reactions.get in the read-back.
	xs[2], xs[3] = xs[3], xs[2]
	r := newRig(t, nil, xs...)
	args := `{"channel":"#agent-updates","timestamp":"1760000000.000100","name":"eyes"}`
	res, err := r.call(ToolAddReaction, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), `"already_reacted":true`) {
		t.Fatalf("output %s", res.Output)
	}
	// Someone else's reaction isn't the bot's.
	_, fs, err := r.readBack(ToolAddReaction, args, res)
	if err != nil || len(fs) != 1 || fs[0].Match {
		t.Fatalf("%v %+v", err, fs)
	}
}

func TestNotInChannelNamesTheStep(t *testing.T) {
	r := newRig(t, nil, fixtures(t, "channels", "not_in_channel")...)
	_, err := r.call(ToolPostMessage, `{"channel":"#agent-updates","text":"hi"}`)
	ce := ceOf(t, err)
	if ce.AgentMessage != "Invite @deedglass to #agent-updates, then try again." || ce.Kind != connector.KindInvalidRequest {
		t.Fatalf("%+v", ce)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		kind    connector.Kind
		code    string
		retry   time.Duration
		detail  string
	}{
		{"channel_not_found", 200, nil, `{"ok":false,"error":"channel_not_found"}`, connector.KindNotFound, "upstream_not_found", 0, ""},
		{"ratelimited 429", 429, map[string]string{"Retry-After": "30"}, `{"ok":false,"error":"ratelimited"}`, connector.KindRateLimited, "upstream_rate_limited", 30 * time.Second, ""},
		{"ratelimited body", 200, map[string]string{"Retry-After": "7"}, `{"ok":false,"error":"ratelimited"}`, connector.KindRateLimited, "upstream_rate_limited", 7 * time.Second, ""},
		{"invalid_auth", 200, nil, `{"ok":false,"error":"invalid_auth"}`, connector.KindAuth, "upstream_auth_failed", 0, ""},
		{"token_revoked", 200, nil, `{"ok":false,"error":"token_revoked"}`, connector.KindAuth, "upstream_auth_failed", 0, ""},
		{"account_inactive", 200, nil, `{"ok":false,"error":"account_inactive"}`, connector.KindAuth, "upstream_auth_failed", 0, ""},
		{"missing_scope", 200, nil, `{"ok":false,"error":"missing_scope","needed":"chat:write"}`, connector.KindAuth, "upstream_auth_failed", 0, "Slack needs the scope chat:write"},
		{"internal_error", 200, nil, `{"ok":false,"error":"internal_error"}`, connector.KindUpstream, "upstream_error", 0, ""},
		{"msg_too_long", 200, nil, `{"ok":false,"error":"msg_too_long"}`, connector.KindInvalidRequest, "upstream_error", 0, ""},
		{"odd code", 200, nil, `{"ok":false,"error":"<script>"}`, connector.KindInvalidRequest, "upstream_error", 0, ""},
		{"500", 500, nil, `oops`, connector.KindUpstream, "upstream_error", 0, ""},
		{"unreadable", 200, nil, `not json`, connector.KindUpstream, "upstream_error", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, func(m map[string]any) { m["channels"] = []any{"C0AGENT01"} },
				saastest.Exchange{Name: "post", Method: "POST", Path: "/api/chat.postMessage", Status: c.status, Headers: c.headers, Body: json.RawMessage(quoteIfNeeded(c.body))})
			_, err := r.call(ToolPostMessage, `{"channel":"C0AGENT01","text":"hi"}`)
			ce := ceOf(t, err)
			if ce.Kind != c.kind || ce.Code != c.code || ce.RetryAfter != c.retry || !strings.Contains(ce.AdminDetail, c.detail) {
				t.Fatalf("%+v", ce)
			}
			if strings.Contains(ce.AgentMessage, "<script>") || !strings.HasSuffix(ce.AgentMessage, ".") {
				t.Fatalf("agent message %q", ce.AgentMessage)
			}
			if r.srv.Count("post") != 1 {
				t.Fatal("a write was sent twice")
			}
		})
	}
}

func quoteIfNeeded(s string) string {
	if json.Valid([]byte(s)) {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func TestReadChannelProjection(t *testing.T) {
	xs := fixtures(t, "read_channel")
	long := strings.Repeat("é", 3000) // 6,000 bytes
	xs[0].Body = json.RawMessage(strings.Replace(string(xs[0].Body), "LONGTEXT", long, 1))
	r := newRig(t, nil, xs...)
	res, err := r.call(ToolReadChannel, `{"channel":"C0AGENT01","limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Channel  string           `json:"channel"`
		Messages []map[string]any `json:"messages"`
		HasMore  bool             `json:"has_more"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.Channel != "C0AGENT01" || len(out.Messages) != 2 || !out.HasMore {
		t.Fatalf("%s", res.Output)
	}
	m0, m1 := out.Messages[0], out.Messages[1]
	if len(m0) != 4 || m0["thread_ts"] != "1760000001.000100" || m0["user"] != "U0PERSON1" {
		t.Fatalf("projection %v", m0)
	}
	if m1["user"] != "B0OTHER01" || len(m1["text"].(string)) != 4096 {
		t.Fatalf("second message: user %v, %d bytes", m1["user"], len(m1["text"].(string)))
	}
	// Above max_read_messages the schema refuses before anything is sent.
	_, err = r.call(ToolReadChannel, `{"channel":"C0AGENT01","limit":6}`)
	if ce := ceOf(t, err); ce.Code != "validation_failed" {
		t.Fatalf("%+v", ce)
	}
}

func TestReadChannelDefaultLimit(t *testing.T) {
	r := newRig(t, nil, saastest.Exchange{Name: "history", Method: "GET", Path: "/api/conversations.history",
		Query: map[string]string{"limit": "5"}, Status: 200, Body: json.RawMessage(`{"ok":true,"messages":[]}`)})
	res, err := r.call(ToolReadChannel, `{"channel":"C0AGENT01"}`)
	if err != nil || string(res.Output) != `{"channel":"C0AGENT01","has_more":false,"messages":[]}` {
		t.Fatalf("%v %s", err, res.Output)
	}
}

func TestListChannels(t *testing.T) {
	r := newRig(t, nil, fixtures(t, "channels")...)
	res, err := r.call(ToolListChannels, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"channels":[{"id":"C0AGENT01","name":"agent-updates","is_private":false},{"id":"C0RANDOM1","name":"random","is_private":false},{"id":"G0PRIV001","name":"ops-private","is_private":true}],"count":3}`
	if string(res.Output) != want {
		t.Fatalf("%s", res.Output)
	}
}

func TestUnknownToolAndBadArgs(t *testing.T) {
	r := newRig(t, nil)
	_, err := New().Execute(context.Background(), r.inst(), connector.Call{Tool: connector.ToolSpec{Name: "delete_channel"}, Args: json.RawMessage(`{}`)})
	if ce := ceOf(t, err); ce.Code != "tool_unknown" {
		t.Fatalf("%+v", ce)
	}
	_, err = New().Execute(context.Background(), r.inst(), connector.Call{Tool: connector.ToolSpec{Name: ToolPostMessage}, Args: json.RawMessage(`{"nope":1}`)})
	if ce := ceOf(t, err); ce.Code != "validation_failed" {
		t.Fatalf("%+v", ce)
	}
}

func TestEnrich(t *testing.T) {
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	withClock(t, &at)
	r := newRig(t, nil, fixtures(t, "channels", "enrich")...)
	args := `{"channel":"#agent-updates","text":"<!here> deploy at 5 <@U0ARJUN01> <@W0OTHER1|priya> @channel"}`
	m := r.enrich(ToolPostMessage, args)
	var f policy.SlackFacts
	b, _ := json.Marshal(m["slack"])
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	want := policy.SlackFacts{ChannelID: "C0AGENT01", ChannelName: "agent-updates", IsExtShared: true, Broadcast: true, UserMentions: 2, Enriched: true}
	if f != want {
		t.Fatalf("facts %+v", f)
	}
	// Cached for 5 minutes: the second enrich makes no conversations.info call.
	at = at.Add(4 * time.Minute)
	m = r.enrich(ToolAddReaction, `{"channel":"C0AGENT01","timestamp":"1760000000.000100","name":"eyes"}`)
	if m["slack"].(map[string]any)["broadcast"] != false || m["slack"].(map[string]any)["enriched"] != true {
		t.Fatalf("%v", m)
	}
	if r.srv.Count("conversations.info") != 1 {
		t.Fatal("conversations.info wasn't cached")
	}
	if m := r.enrich(ToolListChannels, `{}`); m != nil {
		t.Fatalf("list_channels facts %v", m)
	}
}

func TestEnrichFailureNeverBlocks(t *testing.T) {
	r := newRig(t, nil)
	r.op.set(nil, vault.ErrNotFound)
	m := r.enrich(ToolPostMessage, `{"channel":"C0AGENT01","text":"<!everyone>"}`)
	f := m["slack"].(map[string]any)
	if f["enriched"] != false || f["broadcast"] != true || f["channel_id"] != "C0AGENT01" {
		t.Fatalf("%v", f)
	}
	r.op.set(goodFields(), nil)
	r.srv.Add(saastest.Exchange{Name: "info fails", Method: "GET", Path: "/api/conversations.info", Status: 200, Body: json.RawMessage(`{"ok":false,"error":"channel_not_found"}`)})
	m = r.enrich(ToolPostMessage, `{"channel":"C0GONE001","text":"hi"}`)
	if f := m["slack"].(map[string]any); f["enriched"] != false || f["channel_id"] != "C0GONE001" {
		t.Fatalf("%v", f)
	}
	r.srv.Add(saastest.Exchange{Name: "list fails", Method: "GET", Path: "/api/conversations.list", Status: 500, Body: json.RawMessage(`{}`), Repeat: true})
	m = r.enrich(ToolPostMessage, `{"channel":"#agent-updates","text":"hi"}`)
	if f := m["slack"].(map[string]any); f["enriched"] != false || f["channel_id"] != "" {
		t.Fatalf("%v", f)
	}
	m, _ = New().Enrich(context.Background(), r.inst(), connector.ToolSpec{Name: ToolPostMessage}, json.RawMessage(`{"bad":1}`))
	if m["slack"].(map[string]any)["enriched"] != false {
		t.Fatalf("%v", m)
	}
}

func TestSummarize(t *testing.T) {
	c := New()
	sum := func(tool, args string, facts map[string]any) connector.Summary {
		return c.Summarize(connector.SummaryInput{Agent: "support-bot", Qualified: "slack." + tool, Tool: connector.ToolSpec{Name: tool}, Args: json.RawMessage(args), Facts: facts})
	}
	s := sum(ToolPostMessage, `{"channel":"C0AGENT01","text":"<!channel> secret plan"}`,
		map[string]any{"slack": map[string]any{"channel_name": "agent-updates", "enriched": true}})
	if s.Plain() != "support-bot wants to post in #agent-updates." || s[2].Style != connector.SummaryStrong || s[0].Style != connector.SummaryStrong {
		t.Fatalf("%+v", s)
	}
	if strings.Contains(s.Plain(), "secret") {
		t.Fatal("the summary holds the message text")
	}
	if s := sum(ToolPostMessage, `{"channel":"agent-updates","text":"x"}`, nil); s.Plain() != "support-bot wants to post in #agent-updates." {
		t.Fatalf("%q", s.Plain())
	}
	if s := sum(ToolPostMessage, `{"channel":"C0AGENT01","text":"x"}`, nil); s.Plain() != "support-bot wants to post in C0AGENT01." || s[2].Style != connector.SummaryCode {
		t.Fatalf("%+v", s)
	}
	if s := sum(ToolAddReaction, `{"channel":"#agent-updates","timestamp":"1760000000.000100","name":"eyes"}`, nil); s.Plain() != "support-bot wants to react with :eyes: in #agent-updates." {
		t.Fatalf("%q", s.Plain())
	}
	if s := sum(ToolReadChannel, `{"channel":"#agent-updates"}`, nil); s.Plain() != "support-bot wants to read recent messages in #agent-updates." {
		t.Fatalf("%q", s.Plain())
	}
	if s := sum(ToolListChannels, `{}`, nil); s.Plain() != "support-bot wants to list the Slack channels the bot is in." {
		t.Fatalf("%q", s.Plain())
	}
	// Facts that don't look like a channel name are ignored; odd input falls back (nil).
	if s := sum(ToolPostMessage, `{"channel":"#agent-updates","text":"x"}`, map[string]any{"slack": map[string]any{"channel_name": "<!here>"}}); s.Plain() != "support-bot wants to post in #agent-updates." {
		t.Fatalf("%q", s.Plain())
	}
	for _, s := range []connector.Summary{sum(ToolPostMessage, `{"text":"x"}`, nil), sum("other", `{}`, nil), sum(ToolPostMessage, `[`, nil),
		sum(ToolReadChannel, `{}`, nil), sum(ToolAddReaction, `{"channel":"#a","name":"<b>"}`, nil), sum(ToolPostMessage, `{"channel":"<!here>"}`, nil)} {
		if s != nil {
			t.Fatalf("want the fallback, got %+v", s)
		}
	}
}

func TestProdEgressRefusesLoopback(t *testing.T) {
	// A config validated in dev can't reach a loopback API from a prod instance: egress refuses
	// it and nothing is sent.
	r := newRig(t, func(c map[string]any) { c["channels"] = []any{"C0AGENT01"} })
	inst := connector.NewInstance(r.inst().ID, org, "slack", Type, r.row.Config, "sha256:prod", connector.Env{Name: "prod", Egress: &egress.Policy{Env: egress.EnvProd}}, r.op)
	_, err := New().Execute(context.Background(), inst, connector.Call{Tool: connector.ToolSpec{Name: ToolPostMessage}, Args: json.RawMessage(`{"channel":"C0AGENT01","text":"hi"}`)})
	if ce := ceOf(t, err); ce.Kind != connector.KindDestinationBlocked {
		t.Fatalf("%+v", ce)
	}
}

func TestBadStoredConfig(t *testing.T) {
	inst := connector.NewInstance(ids.MustParse[ids.ConnectorKind](conID), org, "slack", Type, json.RawMessage(`{"team_id":`), "sha256:x", devEnv(), nil)
	if _, err := New().Tools(context.Background(), inst); err == nil {
		t.Fatal("a broken config listed tools")
	}
	if _, err := Approvals(inst); err == nil {
		t.Fatal("approvals read a broken config")
	}
	rep, err := New().Test(context.Background(), inst)
	if err != nil || rep.Status != connector.CheckFail {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestClipBytes(t *testing.T) {
	if clipBytes("abc", 5) != "abc" || clipBytes("aé", 2) != "a" || clipBytes("ééé", 5) != "éé" {
		t.Fatal("clipBytes")
	}
	if unescape("&amp;lt;") != "&lt;" || unescape("a &amp; b &lt;c&gt;") != "a & b <c>" {
		t.Fatal("unescape")
	}
}

func TestExampleConfigValidates(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "connectors", "slack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Type   string         `yaml:"type"`
		Name   string         `yaml:"name"`
		Config map[string]any `yaml:"config"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc.Config)
	out, errs := New().Validate(context.Background(), connector.Env{Name: "prod"}, raw)
	if doc.Type != Type || doc.Name != "slack" || len(errs) > 0 {
		t.Fatalf("%s %s %v", doc.Type, doc.Name, errs)
	}
	var cfg Config
	_ = json.Unmarshal(out, &cfg)
	if !cfg.Approvals.Enabled || cfg.Approvals.Channel != "#deedglass-approvals" || cfg.MaxReadMessages != 20 {
		t.Fatalf("%s", out)
	}
}

func TestUnknownNameRefreshesAtMostEvery30s(t *testing.T) {
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	withClock(t, &at)
	r := newRig(t, nil, fixtures(t, "channels")...)
	for i := 0; i < 3; i++ {
		if _, err := r.call(ToolPostMessage, `{"channel":"#nope","text":"hi"}`); ceOf(t, err).Code != "validation_failed" {
			t.Fatal(err)
		}
	}
	if n := r.srv.Count("conversations.list"); n != 1 {
		t.Fatalf("%d list calls within 30 s", n)
	}
	at = at.Add(31 * time.Second)
	_, _ = r.call(ToolPostMessage, `{"channel":"#nope","text":"hi"}`)
	if n := r.srv.Count("conversations.list"); n != 2 {
		t.Fatalf("%d list calls after 31 s", n)
	}
}
