package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
	"github.com/deedglass/deedglass/internal/vault"
)

var reCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// notionError is Notion's error object.
type notionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *notionError) Error() string { return "notion: " + e.Code }

func errorBody(b []byte) notionError {
	var ne notionError
	_ = json.Unmarshal(b, &ne)
	if !reCode.MatchString(ne.Code) {
		ne.Code = ""
	}
	if len(ne.Message) > 300 {
		ne.Message = ne.Message[:300] + "…"
	}
	return ne
}

func requestID(r *saas.Response) string {
	if r == nil {
		return ""
	}
	id := r.Header.Get("X-Request-Id")
	if len(id) > 80 || strings.ContainsAny(id, " \r\n") {
		return ""
	}
	return id
}

// classify maps Notion's answers to connector errors.
func classify(r *saas.Response) *connector.Error {
	if r.Status < 400 {
		return nil
	}
	ne := errorBody(r.Body)
	e := &connector.Error{HTTPStatus: r.Status, Err: &ne}
	detail := fmt.Sprintf("Notion answered %d %s", r.Status, ne.Code)
	if id := requestID(r); id != "" {
		detail += " for request " + id
	}
	if ne.Message != "" {
		detail += ": " + ne.Message
	}
	e.AdminDetail = detail
	switch {
	case r.Status == http.StatusUnauthorized:
		e.Kind, e.Code = connector.KindAuth, connector.CodeUpstreamAuthFailed
		e.AgentMessage = "Notion refused the connector's token. Nothing was changed. An admin can replace it in Console › Connectors."
	case r.Status == http.StatusForbidden:
		e.Kind, e.Code = connector.KindAuth, connector.CodeUpstreamAuthFailed
		e.AgentMessage = "The integration isn't allowed to do this in Notion. Nothing was changed. An admin can give it Insert content, or share the page with it."
	case r.Status == http.StatusNotFound:
		e.Kind, e.Code = connector.KindNotFound, connector.CodeUpstreamNotFound
		e.AgentMessage = "Notion has no page by that id that the integration can see. Check the id, or share the page with the integration."
	case r.Status == http.StatusTooManyRequests:
		e.Kind, e.Code, e.Retryable = connector.KindRateLimited, connector.CodeUpstreamRateLimited, true
		e.RetryAfter = r.RetryAfter(time.Minute)
		e.AgentMessage = "Notion is limiting requests. Nothing was changed. Wait, then send the call again."
		if e.RetryAfter > 0 {
			e.AgentMessage = fmt.Sprintf("Notion is limiting requests. Nothing was changed. Wait %s, then send the call again.", e.RetryAfter.Round(time.Second))
		}
	case r.Status >= 500:
		e.Kind, e.Code = connector.KindUpstream, connector.CodeUpstreamError
		e.AgentMessage = "Notion failed while answering. The call may have run."
	default: // 400, 409: Notion refused it, so nothing was applied
		e.Kind, e.Code = connector.KindToolError, connector.CodeUpstreamError
		e.AgentMessage = "Notion refused the request"
		if ne.Code != "" {
			e.AgentMessage += " (" + ne.Code + ")"
		}
		e.AgentMessage += ". Nothing was changed. Check the arguments."
	}
	return e
}

// session is one opened token and a client that sends it.
type session struct {
	st  *state
	sec *vault.Secret
	cli saas.Client
}

func (c *Connector) open(ctx context.Context, inst *connector.Instance) (*session, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	sec, err := inst.Secret(ctx)
	if err != nil {
		return nil, err
	}
	tok := sec.Field(FieldToken)
	if tok == "" {
		sec.Destroy()
		return nil, &connector.Error{Kind: connector.KindCredentialsMissing, Code: connector.CodeCredentialsMissing,
			AgentMessage: "This Notion connector has no token yet. An admin can store one in Console › Connectors.",
			AdminDetail:  fmt.Sprintf("Store a token: deedglass connector credentials set %s --from-env NOTION_TOKEN.", inst.Name)}
	}
	s := &session{st: st, sec: sec}
	s.cli = saas.Client{Base: st.base, HTTP: c.http(inst),
		Headers:  http.Header{"Accept": {"application/json"}, "Notion-Version": {st.cfg.APIVersion}},
		Classify: classify, MaxBody: maxBody, Sleep: c.sleep}
	s.cli.Auth = func(_ context.Context, r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+sec.Field(FieldToken))
		return nil
	}
	return s, nil
}

func (s *session) close() {
	if s != nil && s.sec != nil {
		s.sec.Destroy()
	}
}

// do sends one request and decodes the answer into out.
func (s *session) do(ctx context.Context, r saas.Request, out any) (*saas.Response, error) {
	resp, err := s.cli.Do(ctx, r)
	if err != nil {
		return resp, err
	}
	if out != nil {
		dec := json.NewDecoder(bytes.NewReader(resp.Body))
		dec.UseNumber()
		if err := dec.Decode(out); err != nil {
			return resp, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, HTTPStatus: resp.Status,
				AgentMessage: "Notion answered with something Deedglass couldn't read. The call may have run.", Err: err}
		}
	}
	return resp, nil
}

func (s *session) get(ctx context.Context, path string, out any) (*saas.Response, error) {
	return s.do(ctx, saas.Request{Method: http.MethodGet, Path: path, Idempotent: true}, out)
}

// ---- Notion objects (only the fields Deedglass reads) ----

type richText struct {
	PlainText string `json:"plain_text"`
	Text      *struct {
		Content string `json:"content"`
	} `json:"text"`
}

func plain(rt []richText) string {
	var b strings.Builder
	for _, r := range rt {
		switch {
		case r.PlainText != "":
			b.WriteString(r.PlainText)
		case r.Text != nil:
			b.WriteString(r.Text.Content)
		}
	}
	return b.String()
}

type parent struct {
	Type         string `json:"type"`
	PageID       string `json:"page_id"`
	DatabaseID   string `json:"database_id"`
	DataSourceID string `json:"data_source_id"`
	BlockID      string `json:"block_id"`
}

// id is the parent's id, normalized, or "workspace".
func (p parent) id() string {
	for _, s := range []string{p.PageID, p.BlockID, p.DatabaseID, p.DataSourceID} {
		if id := NormalizeID(s); id != "" {
			return id
		}
	}
	if p.Type != "" {
		return p.Type
	}
	return "unknown"
}

type page struct {
	Object     string `json:"object"`
	ID         string `json:"id"`
	URL        string `json:"url"`
	InTrash    bool   `json:"in_trash"`
	Archived   bool   `json:"archived"`
	Parent     parent `json:"parent"`
	Properties map[string]struct {
		Type  string     `json:"type"`
		Title []richText `json:"title"`
	} `json:"properties"`
	Title []richText `json:"title"` // data sources and databases carry their title here
}

func (p page) title() string {
	if len(p.Title) > 0 {
		return plain(p.Title)
	}
	for _, v := range p.Properties {
		if v.Type == "title" {
			return plain(v.Title)
		}
	}
	return ""
}

func (p page) trashed() bool { return p.InTrash || p.Archived }

type block struct {
	Object    string `json:"object"`
	ID        string `json:"id"`
	Type      string `json:"type"`
	InTrash   bool   `json:"in_trash"`
	Archived  bool   `json:"archived"`
	Paragraph *struct {
		RichText []richText `json:"rich_text"`
	} `json:"paragraph"`
}

func (b block) text() string {
	if b.Paragraph == nil {
		return ""
	}
	return plain(b.Paragraph.RichText)
}

type blockList struct {
	Results    []block `json:"results"`
	HasMore    bool    `json:"has_more"`
	NextCursor *string `json:"next_cursor"`
}
