package notion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// NativeReadBack implements connector.NativeReadBacker: GETs of what the write made, compared in
// code. The text compared is plain text (rich formatting isn't compared).
func (c *Connector) NativeReadBack(ctx context.Context, inst *connector.Instance, call connector.Call, res connector.Result) (connector.Observation, []connector.L1Finding, error) {
	st, err := parse(inst)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	a, err := decodeArgs(st, call.Tool.Name, call.Args)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	s, err := c.open(ctx, inst)
	if err != nil {
		return connector.Observation{}, nil, err
	}
	defer s.close()
	switch call.Tool.Name {
	case ToolCreatePage:
		return s.readBackPage(ctx, a, res.Evidence["page"])
	case ToolAppendBlock:
		return s.readBackBlocks(ctx, a, res.Evidence["blocks"])
	}
	return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the Notion tool " + call.Tool.Name + " isn't read back"}
}

func (s *session) readOne(ctx context.Context, obs *connector.Observation, path string, out any) error {
	t0 := time.Now()
	resp, err := s.get(ctx, path, out)
	obs.LatencyMS += time.Since(t0).Milliseconds()
	if resp != nil {
		obs.Status = resp.Status
		if obs.Meta == nil {
			obs.Meta = map[string]any{}
		}
		obs.Meta["attempts"] = resp.Attempts
	}
	return err
}

// digestOf is a digest of paragraphs in order (each one's UTF-8 bytes, newline-terminated).
func digestOf(ps []string) string {
	h := sha256.New()
	for _, p := range ps {
		h.Write([]byte(normText(p)))
		h.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// normText is how text is compared: CRLF as LF, trailing spaces of each line dropped.
func normText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

func (s *session) readBackPage(ctx context.Context, a args, id string) (connector.Observation, []connector.L1Finding, error) {
	if NormalizeID(id) == "" {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the call's answer names no page to read back"}
	}
	obs := connector.Observation{Target: "page " + id + " in Notion"}
	var p page
	if err := s.readOne(ctx, &obs, saas.MustSegments("v1", "pages", id), &p); err != nil {
		return obs, nil, err
	}
	fs := []connector.L1Finding{
		connector.Compare("title", a.Title, p.title()),
		connector.Compare("parent", a.ParentPageID, p.Parent.id()),
		connector.Compare("in_trash", false, p.trashed()),
	}
	var got []string
	cursor := ""
	for range 3 { // up to 300 children; a page Deedglass made has at most 100 paragraphs
		var bl blockList
		path := saas.MustSegments("v1", "blocks", id, "children")
		q := url.Values{"page_size": {"100"}}
		if cursor != "" {
			q.Set("start_cursor", cursor)
		}
		t0 := time.Now()
		resp, err := s.do(ctx, saas.Request{Method: "GET", Path: path, Query: q, Idempotent: true}, &bl)
		obs.LatencyMS += time.Since(t0).Milliseconds()
		if resp != nil {
			obs.Status = resp.Status
		}
		if err != nil {
			return obs, nil, err
		}
		for _, b := range bl.Results {
			if b.Type == "paragraph" && !b.InTrash && !b.Archived {
				got = append(got, b.text())
			}
		}
		if !bl.HasMore || bl.NextCursor == nil {
			break
		}
		cursor = *bl.NextCursor
	}
	fs = append(fs, connector.Compare("paragraphs", digestOf(a.Paragraphs), digestOf(got)))
	return obs, fs, nil
}

func (s *session) readBackBlocks(ctx context.Context, a args, ids string) (connector.Observation, []connector.L1Finding, error) {
	list := []string{}
	if ids != "" {
		list = strings.Split(ids, ",")
	}
	if len(list) == 0 {
		return connector.Observation{}, nil, &connector.ErrReadBackSkipped{Why: "the call's answer names no block to read back"}
	}
	obs := connector.Observation{Target: "blocks appended to page " + a.PageID + " in Notion"}
	fs := []connector.L1Finding{connector.Compare("blocks", len(a.Paragraphs), len(list))}
	for i, id := range list {
		if i >= len(a.Paragraphs) {
			break
		}
		var b block
		if err := s.readOne(ctx, &obs, saas.MustSegments("v1", "blocks", id), &b); err != nil {
			return obs, nil, err
		}
		field := "blocks[" + strconv.Itoa(i) + "]"
		fs = append(fs,
			connector.Compare(field+".text", normText(a.Paragraphs[i]), normText(b.text())),
			connector.Compare(field+".in_trash", false, b.InTrash || b.Archived))
	}
	return obs, fs, nil
}
