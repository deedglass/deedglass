package slack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/witness"
)

// Action ids on the card's buttons. POST /v1/slack/interact routes on them.
const (
	CardActionApprove = "deedglass_approve"
	CardActionDeny    = "deedglass_deny"
	CardActionOpen    = "deedglass_open"
	// DenyCallbackID is the deny modal's callback_id; DenyReasonBlockID and DenyReasonActionID name its input.
	DenyCallbackID     = "deedglass_deny"
	DenyReasonBlockID  = "reason"
	DenyReasonActionID = "reason"
	cardBlockActions   = "deedglass_actions"
	cardArgsMaxRunes   = 1200 // the arguments block shows at most this many characters
	cardArgsMaxBytes   = 4800 // and at most this many bytes once JSON-encoded (keeps the card under 16 KiB)
	cardCapTool        = 100
	cardCapAgent       = 80
	cardCapName        = 80
	cardCapRule        = 100
	cardCapReason      = 300
	cardCapOnBehalf    = 100
	cardCapSummary     = 400
	cardCapSentence    = 300
	cardCapDenyReason  = 400
	cardCapPolicy      = 80
	cardCapID          = 64
	cardMaxNames       = 3
)

// Card builds the Slack approval card for v: the Block Kit blocks array and the plain fallback text
// Slack shows in notifications. It is a pure function of v. Every interpolated string is escaped;
// the only URL on the card is v.ConsoleURL. The blocks always pass ValidateLimits.
func Card(v ApprovalView) (blocks json.RawMessage, text string, err error) {
	c := cardBuilder{v: v}
	bs := c.blocks()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // the text is already escaped for Slack; keep "&lt;" as written
	if err := enc.Encode(bs); err != nil {
		return nil, "", fmt.Errorf("slack card: %w", err)
	}
	blocks = bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if err := ValidateLimits(blocks); err != nil {
		return nil, "", err
	}
	return blocks, c.fallback(), nil
}

// DenyModal is the views.open view for Deny: one required reason, 1 to 280 characters (S22's
// limit), recorded with the decision. private_metadata carries "<approval id>|<context_digest>".
func DenyModal(approvalID, contextDigest string) json.RawMessage {
	min, max := 1, 280
	m := bkModal{
		Type:            "modal",
		CallbackID:      DenyCallbackID,
		Title:           bkPlain("Deny action"),
		Submit:          bkPlain("Deny"),
		Close:           bkPlain("Cancel"),
		PrivateMetadata: cardButtonValue(approvalID, contextDigest),
		Blocks: []any{bkInput{
			Type:    "input",
			BlockID: DenyReasonBlockID,
			Label:   bkPlain("Reason · recorded with the decision"),
			Element: bkTextInput{Type: "plain_text_input", ActionID: DenyReasonActionID, Multiline: true, MinLength: &min, MaxLength: &max},
		}},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(m) // fixed shapes and strings: cannot fail
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// Block Kit shapes the card writes. Field order is the JSON order.
type bkText struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji *bool  `json:"emoji,omitempty"`
}

type bkBlock struct {
	Type     string   `json:"type"`
	BlockID  string   `json:"block_id,omitempty"`
	Text     *bkText  `json:"text,omitempty"`
	Fields   []bkText `json:"fields,omitempty"`
	Elements []any    `json:"elements,omitempty"`
}

type bkButton struct {
	Type     string     `json:"type"`
	Text     bkText     `json:"text"`
	ActionID string     `json:"action_id"`
	Value    string     `json:"value,omitempty"`
	URL      string     `json:"url,omitempty"`
	Style    string     `json:"style,omitempty"`
	Confirm  *bkConfirm `json:"confirm,omitempty"`
}

type bkConfirm struct {
	Title   bkText `json:"title"`
	Text    bkText `json:"text"`
	Confirm bkText `json:"confirm"`
	Deny    bkText `json:"deny"`
	Style   string `json:"style,omitempty"`
}

type bkModal struct {
	Type            string `json:"type"`
	CallbackID      string `json:"callback_id"`
	Title           bkText `json:"title"`
	Submit          bkText `json:"submit"`
	Close           bkText `json:"close"`
	PrivateMetadata string `json:"private_metadata"`
	Blocks          []any  `json:"blocks"`
}

type bkInput struct {
	Type    string      `json:"type"`
	BlockID string      `json:"block_id"`
	Label   bkText      `json:"label"`
	Element bkTextInput `json:"element"`
}

type bkTextInput struct {
	Type      string `json:"type"`
	ActionID  string `json:"action_id"`
	Multiline bool   `json:"multiline"`
	MinLength *int   `json:"min_length,omitempty"`
	MaxLength *int   `json:"max_length,omitempty"`
}

var bkNoEmoji = false

// plain is a plain_text object with emoji shortcodes off (":skull:" stays text).
func bkPlain(s string) bkText { return bkText{Type: "plain_text", Text: s, Emoji: &bkNoEmoji} }

func bkMrkdwn(s string) bkText { return bkText{Type: "mrkdwn", Text: s} }

func bkSection(s string) bkBlock {
	t := bkMrkdwn(s)
	return bkBlock{Type: "section", Text: &t}
}

func bkContext(s string) bkBlock {
	return bkBlock{Type: "context", Elements: []any{bkMrkdwn(s)}}
}

type cardBuilder struct{ v ApprovalView }

func (c cardBuilder) tool() string  { return cardText(c.v.Tool, cardCapTool) }
func (c cardBuilder) agent() string { return cardText(c.v.Agent, cardCapAgent) }

func (c cardBuilder) blocks() []bkBlock {
	v := c.v
	bs := make([]bkBlock, 0, 10)
	bs = append(bs,
		bkBlock{Type: "header", Text: bkPtr(bkPlain(Cap(c.headerLead()+"\u00a0· "+c.tool(), limitHeaderText)))},
		bkSection(c.summary()),
	)
	switch v.State {
	case StatePending, StatePartial, "":
		bs = append(bs, bkBlock{Type: "section", Fields: c.fields()})
		bs = append(bs, c.args()...)
		if v.State == StatePartial {
			bs = append(bs, bkSection(c.partialLine()))
		}
		bs = append(bs, c.contextBlock())
		bs = append(bs, bkBlock{Type: "actions", BlockID: cardBlockActions, Elements: c.decisionButtons()})
	default:
		bs = append(bs, bkSection(c.finalLines()))
		bs = append(bs, c.contextBlock())
		if open, ok := c.openButton(); ok {
			bs = append(bs, bkBlock{Type: "actions", BlockID: cardBlockActions, Elements: []any{open}})
		}
	}
	return bs
}

func bkPtr[T any](t T) *T { return &t }

// summary is the one sentence Deedglass wrote about the call (or the fallback), as mrkdwn: strong
// parts bold, code parts in code spans, every part escaped.
func (c cardBuilder) summary() string {
	s := c.v.Summary
	if len(s) == 0 {
		s = connector.FallbackSummary(c.v.Agent, c.v.Tool)
	}
	var b strings.Builder
	for _, p := range s {
		switch p.Style {
		case connector.SummaryStrong:
			t := cardText(strings.NewReplacer("*", "").Replace(p.Text), cardCapSummary)
			if t != "" {
				b.WriteString("*" + t + "*")
			}
		case connector.SummaryCode:
			if strings.TrimSpace(p.Text) != "" {
				b.WriteString(escCodeSpan(p.Text, cardCapSummary))
			}
		default:
			// Plain runs keep their edge spaces ("wants to run "): escOneLine trims, so pad back.
			t := cardText(p.Text, cardCapSummary)
			if strings.HasPrefix(p.Text, " ") {
				t = " " + t
			}
			if strings.HasSuffix(p.Text, " ") && t != " " {
				t += " "
			}
			b.WriteString(t)
		}
	}
	return Cap(b.String(), cardCapSummary*2)
}

func (c cardBuilder) fields() []bkText {
	v := c.v
	fs := make([]bkText, 0, 6)
	add := func(label, value string) {
		if value != "" {
			fs = append(fs, bkMrkdwn("*"+label+"*\n"+value))
		}
	}
	if strings.TrimSpace(v.RuleID) != "" {
		add("Rule", escCodeSpan(v.RuleID, cardCapRule))
	}
	add("Reason", cardText(v.Reason, cardCapReason))
	add("On behalf of", cardText(v.OnBehalfOf, cardCapOnBehalf))
	if !v.ExpiresAt.IsZero() {
		add("Expires", cardDate(v.ExpiresAt))
	}
	switch {
	case v.Required > 0 && v.Approved == 0:
		add("Approvals", fmt.Sprintf("%d needed", v.Required))
	case v.Required > 0:
		add("Approvals", fmt.Sprintf("%d of %d needed", v.Approved, v.Required))
	}
	if strings.TrimSpace(v.RequestDigest) != "" {
		add("Request", escCodeSpan(cardShortDigest(v.RequestDigest), cardCapID))
	}
	if len(fs) == 0 {
		fs = append(fs, bkMrkdwn("*Rule*\nNone recorded"))
	}
	return fs
}

// args is the labelled, fenced arguments block, and a note when the arguments were cut.
func (c cardBuilder) args() []bkBlock {
	raw := bytes.TrimSpace(c.v.Args)
	if len(raw) == 0 {
		return nil
	}
	pretty := string(raw)
	var buf bytes.Buffer
	if json.Valid(raw) && json.Indent(&buf, raw, "", "  ") == nil {
		pretty = cardUnescapeHTML(buf.String())
	}
	body, more := cardFenceBody(pretty)
	out := []bkBlock{
		bkContext("_Values below come from the agent's request._"),
		bkSection("```\n" + body + "\n```"),
	}
	if more > 0 {
		out = append(out, bkContext("… "+cardGroupDigits(more)+" more characters. The full request is in the Console."))
	}
	return out
}

// cardUnescapeHTML turns the JSON escapes \u003c, \u003e and \u0026 (encoding/json's HTML
// escaping) back into <, > and &, so the person sees the characters the agent sent; the fence then
// escapes them for Slack. An escaped backslash ("\\u003c") is left alone. s is valid JSON.
func cardUnescapeHTML(s string) string {
	if !strings.Contains(s, `\u00`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		if s[i+1] == 'u' && i+5 < len(s) {
			switch strings.ToLower(s[i+2 : i+6]) {
			case "003c":
				b.WriteByte('<')
				i += 5
				continue
			case "003e":
				b.WriteByte('>')
				i += 5
				continue
			case "0026":
				b.WriteByte('&')
				i += 5
				continue
			}
		}
		b.WriteByte(c)
		b.WriteByte(s[i+1]) // the escaped character, so "\\" is consumed as a pair
		i++
	}
	return b.String()
}

// cardFenceBody escapes s for a code block (Slack syntax escaped, adjacent backticks split) and cuts it
// at cardArgsMaxRunes characters or cardArgsMaxBytes encoded bytes, whichever comes first. more is how
// many characters of s were left out.
func cardFenceBody(s string) (body string, more int) {
	var b strings.Builder
	b.Grow(min(len(s), cardArgsMaxBytes) + 16)
	runes, bytesOut := 0, 0
	prevTick := false
	for i, r := range s {
		var piece string
		switch r {
		case '&':
			piece = "&amp;"
		case '<':
			piece = "&lt;"
		case '>':
			piece = "&gt;"
		case '`':
			if prevTick {
				piece = escZWSP + "`"
			} else {
				piece = "`"
			}
		default:
			piece = ""
		}
		var n, w int // characters and encoded bytes this rune adds
		if piece != "" {
			n, w = utf8.RuneCountInString(piece), len(piece)
		} else {
			n, w = 1, cardJSONWidth(r)
		}
		if runes+n > cardArgsMaxRunes || bytesOut+w > cardArgsMaxBytes {
			return strings.TrimRight(b.String(), " \n"), utf8.RuneCountInString(s[i:])
		}
		if piece != "" {
			b.WriteString(piece)
		} else {
			b.WriteRune(r)
		}
		runes += n
		bytesOut += w
		prevTick = r == '`'
	}
	return b.String(), 0
}

// cardJSONWidth is how many bytes encoding/json (HTML escaping off) writes for r inside a string.
func cardJSONWidth(r rune) int {
	switch {
	case r == '"' || r == '\\' || r == '\n' || r == '\r' || r == '\t':
		return 2
	case r < 0x20 || r == ' ' || r == ' ' || r == utf8.RuneError:
		return 6
	}
	return utf8.RuneLen(r)
}

func (c cardBuilder) partialLine() string {
	v := c.v
	var b strings.Builder
	fmt.Fprintf(&b, "◐ %d of %d approvals", v.Approved, v.Required)
	if len(v.Countersigned) > 0 {
		b.WriteString(" · ")
		shown := v.Countersigned
		if len(shown) > cardMaxNames {
			shown = shown[:cardMaxNames]
		}
		for i, cs := range shown {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(cardText(cs.Name, cardCapName) + " approved")
			if !cs.At.IsZero() {
				b.WriteString(" " + cardDate(cs.At))
			}
		}
		if extra := len(v.Countersigned) - len(shown); extra > 0 {
			fmt.Fprintf(&b, ", and %d more", extra)
		}
		b.WriteString(".")
	}
	switch left := v.Required - v.Approved; {
	case left == 1:
		b.WriteString(" Waiting for one more person.")
	case left > 1:
		fmt.Fprintf(&b, " Waiting for %d more people.", left)
	}
	return b.String()
}

// finalLines are the one or two quiet lines that replace the fields, arguments and buttons once
// the approval is decided.
func (c cardBuilder) finalLines() string {
	v := c.v
	switch v.State {
	case StateApproved:
		return c.approvedLine() + "\nRunning now."
	case StateSealed:
		return c.approvedLine() + "\n" + c.sealLine()
	case StateDenied:
		line := "✕ Denied by " + c.decider() + c.when(v.DecidedAt)
		if r := cardText(v.DenyReason, cardCapDenyReason); r != "" {
			line += ` · "` + r + `"`
		}
		return line
	case StateExpired:
		if v.ExpiresAt.IsZero() {
			return "○ Expired. Nothing ran."
		}
		return "○ Expired " + cardDate(v.ExpiresAt) + ". Nothing ran."
	case StateCancelled:
		return "○ Cancelled by the agent. Nothing ran."
	}
	return "○ " + cardText(string(v.State), 40)
}

func (c cardBuilder) approvedLine() string {
	return "✓ Approved by " + c.approvers() + c.when(c.v.DecidedAt)
}

// approvers names who approved: everyone who countersigned (oldest first, up to three), else the
// person who made it final.
func (c cardBuilder) approvers() string {
	var names []string
	for _, cs := range c.v.Countersigned {
		if n := cardText(cs.Name, cardCapName); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return c.decider()
	}
	if d := cardText(c.v.DecidedBy, cardCapName); d != "" && names[len(names)-1] != d {
		names = append(names, d)
	}
	extra := 0
	if len(names) > cardMaxNames {
		extra = len(names) - cardMaxNames
		names = names[:cardMaxNames]
	}
	out := cardJoinNames(names)
	if extra > 0 {
		out += fmt.Sprintf(" and %d more", extra)
	}
	return out
}

func cardJoinNames(n []string) string {
	switch len(n) {
	case 1:
		return n[0]
	case 2:
		return n[0] + " and " + n[1]
	}
	return strings.Join(n[:len(n)-1], ", ") + " and " + n[len(n)-1]
}

func (c cardBuilder) decider() string {
	if d := cardText(c.v.DecidedBy, cardCapName); d != "" {
		return d
	}
	return "a person"
}

func (c cardBuilder) when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return " · " + cardDate(t)
}

// sealLine is the deed's line: the verdict, or the tool's failure. The deed id is in the context
// line below it.
func (c cardBuilder) sealLine() string {
	v := c.v
	level := ""
	if l := cardText(v.Level, 4); l != "" {
		level = witness.PlainLevel(l) + " · "
	}
	sentence := ""
	if s := cardText(v.Sentence, cardCapSentence); s != "" {
		sentence = " · " + s
	}
	switch {
	case v.Failed:
		return "✕ The tool failed. Nothing was applied."
	case v.Verdict == "VERIFIED":
		return "✓ Deed sealed · " + level + witness.PlainVerified
	case v.Verdict == "MISMATCH":
		return "✕ " + level + witness.PlainMismatch + sentence
	case v.Verdict == "UNVERIFIABLE":
		return "○ " + level + witness.PlainUnverifiable + sentence
	}
	return "Deed sealed."
}

// headerLead is "Approval needed" while a person can still decide, then "Approval request".
func (c cardBuilder) headerLead() string {
	switch c.v.State {
	case StatePending, StatePartial, "":
		return "Approval needed"
	}
	return "Approval request"
}

func (c cardBuilder) contextBlock() bkBlock {
	v := c.v
	var parts []string
	ids := []string{v.ActionID, v.ApprovalID}
	if v.State == StateSealed {
		ids = append([]string{v.DeedID}, ids...)
	}
	for _, id := range ids {
		if strings.TrimSpace(id) != "" {
			parts = append(parts, escCodeSpan(cardShortID(id), cardCapID))
		}
	}
	if p := cardText(v.PolicyName, cardCapPolicy); p != "" {
		pol := "policy " + p
		if v.PolicyVersion > 0 {
			pol += " v" + strconv.Itoa(v.PolicyVersion)
		}
		parts = append(parts, pol)
	}
	if len(parts) == 0 {
		parts = append(parts, "Deedglass")
	}
	return bkContext(strings.Join(parts, " · "))
}

func (c cardBuilder) decisionButtons() []any {
	v := c.v
	value := cardButtonValue(v.ApprovalID, v.ContextDigest)
	confirmText := "Deedglass will run " + cardPlainText(v.Tool, cardCapTool) + " as " + cardPlainText(v.Agent, cardCapAgent) + "."
	els := []any{
		bkButton{
			Type: "button", Text: bkPlain("Approve"), ActionID: CardActionApprove, Value: value, Style: "primary",
			Confirm: &bkConfirm{
				Title:   bkPlain("Approve this action?"),
				Text:    bkPlain(Cap(confirmText, limitConfirmText)),
				Confirm: bkPlain("Approve"),
				Deny:    bkPlain("Cancel"),
				Style:   "primary",
			},
		},
		bkButton{Type: "button", Text: bkPlain("Deny"), ActionID: CardActionDeny, Value: value, Style: "danger"},
	}
	if open, ok := c.openButton(); ok {
		els = append(els, open)
	}
	return els
}

// cardPlainText is an agent string for a plain_text object: one line, escaped, capped. No escDefuse:
// plain_text never renders links, and emoji are off on every plain_text the card writes.
func cardPlainText(s string, max int) string { return Cap(Escape(escOneLine(s)), max) }

// openButton links to the approval in the Console. The URL is Deedglass's own (http or https,
// with a host); anything else and the card has no link.
func (c cardBuilder) openButton() (bkButton, bool) {
	u := strings.TrimSpace(c.v.ConsoleURL)
	p, err := url.Parse(u)
	if u == "" || err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || len(u) > limitButtonURL {
		return bkButton{}, false
	}
	return bkButton{Type: "button", Text: bkPlain("Open in Console"), ActionID: CardActionOpen, URL: u}, true
}

// fallback is the plain notification text: "Approval needed · github.create_pr · support-bot".
func (c cardBuilder) fallback() string {
	v := c.v
	head := "Approval needed"
	switch v.State {
	case StatePartial:
		head = fmt.Sprintf("Approval needed (%d of %d)", v.Approved, v.Required)
	case StateApproved:
		head = "Approved"
	case StateSealed:
		head = "Deed sealed"
		switch {
		case v.Failed:
			head = "The tool failed"
		case v.Verdict == "MISMATCH" || v.Verdict == "UNVERIFIABLE":
			head = "Deed sealed, " + strings.ToLower(witness.PlainVerdict(v.Verdict))
		}
	case StateDenied:
		head = "Denied"
	case StateExpired:
		head = "Expired"
	case StateCancelled:
		head = "Cancelled"
	}
	parts := []string{head}
	if t := c.tool(); t != "" {
		parts = append(parts, t)
	}
	if a := c.agent(); a != "" {
		parts = append(parts, a)
	}
	return strings.Join(parts, " · ")
}

// cardButtonValue is "<approval id>|<context_digest>": what the person saw is what they decide on.
func cardButtonValue(approvalID, contextDigest string) string {
	return Cap(escOneLine(approvalID), cardCapID) + "|" + Cap(escOneLine(contextDigest), 200)
}

// cardDate is a date token each viewer sees in their own time zone ("today at 4:44 AM"), with
// the UTC time as the fallback for clients that can't render it.
func cardDate(t time.Time) string {
	return "<!date^" + strconv.FormatInt(t.Unix(), 10) + "^{date_short_pretty} at {time}|" + t.UTC().Format("2006-01-02 15:04 UTC") + ">"
}

// cardShortID is "deed_01j9…8j8k" (brand §12): the prefix, then the first and last four characters
// of the ULID. Buttons and metadata always carry the full id.
func cardShortID(id string) string {
	id = strings.TrimSpace(id)
	prefix, rest, ok := strings.Cut(id, "_")
	if !ok || len(rest) <= 10 || !cardIsAlnum(rest) {
		return id
	}
	return prefix + "_" + rest[:4] + "…" + rest[len(rest)-4:]
}

func cardIsAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// cardShortDigest is "sha256:3f9a…c21e" (brand §12): the first and last four hex characters.
func cardShortDigest(d string) string {
	d = strings.TrimSpace(d)
	algo, hex, ok := strings.Cut(d, ":")
	if !ok || len(hex) <= 8 || !cardIsHex(hex) {
		return d
	}
	return algo + ":" + hex[:4] + "…" + hex[len(hex)-4:]
}

func cardIsHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// cardGroupDigits writes n with thousands separators: 51234 → "51,234".
func cardGroupDigits(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
