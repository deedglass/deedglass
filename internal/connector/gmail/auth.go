package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

// TokenMargin is how much life an access token must have left to be reused.
const TokenMargin = 5 * time.Minute

// Messages (S30 §5).
const (
	MsgReauth = "Google revoked or expired this connection. Apps in Testing get 7-day refresh tokens. Run deedglass connector gmail authorize gmail again."
	// ReasonNeedsReauth is health.reason when Google refused the refresh token.
	ReasonNeedsReauth = "needs_reauth"
)

// serviceAccount is the parts of a service-account JSON key Deedglass uses.
type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// accessToken returns a fresh access token from the cache, minting one with the stored credential
// (a refresh token, or a service-account JWT signed with SignRS256).
func accessToken(ctx context.Context, inst *connector.Instance, st *state, sec *vault.Secret) (*vault.Secret, error) {
	key := inst.ID.String() + "|" + inst.ConfigHash
	return tokens.Get(ctx, key, TokenMargin, func(ctx context.Context) (*vault.Secret, time.Time, error) {
		form := url.Values{}
		tokenURL := st.token
		switch st.cfg.Auth {
		case "service_account":
			var sa serviceAccount
			if err := json.Unmarshal([]byte(sec.Field(FieldServiceAccountJSON)), &sa); err != nil || sa.ClientEmail == "" || sa.PrivateKey == "" {
				return nil, time.Time{}, credError(inst, "The service_account_json field isn't a Google service-account key.")
			}
			pk, err := saas.ParseRSAPrivateKey([]byte(sa.PrivateKey))
			if err != nil {
				return nil, time.Time{}, credError(inst, "The service account's private key can't be read.")
			}
			aud := st.cfg.TokenURL
			if sa.TokenURI != "" && st.cfg.TokenURL == DefaultTokenURL {
				if sa.TokenURI != DefaultTokenURL {
					return nil, time.Time{}, credError(inst, "The service account's token_uri isn't Google's token endpoint.")
				}
				aud = sa.TokenURI
			}
			iat := now().Unix()
			jwt, err := saas.SignRS256(pk, map[string]any{"iss": sa.ClientEmail, "scope": ScopeCompose, "aud": aud, "iat": iat, "exp": iat + 3600, "sub": st.cfg.Mailbox})
			if err != nil {
				return nil, time.Time{}, credError(inst, "The service-account token can't be signed.")
			}
			form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
			form.Set("assertion", jwt)
		default:
			id, secret, rt := sec.Field(FieldClientID), sec.Field(FieldClientSecret), sec.Field(FieldRefreshToken)
			if id == "" || secret == "" || rt == "" {
				return nil, time.Time{}, credError(inst, "The credential needs client_id, client_secret and refresh_token.")
			}
			form.Set("grant_type", "refresh_token")
			form.Set("client_id", id)
			form.Set("client_secret", secret)
			form.Set("refresh_token", rt)
		}
		tok, exp, err := ExchangeToken(ctx, client(inst), tokenURL, form)
		if err != nil {
			return nil, time.Time{}, err
		}
		return vault.NewSecretForTest(vault.KindBearer, map[string]string{"token": tok}), exp, nil
	})
}

// TokenAnswer is Google's token endpoint answer.
type TokenAnswer struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

// PostToken posts form to the token endpoint and returns Google's answer. invalid_grant is
// upstream_auth_failed with MsgReauth.
func PostToken(ctx context.Context, hc *http.Client, tokenURL *url.URL, form url.Values) (TokenAnswer, error) {
	cli := saas.Client{Base: &url.URL{Scheme: tokenURL.Scheme, Host: tokenURL.Host}, HTTP: hc, Headers: http.Header{"Accept": {"application/json"}},
		Classify: func(r *saas.Response) *connector.Error {
			if r.Status < 400 {
				return nil
			}
			var b TokenAnswer
			_ = json.Unmarshal(r.Body, &b)
			if b.Error == "invalid_grant" || r.Status == http.StatusUnauthorized {
				return &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed, AgentMessage: MsgReauth,
					AdminDetail: "Google answered invalid_grant. health.reason " + ReasonNeedsReauth + ". " + MsgReauth}
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
			AgentMessage: "Google answered the token request with something Deedglass couldn't use. An admin can run deedglass connector test gmail."}
	}
	return b, nil
}

// ExchangeToken is PostToken returning the access token and its expiry.
func ExchangeToken(ctx context.Context, hc *http.Client, tokenURL *url.URL, form url.Values) (string, time.Time, error) {
	b, err := PostToken(ctx, hc, tokenURL, form)
	if err != nil {
		return "", time.Time{}, err
	}
	exp := b.ExpiresIn
	if exp <= 0 || exp > 86400 {
		exp = 3600
	}
	return b.AccessToken, now().Add(time.Duration(exp) * time.Second), nil
}

func credError(inst *connector.Instance, why string) *connector.Error {
	return &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable,
		AgentMessage: fmt.Sprintf(`The connector "%s" holds a Gmail credential Deedglass can't use, so nothing was sent. An admin can run deedglass connector test %s.`, inst.Name, inst.Name),
		AdminDetail:  why + " Run deedglass connector gmail authorize " + inst.Name + " again."}
}
