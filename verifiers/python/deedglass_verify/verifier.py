"""Inputs to a report: one deed, a chain of deeds (JSONL) or an export bundle. Stops at the first failure."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, BinaryIO, Iterable

from .bundle import BundleReader, Observed, check_manifest, check_manifest_seal
from .deed import Deed, genesis, verify_deed
from .errors import CANNOT_DETERMINE, VerifyError
from .keys import KeySet, parse_key_set

VERIFIER = "deedglass-verify (Python) 0.1.0"


class Chain:
    """Section 11.2, in input order."""

    def __init__(self) -> None:
        self.prev: Deed | None = None

    def next(self, d: Deed) -> None:
        def fail(code: str, **kw: Any) -> VerifyError:
            return VerifyError(code, seq=d.seq, deed=d.deed_id, **kw)

        p = self.prev
        if p is not None and d.org != p.org:
            raise fail("chain_org_mismatch", org=d.org, want_org=p.org)
        if p is None:
            if d.seq != 1:
                raise fail("chain_seq_gap", want=1)
            if d.body["prev_hash"] != genesis(d.org):
                raise fail("chain_genesis_mismatch")
        else:
            if d.seq <= p.seq:
                raise fail("chain_seq_out_of_order", prev=p.seq)
            if d.seq > p.seq + 1:
                raise fail("chain_seq_gap", prev=p.seq, want=p.seq + 1)
            if d.body["prev_hash"] != p.body_hash:
                raise fail("chain_prev_hash_mismatch", prev=p.seq)
            if d.issued_at < p.issued_at:
                raise fail("chain_time_regression", prev=p.seq)
        self.prev = d


@dataclass
class Report:
    deeds: int = 0
    intact: int = 0
    org: str | None = None
    first_seq: int | None = None
    last_seq: int | None = None
    frm: str | None = None
    to: str | None = None
    chain: str = "not_checked"
    kinds: dict[str, int] = field(default_factory=dict)
    keys_used: dict[str, int] = field(default_factory=dict)
    bundle: dict[str, Any] | None = None
    failure: VerifyError | None = None
    warnings: list[dict[str, str]] = field(default_factory=list)
    keys_pinned: bool = False
    needs_pinned: bool = False  # --strict: keys from the bundle itself can't decide (exit 2)

    @property
    def result(self) -> str:
        if self.failure is None:
            return "cannot_determine" if self.needs_pinned and not self.keys_pinned else "valid"
        return "cannot_determine" if self.failure.code in CANNOT_DETERMINE else "invalid"

    @property
    def exit_code(self) -> int:
        return {"valid": 0, "invalid": 1, "cannot_determine": 2}[self.result]

    def add(self, d: Deed) -> None:
        self.intact += 1
        self.org = self.org or d.org
        self.first_seq = d.seq if self.first_seq is None else min(self.first_seq, d.seq)
        self.last_seq = d.seq if self.last_seq is None else max(self.last_seq, d.seq)
        self.frm = d.issued_at if self.frm is None else min(self.frm, d.issued_at)
        self.to = d.issued_at if self.to is None else max(self.to, d.issued_at)
        self.kinds[d.kind] = self.kinds.get(d.kind, 0) + 1
        for k in d.key_ids:
            self.keys_used[k] = self.keys_used.get(k, 0) + 1

    def to_json(self) -> dict[str, Any]:
        f = self.failure
        failures = []
        if f is not None:
            msg, nxt = f.message()
            failures.append({"code": f.code, "seq": f.seq, "deed": f.extra.get("deed"), "field": f.field,
                             "message": msg, "next": nxt})
        return {
            "object": "verification_report",
            "result": self.result,
            "deeds": self.deeds,
            "intact": self.intact,
            "org": self.org,
            "first_seq": self.first_seq,
            "last_seq": self.last_seq,
            "from": self.frm,
            "to": self.to,
            "chain": {"status": self.chain},
            "seal_keys": [{"id": k, "deeds": n} for k, n in sorted(self.keys_used.items())],
            "keys_pinned": self.keys_pinned,
            "kinds": dict(sorted(self.kinds.items())),
            "bundle": self.bundle,
            "failures": failures,
            "warnings": self.warnings,
            "verifier": VERIFIER,
        }


def _chain_status(rep: Report, chained: bool) -> None:
    if not chained:
        return
    if rep.failure is None:
        rep.chain = "intact"
    elif rep.failure.code.startswith("chain_") or rep.intact > 0:
        rep.chain = "broken"


def verify_one(rep: Report, raw: bytes, keys: KeySet | None, strict: bool) -> None:
    """One deed on its own: its checks, and the genesis link when its seq is 1."""
    rep.deeds += 1
    try:
        d = verify_deed(raw.rstrip(b"\r\n"), keys, strict)
        if d.seq == 1 and d.body["prev_hash"] != genesis(d.org):
            raise VerifyError("chain_genesis_mismatch", seq=d.seq, deed=d.deed_id)
    except VerifyError as e:
        rep.failure = e
        return
    rep.add(d)


def verify_lines(rep: Report, lines: Iterable[bytes], keys: KeySet | None, strict: bool, obs: Observed | None = None) -> None:
    """Deeds as one org's chain, in input order."""
    chain = Chain()
    for no, line in enumerate(lines, 1):
        rep.deeds += 1
        try:
            if len(line) == 0:
                raise VerifyError("malformed", detail=f"line {no} is blank")
            d = verify_deed(line, keys, strict)
            chain.next(d)
        except VerifyError as e:
            rep.failure = e
            break
        rep.add(d)
        if obs is not None:
            obs.count += 1
            obs.first_seq = d.seq if obs.first_seq is None else obs.first_seq
            obs.first_prev_hash = d.body["prev_hash"] if obs.first_prev_hash is None else obs.first_prev_hash
            obs.org = d.org
            obs.last_seq = d.seq
            obs.first = d.issued_at if obs.first is None else min(obs.first, d.issued_at)
            obs.last = d.issued_at if obs.last is None else max(obs.last, d.issued_at)
            obs.kinds[d.kind] += 1
            obs.head_seq, obs.head_id, obs.head_hash = d.seq, d.deed_id, d.body_hash
    _chain_status(rep, True)


def split_lines(data: bytes) -> list[bytes]:
    lines = data.split(b"\n")
    if lines and lines[-1].strip() == b"":
        lines.pop()
    return [ln.rstrip(b"\r") for ln in lines]


def verify_bundle(rep: Report, f: BinaryIO, keys: KeySet | None, strict: bool) -> KeySet | None:
    """A bundle: deeds stream through the verifier, then the manifest and its seal are compared.

    Returns the key set used. Without keys from the caller, the bundle's own keys.json is used (and
    is not pinned: keys inside a bundle come from the exporter).
    """
    try:
        br = BundleReader(f)
    except VerifyError as e:
        rep.failure = e
        return keys
    if keys is None:
        try:
            keys = parse_key_set(br.keys_bytes)
        except VerifyError as e:
            rep.failure = e
            return keys
        rep.warnings.append({"code": "keys_not_pinned", "message": "Seal keys came from the bundle itself. This shows nothing changed since export, not who sealed it."})
    obs = Observed()
    try:
        verify_lines(rep, br.deeds(), keys, strict, obs)
    except VerifyError as e:  # the stream itself, not a deed
        rep.failure = e
        return keys
    if rep.failure is not None:
        return keys
    st: dict[str, Any] = {"manifest": "mismatch", "manifest_seal": "missing", "sealed_by": None}
    rep.bundle = st
    try:
        manifest_raw, seal_raw = br.finish()
        m = check_manifest(manifest_raw, br, obs)
    except VerifyError as e:
        rep.failure = e
        return keys
    st["manifest"] = "ok"
    if br.timestamps is not None:
        rep.warnings.append({"code": "timestamps_not_checked", "message": "This bundle carries timestamps.json; this verifier doesn't check timestamps. Use deedglass verify for them."})
    if seal_raw is None:
        rep.warnings.append({"code": "manifest_unsealed", "message": "The bundle manifest is not sealed."})
        return keys
    try:
        st["sealed_by"] = check_manifest_seal(seal_raw, manifest_raw, m["created_at"], keys)
    except VerifyError as e:
        if e.code == "bundle_seal_invalid":
            st["manifest_seal"] = "invalid"
        rep.failure = e
        return keys
    st["manifest_seal"] = "ok"
    return keys
