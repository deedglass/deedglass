package slack

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

// EnrichTimeout bounds the enrich lookups: the decision never waits longer on Slack.
const EnrichTimeout = 2 * time.Second

var (
	reBroadcast = regexp.MustCompile(`<!(channel|here|everyone)(\|[^>]*)?>`)
	reMention   = regexp.MustCompile(`<@[UW][A-Z0-9]+(\|[^>]*)?>`)
)

// Facts is request.slack (policy.SlackFacts has the same JSON).
type Facts struct {
	ChannelID    string `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	IsPrivate    bool   `json:"is_private"`
	IsIM         bool   `json:"is_im"`
	IsExtShared  bool   `json:"is_ext_shared"`
	Broadcast    bool   `json:"broadcast"`
	UserMentions int64  `json:"user_mentions"`
	Enriched     bool   `json:"enriched"`
}

func (f Facts) asMap() map[string]any {
	return map[string]any{"channel_id": f.ChannelID, "channel_name": f.ChannelName, "is_private": f.IsPrivate, "is_im": f.IsIM,
		"is_ext_shared": f.IsExtShared, "broadcast": f.Broadcast, "user_mentions": f.UserMentions, "enriched": f.Enriched}
}

// TextFacts are the facts the text alone gives: broadcast and the count of user mentions.
func TextFacts(text string) (broadcast bool, mentions int64) {
	return reBroadcast.MatchString(text), int64(len(reMention.FindAllStringIndex(text, -1)))
}

// Enrich implements connector.Enricher: {"slack": {…}} for the tools that name a channel. A
// lookup that fails leaves enriched false and is never an error that blocks the decision.
func (c *Connector) Enrich(ctx context.Context, inst *connector.Instance, t connector.ToolSpec, raw json.RawMessage) (map[string]any, error) {
	if t.Name == ToolListChannels {
		return nil, nil
	}
	a, err := decodeArgs(raw)
	if err != nil {
		return map[string]any{"slack": Facts{}.asMap()}, nil
	}
	var f Facts
	if t.Name == ToolPostMessage {
		f.Broadcast, f.UserMentions = TextFacts(a.Text)
	}
	if reChannelID.MatchString(a.Channel) {
		f.ChannelID = a.Channel
	}
	ctx, cancel := context.WithTimeout(ctx, EnrichTimeout)
	defer cancel()
	s, err := open(ctx, inst)
	if err != nil {
		return map[string]any{"slack": f.asMap()}, nil
	}
	defer s.close()
	id, err := s.resolve(ctx, a.Channel)
	if err != nil {
		return map[string]any{"slack": f.asMap()}, nil
	}
	f.ChannelID = id
	ch, err := s.info(ctx, id)
	if err != nil {
		return map[string]any{"slack": f.asMap()}, nil
	}
	f.ChannelName = strings.TrimPrefix(ch.Name, "#")
	f.IsPrivate, f.IsIM, f.IsExtShared = ch.IsPrivate, ch.IsIM, ch.IsExtShared
	f.Enriched = true
	return map[string]any{"slack": f.asMap()}, nil
}

// Summarize implements connector.Summarizer: one sentence from the tool, the channel and the
// enrich facts, never from the message text.
func (c *Connector) Summarize(in connector.SummaryInput) connector.Summary {
	a, err := decodeArgs(in.Args)
	if err != nil {
		return nil
	}
	channel := ""
	if v, ok := in.Facts["slack"]; ok {
		var f Facts
		if b, err := json.Marshal(v); err == nil && json.Unmarshal(b, &f) == nil && f.ChannelName != "" &&
			reChannelName.MatchString("#"+strings.ToLower(f.ChannelName)) {
			channel = "#" + strings.ToLower(f.ChannelName)
		}
	}
	if channel == "" && a.Channel != "" {
		if ref, ok := normChannel(showRef(a.Channel)); ok {
			channel = ref
		}
	}
	agent := connector.SummaryPart{Text: in.Agent, Style: connector.SummaryStrong}
	ch := connector.SummaryPart{Text: channel, Style: connector.SummaryStrong}
	if reChannelID.MatchString(channel) {
		ch.Style = connector.SummaryCode
	}
	switch in.Tool.Name {
	case ToolListChannels:
		return connector.Summary{agent, {Text: " wants to list the Slack channels the bot is in."}}
	case ToolReadChannel:
		if channel == "" {
			return nil
		}
		return connector.Summary{agent, {Text: " wants to read recent messages in "}, ch, {Text: "."}}
	case ToolPostMessage:
		if channel == "" {
			return nil
		}
		return connector.Summary{agent, {Text: " wants to post in "}, ch, {Text: "."}}
	case ToolAddReaction:
		if channel == "" || !reEmoji.MatchString(a.Name) {
			return nil
		}
		return connector.Summary{agent, {Text: " wants to react with "}, {Text: ":" + a.Name + ":", Style: connector.SummaryCode}, {Text: " in "}, ch, {Text: "."}}
	}
	return nil
}

var reEmoji = regexp.MustCompile(emojiPattern)
