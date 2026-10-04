// Package notion is the Notion connector (S30 §3.3, built in S31's carry-over): three tools over
// Notion's REST API with an internal integration token from the vault. The configured parent pages
// are the hard boundary: a page is created only under one of them, and blocks are appended only to
// a page that is one of them or sits directly under one (checked with one GET before anything is
// written). Every write is read back at L1 in code (NativeReadBacker): the page's title, parent and
// in_trash flag and its paragraphs in order, or each appended block's text. Every byte leaves
// through connector/egress via connector/saas.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
)

// Type is the connector type.
const Type = "notion"

// DocsURL is the connector's page.
const DocsURL = "https://docs.deedglass.com/connectors/notion"

// APIVersion is the pinned Notion-Version (2026-03-11: append takes a position object, and
// archived became in_trash; checked 2026-10-04).
const APIVersion = "2026-03-11"

// FieldToken is the bearer credential's field.
const FieldToken = "token"

// IntegrationsURL is where an internal integration token is made.
const IntegrationsURL = "https://www.notion.so/profile/integrations"

// Limits of the config and the tools.
const (
	DefaultAPIBaseURL = "https://api.notion.com"
	DefaultMaxBlocks  = 50
	MaxMaxBlocks      = 100 // Notion takes at most 100 children per request
	MaxParents        = 50
	MaxConfigBytes    = 16 << 10
	MaxTitle          = 200
	MaxParagraph      = 2000 // Notion's rich_text content limit
	MaxQuery          = 200
	MaxSearchLimit    = 20
	requestTimeout    = 20 * time.Second
	maxBody           = 4 << 20
)

// Connector is the Notion connector type. Per-instance state lives on the Instance.
type Connector struct {
	sleep func(ctx context.Context, d time.Duration) bool
	// httpClient replaces the instance's egress client (tests only; nil in production).
	httpClient *http.Client
}

// New returns the Notion connector type, for registration (connector.Options.Types).
func New() *Connector { return &Connector{} }

var (
	_ connector.Connector        = (*Connector)(nil)
	_ connector.NativeReadBacker = (*Connector)(nil)
	_ connector.Summarizer       = (*Connector)(nil)
)

// Type implements connector.Connector.
func (c *Connector) Type() string { return Type }

// Config is the normalized config: what the connectors row stores and its hash covers.
type Config struct {
	APIVersion string   `json:"api_version"`
	Parents    []string `json:"parents"` // page ids, lowercase with dashes
	MaxBlocks  int      `json:"max_blocks"`
	APIBaseURL string   `json:"api_base_url"`
}

func (c *Config) defaults() {
	if c.APIVersion == "" {
		c.APIVersion = APIVersion
	}
	if c.MaxBlocks == 0 {
		c.MaxBlocks = DefaultMaxBlocks
	}
	if c.APIBaseURL == "" {
		c.APIBaseURL = DefaultAPIBaseURL
	}
}

var (
	reVersion = regexp.MustCompile(`^20[0-9]{2}-[01][0-9]-[0-3][0-9]$`)
	reID32    = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// NormalizeID returns a Notion id (32 hex characters, with or without dashes, any case) in its
// canonical form, lowercase 8-4-4-4-12, or "" when s isn't one.
func NormalizeID(s string) string {
	h := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
	if !reID32.MatchString(h) {
		return ""
	}
	if strings.Contains(s, "-") && len(strings.TrimSpace(s)) != 36 {
		return ""
	}
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// configSchema summarizes the config for forms (S26); Validate is the authority.
const configSchema = `{"type":"object","additionalProperties":false,"required":["parents"],"properties":{
"parents":{"type":"array","items":{"type":"string"},"description":"1 to 50 page ids: the hard boundary. Pages are created only under these, and blocks appended only to these pages or their direct children."},
"max_blocks":{"type":"integer","minimum":1,"maximum":100,"description":"The most paragraphs one call may write. Default 50."},
"api_version":{"type":"string","description":"Sent as Notion-Version. Default 2026-03-11."},
"api_base_url":{"type":"string","description":"https://api.notion.com (any http(s) URL in dev only)"}}}`

// Validate implements connector.Connector.
func (c *Connector) Validate(_ context.Context, env connector.Env, raw json.RawMessage) (json.RawMessage, []connector.FieldError) {
	var errs []connector.FieldError
	add := func(param, format string, a ...any) {
		if len(errs) < 50 {
			errs = append(errs, connector.FieldError{Param: param, Message: fmt.Sprintf(format, a...)})
		}
	}
	if len(raw) > MaxConfigBytes {
		add("config", "The config is %d bytes; the limit is 16 KiB.", len(raw))
		return nil, errs
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil || probe == nil {
		add("config", "The config must be a JSON object. See %s.", DocsURL)
		return nil, errs
	}
	for k := range probe {
		switch k {
		case "api_version", "parents", "max_blocks", "api_base_url":
		default:
			add("config."+clip(k, 64), "isn't a Notion connector setting. The settings are parents, max_blocks, api_version and api_base_url.")
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		add("config", "The config can't be read: %s. Check it against %s.", clip(strings.TrimPrefix(err.Error(), "json: "), 160), DocsURL)
		return nil, errs
	}
	cfg.defaults()
	if !reVersion.MatchString(cfg.APIVersion) {
		add("config.api_version", "must be a Notion API version like %s.", APIVersion)
	}
	if len(cfg.Parents) == 0 || len(cfg.Parents) > MaxParents {
		add("config.parents", "needs 1 to 50 page ids: the pages Deedglass may write under.")
	}
	seen := map[string]bool{}
	norm := []string{}
	for i, p := range cfg.Parents {
		id := NormalizeID(p)
		if id == "" {
			add(fmt.Sprintf("config.parents[%d]", i), "must be a Notion page id: 32 hex characters, with or without dashes.")
			continue
		}
		if !seen[id] {
			seen[id] = true
			norm = append(norm, id)
		}
	}
	cfg.Parents = norm
	if cfg.MaxBlocks < 1 || cfg.MaxBlocks > MaxMaxBlocks {
		add("config.max_blocks", "must be 1 to 100.")
	}
	cfg.APIBaseURL = strings.TrimSuffix(cfg.APIBaseURL, "/")
	u, err := url.Parse(cfg.APIBaseURL)
	switch {
	case err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/"):
		add("config.api_base_url", "must be a plain URL like https://api.notion.com.")
	case env.Dev():
		if u.Scheme != "https" && u.Scheme != "http" {
			add("config.api_base_url", "must be an http or https URL.")
		}
	case u.Scheme != "https" || u.Host != "api.notion.com":
		add("config.api_base_url", "must be https://api.notion.com outside dev. Leave it out to use Notion's API.")
	}
	if len(errs) > 0 {
		return nil, errs
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		add("config", "The config can't be written back.")
		return nil, errs
	}
	return out, nil
}

// state is an instance's parsed config.
type state struct {
	cfg     Config
	base    *url.URL
	parents map[string]bool
}

func parse(inst *connector.Instance) (*state, error) {
	v, err := inst.Parsed(func() (any, error) {
		var cfg Config
		if err := json.Unmarshal(inst.Config, &cfg); err != nil {
			return nil, fmt.Errorf("notion: the stored config can't be read: %w", err)
		}
		cfg.defaults()
		u, err := url.Parse(cfg.APIBaseURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("notion: api_base_url can't be read")
		}
		if cfg.MaxBlocks < 1 || cfg.MaxBlocks > MaxMaxBlocks {
			cfg.MaxBlocks = DefaultMaxBlocks
		}
		ps := map[string]bool{}
		for _, p := range cfg.Parents {
			if id := NormalizeID(p); id != "" {
				ps[id] = true
			}
		}
		return &state{cfg: cfg, base: u, parents: ps}, nil
	})
	if err != nil {
		return nil, &connector.Error{Kind: connector.KindConfig, Code: "connector_misconfigured",
			AgentMessage: "This Notion connector's config can't be read. An admin can check Console › Connectors.", Err: err}
	}
	return v.(*state), nil
}

// http returns the instance's egress client: no redirects, a 4 MiB body cap, 20 s per request.
func (c *Connector) http(inst *connector.Instance) *http.Client {
	if c.httpClient != nil {
		return c.httpClient
	}
	return inst.Client(func() *http.Client {
		pol := inst.Env.Egress
		if pol == nil {
			pol = &egress.Policy{Env: egress.EnvProd}
		}
		return pol.Client(egress.ClientOptions{Timeout: requestTimeout, MaxResponseBytes: maxBody, Redirects: egress.RedirectNone})
	})
}

// Tools implements connector.Connector. The three tools are the same for every instance.
func (c *Connector) Tools(_ context.Context, inst *connector.Instance) ([]connector.ToolSpec, error) {
	if inst != nil && len(inst.Config) > 0 {
		if _, err := parse(inst); err != nil {
			return nil, err
		}
	}
	return toolSpecs(), nil
}

// Info implements connector.Connector: the catalog entry.
func (c *Connector) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: Type, Title: "Notion",
		Description: "Search pages, create a page under a parent you list, and append paragraphs, with every write read back from Notion.",
		Summary:     "Pages and paragraphs under the Notion pages you list.",
		Status:      "beta", Icon: "file-text", Modes: []string{},
		Levels: []string{"L0", "L1"},
		CredentialKinds: []connector.CredentialKind{{Kind: "bearer", Fields: []connector.CredentialField{
			{Name: FieldToken, Secret: true, Help: "An internal integration token, shared with the parent pages.", PrefixHint: "ntn_"}}}},
		ConfigSchema: json.RawMessage(configSchema), DocsURL: DocsURL,
		Setup: []connector.SetupStep{
			{Title: "Create an internal integration", Detail: "Give it Read content and Insert content. Then open each parent page › ••• › Connections and add the integration.", URL: IntegrationsURL},
			{Title: "Create the connector", Command: "deedglass connector create --config examples/connectors/notion.yaml"},
			{Title: "Store the token", Command: "deedglass connector credentials set notion --from-env NOTION_TOKEN"},
			{Title: "Test the connection", Command: "deedglass connector test notion"},
		},
		Limits: []string{
			"Only under the parent pages in the config. Nothing is moved, edited or deleted.",
			"Plain paragraphs only: no databases, data source rows, files or comments.",
			"The read-back compares plain text, not rich formatting.",
			"Notion has no idempotency key: a create whose answer was lost is UNVERIFIABLE, never sent twice.",
		},
		Tools: connector.CatalogTools(Type, toolSpecs()),
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
