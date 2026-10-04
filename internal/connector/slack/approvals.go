package slack

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

// The approvals API (S29 §3.4): what internal/notify/slack's notifier and interaction handler
// call. Every call goes through the connector's instance, its credential and its egress client,
// so only this package touches the bot token and the signing secret.

// ApprovalSettings is the approvals part of a Slack connector's config.
type ApprovalSettings struct {
	Enabled             bool
	Channel             string // "#name" or a channel id, as configured
	RequireEmailDomains []string
	TeamID, AppID       string
}

// User is what users.info says about a Slack user (S29 §3.4 step 7).
type User struct {
	ID, TeamID, Name, RealName, Email string
	EmailConfirmed, Deleted, IsBot    bool
	IsRestricted, IsUltraRestricted   bool
}

// Errors of the approvals API.
var (
	ErrApprovalsOff = errors.New("slack: approvals in Slack are off for this connector")
	ErrResponseURL  = errors.New("slack: a response_url must be https://hooks.slack.com/…")
	ErrArgument     = errors.New("slack: an argument doesn't have Slack's shape")
)

var reTrigger = regexp.MustCompile(`^[0-9A-Za-z._-]{1,255}$`)

// Approvals returns the connector's approval settings.
func Approvals(inst *connector.Instance) (ApprovalSettings, error) {
	st, err := parse(inst)
	if err != nil {
		return ApprovalSettings{}, err
	}
	a := st.cfg.Approvals
	return ApprovalSettings{Enabled: a.Enabled, Channel: a.Channel, RequireEmailDomains: append([]string(nil), a.RequireEmailDomains...),
		TeamID: st.cfg.TeamID, AppID: st.cfg.AppID}, nil
}

// guarded runs fn with a deadline of at most APITimeout and a redactor in ctx; every message of
// the error it returns is redacted with the credential and scrubbed.
func guarded(ctx context.Context, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, APITimeout)
	defer cancel()
	ctx = vault.WithRedactor(ctx, vault.NewRedactor())
	red := vault.RedactorFrom(ctx)
	err := fn(ctx)
	if ce := connector.AsError(err); ce != nil {
		ce.AgentMessage = vault.Scrub(red.Redact(ce.AgentMessage))
		ce.AdminDetail = vault.Scrub(red.Redact(ce.AdminDetail))
	}
	return err
}

func checkBlocks(blocks json.RawMessage, text string) error {
	b := strings.TrimSpace(string(blocks))
	if !strings.HasPrefix(b, "[") || !json.Valid(blocks) || utf8.RuneCountInString(text) > MaxText || strings.TrimSpace(text) == "" {
		return ErrArgument
	}
	return nil
}

// PostApproval posts blocks (a Block Kit JSON array) with fallback text to the approvals channel
// (resolved to an id), with unfurls off, and returns where it landed.
func PostApproval(ctx context.Context, inst *connector.Instance, blocks json.RawMessage, text string) (channelID, ts string, err error) {
	set, err := Approvals(inst)
	if err != nil {
		return "", "", err
	}
	if !set.Enabled || set.Channel == "" {
		return "", "", ErrApprovalsOff
	}
	if err := checkBlocks(blocks, text); err != nil {
		return "", "", err
	}
	err = guarded(ctx, func(ctx context.Context) error {
		s, err := open(ctx, inst)
		if err != nil {
			return err
		}
		defer s.close()
		id, err := s.resolve(ctx, set.Channel)
		if errors.Is(err, errChannelUnknown) {
			return &connector.Error{Kind: connector.KindNotFound, Code: connector.CodeUpstreamNotFound,
				AgentMessage: "Slack has no channel named " + set.Channel + " that the bot can see. Create it and invite the bot, then run deedglass connector test " + inst.Name + "."}
		}
		if err != nil {
			return err
		}
		var b struct {
			Channel string `json:"channel"`
			TS      string `json:"ts"`
		}
		f := url.Values{"channel": {id}, "blocks": {string(blocks)}, "text": {text}, "unfurl_links": {"false"}, "unfurl_media": {"false"}}
		if _, err := s.post(ctx, "chat.postMessage", f, false, &b); err != nil {
			return s.notInChannel(err, id)
		}
		channelID, ts = b.Channel, b.TS
		if channelID == "" {
			channelID = id
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return channelID, ts, nil
}

// UpdateApproval replaces the approval message at (channelID, ts) with chat.update.
func UpdateApproval(ctx context.Context, inst *connector.Instance, channelID, ts string, blocks json.RawMessage, text string) error {
	if !reChannelID.MatchString(channelID) || !reTS.MatchString(ts) {
		return ErrArgument
	}
	if err := checkBlocks(blocks, text); err != nil {
		return err
	}
	return guarded(ctx, func(ctx context.Context) error {
		s, err := open(ctx, inst)
		if err != nil {
			return err
		}
		defer s.close()
		f := url.Values{"channel": {channelID}, "ts": {ts}, "blocks": {string(blocks)}, "text": {text}}
		_, err = s.post(ctx, "chat.update", f, true, nil)
		return s.notInChannel(err, channelID)
	})
}

// maxUsers bounds the users.info cache of one instance.
const maxUsers = 10_000

// LookupUser is users.info, cached for UserCacheTTL per (connector, user id). Email is
// lowercased. The caller's ctx carries its budget (S29: 1.5 s).
func LookupUser(ctx context.Context, inst *connector.Instance, userID string) (User, error) {
	if !reUserID.MatchString(userID) {
		return User{}, ErrArgument
	}
	st, err := parse(inst)
	if err != nil {
		return User{}, err
	}
	st.mu.Lock()
	if e, ok := st.users[userID]; ok && now().Sub(e.at) < UserCacheTTL {
		st.mu.Unlock()
		return e.u, nil
	}
	st.mu.Unlock()
	var u User
	err = guarded(ctx, func(ctx context.Context) error {
		s, err := open(ctx, inst)
		if err != nil {
			return err
		}
		defer s.close()
		var b struct {
			User struct {
				ID                string `json:"id"`
				TeamID            string `json:"team_id"`
				Name              string `json:"name"`
				RealName          string `json:"real_name"`
				Deleted           bool   `json:"deleted"`
				IsBot             bool   `json:"is_bot"`
				IsRestricted      bool   `json:"is_restricted"`
				IsUltraRestricted bool   `json:"is_ultra_restricted"`
				IsEmailConfirmed  bool   `json:"is_email_confirmed"`
				Profile           struct {
					Email    string `json:"email"`
					RealName string `json:"real_name"`
				} `json:"profile"`
			} `json:"user"`
		}
		if _, err := s.get(ctx, "users.info", url.Values{"user": {userID}}, true, &b); err != nil {
			return err
		}
		x := b.User
		u = User{ID: x.ID, TeamID: x.TeamID, Name: x.Name, RealName: x.RealName, Email: strings.ToLower(strings.TrimSpace(x.Profile.Email)),
			EmailConfirmed: x.IsEmailConfirmed, Deleted: x.Deleted, IsBot: x.IsBot, IsRestricted: x.IsRestricted, IsUltraRestricted: x.IsUltraRestricted}
		if u.RealName == "" {
			u.RealName = x.Profile.RealName
		}
		if u.ID != userID {
			return &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError,
				AgentMessage: "Slack answered users.info for another user. Try again in a moment."}
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	st.mu.Lock()
	if len(st.users) >= maxUsers {
		clear(st.users)
	}
	st.users[userID] = userEntry{at: now(), u: u}
	st.mu.Unlock()
	return u, nil
}

// OpenView opens a modal with views.open and a trigger_id (Slack's 3 s window is the caller's).
func OpenView(ctx context.Context, inst *connector.Instance, triggerID string, view json.RawMessage) error {
	if !reTrigger.MatchString(triggerID) || !strings.HasPrefix(strings.TrimSpace(string(view)), "{") || !json.Valid(view) {
		return ErrArgument
	}
	return guarded(ctx, func(ctx context.Context) error {
		s, err := open(ctx, inst)
		if err != nil {
			return err
		}
		defer s.close()
		_, err = s.post(ctx, "views.open", url.Values{"trigger_id": {triggerID}, "view": {string(view)}}, false, nil)
		return err
	})
}

// responseBase checks a response_url: https://hooks.slack.com/… only (in dev also the
// api_base_url's scheme and host, for tests).
func responseBase(st *state, inst *connector.Instance, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || !strings.HasPrefix(u.Path, "/") || len(raw) > 2048 {
		return nil, ErrResponseURL
	}
	if u.Scheme == "https" && u.Host == "hooks.slack.com" {
		return u, nil
	}
	if inst.Env.Dev() && u.Scheme == st.base.Scheme && u.Host == st.base.Host {
		return u, nil
	}
	return nil, ErrResponseURL
}

// respondClassify reads a response_url's answer: an HTTP error, or {"ok":false}.
func respondClassify(r *saas.Response) *connector.Error {
	if r.Status >= 400 {
		return connector.FromStatus(r.Status, r.RetryAfter(time.Hour))
	}
	var b struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(r.Body, &b) == nil && b.OK != nil && !*b.OK {
		return fromSlack(b.Error, "", r)
	}
	return nil
}

// Respond posts {"response_type":"ephemeral","replace_original":false,"text":text} to a
// response_url. Anything but https://hooks.slack.com/… is refused without a request.
func Respond(ctx context.Context, inst *connector.Instance, responseURL, text string) error {
	st, err := parse(inst)
	if err != nil {
		return err
	}
	u, err := responseBase(st, inst, responseURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > MaxText {
		return ErrArgument
	}
	return guarded(ctx, func(ctx context.Context) error {
		c := saas.Client{Base: u, HTTP: client(inst), Headers: http.Header{"Accept": {"application/json"}}, Classify: respondClassify, MaxBody: 64 << 10}
		_, err := c.Do(ctx, saas.Request{Method: http.MethodPost, JSON: map[string]any{"response_type": "ephemeral", "replace_original": false, "text": text}})
		if ce := connector.AsError(err); ce != nil && ce.Err != nil {
			// The response_url is itself a credential: keep it out of the cause the server logs.
			ce.Err = errors.New(vault.Redact(ce.Err.Error(), responseURL, u.Path))
		}
		return err
	})
}
