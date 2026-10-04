package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/vault"
)

func checkLines(rep connector.TestReport) string {
	var b strings.Builder
	for _, c := range rep.Checks {
		fmt.Fprintf(&b, "%s %s %s", c.Status, c.ID, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(&b, " | %s", c.Fix)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestConnectorTestSample(t *testing.T) {
	// S29 §5's sample: 4 passed, 1 warning.
	r := newRig(t, nil, fixtures(t, "test_sample", "channels")...)
	rep, err := New().Test(context.Background(), r.inst())
	if err != nil {
		t.Fatal(err)
	}
	want := `pass auth workspace Arjun Sandbox (T0123ABCD) · bot @deedglass
pass app A0123ABCD matches
pass channels #agent-updates · member
warn approvals #deedglass-approvals · the bot isn't a member | Invite @deedglass to #deedglass-approvals, then run this test again.
pass scopes 9 of 9 granted
`
	if got := checkLines(rep); got != want || rep.Status != connector.CheckWarn {
		t.Fatalf("got\n%swant\n%s", got, want)
	}
}

func authTest(team, scopes string) saastest.Exchange {
	return saastest.Exchange{Name: "auth.test", Method: "GET", Path: "/api/auth.test", Status: 200, Headers: map[string]string{"X-OAuth-Scopes": scopes},
		Body: json.RawMessage(`{"ok":true,"team":"Arjun Sandbox","team_id":"` + team + `","user":"deedglass","user_id":"U0BOT0001","bot_id":"B0BOT0001"}`)}
}

func botsInfo(app string) saastest.Exchange {
	return saastest.Exchange{Name: "bots.info", Method: "GET", Path: "/api/bots.info", Status: 200,
		Body: json.RawMessage(`{"ok":true,"bot":{"app_id":"` + app + `"}}`)}
}

const allScopes = "chat:write,channels:read,groups:read,channels:history,groups:history,reactions:read,reactions:write,users:read,users:read.email"

func TestConnectorTestCases(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		xs     func(t *testing.T) []saastest.Exchange
		fields map[string]string
		status connector.CheckStatus
		has    string
	}{
		{"team mismatch", nil, func(t *testing.T) []saastest.Exchange { return []saastest.Exchange{authTest("T0OTHER01", allScopes)} },
			nil, connector.CheckFail, "fail auth workspace Arjun Sandbox (T0OTHER01) · the config says T0123ABCD | Set team_id to T0OTHER01"},
		{"invalid auth", nil, func(t *testing.T) []saastest.Exchange {
			return []saastest.Exchange{{Name: "auth.test", Method: "GET", Path: "/api/auth.test", Status: 200, Body: json.RawMessage(`{"ok":false,"error":"invalid_auth"}`)}}
		}, nil, connector.CheckFail, "fail auth auth.test · Slack refused the bot token (invalid_auth)"},
		{"rate limited", nil, func(t *testing.T) []saastest.Exchange {
			return []saastest.Exchange{{Name: "auth.test", Method: "GET", Path: "/api/auth.test", Status: 429, Headers: map[string]string{"Retry-After": "60"}, Body: json.RawMessage(`{}`)}}
		}, nil, connector.CheckFail, "Slack is limiting requests"},
		{"server error", nil, func(t *testing.T) []saastest.Exchange {
			return []saastest.Exchange{{Name: "auth.test", Method: "GET", Path: "/api/auth.test", Status: 200, Body: json.RawMessage(`{"ok":false,"error":"team_not_found"}`)}}
		}, nil, connector.CheckFail, "Slack answered team_not_found"},
		{"app mismatch", nil, func(t *testing.T) []saastest.Exchange {
			return append([]saastest.Exchange{authTest("T0123ABCD", allScopes), botsInfo("A0OTHER01")}, fixtures(t, "channels")...)
		}, nil, connector.CheckFail, "fail app the token belongs to app A0OTHER01 · the config says A0123ABCD"},
		{"bots.info fails", nil, func(t *testing.T) []saastest.Exchange {
			return append([]saastest.Exchange{authTest("T0123ABCD", allScopes),
				{Name: "bots.info", Method: "GET", Path: "/api/bots.info", Status: 200, Body: json.RawMessage(`{"ok":false,"error":"bot_not_found"}`)}}, fixtures(t, "channels")...)
		}, nil, connector.CheckFail, "fail app bots.info · Slack answered bot_not_found"},
		{"missing scopes", nil, func(t *testing.T) []saastest.Exchange {
			return append([]saastest.Exchange{authTest("T0123ABCD", "chat:write,channels:read,groups:read,channels:history,groups:history,users:read"), botsInfo("A0123ABCD")}, fixtures(t, "channels")...)
		}, nil, connector.CheckFail, "fail scopes 6 of 9 granted · missing reactions:read, reactions:write, users:read.email"},
		{"no scope header", nil, func(t *testing.T) []saastest.Exchange {
			return append([]saastest.Exchange{authTest("T0123ABCD", ""), botsInfo("A0123ABCD")}, fixtures(t, "channels")...)
		}, nil, connector.CheckWarn, "warn scopes Slack didn't report the token's scopes"},
		{"approvals off, all good", func(c map[string]any) {
			c["approvals"] = map[string]any{"enabled": false}
			c["read_channels"] = []any{"C0AGENT01", "G0PRIV001"}
		}, func(t *testing.T) []saastest.Exchange {
			return append([]saastest.Exchange{authTest("T0123ABCD", allScopes), botsInfo("A0123ABCD")}, fixtures(t, "channels")...)
		}, nil, connector.CheckPass, "pass channels #agent-updates · member, #ops-private · member\nskip approvals approvals in Slack are off"},
		{"channel problems", func(c map[string]any) {
			c["channels"] = []any{"#deedglass-approvals", "#gone"}
			c["read_channels"] = []any{"C0PRIVX01"}
			c["approvals"] = map[string]any{"enabled": true, "channel": "#gone"}
		}, func(t *testing.T) []saastest.Exchange {
			return append([]saastest.Exchange{authTest("T0123ABCD", allScopes), botsInfo("A0123ABCD"),
				{Name: "info", Method: "GET", Path: "/api/conversations.info", Status: 200, Body: json.RawMessage(`{"ok":true,"channel":{"id":"C0PRIVX01","name":"secret-room","is_member":false}}`)}},
				fixtures(t, "channels")...)
		}, nil, connector.CheckFail, "fail channels #deedglass-approvals · the bot isn't a member, #gone · not found, #secret-room · the bot isn't a member | Check the name #gone"},
		{"star", func(c map[string]any) { c["channels"] = []any{"*"}; c["read_channels"] = []any{} }, func(t *testing.T) []saastest.Exchange {
			return append([]saastest.Exchange{authTest("T0123ABCD", allScopes), botsInfo("A0123ABCD")}, fixtures(t, "channels")...)
		}, nil, connector.CheckWarn, "pass channels any channel the bot is in · 3 now"},
		{"no channels", func(c map[string]any) { c["channels"] = []any{}; c["read_channels"] = []any{} }, func(t *testing.T) []saastest.Exchange {
			return append([]saastest.Exchange{authTest("T0123ABCD", allScopes), botsInfo("A0123ABCD")}, fixtures(t, "channels")...)
		}, nil, connector.CheckWarn, "warn channels no channels in the config"},
		{"bad signing secret", nil, func(t *testing.T) []saastest.Exchange {
			return append([]saastest.Exchange{authTest("T0123ABCD", allScopes), botsInfo("A0123ABCD")}, fixtures(t, "channels")...)
		}, map[string]string{FieldBotToken: botToken, FieldSigningSecret: "nope"}, connector.CheckFail,
			"fail approvals signing secret · The signing secret must be 32 lowercase hex characters"},
		{"user token", nil, func(t *testing.T) []saastest.Exchange { return nil },
			map[string]string{FieldBotToken: "xoxp-1111111111-22222", FieldSigningSecret: signingSecret}, connector.CheckFail,
			"fail auth " + MsgUserToken + " | Store the Bot User OAuth Token (xoxb-): deedglass connector credentials set slack --field bot_token=env:SLACK_BOT_TOKEN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, c.mutate, c.xs(t)...)
			if c.fields != nil {
				r.op.set(c.fields, nil)
			}
			rep, err := New().Test(context.Background(), r.inst())
			if err != nil {
				t.Fatal(err)
			}
			got := checkLines(rep)
			if rep.Status != c.status || !strings.Contains(got, c.has) {
				t.Fatalf("status %s\n%s\nwant %q", rep.Status, got, c.has)
			}
			if len(rep.Checks) != 5 {
				t.Fatalf("%d checks", len(rep.Checks))
			}
		})
	}
}

func TestConnectorTestCredentials(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{vault.ErrNotFound, "fail auth no credentials yet | Store them: deedglass connector credentials set slack --field bot_token=env:SLACK_BOT_TOKEN --field signing_secret=env:SLACK_SIGNING_SECRET."},
		{vault.ErrIntegrity, "fail auth The credential failed to authenticate"},
	} {
		r := newRig(t, nil)
		r.op.set(nil, c.err)
		rep, _ := New().Test(context.Background(), r.inst())
		if got := checkLines(rep); !strings.Contains(got, c.want) || !strings.Contains(got, "skip scopes no usable credentials") {
			t.Fatalf("%s", got)
		}
	}
}

// TestSecretsNeverLeak is the sentinel sweep: the bot token and the signing secret never appear
// in an output, an error, a finding, a test report, a request body or a log line, even when Slack
// echoes the token back.
func TestSecretsNeverLeak(t *testing.T) {
	echo := json.RawMessage(`{"ok":true,"messages":[{"ts":"1760000002.000100","user":"U1","text":"the token is ` + botToken + ` and ` + signingSecret + `"}]}`)
	xs := append(fixtures(t, "channels", "post_message", "test_sample"),
		saastest.Exchange{Name: "echo", Method: "GET", Path: "/api/conversations.history", Query: map[string]string{"limit": "5"}, Status: 200, Body: echo},
		saastest.Exchange{Name: "bad", Method: "POST", Path: "/api/chat.postMessage", Status: 200, Body: json.RawMessage(`{"ok":false,"error":"invalid_auth","detail":"` + botToken + `"}`)},
	)
	xs = append(xs, fixtures(t, "enrich")...)
	r := newRig(t, nil, xs...)
	var seen []string
	note := func(vs ...any) {
		for _, v := range vs {
			b, _ := json.Marshal(v)
			seen = append(seen, fmt.Sprintf("%v %s", v, b))
		}
	}
	noteErr := func(err error) {
		if err == nil {
			return
		}
		seen = append(seen, err.Error())
		if ce := connector.AsError(err); ce != nil {
			seen = append(seen, ce.AgentMessage, ce.AdminDetail)
		}
	}
	args := `{"channel":"#agent-updates","text":"a & b <c>"}`
	res, err := r.call(ToolPostMessage, args)
	noteErr(err)
	note(res.Output, res.Meta, res.Evidence)
	obs, fs, err := r.readBack(ToolPostMessage, args, res)
	noteErr(err)
	note(obs, fs)
	rep, err := New().Test(context.Background(), r.inst())
	noteErr(err)
	note(rep)
	res, err = r.call(ToolReadChannel, `{"channel":"C0AGENT01"}`)
	noteErr(err)
	note(res.Output)
	if strings.Contains(string(res.Output), "the token is") && !strings.Contains(string(res.Output), "[redacted]") {
		t.Fatalf("an echoed token wasn't redacted: %s", res.Output)
	}
	_, err = r.call(ToolPostMessage, args)
	noteErr(err)
	note(r.enrich(ToolPostMessage, args))
	ts := strconv.FormatInt(t0.Unix(), 10)
	noteErr(VerifyInteraction(context.Background(), r.inst(), signedHeader("ffffffffffffffffffffffffffffffff", ts, []byte("x")), []byte("x"), t0))
	for _, fields := range []map[string]string{
		{FieldBotToken: "xoxp-" + signingSecret, FieldSigningSecret: signingSecret},
		{FieldBotToken: "xapp-" + signingSecret, FieldSigningSecret: signingSecret},
	} {
		r.op.set(fields, nil)
		_, err := r.call(ToolPostMessage, args)
		noteErr(err)
		rep, _ := New().Test(context.Background(), r.inst())
		note(rep)
	}
	seen = append(seen, r.logs.String())
	for _, req := range r.srv.Requests() {
		seen = append(seen, string(req.Body), req.Path, req.Query.Encode())
	}
	for _, s := range seen {
		for _, secret := range []string{botToken, signingSecret, "dgSENTINEL"} {
			if strings.Contains(s, secret) {
				t.Fatalf("a secret leaked: %q", s)
			}
		}
	}
	if len(seen) < 20 {
		t.Fatalf("the sweep saw only %d values", len(seen))
	}
}

// TestLiveSlack runs against the founder's workspace only with DEEDGLASS_IT_LIVE naming slack
// and the credentials in the environment (never in make ci).
func TestLiveSlack(t *testing.T) {
	if !strings.Contains(os.Getenv("DEEDGLASS_IT_LIVE"), "slack") {
		t.Skip("set DEEDGLASS_IT_LIVE=slack and SLACK_BOT_TOKEN, SLACK_SIGNING_SECRET, SLACK_TEAM_ID, SLACK_APP_ID to run against Slack")
	}
	cfg, _ := json.Marshal(map[string]any{"team_id": os.Getenv("SLACK_TEAM_ID"), "app_id": os.Getenv("SLACK_APP_ID"),
		"channels": []string{"#agent-updates"}, "read_channels": []string{"#agent-updates"},
		"approvals": map[string]any{"enabled": true, "channel": "#deedglass-approvals"}})
	env := connector.Env{Name: egress.EnvProd, Egress: &egress.Policy{Env: egress.EnvProd}}
	norm, errs := New().Validate(context.Background(), env, cfg)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	op := &opener{fields: map[string]string{FieldBotToken: os.Getenv("SLACK_BOT_TOKEN"), FieldSigningSecret: os.Getenv("SLACK_SIGNING_SECRET")}}
	inst := connector.NewInstance(ids.MustParse[ids.ConnectorKind](conID), org, "slack", Type, norm, "sha256:live", env, op)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rep, err := New().Test(ctx, inst)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s · %s\n%s", rep.Status, rep.Duration, checkLines(rep))
	if rep.Status == connector.CheckFail {
		t.Fatal("the live test failed")
	}
}
