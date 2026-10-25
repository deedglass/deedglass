# Mastra and Deedglass

Give a Mastra agent Deedglass's tools in one file. The file starts Deedglass's MCP server (`npx -y deedglass mcp serve`) and hands its tools to the agent: each call goes through your policy, the risky ones wait for a person, and each one is sealed as a deed you can check later.

Tested with `@mastra/core` 1.79.0 and `@mastra/mcp` 2.5.0 (2026-10-21).

## Before you start

1. In any folder, run `npx deedglass init` (Docker, about 15 seconds; [the quickstart](../QUICKSTART.md)).
   It starts Deedglass and a demo Postgres with 500 orders, and saves an agent key in
   `.deedglass/quickstart.key`.
2. Run the file below **from that folder** (or a folder inside it). `deedglass mcp serve` looks for
   the key there; from anywhere else, set `DEEDGLASS_API_KEY` and `DEEDGLASS_SERVER` yourself
   ([how mcp serve finds them](../INSTALL_CLIENTS.md)).
3. Under the `postgres-safe` policy that `init` turns on, reads run, `DROP` and `TRUNCATE` are
   refused, and a write waits for a person in Console › Approvals before it runs.

## The MCP route

```ts title="examples/frameworks/mastra/agent.ts"
// Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
import { Agent } from '@mastra/core/agent';
import { MCPClient } from '@mastra/mcp';

const mcp = new MCPClient({ id: 'deedglass', servers: { deedglass: { command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] } } });
try {
  // deedglass_postgres.query -> deedglass_postgres_query: model APIs reject dots in tool names.
  const tools = Object.fromEntries(Object.entries(await mcp.listTools()).map(([name, t]) => [name.replace('.', '_'), t]));
  const agent = new Agent({ id: 'orders', name: 'Orders', instructions: 'Answer questions about orders.', model: 'openai/gpt-5.6-sol', tools });
  console.log((await agent.generate('How many rows are in public.orders?')).text);
} finally {
  await mcp.disconnect();
}
```

Install and run:

```sh
npm install @mastra/core@1.79.0 @mastra/mcp@2.5.0 zod@4.6.7
node agent.ts
```

**Model key:** `OPENAI_API_KEY` (the model string is the one in Mastra's agent docs). Any model in Mastra's router format works.

**Tool names:** Mastra prefixes each tool with its server's key, so the tools arrive as `deedglass_postgres.query`. OpenAI's and Anthropic's APIs take only letters, digits, `_` and `-` in tool names, so the file renames each key (`deedglass_postgres_query`).

**What you see:** the answer (500 rows), and in Console › Ledger or `npx deedglass actions list` one sealed `postgres.query` action per call, with the rule that allowed it. If the agent tries a write, the tool answers that it is waiting for approval and nothing has run; once someone approves, `deedglass.check_approval` lets the agent carry on. A refused call answers with the policy's sentence and a record id.

**First start:** `npx -y deedglass` can take several seconds the first time. If a framework's MCP connect timeout is shorter, run `npx -y deedglass version` once first, or install the CLI ([INSTALL.md](../INSTALL.md)) and use `deedglass` as the command.

## Check it without a model key

`examples/frameworks/mastra/` has the file, its pinned packages and a `check` file that needs no model
key: it starts `deedglass mcp serve` through the same framework client, lists its tools and builds the Agent with them renamed as the file does. From the folder where
`init` ran:

```sh
path/to/deedglass/examples/frameworks/run.sh mastra            # install the pins in a temp folder, run the check
path/to/deedglass/examples/frameworks/run.sh mastra --agent    # also run the agent file when its model key is set
```

The workflow `frameworks.yml` runs every check against a fresh `init` stack each week and on pull
requests that touch these files. It isn't a required check: a framework release that doesn't install
is reported as skipped, not as a failure.

## Sources and what isn't verified

Every framework API in the files above comes from the framework's own docs, checked 2026-10-21:

- `new MCPClient({ id, servers: { name: { command, args } } })`, `await mcpClient.listTools()`, `disconnect()`: https://mastra.ai/docs/tools-mcp/mcp-overview
- `new Agent({ id, name, instructions, model, tools })`, `agent.generate(…)`, `.text`: https://mastra.ai/docs/agents/overview
- `requireToolApproval` per MCP server, to ask your own user before Deedglass sees the call: https://mastra.ai/docs/agents/agent-approval
- Tool-name rules: the MCP spec's tool names (https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-names) and Anthropic's tool definitions (https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools). OpenAI's API reference returned 403 when we checked; its rule here is the one `@deedglass/sdk`'s OpenAI adapter already follows.

Not verified:

- Mastra's docs register agents in a `Mastra` instance and call `getAgentById`; this file calls `generate()` on the agent directly.
- That Mastra calls a tool by its key in the tools object, so the renamed keys work with a live model (no model key in CI).
