# Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY (llama-index-llms-openai).
import asyncio
from llama_index.core.agent.workflow import FunctionAgent
from llama_index.llms.openai import OpenAI
from llama_index.tools.mcp import BasicMCPClient, McpToolSpec

async def main():
    client = BasicMCPClient("npx", args=["-y", "deedglass", "mcp", "serve"])
    tools = await McpToolSpec(client=client).to_tool_list_async()
    for t in tools:  # postgres.query -> postgres_query: OpenAI function names can't hold a dot
        t.metadata.name = t.metadata.name.replace(".", "_")
    agent = FunctionAgent(name="orders", description="Answers questions about orders.", llm=OpenAI(model="gpt-4o"), tools=tools)
    print(await agent.run("How many rows are in public.orders?"))

asyncio.run(main())
