"""Deedglass for Python: put Deedglass in front of your agent's tools.

Policy decides, people approve, every call is sealed as a deed you can check offline.

    >>> from deedglass import Deedglass
    >>> dg = Deedglass()                      # DEEDGLASS_API_KEY, DEEDGLASS_SERVER
    >>> action = dg.act("postgres.execute", {"sql": "UPDATE public.orders SET status = 'shipped' WHERE id = 42"})
    >>> print(action)
    act_01k6… · postgres.execute · executed · L2 · VERIFIED · deed_01k6…

Importing this package never imports an agent framework; the adapters
(``deedglass.langchain``, ``deedglass.claude_agent_sdk``) import theirs when you import them.
"""

from __future__ import annotations

from typing import Any

from ._client import AsyncDeedglass, Deedglass
from ._errors import (
    APIConnectionError,
    APIError,
    APITimeoutError,
    ApprovalPendingError,
    AuthenticationError,
    BudgetExceededError,
    DeedglassError,
    InvalidRequestError,
    PermissionDeniedError,
    PolicyDeniedError,
    RateLimitError,
    ToolExecutionError,
)
from ._types import Action, Approval, Decision, Execution, Money, NextAction, Tool, Whoami, Witness
from ._version import API_VERSION, __version__
from .intents import IntentKey, args_digest, generate_intent_key, sign_intent
from .wrap import denial_text, pending_text, wrap_tool, wrap_tool_async

__all__ = [
    "API_VERSION",
    "APIConnectionError",
    "APIError",
    "APITimeoutError",
    "Action",
    "Approval",
    "ApprovalPendingError",
    "AsyncDeedglass",
    "AuthenticationError",
    "BudgetExceededError",
    "Decision",
    "Deedglass",
    "DeedglassError",
    "Execution",
    "IntentKey",
    "InvalidRequestError",
    "Money",
    "NextAction",
    "PermissionDeniedError",
    "PolicyDeniedError",
    "RateLimitError",
    "Tool",
    "ToolExecutionError",
    "Whoami",
    "Witness",
    "__version__",
    "args_digest",
    "denial_text",
    "generate_intent_key",
    "pending_text",
    "sign_intent",
    "verify_chain",
    "verify_deed",
    "wrap_tool",
    "wrap_tool_async",
]


def verify_deed(*a: Any, **kw: Any) -> Any:
    """See :func:`deedglass.verify.verify_deed` (needs the crypto extra)."""
    from .verify import verify_deed as f

    return f(*a, **kw)


def verify_chain(*a: Any, **kw: Any) -> Any:
    """See :func:`deedglass.verify.verify_chain` (needs the crypto extra)."""
    from .verify import verify_chain as f

    return f(*a, **kw)
