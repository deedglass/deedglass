package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

// Breaker reasons (connector health.reason).
const (
	ReasonLivemode = "livemode_detected"
	ReasonLiveKey  = "live_key_stored"
)

// Messages of the breaker (S30 §5).
const (
	MsgDisabledAgent = "The stripe connector is disabled. An admin has been alerted."
	msgLivemodeAdmin = "Stripe answered in live mode for request %s. Deedglass disabled the %s connector. Check the request in your Stripe dashboard, then replace the key with a sandbox key."
	msgLiveKeyAdmin  = "The %s connector holds a key that isn't a Stripe sandbox key (%s). Deedglass disabled the connector and sent nothing. Store a restricted key that starts with rk_test_."
)

var (
	reRequestID = regexp.MustCompile(`^req_[A-Za-z0-9]{1,64}$`)
	reApreq     = regexp.MustCompile(`apreq_[A-Za-z0-9_]{4,80}`)
	reCode      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// stripeError is Stripe's error object.
type stripeError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

func (e *stripeError) Error() string { return "stripe: " + e.Type + "/" + e.Code }

// errorBody reads {"error": {…}} and, for approval_required, the approval request id wherever
// Stripe puts it (error.approval_request.id, error.approval_request as a string, or the message).
func errorBody(b []byte) (stripeError, string) {
	var w struct {
		Error struct {
			stripeError
			ApprovalRequest json.RawMessage `json:"approval_request"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &w)
	se := w.Error.stripeError
	if !reCode.MatchString(se.Code) {
		se.Code = ""
	}
	if !reCode.MatchString(se.Type) {
		se.Type = ""
	}
	apreq := ""
	if m := reApreq.Find(w.Error.ApprovalRequest); m != nil {
		apreq = string(m)
	} else if m := reApreq.FindString(se.Message); m != "" {
		apreq = m
	}
	return se, apreq
}

// classify maps Stripe's answers to connector errors (S30 §3.1).
func classify(r *saas.Response) *connector.Error {
	if r.Status < 400 {
		return nil
	}
	se, apreq := errorBody(r.Body)
	reqID := requestID(r)
	e := &connector.Error{HTTPStatus: r.Status, Err: &se}
	switch {
	case se.Code == "approval_required" || se.Type == "approval_required":
		if apreq == "" {
			apreq = "apreq_…"
		}
		e.Kind, e.Code = connector.KindUpstreamPending, connector.CodeUpstreamApprovalPending
		e.AgentMessage = fmt.Sprintf("Stripe is holding this for its own approval (%s). Deedglass can't see whether a Stripe reviewer approves it.", apreq)
		e.AdminDetail = fmt.Sprintf("Stripe is holding this refund for its own approval (%s). Deedglass can't see whether a Stripe reviewer approves it. Check Settings › Approvals in Stripe. Request %s.", apreq, reqID)
	case se.Type == "idempotency_error":
		// Same key, different parameters: impossible by construction (the key is the action id).
		e.Kind, e.Code = connector.KindToolError, "internal_error"
		e.AgentMessage = "Deedglass sent Stripe a request it couldn't accept. Nothing was changed. This is a bug in Deedglass."
		e.AdminDetail = "Stripe answered idempotency_error for request " + reqID + ": the same Idempotency-Key with different parameters."
	case r.Status == http.StatusUnauthorized:
		e.Kind, e.Code = connector.KindAuth, connector.CodeUpstreamAuthFailed
		e.AgentMessage = "Stripe refused the connector's key. Nothing was changed. An admin can replace it in Console › Connectors."
		e.AdminDetail = "Stripe answered 401 for request " + reqID + ". Store a working sandbox key, then run deedglass connector test stripe."
	case r.Status == http.StatusForbidden:
		e.Kind, e.Code = connector.KindAuth, connector.CodeUpstreamAuthFailed
		e.AgentMessage = "The connector's Stripe key isn't allowed to do this. Nothing was changed. An admin can widen the restricted key's permissions."
		e.AdminDetail = "Stripe answered 403 for request " + reqID + ": " + clipMsg(se.Message)
	case r.Status == http.StatusNotFound:
		e.Kind, e.Code = connector.KindNotFound, connector.CodeUpstreamNotFound
		e.AgentMessage = "Stripe has no object by that id in this sandbox. Check the id."
	case r.Status == http.StatusTooManyRequests:
		e.Kind, e.Code, e.Retryable = connector.KindRateLimited, connector.CodeUpstreamRateLimited, true
		e.RetryAfter = r.RetryAfter(time.Minute)
		e.AgentMessage = "Stripe is limiting requests. Wait, then send the call again."
	case r.Status == http.StatusConflict:
		e.Kind, e.Code = connector.KindUpstream, connector.CodeUpstreamError
		e.AgentMessage = "Stripe was still working on an earlier attempt with the same key. The call may have run."
	case r.Status >= 500:
		e.Kind, e.Code = connector.KindUpstream, connector.CodeUpstreamError
		e.AgentMessage = "Stripe failed while answering. The call may have run."
	default: // 400, 402: Stripe refused it, so nothing was applied
		e.Kind, e.Code = connector.KindToolError, connector.CodeUpstreamError
		what := se.Code
		if what == "" {
			what = se.Type
		}
		e.AgentMessage = "Stripe refused the request"
		if what != "" {
			e.AgentMessage += " (" + what + ")"
		}
		e.AgentMessage += ". Nothing was changed. Check the arguments."
		e.AdminDetail = "Stripe answered " + http.StatusText(r.Status) + " for request " + reqID + ": " + clipMsg(se.Message)
	}
	return e
}

func clipMsg(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

func requestID(r *saas.Response) string {
	if r == nil {
		return "req_…"
	}
	if id := r.Header.Get("Request-Id"); reRequestID.MatchString(id) {
		return id
	}
	return "req_…"
}

// retryIf is Stripe's retry rule for idempotent requests: never when Stripe-Should-Retry is
// false; always when it is true; a 409 (an attempt with the same key still running) and a 5xx
// once.
func retryIf(r *saas.Response) (bool, time.Duration) {
	switch r.Header.Get("Stripe-Should-Retry") {
	case "false":
		return false, 0
	case "true":
		return true, 300 * time.Millisecond
	}
	if r.Status == http.StatusConflict || r.Status >= 500 {
		return true, 300 * time.Millisecond
	}
	if r.Status == http.StatusTooManyRequests {
		if d := r.RetryAfter(saas.MaxRetryWait); d > 0 {
			return true, d
		}
	}
	return false, 0
}

// Livemode reports whether any "livemode" member anywhere in body is true. A body that isn't
// JSON has none. The breaker reads it on every answer, error or not.
func Livemode(body []byte) bool {
	if !bytes.Contains(body, []byte("livemode")) {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		// Not JSON: fall back to the raw scan, so a malformed answer that names live mode still trips.
		return rawLivemode(body)
	}
	return walkLivemode(v, 0)
}

func walkLivemode(v any, depth int) bool {
	if depth > 64 {
		return true // deeper than Stripe ever nests: treat as suspicious
	}
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if k == "livemode" {
				if b, ok := e.(bool); ok && b {
					return true
				}
				if s, ok := e.(string); ok && s == "true" {
					return true
				}
			}
			if walkLivemode(e, depth+1) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if walkLivemode(e, depth+1) {
				return true
			}
		}
	}
	return false
}

var reRawLivemode = regexp.MustCompile(`"livemode"\s*:\s*"?true`)

func rawLivemode(b []byte) bool { return reRawLivemode.Match(b) }

// session is one opened key and a client that sends it.
type session struct {
	st   *state
	inst *connector.Instance
	sec  *vault.Secret
	cli  saas.Client
	log  *slog.Logger
	sent int // requests that left (tests count them)
}

var logger = slog.Default

// open opens the instance's key and checks it again: a stored key that isn't a sandbox key (an
// edited row, a restored backup) disables the connector and nothing is sent.
func open(ctx context.Context, inst *connector.Instance) (*session, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	if st.tripped.Load() {
		return nil, disabledErr(inst, "the breaker tripped earlier")
	}
	sec, err := inst.Secret(ctx)
	if err != nil {
		return nil, err
	}
	key := sec.Field(FieldToken)
	if code, _ := CheckKey(key); code != "" {
		p := Prefix(key)
		sec.Destroy()
		trip(ctx, inst, st, ReasonLiveKey, fmt.Sprintf(msgLiveKeyAdmin, inst.Name, p), "")
		e := &connector.Error{Kind: connector.KindConfig, Code: connector.CodeStripeLiveKeyRefused,
			AgentMessage: MsgDisabledAgent, AdminDetail: fmt.Sprintf(msgLiveKeyAdmin, inst.Name, p)}
		if code != connector.CodeStripeLiveKeyRefused {
			e.Code = connector.CodeConnectorUnavailable
		}
		return nil, e
	}
	s := &session{st: st, inst: inst, sec: sec, log: logger()}
	s.cli = saas.Client{Base: st.base, HTTP: client(inst),
		Headers:  http.Header{"Accept": {"application/json"}, "Stripe-Version": {st.cfg.APIVersion}},
		Classify: classify, IdempotencyHeader: "Idempotency-Key", MaxBody: maxBody, RetryIf: retryIf}
	s.cli.Auth = func(_ context.Context, r *http.Request) error {
		k := sec.Field(FieldToken)
		if code, _ := CheckKey(k); code != "" { // the last look before a byte leaves
			return disabledErr(inst, "the key isn't a sandbox key")
		}
		r.Header.Set("Authorization", "Bearer "+k)
		s.sent++
		return nil
	}
	return s, nil
}

func (s *session) close() {
	if s != nil && s.sec != nil {
		s.sec.Destroy()
	}
}

func disabledErr(inst *connector.Instance, why string) *connector.Error {
	return &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable, AgentMessage: MsgDisabledAgent,
		AdminDetail: fmt.Sprintf("The %s connector is disabled (%s). Run deedglass connector test %s after fixing the key.", inst.Name, why, inst.Name)}
}

// trip disables the connector: the instance refuses every further call at once, the breaker (the
// serve wiring) sets status disabled with health.reason and seals connector.disabled, and an
// alert line goes to the log. Safe to call twice.
func trip(ctx context.Context, inst *connector.Instance, st *state, reason, detail, reqID string) {
	first := st.tripped.CompareAndSwap(false, true)
	if !first {
		return
	}
	logger().ErrorContext(ctx, "stripe: the breaker disabled the connector; nothing more is sent", "alert", "stripe_"+reason,
		"connector", inst.Name, "connector_id", inst.ID.String(), "org_id", inst.OrgID.String(), "stripe_request_id", reqID)
	if inst.Env.Breaker == nil {
		return
	}
	bctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := inst.Env.Breaker.Trip(bctx, inst, reason, detail); err != nil {
		logger().ErrorContext(ctx, "stripe: recording the disabled connector failed; the instance still refuses every call",
			"alert", "stripe_"+reason, "connector", inst.Name, "err", err)
	}
}

// do sends one request and runs the breaker on the answer: a live-mode answer anywhere disables
// the connector and the call is unknown (something may have happened in live mode).
func (s *session) do(ctx context.Context, r saas.Request) (*saas.Response, error) {
	if s.st.tripped.Load() {
		return nil, disabledErr(s.inst, "the breaker tripped earlier")
	}
	resp, err := s.cli.Do(ctx, r)
	if resp != nil && Livemode(resp.Body) {
		reqID := requestID(resp)
		detail := fmt.Sprintf(msgLivemodeAdmin, reqID, s.inst.Name)
		trip(ctx, s.inst, s.st, ReasonLivemode, detail, reqID)
		return resp, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeStripeLivemodeDetected, HTTPStatus: resp.Status,
			AgentMessage: MsgDisabledAgent, AdminDetail: detail}
	}
	return resp, err
}

// get reads one object or list.
func (s *session) get(ctx context.Context, path string, q url.Values, out any) (*saas.Response, error) {
	resp, err := s.do(ctx, saas.Request{Method: http.MethodGet, Path: path, Query: q, Idempotent: true})
	return decode(resp, err, out)
}

// post sends a form with Idempotency-Key = key. A POST with a key is idempotent at Stripe, so the
// client may retry it once under Stripe's rules (retryIf).
func (s *session) post(ctx context.Context, path string, params map[string]any, key string, out any) (*saas.Response, error) {
	form, err := Form(params)
	if err != nil {
		return nil, &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeValidationFailed,
			AgentMessage: "The arguments can't be sent to Stripe.", Err: err}
	}
	resp, err := s.do(ctx, saas.Request{Method: http.MethodPost, Path: path, Form: form, IdempotencyKey: key, Idempotent: key != ""})
	return decode(resp, err, out)
}

// del sends a DELETE (idempotent by nature at Stripe; no Idempotency-Key).
func (s *session) del(ctx context.Context, path string, out any) (*saas.Response, error) {
	resp, err := s.do(ctx, saas.Request{Method: http.MethodDelete, Path: path, Idempotent: true})
	return decode(resp, err, out)
}

func decode(resp *saas.Response, err error, out any) (*saas.Response, error) {
	if err != nil {
		return resp, err
	}
	if out != nil {
		dec := json.NewDecoder(bytes.NewReader(resp.Body))
		dec.UseNumber()
		if err := dec.Decode(out); err != nil {
			return resp, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, HTTPStatus: resp.Status,
				AgentMessage: "Stripe answered with something Deedglass couldn't read. The call may have run.", Err: err}
		}
	}
	return resp, nil
}

// stripeErr returns Stripe's error object in err's chain.
func stripeErr(err error) *stripeError {
	var se *stripeError
	if errors.As(err, &se) {
		return se
	}
	return nil
}
