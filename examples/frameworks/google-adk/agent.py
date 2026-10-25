# From the folder where you ran `npx deedglass init`: adk run path/to/google-adk. Needs GOOGLE_API_KEY.
from google.adk.agents import LlmAgent
from google.adk.tools.mcp_tool import McpToolset
from google.adk.tools.mcp_tool.mcp_session_manager import StdioConnectionParams
from mcp import StdioServerParameters

deedglass = McpToolset(
    connection_params=StdioConnectionParams(
        server_params=StdioServerParameters(command="npx", args=["-y", "deedglass", "mcp", "serve"]), timeout=30
    ),
    tool_filter=["postgres.describe", "postgres.query"],  # reads only; add postgres.execute for writes
)
root_agent = LlmAgent(model="gemini-flash-latest", name="orders", instruction="Answer questions about orders.",
                      tools=[deedglass])
