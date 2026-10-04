"""The closed-schema helpers, a port of internal/deed/tree.go and the patterns of body.go.

Error fields and the order the checks run in are Go's, so every verifier names the same field.
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import Any

from ._verror import VerifyError, schema_field


@dataclass
class Obj:
    m: dict[str, Any]
    path: str


def at(o: Obj, k: str) -> str:
    return k if o.path == "" else f"{o.path}.{k}"


def as_obj(v: Any, path: str) -> Obj:
    if not isinstance(v, dict):
        raise schema_field("(top level)" if path == "" else path, "must be an object")
    return Obj(v, path)


def closed(o: Obj, keys: tuple[str, ...]) -> None:
    """Refuses unknown keys (the first in code point order), then missing keys (in spec order)."""
    unknown = sorted(k for k in o.m if k not in keys)
    if unknown:
        raise schema_field(at(o, unknown[0]), "is not part of the deed schema")
    for k in keys:
        if k not in o.m:
            raise schema_field(at(o, k), "is missing")


def _is_int(v: Any) -> bool:
    return isinstance(v, int) and not isinstance(v, bool)


class Decoder:
    """Accumulates the first error so field reads stay linear (Go's decoder)."""

    def __init__(self) -> None:
        self.err: VerifyError | None = None

    def check(self) -> None:
        if self.err is not None:
            raise self.err

    def s(self, o: Obj, k: str) -> str:
        if self.err is not None:
            return ""
        v = o.m.get(k)
        if not isinstance(v, str):
            self.err = schema_field(at(o, k), "must be a string")
            return ""
        return v

    def ns(self, o: Obj, k: str) -> str | None:
        if self.err is not None:
            return None
        v = o.m.get(k)
        if v is None:
            return None
        if not isinstance(v, str):
            self.err = schema_field(at(o, k), "must be a string or null")
            return None
        return v

    def i(self, o: Obj, k: str) -> int:
        if self.err is not None:
            return 0
        v = o.m.get(k)
        if not isinstance(v, int) or isinstance(v, bool):
            self.err = schema_field(at(o, k), "must be an integer")
            return 0
        return v

    def b(self, o: Obj, k: str) -> bool:
        if self.err is not None:
            return False
        v = o.m.get(k)
        if not isinstance(v, bool):
            self.err = schema_field(at(o, k), "must be true or false")
            return False
        return v

    def obj(self, o: Obj, k: str, keys: tuple[str, ...]) -> Obj | None:
        if self.err is not None:
            return None
        v = o.m.get(k)
        if v is None:
            return None
        if not isinstance(v, dict):
            self.err = schema_field(at(o, k), "must be an object or null")
            return None
        c = Obj(v, at(o, k))
        try:
            closed(c, keys)
        except VerifyError as e:
            self.err = e
            return None
        return c

    def strs(self, o: Obj, k: str) -> list[str]:
        v = o.m.get(k)
        if not isinstance(v, list):
            self.err = schema_field(at(o, k), "must be an array")
            return []
        out: list[str] = []
        for i, s in enumerate(v):
            if not isinstance(s, str):
                self.err = schema_field(f"{at(o, k)}[{i}]", "must be a string")
                return []
            out.append(s)
        return out


# --- patterns (DEED_SPEC.md §4) ---

RE_DIGEST = re.compile(r"^sha256:[0-9a-f]{64}\Z")
RE_SEAL = re.compile(r"^ed25519:[A-Za-z0-9_-]{86}\Z")
RE_TIME = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z\Z", re.ASCII)
RE_HEADER_REF = re.compile(r"^[A-Za-z0-9._~:@/+=-]{1,256}\Z")
RE_METHOD = re.compile(r"^[a-z0-9][a-z0-9._-]{0,63}\Z")
RE_ERROR_CODE = re.compile(r"^[a-z][a-z0-9_]{0,63}\Z")
RE_RULE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}\Z")
RE_ADMIN_ACT = re.compile(r"^[a-z][a-z_]{0,30}\.[a-z][a-z_]{0,30}\Z")
RE_REF_TYPE = re.compile(r"^[a-z][a-z_]{0,31}\Z")
RE_REF_ID = re.compile(r"^[a-z][a-z0-9_-]{0,63}\Z")
RE_CURRENCY = re.compile(r"^[a-z]{3}\Z")

_ID_RES: dict[str, re.Pattern[str]] = {}


def is_id(prefix: str, s: str) -> bool:
    """Contract §3 ids: '<prefix>_' + 26 lowercase Crockford base32 characters, the first 0-7."""
    r = _ID_RES.get(prefix)
    if r is None:
        r = re.compile(rf"^{prefix}_[0-7][0-9a-hjkmnp-tv-z]{{25}}\Z")
        _ID_RES[prefix] = r
    return bool(r.match(s))


def _days_from_civil(y: int, m: int, d: int) -> int:
    """Days since 1970-01-01 in the proleptic Gregorian calendar (any year, as Go and JavaScript)."""
    y -= m <= 2
    era = (y if y >= 0 else y - 399) // 400
    yoe = y - era * 400
    doy = (153 * (m + (-3 if m > 2 else 9)) + 2) // 5 + d - 1
    doe = yoe * 365 + yoe // 4 - yoe // 100 + doy
    return era * 146097 + doe - 719468


def parse_time(s: str) -> int | None:
    """Milliseconds since the epoch for a deed time (exactly YYYY-MM-DDTHH:MM:SS.mmmZ), or None."""
    if not RE_TIME.match(s):
        return None
    year, month, day = int(s[0:4]), int(s[5:7]), int(s[8:10])
    hour, minute, second, ms = int(s[11:13]), int(s[14:16]), int(s[17:19]), int(s[20:23])
    if not 1 <= month <= 12 or hour > 23 or minute > 59 or second > 59:
        return None
    leap = year % 4 == 0 and (year % 100 != 0 or year % 400 == 0)
    days = (31, 29 if leap else 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31)[month - 1]
    if not 1 <= day <= days:
        return None
    return ((_days_from_civil(year, month, day) * 24 + hour) * 60 + minute) * 60_000 + second * 1000 + ms


def plain_text(s: str, lo: int, hi: int, newline: bool) -> bool:
    """Text of lo..hi code points with no control characters (a newline only when allowed)."""
    for ch in s:
        r = ord(ch)
        if (r < 0x20 and not (newline and r == 0x0A)) or r == 0x7F or 0x80 <= r < 0xA0:
            return False
    return lo <= len(s) <= hi
