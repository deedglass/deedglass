package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

var logger = slog.Default

// session is one opened credential and an access token.
type session struct {
	st   *state
	inst *connector.Instance
	sec  *vault.Secret
	cli  saas.Client
}

func open(ctx context.Context, inst *connector.Instance) (*session, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	sec, err := inst.Secret(ctx)
	if err != nil {
		return nil, err
	}
	s := &session{st: st, inst: inst, sec: sec}
	s.cli = saas.Client{Base: st.base, HTTP: client(inst), Headers: http.Header{"Accept": {"application/json"}}, Classify: classify, MaxBody: maxBody}
	s.cli.Auth = func(ctx context.Context, r *http.Request) error {
		tok, err := accessToken(ctx, inst, st, sec)
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

// mailboxPath builds /gmail/v1/users/{mailbox}/<rest…> and checks it against the allow-list. A
// path that isn't on it is refused here, before any byte leaves, with an alert.
func (s *session) mailboxPath(method string, rest ...string) (string, error) {
	parts := append([]string{"gmail", "v1", "users", s.st.cfg.Mailbox}, rest...)
	p, err := saas.Segments(parts...)
	if err == nil && Allowed(method, p) {
		return p, nil
	}
	logger().ErrorContext(context.Background(), "gmail: a request outside the allow-list was refused before sending", "alert", "gmail_endpoint_refused",
		"connector", s.inst.Name, "method", method)
	return "", &connector.Error{Kind: connector.KindInvalidRequest, Code: "internal_error",
		AgentMessage: "Deedglass refused to send that request to Gmail. Nothing was sent. This connector writes drafts only."}
}

func (s *session) call(ctx context.Context, method string, q url.Values, body any, out any, rest ...string) (*saas.Response, error) {
	p, err := s.mailboxPath(method, rest...)
	if err != nil {
		return nil, err
	}
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

// classify maps Gmail's answers.
func classify(r *saas.Response) *connector.Error {
	if r.Status < 400 {
		return nil
	}
	var b struct {
		Error struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &b)
	switch r.Status {
	case http.StatusUnauthorized:
		return &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed, HTTPStatus: r.Status, AgentMessage: MsgReauth}
	case http.StatusForbidden:
		return &connector.Error{Kind: connector.KindAuth, Code: connector.CodeUpstreamAuthFailed, HTTPStatus: r.Status,
			AgentMessage: "Gmail refused this with the connector's scopes. Nothing was changed. An admin can authorize again with the scopes it needs."}
	case http.StatusNotFound:
		return &connector.Error{Kind: connector.KindNotFound, Code: connector.CodeUpstreamNotFound, HTTPStatus: r.Status,
			AgentMessage: "Gmail has nothing by that id in this mailbox. Check the id."}
	case http.StatusTooManyRequests:
		return &connector.Error{Kind: connector.KindRateLimited, Code: connector.CodeUpstreamRateLimited, HTTPStatus: r.Status, Retryable: true,
			RetryAfter: r.RetryAfter(time.Minute), AgentMessage: "Gmail is limiting requests. Wait, then send the call again."}
	case http.StatusBadRequest:
		return &connector.Error{Kind: connector.KindToolError, Code: connector.CodeUpstreamError, HTTPStatus: r.Status,
			AgentMessage: "Gmail refused the draft. Nothing was changed. Check the arguments."}
	}
	return connector.FromStatus(r.Status, r.RetryAfter(time.Minute))
}
