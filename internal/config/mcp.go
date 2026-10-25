package config

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// MCPSettings are the mcp.* settings of deedglass.yaml (S15): the browser origins allowed on
// /mcp/*, the method families forwarded to upstreams, and the upstream MCP servers the gateway
// fronts at /mcp/<slug>. S16 moves upstreams into connector rows (type mcp_http) with credentials
// in the vault; until then they live here, and their secrets only in the environment.
type MCPSettings struct {
	AllowedOrigins []string
	Passthrough    []string // "resources", "prompts", "completion"
	Upstreams      []MCPUpstream
	// ToolNames is how tools/list spells qualified tools: "safe" ("postgres_query", the default,
	// accepted by every model API) or "dotted" ("postgres.query", deprecated). tools/call takes
	// either spelling.
	ToolNames string
}

// MCPUpstream is mcp.upstreams.<slug>.
type MCPUpstream struct {
	Slug           string
	URL            *url.URL
	HeadersFromEnv map[string]string // header → environment variable; the value is never in the file
	Timeout        time.Duration
	ToolsTTL       time.Duration
}

// MCP setting defaults.
const (
	DefaultUpstreamTimeout  = 30 * time.Second
	DefaultUpstreamToolsTTL = 30 * time.Second
)

var (
	reMCPSlug    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	reHeaderName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")
	reEnvName    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	// reservedSlugs can't name an upstream (contract §3: reserved connector names).
	reservedSlugs = map[string]bool{"deedglass": true, "mcp": true, "native": true, "fake": true}
	passFamilies  = map[string]bool{"resources": true, "prompts": true, "completion": true}
)

// readMCP reads the mcp section.
func (l *loader) readMCP(file string, n *yaml.Node) {
	at := func(x *yaml.Node) string { return fmt.Sprintf("%s:%d", file, x.Line) }
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return
	}
	if n.Kind != yaml.MappingNode {
		l.problem(at(n), `"mcp" must be a mapping of settings.`, "See deedglass.example.yaml for the mcp section.")
		return
	}
	m := &MCPSettings{}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if seen[k.Value] {
			l.problem(at(k), fmt.Sprintf("%q appears twice.", "mcp."+k.Value), "Keep one.")
			continue
		}
		seen[k.Value] = true
		switch k.Value {
		case "allowed_origins":
			for _, s := range l.stringList(file, "mcp.allowed_origins", v) {
				u, err := url.Parse(s)
				if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
					l.problem(at(v), fmt.Sprintf("mcp.allowed_origins: %q isn't an origin.", s), "Write a scheme and host only, for example https://app.example.com.")
					continue
				}
				m.AllowedOrigins = append(m.AllowedOrigins, strings.TrimSuffix(s, "/"))
			}
		case "passthrough":
			for _, s := range l.stringList(file, "mcp.passthrough", v) {
				if !passFamilies[s] {
					l.problem(at(v), fmt.Sprintf("mcp.passthrough: %q isn't a method family Deedglass forwards.", s), "Use resources, prompts or completion.")
					continue
				}
				m.Passthrough = append(m.Passthrough, s)
			}
		case "upstreams":
			l.readUpstreams(file, v, m)
		case "tool_names":
			if v.Kind != yaml.ScalarNode || (v.Value != "safe" && v.Value != "dotted") {
				l.problem(at(v), fmt.Sprintf("mcp.tool_names: %q isn't safe or dotted.", v.Value), "Use safe (postgres_query, the default) or dotted (postgres.query, deprecated).")
				continue
			}
			m.ToolNames = v.Value
		default:
			next := "The mcp section takes allowed_origins, passthrough, tool_names and upstreams."
			if s := suggest("mcp."+k.Value, []string{"mcp.allowed_origins", "mcp.passthrough", "mcp.tool_names", "mcp.upstreams"}); s != "" {
				next = "Did you mean " + s + "?"
			}
			l.problem(at(k), fmt.Sprintf("%q isn't a Deedglass setting.", "mcp."+k.Value), next)
		}
	}
	sort.Slice(m.Upstreams, func(i, j int) bool { return m.Upstreams[i].Slug < m.Upstreams[j].Slug })
	l.mcp = m
}

func (l *loader) stringList(file, path string, v *yaml.Node) []string {
	if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
		return nil
	}
	if v.Kind != yaml.SequenceNode {
		l.problem(fmt.Sprintf("%s:%d", file, v.Line), fmt.Sprintf("%q must be a list.", path), "Write it as [a, b], or leave it out.")
		return nil
	}
	var out []string
	for _, c := range v.Content {
		if c.Kind != yaml.ScalarNode {
			l.problem(fmt.Sprintf("%s:%d", file, c.Line), fmt.Sprintf("%q must be a list of plain values.", path), "Write each item on one line.")
			continue
		}
		out = append(out, c.Value)
	}
	return out
}

func (l *loader) readUpstreams(file string, v *yaml.Node, m *MCPSettings) {
	at := func(x *yaml.Node) string { return fmt.Sprintf("%s:%d", file, x.Line) }
	if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
		return
	}
	if v.Kind != yaml.MappingNode {
		l.problem(at(v), `"mcp.upstreams" must map each connector name to its settings.`, "See deedglass.example.yaml for an example upstream.")
		return
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, body := v.Content[i], v.Content[i+1]
		slug := k.Value
		path := "mcp.upstreams." + slug
		if !reMCPSlug.MatchString(slug) || reservedSlugs[slug] {
			l.problem(at(k), fmt.Sprintf("%q isn't a connector name Deedglass accepts.", slug),
				"Use 2 to 32 lowercase letters, digits or hyphens, starting with a letter, and not deedglass, mcp, native or fake.")
			continue
		}
		if body.Kind != yaml.MappingNode {
			l.problem(at(body), fmt.Sprintf("%q must be a mapping with url.", path), "For example: url: https://mcp.example.com/mcp")
			continue
		}
		u := MCPUpstream{Slug: slug, Timeout: DefaultUpstreamTimeout, ToolsTTL: DefaultUpstreamToolsTTL, HeadersFromEnv: map[string]string{}}
		ok := true
		for j := 0; j+1 < len(body.Content); j += 2 {
			fk, fv := body.Content[j], body.Content[j+1]
			switch fk.Value {
			case "url":
				p, err := url.Parse(fv.Value)
				if fv.Kind != yaml.ScalarNode || err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || p.User != nil || p.Fragment != "" {
					l.problem(at(fv), path+".url must be an http or https URL without credentials.", "Put credentials in headers_from_env, never in the URL.")
					ok = false
					continue
				}
				u.URL = p
			case "timeout", "tools_ttl":
				d, err := time.ParseDuration(fv.Value)
				if err != nil || d < 100*time.Millisecond || d > 10*time.Minute {
					l.problem(at(fv), fmt.Sprintf("%s.%s must be a duration from 100ms to 10m (got %q).", path, fk.Value, fv.Value), "For example "+fk.Value+": 30s.")
					ok = false
					continue
				}
				if fk.Value == "timeout" {
					u.Timeout = d
				} else {
					u.ToolsTTL = d
				}
			case "headers_from_env":
				if fv.Kind != yaml.MappingNode {
					if fv.Tag != "!!null" {
						l.problem(at(fv), path+".headers_from_env must map header names to environment variable names.", `For example: {"Authorization": "EVERYTHING_MCP_AUTH"}`)
						ok = false
					}
					continue
				}
				for h := 0; h+1 < len(fv.Content); h += 2 {
					hn, en := fv.Content[h].Value, fv.Content[h+1].Value
					switch {
					case !reHeaderName.MatchString(hn):
						l.problem(at(fv.Content[h]), fmt.Sprintf("%s.headers_from_env: %q isn't a header name.", path, hn), "Use a plain header name such as Authorization.")
						ok = false
					case !reEnvName.MatchString(en):
						l.problem(at(fv.Content[h+1]), fmt.Sprintf("%s.headers_from_env: %q isn't an environment variable name.", path, en), "Use uppercase letters, digits and underscores.")
						ok = false
					case strings.HasPrefix(en, envPrefix):
						l.problem(at(fv.Content[h+1]), fmt.Sprintf("%s.headers_from_env: %q starts with DEEDGLASS_, which is reserved for Deedglass's own settings.", path, en),
							"Use another name, for example "+strings.ToUpper(strings.ReplaceAll(slug, "-", "_"))+"_MCP_AUTH.")
						ok = false
					default:
						u.HeadersFromEnv[hn] = en
					}
				}
			default:
				l.problem(at(fk), fmt.Sprintf("%q isn't a Deedglass setting.", path+"."+fk.Value), "An upstream takes url, headers_from_env, timeout and tools_ttl.")
				ok = false
			}
		}
		if u.URL == nil {
			if ok {
				l.problem(at(k), path+" needs a url.", "For example: url: https://mcp.example.com/mcp")
			}
			continue
		}
		if ok {
			m.Upstreams = append(m.Upstreams, u)
		}
	}
}
