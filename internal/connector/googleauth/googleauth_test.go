package googleauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

func tokenServer(t *testing.T, refresh bool, status int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		if status != 200 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		ans := map[string]any{"access_token": "at", "expires_in": 100000, "scope": "a b"}
		if refresh {
			ans["refresh_token"] = "rt"
		}
		_ = json.NewEncoder(w).Encode(ans)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func client() *http.Client {
	return (&egress.Policy{Env: egress.EnvDev, AllowPrivate: true}).Client(egress.ClientOptions{Timeout: 5 * time.Second, Redirects: egress.RedirectNone})
}

func run(t *testing.T, srv *httptest.Server, callback func(consent, cb string) string, timeout time.Duration) (Result, error, Outcome) {
	t.Helper()
	var page Outcome
	res, err := Authorize(context.Background(), Options{ClientID: "id", ClientSecret: "s", Scopes: []string{"x", "y"},
		AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token", HTTP: client(), Timeout: timeout,
		OnURL: func(consent, cb string) {
			u := callback(consent, cb)
			if u == "" {
				return
			}
			go func() {
				resp, err := http.Get(u)
				if err == nil {
					resp.Body.Close()
				}
			}()
		},
		Finish: func(_ context.Context, ans TokenAnswer) (Result, error) {
			return Result{Email: "me@x.example", RefreshToken: ans.RefreshToken}, nil
		},
		Page: func(w http.ResponseWriter, o Outcome) { page = o; _, _ = w.Write([]byte("ok")) }})
	return res, err, page
}

func stateOf(consent string) string {
	u, _ := url.Parse(consent)
	return u.Query().Get("state")
}

func TestAuthorize(t *testing.T) {
	srv := tokenServer(t, true, 200)
	res, err, page := run(t, srv, func(consent, cb string) string {
		u, _ := url.Parse(consent)
		q := u.Query()
		if q.Get("scope") != "x y" || q.Get("code_challenge_method") != "S256" || q.Get("access_type") != "offline" || len(q.Get("state")) < 40 {
			t.Errorf("consent %v", q)
		}
		if !strings.HasPrefix(cb, "http://127.0.0.1:") {
			t.Errorf("callback %s", cb)
		}
		return cb + "?code=c&state=" + url.QueryEscape(q.Get("state"))
	}, 5*time.Second)
	if err != nil || res.RefreshToken != "rt" || !page.OK || page.Email != "me@x.example" {
		t.Fatalf("%v %+v %+v", err, res, page)
	}
}

func TestAuthorizeRefusals(t *testing.T) {
	ok := tokenServer(t, true, 200)
	cases := map[string]struct {
		srv  *httptest.Server
		cb   func(consent, cb string) string
		want string
	}{
		"state":      {ok, func(c, cb string) string { return cb + "?code=c&state=wrong" }, MsgStateMismatch},
		"denied":     {ok, func(c, cb string) string { return cb + "?error=access_denied&state=" + url.QueryEscape(stateOf(c)) }, MsgDenied},
		"no code":    {ok, func(c, cb string) string { return cb + "?state=" + url.QueryEscape(stateOf(c)) }, MsgNoCode},
		"no refresh": {tokenServer(t, false, 200), func(c, cb string) string { return cb + "?code=c&state=" + url.QueryEscape(stateOf(c)) }, MsgNoRefreshToken},
		"refused":    {tokenServer(t, true, 400), func(c, cb string) string { return cb + "?code=c&state=" + url.QueryEscape(stateOf(c)) }, MsgCodeRefused},
	}
	for name, tc := range cases {
		_, err, page := run(t, tc.srv, tc.cb, 5*time.Second)
		var e *Error
		if !errors.As(err, &e) || e.Message != tc.want || page.OK || page.Message != tc.want {
			t.Errorf("%s: %v %+v", name, err, page)
		}
	}
	if _, err, _ := run(t, ok, func(string, string) string { return "" }, 150*time.Millisecond); err == nil || err.Error() != MsgTimeout {
		t.Errorf("timeout: %v", err)
	}
	if _, err := Authorize(context.Background(), Options{}); err == nil || err.Error() != MsgNoClient {
		t.Errorf("no client: %v", err)
	}
	if _, err := Authorize(context.Background(), Options{ClientID: "a", ClientSecret: "b"}); err == nil {
		t.Error("no Finish/Page accepted")
	}
}

func TestTokenEndpoint(t *testing.T) {
	ok := tokenServer(t, false, 200)
	u, _ := url.Parse(ok.URL + "/token")
	tok, exp, err := ExchangeToken(context.Background(), client(), u, url.Values{"grant_type": {"refresh_token"}}, "reauth", time.Unix(0, 0))
	if err != nil || tok != "at" || !exp.Equal(time.Unix(3600, 0)) {
		t.Fatalf("%v %s %s", err, tok, exp)
	}
	bad := tokenServer(t, false, 400)
	u, _ = url.Parse(bad.URL + "/token")
	_, _, err = ExchangeToken(context.Background(), client(), u, url.Values{}, "reauth now", time.Now())
	var ce *connector.Error
	if !errors.As(err, &ce) || ce.Code != connector.CodeUpstreamAuthFailed || ce.AgentMessage != "reauth now" {
		t.Fatalf("%v", err)
	}
	if s, err := RandomString(32); err != nil || len(s) != 43 {
		t.Fatalf("%q", s)
	}
}
