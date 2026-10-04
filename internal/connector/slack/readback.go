package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
)

// NativeReadBack implements connector.NativeReadBacker (S29 §3.3): one read per call. A post is
// read with conversations.history at exactly its ts (with metadata); a reaction with
// reactions.get. A message Slack doesn't show is KindNotFound (the Witness tries again); a 429 is
// an error (a gap, never a mismatch).
func (c *Connector) NativeReadBack(ctx context.Context, inst *connector.Instance, call connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	switch call.Tool.Name {
	case ToolPostMessage, ToolAddReaction:
	default:
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the Slack tool " + call.Tool.Name + " isn't read back"}
	}
	a, err := decodeArgs(call.Args)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	ev := evidence(res)
	if !reChannelID.MatchString(ev["channel"]) || !reTS.MatchString(ev["ts"]) {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the call's answer has no channel and ts to read back"}
	}
	s, err := open(ctx, inst)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	defer s.close()
	if call.Tool.Name == ToolPostMessage {
		return s.readBackPost(ctx, call, a, ev)
	}
	return s.readBackReaction(ctx, a, ev)
}

// evidence is the call's channel and ts: Evidence first, the output as a fallback.
func evidence(res connector.Result) map[string]string {
	ev := map[string]string{}
	for k, v := range res.Evidence {
		ev[k] = v
	}
	if ev["channel"] == "" || ev["ts"] == "" {
		var o struct {
			Channel   string `json:"channel"`
			TS        string `json:"ts"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(res.Output, &o) == nil {
			ev["channel"] = o.Channel
			ev["ts"] = o.TS
			if ev["ts"] == "" {
				ev["ts"] = o.Timestamp
			}
		}
	}
	return ev
}

// readBackError words a read-back failure: a rate limit is said as such.
func readBackError(err error) error {
	if ce := connector.AsError(err); ce != nil && ce.Kind == connector.KindRateLimited {
		ce.AgentMessage = "Slack rate-limited the read-back."
	}
	return err
}

func (s *session) readBackPost(ctx context.Context, call connector.Call, a args, ev map[string]string) (connector.Observation, []connector.L1Finding, error) {
	ch, ts := ev["channel"], ev["ts"]
	obs := connector.Observation{Target: fmt.Sprintf("the message in %s on Slack", s.channelName(ch))}
	q := url.Values{"channel": {ch}, "oldest": {ts}, "latest": {ts}, "inclusive": {"true"}, "limit": {"1"}, "include_all_metadata": {"true"}}
	var b struct {
		Messages []struct {
			TS       string `json:"ts"`
			Text     string `json:"text"`
			ThreadTS string `json:"thread_ts"`
			Metadata struct {
				EventType    string `json:"event_type"`
				EventPayload struct {
					ActionID string `json:"action_id"`
				} `json:"event_payload"`
			} `json:"metadata"`
		} `json:"messages"`
	}
	t0 := time.Now()
	resp, err := s.get(ctx, "conversations.history", q, false, &b)
	obs.LatencyMS = time.Since(t0).Milliseconds()
	if resp != nil {
		obs.Status = resp.Status
	}
	if err != nil {
		return obs, nil, readBackError(err)
	}
	if len(b.Messages) == 0 || b.Messages[0].TS != ts {
		return obs, nil, &connector.Error{Kind: connector.KindNotFound, Code: connector.CodeUpstreamNotFound,
			AgentMessage: "Slack doesn't show the message at that ts."}
	}
	m := b.Messages[0]
	posted := ev["posted_channel"]
	if posted == "" {
		posted = ch
	}
	fs := []connector.L1Finding{
		connector.Compare("text", unescape(a.Text), unescape(m.Text)),
		connector.Compare("channel", ch, posted),
		connector.Compare("metadata.action_id", call.ActionID.String(), observedActionID(m.Metadata.EventType, m.Metadata.EventPayload.ActionID)),
	}
	if a.ThreadTS != "" {
		fs = append(fs, connector.Compare("thread_ts", a.ThreadTS, m.ThreadTS))
	}
	obs.Meta = map[string]any{"slack_method": "conversations.history", "attempts": resp.Attempts}
	return obs, fs, nil
}

// observedActionID is the action id a message's metadata carries, only under Deedglass's event
// type.
func observedActionID(eventType, id string) string {
	if eventType != "deedglass_action" {
		return ""
	}
	return id
}

func (s *session) readBackReaction(ctx context.Context, a args, ev map[string]string) (connector.Observation, []connector.L1Finding, error) {
	ch, ts := ev["channel"], ev["ts"]
	name := a.Name
	if r := ev["reaction"]; r != "" {
		name = r
	}
	obs := connector.Observation{Target: fmt.Sprintf("the :%s: reaction in %s on Slack", name, s.channelName(ch))}
	me, err := s.whoami(ctx, false)
	if err != nil {
		return obs, nil, readBackError(err)
	}
	var b struct {
		Message struct {
			Reactions []struct {
				Name  string   `json:"name"`
				Users []string `json:"users"`
			} `json:"reactions"`
		} `json:"message"`
	}
	t0 := time.Now()
	resp, err := s.get(ctx, "reactions.get", url.Values{"channel": {ch}, "timestamp": {ts}, "full": {"true"}}, false, &b)
	obs.LatencyMS = time.Since(t0).Milliseconds()
	if resp != nil {
		obs.Status = resp.Status
	}
	if err != nil {
		return obs, nil, readBackError(err)
	}
	observed := ""
	for _, r := range b.Message.Reactions {
		if r.Name != name {
			continue
		}
		for _, u := range r.Users {
			if u == me.UserID && u != "" {
				observed = r.Name
			}
		}
	}
	obs.Meta = map[string]any{"slack_method": "reactions.get", "attempts": resp.Attempts}
	return obs, []connector.L1Finding{connector.Compare("reaction", name, observed)}, nil
}

// unescape undoes the three entities Slack stores (&amp; last, so "&amp;lt;" stays "&lt;").
func unescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	return strings.ReplaceAll(s, "&amp;", "&")
}
