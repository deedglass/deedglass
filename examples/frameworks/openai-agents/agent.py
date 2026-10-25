# Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
import asyncio
from agents import Agent, Runner
from agents.mcp import MCPServerStdio

async def main():
    params = {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}
    async with MCPServerStdio(name="deedglass", params=params) as server:
        agent = Agent(name="orders", instructions="Answer questions about orders.", mcp_servers=[server])
        result = await Runner.run(agent, "How many rows are in public.orders?")
        print(result.final_output)

asyncio.run(main())
