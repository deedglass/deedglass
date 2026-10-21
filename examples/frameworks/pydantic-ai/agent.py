# Run where you ran `npx deedglass init`. Needs OPENAI_API_KEY.
from fastmcp.client.transports import StdioTransport
from pydantic_ai import Agent
from pydantic_ai.mcp import MCPToolset

NAMES = ["postgres.query", "postgres.describe", "postgres.execute", "postgres.call", "deedglass.check_approval"]
toolset = MCPToolset(StdioTransport(command="npx", args=["-y", "deedglass", "mcp", "serve"]))
# renamed() maps new name -> original: postgres_query -> postgres.query (model APIs reject dots).
toolset = toolset.renamed({name.replace(".", "_"): name for name in NAMES})
agent = Agent("openai:gpt-6.1-sol", toolsets=[toolset])
print(agent.run_sync("How many rows are in public.orders?").output)
