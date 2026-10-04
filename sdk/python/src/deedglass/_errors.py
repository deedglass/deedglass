"""Typed errors (S32 §3.4), mirroring the TypeScript SDK's classes (S31).

Classes are chosen by the envelope's ``type`` first, then its ``code``; an unknown code still
produces a :class:`DeedglassError`, so an older SDK keeps working against a newer server. Server
messages pass through verbatim. Python's builtins ``PermissionError``, ``ConnectionError`` and
``TimeoutError`` are never shadowed: their counterparts are :class:`PermissionDeniedError`,
:class:`APIConnectionError` and :class:`APITimeoutError`.
"""

from __future__ import annotations

from collections.abc import Mapping
from typing import TYPE_CHECKING, Any

from ._errors_gen import CODES

if TYPE_CHECKING:
    from ._types import Action

__all__ = [
    "APIConnectionError",
    "APIError",
    "APITimeoutError",
    "ApprovalPendingError",
    "AuthenticationError",
    "BudgetExceededError",
    "DeedglassError",
    "InvalidRequestError",
    "PermissionDeniedError",
    "PolicyDeniedError",
    "RateLimitError",
    "ToolExecutionError",
    "class_for",
    "from_envelope",
]


class DeedglassError(Exception):
    """Every error the SDK raises. ``str(err)`` is the sentence plus the request id."""

    def __init__(
        self,
        message: str,
        *,
        type: str = "client_error",
        code: str = "client_error",
        param: str | None = None,
        status: int | None = None,
        request_id: str | None = None,
        doc_url: str | None = None,
        action: Action | None = None,
        raw: Mapping[str, Any] | None = None,
    ) -> None:
        super().__init__(message)
        self.message = message
        self.type = type
        self.code = code
        self.param = param
        self.status = status
        self.request_id = request_id
        self.doc_url = doc_url
        self.action = action
        self.raw: Mapping[str, Any] = raw or {}

    def __str__(self) -> str:
        return f"{self.message} ({self.request_id})" if self.request_id else self.message

    def __repr__(self) -> str:
        return f"{type(self).__name__}(code={self.code!r}, status={self.status!r}, request_id={self.request_id!r})"


class AuthenticationError(DeedglassError):
    """invalid_api_key, api_key_revoked, api_key_expired."""


class PermissionDeniedError(DeedglassError):
    """agent_stopped, agent_suspended, org_stopped, insufficient_scope, intent_*."""


class PolicyDeniedError(DeedglassError):
    """policy_denied (and a recorded tool_unknown): the message is the policy's sentence verbatim."""

    @property
    def rule_id(self) -> str | None:
        d = self.action.decision if self.action else None
        return d.rule_id if d else None

    @property
    def reason(self) -> str | None:
        d = self.action.decision if self.action else None
        return d.reason if d else None


class RateLimitError(DeedglassError):
    """429 rate_limited, after the retries the SDK allows. ``retry_after`` is in seconds."""

    def __init__(self, message: str, *, retry_after: float = 0.0, **kw: Any) -> None:
        super().__init__(message, **kw)
        self.retry_after = retry_after


class BudgetExceededError(RateLimitError):
    """429 budget_exceeded: a policy limit that resets on its own schedule. Never retried."""


class InvalidRequestError(DeedglassError):
    """validation_failed, not_found, conflict, idempotency_conflict, approval_expired, …"""


class APIError(DeedglassError):
    """5xx, internal_error, or an answer the SDK couldn't read."""


class APIConnectionError(DeedglassError):
    """The network failed before an answer arrived. ``__cause__`` is the transport error."""


class APITimeoutError(APIConnectionError):
    """No answer within the timeout. The action may still be running."""

    def __init__(self, message: str, *, action_id: str | None = None, **kw: Any) -> None:
        super().__init__(message, **kw)
        self.action_id = action_id


class ApprovalPendingError(DeedglassError):
    """A person must approve the action first (``wrap_tool`` and ``Deedglass.tool`` with on_pending='raise')."""

    def __init__(self, message: str, *, approval_id: str, action_id: str, expires_at: str, **kw: Any) -> None:
        super().__init__(message, **kw)
        self.approval_id = approval_id
        self.action_id = action_id
        self.expires_at = expires_at


class ToolExecutionError(DeedglassError):
    """Your function raised after Deedglass authorized it. ``__cause__`` is the original error."""

    def __init__(self, message: str, *, action_id: str, **kw: Any) -> None:
        super().__init__(message, **kw)
        self.action_id = action_id


_BY_TYPE: dict[str, type[DeedglassError]] = {
    "authentication_error": AuthenticationError,
    "permission_error": PermissionDeniedError,
    "policy_error": PolicyDeniedError,
    "rate_limit_error": RateLimitError,
    "invalid_request_error": InvalidRequestError,
    "api_error": APIError,
}


def class_for(type_: str, code: str, *, has_action: bool = False) -> type[DeedglassError]:
    """The class for an envelope's type and code (the same rule as the TypeScript SDK's fromEnvelope)."""
    if type_ == "rate_limit_error":
        return BudgetExceededError if code == "budget_exceeded" else RateLimitError
    if type_ == "invalid_request_error" and code == "tool_unknown" and has_action:
        return PolicyDeniedError
    return _BY_TYPE.get(type_, DeedglassError)


def from_envelope(
    status: int,
    body: Any,
    request_id: str | None,
    retry_after: float,
    action: Action | None = None,
) -> DeedglassError:
    """The typed error for an API error envelope."""
    e = body.get("error") if isinstance(body, Mapping) else None
    e = e if isinstance(e, Mapping) else {}

    def s(k: str) -> str | None:
        v = e.get(k)
        return v if isinstance(v, str) else None

    code = s("code") or ("internal_error" if status >= 500 else "unknown_error")
    known = CODES.get(code)
    type_ = s("type") or (known[1] if known else ("api_error" if status >= 500 else "invalid_request_error"))
    cls = class_for(type_, code, has_action=action is not None)
    kw: dict[str, Any] = {
        "type": type_,
        "code": code,
        "param": s("param"),
        "status": status,
        "request_id": s("request_id") or request_id,
        "doc_url": s("doc_url"),
        "action": action,
        "raw": body if isinstance(body, Mapping) else {},
    }
    message = s("message") or f"Deedglass answered {status}."
    if issubclass(cls, RateLimitError):
        return cls(message, retry_after=retry_after, **kw)
    return cls(message, **kw)
