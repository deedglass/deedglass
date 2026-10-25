# CI check, no model key: the toolset from agent.py, driven by Pydantic AI's TestModel, which calls
# postgres_query once; the call goes through Deedglass to the demo database.
from fastmcp.client.transports import StdioTransport
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPToolset
from pydantic_ai.models.test import TestModel

NAMES = ["postgres.query", "postgres.describe", "postgres.execute", "postgres.call", "deedglass.check_approval"]
toolset = MCPToolset(StdioTransport(command="npx", args=["-y", "deedglass", "mcp", "serve"]))
toolset = toolset.renamed({name.replace(".", "_"): name for name in NAMES})
out = str(Agent(TestModel(call_tools=["postgres_describe"]), toolsets=[toolset]).run_sync("describe").output)
print(out[:200])
assert "orders" in out, out
