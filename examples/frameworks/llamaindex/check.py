# CI check, no model key: McpToolSpec starts `deedglass mcp serve`, the tools are renamed as in
# agent.py, and postgres_query runs once through Deedglass with the tool's own acall().
import asyncio
from llama_index.tools.mcp import BasicMCPClient, McpToolSpec

async def main():
    client = BasicMCPClient("npx", args=["-y", "deedglass", "mcp", "serve"])
    tools = await McpToolSpec(client=client).to_tool_list_async()
    for t in tools:
        t.metadata.name = t.metadata.name.replace(".", "_")
    query = next(t for t in tools if t.metadata.name == "postgres_query")
    out = str(await query.acall(sql="select count(*) as n from public.orders"))
    print(out[:200])
    assert "row_count" in out, out

asyncio.run(main())
