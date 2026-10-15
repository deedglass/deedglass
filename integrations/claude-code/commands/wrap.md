---
description: Put an MCP server (for example a Postgres MCP server) behind Deedglass with deedglass mcp wrap. Prints the claude mcp add line; changes nothing until you run it.
allowed-tools: Bash(npx -y deedglass mcp snippet:*)
argument-hint: <connector-name> -- <the MCP server's command>
---

The user wants an MCP server's tool calls to go through Deedglass's rules first. Arguments:
`$ARGUMENTS` (a connector name, then `--`, then the command that starts the server).

1. If the arguments are missing, ask for the two parts: a short connector name (policies match
   tools as `<connector>.<tool>`, for example `orders.execute_sql`) and the command that starts the
   server today (for example `uvx postgres-mcp --access-mode=unrestricted`).
2. If the server runs SQL (a Postgres MCP server), add `--connector-type postgres`, so policies can
   read the SQL (`sql.kind`, `sql.tables`, `sql.has_where`).
3. Run `npx -y deedglass mcp snippet --client claude-code --connector <name> [--connector-type postgres] -- <command>`.
   It prints a `claude mcp add` line with absolute paths and a placeholder key. It uploads nothing
   and changes no file.
4. Show the line. Tell the user to replace the placeholder key in it with an agent key (from
   `npx deedglass init`, or `deedglass agent keys create`) and to run it in their own terminal.
   Never put a real key in the conversation, and never run `claude mcp add` yourself.
5. Say what changes: every tool call of that server now asks the Gate first; denied calls never
   reach the server; database URLs stay in the server's own environment.

If the user hasn't run `npx deedglass init` yet, say that first: wrap needs a running Deedglass
server and an agent key.
