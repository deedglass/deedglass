"""The S32 §6 client table, every case sync and async, over httpx.MockTransport (no network)."""

from __future__ import annotations

import inspect
import json
import logging
from collections.abc import Callable
from typing import Any

import httpx
import pytest

from deedglass import (
    APIConnectionError,
    APIError,
    AsyncDeedglass,
    AuthenticationError,
    BudgetExceededError,
    Deedglass,
    PolicyDeniedError,
    RateLimitError,
)
from deedglass._core import Core, run_sync
from deedglass._ops import create_action

from .helpers import ACT, BASE, DEED, DENY_MSG, KEY, PENDING, SECRET, action, denied_body, err, ok

Responder = Callable[[httpx.Request], httpx.Response]


class Script:
    """Each request takes the next responder (the last one repeats); requests are recorded."""

    def __init__(self, *responders: Responder | httpx.Response) -> None:
        self.responders = responders
        self.calls: list[httpx.Request] = []

    def __call__(self, req: httpx.Request) -> httpx.Response:
        self.calls.append(req)
        r = self.responders[min(len(self.calls) - 1, len(self.responders) - 1)]
        return r(req) if callable(r) else r

    def body(self, i: int) -> Any:
        return json.loads(self.calls[i].content)


@pytest.fixture(params=["sync", "async"])
def kind(request: pytest.FixtureRequest) -> str:
    return str(request.param)


def make(kind: str, script: Script, **kw: Any) -> Any:
    sleeps: list[float] = []
    if kind == "sync":
        c: Any = Deedglass(KEY, base_url=BASE, http_client=httpx.Client(transport=httpx.MockTransport(script)), **kw)
        c._sleep = sleeps.append
    else:
        c = AsyncDeedglass(
            KEY, base_url=BASE, http_client=httpx.AsyncClient(transport=httpx.MockTransport(script)), **kw
        )

        async def asleep(s: float) -> None:
            sleeps.append(s)

        c._sleep = asleep
    c.sleeps = sleeps
    return c


async def aw(v: Any) -> Any:
    return await v if inspect.isawaitable(v) else v


async def test_executed(kind: str) -> None:
    s = Script(ok(action(), 200))
    a = await aw(
        make(kind, s).act("postgres.execute", {"sql": "UPDATE public.orders SET status = 'shipped' WHERE id = 42"})
    )
    assert a.verified and a.request_id == "req_test" and a.result == {"rows": 1}
    assert str(a) == "act_01j9… · postgres.execute · executed · L2 · VERIFIED · deed_01j9…"
    h = s.calls[0].headers
    assert h["authorization"] == "Bearer " + KEY
    assert h["deedglass-version"] == "2026-10-01"
    assert h["idempotency-key"].startswith("idk_") and len(h["idempotency-key"]) == 30
    assert h["user-agent"].startswith("deedglass-python/0.11.0 (python ")
    b = s.body(0)
    assert b["mode"] == "gateway" and b["wait"] == 10.0
    assert b["context"] == {"client": {"name": "deedglass-python", "version": "0.11.0"}, "transport": "sdk"}


async def test_policy_denied(kind: str) -> None:
    s = Script(ok(denied_body(), 403))
    with pytest.raises(PolicyDeniedError) as e:
        await aw(make(kind, s).act("postgres.execute", {"sql": "DROP TABLE public.orders"}))
    assert e.value.action is not None and e.value.action.deed == DEED
    assert e.value.doc_url == "https://docs.deedglass.com/errors/policy_denied"
    assert e.value.rule_id == "block-destructive-sql"
    assert str(e.value) == DENY_MSG + " (req_01k6)"


async def test_budget_exceeded_not_retried(kind: str) -> None:
    s = Script(err(429, "rate_limit_error", "budget_exceeded", "Over budget.", **{"retry-after": "41"}))
    c = make(kind, s)
    with pytest.raises(BudgetExceededError) as e:
        await aw(c.act("postgres.execute", {"sql": "SELECT 1"}))
    assert e.value.retry_after == 41 and len(s.calls) == 1 and c.sleeps == []


async def test_rate_limited_retried(kind: str) -> None:
    s = Script(err(429, "rate_limit_error", "rate_limited", **{"retry-after": "1"}), ok(action()))
    c = make(kind, s)
    a = await aw(c.act("postgres.query", {"sql": "SELECT 1"}))
    assert a.status == "executed" and len(s.calls) == 2 and c.sleeps == [1.0]


async def test_503_twice_same_key(kind: str) -> None:
    s = Script(err(503, "api_error", "service_unavailable"), err(503, "api_error", "service_unavailable"), ok(action()))
    c = make(kind, s)
    await aw(c.act("postgres.query", {"sql": "SELECT 1"}))
    keys = {r.headers["idempotency-key"] for r in s.calls}
    assert len(s.calls) == 3 and len(keys) == 1
    assert all(0.375 <= x <= 1.25 * 2 for x in c.sleeps)


async def test_connection_reset_on_get(kind: str) -> None:
    def boom(req: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("reset", request=req)

    s = Script(boom)
    with pytest.raises(APIConnectionError) as e:
        await aw(make(kind, s).actions.get(ACT))
    assert len(s.calls) == 3
    assert str(e.value) == "Can't reach Deedglass at http://dg.test. Is deedglass serve running? Try: deedglass doctor."
    assert isinstance(e.value.__cause__, httpx.ConnectError)


async def test_pending_then_polls(kind: str) -> None:
    s = Script(ok(action(**PENDING), 202), ok(action(**PENDING)), ok(action(**PENDING)), ok(action()))
    a = await aw(make(kind, s).act("postgres.execute", {"sql": "UPDATE …"}, wait_for_approval=60))
    assert a.status == "executed"
    gets = [r for r in s.calls if r.method == "GET"]
    assert len(gets) == 3 and all(r.url.params["wait"] == "25" for r in gets)


async def test_pending_past_deadline(kind: str) -> None:
    s = Script(ok(action(**PENDING), 202), ok(action(**PENDING)))
    a = await aw(make(kind, s).act("postgres.execute", {"sql": "UPDATE …"}, wait_for_approval=0.001))
    assert a.status == "pending_approval"
    s2 = Script(ok(action(**PENDING), 202))
    a2 = await aw(make(kind, s2).act("postgres.execute", {"sql": "UPDATE …"}))
    assert a2.status == "pending_approval" and len(s2.calls) == 1


async def test_replayed(kind: str) -> None:
    s = Script(ok(action(), 200, **{"deedglass-idempotent-replayed": "true"}))
    a = await aw(make(kind, s).act("postgres.query", {}))
    assert a.replayed is True


async def test_unknown_field_kept(kind: str) -> None:
    s = Script(ok(action(future_field={"x": 1})))
    a = await aw(make(kind, s).actions.get(ACT))
    assert a.raw["future_field"] == {"x": 1}


async def test_body_too_large(kind: str) -> None:
    s = Script(httpx.Response(200, content=b"{" + b" " * (9 << 20) + b"}"))
    with pytest.raises(APIError) as e:
        await aw(make(kind, s).actions.get(ACT))
    assert e.value.message == "The response was larger than 8 MiB."


async def test_unreadable_2xx(kind: str) -> None:
    s = Script(httpx.Response(200, content=b"<html>"))
    with pytest.raises(APIError) as e:
        await aw(make(kind, s).actions.get(ACT))
    assert e.value.code == "unreadable_response"


def test_malformed_key_sends_nothing() -> None:
    s = Script(ok(action()))
    for bad in ("dg_test_short", "sk_live_" + "x" * 40, "dg_test_Ab12Cd34_" + "x" * 31):
        with pytest.raises(AuthenticationError) as e:
            Deedglass(bad, base_url=BASE, http_client=httpx.Client(transport=httpx.MockTransport(s)))
        assert "dg_live_ or dg_test_" in str(e.value)
    assert s.calls == []


def test_missing_key_message(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("DEEDGLASS_API_KEY", raising=False)
    with pytest.raises(AuthenticationError) as e:
        Deedglass()
    assert str(e.value) == (
        "No API key. Pass api_key=… or set DEEDGLASS_API_KEY. Create one with: deedglass agent keys create <agent>."
    )


def test_env_defaults(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("DEEDGLASS_API_KEY", KEY)
    monkeypatch.setenv("DEEDGLASS_SERVER", "http://example.test:9/")
    assert Deedglass().base_url == "http://example.test:9"


async def test_secret_never_printed(kind: str, caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.DEBUG)
    s = Script(err(503, "api_error", "service_unavailable"), ok(denied_body(), 403))
    c = make(kind, s)
    with pytest.raises(PolicyDeniedError) as e:
        await aw(c.act("postgres.execute", {"sql": "DROP TABLE x"}))
    texts = [repr(c), str(e.value), repr(e.value), caplog.text, repr(c._core)]
    assert all(SECRET not in t for t in texts)
    assert repr(c) == f"{type(c).__name__}(base_url='http://dg.test', api_key='dg_test_Ab12Cd34_••••')"


async def test_list_three_pages(kind: str) -> None:
    pages = [
        ok(
            {
                "object": "list",
                "data": [action(id=f"act_01j9zw000000000000000000{i}a")],
                "has_more": i < 2,
                "next_cursor": f"c{i}",
            }
        )
        for i in range(3)
    ]
    s = Script(*pages)
    it = make(kind, s).actions.list(limit=1, verdict="MISMATCH", created_at__gte="2026-10-01T00:00:00Z")
    if kind == "sync":
        got = [a.id for a in it]
    else:
        got = [a.id async for a in it]
    assert len(got) == 3 and len(s.calls) == 3
    assert (
        s.calls[1].url.params["cursor"] == "c0" and s.calls[0].url.params["created_at[gte]"] == "2026-10-01T00:00:00Z"
    )


async def test_proxy_report_key(kind: str) -> None:
    s = Script(ok(action(mode="proxy", status="authorized", state="executing")), ok(action(mode="proxy")))
    c = make(kind, s)
    a = await aw(c.submit_proxy("app.refund", {"amount": 1}, call_id="toolu_01"))
    assert s.body(0)["connector_type"] == "sdk" and s.body(0)["context"]["call_id"] == "toolu_01"
    await aw(c.actions.report_result(a.id, {"status": "succeeded", "started_at": "x", "finished_at": "y"}))
    assert s.calls[1].headers["idempotency-key"] == "sdkr_" + ACT


async def test_call_id_outside_rule_dropped(kind: str) -> None:
    s = Script(ok(action()))
    await aw(make(kind, s).act("postgres.query", {}, call_id="has a space"))
    assert "call_id" not in s.body(0)["context"]


async def test_bad_tool_and_args_local(kind: str) -> None:
    s = Script(ok(action()))
    c = make(kind, s)
    with pytest.raises(Exception, match="isn't a Deedglass tool name"):
        await aw(c.act("Postgres.Execute", {}))
    with pytest.raises(Exception, match="Send it as a string"):
        await aw(c.act("postgres.execute", {"n": 2**53}))
    assert s.calls == []


def test_sync_and_async_send_identical_requests() -> None:
    """30 calls through both drivers: the same method, URL, headers (but the random key) and body."""
    core = Core.create(KEY, BASE, 30.0, 0, "2026-10-01", None, None, None)
    reqs: list[httpx.Request] = []

    def capture(req: httpx.Request) -> httpx.Response:
        reqs.append(req)
        return ok(action())

    client = httpx.Client(transport=httpx.MockTransport(capture))

    async def arun() -> None:
        from deedglass._core import run_async

        ac = httpx.AsyncClient(transport=httpx.MockTransport(capture))
        for i in range(30):
            await run_async(ac, create_action(core, "app.echo", {"n": i}, idempotency_key=f"k{i}"), lambda s: None)  # type: ignore[arg-type,return-value]

    for i in range(30):
        run_sync(client, create_action(core, "app.echo", {"n": i}, idempotency_key=f"k{i}"), lambda s: None)
    import asyncio

    asyncio.run(arun())
    assert len(reqs) == 60
    for a, b in zip(reqs[:30], reqs[30:]):
        assert (a.method, str(a.url), a.content) == (b.method, str(b.url), b.content)
        assert dict(a.headers) == dict(b.headers)


async def test_tool_decorator_gateway(kind: str) -> None:
    s = Script(ok(denied_body(), 403), ok(action()))
    c = make(kind, s)
    if kind == "sync":

        @c.tool("postgres.execute", on_deny="return")
        def execute(sql: str) -> dict[str, Any]:
            raise AssertionError("the body never runs in gateway mode")

        assert execute("DROP TABLE public.orders") == DENY_MSG + " Nothing was run."
        assert execute(sql="UPDATE …") == {"rows": 1}
    else:

        @c.tool("postgres.execute", on_deny="return")
        async def aexecute(sql: str) -> dict[str, Any]:
            raise AssertionError("the body never runs in gateway mode")

        assert await aexecute("DROP TABLE public.orders") == DENY_MSG + " Nothing was run."
        assert await aexecute(sql="UPDATE …") == {"rows": 1}
    assert s.body(0)["args"] == {"sql": "DROP TABLE public.orders"}


async def test_tool_decorator_raise(kind: str) -> None:
    s = Script(ok(denied_body(), 403))
    c = make(kind, s)

    if kind == "sync":

        @c.tool("postgres.execute")
        def f(sql: str) -> None: ...

        with pytest.raises(PolicyDeniedError):
            f("DROP TABLE x")
    else:

        @c.tool("postgres.execute")
        async def g(sql: str) -> None: ...

        with pytest.raises(PolicyDeniedError):
            await g("DROP TABLE x")


def test_rate_limit_error_class() -> None:
    from deedglass._errors import class_for

    assert class_for("rate_limit_error", "rate_limited") is RateLimitError
    assert class_for("rate_limit_error", "budget_exceeded") is BudgetExceededError
    assert class_for("invalid_request_error", "tool_unknown", has_action=True) is PolicyDeniedError
