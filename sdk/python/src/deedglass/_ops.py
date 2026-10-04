"""The operations both clients drive (S32 §3.2, §3.5): pure generators over :mod:`._core`.

Each yields requests and sleeps and returns typed results, so the sync and async clients share one
implementation of every call, the wait loop and pagination.
"""

from __future__ import annotations

import re
import time
from collections.abc import Callable, Mapping
from typing import Any

from . import _copy
from ._args import canonical_args, check_args
from ._core import CALL_ID, REF, TOOL_NAME, Core, Meta, Op, new_idempotency_key
from ._errors import InvalidRequestError
from ._types import Action, Approval, Tool, Whoami
from ._version import CLIENT_NAME, __version__
from .intents import IntentKey, sign_intent

ACTION_ID = re.compile(r"^act_[0-7][0-9a-hjkmnp-tv-z]{25}\Z")
DEED_ID = re.compile(r"^deed_[0-7][0-9a-hjkmnp-tv-z]{25}\Z")
APPROVAL_ID = re.compile(r"^apr_[0-7][0-9a-hjkmnp-tv-z]{25}\Z")
SETTLED = frozenset({"denied", "executed", "failed", "expired", "cancelled", "authorized"})
MAX_WAIT = 25.0


def _id(pattern: re.Pattern[str], v: str, what: str) -> str:
    if not isinstance(v, str) or not pattern.match(v):
        raise TypeError(f"{str(v)[:40]!r} isn't {what}.")
    return v


def action_from(data: Any, meta: Meta) -> Action:
    return Action.from_json(data, request_id=meta.request_id, replayed=meta.replayed)


def whoami(core: Core) -> Op[Whoami]:
    data, _ = yield from core.request("GET", "/v1/whoami")
    return Whoami.from_json(data)


def create_action(
    core: Core,
    tool: str,
    args: Mapping[str, Any] | None,
    *,
    mode: str = "gateway",
    wait: float | None = 10.0,
    idempotency_key: str | None = None,
    on_behalf_of: str | None = None,
    session: str | None = None,
    connector_type: str | None = None,
    call_id: str | None = None,
    client_name: str = CLIENT_NAME,
    intent_key: IntentKey | None = None,
    who: Callable[[], Whoami | None] | None = None,
) -> Op[Action]:
    """POST /v1/actions. The Idempotency-Key is made once here, so every retry reuses it."""
    if not isinstance(tool, str) or len(tool) > 200 or not TOOL_NAME.match(tool):
        raise InvalidRequestError(_copy.bad_tool_name(str(tool)), code="validation_failed", param="tool")
    checked = check_args({} if args is None else args)
    if not isinstance(checked, dict):
        raise InvalidRequestError(
            _copy.args_invalid("they must be a JSON object"), code="validation_failed", param="args"
        )
    canonical_args(checked)
    for name, v in (("on_behalf_of", on_behalf_of), ("session", session)):
        if v is not None and not REF.match(v):
            raise InvalidRequestError(_copy.bad_ref(name), code="validation_failed", param=name)
    key = idempotency_key or new_idempotency_key()
    w = max(0.0, min(MAX_WAIT, wait if wait is not None else 10.0))
    context: dict[str, Any] = {"client": {"name": client_name, "version": __version__}, "transport": "sdk"}
    if call_id is not None and CALL_ID.match(call_id):
        context["call_id"] = call_id
    body: dict[str, Any] = {"tool": tool, "args": checked, "mode": mode, "wait": w, "context": context}
    if mode == "proxy":
        body["connector_type"] = connector_type or "sdk"
    headers: dict[str, str | None] = {"Deedglass-On-Behalf-Of": on_behalf_of, "Deedglass-Session": session}
    if intent_key is not None:
        org, agent = intent_key.org, intent_key.agent
        if not org or not agent:
            cached = who() if who else None
            me = cached if cached is not None else (yield from whoami(core))
            org = org or me.org_id
            agent = agent or (me.agent.id if me.agent else "")
        headers["Deedglass-Intent"] = sign_intent(
            intent_key,
            org=org,
            agent=agent,
            tool=tool,
            args=checked,
            on_behalf_of=on_behalf_of or core.on_behalf_of,
            session=session or core.session,
        ).header
    data, meta = yield from core.request(
        "POST", "/v1/actions", body=body, headers=headers, idempotency_key=key, read_timeout=w + 30.0
    )
    return action_from(data, meta)


def get_action(core: Core, action_id: str, *, wait: float = 0.0, expand: list[str] | None = None) -> Op[Action]:
    _id(ACTION_ID, action_id, "an action id (act_…)")
    w = max(0.0, min(MAX_WAIT, wait))
    query: dict[str, Any] = {}
    if w > 0:
        query["wait"] = f"{w:g}"
    if expand:
        query["expand[]"] = list(expand)
    data, meta = yield from core.request(
        "GET", f"/v1/actions/{action_id}", query=query, read_timeout=(w + 30.0) if w else None, action_id=action_id
    )
    return action_from(data, meta)


def wait_until_settled(
    core: Core, action_id: str, *, timeout: float = 600.0, clock: Callable[[], float] = time.monotonic
) -> Op[Action]:
    """Long-polls GET ?wait=25 until the action is settled (anything but processing or pending_approval)."""
    deadline = clock() + timeout
    while True:
        left = deadline - clock()
        a = yield from get_action(core, action_id, wait=max(0.0, min(MAX_WAIT, left)))
        if a.status in SETTLED or clock() >= deadline:
            return a


def act(
    core: Core,
    tool: str,
    args: Mapping[str, Any] | None,
    *,
    wait: float = 10.0,
    wait_for_approval: float | None = None,
    clock: Callable[[], float] = time.monotonic,
    **kw: Any,
) -> Op[Action]:
    """The one-call helper: create in gateway mode, then wait for a person when asked to."""
    a = yield from create_action(core, tool, args, mode="gateway", wait=wait, **kw)
    if a.status == "pending_approval" and wait_for_approval:
        a = yield from wait_until_settled(core, a.id, timeout=wait_for_approval, clock=clock)
    return a


def cancel_action(core: Core, action_id: str) -> Op[Action]:
    _id(ACTION_ID, action_id, "an action id (act_…)")
    data, meta = yield from core.request("POST", f"/v1/actions/{action_id}/cancel", action_id=action_id)
    return action_from(data, meta)


def report_result(
    core: Core,
    action_id: str,
    result: Mapping[str, Any],
    *,
    idempotency_key: str | None = None,
) -> Op[Action]:
    """POST /v1/actions/{id}/result: proxy mode, after your code ran. The key is 'sdkr_' + the action id."""
    _id(ACTION_ID, action_id, "an action id (act_…)")
    data, meta = yield from core.request(
        "POST",
        f"/v1/actions/{action_id}/result",
        body=dict(result),
        idempotency_key=idempotency_key or "sdkr_" + action_id,
        action_id=action_id,
    )
    return action_from(data, meta)


def list_page(core: Core, path: str, query: Mapping[str, Any]) -> Op[tuple[list[Any], bool, str | None]]:
    data, _ = yield from core.request("GET", path, query=query)
    items = data.get("data") if isinstance(data, dict) else None
    nxt = data.get("next_cursor") if isinstance(data, dict) else None
    has_more = isinstance(data, dict) and data.get("has_more") is True and isinstance(nxt, str) and nxt != ""
    return (items if isinstance(items, list) else []), has_more, (nxt if isinstance(nxt, str) else None)


def filters(kw: Mapping[str, Any]) -> dict[str, Any]:
    """Python names to query names: created_at__gte → created_at[gte]."""
    out: dict[str, Any] = {}
    for k, v in kw.items():
        if v is None:
            continue
        name, _, op = k.partition("__")
        out[f"{name}[{op}]" if op else name] = v
    return out


def list_tools(core: Core, *, include_hidden: bool = False) -> Op[list[Tool]]:
    out: list[Tool] = []
    cursor: str | None = None
    for _ in range(100):
        items, more, cursor = yield from list_page(
            core, "/v1/tools", {"include_hidden": "true" if include_hidden else None, "cursor": cursor}
        )
        out += [Tool.from_json(t) for t in items if isinstance(t, dict)]
        if not more:
            break
    return out


def get_deed(core: Core, deed_id: str) -> Op[dict[str, Any]]:
    _id(DEED_ID, deed_id, "a deed id (deed_…)")
    data, _ = yield from core.request("GET", f"/v1/deeds/{deed_id}")
    return data  # type: ignore[no-any-return]


def export_deeds(core: Core, query: Mapping[str, Any]) -> Op[bytes]:
    data, _ = yield from core.request("GET", "/v1/deeds/export", query=query, raw=True, read_timeout=300.0)
    return data  # type: ignore[no-any-return]


def get_approval(core: Core, approval_id: str) -> Op[Approval]:
    _id(APPROVAL_ID, approval_id, "an approval id (apr_…)")
    data, _ = yield from core.request("GET", f"/v1/approvals/{approval_id}")
    return Approval.from_json(data)
