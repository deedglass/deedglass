# Installing the Deedglass MCP server (for AI agents doing the setup)

Deedglass puts an agent's tool calls behind rules, checks what really happened and keeps signed
records. Its MCP server, `deedglass mcp serve`, talks to a Deedglass server, so setup has two
parts: start a local Deedglass server once, then add the MCP server to the client.

## Requirements

- Node.js 22.12 or newer (`node --version`).
- Docker, running (`docker info` succeeds). `deedglass init` starts Postgres and the server in
  Docker on their own ports.

## 1. Start a local Deedglass server (once per project)

In the user's project directory:

```sh
npx -y deedglass init --yes --client none
```

It prints each step and ends with a blocked `DROP TABLE` and an `UPDATE` checked at Proven. It
writes `.deedglass/` in that directory (an agent key in `.deedglass/quickstart.key`, mode 0600).
Don't print or copy that key; the MCP server reads it from there.

If it fails, run `npx -y deedglass doctor` and follow the fix it prints for the first failed check.

## 2. Add the MCP server

Add this server to the client's MCP settings (for Cline, `cline_mcp_settings.json`):

```json
{
  "mcpServers": {
    "deedglass": {
      "command": "npx",
      "args": ["-y", "deedglass", "mcp", "serve"]
    }
  }
}
```

Start the client from the project directory where `deedglass init` ran, so `mcp serve` finds
`.deedglass/quickstart.key`. To run it from elsewhere, set `DEEDGLASS_API_KEY` (an agent key) and
`DEEDGLASS_SERVER` (default `http://127.0.0.1:8080`) in the server's `env` instead; never write a
key into a file the user commits.

## 3. Check it

List the server's tools. Calling a tool sends it through the `postgres-safe` policy that `init`
enforced: reads run, `DROP` and `TRUNCATE` are refused with the rule's reason, and other writes
wait for a person.

## Optional: put an existing MCP server behind Deedglass

`deedglass mcp wrap` runs another stdio MCP server so its tool calls meet the rules first. Print
the exact client config with:

```sh
npx -y deedglass mcp snippet --client claude-desktop --connector <name> -- <the server's command>
```

The output has a placeholder key; the user replaces it with an agent key.

## Nothing to install? Try it first

- `npx -y deedglass play`: the demo's story in the terminal, offline, no Docker.
- `npx -y deedglass scan`: a read-only grade of the project's agent setup; it sends nothing.

Docs: https://docs.deedglass.com
