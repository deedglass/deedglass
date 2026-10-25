# OpenAI Agents SDK and Deedglass

Give an OpenAI Agents SDK agent, in Python or TypeScript, Deedglass's tools in one file. The file starts Deedglass's MCP server (`npx -y deedglass mcp serve`) and hands its tools to the agent: each call goes through your policy, the risky ones wait for a person, and each one is sealed as a deed you can check later. There is also a native adapter in `@deedglass/sdk`.

Tested with `openai-agents` 0.24.0 for Python and `@openai/agents` 0.21.0 (2026-10-21).

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

```python title="examples/frameworks/openai-agents/agent.py"
# Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
import asyncio
from agents import Agent, Runner
from agents.mcp import MCPServerStdio

async def main():
    params = {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}
    async with MCPServerStdio(name="deedglass", params=params) as server:
        agent = Agent(name="orders", instructions="Answer questions about orders.", mcp_servers=[server])
        result = await Runner.run(agent, "How many rows are in public.orders?")
        print(result.final_output)

asyncio.run(main())
```

```ts title="examples/frameworks/openai-agents/agent.ts"
// Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
import { Agent, MCPServerStdio, run } from '@openai/agents';

const server = new MCPServerStdio({ name: 'deedglass', fullCommand: 'npx -y deedglass mcp serve' });
await server.connect();
try {
  const agent = new Agent({ name: 'orders', instructions: 'Answer questions about orders.', mcpServers: [server] });
  const result = await run(agent, 'How many rows are in public.orders?');
  console.log(result.finalOutput);
} finally {
  await server.close();
}
```

Install and run:

```sh
pip install openai-agents==0.24.0 && python agent.py
npm install @openai/agents@0.21.0 zod@4.6.7 && node agent.ts
```

**Model key:** `OPENAI_API_KEY`. The agent uses the SDK's default model.

**Tool names:** Deedglass lists its tools with names every model API takes: `postgres_query`, `postgres_execute`, `deedglass_check_approval` (letters, digits, `_` and `-`; Anthropic's rule is `^[a-zA-Z0-9_-]{1,128}$` and OpenAI's allows the same characters up to 64). Your policies and the sealed records keep the dotted name, `postgres.query`; Deedglass maps one to the other ([MCP.md](../MCP.md#tool-names)). Deedglass 0.7 and earlier listed the dotted names, which these APIs refuse. `include_server_in_tool_names` (`includeServerInToolNames` in TypeScript) is no longer needed; turn it on only to tell several servers' tools apart.

**What you see:** the answer (500 rows), and in Console › Ledger or `npx deedglass actions list` one sealed `postgres.query` action per call, with the rule that allowed it. If the agent tries a write, the tool answers that it is waiting for approval and nothing has run; once someone approves, `deedglass_check_approval` lets the agent carry on. A refused call answers with the policy's sentence and a record id.

**First start:** `npx -y deedglass` can take several seconds the first time. If a framework's MCP connect timeout is shorter, run `npx -y deedglass version` once first, or install the CLI ([INSTALL.md](../INSTALL.md)) and use `deedglass` as the command.

## The native adapter

`@deedglass/sdk` has an OpenAI Agents adapter for TypeScript ([its page](../sdk/adapters/openai-agents.md)). `deedglassTools` asks Deedglass for the org's connector tools as `FunctionTool`s named `postgres__query` and so on; Deedglass runs each call. `governTool` puts Deedglass in front of a tool you wrote. There is no Python adapter yet: `deedglass.openai_agents` is a placeholder that raises `ImportError`.

```ts title="examples/frameworks/openai-agents/native.ts"
// The native route: @deedglass/sdk's OpenAI Agents adapter. Needs DEEDGLASS_API_KEY, DEEDGLASS_SERVER
// and OPENAI_API_KEY.
import { Agent, run } from '@openai/agents';
import { Deedglass } from '@deedglass/sdk';
import { deedglassTools } from '@deedglass/sdk/openai-agents';

// The org's connector tools, named postgres__query and so on; Deedglass runs each one.
const tools = await deedglassTools(new Deedglass(), { include: ['postgres.*'] });
const agent = new Agent({ name: 'orders', instructions: 'Answer questions about orders.', tools });
const result = await run(agent, 'How many rows are in public.orders?');
console.log(result.finalOutput);
```

```sh
npm install @openai/agents@0.21.0 zod@4.6.7 @deedglass/sdk@0.6.0
export DEEDGLASS_API_KEY="$(cat .deedglass/quickstart.key)" DEEDGLASS_SERVER=http://127.0.0.1:8080
node native.ts
```

Use the port `init` printed if it isn't 8080.

## Check it without a model key

`examples/frameworks/openai-agents/` has the file, its pinned packages and a `check` file that needs no model
key: it starts `deedglass mcp serve` through the same framework client, lists its tools (Python) or connects (TypeScript), builds the Agent with the same `mcp_config`, and lists the native adapter's tools. From the folder where
`init` ran:

```sh
path/to/deedglass/examples/frameworks/run.sh openai-agents            # install the pins in a temp folder, run the check
path/to/deedglass/examples/frameworks/run.sh openai-agents --agent    # also run the agent file when its model key is set
```

The workflow `frameworks.yml` runs every check against a fresh `init` stack each week and on pull
requests that touch these files. It isn't a required check: a framework release that doesn't install
is reported as skipped, not as a failure.

## Sources and what isn't verified

Every framework API in the files above comes from the framework's own docs, checked 2026-10-21:

- `MCPServerStdio` with `params={"command", "args"}` as an async context manager, `mcp_servers=[server]`, `list_tools()`: https://openai.github.io/openai-agents-python/mcp/
- `MCPServerStdio({ name, fullCommand })`, `connect()`, `close()`, `mcpServers`: https://openai.github.io/openai-agents-js/guides/mcp/
- `tool_input_guardrails` on a local MCP server, for checks of your own before Deedglass sees the call: https://openai.github.io/openai-agents-python/guardrails/
- Tool-name rules: the MCP spec's tool names (https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-names) and Anthropic's tool definitions (https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools). OpenAI's API reference returned 403 when we checked; its rule here is the one `@deedglass/sdk`'s OpenAI adapter already follows.

Not verified:

- A run with a live model (no model key in CI). The JavaScript docs don't document a public `listTools()`, so the TypeScript check only connects.
