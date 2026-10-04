package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

// Credential field names (kind "fields").
const (
	FieldBotToken      = "bot_token"
	FieldSigningSecret = "signing_secret"
)

// Messages for a credential Deedglass refuses (S29 §3.3).
const (
	MsgUserToken     = "That is a user token. Deedglass acts as a bot, not as a person. Use the Bot User OAuth Token (xoxb-)."
	MsgAppToken      = "That is an app-level token for Socket Mode, which Deedglass doesn't use yet."
	MsgNotBotToken   = "That isn't a bot token. Use the Bot User OAuth Token (xoxb-) from the app's OAuth & Permissions page."
	MsgSigningSecret = "The signing secret must be 32 lowercase hex characters. Copy it from the app's Basic Information page."
)

var (
	reSigningSecret = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reTokenBody     = regexp.MustCompile(`^xoxb-[A-Za-z0-9-]{10,}$`)
	reSlackCode     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	reNeeded        = regexp.MustCompile(`^[a-z:.,_]{0,200}$`)
	reTS            = regexp.MustCompile(tsPattern)
	reUserID        = regexp.MustCompile(`^[UW][A-Z0-9]{2,20}$`)
)

// checkBotToken returns the sentence for a token Deedglass refuses, or "".
func checkBotToken(tok string) string {
	switch {
	case tok == "":
		return "The credential has no bot_token field."
	case strings.HasPrefix(tok, "xoxp-"):
		return MsgUserToken
	case strings.HasPrefix(tok, "xapp-"):
		return MsgAppToken
	case !reTokenBody.MatchString(tok):
		return MsgNotBotToken
	}
	return ""
}

// checkSigningSecret returns the sentence for a signing secret Deedglass refuses, or "".
func checkSigningSecret(s string) string {
	if !reSigningSecret.MatchString(s) {
		if s == "" {
			return "The credential has no signing_secret field."
		}
		return MsgSigningSecret
	}
	return ""
}

func setCredentialsFix(name string) string {
	return fmt.Sprintf("deedglass connector credentials set %s --field bot_token=env:SLACK_BOT_TOKEN --field signing_secret=env:SLACK_SIGNING_SECRET", name)
}

// credError is a stored credential Deedglass refuses: nothing was sent.
func credError(inst *connector.Instance, why string) *connector.Error {
	return &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable,
		AgentMessage: fmt.Sprintf(`The connector "%s" holds a Slack credential Deedglass can't use, so nothing was sent. An admin can run deedglass connector test %s.`, inst.Name, inst.Name),
		AdminDetail:  why + " Store it again: " + setCredentialsFix(inst.Name) + "."}
}

// slackError is Slack's {"ok":false,"error":code} answer.
type slackError struct {
	Code   string
	Needed string // missing_scope: the scope Slack asked for
}

func (e *slackError) Error() string { return "slack: " + e.Code }

// slackCode returns Slack's error code in err's chain, or "".
func slackCode(err error) string {
	var se *slackError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

// classify maps Slack's answers: HTTP 429 and ok:false (S29 §3.3 errors).
func classify(r *saas.Response) *connector.Error {
	if r.Status == http.StatusTooManyRequests {
		return rateLimited(r.RetryAfter(time.Hour), r.Status)
	}
	if r.Status >= 400 {
		return connector.FromStatus(r.Status, r.RetryAfter(time.Hour))
	}
	var b struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Needed string `json:"needed"`
	}
	if err := json.Unmarshal(r.Body, &b); err != nil {
		return &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, HTTPStatus: r.Status,
			AgentMessage: "Slack answered with something Deedglass couldn't read. The call may have run."}
	}
	if b.OK {
		return nil
	}
	return fromSlack(b.Error, b.Needed, r)
}

func rateLimited(ra time.Duration, status int) *connector.Error {
	e := &connector.Error{Kind: connector.KindRateLimited, Code: connector.CodeUpstreamRateLimited, Retryable: true, RetryAfter: ra, HTTPStatus: status,
		AgentMessage: "Slack is limiting requests. Wait, then send the call again.", Err: &slackError{Code: "ratelimited"}}
	if ra > 0 {
		e.AgentMessage = fmt.Sprintf("Slack is limiting requests. Wait %s, then send the call again.", ra.Round(time.Second))
	}
	return e
}

// fromSlack maps one ok:false code.
func fromSlack(code, needed string, r *saas.Response) *connector.Error {
	if !reSlackCode.MatchString(code) {
		code = "unknown_error"
	}
	if !reNeeded.MatchString(needed) {
		needed = ""
	}
	se := &slackError{Code: code, Needed: needed}
	e := &connector.Error{Err: se, HTTPStatus: r.Status}
	switch code {
	case "ratelimited", "rate_limited":
		return rateLimited(r.RetryAfter(time.Hour), r.Status)
	case "not_in_channel":
		e.Kind, e.Code = connector.KindInvalidRequest, connector.CodeUpstreamError
		e.AgentMessage = "Invite @deedglass to the channel, then try again."
	case "channel_not_found":
		e.Kind, e.Code = connector.KindNotFound, connector.CodeUpstreamNotFound
		e.AgentMessage = "Slack has no channel by that id that the bot can see. Check the channel, or invite @deedglass to it."
	case "message_not_found", "thread_not_found", "no_reaction", "user_not_found", "users_not_found", "bot_not_found":
		e.Kind, e.Code = connector.KindNotFound, connector.CodeUpstreamNotFound
		e.AgentMessage = fmt.Sprintf("Slack has nothing there (%s). Check the arguments.", code)
	case "invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive", "missing_scope",
		"not_allowed_token_type", "no_permission", "ekm_access_denied", "team_access_not_granted":
		e.Kind, e.Code = connector.KindAuth, connector.CodeUpstreamAuthFailed
		e.AgentMessage = fmt.Sprintf("Slack refused the connector's bot token (%s). Nothing was changed. An admin can replace it in Console › Connectors.", code)
		e.AdminDetail = fmt.Sprintf("Slack answered %s. Store a working bot token, then run deedglass connector test slack.", code)
		if code == "missing_scope" && needed != "" {
			e.AdminDetail = fmt.Sprintf("Slack needs the scope %s for this call. Add it in the app's OAuth & Permissions page, reinstall the app, then run deedglass connector test slack.", needed)
		}
	case "internal_error", "fatal_error", "service_unavailable", "request_timeout":
		e.Kind, e.Code, e.Retryable = connector.KindUpstream, connector.CodeUpstreamError, true
		e.AgentMessage = fmt.Sprintf("Slack failed (%s). The call may have run.", code)
	default:
		e.Kind, e.Code = connector.KindInvalidRequest, connector.CodeUpstreamError
		e.AgentMessage = fmt.Sprintf("Slack refused the request (%s). Check the arguments.", code)
	}
	return e
}

// session is one opened credential and a client that sends it.
type session struct {
	st   *state
	inst *connector.Instance
	sec  *vault.Secret
	cli  saas.Client
}

// open opens the instance's credential and checks the bot token. Callers close the session as
// soon as the calls are done (the secret is destroyed).
func open(ctx context.Context, inst *connector.Instance) (*session, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	sec, err := inst.Secret(ctx)
	if err != nil {
		return nil, err
	}
	if why := checkBotToken(sec.Field(FieldBotToken)); why != "" {
		sec.Destroy()
		return nil, credError(inst, why)
	}
	s := &session{st: st, inst: inst, sec: sec}
	s.cli = saas.Client{Base: st.base, HTTP: client(inst), Headers: http.Header{"Accept": {"application/json"}},
		Classify: classify, MaxBody: maxBody}
	s.cli.Auth = func(_ context.Context, r *http.Request) error {
		tok := sec.Field(FieldBotToken)
		if tok == "" {
			return credError(inst, "The credential was already closed.")
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}
	return s, nil
}

func (s *session) close() {
	if s != nil {
		s.sec.Destroy()
	}
}

// get calls a read method with a query. idem allows saas's one retry.
func (s *session) get(ctx context.Context, method string, q url.Values, idem bool, out any) (*saas.Response, error) {
	resp, err := s.cli.Do(ctx, saas.Request{Method: http.MethodGet, Path: saas.MustSegments(method), Query: q, Idempotent: idem})
	return decode(resp, err, out)
}

// post calls a write method with a form body. It is never retried unless idem.
func (s *session) post(ctx context.Context, method string, f url.Values, idem bool, out any) (*saas.Response, error) {
	resp, err := s.cli.Do(ctx, saas.Request{Method: http.MethodPost, Path: saas.MustSegments(method), Form: f, Idempotent: idem})
	return decode(resp, err, out)
}

func decode(resp *saas.Response, err error, out any) (*saas.Response, error) {
	if err != nil {
		return resp, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return resp, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, HTTPStatus: resp.Status,
				AgentMessage: "Slack answered with something Deedglass couldn't read. The call may have run.", Err: err}
		}
	}
	return resp, nil
}

// Channel is what Deedglass knows about a conversation.
type Channel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IsPrivate   bool   `json:"is_private"`
	IsIM        bool   `json:"is_im"`
	IsExtShared bool   `json:"is_ext_shared"`
	IsMember    bool   `json:"is_member"`
	IsArchived  bool   `json:"is_archived"`
}

// channelList is conversations.list, cached.
type channelList struct {
	at     time.Time
	byID   map[string]Channel
	byName map[string]string
	order  []string
}

// maxListPages bounds the channel list: 10 pages of 200.
const maxListPages = 10

// minRefresh is how old the cached list must be before a name it doesn't know refetches it, so
// unknown names (a stale config entry, an agent guessing) can't spend Slack's rate limit.
const minRefresh = 30 * time.Second

// channels returns the channel list, from the cache unless it is older than ChannelCacheTTL or
// fresh is set.
func (s *session) channels(ctx context.Context, fresh bool) (*channelList, error) {
	s.st.chMu.Lock()
	defer s.st.chMu.Unlock()
	if c := s.st.chans; c != nil && !fresh && now().Sub(c.at) < ChannelCacheTTL {
		return c, nil
	}
	cl := &channelList{at: now(), byID: map[string]Channel{}, byName: map[string]string{}}
	cursor := ""
	for page := 0; page < maxListPages; page++ {
		q := url.Values{"types": {"public_channel,private_channel"}, "exclude_archived": {"true"}, "limit": {"200"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var b struct {
			Channels []Channel `json:"channels"`
			Meta     struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if _, err := s.get(ctx, "conversations.list", q, true, &b); err != nil {
			return nil, err
		}
		for _, c := range b.Channels {
			if !reChannelID.MatchString(c.ID) {
				continue
			}
			if _, dup := cl.byID[c.ID]; !dup {
				cl.order = append(cl.order, c.ID)
			}
			cl.byID[c.ID] = c
			cl.byName[strings.ToLower(c.Name)] = c.ID
		}
		cursor = b.Meta.NextCursor
		if cursor == "" {
			break
		}
	}
	s.st.chans = cl
	return cl, nil
}

// errChannelUnknown: a channel name that doesn't resolve.
var errChannelUnknown = errors.New("slack: no channel by that name")

// resolve turns "#name", "name" or an id into a channel id. An id is kept as it is (it may be a
// conversation the list doesn't show); a name resolves through the cached list, refreshed once on
// a miss when the list is at least minRefresh old.
func (s *session) resolve(ctx context.Context, ref string) (string, error) {
	if reChannelID.MatchString(ref) {
		return ref, nil
	}
	name := strings.ToLower(strings.TrimPrefix(ref, "#"))
	for _, fresh := range []bool{false, true} {
		cl, err := s.channels(ctx, fresh)
		if err != nil {
			return "", err
		}
		if id, ok := cl.byName[name]; ok {
			return id, nil
		}
		if now().Sub(cl.at) < minRefresh {
			break // fetched moments ago: an unknown name doesn't refetch the list
		}
	}
	return "", errChannelUnknown
}

// allowed reports whether channel id may be used under list (ids compared; "*" = any channel the
// bot is a member of).
func (s *session) allowed(ctx context.Context, list []string, id string) (bool, error) {
	if len(list) == 1 && list[0] == "*" {
		cl, err := s.channels(ctx, false)
		if err != nil {
			return false, err
		}
		c, ok := cl.byID[id]
		if !ok || !c.IsMember {
			if cl, err = s.channels(ctx, true); err != nil {
				return false, err
			}
			c, ok = cl.byID[id]
		}
		return ok && c.IsMember, nil
	}
	for _, e := range list {
		eid, err := s.resolve(ctx, e)
		if errors.Is(err, errChannelUnknown) {
			continue
		}
		if err != nil {
			return false, err
		}
		if eid == id {
			return true, nil
		}
	}
	return false, nil
}

// channelName is "#name" for an id when the cache knows it, else the id.
func (s *session) channelName(id string) string {
	s.st.chMu.Lock()
	defer s.st.chMu.Unlock()
	if c := s.st.chans; c != nil {
		if ch, ok := c.byID[id]; ok && ch.Name != "" {
			return "#" + ch.Name
		}
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	if e, ok := s.st.infos[id]; ok && e.ch.Name != "" {
		return "#" + e.ch.Name
	}
	return id
}

// info is conversations.info, cached for ChannelCacheTTL.
func (s *session) info(ctx context.Context, id string) (Channel, error) {
	s.st.mu.Lock()
	if e, ok := s.st.infos[id]; ok && now().Sub(e.at) < ChannelCacheTTL {
		s.st.mu.Unlock()
		return e.ch, nil
	}
	s.st.mu.Unlock()
	var b struct {
		Channel Channel `json:"channel"`
	}
	if _, err := s.get(ctx, "conversations.info", url.Values{"channel": {id}}, true, &b); err != nil {
		return Channel{}, err
	}
	s.st.mu.Lock()
	if len(s.st.infos) > 1000 {
		clear(s.st.infos)
	}
	s.st.infos[id] = infoEntry{at: now(), ch: b.Channel}
	s.st.mu.Unlock()
	return b.Channel, nil
}

// whoami is auth.test, cached for ChannelCacheTTL (fresh skips the cache).
func (s *session) whoami(ctx context.Context, fresh bool) (*selfInfo, error) {
	s.st.mu.Lock()
	if me := s.st.self; me != nil && !fresh && now().Sub(me.at) < ChannelCacheTTL {
		s.st.mu.Unlock()
		return me, nil
	}
	s.st.mu.Unlock()
	var b struct {
		Team   string `json:"team"`
		TeamID string `json:"team_id"`
		User   string `json:"user"`
		UserID string `json:"user_id"`
		BotID  string `json:"bot_id"`
	}
	resp, err := s.get(ctx, "auth.test", nil, true, &b)
	if err != nil {
		return nil, err
	}
	me := &selfInfo{at: now(), TeamID: b.TeamID, Team: b.Team, UserID: b.UserID, UserName: b.User, BotID: b.BotID}
	for _, sc := range strings.Split(resp.Header.Get("X-Oauth-Scopes"), ",") {
		if sc = strings.TrimSpace(sc); sc != "" {
			me.Scopes = append(me.Scopes, sc)
		}
	}
	s.st.mu.Lock()
	s.st.self = me
	s.st.mu.Unlock()
	return me, nil
}

// botName is "@name" of the bot when auth.test has been seen, else "@deedglass".
func (st *state) botName() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.self != nil && st.self.UserName != "" {
		return "@" + st.self.UserName
	}
	return "@deedglass"
}
