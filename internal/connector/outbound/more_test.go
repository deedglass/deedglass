//go:build outboundmail

package outbound

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

func TestInfoAndTools(t *testing.T) {
	c := New(Options{})
	if c.APIBase() != DefaultAPIBase || c.TokenURL() != DefaultTokenURL || c.Type() != Type {
		t.Fatal("defaults")
	}
	info := c.Info()
	if info.Type != Type || len(info.Tools) != 5 || len(info.CredentialKinds) != 1 || !json.Valid(info.ConfigSchema) {
		t.Fatalf("%+v", info)
	}
	for _, s := range toolSpecs() {
		if !json.Valid(s.InputSchema) {
			t.Errorf("%s schema", s.Name)
		}
	}
	r := newRig(t, nil)
	ts, err := New(Options{APIBase: r.srv.URL}).Tools(context.Background(), r.inst())
	if err != nil || len(ts) != 5 {
		t.Fatal(err)
	}
}

func TestClassify(t *testing.T) {
	for status, kind := range map[int]connector.Kind{401: connector.KindAuth, 403: connector.KindAuth, 404: connector.KindNotFound,
		429: connector.KindRateLimited, 400: connector.KindToolError, 500: connector.KindUpstream, 503: connector.KindUpstream} {
		e := classify(&saas.Response{Status: status, Header: http.Header{}})
		if e == nil || e.Kind != kind {
			t.Errorf("%d: %+v", status, e)
		}
	}
	if classify(&saas.Response{Status: 200}) != nil {
		t.Error("200")
	}
}

func TestThreadedSend(t *testing.T) {
	r := newRig(t, nil)
	args := sendArgs(func(m map[string]any) {
		m["step"], m["subject"] = 2, "Re: your agent and Postgres"
		m["thread"] = map[string]any{"gmail_thread_id": "thr000009", "in_reply_to": "<a@" + domain + ">", "references": []any{"<a@" + domain + ">"}}
	})
	res, c, err := r.call(org, ToolSend, args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Evidence["thread_id"] != "thr000009" {
		t.Fatalf("%v", res.Evidence)
	}
	_, fs, err := r.readBack(c, res)
	if err != nil {
		t.Fatal(err)
	}
	allMatch(t, fs)
	raw := string(r.fake.Messages()[0].Raw)
	if !strings.Contains(raw, "In-Reply-To: <a@"+domain+">") || !strings.Contains(raw, "References: <a@"+domain+">") {
		t.Fatal("threading headers")
	}
	for _, bad := range []map[string]any{
		{"gmail_thread_id": "bad id!", "in_reply_to": "<a@b>"},
		{"gmail_thread_id": "t1", "in_reply_to": "no-brackets"},
		{"gmail_thread_id": "t1", "in_reply_to": "<a@b>", "references": []any{"x"}},
	} {
		if _, _, err := r.call(org, ToolSend, sendArgs(func(m map[string]any) { m["thread"] = bad })); codeOf(err) != connector.CodeValidationFailed {
			t.Errorf("%v: %v", bad, err)
		}
	}
}

func TestParseRawBounce(t *testing.T) {
	raw := "From: mailer-daemon@googlemail.com\r\nTo: arjun@trydeedglass.example\r\nSubject: Delivery Status Notification\r\n" +
		"Content-Type: multipart/report; report-type=delivery-status; boundary=\"b1\"\r\n\r\n--b1\r\nContent-Type: text/plain\r\n\r\nIt bounced.\r\n" +
		"--b1\r\nContent-Type: message/delivery-status\r\n\r\nFinal-Recipient: rfc822; maya@acme.example\r\nAction: failed\r\nStatus: 5.1.1\r\n" +
		"Diagnostic-Code: smtp; 550 5.1.1 no such user\r\n--b1--\r\n"
	v, err := ParseRaw([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if v.DSN["status"] != "5.1.1" || v.DSN["action"] != "failed" || !strings.Contains(v.DSN["diagnostic_code"], "550") {
		t.Fatalf("%v", v.DSN)
	}
	if _, ok := v.public()["dsn"]; !ok {
		t.Fatal("public view drops the bounce")
	}
	// S41: the original's Message-ID from a text/rfc822-headers part, the auto-reply headers,
	// References and Delivered-To.
	withOrig := strings.Replace(raw, "--b1--", "--b1\r\nContent-Type: text/rfc822-headers\r\n\r\nFrom: arjun@trydeedglass.example\r\nMessage-ID: <m1@trydeedglass.example>\r\n\r\n--b1--", 1)
	if v, err := ParseRaw([]byte(withOrig)); err != nil || v.DSN["original_message_id"] != "<m1@trydeedglass.example>" || v.DSN["status"] != "5.1.1" {
		t.Fatalf("original id: %v %v", err, v.DSN)
	}
	auto := "From: sam@lumen.example\r\nTo: arjun@trydeedglass.example\r\nDelivered-To: U+abc@trydeedglass.example\r\nSubject: Re: x\r\n" +
		"Auto-Submitted: auto-replied\r\nPrecedence: auto_reply\r\nReferences: <a@t.example>\r\n  <b@t.example>\r\n\r\nAway.\r\n"
	av, err := ParseRaw([]byte(auto))
	if err != nil || av.Headers["auto-submitted"] != "auto-replied" || av.Headers["precedence"] != "auto_reply" || av.References != "<a@t.example> <b@t.example>" || av.DeliveredTo != "u+abc@trydeedglass.example" {
		t.Fatalf("headers: %v %+v", err, av)
	}
	pub := av.public()
	if pub["headers"] == nil || pub["references"] == nil || pub["delivered_to"] == nil {
		t.Fatalf("public view: %v", pub)
	}
	if _, ok := v.public()["headers"]; ok {
		t.Fatal("no headers key when there are none")
	}
	if !isDigits("81431") || isDigits("8a") || isDigits("") {
		t.Fatal("isDigits")
	}
	b64 := "From: a@b.example\r\nTo: c@d.example\r\nSubject: =?UTF-8?Q?caf=C3=A9?=\r\nContent-Type: multipart/alternative; boundary=x\r\n\r\n--x\r\n" +
		"Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\naGVs\r\nbG8K\r\n--x--\r\n"
	v, err = ParseRaw([]byte(b64))
	if err != nil || v.Subject != "café" || v.BodyText != "hello\n" {
		t.Fatalf("%v %+v", err, v)
	}
	if _, err := ParseRaw([]byte("not a message")); err == nil {
		t.Fatal("garbage parsed")
	}
	if clipBytes("héllo", 2) != "h" || clipBytes("abc", 10) != "abc" {
		t.Fatal("clip")
	}
}

func TestHistoryExpiredAndUnknownTool(t *testing.T) {
	r := newRig(t, nil)
	// The fake answers unknown paths with 404; an unknown start id on a real mailbox is 404 too.
	old := r.srv.Config.Handler
	r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path == "/gmail/v1/users/me/history" {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		old.ServeHTTP(w, q)
	})
	if _, _, err := r.call(org, ToolListHistory, `{"start_history_id":"9"}`); codeOf(err) != CodeHistoryExpired {
		t.Fatalf("%v", err)
	}
	inst := r.inst()
	if _, err := New(Options{APIBase: r.srv.URL}).Execute(context.Background(), inst, connector.Call{Tool: connector.ToolSpec{Name: "trash"}}); err == nil {
		t.Fatal("unknown tool ran")
	}
	if _, _, err := New(Options{APIBase: r.srv.URL}).NativeReadBack(context.Background(), inst, connector.Call{Tool: connector.ToolSpec{Name: ToolGetMessage}}, connector.Result{}); err == nil {
		t.Fatal("read tools aren't read back")
	}
}

func TestExecuteFromGuardTrips(t *testing.T) {
	r := newRig(t, nil)
	// A row whose stored config names a denied mailbox (written around Validate) never sends.
	var cfg Config
	_ = json.Unmarshal(r.row.Config, &cfg)
	cfg.Mailbox, cfg.SendingDomain = "arjun@deedglass.com", "deedglass.com"
	r.row.Config, _ = json.Marshal(cfg)
	r.row.ConfigHash += "-tampered"
	c := New(Options{APIBase: r.srv.URL})
	inst := r.inst()
	_, err := c.Execute(context.Background(), inst, connector.Call{Tool: connector.ToolSpec{Name: ToolSend}, Args: json.RawMessage(sendArgs(nil))})
	if codeOf(err) != connector.CodeConnectorUnavailable || len(r.br.trips) != 1 || len(r.fake.Log()) != 0 {
		t.Fatalf("%v trips %v log %d", err, r.br.trips, len(r.fake.Log()))
	}
}

func TestEnrichAndCheckArgs(t *testing.T) {
	r := newRig(t, nil)
	c := New(Options{APIBase: r.srv.URL})
	m, err := c.Enrich(context.Background(), r.inst(), connector.ToolSpec{Name: ToolSend}, json.RawMessage(sendArgs(nil)))
	if err != nil || m["mail"].(Facts).FromDomain != domain {
		t.Fatalf("%v %v", err, m)
	}
	if m, _ := c.Enrich(context.Background(), r.inst(), connector.ToolSpec{Name: ToolGetMessage}, nil); m != nil {
		t.Fatal("reads have no facts")
	}
	if err := c.CheckArgs(connector.ToolSpec{Name: ToolListMessages}, json.RawMessage(`{"q":"from:x"}`)); err == nil {
		t.Fatal("query grammar")
	}
	if err := c.CheckArgs(connector.ToolSpec{Name: ToolSend}, json.RawMessage(`{`)); err == nil {
		t.Fatal("unreadable args")
	}
	if got := windowText(SendWindow{NotBefore: t0, NotAfter: t0.Add(time.Hour)}); !strings.HasSuffix(got, "UTC") {
		t.Fatalf("%q", got)
	}
}

func TestValidateMore(t *testing.T) {
	c := New(Options{})
	for raw, param := range map[string]string{
		`[]`: "config",
		`{"mailbox":"` + mailbox + `","sending_domain":"` + domain + `","deny_from_domains":["not a domain"],"unsubscribe_base":"https://` + domain + `/u/"}`:                       "config.deny_from_domains[0]",
		`{"mailbox":"` + mailbox + `","sending_domain":"` + domain + `","deny_from_domains":["deedglass.com"],"unsubscribe_base":"https://` + domain + `/u/","scopes":["evil"]}`:    "config.scopes[0]",
		`{"mailbox":"` + mailbox + `","sending_domain":"` + domain + `","deny_from_domains":["deedglass.com"],"unsubscribe_base":"https://` + domain + `/u/","require_window":"x"}`: "config",
		`{"mailbox":"` + mailbox + `","sending_domain":"bad","deny_from_domains":["deedglass.com"],"unsubscribe_base":"https://bad/u/"}`:                                            "config.sending_domain",
	} {
		_, errs := c.Validate(context.Background(), devEnv, json.RawMessage(raw))
		if len(errs) == 0 || errs[0].Param != param {
			t.Errorf("%s: %+v", raw, errs)
		}
	}
	if _, errs := c.Validate(context.Background(), devEnv, json.RawMessage(`{"x":"`+strings.Repeat("a", MaxConfigBytes)+`"}`)); len(errs) == 0 {
		t.Error("oversized config")
	}
}

func TestTestReportFailures(t *testing.T) {
	r := newRig(t, nil)
	c := New(Options{APIBase: r.srv.URL})
	// The profile belongs to someone else.
	r.fake.Email = "someone@" + domain
	rep, _ := c.Test(context.Background(), r.inst())
	var failed []string
	for _, ch := range rep.Checks {
		if ch.Status == connector.CheckFail {
			failed = append(failed, ch.ID)
		}
	}
	if strings.Join(failed, ",") != "profile" {
		t.Fatalf("failed %v", failed)
	}
	// Auth refused: the fake rejects any token but its own.
	r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	})
	tokens = &saas.TokenCache{}
	rep, _ = c.Test(context.Background(), r.inst())
	auth := false
	for _, ch := range rep.Checks {
		if ch.ID == "auth" && ch.Status == connector.CheckFail {
			auth = true
		}
	}
	if !auth {
		t.Fatalf("%+v", rep.Checks)
	}
}
