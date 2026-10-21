# Python examples

Each runs against a Deedglass server with the Acme orders database: `make demo-up` (the
`acme-demo` policy lets order-status updates run) or `deedglass init` (under `postgres-safe` the
UPDATE waits for a person). Every example has a `--scripted` mode that needs no model and no LLM
key, and prints one `✕` line (the blocked DROP TABLE) and one `✓ … L2 · VERIFIED` line.

```sh
pip install "deedglass[crypto,langchain,claude-agent-sdk] @ git+https://github.com/deedglass/deedglass#subdirectory=sdk/python"   # not on PyPI yet
export DEEDGLASS_API_KEY=<an agent key> DEEDGLASS_SERVER=http://127.0.0.1:8080
python examples/python/quickstart.py
python examples/python/langchain_orders.py --scripted
python examples/python/claude_agent_sdk_orders.py --scripted
```

`make py-examples` runs all three against `DEEDGLASS_SERVER`.

| Example | Framework | What it shows |
|---|---|---|
| `quickstart.py` | none | `act`, a typed `PolicyDeniedError`, a sealed action |
| `langchain_orders.py` | LangChain 1.x / LangGraph | `deedglass_tool`: a gateway tool in `create_agent` |
| `claude_agent_sdk_orders.py` | Claude Agent SDK | `deedglass_hooks` and the `deedglass` MCP server |
| `openai_agents_orders.py` | OpenAI Agents SDK | next release |
| `crewai_orders.py` | CrewAI | next release |

What they don't do: they don't evaluate policies locally, and the scripted runs don't call a model.
