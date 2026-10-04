"""The Claude Agent SDK with Deedglass: hooks decide every built-in tool, and an in-process MCP
server runs the database tools through Deedglass (gateway mode).

    pip install "deedglass[claude-agent-sdk]"
    python examples/python/claude_agent_sdk_orders.py --scripted   # no Claude login needed

--scripted calls the same code paths the SDK calls: the PreToolUse hook for a Bash rm -rf (denied:
no rule allows it), then the MCP server's postgres__execute tool for DROP TABLE (blocked) and for
the UPDATE of order 42 (✓ L2 · VERIFIED). Without it, it runs a real Claude Agent SDK query, which
needs the Claude Code CLI and your own login.
"""

from __future__ import annotations

import argparse
import asyncio

from _show import show

from deedglass import AsyncDeedglass
from deedglass.claude_agent_sdk import call_gateway_tool, deedglass_hooks, deedglass_mcp_server


async def scripted(dg: AsyncDeedglass) -> None:
    hooks = deedglass_hooks(dg)
    pre = hooks["PreToolUse"][0].hooks[0]
    out = await pre({"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": {"command": "rm -rf /"}, "tool_use_id": "toolu_1"}, "toolu_1", None)
    reason = out.get("hookSpecificOutput", {}).get("permissionDecisionReason", "")
    print(("✕ claude.Bash · denied · " + reason) if reason else "○ claude.Bash · allowed (check the policy)")
    for sql in ("DROP TABLE public.orders", "UPDATE public.orders SET status = 'shipped' WHERE id = 42"):
        r = await call_gateway_tool(dg, "postgres.execute", {"sql": sql}, wait_for_approval=0)
        show(r.get("action"), r["content"][0]["text"])


async def live(dg: AsyncDeedglass) -> None:
    from claude_agent_sdk import ClaudeAgentOptions, query

    tools = [t for t in await dg.tools.list() if t.connector == "postgres"]
    options = ClaudeAgentOptions(hooks=deedglass_hooks(dg), mcp_servers={"deedglass": deedglass_mcp_server(dg, tools)})
    async for message in query(prompt="Ship order 42 in the orders database.", options=options):
        print(message)


async def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("--scripted", action="store_true")
    args = p.parse_args()
    async with AsyncDeedglass() as dg:
        await (scripted(dg) if args.scripted else live(dg))


if __name__ == "__main__":
    asyncio.run(main())
