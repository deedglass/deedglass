"""Verification failures: the same fields as Go's deed.VerifyError."""

from __future__ import annotations

from typing import Any


class VerifyError(Exception):
    """One verification failure. ``code`` is one of the codes in DEED_SPEC.md §11."""

    def __init__(self, code: str, **kw: Any) -> None:
        super().__init__(code)
        self.code = code
        self.seq: int = kw.get("seq", 0)
        self.deed_id: str = kw.get("deed_id", "")
        self.field: str = kw.get("field", "")
        self.detail: str = kw.get("detail", "")
        self.key: str = kw.get("key", "")
        self.issued_at: str = kw.get("issued_at", "")
        self.version: int = kw.get("version", 0)
        self.prev: int = kw.get("prev", 0)
        self.want: int = kw.get("want", 0)
        self.org: str = kw.get("org", "")
        self.want_org: str = kw.get("want_org", "")

    def __str__(self) -> str:
        from ._report import message

        return message(self)[0]


def schema_field(field: str, detail: str) -> VerifyError:
    return VerifyError("schema_invalid", field=field, detail=detail)


def malformed(detail: str) -> VerifyError:
    return VerifyError("malformed", detail=detail)


def key_rejected(key: str, detail: str) -> VerifyError:
    return VerifyError("seal_key_rejected", key=key, detail=detail)


def unsupported(v: int) -> VerifyError:
    return VerifyError("unsupported_version", version=v, detail=f"v={v}")
