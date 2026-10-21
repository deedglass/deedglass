# LlamaIndex and Deedglass

Give a LlamaIndex agent Deedglass's tools in one file. The file starts Deedglass's MCP server (`npx -y deedglass mcp serve`) and hands its tools to the agent: each call goes through your policy, the risky ones wait for a person, and each one is sealed as a deed you can check later.

Tested with `llama-index-core` 0.14.25, `llama-index-tools-mcp` 0.6.0 and `llama-index-llms-openai` 0.8.3 (2026-10-21).

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

```python title="examples/frameworks/llamaindex/agent.py"
# Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY (llama-index-llms-openai).
import asyncio
from llama_index.core.agent.workflow import FunctionAgent
from llama_index.llms.openai import OpenAI
from llama_index.tools.mcp import BasicMCPClient, McpToolSpec

async def main():
    client = BasicMCPClient("npx", args=["-y", "deedglass", "mcp", "serve"])
    tools = await McpToolSpec(client=client).to_tool_list_async()
    for t in tools:  # postgres.query -> postgres_query: OpenAI function names can't hold a dot
        t.metadata.name = t.metadata.name.replace(".", "_")
    agent = FunctionAgent(name="orders", description="Answers questions about orders.", llm=OpenAI(model="gpt-4o"), tools=tools)
    print(await agent.run("How many rows are in public.orders?"))

asyncio.run(main())
```

Install and run:

```sh
pip install llama-index-core==0.14.25 llama-index-tools-mcp==0.6.0 llama-index-llms-openai==0.8.3
python agent.py
```

**Model key:** `OPENAI_API_KEY` (the model string is the one in the package's README).

**Tool names:** Deedglass's tool names have a dot (`postgres.query`). The MCP spec allows dots in tool names, but model APIs often don't: Anthropic's takes `^[a-zA-Z0-9_-]{1,128}$`, and OpenAI's function names take the same characters. So the file renames each tool's `metadata.name` (`postgres_query`). The tool still calls `postgres.query` on the server; the check runs a renamed tool with `acall()` to show it.

**What you see:** the answer (500 rows), and in Console › Ledger or `npx deedglass actions list` one sealed `postgres.query` action per call, with the rule that allowed it. If the agent tries a write, the tool answers that it is waiting for approval and nothing has run; once someone approves, `deedglass.check_approval` lets the agent carry on. A refused call answers with the policy's sentence and a record id.

**First start:** `npx -y deedglass` can take several seconds the first time. If a framework's MCP connect timeout is shorter, run `npx -y deedglass version` once first, or install the CLI ([INSTALL.md](../INSTALL.md)) and use `deedglass` as the command.

## Check it without a model key

`examples/frameworks/llamaindex/` has the file, its pinned packages and a `check` file that needs no model
key: it starts `deedglass mcp serve` through the same framework client, renames the tools as the file does, and runs `postgres_query` once with `acall()`. From the folder where
`init` ran:

```sh
path/to/deedglass/examples/frameworks/run.sh llamaindex            # install the pins in a temp folder, run the check
path/to/deedglass/examples/frameworks/run.sh llamaindex --agent    # also run the agent file when its model key is set
```

The workflow `frameworks.yml` runs every check against a fresh `init` stack each week and on pull
requests that touch these files. It isn't a required check: a framework release that doesn't install
is reported as skipped, not as a failure.

## Sources and what isn't verified

Every framework API in the files above comes from the framework's own docs, checked 2026-10-21:

- `BasicMCPClient("npx", args=[…])` for stdio, `list_tools()`, `call_tool()`: https://developers.llamaindex.ai/python/examples/tools/mcp/
- `McpToolSpec(client=…).to_tool_list_async()`, `FunctionAgent(name, description, llm, tools)`, `await agent.run(…)`: https://pypi.org/project/llama-index-tools-mcp/ (the package's README)
- Tool-name rules: the MCP spec's tool names (https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-names) and Anthropic's tool definitions (https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools). OpenAI's API reference returned 403 when we checked; its rule here is the one `@deedglass/sdk`'s OpenAI adapter already follows.

Not verified:

- Renaming `metadata.name`: not documented; the tool's call keeps the server's name in 0.6.0, which the check runs.
- A run with a live model (no model key in CI). LlamaIndex has no tool-call hook for checks of your own; human-in-the-loop goes through workflow events.
