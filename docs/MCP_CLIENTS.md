# MCP client configs: `mcp wrap` and the HTTP endpoints

`deedglass mcp snippet` prints a ready config for each client below. It resolves `deedglass` and
the server's command to absolute paths and sets `PATH` in `env`, because GUI apps on macOS start
with a minimal `PATH` and won't find `npx` or `node` otherwise. The key is the placeholder
`dg_test_REPLACE_ME` unless you pass `--api-key-from-env` (which copies `DEEDGLASS_API_KEY` from
your shell into the output; keep that output out of chat, tickets and git).

```sh
deedglass mcp snippet --client claude-desktop --connector scratch -- \
  npx -y @modelcontextprotocol/server-filesystem /tmp/deedglass-demo
```

Wrap flags given to `snippet` (`--connector-type postgres`) are copied into the generated `args`.
The examples on this page and in `examples/clients/` use `/Users/you/…` paths; yours will be your
own. Client formats were checked on 2026-10-01 against the pages linked under each heading.

## Claude Desktop

- **File:** `~/Library/Application Support/Claude/claude_desktop_config.json` (Windows:
  `%APPDATA%\Claude\claude_desktop_config.json`). Open it from Claude Desktop › Settings ›
  Developer › Edit Config.
- **Shape:** top-level `mcpServers`, one key per server, `command`, `args`, `env`.
- **After editing:** quit Claude Desktop completely (⌘Q) and open it again.
- **Logs:** `~/Library/Logs/Claude/mcp.log` and `mcp-server-<name>.log`, which holds the wrapper's
  stderr lines.
- Source: https://modelcontextprotocol.io/docs/develop/connect-local-servers (checked 2026-10-01).

```json
{
  "mcpServers": {
    "scratch": {
      "command": "/Users/you/txv/product/bin/deedglass",
      "args": ["mcp", "wrap", "--connector", "scratch", "--",
               "/usr/local/bin/npx", "-y", "@modelcontextprotocol/server-filesystem@2026.8.31", "/tmp/deedglass-demo"],
      "env": {
        "DEEDGLASS_API_KEY": "dg_test_REPLACE_ME",
        "DEEDGLASS_SERVER": "http://127.0.0.1:8080",
        "PATH": "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"
      }
    }
  }
}
```

## Claude Code

- **Command:** `claude mcp add`, with `--env` pairs and `--transport stdio` before the server name
  (a name right after `--env` is read as another pair), then `--` and the command.
- **Or a file:** `.mcp.json` in the project, `mcpServers` with `command`, `args` and `env`; values
  may use `${DEEDGLASS_API_KEY}` expansion, which keeps the key out of the file.
- **Tool names:** Claude Code shows MCP tools as `mcp__<server>__<tool>` and replaces characters
  other than letters, digits, `_` and `-` with `_`, so `deedglass.check_approval` appears as
  `mcp__scratch__deedglass_check_approval`. That is expected.
- Source: https://code.claude.com/docs/en/mcp (checked 2026-10-01).

```sh
claude mcp add --env DEEDGLASS_API_KEY=dg_test_REPLACE_ME --env DEEDGLASS_SERVER=http://127.0.0.1:8080 \
  --env PATH=/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin --transport stdio scratch -- \
  /Users/you/txv/product/bin/deedglass mcp wrap --connector scratch -- \
  /usr/local/bin/npx -y @modelcontextprotocol/server-filesystem@2026.8.31 /tmp/deedglass-demo
```

## Cursor

- **File:** `~/.cursor/mcp.json` (every project) or `.cursor/mcp.json` (one project).
- **Shape:** `mcpServers` with `command`, `args`, `env`, as for Claude Desktop. Values may use
  `${env:NAME}`, so `"DEEDGLASS_API_KEY": "${env:DEEDGLASS_API_KEY}"` keeps the key out of the file.
- Source: https://cursor.com/docs/context/mcp (checked 2026-10-01).

## VS Code

- **File:** `.vscode/mcp.json` in the workspace.
- **Shape:** top-level `servers` (not `mcpServers`), each with `"type": "stdio"`, `command`, `args`,
  `env`. VS Code supports input variables for secrets.
- Source: https://code.visualstudio.com/docs/copilot/customization/mcp-servers (checked 2026-10-01).

```json
{
  "servers": {
    "scratch": {
      "type": "stdio",
      "command": "/Users/you/txv/product/bin/deedglass",
      "args": ["mcp", "wrap", "--connector", "scratch", "--", "/usr/local/bin/npx", "-y",
               "@modelcontextprotocol/server-filesystem@2026.8.31", "/tmp/deedglass-demo"],
      "env": { "DEEDGLASS_API_KEY": "dg_test_REPLACE_ME", "DEEDGLASS_SERVER": "http://127.0.0.1:8080",
               "PATH": "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin" }
    }
  }
}
```

## Windsurf

- **File:** `~/.codeium/windsurf/mcp_config.json`, `mcpServers` with `command`, `args`, `env`.
- **Note:** on 2026-10-01 Windsurf's MCP documentation URL redirected to Devin Desktop's
  (https://docs.devin.ai/desktop/cascade/mcp), which documents the same `mcpServers` shape at
  `~/.config/devin/mcp_config.json` for the legacy Cascade agent. Check the path your version uses;
  the JSON is the same.

## Over HTTP: `/mcp/native` and the gateway (S15)

`deedglass mcp snippet --transport http --client <client> --connector native|<upstream>` prints
config that reaches the Gate's HTTP endpoints instead of wrapping a server. The key is a
placeholder unless `--api-key-from-env` is given; VS Code never gets one in the file (it prompts).
Formats checked on 2026-10-02 against each client's documentation:

| Client | Shape | Source |
|---|---|---|
| Claude Code | `claude mcp add --transport http --header "Authorization: Bearer …" deedglass http://127.0.0.1:8080/mcp/native` | https://code.claude.com/docs/en/mcp |
| Cursor | `mcpServers.deedglass` with `url` and `headers` (headers accept `${env:NAME}`) | https://cursor.com/docs/context/mcp |
| VS Code | `servers.deedglass` with `"type": "http"`, `url`, `headers` using `${input:deedglass-api-key}`, and an `inputs` entry with `"password": true` | https://code.visualstudio.com/docs/agents/reference/mcp-configuration |
| Windsurf | `mcpServers.deedglass` with `serverUrl` and `headers` (the docs now live at Devin Desktop, `~/.config/devin/mcp_config.json`) | https://docs.devin.ai/desktop/cascade/mcp |
| Claude Desktop | the stdio bridge: `command` = `deedglass`, `args` = `mcp serve --connector native`, `env` with the key and server | https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp |

**Why Claude Desktop gets the bridge.** Its custom connectors connect to a remote MCP server "from
Anthropic's cloud infrastructure", so a server on your `127.0.0.1` isn't reachable that way; local
servers belong in `claude_desktop_config.json`, which runs them on your machine. The bridge is that
local server. The generated copies are `examples/clients/http-*`.

## Every client

- Generated, golden-tested copies are in `examples/clients/` (`go generate ./internal/mcp/snippet`).
- The wrapper's own log is `~/.deedglass/logs/mcp-<connector>.log`.
- To check the setup without a client, run the wrapper by hand and type
  `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` after an `initialize`; or use the
  MCP Inspector.
