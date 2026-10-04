package gmail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Authorize defaults (S30 §3.2).
const (
	DefaultAuthURL   = "https://accounts.google.com/o/oauth2/v2/auth"
	AuthorizeTimeout = 5 * time.Minute
)

// Messages of the authorize flow.
const (
	MsgNoRefreshToken = "Google didn't return a refresh token. Remove Deedglass at myaccount.google.com › Security › Third-party access, then authorize again."
	MsgStateMismatch  = "The answer from Google didn't match this sign-in, so Deedglass ignored it. Start again."
	MsgTimeout        = "Nobody finished signing in within 5 minutes."
	MsgDenied         = "Google says access wasn't granted."
)

// ClientFile is a Desktop-app OAuth client JSON from Google Cloud.
type ClientFile struct {
	Installed struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	} `json:"installed"`
}

// AuthorizeOptions configure Authorize. HTTP must be an egress client.
type AuthorizeOptions struct {
	ClientID, ClientSecret string
	Readonly               bool
	Name                   string // the connector's name, for the page
	AuthURL                string
	TokenURL               string
	APIBaseURL             string
	HTTP                   *http.Client
	Timeout                time.Duration
	// OnURL is called with the consent URL and the loopback callback URL once the listener is up
	// (print it, and open a browser).
	OnURL func(consent, callback string)
}

// AuthorizeResult is what a finished sign-in gives. RefreshToken is a secret: the caller stores it
// through the vault and forgets it.
type AuthorizeResult struct {
	Email        string
	RefreshToken string
	Scopes       []string
	AuthorizedAt time.Time
}

// AuthorizeError is a refused sign-in: nothing is stored.
type AuthorizeError struct{ Message string }

func (e *AuthorizeError) Error() string { return e.Message }

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Authorize runs the OAuth sign-in: a PKCE S256 verifier (64 random bytes), a 32-byte state, a
// listener on 127.0.0.1 only, the consent URL, the callback (state compared in constant time),
// the code exchange with the verifier, and the profile read for the email address. It gives up
// after Timeout (5 minutes).
func Authorize(ctx context.Context, o AuthorizeOptions) (AuthorizeResult, error) {
	if o.AuthURL == "" {
		o.AuthURL = DefaultAuthURL
	}
	if o.TokenURL == "" {
		o.TokenURL = DefaultTokenURL
	}
	if o.APIBaseURL == "" {
		o.APIBaseURL = DefaultAPIBaseURL
	}
	if o.Timeout <= 0 {
		o.Timeout = AuthorizeTimeout
	}
	if o.ClientID == "" || o.ClientSecret == "" {
		return AuthorizeResult{}, &AuthorizeError{"The client file has no installed.client_id and client_secret. Download the JSON of a Desktop app OAuth client."}
	}
	verifier, err := randomString(64)
	if err != nil {
		return AuthorizeResult{}, err
	}
	state, err := randomString(32)
	if err != nil {
		return AuthorizeResult{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return AuthorizeResult{}, err
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	scopes := []string{ScopeCompose}
	if o.Readonly {
		scopes = append(scopes, ScopeReadonly)
	}
	q := url.Values{"client_id": {o.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {strings.Join(scopes, " ")},
		"access_type": {"offline"}, "prompt": {"consent"}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}}
	consent := o.AuthURL + "?" + q.Encode()

	type outcome struct {
		res AuthorizeResult
		err error
	}
	done := make(chan outcome, 1)
	finish := func(w http.ResponseWriter, oc outcome) {
		d := PageData{OK: oc.err == nil, Name: nonEmpty(o.Name, "gmail")}
		if oc.err == nil {
			d.Title, d.Email = "Gmail is connected", oc.res.Email
		} else {
			d.Title, d.Message = "Gmail isn't connected", oc.err.Error()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if oc.err != nil {
			w.WriteHeader(http.StatusBadRequest)
		}
		_ = WritePage(w, d)
		select {
		case done <- oc:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		cq := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(cq.Get("state")), []byte(state)) != 1 {
			finish(w, outcome{err: &AuthorizeError{MsgStateMismatch}})
			return
		}
		if e := cq.Get("error"); e != "" {
			finish(w, outcome{err: &AuthorizeError{MsgDenied}})
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
		return AuthorizeResult{}, &AuthorizeError{MsgTimeout}
	case <-ctx.Done():
		return AuthorizeResult{}, ctx.Err()
	}
}

func exchange(ctx context.Context, o AuthorizeOptions, code, verifier, redirect string) (AuthorizeResult, error) {
	if code == "" || len(code) > 2048 {
		return AuthorizeResult{}, &AuthorizeError{"Google's answer had no code. Start again."}
	}
	tu, err := url.Parse(o.TokenURL)
	if err != nil {
		return AuthorizeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ans, err := PostToken(ctx, o.HTTP, tu, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier},
		"client_id": {o.ClientID}, "client_secret": {o.ClientSecret}, "redirect_uri": {redirect}})
	if err != nil {
		var ce *connector.Error
		if errors.As(err, &ce) {
			return AuthorizeResult{}, &AuthorizeError{"Google refused the sign-in code. Start again."}
		}
		return AuthorizeResult{}, &AuthorizeError{"Deedglass couldn't reach Google to finish the sign-in. Check the network, then start again."}
	}
	if ans.RefreshToken == "" {
		return AuthorizeResult{}, &AuthorizeError{MsgNoRefreshToken}
	}
	base, err := url.Parse(o.APIBaseURL)
	if err != nil {
		return AuthorizeResult{}, err
	}
	path := saas.MustSegments("gmail", "v1", "users", "me", "profile")
	if !Allowed(http.MethodGet, path) {
		return AuthorizeResult{}, errors.New("gmail: the profile path left the allow-list")
	}
	tok := ans.AccessToken
	cli := saas.Client{Base: base, HTTP: o.HTTP, Headers: http.Header{"Accept": {"application/json"}}, Classify: classify,
		Auth: func(_ context.Context, r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+tok)
			return nil
		}}
	resp, err := cli.Do(ctx, saas.Request{Method: http.MethodGet, Path: path})
	if err != nil {
		return AuthorizeResult{}, &AuthorizeError{"Google signed you in, but the Gmail profile couldn't be read. Check that the Gmail API is enabled in the project, then start again."}
	}
	var p struct {
		EmailAddress string `json:"emailAddress"`
	}
	if json.Unmarshal(resp.Body, &p) != nil || p.EmailAddress == "" {
		return AuthorizeResult{}, &AuthorizeError{"The Gmail profile had no email address."}
	}
	sc := strings.Fields(ans.Scope)
	if len(sc) == 0 {
		sc = []string{ScopeCompose}
	}
	return AuthorizeResult{Email: strings.ToLower(p.EmailAddress), RefreshToken: ans.RefreshToken, Scopes: sc, AuthorizedAt: now().UTC()}, nil
}

func nonEmpty(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
