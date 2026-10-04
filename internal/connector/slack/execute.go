package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
)

// args is every tool's arguments (the schemas are closed; ValidateArgs ran first in the registry).
type args struct {
	Channel   string `json:"channel"`
	Limit     int    `json:"limit"`
	Text      string `json:"text"`
	ThreadTS  string `json:"thread_ts"`
	Timestamp string `json:"timestamp"`
	Name      string `json:"name"`
}

func decodeArgs(raw json.RawMessage) (args, error) {
	var a args
	if len(raw) == 0 {
		return a, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return a, connector.ErrValidation("args", "must match the tool's input schema")
	}
	a.Name = strings.Trim(a.Name, ":")
	return a, nil
}

// refused is a channel outside the allow-list: nothing is sent.
func refused(inst *connector.Instance, ref string) *connector.Error {
	return &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeValidationFailed,
		AgentMessage: fmt.Sprintf("args.channel: %s isn't in this connector's channels. Add it in Console › Connectors › %s, or use an allowed channel.", showRef(ref), inst.Name)}
}

func showRef(ref string) string {
	if reChannelID.MatchString(ref) || strings.HasPrefix(ref, "#") {
		return ref
	}
	return "#" + ref
}

// target resolves the channel argument and checks it against list. A name that doesn't resolve
// and a channel outside the list are validation_failed, and nothing is sent.
func (s *session) target(ctx context.Context, list []string, ref string) (string, error) {
	id, err := s.resolve(ctx, ref)
	if errors.Is(err, errChannelUnknown) {
		return "", &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeValidationFailed,
			AgentMessage: fmt.Sprintf("args.channel: Slack has no channel named %s that the bot can see. Check the name, or use the channel id.", showRef(ref))}
	}
	if err != nil {
		return "", err
	}
	ok, err := s.allowed(ctx, list, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", refused(s.inst, ref)
	}
	return id, nil
}

// notInChannel names the channel in Slack's not_in_channel refusal.
func (s *session) notInChannel(err error, id string) error {
	if ce := connector.AsError(err); ce != nil && slackCode(err) == "not_in_channel" {
		ce.AgentMessage = fmt.Sprintf("Invite %s to %s, then try again.", s.st.botName(), s.channelName(id))
	}
	return err
}

// Execute implements connector.Connector.
func (c *Connector) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	a, err := decodeArgs(call.Args)
	if err != nil {
		return connector.Result{}, err
	}
	switch call.Tool.Name {
	case ToolListChannels, ToolReadChannel, ToolPostMessage, ToolAddReaction:
	default:
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown",
			AgentMessage: "The Slack connector has no tool by that name. Its tools are list_channels, read_channel, post_message and add_reaction."}
	}
	if call.Tool.Name == ToolPostMessage && strings.TrimSpace(a.Text) == "" {
		return connector.Result{}, connector.ErrValidation("args.text", "is empty")
	}
	s, err := open(ctx, inst)
	if err != nil {
		return connector.Result{}, err
	}
	defer s.close()
	switch call.Tool.Name {
	case ToolListChannels:
		return s.listChannels(ctx)
	case ToolReadChannel:
		return s.readChannel(ctx, a)
	case ToolPostMessage:
		return s.postMessage(ctx, call, a)
	default:
		return s.addReaction(ctx, a)
	}
}

func (s *session) listChannels(ctx context.Context) (connector.Result, error) {
	cl, err := s.channels(ctx, true)
	if err != nil {
		return connector.Result{}, err
	}
	type item struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		IsPrivate bool   `json:"is_private"`
	}
	out := []item{}
	for _, id := range cl.order {
		ch := cl.byID[id]
		if !ch.IsMember || ch.IsArchived {
			continue
		}
		if len(out) == MaxListed {
			break
		}
		out = append(out, item{ID: ch.ID, Name: ch.Name, IsPrivate: ch.IsPrivate})
	}
	b, _ := json.Marshal(map[string]any{"channels": out, "count": len(out)})
	return connector.Result{Output: b, Meta: map[string]any{"slack_method": "conversations.list", "http_status": 200}}, nil
}

func (s *session) readChannel(ctx context.Context, a args) (connector.Result, error) {
	id, err := s.target(ctx, s.st.cfg.ReadChannels, a.Channel)
	if err != nil {
		return connector.Result{}, err
	}
	limit := a.Limit
	max := s.st.cfg.MaxReadMessages
	if limit <= 0 || limit > max {
		limit = max
	}
	var b struct {
		Messages []struct {
			TS       string `json:"ts"`
			User     string `json:"user"`
			BotID    string `json:"bot_id"`
			Text     string `json:"text"`
			ThreadTS string `json:"thread_ts"`
		} `json:"messages"`
		HasMore bool `json:"has_more"`
	}
	resp, err := s.get(ctx, "conversations.history", url.Values{"channel": {id}, "limit": {fmt.Sprint(limit)}}, true, &b)
	if err != nil {
		return connector.Result{}, s.notInChannel(err, id)
	}
	type msg struct {
		TS       string `json:"ts"`
		User     string `json:"user,omitempty"`
		Text     string `json:"text"`
		ThreadTS string `json:"thread_ts,omitempty"`
	}
	out := make([]msg, 0, len(b.Messages))
	for i, m := range b.Messages {
		if i == limit {
			break
		}
		u := m.User
		if u == "" {
			u = m.BotID
		}
		out = append(out, msg{TS: m.TS, User: u, Text: clipBytes(m.Text, MaxReadText), ThreadTS: m.ThreadTS})
	}
	body, _ := json.Marshal(map[string]any{"channel": id, "messages": out, "has_more": b.HasMore || len(b.Messages) > limit})
	return connector.Result{Output: body, Meta: map[string]any{"slack_method": "conversations.history", "http_status": resp.Status, "channel_id": id}}, nil
}

// metadata is the message metadata every post carries (S29 §3.3: L1 reads it back).
func metadata(actionID string) string {
	b, _ := json.Marshal(map[string]any{"event_type": "deedglass_action", "event_payload": map[string]string{"action_id": actionID}})
	return string(b)
}

func (s *session) postMessage(ctx context.Context, call connector.Call, a args) (connector.Result, error) {
	id, err := s.target(ctx, s.st.cfg.Channels, a.Channel)
	if err != nil {
		return connector.Result{}, err
	}
	f := url.Values{"channel": {id}, "text": {a.Text}, "unfurl_links": {"false"}, "unfurl_media": {"false"},
		"link_names": {"false"}, "metadata": {metadata(call.ActionID.String())}}
	if a.ThreadTS != "" {
		f.Set("thread_ts", a.ThreadTS)
	}
	var b struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	resp, err := s.post(ctx, "chat.postMessage", f, false, &b)
	if err != nil {
		return connector.Result{}, s.notInChannel(err, id)
	}
	if b.Channel == "" {
		b.Channel = id
	}
	out := map[string]any{"channel": b.Channel, "ts": b.TS}
	if a.ThreadTS != "" {
		out["thread_ts"] = a.ThreadTS
	}
	body, _ := json.Marshal(out)
	return connector.Result{Output: body,
		Meta:     map[string]any{"slack_method": "chat.postMessage", "http_status": resp.Status, "channel_id": b.Channel, "ts": b.TS},
		Evidence: map[string]string{"channel": id, "ts": b.TS, "posted_channel": b.Channel}}, nil
}

func (s *session) addReaction(ctx context.Context, a args) (connector.Result, error) {
	id, err := s.target(ctx, s.st.cfg.Channels, a.Channel)
	if err != nil {
		return connector.Result{}, err
	}
	f := url.Values{"channel": {id}, "timestamp": {a.Timestamp}, "name": {a.Name}}
	already := false
	resp, err := s.post(ctx, "reactions.add", f, true, nil)
	switch {
	case err != nil && slackCode(err) == "already_reacted":
		already = true
	case err != nil:
		return connector.Result{}, s.notInChannel(err, id)
	}
	status := 200
	if resp != nil {
		status = resp.Status
	}
	body, _ := json.Marshal(map[string]any{"channel": id, "timestamp": a.Timestamp, "name": a.Name, "already_reacted": already})
	return connector.Result{Output: body,
		Meta:     map[string]any{"slack_method": "reactions.add", "http_status": status, "channel_id": id, "already_reacted": already},
		Evidence: map[string]string{"channel": id, "ts": a.Timestamp, "reaction": a.Name}}, nil
}

// clipBytes cuts s to at most n bytes on a rune boundary.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
