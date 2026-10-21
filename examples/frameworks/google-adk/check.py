# CI check (no model key): ADK's McpToolset starts `deedglass mcp serve` and lists its tools
# (BaseToolset.get_tools, https://adk.dev/tools-custom/).
import asyncio

from google.adk.tools.mcp_tool import McpToolset
from google.adk.tools.mcp_tool.mcp_session_manager import StdioConnectionParams
from mcp import StdioServerParameters


async def main():
    params = StdioServerParameters(command="npx", args=["-y", "deedglass", "mcp", "serve"])
    toolset = McpToolset(connection_params=StdioConnectionParams(server_params=params, timeout=30))
    try:
        names = [t.name for t in await toolset.get_tools()]
    finally:
        await toolset.close()
    print(names)
    assert "postgres.query" in names, names


asyncio.run(main())
