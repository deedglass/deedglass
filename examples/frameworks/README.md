# Framework recipes

One folder per agent framework, each with a runnable file of 15 lines or fewer that gives the agent
Deedglass's tools through `npx -y deedglass mcp serve`, plus the native `@deedglass/sdk` adapter
where one exists. The page for each is in `docs/integrations/` (docs.deedglass.com/frameworks/).

| Folder | Files | Model key the agent file needs |
|---|---|---|
| `vercel-ai-sdk` | `agent.ts`, `native.ts` | `AI_GATEWAY_API_KEY` |
| `claude-agent-sdk` | `agent.ts`, `agent.py`, `native.ts` | `ANTHROPIC_API_KEY` |
| `openai-agents` | `agent.py`, `agent.ts`, `native.ts` | `OPENAI_API_KEY` |
| `langchain` | `agent.py` | `ANTHROPIC_API_KEY` |
| `mastra` | `agent.ts` | `OPENAI_API_KEY` |
| `pydantic-ai` | `agent.py` | `OPENAI_API_KEY` |
| `google-adk` | `agent.py` (run with `adk run`) | `GOOGLE_API_KEY` |
| `crewai` | `agent.py` | `OPENAI_API_KEY` |
| `llamaindex` | `agent.py` | `OPENAI_API_KEY` |

Each folder also has `check.ts` / `check.py`, which needs no model key: it starts `deedglass mcp
serve` through the framework's own MCP client, lists the tools, and where the framework can run a
tool without a model, sends one `postgres.query` (or `postgres.describe`) through Deedglass.

```sh
mkdir try && cd try && npx deedglass init      # once
path/to/examples/frameworks/run.sh langchain   # pinned install in a temp folder, then check.py
path/to/examples/frameworks/run.sh langchain --agent   # also agent.py, when ANTHROPIC_API_KEY is set
```

`run.sh` reads the key and the server from `.deedglass/` in the current folder (`STACK_DIR` to point
elsewhere). An install that fails is reported as skipped and exits 0 (`STRICT=1` fails instead).
`.github/workflows/frameworks.yml` runs every folder that way each week and on pull requests that
touch these files; it isn't one of the required checks.

The versions in `package.json` and `requirements.txt` are what the files were tested with on
2026-10-21. They are example pins, not dependencies of Deedglass. Python 3.13 is used when present
(CrewAI needs Python below 3.14). `docs/integrations_test.go` keeps each page's code equal to these
files and each agent file at 15 lines or fewer.
