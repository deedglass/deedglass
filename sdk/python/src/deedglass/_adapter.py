"""What every adapter shares (S32 §3.8): names, decide-before-execute, the model-facing sentences.

Fail closed: if Deedglass can't be reached, or answers anything but ``authorized``, the tool is not
run. ``fail_open=True`` exists only for local experiments and logs a WARNING on every call.
"""

from __future__ import annotations

import asyncio
import logging
import re
from collections.abc import Mapping
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any

from . import _copy, _ops
from ._client import AsyncDeedglass, Deedglass
from ._core import REF
from ._errors import APIConnectionError, DeedglassError
from ._types import Action
from ._version import CLIENT_NAME
from .wrap import _failure_report, _success_report, denial_text, pending_text

log = logging.getLogger("deedglass")
CONNECTOR = re.compile(r"^[a-z][a-z0-9-]{1,31}\Z")
TOOL_PART = re.compile(r"^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}\Z")
WAIT_FOR_APPROVAL = 120.0


def unknown_tool_text(name: str) -> str:
    return f"{name[:80]!r} isn't a tool Deedglass knows, so it was not run. {_copy.NOTHING_RAN}"


@dataclass
class Verdict:
    """'run' (the tool may run; report afterwards), or 'text' (give the model this sentence)."""

    kind: str
    text: str = ""
    action: Action | None = None
    started: str = ""


def _now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


class Governor:
    def __init__(
        self,
        client: Deedglass | AsyncDeedglass,
        *,
        framework: str,
        connector: str = "app",
        name_map: Mapping[str, str] | None = None,
        wait_for_approval: float = WAIT_FOR_APPROVAL,
        fail_open: bool = False,
    ) -> None:
        if not CONNECTOR.match(connector):
            raise ValueError(f"connector {connector!r} must match ^[a-z][a-z0-9-]{{1,31}}$.")
        self.client = client
        self.framework = framework
        self.connector = connector
        self.name_map = dict(name_map or {})
        self.wait_for_approval = wait_for_approval
        self.fail_open = fail_open

    def name(self, framework_name: str) -> str | None:
        """The Deedglass tool name, or None when it can't be one (refused locally, never sent)."""
        n = self.name_map.get(framework_name) or f"{self.connector}.{framework_name}"
        conn, _, tool = n.partition(".")
        if not CONNECTOR.match(conn) or not TOOL_PART.match(tool) or not n.isascii():
            return None
        return n

    def _create(self, tool: str, args: Mapping[str, Any], call_id: str | None, session: str | None = None) -> Any:
        return _ops.create_action(
            self.client._core,
            tool,
            args,
            mode="proxy",
            wait=0,
            call_id=call_id,
            session=session if session and REF.match(session) else None,
            client_name=f"{CLIENT_NAME}/{self.framework}",
            intent_key=self.client._intent_key,
        )

    def _settle(self, a: Action) -> Verdict:
        if a.status == "authorized":
            return Verdict("run", action=a, started=_now())
        if a.status == "pending_approval":
            return Verdict("text", pending_text(a), a)
        return Verdict("text", denial_text(a), a)

    def _refused(self, e: DeedglassError) -> Verdict:
        if isinstance(e, APIConnectionError):
            if self.fail_open:
                log.warning("deedglass: fail_open is on and Deedglass is unreachable; running the tool anyway.")
                return Verdict("run")
            return Verdict("text", _copy.OUTAGE)
        return Verdict("text", denial_text(e), e.action)

    def decide(
        self, framework_name: str, args: Mapping[str, Any], call_id: str | None = None, session: str | None = None
    ) -> Verdict:
        assert isinstance(self.client, Deedglass)
        tool = self.name(framework_name)
        if tool is None:
            return Verdict("text", unknown_tool_text(framework_name))
        try:
            a = self.client._run(self._create(tool, args, call_id, session))
            if a.status == "pending_approval" and self.wait_for_approval > 0:
                a = self.client.actions.wait_until_settled(a.id, timeout=self.wait_for_approval)
        except DeedglassError as e:
            return self._refused(e)
        except Exception:
            log.exception("deedglass: deciding failed; the tool was not run")
            return Verdict("text", _copy.OUTAGE)
        return self._settle(a)

    async def adecide(
        self, framework_name: str, args: Mapping[str, Any], call_id: str | None = None, session: str | None = None
    ) -> Verdict:
        if isinstance(self.client, Deedglass):
            return await asyncio.to_thread(self.decide, framework_name, args, call_id, session)
        tool = self.name(framework_name)
        if tool is None:
            return Verdict("text", unknown_tool_text(framework_name))
        try:
            a = await self.client._run(self._create(tool, args, call_id, session))
            if a.status == "pending_approval" and self.wait_for_approval > 0:
                a = await self.client.actions.wait_until_settled(a.id, timeout=self.wait_for_approval)
        except DeedglassError as e:
            return self._refused(e)
        except Exception:
            log.exception("deedglass: deciding failed; the tool was not run")
            return Verdict("text", _copy.OUTAGE)
        return self._settle(a)

    def _body(self, v: Verdict, value: Any, err: BaseException | None) -> dict[str, Any]:
        if err is not None:
            return _failure_report("unknown", v.started, err)
        return _success_report(value, v.started, None)

    def report(self, v: Verdict, value: Any = None, err: BaseException | None = None) -> None:
        if v.action is None:
            return
        assert isinstance(self.client, Deedglass)
        try:
            self.client.actions.report_result(v.action.id, self._body(v, value, err))
        except DeedglassError:
            log.warning(_copy.report_lost(v.action.id))

    async def areport(self, v: Verdict, value: Any = None, err: BaseException | None = None) -> None:
        if v.action is None:
            return
        if isinstance(self.client, Deedglass):
            await asyncio.to_thread(self.report, v, value, err)
            return
        try:
            await self.client.actions.report_result(v.action.id, self._body(v, value, err))
        except DeedglassError:
            log.warning(_copy.report_lost(v.action.id))
