"""The clients (S32 §3.2): ``Deedglass`` (httpx.Client) and ``AsyncDeedglass`` (httpx.AsyncClient).

Both drive the same operations (:mod:`._ops`), so they send identical requests.
"""

from __future__ import annotations

import asyncio
import functools
import logging
import time
from collections.abc import AsyncIterator, Awaitable, Callable, Iterator, Mapping
from typing import Any, Literal, TypeVar

import httpx

from . import _ops
from ._core import Core, Op, mask_key, run_async, run_sync
from ._errors import PermissionDeniedError, PolicyDeniedError, RateLimitError
from ._types import Action, Approval, Tool, Whoami
from ._version import API_VERSION
from .intents import IntentKey
from .wrap import _Registry, denial_text, wrap_tool, wrap_tool_async

T = TypeVar("T")
F = TypeVar("F", bound=Callable[..., Any])
WHOAMI_TTL = 300.0

__all__ = ["AsyncDeedglass", "Deedglass"]


class _Base:
    _core: Core
    _intent_key: IntentKey | None
    _who: tuple[float, Whoami] | None

    def _cached_whoami(self) -> Whoami | None:
        if self._who and time.monotonic() - self._who[0] < WHOAMI_TTL:
            return self._who[1]
        return None

    @property
    def base_url(self) -> str:
        return self._core.base_url

    def __repr__(self) -> str:
        return f"{type(self).__name__}(base_url={self._core.base_url!r}, api_key={mask_key(self._core.api_key)!r})"


# --- sync ---


class Page(Iterator[T]):
    """Auto-paginating iterator over a list route ({object: list, data, has_more, next_cursor})."""

    def __init__(self, client: Deedglass, path: str, query: Mapping[str, Any], build: Callable[[Any], T]) -> None:
        self._c, self._path, self._query, self._build = client, path, dict(query), build
        self._buf: list[Any] = []
        self._cursor: str | None = None
        self._done = False

    def __iter__(self) -> Page[T]:
        return self

    def __next__(self) -> T:
        while not self._buf:
            if self._done:
                raise StopIteration
            q = {**self._query, "cursor": self._cursor}
            items, more, self._cursor = self._c._run(_ops.list_page(self._c._core, self._path, q))
            self._buf = list(items)
            self._done = not more
        return self._build(self._buf.pop(0))


class Actions:
    def __init__(self, c: Deedglass) -> None:
        self._c = c

    def create(self, tool: str, args: Mapping[str, Any] | None = None, **kw: Any) -> Action:
        """POST /v1/actions (mode, wait, idempotency_key, on_behalf_of, session, connector_type, call_id)."""
        return self._c._run(_ops.create_action(self._c._core, tool, args, **self._c._sign(kw)))

    def get(self, action_id: str, *, wait: float = 0.0, expand: list[str] | None = None) -> Action:
        """GET /v1/actions/{id}, optionally waiting up to 25 s for a change."""
        return self._c._run(_ops.get_action(self._c._core, action_id, wait=wait, expand=expand))

    def wait_until_settled(self, action_id: str, *, timeout: float = 600.0) -> Action:
        """Long-polls until the action is settled or ``timeout`` seconds pass."""
        return self._c._run(_ops.wait_until_settled(self._c._core, action_id, timeout=timeout))

    wait = wait_until_settled

    def list(self, *, limit: int | None = None, **filters: Any) -> Page[Action]:
        """GET /v1/actions, every page. Filters: agent, tool, state, verdict, created_at__gte, …"""
        q = {"limit": limit, **_ops.filters(filters)}
        return Page(self._c, "/v1/actions", q, Action.from_json)

    def cancel(self, action_id: str) -> Action:
        """POST /v1/actions/{id}/cancel: only while pending_approval."""
        return self._c._run(_ops.cancel_action(self._c._core, action_id))

    def report_result(self, action_id: str, result: Mapping[str, Any], *, idempotency_key: str | None = None) -> Action:
        """POST /v1/actions/{id}/result: proxy mode, after your code ran."""
        return self._c._run(_ops.report_result(self._c._core, action_id, result, idempotency_key=idempotency_key))


class Deeds:
    def __init__(self, c: Deedglass) -> None:
        self._c = c

    def get(self, deed_id: str) -> dict[str, Any]:
        """GET /v1/deeds/{id}: the deed envelope, exactly as sealed."""
        return self._c._run(_ops.get_deed(self._c._core, deed_id))

    def list(self, *, limit: int | None = None, **filters: Any) -> Page[dict[str, Any]]:
        """GET /v1/deeds, every page. Filters: from_seq, to_seq, kind, action, order."""
        return Page(self._c, "/v1/deeds", {"limit": limit, **_ops.filters(filters)}, dict)

    def export(self, **filters: Any) -> bytes:
        """GET /v1/deeds/export: the export bundle v1 (tar.gz). Needs an org admin key."""
        return self._c._run(_ops.export_deeds(self._c._core, _ops.filters(filters)))


class Approvals:
    def __init__(self, c: Deedglass) -> None:
        self._c = c

    def get(self, approval_id: str) -> Approval:
        return self._c._run(_ops.get_approval(self._c._core, approval_id))


class Tools:
    def __init__(self, c: Deedglass) -> None:
        self._c = c

    def list(self, *, include_hidden: bool = False) -> list[Tool]:
        """GET /v1/tools: the org's connector tools this agent may call, every page."""
        return self._c._run(_ops.list_tools(self._c._core, include_hidden=include_hidden))


class Deedglass(_Base):
    """The sync client. ``api_key`` defaults to DEEDGLASS_API_KEY, ``base_url`` to DEEDGLASS_SERVER."""

    def __init__(
        self,
        api_key: str | None = None,
        *,
        base_url: str | None = None,
        timeout: float = 30.0,
        max_retries: int = 2,
        api_version: str = API_VERSION,
        on_behalf_of: str | None = None,
        session: str | None = None,
        intent_key: IntentKey | None = None,
        app_info: str | None = None,
        http_client: httpx.Client | None = None,
    ) -> None:
        self._core = Core.create(api_key, base_url, timeout, max_retries, api_version, on_behalf_of, session, app_info)
        self._intent_key = intent_key
        self._who = None
        self._own = http_client is None
        self._http = http_client or httpx.Client()
        self._sleep: Callable[[float], None] = time.sleep
        self._log = logging.getLogger("deedglass")
        self._registry = _Registry()
        self.actions = Actions(self)
        self.deeds = Deeds(self)
        self.approvals = Approvals(self)
        self.tools = Tools(self)

    def _run(self, op: Op[T]) -> T:
        return run_sync(self._http, op, self._sleep)

    def _sign(self, kw: dict[str, Any]) -> dict[str, Any]:
        if self._intent_key is not None and "intent_key" not in kw:
            kw = {**kw, "intent_key": self._intent_key}
        if kw.get("intent_key") is not None:
            kw.setdefault("who", self._cached_whoami)
        return kw

    def close(self) -> None:
        if self._own:
            self._http.close()

    def __enter__(self) -> Deedglass:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def whoami(self) -> Whoami:
        """GET /v1/whoami: this key's org, agent and mode. Cached for 5 minutes."""
        w = self._cached_whoami()
        if w is None:
            w = self._run(_ops.whoami(self._core))
            self._who = (time.monotonic(), w)
        return w

    def act(
        self,
        tool: str,
        args: Mapping[str, Any] | None = None,
        *,
        on_behalf_of: str | None = None,
        session: str | None = None,
        idempotency_key: str | None = None,
        intent_key: IntentKey | None = None,
        wait: float = 10.0,
        wait_for_approval: float | None = None,
        call_id: str | None = None,
    ) -> Action:
        """Runs a connector tool through Deedglass (gateway mode): decided, approved if a rule says so,
        run, witnessed and sealed. A deny raises PolicyDeniedError; a MISMATCH is not an exception."""
        kw = self._sign(
            {
                "on_behalf_of": on_behalf_of,
                "session": session,
                "idempotency_key": idempotency_key,
                "call_id": call_id,
                **({"intent_key": intent_key} if intent_key else {}),
            }
        )
        return self._run(_ops.act(self._core, tool, args, wait=wait, wait_for_approval=wait_for_approval, **kw))

    def submit_proxy(
        self,
        tool: str,
        args: Mapping[str, Any] | None = None,
        *,
        connector_type: str | None = None,
        call_id: str | None = None,
        on_behalf_of: str | None = None,
        session: str | None = None,
    ) -> Action:
        """Proxy mode: Deedglass decides, your code runs (used by wrap_tool and the adapters)."""
        kw = self._sign(
            {"on_behalf_of": on_behalf_of, "session": session, "connector_type": connector_type, "call_id": call_id}
        )
        return self._run(_ops.create_action(self._core, tool, args, mode="proxy", wait=0, **kw))

    def tool(
        self,
        name: str,
        *,
        args_from: Callable[..., Mapping[str, Any]] | None = None,
        on_deny: Literal["raise", "return"] = "raise",
        execute: Literal["gateway", "local"] = "gateway",
        failure_means: Literal["unknown", "not_applied"] = "unknown",
        on_pending: Literal["raise", "wait"] = "raise",
        approval_timeout: float = 600.0,
    ) -> Callable[[F], F]:
        """Decorator. ``execute="gateway"``: the body never runs, the connector does. ``execute="local"``:
        the body runs only once Deedglass authorizes it. ``on_deny="return"`` returns the Gate's
        sentence as a string instead of raising."""

        def deco(fn: F) -> F:
            inner: Callable[..., Any]
            if execute == "local":
                inner = wrap_tool(
                    fn,
                    client=self,
                    tool=name,
                    failure_means=failure_means,
                    on_pending=on_pending,
                    approval_timeout=approval_timeout,
                    args_from=args_from,
                )
            else:
                from .wrap import bind_args

                def inner(*a: Any, **kw: Any) -> Any:
                    call_id = kw.pop("_deedglass_call_id", None)
                    args = dict(args_from(*a, **kw)) if args_from else bind_args(fn, a, kw)
                    act = self.act(
                        name,
                        args,
                        call_id=call_id,
                        wait_for_approval=approval_timeout if on_pending == "wait" else None,
                    )
                    return act.result

            @functools.wraps(fn)
            def wrapped(*a: Any, **kw: Any) -> Any:
                try:
                    return inner(*a, **kw)
                except (PolicyDeniedError, PermissionDeniedError, RateLimitError) as e:
                    if on_deny == "return" and not (isinstance(e, RateLimitError) and e.code == "rate_limited"):
                        return denial_text(e)
                    raise

            return wrapped  # type: ignore[return-value]

        return deco


# --- async ---


class AsyncPage(AsyncIterator[T]):
    def __init__(self, client: AsyncDeedglass, path: str, query: Mapping[str, Any], build: Callable[[Any], T]) -> None:
        self._c, self._path, self._query, self._build = client, path, dict(query), build
        self._buf: list[Any] = []
        self._cursor: str | None = None
        self._done = False

    def __aiter__(self) -> AsyncPage[T]:
        return self

    async def __anext__(self) -> T:
        while not self._buf:
            if self._done:
                raise StopAsyncIteration
            q = {**self._query, "cursor": self._cursor}
            items, more, self._cursor = await self._c._run(_ops.list_page(self._c._core, self._path, q))
            self._buf = list(items)
            self._done = not more
        return self._build(self._buf.pop(0))


class AsyncActions:
    def __init__(self, c: AsyncDeedglass) -> None:
        self._c = c

    async def create(self, tool: str, args: Mapping[str, Any] | None = None, **kw: Any) -> Action:
        return await self._c._run(_ops.create_action(self._c._core, tool, args, **self._c._sign(kw)))

    async def get(self, action_id: str, *, wait: float = 0.0, expand: list[str] | None = None) -> Action:
        return await self._c._run(_ops.get_action(self._c._core, action_id, wait=wait, expand=expand))

    async def wait_until_settled(self, action_id: str, *, timeout: float = 600.0) -> Action:
        return await self._c._run(_ops.wait_until_settled(self._c._core, action_id, timeout=timeout))

    wait = wait_until_settled

    def list(self, *, limit: int | None = None, **filters: Any) -> AsyncPage[Action]:
        return AsyncPage(self._c, "/v1/actions", {"limit": limit, **_ops.filters(filters)}, Action.from_json)

    async def cancel(self, action_id: str) -> Action:
        return await self._c._run(_ops.cancel_action(self._c._core, action_id))

    async def report_result(
        self, action_id: str, result: Mapping[str, Any], *, idempotency_key: str | None = None
    ) -> Action:
        return await self._c._run(_ops.report_result(self._c._core, action_id, result, idempotency_key=idempotency_key))


class AsyncDeeds:
    def __init__(self, c: AsyncDeedglass) -> None:
        self._c = c

    async def get(self, deed_id: str) -> dict[str, Any]:
        return await self._c._run(_ops.get_deed(self._c._core, deed_id))

    def list(self, *, limit: int | None = None, **filters: Any) -> AsyncPage[dict[str, Any]]:
        return AsyncPage(self._c, "/v1/deeds", {"limit": limit, **_ops.filters(filters)}, dict)

    async def export(self, **filters: Any) -> bytes:
        return await self._c._run(_ops.export_deeds(self._c._core, _ops.filters(filters)))


class AsyncApprovals:
    def __init__(self, c: AsyncDeedglass) -> None:
        self._c = c

    async def get(self, approval_id: str) -> Approval:
        return await self._c._run(_ops.get_approval(self._c._core, approval_id))


class AsyncTools:
    def __init__(self, c: AsyncDeedglass) -> None:
        self._c = c

    async def list(self, *, include_hidden: bool = False) -> list[Tool]:
        return await self._c._run(_ops.list_tools(self._c._core, include_hidden=include_hidden))


class AsyncDeedglass(_Base):
    """The async client: the same calls as :class:`Deedglass`, awaited."""

    def __init__(
        self,
        api_key: str | None = None,
        *,
        base_url: str | None = None,
        timeout: float = 30.0,
        max_retries: int = 2,
        api_version: str = API_VERSION,
        on_behalf_of: str | None = None,
        session: str | None = None,
        intent_key: IntentKey | None = None,
        app_info: str | None = None,
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        self._core = Core.create(api_key, base_url, timeout, max_retries, api_version, on_behalf_of, session, app_info)
        self._intent_key = intent_key
        self._who = None
        self._own = http_client is None
        self._http = http_client or httpx.AsyncClient()
        self._sleep: Callable[[float], Awaitable[None]] = asyncio.sleep
        self._log = logging.getLogger("deedglass")
        self._registry = _Registry()
        self.actions = AsyncActions(self)
        self.deeds = AsyncDeeds(self)
        self.approvals = AsyncApprovals(self)
        self.tools = AsyncTools(self)

    async def _run(self, op: Op[T]) -> T:
        return await run_async(self._http, op, self._sleep)

    def _sign(self, kw: dict[str, Any]) -> dict[str, Any]:
        return Deedglass._sign(self, kw)  # type: ignore[arg-type]

    async def aclose(self) -> None:
        if self._own:
            await self._http.aclose()

    async def __aenter__(self) -> AsyncDeedglass:
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self.aclose()

    async def whoami(self) -> Whoami:
        w = self._cached_whoami()
        if w is None:
            w = await self._run(_ops.whoami(self._core))
            self._who = (time.monotonic(), w)
        return w

    async def act(
        self,
        tool: str,
        args: Mapping[str, Any] | None = None,
        *,
        on_behalf_of: str | None = None,
        session: str | None = None,
        idempotency_key: str | None = None,
        intent_key: IntentKey | None = None,
        wait: float = 10.0,
        wait_for_approval: float | None = None,
        call_id: str | None = None,
    ) -> Action:
        kw = self._sign(
            {
                "on_behalf_of": on_behalf_of,
                "session": session,
                "idempotency_key": idempotency_key,
                "call_id": call_id,
                **({"intent_key": intent_key} if intent_key else {}),
            }
        )
        return await self._run(_ops.act(self._core, tool, args, wait=wait, wait_for_approval=wait_for_approval, **kw))

    async def submit_proxy(
        self,
        tool: str,
        args: Mapping[str, Any] | None = None,
        *,
        connector_type: str | None = None,
        call_id: str | None = None,
        on_behalf_of: str | None = None,
        session: str | None = None,
    ) -> Action:
        kw = self._sign(
            {"on_behalf_of": on_behalf_of, "session": session, "connector_type": connector_type, "call_id": call_id}
        )
        return await self._run(_ops.create_action(self._core, tool, args, mode="proxy", wait=0, **kw))

    def tool(
        self,
        name: str,
        *,
        args_from: Callable[..., Mapping[str, Any]] | None = None,
        on_deny: Literal["raise", "return"] = "raise",
        execute: Literal["gateway", "local"] = "gateway",
        failure_means: Literal["unknown", "not_applied"] = "unknown",
        on_pending: Literal["raise", "wait"] = "raise",
        approval_timeout: float = 600.0,
    ) -> Callable[[Callable[..., Awaitable[Any]]], Callable[..., Awaitable[Any]]]:
        """The async decorator: the same options as :meth:`Deedglass.tool`, for ``async def`` tools."""

        def deco(fn: Callable[..., Awaitable[Any]]) -> Callable[..., Awaitable[Any]]:
            if execute == "local":
                inner = wrap_tool_async(
                    fn,
                    client=self,
                    tool=name,
                    failure_means=failure_means,
                    on_pending=on_pending,
                    approval_timeout=approval_timeout,
                    args_from=args_from,
                )
            else:
                from .wrap import bind_args

                async def inner(*a: Any, **kw: Any) -> Any:
                    call_id = kw.pop("_deedglass_call_id", None)
                    args = dict(args_from(*a, **kw)) if args_from else bind_args(fn, a, kw)
                    act = await self.act(
                        name,
                        args,
                        call_id=call_id,
                        wait_for_approval=approval_timeout if on_pending == "wait" else None,
                    )
                    return act.result

            @functools.wraps(fn)
            async def wrapped(*a: Any, **kw: Any) -> Any:
                try:
                    return await inner(*a, **kw)
                except (PolicyDeniedError, PermissionDeniedError, RateLimitError) as e:
                    if on_deny == "return" and not (isinstance(e, RateLimitError) and e.code == "rate_limited"):
                        return denial_text(e)
                    raise

            return wrapped

        return deco
