"""The adapters (S32 §3.8, §6): LangChain v1 on its real agent loop, the Claude Agent SDK's hooks.

Deny → the model-visible sentence; allow → the result passes through; pending → wait, then the
waiting sentence; unreachable → fail closed with the sentence and the local body NOT run (a counter
shows nothing ran); edge mode reports exactly once.
"""

from __future__ import annotations

import json
import subprocess
import sys
from typing import Any

import httpx
import pytest

from deedglass import AsyncDeedglass, Deedglass

from .helpers import BASE, DENY_MSG, KEY, PENDING, action, denied_body, ok
from .test_client import Script

pytest.importorskip("langchain")
pytest.importorskip("claude_agent_sdk")

from langchain.agents import create_agent
from langchain_core.language_models.fake_chat_models import GenericFakeChatModel
from langchain_core.messages import AIMessage, ToolMessage
from langchain_core.tools import tool

from deedglass.claude_agent_sdk import deedglass_hooks, deedglass_mcp_server
from deedglass.langchain import DeedglassMiddleware, deedglass_tool

AUTH = {"mode": "proxy", "status": "authorized", "state": "executing", "witness": None, "deed": None}
OUTAGE = "Deedglass isn't reachable, so this tool was not run. Check that deedglass serve is running."


class FakeModel(GenericFakeChatModel):
    def bind_tools(self, tools: Any, **kw: Any) -> FakeModel:
        return self


def model() -> FakeModel:
    call = {"name": "ship_order", "args": {"order_id": 42}, "id": "call_1"}
    return FakeModel(messages=iter([AIMessage(content="", tool_calls=[call]), AIMessage(content="done")]))


def sync(s: Script) -> Deedglass:
    c = Deedglass(KEY, base_url=BASE, http_client=httpx.Client(transport=httpx.MockTransport(s)))
    c._sleep = lambda _s: None
    return c


def boom(req: httpx.Request) -> httpx.Response:
    raise httpx.ConnectError("refused", request=req)


def run_agent(s: Script, **kw: Any) -> tuple[list[int], list[Any]]:
    ran: list[int] = []

    @tool
    def ship_order(order_id: int) -> str:
        """Mark an order shipped."""
        ran.append(order_id)
        return f"shipped {order_id}"

    agent = create_agent(model(), tools=[ship_order], middleware=[DeedglassMiddleware(sync(s), **kw)])
    out = agent.invoke({"messages": [{"role": "user", "content": "ship 42"}]})
    return ran, [m for m in out["messages"] if isinstance(m, ToolMessage)]


def test_langchain_allow_reports_once() -> None:
    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    ran, msgs = run_agent(s, connector="orders")
    assert ran == [42] and msgs[0].content == "shipped 42"
    b = s.body(0)
    assert b["tool"] == "orders.ship_order" and b["args"] == {"order_id": 42}
    assert b["context"] == {
        "client": {"name": "deedglass-python/langchain", "version": "0.15.0"},
        "transport": "sdk",
        "call_id": "call_1",
    }
    assert [r.url.path for r in s.calls] == ["/v1/actions", f"/v1/actions/{action()['id']}/result"]
    assert s.body(1)["status"] == "succeeded" and s.body(1)["result"] == "shipped 42"


def test_langchain_deny_never_runs() -> None:
    ran, msgs = run_agent(Script(ok(denied_body(), 403)))
    assert ran == [] and msgs[0].status == "error"
    assert msgs[0].content == DENY_MSG + " Nothing was run."


def test_langchain_pending_then_sentence() -> None:
    pend = action(**{**PENDING, "mode": "proxy"})
    ran, msgs = run_agent(Script(ok(pend, 202), ok(pend)), wait_for_approval=0.001)
    assert ran == []
    assert msgs[0].content == (
        "Waiting for approval in Console › Approvals (apr_01j9zw0000000000000000000a). Try again after someone decides."
    )


def test_langchain_pending_then_approved() -> None:
    pend = action(**{**PENDING, "mode": "proxy"})
    ran, msgs = run_agent(Script(ok(pend, 202), ok(action(**AUTH)), ok(action(mode="proxy"))))
    assert ran == [42] and msgs[0].content == "shipped 42"


def test_langchain_unreachable_fails_closed() -> None:
    ran, msgs = run_agent(Script(boom))
    assert ran == [] and msgs[0].content == OUTAGE


def test_langchain_bad_name_refused_locally() -> None:
    s = Script(ok(action()))
    ran, msgs = run_agent(s, name_map={"ship_order": "orders.shïp"})
    assert ran == [] and s.calls == [] and "isn't a tool Deedglass knows" in msgs[0].content


async def test_langchain_async() -> None:
    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    ran: list[int] = []

    @tool
    async def ship_order(order_id: int) -> str:
        """Mark an order shipped."""
        ran.append(order_id)
        return f"shipped {order_id}"

    c = AsyncDeedglass(KEY, base_url=BASE, http_client=httpx.AsyncClient(transport=httpx.MockTransport(s)))
    agent = create_agent(model(), tools=[ship_order], middleware=[DeedglassMiddleware(c)])
    out = await agent.ainvoke({"messages": [{"role": "user", "content": "ship 42"}]})
    assert ran == [42] and any(isinstance(m, ToolMessage) and m.content == "shipped 42" for m in out["messages"])
    assert len(s.calls) == 2


def test_deedglass_tool_gateway() -> None:
    from pydantic import BaseModel

    class Args(BaseModel):
        sql: str

    s = Script(ok(denied_body(), 403), ok(action()))
    t = deedglass_tool(sync(s), "postgres.execute", Args)
    assert t.name == "postgres__execute" and t.metadata == {"deedglass_gateway": "postgres.execute"}
    assert t.invoke({"sql": "DROP TABLE public.orders"}) == DENY_MSG + " Nothing was run."
    assert t.invoke({"sql": "UPDATE public.orders SET status = 'shipped' WHERE id = 42"}) == {"rows": 1}


# --- Claude Agent SDK ---


def pre_input(tool: str = "Bash", args: Any = None, tid: str = "toolu_01") -> dict[str, Any]:
    return {
        "hook_event_name": "PreToolUse",
        "tool_name": tool,
        "tool_input": args or {"command": "ls"},
        "tool_use_id": tid,
    }


async def test_claude_deny() -> None:
    s = Script(ok(denied_body(), 403))
    hooks = deedglass_hooks(
        AsyncDeedglass(KEY, base_url=BASE, http_client=httpx.AsyncClient(transport=httpx.MockTransport(s)))
    )
    pre = hooks["PreToolUse"][0].hooks[0]
    out = await pre(pre_input(args={"command": "rm -rf /"}), "toolu_01", None)
    assert out["hookSpecificOutput"]["permissionDecision"] == "deny"
    assert out["hookSpecificOutput"]["permissionDecisionReason"] == DENY_MSG + " Nothing was run."
    assert s.body(0)["tool"] == "claude.Bash" and s.body(0)["context"]["call_id"] == "toolu_01"


async def test_claude_authorized_then_post_reports_once() -> None:
    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    hooks = deedglass_hooks(
        AsyncDeedglass(KEY, base_url=BASE, http_client=httpx.AsyncClient(transport=httpx.MockTransport(s)))
    )
    pre, post = hooks["PreToolUse"][0].hooks[0], hooks["PostToolUse"][0].hooks[0]
    assert await pre(pre_input(), "toolu_01", None) == {}
    post_in = {
        "hook_event_name": "PostToolUse",
        "tool_name": "Bash",
        "tool_use_id": "toolu_01",
        "tool_response": {"stdout": "a"},
    }
    assert await post(post_in, "toolu_01", None) == {}
    assert await post(post_in, "toolu_01", None) == {}  # a second Post reports nothing
    assert len(s.calls) == 2 and s.body(1)["result"] == {"stdout": "a"}


async def test_claude_failure_is_unknown() -> None:
    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    hooks = deedglass_hooks(
        AsyncDeedglass(KEY, base_url=BASE, http_client=httpx.AsyncClient(transport=httpx.MockTransport(s)))
    )
    await hooks["PreToolUse"][0].hooks[0](pre_input(tid="toolu_02"), "toolu_02", None)
    fail = {"hook_event_name": "PostToolUseFailure", "tool_use_id": "toolu_02", "error": "exit 1"}
    await hooks["PostToolUseFailure"][0].hooks[0](fail, "toolu_02", None)
    assert s.body(1)["status"] == "unknown" and s.body(1)["error_code"] == "tool_error"


async def test_claude_unreachable_denies() -> None:
    hooks = deedglass_hooks(sync(Script(boom)))  # a sync client works too (run in a thread)
    out = await hooks["PreToolUse"][0].hooks[0](pre_input(), "toolu_03", None)
    assert out["hookSpecificOutput"]["permissionDecisionReason"] == OUTAGE


def test_claude_mcp_server_builds() -> None:
    from deedglass._types import Tool

    t = Tool.from_json(
        {
            "name": "postgres.execute",
            "connector": "postgres",
            "description": "Run SQL.",
            "input_schema": {"type": "object"},
        }
    )
    srv = deedglass_mcp_server(AsyncDeedglass(KEY, base_url=BASE), [t])
    assert srv["name"] == "deedglass" and srv["type"] == "sdk"


# --- import hygiene and placeholders ---


def test_import_loads_no_framework() -> None:
    code = "import sys, deedglass; print(json.dumps(sorted(m for m in sys.modules if m.split('.')[0] in ('langchain','langchain_core','langgraph','claude_agent_sdk','agents','crewai','cryptography'))))"
    out = subprocess.run([sys.executable, "-c", "import json; " + code], capture_output=True, text=True, check=True)
    assert json.loads(out.stdout) == []


@pytest.mark.parametrize("mod", ["openai_agents", "crewai"])
def test_placeholders(mod: str) -> None:
    with pytest.raises(ImportError, match="lands in the next release"):
        __import__(f"deedglass.{mod}")
