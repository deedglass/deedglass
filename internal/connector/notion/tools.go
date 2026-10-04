package notion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
)

// Tool names (unqualified).
const (
	ToolSearch      = "search"
	ToolCreatePage  = "create_page"
	ToolAppendBlock = "append_block"
)

// L1 sentences: set on the writes, so the Witness runs NativeReadBack.
const (
	L1CreatePage  = "Notion shows the page under the same parent with the same title, not in the trash, and its paragraphs in order."
	L1AppendBlock = "Notion shows each appended block on the page with the same text, not in the trash."
)

const idPattern = `^([0-9a-fA-F]{32}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`

func toolSpecs() []connector.ToolSpec {
	obj := func(req []string, props string) json.RawMessage {
		r, _ := json.Marshal(req)
		return json.RawMessage(`{"type":"object","additionalProperties":false,"required":` + string(r) + `,"properties":{` + props + `}}`)
	}
	paragraphs := `"paragraphs":{"type":"array","items":{"type":"string","maxLength":2000},"description":"Plain paragraphs, at most 2,000 characters each."}`
	return []connector.ToolSpec{
		{Name: ToolSearch, Description: "Search the pages shared with the integration by title (at most 20 results).",
			InputSchema: obj([]string{}, `"query":{"type":"string","maxLength":200},"limit":{"type":"integer","minimum":1,"maximum":20,"description":"At most 20; default 10."}`),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolCreatePage, Description: "Create a page under one of the connector's parent pages, with a title and plain paragraphs.",
			InputSchema: obj([]string{"parent_page_id", "title"}, `"parent_page_id":{"type":"string","pattern":`+js(idPattern)+`,"description":"One of the connector's parent pages."},"title":{"type":"string","maxLength":200},`+paragraphs),
			SideEffect:  connector.SideEffectWrite, L1: L1CreatePage},
		{Name: ToolAppendBlock, Description: "Append plain paragraphs to the end of a page that is a parent page or sits directly under one.",
			InputSchema: obj([]string{"page_id", "paragraphs"}, `"page_id":{"type":"string","pattern":`+js(idPattern)+`},`+paragraphs),
			SideEffect:  connector.SideEffectWrite, L1: L1AppendBlock},
	}
}

func specFor(name string) (connector.ToolSpec, bool) {
	for _, t := range toolSpecs() {
		if t.Name == name {
			return t, true
		}
	}
	return connector.ToolSpec{}, false
}

func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// args is every tool's arguments (the schemas are closed).
type args struct {
	Query        string   `json:"query"`
	Limit        int      `json:"limit"`
	ParentPageID string   `json:"parent_page_id"`
	PageID       string   `json:"page_id"`
	Title        string   `json:"title"`
	Paragraphs   []string `json:"paragraphs"`
}

func validation(param, msg string) *connector.Error {
	return &connector.Error{Kind: connector.KindInvalidRequest, Code: connector.CodeValidationFailed, AgentMessage: msg,
		AdminDetail: param + ": " + msg}
}

// decodeArgs reads and checks the arguments against the config. Nothing is sent when it refuses.
func decodeArgs(st *state, tool string, raw json.RawMessage) (args, error) {
	var a args
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&a); err != nil {
		return a, connector.ErrValidation("args", "can't be read: "+strings.TrimPrefix(err.Error(), "json: "))
	}
	for i, p := range a.Paragraphs {
		if utf8.RuneCountInString(p) > MaxParagraph {
			return a, validation(fmt.Sprintf("args.paragraphs[%d]", i), "A paragraph is at most 2,000 characters. Split it into several.")
		}
	}
	if len(a.Paragraphs) > st.cfg.MaxBlocks {
		return a, validation("args.paragraphs", fmt.Sprintf("Send at most %d paragraphs in one call (the connector's max_blocks).", st.cfg.MaxBlocks))
	}
	switch tool {
	case ToolSearch:
		if utf8.RuneCountInString(a.Query) > MaxQuery {
			return a, validation("args.query", "The query is at most 200 characters.")
		}
		if a.Limit == 0 {
			a.Limit = 10
		}
	case ToolCreatePage:
		id := NormalizeID(a.ParentPageID)
		if id == "" || !st.parents[id] {
			return a, validation("args.parent_page_id", "This page isn't one of the connector's parent pages, so Deedglass won't create a page under it. Nothing was sent. An admin can add it to parents in the connector config.")
		}
		a.ParentPageID = id
		a.Title = strings.TrimSpace(a.Title)
		if a.Title == "" || utf8.RuneCountInString(a.Title) > MaxTitle {
			return a, validation("args.title", "Send a title of 1 to 200 characters.")
		}
	case ToolAppendBlock:
		id := NormalizeID(a.PageID)
		if id == "" {
			return a, validation("args.page_id", "Send a Notion page id: 32 hex characters, with or without dashes.")
		}
		a.PageID = id
		if len(a.Paragraphs) == 0 {
			return a, validation("args.paragraphs", "Send at least one paragraph.")
		}
	}
	return a, nil
}

// paragraphBlocks builds Notion paragraph blocks of plain text.
func paragraphBlocks(ps []string) []map[string]any {
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, map[string]any{"object": "block", "type": "paragraph",
			"paragraph": map[string]any{"rich_text": []map[string]any{{"type": "text", "text": map[string]any{"content": p}}}}})
	}
	return out
}
