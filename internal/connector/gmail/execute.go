package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// args is every tool's arguments.
type args struct {
	DraftID   string   `json:"draft_id"`
	To        []string `json:"to"`
	Cc        []string `json:"cc"`
	Bcc       []string `json:"bcc"`
	Subject   string   `json:"subject"`
	BodyText  string   `json:"body_text"`
	BodyHTML  string   `json:"body_html"`
	ThreadID  string   `json:"thread_id"`
	InReplyTo string   `json:"in_reply_to"`
	Limit     int      `json:"limit"`
}

func decodeArgs(raw json.RawMessage) (args, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, connector.ErrValidation("args", "can't be read")
	}
	return a, nil
}

func validation(param, msg string) *connector.Error {
	return &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeValidationFailed, AgentMessage: msg, AdminDetail: param + ": " + msg}
}

// CheckArgs implements connector.ArgsChecker: line breaks in headers are refused with the exact
// message before anything else runs.
func (c *Connector) CheckArgs(t connector.ToolSpec, raw json.RawMessage) error {
	if t.Name != ToolCreateDraft && t.Name != ToolUpdateDraft {
		return nil
	}
	a, err := decodeArgs(raw)
	if err != nil {
		return nil // the schema check words it
	}
	for _, v := range []struct{ p, s string }{{"args.subject", a.Subject}, {"args.in_reply_to", a.InReplyTo}} {
		if strings.ContainsAny(v.s, "\r\n") {
			return validation(v.p, MsgHeaderInjection)
		}
	}
	for _, l := range []struct {
		p  string
		xs []string
	}{{"args.to", a.To}, {"args.cc", a.Cc}, {"args.bcc", a.Bcc}} {
		for _, x := range l.xs {
			if strings.ContainsAny(x, "\r\n") {
				return validation(l.p, MsgHeaderInjection)
			}
		}
	}
	return nil
}

// draft parses and checks a draft's arguments against the config.
func (s *session) draft(a args) (Draft, *connector.Error) {
	var d Draft
	if a.InReplyTo != "" && (!strings.HasPrefix(a.InReplyTo, "<") || !strings.HasSuffix(a.InReplyTo, ">") || strings.ContainsAny(a.InReplyTo, " \t")) {
		return d, validation("args.in_reply_to", "Send in_reply_to as a Message-ID in angle brackets, like <abc@mail.gmail.com>.")
	}
	if len(a.Bcc) > 0 && !s.st.cfg.AllowBCC {
		return d, validation("args.bcc", "This connector doesn't allow Bcc. Put every recipient in to or cc, so whoever sends the draft sees them.")
	}
	var err error
	for _, l := range []struct {
		p   string
		in  []string
		out *[]*mail.Address
	}{{"args.to", a.To, &d.To}, {"args.cc", a.Cc, &d.Cc}, {"args.bcc", a.Bcc, &d.Bcc}} {
		if *l.out, err = ParseAddresses(l.p, l.in); err != nil {
			if errors.Is(err, ErrInjection) {
				return d, validation(l.p, MsgHeaderInjection)
			}
			return d, validation(l.p, "Each entry must be exactly one email address, like Priya <priya@acme.com>.")
		}
	}
	if len(d.To) == 0 {
		return d, validation("args.to", "Send at least one recipient in to.")
	}
	if strings.TrimSpace(a.Subject) == "" {
		return d, validation("args.subject", "Send a subject.")
	}
	if n := len(d.To) + len(d.Cc) + len(d.Bcc); n > s.st.cfg.MaxRecipients {
		return d, validation("args.to", "This draft has "+strconv.Itoa(n)+" recipients; the connector allows "+strconv.Itoa(s.st.cfg.MaxRecipients)+".")
	}
	d.Subject, d.BodyText, d.BodyHTML, d.InReplyTo = a.Subject, a.BodyText, a.BodyHTML, a.InReplyTo
	return d, nil
}

// Execute implements connector.Connector.
func (c *Connector) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	a, err := decodeArgs(call.Args)
	if err != nil {
		return connector.Result{}, err
	}
	s, err := open(ctx, inst)
	if err != nil {
		return connector.Result{}, err
	}
	defer s.close()
	switch call.Tool.Name {
	case ToolCreateDraft, ToolUpdateDraft:
		return s.writeDraft(ctx, call.Tool.Name, a)
	case ToolListDrafts:
		limit := a.Limit
		if limit < 1 || limit > 25 {
			limit = 10
		}
		var b struct {
			Drafts []struct {
				ID      string `json:"id"`
				Message struct {
					ThreadID string `json:"threadId"`
				} `json:"message"`
			} `json:"drafts"`
		}
		resp, err := s.call(ctx, http.MethodGet, url.Values{"maxResults": {strconv.Itoa(limit)}}, nil, &b, "drafts")
		if err != nil {
			return connector.Result{}, err
		}
		out := []map[string]string{}
		for _, d := range b.Drafts {
			out = append(out, map[string]string{"draft_id": d.ID, "thread_id": d.Message.ThreadID})
		}
		return result(resp, map[string]any{"drafts": out}, nil)
	case ToolGetDraft:
		v, resp, err := s.readDraft(ctx, a.DraftID)
		if err != nil {
			return connector.Result{}, err
		}
		return result(resp, map[string]any{"draft_id": a.DraftID, "to": v.To, "cc": v.Cc, "subject": v.Subject,
			"body_text": clipBytes(v.BodyText, 64<<10), "thread_id": v.ThreadID}, nil)
	case ToolGetThread:
		if !s.st.cfg.readonly() {
			return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown",
				AgentMessage: "gmail.get_thread needs the gmail.readonly scope, which this connector wasn't authorized with."}
		}
		return s.thread(ctx, a.ThreadID)
	}
	return connector.Result{}, connector.ErrValidation("tool", "isn't a Gmail tool")
}

func result(resp *saas.Response, out any, ev map[string]string) (connector.Result, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return connector.Result{}, err
	}
	return connector.Result{Output: b, Meta: map[string]any{"http_status": resp.Status}, Evidence: ev, Latency: resp.Latency}, nil
}

func (s *session) writeDraft(ctx context.Context, tool string, a args) (connector.Result, error) {
	d, verr := s.draft(a)
	if verr != nil {
		return connector.Result{}, verr
	}
	raw, err := Build(d)
	if err != nil {
		if errors.Is(err, ErrInjection) {
			return connector.Result{}, validation("args.subject", MsgHeaderInjection)
		}
		return connector.Result{}, validation("args", "The draft can't be built: "+err.Error()+".")
	}
	msg := map[string]any{"raw": base64.URLEncoding.EncodeToString(raw)}
	if a.ThreadID != "" {
		msg["threadId"] = a.ThreadID
	}
	var b struct {
		ID      string `json:"id"`
		Message struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"message"`
	}
	method, rest := http.MethodPost, []string{"drafts"}
	if tool == ToolUpdateDraft {
		method, rest = http.MethodPut, []string{"drafts", a.DraftID}
	}
	resp, err := s.call(ctx, method, nil, map[string]any{"message": msg}, &b, rest...)
	if err != nil {
		return connector.Result{}, err
	}
	out := map[string]any{"draft_id": b.ID, "message_id": b.Message.ID, "thread_id": b.Message.ThreadID,
		"note": "Saved as a draft. A person reviews and sends it from Gmail."}
	return result(resp, out, map[string]string{"draft_id": b.ID, "thread_id": b.Message.ThreadID})
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8Valid(s) {
		s = s[:len(s)-1]
	}
	return s
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }
