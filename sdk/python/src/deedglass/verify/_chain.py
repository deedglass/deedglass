"""The deed chain, a port of deed.ChainChecker: seq continuity, prev_hash links, org and time order."""

from __future__ import annotations

from dataclasses import dataclass

from ._deed import DeedResult, genesis
from ._verror import VerifyError


@dataclass
class Head:
    seq: int
    body_hash: str
    issued_at: int | None = None


class ChainChecker:
    def __init__(self, *, org: str = "", anchor: Head | None = None) -> None:
        self._prev: DeedResult | None = None
        self._anchor = anchor
        self._org = org

    def next(self, r: DeedResult) -> None:
        """Checks r against the previous deed. Raises VerifyError; the last good deed is kept."""
        b = r.body

        def fail(code: str, **kw: object) -> VerifyError:
            e = VerifyError(code, seq=r.seq, deed_id=r.deed_id, **kw)
            if self._prev is not None and "prev" not in kw:
                e.prev = self._prev.seq
            return e

        want_org = self._prev.org if self._prev else self._org
        if want_org != "" and r.org != want_org:
            raise fail("chain_org_mismatch", org=r.org, want_org=want_org)
        if self._prev is None and self._anchor is None:
            if r.seq != 1:
                raise fail("chain_seq_gap", want=1)
            if b["prev_hash"] != genesis(r.org):
                raise fail("chain_genesis_mismatch")
        else:
            if self._prev is not None:
                prev_seq, prev_hash, prev_time = self._prev.seq, self._prev.body_hash, self._prev.issued_at
            else:
                assert self._anchor is not None
                a = self._anchor
                prev_seq, prev_hash = a.seq, a.body_hash
                prev_time = a.issued_at if a.issued_at is not None else -(10**18)
            if r.seq <= prev_seq:
                raise fail("chain_seq_out_of_order", prev=prev_seq)
            if r.seq > prev_seq + 1:
                raise fail("chain_seq_gap", prev=prev_seq, want=prev_seq + 1)
            if b["prev_hash"] != prev_hash:
                raise fail("chain_prev_hash_mismatch", prev=prev_seq)
            if r.issued_at < prev_time:
                raise fail("chain_time_regression", prev=prev_seq)
        self._prev = r

    def continue_from(self, r: DeedResult) -> None:
        self._prev = r

    def restart(self, nxt: DeedResult) -> None:
        self._prev = None
        self._anchor = Head(nxt.seq - 1, nxt.body["prev_hash"])
