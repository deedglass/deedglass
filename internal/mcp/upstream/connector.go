package upstream

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/identity"
	"github.com/deedglass/deedglass/internal/ids"
	"github.com/deedglass/deedglass/internal/mcp/front"
)

// The mcp_http connector type (S18, S16's step 14): a remote MCP server as an org's connector row,
// with its credential in the vault. It replaces mcp.upstreams' headers_from_env, which stays for
// dev only. The row's tools are the server's own (the MCP charset, side effect unknown); the Gate
// runs tools/call through the same pool as the file upstreams, with the credential opened only
// when a session is made (Instance.Secret), never logged, and redacted from every output.

// Row limits.
const (
	maxRowConfig     = 16 << 10
	defaultRowTimeMS = 30_000
	defaultRowTTLMS  = 30_000
)

// RowConfig is an mcp_http connector's config.
type RowConfig struct {
	URL        string  `json:"url"`
	Auth       RowAuth `json:"auth"`
	TimeoutMS  int     `json:"timeout_ms"`
	ToolsTTLMS int     `json:"tools_ttl_ms"`
}

// RowAuth says how the credential reaches the server: bearer (Authorization: Bearer <token>),
// header (<header>: <value>) or none.
type RowAuth struct {
	Kind   string `json:"kind"`
	Header string `json:"header,omitempty"`
}

var reRowHeader = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,63}$`)

// rowForbiddenHeaders can't carry the credential: they belong to the transport or to MCP itself.
var rowForbiddenHeaders = []string{"host", "content-length", "content-type", "accept", "connection", "transfer-encoding",
	"cookie", "user-agent", "mcp-session-id", "mcp-protocol-version", "mcp-method", "mcp-name", "last-event-id"}

const rowConfigSchema = `{"type":"object","required":["url"],"properties":{
"url":{"type":"string","description":"https://mcp.example.com/mcp (http in dev only)"},
"auth":{"type":"object","properties":{"kind":{"type":"string","enum":["bearer","header","none"]},"header":{"type":"string"}}},
"timeout_ms":{"type":"integer","minimum":100,"maximum":600000},
"tools_ttl_ms":{"type":"integer","minimum":0,"maximum":3600000}}}`

// Type is the mcp_http connector type. Its sessions live in the Pool, so a file upstream and a row
// share one stash of raw results for the MCP front.
type Type struct {
	p   *Pool
	mac []byte // keys the session cache by credential without keeping the credential
}

var _ connector.Connector = (*Type)(nil)

// NewType returns the mcp_http type over p.
func NewType(p *Pool) *Type {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("upstream: crypto/rand failed: " + err.Error())
	}
	return &Type{p: p, mac: k}
}

// Type implements connector.Connector.
func (t *Type) Type() string { return ConnectorType }

// Info implements connector.Connector.
func (t *Type) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: ConnectorType, Title: "Remote MCP server",
		Description: "A remote MCP server over Streamable HTTP. Its tools pass through the Gate; its credential stays in the vault and is added only when the Gate opens a session.",
		Levels:      []string{"L0"},
		CredentialKinds: []connector.CredentialKind{
			{Kind: "bearer", Fields: []connector.CredentialField{{Name: "token", Secret: true, Help: "The server's bearer token."}}},
			{Kind: "header", Fields: []connector.CredentialField{{Name: "value", Secret: true, Help: "The value of the header the config names."}}},
		},
		ConfigSchema: json.RawMessage(rowConfigSchema), DocsURL: "https://docs.deedglass.com/connectors/mcp-http",
		Summary: "A remote MCP server's tools, through the Gate.",
		Status:  "beta", Icon: "plug", Modes: []string{}, ToolsDynamic: true, Tools: []connector.ToolInfo{},
		Setup: []connector.SetupStep{
			{Title: "Create the connector", Detail: "The server's URL and how it authenticates.", Command: "deedglass connector create --type mcp_http --name my-mcp --config my-mcp.yaml"},
			{Title: "Store the credential", Command: "deedglass connector credentials set my-mcp --from-env MCP_TOKEN"},
			{Title: "Test the connection", Command: "deedglass connector test my-mcp"},
		},
		Limits: []string{
			"Tools come from the server. Their side effect is unknown: their annotations are the server's claim.",
			"L0 only: Deedglass can't read an MCP server's writes back.",
		}}
}

// Validate implements connector.Connector.
func (t *Type) Validate(ctx context.Context, env connector.Env, raw json.RawMessage) (json.RawMessage, []connector.FieldError) {
	var errs []connector.FieldError
	bad := func(param, msg string) { errs = append(errs, connector.FieldError{Param: param, Message: msg}) }
	if len(raw) > maxRowConfig {
		bad("config", "The config is larger than 16 KiB.")
		return nil, errs
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c RowConfig
	if err := dec.Decode(&c); err != nil {
		bad("config", "The config can't be read: "+strings.TrimPrefix(err.Error(), "json: ")+". An mcp_http config takes url, auth, timeout_ms and tools_ttl_ms.")
		return nil, errs
	}
	switch {
	case c.URL == "":
		bad("config.url", "url is required, like https://mcp.example.com/mcp.")
	default:
		pol := env.Egress
		if pol == nil {
			pol = &egress.Policy{Env: egress.EnvProd}
		}
		chk, err := pol.CheckURL(ctx, c.URL)
		if be, ok := egress.IsBlocked(err); ok {
			errs = append(errs, connector.FieldError{Param: "config.url", Message: be.About("url"), Code: connector.CodeDestinationBlocked})
		} else if chk != nil {
			if chk.URL.User != nil || chk.URL.Fragment != "" {
				bad("config.url", "url can't carry credentials or a fragment. Store the credential with deedglass connector credentials set.")
			} else {
				c.URL = chk.URL.String()
			}
		} else if err != nil {
			bad("config.url", "url isn't a valid URL: use https://host/path.")
		}
	}
	if c.Auth.Kind == "" {
		c.Auth.Kind = "bearer"
	}
	switch c.Auth.Kind {
	case "bearer", "none":
		if c.Auth.Header != "" {
			bad("config.auth.header", "auth.header is only for auth.kind header.")
		}
	case "header":
		if !reRowHeader.MatchString(c.Auth.Header) || isForbiddenRowHeader(c.Auth.Header) {
			bad("config.auth.header", "auth.header must be a plain header name, like X-Api-Key, and not one the transport sets.")
		} else {
			c.Auth.Header = http.CanonicalHeaderKey(c.Auth.Header)
		}
	default:
		bad("config.auth.kind", "auth.kind must be bearer, header or none.")
	}
	if c.TimeoutMS == 0 {
		c.TimeoutMS = defaultRowTimeMS
	}
	if c.TimeoutMS < 100 || c.TimeoutMS > 600_000 {
		bad("config.timeout_ms", "timeout_ms must be from 100 to 600000.")
	}
	if c.ToolsTTLMS == 0 {
		c.ToolsTTLMS = defaultRowTTLMS
	}
	if c.ToolsTTLMS < 0 || c.ToolsTTLMS > 3_600_000 {
		bad("config.tools_ttl_ms", "tools_ttl_ms must be from 0 to 3600000.")
	}
	if len(errs) > 0 {
		return nil, errs
	}
	out, err := json.Marshal(c)
	if err != nil {
		bad("config", "The config can't be written back.")
		return nil, errs
	}
	return out, nil
}

func isForbiddenRowHeader(h string) bool {
	l := strings.ToLower(h)
	for _, f := range rowForbiddenHeaders {
		if l == f {
			return true
		}
	}
	return strings.HasPrefix(l, "mcp-param-") || strings.HasPrefix(l, "deedglass-")
}

type rowState struct {
	cfg RowConfig
	url *url.URL
}

func parseRow(inst *connector.Instance) (*rowState, error) {
	v, err := inst.Parsed(func() (any, error) {
		var c RowConfig
		if err := json.Unmarshal(inst.Config, &c); err != nil {
			return nil, fmt.Errorf("mcp_http: the stored config can't be read: %w", err)
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("mcp_http: the stored url can't be read")
		}
		return &rowState{cfg: c, url: u}, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*rowState), nil
}

type rowKey struct{ id, hash, mac string }

// session returns inst's pooled upstream: one per (connector, config hash, credential), so a
// replaced credential or config opens a new session and the old one is closed.
func (t *Type) session(ctx context.Context, inst *connector.Instance) (*up, error) {
	st, err := parseRow(inst)
	if err != nil {
		return nil, &connector.Error{Kind: connector.KindConfig, Code: connector.CodeConnectorUnavailable,
			AgentMessage: rowUnavailable(inst.Name), Err: err}
	}
	hdr := http.Header{}
	m := hmac.New(sha256.New, t.mac)
	if st.cfg.Auth.Kind != "none" {
		sec, err := inst.Secret(ctx)
		if err != nil {
			return nil, err
		}
		switch st.cfg.Auth.Kind {
		case "bearer":
			if sec.Kind() != "bearer" {
				sec.Destroy()
				return nil, connector.ErrCredentialsMissing(inst.Name)
			}
			hdr.Set("Authorization", "Bearer "+sec.Field("token"))
		case "header":
			if sec.Kind() != "header" {
				sec.Destroy()
				return nil, connector.ErrCredentialsMissing(inst.Name)
			}
			hdr.Set(st.cfg.Auth.Header, sec.Field("value"))
		}
		sec.Destroy()
		for _, k := range []string{"Authorization", st.cfg.Auth.Header} {
			if k != "" {
				m.Write([]byte(k + "\x00" + hdr.Get(k) + "\x00"))
			}
		}
	}
	key := rowKey{id: inst.ID.String(), hash: inst.ConfigHash, mac: hex.EncodeToString(m.Sum(nil))}
	if v, ok := t.p.rows.Load(key); ok {
		return v.(*up), nil
	}
	pol := inst.Env.Egress
	if pol == nil {
		pol = &egress.Policy{Env: egress.EnvProd}
	}
	cfg := Config{Slug: inst.Name, URL: st.url, Timeout: time.Duration(st.cfg.TimeoutMS) * time.Millisecond,
		ToolsTTL: time.Duration(st.cfg.ToolsTTLMS) * time.Millisecond}
	nu := newUp(cfg, hdr, pol, t.p.version, t.p.log)
	nu.scope, nu.org, nu.connectorID = "row:"+inst.ID.String(), inst.OrgID, inst.ID.String()
	v, loaded := t.p.rows.LoadOrStore(key, nu)
	if !loaded {
		// Close the sessions of this connector's older configs and credentials.
		t.p.rows.Range(func(k, old any) bool {
			if rk := k.(rowKey); rk.id == key.id && rk != key {
				t.p.rows.Delete(k)
				o := old.(*up)
				o.mu.Lock()
				o.dropLocked()
				o.mu.Unlock()
			}
			return true
		})
	}
	return v.(*up), nil
}

func rowUnavailable(name string) string {
	return "The MCP server " + name + " isn't available. Ask an admin to check the connector with deedglass connector test " + name + "."
}

// Tools implements connector.Connector: the server's tools in the MCP charset, side effect unknown.
func (t *Type) Tools(ctx context.Context, inst *connector.Instance) ([]connector.ToolSpec, error) {
	u, err := t.session(ctx, inst)
	if err != nil {
		return nil, err
	}
	ts, err := t.p.listOn(ctx, u)
	if err != nil {
		return nil, err
	}
	out := make([]connector.ToolSpec, 0, len(ts))
	for _, x := range ts {
		if x == nil || strings.HasPrefix(x.Name, "deedglass.") {
			continue
		}
		if !front.ReMCPName.MatchString(x.Name) {
			t.p.log.Warn(HiddenToolMessage(x.Name, inst.Name), "upstream", inst.Name)
			continue
		}
		spec := connector.ToolSpec{Name: x.Name, Description: x.Description, SideEffect: connector.SideEffectUnknown}
		if b, err := json.Marshal(x.InputSchema); err == nil && string(b) != "null" {
			spec.InputSchema = b
		}
		out = append(out, spec)
	}
	return out, nil
}

// Execute implements connector.Connector: one tools/call, classified as for file upstreams.
func (t *Type) Execute(ctx context.Context, inst *connector.Instance, call connector.Call) (connector.Result, error) {
	u, err := t.session(ctx, inst)
	if err != nil {
		return connector.Result{}, err
	}
	return t.p.execOn(ctx, u, inst.Name, call)
}

// Test implements connector.Connector: config, credentials, destination and a tools/list.
func (t *Type) Test(ctx context.Context, inst *connector.Instance) (connector.TestReport, error) {
	start := time.Now()
	rep := connector.TestReport{}
	add := func(id, title string, s connector.CheckStatus, detail, fix string) {
		rep.Checks = append(rep.Checks, connector.Check{ID: id, Title: title, Status: s, Detail: detail, Fix: fix,
			DocURL: "https://docs.deedglass.com/connectors/mcp-http#" + id})
	}
	st, err := parseRow(inst)
	if err != nil {
		add("config", "config", connector.CheckFail, "the stored config can't be read", "Update the connector's config.")
		rep.Settle()
		return rep, nil
	}
	add("config", "config", connector.CheckPass, st.url.Host+st.url.Path, "")
	pol := inst.Env.Egress
	if pol == nil {
		pol = &egress.Policy{Env: egress.EnvProd}
	}
	if _, err := pol.CheckURL(ctx, st.cfg.URL); err != nil {
		if be, ok := egress.IsBlocked(err); ok {
			add("destination", "destination", connector.CheckFail, be.About("url"), "Point url at a public https MCP server.")
			rep.Settle()
			return rep, nil
		}
		add("destination", "destination", connector.CheckWarn, "the host doesn't resolve yet", "Check the url's host name.")
	} else {
		add("destination", "destination", connector.CheckPass, "allowed by the egress policy", "")
	}
	tools, err := t.Tools(ctx, inst)
	var ce *connector.Error
	switch {
	case err == nil:
		if st.cfg.Auth.Kind != "none" {
			add("credentials", "credentials", connector.CheckPass, "set", "")
		}
		add("tools", "tools/list", connector.CheckPass, fmt.Sprintf("%d tools", len(tools)), "")
	case errors.As(err, &ce) && (ce.Code == connector.CodeCredentialsMissing || ce.Code == connector.CodeVaultUnavailable):
		add("credentials", "credentials", connector.CheckFail, ce.AgentMessage, "Store the credential with deedglass connector credentials set "+inst.Name+".")
	default:
		add("tools", "tools/list", connector.CheckFail, "the server didn't list its tools", "Check that the server is up and the credential is current.")
	}
	rep.Duration = time.Since(start)
	rep.Settle()
	return rep, nil
}

// --- the MCP front for rows (/mcp/{name}) ---

type rowBackend struct {
	t    *Type
	reg  *connector.Registry
	org  ids.OrgID
	name string
}

func (b rowBackend) session(ctx context.Context) (*up, error) {
	inst, impl, err := b.reg.Named(ctx, b.org, b.name)
	if err != nil {
		return nil, err
	}
	if impl.Type() != ConnectorType {
		return nil, connector.ErrToolUnknown
	}
	return b.t.session(ctx, inst)
}

// RowSource is the ToolSource of org's mcp_http connector named name, for the MCP front.
func (t *Type) RowSource(reg *connector.Registry, org ids.OrgID, name string, vis front.Visibility) *Source {
	return &Source{p: t.p, slug: name, vis: vis, b: rowBackend{t: t, reg: reg, org: org, name: name}}
}

// Serves reports whether org has a live mcp_http connector named name.
func (t *Type) Serves(ctx context.Context, reg *connector.Registry, p identity.Principal, name string) (bool, error) {
	_, impl, err := reg.Named(ctx, p.OrgID, name)
	switch {
	case errors.Is(err, connector.ErrToolUnknown), errors.Is(err, connector.ErrConnectorUnavailable):
		return false, nil
	case err != nil:
		return false, err
	}
	return impl.Type() == ConnectorType, nil
}
