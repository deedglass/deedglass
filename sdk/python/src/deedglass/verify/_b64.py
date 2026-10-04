"""base64url without padding, strict as Go's base64.RawURLEncoding.Strict()."""

from __future__ import annotations

import base64
import re

_RE = re.compile(r"^[A-Za-z0-9_-]*$")


def decode_b64url(s: str) -> bytes | None:
    """Decodes strict base64url (no padding, zero trailing bits), or returns None."""
    if len(s) % 4 == 1 or not _RE.match(s):
        return None
    try:
        out = base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))
    except ValueError:
        return None
    return out if encode_b64url(out) == s else None


def encode_b64url(b: bytes) -> str:
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode("ascii")
