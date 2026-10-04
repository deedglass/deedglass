package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/approve"
	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/httpx"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/store"
)

// POST /v1/slack/interact (S29 §3.4): Slack's interactivity request URL. A click becomes S22's
// Decide only after the request is signed with the routed org's signing secret, fresh, not a
// replay, and from a Slack user who maps to an active member by confirmed email. Every failure
// before the signature is verified is a bare 401. The handler answers within Budget; it never runs
// a connector call for the action: the Gate resumes approved actions in its worker.

// Limits and timeouts of the handler.
const (
	MaxBody        = 256 << 10
	MaxSkew        = 300 * time.Second
	ReplayWindow   = 10 * time.Minute
	DefaultBudget  = 2500 * time.Millisecond
	DefaultLookup  = 1500 * time.Millisecond
	PerIPPerMinute = 120
	// Action ids on the card and the deny modal's callback (card.go).
	ActionApprove = CardActionApprove
	ActionDeny    = CardActionDeny
	ActionOpen    = CardActionOpen
	CallbackDeny  = DenyCallbackID
)

var reSignature = regexp.MustCompile(`^v0=[0-9a-f]{64}$`)

// Decider is approve.Service's Decide (S22): the one way any person decides.
type Decider interface {
	Decide(ctx context.Context, in approve.DecideInput) (*approve.Approval, error)
}

// Handler serves POST /v1/slack/interact.
type Handler struct {
	Store     Store
	Conns     Instances
	API       API
	Approvals Decider
	Log       *slog.Logger
	Now       func() time.Time
	Budget    time.Duration // DefaultBudget
	Lookup    time.Duration // DefaultLookup: users.info's limit
	// Async runs the ephemeral replies after the answer (tests run them inline).
	Async func(func())

	limiter *httpx.Limiter
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

func (h *Handler) api() API {
	if h.API != nil {
		return h.API
	}
	return ConnectorAPI{}
}

func (h *Handler) async(f func()) {
	if h.Async != nil {
		h.Async(f)
		return
	}
	go f()
}

// Routes is the route serve mounts.
func (h *Handler) Routes() []httpx.Route {
	h.limiter = httpx.NewLimiter(PerIPPerMinute, time.Minute, nil)
	return []httpx.Route{{Method: "POST", Pattern: "/v1/slack/interact", OperationID: "slackInteract", Auth: httpx.Public,
		Idempotency: httpx.IdemNever, MaxBody: MaxBody, BodyType: "application/x-www-form-urlencoded", Timeout: 5 * time.Second,
		Guard: bareGuard{}, Handler: h.serve}}
}

// bareGuard renders every refusal of this route as a bare status with no body: Slack reads only
// the status, and a forged request learns nothing. Client errors are 401; a rate limit stays 429.
type bareGuard struct{}

func (bareGuard) Admit(http.ResponseWriter, *http.Request) bool { return true }
func (bareGuard) Challenge(_ *http.Request, v string) string    { return v }
func (bareGuard) RenderError(w http.ResponseWriter, _ *http.Request, e *httpx.Error, _ string) {
	st := e.Status
	switch {
	case st == http.StatusTooManyRequests:
		if v := e.Header.Get("Retry-After"); v != "" {
			w.Header().Set("Retry-After", v)
		}
	case st < 500:
		st = http.StatusUnauthorized
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(st)
}

// payload is the parts of Slack's interaction payload Deedglass reads.
type payload struct {
	Type string `json:"type"`
	Team struct {
		ID string `json:"id"`
	} `json:"team"`
	APIAppID string `json:"api_app_id"`
	User     struct {
		ID     string `json:"id"`
		TeamID string `json:"team_id"`
	} `json:"user"`
	TriggerID   string `json:"trigger_id"`
	ResponseURL string `json:"response_url"`
	Actions     []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	View struct {
		CallbackID      string `json:"callback_id"`
		PrivateMetadata string `json:"private_metadata"`
		State           struct {
			Values map[string]map[string]struct {
				Value *string `json:"value"`
			} `json:"values"`
		} `json:"state"`
	} `json:"view"`
}

// refuse logs why a request was refused (never the payload, the signature or a secret) and answers
// a bare 401.
func (h *Handler) refuse(w http.ResponseWriter, r *http.Request, why string) error {
	h.log().Info("slack: interaction refused", "request_id", httpx.RequestID(r.Context()), "reason", why)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	return nil
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) error {
	budget := h.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	ctx, cancel := context.WithTimeout(r.Context(), budget)
	defer cancel()
	if h.limiter != nil {
		if ok, wait := h.limiter.Allow(httpx.ClientIP(r.Context())); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(wait.Seconds()+0.5))))
			w.WriteHeader(http.StatusTooManyRequests)
			return nil
		}
	}
	// 1. Size and type are the pipeline's (MaxBody, BodyType); read what it allowed.
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil || len(body) > MaxBody {
		return h.refuse(w, r, "body")
	}
	// 2. Timestamp and signature shape.
	now := h.now()
	ts, err := strconv.ParseInt(r.Header.Get("X-Slack-Request-Timestamp"), 10, 64)
	if err != nil || abs(now.Unix()-ts) > int64(MaxSkew/time.Second) {
		return h.refuse(w, r, "timestamp")
	}
	sig := r.Header.Get("X-Slack-Signature")
	if !reSignature.MatchString(sig) {
		return h.refuse(w, r, "signature_shape")
	}
	// 3. Routing hints only: nothing in the payload is trusted until the signature verifies.
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return h.refuse(w, r, "form")
	}
	raw := form.Get("payload")
	var p payload
	if raw == "" || !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &p) != nil {
		return h.refuse(w, r, "payload")
	}
	// 4. Which org's secret to check.
	org, conID, ok, err := h.Store.SlackRoute(ctx, p.Team.ID, p.APIAppID)
	if err != nil {
		h.log().Warn("slack: routing failed", "request_id", httpx.RequestID(r.Context()), "err", err.Error())
		return h.refuse(w, r, "route_error")
	}
	if !ok {
		return h.refuse(w, r, "route")
	}
	inst, err := h.instance(ctx, org, conID)
	if err != nil {
		return h.refuse(w, r, "connector")
	}
	// 5. The signature, with that org's signing secret.
	if err := h.api().VerifyInteraction(ctx, inst, r.Header, body, now); err != nil {
		return h.refuse(w, r, "signature")
	}
	// 6. Replay: one signature is acted on once.
	sum := sha256.Sum256([]byte(sig))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	fresh, err := h.Store.RecordInbound(ctx, org, digest, time.Unix(ts, 0).Add(ReplayWindow))
	if err != nil {
		h.log().Warn("slack: the replay check failed", "request_id", httpx.RequestID(r.Context()), "err", err.Error())
		w.WriteHeader(http.StatusServiceUnavailable)
		return nil
	}
	if !fresh {
		h.log().Info("slack: a replayed interaction was ignored", "request_id", httpx.RequestID(r.Context()), "org_id", org.String())
		w.WriteHeader(http.StatusOK)
		return nil
	}
	c := &call{h: h, ctx: ctx, org: org, inst: inst, p: p, rid: httpx.RequestID(r.Context())}
	switch p.Type {
	case "block_actions":
		c.blockActions(w)
	case "view_submission":
		c.viewSubmission(w)
	default:
		w.WriteHeader(http.StatusOK)
	}
	return nil
}

func (h *Handler) instance(ctx context.Context, org ids.OrgID, conID string) (*connector.Instance, error) {
	row, err := h.Store.Connector(ctx, org, conID)
	if err != nil {
		return nil, err
	}
	return h.Conns.Instance(org, row)
}

// call is one verified interaction.
type call struct {
	h    *Handler
	ctx  context.Context
	org  ids.OrgID
	inst *connector.Instance
	p    payload
	rid  string
}

// ephemeral replies to the clicker only, after the answer.
func (c *call) ephemeral(text string) {
	if c.p.ResponseURL == "" {
		return
	}
	h, inst, u := c.h, c.inst, c.p.ResponseURL
	h.async(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.api().Respond(ctx, inst, u, text); err != nil {
			h.log().Warn("slack: an ephemeral reply failed", "request_id", c.rid, "err", failureText(err))
		}
	})
}

func (c *call) blockActions(w http.ResponseWriter) {
	defer w.WriteHeader(http.StatusOK)
	if len(c.p.Actions) != 1 {
		return
	}
	a := c.p.Actions[0]
	if a.ActionID == ActionOpen {
		return // a link button: nothing to record
	}
	apr, digest, ok := splitValue(a.Value)
	if !ok || (a.ActionID != ActionApprove && a.ActionID != ActionDeny) {
		return
	}
	user, msg := c.person()
	if msg != "" {
		c.ephemeral(msg)
		return
	}
	switch a.ActionID {
	case ActionApprove:
		c.decide(apr, digest, user, approve.VoteApprove, "")
	case ActionDeny:
		if err := c.h.api().OpenView(c.ctx, c.inst, c.p.TriggerID, DenyModal(apr, digest)); err != nil {
			c.h.log().Warn("slack: opening the deny form failed", "request_id", c.rid, "err", failureText(err))
			c.ephemeral(MsgFailed)
		}
	}
}

func (c *call) viewSubmission(w http.ResponseWriter) {
	if c.p.View.CallbackID != CallbackDeny {
		w.WriteHeader(http.StatusOK)
		return
	}
	apr, digest, ok := splitValue(c.p.View.PrivateMetadata)
	if !ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	reason := ""
	if b, ok := c.p.View.State.Values[DenyReasonBlockID]; ok {
		if v, ok := b[DenyReasonActionID]; ok && v.Value != nil {
			reason = strings.TrimSpace(*v.Value)
		}
	}
	fieldError := func(msg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		b, _ := json.Marshal(map[string]any{"response_action": "errors", "errors": map[string]string{DenyReasonBlockID: msg}})
		_, _ = w.Write(b)
	}
	switch {
	case reason == "":
		fieldError(MsgReasonEmpty)
		return
	case utf8.RuneCountInString(reason) > approve.MaxReason:
		fieldError(MsgReasonLong)
		return
	}
	user, msg := c.person()
	if msg != "" {
		fieldError(msg)
		return
	}
	if msg := c.decide(apr, digest, user, approve.VoteDeny, reason); msg != "" {
		fieldError(msg)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// person maps the Slack user to an active member of the org by confirmed email. msg is the
// ephemeral sentence when they can't decide.
func (c *call) person() (ids.UserID, string) {
	lookup := c.h.Lookup
	if lookup <= 0 {
		lookup = DefaultLookup
	}
	ctx, cancel := context.WithTimeout(c.ctx, lookup)
	defer cancel()
	u, err := c.h.api().LookupUser(ctx, c.inst, c.p.User.ID)
	if err != nil {
		if ctx.Err() != nil {
			return ids.UserID{}, MsgSlow
		}
		c.h.log().Info("slack: users.info failed", "request_id", c.rid, "err", failureText(err))
		return ids.UserID{}, MsgUnmapped
	}
	if u.Deleted || u.IsBot || u.IsRestricted || u.IsUltraRestricted || !u.EmailConfirmed || u.Email == "" {
		return ids.UserID{}, MsgUnmapped
	}
	if u.TeamID != "" && c.p.Team.ID != "" && u.TeamID != c.p.Team.ID {
		return ids.UserID{}, MsgUnmapped
	}
	settings, err := c.h.api().Approvals(c.inst)
	if err != nil {
		return ids.UserID{}, MsgFailed
	}
	if len(settings.RequireEmailDomains) > 0 {
		_, domain, _ := strings.Cut(strings.ToLower(u.Email), "@")
		allowed := false
		for _, d := range settings.RequireEmailDomains {
			allowed = allowed || domain == strings.ToLower(d)
		}
		if !allowed {
			return ids.UserID{}, MsgUnmapped
		}
	}
	id, err := c.h.Store.MemberByEmail(c.ctx, c.org, u.Email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ids.UserID{}, MsgUnmapped
	case err != nil:
		return ids.UserID{}, MsgFailed
	}
	return id, ""
}

// decide records the person's vote through S22's Decide. A refusal is answered in Slack: as an
// ephemeral message for a click, or returned (msg) for the deny form.
func (c *call) decide(apr, digest string, user ids.UserID, vote approve.Vote, reason string) (msg string) {
	_, err := c.h.Approvals.Decide(c.ctx, approve.DecideInput{Org: c.org, ApprovalID: apr, Vote: vote, Reason: reason, ContextDigest: digest,
		Approver: approve.Approver{UserID: user, Via: approve.ViaSlack, External: c.p.Team.ID + "/" + c.p.User.ID}, RequestID: c.rid})
	if err == nil {
		return ""
	}
	msg = c.refusal(apr, err)
	if vote == approve.VoteApprove {
		c.ephemeral(msg)
	}
	return msg
}

// refusal is S22's refusal as a Slack sentence.
func (c *call) refusal(apr string, err error) string {
	e := httpx.AsError(err)
	if e == nil {
		c.h.log().Warn("slack: a decision failed", "request_id", c.rid, "org_id", c.org.String(), "err", err.Error())
		return MsgFailed
	}
	row, _ := c.h.Store.ApprovalCard(context.WithoutCancel(c.ctx), c.org, apr)
	switch e.Code {
	case httpx.CodeSelfApproval:
		return MsgSelf
	case httpx.CodeAlreadyCountersigned:
		return MsgRepeat
	case httpx.CodeApproverNotEligible:
		if row != nil {
			snap := parseSnapshot(row.Context)
			return MsgNotEligible(snap.Policy.RuleID, approverWords(snap.Policy.Approvers))
		}
		return MsgNotEligible("", "")
	case httpx.CodeApprovalExpired:
		if row != nil {
			return MsgExpired(row.ExpiresAt)
		}
		return MsgExpired(time.Time{})
	case httpx.CodeApprovalDecided:
		if row != nil {
			v := viewOf(row, nil, "")
			switch row.Status {
			case "denied":
				return MsgDecided(v.DecidedBy, "denied", v.DecidedAt)
			case "expired":
				return MsgExpired(row.ExpiresAt)
			case "cancelled":
				return "The agent cancelled this request. Nothing ran."
			}
			return MsgDecided(v.DecidedBy, "approved", v.DecidedAt)
		}
		return MsgDecided("", "decided", time.Time{})
	case httpx.CodeContextChanged:
		return MsgChanged
	case httpx.CodeNotFound:
		return MsgGone
	}
	c.h.log().Warn("slack: a decision was refused", "request_id", c.rid, "org_id", c.org.String(), "code", string(e.Code))
	return MsgFailed
}

// splitValue reads "<approval id>|<context digest>".
func splitValue(v string) (apr, digest string, ok bool) {
	apr, digest, ok = strings.Cut(v, "|")
	if !ok || !strings.HasPrefix(apr, "apr_") || len(apr) > 40 || !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 {
		return "", "", false
	}
	return apr, digest, true
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
