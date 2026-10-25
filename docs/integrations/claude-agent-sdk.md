# Claude Agent SDK and Deedglass

Give a Claude Agent SDK agent, in TypeScript or Python, Deedglass's tools in one file. The file starts Deedglass's MCP server (`npx -y deedglass mcp serve`) and hands its tools to the agent: each call goes through your policy, the risky ones wait for a person, and each one is sealed as a deed you can check later. There is also a native adapter in `@deedglass/sdk`.

Tested with `@anthropic-ai/claude-agent-sdk` 0.3.306 and `claude-agent-sdk` 0.2.170 for Python (2026-10-21).

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

```ts title="examples/frameworks/claude-agent-sdk/agent.ts"
// Run where you ran `npx deedglass init`. Needs ANTHROPIC_API_KEY.
import { query } from '@anthropic-ai/claude-agent-sdk';

for await (const message of query({
  prompt: 'How many rows are in public.orders?',
  options: {
    mcpServers: { deedglass: { command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] } },
    allowedTools: ['mcp__deedglass__*'], // Claude Code names them mcp__deedglass__postgres_query and so on
  },
})) {
  if (message.type === 'result' && message.subtype === 'success') console.log(message.result);
}
```

```python title="examples/frameworks/claude-agent-sdk/agent.py"
# Run where you ran `npx deedglass init`. Needs ANTHROPIC_API_KEY.
import asyncio
from claude_agent_sdk import ClaudeAgentOptions, ResultMessage, query

options = ClaudeAgentOptions(
    mcp_servers={"deedglass": {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}},
    allowed_tools=["mcp__deedglass__*"],  # Claude Code names them mcp__deedglass__postgres_query and so on
)

async def main():
    async for message in query(prompt="How many rows are in public.orders?", options=options):
        if isinstance(message, ResultMessage) and message.subtype == "success":
            print(message.result)

asyncio.run(main())
```

Install and run:

```sh
npm install @anthropic-ai/claude-agent-sdk@0.3.306 && node agent.ts
pip install claude-agent-sdk==0.2.170 && python agent.py
```

**Model key:** `ANTHROPIC_API_KEY`, or any sign-in the Agent SDK accepts.

**Tool names:** The Agent SDK names MCP tools `mcp__<server>__<tool>`, so `postgres_query` reaches Claude as `mcp__deedglass__postgres_query` (Claude Code's MCP docs). `allowedTools: ['mcp__deedglass__*']` lets Claude call them without a permission prompt; Deedglass's policy still decides each call. Policies and records keep the dotted name, `postgres.query`.

**What you see:** the answer (500 rows), and in Console › Ledger or `npx deedglass actions list` one sealed `postgres.query` action per call, with the rule that allowed it. If the agent tries a write, the tool answers that it is waiting for approval and nothing has run; once someone approves, `deedglass_check_approval` lets the agent carry on. A refused call answers with the policy's sentence and a record id.

**First start:** `npx -y deedglass` can take several seconds the first time. If a framework's MCP connect timeout is shorter, run `npx -y deedglass version` once first, or install the CLI ([INSTALL.md](../INSTALL.md)) and use `deedglass` as the command.

## The native adapter

`@deedglass/sdk` has a Claude Agent SDK adapter ([its page](../sdk/adapters/claude-agent-sdk.md)). `deedglassHooks` asks Deedglass in `PreToolUse` before every tool Claude runs (`Bash`, `Write`, MCP tools) and reports the result in `PostToolUse`; a refusal reaches Claude as `permissionDecision: 'deny'` with the policy's sentence. `deedglassMcpServer` points the SDK at Deedglass's own tools over HTTP. The Python SDK has the same pair (`deedglass.claude_agent_sdk`); it isn't on PyPI yet ([SDK_PYTHON.md](../SDK_PYTHON.md)).

```ts title="examples/frameworks/claude-agent-sdk/native.ts"
// The native route: @deedglass/sdk's hooks ask Deedglass before every tool Claude runs (Bash, Write,
// MCP tools), and Deedglass's own tools come over HTTP. Needs DEEDGLASS_API_KEY, DEEDGLASS_SERVER
// and ANTHROPIC_API_KEY.
import { query } from '@anthropic-ai/claude-agent-sdk';
import { Deedglass } from '@deedglass/sdk';
import { deedglassHooks, deedglassMcpServer } from '@deedglass/sdk/claude-agent-sdk';

const dg = new Deedglass();
const options = { hooks: deedglassHooks({ client: dg }), mcpServers: { deedglass: deedglassMcpServer(dg) }, allowedTools: ['mcp__deedglass__*'] };
for await (const message of query({ prompt: 'How many rows are in public.orders?', options })) {
  if (message.type === 'result' && message.subtype === 'success') console.log(message.result);
}
```

```sh
npm install @anthropic-ai/claude-agent-sdk@0.3.306 @deedglass/sdk@0.6.0
export DEEDGLASS_API_KEY="$(cat .deedglass/quickstart.key)" DEEDGLASS_SERVER=http://127.0.0.1:8080
node native.ts
```

Use the port `init` printed if it isn't 8080.

## Check it without a model key

`examples/frameworks/claude-agent-sdk/` has the file, its pinned packages and a `check` file that needs no model
key: it starts `deedglass mcp serve` through the same framework client's imports and builds the native adapter's hooks and MCP entry against the stack (the Agent SDK can't list tools without a model). From the folder where
`init` ran:

```sh
path/to/deedglass/examples/frameworks/run.sh claude-agent-sdk            # install the pins in a temp folder, run the check
path/to/deedglass/examples/frameworks/run.sh claude-agent-sdk --agent    # also run the agent file when its model key is set
```

The workflow `frameworks.yml` runs every check against a fresh `init` stack each week and on pull
requests that touch these files. It isn't a required check: a framework release that doesn't install
is reported as skipped, not as a failure.

## Sources and what isn't verified

Every framework API in the files above comes from the framework's own docs, checked 2026-10-21:

- `query()` with `mcpServers` / `mcp_servers` (stdio `command` and `args`), `allowedTools` with `mcp__deedglass__*`, the `result` message: https://code.claude.com/docs/en/agent-sdk/mcp
- `PreToolUse` and `PostToolUse` hooks, `permissionDecision`: https://code.claude.com/docs/en/agent-sdk/hooks
- Tool-name rules: the MCP spec's tool names (https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-names) and Anthropic's tool definitions (https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools). OpenAI's API reference returned 403 when we checked; its rule here is the one `@deedglass/sdk`'s OpenAI adapter already follows.

Not verified:

- A run with a live model: our checks build the options and the adapter, but no model ran (no model key in CI).
