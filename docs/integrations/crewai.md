# CrewAI and Deedglass

Give a CrewAI agent Deedglass's tools in one file. The file starts Deedglass's MCP server (`npx -y deedglass mcp serve`) and hands its tools to the agent: each call goes through your policy, the risky ones wait for a person, and each one is sealed as a deed you can check later.

Tested with `crewai` and `crewai-tools[mcp]` 1.15.28 (2026-10-21; CrewAI needs Python below 3.14).

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

```python title="examples/frameworks/crewai/agent.py"
# Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY (CrewAI's default model).
from crewai import Agent, Crew, Task
from crewai_tools import MCPServerAdapter
from mcp import StdioServerParameters

params = StdioServerParameters(command="npx", args=["-y", "deedglass", "mcp", "serve"])
with MCPServerAdapter(params, connect_timeout=60) as mcp_tools:  # CrewAI names them postgres_query, ...
    analyst = Agent(role="Orders analyst", goal="Answer questions about orders from the database.",
                    backstory="You read the orders database through Deedglass.", tools=mcp_tools)
    task = Task(description="How many rows are in public.orders?", expected_output="One number.", agent=analyst)
    print(Crew(agents=[analyst], tasks=[task]).kickoff())
```

Install and run:

```sh
pip install crewai==1.15.28 "crewai-tools[mcp]==1.15.28"
python agent.py
```

**Model key:** `OPENAI_API_KEY`, for CrewAI's default model.

**Tool names:** CrewAI's MCP adapter lists the tools as `postgres_query` and so on, as Deedglass names them. Policies and records keep the dotted name, `postgres.query` ([MCP.md](../MCP.md#tool-names)).

**What you see:** the answer (500 rows), and in Console › Ledger or `npx deedglass actions list` one sealed `postgres.query` action per call, with the rule that allowed it. If the agent tries a write, the tool answers that it is waiting for approval and nothing has run; once someone approves, `deedglass_check_approval` lets the agent carry on. A refused call answers with the policy's sentence and a record id.

**First start:** `npx -y deedglass` can take several seconds the first time. If a framework's MCP connect timeout is shorter, run `npx -y deedglass version` once first, or install the CLI ([INSTALL.md](../INSTALL.md)) and use `deedglass` as the command.

## Check it without a model key

`examples/frameworks/crewai/` has the file, its pinned packages and a `check` file that needs no model
key: it starts `deedglass mcp serve` through the same framework client, lists its tools and gives them to an Agent. From the folder where
`init` ran:

```sh
path/to/deedglass/examples/frameworks/run.sh crewai            # install the pins in a temp folder, run the check
path/to/deedglass/examples/frameworks/run.sh crewai --agent    # also run the agent file when its model key is set
```

The workflow `frameworks.yml` runs every check against a fresh `init` stack each week and on pull
requests that touch these files. It isn't a required check: a framework release that doesn't install
is reported as skipped, not as a failure.

## Sources and what isn't verified

Every framework API in the files above comes from the framework's own docs, checked 2026-10-21:

- `MCPServerAdapter(StdioServerParameters(command, args), connect_timeout=60)` as a context manager, `tools=mcp_tools`: https://docs.crewai.com/en/mcp/overview
- `PRE_TOOL_CALL` and `POST_TOOL_CALL` hooks, for checks of your own: https://docs.crewai.com/en/learn/tool-hooks
- Tool-name rules: the MCP spec's tool names (https://modelcontextprotocol.io/specification/2025-11-25/server/tools#tool-names) and Anthropic's tool definitions (https://platform.claude.com/docs/en/agents-and-tools/tool-use/define-tools). OpenAI's API reference returned 403 when we checked; its rule here is the one `@deedglass/sdk`'s OpenAI adapter already follows.

Not verified:

- `Task(description, expected_output, agent)`, `Crew(agents, tasks).kickoff()`: CrewAI's MCP page stops at the Agent, so these come from CrewAI's core classes as installed.
- A run with a live model (no model key in CI).
