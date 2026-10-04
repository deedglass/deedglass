"""The Claude Agent SDK: Deedglass decides before each tool use (S32 §3.8).

    from claude_agent_sdk import ClaudeAgentOptions
    from deedglass import AsyncDeedglass
    from deedglass.claude_agent_sdk import deedglass_hooks

    options = ClaudeAgentOptions(hooks=deedglass_hooks(AsyncDeedglass()))

PreToolUse asks Deedglass (proxy mode, tool ``claude.<ToolName>``): a refusal becomes
``permissionDecision: "deny"`` with the Gate's sentence as the reason Claude reads; an authorized
call returns no decision, so the host's own permission prompts still apply. PostToolUse and
PostToolUseFailure report the outcome so the deed is sealed.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from typing import Any, cast

from .._copy import adapter_missing

try:
    from claude_agent_sdk import HookMatcher, create_sdk_mcp_server, tool
except ImportError as e:  # pragma: no cover - exercised only without the extra
    raise ImportError(adapter_missing("claude_agent_sdk", "claude-agent-sdk", "the Claude Agent SDK")) from e

from .._adapter import Governor, Verdict
from .._client import AsyncDeedglass, Deedglass
from .._errors import DeedglassError
from .._types import Tool
from ..wrap import denial_text, pending_text

__all__ = ["call_gateway_tool", "deedglass_hooks", "deedglass_mcp_server"]


def _deny(reason: str) -> dict[str, Any]:
    return {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
    }


def deedglass_hooks(
    client: Deedglass | AsyncDeedglass,
    matcher: str | None = None,
    *,
    connector: str = "claude",
    name_map: Mapping[str, str] | None = None,
    wait_for_approval: float = 120.0,
    fail_open: bool = False,
) -> dict[str, list[Any]]:
    """Hooks for ``ClaudeAgentOptions(hooks=…)``: PreToolUse decides, PostToolUse reports."""
    gov = Governor(
        client,
        framework="claude-agent-sdk",
        connector=connector,
        name_map=name_map,
        wait_for_approval=wait_for_approval,
        fail_open=fail_open,
    )
    open_calls: dict[str, Verdict] = {}

    async def pre(input_data: Any, tool_use_id: str | None, context: Any) -> dict[str, Any]:
        if input_data.get("hook_event_name") != "PreToolUse":
            return {}
        tid = input_data.get("tool_use_id") or tool_use_id or ""
        args = input_data.get("tool_input")
        v = await gov.adecide(
            input_data.get("tool_name", ""),
            args if isinstance(args, dict) else {},
            tid or None,
            input_data.get("session_id"),
        )
        if v.kind != "run":
            return _deny(v.text)
        if tid and v.action is not None:
            open_calls[tid] = v
        return {}

    async def post(input_data: Any, tool_use_id: str | None, context: Any) -> dict[str, Any]:
        event = input_data.get("hook_event_name")
        tid = input_data.get("tool_use_id") or tool_use_id or ""
        v = open_calls.pop(tid, None)
        if v is None:
            return {}
        if event == "PostToolUse":
            await gov.areport(v, input_data.get("tool_response"))
        elif event == "PostToolUseFailure":
            await gov.areport(v, err=RuntimeError(str(input_data.get("error") or "The tool failed.")))
        return {}

    return {
        "PreToolUse": [
            HookMatcher(matcher=matcher, hooks=[cast(Any, pre)], timeout=max(60.0, wait_for_approval + 30.0))
        ],
        "PostToolUse": [HookMatcher(matcher=matcher, hooks=[cast(Any, post)])],
        "PostToolUseFailure": [HookMatcher(matcher=matcher, hooks=[cast(Any, post)])],
    }


async def call_gateway_tool(
    client: AsyncDeedglass, tool_name: str, args: dict[str, Any], *, wait_for_approval: float = 120.0
) -> dict[str, Any]:
    """Runs one connector tool through Deedglass and returns an MCP tool result (what the server's tools do)."""
    import json

    try:
        a = await client.act(tool_name, args, wait_for_approval=wait_for_approval)
    except DeedglassError as e:
        return {"content": [{"type": "text", "text": denial_text(e)}], "isError": True, "action": e.action}
    if a.status == "pending_approval":
        return {"content": [{"type": "text", "text": pending_text(a)}], "isError": True, "action": a}
    if a.status != "executed":
        return {"content": [{"type": "text", "text": denial_text(a)}], "isError": True, "action": a}
    r = a.result
    return {"content": [{"type": "text", "text": r if isinstance(r, str) else json.dumps(r)}], "action": a}


def deedglass_mcp_server(client: AsyncDeedglass, tools: Sequence[Tool], *, wait_for_approval: float = 120.0) -> Any:
    """An in-process MCP server ``deedglass`` whose tools Deedglass runs (gateway mode).

    Pass the tools from ``await client.tools.list()``.
    """

    def make(t: Tool) -> Any:
        @tool(t.name.replace(".", "__", 1), t.description or t.title or t.name, dict(t.input_schema))
        async def run(args: dict[str, Any]) -> dict[str, Any]:
            out = await call_gateway_tool(client, t.name, args, wait_for_approval=wait_for_approval)
            out.pop("action", None)
            return out

        return run

    return create_sdk_mcp_server("deedglass", "0.1.0", [make(t) for t in tools])
