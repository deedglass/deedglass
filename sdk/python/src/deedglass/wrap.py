"""wrap_tool and the tool decorator (S32 §3.6), mirroring S31's wrapTool.

Edge mode (``execute="local"``): Deedglass decides in proxy mode; your function runs only after the
action is authorized, at most once per action; the outcome is reported and sealed (L0,
edge-reported). A raised error means "it may have run" unless you say ``failure_means="not_applied"``.
Gateway mode (``execute="gateway"``): the function body is a typed signature only; ``act`` runs the
connector's tool and the result comes back.
"""

from __future__ import annotations

import asyncio
import inspect
import json
import threading
from collections.abc import Awaitable, Callable, Mapping
from datetime import datetime, timezone
from typing import TYPE_CHECKING, Any, Literal, TypeVar, cast

from . import _copy
from ._args import args_digest, canonical_args, to_json_value
from ._core import TOOL_NAME
from ._errors import (
    ApprovalPendingError,
    DeedglassError,
    InvalidRequestError,
    PermissionDeniedError,
    PolicyDeniedError,
    ToolExecutionError,
)
from ._scrub import scrub
from ._types import Action
from .verify._jcs import canonicalize

if TYPE_CHECKING:
    from ._client import AsyncDeedglass, Deedglass

F = TypeVar("F", bound=Callable[..., Any])
FailureMeans = Literal["unknown", "not_applied"]
MAX_RESULT_BYTES = 256 * 1024
MAX_TEXT_RESULT = 65_536
REPORT_ATTEMPTS = 5

__all__ = ["denial_text", "pending_text", "wrap_tool", "wrap_tool_async"]


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def clock(t: datetime | None) -> str:
    """'10:42 UTC' from a time."""
    if t is None:
        return "its expiry"
    return t.astimezone(timezone.utc).strftime("%H:%M UTC")


def denial_text(err: DeedglassError | Action) -> str:
    """The Gate's sentence for a refusal, then 'Nothing was run.' (Stop gets its own sentence)."""
    if isinstance(err, Action):
        a = err
        if a.approval and a.approval.decision_reason:
            msg = f"A person denied this action: {a.approval.decision_reason}"
        else:
            msg = (a.decision.message if a.decision else None) or "Deedglass refused this action."
    else:
        if err.code in ("agent_stopped", "org_stopped"):
            return _copy.STOPPED
        d = err.action.decision if err.action else None
        msg = (d.message if d else None) or err.message
    msg = msg.strip()
    if msg.endswith(_copy.NOTHING_RAN):
        return msg
    return f"{msg.rstrip('.')}. {_copy.NOTHING_RAN}"


def pending_text(a: Action) -> str:
    """What a model reads while a person decides: where to look, and that it should try again later."""
    apr = (a.approval.id if a.approval else None) or (a.next_action.approval if a.next_action else None) or "apr_…"
    return _copy.approval_waiting(apr)


def as_json(v: Any, _depth: int = 0) -> Any:
    """The JSON value of v, or a sentinel _NOT_JSON when v isn't plain JSON."""
    try:
        return json.loads(json.dumps(to_json_value(v), allow_nan=False))
    except (TypeError, ValueError, RecursionError):
        return _NOT_JSON


_NOT_JSON = object()


def _text_of(r: Any) -> str:
    try:
        return str(r)[:MAX_TEXT_RESULT]
    except Exception:
        return object.__repr__(r)[:MAX_TEXT_RESULT]


def result_part(v: Any) -> dict[str, Any]:
    """The JSON up to 256 KiB, else only its digest."""
    c = canonicalize(v)
    if len(c.encode("utf-8")) <= MAX_RESULT_BYTES:
        return {"result": v}
    from ._args import digest

    return {"result_digest": digest(c)}


def bind_args(fn: Callable[..., Any], a: tuple[Any, ...], kw: Mapping[str, Any]) -> dict[str, Any]:
    """The call's bound arguments as a JSON object; a non-JSON value raises TypeError naming it."""
    sig = inspect.signature(fn)
    ba = sig.bind(*a, **kw)
    ba.apply_defaults()
    out: dict[str, Any] = {}
    for name, v in ba.arguments.items():
        p = sig.parameters[name]
        if p.kind is inspect.Parameter.VAR_KEYWORD:
            vals = dict(v)
        elif p.kind is inspect.Parameter.VAR_POSITIONAL:
            vals = {name: list(v)}
        else:
            vals = {name: v}
        for k, x in vals.items():
            j = as_json(x)
            if j is _NOT_JSON:
                raise TypeError(f"Argument {k!r} can't be sent to Deedglass: it isn't JSON ({type(x).__name__}).")
            out[k] = j
    return out


def _denied_error(a: Action) -> DeedglassError:
    if a.status == "expired":
        apr = a.approval.id if a.approval else ""
        return InvalidRequestError(
            f"Approval {apr} expired before a person decided. Nothing was run.".replace("  ", " "),
            type="invalid_request_error",
            code="approval_expired",
            action=a,
            request_id=a.request_id,
        )
    if a.status == "cancelled":
        return PermissionDeniedError(
            "The action was cancelled before it ran. Nothing was run.",
            type="permission_error",
            code="cancelled",
            action=a,
            request_id=a.request_id,
        )
    return PolicyDeniedError(
        denial_text(a), type="policy_error", code="policy_denied", action=a, request_id=a.request_id
    )


def _pending_error(a: Action) -> ApprovalPendingError:
    apr = (a.approval.id if a.approval else None) or (a.next_action.approval if a.next_action else None) or ""
    exp_t = (a.approval.expires_at if a.approval else None) or (a.next_action.expires_at if a.next_action else None)
    msg = _copy.approval_pending(apr, clock(exp_t))
    if a.decision and a.decision.message:
        msg = f"{a.decision.message.rstrip('.')}. {msg}"
    return ApprovalPendingError(
        msg,
        type="policy_error",
        code="approval_required",
        action=a,
        request_id=a.request_id,
        approval_id=apr,
        action_id=a.id,
        expires_at=exp_t.isoformat() if exp_t else "",
    )


class _Registry:
    """The pending registry and per-action locks of one client (S31's, keyed the same way)."""

    def __init__(self) -> None:
        self.pending: dict[str, str] = {}
        self.locks: dict[str, threading.Lock] = {}
        self.done: dict[str, Any] = {}
        self.mu = threading.Lock()

    def lock(self, action_id: str) -> threading.Lock:
        with self.mu:
            return self.locks.setdefault(action_id, threading.Lock())


def _validate(tool: str) -> None:
    if not isinstance(tool, str) or len(tool) > 200 or not TOOL_NAME.match(tool):
        raise InvalidRequestError(_copy.bad_tool_name(str(tool)), code="validation_failed", param="tool")


def _failure_report(failure_means: FailureMeans, started: str, err: BaseException) -> dict[str, Any]:
    return {
        "status": "failed" if failure_means == "not_applied" else "unknown",
        "started_at": started,
        "finished_at": _now(),
        "error_code": "tool_error",
        "error_message": scrub(str(err) or "The function raised."),
    }


def _success_report(value: Any, started: str, to_result: Callable[[Any], Any] | None) -> dict[str, Any]:
    try:
        j = to_result(value) if to_result else as_json(value)
    except Exception:
        j = _NOT_JSON
    if j is _NOT_JSON or (j is None and value is not None):
        j = {"text": _text_of(value)}
    return {"status": "succeeded", "started_at": started, "finished_at": _now(), **result_part(j)}


def wrap_tool(
    fn: F,
    *,
    client: Deedglass,
    tool: str,
    failure_means: FailureMeans = "unknown",
    on_pending: Literal["raise", "wait"] = "raise",
    approval_timeout: float = 600.0,
    to_result: Callable[[Any], Any] | None = None,
    on_sealed: Callable[[Action], None] | None = None,
    connector_type: str | None = None,
    args_from: Callable[..., Mapping[str, Any]] | None = None,
) -> F:
    """Puts Deedglass in front of a sync function (proxy mode)."""
    _validate(tool)
    reg: _Registry = client._registry

    def report(a: Action, r: dict[str, Any]) -> None:
        for i in range(REPORT_ATTEMPTS):
            try:
                sealed = client.actions.report_result(a.id, r)
                if on_sealed:
                    on_sealed(sealed)
                return
            except DeedglassError as e:
                if e.status == 409:
                    return
                if e.status is not None and e.status < 500 and e.status not in (408, 429):
                    break
                client._sleep(min(4.0, 0.25 * 2**i))
        client._log.warning(_copy.report_lost(a.id))

    def run(a: Action, a_: tuple[Any, ...], kw: Mapping[str, Any]) -> Any:
        with reg.lock(a.id):
            if a.id in reg.done:
                return reg.done[a.id]
            started = _now()
            try:
                value = fn(*a_, **kw)
            except Exception as err:
                report(a, _failure_report(failure_means, started, err))
                msg = (
                    _copy.tool_failed_not_applied(tool)
                    if failure_means == "not_applied"
                    else _copy.tool_execution(tool)
                )
                raise ToolExecutionError(msg, code="tool_error", action_id=a.id) from err
            report(a, _success_report(value, started, to_result))
            reg.done[a.id] = value
            return value

    def decide(a: Action, reg_key: str, a_: tuple[Any, ...], kw: Mapping[str, Any]) -> Any:
        if a.status == "authorized":
            try:
                return run(a, a_, kw)
            finally:
                reg.pending.pop(reg_key, None)
        if a.status == "pending_approval":
            reg.pending[reg_key] = a.id
            if on_pending == "wait":
                settled = client.actions.wait_until_settled(a.id, timeout=approval_timeout)
                if settled.status == "pending_approval":
                    raise _pending_error(settled)
                return decide(settled, reg_key, a_, kw)
            raise _pending_error(a)
        reg.pending.pop(reg_key, None)
        if a.status in ("denied", "expired", "cancelled"):
            raise _denied_error(a)
        raise InvalidRequestError(
            f"Deedglass answered status {a.status} for a proxy action. Nothing was run.",
            code="unexpected_status",
            action=a,
        )

    def wrapped(*a_: Any, **kw: Any) -> Any:
        call_id = kw.pop("_deedglass_call_id", None)
        args = dict(args_from(*a_, **kw)) if args_from else bind_args(fn, a_, kw)
        canonical_args(args)
        reg_key = f"{tool}\n{args_digest(args)}\n{client._core.on_behalf_of or ''}"
        pid = reg.pending.get(reg_key)
        if pid:
            a = client.actions.get(pid)
            if a.status not in ("executed", "failed"):
                return decide(a, reg_key, a_, kw)
            reg.pending.pop(reg_key, None)
        a = client.submit_proxy(tool, args, connector_type=connector_type, call_id=call_id)
        return decide(a, reg_key, a_, kw)

    wrapped.__name__ = getattr(fn, "__name__", "tool")
    wrapped.__doc__ = fn.__doc__
    wrapped.__wrapped__ = fn  # type: ignore[attr-defined]
    return cast(F, wrapped)


def wrap_tool_async(
    fn: Callable[..., Awaitable[Any]],
    *,
    client: AsyncDeedglass,
    tool: str,
    failure_means: FailureMeans = "unknown",
    on_pending: Literal["raise", "wait"] = "raise",
    approval_timeout: float = 600.0,
    to_result: Callable[[Any], Any] | None = None,
    on_sealed: Callable[[Action], None] | None = None,
    connector_type: str | None = None,
    args_from: Callable[..., Mapping[str, Any]] | None = None,
) -> Callable[..., Awaitable[Any]]:
    """Puts Deedglass in front of an async function (proxy mode)."""
    _validate(tool)
    reg: _Registry = client._registry
    alocks: dict[str, asyncio.Lock] = {}

    async def report(a: Action, r: dict[str, Any]) -> None:
        for i in range(REPORT_ATTEMPTS):
            try:
                sealed = await client.actions.report_result(a.id, r)
                if on_sealed:
                    on_sealed(sealed)
                return
            except DeedglassError as e:
                if e.status == 409:
                    return
                if e.status is not None and e.status < 500 and e.status not in (408, 429):
                    break
                await client._sleep(min(4.0, 0.25 * 2**i))
        client._log.warning(_copy.report_lost(a.id))

    async def run(a: Action, a_: tuple[Any, ...], kw: Mapping[str, Any]) -> Any:
        lk = alocks.setdefault(a.id, asyncio.Lock())
        async with lk:
            if a.id in reg.done:
                return reg.done[a.id]
            started = _now()
            try:
                value = await fn(*a_, **kw)
            except Exception as err:
                await report(a, _failure_report(failure_means, started, err))
                msg = (
                    _copy.tool_failed_not_applied(tool)
                    if failure_means == "not_applied"
                    else _copy.tool_execution(tool)
                )
                raise ToolExecutionError(msg, code="tool_error", action_id=a.id) from err
            await report(a, _success_report(value, started, to_result))
            reg.done[a.id] = value
            return value

    async def decide(a: Action, reg_key: str, a_: tuple[Any, ...], kw: Mapping[str, Any]) -> Any:
        if a.status == "authorized":
            try:
                return await run(a, a_, kw)
            finally:
                reg.pending.pop(reg_key, None)
        if a.status == "pending_approval":
            reg.pending[reg_key] = a.id
            if on_pending == "wait":
                settled = await client.actions.wait_until_settled(a.id, timeout=approval_timeout)
                if settled.status == "pending_approval":
                    raise _pending_error(settled)
                return await decide(settled, reg_key, a_, kw)
            raise _pending_error(a)
        reg.pending.pop(reg_key, None)
        if a.status in ("denied", "expired", "cancelled"):
            raise _denied_error(a)
        raise InvalidRequestError(
            f"Deedglass answered status {a.status} for a proxy action. Nothing was run.",
            code="unexpected_status",
            action=a,
        )

    async def wrapped(*a_: Any, **kw: Any) -> Any:
        call_id = kw.pop("_deedglass_call_id", None)
        args = dict(args_from(*a_, **kw)) if args_from else bind_args(fn, a_, kw)
        canonical_args(args)
        reg_key = f"{tool}\n{args_digest(args)}\n{client._core.on_behalf_of or ''}"
        pid = reg.pending.get(reg_key)
        if pid:
            a = await client.actions.get(pid)
            if a.status not in ("executed", "failed"):
                return await decide(a, reg_key, a_, kw)
            reg.pending.pop(reg_key, None)
        a = await client.submit_proxy(tool, args, connector_type=connector_type, call_id=call_id)
        return await decide(a, reg_key, a_, kw)

    wrapped.__name__ = getattr(fn, "__name__", "tool")
    wrapped.__doc__ = fn.__doc__
    return wrapped
