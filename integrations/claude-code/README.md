# Deedglass for Claude Code

A Claude Code plugin that adds:

- the Deedglass MCP server (`npx -y deedglass mcp serve`), the same server object as every other
  install link;
- `/deedglass:scan`, which runs `npx -y deedglass scan` in your project and explains the grade;
- the `read-a-scan` skill, which explains a scan result or a Deedglass block message.

## Install

Once the repository is public:

```
/plugin marketplace add deedglass/deedglass
/plugin install deedglass@deedglass
```

Run `npx deedglass init` in a terminal first. `mcp serve` reads the key it wrote
(`.deedglass/quickstart.key`) or `DEEDGLASS_API_KEY`.

## What it doesn't do

- No hooks: it never changes how Claude Code runs a tool. Putting tools behind Deedglass stays
  your explicit `deedglass mcp wrap` step.
- No telemetry, and no key in any file here.
- The scan reads your files and, if you set one, your database role; it uploads nothing.
