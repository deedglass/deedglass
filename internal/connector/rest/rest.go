package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

// Connector is the REST connector type. It holds no state of its own: each Instance carries its
// parsed config and its two egress clients.
type Connector struct{}

// New returns the REST connector type.
func New() *Connector { return &Connector{} }

var (
	_ connector.Connector = (*Connector)(nil)
	_ connector.Enricher  = (*Connector)(nil)
)

// Type implements connector.Connector.
func (c *Connector) Type() string { return Type }

// configSchema is a summary of the config for forms (S26); Validate is the authority.
const configSchema = `{"type":"object","required":["base_url","allowed_methods","allowed_path_prefixes","tools"],"properties":{
"base_url":{"type":"string","description":"https://api.example.com (http in dev only)"},
"auth":{"type":"object","properties":{"kind":{"type":"string","enum":["bearer","header","basic","query","none"]},"header":{"type":"string"},"param":{"type":"string"}}},
"allowed_methods":{"type":"array","items":{"type":"string","enum":["GET","HEAD","POST","PUT","PATCH","DELETE"]}},
"allowed_path_prefixes":{"type":"array","items":{"type":"string"}},
"timeout_ms":{"type":"integer","minimum":100,"maximum":60000},
"max_response_bytes":{"type":"integer","minimum":1024,"maximum":16777216},
"redirects":{"type":"string","enum":["none","same_host"]},
"send_action_id":{"type":"boolean"},
"headers":{"type":"object"},
"test":{"type":"object","properties":{"method":{"type":"string","enum":["GET","HEAD"]},"path":{"type":"string"}}},
"tools":{"type":"array","items":{"type":"object","required":["name","method","path"]}}}}`

// Info implements connector.Connector.
func (c *Connector) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: Type, Title: "REST API",
		Description: "Any JSON API as governed tools: one method on one templated path per tool, with the credential added only when the Gate runs a call.",
		Levels:      []string{"L0"},
		CredentialKinds: []connector.CredentialKind{
			{Kind: "bearer", Fields: []connector.CredentialField{{Name: "token", Secret: true}}},
			{Kind: "header", Fields: []connector.CredentialField{{Name: "value", Secret: true}}},
			{Kind: "basic", Fields: []connector.CredentialField{{Name: "username", Secret: false}, {Name: "password", Secret: true}}},
			{Kind: "query", Fields: []connector.CredentialField{{Name: "value", Secret: true}}},
		},
		ConfigSchema: json.RawMessage(configSchema), DocsURL: "https://docs.deedglass.com/connectors/rest"}
}

// state is an instance's parsed config and its clients.
type state struct {
	cfg   Config
	base  *url.URL
	tools map[string]Tool
	specs []connector.ToolSpec

	clientOnce  sync.Once
	read, write *http.Client
}

func parse(inst *connector.Instance) (*state, error) {
	v, err := inst.Parsed(func() (any, error) {
		var cfg Config
		dec := json.NewDecoder(bytesReader(inst.Config))
		dec.UseNumber()
		if err := dec.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("rest: the stored config can't be read: %w", err)
		}
		u, err := url.Parse(cfg.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("rest: base_url: %w", err)
		}
		st := &state{cfg: cfg, base: u, tools: map[string]Tool{}}
		for _, t := range cfg.Tools {
			st.tools[t.Name] = t
			st.specs = append(st.specs, connector.ToolSpec{Name: t.Name, Description: t.Description, InputSchema: t.inputSchema(),
				SideEffect: t.sideOf(), Idempotent: t.idempotent(), Postcondition: t.Postcondition, ReadBack: t.ReadBack})
		}
		return st, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*state), nil
}

// clients builds the instance's two egress clients: reads follow the configured redirect rule;
// writes never follow a redirect.
func (st *state) clients(inst *connector.Instance) (read, write *http.Client) {
	st.clientOnce.Do(func() {
		pol := inst.Env.Egress
		if pol == nil {
			pol = &egress.Policy{Env: egress.EnvProd}
		}
		timeout := time.Duration(st.cfg.TimeoutMS) * time.Millisecond
		st.read = pol.Client(egress.ClientOptions{Timeout: timeout, MaxResponseBytes: st.cfg.MaxResponseBytes, Redirects: egress.Redirects(st.cfg.Redirects)})
		st.write = pol.Client(egress.ClientOptions{Timeout: timeout, MaxResponseBytes: st.cfg.MaxResponseBytes, Redirects: egress.RedirectNone})
	})
	return st.read, st.write
}

// Tools implements connector.Connector.
func (c *Connector) Tools(_ context.Context, inst *connector.Instance) ([]connector.ToolSpec, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	return append([]connector.ToolSpec(nil), st.specs...), nil
}
