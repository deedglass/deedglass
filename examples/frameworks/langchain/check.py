# CI check, no model key: LangChain's MCP adapter starts `deedglass mcp serve`, the tools are renamed
# as in agent.py, and postgres_query runs once through Deedglass with the tool's own ainvoke().
import asyncio
from langchain.mcp import MCPAdapter

CONFIG = {"mcpServers": {"deedglass": {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}}}

async def main():
    async with MCPAdapter(CONFIG) as adapter:
        tools = await adapter.list_tools()
        for t in tools:
            t.name = t.name.replace(".", "_")
        query = next(t for t in tools if t.name == "postgres_query")
        out = str(await query.ainvoke({"sql": "select count(*) as n from public.orders"}))
    print(out[:200])
    assert "row_count" in out, out

asyncio.run(main())
