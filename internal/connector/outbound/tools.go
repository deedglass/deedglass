//go:build outboundmail

package outbound

import (
	"encoding/json"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
)

// Tool names (unqualified; the connector is named mail, so mail.send).
const (
	ToolSend         = "send"
	ToolGetMessage   = "get_message"
	ToolListHistory  = "list_history"
	ToolListMessages = "list_messages"
	ToolGetThread    = "get_thread"
)

// L1Send is the read-back's sentence.
const L1Send = "Gmail's Sent copy has the same recipient, subject, body, From and unsubscribe headers."

const patID = `^[A-Za-z0-9_-]{1,128}$`

// sendSchema: the top level is closed; recipient, signal, checks and review are open objects the
// connector ignores and the policy reads.
const sendSchema = `{"type":"object","additionalProperties":false,
"required":["to","subject","body_text","body_sha256","step","unsubscribe","window","message_ref"],
"properties":{
"to":{"type":"string","maxLength":254,"description":"One recipient address, no display name."},
"subject":{"type":"string","maxLength":120},
"body_text":{"type":"string","maxLength":16384,"description":"The plain-text body, exactly as approved."},
"body_sha256":{"type":"string","pattern":"^sha256:[0-9a-f]{64}$","description":"sha256 of the normalised body."},
"step":{"type":"integer","minimum":1,"maximum":3},
"kind":{"type":"string","maxLength":32},
"thread":{"description":"null for a first email; {gmail_thread_id, in_reply_to, references} for a threaded step."},
"unsubscribe":{"type":"object"},
"window":{"type":"object"},
"message_ref":{"type":"string","maxLength":64},
"template_hash":{"type":"string","maxLength":80},
"arm":{"type":"string","maxLength":16},
"exp":{"type":"string","maxLength":64},
"unit_ref":{"type":"string","maxLength":64},
"w_ppm":{"type":"array","items":{"type":"integer"}},
"snapshot_digest":{"type":"string","maxLength":80},
"recipient":{"type":"object"},
"signal":{"type":"object"},
"checks":{"type":"object"},
"review":{"type":"object"}}}`

func toolSpecs() []connector.ToolSpec {
	return []connector.ToolSpec{
		{Name: ToolSend, Description: "Send one plain-text email from the mailbox, inside its window, exactly as approved. Never retried.",
			InputSchema: json.RawMessage(sendSchema), SideEffect: connector.SideEffectWrite, L1: L1Send},
		{Name: ToolGetMessage, Description: "Read one message: headers, the plain-text body (up to 16 KiB) and bounce fields.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","pattern":` + js(patID) + `}}}`),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolListHistory, Description: "List messages added to the inbox since a history id (at most 100).",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["start_history_id"],"properties":{"start_history_id":{"type":"string","pattern":"^[0-9]{1,20}$"}}}`),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolListMessages, Description: "List message ids for one of three queries: in:inbox newer_than:<1..14>d, in:sent after:YYYY/MM/DD before:YYYY/MM/DD, rfc822msgid:<id>.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["q"],"properties":{"q":{"type":"string","maxLength":300}}}`),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolGetThread, Description: "Read a thread's headers: up to 20 messages, no bodies.",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["id"],"properties":{"id":{"type":"string","pattern":` + js(patID) + `}}}`),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
	}
}

func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// SendArgs are mail.send's arguments that the connector reads.
type SendArgs struct {
	To          string     `json:"to"`
	Subject     string     `json:"subject"`
	BodyText    string     `json:"body_text"`
	BodySHA256  string     `json:"body_sha256"`
	Step        int        `json:"step"`
	Kind        string     `json:"kind"`
	Thread      *Thread    `json:"thread"`
	Unsubscribe Unsub      `json:"unsubscribe"`
	Window      SendWindow `json:"window"`
	MessageRef  string     `json:"message_ref"`
}

// Thread is a threaded step's reply fields (S41's steps 2 and 3).
type Thread struct {
	GmailThreadID string   `json:"gmail_thread_id"`
	InReplyTo     string   `json:"in_reply_to"`
	References    []string `json:"references"`
}

// Unsub is the unsubscribe token.
type Unsub struct {
	Token string `json:"token"`
}

// SendWindow is the recipient's morning the send must fall in.
type SendWindow struct {
	NotBefore      time.Time `json:"not_before"`
	NotAfter       time.Time `json:"not_after"`
	TZ             string    `json:"tz"`
	LocalStartHour int       `json:"local_start_hour"`
	LocalEndHour   int       `json:"local_end_hour"`
	LocalWeekday   string    `json:"local_weekday"`
	ISTDate        string    `json:"ist_date"`
}

// The other tools' arguments.
type readArgs struct {
	ID             string `json:"id"`
	StartHistoryID string `json:"start_history_id"`
	Q              string `json:"q"`
}

var (
	reToken = regexp.MustCompile(`^[a-z2-7]{32}$`)
	reMsgID = regexp.MustCompile(`^<[^<>\s]{1,250}>$`)
	reReFwd = regexp.MustCompile(`(?i)^\s*(re|fwd?|aw|sv)\s*:`)
)

func validation(param, msg string) *connector.Error {
	return &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeValidationFailed, AgentMessage: msg, AdminDetail: param + ": " + msg}
}

// checkSend applies every mail.send rule that needs no clock and no network.
func checkSend(raw json.RawMessage) (SendArgs, *connector.Error) {
	var a SendArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, validation("args", "The mail.send arguments can't be read. Check them against the tool's schema.")
	}
	if strings.ContainsAny(a.To, "\r\n,;") {
		return a, validation("args.to", "Send exactly one recipient address, with no line break, comma or display name.")
	}
	ad, err := mail.ParseAddress(a.To)
	if err != nil || ad.Name != "" || ad.Address != strings.TrimSpace(a.To) {
		return a, validation("args.to", "Send exactly one recipient address, with no line break, comma or display name.")
	}
	if strings.ContainsAny(a.Subject, "\r\n") || strings.TrimSpace(a.Subject) == "" || utf8.RuneCountInString(a.Subject) > MaxSubject {
		return a, validation("args.subject", "Send a subject of 1 to 120 characters on one line.")
	}
	if a.Thread == nil && reReFwd.MatchString(a.Subject) {
		return a, validation("args.subject", "A first email can't start with Re: or Fwd:. Only a real reply in a thread can.")
	}
	if len(a.BodyText) > MaxBodyBytes || !utf8.ValidString(a.BodyText) || strings.ContainsRune(a.BodyText, 0) ||
		strings.Contains(strings.ToLower(a.BodyText), "<html") {
		return a, validation("args.body_text", "Send a plain-text body of at most 16 KiB of UTF-8, with no HTML.")
	}
	if BodySHA256(a.BodyText) != a.BodySHA256 {
		return a, validation("args.body_sha256", "The body doesn't match body_sha256, so nothing was sent. A changed body needs a new draft and a new approval.")
	}
	if !reToken.MatchString(a.Unsubscribe.Token) {
		return a, validation("args.unsubscribe.token", "Send the recipient's unsubscribe token: 32 characters of a-z and 2-7.")
	}
	if a.Window.NotBefore.IsZero() || a.Window.NotAfter.IsZero() || !a.Window.NotBefore.Before(a.Window.NotAfter) {
		return a, validation("args.window", "Send window.not_before and window.not_after as RFC 3339 times, not_before first.")
	}
	if t := a.Thread; t != nil {
		if !regexp.MustCompile(patID).MatchString(t.GmailThreadID) || !reMsgID.MatchString(t.InReplyTo) || len(t.References) > 20 {
			return a, validation("args.thread", "Send thread.gmail_thread_id, thread.in_reply_to as <message-id> and at most 20 references.")
		}
		for _, r := range t.References {
			if !reMsgID.MatchString(r) {
				return a, validation("args.thread.references", "Each reference is one <message-id>.")
			}
		}
	}
	return a, nil
}

// CheckArgs implements connector.ArgsChecker: mail.send's rules run before the schema, so the
// agent hears the exact fix and nothing is sent.
func (c *Connector) CheckArgs(t connector.ToolSpec, raw json.RawMessage) error {
	switch t.Name {
	case ToolSend:
		if _, err := checkSend(raw); err != nil {
			return err
		}
	case ToolListMessages:
		var a readArgs
		if json.Unmarshal(raw, &a) == nil && a.Q != "" && !ValidQuery(a.Q) {
			return validation("args.q", "Use one of: in:inbox newer_than:<1..14>d, in:sent after:YYYY/MM/DD before:YYYY/MM/DD, rfc822msgid:<id>.")
		}
	}
	return nil
}
