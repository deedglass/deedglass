package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
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

// view is a draft as Gmail stores it, parsed from format=raw with net/mail.
type view struct {
	To, Cc   []string
	Subject  string
	BodyText string
	ThreadID string
}

func (s *session) readDraft(ctx context.Context, id string) (view, *saas.Response, error) {
	var b struct {
		Message struct {
			ThreadID string `json:"threadId"`
			Raw      string `json:"raw"`
		} `json:"message"`
	}
	resp, err := s.call(ctx, http.MethodGet, url.Values{"format": {"raw"}}, nil, &b, "drafts", id)
	if err != nil {
		return view{}, resp, err
	}
	raw, err := decodeRaw(b.Message.Raw)
	if err != nil {
		return view{}, resp, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError,
			AgentMessage: "Gmail answered with a draft Deedglass couldn't read."}
	}
	v, err := ParseRaw(raw)
	if err != nil {
		return view{}, resp, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError,
			AgentMessage: "Gmail answered with a draft Deedglass couldn't read."}
	}
	v.ThreadID = b.Message.ThreadID
	return v, resp, nil
}

func decodeRaw(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	return base64.RawURLEncoding.DecodeString(s)
}

// ParseRaw reads an RFC 5322 message: the recipients (lowercase addresses), the decoded subject
// and the plain-text body (the text/plain part of a multipart/alternative).
func ParseRaw(raw []byte) (view, error) {
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return view{}, err
	}
	var v view
	for _, h := range []struct {
		name string
		dst  *[]string
	}{{"To", &v.To}, {"Cc", &v.Cc}} {
		*h.dst = []string{}
		if m.Header.Get(h.name) == "" {
			continue
		}
		as, err := m.Header.AddressList(h.name)
		if err != nil {
			return view{}, err
		}
		for _, a := range as {
			*h.dst = append(*h.dst, strings.ToLower(a.Address))
		}
	}
	dec := new(mime.WordDecoder)
	v.Subject, err = dec.DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		v.Subject = m.Header.Get("Subject")
	}
	v.BodyText, err = textBody(m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), m.Body, 0)
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
	var body io.Reader = io.LimitReader(r, MaxRaw)
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, &stripNewlines{r: body})
	}
	b, err := io.ReadAll(body)
	return string(b), err
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

func (s *session) thread(ctx context.Context, id string) (connector.Result, error) {
	var b struct {
		Messages []struct {
			ID  string `json:"id"`
			Raw string `json:"raw"`
		} `json:"messages"`
	}
	resp, err := s.call(ctx, http.MethodGet, url.Values{"format": {"raw"}}, nil, &b, "threads", id)
	if err != nil {
		return connector.Result{}, err
	}
	out := []map[string]any{}
	for i, m := range b.Messages {
		if i == 20 {
			break
		}
		raw, err := decodeRaw(m.Raw)
		if err != nil {
			continue
		}
		v, err := ParseRaw(raw)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{"message_id": m.ID, "to": v.To, "cc": v.Cc, "subject": v.Subject, "body_text": clipBytes(v.BodyText, 16<<10)})
	}
	return result(resp, map[string]any{"thread_id": id, "messages": out}, nil)
}

// NativeReadBack implements connector.NativeReadBacker: the saved draft, read as raw and parsed
// with net/mail, against what the call asked for. Bodies compare as digests after NormalizeBody.
func (c *Connector) NativeReadBack(ctx context.Context, inst *connector.Instance, call connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	if call.Tool.Name != ToolCreateDraft && call.Tool.Name != ToolUpdateDraft {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the Gmail tool " + call.Tool.Name + " isn't read back"}
	}
	a, err := decodeArgs(call.Args)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	id := res.Evidence["draft_id"]
	if id == "" {
		id = a.DraftID
	}
	if !matchDraftID(id) {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the call's answer names no draft to read back"}
	}
	s, err := open(ctx, inst)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	defer s.close()
	obs := connector.Observation{Target: "draft " + id + " in Gmail"}
	t0 := time.Now()
	v, resp, err := s.readDraft(ctx, id)
	obs.LatencyMS = time.Since(t0).Milliseconds()
	if resp != nil {
		obs.Status = resp.Status
	}
	if err != nil {
		return obs, nil, err
	}
	fs := []connector.L1Finding{
		connector.Compare("to", addrSet(a.To), sorted(v.To)),
		connector.Compare("cc", addrSet(a.Cc), sorted(v.Cc)),
		connector.Compare("subject", a.Subject, v.Subject),
		connector.Compare("body_text", BodyDigest(a.BodyText), BodyDigest(v.BodyText)),
	}
	if a.ThreadID != "" {
		fs = append(fs, connector.Compare("thread_id", a.ThreadID, v.ThreadID))
	}
	return obs, fs, nil
}

func addrSet(in []string) []string {
	out := []string{}
	for _, s := range in {
		if a, err := mail.ParseAddress(s); err == nil {
			out = append(out, strings.ToLower(a.Address))
		}
	}
	return sorted(out)
}

func sorted(xs []string) []string {
	out := slices.Clone(xs)
	if out == nil {
		out = []string{}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
