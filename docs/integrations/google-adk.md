# Google ADK and Deedglass

Give a Google ADK agent Deedglass's tools in one file. The file starts Deedglass's MCP server (`npx -y deedglass mcp serve`) and hands its tools to the agent: each call goes through your policy, the risky ones wait for a person, and each one is sealed as a deed you can check later.

Tested with `google-adk` 2.12.0 with the `mcp` extra (2026-10-21).

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

```python title="examples/frameworks/google-adk/agent.py"
# From the folder where you ran `npx deedglass init`: adk run path/to/google-adk. Needs GOOGLE_API_KEY.
from google.adk.agents import LlmAgent
from google.adk.tools.mcp_tool import McpToolset
from google.adk.tools.mcp_tool.mcp_session_manager import StdioConnectionParams
from mcp import StdioServerParameters

deedglass = McpToolset(
    connection_params=StdioConnectionParams(
        server_params=StdioServerParameters(command="npx", args=["-y", "deedglass", "mcp", "serve"]), timeout=30
    ),
    tool_filter=["postgres_describe", "postgres_query"],  # reads only; add postgres_execute for writes
)
root_agent = LlmAgent(model="gemini-flash-latest", name="orders", instruction="Answer questions about orders.",
                      tools=[deedglass])
```

Install and run:

```sh
pip install "google-adk[mcp]==2.12.0"
adk run path/to/google-adk       # the folder holding agent.py and __init__.py
```

**Model key:** `GOOGLE_API_KEY` (the model string is the one in ADK's MCP tools page).

**Tool names:** `tool_filter` keeps the agent to the two read tools; add `postgres_execute` when it should write (writes wait for a person under `postgres-safe`). Deedglass lists its tools with names every model API takes: `postgres_query`, `postgres_execute`, `deedglass_check_approval` (letters, digits, `_` and `-`; Anthropic's rule is `^[a-zA-Z0-9_-]{1,128}$` and OpenAI's allows the same characters up to 64). Your policies and the sealed records keep the dotted name, `postgres.query`; Deedglass maps one to the other ([MCP.md](../MCP.md#tool-names)). Deedglass 0.7 and earlier listed the dotted names, which these APIs refuse.

**What you see:** the answer (500 rows), and in Console › Ledger or `npx deedglass actions list` one sealed `postgres.query` action per call, with the rule that allowed it. If the agent tries a write, the tool answers that it is waiting for approval and nothing has run; once someone approves, `deedglass_check_approval` lets the agent carry on. A refused call answers with the policy's sentence and a record id.

**First start:** `npx -y deedglass` can take several seconds the first time. If a framework's MCP connect timeout is shorter, run `npx -y deedglass version` once first, or install the CLI ([INSTALL.md](../INSTALL.md)) and use `deedglass` as the command.

## Check it without a model key

`examples/frameworks/google-adk/` has the file, its pinned packages and a `check` file that needs no model
key: it starts `deedglass mcp serve` through the same framework client and lists its tools with `get_tools()`. From the folder where
`init` ran:

```sh
path/to/deedglass/examples/frameworks/run.sh google-adk            # install the pins in a temp folder, run the check
path/to/deedglass/examples/frameworks/run.sh google-adk --agent    # also run the agent file when its model key is set
```

The workflow `frameworks.yml` runs every check against a fresh `init` stack each week and on pull
requests that touch these files. It isn't a required check: a framework release that doesn't install
is reported as skipped, not as a failure.

## Sources and what isn't verified

Every framework API in the files above comes from the framework's own docs, checked 2026-10-21:

- `McpToolset(connection_params=StdioConnectionParams(server_params=StdioServerParameters(…)), tool_filter=[…])`, `LlmAgent(model, name, instruction, tools)`, `await toolset.close()`: https://adk.dev/tools-custom/mcp-tools/
- `BaseToolset.get_tools()` and `close()`: https://adk.dev/tools-custom/
- `before_tool_callback` and plugins, for checks of your own: https://adk.dev/callbacks/types-of-callbacks/
- Tool-name rules: the MCP spec's tool names (https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-names) and Anthropic's tool definitions (https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools). OpenAI's API reference returned 403 when we checked; its rule here is the one `@deedglass/sdk`'s OpenAI adapter already follows.

Not verified:

- The `mcp` extra: ADK's MCP page doesn't name it, but `McpToolset` doesn't import without it in 2.12.0 (the extra is declared in the package's metadata).
- A run with Gemini (no model key in CI).
