# Run where you ran `npx deedglass init`. Needs ANTHROPIC_API_KEY.
import asyncio
from claude_agent_sdk import ClaudeAgentOptions, ResultMessage, query

options = ClaudeAgentOptions(
    mcp_servers={"deedglass": {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}},
    allowed_tools=["mcp__deedglass__*"],  # Claude Code names them mcp__deedglass__postgres_query and so on
)

async def main():
    async for message in query(prompt="How many rows are in public.orders?", options=options):
        if isinstance(message, ResultMessage) and message.subtype == "success":
            print(message.result)

asyncio.run(main())
