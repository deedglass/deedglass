//go:build outboundmail

package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/outbound/fakegmail"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
	"github.com/deedglass/deedglass/internal/vault"
)

var update = flag.Bool("update", false, "rewrite the MIME and normalisation goldens")

const (
	actID   = "act_01j9zq7c4m8x2v6t0r3n5k1h7d"
	conID   = "con_01j9zq7c4m8x2v6t0r3n5k1h7d"
	mailbox = "arjun@trydeedglass.example"
	domain  = "trydeedglass.example"
	token   = "k3x7abcdefghijklmnopqrstuvwxyz23"
)

var (
	org      = ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvp")
	otherOrg = ids.MustParse[ids.OrgKind]("org_01k6c9kb2zcjsyn7b8g2k1vqvq")
	devEnv   = connector.Env{Name: egress.EnvDev, Egress: &egress.Policy{Env: egress.EnvDev, AllowPrivate: true}}
	t0       = time.Date(2026, 10, 21, 15, 37, 12, 0, time.UTC)
)

type memLoader struct{ row store.Connector }

func (m memLoader) Head(_ context.Context, _ ids.OrgID, name string) (store.ConnectorHead, error) {
	if name != m.row.Name {
		return store.ConnectorHead{}, store.ErrNotFound
	}
	return store.ConnectorHead{ID: m.row.ID, Type: m.row.Type, Status: m.row.Status, ConfigHash: m.row.ConfigHash}, nil
}

func (m memLoader) Load(_ context.Context, _ ids.OrgID, id string) (store.Connector, error) {
	if id != m.row.ID {
		return store.Connector{}, store.ErrNotFound
	}
	return m.row, nil
}

type opener struct{ fields map[string]string }

func (o *opener) Open(context.Context, vault.Ref) (*vault.Secret, error) {
	return vault.NewSecretForTest(vault.KindFields, o.fields), nil
}

type breaker struct {
	mu    sync.Mutex
	trips []string
}

func (b *breaker) Trip(_ context.Context, _ *connector.Instance, reason, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trips = append(b.trips, reason)
	return nil
}

type rig struct {
	t    *testing.T
	fake *fakegmail.Server
	srv  *httptest.Server
	reg  *connector.Registry
	row  store.Connector
	br   *breaker
	logs *bytes.Buffer
}

var hashN int

func baseConfig() map[string]any {
	return map[string]any{"mailbox": mailbox, "from_name": "Arjun Prabhakaran", "sending_domain": domain,
		"deny_from_domains": []any{"deedglass.com"}, "unsubscribe_base": "https://" + domain + "/u/"}
}

func newRig(t *testing.T, mutate func(map[string]any)) *rig {
	t.Helper()
	fake := fakegmail.New(mailbox)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cfg := baseConfig()
	if mutate != nil {
		mutate(cfg)
	}
	raw, _ := json.Marshal(cfg)
	c := New(Options{APIBase: srv.URL})
	norm, errs := c.Validate(context.Background(), devEnv, raw)
	if len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	hashN++
	r := &rig{t: t, fake: fake, srv: srv, br: &breaker{}, logs: &bytes.Buffer{}}
	r.row = store.Connector{ID: conID, OrgID: org.String(), Type: Type, Name: "mail", Config: norm,
		ConfigHash: "sha256:test" + strings.Repeat("x", hashN), Status: "active"}
	env := devEnv
	env.Breaker = r.br
	r.reg = connector.New(connector.Options{Types: []connector.Connector{c}, Loader: memLoader{r.row},
		Opener: &opener{fields: map[string]string{FieldClientID: "123.apps", FieldClientSecret: "GOCSPX-dgSENTINEL", FieldRefreshToken: "1//dgSENTINEL"}},
		Env:    env, Logger: slog.New(slog.NewTextHandler(r.logs, nil)), OrgTypes: map[string][]string{Type: {org.String()}}})
	oldNow, oldLog := now, logger
	now = func() time.Time { return t0 }
	logger = func() *slog.Logger { return slog.New(slog.NewTextHandler(r.logs, nil)) }
	t.Cleanup(func() { now, logger = oldNow, oldLog })
	return r
}

func sendArgs(mut func(map[string]any)) string {
	body := "Hi Maya,\n\nYour jobs post says the support agent updates order records in Postgres.\n\nOne question: would anyone see it?\n\nArjun Prabhakaran\n"
	m := map[string]any{"to": "maya@acme-robotics.example", "subject": "your agent and Postgres", "body_text": body,
		"body_sha256": BodySHA256(body), "step": 1, "kind": "cold", "thread": nil, "unsubscribe": map[string]any{"token": token},
		"window": map[string]any{"not_before": "2026-10-21T15:30:00Z", "not_after": "2026-10-21T17:30:00Z", "tz": "America/Los_Angeles",
			"local_start_hour": 8, "local_end_hour": 10, "local_weekday": "wed", "ist_date": "2026-10-21"},
		"message_ref": "msg_01jcp", "template_hash": "sha256:41ad", "arm": "A",
		"recipient": map[string]any{"country": "US"}, "checks": map[string]any{"lint": "pass"}, "review": map[string]any{"company": "Acme Robotics"}}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (r *rig) call(o ids.OrgID, tool, args string) (connector.Result, connector.Call, error) {
	r.t.Helper()
	ex, spec, _, err := r.reg.Resolve(context.Background(), o, "mail."+tool)
	if err != nil {
		return connector.Result{}, connector.Call{}, err
	}
	c := connector.Call{ActionID: ids.MustParse[ids.ActionKind](actID), OrgID: o, Tool: spec, Qualified: "mail." + tool, Args: json.RawMessage(args)}
	if ac, ok := ex.(interface {
		CheckArgs(connector.ToolSpec, json.RawMessage) error
	}); ok {
		if err := ac.CheckArgs(spec, c.Args); err != nil {
			return connector.Result{}, c, err
		}
	}
	res, err := ex.Execute(context.Background(), c)
	return res, c, err
}

func (r *rig) inst() *connector.Instance {
	r.t.Helper()
	inst, err := r.reg.Instance(org, r.row)
	if err != nil {
		r.t.Fatal(err)
	}
	return inst
}

func (r *rig) readBack(c connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	ex, _, _, err := r.reg.Resolve(context.Background(), org, c.Qualified)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	return ex.(connector.NativeReadBacking).NativeReadBack(context.Background(), c, res)
}

func allMatch(t *testing.T, fs []connector.L1Finding) {
	t.Helper()
	for _, f := range fs {
		if !f.Match {
			t.Errorf("finding %s: expected %v, observed %v", f.Field, f.Expected, f.Observed)
		}
	}
	if len(fs) < 7 {
		t.Errorf("only %d findings", len(fs))
	}
}

func codeOf(err error) string {
	var ce *connector.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func TestValidate(t *testing.T) {
	c := New(Options{})
	cases := []struct {
		name  string
		mut   func(map[string]any)
		param string
		code  string
	}{
		{"deedglass mailbox", func(m map[string]any) {
			m["mailbox"] = "arjun@deedglass.com"
			m["sending_domain"] = "deedglass.com"
			m["unsubscribe_base"] = "https://deedglass.com/u/"
		}, "config.mailbox", CodeFromDenied},
		{"deedglass subdomain", func(m map[string]any) {
			m["mailbox"] = "x@mail.deedglass.com"
			m["sending_domain"] = "mail.deedglass.com"
			m["unsubscribe_base"] = "https://mail.deedglass.com/u/"
		}, "config.mailbox", CodeFromDenied},
		{"configured deny", func(m map[string]any) { m["deny_from_domains"] = []any{"trydeedglass.example"} }, "config.mailbox", CodeFromDenied},
		{"empty deny list", func(m map[string]any) { m["deny_from_domains"] = []any{} }, "config.deny_from_domains", ""},
		{"domain mismatch", func(m map[string]any) { m["sending_domain"] = "other.example" }, "config.sending_domain", ""},
		{"unsub off domain", func(m map[string]any) { m["unsubscribe_base"] = "https://evil.example/u/" }, "config.unsubscribe_base", ""},
		{"unsub http", func(m map[string]any) { m["unsubscribe_base"] = "http://" + domain + "/u/" }, "config.unsubscribe_base", ""},
		{"display name mailbox", func(m map[string]any) { m["mailbox"] = "Arjun <" + mailbox + ">" }, "config.mailbox", ""},
		{"unknown key", func(m map[string]any) { m["send_anywhere"] = true }, "config.send_anywhere", ""},
		{"email not mailbox", func(m map[string]any) { m["email"] = "other@" + domain }, "config.email", ""},
		{"from name quote", func(m map[string]any) { m["from_name"] = `A "B"` }, "config.from_name", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseConfig()
			tc.mut(m)
			raw, _ := json.Marshal(m)
			norm, errs := c.Validate(context.Background(), devEnv, raw)
			if norm != nil || len(errs) == 0 {
				t.Fatalf("accepted: %s", norm)
			}
			if errs[0].Param != tc.param || errs[0].Code != tc.code {
				t.Fatalf("got %+v, want param %s code %q", errs[0], tc.param, tc.code)
			}
			if tc.code == CodeFromDenied && errs[0].Message != MsgFromDenied {
				t.Fatalf("message %q", errs[0].Message)
			}
		})
	}
	raw, _ := json.Marshal(baseConfig())
	norm, errs := c.Validate(context.Background(), devEnv, raw)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var got Config
	_ = json.Unmarshal(norm, &got)
	if !got.RequireWindow {
		t.Fatal("require_window should default to true")
	}
}

func TestDeniedDomain(t *testing.T) {
	deny := []string{"deedglass.com"}
	for addr, want := range map[string]bool{"a@deedglass.com": true, "a@DEEDGLASS.COM": true, "a@x.deedglass.com": true,
		"a@deedglass.com.": true, "a@notdeedglass.com": false, "a@trydeedglass.com": false, "not-an-address": true} {
		if got := DeniedDomain(addr, deny); got != want {
			t.Errorf("%s: %t", addr, got)
		}
	}
}

func TestAllowed(t *testing.T) {
	ok := []struct{ m, p string }{
		{"POST", "/gmail/v1/users/me/messages/send"}, {"GET", "/gmail/v1/users/me/messages/abc123"},
		{"GET", "/gmail/v1/users/me/messages"}, {"GET", "/gmail/v1/users/me/history"},
		{"GET", "/gmail/v1/users/me/threads/t1"}, {"GET", "/gmail/v1/users/me/profile"},
	}
	for _, x := range ok {
		if !Allowed(x.m, x.p) {
			t.Errorf("refused %s %s", x.m, x.p)
		}
	}
	bad := []struct{ m, p string }{
		{"POST", "/gmail/v1/users/me/drafts"}, {"POST", "/gmail/v1/users/me/drafts/send"}, {"POST", "/gmail/v1/users/me/messages/abc/modify"},
		{"POST", "/gmail/v1/users/me/messages/abc/trash"}, {"POST", "/gmail/v1/users/me/messages/abc/untrash"},
		{"DELETE", "/gmail/v1/users/me/messages/abc"}, {"GET", "/gmail/v1/users/me/settings/sendAs"},
		{"POST", "/gmail/v1/users/me/messages/import"}, {"POST", "/gmail/v1/users/me/messages"},
		{"POST", "/gmail/v1/users/me/messages/batchDelete"}, {"POST", "/gmail/v1/users/me/messages/batchModify"},
		{"POST", "/gmail/v1/users/me/watch"}, {"POST", "/gmail/v1/users/other/messages/send"}, {"GET", "/gmail/v1/users/me/messages/send"},
		{"POST", "/gmail/v1/users/me/messages/send/x"},
	}
	for _, x := range bad {
		if Allowed(x.m, x.p) {
			t.Errorf("allowed %s %s", x.m, x.p)
		}
	}
}

func TestForbiddenEndpointsMakeNoRequest(t *testing.T) {
	r := newRig(t, nil)
	ex, _, _, err := r.reg.Resolve(context.Background(), org, "mail.get_message")
	if err != nil {
		t.Fatal(err)
	}
	_ = ex
	s, err := New(Options{APIBase: r.srv.URL}).open(context.Background(), r.inst())
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for _, x := range []struct {
		m    string
		rest []string
	}{{"POST", []string{"drafts"}}, {"POST", []string{"messages", "x", "modify"}}, {"POST", []string{"messages", "x", "trash"}},
		{"DELETE", []string{"messages", "x"}}, {"GET", []string{"settings", "sendAs"}}, {"POST", []string{"messages", "import"}},
		{"POST", []string{"messages", "batchDelete"}}, {"POST", []string{"watch"}}, {"POST", []string{"messages", "x", "untrash"}}} {
		if _, err := s.call(context.Background(), x.m, nil, nil, nil, x.rest...); err == nil {
			t.Errorf("%s %v was requested", x.m, x.rest)
		}
	}
	if n := len(r.fake.Log()); n != 0 {
		t.Fatalf("%d requests reached the fake", n)
	}
	if !strings.Contains(r.logs.String(), "outbound_endpoint_refused") {
		t.Fatal("no alert logged")
	}
}

func TestNormaliseBody(t *testing.T) {
	a := "Hi\r\nthere  \t\r\n\r\n\r\n"
	b := "Hi\nthere\n"
	if NormaliseBody(a) != NormaliseBody(b) || BodySHA256(a) != BodySHA256(b) {
		t.Fatal("CRLF and trailing spaces should normalise away")
	}
	if BodySHA256("Hi\nthere\n") == BodySHA256("Hi\nthera\n") {
		t.Fatal("one changed byte must change the hash")
	}
	// NFC: e + combining acute equals é.
	if BodySHA256("café\n") != BodySHA256("café\n") {
		t.Fatal("NFC")
	}
	golden := filepath.Join("testdata", "normalise.json")
	cases := []string{"Hi Maya,\r\n\r\nline  \n", "a\rb\r\n", "no final newline", "trailing\n\n\n", "tabs\t\t\nx", "café"}
	type row struct {
		In, Normalised, SHA256 string
	}
	var rows []row
	for _, c := range cases {
		rows = append(rows, row{c, NormaliseBody(c), BodySHA256(c)})
	}
	got, _ := json.MarshalIndent(rows, "", "  ")
	if *update {
		_ = os.MkdirAll("testdata", 0o755)
		_ = os.WriteFile(golden, append(got, '\n'), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got)+"\n" {
		t.Fatalf("normalise golden differs (run with -update and review):\n%s", got)
	}
}

func TestMessageID(t *testing.T) {
	a := MessageID(actID, conID, domain)
	if a != MessageID(actID, conID, domain) || !strings.HasSuffix(a, "@"+domain+">") || len(a) != 1+26+1+len(domain)+1 {
		t.Fatalf("%q", a)
	}
	if a == MessageID("act_01j9zq7c4m8x2v6t0r3n5k1h7e", conID, domain) {
		t.Fatal("another action, same id")
	}
}

func TestMIMEGoldens(t *testing.T) {
	long := strings.Repeat("A long line that quoted-printable must fold at seventy-six characters. ", 3) + "end"
	cases := map[string]Message{
		"step1": {FromName: "Arjun Prabhakaran", From: mailbox, To: "maya@acme-robotics.example", Subject: "your agent and Postgres",
			BodyText: "Hi Maya,\n\nOne question?\n\nArjun Prabhakaran\n"},
		"threaded-step2": {FromName: "Arjun Prabhakaran", From: mailbox, To: "maya@acme-robotics.example", Subject: "Re: your agent and Postgres",
			BodyText: "Hi Maya, one more thing on my note below.\n", InReplyTo: "<abc@" + domain + ">", References: []string{"<abc@" + domain + ">"}},
		"non-ascii-subject": {FromName: "Arjun Prabhakaran", From: mailbox, To: "maya@acme-robotics.example", Subject: "café and Postgres",
			BodyText: "Hi, naïve question.\n"},
		"long-line": {FromName: "Arjun Prabhakaran", From: mailbox, To: "maya@acme-robotics.example", Subject: "folding", BodyText: long + "\n"},
	}
	for name, m := range cases {
		m.Date = t0
		m.MessageID = MessageID(actID, conID, domain)
		m.UnsubURL = "https://" + domain + "/u/" + token
		m.UnsubMailto = "mailto:u+" + token + "@" + domain + "?subject=unsubscribe"
		got, err := Build(m)
		if err != nil {
			t.Fatal(name, err)
		}
		p := filepath.Join("testdata", "mime", name+".eml")
		if *update {
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, got, 0o644)
		}
		want, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s differs (run with -update and review):\n%s", name, got)
		}
		for _, bad := range []string{"X-Mailer", "text/html", "Content-Disposition", "<img"} {
			if bytes.Contains(got, []byte(bad)) {
				t.Errorf("%s holds %s", name, bad)
			}
		}
		v, err := ParseRaw(got)
		if err != nil {
			t.Fatal(err)
		}
		if v.Subject != m.Subject || BodySHA256(v.BodyText) != BodySHA256(m.BodyText) || v.ListUnsubPost != "List-Unsubscribe=One-Click" {
			t.Errorf("%s round trip: %+v", name, v)
		}
	}
	if _, err := Build(Message{From: mailbox, To: "a@b.example", Subject: "x\r\nBcc: c@d.example"}); !errors.Is(err, ErrInjection) {
		t.Fatal("header injection accepted")
	}
}

func TestSendVerified(t *testing.T) {
	r := newRig(t, nil)
	res, c, err := r.call(org, ToolSend, sendArgs(nil))
	if err != nil {
		t.Fatal(err)
	}
	if r.fake.Sends() != 1 || r.fake.Writes() != 1 {
		t.Fatalf("sends %d writes %d", r.fake.Sends(), r.fake.Writes())
	}
	if res.Evidence["gmail_id"] == "" || res.Evidence["message_id"] != MessageID(actID, conID, domain) {
		t.Fatalf("evidence %v", res.Evidence)
	}
	obs, fs, err := r.readBack(c, res)
	if err != nil {
		t.Fatal(err)
	}
	allMatch(t, fs)
	if !strings.Contains(obs.Target, "Sent") {
		t.Fatalf("target %q", obs.Target)
	}
	raw := r.fake.Messages()[0].Raw
	for _, h := range []string{"From: \"Arjun Prabhakaran\" <" + mailbox + ">", "List-Unsubscribe: <https://" + domain + "/u/" + token + ">, <mailto:u+" + token + "@" + domain + "?subject=unsubscribe>",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click", "Message-ID: " + MessageID(actID, conID, domain)} {
		if !bytes.Contains(raw, []byte(h)) {
			t.Errorf("sent message lacks %q", h)
		}
	}
	if strings.Contains(r.logs.String(), "dgSENTINEL") {
		t.Fatal("a credential reached the logs")
	}
}

func TestSendRefusalsSendNothing(t *testing.T) {
	cases := []struct {
		name string
		mut  func(map[string]any)
		code string
	}{
		{"changed byte", func(m map[string]any) { m["body_text"] = strings.Replace(m["body_text"].(string), "Maya", "Maye", 1) }, connector.CodeValidationFailed},
		{"two recipients", func(m map[string]any) { m["to"] = `"a, b" <c@d.example>` }, connector.CodeValidationFailed},
		{"comma list", func(m map[string]any) { m["to"] = "a@b.example, c@d.example" }, connector.CodeValidationFailed},
		{"re on step 1", func(m map[string]any) { m["subject"] = "Re: x" }, connector.CodeValidationFailed},
		{"html", func(m map[string]any) { b := "<html>hi</html>\n"; m["body_text"] = b; m["body_sha256"] = BodySHA256(b) }, connector.CodeValidationFailed},
		{"bad token", func(m map[string]any) { m["unsubscribe"] = map[string]any{"token": "UPPER"} }, connector.CodeValidationFailed},
		{"window past", func(m map[string]any) {
			m["window"] = map[string]any{"not_before": "2026-10-20T15:30:00Z", "not_after": "2026-10-20T17:30:00Z", "tz": "UTC"}
		}, CodeOutsideSendWindow},
		{"window future", func(m map[string]any) {
			m["window"] = map[string]any{"not_before": "2026-10-22T15:30:00Z", "not_after": "2026-10-22T17:30:00Z"}
		}, CodeOutsideSendWindow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, nil)
			_, _, err := r.call(org, ToolSend, sendArgs(tc.mut))
			if codeOf(err) != tc.code {
				t.Fatalf("err %v, want %s", err, tc.code)
			}
			if n := len(r.fake.Log()); n != 0 {
				t.Fatalf("%d requests", n)
			}
		})
	}
	// The CRLF form of the same body hashes the same and sends.
	r := newRig(t, nil)
	if _, _, err := r.call(org, ToolSend, sendArgs(func(m map[string]any) {
		m["body_text"] = strings.ReplaceAll(m["body_text"].(string), "\n", "  \r\n")
	})); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitedFails(t *testing.T) {
	r := newRig(t, nil)
	r.fake.SetMode(fakegmail.ModeRateLimited)
	_, _, err := r.call(org, ToolSend, sendArgs(nil))
	var ce *connector.Error
	if !errors.As(err, &ce) || ce.Kind != connector.KindRateLimited || ce.Code != connector.CodeUpstreamRateLimited {
		t.Fatalf("%v", err)
	}
	if r.fake.Sends() != 1 {
		t.Fatalf("sends %d", r.fake.Sends())
	}
}

func TestUnknownIsReadBackByMessageID(t *testing.T) {
	r := newRig(t, nil)
	r.fake.SetMode(fakegmail.ModeFailAfter)
	res, c, err := r.call(org, ToolSend, sendArgs(nil))
	var ce *connector.Error
	if !errors.As(err, &ce) || ce.Kind != connector.KindUpstream {
		t.Fatalf("want unknown (upstream), got %v", err)
	}
	if r.fake.Sends() != 1 {
		t.Fatalf("never retried: sends %d", r.fake.Sends())
	}
	_, fs, err := r.readBack(c, res)
	if err != nil {
		t.Fatal(err)
	}
	allMatch(t, fs)
	var sawQuery bool
	for _, q := range r.fake.Log() {
		if q.Path == "/gmail/v1/users/me/messages" {
			sawQuery = true
		}
	}
	if !sawQuery {
		t.Fatal("the read-back didn't search by rfc822msgid")
	}
}

func TestNotFoundAfterUnknown(t *testing.T) {
	r := newRig(t, nil)
	r.fake.SetMode(fakegmail.ModeDropSent)
	res, c, _ := r.call(org, ToolSend, sendArgs(nil))
	_, _, err := r.readBack(c, res)
	var ce *connector.Error
	if !errors.As(err, &ce) || ce.Kind != connector.KindNotFound {
		t.Fatalf("want not found (UNVERIFIABLE), got %v", err)
	}
}

func TestTamperedSentCopyMismatches(t *testing.T) {
	r := newRig(t, nil)
	r.fake.SetMode(fakegmail.ModeTamperBody)
	res, c, err := r.call(org, ToolSend, sendArgs(nil))
	if err != nil {
		t.Fatal(err)
	}
	_, fs, err := r.readBack(c, res)
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	for _, f := range fs {
		if !f.Match {
			bad = append(bad, f.Field)
		}
	}
	if len(bad) != 1 || bad[0] != "body_text" {
		t.Fatalf("mismatched fields %v", bad)
	}
}

func TestDeniedFromInSentCopyTrips(t *testing.T) {
	r := newRig(t, nil)
	r.fake.SetMode(fakegmail.ModeFromDenied)
	res, c, err := r.call(org, ToolSend, sendArgs(nil))
	if err != nil {
		t.Fatal(err)
	}
	_, fs, err := r.readBack(c, res)
	if err != nil {
		t.Fatal(err)
	}
	from := false
	for _, f := range fs {
		if f.Field == "from" && !f.Match {
			from = true
		}
	}
	if !from {
		t.Fatal("the From finding should mismatch")
	}
	if len(r.br.trips) != 1 || r.br.trips[0] != "from_denied" {
		t.Fatalf("trips %v", r.br.trips)
	}
	r.fake.SetMode(fakegmail.ModeOK)
	n := len(r.fake.Log())
	if _, _, err := r.call(org, ToolSend, sendArgs(nil)); codeOf(err) != connector.CodeConnectorUnavailable {
		t.Fatalf("next call: %v", err)
	}
	if len(r.fake.Log()) != n {
		t.Fatal("a disabled connector made a request")
	}
}

func TestOrgGate(t *testing.T) {
	r := newRig(t, nil)
	if _, ok := r.reg.TypeFor(otherOrg.String(), Type); ok {
		t.Fatal("another org sees the type")
	}
	if _, ok := r.reg.TypeFor(org.String(), Type); !ok {
		t.Fatal("the listed org doesn't see the type")
	}
	if !r.reg.Gated(Type) {
		t.Fatal("not gated")
	}
	for _, c := range r.reg.TypesFor(otherOrg.String()) {
		if c.Type() == Type {
			t.Fatal("listed for another org")
		}
	}
	if _, _, err := r.call(otherOrg, ToolSend, sendArgs(nil)); err == nil {
		t.Fatal("another org resolved mail.send")
	}
	if len(r.fake.Log()) != 0 {
		t.Fatal("requests made")
	}
}

func TestReadTools(t *testing.T) {
	r := newRig(t, nil)
	res, _, err := r.call(org, ToolSend, sendArgs(nil))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := r.call(org, ToolGetMessage, `{"id":"`+res.Evidence["gmail_id"]+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(got.Output, &m)
	if m["from"] != mailbox || m["subject"] != "your agent and Postgres" {
		t.Fatalf("%v", m)
	}
	if _, _, err := r.call(org, ToolListMessages, `{"q":"in:inbox newer_than:3d"}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.call(org, ToolListMessages, `{"q":"from:anyone"}`); codeOf(err) != connector.CodeValidationFailed {
		t.Fatalf("query grammar: %v", err)
	}
	if _, _, err := r.call(org, ToolListHistory, `{"start_history_id":"1"}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.call(org, ToolGetThread, `{"id":"thr000001"}`); err != nil {
		t.Fatal(err)
	}
	if r.fake.Writes() != 1 {
		t.Fatalf("reads wrote: %d", r.fake.Writes())
	}
}

func TestEnrichFacts(t *testing.T) {
	cfg := Config{Mailbox: mailbox, FromName: "Arjun Prabhakaran", UnsubscribeBase: "https://" + domain + "/u/"}
	f := FactsOf(cfg, json.RawMessage(sendArgs(nil)))
	if f.FromDomain != domain || f.ToDomain != "acme-robotics.example" || f.Links != 0 || f.BareDomains != 0 || f.Questions != 1 || !f.Enriched || !f.UnsubscribeHTTPS || f.Step != 1 {
		t.Fatalf("%+v", f)
	}
	if f.Words != 20 {
		t.Fatalf("words %d", f.Words)
	}
	for body, want := range map[string][2]int{
		"see deedglass.com today\n":                   {0, 1},
		"see https://deedglass.com/play now\n":        {1, 0},
		"mail me at a@deedglass.com\n":                {0, 0},
		"www.example.org and acme.io\n":               {0, 2},
		"run npx deedglass scan, e.g. in a minute.\n": {0, 0},
	} {
		if CountLinks(body) != want[0] || CountBareDomains(body) != want[1] {
			t.Errorf("%q: links %d bare %d", body, CountLinks(body), CountBareDomains(body))
		}
	}
	if (FactsOf(cfg, json.RawMessage(`{`)) != Facts{}) {
		t.Fatal("unreadable args must give no facts")
	}
}

func TestTestReport(t *testing.T) {
	r := newRig(t, nil)
	inst := r.inst()
	rep, err := New(Options{APIBase: r.srv.URL}).Test(context.Background(), inst)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range rep.Checks {
		ids = append(ids, c.ID)
		if c.Status == connector.CheckFail {
			t.Errorf("%s failed: %s", c.ID, c.Detail)
		}
	}
	if strings.Join(ids, ",") != strings.Join(TestCheckIDs, ",") {
		t.Fatalf("checks %v", ids)
	}
	if r.fake.Writes() != 0 {
		t.Fatal("Test wrote")
	}
}

func TestAuthorize(t *testing.T) {
	fake := fakegmail.New(mailbox)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	hc := devEnv.Egress.Client(egress.ClientOptions{Timeout: 5 * time.Second, Redirects: egress.RedirectNone})
	run := func(mb string) error {
		_, err := Authorize(context.Background(), AuthorizeOptions{ClientID: "id", ClientSecret: "s", Name: "mail", Mailbox: mb,
			AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", APIBaseURL: srv.URL, HTTP: hc, Timeout: 5 * time.Second,
			OnURL: func(consent, callback string) {
				u, _ := url.Parse(consent)
				if got := u.Query().Get("scope"); got != ScopeSend+" "+ScopeReadonly {
					t.Errorf("scope %q", got)
				}
				go func() {
					resp, err := http.Get(callback + "?code=abc&state=" + url.QueryEscape(u.Query().Get("state")))
					if err == nil {
						resp.Body.Close()
					}
				}()
			}})
		return err
	}
	if err := run(mailbox); err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if err := run("someone-else@" + domain); err == nil || !strings.Contains(err.Error(), "You signed in as") {
		t.Fatalf("wrong profile: %v", err)
	}
	if err := run("arjun@deedglass.com"); err == nil || err.Error() != MsgFromDenied {
		t.Fatalf("denied domain: %v", err)
	}
	fake.Email = "x@deedglass.com"
	if err := run("x@deedglass.com"); err == nil || err.Error() != MsgFromDenied {
		t.Fatalf("denied profile: %v", err)
	}
}

func FuzzOutboundHeaders(f *testing.F) {
	f.Add("your agent", "maya@acme.example", "Hi\n")
	f.Add("a\r\nBcc: x@y.example", "b@c.example", "x")
	f.Add("café ✓", "d@e.example", "\x00")
	f.Fuzz(func(t *testing.T, subject, to, body string) {
		raw, err := Build(Message{FromName: "A", From: mailbox, To: to, Subject: subject, BodyText: body, Date: t0,
			MessageID: "<a@b.example>", UnsubURL: "https://b.example/u/x", UnsubMailto: "mailto:u+x@b.example"})
		if err != nil {
			return
		}
		head, _, _ := bytes.Cut(raw, []byte("\r\n\r\n"))
		if bytes.Count(head, []byte("\r\nBcc:")) > 0 || bytes.Count(head, []byte("\r\nCc:")) > 0 {
			t.Fatalf("injected header: %q", head)
		}
	})
}

func FuzzBodyNormalise(f *testing.F) {
	f.Add("Hi\r\nthere  \n")
	f.Add("\r\r\n\t")
	f.Fuzz(func(t *testing.T, s string) {
		n := NormaliseBody(s)
		if NormaliseBody(n) != n || BodySHA256(n) != BodySHA256(s) {
			t.Fatalf("not idempotent: %q", s)
		}
	})
}

func FuzzMailQuery(f *testing.F) {
	f.Add("in:inbox newer_than:3d")
	f.Add("in:sent after:2026/10/01 before:2026/10/02")
	f.Add("rfc822msgid:<a@b.example>")
	f.Add("from:x OR in:inbox newer_than:3d")
	f.Fuzz(func(t *testing.T, q string) {
		if !ValidQuery(q) {
			return
		}
		if !(strings.HasPrefix(q, "in:inbox newer_than:") || strings.HasPrefix(q, "in:sent after:") || strings.HasPrefix(q, "rfc822msgid:")) {
			t.Fatalf("accepted %q", q)
		}
		if strings.ContainsAny(q, "{}()|\"") || strings.Contains(q, " OR ") {
			t.Fatalf("accepted operators in %q", q)
		}
	})
}
