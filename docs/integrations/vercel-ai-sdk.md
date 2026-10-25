# Vercel AI SDK and Deedglass

Give an AI SDK agent Deedglass's tools in one file. The file starts Deedglass's MCP server (`npx -y deedglass mcp serve`) and hands its tools to the agent: each call goes through your policy, the risky ones wait for a person, and each one is sealed as a deed you can check later. There is also a native adapter in `@deedglass/sdk`.

Tested with `ai` 7.0.148 and `@ai-sdk/mcp` 2.0.81 (2026-10-21).

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

```ts title="examples/frameworks/vercel-ai-sdk/agent.ts"
// Run where you ran `npx deedglass init`. The model goes through the AI Gateway (AI_GATEWAY_API_KEY).
import { generateText, stepCountIs } from 'ai';
import { createMCPClient } from '@ai-sdk/mcp';
import { Experimental_StdioMCPTransport as StdioClientTransport } from '@ai-sdk/mcp/mcp-stdio';

const mcp = await createMCPClient({ transport: new StdioClientTransport({ command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] }) });
try {
  const tools = await mcp.tools(); // postgres_query and so on
  const { text } = await generateText({ model: 'anthropic/claude-sonnet-5.5', tools, stopWhen: stepCountIs(5), prompt: 'How many rows are in public.orders?' });
  console.log(text);
} finally {
  await mcp.close();
}
```

Install and run:

```sh
npm install ai@7.0.148 @ai-sdk/mcp@2.0.81
node agent.ts                     # Node 24 runs the file as it is
```

**Model key:** `AI_GATEWAY_API_KEY`: the model string goes through the AI Gateway, as in the AI SDK's own example. Any model the AI SDK supports works.

**Tool names:** Deedglass lists its tools with names every model API takes: `postgres_query`, `postgres_execute`, `deedglass_check_approval` (letters, digits, `_` and `-`; Anthropic's rule is `^[a-zA-Z0-9_-]{1,128}$` and OpenAI's allows the same characters up to 64). Your policies and the sealed records keep the dotted name, `postgres.query`; Deedglass maps one to the other ([MCP.md](../MCP.md#tool-names)). Deedglass 0.7 and earlier listed the dotted names, which these APIs refuse. The AI SDK calls a tool by its key in the tools object.

**What you see:** the answer (500 rows), and in Console › Ledger or `npx deedglass actions list` one sealed `postgres.query` action per call, with the rule that allowed it. If the agent tries a write, the tool answers that it is waiting for approval and nothing has run; once someone approves, `deedglass_check_approval` lets the agent carry on. A refused call answers with the policy's sentence and a record id.

**First start:** `npx -y deedglass` can take several seconds the first time. If a framework's MCP connect timeout is shorter, run `npx -y deedglass version` once first, or install the CLI ([INSTALL.md](../INSTALL.md)) and use `deedglass` as the command.

## The native adapter

`@deedglass/sdk` has an AI SDK adapter ([its page](../sdk/adapters/ai-sdk.md)). `deedglassTools` asks Deedglass for the org's connector tools and names them `postgres__query` and so on; Deedglass runs each call with the connector's credentials. `governTools` puts Deedglass in front of tools you wrote yourself.

```ts title="examples/frameworks/vercel-ai-sdk/native.ts"
// The native route: @deedglass/sdk's AI SDK adapter. Needs AI_GATEWAY_API_KEY, DEEDGLASS_API_KEY and
// DEEDGLASS_SERVER.
import { generateText, stepCountIs } from 'ai';
import { Deedglass } from '@deedglass/sdk';
import { deedglassTools } from '@deedglass/sdk/ai-sdk';

// The org's connector tools, named postgres__query and so on; Deedglass runs each one.
const tools = await deedglassTools(new Deedglass(), { include: ['postgres.*'] });
const { text } = await generateText({ model: 'anthropic/claude-sonnet-5.5', tools, stopWhen: stepCountIs(5), prompt: 'How many rows are in public.orders?' });
console.log(text);
```

```sh
npm install ai@7.0.148 @deedglass/sdk@0.6.0
export DEEDGLASS_API_KEY="$(cat .deedglass/quickstart.key)" DEEDGLASS_SERVER=http://127.0.0.1:8080
node native.ts
```

Use the port `init` printed if it isn't 8080.

## Check it without a model key

`examples/frameworks/vercel-ai-sdk/` has the file, its pinned packages and a `check` file that needs no model
key: it starts `deedglass mcp serve` through the same framework client and the native adapter, and runs `postgres_query` once through each with the tool's own `execute()`. From the folder where
`init` ran:

```sh
path/to/deedglass/examples/frameworks/run.sh vercel-ai-sdk            # install the pins in a temp folder, run the check
path/to/deedglass/examples/frameworks/run.sh vercel-ai-sdk --agent    # also run the agent file when its model key is set
```

The workflow `frameworks.yml` runs every check against a fresh `init` stack each week and on pull
requests that touch these files. It isn't a required check: a framework release that doesn't install
is reported as skipped, not as a failure.

## Sources and what isn't verified

Every framework API in the files above comes from the framework's own docs, checked 2026-10-21:

- `createMCPClient`, `Experimental_StdioMCPTransport` from `@ai-sdk/mcp/mcp-stdio`, `mcpClient.tools()`, `mcpClient.close()` in `finally`: https://ai-sdk.dev/docs/ai-sdk-core/mcp-tools
- `generateText` with `tools` and `stopWhen: stepCountIs(n)`: https://ai-sdk.dev/docs/ai-sdk-core/tools-and-tool-calling
- Tool-name rules: the MCP spec's tool names (https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-names) and Anthropic's tool definitions (https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools). OpenAI's API reference returned 403 when we checked; its rule here is the one `@deedglass/sdk`'s OpenAI adapter already follows.

Not verified:

- The docs page shows `streamText`; this file uses `generateText` with the same `tools` object.
- That the tools object works end to end with a live model: the check runs `execute()` on `postgres_query`, but no model ran in our tests (no model key in CI).
