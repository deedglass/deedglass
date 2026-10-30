# LangChain and LangGraph and Deedglass

Give a LangChain agent Deedglass's tools in one file. The file starts Deedglass's MCP server (`npx -y deedglass mcp serve`) and hands its tools to the agent: each call goes through your policy, the risky ones wait for a person, and each one is sealed as a deed you can check later.

Tested with `langchain` 1.4.6 with the `mcp` extra and `langchain-anthropic` 1.7.6 (2026-10-21).

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

```python title="examples/frameworks/langchain/agent.py"
# Run where you ran `npx deedglass init`. Needs ANTHROPIC_API_KEY (langchain-anthropic).
import asyncio
from langchain.agents import create_agent
from langchain.mcp import MCPAdapter

CONFIG = {"mcpServers": {"deedglass": {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}}}

async def main():
    async with MCPAdapter(CONFIG) as adapter:
        tools = await adapter.list_tools()
        agent = create_agent("claude-sonnet-5", tools)
        print((await agent.ainvoke({"messages": [{"role": "user", "content": "How many rows are in public.orders?"}]}))["messages"][-1].content)

asyncio.run(main())
```

Install and run:

```sh
pip install "langchain[mcp]==1.4.6" langchain-anthropic==1.7.6
python agent.py
```

**Model key:** `ANTHROPIC_API_KEY` (the model string is the one in LangChain's MCP page). `create_agent` takes any chat model LangChain supports.

**Tool names:** Deedglass lists its tools with names every model API takes: `postgres_query`, `postgres_execute`, `deedglass_check_approval` (letters, digits, `_` and `-`; Anthropic's rule is `^[a-zA-Z0-9_-]{1,128}$` and OpenAI's allows the same characters up to 64). Your policies and the sealed records keep the dotted name, `postgres.query`; Deedglass maps one to the other ([MCP.md](../MCP.md#tool-names)). Deedglass 0.7 and earlier listed the dotted names, which these APIs refuse. `langchain.mcp` is marked beta in 1.4.6 (it warns on import).

**What you see:** the answer (500 rows), and in Console › Ledger or `npx deedglass actions list` one sealed `postgres.query` action per call, with the rule that allowed it. If the agent tries a write, the tool answers that it is waiting for approval and nothing has run; once someone approves, `deedglass_check_approval` lets the agent carry on. A refused call answers with the policy's sentence and a record id.

**First start:** `npx -y deedglass` can take several seconds the first time. If a framework's MCP connect timeout is shorter, run `npx -y deedglass version` once first, or install the CLI ([INSTALL.md](../INSTALL.md)) and use `deedglass` as the command.

## The native adapter

The Python SDK has a LangChain adapter, `DeedglassMiddleware`, built on LangChain's `wrap_tool_call` middleware: it asks Deedglass before each of your own tools runs and reports the result after ([SDK_PYTHON.md](../SDK_PYTHON.md)). For LangGraph's `ToolNode`, `govern_tool` wraps tools directly with pre-execution policy checks and clean error reporting. It isn't on PyPI yet. For LangChain.js, `@deedglass/sdk/langchain` has `governTool` and `deedglassTools` ([its page](../sdk/adapters/langchain.md)).

## Check it without a model key

`examples/frameworks/langchain/` has the file, its pinned packages and a `check` file that needs no model
key: it starts `deedglass mcp serve` through the same framework client, and runs `postgres_query` once with `ainvoke()`. From the folder where
`init` ran:

```sh
path/to/deedglass/examples/frameworks/run.sh langchain            # install the pins in a temp folder, run the check
path/to/deedglass/examples/frameworks/run.sh langchain --agent    # also run the agent file when its model key is set
```

The workflow `frameworks.yml` runs every check against a fresh `init` stack each week and on pull
requests that touch these files. It isn't a required check: a framework release that doesn't install
is reported as skipped, not as a failure.

## Sources and what isn't verified

Every framework API in the files above comes from the framework's own docs, checked 2026-10-21:

- `from langchain.mcp import MCPAdapter`, `async with MCPAdapter(config)`, `await adapter.list_tools()`, `create_agent(model, tools)`, `agent.ainvoke({"messages": …})`: https://docs.langchain.com/oss/python/langchain/mcp
- The `{"mcpServers": {name: {"command", "args"}}}` config for a stdio server: https://docs.langchain.com/oss/python/langchain/mcp/connections
- `wrap_tool_call` middleware (what the Python adapter builds on): https://docs.langchain.com/oss/python/langchain/middleware/custom
- Tool-name rules: the MCP spec's tool names (https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-names) and Anthropic's tool definitions (https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools). OpenAI's API reference returned 403 when we checked; its rule here is the one `@deedglass/sdk`'s OpenAI adapter already follows.

Not verified:

- Renaming a tool by setting `.name`: LangChain documents `name` as a tool field, but not that the MCP adapter keeps calling the server's name; we observed it in 1.4.6.
- A run with a live model (no model key in CI).
