package slack

import (
	"encoding/json"
	"fmt"

	"github.com/deedglass/deedglass/internal/connector"
)

// Tool names (unqualified).
const (
	ToolListChannels = "list_channels"
	ToolReadChannel  = "read_channel"
	ToolPostMessage  = "post_message"
	ToolAddReaction  = "add_reaction"
)

// L1 sentences: set on the two writes, so the Witness runs NativeReadBack.
const (
	L1PostMessage = "Slack shows the message in that channel with the same text and this action's id in its metadata."
	L1AddReaction = "Slack lists the reaction on that message by this bot."
)

// Limits of the tools.
const (
	MaxText        = 4000    // characters in a post
	MaxReadText    = 4 << 10 // bytes of each message's text read_channel returns
	MaxListed      = 200     // channels list_channels returns
	channelPattern = `^(#?[a-z0-9][a-z0-9._-]{0,79}|[CG][A-Z0-9]{6,20})$`
	tsPattern      = `^[0-9]{10}\.[0-9]{6}$`
	emojiPattern   = `^:?[a-z0-9_+'-]{1,80}(::skin-tone-[2-6])?:?$`
)

var channelArg = `{"type":"string","pattern":` + js(channelPattern) + `,"maxLength":81,"description":"#name, name, or a channel id (C… or G…)."}`

func toolSpecs(maxRead int) []connector.ToolSpec {
	readSchema := fmt.Sprintf(`{"type":"object","additionalProperties":false,"required":["channel"],"properties":{
"channel":%s,
"limit":{"type":"integer","minimum":1,"maximum":%d,"description":"How many recent messages, at most %d."}}}`, channelArg, maxRead, maxRead)
	return []connector.ToolSpec{
		{Name: ToolListChannels, Description: "List the public and private channels the bot is in (at most 200).",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolReadChannel, Description: "Read recent messages of an allowed channel: ts, user, text (up to 4 KiB each) and thread_ts.",
			InputSchema: json.RawMessage(readSchema), SideEffect: connector.SideEffectRead, Idempotent: true},
		{Name: ToolPostMessage, Description: "Post a message as the bot in an allowed channel, optionally in a thread. Links are not unfurled.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["channel","text"],"properties":{
"channel":` + channelArg + `,
"text":{"type":"string","maxLength":4000,"description":"Slack mrkdwn, at most 4,000 characters."},
"thread_ts":{"type":"string","pattern":` + js(tsPattern) + `,"description":"Reply in this thread (the parent message's ts)."}}}`),
			SideEffect: connector.SideEffectWrite, L1: L1PostMessage},
		{Name: ToolAddReaction, Description: "Add an emoji reaction as the bot to a message in an allowed channel. A reaction already there counts as done.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["channel","timestamp","name"],"properties":{
"channel":` + channelArg + `,
"timestamp":{"type":"string","pattern":` + js(tsPattern) + `,"description":"The message's ts."},
"name":{"type":"string","pattern":` + js(emojiPattern) + `,"maxLength":100,"description":"The emoji name, like eyes or white_check_mark."}}}`),
			SideEffect: connector.SideEffectWrite, Idempotent: true, L1: L1AddReaction},
	}
}

// js is s as a JSON string.
func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
