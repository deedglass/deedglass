"""The remaining client and wrap paths: whoami, intents on act, tools, deeds, approvals, cancel, waits."""

from __future__ import annotations

import json
from typing import Any

import httpx
import pytest

from deedglass import (
    ApprovalPendingError,
    AsyncDeedglass,
    Deedglass,
    IntentKey,
    InvalidRequestError,
    PermissionDeniedError,
    PolicyDeniedError,
    wrap_tool,
    wrap_tool_async,
)

from .conftest import repo
from .helpers import ACT, APR, BASE, DEED, KEY, PENDING, action, ok
from .test_client import Script, aw, make

ORG = "org_01k6h7kp6msmzrhdzwtcr3jnkp"
AGT = "agt_01k633rrmh3bcz2p39gg4vp46q"
WHO = {
    "object": "whoami",
    "org": {"id": ORG, "name": "Local"},
    "agent": {"id": AGT, "name": "quickstart-agent"},
    "key": {"mode": "test", "scopes": ["actions:execute"]},
}
AUTH = {"mode": "proxy", "status": "authorized", "state": "executing", "witness": None, "deed": None}


def pem() -> str:
    return repo("testdata/intents/keys/test1.pem").read_text()


async def test_whoami_cached(kind: str) -> None:
    s = Script(ok(WHO))
    c = make(kind, s)
    w = await aw(c.whoami())
    w2 = await aw(c.whoami())
    assert w.org_id == ORG and w.agent is not None and w.agent.name == "quickstart-agent" and w2 is w
    assert len(s.calls) == 1 and w.scopes == ("actions:execute",)


async def test_act_signs_an_intent(kind: str) -> None:
    s = Script(ok(WHO), ok(action()))
    c = make(kind, s, intent_key=IntentKey(pem()))
    await aw(c.act("stripe.refund", {"charge": "ch_1", "amount": 1200}, on_behalf_of="usr_ext_4821"))
    h = s.calls[1].headers
    assert h["deedglass-on-behalf-of"] == "usr_ext_4821"
    intent = json.loads(__import__("base64").urlsafe_b64decode(h["deedglass-intent"].split(".")[0] + "=="))
    assert intent["org"] == ORG and intent["agent"] == AGT and intent["on_behalf_of"] == "usr_ext_4821"
    assert intent["tool"] == "stripe.refund"


async def test_tools_list_pages(kind: str) -> None:
    t = {"object": "tool", "name": "postgres.query", "connector": "postgres", "side_effect": "read", "levels": ["L0"]}
    s = Script(
        ok({"object": "list", "data": [t], "has_more": True, "next_cursor": "c1"}),
        ok({"object": "list", "data": [t], "has_more": False}),
    )
    tools = await aw(make(kind, s).tools.list(include_hidden=True))
    assert [x.name for x in tools] == ["postgres.query", "postgres.query"] and tools[0].levels == ("L0",)
    assert s.calls[0].url.params["include_hidden"] == "true" and s.calls[1].url.params["cursor"] == "c1"


async def test_tools_list_glob_filter(kind: str) -> None:
    t1 = {"object": "tool", "name": "postgres.query", "connector": "postgres"}
    t2 = {"object": "tool", "name": "billing.refund", "connector": "billing"}
    t3 = {"object": "tool", "name": "stripe.charge", "connector": "stripe"}
    s = Script(ok({"object": "list", "data": [t1, t2, t3], "has_more": False}))
    tools = await aw(make(kind, s).tools.list(include=["billing.*", "*.charge"]))
    assert [x.name for x in tools] == ["billing.refund", "stripe.charge"]


async def test_deeds_and_approvals(kind: str) -> None:
    env = {"object": "deed", "id": DEED}
    apr = {
        "object": "approval",
        "id": APR,
        "status": "pending",
        "rule_id": "r",
        "summary": "S.",
        "expires_at": "2026-10-10T10:42:00.000Z",
    }
    s = Script(
        ok(env),
        ok({"object": "list", "data": [env], "has_more": False}),
        httpx.Response(200, content=b"\x1f\x8bTAR"),
        ok(apr),
    )
    c = make(kind, s)
    assert await aw(c.deeds.get(DEED)) == env
    it = c.deeds.list(kind="action", from_seq=1)
    got = [d async for d in it] if kind == "async" else list(it)
    assert got == [env] and s.calls[1].url.params["from_seq"] == "1"
    assert await aw(c.deeds.export(issued_at__gte="2026-10-01T00:00:00Z")) == b"\x1f\x8bTAR"
    assert s.calls[2].url.params["issued_at[gte]"] == "2026-10-01T00:00:00Z"
    a = await aw(c.approvals.get(APR))
    assert a.status == "pending" and a.expires_at is not None
    with pytest.raises(TypeError):
        await aw(c.deeds.get("nope"))


async def test_cancel_and_wait(kind: str) -> None:
    s = Script(ok(action(status="cancelled", state="sealed")), ok(action(**PENDING)), ok(action()))
    c = make(kind, s)
    a = await aw(c.actions.cancel(ACT))
    assert a.status == "cancelled" and s.calls[0].headers["idempotency-key"].startswith("idk_")
    b = await aw(c.actions.wait(ACT, timeout=60))
    assert b.status == "executed" and len(s.calls) == 3
    created = await aw(make(kind, Script(ok(action()))).actions.create("app.echo", {"n": 1}, mode="proxy", wait=0))
    assert created.status == "executed"


# --- wrap paths ---


def sync(s: Script) -> Deedglass:
    c = Deedglass(KEY, base_url=BASE, http_client=httpx.Client(transport=httpx.MockTransport(s)))
    c._sleep = lambda _s: None
    return c


def test_wrap_wait_for_approval_then_runs() -> None:
    pend = action(**{**PENDING, "mode": "proxy"})
    s = Script(ok(pend, 202), ok(action(**AUTH)), ok(action(mode="proxy")))
    sealed: list[Any] = []
    w = wrap_tool(lambda n: n + 1, client=sync(s), tool="app.inc", on_pending="wait", on_sealed=sealed.append)
    assert w(1) == 2 and len(sealed) == 1


def test_wrap_wait_still_pending_raises() -> None:
    pend = action(**{**PENDING, "mode": "proxy"})
    w = wrap_tool(
        lambda: 1, client=sync(Script(ok(pend, 202), ok(pend))), tool="app.x", on_pending="wait", approval_timeout=0.001
    )
    with pytest.raises(ApprovalPendingError):
        w()


@pytest.mark.parametrize(
    ("status", "exc", "code"),
    [
        ("expired", InvalidRequestError, "approval_expired"),
        ("cancelled", PermissionDeniedError, "cancelled"),
        ("denied", PolicyDeniedError, "policy_denied"),
    ],
)
def test_wrap_settled_refusals(status: str, exc: type[Exception], code: str) -> None:
    pend = action(**{**PENDING, "mode": "proxy"})
    done = action(
        **{
            **PENDING,
            "mode": "proxy",
            "status": status,
            "approval": {"id": APR, "status": status, "decision_reason": "Not today."},
        }
    )
    w = wrap_tool(lambda: 1, client=sync(Script(ok(pend, 202), ok(done))), tool="app.x", on_pending="wait")
    with pytest.raises(exc) as e:
        w()
    assert getattr(e.value, "code", "") == code


def test_wrap_result_shapes_and_report_retry() -> None:
    class Obj:
        def __str__(self) -> str:
            return "an object"

    s = Script(
        ok(action(**AUTH)),
        httpx.Response(503, json={"error": {"type": "api_error", "code": "service_unavailable", "message": "x."}}),
        ok(action(mode="proxy")),
    )
    w = wrap_tool(lambda: Obj(), client=sync(s), tool="app.obj")
    w()
    rep = json.loads(s.calls[-1].content)
    assert rep["result"] == {"text": "an object"}
    s2 = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    w2 = wrap_tool(lambda: "x" * 300_000, client=sync(s2), tool="app.big", to_result=lambda r: {"len": len(r)})
    w2()
    assert json.loads(s2.calls[-1].content)["result"] == {"len": 300_000}
    s3 = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    wrap_tool(lambda: "y" * 300_000, client=sync(s3), tool="app.big")()
    assert "result_digest" in json.loads(s3.calls[-1].content)


def test_wrap_report_lost_logs(caplog: pytest.LogCaptureFixture) -> None:
    s = Script(
        ok(action(**AUTH)),
        httpx.Response(
            400, json={"error": {"type": "invalid_request_error", "code": "validation_failed", "message": "x."}}
        ),
    )
    wrap_tool(lambda: 1, client=sync(s), tool="app.x")()
    assert "after 5 attempts" in caplog.text


def test_wrap_args_from_and_bad_tool() -> None:
    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    w = wrap_tool(lambda a, b: a + b, client=sync(s), tool="app.add", args_from=lambda a, b: {"sum_of": [a, b]})
    assert w(1, 2) == 3 and json.loads(s.calls[0].content)["args"] == {"sum_of": [1, 2]}
    with pytest.raises(InvalidRequestError):
        wrap_tool(lambda: 1, client=sync(s), tool="Bad Name")


async def test_wrap_async_paths() -> None:
    pend = action(**{**PENDING, "mode": "proxy"})
    s = Script(ok(pend, 202), ok(action(**AUTH)), ok(action(mode="proxy")))
    c = AsyncDeedglass(KEY, base_url=BASE, http_client=httpx.AsyncClient(transport=httpx.MockTransport(s)))

    async def f(x: int) -> int:
        return x

    w = wrap_tool_async(f, client=c, tool="app.f", on_pending="wait")
    assert await w(7) == 7

    async def g() -> None:
        raise ValueError("nope")

    s2 = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    c2 = AsyncDeedglass(KEY, base_url=BASE, http_client=httpx.AsyncClient(transport=httpx.MockTransport(s2)))
    with pytest.raises(Exception, match="raised after Deedglass authorized"):
        await wrap_tool_async(g, client=c2, tool="app.g", failure_means="not_applied")()
    assert json.loads(s2.calls[-1].content)["status"] == "failed"
    s3 = Script(ok(pend, 202))
    c3 = AsyncDeedglass(KEY, base_url=BASE, http_client=httpx.AsyncClient(transport=httpx.MockTransport(s3)))
    with pytest.raises(ApprovalPendingError):
        await wrap_tool_async(f, client=c3, tool="app.f")(1)


async def test_local_tool_decorator(kind: str) -> None:
    s = Script(ok(action(**AUTH)), ok(action(mode="proxy")))
    c = make(kind, s)
    if kind == "sync":

        @c.tool("billing.refund", execute="local")
        def refund(amount: int) -> dict[str, int]:
            return {"refunded": amount}

        assert refund(5) == {"refunded": 5}
    else:

        @c.tool("billing.refund", execute="local")
        async def arefund(amount: int) -> dict[str, int]:
            return {"refunded": amount}

        assert await arefund(5) == {"refunded": 5}
    assert json.loads(s.calls[0].content)["mode"] == "proxy"


def test_context_manager_and_close() -> None:
    with Deedglass(KEY, base_url=BASE) as c:
        assert c.base_url == BASE


async def test_async_context_manager() -> None:
    async with AsyncDeedglass(KEY, base_url=BASE) as c:
        assert c.base_url == BASE
