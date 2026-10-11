//go:build outboundmail

package outbound

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/googleauth"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

// TokenMargin is how much life an access token must have left to be reused.
const TokenMargin = 5 * time.Minute

// Messages.
const (
	MsgReauth = "Google revoked or expired the mail connector's sign-in, so nothing was sent. Run deedglass connector outbound authorize mail again."
	// ReasonNeedsReauth is health.reason when Google refused the refresh token.
	ReasonNeedsReauth = googleauth.ReasonNeedsReauth
)

// session is one opened credential and an access token.
type session struct {
	st   *state
	inst *connector.Instance
	sec  *vault.Secret
	cli  saas.Client
}

func (c *Connector) open(ctx context.Context, inst *connector.Instance) (*session, error) {
	st, err := c.parse(inst)
	if err != nil {
		return nil, err
	}
	if st.tripped.Load() {
		return nil, disabledErr(inst)
	}
	sec, err := inst.Secret(ctx)
	if err != nil {
		return nil, err
	}
	s := &session{st: st, inst: inst, sec: sec}
	s.cli = saas.Client{Base: st.base, HTTP: client(inst), Headers: http.Header{"Accept": {"application/json"}}, Classify: classify, MaxBody: maxBody}
	s.cli.Auth = func(ctx context.Context, r *http.Request) error {
		tok, err := s.accessToken(ctx)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+tok.Field("token"))
		return nil
	}
	return s, nil
}

func (s *session) close() {
	if s != nil && s.sec != nil {
		s.sec.Destroy()
	}
}

func (s *session) accessToken(ctx context.Context) (*vault.Secret, error) {
	key := s.inst.ID.String() + "|" + s.inst.ConfigHash
	return tokens.Get(ctx, key, TokenMargin, func(ctx context.Context) (*vault.Secret, time.Time, error) {
		id, secret, rt := s.sec.Field(FieldClientID), s.sec.Field(FieldClientSecret), s.sec.Field(FieldRefreshToken)
		if id == "" || secret == "" || rt == "" {
			return nil, time.Time{}, &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable,
				AgentMessage: fmt.Sprintf(`The connector "%s" has no usable Google sign-in, so nothing was sent. Run deedglass connector outbound authorize %s.`, s.inst.Name, s.inst.Name)}
		}
		form := url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "client_secret": {secret}, "refresh_token": {rt}}
		tok, exp, err := googleauth.ExchangeToken(ctx, client(s.inst), s.st.token, form, MsgReauth, now())
		if err != nil {
			return nil, time.Time{}, err
		}
		return vault.NewSecretForTest(vault.KindBearer, map[string]string{"token": tok}), exp, nil
	})
}

// path builds /gmail/v1/users/me/<rest…> and checks it against the allow-list. A path that isn't
// on it is refused here, before any byte leaves, with an alert.
func (s *session) path(method string, rest ...string) (string, error) {
	parts := append([]string{"gmail", "v1", "users", "me"}, rest...)
	p, err := saas.Segments(parts...)
	if err == nil && Allowed(method, p) {
		return p, nil
	}
	logger().ErrorContext(context.Background(), "outbound: a request outside the allow-list was refused before sending", "alert", "outbound_endpoint_refused",
		"connector", s.inst.Name, "method", method)
	return "", &connector.Error{Kind: connector.KindInvalidRequest, Code: "internal_error",
		AgentMessage: "Deedglass refused to send that request to Gmail. Nothing was sent."}
}

func (s *session) call(ctx context.Context, method string, q url.Values, body any, out any, rest ...string) (*saas.Response, error) {
	p, err := s.path(method, rest...)
	if err != nil {
		return nil, err
	}
	// Idempotent only for reads: a send is never retried (Gmail's send has no idempotency key).
	resp, err := s.cli.Do(ctx, saas.Request{Method: method, Path: p, Query: q, JSON: body, Idempotent: method == http.MethodGet})
	if err != nil {
		return resp, err
	}
	if out != nil {
		if err := json.NewDecoder(bytes.NewReader(resp.Body)).Decode(out); err != nil {
			return resp, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, HTTPStatus: resp.Status,
				AgentMessage: "Gmail answered with something Deedglass couldn't read. The call may have run.", Err: err}
		}
	}
	return resp, nil
}

// classify maps Gmail's answers. 400, 403 and 404 certainly didn't send (failed); 429 is failed
// upstream_rate_limited; 401 is the reauth message; 5xx is unknown (it may have sent).
func classify(r *saas.Response) *connector.Error {
	if r.Status < 400 {
		return nil
	}
	switch r.Status {
	case http.StatusUnauthorized:
		return &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed, HTTPStatus: r.Status, AgentMessage: MsgReauth}
	case http.StatusForbidden:
		return &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed, HTTPStatus: r.Status,
			AgentMessage: "Gmail refused this with the connector's scopes. Nothing was sent. An admin can authorize again with gmail.send and gmail.readonly."}
	case http.StatusNotFound:
		return &connector.Error{Kind: connector.KindNotFound, Code: connector.CodeUpstreamNotFound, HTTPStatus: r.Status,
			AgentMessage: "Gmail has nothing by that id in this mailbox. Check the id."}
	case http.StatusTooManyRequests:
		return &connector.Error{Kind: connector.KindRateLimited, Code: connector.CodeUpstreamRateLimited, HTTPStatus: r.Status,
			RetryAfter: r.RetryAfter(time.Minute), AgentMessage: "Gmail is limiting this mailbox. Nothing was sent; the sender plans it for another window."}
	case http.StatusBadRequest:
		return &connector.Error{Kind: connector.KindToolError, Code: connector.CodeUpstreamError, HTTPStatus: r.Status,
			AgentMessage: "Gmail refused the message. Nothing was sent. Check the arguments."}
	}
	return &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, HTTPStatus: r.Status,
		AgentMessage: "Gmail answered with an error after the message was sent to it, so it may have left. The read-back looks for it by its Message-ID; it is never sent again."}
}
