# CI check, no model key: the toolset from agent.py, driven by Pydantic AI's TestModel, which calls
# postgres_describe once; the call goes through Deedglass to the demo database.
import asyncio

from fastmcp import Client
from fastmcp.client.transports import StdioTransport
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPToolset
from pydantic_ai.models.test import TestModel

def serve():
    return StdioTransport(command="npx", args=["-y", "deedglass", "mcp", "serve"])

toolset = MCPToolset(serve())

async def listed():
    async with Client(serve()) as c:
        return [t.name for t in await c.list_tools()]

# postgres.describe: the name deedglass 0.7 and earlier lists.
describe = "postgres_describe" if "postgres_describe" in asyncio.run(listed()) else "postgres.describe"
out = str(Agent(TestModel(call_tools=[describe]), toolsets=[toolset]).run_sync("describe").output)
print(out[:200])
assert "orders" in out, out
