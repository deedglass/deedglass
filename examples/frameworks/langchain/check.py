# CI check, no model key: LangChain's MCP adapter starts `deedglass mcp serve`, and postgres_query runs
# once through Deedglass with the tool's own ainvoke().
import asyncio
from langchain.mcp import MCPAdapter

CONFIG = {"mcpServers": {"deedglass": {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}}}

async def main():
    async with MCPAdapter(CONFIG) as adapter:
        tools = await adapter.list_tools()
        query = next(t for t in tools if t.name in ("postgres_query", "postgres.query"))  # postgres.query: the name deedglass 0.7 and earlier lists
        out = str(await query.ainvoke({"sql": "select count(*) as n from public.orders"}))
    print(out[:200])
    assert "row_count" in out, out

asyncio.run(main())
