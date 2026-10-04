"""The objects the Deedglass API answers with (contract §8), as frozen dataclasses.

Every object keeps the server's JSON in ``.raw``, so a field a newer server adds is never lost and
never breaks an older SDK. Timestamps parse to aware datetimes; money stays integer minor units.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field
from datetime import datetime
from typing import Any


def _s(m: Mapping[str, Any], k: str) -> str | None:
    v = m.get(k)
    return v if isinstance(v, str) else None


def _i(m: Mapping[str, Any], k: str) -> int | None:
    v = m.get(k)
    return v if isinstance(v, int) and not isinstance(v, bool) else None


def _m(m: Mapping[str, Any], k: str) -> Mapping[str, Any] | None:
    v = m.get(k)
    return v if isinstance(v, Mapping) else None


def parse_time(s: str | None) -> datetime | None:
    """An RFC 3339 time as an aware datetime, or None."""
    if not s:
        return None
    try:
        return datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return None


def short(v: str | None) -> str:
    """'act_01k6…' for an id: the prefix and four characters, as the brand shows ids in a line."""
    if not v:
        return "—"
    head, _, tail = v.partition("_")
    return f"{head}_{tail[:4]}…" if tail else v


@dataclass(frozen=True, slots=True)
class Ref:
    id: str
    name: str


@dataclass(frozen=True, slots=True)
class Decision:
    outcome: str
    code: str | None
    source: str | None
    rule_id: str | None
    reason: str | None
    message: str | None
    policy: str | None
    policy_version: str | None
    raw: Mapping[str, Any] = field(repr=False)

    @classmethod
    def from_json(cls, m: Mapping[str, Any]) -> Decision:
        return cls(
            _s(m, "outcome") or "",
            _s(m, "code"),
            _s(m, "source"),
            _s(m, "rule_id"),
            _s(m, "reason"),
            _s(m, "message"),
            _s(m, "policy"),
            _s(m, "policy_version"),
            m,
        )


@dataclass(frozen=True, slots=True)
class ActionApproval:
    id: str
    status: str
    expires_at: datetime | None
    decision_reason: str | None
    raw: Mapping[str, Any] = field(repr=False)

    @classmethod
    def from_json(cls, m: Mapping[str, Any]) -> ActionApproval:
        return cls(
            _s(m, "id") or "", _s(m, "status") or "", parse_time(_s(m, "expires_at")), _s(m, "decision_reason"), m
        )


@dataclass(frozen=True, slots=True)
class NextAction:
    type: str
    approval: str | None
    expires_at: datetime | None
    poll_url: str | None
    report_url: str | None
    deadline: datetime | None
    raw: Mapping[str, Any] = field(repr=False)

    @classmethod
    def from_json(cls, m: Mapping[str, Any]) -> NextAction:
        return cls(
            _s(m, "type") or "",
            _s(m, "approval"),
            parse_time(_s(m, "expires_at")),
            _s(m, "poll_url"),
            _s(m, "report_url"),
            parse_time(_s(m, "deadline")),
            m,
        )


@dataclass(frozen=True, slots=True)
class Execution:
    status: str
    started_at: datetime | None
    finished_at: datetime | None
    latency_ms: int | None
    error_code: str | None
    error_message: str | None
    result: Any
    raw: Mapping[str, Any] = field(repr=False)

    @classmethod
    def from_json(cls, m: Mapping[str, Any]) -> Execution:
        return cls(
            _s(m, "status") or "",
            parse_time(_s(m, "started_at")),
            parse_time(_s(m, "finished_at")),
            _i(m, "latency_ms"),
            _s(m, "error_code"),
            _s(m, "error_message"),
            m.get("result"),
            m,
        )


@dataclass(frozen=True, slots=True)
class Reason:
    code: str
    cls: str
    level: str
    message: str

    @classmethod
    def from_json(cls, m: Mapping[str, Any]) -> Reason:
        return cls(_s(m, "code") or "", _s(m, "class") or "", _s(m, "level") or "", _s(m, "message") or "")


@dataclass(frozen=True, slots=True)
class Witness:
    level: str
    verdict: str | None
    method: str | None
    required_level: str | None
    summary: str | None
    reasons: tuple[Reason, ...]
    raw: Mapping[str, Any] = field(repr=False)

    @classmethod
    def from_json(cls, m: Mapping[str, Any]) -> Witness:
        rs = m.get("reasons")
        reasons = tuple(Reason.from_json(r) for r in rs if isinstance(r, Mapping)) if isinstance(rs, list) else ()
        return cls(
            _s(m, "level") or "",
            _s(m, "verdict"),
            _s(m, "method"),
            _s(m, "required_level"),
            _s(m, "summary"),
            reasons,
            m,
        )


@dataclass(frozen=True, slots=True)
class Money:
    amount: int
    currency: str


@dataclass(frozen=True, slots=True)
class Action:
    """The action object. ``str(action)`` is the brand's one line about it."""

    id: str
    object: str
    created_at: datetime | None
    livemode: bool
    mode: str
    tool: str
    connector: str | None
    agent: Ref | None
    state: str
    status: str
    decision: Decision | None
    approval: ActionApproval | None
    execution: Execution | None
    witness: Witness | None
    cost: Money | None
    deed: str | None
    next_action: NextAction | None
    request_id: str | None
    replayed: bool
    raw: Mapping[str, Any] = field(repr=False)

    @property
    def verified(self) -> bool:
        return self.witness is not None and self.witness.verdict == "VERIFIED"

    @property
    def result(self) -> Any:
        return self.execution.result if self.execution else None

    def __str__(self) -> str:
        parts = [short(self.id), self.tool, self.status]
        w = self.witness
        if w is not None and w.verdict:
            parts += [w.level, w.verdict]
        if self.deed:
            parts.append(short(self.deed))
        return " · ".join(parts)

    @classmethod
    def from_json(cls, m: Mapping[str, Any], *, request_id: str | None = None, replayed: bool = False) -> Action:
        ag = _m(m, "agent")
        cost = _m(m, "cost")
        d, ap, ex, wi, na = (_m(m, k) for k in ("decision", "approval", "execution", "witness", "next_action"))
        amount = _i(cost, "amount") if cost else None
        return cls(
            id=_s(m, "id") or "",
            object=_s(m, "object") or "action",
            created_at=parse_time(_s(m, "created_at")),
            livemode=m.get("livemode") is True,
            mode=_s(m, "mode") or "",
            tool=_s(m, "tool") or "",
            connector=_s(m, "connector"),
            agent=Ref(_s(ag, "id") or "", _s(ag, "name") or "") if ag else None,
            state=_s(m, "state") or "",
            status=_s(m, "status") or "",
            decision=Decision.from_json(d) if d else None,
            approval=ActionApproval.from_json(ap) if ap else None,
            execution=Execution.from_json(ex) if ex else None,
            witness=Witness.from_json(wi) if wi else None,
            cost=Money(amount, _s(cost, "currency") or "") if cost and amount is not None else None,
            deed=_s(m, "deed"),
            next_action=NextAction.from_json(na) if na else None,
            request_id=request_id,
            replayed=replayed,
            raw=m,
        )


@dataclass(frozen=True, slots=True)
class Whoami:
    org_id: str
    org_name: str
    agent: Ref | None
    key_mode: str | None
    scopes: tuple[str, ...]
    raw: Mapping[str, Any] = field(repr=False)

    @classmethod
    def from_json(cls, m: Mapping[str, Any]) -> Whoami:
        org = _m(m, "org") or {}
        ag = _m(m, "agent")
        key = _m(m, "key") or {}
        sc = key.get("scopes")
        return cls(
            _s(org, "id") or "",
            _s(org, "name") or "",
            Ref(_s(ag, "id") or "", _s(ag, "name") or "") if ag else None,
            _s(key, "mode"),
            tuple(x for x in sc if isinstance(x, str)) if isinstance(sc, list) else (),
            m,
        )


@dataclass(frozen=True, slots=True)
class Tool:
    name: str
    connector: str
    title: str
    description: str
    side_effect: str
    input_schema: Mapping[str, Any]
    levels: tuple[str, ...]
    raw: Mapping[str, Any] = field(repr=False)

    @classmethod
    def from_json(cls, m: Mapping[str, Any]) -> Tool:
        lv = m.get("levels")
        return cls(
            _s(m, "name") or "",
            _s(m, "connector") or "",
            _s(m, "title") or "",
            _s(m, "description") or "",
            _s(m, "side_effect") or "unknown",
            _m(m, "input_schema") or {},
            tuple(x for x in lv if isinstance(x, str)) if isinstance(lv, list) else (),
            m,
        )


@dataclass(frozen=True, slots=True)
class Approval:
    """GET /v1/approvals/{id}. An agent key sees no approvers, context or ``you`` (contract §8)."""

    id: str
    status: str
    rule_id: str | None
    summary: str | None
    expires_at: datetime | None
    deed: str | None
    raw: Mapping[str, Any] = field(repr=False)

    @classmethod
    def from_json(cls, m: Mapping[str, Any]) -> Approval:
        return cls(
            _s(m, "id") or "",
            _s(m, "status") or "",
            _s(m, "rule_id"),
            _s(m, "summary"),
            parse_time(_s(m, "expires_at")),
            _s(m, "deed"),
            m,
        )
