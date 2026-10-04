"""Offline deed verification: the same answers as ``deedglass verify`` on every published vector.

Needs the ``crypto`` extra for Ed25519: ``pip install "deedglass[crypto]"``.
"""

from __future__ import annotations

from collections.abc import Iterable, Mapping
from typing import Any

from ._chain import ChainChecker
from ._deed import DeedResult, genesis, verify_envelope
from ._fetch import KeysFetchError, fetch_key_set
from ._jcs import canonical_bytes, canonicalize
from ._keys import KeySet, parse_key_set, unsafe_key
from ._messages_gen import CODES, MESSAGES
from ._report import Failure, Report, Verifier, cannot_determine, message, split_lines
from ._strict import parse_strict
from ._verror import VerifyError

__all__ = [
    "CODES",
    "MESSAGES",
    "ChainChecker",
    "DeedResult",
    "Failure",
    "KeySet",
    "KeysFetchError",
    "Report",
    "Verifier",
    "VerifyError",
    "cannot_determine",
    "canonical_bytes",
    "canonicalize",
    "fetch_key_set",
    "genesis",
    "message",
    "parse_key_set",
    "parse_strict",
    "split_lines",
    "unsafe_key",
    "verify_chain",
    "verify_deed",
]


def _raw(envelope: bytes | str | Mapping[str, Any]) -> bytes | str:
    if isinstance(envelope, Mapping):
        return canonicalize(envelope)
    return envelope


def verify_deed(envelope: bytes | str | Mapping[str, Any], keys: KeySet, *, strict: bool = False) -> DeedResult:
    """Checks one deed on its own. Returns the result, or raises VerifyError.

    A mapping is canonicalized first, so a dict that came from ``json.loads`` verifies like the
    bytes it was read from (duplicate keys and big integers are lost by ``json.loads``: pass the
    bytes when you have them).
    """
    return verify_envelope(_raw(envelope), keys, strict=strict)


def verify_chain(
    lines: Iterable[bytes | str],
    keys: KeySet | None,
    *,
    org: str = "",
    strict: bool = False,
    all: bool = False,
) -> Report:
    """Checks deeds as one org's chain (JSONL lines, in order) and returns the report."""
    v = Verifier(keys, org=org, strict=strict, all=all)
    v.chain(lines)
    return v.report()
