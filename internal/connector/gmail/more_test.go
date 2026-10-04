package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
)

func TestDraftLifecycle(t *testing.T) {
	put := gx("update", "PUT", "/gmail/v1/users/me/drafts/r-123", 200, `{"id":"r-123","message":{"id":"m2","threadId":"t1"}}`)
	list := gx("list", "GET", "/gmail/v1/users/me/drafts", 200, `{"drafts":[{"id":"r-123","message":{"threadId":"t1"}}]}`)
	list.Query = map[string]string{"maxResults": "10"}
	raw := "To: a@acme.com\r\nCc: b@acme.com\r\nSubject: =?utf-8?q?Caf=C3=A9?=\r\nContent-Type: multipart/alternative; boundary=\"zz\"\r\n\r\n--zz\r\nContent-Type: text/html\r\n\r\n<p>x</p>\r\n--zz\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString([]byte("Héllo\n")) + "\r\n--zz--\r\n"
	get := gx("get", "GET", "/gmail/v1/users/me/drafts/r-123", 200, `{"id":"r-123","message":{"threadId":"t1","raw":"`+base64.URLEncoding.EncodeToString([]byte(raw))+`"}}`)
	r := newRig(t, nil, tokenEx(), put, list, get)
	res, err, c := r.call(ToolUpdateDraft, `{"draft_id":"r-123","to":["a@acme.com"],"cc":["b@acme.com"],"subject":"Café","body_text":"Héllo","body_html":"<p>x</p>"}`)
	if err != nil || !strings.Contains(string(res.Output), `"draft_id":"r-123"`) {
		t.Fatalf("%v %s", err, res.Output)
	}
	_ = c
	if res, err, _ = r.call(ToolListDrafts, `{}`); err != nil || !strings.Contains(string(res.Output), "r-123") {
		t.Fatalf("%v %s", err, res.Output)
	}
	res, err, _ = r.call(ToolGetDraft, `{"draft_id":"r-123"}`)
	if err != nil || !strings.Contains(string(res.Output), `"subject":"Café"`) || !strings.Contains(string(res.Output), "Héllo") {
		t.Fatalf("%v %s", err, res.Output)
	}
}

func TestDraftRefusals(t *testing.T) {
	r := newRig(t, func(m map[string]any) { m["max_recipients"] = 2 })
	for _, tc := range []struct{ args, want string }{
		{`{"to":["a@acme.com","b@acme.com","c@acme.com"],"subject":"x","body_text":"y"}`, "3 recipients"},
		{`{"to":[],"subject":"x","body_text":"y"}`, "at least one recipient"},
		{`{"to":["a@acme.com"],"subject":"  ","body_text":"y"}`, "Send a subject"},
		{`{"to":["a@acme.com"],"subject":"x","body_text":"y","in_reply_to":"abc"}`, "Message-ID"},
		{`{"to":["not an address"],"subject":"x","body_text":"y"}`, "exactly one email address"},
	} {
		_, err, _ := r.call(ToolCreateDraft, tc.args)
		if ce := connector.AsError(err); ce == nil || !strings.Contains(ce.AgentMessage, tc.want) {
			t.Errorf("%s: %v", tc.args, err)
		}
	}
}

func TestClassifyGmail(t *testing.T) {
	for st, kind := range map[int]connector.Kind{401: connector.KindAuth, 403: connector.KindAuth, 404: connector.KindNotFound, 429: connector.KindRateLimited,
		400: connector.KindToolError, 500: connector.KindUpstream} {
		e := classify(&saas.Response{Status: st, Header: http.Header{}, Body: []byte(`{"error":{"status":"X"}}`)})
		if e == nil || e.Kind != kind {
			t.Errorf("%d: %v", st, e)
		}
	}
	if classify(&saas.Response{Status: 200}) != nil {
		t.Error("200")
	}
}

func TestGmailTestFailures(t *testing.T) {
	r := newRig(t, nil)
	r.op.fields = map[string]string{FieldClientID: "x"}
	rep, _ := New().Test(context.Background(), r.inst())
	if rep.Checks[3].Status != connector.CheckFail || rep.Checks[6].ID != "send_guard" {
		t.Errorf("%+v", rep.Checks)
	}
	prof := gx("profile", "GET", "/gmail/v1/users/me/profile", 200, `{"emailAddress":"someone@else.test"}`)
	r = newRig(t, func(m map[string]any) { m["scopes"] = []any{"https://www.googleapis.com/auth/gmail.readonly"} }, tokenEx(), prof)
	rep, _ = New().Test(context.Background(), r.inst())
	if rep.Checks[4].Status != connector.CheckFail || rep.Checks[5].Status != connector.CheckFail {
		t.Errorf("%+v", rep.Checks)
	}
	r = newRig(t, func(m map[string]any) { m["auth"], m["mailbox"] = "service_account", "ops@acme.com" }, saastest.Exchange{Name: "t", Method: "POST", Path: "/token", Status: 400, Optional: true, Body: json.RawMessage(`{"error":"invalid_grant"}`)})
	r.op.fields = map[string]string{FieldServiceAccountJSON: `{"client_email":"x"}`}
	rep, _ = New().Test(context.Background(), r.inst())
	if rep.Checks[1].Detail != "service account · acting as ops@acme.com" || rep.Checks[3].Status != connector.CheckFail {
		t.Errorf("%+v", rep.Checks)
	}
	if !hasScope([]string{"gmail.compose"}, ScopeCompose) || hasScope(nil, ScopeCompose) {
		t.Error("hasScope")
	}
	if errText(nil) == "" {
		t.Error("errText")
	}
}

func TestEnrichEdges(t *testing.T) {
	r := newRig(t, nil)
	ex, spec, _, _ := r.reg.Resolve(context.Background(), org, "gmail.create_draft")
	m, _ := ex.(connector.ArgsEnricher).EnrichArgs(context.Background(), spec, json.RawMessage(`{"to":["bad"],"subject":"x","body_text":"y"}`))
	if b, _ := json.Marshal(m["email"]); strings.Contains(string(b), `"enriched":true`) {
		t.Errorf("%s", b)
	}
	ex2, spec2, _, _ := r.reg.Resolve(context.Background(), org, "gmail.list_drafts")
	if m, _ := ex2.(connector.ArgsEnricher).EnrichArgs(context.Background(), spec2, json.RawMessage(`{}`)); m != nil {
		t.Error("a read enriched")
	}
	got := New().Summarize(connector.SummaryInput{Qualified: "gmail.update_draft", Args: json.RawMessage(`{"to":["a@acme.com"],"subject":"` + strings.Repeat("s", 100) + `"}`)}).Plain()
	if !strings.HasPrefix(got, "An agent wants to draft an email to 1 person: \"") || !strings.HasSuffix(got, "…\".") {
		t.Errorf("%q", got)
	}
	if New().Summarize(connector.SummaryInput{Qualified: "gmail.get_draft", Args: json.RawMessage(`{}`)}) != nil {
		t.Error("a read summarized")
	}
	if clipBytes("héllo", 2) != "h" || clipBytes("ab", 5) != "ab" {
		t.Errorf("%q", clipBytes("héllo", 2))
	}
}

func TestAuthorizeEdges(t *testing.T) {
	if _, err := Authorize(context.Background(), AuthorizeOptions{}); err == nil {
		t.Error("no client accepted")
	}
	g := newFakeGoogle(t, refreshToken)
	_, err, _ := runAuthorize(t, g, 5*time.Second, func(consent, callback string) *http.Response {
		r, _ := http.Get(callback + "?state=" + mustState(consent) + "&code=wrong")
		return r
	})
	if err == nil || !strings.Contains(err.Error(), "refused the sign-in code") {
		t.Errorf("%v", err)
	}
	_, err, _ = runAuthorize(t, g, 5*time.Second, func(consent, callback string) *http.Response {
		r, _ := http.Get(callback + "?state=" + mustState(consent))
		return r
	})
	if err == nil || !strings.Contains(err.Error(), "no code") {
		t.Errorf("%v", err)
	}
	if nonEmpty("", "d") != "d" {
		t.Error("nonEmpty")
	}
}

func mustState(consent string) string {
	i := strings.Index(consent, "state=")
	s := consent[i+6:]
	if j := strings.Index(s, "&"); j >= 0 {
		s = s[:j]
	}
	return s
}

func TestValidateEdges(t *testing.T) {
	env := connector.Env{Name: "dev"}
	if _, errs := New().Validate(context.Background(), env, nil); len(errs) != 0 {
		t.Errorf("%v", errs)
	}
	for _, bad := range []string{`[`, strings.Repeat(" ", 17<<10), `{"email":"no"}`, `{"scopes":["x"]}`, `{"api_base_url":"ftp://x"}`, `{"` + strings.Repeat("k", 80) + `":1}`, `{"auth":1}`} {
		if _, errs := New().Validate(context.Background(), env, json.RawMessage(bad)); len(errs) == 0 {
			t.Errorf("%.30s accepted", bad)
		}
	}
	if credError(nil_inst(), "why").AdminDetail == "" {
		t.Error("credError")
	}
}

func nil_inst() *connector.Instance {
	return connector.NewInstance(mustCon(), org, "gmail", Type, nil, "", connector.Env{}, nil)
}
