// Package installlinks builds every one-click install link and command for Deedglass's MCP server
// (S47 §3.6) from one server object, so the README buttons, deedglass.com/install, the registry
// server.json, the Claude Code plugin and the listing kits can never drift apart.
//
// No link holds a key or a server URL with credentials: `deedglass mcp serve` finds the key the way
// the CLI does (DEEDGLASS_API_KEY, or the key file `deedglass init` wrote). The package is pure: no
// network, no file reads.
package installlinks

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
)

// Version is the npm and release version the links point at (npm/deedglass/package.json; a test
// keeps them equal).
const Version = "0.10.0"

// Name is the server name every client shows.
const Name = "deedglass"

// Description is the one line clients and registries show: docs/description.json's tagline
// (docs/description_test.go keeps them equal). The Claude Desktop bundle and the Goose link carry it.
const Description = "Deedglass lets rules decide each AI agent tool call, checks what really changed and seals a record."

// CursorMaxURL is Cursor's limit for an install link.
const CursorMaxURL = 10000

// MCPBOnRelease says whether the GitHub release at Version carries the Claude Desktop bundles
// (deedglass-<version>-<platform>.mcpb). While it is false the three Claude Desktop entries point at
// the docs page that connects Claude Desktop by hand, because the download URLs would answer 404.
// Flip it in the release that attaches the bundles (TestClaudeDesktopLinks checks both ways).
const MCPBOnRelease = false

// ClaudeDesktopGuide is the docs page for connecting Claude Desktop without a bundle.
const ClaudeDesktopGuide = "https://docs.deedglass.com/get-started/claude-desktop/"

// ClaudeCodeCommand is the one Claude Code line: the README, the install page and deedglass init all
// print it. User scope, so it works in every project; mcp serve finds the key deedglass init wrote
// (.deedglass/ in the project or a parent, then ~/.deedglass/current.json), and no key enters
// Claude Code's config.
const ClaudeCodeCommand = "claude mcp add --scope user deedglass -- npx -y deedglass mcp serve"

// BundleURL is where the GitHub release at Version carries one platform's Claude Desktop bundle
// (internal/mcpb names the file; its test keeps the names equal).
func BundleURL(platform string) string {
	return "https://github.com/deedglass/deedglass/releases/download/v" + Version + "/deedglass-" + Version + "-" + platform + ".mcpb"
}

// ServerObject is the stdio server every link installs.
type ServerObject struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// Server is the one server object: npx runs the npm package, which runs the platform binary.
func Server() ServerObject {
	return ServerObject{Command: "npx", Args: []string{"-y", "deedglass", "mcp", "serve"}}
}

// ServerJSON is Server as compact JSON, the bytes every link encodes.
func ServerJSON() []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(Server())
	return bytes.TrimRight(b.Bytes(), "\n")
}

// Link is one client's way to install.
type Link struct {
	ID      string `json:"id"`      // anchor on deedglass.com/install
	Client  string `json:"client"`  // the client's name
	Kind    string `json:"kind"`    // deeplink, web, command, download (only when MCPBOnRelease), config
	URL     string `json:"url"`     // a deeplink or https URL, or "" for a command
	Command string `json:"command"` // a shell command, or a config block for kind config
	Note    string `json:"note"`
}

// Links returns every client's install, in the order /install shows them.
func Links() []Link {
	cfg := ServerJSON()
	b64 := base64.StdEncoding.EncodeToString(cfg)
	withName := `{"name":"deedglass",` + strings.TrimPrefix(string(cfg), "{")
	vsQ := url.Values{"name": {Name}, "config": {string(cfg)}}.Encode()
	goose := "goose://extension?cmd=npx&arg=-y&arg=deedglass&arg=mcp&arg=serve&id=deedglass&name=Deedglass&description=" + url.PathEscape(Description)
	snippetJSON := `{"mcpServers":{"deedglass":` + string(cfg) + `}}`
	claudeDesktop := func(id, client, platform, note string) Link {
		if MCPBOnRelease {
			return Link{ID: id, Client: client, Kind: "download", URL: BundleURL(platform), Note: note}
		}
		if note != "" {
			note = "The .mcpb bundles aren't attached to a release yet; this page connects Claude Desktop by hand."
		}
		return Link{ID: id, Client: client, Kind: "web", URL: ClaudeDesktopGuide, Note: note}
	}
	return []Link{
		{ID: "cursor", Client: "Cursor", Kind: "deeplink", URL: "cursor://anysphere.cursor-deeplink/mcp/install?name=" + Name + "&config=" + url.QueryEscape(b64), Note: "Opens Cursor's install dialog."},
		{ID: "cursor-web", Client: "Cursor (web link)", Kind: "web", URL: "https://cursor.com/en/install-mcp?name=" + Name + "&config=" + url.QueryEscape(b64), Note: "The https form, for READMEs."},
		{ID: "vs-code", Client: "VS Code", Kind: "deeplink", URL: "vscode:mcp/install?" + url.QueryEscape(withName), Note: "Opens VS Code's install dialog."},
		{ID: "vs-code-web", Client: "VS Code (web link)", Kind: "web", URL: "https://vscode.dev/redirect/mcp/install?" + vsQ, Note: "The https form, for READMEs."},
		{ID: "vs-code-cli", Client: "VS Code (command)", Kind: "command", Command: "code --add-mcp '" + withName + "'"},
		{ID: "lm-studio", Client: "LM Studio", Kind: "deeplink", URL: "lmstudio://add_mcp?name=" + Name + "&config=" + url.QueryEscape(b64)},
		{ID: "goose", Client: "Goose", Kind: "deeplink", URL: goose},
		{ID: "claude-code", Client: "Claude Code", Kind: "command", Command: ClaudeCodeCommand, Note: "Every project on this machine. Use --scope project instead to write .mcp.json for your team."},
		claudeDesktop("claude-desktop", "Claude Desktop (macOS, Apple silicon)", "darwin-arm64", "Open the .mcpb file to install it."),
		claudeDesktop("claude-desktop-intel", "Claude Desktop (macOS, Intel)", "darwin-x64", ""),
		claudeDesktop("claude-desktop-windows", "Claude Desktop (Windows)", "win32-x64", ""),
		{ID: "windsurf", Client: "Windsurf", Kind: "config", Command: snippetJSON, Note: "Add to ~/.codeium/windsurf/mcp_config.json."},
		{ID: "codex", Client: "Codex CLI", Kind: "command", Command: "codex mcp add deedglass -- npx -y deedglass mcp serve", Note: "Writes ~/.codex/config.toml."},
		{ID: "gemini", Client: "Gemini CLI", Kind: "config", Command: snippetJSON, Note: "Add to .gemini/settings.json, or ~/.gemini/settings.json for every project."},
		{ID: "copilot", Client: "GitHub Copilot CLI", Kind: "command", Command: "copilot mcp add deedglass -- npx -y deedglass mcp serve", Note: "Writes ~/.copilot/mcp-config.json."},
		{ID: "docker", Client: "Docker MCP Catalog", Kind: "command", Command: "docker mcp server enable deedglass", Note: "After the catalog lists Deedglass."},
	}
}

// Decode reads a link or command back to the server object it installs (tests, check-listings).
func Decode(l Link) (ServerObject, bool) {
	var raw string
	switch l.ID {
	case "cursor", "cursor-web", "lm-studio":
		u, err := url.Parse(l.URL)
		if err != nil {
			return ServerObject{}, false
		}
		b, err := base64.StdEncoding.DecodeString(u.Query().Get("config"))
		if err != nil {
			return ServerObject{}, false
		}
		raw = string(b)
	case "vs-code":
		q, err := url.QueryUnescape(strings.TrimPrefix(l.URL, "vscode:mcp/install?"))
		if err != nil {
			return ServerObject{}, false
		}
		raw = q
	case "vs-code-web":
		u, err := url.Parse(l.URL)
		if err != nil {
			return ServerObject{}, false
		}
		raw = u.Query().Get("config")
	case "vs-code-cli":
		raw = strings.TrimSuffix(strings.TrimPrefix(l.Command, "code --add-mcp '"), "'")
	case "goose":
		u, err := url.Parse(l.URL)
		if err != nil {
			return ServerObject{}, false
		}
		return ServerObject{Command: u.Query().Get("cmd"), Args: u.Query()["arg"]}, true
	case "claude-code", "codex", "copilot":
		f := strings.Fields(strings.SplitN(l.Command, " -- ", 2)[1])
		return ServerObject{Command: f[0], Args: f[1:]}, true
	case "windsurf", "gemini":
		var m struct {
			MCPServers map[string]ServerObject `json:"mcpServers"`
		}
		if json.Unmarshal([]byte(l.Command), &m) != nil {
			return ServerObject{}, false
		}
		return m.MCPServers[Name], true
	default:
		return ServerObject{}, false
	}
	var s ServerObject
	dec := json.NewDecoder(strings.NewReader(raw))
	if err := dec.Decode(&s); err != nil {
		return ServerObject{}, false
	}
	return s, true
}

// SecretPatterns are the strings no link, listing or generated file may hold.
var SecretPatterns = []string{"dg_live_", "dg_test_", "DEEDGLASS_API_KEY=", "Bearer "}

// HasSecret reports whether s holds a key pattern or a URL with credentials (scheme://user@host).
func HasSecret(s string) bool {
	for _, p := range SecretPatterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	for i := 0; ; {
		j := strings.Index(s[i:], "://")
		if j < 0 {
			return false
		}
		rest := s[i+j+3:]
		end := strings.IndexAny(rest, "/?#\"' ")
		if end < 0 {
			end = len(rest)
		}
		if strings.Contains(rest[:end], "@") {
			return true
		}
		i += j + 3
	}
}
