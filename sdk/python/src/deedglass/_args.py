"""The strict args profile (docs/INTENTS.md §3, Go's intent.ParseStrictArgs) for Python values.

Finite numbers, integers within ±(2^53 − 1), depth at most 32, at most 256 KiB once canonical, and
only JSON types: dict with str keys, list or tuple, str, int, float, bool and None.
"""

from __future__ import annotations

import hashlib
import math
import os
import uuid
from collections.abc import Mapping, Sequence
from dataclasses import asdict, is_dataclass
from datetime import date, datetime, time
from decimal import Decimal
from enum import Enum
from typing import Any

from . import _copy
from ._errors import InvalidRequestError
from .verify._jcs import canonicalize

MAX_ARGS_BYTES = 256 * 1024
MAX_ARGS_DEPTH = 32
MAX_SAFE = 9007199254740991


def _refuse(why: str, code: str = "validation_failed") -> InvalidRequestError:
    return InvalidRequestError(why, type="client_error", code=code, param="args")


def to_json_value(v: Any, _depth: int = 0) -> Any:
    """Convert Python objects (Pydantic models, dataclasses, datetime, UUID, Enum, Decimal) to JSON-safe primitives."""
    if _depth > MAX_ARGS_DEPTH:
        return v
    if v is None or isinstance(v, (bool, int, float, str)):
        return v
    if hasattr(v, "model_dump") and callable(v.model_dump):
        return to_json_value(v.model_dump(), _depth + 1)
    if hasattr(v, "dict") and callable(v.dict) and not isinstance(v, type):
        return to_json_value(v.dict(), _depth + 1)
    if is_dataclass(v) and not isinstance(v, type):
        return to_json_value(asdict(v), _depth + 1)
    if isinstance(v, (datetime, date, time)):
        return v.isoformat()
    if isinstance(v, uuid.UUID):
        return str(v)
    if isinstance(v, Enum):
        return to_json_value(v.value, _depth + 1)
    if isinstance(v, Decimal):
        return float(v) if v.is_finite() else str(v)
    if isinstance(v, os.PathLike):
        return str(v)
    if isinstance(v, (set, frozenset)):
        return [to_json_value(x, _depth + 1) for x in v]
    if isinstance(v, Mapping):
        return {str(k): to_json_value(val, _depth + 1) for k, val in v.items()}
    if isinstance(v, (list, tuple)):
        return [to_json_value(x, _depth + 1) for x in v]
    return v


def check_args(v: Any, depth: int = 0, _seen: set[int] | None = None) -> Any:
    """Checks v against the args profile and returns it as plain JSON, or raises InvalidRequestError."""
    v = to_json_value(v)
    seen = _seen if _seen is not None else set()
    if depth > MAX_ARGS_DEPTH:
        raise _refuse(_copy.args_invalid("they nest deeper than 32 levels"))
    if v is None or isinstance(v, bool):
        return v
    if isinstance(v, str):
        try:
            v.encode("utf-8")
        except UnicodeEncodeError:
            raise _refuse(_copy.args_invalid("a string holds a lone surrogate")) from None
        return v
    if isinstance(v, int):
        if abs(v) > MAX_SAFE:
            raise _refuse(_copy.BIG_INT, "big_int")
        return v
    if isinstance(v, float):
        if not math.isfinite(v):
            raise _refuse(_copy.args_invalid("a number is NaN or infinite"))
        if v.is_integer() and abs(v) > MAX_SAFE and abs(v) < 1e21:
            raise _refuse(_copy.BIG_INT, "big_int")
        return v
    if isinstance(v, (Mapping, list, tuple)):
        if id(v) in seen:
            raise _refuse(_copy.args_invalid("they refer to themselves (a cycle)"))
        seen.add(id(v))
        try:
            if isinstance(v, Mapping):
                out: dict[str, Any] = {}
                for k, x in v.items():
                    if not isinstance(k, str):
                        raise _refuse(_copy.args_invalid(f"a key is a {type(k).__name__}, not a string"))
                    out[k] = check_args(x, depth + 1, seen)
                return out
            assert isinstance(v, Sequence)
            return [check_args(x, depth + 1, seen) for x in v]
        finally:
            seen.discard(id(v))
    raise _refuse(_copy.args_invalid(f"they hold a {type(v).__name__}, which JSON can't carry"))


def canonical_args(args: Any) -> str:
    """JCS of the args after the profile, refusing more than 256 KiB."""
    c = canonicalize(check_args(args))
    if len(c.encode("utf-8")) > MAX_ARGS_BYTES:
        raise _refuse(_copy.args_invalid("they are larger than 256 KiB"))
    return c


def digest(s: str | bytes) -> str:
    return "sha256:" + hashlib.sha256(s.encode("utf-8") if isinstance(s, str) else s).hexdigest()


def args_digest(args: Any) -> str:
    """'sha256:' + hex(SHA-256(JCS(args))) after the args profile: the signed intent's args_digest."""
    return digest(canonical_args(args))
