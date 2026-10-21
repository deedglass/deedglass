# CI check, no model key: CrewAI's MCPServerAdapter starts `deedglass mcp serve` and lists its tools,
# and an Agent takes them.
from crewai import Agent
from crewai_tools import MCPServerAdapter
from mcp import StdioServerParameters

params = StdioServerParameters(command="npx", args=["-y", "deedglass", "mcp", "serve"])
with MCPServerAdapter(params, connect_timeout=60) as mcp_tools:
    names = [t.name for t in mcp_tools]
    Agent(role="Orders analyst", goal="Answer questions about orders.", backstory="Reads orders.", tools=mcp_tools)
print(names)
assert "postgres_query" in names, names
