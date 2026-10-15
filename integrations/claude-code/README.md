# Deedglass for Claude Code

A Claude Code plugin that adds:

- the Deedglass MCP server (`npx -y deedglass mcp serve`), the same server object as every other
  install link;
- `/deedglass:scan`, which runs `npx -y deedglass scan` in your project and explains the grade;
- `/deedglass:wrap`, which prints the `claude mcp add` line that runs one of your MCP servers (for
  example a Postgres MCP server) through `deedglass mcp wrap`, so its tool calls meet your rules
  first. It prints; you run it, with your own key;
- the `read-a-scan` skill, which explains a scan result or a Deedglass block message;
- the `safe-postgres-agents` skill: how to give an agent Postgres access in layers (a
  least-privilege role, a scan, rules before each call, a check of what Postgres committed, undo
  with a person in the loop).

## Install

```
/plugin marketplace add deedglass/deedglass
/plugin install deedglass@deedglass
```

Run `npx deedglass init` in a terminal first. `mcp serve` reads the key it wrote
(`.deedglass/quickstart.key`) or `DEEDGLASS_API_KEY`.

## What it doesn't do

- No hooks: it never changes how Claude Code runs a tool. Putting tools behind Deedglass stays
  your explicit `deedglass mcp wrap` step (`/deedglass:wrap` prints it; it never runs it).
- It doesn't start a wrapped server for you: that needs your database URL and an agent key, which
  stay in your own `claude mcp add` line, never in this plugin.
- No telemetry, and no key in any file here.
- The scan reads your files and, if you set one, your database role; it uploads nothing.
