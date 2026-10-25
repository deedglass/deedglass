# Pydantic AI and Deedglass

Give a Pydantic AI agent Deedglass's tools in one file. The file starts Deedglass's MCP server (`npx -y deedglass mcp serve`) and hands its tools to the agent: each call goes through your policy, the risky ones wait for a person, and each one is sealed as a deed you can check later.

Tested with `pydantic-ai-slim` 2.59.0 with the `mcp` and `openai` extras, and `fastmcp` 4.1.3 (2026-10-21).

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

```python title="examples/frameworks/pydantic-ai/agent.py"
# Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
from fastmcp.client.transports import StdioTransport
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPToolset

toolset = MCPToolset(StdioTransport(command="npx", args=["-y", "deedglass", "mcp", "serve"]))
agent = Agent("openai:gpt-6.1-sol", toolsets=[toolset])
print(agent.run_sync("How many rows are in public.orders?").output)
```

Install and run:

```sh
pip install "pydantic-ai-slim[mcp,openai]==2.59.0" fastmcp==4.1.3
python agent.py
```

**Model key:** `OPENAI_API_KEY` (the model string is the one in Pydantic AI's MCP client page). Any model Pydantic AI supports works.

**Tool names:** Deedglass lists its tools with names every model API takes: `postgres_query`, `postgres_execute`, `deedglass_check_approval` (letters, digits, `_` and `-`; Anthropic's rule is `^[a-zA-Z0-9_-]{1,128}$` and OpenAI's allows the same characters up to 64). Your policies and the sealed records keep the dotted name, `postgres.query`; Deedglass maps one to the other ([MCP.md](../MCP.md#tool-names)). Deedglass 0.7 and earlier listed the dotted names, which these APIs refuse.

**What you see:** the answer (500 rows), and in Console › Ledger or `npx deedglass actions list` one sealed `postgres.query` action per call, with the rule that allowed it. If the agent tries a write, the tool answers that it is waiting for approval and nothing has run; once someone approves, `deedglass_check_approval` lets the agent carry on. A refused call answers with the policy's sentence and a record id.

**First start:** `npx -y deedglass` can take several seconds the first time. If a framework's MCP connect timeout is shorter, run `npx -y deedglass version` once first, or install the CLI ([INSTALL.md](../INSTALL.md)) and use `deedglass` as the command.

## Check it without a model key

`examples/frameworks/pydantic-ai/` has the file, its pinned packages and a `check` file that needs no model
key: it starts `deedglass mcp serve` through the same framework client and drives the agent with Pydantic AI's `TestModel`, which calls `postgres_describe` for real, through Deedglass. From the folder where
`init` ran:

```sh
path/to/deedglass/examples/frameworks/run.sh pydantic-ai            # install the pins in a temp folder, run the check
path/to/deedglass/examples/frameworks/run.sh pydantic-ai --agent    # also run the agent file when its model key is set
```

The workflow `frameworks.yml` runs every check against a fresh `init` stack each week and on pull
requests that touch these files. It isn't a required check: a framework release that doesn't install
is reported as skipped, not as a failure.

## Sources and what isn't verified

Every framework API in the files above comes from the framework's own docs, checked 2026-10-21:

- `MCPToolset(StdioTransport(command=…, args=…))`, `Agent(model, toolsets=[toolset])`: https://pydantic.dev/docs/ai/mcp/client/
- `WrapperToolset.call_tool` for checks of your own: https://pydantic.dev/docs/ai/tools-toolsets/toolsets/
- Tool-name rules: the MCP spec's tool names (https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-names) and Anthropic's tool definitions (https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools). OpenAI's API reference returned 403 when we checked; its rule here is the one `@deedglass/sdk`'s OpenAI adapter already follows.

Not verified:

- A run with a live model; the check uses `TestModel` instead (no model key in CI).
