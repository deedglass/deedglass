package wrap

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/mcp/present"
	"github.com/deedglass/deedglass/internal/mcp/toolscan"
	"github.com/deedglass/deedglass/internal/toolname"
)

// The tool catalog (S14 §3.5): every tool's definition digest is pinned the first time it is seen;
// a later different digest marks the tool changed (hidden, calls refused until restart). Tools the
// policy always denies are hidden; deedglass.* names from the server are hidden; the wrapper's own
// deedglass.check_approval is appended to the first page.

// Visibility of a tool in the catalog.
const (
	visVisible = "visible"
	visHidden  = "hidden"
	visChanged = "changed"
	visFlagged = "flagged" // S34 (Q01): the definition's text carries instructions or hidden characters
)

// maxTTL caps a modern tools/list's ttlMs: the list is filtered per caller.
const maxTTL = 60000

// CatalogEntry is one tool the server listed.
type CatalogEntry struct {
	Name       string
	Digest     string // pinned: sha256 of JCS({name,title,description,inputSchema,outputSchema,annotations})
	Visibility string // visible | hidden | changed
	RuleID     string
	Reason     string
}

type catalog struct {
	w       *Wrapper
	mu      sync.Mutex
	entries map[string]*CatalogEntry
	seen    bool   // a tools/list answer has been processed
	hidden  string // the last hidden set, for the change log line
	policy  string
	warned  bool
}

func newCatalog(w *Wrapper) *catalog { return &catalog{w: w, entries: map[string]*CatalogEntry{}} }

var reMCPName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// toolDef is the part of a tool definition the digest covers.
var digestFields = []string{"name", "title", "description", "inputSchema", "outputSchema", "annotations"}

// Digest is a tool definition's digest.
func Digest(raw json.RawMessage) (name, digest string, ok bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return "", "", false
	}
	if json.Unmarshal(m["name"], &name) != nil || name == "" {
		return "", "", false
	}
	sub := map[string]json.RawMessage{}
	for _, f := range digestFields {
		if v, ok := m[f]; ok {
			sub[f] = v
		}
	}
	d, err := deed.DigestJSON(jsonrpc.MarshalObject(sub))
	if err != nil {
		return "", "", false
	}
	return name, d, true
}

func (c *catalog) invalidate() {} // every tools/list answer is resolved again; pins stay

func (c *catalog) get(name string) (*CatalogEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[name]
	if !ok {
		return nil, c.seen
	}
	cp := *e
	return &cp, c.seen
}

// process rewrites one tools/list result page: pin, resolve, filter, append, cache hints.
func (c *catalog) process(ctx context.Context, result json.RawMessage, modern, first bool) json.RawMessage {
	var res map[string]json.RawMessage
	if json.Unmarshal(result, &res) != nil {
		return result
	}
	var tools []json.RawMessage
	if json.Unmarshal(res["tools"], &tools) != nil {
		return result
	}
	type item struct {
		raw          json.RawMessage
		name, digest string
		ok           bool
	}
	items := make([]item, 0, len(tools))
	var refs []gate.ToolRef
	c.mu.Lock()
	for _, raw := range tools {
		name, d, ok := Digest(raw)
		it := item{raw: raw, name: name, digest: d, ok: ok}
		items = append(items, it)
		if !ok {
			continue
		}
		e := c.entries[name]
		if e != nil && e.Visibility == visFlagged && e.Digest != d {
			e = nil // a flagged definition was never pinned; its replacement is a first listing
		}
		f, flagged := toolscan.Flagged(raw)
		switch {
		case e == nil && flagged:
			c.entries[name] = &CatalogEntry{Name: name, Digest: d, Visibility: visFlagged}
			c.w.log.sym(symDenied, toolscan.Sentence(name, c.w.cfg.Connector, f), "hidden; calls are refused as an unknown tool")
		case e == nil:
			c.entries[name] = &CatalogEntry{Name: name, Digest: d, Visibility: visVisible}
		case e.Visibility == visFlagged:
		case e.Digest != d && e.Visibility != visChanged:
			e.Visibility, e.RuleID, e.Reason = visChanged, "", "the definition changed after it was first listed"
			c.w.log.sym(symDenied, c.w.cfg.Connector+"."+name+" changed its definition after it was first listed", "hidden until the MCP server restarts")
		}
		if v := c.entries[name].Visibility; v != visChanged && v != visFlagged && !present.Reserved(name) && reMCPName.MatchString(name) {
			refs = append(refs, gate.ToolRef{Name: name, Digest: c.entries[name].Digest})
		}
	}
	c.seen = true
	c.mu.Unlock()

	// Ask the Gate. When it can't answer, every tool stays visible: calls still fail closed.
	if len(refs) > 0 {
		vis, err := c.w.gc.Resolve(ctx, c.w.cfg.Connector, c.w.cfg.ConnectorType, refs)
		c.mu.Lock()
		if err != nil {
			if !c.warned {
				c.w.log.sym(symDenied, "could not ask Deedglass which tools to hide ("+shortErr(err)+")", "showing every tool; calls are still decided by the Gate")
				c.warned = true
			}
		} else {
			c.warned = false
			if vis.PolicyVersion != nil {
				c.policy = *vis.PolicyVersion
			}
			for _, v := range vis.Data {
				e := c.entries[v.Name]
				if e == nil || e.Visibility == visChanged || e.Visibility == visFlagged {
					continue
				}
				e.Visibility = visVisible
				if v.Visibility == gate.VisibilityHidden {
					e.Visibility = visHidden
				}
				e.RuleID, e.Reason = deref(v.RuleID), deref(v.Reason)
			}
		}
		c.mu.Unlock()
	}

	out := make([]json.RawMessage, 0, len(items)+1)
	c.mu.Lock()
	var hidden []string
	for _, it := range items {
		if it.ok && present.Reserved(it.name) {
			continue // the reserved prefix: a server can't pose as Deedglass
		}
		if it.ok {
			e := c.entries[it.name]
			if e.Visibility == visChanged || e.Visibility == visFlagged {
				continue
			}
			if e.Visibility == visHidden {
				hidden = append(hidden, c.w.cfg.Connector+"."+it.name)
				if c.w.cfg.HideDenied {
					continue
				}
			}
		}
		out = append(out, it.raw)
	}
	sort.Strings(hidden)
	set := strings.Join(hidden, " · ")
	changed := set != c.hidden
	c.hidden = set
	policy := c.policy
	c.mu.Unlock()
	if changed && len(hidden) > 0 {
		verb := "hidden by policy"
		if !c.w.cfg.HideDenied {
			verb = "always denied by policy (shown: --hide-denied=false)"
		}
		parts := []string{itoa(len(hidden)) + " tools " + verb}
		if len(hidden) == 1 {
			parts[0] = "1 tool " + verb
		}
		if policy != "" {
			parts = append(parts, "policy "+policy)
		}
		c.w.log.sym(symPending, parts[0], append(parts[1:], hidden...)...)
	}
	if first {
		out = append(out, present.CheckApprovalTool(toolname.Style(c.w.cfg.ToolNames)))
	}
	res["tools"] = jsonArray(out)
	if modern {
		ttl := int64(maxTTL)
		var up int64
		if v, ok := res["ttlMs"]; ok && json.Unmarshal(v, &up) == nil && up >= 0 && up < ttl {
			ttl = up
		}
		res["ttlMs"] = jsonrpc.Int(ttl)
		res["cacheScope"] = jsonrpc.String("private")
	}
	return jsonrpc.MarshalObject(res)
}

func jsonArray(items []json.RawMessage) json.RawMessage {
	var b strings.Builder
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	b.WriteByte(']')
	return json.RawMessage(b.String())
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// finishList answers a client's tools/list.
func (w *Wrapper) finishList(p *pending, e jsonrpc.Envelope) {
	raw, ok := e["result"]
	if !ok {
		w.reply(p, e)
		return
	}
	first := true
	var params struct {
		Cursor *string `json:"cursor"`
	}
	if len(p.params) > 0 && json.Unmarshal(p.params, &params) == nil && params.Cursor != nil && *params.Cursor != "" {
		first = false
	}
	out := cloneEnvelope(e)
	out["result"] = w.cat.process(w.ctx, raw, p.modern, first)
	w.reply(p, out)
}

// refresh lists the server's tools for the wrapper itself (a call before any tools/list).
func (w *Wrapper) refresh(ctx context.Context, modern bool) {
	var cursor string
	for page := 0; page < 20; page++ {
		params := map[string]json.RawMessage{}
		if cursor != "" {
			params["cursor"] = jsonrpc.String(cursor)
		}
		if modern {
			w.hmu.Lock()
			if len(w.modernMeta) > 0 {
				params["_meta"] = jsonrpc.MarshalObject(w.modernMeta)
			}
			w.hmu.Unlock()
		}
		e, err := w.request(ctx, mToolsList, jsonrpc.MarshalObject(params))
		if err != nil {
			return
		}
		raw, ok := e["result"]
		if !ok {
			return
		}
		w.cat.process(ctx, raw, modern, false)
		var next struct {
			NextCursor string `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &next) != nil || next.NextCursor == "" {
			return
		}
		cursor = next.NextCursor
	}
}
