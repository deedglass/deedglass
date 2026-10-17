# Installing the Deedglass MCP server (for AI agents doing the setup)

Deedglass puts an agent's tool calls behind rules, checks what really happened and keeps signed
records. Its MCP server, `deedglass mcp serve`, talks to a Deedglass server, so setup has two
parts: start a local Deedglass server once, then add the MCP server to the client.

**Ask the user before each change.** Each numbered step below changes something on the user's
machine; say what it changes, in one or two sentences, and run it only after the user says yes. If
they say no, stop there: the steps that need it can't run yet. Never skip a question because an
earlier one was answered.

## Requirements

- Node.js 22.12 or newer (`node --version`).
- Docker, running (`docker info` succeeds).

Checking these changes nothing.

## 1. Start a local Deedglass server (once per project)

Ask first. In the user's project directory this command:

- starts two Postgres 18.6 containers in Docker (Deedglass's own database and a demo database), on
  ports of their own;
- starts `deedglass serve` in the background on `127.0.0.1:8080`;
- writes `.deedglass/` in that directory, with an agent key in `.deedglass/quickstart.key` (mode
  0600).

```sh
npx -y deedglass init --client none
```

It prints each step and ends with a blocked `DROP TABLE` and an `UPDATE` checked at Proven. Don't
print or copy the key; the MCP server reads it from `.deedglass/`.

If it fails, run `npx -y deedglass doctor` (read-only) and tell the user the fix it prints for the
first failed check. Ask before running that fix.

## 2. Add the MCP server

Ask first: this edits the client's MCP settings file (for Cline, `cline_mcp_settings.json`). Show
the user the entry you will add:

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
`.deedglass/quickstart.key`. To run it from elsewhere, the user sets `DEEDGLASS_API_KEY` (an agent
key) and `DEEDGLASS_SERVER` (default `http://127.0.0.1:8080`) in the server's `env` instead; never
write a key into a file the user commits.

## 3. Check it

List the server's tools; that changes nothing. Calling a tool sends it through the `postgres-safe`
policy that `init` enforced: reads run, `DROP` and `TRUNCATE` are refused with the rule's reason,
and other writes wait for a person. Ask before calling a tool that writes.

## Optional: put an existing MCP server behind Deedglass

`deedglass mcp wrap` runs another stdio MCP server so its tool calls meet the rules first. Print the
exact client config with the command below (it prints and changes nothing), show it to the user,
and ask before editing their client config with it:

```sh
npx -y deedglass mcp snippet --client claude-desktop --connector <name> -- <the server's command>
```

The output has a placeholder key; the user replaces it with an agent key.

## Nothing to install? Try it first

Neither of these sends anything anywhere or needs Docker:

- `npx -y deedglass play`: the demo's story in the terminal, offline.
- `npx -y deedglass scan --no-write`: a read-only grade of the project's agent setup, printed only.
  Without `--no-write` it also writes `deedglass-scan.md` and `deedglass-scan.html`, so ask first.

Giving an agent Postgres access carefully:
[`integrations/claude-code/skills/safe-postgres-agents/SKILL.md`](integrations/claude-code/skills/safe-postgres-agents/SKILL.md).

Docs: https://docs.deedglass.com
