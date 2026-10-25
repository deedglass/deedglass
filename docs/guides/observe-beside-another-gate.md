# Observe mode beside another gate

You may already route your agent's tools through something else: an MCP proxy, a sandbox, an
approval bot, or nothing at all. Observe mode doesn't sit in that path. It reads only the Postgres
change log, through the `deedglass_witness` role and the replication slot that
`deedglass connector postgres init` creates, so the agent keeps connecting exactly as it does now.

1. Run `deedglass connector postgres init --connector pg-demo --apply` once (a plan you review, then
   one transaction).
2. Turn on `witness.observe.enabled` in the connector's config.
3. Wrap the agent's run: `deedglass observe run --connector pg-demo -- <the agent's command>`, or
   open and close sessions from the Console around a run you start elsewhere.

What you get: every transaction Postgres committed on the watched tables during the run, sealed in
the record, with the undo SQL to put it back (free) or a one-step undo (also free).

What you don't get: Deedglass's own decisions. Observe mode doesn't allow or deny anything, doesn't
see the tool calls, and can't tell your agent's writes from another program's in the same window.
For decisions before a write, route the tools through the Gate.

What an MCP gateway does and what Deedglass adds beside it, side by side:
[MCP gateways and Deedglass](https://deedglass.com/compare/mcp-gateways).
