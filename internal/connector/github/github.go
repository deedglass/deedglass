// Package github is the GitHub connector (S29 §3.2): fourteen tools over GitHub's REST API, with a
// fine-grained token or a GitHub App installation token from the vault. The configured repos are
// the hard boundary (a repo outside them is refused before anything is sent), every write is read
// back at L1 in code (NativeReadBacker), bodies Deedglass creates carry a hidden marker with the
// action id, and a create that may have run is reconciled by that marker instead of being sent
// again. Every byte leaves through connector/egress via connector/saas.
package github

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
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Type is the connector type.
const Type = "github"

// DocsURL is the connector's page.
const DocsURL = "https://docs.deedglass.com/connectors/github"

// Timeouts and caps.
const (
	requestTimeout    = 30 * time.Second
	maxResponseBytes  = 8 << 20
	defaultBranchTTL  = 5 * time.Minute
	tokenMargin       = 5 * time.Minute
	maxRateLimitWait  = time.Hour
	defaultRateWait   = 60 * time.Second
	maxTextFileBytes  = 256 << 10
	reconcileReserve  = 2 * time.Second
	reconcilePageSize = 30
)

// Connector is the GitHub connector type. Per-instance state (the parsed config, the default
// branch cache, the identity login) lives on the Instance; installation tokens live in one
// in-memory TokenCache keyed by connector id and credential version.
type Connector struct {
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) bool
	tokens *saas.TokenCache
	// httpClient replaces the instance's egress client (benchmarks only; nil in production).
	httpClient *http.Client
}

// Option configures New.
type Option func(*Connector)

// WithClock sets the clock (JWT claims, rate-limit waits, caches, test dates). Default time.Now.
func WithClock(now func() time.Time) Option {
	return func(c *Connector) {
		if now != nil {
			c.now = now
		}
	}
}

// New returns the GitHub connector type. Register it with the registry's Types.
func New(opts ...Option) *Connector {
	c := &Connector{now: time.Now}
	for _, o := range opts {
		o(c)
	}
	c.tokens = &saas.TokenCache{Now: c.now}
	return c
}

var (
	_ connector.Connector        = (*Connector)(nil)
	_ connector.Enricher         = (*Connector)(nil)
	_ connector.NativeReadBacker = (*Connector)(nil)
	_ connector.Summarizer       = (*Connector)(nil)
)

// Type implements connector.Connector.
func (c *Connector) Type() string { return Type }

// configSchema summarizes the config for forms (S26); Validate is the authority.
const configSchema = `{"type":"object","required":["repos"],"properties":{
"auth":{"type":"string","enum":["pat","app"],"description":"pat: a fine-grained token (credential kind bearer). app: a GitHub App (credential kind fields, private_key)."},
"app":{"type":"object","properties":{"client_id":{"type":"string"},"app_id":{"type":"integer"},"installation_id":{"type":"integer"}}},
"repos":{"type":"array","items":{"type":"string"},"description":"1 to 50 entries, owner/name or owner/*: the hard boundary."},
"protected_branches":{"type":"array","items":{"type":"string"},"description":"Branch names or globs; default main, master, release/*."},
"issue_marker":{"type":"boolean","description":"Append <!-- deedglass:act_… --> to bodies Deedglass creates (default true)."},
"api_base_url":{"type":"string","description":"Default https://api.github.com."},
"api_version":{"type":"string","description":"X-GitHub-Api-Version; default 2026-03-10."},
"max_file_bytes":{"type":"integer","minimum":1,"maximum":1048576}}}`

// Info implements connector.Connector.
func (c *Connector) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: Type, Title: "GitHub",
		Description: "Issues, branches, files and pull requests in the repositories you list, with every write read back from GitHub.",
		Levels:      []string{"L0", "L1"},
		CredentialKinds: []connector.CredentialKind{
			{Kind: "bearer", Fields: []connector.CredentialField{{Name: "token", Secret: true}}},
			{Kind: "fields", Fields: []connector.CredentialField{{Name: "private_key", Secret: true}}},
		},
		ConfigSchema: json.RawMessage(configSchema), DocsURL: DocsURL}
}

// state is an instance's parsed config and caches.
type state struct {
	cfg   Config
	base  *url.URL
	rules []repoRule

	mu       sync.Mutex
	defaults map[string]cachedBranch // lowercased repo → default branch
	logins   map[string]string       // credential fingerprint → identity login
}

type cachedBranch struct {
	name string
	at   time.Time
}

func (c *Connector) state(inst *connector.Instance) (*state, error) {
	v, err := inst.Parsed(func() (any, error) {
		var cfg Config
		if err := json.Unmarshal(inst.Config, &cfg); err != nil {
			return nil, fmt.Errorf("github: the stored config can't be read: %w", err)
		}
		u, err := url.Parse(cfg.APIBaseURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("github: api_base_url can't be read")
		}
		if cfg.Auth == "app" && cfg.App == nil {
			return nil, fmt.Errorf("github: auth app without app")
		}
		if cfg.MaxFileBytes <= 0 || cfg.MaxFileBytes > MaxMaxFileBytes {
			cfg.MaxFileBytes = DefaultMaxFileBytes
		}
		return &state{cfg: cfg, base: u, rules: parseRules(cfg.Repos), defaults: map[string]cachedBranch{}, logins: map[string]string{}}, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*state), nil
}

func misconfigured(err error) *connector.Error {
	return &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
		AgentMessage: "This connector's config can't be read. An admin can check Console › Connectors.", Err: err}
}

// http returns the instance's egress client. Writes never follow a redirect, and reads don't
// either: GitHub's REST API answers in place.
func (c *Connector) http(inst *connector.Instance) *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	return inst.Client(func() *http.Client {
		pol := inst.Env.Egress
		if pol == nil {
			pol = &egress.Policy{Env: egress.EnvProd}
		}
		return pol.Client(egress.ClientOptions{Timeout: requestTimeout, MaxResponseBytes: maxResponseBytes, Redirects: egress.RedirectNone})
	})
}

// client is the instance's saas client without auth.
func (c *Connector) client(inst *connector.Instance, st *state) saas.Client {
	return saas.Client{Base: st.base, HTTP: c.http(inst),
		Headers:  http.Header{"Accept": {"application/vnd.github+json"}, "X-Github-Api-Version": {st.cfg.APIVersion}},
		Classify: c.classify, MaxBody: maxResponseBytes, Sleep: c.sleep}
}

// Tools implements connector.Connector. The fourteen tools are the same for every instance.
func (c *Connector) Tools(_ context.Context, inst *connector.Instance) ([]connector.ToolSpec, error) {
	if inst != nil && len(inst.Config) > 0 {
		if _, err := c.state(inst); err != nil {
			return nil, err
		}
	}
	return append([]connector.ToolSpec(nil), toolSpecs...), nil
}

func specFor(name string) (connector.ToolSpec, bool) {
	for _, t := range toolSpecs {
		if t.Name == name {
			return t, true
		}
	}
	return connector.ToolSpec{}, false
}
