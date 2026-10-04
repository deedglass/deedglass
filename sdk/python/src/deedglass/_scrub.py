"""A port of Go's vault.Scrub (internal/vault/redact.go): secrets removed by their shape before an
error message leaves this process. Keep the list in step with Go and TypeScript (a test compares them)."""

from __future__ import annotations

import re

_R = "[redacted]"

SCRUB_RULES: tuple[tuple[re.Pattern[str], str], ...] = (
    (re.compile(r"dg_(live|test)_[0-9A-Za-z_]+"), r"dg_\1_" + _R),
    (re.compile(r"\b(sk|rk)_(live|test)_[0-9A-Za-z]{8,}"), r"\1_\2_" + _R),
    (re.compile(r"\bbearer\s+[A-Za-z0-9._~+/=-]{8,}", re.IGNORECASE), "Bearer " + _R),
    (re.compile(r"://[^/\s:@]+:[^/\s@]+@"), "://" + _R + "@"),
    (re.compile(r"\b(AKIA|ASIA)[0-9A-Z]{16}\b"), _R),
    (re.compile(r"\bgh[pousr]_[A-Za-z0-9]{36,}"), _R),
    (re.compile(r"\bgithub_pat_[A-Za-z0-9_]{22,}"), _R),
    (re.compile(r"\bxox[abprs]-[A-Za-z0-9-]{10,}"), _R),
    (re.compile(r"\bxapp-[A-Za-z0-9-]{10,}"), _R),
    (
        re.compile(
            r"-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----(?:[\s\S]*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----|[\s\S]*)"
        ),
        _R,
    ),
    (re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{16,}"), _R),
    (re.compile(r"\bya29\.[A-Za-z0-9_-]{16,}"), _R),
    (re.compile(r"\b1//0[A-Za-z0-9_-]{16,}"), _R),
    (re.compile(r"\bGOCSPX-[A-Za-z0-9_-]{16,}"), _R),
    (re.compile(r"\bntn_[A-Za-z0-9]{20,}"), _R),
    (re.compile(r"\bsecret_[A-Za-z0-9]{43}\b"), _R),
)


def scrub(s: str, limit: int = 1024) -> str:
    """Removes secrets by shape and caps the text at ``limit`` UTF-8 bytes (default 1 KiB)."""
    out = s
    for rx, repl in SCRUB_RULES:
        out = rx.sub(repl, out)
    if len(out.encode("utf-8")) <= limit:
        return out
    cut = out[:limit]
    while len(cut.encode("utf-8")) > limit - 3:
        cut = cut[:-1]
    return cut + "…"
