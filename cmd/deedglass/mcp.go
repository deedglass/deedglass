package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/deedglass/deedglass/internal/mcp/bridge"
	"github.com/deedglass/deedglass/internal/mcp/snippet"
	"github.com/deedglass/deedglass/internal/mcp/wrap"
)

// deedglass mcp wrap and deedglass mcp snippet (S14). wrap's stdout belongs to the MCP client: it
// carries JSON-RPC and nothing else. Its own lines go to stderr and the log file.

type mcpWrapOpts struct {
	connector, connectorType, resultMode, onBehalfOf, logFile string
	hideDenied, failOpen, insecureHTTP, noLogFile             bool
	callTimeout                                               time.Duration
}

var mcpWrapValues mcpWrapOpts

// wrapMemoryLimit is the wrapper's soft Go memory limit.
const wrapMemoryLimit = 32 << 20

// mcpWrapHelp is `deedglass mcp wrap --help` after Usage (S14 §5): short and grouped.
const mcpWrapHelp = `
What it does
  Runs an MCP server and sits between it and your MCP client. Every tool call goes to the Gate
  first and runs only if it is allowed; everything else passes through unchanged.

Connection
  --connector <name>      the name tools are matched as in policies: <connector>.<tool>
  --server <url>          the Deedglass server (default DEEDGLASS_SERVER, then http://127.0.0.1:8080)
  --api-key <key>         an agent's key (default DEEDGLASS_API_KEY); never passed to the server
  --on-behalf-of <ref>    the person the agent acts for
Policy
  --connector-type <t>    mcp (default), or postgres for servers whose tools take args.sql
  --hide-denied=false     list tools the policy always denies (calls are still refused)
  --fail-open             run calls when Deedglass is unreachable (DEEDGLASS_ENV=dev only)
Limits
  --call-timeout <d>      give up on a call after d, for example 2m (default: no limit)
  --result-mode <m>       full (results up to 256 KiB) or digest
Logging
  --log-file <path>       default ~/.deedglass/logs/mcp-<connector>.log; --no-log-file for stderr only

Examples
  Claude Desktop (claude_desktop_config.json): deedglass mcp snippet --client claude-desktop --connector scratch -- npx -y @modelcontextprotocol/server-filesystem /tmp/deedglass-demo
  Claude Code: claude mcp add --env DEEDGLASS_API_KEY=<key> --transport stdio scratch -- deedglass mcp wrap --connector scratch -- npx -y @modelcontextprotocol/server-filesystem /tmp/deedglass-demo

Learn more: https://docs.deedglass.com/mcp
`

// mcpSnippetHelp is `deedglass mcp snippet --help` after Usage.
const mcpSnippetHelp = `
What it does
  Prints the config that runs an MCP server through deedglass mcp wrap, with absolute paths and a
  PATH entry, for one client. The key is a placeholder unless --api-key-from-env is given.

  With --transport http it prints config for the Gate's own MCP endpoints instead: /mcp/native, or
  /mcp/<connector> for an upstream behind the gateway. Claude Desktop gets deedglass mcp serve.

Examples
  deedglass mcp snippet --client claude-desktop --connector scratch -- npx -y @modelcontextprotocol/server-filesystem /tmp/deedglass-demo
  deedglass mcp snippet --transport http --client claude-code --connector native

Learn more: https://docs.deedglass.com/mcp/clients
`

func mcpWrapFlags(fs *flag.FlagSet) {
	o := &mcpWrapValues
	*o = mcpWrapOpts{}
	// Connection
	fs.StringVar(&o.connector, "connector", "", "the connector name tools are matched as (<connector>.<tool>), for example scratch")
	fs.BoolVar(&o.insecureHTTP, "insecure-http", false, "allow http to a server that isn't loopback (DEEDGLASS_ENV=dev only)")
	fs.StringVar(&o.onBehalfOf, "on-behalf-of", "", "the person the agent acts for (Deedglass-On-Behalf-Of)")
	// Policy
	fs.StringVar(&o.connectorType, "connector-type", "mcp", "the connector type policies match (mcp; postgres for servers whose tools take args.sql)")
	fs.BoolVar(&o.hideDenied, "hide-denied", true, "hide tools the active policy always denies from tools/list")
	fs.BoolVar(&o.failOpen, "fail-open", false, "run calls without a decision when Deedglass is unreachable (DEEDGLASS_ENV=dev only)")
	// Limits
	fs.DurationVar(&o.callTimeout, "call-timeout", 0, "give up on a tool call after this long, for example 2m (0: no limit)")
	fs.StringVar(&o.resultMode, "result-mode", "full", "report results in full (up to 256 KiB) or as a digest only")
	// Logging
	fs.StringVar(&o.logFile, "log-file", "", "the log file (default ~/.deedglass/logs/mcp-<connector>.log)")
	fs.BoolVar(&o.noLogFile, "no-log-file", false, "log to stderr only")
}

const mcpWrapExample = "deedglass mcp wrap --connector scratch -- npx -y @modelcontextprotocol/server-filesystem /tmp/deedglass-demo"

// splitDashDash parses the wrap flags up to "--" and returns the command after it.
func splitDashDash(name string, flags func(*flag.FlagSet), args []string) ([]string, error) {
	i := -1
	for k, a := range args {
		if a == "--" {
			i = k
			break
		}
	}
	if i < 0 {
		return nil, usageErr(fmt.Errorf("%s needs the MCP server's command after --", name), "For example: "+mcpWrapExample)
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flags(fs)
	if err := fs.Parse(args[:i]); err != nil {
		return nil, usageErr(fmt.Errorf("%s: %v", name, err), "Run deedglass help "+strings.TrimPrefix(name, "deedglass ")+".")
	}
	if fs.NArg() > 0 {
		return nil, usageErr(fmt.Errorf("%s: unexpected %q before --", name, fs.Arg(0)), "For example: "+mcpWrapExample)
	}
	up := args[i+1:]
	if len(up) == 0 {
		return nil, usageErr(fmt.Errorf("%s needs the MCP server's command after --", name), "For example: "+mcpWrapExample)
	}
	return up, nil
}

func runMCPWrap(ctx context.Context, e *Env, args []string) error {
	up, err := splitDashDash("deedglass mcp wrap", mcpWrapFlags, args)
	if err != nil {
		return err
	}
	o := mcpWrapValues
	if o.connector == "" {
		return usageErr(errors.New("deedglass mcp wrap needs --connector"), "For example: "+mcpWrapExample)
	}
	server := firstNonEmpty(e.Globals.Server, e.Getenv("DEEDGLASS_SERVER"), e.Getenv("DEEDGLASS_BASE_URL"), defaultServer)
	u, err := url.Parse(strings.TrimSuffix(server, "/"))
	if err != nil {
		return usageErr(fmt.Errorf("the server %q isn't a URL", server), "Use --server http://127.0.0.1:8080 or set DEEDGLASS_SERVER.")
	}
	logFile := o.logFile
	if logFile == "" && !o.noLogFile {
		if home, herr := os.UserHomeDir(); herr == nil {
			logFile = filepath.Join(home, ".deedglass", "logs", "mcp-"+o.connector+".log")
		}
	}
	if o.noLogFile {
		logFile = ""
	}
	cfg := wrap.Config{Connector: o.connector, Server: u, APIKey: firstNonEmpty(e.Globals.APIKey, e.Getenv("DEEDGLASS_API_KEY")),
		HideDenied: o.hideDenied, FailOpen: o.failOpen, CallTimeout: o.callTimeout, ResultMode: o.resultMode,
		ConnectorType: o.connectorType, OnBehalfOf: o.onBehalfOf, LogFile: logFile, Upstream: up, Env: os.Environ(),
		Version: buildInfo().Version, DevEnv: e.Getenv("DEEDGLASS_ENV") == "dev", InsecureHTTP: o.insecureHTTP}
	w, err := wrap.New(cfg, e.Stderr)
	switch {
	case errors.Is(err, wrap.ErrFailOpenProd):
		return usageErrCode("fail_open_refused", errors.New("--fail-open is for local development only"), "Set DEEDGLASS_ENV=dev to use it.")
	case err != nil:
		return usageErr(err, "Run deedglass help mcp wrap.")
	}
	if cfg.APIKey == "" {
		fmt.Fprintln(e.Stderr, "✕ no API key: set DEEDGLASS_API_KEY in your MCP client config. Every call is refused until then.")
	}
	// The wrapper lives as long as the MCP client and mostly holds messages in flight: a soft
	// memory limit keeps its footprint small under bursts (S14 §3.10). GOMEMLIMIT overrides it.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(wrapMemoryLimit)
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM)
	defer stop()
	return w.Run(ctx, e.stdin(), e.Stdout)
}

// --- mcp snippet ---

type mcpSnippetOpts struct {
	client, connector, connectorType, transport string
	fromEnv                                     bool
}

var mcpSnippetValues mcpSnippetOpts

func mcpSnippetFlags(fs *flag.FlagSet) {
	o := &mcpSnippetValues
	*o = mcpSnippetOpts{}
	fs.StringVar(&o.client, "client", "", "claude-desktop, claude-code, cursor, windsurf or vscode")
	fs.StringVar(&o.connector, "connector", "", "the connector name, for example scratch")
	fs.StringVar(&o.connectorType, "connector-type", "", "copied to the wrap command (postgres for servers whose tools take args.sql)")
	fs.BoolVar(&o.fromEnv, "api-key-from-env", false, "put DEEDGLASS_API_KEY from this shell into the snippet, instead of a placeholder")
	fs.StringVar(&o.transport, "transport", "stdio", "stdio (deedglass mcp wrap around a server command) or http (the Gate's /mcp endpoints)")
}

// lookPath is replaced in tests.
var lookPath = exec.LookPath

// executable is replaced in tests.
var executable = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p, nil
}

func runMCPSnippet(ctx context.Context, e *Env, args []string) error {
	if !slices.Contains(args, "--") {
		return runMCPSnippetHTTP(e, args)
	}
	up, err := splitDashDash("deedglass mcp snippet", mcpSnippetFlags, args)
	if err != nil {
		return err
	}
	o := mcpSnippetValues
	if o.transport != "stdio" {
		return usageErr(errors.New("--transport http takes no server command after --"),
			"For example: deedglass mcp snippet --transport http --client cursor --connector native")
	}
	if !snippet.Known(o.client) {
		return usageErr(fmt.Errorf("--client must be one of %s", strings.Join(snippet.Clients, ", ")),
			"For example: deedglass mcp snippet --client claude-desktop --connector scratch -- npx -y @modelcontextprotocol/server-filesystem /tmp/deedglass-demo")
	}
	if o.connector == "" {
		return usageErr(errors.New("deedglass mcp snippet needs --connector"), "For example --connector scratch.")
	}
	self, err := executable()
	if err != nil {
		return &ExitError{Code: ExitFailure, Err: fmt.Errorf("can't find this deedglass binary: %w", err), Next: "Run it by its full path."}
	}
	cmd := up[0]
	if !filepath.IsAbs(cmd) {
		p, lerr := lookPath(cmd)
		if lerr != nil {
			return &ExitError{Code: ExitFailure, ErrCode: "not_found", Err: fmt.Errorf("%q isn't on your PATH", cmd),
				Next: "Install it, or pass its full path after --."}
		}
		if abs, aerr := filepath.Abs(p); aerr == nil {
			p = abs
		}
		cmd = p
	}
	upstream := append([]string{cmd}, up[1:]...)
	var extra []string
	if node, nerr := lookPath("node"); nerr == nil {
		extra = append(extra, filepath.Dir(node))
	}
	var flags []string
	if o.connectorType != "" {
		flags = append(flags, "--connector-type", o.connectorType)
	}
	server := firstNonEmpty(e.Globals.Server, e.Getenv("DEEDGLASS_SERVER"), e.Getenv("DEEDGLASS_BASE_URL"), defaultServer)
	opts := snippet.Options{Client: o.client, Connector: o.connector, Deedglass: self, Upstream: upstream, WrapFlags: flags,
		Server: server, ExtraPATH: extra}
	if o.fromEnv {
		opts.APIKey = e.Getenv("DEEDGLASS_API_KEY")
		if opts.APIKey == "" {
			return usageErr(errors.New("--api-key-from-env: DEEDGLASS_API_KEY isn't set"), "Set it, or leave the flag out to get a placeholder.")
		}
	}
	b, err := snippet.Build(opts)
	if err != nil {
		return usageErr(err, "Run deedglass help mcp snippet.")
	}
	_, err = e.Stdout.Write(b)
	return err
}

// --- mcp serve (S15) ---

var mcpServeConnector string

// mcpServeHelp is `deedglass mcp serve --help` after Usage.
const mcpServeHelp = `
What it does
  Lets a stdio MCP client (Claude Desktop's local servers) use Deedglass as an MCP server. Each
  message from stdin goes to the Gate's HTTP endpoint, /mcp/native by default, and each answer
  comes back on stdout. It holds no credentials but the agent's key, and runs no tools itself.

Connection
  --connector <name>      native (the Gate's own tools, default) or an upstream's name
  --server <url>          the Deedglass server (default DEEDGLASS_SERVER, then http://127.0.0.1:8080)
  --api-key <key>         an agent's key (default DEEDGLASS_API_KEY)

Example
  Claude Desktop: deedglass mcp snippet --client claude-desktop --transport http --connector native

Learn more: https://docs.deedglass.com/mcp
`

func mcpServeFlags(fs *flag.FlagSet) {
	mcpServeConnector = ""
	fs.StringVar(&mcpServeConnector, "connector", "native", "native, or the name of an upstream MCP server (mcp.upstreams)")
}

func runMCPServe(ctx context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass mcp serve takes no arguments (got %q)", args[0]), "Run deedglass help mcp serve.")
	}
	server := firstNonEmpty(e.Globals.Server, e.Getenv("DEEDGLASS_SERVER"), e.Getenv("DEEDGLASS_BASE_URL"), defaultServer)
	u, err := url.Parse(strings.TrimSuffix(server, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return usageErr(fmt.Errorf("the server %q isn't a URL", server), "Use --server http://127.0.0.1:8080 or set DEEDGLASS_SERVER.")
	}
	c := mcpServeConnector
	if c != "native" && !reServeConnector.MatchString(c) {
		return usageErr(fmt.Errorf("--connector %q isn't a connector name", c), "Use native, or an upstream's name such as everything.")
	}
	key := firstNonEmpty(e.Globals.APIKey, e.Getenv("DEEDGLASS_API_KEY"))
	if key == "" {
		fmt.Fprintln(e.Stderr, "✕ no API key: set DEEDGLASS_API_KEY in your MCP client config. Every call is refused until then.")
	}
	b, err := bridge.New(bridge.Config{Server: u, Connector: c, APIKey: key, Version: buildInfo().Version})
	if err != nil {
		return usageErr(err, "Run deedglass help mcp serve.")
	}
	fmt.Fprintln(e.Stderr, "deedglass mcp serve · "+b.Endpoint())
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM)
	defer stop()
	return b.Run(ctx, e.stdin(), e.Stdout)
}

var reServeConnector = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// runMCPSnippetHTTP is mcp snippet --transport http (S15): config that reaches /mcp/native or
// /mcp/<connector> over HTTP; Claude Desktop gets the stdio bridge.
func runMCPSnippetHTTP(e *Env, args []string) error {
	fs := flag.NewFlagSet("deedglass mcp snippet", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mcpSnippetFlags(fs)
	if err := fs.Parse(args); err != nil {
		return usageErr(fmt.Errorf("deedglass mcp snippet: %v", err), "Run deedglass help mcp snippet.")
	}
	o := mcpSnippetValues
	if o.transport != "http" || fs.NArg() > 0 {
		return usageErr(errors.New("deedglass mcp snippet needs the MCP server's command after --, or --transport http"),
			"For example: deedglass mcp snippet --transport http --client cursor --connector native")
	}
	if !snippet.Known(o.client) {
		return usageErr(fmt.Errorf("--client must be one of %s", strings.Join(snippet.Clients, ", ")),
			"For example: deedglass mcp snippet --transport http --client claude-code --connector native")
	}
	c := o.connector
	if c == "" {
		c = "native"
	}
	if c != "native" && !reServeConnector.MatchString(c) {
		return usageErr(fmt.Errorf("--connector %q isn't a connector name", c), "Use native, or an upstream's name such as everything.")
	}
	self, err := executable()
	if err != nil {
		return &ExitError{Code: ExitFailure, Err: fmt.Errorf("can't find this deedglass binary: %w", err), Next: "Run it by its full path."}
	}
	server := firstNonEmpty(e.Globals.Server, e.Getenv("DEEDGLASS_SERVER"), e.Getenv("DEEDGLASS_BASE_URL"), defaultServer)
	opts := snippet.HTTPOptions{Client: o.client, Connector: c, Server: server, Deedglass: self}
	if o.fromEnv {
		opts.APIKey = e.Getenv("DEEDGLASS_API_KEY")
		if opts.APIKey == "" {
			return usageErr(errors.New("--api-key-from-env: DEEDGLASS_API_KEY isn't set"), "Set it, or leave the flag out to get a placeholder.")
		}
	}
	b, err := snippet.BuildHTTP(opts)
	if err != nil {
		return usageErr(err, "Run deedglass help mcp snippet.")
	}
	_, err = e.Stdout.Write(b)
	return err
}
