package harness

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// SSEEvent is one event of a text/event-stream body (S34's SSE reader, P08).
type SSEEvent struct {
	ID, Event, Data string
}

// ParseSSE splits a stream body into events: fields per the WHATWG rules the Console reads
// (id, event, data; comments and retry ignored; an event ends at a blank line).
func ParseSSE(body []byte) []SSEEvent {
	var out []SSEEvent
	var cur SSEEvent
	has := false
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		if line == "" {
			if has {
				out = append(out, cur)
			}
			cur, has = SSEEvent{}, false
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		v = strings.TrimPrefix(v, " ")
		switch k {
		case "id":
			cur.ID, has = v, true
		case "event":
			cur.Event, has = v, true
		case "data":
			if cur.Data != "" {
				cur.Data += "\n"
			}
			cur.Data, has = cur.Data+v, true
		}
	}
	if has {
		out = append(out, cur)
	}
	return out
}

// SlackSign is Slack's v0 request signature (S34's Slack signer, P03 and P04):
// "v0=" + hex(HMAC-SHA256(secret, "v0:" + ts + ":" + body)).
func SlackSign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("v0:" + strconv.FormatInt(ts, 10) + ":"))
	m.Write(body)
	return "v0=" + hex.EncodeToString(m.Sum(nil))
}

// SlackForm is an interaction body as Slack sends it: payload=<url-encoded JSON>.
func SlackForm(payload any) []byte {
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return []byte("payload=" + url.QueryEscape(string(b)))
}

// SlackClick is a block_actions payload: user clicks action on the card with value
// "<approval id>|<context digest>".
func SlackClick(team, app, user, action, value, responseURL string) map[string]any {
	return map[string]any{"type": "block_actions", "team": map[string]any{"id": team}, "api_app_id": app,
		"user": map[string]any{"id": user, "team_id": team}, "trigger_id": "1.2.attack", "response_url": responseURL,
		"actions": []map[string]any{{"action_id": action, "value": value}}}
}

// HasControl reports a raw ESC or another C0 control (other than tab, newline and carriage
// return) in b (R07's sinks).
func HasControl(b []byte) bool {
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' || c == 0x7f {
			return true
		}
	}
	return false
}
