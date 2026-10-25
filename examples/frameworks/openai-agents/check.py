# CI check, no model key: MCPServerStdio starts `deedglass mcp serve` and lists its tools, and an Agent
# takes the server.
import asyncio
from agents import Agent
from agents.mcp import MCPServerStdio

async def main():
    params = {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}
    async with MCPServerStdio(name="deedglass", params=params) as server:
        names = [t.name for t in await server.list_tools()]
        Agent(name="orders", mcp_servers=[server])
    print(names)
    assert "postgres_query" in names or "postgres.query" in names, names  # postgres.query: the name deedglass 0.7 and earlier lists

asyncio.run(main())
