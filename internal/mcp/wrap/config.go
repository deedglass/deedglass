// Package wrap is `deedglass mcp wrap` (S14): a JSON-RPC relay between an MCP client and a stdio
// MCP server that sends every tools/call to the Gate in proxy mode first, and forwards it only when
// the Gate answers authorized. Everything it doesn't govern passes through unchanged, in both MCP
// eras (2026-07-28 and the initialize-based revisions). It fails closed.
package wrap

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/deedglass/deedglass/internal/toolname"
)

// Config is one wrapper.
type Config struct {
	Connector     string        // ^[a-z][a-z0-9-]{1,31}$; deedglass, mcp and native are reserved
	Server        *url.URL      // https unless loopback (or dev with InsecureHTTP)
	APIKey        string        // never logged, never passed to the child
	HideDenied    bool          // hide tools the policy always denies (default true)
	FailOpen      bool          // dev only: forward when the Gate is unreachable, with a warning per call
	MaxMessage    int           // default 4 MiB
	CallTimeout   time.Duration // 0 = none
	ResultMode    string        // "full" (≤ 256 KiB, else digest) | "digest"
	ConnectorType string        // S10's enum; default mcp
	OnBehalfOf    string        // Deedglass-On-Behalf-Of
	LogFile       string        // "" = no file
	Upstream      []string      // argv after "--"
	Env           []string      // the parent environment; DEEDGLASS_* is removed before the child sees it
	Version       string        // for User-Agent
	DevEnv        bool          // DEEDGLASS_ENV=dev
	InsecureHTTP  bool          // dev only: allow http to a non-loopback host
	// S34 (Q06): the server-to-client requests relayed to the client. Empty: sampling/createMessage
	// and elicitation/create are answered with an error and never relayed; roots/list and ping
	// always pass. Values: "sampling", "elicitation".
	AllowServerRequests []string
	// ToolNames is how tools/list spells the wrapper's own tool: safe (deedglass_check_approval,
	// the default) or dotted (deedglass.check_approval, deprecated). The server's tools keep
	// their names. tools/call takes either spelling.
	ToolNames string

	// Tests.
	RestartBase  time.Duration // first restart delay (250 ms)
	ShutdownWait time.Duration // stdin close → SIGTERM (5 s)
	KillWait     time.Duration // SIGTERM → SIGKILL (2 s)
}

// MaxResultBytes is the largest result reported in full; larger ones are sent as a digest.
const MaxResultBytes = 256 << 10

var reConnector = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// reserved connector names (S16's naming rule).
var reserved = map[string]bool{"deedglass": true, "mcp": true, "native": true}

// Config errors the CLI prints (each with its next step there).
var (
	ErrFailOpenProd = errors.New("--fail-open is for local development only. Set DEEDGLASS_ENV=dev to use it")
	ErrNoUpstream   = errors.New("no MCP server command after --")
)

// Validate checks the config and fills defaults.
func (c *Config) Validate() error {
	if !reConnector.MatchString(c.Connector) {
		return fmt.Errorf("--connector %q isn't a connector name: use 2 to 32 lowercase letters, digits or hyphens, starting with a letter", c.Connector)
	}
	if reserved[c.Connector] {
		return fmt.Errorf("--connector %q is reserved. Pick another name, such as scratch", c.Connector)
	}
	if len(c.Upstream) == 0 || strings.TrimSpace(c.Upstream[0]) == "" {
		return ErrNoUpstream
	}
	if c.FailOpen && !c.DevEnv {
		return ErrFailOpenProd
	}
	if c.Server == nil || (c.Server.Scheme != "http" && c.Server.Scheme != "https") || c.Server.Host == "" {
		return errors.New("the server must be an http(s)://host[:port] URL")
	}
	if c.Server.Scheme == "http" && !IsLoopback(c.Server.Hostname()) && !(c.DevEnv && c.InsecureHTTP) {
		return fmt.Errorf("the server %s uses http, which would send the API key in clear. Use https, or a loopback address", c.Server.Redacted())
	}
	switch c.ResultMode {
	case "":
		c.ResultMode = "full"
	case "full", "digest":
	default:
		return fmt.Errorf("--result-mode must be full or digest (got %q)", c.ResultMode)
	}
	if c.ConnectorType == "" {
		c.ConnectorType = "mcp"
	}
	st, ok := toolname.ParseStyle(c.ToolNames)
	if !ok {
		return fmt.Errorf("--tool-names must be safe or dotted (got %q)", c.ToolNames)
	}
	c.ToolNames = string(st)
	if c.MaxMessage <= 0 {
		c.MaxMessage = 4 << 20
	}
	if c.CallTimeout < 0 {
		return errors.New("--call-timeout must not be negative")
	}
	for _, r := range c.AllowServerRequests {
		if r != "sampling" && r != "elicitation" {
			return fmt.Errorf("--allow-server-requests takes sampling and elicitation, not %q", r)
		}
	}
	if c.RestartBase <= 0 {
		c.RestartBase = 250 * time.Millisecond
	}
	if c.ShutdownWait <= 0 {
		c.ShutdownWait = 5 * time.Second
	}
	if c.KillWait <= 0 {
		c.KillWait = 2 * time.Second
	}
	return nil
}

// IsLoopback reports a loopback host name or address.
func IsLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ChildEnv is env without any DEEDGLASS_* variable: the upstream server never sees the agent's key.
func ChildEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(strings.ToUpper(kv), "DEEDGLASS_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
