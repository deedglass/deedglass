// Package fhir is the native FHIR R4 connector (ADR-105). It is an adapter: the Gate, the policy
// engine, the Witness and the deed code know nothing about FHIR; this package implements the
// connector interfaces (Connector, Enricher, NativeReadBacker, PHIKeeper) and nothing else. One
// tool per interaction (read, vread, search, history, create, update, patch, delete), each taking
// a resource_type from the connector's allowed list. Paths are built only from that type, a FHIR
// id and a version id. Writes carry If-Match and If-None-Exist when the agent sends them, and a
// 16-character binding token in X-Request-ID. A write is checked (L1) by reading back exactly the
// version the server reported. Credentials are a static bearer token or SMART Backend Services
// (OAuth 2.0 client credentials with a signed JWT assertion, RFC 7523). Standard library only.
package fhir

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/saas"
)

// Type is the connector type.
const Type = "fhir"

// DocsURL is the connector's page.
const DocsURL = "https://docs.deedglass.com/connectors/fhir"

// Interactions a tool can be, in catalog order. Reads first.
var Interactions = []string{"read", "vread", "search", "history", "create", "update", "patch", "delete"}

var defaultInteractions = []string{"read", "vread", "search", "history"}

// Auth kinds.
const (
	AuthSMART  = "smart_backend"
	AuthBearer = "bearer"
	AuthNone   = "none"
)

// Limits.
const (
	MaxTypes           = 64
	MaxConfigBytes     = 16 << 10
	DefaultTimeoutMS   = 10_000
	MaxTimeoutMS       = 60_000
	DefaultMaxResponse = 4 << 20
	MaxMaxResponse     = 16 << 20
	MaxTokenLife       = 300 * time.Second
)

var (
	reType      = regexp.MustCompile(`^[A-Z][A-Za-z]{1,63}$`)
	reID        = regexp.MustCompile(`^[A-Za-z0-9\-.]{1,64}$`)
	reHeader    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)
	reScope     = regexp.MustCompile(`^system/(\*|[A-Z][A-Za-z]{1,63})\.(\*|read|write|c?r?u?d?s?)$`)
	reTagSystem = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)
)

// Config is the normalized config.
type Config struct {
	BaseURL              string   `json:"base_url"`
	FHIRVersion          string   `json:"fhir_version"`
	Auth                 Auth     `json:"auth"`
	AllowedResourceTypes []string `json:"allowed_resource_types"`
	AllowedInteractions  []string `json:"allowed_interactions"`
	IfMatch              string   `json:"if_match"`          // when_known | required
	RequestIDHeader      string   `json:"request_id_header"` // a header name, or none
	ServerTagSystems     []string `json:"server_tag_systems,omitempty"`
	TimeoutMS            int      `json:"timeout_ms"`
	MaxResponseBytes     int64    `json:"max_response_bytes"`
	Redirects            string   `json:"redirects"`
}

// Auth says how the connector gets its access token.
type Auth struct {
	Kind     string   `json:"kind"`                // smart_backend | bearer | none
	TokenURL string   `json:"token_url,omitempty"` // smart_backend: else discovered from <base>/.well-known/smart-configuration
	Scopes   []string `json:"scopes,omitempty"`    // smart_backend: system/<Type>.<cruds>; derived from the allowed types and interactions when empty
}

// Connector is the fhir connector type.
type Connector struct {
	tokens saas.TokenCache
	now    func() time.Time
}

// New returns the fhir connector type.
func New() *Connector { return &Connector{} }

var (
	_ connector.Connector        = (*Connector)(nil)
	_ connector.Enricher         = (*Connector)(nil)
	_ connector.NativeReadBacker = (*Connector)(nil)
	_ connector.PHIKeeper        = (*Connector)(nil)
)

func (c *Connector) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Type implements connector.Connector.
func (c *Connector) Type() string { return Type }

const configSchema = `{"type":"object","required":["base_url","auth","allowed_resource_types"],"properties":{
"base_url":{"type":"string","description":"The FHIR base, like https://fhir.example.org/r4 (http in dev only)."},
"fhir_version":{"type":"string","enum":["R4"]},
"auth":{"type":"object","properties":{"kind":{"type":"string","enum":["smart_backend","bearer","none"]},"token_url":{"type":"string"},"scopes":{"type":"array","items":{"type":"string"}}}},
"allowed_resource_types":{"type":"array","items":{"type":"string"},"description":"1 to 64 FHIR resource types, like Observation. Anything else is refused before a request is built."},
"allowed_interactions":{"type":"array","items":{"type":"string","enum":["read","vread","search","history","create","update","patch","delete"]},"description":"Default read, vread, search, history."},
"if_match":{"type":"string","enum":["when_known","required"]},
"request_id_header":{"type":"string","description":"Default X-Request-ID; none turns it off."},
"server_tag_systems":{"type":"array","items":{"type":"string"},"description":"meta.tag and meta.security systems the server adds; the read-back ignores them."},
"timeout_ms":{"type":"integer","minimum":100,"maximum":60000},
"max_response_bytes":{"type":"integer","minimum":1024,"maximum":16777216},
"redirects":{"type":"string","enum":["none","same_host"]}}}`

// Info implements connector.Connector.
func (c *Connector) Info() connector.TypeInfo {
	return connector.TypeInfo{Object: "connector_type", Type: Type, Title: "FHIR",
		Description: "A FHIR R4 server's REST API as governed tools: one tool per interaction on the resource types you list, with every write read back at the version the server reported.",
		Summary:     "FHIR R4 reads and writes on the resource types you list, each write checked at the version the server reported.",
		Status:      "beta", Icon: "heart-pulse", Modes: []string{},
		Levels: []string{"L0", "L1"},
		CredentialKinds: []connector.CredentialKind{
			{Kind: "fields", Fields: []connector.CredentialField{
				{Name: "client_id", Secret: false, Help: "The client id the FHIR server's authorization server registered for this backend service."},
				{Name: "private_jwk", Secret: true, Help: "The client's private key as a JWK (RS384 or ES384) or a JWK Set of one or two keys, on one line; the first key signs."}}},
			{Kind: "bearer", Fields: []connector.CredentialField{{Name: "token", Secret: true, Help: "A static access token (auth.kind bearer)."}}},
		},
		ConfigSchema: json.RawMessage(configSchema), DocsURL: DocsURL,
		Setup: []connector.SetupStep{
			{Title: "Register a backend service", Detail: "On the FHIR server's authorization server, register a SMART Backend Services client with your public key and system scopes for the types you list.", URL: DocsURL},
			{Title: "Create the connector", Command: "deedglass connector create --config examples/connectors/fhir.yaml"},
			{Title: "Store the key", Command: "deedglass connector credentials set fhir --kind fields --field client_id=env:FHIR_CLIENT_ID --field private_jwk=file:client.jwk"},
			{Title: "Test the connection", Command: "deedglass connector test fhir"},
		},
		Limits: []string{
			"FHIR R4 (4.0.1) only, JSON only.",
			"No transaction or batch Bundles yet; one resource per call.",
			"Checked (L1) only: Proven (L2) for Postgres-backed FHIR servers and AuditEvent or Provenance export aren't built yet.",
			"A patch is read back for the version and the JSON Patch add, replace and remove paths; a FHIRPath Patch only for the version.",
			"After a 5xx or a timeout the write is found only when the server stores request ids (meta.source); otherwise the action stays UNVERIFIABLE.",
		},
		Tools: connector.CatalogTools(Type, toolSpecs(nil, Interactions, "when_known")),
	}
}

// state is an instance's parsed config and its clients.
type state struct {
	cfg  Config
	base *url.URL
	// specs by name, for the allowed interactions
	tools map[string]connector.ToolSpec
	specs []connector.ToolSpec

	clientOnce  sync.Once
	read, write *http.Client
	tokenKey    atomic.Value // the token cache key last used (string)
}

func parse(inst *connector.Instance) (*state, error) {
	v, err := inst.Parsed(func() (any, error) {
		var cfg Config
		dec := json.NewDecoder(bytes.NewReader(inst.Config))
		if err := dec.Decode(&cfg); err != nil {
			return nil, fmt.Errorf("fhir: the stored config can't be read: %w", err)
		}
		u, err := url.Parse(cfg.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("fhir: base_url: %w", err)
		}
		st := &state{cfg: cfg, base: u, tools: map[string]connector.ToolSpec{}}
		st.specs = toolSpecs(cfg.AllowedResourceTypes, cfg.AllowedInteractions, cfg.IfMatch)
		for _, s := range st.specs {
			st.tools[s.Name] = s
		}
		return st, nil
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

// Tools implements connector.Connector.
func (c *Connector) Tools(_ context.Context, inst *connector.Instance) ([]connector.ToolSpec, error) {
	st, err := parse(inst)
	if err != nil {
		return nil, err
	}
	return slices.Clone(st.specs), nil
}

// Validate implements connector.Connector.
func (c *Connector) Validate(ctx context.Context, env connector.Env, raw json.RawMessage) (json.RawMessage, []connector.FieldError) {
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
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		add("config", "The config can't be read: %s. Check it against docs/connectors/fhir.md.", clip(strings.TrimPrefix(err.Error(), "json: "), 160))
		return nil, errs
	}
	checkURL := func(param, raw string) string {
		if env.Egress == nil {
			return raw
		}
		chk, err := env.Egress.CheckURL(ctx, raw)
		if be, ok := egress.IsBlocked(err); ok {
			errs = append(errs, connector.FieldError{Param: param, Message: be.About(strings.TrimPrefix(param, "config.")), Code: connector.CodeDestinationBlocked})
			return raw
		}
		if chk == nil {
			if err != nil {
				add(param, "%s isn't a URL Deedglass can reach: %s.", strings.TrimPrefix(param, "config."), clip(err.Error(), 160))
			}
			return raw
		}
		if chk.URL.RawQuery != "" || chk.URL.Fragment != "" {
			add(param, "%s can't carry a query or a fragment.", strings.TrimPrefix(param, "config."))
		}
		u := *chk.URL
		u.Path, u.RawPath = strings.TrimSuffix(u.Path, "/"), ""
		return u.String()
	}
	if cfg.BaseURL == "" {
		add("config.base_url", "base_url is required, like https://fhir.example.org/r4.")
	} else {
		cfg.BaseURL = checkURL("config.base_url", cfg.BaseURL)
	}
	switch cfg.FHIRVersion {
	case "":
		cfg.FHIRVersion = "R4"
	case "R4":
	default:
		add("config.fhir_version", "fhir_version must be R4: Deedglass speaks FHIR R4 (4.0.1) only for now.")
	}
	// resource types
	if n := len(cfg.AllowedResourceTypes); n == 0 || n > MaxTypes {
		add("config.allowed_resource_types", "allowed_resource_types must list 1 to %d FHIR resource types, like [Observation, Condition].", MaxTypes)
	}
	seen := map[string]bool{}
	for i, t := range cfg.AllowedResourceTypes {
		p := fmt.Sprintf("config.allowed_resource_types[%d]", i)
		switch {
		case !reType.MatchString(t):
			add(p, "%q isn't a FHIR resource type name. Write it like Observation.", clip(t, 64))
		case seen[t]:
			add(p, "%s is listed twice.", t)
		}
		seen[t] = true
	}
	slices.Sort(cfg.AllowedResourceTypes)
	// interactions
	if cfg.AllowedInteractions == nil {
		cfg.AllowedInteractions = slices.Clone(defaultInteractions)
	}
	seen = map[string]bool{}
	for i, in := range cfg.AllowedInteractions {
		p := fmt.Sprintf("config.allowed_interactions[%d]", i)
		switch {
		case in == "transaction" || in == "batch":
			add(p, "%s Bundles aren't supported yet. Send one resource per call.", in)
		case !slices.Contains(Interactions, in):
			add(p, "%q isn't an interaction (%s).", clip(in, 32), strings.Join(Interactions, ", "))
		case seen[in]:
			add(p, "%s is listed twice.", in)
		}
		seen[in] = true
	}
	if len(cfg.AllowedInteractions) == 0 {
		add("config.allowed_interactions", "allowed_interactions can't be empty. Leave it out for read, vread, search and history.")
	}
	slices.SortFunc(cfg.AllowedInteractions, func(a, b string) int { return slices.Index(Interactions, a) - slices.Index(Interactions, b) })
	// if_match
	switch cfg.IfMatch {
	case "":
		cfg.IfMatch = "when_known"
	case "when_known", "required":
	default:
		add("config.if_match", "if_match must be when_known or required.")
	}
	// request id header
	switch {
	case cfg.RequestIDHeader == "":
		cfg.RequestIDHeader = "X-Request-ID"
	case cfg.RequestIDHeader == "none":
	case !reHeader.MatchString(cfg.RequestIDHeader) || forbiddenHeader(cfg.RequestIDHeader):
		add("config.request_id_header", "request_id_header must be a header name like X-Request-ID, or none.")
	}
	for i, s := range cfg.ServerTagSystems {
		if !reTagSystem.MatchString(s) {
			add(fmt.Sprintf("config.server_tag_systems[%d]", i), "Each server tag system is a URI of 1 to 256 visible characters.")
		}
	}
	if len(cfg.ServerTagSystems) > 16 {
		add("config.server_tag_systems", "At most 16 server tag systems.")
	}
	// auth
	switch cfg.Auth.Kind {
	case AuthSMART:
		if cfg.Auth.TokenURL != "" {
			cfg.Auth.TokenURL = checkURL("config.auth.token_url", cfg.Auth.TokenURL)
		}
		if len(cfg.Auth.Scopes) == 0 && len(errs) == 0 {
			cfg.Auth.Scopes = deriveScopes(cfg.AllowedResourceTypes, cfg.AllowedInteractions)
		}
		if len(cfg.Auth.Scopes) > 64 {
			add("config.auth.scopes", "At most 64 scopes.")
		}
		for i, s := range cfg.Auth.Scopes {
			if !reScope.MatchString(s) || strings.HasSuffix(s, ".") {
				add(fmt.Sprintf("config.auth.scopes[%d]", i), "%q isn't a SMART system scope. Write it like system/Observation.rs or system/Observation.cu.", clip(s, 80))
			}
		}
	case AuthBearer, AuthNone:
		if cfg.Auth.TokenURL != "" || len(cfg.Auth.Scopes) > 0 {
			add("config.auth", "auth.token_url and auth.scopes apply only to smart_backend.")
		}
		if cfg.Auth.Kind == AuthNone && !env.Dev() {
			add("config.auth.kind", "auth.kind none works in dev only. Use smart_backend, or bearer with a token.")
		}
	case "":
		add("config.auth.kind", "auth.kind is required: smart_backend, bearer or none (dev only).")
	default:
		add("config.auth.kind", "auth.kind must be smart_backend, bearer or none (dev only).")
	}
	// limits
	if cfg.TimeoutMS == 0 {
		cfg.TimeoutMS = DefaultTimeoutMS
	}
	if cfg.TimeoutMS < 100 || cfg.TimeoutMS > MaxTimeoutMS {
		add("config.timeout_ms", "timeout_ms must be from 100 to %d.", MaxTimeoutMS)
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = DefaultMaxResponse
	}
	if cfg.MaxResponseBytes < 1024 || cfg.MaxResponseBytes > MaxMaxResponse {
		add("config.max_response_bytes", "max_response_bytes must be from 1024 to %d (16 MiB).", MaxMaxResponse)
	}
	if cfg.Redirects == "" {
		cfg.Redirects = string(egress.RedirectNone)
	}
	if cfg.Redirects != string(egress.RedirectNone) && cfg.Redirects != string(egress.RedirectSameHost) {
		add("config.redirects", "redirects must be none or same_host.")
	}
	if len(errs) > 0 {
		return nil, errs
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		add("config", "The config can't be written back: %v.", err)
		return nil, errs
	}
	return out, nil
}

// deriveScopes is one system scope per type with the SMART v2 letters the interactions need:
// c create, r read and vread, u update and patch, d delete, s search and history.
func deriveScopes(types, inters []string) []string {
	letters := ""
	for _, l := range []struct{ letter, of string }{{"c", "create"}, {"r", "read vread"}, {"u", "update patch"}, {"d", "delete"}, {"s", "search history"}} {
		for _, in := range inters {
			if slices.Contains(strings.Fields(l.of), in) {
				letters += l.letter
				break
			}
		}
	}
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, "system/"+t+"."+letters)
	}
	return out
}

// forbiddenHeader: headers a config may not take over.
func forbiddenHeader(h string) bool {
	switch strings.ToLower(h) {
	case "authorization", "cookie", "host", "content-length", "content-type", "accept", "if-match", "if-none-exist", "prefer",
		"transfer-encoding", "connection", "te", "upgrade", "user-agent":
		return true
	}
	l := strings.ToLower(h)
	return strings.HasPrefix(l, "proxy-") || strings.HasPrefix(l, "x-forwarded-") || strings.HasPrefix(l, "deedglass-")
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
