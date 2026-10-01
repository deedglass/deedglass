package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/mcp/front"
	mcpjson "github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/policy"
)

// Source is one upstream's tools for its front: the upstream's names, filtered for the caller.
type Source struct {
	p    *Pool
	slug string
	vis  front.Visibility
	b    backend
}

// backend is where a Source's session comes from: a file upstream (mcp.upstreams) or an mcp_http
// connector row whose credential is in the vault (S18).
type backend interface {
	session(ctx context.Context) (*up, error)
}

type fileBackend struct {
	p    *Pool
	slug string
}

func (f fileBackend) session(context.Context) (*up, error) {
	if u := f.p.ups[f.slug]; u != nil {
		return u, nil
	}
	return nil, ErrDown
}

// Source returns slug's ToolSource. vis marks the tools the active policy always denies.
func (p *Pool) Source(slug string, vis front.Visibility) *Source {
	return &Source{p: p, slug: slug, vis: vis, b: fileBackend{p: p, slug: slug}}
}

var _ front.ToolSource = (*Source)(nil)

// Label implements front.ToolSource.
func (s *Source) Label() string { return s.slug }

// tools converts the upstream's list: names outside the MCP charset, the reserved deedglass.
// prefix and tools with invalid x-mcp-header annotations are left out (a conforming client would
// drop the last ones too).
func (s *Source) tools(ctx context.Context) ([]front.Tool, error) {
	u, err := s.b.session(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", front.ErrUpstreamDown, err)
	}
	ts, err := s.p.listOn(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", front.ErrUpstreamDown, err)
	}
	out := make([]front.Tool, 0, len(ts))
	for _, t := range ts {
		if t == nil || strings.HasPrefix(t.Name, "deedglass.") {
			continue
		}
		if !front.ReMCPName.MatchString(t.Name) {
			// S18 (attack K04): a look-alike name (Cyrillic letters, spaces, …) is never shown or
			// callable; it is logged once per listing, quoted and clipped, for the admin.
			s.p.log.Warn(HiddenToolMessage(t.Name, s.slug), "upstream", s.slug)
			continue
		}
		def, err := json.Marshal(t)
		if err != nil {
			continue
		}
		schema, _ := json.Marshal(t.InputSchema)
		if _, ok := front.ParamHeaders(schema); !ok {
			s.p.log.Warn("mcp: an upstream tool has an invalid x-mcp-header annotation; it is left out", "upstream", s.slug, "tool", t.Name)
			continue
		}
		out = append(out, front.Tool{Name: t.Name, Gate: s.slug + "." + t.Name, Def: mcpjson.Value(json.RawMessage(def)),
			Schema: schema, Digest: front.DefDigest(def)})
	}
	front.SortTools(out)
	return out, nil
}

// List implements front.ToolSource.
func (s *Source) List(ctx context.Context, pr identity.Principal) ([]front.Tool, error) {
	tools, err := s.tools(ctx)
	if err != nil {
		return nil, err
	}
	facts := make([]policy.ToolFacts, len(tools))
	for i, t := range tools {
		facts[i] = policy.ToolFacts{Name: t.Gate, Connector: s.slug, ConnectorType: ConnectorType, SideEffect: "unknown"}
	}
	if err := front.MarkHidden(ctx, s.vis, pr, tools, facts); err != nil {
		s.p.log.Warn("mcp: could not ask the Gate which tools to hide; showing every tool (calls are still decided)", "upstream", s.slug, "err", err.Error())
	}
	return tools, nil
}

// Lookup implements front.ToolSource. When the upstream can't list, the tool is still returned
// (without a digest), so the call reaches the Gate, is recorded, and fails as
// connector_unavailable instead of disappearing.
func (s *Source) Lookup(ctx context.Context, _ identity.Principal, name string) (front.Tool, bool, error) {
	if !front.ReMCPName.MatchString(name) {
		return front.Tool{}, false, nil
	}
	tools, err := s.tools(ctx)
	if errors.Is(err, front.ErrUpstreamDown) {
		return front.Tool{Name: name, Gate: s.slug + "." + name}, true, nil
	}
	if err != nil {
		return front.Tool{}, false, err
	}
	for _, t := range tools {
		if t.Name == name {
			return t, true, nil
		}
	}
	return front.Tool{}, false, nil
}

// --- passthrough (mcp.passthrough) ---

// Forward implements front.Passthrough for one upstream: resources/*, prompts/* and
// completion/complete, through the SDK's typed calls. Logged by the front, never sealed.
func (s *Source) Forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *mcpjson.ErrorObject, error) {
	u, err := s.b.session(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", front.ErrUpstreamDown, err)
	}
	u.mu.Lock()
	sess, err := u.sessionLocked(ctx)
	u.mu.Unlock()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", front.ErrUpstreamDown, err)
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	var res any
	switch method {
	case "resources/list":
		res, err = call(ctx, params, sess.ListResources)
	case "resources/templates/list":
		res, err = call(ctx, params, sess.ListResourceTemplates)
	case "resources/read":
		res, err = call(ctx, params, sess.ReadResource)
	case "prompts/list":
		res, err = call(ctx, params, sess.ListPrompts)
	case "prompts/get":
		res, err = call(ctx, params, sess.GetPrompt)
	case "completion/complete":
		res, err = call(ctx, params, sess.Complete)
	default:
		return nil, &mcpjson.ErrorObject{Code: mcpjson.CodeMethodNotFound, Message: "Method not found."}, nil
	}
	var we *mcpWireError
	if errors.As(err, &we) {
		return nil, &mcpjson.ErrorObject{Code: int(we.Code), Message: we.Message}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(res)
	return b, nil, err
}

func call[P any, R any](ctx context.Context, raw json.RawMessage, f func(context.Context, *P) (R, error)) (any, error) {
	var p P
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &mcpWireError{Code: int64(mcpjson.CodeInvalidParams), Message: "The params couldn't be read: " + err.Error()}
	}
	return f(ctx, &p)
}

// HiddenToolMessage is the log line for an upstream tool whose name isn't in the MCP charset.
func HiddenToolMessage(name, upstream string) string {
	if r := []rune(name); len(r) > 64 {
		name = string(r[:64]) + "…"
	}
	return fmt.Sprintf("Hid tool %q from %s: the name isn't plain ASCII (letters, digits, _ . -).", name, upstream)
}
