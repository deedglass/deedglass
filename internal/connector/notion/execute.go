package notion

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// MsgParentRefused is append_block's refusal for a page outside the parents.
const MsgParentRefused = "This page isn't one of the connector's parent pages or directly under one, so Deedglass won't write to it. Nothing was sent. An admin can add its parent to parents in the connector config."

// Execute implements connector.Connector.
func (c *Connector) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	st, err := parse(inst)
	if err != nil {
		return connector.Result{}, err
	}
	a, err := decodeArgs(st, call.Tool.Name, call.Args)
	if err != nil {
		return connector.Result{}, err
	}
	s, err := c.open(ctx, inst)
	if err != nil {
		return connector.Result{}, err
	}
	defer s.close()
	switch call.Tool.Name {
	case ToolSearch:
		return s.search(ctx, a)
	case ToolCreatePage:
		return s.createPage(ctx, a)
	case ToolAppendBlock:
		return s.appendBlock(ctx, a)
	}
	return connector.Result{}, &connector.Error{Kind: connector.KindInvalidRequest, Code: "tool_unknown",
		AgentMessage: "The Notion connector has no tool " + call.Tool.Name + "."}
}

func meta(resp *saas.Response) map[string]any {
	m := map[string]any{}
	if resp != nil {
		m["http_status"] = resp.Status
		m["attempts"] = resp.Attempts
		if id := requestID(resp); id != "" {
			m["notion_request_id"] = id
		}
	}
	return m
}

func output(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

type found struct {
	ID     string `json:"id"`
	Object string `json:"object"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Parent string `json:"parent"`
}

func (s *session) search(ctx context.Context, a args) (connector.Result, error) {
	body := map[string]any{"page_size": a.Limit}
	if a.Query != "" {
		body["query"] = a.Query
	}
	var out struct {
		Results []page `json:"results"`
		HasMore bool   `json:"has_more"`
	}
	// A search changes nothing, so it may be sent twice.
	resp, err := s.do(ctx, saas.Request{Method: http.MethodPost, Path: saas.MustSegments("v1", "search"), JSON: body, Idempotent: true}, &out)
	if err != nil {
		return connector.Result{Meta: meta(resp)}, err
	}
	rows := make([]found, 0, len(out.Results))
	for _, p := range out.Results {
		if len(rows) == a.Limit {
			break
		}
		rows = append(rows, found{ID: NormalizeID(p.ID), Object: p.Object, Title: p.title(), URL: p.URL, Parent: p.Parent.id()})
	}
	return connector.Result{Output: output(map[string]any{"results": rows, "has_more": out.HasMore || len(out.Results) > a.Limit}), Meta: meta(resp)}, nil
}

func (s *session) createPage(ctx context.Context, a args) (connector.Result, error) {
	body := map[string]any{
		"parent":     map[string]any{"type": "page_id", "page_id": a.ParentPageID},
		"properties": map[string]any{"title": map[string]any{"title": []map[string]any{{"type": "text", "text": map[string]any{"content": a.Title}}}}},
	}
	if len(a.Paragraphs) > 0 {
		body["children"] = paragraphBlocks(a.Paragraphs)
	}
	var p page
	// Notion has no idempotency key: a create is sent once, and a lost answer is unknown.
	resp, err := s.do(ctx, saas.Request{Method: http.MethodPost, Path: saas.MustSegments("v1", "pages"), JSON: body}, &p)
	if err != nil {
		return connector.Result{Meta: meta(resp)}, err
	}
	id := NormalizeID(p.ID)
	if id == "" {
		return connector.Result{Meta: meta(resp)}, &connector.Error{Kind: connector.KindUpstream, Code: connector.CodeUpstreamError, HTTPStatus: resp.Status,
			AgentMessage: "Notion answered without a page id. The page may have been created."}
	}
	return connector.Result{Output: output(map[string]any{"id": id, "url": p.URL, "title": a.Title, "paragraphs": len(a.Paragraphs)}),
		Meta: meta(resp), Evidence: map[string]string{"page": id}}, nil
}

// allowedPage reads the page and reports whether Deedglass may append to it: it is a parent, or
// its parent is one. A page in the trash is refused.
func (s *session) allowedPage(ctx context.Context, id string) (page, error) {
	var p page
	if _, err := s.get(ctx, saas.MustSegments("v1", "pages", id), &p); err != nil {
		return p, err
	}
	if !s.st.parents[id] && !s.st.parents[p.Parent.id()] {
		return p, validation("args.page_id", MsgParentRefused)
	}
	if p.trashed() {
		return p, validation("args.page_id", "This page is in Notion's trash. Nothing was sent. Restore it in Notion first.")
	}
	return p, nil
}

func (s *session) appendBlock(ctx context.Context, a args) (connector.Result, error) {
	if _, err := s.allowedPage(ctx, a.PageID); err != nil {
		return connector.Result{}, err
	}
	body := map[string]any{"children": paragraphBlocks(a.Paragraphs), "position": map[string]any{"type": "end"}}
	var out blockList
	resp, err := s.do(ctx, saas.Request{Method: http.MethodPatch, Path: saas.MustSegments("v1", "blocks", a.PageID, "children"), JSON: body}, &out)
	if err != nil {
		return connector.Result{Meta: meta(resp)}, err
	}
	ids := make([]string, 0, len(out.Results))
	for _, b := range out.Results {
		if id := NormalizeID(b.ID); id != "" {
			ids = append(ids, id)
		}
	}
	// Notion answers the page's first page of children; the appended blocks are its last ones.
	if len(ids) >= len(a.Paragraphs) {
		ids = ids[len(ids)-len(a.Paragraphs):]
	}
	return connector.Result{Output: output(map[string]any{"page_id": a.PageID, "block_ids": ids}), Meta: meta(resp),
		Evidence: map[string]string{"page": a.PageID, "blocks": strings.Join(ids, ",")}}, nil
}
