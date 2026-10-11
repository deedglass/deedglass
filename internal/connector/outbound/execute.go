//go:build outboundmail

package outbound

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// CodeOutsideSendWindow is the failed code when the clock is outside args.window (S40 §5.5).
const CodeOutsideSendWindow = "outside_send_window"

// CodeHistoryExpired is the failed code when Gmail no longer has the history asked for.
const CodeHistoryExpired = "history_expired"

// Execute implements connector.Connector. mail.send runs its guards in this order, each before any
// byte leaves: the From guard, the window, the body hash, the MIME build; then one POST, never
// retried.
func (c *Connector) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	st, err := c.parse(inst)
	if err != nil {
		return connector.Result{}, err
	}
	if st.tripped.Load() {
		return connector.Result{}, disabledErr(inst)
	}
	if call.Tool.Name == ToolSend {
		// The From guard first: a config that names a denied mailbox disables the connector.
		if DeniedDomain(st.cfg.Mailbox, st.cfg.denied()) || (st.cfg.Email != "" && DeniedDomain(st.cfg.Email, st.cfg.denied())) {
			trip(ctx, inst, st, "from_denied", MsgFromDenied)
			return connector.Result{}, disabledErr(inst)
		}
	}
	s, err := c.open(ctx, inst)
	if err != nil {
		return connector.Result{}, err
	}
	defer s.close()
	switch call.Tool.Name {
	case ToolSend:
		return s.send(ctx, call)
	case ToolGetMessage:
		var a readArgs
		_ = json.Unmarshal(call.Args, &a)
		v, resp, err := s.getMessage(ctx, a.ID)
		if err != nil {
			return connector.Result{}, err
		}
		return result(resp, v.public(), nil)
	case ToolListHistory:
		var a readArgs
		_ = json.Unmarshal(call.Args, &a)
		return s.history(ctx, a.StartHistoryID)
	case ToolListMessages:
		var a readArgs
		_ = json.Unmarshal(call.Args, &a)
		if !ValidQuery(a.Q) {
			return connector.Result{}, validation("args.q", "Use one of: in:inbox newer_than:<1..14>d, in:sent after:YYYY/MM/DD before:YYYY/MM/DD, rfc822msgid:<id>.")
		}
		ids, resp, err := s.list(ctx, a.Q, 50)
		if err != nil {
			return connector.Result{}, err
		}
		return result(resp, map[string]any{"messages": ids}, nil)
	case ToolGetThread:
		var a readArgs
		_ = json.Unmarshal(call.Args, &a)
		return s.thread(ctx, a.ID)
	}
	return connector.Result{}, connector.ErrValidation("tool", "isn't an outbound_mail tool")
}

func result(resp *saas.Response, out any, ev map[string]string) (connector.Result, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return connector.Result{}, err
	}
	r := connector.Result{Output: b, Evidence: ev}
	if resp != nil {
		r.Meta, r.Latency = map[string]any{"http_status": resp.Status}, resp.Latency
	}
	return r, nil
}

func (s *session) message(call connector.Call, a SendArgs) Message {
	dom := s.st.cfg.SendingDomain
	m := Message{FromName: s.st.cfg.FromName, From: s.st.cfg.Mailbox, To: a.To, Subject: a.Subject, BodyText: a.BodyText, Date: now(),
		MessageID: MessageID(call.ActionID.String(), s.inst.ID.String(), dom),
		UnsubURL:  s.st.cfg.UnsubscribeBase + a.Unsubscribe.Token, UnsubMailto: "mailto:u+" + a.Unsubscribe.Token + "@" + dom + "?subject=unsubscribe"}
	if a.Thread != nil {
		m.InReplyTo, m.References = a.Thread.InReplyTo, a.Thread.References
	}
	return m
}

func (s *session) send(ctx context.Context, call connector.Call) (connector.Result, error) {
	a, verr := checkSend(call.Args)
	if verr != nil {
		return connector.Result{}, verr
	}
	if t := now(); s.st.cfg.RequireWindow && (t.Before(a.Window.NotBefore) || t.After(a.Window.NotAfter)) {
		return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: CodeOutsideSendWindow,
			AgentMessage: "It's outside the send window (" + windowText(a.Window) + "). Nothing was sent."}
	}
	m := s.message(call, a)
	raw, err := Build(m)
	if err != nil {
		if errors.Is(err, ErrInjection) {
			return connector.Result{}, validation("args.subject", "A header value holds a line break. Nothing was sent.")
		}
		return connector.Result{}, validation("args", "The message can't be built: "+err.Error()+". Nothing was sent.")
	}
	body := map[string]any{"raw": base64.URLEncoding.EncodeToString(raw)}
	if a.Thread != nil {
		body["threadId"] = a.Thread.GmailThreadID
	}
	var b struct {
		ID       string   `json:"id"`
		ThreadID string   `json:"threadId"`
		LabelIDs []string `json:"labelIds"`
	}
	resp, err := s.call(ctx, http.MethodPost, nil, body, &b, "messages", "send")
	if err != nil {
		return connector.Result{}, err
	}
	out := map[string]any{"id": b.ID, "thread_id": b.ThreadID, "label_ids": b.LabelIDs, "message_id": m.MessageID}
	return result(resp, out, map[string]string{"gmail_id": b.ID, "thread_id": b.ThreadID, "message_id": m.MessageID})
}

func windowText(w SendWindow) string {
	if w.TZ == "" {
		return w.NotBefore.UTC().Format("15:04") + " to " + w.NotAfter.UTC().Format("15:04") + " UTC"
	}
	return w.NotBefore.Format("15:04") + " to " + w.NotAfter.Format("15:04") + " " + w.TZ
}

func (s *session) list(ctx context.Context, q string, max int) ([]map[string]string, *saas.Response, error) {
	var b struct {
		Messages []struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"messages"`
	}
	resp, err := s.call(ctx, http.MethodGet, url.Values{"q": {q}, "maxResults": {itoa(max)}}, nil, &b, "messages")
	if err != nil {
		return nil, resp, err
	}
	out := []map[string]string{}
	for i, m := range b.Messages {
		if i == max {
			break
		}
		out = append(out, map[string]string{"id": m.ID, "thread_id": m.ThreadID})
	}
	return out, resp, nil
}

func (s *session) history(ctx context.Context, start string) (connector.Result, error) {
	var b struct {
		History []struct {
			MessagesAdded []struct {
				Message struct {
					ID       string `json:"id"`
					ThreadID string `json:"threadId"`
				} `json:"message"`
			} `json:"messagesAdded"`
		} `json:"history"`
		HistoryID string `json:"historyId"`
	}
	resp, err := s.call(ctx, http.MethodGet, url.Values{"startHistoryId": {start}, "historyTypes": {"messageAdded"}, "labelId": {"INBOX"}, "maxResults": {"100"}},
		nil, &b, "history")
	if err != nil {
		var ce *connector.Error
		if errors.As(err, &ce) && ce.Kind == connector.KindNotFound {
			return connector.Result{}, &connector.Error{Kind: connector.KindNotFound, Code: CodeHistoryExpired,
				AgentMessage: "Gmail no longer has history from that point. List recent inbox messages instead."}
		}
		return connector.Result{}, err
	}
	out := []map[string]string{}
	for _, h := range b.History {
		for _, m := range h.MessagesAdded {
			if len(out) == 100 {
				break
			}
			out = append(out, map[string]string{"id": m.Message.ID, "thread_id": m.Message.ThreadID})
		}
	}
	return result(resp, map[string]any{"messages": out, "history_id": b.HistoryID}, nil)
}

func (s *session) thread(ctx context.Context, id string) (connector.Result, error) {
	var b struct {
		Messages []struct {
			ID       string   `json:"id"`
			LabelIDs []string `json:"labelIds"`
			Payload  struct {
				Headers []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"headers"`
			} `json:"payload"`
		} `json:"messages"`
	}
	resp, err := s.call(ctx, http.MethodGet, url.Values{"format": {"metadata"}}, nil, &b, "threads", id)
	if err != nil {
		return connector.Result{}, err
	}
	keep := map[string]bool{"from": true, "to": true, "subject": true, "date": true, "message-id": true, "in-reply-to": true}
	out := []map[string]any{}
	for i, m := range b.Messages {
		if i == 20 {
			break
		}
		h := map[string]string{}
		for _, x := range m.Payload.Headers {
			if k := strings.ToLower(x.Name); keep[k] {
				h[k] = clipBytes(x.Value, 998)
			}
		}
		out = append(out, map[string]any{"id": m.ID, "label_ids": m.LabelIDs, "headers": h})
	}
	return result(resp, map[string]any{"thread_id": id, "messages": out}, nil)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
