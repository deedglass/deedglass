// Package googleauth is Google's OAuth for installed apps, shared by the Gmail connector (S30) and
// the ops build's outbound-mail connector (S40): a PKCE S256 sign-in through a loopback redirect on
// 127.0.0.1, and the token endpoint (code exchange and refresh). It holds no scope and no product
// copy of its own beyond the sign-in's refusals: each connector names its scopes, reads its profile
// and renders its own callback page. Moved out of internal/connector/gmail in S40 with no change in
// behaviour.
package googleauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Google's endpoints and the sign-in's time limit.
const (
	DefaultAuthURL   = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultTokenURL  = "https://oauth2.googleapis.com/token"
	AuthorizeTimeout = 5 * time.Minute
)

// Refusals of the sign-in (S30 §5).
const (
	MsgNoRefreshToken = "Google didn't return a refresh token. Remove Deedglass at myaccount.google.com › Security › Third-party access, then authorize again."
	MsgStateMismatch  = "The answer from Google didn't match this sign-in, so Deedglass ignored it. Start again."
	MsgTimeout        = "Nobody finished signing in within 5 minutes."
	MsgDenied         = "Google says access wasn't granted."
	MsgNoClient       = "The client file has no installed.client_id and client_secret. Download the JSON of a Desktop app OAuth client."
	MsgNoCode         = "Google's answer had no code. Start again."
	MsgCodeRefused    = "Google refused the sign-in code. Start again."
	MsgUnreachable    = "Deedglass couldn't reach Google to finish the sign-in. Check the network, then start again."
)

// ClientFile is a Desktop-app OAuth client JSON from Google Cloud.
type ClientFile struct {
	Installed struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	} `json:"installed"`
}

// Error is a refused sign-in: nothing is stored.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// Outcome is what the callback page shows.
type Outcome struct {
	OK      bool
	Email   string // on success
	Message string // on failure
}

// Options configure Authorize. HTTP must be an egress client.
type Options struct {
	ClientID, ClientSecret string
	Scopes                 []string // full scope URLs, in the order they are asked for
	AuthURL                string
	TokenURL               string
	HTTP                   *http.Client
	Timeout                time.Duration
	// OnURL is called with the consent URL and the loopback callback URL once the listener is up.
	OnURL func(consent, callback string)
	// Finish runs after the code exchange with Google's answer (a refresh token is present): the
	// connector reads its profile and checks it. Its error is the sign-in's error.
	Finish func(ctx context.Context, ans TokenAnswer) (Result, error)
	// Page renders the callback page for an outcome (headers are already set).
	Page func(w http.ResponseWriter, o Outcome)
}

// Result is a finished sign-in. RefreshToken is a secret: the caller stores it through the vault
// and forgets it.
type Result struct {
	Email        string
	RefreshToken string
	Scopes       []string
	AuthorizedAt time.Time
}

// RandomString is n random bytes, base64url without padding.
func RandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Authorize runs the OAuth sign-in: a PKCE S256 verifier (64 random bytes), a 32-byte state, a
// listener on 127.0.0.1 only, the consent URL, the callback (state compared in constant time),
// the code exchange with the verifier, then Finish. It gives up after Timeout (5 minutes).
func Authorize(ctx context.Context, o Options) (Result, error) {
	if o.AuthURL == "" {
		o.AuthURL = DefaultAuthURL
	}
	if o.TokenURL == "" {
		o.TokenURL = DefaultTokenURL
	}
	if o.Timeout <= 0 {
		o.Timeout = AuthorizeTimeout
	}
	if o.ClientID == "" || o.ClientSecret == "" {
		return Result{}, &Error{MsgNoClient}
	}
	if o.Finish == nil || o.Page == nil {
		return Result{}, fmt.Errorf("googleauth: Finish and Page are required")
	}
	verifier, err := RandomString(64)
	if err != nil {
		return Result{}, err
	}
	state, err := RandomString(32)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Result{}, err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	q := url.Values{"client_id": {o.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {strings.Join(o.Scopes, " ")},
		"access_type": {"offline"}, "prompt": {"consent"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}}
	consent := o.AuthURL + "?" + q.Encode()

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	finish := func(w http.ResponseWriter, oc outcome) {
		po := Outcome{OK: oc.err == nil}
		if oc.err == nil {
			po.Email = oc.res.Email
		} else {
			po.Message = oc.err.Error()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if oc.err != nil {
			w.WriteHeader(http.StatusBadRequest)
		}
		o.Page(w, po)
		select {
		case done <- oc:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		cq := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(cq.Get("state")), []byte(state)) != 1 {
			finish(w, outcome{err: &Error{MsgStateMismatch}})
			return
		}
		if e := cq.Get("error"); e != "" {
			finish(w, outcome{err: &Error{MsgDenied}})
			return
		}
		res, err := exchange(r.Context(), o, cq.Get("code"), verifier, redirect)
		finish(w, outcome{res: res, err: err})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if o.OnURL != nil {
		o.OnURL(consent, redirect)
	}
	timer := time.NewTimer(o.Timeout)
	defer timer.Stop()
	select {
	case oc := <-done:
		return oc.res, oc.err
	case <-timer.C:
		return Result{}, &Error{MsgTimeout}
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func exchange(ctx context.Context, o Options, code, verifier, redirect string) (Result, error) {
	if code == "" || len(code) > 2048 {
		return Result{}, &Error{MsgNoCode}
	}
	tu, err := url.Parse(o.TokenURL)
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ans, err := PostToken(ctx, o.HTTP, tu, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"client_id": {o.ClientID}, "client_secret": {o.ClientSecret}, "redirect_uri": {redirect}}, "")
	if err != nil {
		if _, ok := err.(*connector.Error); ok {
			return Result{}, &Error{MsgCodeRefused}
		}
		return Result{}, &Error{MsgUnreachable}
	}
	if ans.RefreshToken == "" {
		return Result{}, &Error{MsgNoRefreshToken}
	}
	return o.Finish(ctx, ans)
}

// TokenAnswer is Google's token endpoint answer.
type TokenAnswer struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

// ReasonNeedsReauth is health.reason when Google refused the refresh token.
const ReasonNeedsReauth = "needs_reauth"

// PostToken posts form to the token endpoint and returns Google's answer. invalid_grant (or a 401)
// is upstream_auth_failed with reauth as the agent's message.
func PostToken(ctx context.Context, hc *http.Client, tokenURL *url.URL, form url.Values, reauth string) (TokenAnswer, error) {
	cli := saas.Client{Base: &url.URL{Scheme: tokenURL.Scheme, Host: tokenURL.Host}, HTTP: hc, Headers: http.Header{"Accept": {"application/json"}},
		Classify: func(r *saas.Response) *connector.Error {
			if r.Status < 400 {
				return nil
			}
			var b TokenAnswer
			_ = json.Unmarshal(r.Body, &b)
			if b.Error == "invalid_grant" || r.Status == http.StatusUnauthorized {
				return &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed, AgentMessage: reauth,
					AdminDetail: "Google answered invalid_grant. health.reason " + ReasonNeedsReauth + ". " + reauth}
			}
			return connector.FromStatus(r.Status, r.RetryAfter(time.Minute))
		}}
	segs := strings.Split(strings.Trim(tokenURL.Path, "/"), "/")
	path, err := saas.Segments(segs...)
	if err != nil {
		return TokenAnswer{}, err
	}
	resp, err := cli.Do(ctx, saas.Request{Method: http.MethodPost, Path: path, Form: form})
	if err != nil {
		return TokenAnswer{}, err
	}
	var b TokenAnswer
	if err := json.Unmarshal(resp.Body, &b); err != nil || b.AccessToken == "" {
		return TokenAnswer{}, &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed,
			AgentMessage: "Google answered the token request with something Deedglass couldn't use. An admin can run deedglass connector test on the connector."}
	}
	return b, nil
}

// ExchangeToken is PostToken returning the access token and its expiry from now.
func ExchangeToken(ctx context.Context, hc *http.Client, tokenURL *url.URL, form url.Values, reauth string, now time.Time) (string, time.Time, error) {
	b, err := PostToken(ctx, hc, tokenURL, form, reauth)
	if err != nil {
		return "", time.Time{}, err
	}
	exp := b.ExpiresIn
	if exp <= 0 || exp > 86400 {
		exp = 3600
	}
	return b.AccessToken, now.Add(time.Duration(exp) * time.Second), nil
}
