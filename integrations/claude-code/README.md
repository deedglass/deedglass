# Deedglass for Claude Code

A Claude Code plugin that adds:

- the Deedglass MCP server (`npx -y deedglass mcp serve`), the same server object as every other
  install link;
- `/deedglass:scan`, which runs `npx -y deedglass scan` in your project and explains the grade;
- `/deedglass:wrap`, which prints the `claude mcp add` line that runs one of your MCP servers (for
  example a Postgres MCP server) through `deedglass mcp wrap`, so its tool calls meet your rules
  first. It prints; you run it, with your own key;
- `/deedglass:watch`, which prints the `deedglass observe run … -- claude --continue` line that
  restarts your session inside a watched session: when you quit, you see the row changes Postgres
  committed on the watched tables (shell commands included), what Deedglass caught, and the undo
  SQL saved for you to review and run. It prints; you run it, with your own key;
- the `read-a-scan` skill, which explains a scan result or a Deedglass block message;
- the `safe-postgres-agents` skill: how to give an agent Postgres access in layers (a
  least-privilege role, a scan, rules before each call, a check of what Postgres committed, undo
  with a person in the loop).
- the shell guard, off until you turn it on: a `PreToolUse` hook that asks you first when a Bash
  command would destroy database data (`prisma migrate reset`, `alembic downgrade base`, `dropdb`,
  a `psql -c` with `DROP TABLE`). A second check, not a sandbox.

## Install

```
/plugin marketplace add deedglass/deedglass
/plugin install deedglass@deedglass
```

Run `npx deedglass init` in a terminal first. `mcp serve` reads the key it wrote
(`.deedglass/quickstart.key`) or `DEEDGLASS_API_KEY`.

## The shell guard (off by default)

`hooks/hooks.json` registers one hook: before each Bash command, `hooks/shell-guard.sh` runs. Until
you set `DEEDGLASS_SHELL_GUARD`, it reads its input and answers nothing, so Claude Code behaves as if
the hook weren't there. To turn it on, put this in `.claude/settings.json` or
`~/.claude/settings.json` and restart Claude Code:

```json
{
  "env": { "DEEDGLASS_SHELL_GUARD": "ask" }
}
```

With `ask`, a command that would destroy database data waits for your yes, with one sentence such as
"Deedglass asks first: prisma db push --force-reset resets the database before it pushes the schema,
which deletes every row." With `deny`, it doesn't run and Claude reads why. It needs a `deedglass`
binary with the `guard shell` command on your `PATH`; it runs that binary locally and uses no
network. It never answers allow, and your own permission rules still apply. What it reads, the
catalog and what it can't see: [docs/SHELL_GUARD.md](../../docs/SHELL_GUARD.md).

If it is on but can't check (no `deedglass` on your `PATH`, or an older one without `guard shell`),
it never blocks the command: with `ask` it answers nothing and shows you a note saying it checked
nothing; with `deny` it exits 1, which Claude Code shows as a non-blocking error.

**On Windows**, the hook runs `sh`, which Git for Windows provides. The hook sets
`"onFailure": "continue"`, so where it can't start (no Git Bash, so Claude Code would run it in
PowerShell) or times out, your Bash commands still run, unchecked. Without Git Bash, Claude Code
has no Bash tool, so the hook never runs and the guard checks nothing. To use the guard on Windows,
install Git for Windows and a `deedglass` binary on your `PATH`. `onFailure` needs Claude Code
v2.1.295 or later.

## What it doesn't do

- One hook, off by default (above). It never approves a tool call. Putting MCP tools behind
  Deedglass stays your explicit `deedglass mcp wrap` step (`/deedglass:wrap` prints it; it never
  runs it).
- It doesn't open watched sessions for you: you run `observe run` with your admin key, and it keeps
  every `DEEDGLASS_*` variable out of Claude Code's environment. Claude Code can still read files
  you can, so keep the admin key out of the project: `init` saves it in `.deedglass/admin.key`.
  Inside a watched session this plugin's MCP server finds no key in its environment, so its tools
  refuse calls until you quit, unless it finds the agent key `init` wrote in `.deedglass/` above
  the folder.
- It doesn't start a wrapped server for you: that needs your database URL and an agent key, which
  stay in your own `claude mcp add` line, never in this plugin.
- No telemetry, and no key in any file here.
- The scan reads your files and, if you set one, your database role; it uploads nothing.
