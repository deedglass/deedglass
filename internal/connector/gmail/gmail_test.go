package gmail

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas/saastest"
	"github.com/deedglass/deedglass/internal/ids"
)

var update = flag.Bool("update", false, "rewrite the MIME goldens")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", "mime", name+".eml")
	if *update {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs:\n%s\nwant:\n%s", name, got, want)
	}
}

func mustAddrs(t *testing.T, xs ...string) []*mail.Address {
	t.Helper()
	as, err := ParseAddresses("to", xs)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func TestMIMEGoldens(t *testing.T) {
	base := Draft{To: mustAddrs(t, "Priya Raman <Priya@acme.com>"), Subject: "Order 4121", BodyText: "Hi Priya,\nYour refund is on its way.  \n", Date: "Sat, 10 Oct 2026 09:00:00 +0000"}
	plain, err := Build(base)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "plain", plain)
	html := base
	html.BodyHTML, html.Boundary = "<p>Hi Priya,</p><p>Your refund is on its way.</p>", "dgboundary0001"
	b, err := Build(html)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "html_alternative", b)
	nonASCII := base
	nonASCII.Subject = "Rückerstattung für Bestellung 4121 · ₹1,499.00"
	b, err = Build(nonASCII)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "non_ascii_subject", b)
	reply := base
	reply.Subject, reply.InReplyTo, reply.Cc = "Re: Order 4121", "<CAF1234@mail.gmail.com>", mustAddrs(t, "ops@acme.com")
	b, err = Build(reply)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "reply", b)
	// Round trip: what Build writes, ParseRaw reads back the same.
	for _, d := range []Draft{base, html, nonASCII, reply} {
		raw, _ := Build(d)
		v, err := ParseRaw(raw)
		if err != nil || v.Subject != d.Subject || BodyDigest(v.BodyText) != BodyDigest(d.BodyText) || v.To[0] != "priya@acme.com" {
			t.Errorf("round trip %q: %+v %v", d.Subject, v, err)
		}
	}
	if strings.Contains(string(plain), "eedglass") || strings.Contains(string(plain), "act_") {
		t.Error("the message names Deedglass")
	}
}

func TestHeaderInjection(t *testing.T) {
	r := newRig(t, nil)
	for _, args := range []string{
		`{"to":["priya@acme.com"],"subject":"Order 4121\r\nBcc: x@evil.test","body_text":"hi"}`,
		`{"to":["priya@acme.com\r\nBcc: x@evil.test"],"subject":"Order","body_text":"hi"}`,
		`{"to":["priya@acme.com"],"subject":"Order","body_text":"hi","in_reply_to":"<a@b>\nBcc: x@evil.test"}`,
	} {
		_, err, _ := r.call(ToolCreateDraft, args)
		if ce := connector.AsError(err); ce == nil || ce.Code != connector.CodeValidationFailed || ce.AgentMessage != MsgHeaderInjection {
			t.Errorf("%s: %v", args, err)
		}
	}
	if n := len(r.srv.Requests()); n != 0 {
		t.Errorf("%d requests", n)
	}
	if _, err := Build(Draft{Subject: "a\nb"}); err == nil {
		t.Error("Build accepted a line break")
	}
}

func TestRecipients(t *testing.T) {
	as, err := ParseAddresses("to", []string{`"Smith, John" <John@Acme.com>`})
	if err != nil || len(as) != 1 || as[0].Address != "john@acme.com" {
		t.Fatalf("%v %v", as, err)
	}
	if _, err := ParseAddresses("to", []string{"a@acme.com, b@evil.test"}); err == nil {
		t.Error("two addresses in one entry were accepted")
	}
	r := newRig(t, nil)
	_, err, _ = r.call(ToolCreateDraft, `{"to":["a@acme.com"],"bcc":["hidden@evil.test"],"subject":"x","body_text":"y"}`)
	if ce := connector.AsError(err); ce == nil || ce.Code != connector.CodeValidationFailed || !strings.Contains(ce.AgentMessage, "Bcc") {
		t.Errorf("bcc: %v", err)
	}
	_, err, _ = r.call(ToolCreateDraft, `{"to":["a@acme.com, b@evil.test"],"subject":"x","body_text":"y"}`)
	if ce := connector.AsError(err); ce == nil || ce.Code != connector.CodeValidationFailed {
		t.Errorf("comma trick: %v", err)
	}
	if n := len(r.srv.Requests()); n != 0 {
		t.Errorf("%d requests", n)
	}
}

func TestSendUnreachable(t *testing.T) {
	for _, c := range []struct{ m, p string }{
		{"POST", "/gmail/v1/users/me/drafts/send"}, {"POST", "/gmail/v1/users/me/messages/send"},
		{"POST", "/gmail/v1/users/me/messages/abc/modify"}, {"POST", "/gmail/v1/users/me/messages/abc/trash"},
		{"DELETE", "/gmail/v1/users/me/drafts/abc"}, {"POST", "/gmail/v1/users/drafts/drafts"}, {"GET", "/gmail/v1/users/me/messages/abc"},
		{"POST", "/gmail/v1/users/send/drafts"}, {"PUT", "/gmail/v1/users/me/drafts"},
	} {
		if Allowed(c.m, c.p) {
			t.Errorf("%s %s is allowed", c.m, c.p)
		}
	}
	for _, c := range []struct{ m, p string }{{"POST", "/gmail/v1/users/me/drafts"}, {"GET", "/gmail/v1/users/ops@acme.com/profile"}, {"PUT", "/gmail/v1/users/me/drafts/r123"}} {
		if !Allowed(c.m, c.p) {
			t.Errorf("%s %s is refused", c.m, c.p)
		}
	}
	// The builder refuses before any byte leaves, with an alert.
	r := newRig(t, nil, tokenEx())
	s, err := open(context.Background(), r.inst())
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for _, rest := range [][]string{{"drafts", "send"}, {"messages", "send"}, {"messages", "m1", "modify"}} {
		_, err := s.call(context.Background(), http.MethodPost, nil, map[string]any{}, nil, rest...)
		if ce := connector.AsError(err); ce == nil || ce.Code != "internal_error" {
			t.Errorf("%v: %v", rest, err)
		}
	}
	if n := len(r.srv.Requests()); n != 0 {
		t.Errorf("%d requests left; want 0", n)
	}
	if !strings.Contains(r.logs.String(), "alert=gmail_endpoint_refused") {
		t.Error("no alert")
	}
	// Source: no request path in the package names a send segment.
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`"send"|/send"|"send/|messages/`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "allow.go" {
			continue
		}
		b, _ := os.ReadFile(f)
		if m := re.Find(b); m != nil {
			t.Errorf("%s contains %q", f, m)
		}
	}
	for _, tl := range New().Info().Tools {
		if strings.Contains(tl.Name, "send") {
			t.Errorf("a send tool: %s", tl.Name)
		}
	}
}

func draftRaw(to, subject, body string) string {
	raw := "To: " + to + "\r\nSubject: " + subject + "\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" + body
	return base64.URLEncoding.EncodeToString([]byte(raw))
}

func TestCreateDraftVerified(t *testing.T) {
	post := gx("create", "POST", "/gmail/v1/users/me/drafts", 200, `{"id":"r-123","message":{"id":"m1","threadId":"t1"}}`)
	rb := gx("rb", "GET", "/gmail/v1/users/me/drafts/r-123", 200, `{"id":"r-123","message":{"threadId":"t1","raw":"`+draftRaw("Priya@acme.com", "Order 4121", "Hi Priya,  \r\nOn its way.\r\n\r\n")+`"}}`)
	rb.Query = map[string]string{"format": "raw"}
	r := newRig(t, nil, tokenEx(), post, rb)
	res, err, c := r.call(ToolCreateDraft, `{"to":["Priya Raman <priya@acme.com>"],"subject":"Order 4121","body_text":"Hi Priya,\nOn its way."}`)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(res.Output, &out)
	if out["draft_id"] != "r-123" {
		t.Errorf("%v", out)
	}
	var body struct {
		Message struct {
			Raw string `json:"raw"`
		} `json:"message"`
	}
	_ = json.Unmarshal(r.srv.Requests()[1].Body, &body)
	raw, _ := base64.URLEncoding.DecodeString(body.Message.Raw)
	if !strings.Contains(string(raw), "To: \"Priya Raman\" <priya@acme.com>") {
		t.Errorf("raw %s", raw)
	}
	_, fs, err := r.readBack(c, res)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if !f.Match {
			t.Errorf("%s: %v vs %v", f.Field, f.Expected, f.Observed)
		}
	}
	if r.srv.Count("token") != 1 {
		t.Errorf("token minted %d times; the cache keeps one", r.srv.Count("token"))
	}
	r.sweep(string(res.Output))
}

func TestReadBackMismatch(t *testing.T) {
	rb := gx("rb", "GET", "/gmail/v1/users/me/drafts/r-123", 200, `{"id":"r-123","message":{"threadId":"t1","raw":"`+draftRaw("priya@acme.com", "Order 9999", "Hi")+`"}}`)
	r := newRig(t, nil, tokenEx(), rb)
	ex, spec, _, _ := r.reg.Resolve(context.Background(), org, "gmail.create_draft")
	c := connector.Call{ActionID: mustAct(), OrgID: org, Tool: spec, Qualified: "gmail.create_draft",
		Args: json.RawMessage(`{"to":["priya@acme.com"],"subject":"Order 4121","body_text":"Hi"}`)}
	_, fs, err := ex.(connector.NativeReadBacking).NativeReadBack(context.Background(), c, connector.Result{Evidence: map[string]string{"draft_id": "r-123"}})
	if err != nil {
		t.Fatal(err)
	}
	if fs[2].Field != "subject" || fs[2].Match {
		t.Errorf("%+v", fs)
	}
}

func TestGetThreadScope(t *testing.T) {
	r := newRig(t, nil)
	_, err, _ := r.call(ToolGetThread, `{"thread_id":"t1"}`)
	if err == nil || !strings.Contains(err.Error(), "no tool") {
		t.Fatalf("get_thread without readonly: %v", err)
	}
	tools, _ := New().Tools(context.Background(), r.inst())
	for _, tl := range tools {
		if tl.Name == ToolGetThread {
			t.Error("listed")
		}
	}
	thread := gx("thread", "GET", "/gmail/v1/users/me/threads/t1", 200, `{"messages":[{"id":"m1","raw":"`+draftRaw("a@acme.com", "Hello", "Body")+`"}]}`)
	r = newRig(t, func(m map[string]any) { m["scopes"] = []any{ScopeCompose, ScopeReadonly} }, tokenEx(), thread)
	res, err, _ := r.call(ToolGetThread, `{"thread_id":"t1"}`)
	if err != nil || !strings.Contains(string(res.Output), `"subject":"Hello"`) {
		t.Fatalf("%v %s", err, res.Output)
	}
}

func TestInvalidGrant(t *testing.T) {
	tok := saastest.Exchange{Name: "token", Method: "POST", Path: "/token", Status: 400, Body: json.RawMessage(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)}
	r := newRig(t, nil, tok)
	_, err, _ := r.call(ToolListDrafts, `{}`)
	ce := connector.AsError(err)
	if ce == nil || ce.Code != connector.CodeUpstreamAuthFailed || ce.AgentMessage != MsgReauth || !strings.Contains(ce.AdminDetail, ReasonNeedsReauth) {
		t.Fatalf("%v", err)
	}
}

func TestServiceAccountJWT(t *testing.T) {
	key := saastest.TestRSAKey()
	sa, _ := json.Marshal(map[string]string{"client_email": "dg@proj.iam.gserviceaccount.com", "private_key": string(saastest.TestRSAKeyPEM()), "token_uri": DefaultTokenURL})
	var assertion string
	tok := saastest.Exchange{Name: "token", Method: "POST", Path: "/token", Status: 200, BodySubset: map[string]any{"grant_type": "urn:ietf:params:oauth:grant-type:jwt-bearer"},
		Body: json.RawMessage(`{"access_token":"` + accessTok + `","expires_in":3600}`)}
	r := newRig(t, func(m map[string]any) { m["auth"], m["mailbox"] = "service_account", "ops@acme.com" }, tok,
		gx("list", "GET", "/gmail/v1/users/ops@acme.com/drafts", 200, `{"drafts":[{"id":"r1","message":{"threadId":"t1"}}]}`))
	r.op.fields = map[string]string{FieldServiceAccountJSON: string(sa)}
	if _, err, _ := r.call(ToolListDrafts, `{}`); err != nil {
		t.Fatal(err)
	}
	q, _ := url.ParseQuery(string(r.srv.Requests()[0].Body))
	assertion = q.Get("assertion")
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("assertion %q", assertion)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(cb, &claims)
	if claims["sub"] != "ops@acme.com" || claims["scope"] != ScopeCompose || claims["iss"] != "dg@proj.iam.gserviceaccount.com" {
		t.Errorf("claims %v", claims)
	}
	if claims["exp"].(float64)-claims["iat"].(float64) != 3600 {
		t.Errorf("lifetime %v", claims)
	}
}

func TestEnrichAndSummary(t *testing.T) {
	r := newRig(t, nil)
	ex, spec, _, _ := r.reg.Resolve(context.Background(), org, "gmail.create_draft")
	m, _ := ex.(connector.ArgsEnricher).EnrichArgs(context.Background(), spec,
		json.RawMessage(`{"to":["priya@acme.com","bob@globex.test"],"subject":"Re: Order 4121","body_text":"x","thread_id":"t1"}`))
	b, _ := json.Marshal(m["email"])
	if string(b) != `{"domains":["acme.com","globex.test"],"enriched":true,"external_domains":["globex.test"],"has_bcc":false,"has_external":true,"is_reply":true,"recipient_count":2}` {
		t.Errorf("%s", b)
	}
	got := New().Summarize(connector.SummaryInput{Agent: "support-bot", Qualified: "gmail.create_draft", Tool: connector.ToolSpec{Name: ToolCreateDraft},
		Args: json.RawMessage(`{"to":["priya@acme.com","bob@globex.test"],"subject":"Re: Order 4121","body_text":"x"}`), Facts: m}).Plain()
	if got != `support-bot wants to draft an email to 2 people, 1 outside acme.com: "Re: Order 4121".` {
		t.Errorf("%q", got)
	}
}

func TestConnectorTest(t *testing.T) {
	prof := gx("profile", "GET", "/gmail/v1/users/me/profile", 200, `{"emailAddress":"arjun@acme.com"}`)
	r := newRig(t, func(m map[string]any) {
		m["authorized_at"] = time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	}, tokenEx(), prof)
	rep, err := New().Test(context.Background(), r.inst())
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, c := range rep.Checks {
		got = append(got, c.ID+":"+string(c.Status))
	}
	if strings.Join(got, " ") != "config:pass credentials:warn destination:pass auth:pass profile:pass scopes:pass send_guard:pass" {
		t.Errorf("%s", strings.Join(got, " "))
	}
	if !strings.Contains(rep.Checks[1].Detail, "Authorize again before") || rep.Checks[6].Detail != MsgSendGuard {
		t.Errorf("%+v", rep.Checks)
	}
	r.sweep(fmt.Sprint(rep.Checks))
}

// Authorize: a fake Google (consent is a GET the test drives; token and profile on one server).
type fakeGoogle struct {
	mu           sync.Mutex
	srv          *httptest.Server
	refresh      string
	tokenCalls   int
	lastVerifier string
}

func newFakeGoogle(t *testing.T, refresh string) *fakeGoogle {
	g := &fakeGoogle{refresh: refresh}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		g.tokenCalls++
		g.lastVerifier = r.PostForm.Get("code_verifier")
		g.mu.Unlock()
		if r.PostForm.Get("code") != "code-ok" {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		b, _ := json.Marshal(map[string]any{"access_token": accessTok, "expires_in": 3600, "refresh_token": g.refresh, "scope": ScopeCompose})
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"emailAddress":"Arjun@Example.com"}`)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func runAuthorize(t *testing.T, g *fakeGoogle, timeout time.Duration, drive func(consent, callback string) *http.Response) (AuthorizeResult, error, *http.Response) {
	t.Helper()
	var resp *http.Response
	done := make(chan struct{})
	started := false
	pol := egressDev()
	res, err := Authorize(context.Background(), AuthorizeOptions{ClientID: "123.apps.googleusercontent.com", ClientSecret: clientSecret, Name: "gmail",
		AuthURL: g.srv.URL + "/auth", TokenURL: g.srv.URL + "/token", APIBaseURL: g.srv.URL, HTTP: pol, Timeout: timeout,
		OnURL: func(consent, callback string) {
			if drive != nil {
				started = true
				go func() { resp = drive(consent, callback); close(done) }()
			}
		}})
	if started {
		<-done
	}
	return res, err, resp
}

func TestAuthorize(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		g := newFakeGoogle(t, refreshToken)
		var page string
		res, err, _ := runAuthorize(t, g, 5*time.Second, func(consent, callback string) *http.Response {
			u, _ := url.Parse(consent)
			q := u.Query()
			if q.Get("code_challenge_method") != "S256" || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || !strings.HasPrefix(callback, "http://127.0.0.1:") {
				t.Errorf("consent %s", consent)
			}
			r, err := http.Get(callback + "?state=" + url.QueryEscape(q.Get("state")) + "&code=code-ok")
			if err == nil {
				b, _ := io.ReadAll(r.Body)
				page = string(b)
			}
			return r
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.Email != "arjun@example.com" || res.RefreshToken != refreshToken || len(res.Scopes) != 1 {
			t.Errorf("%+v", res)
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		if len(g.lastVerifier) != 86 {
			t.Errorf("verifier length %d", len(g.lastVerifier))
		}
		if !strings.Contains(page, "Deedglass is connected to <span class=\"who\" translate=\"no\">arjun@example.com</span>. You can close this tab.") || strings.Contains(page, "http") && strings.Contains(page, "src=\"http") {
			t.Errorf("page %s", page)
		}
	})
	t.Run("state mismatch", func(t *testing.T) {
		g := newFakeGoogle(t, refreshToken)
		_, err, _ := runAuthorize(t, g, 5*time.Second, func(_, callback string) *http.Response {
			r, _ := http.Get(callback + "?state=forged&code=code-ok")
			return r
		})
		g.mu.Lock()
		defer g.mu.Unlock()
		if err == nil || err.Error() != MsgStateMismatch || g.tokenCalls != 0 {
			t.Fatalf("%v (token calls %d)", err, g.tokenCalls)
		}
	})
	t.Run("no refresh token", func(t *testing.T) {
		g := newFakeGoogle(t, "")
		_, err, _ := runAuthorize(t, g, 5*time.Second, func(consent, callback string) *http.Response {
			u, _ := url.Parse(consent)
			r, _ := http.Get(callback + "?state=" + url.QueryEscape(u.Query().Get("state")) + "&code=code-ok")
			return r
		})
		if err == nil || err.Error() != MsgNoRefreshToken {
			t.Fatalf("%v", err)
		}
	})
	t.Run("denied", func(t *testing.T) {
		g := newFakeGoogle(t, refreshToken)
		_, err, _ := runAuthorize(t, g, 5*time.Second, func(consent, callback string) *http.Response {
			u, _ := url.Parse(consent)
			r, _ := http.Get(callback + "?state=" + url.QueryEscape(u.Query().Get("state")) + "&error=access_denied")
			return r
		})
		if err == nil || err.Error() != MsgDenied {
			t.Fatalf("%v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		g := newFakeGoogle(t, refreshToken)
		_, err, _ := runAuthorize(t, g, 200*time.Millisecond, nil)
		if err == nil || err.Error() != MsgTimeout {
			t.Fatalf("%v", err)
		}
	})
}

func TestPageGolden(t *testing.T) {
	for _, d := range []PageData{{OK: true, Title: "Gmail is connected", Email: "arjun@example.com", Name: "gmail"},
		{OK: false, Title: "Gmail isn't connected", Message: MsgNoRefreshToken, Name: "gmail"}} {
		var b strings.Builder
		if err := WritePage(&b, d); err != nil {
			t.Fatal(err)
		}
		s := b.String()
		for _, bad := range []string{"src=\"http", "href=\"http", "<script", "—", "–", "!"} {
			if strings.Contains(strings.TrimPrefix(s, "<!doctype html>"), bad) {
				t.Errorf("page has %q", bad)
			}
		}
		name := "page_ok"
		if !d.OK {
			name = "page_fail"
		}
		goldenHTML(t, name, s)
	}
}

func goldenHTML(t *testing.T, name, got string) {
	p := filepath.Join("testdata", name+".html")
	if *update {
		_ = os.WriteFile(p, []byte(got), 0o644)
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs", name)
	}
}

func TestCatalogAndValidate(t *testing.T) {
	info := New().Info()
	if len(info.Tools) != 5 || info.Modes[0] != "drafts_only" {
		t.Fatalf("%d tools", len(info.Tools))
	}
	for _, tl := range info.Tools {
		if tl.SideEffect == connector.SideEffectWrite && tl.ReadBack == "" {
			t.Errorf("%s has no read_back", tl.Name)
		}
	}
	prod := connector.Env{Name: "prod"}
	for _, bad := range []string{`{"auth":"magic"}`, `{"auth":"service_account","mailbox":"me"}`, `{"api_base_url":"https://evil.test"}`,
		`{"send":true}`, `{"max_recipients":-1}`, `{"internal_domains":["not a domain"]}`, `{"auth":"oauth","mailbox":"x@acme.com"}`} {
		if _, errs := New().Validate(context.Background(), prod, json.RawMessage(bad)); len(errs) == 0 {
			t.Errorf("%s accepted", bad)
		}
	}
}

func mustAct() ids.ActionID { return ids.MustParse[ids.ActionKind](actID) }

func egressDev() *http.Client {
	return (&egress.Policy{Env: egress.EnvDev, AllowPrivate: true}).Client(egress.ClientOptions{Timeout: 5 * time.Second, Redirects: egress.RedirectNone})
}
