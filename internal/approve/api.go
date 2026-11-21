package approve

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
)

// The approval routes (S22 §3.9). Approving and denying need a person: the routes accept a key so
// that an agent or admin key gets the exact refusal (approval_needs_person), never a sign-in
// challenge. The link routes are public: the one-time token in the body is the credential.

// Rate limits on the link routes (S22 §3.7): per client IP and per token.
const (
	LinkPerIPMinute    = 30
	LinkPerTokenMinute = 10
	maxLinkBody        = 4 << 10
)

// Routes are the approval routes. With a nil Service (route-table tests) every handler answers
// service_unavailable.
func Routes(s *Service) []httpx.Route {
	h := &handlers{s: s, ip: httpx.NewLimiter(LinkPerIPMinute, time.Minute, nil), tok: httpx.NewLimiter(LinkPerTokenMinute, time.Minute, nil)}
	read := []string{httpx.ScopeActionsRead}
	return []httpx.Route{
		{Method: "GET", Pattern: "/v1/approvals", OperationID: "listApprovals", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: read, Handler: h.list},
		{Method: "GET", Pattern: "/v1/approvals/{id}", OperationID: "getApproval", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: read, Timeout: gate.MaxWait + 10*time.Second, Handler: h.get},
		{Method: "POST", Pattern: "/v1/approvals/{id}/approve", OperationID: "approveApproval", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleViewer, Purpose: "countersigning an approval", Scopes: read, Idempotency: httpx.IdemOptional,
			MaxBody: maxLinkBody, Handler: h.vote(VoteApprove)},
		{Method: "POST", Pattern: "/v1/approvals/{id}/deny", OperationID: "denyApproval", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleViewer, Purpose: "denying an approval", Scopes: read, Idempotency: httpx.IdemOptional,
			MaxBody: maxLinkBody, Handler: h.vote(VoteDeny)},
		{Method: "POST", Pattern: "/v1/approval-links/preview", OperationID: "previewApprovalLink", Auth: httpx.Public,
			Idempotency: httpx.IdemNever, MaxBody: maxLinkBody, Handler: h.preview},
		{Method: "POST", Pattern: "/v1/approval-links/decide", OperationID: "decideApprovalLink", Auth: httpx.Public,
			Idempotency: httpx.IdemNever, MaxBody: maxLinkBody, Handler: h.decideLink},
		{Method: "POST", Pattern: "/v1/approval-links/status", OperationID: "approvalLinkStatus", Auth: httpx.Public,
			Idempotency: httpx.IdemNever, MaxBody: maxLinkBody, Handler: h.status},
		{Method: "GET", Pattern: "/v1/break-glass", OperationID: "listBreakGlassReviews", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: read, Extension: "ADR-105", Handler: h.listBreakGlass},
		{Method: "GET", Pattern: "/v1/break-glass/digest", OperationID: "getBreakGlassWeeklyDigest", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: read, Extension: "ADR-105", Handler: h.digestBreakGlass},
		{Method: "GET", Pattern: "/v1/break-glass/{id}", OperationID: "getBreakGlassReview", Auth: httpx.SessionOrKey, MinRole: httpx.RoleViewer,
			Scopes: read, Extension: "ADR-105", Handler: h.getBreakGlass},
		{Method: "POST", Pattern: "/v1/break-glass/{id}/review", OperationID: "closeBreakGlassReview", Auth: httpx.SessionOrKey,
			MinRole: httpx.RoleAdmin, Purpose: "closing post-hoc emergency break-glass review", Scopes: []string{httpx.ScopeAdmin}, Extension: "ADR-105", Idempotency: httpx.IdemOptional,
			MaxBody: maxLinkBody, Handler: h.reviewBreakGlass},
	}
}

type handlers struct {
	s       *Service
	ip, tok *httpx.Limiter
}

func (h *handlers) principal(r *http.Request) (httpx.Principal, error) {
	if h.s == nil {
		return httpx.Principal{}, httpx.E(httpx.CodeServiceUnavailable)
	}
	p, ok := httpx.PrincipalFrom(r.Context())
	if !ok || p.OrgID.IsZero() {
		return p, httpx.E(httpx.CodeAuthenticationRequired)
	}
	return p, nil
}

var listParams = map[string]bool{"status": true, "agent": true, "tool": true, "limit": true, "cursor": true}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	for k := range q {
		if !listParams[k] {
			return httpx.ErrValidation(k, "Unknown parameter "+strconv.Quote(clip(k, 40))+". Use status, agent, tool, limit or cursor.")
		}
	}
	st, err := ParseStatuses(q.Get("status"))
	if err != nil {
		return err
	}
	limit := 20
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return httpx.ErrValidation("limit", "limit must be a whole number from 1 to 100.")
		}
		limit = n
	}
	l, err := h.s.List(r.Context(), ViewerOf(p), Filter{Statuses: st, Agent: q.Get("agent"), Tool: q.Get("tool")}, limit, q.Get("cursor"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, l)
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	wait := time.Duration(0)
	if v := r.URL.Query().Get("wait"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > gate.MaxWait.Seconds() {
			return httpx.ErrValidation("wait", "wait must be a number of seconds from 0 to 25.")
		}
		wait = time.Duration(f * float64(time.Second))
	}
	a, err := h.s.Wait(r.Context(), ViewerOf(p), r.PathValue("id"), wait)
	if err != nil {
		return err
	}
	httpx.Annotate(r.Context(), p.OrgID.String(), a.Agent.ID, a.Action)
	return httpx.WriteJSON(w, http.StatusOK, a)
}

type voteBody struct {
	Reason        string `json:"reason"`
	ContextDigest string `json:"context_digest"`
}

func (h *handlers) vote(v Vote) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		p, err := h.principal(r)
		if err != nil {
			return err
		}
		if p.Kind != httpx.KindUser || p.UserID.IsZero() {
			// A key never votes. It learns that only about an approval it can see: another org's id,
			// or one it can't read, answers exactly like an unknown id.
			if _, err := h.s.Get(r.Context(), ViewerOf(p), r.PathValue("id")); err != nil {
				return err
			}
			return httpx.E(httpx.CodeApprovalNeedsPerson)
		}
		var b voteBody
		if err := httpx.DecodeJSON(r, &b); err != nil {
			return err
		}
		ap := Approver{UserID: p.UserID, Via: ViaConsole}
		if !p.SessionID.IsZero() {
			sid := p.SessionID
			ap.SessionID = &sid
		}
		a, err := h.s.Decide(r.Context(), DecideInput{Org: p.OrgID, ApprovalID: r.PathValue("id"), Approver: ap, Vote: v,
			Reason: b.Reason, ContextDigest: b.ContextDigest, RequestID: httpx.RequestID(r.Context())})
		if err != nil {
			return err
		}
		httpx.Annotate(r.Context(), p.OrgID.String(), a.Agent.ID, a.Action)
		return httpx.WriteJSON(w, http.StatusOK, a)
	}
}

type tokenBody struct {
	Token         string `json:"token"`
	Vote          Vote   `json:"vote"`
	Reason        string `json:"reason"`
	ContextDigest string `json:"context_digest"`
}

// limit charges the client's IP and the token, and refuses with rate_limited.
func (h *handlers) limit(r *http.Request, token string) error {
	if h.s == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	for _, c := range []struct {
		l   *httpx.Limiter
		key string
	}{{h.ip, "ip:" + httpx.ClientIP(r.Context())}, {h.tok, "tok:" + LinkHash(token)}} {
		if ok, after := c.l.Allow(c.key); !ok {
			n := int(after.Seconds()) + 1
			return httpx.E(httpx.CodeRateLimited, httpx.Msg("Too many requests for this approval link. Retry after "+strconv.Itoa(n)+" s."),
				httpx.WithHeader("Retry-After", strconv.Itoa(n)))
		}
	}
	return nil
}

func (h *handlers) readToken(r *http.Request) (tokenBody, error) {
	var b tokenBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return b, err
	}
	b.Token = strings.TrimSpace(b.Token)
	return b, h.limit(r, b.Token)
}

func (h *handlers) preview(w http.ResponseWriter, r *http.Request) error {
	b, err := h.readToken(r)
	if err != nil {
		return err
	}
	out, err := h.s.Preview(r.Context(), b.Token)
	if err != nil {
		return err
	}
	noStore(w)
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handlers) decideLink(w http.ResponseWriter, r *http.Request) error {
	b, err := h.readToken(r)
	if err != nil {
		return err
	}
	a, err := h.s.DecideLink(r.Context(), LinkDecision{Token: b.Token, Vote: b.Vote, Reason: b.Reason, ContextDigest: b.ContextDigest},
		httpx.RequestID(r.Context()))
	if err != nil {
		return err
	}
	noStore(w)
	return httpx.WriteJSON(w, http.StatusOK, a)
}

func (h *handlers) status(w http.ResponseWriter, r *http.Request) error {
	b, err := h.readToken(r)
	if err != nil {
		return err
	}
	out, err := h.s.Status(r.Context(), b.Token)
	if err != nil {
		return err
	}
	noStore(w)
	return httpx.WriteJSON(w, http.StatusOK, out)
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

type listBreakGlassResponse struct {
	Reviews []*BreakGlassReview `json:"reviews"`
	Count   int                 `json:"count"`
}

type reviewBreakGlassBody struct {
	Reason          string `json:"reason"`
	ReviewLatencyMs int64  `json:"review_latency_ms"`
}

func (h *handlers) listBreakGlass(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	if h.s == nil || h.s.breakGlass == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	st := r.URL.Query().Get("status")
	reviews := h.s.breakGlass.List(p.OrgID, ReviewStatus(st))
	if reviews == nil {
		reviews = []*BreakGlassReview{}
	}
	return httpx.WriteJSON(w, http.StatusOK, listBreakGlassResponse{
		Reviews: reviews,
		Count:   len(reviews),
	})
}

func (h *handlers) getBreakGlass(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	if h.s == nil || h.s.breakGlass == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	id := r.PathValue("id")
	rev, err := h.s.breakGlass.Get(id)
	if err != nil || (rev.OrgID != (ids.OrgID{}) && rev.OrgID != p.OrgID) {
		return httpx.E(httpx.CodeNotFound)
	}
	return httpx.WriteJSON(w, http.StatusOK, rev)
}

func (h *handlers) reviewBreakGlass(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	if h.s == nil || h.s.breakGlass == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	id := r.PathValue("id")
	var b reviewBreakGlassBody
	if err := httpx.DecodeJSON(r, &b); err != nil {
		return err
	}
	reason := strings.TrimSpace(b.Reason)
	if reason == "" {
		return httpx.ErrValidation("reason", "reviewer reason is required to close break-glass review (HC-P08).")
	}
	if len(reason) > 280 {
		return httpx.ErrValidation("reason", "reviewer reason cannot exceed 280 characters.")
	}

	reviewer := ""
	if !p.UserID.IsZero() {
		reviewer = p.UserID.String()
	} else if !p.KeyID.IsZero() {
		reviewer = p.KeyID.String()
	} else {
		reviewer = "clinician-reviewer"
	}

	rev, err := h.s.breakGlass.CloseReview(CloseReviewInput{
		ReviewID:        id,
		Reviewer:        reviewer,
		Reason:          reason,
		ReviewLatencyMs: b.ReviewLatencyMs,
	})
	if err != nil {
		return httpx.ErrValidation("id", err.Error())
	}
	return httpx.WriteJSON(w, http.StatusOK, rev)
}

func (h *handlers) digestBreakGlass(w http.ResponseWriter, r *http.Request) error {
	p, err := h.principal(r)
	if err != nil {
		return err
	}
	if h.s == nil || h.s.breakGlass == nil {
		return httpx.E(httpx.CodeServiceUnavailable)
	}
	days := 7
	if d := r.URL.Query().Get("window_days"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 1 || n > 30 {
			return httpx.ErrValidation("window_days", "window_days must be between 1 and 30.")
		}
		days = n
	}
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -days)
	digest := h.s.breakGlass.GenerateWeeklyDigest(p.OrgID, start, now)

	format := r.URL.Query().Get("format")
	if format == "json" {
		return httpx.WriteJSON(w, http.StatusOK, digest)
	}

	md := FormatWeeklyDigest(digest)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(md))
	return nil
}
