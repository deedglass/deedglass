"""LangChain v1 and LangGraph: every tool call goes through Deedglass first (S32 §3.8).

    from langchain.agents import create_agent
    from deedglass import Deedglass
    from deedglass.langchain import DeedglassMiddleware

    agent = create_agent(model, tools=[ship_order], middleware=[DeedglassMiddleware(Deedglass())])

A denied, pending or unreachable call becomes a ``ToolMessage(status="error")`` the model reads; the
tool itself runs only once Deedglass authorizes it. ``deedglass_tool`` wraps a connector tool that
Deedglass runs itself (gateway mode), for LangGraph's ``ToolNode``.
"""

from __future__ import annotations

import asyncio
from collections.abc import Awaitable, Callable, Mapping
from typing import Any

from .._copy import adapter_missing

try:
    from langchain.agents.middleware import AgentMiddleware
    from langchain_core.messages import ToolMessage
    from langchain_core.tools import StructuredTool
except ImportError as e:  # pragma: no cover - exercised only without the extra
    raise ImportError(adapter_missing("langchain", "langchain", "LangChain")) from e

from .._adapter import Governor
from .._client import AsyncDeedglass, Deedglass
from .._errors import DeedglassError
from ..wrap import denial_text

__all__ = ["DeedglassMiddleware", "deedglass_tool", "govern_tool"]

GATEWAY = "deedglass_gateway"


def _content(value: Any) -> Any:
    msg = value if isinstance(value, ToolMessage) else None
    return msg.content if msg is not None else value


class DeedglassMiddleware(AgentMiddleware):  # type: ignore[misc,unused-ignore]
    """Asks Deedglass before each tool call (proxy mode) and reports the result after it."""

    def __init__(
        self,
        client: Deedglass | AsyncDeedglass,
        tools: list[str] | None = None,
        name_map: Mapping[str, str] | None = None,
        *,
        connector: str = "app",
        wait_for_approval: float = 120.0,
        fail_open: bool = False,
    ) -> None:
        super().__init__()
        self.only = set(tools) if tools is not None else None
        self.gov = Governor(
            client,
            framework="langchain",
            connector=connector,
            name_map=name_map,
            wait_for_approval=wait_for_approval,
            fail_open=fail_open,
        )

    def _skip(self, request: Any) -> bool:
        tool = getattr(request, "tool", None)
        meta = getattr(tool, "metadata", None) or {}
        name = request.tool_call.get("name", "")
        return bool(meta.get(GATEWAY)) or (self.only is not None and name not in self.only)

    def _refusal(self, request: Any, text: str) -> ToolMessage:
        return ToolMessage(content=text, tool_call_id=request.tool_call.get("id") or "", status="error")

    def wrap_tool_call(self, request: Any, handler: Callable[[Any], Any]) -> Any:
        if self._skip(request):
            return handler(request)
        tc = request.tool_call
        v = self.gov.decide(tc.get("name", ""), tc.get("args") or {}, tc.get("id"))
        if v.kind != "run":
            return self._refusal(request, v.text)
        try:
            out = handler(request)
        except Exception as err:
            self.gov.report(v, err=err)
            raise
        self.gov.report(v, _content(out))
        return out

    async def awrap_tool_call(self, request: Any, handler: Callable[[Any], Awaitable[Any]]) -> Any:
        if self._skip(request):
            return await handler(request)
        tc = request.tool_call
        v = await self.gov.adecide(tc.get("name", ""), tc.get("args") or {}, tc.get("id"))
        if v.kind != "run":
            return self._refusal(request, v.text)
        try:
            out = await handler(request)
        except Exception as err:
            await self.gov.areport(v, err=err)
            raise
        await self.gov.areport(v, _content(out))
        return out


def deedglass_tool(
    client: Deedglass,
    name: str,
    args_schema: Any,
    *,
    description: str | None = None,
    wait_for_approval: float = 120.0,
    on_action: Callable[[Any], None] | None = None,
) -> StructuredTool:
    """A LangChain tool that Deedglass runs (gateway mode): ``postgres.execute``, ``github.create_issue``, …

    The model sees ``<connector>__<tool>``. A denial or a pending approval comes back as text.
    """

    def run(**kwargs: Any) -> Any:
        try:
            a = client.act(name, kwargs, wait_for_approval=wait_for_approval)
        except DeedglassError as e:
            if on_action and e.action is not None:
                on_action(e.action)
            return denial_text(e)
        if on_action:
            on_action(a)
        if a.status == "pending_approval":
            from ..wrap import pending_text

            return pending_text(a)
        return a.result if a.status == "executed" else denial_text(a)

    return StructuredTool.from_function(
        func=run,
        name=name.replace(".", "__", 1),
        description=description or f"Runs {name} through Deedglass.",
        args_schema=args_schema,
        metadata={GATEWAY: name},
    )


def govern_tool(
    target_tool: Any,
    client: Deedglass | AsyncDeedglass,
    *,
    connector: str = "app",
    name_map: Mapping[str, str] | None = None,
    wait_for_approval: float = 120.0,
    fail_open: bool = False,
) -> StructuredTool:
    """Puts Deedglass in front of an existing LangChain tool (e.g. for LangGraph ToolNode).

    When invoked, Deedglass decides before execution; if authorized, the original tool executes and
    its result is reported to Deedglass. If denied or pending, returns the denial or pending text.
    """
    gov = Governor(
        client,
        framework="langchain",
        connector=connector,
        name_map=name_map,
        wait_for_approval=wait_for_approval,
        fail_open=fail_open,
    )

    orig_name = getattr(target_tool, "name", "tool")
    orig_desc = getattr(target_tool, "description", None) or f"Governed {orig_name}"
    orig_schema = getattr(target_tool, "args_schema", None)

    def run(**kwargs: Any) -> Any:
        v = gov.decide(orig_name, kwargs)
        if v.kind != "run":
            return v.text
        try:
            res = target_tool.invoke(kwargs) if hasattr(target_tool, "invoke") else target_tool(**kwargs)
        except Exception as err:
            gov.report(v, err=err)
            raise
        gov.report(v, _content(res))
        return res

    async def arun(**kwargs: Any) -> Any:
        v = await gov.adecide(orig_name, kwargs)
        if v.kind != "run":
            return v.text
        try:
            if hasattr(target_tool, "ainvoke"):
                res = await target_tool.ainvoke(kwargs)
            elif hasattr(target_tool, "invoke"):
                res = target_tool.invoke(kwargs)
            elif asyncio.iscoroutinefunction(target_tool):
                res = await target_tool(**kwargs)
            else:
                res = target_tool(**kwargs)
        except Exception as err:
            await gov.areport(v, err=err)
            raise
        await gov.areport(v, _content(res))
        return res

    return StructuredTool.from_function(
        func=run,
        coroutine=arun,
        name=orig_name,
        description=orig_desc,
        args_schema=orig_schema,
    )
