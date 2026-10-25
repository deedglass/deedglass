# CI check, no model key: the toolset from agent.py, driven by Pydantic AI's TestModel, which calls
# postgres_describe once; the call goes through Deedglass to the demo database.
from fastmcp.client.transports import StdioTransport
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPToolset
from pydantic_ai.models.test import TestModel

toolset = MCPToolset(StdioTransport(command="npx", args=["-y", "deedglass", "mcp", "serve"]))
# postgres.describe: the name deedglass 0.7 and earlier lists (TestModel skips a name it doesn't see).
out = str(Agent(TestModel(call_tools=["postgres_describe", "postgres.describe"]), toolsets=[toolset]).run_sync("describe").output)
print(out[:200])
assert "orders" in out, out
