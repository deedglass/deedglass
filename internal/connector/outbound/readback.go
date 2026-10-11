//go:build outboundmail

package outbound

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// MaxRead is how much of a raw message is read.
const MaxRead = 64 << 10

// view is a message as Gmail stores it, parsed from format=raw with net/mail.
type view struct {
	ID, ThreadID       string
	Labels             []string
	From, To           string
	Subject            string
	BodyText           string
	MessageID          string
	InReplyTo          string
	ListUnsub          string
	ListUnsubPost      string
	Date               string
	DSN                map[string]string // multipart/report: the delivery-status fields
	BodyTruncatedBytes int
	// S41: what the reply triager needs. The auto-reply headers (RFC 3834 and vendor ones),
	// References and Delivered-To for matching our threads and mailto unsubscribes, and Gmail's
	// historyId to reset the triager's history point.
	Headers     map[string]string
	References  string
	DeliveredTo string
	HistoryID   string
}

// AutoHeaders are the auto-reply headers get_message returns (lowercase names).
var AutoHeaders = []string{"auto-submitted", "x-autoreply", "x-autorespond", "precedence", "x-auto-response-suppress"}

func (v view) public() map[string]any {
	out := map[string]any{"id": v.ID, "thread_id": v.ThreadID, "label_ids": v.Labels, "from": v.From, "to": v.To, "subject": v.Subject,
		"date": v.Date, "message_id": v.MessageID, "in_reply_to": v.InReplyTo, "body_text": clipBytes(v.BodyText, 16<<10)}
	if len(v.DSN) > 0 {
		out["dsn"] = v.DSN
	}
	if len(v.Headers) > 0 {
		out["headers"] = v.Headers
	}
	if v.References != "" {
		out["references"] = clipBytes(v.References, 998)
	}
	if v.DeliveredTo != "" {
		out["delivered_to"] = v.DeliveredTo
	}
	if v.HistoryID != "" {
		out["history_id"] = v.HistoryID
	}
	return out
}

func (s *session) getMessage(ctx context.Context, id string) (view, *saas.Response, error) {
	var b struct {
		ID        string   `json:"id"`
		ThreadID  string   `json:"threadId"`
		LabelIDs  []string `json:"labelIds"`
		Raw       string   `json:"raw"`
		HistoryID string   `json:"historyId"`
	}
	resp, err := s.call(ctx, http.MethodGet, url.Values{"format": {"raw"}}, nil, &b, "messages", id)
	if err != nil {
		return view{}, resp, err
	}
	raw, err := decodeRaw(b.Raw)
	if err != nil {
		return view{}, resp, unreadable()
	}
	if len(raw) > MaxRead {
		raw = raw[:MaxRead]
	}
	v, err := ParseRaw(raw)
	if err != nil {
		return view{}, resp, unreadable()
	}
	v.ID, v.ThreadID, v.Labels = b.ID, b.ThreadID, b.LabelIDs
	if isDigits(b.HistoryID) {
		v.HistoryID = b.HistoryID
	}
	return v, resp, nil
}

func unreadable() *connector.Error {
	return &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, AgentMessage: "Gmail answered with a message Deedglass couldn't read."}
}

func decodeRaw(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	return base64.RawURLEncoding.DecodeString(s)
}

// ParseRaw reads an RFC 5322 message: the From and To addresses (lowercase), the decoded subject,
// the plain-text body, the unsubscribe headers, Message-ID and, for a bounce, its status fields.
func ParseRaw(raw []byte) (view, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return view{}, err
	}
	var v view
	if a, err := mail.ParseAddress(m.Header.Get("From")); err == nil {
		v.From = strings.ToLower(a.Address)
	} else {
		v.From = strings.ToLower(strings.TrimSpace(m.Header.Get("From")))
	}
	if as, err := m.Header.AddressList("To"); err == nil && len(as) > 0 {
		v.To = strings.ToLower(as[0].Address)
		if len(as) > 1 {
			v.To += ",+" // more than one recipient never matches a send
		}
	}
	dec := new(mime.WordDecoder)
	v.Subject, err = dec.DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		v.Subject = m.Header.Get("Subject")
	}
	v.MessageID = strings.TrimSpace(m.Header.Get("Message-Id"))
	v.InReplyTo = strings.TrimSpace(m.Header.Get("In-Reply-To"))
	v.ListUnsub = strings.TrimSpace(m.Header.Get("List-Unsubscribe"))
	v.ListUnsubPost = strings.TrimSpace(m.Header.Get("List-Unsubscribe-Post"))
	v.Date = m.Header.Get("Date")
	v.References = strings.Join(strings.Fields(m.Header.Get("References")), " ")
	if a, err := mail.ParseAddress(m.Header.Get("Delivered-To")); err == nil {
		v.DeliveredTo = strings.ToLower(a.Address)
	}
	for _, h := range AutoHeaders {
		if val := strings.TrimSpace(m.Header.Get(h)); val != "" {
			if v.Headers == nil {
				v.Headers = map[string]string{}
			}
			v.Headers[h] = clipBytes(val, 200)
		}
	}
	ct := m.Header.Get("Content-Type")
	if mt, params, err := mime.ParseMediaType(ct); err == nil && mt == "multipart/report" {
		v.DSN = dsn(m.Body, params["boundary"])
		return v, nil
	}
	v.BodyText, err = textBody(ct, m.Header.Get("Content-Transfer-Encoding"), m.Body, 0)
	return v, err
}

func textBody(ctype, cte string, r io.Reader, depth int) (string, error) {
	mt, params, err := mime.ParseMediaType(ctype)
	if ctype == "" {
		mt, err = "text/plain", nil
	}
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(mt, "multipart/") && depth < 3 {
		mr := multipart.NewReader(r, params["boundary"])
		for {
			p, err := mr.NextRawPart()
			if err != nil {
				return "", nil
			}
			t, err := textBody(p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"), p, depth+1)
			if err == nil && t != "" {
				return t, nil
			}
		}
	}
	if mt != "text/plain" {
		return "", nil
	}
	var body io.Reader = io.LimitReader(r, MaxRead)
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, &stripNewlines{r: body})
	}
	b, err := io.ReadAll(body)
	return string(b), err
}

// dsn reads the message/delivery-status part of a bounce: Action, Status, Final-Recipient and
// Diagnostic-Code.
func dsn(r io.Reader, boundary string) map[string]string {
	out := map[string]string{}
	mr := multipart.NewReader(io.LimitReader(r, MaxRead), boundary)
	for {
		p, err := mr.NextPart()
		if err != nil {
			return out
		}
		ct := strings.ToLower(p.Header.Get("Content-Type"))
		if strings.HasPrefix(ct, "message/rfc822") || strings.HasPrefix(ct, "text/rfc822-headers") {
			// S41: the original's Message-ID, so a bounce matches the exact send.
			if id := originalMessageID(io.LimitReader(p, 16<<10)); id != "" && out["original_message_id"] == "" {
				out["original_message_id"] = clipBytes(id, 300)
			}
			continue
		}
		if !strings.HasPrefix(ct, "message/delivery-status") {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(p, 8<<10))
		for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
			k, val, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			switch k = strings.ToLower(strings.TrimSpace(k)); k {
			case "action", "status", "final-recipient", "diagnostic-code":
				out[strings.ReplaceAll(k, "-", "_")] = clipBytes(strings.TrimSpace(val), 300)
			}
		}
	}
}

type stripNewlines struct{ r io.Reader }

func (s *stripNewlines) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	j := 0
	for _, c := range p[:n] {
		if c != '\r' && c != '\n' {
			p[j] = c
			j++
		}
	}
	return j, err
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && strings.ToValidUTF8(s, "�") != s {
		s = s[:len(s)-1]
	}
	return s
}

// NativeReadBack implements connector.NativeReadBacker: Gmail's Sent copy, read as raw and parsed
// with net/mail, against what was approved. With an id from the send it reads that message; after
// an unknown outcome it first looks the message up by its Message-ID. A From on a denied domain is
// a violation and disables the connector.
func (c *Connector) NativeReadBack(ctx context.Context, inst *connector.Instance, call connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	if call.Tool.Name != ToolSend {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the mail tool " + call.Tool.Name + " isn't read back"}
	}
	a, verr := checkSend(call.Args)
	if verr != nil {
		return connector.Observation{}, nil, verr
	}
	s, err := c.open(ctx, inst)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	defer s.close()
	want := MessageID(call.ActionID.String(), inst.ID.String(), s.st.cfg.SendingDomain)
	id := res.Evidence["gmail_id"]
	obs := connector.Observation{}
	t0 := time.Now()
	if id == "" {
		ids, resp, err := s.list(ctx, "rfc822msgid:"+strings.Trim(want, "<>"), 5)
		if resp != nil {
			obs.Status = resp.Status
		}
		if err != nil {
			return obs, nil, err
		}
		if len(ids) == 0 {
			return obs, nil, &connector.Error{Kind: connector.KindNotFound, Code: connector.CodeUpstreamNotFound,
				AgentMessage: "Gmail's Sent folder has no message with this action's Message-ID."}
		}
		id = ids[0]["id"]
	}
	obs.Target = "message " + id + " in Gmail's Sent folder"
	v, resp, err := s.getMessage(ctx, id)
	obs.LatencyMS = time.Since(t0).Milliseconds()
	if resp != nil {
		obs.Status = resp.Status
	}
	if err != nil {
		return obs, nil, err
	}
	if DeniedDomain(v.From, s.st.cfg.denied()) {
		trip(ctx, inst, s.st, "from_denied", "Gmail's Sent copy shows a From on a refused domain. "+MsgFromDenied)
	}
	unsub := "<" + s.st.cfg.UnsubscribeBase + a.Unsubscribe.Token + ">"
	fs := []connector.L1Finding{
		connector.Compare("label", "SENT", sentLabel(v.Labels)),
		connector.Compare("to", strings.ToLower(a.To), v.To),
		connector.Compare("subject", a.Subject, v.Subject),
		connector.Compare("body_text", BodySHA256(a.BodyText), BodySHA256(v.BodyText)),
		connector.Compare("from", s.st.cfg.Mailbox, v.From),
		connector.Compare("list_unsubscribe", true, strings.Contains(v.ListUnsub, unsub)),
		connector.Compare("list_unsubscribe_post", "List-Unsubscribe=One-Click", v.ListUnsubPost),
	}
	// Gmail may rewrite the Message-ID; that is recorded, never a mismatch on its own.
	if v.MessageID != "" && v.MessageID != want {
		obs.Meta = map[string]any{"message_id_rewritten": true, "gmail_message_id": v.MessageID}
	}
	if a.Thread != nil {
		fs = append(fs, connector.Compare("thread_id", a.Thread.GmailThreadID, v.ThreadID))
	}
	return obs, fs, nil
}

func sentLabel(ls []string) string {
	if slices.Contains(ls, "SENT") {
		return "SENT"
	}
	return strings.Join(ls, ",")
}

var errNoProfile = errors.New("outbound: the profile has no email address")

// originalMessageID reads the Message-ID header of an attached original (headers only).
func originalMessageID(r io.Reader) string {
	b, _ := io.ReadAll(r)
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if l == "" {
			break
		}
		if k, val, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "message-id") {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func isDigits(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
