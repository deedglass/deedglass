# CI check, no model key: the recipe's imports resolve and its options build.
# A model run needs ANTHROPIC_API_KEY (run.sh --agent).
from claude_agent_sdk import ClaudeAgentOptions, ResultMessage, query

options = ClaudeAgentOptions(
    mcp_servers={"deedglass": {"command": "npx", "args": ["-y", "deedglass", "mcp", "serve"]}},
    allowed_tools=["mcp__deedglass__*"],
)
assert callable(query) and ResultMessage
print("options", options.mcp_servers, options.allowed_tools)
