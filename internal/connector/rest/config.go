// Package rest is the REST connector (S16 §3.4): any JSON API becomes governed tools from a
// declarative config. Each tool is one HTTP method on one templated path under an allowed prefix;
// arguments go to the path, the query, the JSON body or an X- header; the credential is added
// last, from the vault, at the moment the Gate runs the call. Every byte leaves through
// connector/egress.
package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/deedglass/deedglass/internal/connector"
	"github.com/deedglass/deedglass/internal/connector/egress"
	"github.com/deedglass/deedglass/internal/connector/schema"
	"github.com/deedglass/deedglass/internal/deed"
	"github.com/deedglass/deedglass/internal/witness"
)

// Type is the connector type.
const Type = "rest"

// Limits (S16 §3.4).
const (
	MaxTools            = 64
	MaxConfigBytes      = 64 << 10
	DefaultTimeoutMS    = 10_000
	MaxTimeoutMS        = 60_000
	DefaultMaxResponse  = 4 << 20
	MaxMaxResponse      = 16 << 20
	MaxArgs             = 32
	MaxDescription      = 1024
	defaultHeaderAuth   = "X-API-Key"
	defaultQueryAuth    = "api_key"
	maxDefaultHeaders   = 16
	maxHeaderValueBytes = 1024
)

// Methods a REST tool may use.
var Methods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"}

// AuthKinds a REST connector takes.
var AuthKinds = []string{"bearer", "header", "basic", "query", "none"}

// Config is the normalized config: what the connectors row stores and its hash covers.
type Config struct {
	BaseURL             string            `json:"base_url"`
	Auth                Auth              `json:"auth"`
	AllowedMethods      []string          `json:"allowed_methods"`
	AllowedPathPrefixes []string          `json:"allowed_path_prefixes"`
	TimeoutMS           int               `json:"timeout_ms"`
	MaxResponseBytes    int64             `json:"max_response_bytes"`
	Redirects           string            `json:"redirects"`
	SendActionID        *bool             `json:"send_action_id"`
	Headers             map[string]string `json:"headers,omitempty"`
	Test                *TestRequest      `json:"test,omitempty"`
	Tools               []Tool            `json:"tools"`
}

// Auth says how the credential travels.
type Auth struct {
	Kind   string `json:"kind"`             // bearer | header | basic | query | none
	Header string `json:"header,omitempty"` // kind header: the header name (default X-API-Key)
	Param  string `json:"param,omitempty"`  // kind query: the query parameter (default api_key)
}

// TestRequest is the request `connector test` sends to check auth.
type TestRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Tool is one REST tool.
type Tool struct {
	Name              string                  `json:"name"`
	Description       string                  `json:"description,omitempty"`
	Method            string                  `json:"method"`
	Path              string                  `json:"path"`
	SideEffect        string                  `json:"side_effect,omitempty"`
	Idempotent        bool                    `json:"idempotent,omitempty"`
	IdempotencyHeader string                  `json:"idempotency_header,omitempty"`
	Args              map[string]Arg          `json:"args,omitempty"`
	ReadBack          *connector.ReadBackSpec `json:"readback,omitempty"`
	Postcondition     string                  `json:"postcondition,omitempty"`
}

// Arg is one argument of a tool.
type Arg struct {
	In          string   `json:"in"`               // path | query | body | header
	Type        string   `json:"type"`             // string | integer | number | boolean | object | array
	Header      string   `json:"header,omitempty"` // in header: the header name (X-…)
	Description string   `json:"description,omitempty"`
	Enum        []any    `json:"enum,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	MaxLength   *int     `json:"max_length,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Sensitive   bool     `json:"sensitive,omitempty"`
	// Field is a body arg's key in the JSON body when it differs from the arg's name (HC-P00):
	// arg names are lowercase (resource_type), while some APIs use other keys (FHIR's resourceType).
	Field string `json:"field,omitempty"`
}

// bodyKey is the key a body arg is sent under: field when set, else the arg's name.
func (a Arg) bodyKey(name string) string {
	if a.Field != "" {
		return a.Field
	}
	return name
}

var (
	reHeaderArg   = regexp.MustCompile(`^X-[A-Za-z0-9-]{1,64}$`)
	reHeaderName  = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	reQueryParam  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	reArgName     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	reBodyField   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	rePlaceholder = regexp.MustCompile(`\{([^{}]*)\}`)
	rePathLiteral = regexp.MustCompile(`^[A-Za-z0-9._~!$&'()*+,;=:@%/-]*$`)
	argIns        = []string{"path", "query", "body", "header"}
	argTypes      = []string{"string", "integer", "number", "boolean", "object", "array"}
)

// forbiddenHeader reports whether a config may not set this header (S16 §3.4).
func forbiddenHeader(h string) bool {
	l := strings.ToLower(h)
	switch l {
	case "authorization", "cookie", "host", "content-length", "transfer-encoding", "connection", "te", "upgrade",
		"user-agent", "idempotency-key", "content-type":
		return true
	}
	return strings.HasPrefix(l, "proxy-") || strings.HasPrefix(l, "x-forwarded-") || strings.HasPrefix(l, "deedglass-")
}

// defaultSide is the side effect a method implies; a tool can raise it, never lower it.
func defaultSide(method string) connector.SideEffect {
	switch method {
	case "GET", "HEAD":
		return connector.SideEffectRead
	case "DELETE":
		return connector.SideEffectDestructive
	}
	return connector.SideEffectWrite
}

// sideOf is a tool's effective side effect.
func (t Tool) sideOf() connector.SideEffect {
	if t.SideEffect != "" {
		return connector.SideEffect(t.SideEffect)
	}
	return defaultSide(t.Method)
}

// idempotent: GET/HEAD, a declared idempotency header, or idempotent: true. Only these retry.
func (t Tool) idempotent() bool {
	return t.Method == "GET" || t.Method == "HEAD" || t.IdempotencyHeader != "" || t.Idempotent
}

type validator struct {
	errs []connector.FieldError
}

func (v *validator) add(param, format string, a ...any) {
	if len(v.errs) < 50 {
		v.errs = append(v.errs, connector.FieldError{Param: param, Message: fmt.Sprintf(format, a...)})
	}
}

// Validate implements connector.Connector: it checks and normalizes cfg. base_url passes
// egress.CheckURL (a DNS failure is tolerated here; the test reports it).
func (c *Connector) Validate(ctx context.Context, env connector.Env, raw json.RawMessage) (json.RawMessage, []connector.FieldError) {
	v := &validator{}
	if len(raw) > MaxConfigBytes {
		v.add("config", "The config is %d bytes; the limit is 64 KiB.", len(raw))
		return nil, v.errs
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		v.add("config", "The config can't be read: %s. Check it against docs/CONNECTORS.md.", cleanJSONErr(err))
		return nil, v.errs
	}
	c.normalize(ctx, env, &cfg, v)
	if len(v.errs) > 0 {
		return nil, v.errs
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		v.add("config", "The config can't be written back: %v.", err)
		return nil, v.errs
	}
	return out, nil
}

func cleanJSONErr(err error) string {
	s := err.Error()
	s = strings.TrimPrefix(s, "json: ")
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

func (c *Connector) normalize(ctx context.Context, env connector.Env, cfg *Config, v *validator) {
	// base_url
	if cfg.BaseURL == "" {
		v.add("config.base_url", "base_url is required, like https://api.example.com.")
	} else if pol := env.Egress; pol != nil {
		chk, err := pol.CheckURL(ctx, cfg.BaseURL)
		if be, ok := egress.IsBlocked(err); ok {
			v.errs = append(v.errs, connector.FieldError{Param: "config.base_url", Message: be.About("base_url"), Code: connector.CodeDestinationBlocked})
		} else if chk != nil {
			if chk.URL.RawQuery != "" || chk.URL.Fragment != "" {
				v.add("config.base_url", "base_url can't carry a query or a fragment. Put query values in tool args.")
			}
			u := *chk.URL
			u.Path = strings.TrimSuffix(u.Path, "/")
			u.RawPath = ""
			cfg.BaseURL = u.String()
		}
	}
	// auth
	if cfg.Auth.Kind == "" {
		cfg.Auth.Kind = "none"
	}
	if !slices.Contains(AuthKinds, cfg.Auth.Kind) {
		v.add("config.auth.kind", "auth.kind must be one of %s.", strings.Join(AuthKinds, ", "))
	}
	switch cfg.Auth.Kind {
	case "header":
		if cfg.Auth.Header == "" {
			cfg.Auth.Header = defaultHeaderAuth
		}
		if !reHeaderName.MatchString(cfg.Auth.Header) || forbiddenHeader(cfg.Auth.Header) && !strings.EqualFold(cfg.Auth.Header, "authorization") {
			v.add("config.auth.header", "auth.header must be a header name like X-API-Key.")
		}
		cfg.Auth.Param = ""
	case "query":
		if cfg.Auth.Param == "" {
			cfg.Auth.Param = defaultQueryAuth
		}
		if !reQueryParam.MatchString(cfg.Auth.Param) {
			v.add("config.auth.param", "auth.param must be a query parameter name like api_key.")
		}
		cfg.Auth.Header = ""
	default:
		if cfg.Auth.Header != "" || cfg.Auth.Param != "" {
			v.add("config.auth", "auth.header and auth.param apply only to the header and query kinds.")
		}
	}
	// methods
	if len(cfg.AllowedMethods) == 0 {
		v.add("config.allowed_methods", "allowed_methods is required: list the methods tools may use, like [GET, PATCH].")
	}
	seen := map[string]bool{}
	for i, m := range cfg.AllowedMethods {
		m = strings.ToUpper(m)
		cfg.AllowedMethods[i] = m
		if !slices.Contains(Methods, m) {
			v.add(fmt.Sprintf("config.allowed_methods[%d]", i), "%q isn't a method a REST tool may use (%s).", clip(m), strings.Join(Methods, ", "))
		}
		if seen[m] {
			v.add(fmt.Sprintf("config.allowed_methods[%d]", i), "%s is listed twice.", m)
		}
		seen[m] = true
	}
	sort.Slice(cfg.AllowedMethods, func(i, j int) bool {
		return slices.Index(Methods, cfg.AllowedMethods[i]) < slices.Index(Methods, cfg.AllowedMethods[j])
	})
	// prefixes
	if len(cfg.AllowedPathPrefixes) == 0 {
		v.add("config.allowed_path_prefixes", "allowed_path_prefixes is required: the paths tools may reach, like [/v1/orders].")
	}
	for i, p := range cfg.AllowedPathPrefixes {
		param := fmt.Sprintf("config.allowed_path_prefixes[%d]", i)
		p = strings.TrimSuffix(p, "/")
		if p == "" {
			p = "/"
		}
		cfg.AllowedPathPrefixes[i] = p
		if !strings.HasPrefix(p, "/") || strings.Contains(p, "{") || strings.Contains(p, "..") || strings.Contains(p, "//") || !rePathLiteral.MatchString(p) {
			v.add(param, "%q isn't a path prefix. Write it like /v1/orders.", clip(p))
		}
	}
	// limits
	if cfg.TimeoutMS == 0 {
		cfg.TimeoutMS = DefaultTimeoutMS
	}
	if cfg.TimeoutMS < 100 || cfg.TimeoutMS > MaxTimeoutMS {
		v.add("config.timeout_ms", "timeout_ms must be from 100 to %d.", MaxTimeoutMS)
	}
	if cfg.MaxResponseBytes == 0 {
		cfg.MaxResponseBytes = DefaultMaxResponse
	}
	if cfg.MaxResponseBytes < 1024 || cfg.MaxResponseBytes > MaxMaxResponse {
		v.add("config.max_response_bytes", "max_response_bytes must be from 1024 to %d (16 MiB).", MaxMaxResponse)
	}
	if cfg.Redirects == "" {
		cfg.Redirects = string(egress.RedirectNone)
	}
	if cfg.Redirects != string(egress.RedirectNone) && cfg.Redirects != string(egress.RedirectSameHost) {
		v.add("config.redirects", "redirects must be none or same_host.")
	}
	if cfg.SendActionID == nil {
		t := true
		cfg.SendActionID = &t
	}
	// default headers
	if len(cfg.Headers) > maxDefaultHeaders {
		v.add("config.headers", "At most %d default headers.", maxDefaultHeaders)
	}
	for h, val := range cfg.Headers {
		param := "config.headers." + clip(h)
		switch {
		case !reHeaderName.MatchString(h) || forbiddenHeader(h):
			v.add(param, "%s can't be set here. Credentials go in deedglass connector credentials set; Deedglass sets the rest.", clip(h))
		case len(val) > maxHeaderValueBytes || strings.ContainsAny(val, "\r\n\x00") || !utf8.ValidString(val):
			v.add(param, "The value must be one line of text, at most 1 KiB.")
		}
	}
	// test
	if cfg.Test != nil {
		cfg.Test.Method = strings.ToUpper(cfg.Test.Method)
		if cfg.Test.Method != "GET" && cfg.Test.Method != "HEAD" {
			v.add("config.test.method", "The test request must be a GET or a HEAD, so testing never changes anything.")
		}
		if !strings.HasPrefix(cfg.Test.Path, "/") || strings.Contains(cfg.Test.Path, "..") || strings.ContainsAny(cfg.Test.Path, "{}?#") || !rePathLiteral.MatchString(cfg.Test.Path) {
			v.add("config.test.path", "test.path must be a plain path like /v1/health.")
		}
	}
	// tools
	if len(cfg.Tools) == 0 {
		v.add("config.tools", "Add at least one tool.")
	}
	if len(cfg.Tools) > MaxTools {
		v.add("config.tools", "At most %d tools.", MaxTools)
	}
	names := map[string]int{}
	for i := range cfg.Tools {
		c.normalizeTool(cfg, i, v)
		if j, dup := names[cfg.Tools[i].Name]; dup {
			v.add(fmt.Sprintf("config.tools[%d].name", i), "The name %s is already used by tools[%d].", cfg.Tools[i].Name, j)
		}
		names[cfg.Tools[i].Name] = i
	}
	// Read-backs and postconditions (S19): the read tool exists, is a read and is idempotent; the
	// templates name declared args; the CEL compiles under its cost limit.
	infos := make(map[string]witness.ToolInfo, len(cfg.Tools))
	for _, t := range cfg.Tools {
		infos[t.Name] = toolInfo(t)
	}
	for i, t := range cfg.Tools {
		if t.ReadBack == nil && t.Postcondition == "" {
			continue
		}
		for _, fe := range witness.CompileToolSpec(fmt.Sprintf("config.tools[%d]", i), infos[t.Name], WitnessReadBack(t.ReadBack), t.Postcondition, infos) {
			v.add(fe.Param, "%s", fe.Message)
		}
	}
}

// toolInfo is what witness.CompileToolSpec needs to know about a tool.
func toolInfo(t Tool) witness.ToolInfo {
	args := make([]string, 0, len(t.Args))
	for n := range t.Args {
		args = append(args, n)
	}
	return witness.ToolInfo{Name: t.Name, SideEffect: string(t.sideOf()), Idempotent: t.idempotent(), Args: args}
}

// WitnessReadBack converts the stored read-back to the Witness's type (the same JSON shape).
func WitnessReadBack(r *connector.ReadBackSpec) *witness.ReadBackSpec {
	if r == nil {
		return nil
	}
	return &witness.ReadBackSpec{Tool: r.Tool, Args: r.Args, Expect: r.Expect, Consistency: r.Consistency, Attempts: r.Attempts,
		IntervalMS: r.IntervalMS, OnNotFound: r.OnNotFound}
}

func (c *Connector) normalizeTool(cfg *Config, i int, v *validator) {
	t := &cfg.Tools[i]
	at := fmt.Sprintf("config.tools[%d]", i)
	if !connector.ReNativeTool.MatchString(t.Name) {
		v.add(at+".name", "Tool names use lowercase letters, digits and underscores, like update_status.")
	}
	if utf8.RuneCountInString(t.Description) > MaxDescription {
		v.add(at+".description", "The description is longer than %d characters.", MaxDescription)
	}
	t.Method = strings.ToUpper(t.Method)
	if !slices.Contains(cfg.AllowedMethods, t.Method) {
		v.add(at+".method", "%s isn't in allowed_methods.", clip(t.Method))
	}
	// side effects can be raised, never lowered
	if t.SideEffect != "" {
		se := connector.SideEffect(t.SideEffect)
		switch {
		case se.Rank() == 0:
			v.add(at+".side_effect", "side_effect must be read, write or destructive.")
		case se.Rank() < defaultSide(t.Method).Rank():
			v.add(at+".side_effect", "tools[%d] is a %s, so its side effect can't be %q. Use %q, or leave side_effect out.", i, t.Method, t.SideEffect, defaultSide(t.Method))
		case se == defaultSide(t.Method):
			t.SideEffect = "" // the default, normalized away
		}
	}
	if t.IdempotencyHeader != "" && (!reHeaderName.MatchString(t.IdempotencyHeader) || forbiddenHeader(t.IdempotencyHeader) && !strings.EqualFold(t.IdempotencyHeader, "Idempotency-Key")) {
		v.add(at+".idempotency_header", "idempotency_header must be a header name like Idempotency-Key.")
	}
	// path
	if !strings.HasPrefix(t.Path, "/") || strings.Contains(t.Path, "..") || strings.Contains(t.Path, "//") || strings.ContainsAny(t.Path, "?#") {
		v.add(at+".path", "The path must start with / and have no .., //, ? or #.")
	} else if !underPrefix(literalPrefix(t.Path), cfg.AllowedPathPrefixes) && !underPrefix(t.Path, cfg.AllowedPathPrefixes) {
		v.add(at+".path", "%s isn't under allowed_path_prefixes (%s).", clip(t.Path), strings.Join(cfg.AllowedPathPrefixes, ", "))
	}
	holders := map[string]bool{}
	for _, seg := range strings.Split(t.Path, "/") {
		lit := rePlaceholder.ReplaceAllString(seg, "")
		if m := rePlaceholder.FindAllStringSubmatch(seg, -1); len(m) > 0 {
			if len(m) > 1 || lit != "" {
				v.add(at+".path", "A placeholder must be a whole path segment, like /v1/orders/{id}.")
			}
			for _, x := range m {
				holders[x[1]] = true
			}
		} else if !rePathLiteral.MatchString(seg) || strings.ContainsAny(seg, "{}") {
			v.add(at+".path", "The path has characters that aren't allowed.")
		}
	}
	if len(t.Args) > MaxArgs {
		v.add(at+".args", "At most %d args.", MaxArgs)
	}
	pathArgs := map[string]bool{}
	bodyKeys := map[string]string{} // body key → the arg that sends it
	argNames := make([]string, 0, len(t.Args))
	for name := range t.Args {
		argNames = append(argNames, name)
	}
	sort.Strings(argNames)
	for _, name := range argNames {
		if a := t.Args[name]; a.In == "body" {
			k := a.bodyKey(name)
			if other, dup := bodyKeys[k]; dup {
				v.add(at+".args."+clip(name)+".field", "The body key %s is already sent by the arg %s.", clip(k), other)
			}
			bodyKeys[k] = name
		}
	}
	for name, a := range t.Args {
		ap := at + ".args." + clip(name)
		if !reArgName.MatchString(name) {
			v.add(ap, "Arg names use lowercase letters, digits and underscores, like order_id.")
		}
		if !slices.Contains(argIns, a.In) {
			v.add(ap+".in", "in must be path, query, body or header.")
		}
		if a.Type == "" {
			a.Type = "string"
		}
		if !slices.Contains(argTypes, a.Type) {
			v.add(ap+".type", "type must be one of %s.", strings.Join(argTypes, ", "))
		}
		switch a.In {
		case "path":
			pathArgs[name] = true
			a.Required = true
			if a.Type != "string" && a.Type != "integer" {
				v.add(ap+".type", "A path arg must be a string or an integer.")
			}
		case "header":
			if !reHeaderArg.MatchString(a.Header) || forbiddenHeader(a.Header) {
				v.add(ap+".header", "A header arg needs header: X-Something (X- and up to 64 letters, digits or hyphens).")
			}
			if a.Type != "string" && a.Type != "integer" && a.Type != "boolean" {
				v.add(ap+".type", "A header arg must be a string, an integer or a boolean.")
			}
		case "query":
			if !reQueryParam.MatchString(name) {
				v.add(ap, "A query arg name must be a query parameter name.")
			}
			if a.Type == "object" {
				v.add(ap+".type", "A query arg can't be an object.")
			}
		}
		if a.In != "header" && a.Header != "" {
			v.add(ap+".header", "header applies only to in: header.")
		}
		switch {
		case a.Field != "" && a.In != "body":
			v.add(ap+".field", "field applies only to in: body.")
		case a.Field != "" && !reBodyField.MatchString(a.Field):
			v.add(ap+".field", "field must be a JSON key of letters, digits and underscores, like resourceType.")
		case a.Field == name:
			a.Field = "" // the default, normalized away
		}
		if a.Pattern != "" {
			if _, err := regexp.Compile(a.Pattern); err != nil {
				v.add(ap+".pattern", "The pattern isn't a valid RE2 expression.")
			}
		}
		t.Args[name] = a
	}
	for h := range holders {
		if !pathArgs[h] {
			v.add(at+".path", "The placeholder {%s} has no arg with in: path.", clip(h))
		}
	}
	for a := range pathArgs {
		if !holders[a] {
			v.add(at+".args."+a, "The path arg %s has no {%s} placeholder in the path.", a, a)
		}
	}
	if t.Postcondition != "" && utf8.RuneCountInString(t.Postcondition) > 2048 {
		v.add(at+".postcondition", "The postcondition is longer than 2,048 characters.")
	}
	if _, err := schema.Compile(t.inputSchema()); err != nil {
		v.add(at+".args", "The args don't form a valid schema: %v.", err)
	}
}

// literalPrefix is the path up to its first placeholder.
func literalPrefix(p string) string {
	if i := strings.Index(p, "{"); i >= 0 {
		return strings.TrimSuffix(p[:i], "/")
	}
	return p
}

// underPrefix reports whether p is one of prefixes or under one on a segment boundary
// (/v1/orders admits /v1/orders/42, never /v1/orders-admin).
func underPrefix(p string, prefixes []string) bool {
	for _, pre := range prefixes {
		if pre == "/" || p == pre || strings.HasPrefix(p, pre+"/") {
			return true
		}
	}
	return false
}

func clip(s string) string {
	if len(s) > 64 || !utf8.ValidString(s) {
		return "(value)"
	}
	return s
}

// inputSchema is the tool's JSON Schema: one property per arg, additionalProperties false.
func (t Tool) inputSchema() json.RawMessage {
	props := map[string]any{}
	var req []string
	names := make([]string, 0, len(t.Args))
	for n := range t.Args {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a := t.Args[n]
		p := map[string]any{"type": a.Type}
		if a.Type == "" {
			p["type"] = "string"
		}
		if a.Description != "" {
			p["description"] = a.Description
		}
		if len(a.Enum) > 0 {
			p["enum"] = a.Enum
		}
		if a.Pattern != "" {
			p["pattern"] = a.Pattern
		}
		if a.Min != nil {
			p["minimum"] = *a.Min
		}
		if a.Max != nil {
			p["maximum"] = *a.Max
		}
		if a.MaxLength != nil {
			p["maxLength"] = *a.MaxLength
		}
		props[n] = p
		if a.Required {
			req = append(req, n)
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(req) > 0 {
		s["required"] = req
	}
	b, _ := json.Marshal(s)
	return b
}

// Hash is the config hash: "sha256:…" of the normalized config's JCS form.
func Hash(normalized json.RawMessage) (string, error) { return deed.DigestJSON(normalized) }
