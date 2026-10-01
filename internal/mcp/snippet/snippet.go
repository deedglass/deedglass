// Package snippet writes MCP client config that runs a server through `deedglass mcp wrap` (S14):
// Claude Desktop, Claude Code, Cursor, VS Code and Windsurf. Paths are absolute and PATH is set,
// because GUI apps on macOS start with a minimal PATH (npx and node are otherwise not found).
package snippet

//go:generate go test . -run TestGoldens -update

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Clients the snippet command knows (verified 2026-10-01; docs/MCP_CLIENTS.md).
var Clients = []string{"claude-desktop", "claude-code", "cursor", "windsurf", "vscode"}

// PlaceholderKey is what the snippet shows instead of a real key.
const PlaceholderKey = "dg_test_REPLACE_ME"

// basePATH is appended after the directories of the resolved commands.
var basePATH = []string{"/usr/local/bin", "/opt/homebrew/bin", "/usr/bin", "/bin"}

// Options describe one snippet. Deedglass and Upstream[0] are absolute paths (the CLI resolves
// them); WrapFlags are copied into the generated args before "--".
type Options struct {
	Client    string
	Connector string
	Deedglass string   // absolute path of the deedglass binary
	Upstream  []string // argv, with Upstream[0] absolute
	WrapFlags []string // e.g. --connector-type postgres
	Server    string   // DEEDGLASS_SERVER
	APIKey    string   // "" = PlaceholderKey
	ExtraPATH []string // more directories for PATH (for example node's)
}

// Known reports whether c is a supported client.
func Known(c string) bool {
	for _, k := range Clients {
		if k == c {
			return true
		}
	}
	return false
}

// PATH is the PATH entry: the commands' directories first, then the usual ones, without repeats.
func (o Options) PATH() string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d == "" || d == "." || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	if len(o.Upstream) > 0 && filepath.IsAbs(o.Upstream[0]) {
		add(filepath.Dir(o.Upstream[0]))
	}
	for _, d := range o.ExtraPATH {
		add(d)
	}
	for _, d := range basePATH {
		add(d)
	}
	return strings.Join(out, ":")
}

func (o Options) args() []string {
	a := []string{"mcp", "wrap", "--connector", o.Connector}
	a = append(a, o.WrapFlags...)
	a = append(a, "--")
	return append(a, o.Upstream...)
}

func (o Options) env() map[string]string {
	key := o.APIKey
	if key == "" {
		key = PlaceholderKey
	}
	return map[string]string{"DEEDGLASS_API_KEY": key, "DEEDGLASS_SERVER": o.Server, "PATH": o.PATH()}
}

// Build returns the snippet: JSON for the four config-file clients, a shell command for Claude Code.
func Build(o Options) ([]byte, error) {
	if !Known(o.Client) {
		return nil, fmt.Errorf("unknown client %q: use one of %s", o.Client, strings.Join(Clients, ", "))
	}
	if o.Connector == "" || len(o.Upstream) == 0 || o.Deedglass == "" {
		return nil, fmt.Errorf("a snippet needs a connector, the deedglass path and the MCP server command")
	}
	type server struct {
		Type    string            `json:"type,omitempty"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	}
	s := server{Command: o.Deedglass, Args: o.args(), Env: o.env()}
	var v any
	switch o.Client {
	case "claude-code":
		return shell(o), nil
	case "vscode":
		s.Type = "stdio"
		v = map[string]any{"servers": map[string]server{o.Connector: s}}
	default: // claude-desktop, cursor, windsurf
		v = map[string]any{"mcpServers": map[string]server{o.Connector: s}}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// shell is the `claude mcp add` one-liner. Options go before the name, with --transport between
// the last --env and the name (the CLI would read a name right after --env as another pair).
func shell(o Options) []byte {
	env := o.env()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("claude mcp add")
	for _, k := range keys {
		b.WriteString(" --env " + shQuote(k+"="+env[k]))
	}
	b.WriteString(" --transport stdio " + shQuote(o.Connector) + " -- " + shQuote(o.Deedglass))
	for _, a := range o.args() {
		b.WriteString(" " + shQuote(a))
	}
	b.WriteString("\n")
	return []byte(b.String())
}

// shQuote quotes s for a POSIX shell when it needs it.
func shQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@+,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Filename is the golden file name for a client.
func Filename(client string) string {
	if client == "claude-code" {
		return client + ".sh"
	}
	return client + ".json"
}
