"""RFC 8785 JSON Canonicalization Scheme (JCS) for Python values.

Object keys sort by UTF-16 code units; strings are escaped as ECMAScript's ``JSON.stringify``
escapes them; numbers use ECMAScript's Number-to-String rules (shortest round-trip digits,
exponent form outside 1e-6 <= |x| < 1e21, -0 written as 0). Deed bodies are integers only, but
signed-intent args may hold fractions, so floats are supported here.
"""

from __future__ import annotations

import math
from collections.abc import Mapping, Sequence
from typing import Any

_ESCAPES = {'"': '\\"', "\\": "\\\\", "\b": "\\b", "\f": "\\f", "\n": "\\n", "\r": "\\r", "\t": "\\t"}


def _string(s: str) -> str:
    out = ['"']
    for c in s:
        e = _ESCAPES.get(c)
        if e is not None:
            out.append(e)
        elif ord(c) < 0x20 or 0xD800 <= ord(c) <= 0xDFFF:
            out.append(f"\\u{ord(c):04x}")
        else:
            out.append(c)
    out.append('"')
    return "".join(out)


def es_number(x: float | int) -> str:
    """ECMAScript Number.prototype.toString for a finite number."""
    if isinstance(x, bool):
        raise TypeError("JCS: a bool is not a number")
    if isinstance(x, int):
        return str(x)
    if not math.isfinite(x):
        raise TypeError("JCS: numbers must be finite")
    if x == 0:
        return "0"
    sign = "-" if x < 0 else ""
    r = repr(abs(x))
    mant, _, exp = r.partition("e")
    e = int(exp) if exp else 0
    intpart, _, frac = mant.partition(".")
    digits = (intpart + frac).lstrip("0")
    # The decimal point sits after len(intpart) digits of intpart+frac, before stripping zeros.
    n = len(intpart) + e - (len(intpart + frac) - len((intpart + frac).lstrip("0")))
    digits = digits.rstrip("0") or "0"
    k = len(digits)
    if k <= n <= 21:
        return sign + digits + "0" * (n - k)
    if 0 < n <= 21:
        return sign + digits[:n] + "." + digits[n:]
    if -6 < n <= 0:
        return sign + "0." + "0" * (-n) + digits
    ex = n - 1
    es = ("+" if ex >= 0 else "-") + str(abs(ex))
    if k == 1:
        return sign + digits + "e" + es
    return sign + digits[0] + "." + digits[1:] + "e" + es


def canonicalize(v: Any) -> str:
    """JCS of a JSON value built from dict, list/tuple, str, int, float, bool and None."""
    if v is None:
        return "null"
    if v is True:
        return "true"
    if v is False:
        return "false"
    if isinstance(v, (int, float)):
        return es_number(v)
    if isinstance(v, str):
        return _string(v)
    if isinstance(v, Mapping):
        keys = sorted(v.keys(), key=lambda k: k.encode("utf-16-be", "surrogatepass"))
        return "{" + ",".join(_string(k) + ":" + canonicalize(v[k]) for k in keys) + "}"
    if isinstance(v, Sequence) and not isinstance(v, (bytes, bytearray)):
        return "[" + ",".join(canonicalize(x) for x in v) + "]"
    raise TypeError(f"JCS: {type(v).__name__} is not a JSON value")


def canonical_bytes(v: Any) -> bytes:
    return canonicalize(v).encode("utf-8", "surrogatepass")
