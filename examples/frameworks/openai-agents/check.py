# CI check, no model key: MCPServerStdio starts `deedglass mcp serve` and lists its tools, and an Agent
# takes the server with include_server_in_tool_names.
import asyncio
from agents import Agent
from agents.mcp import MCPServerStdio

async def main():
    params = {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}
    async with MCPServerStdio(name="deedglass", params=params) as server:
        names = [t.name for t in await server.list_tools()]
        Agent(name="orders", mcp_servers=[server], mcp_config={"include_server_in_tool_names": True})
    print(names)
    assert "postgres.query" in names, names

asyncio.run(main())
