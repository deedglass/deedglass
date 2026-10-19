"""The verification_report (Go's deed.Report field names) and the Verifier that builds it."""

from __future__ import annotations

import re
import time
from collections.abc import Iterable
from dataclasses import dataclass, field
from typing import Any

from ._chain import ChainChecker, Head
from ._deed import MAX_INPUT_BYTES, DeedResult, computed_body_hash, genesis, verify_envelope
from ._keys import KeySet
from ._keys_gen import TEST_KEY_THUMBPRINTS
from ._messages_gen import MESSAGES
from ._schema import parse_time
from ._verror import VerifyError

MAX_FAILURES = 100
VERIFIER = "deedglass-verify-python 0.1.0"


def cannot_determine(code: str) -> bool:
    """Codes that mean 'cannot decide' (exit 2 in the CLI), not 'failed'."""
    return code in ("keys_missing", "unsupported_version", "unsupported_algorithm")


def _message_key(e: VerifyError) -> str:
    if e.code == "chain_seq_gap" and e.prev == 0 and e.want == 1:
        return "chain_seq_gap.start"
    return e.code if e.code in MESSAGES else "unknown"


def _vars(e: VerifyError) -> dict[str, str]:
    ref = f"Deed {e.seq}" if e.seq > 0 else "The deed"

    def dash(s: str) -> str:
        return s if s else "—"

    detail = e.detail or ("This file is not a valid deed bundle." if e.code == "bundle_invalid" else "unreadable")
    return {
        "Deed": ref,
        "deed": ref[0].lower() + ref[1:],
        "seq": str(e.seq),
        "prev": str(e.prev),
        "want": str(e.want),
        "prev_anchor": str(max(e.seq - 1, 0)),
        "v": str(e.version),
        "field": dash(e.field),
        "key": dash(e.key),
        "issued_at": dash(e.issued_at),
        "org": dash(e.org),
        "want_org": dash(e.want_org),
        "detail": detail,
        "code": e.code,
        "keys_url": "<issuer>/.well-known/deedglass/keys.json",
    }


_PH = re.compile(r"\{([^{}]*)\}")


def render_message(tmpl: str, v: dict[str, str]) -> str:
    """Replaces every {name} with v[name]; unknown names stay as they are (Go's RenderMessage)."""
    return _PH.sub(lambda m: v.get(m.group(1), m.group(0)), tmpl)


def message(e: VerifyError) -> tuple[str, str]:
    """The one sentence and the one next step a person sees for e: exactly what the CLI prints."""
    sentence, nxt = MESSAGES[_message_key(e)]
    v = _vars(e)
    return render_message(sentence, v), render_message(nxt, v)


@dataclass
class Failure:
    code: str
    seq: int | None
    deed: str | None
    input: str | None
    line: int | None
    field: str | None
    expected: str | None
    computed: str | None
    message: str
    next: str


def failure_from(e: VerifyError) -> Failure:
    s, n = message(e)
    return Failure(
        e.code, e.seq if e.seq > 0 else None, e.deed_id or None, None, None, e.field or None, None, None, s, n
    )


@dataclass
class Report:
    """The verification report: the same fields as Go's deed.Report."""

    result: str = "valid"
    deeds: int = 0
    intact: int = 0
    org: str | None = None
    first_seq: int | None = None
    last_seq: int | None = None
    from_: str | None = None
    to: str | None = None
    chain_status: str = "not_checked"
    chain_anchor: dict[str, Any] = field(default_factory=lambda: {"kind": "none", "seq": None, "body_hash": None})
    seal_keys: list[dict[str, Any]] = field(default_factory=list)
    keys_pinned: bool = False
    kinds: dict[str, int] = field(default_factory=dict)
    verdicts_recorded: dict[str, int] = field(default_factory=dict)
    failures: list[Failure] = field(default_factory=list)
    warnings: list[dict[str, str]] = field(default_factory=list)
    duration_ms: int = 0
    verifier: str = VERIFIER

    @property
    def ok(self) -> bool:
        return self.result == "valid"

    def to_dict(self) -> dict[str, Any]:
        return {
            "object": "verification_report",
            "result": self.result,
            "deeds": self.deeds,
            "intact": self.intact,
            "org": self.org,
            "first_seq": self.first_seq,
            "last_seq": self.last_seq,
            "from": self.from_,
            "to": self.to,
            "chain": {"status": self.chain_status, "anchor": self.chain_anchor},
            "seal_keys": self.seal_keys,
            "keys_pinned": self.keys_pinned,
            "kinds": self.kinds,
            "verdicts_recorded": self.verdicts_recorded,
            "bundle": None,
            "failures": [f.__dict__ for f in self.failures],
            "warnings": self.warnings,
            "duration_ms": self.duration_ms,
            "verifier": self.verifier,
        }


class Verifier:
    """Checks deeds as one chain (``chain``) or one at a time (``loose``), then ``report()``."""

    def __init__(
        self,
        keys: KeySet | None,
        *,
        org: str = "",
        anchor: tuple[int, str] | None = None,
        strict: bool = False,
        all: bool = False,
        keys_pinned: bool = False,
        key_source: str = "",
    ) -> None:
        self._keys = keys
        self._org = org
        self._anchor = anchor
        self._strict = strict
        self._all = all
        self._key_source = key_source
        self._rep = Report(keys_pinned=keys_pinned)
        self._started = time.monotonic()
        self._mode = ""
        self._cc: ChainChecker | None = None
        self._resync = False
        self.stopped = False
        self._first_org = ""
        self._orgs: set[str] = set()
        self._tally: dict[str, dict[str, Any]] = {}

    def chain(self, lines: Iterable[bytes | str], input: str = "-") -> None:
        if self._mode == "":
            self._mode = "chain"
        for no, line in enumerate(lines, start=1):
            if self.stopped:
                break
            self._consume(self._check(line, no), input, no)

    def loose(self, raw: bytes | str, input: str = "-") -> None:
        if self.stopped:
            return
        if self._mode == "":
            self._mode = "loose"
        self._consume(self._check(raw, 1, loose=True), input, 1)

    def _check(self, raw: bytes | str, no: int, loose: bool = False) -> dict[str, Any]:
        size = len(raw.encode("utf-8", "surrogatepass")) if isinstance(raw, str) else len(raw)
        if not loose and size > MAX_INPUT_BYTES:
            return {"err": VerifyError("malformed", detail=f"line {no} is longer than {MAX_INPUT_BYTES >> 10} KiB")}
        if size == 0:
            return {"err": VerifyError("malformed", detail=f"line {no} is blank")}
        keys = self._keys if self._keys is not None else KeySet()
        try:
            return {"res": verify_envelope(raw, keys, strict=self._strict)}
        except VerifyError as e:
            err = e
            if self._keys is None and err.code == "seal_key_unknown":
                err = VerifyError("keys_missing", seq=err.seq, deed_id=err.deed_id, key=err.key)
            out: dict[str, Any] = {"err": err}
            if err.code != "malformed":
                c = computed_body_hash(raw)
                if c is not None:
                    out["env"] = c[0]
                    if err.code == "body_hash_mismatch":
                        out["computed"] = c[1]
            return out

    def _fail(self, f: Failure) -> None:
        if len(self._rep.failures) < MAX_FAILURES:
            self._rep.failures.append(f)
        if not self._all or cannot_determine(f.code):
            self.stopped = True

    def _consume(self, o: dict[str, Any], input: str, line: int) -> None:
        self._rep.deeds += 1
        err: VerifyError | None = o.get("err")
        if err is not None:
            f = failure_from(err)
            f.input, f.line = input, line
            if o.get("computed") and o.get("env"):
                f.expected, f.computed = o["env"]["body_hash"], o["computed"]
            self._fail(f)
            if self._mode == "loose":
                return
            env = o.get("env")
            if env is not None:
                self._checker().continue_from(_result_of(env))
            else:
                self._resync = True
            return
        r: DeedResult = o["res"]
        if self._mode == "loose":
            ce: VerifyError | None = None
            if self._org and r.org != self._org:
                ce = VerifyError("chain_org_mismatch", seq=r.seq, deed_id=r.deed_id, org=r.org, want_org=self._org)
            elif r.seq == 1 and r.body["prev_hash"] != genesis(r.org):
                ce = VerifyError("chain_genesis_mismatch", seq=r.seq, deed_id=r.deed_id)
            if ce is not None:
                f = failure_from(ce)
                f.input, f.line = input, line
                self._fail(f)
                return
        else:
            cc = self._checker()
            if self._resync:
                cc.restart(r)
                self._resync = False
            try:
                cc.next(r)
            except VerifyError as e:
                f = failure_from(e)
                f.input, f.line = input, line
                self._fail(f)
                cc.continue_from(r)
                return
        self._intact(r)

    def _checker(self) -> ChainChecker:
        if self._cc is None:
            a = self._anchor
            self._cc = ChainChecker(org=self._org, anchor=Head(a[0], a[1]) if a else None)
        return self._cc

    def _intact(self, r: DeedResult) -> None:
        rep = self._rep
        rep.intact += 1
        if self._first_org == "":
            self._first_org = r.org
        self._orgs.add(r.org)
        rep.first_seq = r.seq if rep.first_seq is None else min(rep.first_seq, r.seq)
        rep.last_seq = r.seq if rep.last_seq is None else max(rep.last_seq, r.seq)
        t = r.body["issued_at"]
        rep.from_ = t if rep.from_ is None or t < rep.from_ else rep.from_
        rep.to = t if rep.to is None or t > rep.to else rep.to
        kind = r.body["kind"]
        rep.kinds[kind] = rep.kinds.get(kind, 0) + 1
        verdict = r.body["witness"]["verdict"] if r.body["witness"] else "none"
        rep.verdicts_recorded[verdict] = rep.verdicts_recorded.get(verdict, 0) + 1
        k = self._tally.get(r.seal_key_id)
        if k is None:
            e = self._keys.lookup(r.seal_key_id) if self._keys else None
            k = {
                "id": r.seal_key_id,
                "thumbprint": e.key.thumbprint if e else "",
                "deeds": 0,
                "first_seq": r.seq,
                "last_seq": r.seq,
                "source": self._key_source,
            }
            self._tally[r.seal_key_id] = k
        k["deeds"] += 1
        k["first_seq"] = min(k["first_seq"], r.seq)
        k["last_seq"] = max(k["last_seq"], r.seq)

    def report(self) -> Report:
        rep = self._rep
        rep.seal_keys = sorted(self._tally.values(), key=lambda k: k["first_seq"])
        if len(self._orgs) == 1:
            rep.org = self._first_org
        elif not self._orgs and self._org:
            rep.org = self._org
        if self._mode == "chain":
            rep.chain_status = "intact"
            if self._anchor:
                rep.chain_anchor = {"kind": "deed", "seq": self._anchor[0], "body_hash": self._anchor[1]}
            else:
                rep.chain_anchor = {"kind": "genesis", "seq": 0, "body_hash": genesis(rep.org) if rep.org else None}
        first: dict[str | None, int] = {}
        for f in rep.failures:
            first.setdefault(f.input, len(first))
        rep.failures.sort(key=lambda f: (first[f.input], f.line or 0))
        rep.result = "valid"
        for f in rep.failures:
            if cannot_determine(f.code):
                if rep.result == "valid":
                    rep.result = "cannot_determine"
            else:
                rep.result = "invalid"
        if rep.result == "invalid" and rep.chain_status == "intact":
            rep.chain_status = "broken"
        if rep.result == "cannot_determine" and rep.chain_status == "intact":
            rep.chain_status = "not_checked"
        if rep.deeds == 0 and not rep.failures:
            rep.chain_status = "not_checked"
        if any(k["thumbprint"] in TEST_KEY_THUMBPRINTS for k in rep.seal_keys) and not any(
            w["code"] == "test_keys" for w in rep.warnings
        ):
            s, n = MESSAGES["warning.test_keys"]
            rep.warnings.append({"code": "test_keys", "message": s, "next": n})
        rep.duration_ms = int((time.monotonic() - self._started) * 1000)
        return rep


def _result_of(env: dict[str, Any]) -> DeedResult:
    b = env["body"]
    return DeedResult(
        env,
        b,
        env["body_hash"],
        b["seq"],
        b["deed_id"],
        b["org"],
        parse_time(b["issued_at"]) or 0,
        env["seal_key_id"],
        False,
    )


def split_lines(b: bytes) -> list[bytes]:
    """Splits bytes on '\\n' (the last line may lack it; '\\r' is kept, as Go keeps it)."""
    parts = b.split(b"\n")
    if parts and parts[-1] == b"":
        parts.pop()
    return parts
