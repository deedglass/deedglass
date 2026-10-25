# Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
from fastmcp.client.transports import StdioTransport
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPToolset

toolset = MCPToolset(StdioTransport(command="npx", args=["-y", "deedglass", "mcp", "serve"]))
agent = Agent("openai:gpt-6.1-sol", toolsets=[toolset])
print(agent.run_sync("How many rows are in public.orders?").output)
