package front

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/gate"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/mcp/jsonrpc"
	"github.com/deedglass/deedglass/internal/policy"
)

// Visibility is the Gate's per-agent tool visibility (*gate.Pipeline).
type Visibility interface {
	Visibility(ctx context.Context, pr identity.Principal, tools []policy.ToolFacts) (*gate.ToolVisibilityList, error)
}

// digestFields are the parts of a tool definition its digest covers (as S14's wrap pins them).
var digestFields = []string{"name", "title", "description", "inputSchema", "outputSchema", "annotations"}

// DefDigest is a tool definition's digest: sha256 of JCS of the covered members.
func DefDigest(def json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(def, &m) != nil {
		return ""
	}
	sub := map[string]json.RawMessage{}
	for _, f := range digestFields {
		if v, ok := m[f]; ok {
			sub[f] = v
		}
	}
	d, err := deed.DigestJSON(jsonrpc.MarshalObject(sub))
	if err != nil {
		return ""
	}
	return d
}

// ReMCPName is the MCP tool-name charset the Gate accepts (S14).
var ReMCPName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// MarkHidden asks vis which tools the active policy always denies to pr and sets Hidden. An error
// leaves every tool visible: calls are still decided by the Gate.
func MarkHidden(ctx context.Context, vis Visibility, pr identity.Principal, tools []Tool, facts []policy.ToolFacts) error {
	_, err := markHidden(ctx, vis, pr, tools, facts)
	return err
}

func markHidden(ctx context.Context, vis Visibility, pr identity.Principal, tools []Tool, facts []policy.ToolFacts) (*string, error) {
	if vis == nil || len(tools) == 0 {
		return nil, nil
	}
	out, err := vis.Visibility(ctx, pr, facts)
	if err != nil {
		return nil, err
	}
	for i := range out.Data {
		if i < len(tools) && out.Data[i].Visibility == gate.VisibilityHidden {
			tools[i].Hidden = true
		}
	}
	return out.PolicyVersion, nil
}

// NativeSource is /mcp/native's tools: every native connector's tools as "<connector>.<tool>"
// (fake.echo today; postgres.*, rest.*, … as S16–S30 add connectors).
type NativeSource struct {
	Connectors []connector.Connector
	Vis        Visibility
	// Rows lists the caller's org's connectors of native types with their tools (S18: postgres.*,
	// orders.*, …). nil: built-ins only.
	Rows func(ctx context.Context, pr identity.Principal) ([]connector.RowTools, error)
}

var _ ToolSource = (*NativeSource)(nil)

// Label implements ToolSource.
func (*NativeSource) Label() string { return "native" }

// NativeTool is one native tool with its facts, for GET /v1/tools.
type NativeTool struct {
	Tool
	Connector     string
	ConnectorType string
	Title         string
	Description   string
	SideEffect    string
	InputSchema   json.RawMessage
}

// All lists every native tool with Hidden set for pr, sorted by name.
func (s *NativeSource) All(ctx context.Context, pr identity.Principal) ([]NativeTool, error) {
	nts, _, err := s.AllWithPolicy(ctx, pr)
	return nts, err
}

// AllWithPolicy is All plus the enforced policy version the visibility came from (nil when none).
func (s *NativeSource) AllWithPolicy(ctx context.Context, pr identity.Principal) ([]NativeTool, *string, error) {
	var nts []NativeTool
	add := func(conn, typ string, spec connector.ToolSpec) {
		if spec.Internal {
			return
		}
		name := conn + "." + spec.Name
		def := nativeDef(name, spec)
		nts = append(nts, NativeTool{
			Tool:      Tool{Name: name, Gate: name, Def: def, Schema: spec.InputSchema, Digest: DefDigest(def)},
			Connector: conn, ConnectorType: typ, Title: title(spec.Name), Description: spec.Description,
			SideEffect: SideEffectOf(spec.SideEffect), InputSchema: spec.InputSchema,
		})
	}
	for _, cc := range s.Connectors {
		c := connector.Builtin(cc)
		for _, spec := range c.Tools() {
			add(c.Name(), c.Type(), spec)
		}
	}
	rows, err := s.rows(ctx, pr)
	if err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		for _, spec := range row.Tools {
			add(row.Name, row.Type, spec)
		}
	}
	sortNative(nts)
	tools := make([]Tool, len(nts))
	facts := make([]policy.ToolFacts, len(nts))
	for i, nt := range nts {
		tools[i] = nt.Tool
		facts[i] = policy.ToolFacts{Name: nt.Name, Connector: nt.Connector, ConnectorType: nt.ConnectorType, SideEffect: nt.SideEffect}
	}
	pv, err := markHidden(ctx, s.Vis, pr, tools, facts)
	if err != nil {
		return nil, nil, err
	}
	for i := range nts {
		nts[i].Hidden = tools[i].Hidden
	}
	return nts, pv, nil
}

func sortNative(nts []NativeTool) {
	for i := 1; i < len(nts); i++ {
		for j := i; j > 0 && nts[j].Name < nts[j-1].Name; j-- {
			nts[j], nts[j-1] = nts[j-1], nts[j]
		}
	}
}

// List implements ToolSource.
func (s *NativeSource) List(ctx context.Context, pr identity.Principal) ([]Tool, error) {
	nts, err := s.All(ctx, pr)
	if err != nil {
		return nil, err
	}
	out := make([]Tool, len(nts))
	for i, nt := range nts {
		out[i] = nt.Tool
	}
	return out, nil
}

// Lookup implements ToolSource. Built-ins need no I/O; the org's rows are read through Rows.
func (s *NativeSource) Lookup(ctx context.Context, pr identity.Principal, name string) (Tool, bool, error) {
	conn, tool, ok := strings.Cut(name, ".")
	if !ok {
		return Tool{}, false, nil
	}
	found := func(spec connector.ToolSpec) Tool {
		def := nativeDef(name, spec)
		return Tool{Name: name, Gate: name, Def: def, Schema: spec.InputSchema, Digest: DefDigest(def)}
	}
	for _, cc := range s.Connectors {
		c := connector.Builtin(cc)
		if c.Name() != conn {
			continue
		}
		for _, spec := range c.Tools() {
			if spec.Name == tool && !spec.Internal {
				return found(spec), true, nil
			}
		}
		return Tool{}, false, nil
	}
	rows, err := s.rows(ctx, pr)
	if err != nil {
		return Tool{}, false, err
	}
	for _, row := range rows {
		if row.Name != conn {
			continue
		}
		for _, spec := range row.Tools {
			if spec.Name == tool && !spec.Internal {
				return found(spec), true, nil
			}
		}
	}
	return Tool{}, false, nil
}

func (s *NativeSource) rows(ctx context.Context, pr identity.Principal) ([]connector.RowTools, error) {
	if s.Rows == nil {
		return nil, nil
	}
	return s.Rows(ctx, pr)
}

// nativeDef is an MCP tool definition for a native tool. The annotations repeat the connector's
// declared side effect (Deedglass's own claim, unlike an upstream's hints).
func nativeDef(name string, spec connector.ToolSpec) json.RawMessage {
	schema := spec.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	var compact strings.Builder
	if b, err := json.Marshal(json.RawMessage(schema)); err == nil {
		compact.Write(b)
	} else {
		compact.Write(schema)
	}
	ann := map[string]json.RawMessage{
		"readOnlyHint":    jsonBool(spec.SideEffect == connector.SideEffectRead),
		"destructiveHint": jsonBool(spec.SideEffect == connector.SideEffectDestructive),
		"idempotentHint":  jsonBool(spec.Idempotent),
		"openWorldHint":   jsonBool(false),
	}
	return jsonrpc.MarshalObject(map[string]json.RawMessage{
		"name":        jsonrpc.String(name),
		"title":       jsonrpc.String(title(spec.Name)),
		"description": jsonrpc.String(spec.Description),
		"inputSchema": json.RawMessage(compact.String()),
		"annotations": jsonrpc.MarshalObject(ann),
	})
}

func jsonBool(b bool) json.RawMessage {
	if b {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

// title is a tool name in sentence case: "echo" → "Echo", "list_rows" → "List rows".
func title(name string) string {
	s := strings.ReplaceAll(name, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
