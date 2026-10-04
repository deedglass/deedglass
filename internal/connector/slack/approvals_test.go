package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/vault"
)

var (
	cardBlocks = json.RawMessage(`[{"type":"section","text":{"type":"mrkdwn","text":"hi"}}]`)
	t0         = time.Unix(1760000000, 0)
)

func TestApprovalsSettings(t *testing.T) {
	r := newRig(t, nil)
	a, err := Approvals(r.inst())
	if err != nil {
		t.Fatal(err)
	}
	if !a.Enabled || a.Channel != "#deedglass-approvals" || strings.Join(a.RequireEmailDomains, ",") != "example.com" || a.TeamID != "T0123ABCD" || a.AppID != "A0123ABCD" {
		t.Fatalf("%+v", a)
	}
}

func TestApprovalsAPI(t *testing.T) {
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	withClock(t, &at)
	r := newRig(t, nil, fixtures(t, "channels", "approvals")...)
	inst := r.inst()
	ctx := context.Background()

	ch, ts, err := PostApproval(ctx, inst, cardBlocks, "Approval needed · github.create_pr")
	if err != nil || ch != "C0APPROV1" || ts != "1760000100.000200" {
		t.Fatalf("post: %s %s %v", ch, ts, err)
	}
	if err := UpdateApproval(ctx, inst, ch, ts, cardBlocks, "Approved by Arjun"); err != nil {
		t.Fatalf("update: %v", err)
	}
	u, err := LookupUser(ctx, inst, "U0ARJUN01")
	want := User{ID: "U0ARJUN01", TeamID: "T0123ABCD", Name: "arjun", RealName: "Arjun", Email: "arjun@example.com", EmailConfirmed: true}
	if err != nil || u != want {
		t.Fatalf("lookup: %+v %v", u, err)
	}
	// Cached for 10 minutes per (connector, user): no second users.info.
	at = at.Add(9 * time.Minute)
	if u2, err := LookupUser(ctx, inst, "U0ARJUN01"); err != nil || u2 != want {
		t.Fatalf("cached lookup: %+v %v", u2, err)
	}
	if r.srv.Count("users.info") != 1 {
		t.Fatal("users.info wasn't cached")
	}
	if err := OpenView(ctx, inst, "12345.98765.abcd2358fdea", json.RawMessage(`{"type":"modal","callback_id":"deedglass_deny"}`)); err != nil {
		t.Fatalf("views.open: %v", err)
	}
	if err := Respond(ctx, inst, r.srv.URL+"/actions/T0123ABCD/1234/abcdef", "You asked for this action, so someone else approves it."); err != nil {
		t.Fatalf("respond: %v", err)
	}
	// After 10 minutes the user is looked up again.
	at = at.Add(2 * time.Minute)
	r.srv.Add(saastest.Exchange{Name: "users.info again", Method: "GET", Path: "/api/users.info", Status: 200,
		Body: json.RawMessage(`{"ok":true,"user":{"id":"U0ARJUN01","deleted":true,"profile":{"email":"a@b.co"}}}`)})
	if u3, err := LookupUser(ctx, inst, "U0ARJUN01"); err != nil || !u3.Deleted || u3.Email != "a@b.co" {
		t.Fatalf("refreshed lookup: %+v %v", u3, err)
	}
}

func TestApprovalsAPIRefusals(t *testing.T) {
	r := newRig(t, nil)
	inst := r.inst()
	ctx := context.Background()
	for _, c := range []struct {
		name string
		err  error
	}{
		{"blocks not an array", func() error { _, _, err := PostApproval(ctx, inst, json.RawMessage(`{}`), "x"); return err }()},
		{"empty text", func() error { _, _, err := PostApproval(ctx, inst, cardBlocks, " "); return err }()},
		{"update bad channel", UpdateApproval(ctx, inst, "#x", "1760000100.000200", cardBlocks, "x")},
		{"update bad ts", UpdateApproval(ctx, inst, "C0APPROV1", "1", cardBlocks, "x")},
		{"update bad blocks", UpdateApproval(ctx, inst, "C0APPROV1", "1760000100.000200", json.RawMessage(`[`), "x")},
		{"lookup bad id", func() error { _, err := LookupUser(ctx, inst, "u1"); return err }()},
		{"view bad trigger", OpenView(ctx, inst, "a b", json.RawMessage(`{}`))},
		{"view not an object", OpenView(ctx, inst, "1.2.3", json.RawMessage(`[]`))},
		{"respond empty", Respond(ctx, inst, r.srv.URL+"/actions/x", "")},
	} {
		if !errors.Is(c.err, ErrArgument) {
			t.Errorf("%s: %v", c.name, c.err)
		}
	}
	if len(r.srv.Requests()) != 0 || r.op.opens.Load() != 0 {
		t.Fatal("a refused argument reached Slack or the vault")
	}
}

func TestApprovalsOff(t *testing.T) {
	r := newRig(t, func(c map[string]any) { c["approvals"] = map[string]any{"enabled": false} })
	if _, _, err := PostApproval(context.Background(), r.inst(), cardBlocks, "x"); !errors.Is(err, ErrApprovalsOff) {
		t.Fatalf("%v", err)
	}
}

func TestPostApprovalErrors(t *testing.T) {
	r := newRig(t, nil, append(fixtures(t, "channels"),
		saastest.Exchange{Name: "post fails", Method: "POST", Path: "/api/chat.postMessage", Status: 200, Body: json.RawMessage(`{"ok":false,"error":"not_in_channel"}`)})...)
	_, _, err := PostApproval(context.Background(), r.inst(), cardBlocks, "x")
	if ce := ceOf(t, err); ce.AgentMessage != "Invite @deedglass to #deedglass-approvals, then try again." {
		t.Fatalf("%+v", ce)
	}
	r2 := newRig(t, func(c map[string]any) {
		c["approvals"] = map[string]any{"enabled": true, "channel": "#gone"}
	}, fixtures(t, "channels")...)
	_, _, err = PostApproval(context.Background(), r2.inst(), cardBlocks, "x")
	if ce := ceOf(t, err); ce.Kind != connector.KindNotFound || !strings.Contains(ce.AgentMessage, "#gone") {
		t.Fatalf("%+v", ce)
	}
	r3 := newRig(t, nil, saastest.Exchange{Name: "users.info other", Method: "GET", Path: "/api/users.info", Status: 200,
		Body: json.RawMessage(`{"ok":true,"user":{"id":"U0SOMEONE"}}`)})
	if _, err := LookupUser(context.Background(), r3.inst(), "U0ARJUN01"); err == nil {
		t.Fatal("an answer about another user was accepted")
	}
	r3.op.set(nil, vault.ErrNotFound)
	if _, err := LookupUser(context.Background(), r3.inst(), "U0ARJUN02"); ceOf(t, err).Kind != connector.KindCredentialsMissing {
		t.Fatal(err)
	}
	if err := OpenView(context.Background(), r3.inst(), "1.2.3", json.RawMessage(`{}`)); ceOf(t, err).Kind != connector.KindCredentialsMissing {
		t.Fatal(err)
	}
	if err := UpdateApproval(context.Background(), r3.inst(), "C0APPROV1", "1760000100.000200", cardBlocks, "x"); ceOf(t, err).Kind != connector.KindCredentialsMissing {
		t.Fatal(err)
	}
	if _, _, err := PostApproval(context.Background(), r3.inst(), cardBlocks, "x"); ceOf(t, err).Kind != connector.KindCredentialsMissing {
		t.Fatal(err)
	}
}

func TestRespondRefusesOtherURLs(t *testing.T) {
	r := newRig(t, nil)
	inst := r.inst()
	bad := []string{
		"https://evil.example.com/actions/T0/1/x",
		"http://hooks.slack.com/actions/T0/1/x",
		"https://hooks.slack.com.evil.com/actions/T0/1/x",
		"https://user@hooks.slack.com/actions/T0/1/x",
		"https://hooks.slack.com/actions/T0/1/x?redirect=1",
		"https://hooks.slack.com/actions/T0/1/x#f",
		"https://hooks.slack.com:8443/actions/T0/1/x",
		"hooks.slack.com/actions",
		"%zz",
		strings.Replace(r.srv.URL, "127.0.0.1", "localhost", 1) + "/actions/x",
	}
	for _, u := range bad {
		if err := Respond(context.Background(), inst, u, "hi"); !errors.Is(err, ErrResponseURL) {
			t.Errorf("%s: %v", u, err)
		}
	}
	if len(r.srv.Requests()) != 0 {
		t.Fatal("a refused response_url got a request")
	}
	// Outside dev the api_base_url's host isn't allowed either.
	prod := connector.NewInstance(inst.ID, org, "slack", Type, inst.Config, "sha256:prod", connector.Env{Name: "prod"}, r.op)
	if err := Respond(context.Background(), prod, r.srv.URL+"/actions/x", "hi"); !errors.Is(err, ErrResponseURL) {
		t.Fatalf("%v", err)
	}
	if err := Respond(context.Background(), prod, "https://hooks.slack.com/actions/T0/1/x", strings.Repeat("a", 4001)); !errors.Is(err, ErrArgument) {
		t.Fatalf("%v", err)
	}
}

func TestRespondErrors(t *testing.T) {
	r := newRig(t, nil,
		saastest.Exchange{Name: "expired", Method: "POST", Path: "/actions/x", Status: 404, Body: json.RawMessage(`"no_text"`)},
		saastest.Exchange{Name: "refused", Method: "POST", Path: "/actions/y", Status: 200, Body: json.RawMessage(`{"ok":false,"error":"expired_url"}`)})
	if err := Respond(context.Background(), r.inst(), r.srv.URL+"/actions/x", "hi"); ceOf(t, err).Kind != connector.KindNotFound {
		t.Fatal(err)
	}
	if err := Respond(context.Background(), r.inst(), r.srv.URL+"/actions/y", "hi"); ceOf(t, err).Code != "upstream_error" {
		t.Fatal(err)
	}
}

// --- signatures ---

func signedHeader(secret, ts string, body []byte) http.Header {
	h := http.Header{}
	h.Set("X-Slack-Request-Timestamp", ts)
	h.Set("X-Slack-Signature", Sign([]byte(secret), ts, body))
	return h
}

func TestSignKnownVector(t *testing.T) {
	// Slack's documented example (api.slack.com/authentication/verifying-requests-from-slack).
	body := []byte("token=xyzz0WbapA4vBCDEFasx0q6G&team_id=T1DC2JH3J&team_domain=testteamnow&channel_id=G8PSS9T3V&channel_name=foobar&user_id=U2CERLKJA&user_name=roadrunner&command=%2Fwebhook-collect&text=&response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2FT1DC2JH3J%2F397700885554%2F96rGlfmibIGlgcZRskXaIFfN&trigger_id=398738663015.47445629121.803a0bc887a14d10d2c447fce8b6703c")
	got := Sign([]byte("8f742231b10e8888abcd99yyyzzz85a5"), "1531420618", body)
	if got != "v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503" {
		t.Fatalf("Sign = %s", got)
	}
}

func TestVerifyTable(t *testing.T) {
	body := []byte("payload=%7B%22type%22%3A%22block_actions%22%7D")
	ts := strconv.FormatInt(t0.Unix(), 10)
	good := signedHeader(signingSecret, ts, body)
	mk := func(edit func(h http.Header)) http.Header {
		h := good.Clone()
		edit(h)
		return h
	}
	cases := []struct {
		name string
		h    http.Header
		body []byte
		now  time.Time
		want error
	}{
		{"good", good, body, t0, nil},
		{"good at +300 s", good, body, t0.Add(300 * time.Second), nil},
		{"good at -300 s", good, body, t0.Add(-300 * time.Second), nil},
		{"timestamp 301 s old", good, body, t0.Add(301 * time.Second), ErrTimestampWindow},
		{"timestamp 301 s ahead", good, body, t0.Add(-301 * time.Second), ErrTimestampWindow},
		{"bad signature", mk(func(h http.Header) { h.Set("X-Slack-Signature", "v0="+strings.Repeat("0", 64)) }), body, t0, ErrSignatureMismatch},
		{"body changed", good, append([]byte("x"), body...), t0, ErrSignatureMismatch},
		{"wrong secret", signedHeader("00000000000000000000000000000000", ts, body), body, t0, ErrSignatureMismatch},
		{"non-integer timestamp", mk(func(h http.Header) { h.Set("X-Slack-Request-Timestamp", "1760000000.5") }), body, t0, ErrTimestampInvalid},
		{"signed timestamp", mk(func(h http.Header) { h.Set("X-Slack-Request-Timestamp", "+1760000000") }), body, t0, ErrTimestampInvalid},
		{"huge timestamp", mk(func(h http.Header) { h.Set("X-Slack-Request-Timestamp", "99999999999999999999") }), body, t0, ErrTimestampInvalid},
		{"non-hex signature", mk(func(h http.Header) { h.Set("X-Slack-Signature", "v0="+strings.Repeat("g", 64)) }), body, t0, ErrSignatureMalformed},
		{"upper-case hex", mk(func(h http.Header) { h.Set("X-Slack-Signature", strings.ToUpper(h.Get("X-Slack-Signature"))) }), body, t0, ErrSignatureMalformed},
		{"v1 signature", mk(func(h http.Header) { h.Set("X-Slack-Signature", "v1="+h.Get("X-Slack-Signature")[3:]) }), body, t0, ErrSignatureMalformed},
		{"short signature", mk(func(h http.Header) { h.Set("X-Slack-Signature", "v0=abc") }), body, t0, ErrSignatureMalformed},
		{"missing timestamp", mk(func(h http.Header) { h.Del("X-Slack-Request-Timestamp") }), body, t0, ErrTimestampMissing},
		{"missing signature", mk(func(h http.Header) { h.Del("X-Slack-Signature") }), body, t0, ErrSignatureMissing},
		{"missing both", http.Header{}, body, t0, ErrTimestampMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := verify([]byte(signingSecret), c.h, c.body, c.now)
			if !errors.Is(err, c.want) || (c.want == nil && err != nil) {
				t.Fatalf("err %v, want %v", err, c.want)
			}
			if err != nil {
				msg := err.Error()
				if strings.Contains(msg, signingSecret) || strings.Contains(msg, "payload") || strings.Contains(msg, c.h.Get("X-Slack-Signature")+"x") {
					t.Fatalf("the error says too much: %q", msg)
				}
			}
		})
	}
	if err := verify(nil, good, body, t0); !errors.Is(err, ErrSigningSecret) {
		t.Fatalf("no secret: %v", err)
	}
}

func TestVerifyInteraction(t *testing.T) {
	r := newRig(t, nil)
	inst := r.inst()
	body := []byte("payload=%7B%7D")
	ts := strconv.FormatInt(t0.Unix(), 10)
	if err := VerifyInteraction(context.Background(), inst, signedHeader(signingSecret, ts, body), body, t0); err != nil {
		t.Fatal(err)
	}
	if err := VerifyInteraction(context.Background(), inst, signedHeader("ffffffffffffffffffffffffffffffff", ts, body), body, t0); !errors.Is(err, ErrSignatureMismatch) {
		t.Fatalf("org B's secret: %v", err)
	}
	opens := r.op.opens.Load()
	// Malformed requests are refused before the vault is opened.
	h := signedHeader(signingSecret, ts, body)
	h.Set("X-Slack-Signature", "v0=zz")
	if err := VerifyInteraction(context.Background(), inst, h, body, t0); !errors.Is(err, ErrSignatureMalformed) || r.op.opens.Load() != opens {
		t.Fatalf("%v (opens %d → %d)", err, opens, r.op.opens.Load())
	}
	if err := VerifyInteraction(context.Background(), inst, signedHeader(signingSecret, ts, body), body, t0.Add(10*time.Minute)); !errors.Is(err, ErrTimestampWindow) || r.op.opens.Load() != opens {
		t.Fatalf("%v", err)
	}
	// A stored secret of the wrong shape, or none, never verifies.
	r.op.set(map[string]string{FieldBotToken: botToken, FieldSigningSecret: "short"}, nil)
	if err := VerifyInteraction(context.Background(), inst, signedHeader("short", ts, body), body, t0); !errors.Is(err, ErrSigningSecret) {
		t.Fatalf("%v", err)
	}
	r.op.set(nil, vault.ErrNotFound)
	err := VerifyInteraction(context.Background(), inst, signedHeader(signingSecret, ts, body), body, t0)
	if !errors.Is(err, ErrSigningSecret) || !strings.Contains(err.Error(), "credentials_missing") {
		t.Fatalf("%v", err)
	}
	if len(r.srv.Requests()) != 0 {
		t.Fatal("verifying sent a request")
	}
}

func FuzzSlackVerify(f *testing.F) {
	body := []byte("payload=%7B%22type%22%3A%22block_actions%22%7D")
	ts := strconv.FormatInt(t0.Unix(), 10)
	f.Add(ts, Sign([]byte(signingSecret), ts, body), body)
	f.Add(ts, "v0="+strings.Repeat("a", 64), body)
	f.Add("1760000000.0", "v0=", []byte{})
	f.Add("", "", []byte("x"))
	f.Add("-1", "v0="+strings.Repeat("F", 64), []byte{0xff, 0xfe})
	f.Add("17600000000000000000000", "v1=00", body)
	f.Fuzz(func(t *testing.T, ts, sig string, body []byte) {
		h := http.Header{}
		h.Set("X-Slack-Request-Timestamp", ts)
		h.Set("X-Slack-Signature", sig)
		// Without the secret nothing ever verifies.
		if err := verify(nil, h, body, t0); err == nil {
			t.Fatal("verified without a secret")
		}
		if err := verify([]byte{}, h, body, t0); err == nil {
			t.Fatal("verified with an empty secret")
		}
		err := verify([]byte(signingSecret), h, body, t0)
		if err == nil && h.Get("X-Slack-Signature") != Sign([]byte(signingSecret), h.Get("X-Slack-Request-Timestamp"), body) {
			t.Fatal("verified a signature that isn't Sign's")
		}
	})
}

// BenchmarkSlackVerify is the pure check (timestamp, shape, HMAC-SHA256, hmac.Equal) over a 4 KiB
// body; the vault open is outside it.
func BenchmarkSlackVerify(b *testing.B) {
	body := []byte(strings.Repeat("payload=x&", 410)[:4096])
	ts := strconv.FormatInt(t0.Unix(), 10)
	h := signedHeader(signingSecret, ts, body)
	secret := []byte(signingSecret)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if err := verify(secret, h, body, t0); err != nil {
			b.Fatal(err)
		}
	}
}
