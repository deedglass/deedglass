# Run where you ran `npx deedglass init`. Needs ANTHROPIC_API_KEY (langchain-anthropic).
import asyncio
from langchain.agents import create_agent
from langchain.mcp import MCPAdapter

CONFIG = {"mcpServers": {"deedglass": {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}}}

async def main():
    async with MCPAdapter(CONFIG) as adapter:
        tools = await adapter.list_tools()
        for t in tools: t.name = t.name.replace(".", "_")  # postgres_query: model APIs reject dots
        agent = create_agent("claude-sonnet-5", tools)
        print((await agent.ainvoke({"messages": [{"role": "user", "content": "How many rows are in public.orders?"}]}))["messages"][-1].content)

asyncio.run(main())
