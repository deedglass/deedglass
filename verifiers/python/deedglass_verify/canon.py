"""The strict JSON profile (DEED_SPEC.md section 6) and JCS, RFC 8785 (section 7.1).

Written from the spec text alone. Bodies and envelopes only ever hold strings, integers,
booleans, null, arrays and objects, so no floating-point rules are needed.
"""

from __future__ import annotations

import json
from typing import Any

MAX_INT = 2**53 - 1
MAX_DEPTH = 16


class StrictError(ValueError):
    """The input fails the strict profile: the verifier refuses it as `malformed`."""


def _int(s: str) -> int:
    if s.startswith("-"):
        raise StrictError("a negative number")
    v = int(s)
    if v > MAX_INT:
        raise StrictError("an integer above 2^53-1")
    return v


def _float(_: str) -> Any:
    raise StrictError("a number with a fraction or an exponent")


def _const(s: str) -> Any:
    raise StrictError(f"{s} is not JSON")


def _pairs(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    out: dict[str, Any] = {}
    for k, v in pairs:
        if k in out:
            raise StrictError(f'a duplicate member "{k}"')
        out[k] = v
    return out


def _walk(v: Any, depth: int) -> None:
    if isinstance(v, str):
        if any(0xD800 <= ord(c) <= 0xDFFF for c in v):
            raise StrictError("a lone UTF-16 surrogate")
        return
    if isinstance(v, (dict, list)):
        if depth > MAX_DEPTH:
            raise StrictError(f"nesting deeper than {MAX_DEPTH} levels")
        items = v.items() if isinstance(v, dict) else enumerate(v)
        for k, x in items:
            if isinstance(k, str):
                _walk(k, depth)
            _walk(x, depth + 1)


def parse_strict(data: bytes) -> Any:
    """Parses bytes under the strict profile, or raises StrictError. Never repairs."""
    try:
        text = data.decode("utf-8")
    except UnicodeDecodeError:
        raise StrictError("invalid UTF-8") from None
    try:
        v = json.loads(
            text,
            parse_int=_int,
            parse_float=_float,
            parse_constant=_const,
            object_pairs_hook=_pairs,
        )
    except StrictError:
        raise
    except (ValueError, RecursionError) as e:
        raise StrictError(str(e).split(":")[0]) from None
    _walk(v, 1)
    return v


_ESC = {'"': '\\"', "\\": "\\\\", "\b": "\\b", "\f": "\\f", "\n": "\\n", "\r": "\\r", "\t": "\\t"}


def _str(s: str) -> str:
    out = ['"']
    for c in s:
        e = _ESC.get(c)
        if e is not None:
            out.append(e)
        elif ord(c) < 0x20:
            out.append("\\u%04x" % ord(c))
        else:
            out.append(c)
    out.append('"')
    return "".join(out)


def _ser(v: Any) -> str:
    if v is None:
        return "null"
    if v is True:
        return "true"
    if v is False:
        return "false"
    if isinstance(v, int):
        return str(v)
    if isinstance(v, str):
        return _str(v)
    if isinstance(v, list):
        return "[" + ",".join(_ser(x) for x in v) + "]"
    if isinstance(v, dict):
        keys = sorted(v, key=lambda k: k.encode("utf-16-be"))
        return "{" + ",".join(_str(k) + ":" + _ser(v[k]) for k in keys) + "}"
    raise TypeError(f"JCS: unsupported type {type(v).__name__}")


def jcs(v: Any) -> bytes:
    """RFC 8785 serialization of a value already parsed under the strict profile."""
    return _ser(v).encode("utf-8")
