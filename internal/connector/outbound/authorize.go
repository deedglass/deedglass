//go:build outboundmail

package outbound

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector/gmail"
	"github.com/deedglass/deedglass/internal/connector/googleauth"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// AuthorizeOptions configure Authorize. HTTP must be an egress client.
type AuthorizeOptions struct {
	ClientID, ClientSecret string
	Name                   string // the connector's name, for the page
	Mailbox                string // the config's mailbox: the profile must match it
	Deny                   []string
	AuthURL                string
	TokenURL               string
	APIBaseURL             string
	HTTP                   *http.Client
	Timeout                time.Duration
	OnURL                  func(consent, callback string)
}

// Authorize runs the Google sign-in for exactly gmail.send and gmail.readonly, then reads the
// profile and refuses an address other than the mailbox or on a denied domain. Nothing is stored
// on a refusal.
func Authorize(ctx context.Context, o AuthorizeOptions) (googleauth.Result, error) {
	if o.APIBaseURL == "" {
		o.APIBaseURL = DefaultAPIBase
	}
	if o.TokenURL == "" {
		o.TokenURL = DefaultTokenURL
	}
	deny := append(append([]string{}, AlwaysDenied...), o.Deny...)
	if DeniedDomain(o.Mailbox, deny) {
		return googleauth.Result{}, &googleauth.Error{Message: MsgFromDenied}
	}
	return googleauth.Authorize(ctx, googleauth.Options{ClientID: o.ClientID, ClientSecret: o.ClientSecret, Scopes: []string{ScopeSend, ScopeReadonly},
		AuthURL: o.AuthURL, TokenURL: o.TokenURL, HTTP: o.HTTP, Timeout: o.Timeout, OnURL: o.OnURL,
		Finish: func(ctx context.Context, ans googleauth.TokenAnswer) (googleauth.Result, error) {
			email, err := profile(ctx, o, ans.AccessToken)
			if err != nil {
				return googleauth.Result{}, err
			}
			if DeniedDomain(email, deny) {
				return googleauth.Result{}, &googleauth.Error{Message: MsgFromDenied}
			}
			if email != strings.ToLower(o.Mailbox) {
				return googleauth.Result{}, &googleauth.Error{Message: "You signed in as " + email + ", but this connector sends from " + o.Mailbox + ". Sign in as " + o.Mailbox + ", then start again."}
			}
			sc := strings.Fields(ans.Scope)
			if len(sc) == 0 {
				sc = []string{ScopeSend, ScopeReadonly}
			}
			return googleauth.Result{Email: email, RefreshToken: ans.RefreshToken, Scopes: sc, AuthorizedAt: now().UTC()}, nil
		},
		Page: func(w http.ResponseWriter, oc googleauth.Outcome) {
			d := gmail.PageData{OK: oc.OK, Name: o.Name}
			if oc.OK {
				d.Title, d.Email = "Outbound mail is connected", oc.Email
			} else {
				d.Title, d.Message = "Outbound mail isn't connected", oc.Message
			}
			_ = gmail.WritePage(w, d)
		}})
}

func profile(ctx context.Context, o AuthorizeOptions, tok string) (string, error) {
	base, err := url.Parse(o.APIBaseURL)
	if err != nil {
		return "", err
	}
	path := saas.MustSegments("gmail", "v1", "users", "me", "profile")
	if !Allowed(http.MethodGet, path) {
		return "", errNoProfile
	}
	cli := saas.Client{Base: base, HTTP: o.HTTP, Headers: http.Header{"Accept": {"application/json"}}, Classify: classify,
		Auth: func(_ context.Context, r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+tok)
			return nil
		}}
	resp, err := cli.Do(ctx, saas.Request{Method: http.MethodGet, Path: path})
	if err != nil {
		return "", &googleauth.Error{Message: "Google signed you in, but the Gmail profile couldn't be read. Check that the Gmail API is enabled in the project, then start again."}
	}
	var p struct {
		EmailAddress string `json:"emailAddress"`
	}
	if json.Unmarshal(resp.Body, &p) != nil || p.EmailAddress == "" {
		return "", &googleauth.Error{Message: "The Gmail profile had no email address."}
	}
	return strings.ToLower(p.EmailAddress), nil
}
