package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector/googleauth"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Authorize defaults (S30 §3.2). The sign-in itself lives in internal/connector/googleauth (S40),
// shared with the ops build's outbound-mail connector; this file keeps the Gmail scopes, profile
// read and page.
const (
	DefaultAuthURL   = googleauth.DefaultAuthURL
	AuthorizeTimeout = googleauth.AuthorizeTimeout
)

// Messages of the authorize flow.
const (
	MsgNoRefreshToken = googleauth.MsgNoRefreshToken
	MsgStateMismatch  = googleauth.MsgStateMismatch
	MsgTimeout        = googleauth.MsgTimeout
	MsgDenied         = googleauth.MsgDenied
)

// ClientFile is a Desktop-app OAuth client JSON from Google Cloud.
type ClientFile = googleauth.ClientFile

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
type AuthorizeResult = googleauth.Result

// AuthorizeError is a refused sign-in: nothing is stored.
type AuthorizeError = googleauth.Error

// Authorize runs the OAuth sign-in (googleauth.Authorize: PKCE S256, 32-byte state, a listener on
// 127.0.0.1 only, 5 minutes) for gmail.compose, plus gmail.readonly with Readonly, then reads the
// Gmail profile for the email address.
func Authorize(ctx context.Context, o AuthorizeOptions) (AuthorizeResult, error) {
	if o.TokenURL == "" {
		o.TokenURL = DefaultTokenURL
	}
	if o.APIBaseURL == "" {
		o.APIBaseURL = DefaultAPIBaseURL
	}
	scopes := []string{ScopeCompose}
	if o.Readonly {
		scopes = append(scopes, ScopeReadonly)
	}
	name := nonEmpty(o.Name, "gmail")
	return googleauth.Authorize(ctx, googleauth.Options{ClientID: o.ClientID, ClientSecret: o.ClientSecret, Scopes: scopes,
		AuthURL: o.AuthURL, TokenURL: o.TokenURL, HTTP: o.HTTP, Timeout: o.Timeout, OnURL: o.OnURL,
		Finish: func(ctx context.Context, ans googleauth.TokenAnswer) (googleauth.Result, error) {
			return profile(ctx, o, ans)
		},
		Page: func(w http.ResponseWriter, oc googleauth.Outcome) {
			d := PageData{OK: oc.OK, Name: name}
			if oc.OK {
				d.Title, d.Email = "Gmail is connected", oc.Email
			} else {
				d.Title, d.Message = "Gmail isn't connected", oc.Message
			}
			_ = WritePage(w, d)
		}})
}

// profile reads the Gmail profile with the fresh access token.
func profile(ctx context.Context, o AuthorizeOptions, ans googleauth.TokenAnswer) (AuthorizeResult, error) {
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
		return AuthorizeResult{}, &AuthorizeError{Message: "Google signed you in, but the Gmail profile couldn't be read. Check that the Gmail API is enabled in the project, then start again."}
	}
	var p struct {
		EmailAddress string `json:"emailAddress"`
	}
	if json.Unmarshal(resp.Body, &p) != nil || p.EmailAddress == "" {
		return AuthorizeResult{}, &AuthorizeError{Message: "The Gmail profile had no email address."}
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
