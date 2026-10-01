package snippet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// HTTPOptions describe a snippet that reaches the Gate over Streamable HTTP (S15): /mcp/native,
// or /mcp/<connector> for an upstream behind the gateway. Claude Desktop gets the stdio bridge
// (deedglass mcp serve) instead, because its remote connectors are reached from Anthropic's cloud
// and can't see 127.0.0.1 (docs/MCP_CLIENTS.md).
type HTTPOptions struct {
	Client    string
	Connector string // "native" or an upstream's name
	Server    string // http://127.0.0.1:8080
	APIKey    string // "" = PlaceholderKey (VS Code never gets one: it prompts)
	Deedglass string // absolute path of the deedglass binary (Claude Desktop's bridge)
}

// VSCodeInputID is the prompt VS Code shows for the key, stored by VS Code, never in the file.
const VSCodeInputID = "deedglass-api-key"

// ServerName is the name the client lists: deedglass for the native tools, else the upstream's.
func (o HTTPOptions) ServerName() string {
	if o.Connector == "" || o.Connector == "native" {
		return "deedglass"
	}
	return o.Connector
}

// URL is the MCP endpoint.
func (o HTTPOptions) URL() string {
	c := o.Connector
	if c == "" {
		c = "native"
	}
	return strings.TrimSuffix(o.Server, "/") + "/mcp/" + c
}

func (o HTTPOptions) key() string {
	if o.APIKey == "" {
		return PlaceholderKey
	}
	return o.APIKey
}

// BuildHTTP returns the HTTP snippet for one client.
func BuildHTTP(o HTTPOptions) ([]byte, error) {
	if !Known(o.Client) {
		return nil, fmt.Errorf("unknown client %q: use one of %s", o.Client, strings.Join(Clients, ", "))
	}
	if o.Server == "" {
		return nil, fmt.Errorf("an HTTP snippet needs the Deedglass server URL")
	}
	name, auth := o.ServerName(), "Bearer "+o.key()
	var v any
	switch o.Client {
	case "claude-code":
		return []byte("claude mcp add --transport http --header " + shQuote("Authorization: "+auth) + " " + shQuote(name) + " " + shQuote(o.URL()) + "\n"), nil
	case "claude-desktop":
		if o.Deedglass == "" {
			return nil, fmt.Errorf("the Claude Desktop snippet needs the deedglass path")
		}
		c := o.Connector
		if c == "" {
			c = "native"
		}
		v = map[string]any{"mcpServers": map[string]any{name: map[string]any{
			"command": o.Deedglass, "args": []string{"mcp", "serve", "--connector", c},
			"env": map[string]string{"DEEDGLASS_API_KEY": o.key(), "DEEDGLASS_SERVER": strings.TrimSuffix(o.Server, "/")}}}}
	case "cursor":
		v = map[string]any{"mcpServers": map[string]any{name: map[string]any{"url": o.URL(), "headers": map[string]string{"Authorization": auth}}}}
	case "windsurf":
		v = map[string]any{"mcpServers": map[string]any{name: map[string]any{"serverUrl": o.URL(), "headers": map[string]string{"Authorization": auth}}}}
	case "vscode":
		v = map[string]any{
			"inputs": []map[string]any{{"type": "promptString", "id": VSCodeInputID, "description": "Deedglass agent API key (dg_test_… or dg_live_…)", "password": true}},
			"servers": map[string]any{name: map[string]any{"type": "http", "url": o.URL(),
				"headers": map[string]string{"Authorization": "Bearer ${input:" + VSCodeInputID + "}"}}},
		}
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

// HTTPFilename is the golden file name of an HTTP snippet.
func HTTPFilename(client string) string { return "http-" + Filename(client) }
