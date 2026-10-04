"""How the examples print an action: the CLI's grammar (✓ ✕ ◐ ○), colour only on a terminal."""

from __future__ import annotations

import os
import sys

from deedglass import Action

_TTY = sys.stdout.isatty() and not os.environ.get("NO_COLOR")


def _c(code: str, s: str) -> str:
    return f"\033[{code}m{s}\033[0m" if _TTY else s


def show(a: Action | None, sentence: str = "") -> None:
    if a is None:
        print(_c("33", "○") + " " + sentence)
        return
    deed = f" · {a.deed}" if a.deed else ""
    if a.status == "denied":
        print(_c("31", "✕") + f" {a.tool} · denied · {sentence or (a.decision.message if a.decision else '')}")
    elif a.status == "pending_approval":
        apr = a.approval.id if a.approval else "apr_…"
        print(_c("35", "◐") + f" {a.tool} · pending approval · {apr}")
    elif a.witness and a.witness.verdict == "VERIFIED":
        print(_c("36", "✓") + f" {a.tool} · {a.status} · {a.witness.level} · VERIFIED{deed}")
    elif a.witness and a.witness.verdict:
        print(_c("33", "○") + f" {a.tool} · {a.status} · {a.witness.level} · {a.witness.verdict}{deed}")
    else:
        print(_c("33", "○") + f" {a.tool} · {a.status}{deed}")
