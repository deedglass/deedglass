"""Seal key sets (keys.json): a port of deed.ParseKeySet and keys.ParsePublicKey.

Any unsafe key (small-order or non-canonical Ed25519, attack case A13) or inconsistent key refuses
the whole set with ``seal_key_rejected``: a verifier never picks and chooses.
"""

from __future__ import annotations

import hashlib
import re
from dataclasses import dataclass, field
from typing import Any

from ._b64 import decode_b64url
from ._keys_gen import SMALL_ORDER_KEYS
from ._schema import Decoder, as_obj, closed, is_id, parse_time
from ._strict import StrictError, malformed_from, parse_strict_value
from ._verror import VerifyError, key_rejected, malformed, schema_field

# Deed v2 (ADR-109): every key names its algorithm. This verifier checks Ed25519 seals; a key of
# another algorithm is kept (checked for what can be checked without the algorithm) and never used,
# so a key set that also lists the issuer's ML-DSA-65 key still verifies its Ed25519 deeds.
ALG_ED25519 = "Ed25519"
# The algorithms the deed v2 spec registers: the key-set rule (DEED_SPEC.md §11.4) requires a seal of
# each that is active at a deed's issued_at, even one this verifier can't check, so a v1 deed sealed
# after the issuer added an ML-DSA-65 key is refused (seal_missing), never verified.
SPEC_ALGS = ("Ed25519", "ML-DSA-65")
_RE_ALG = re.compile(r"^[A-Za-z0-9][A-Za-z0-9-]{0,31}$")
_MAX_UNKNOWN_KEY_BYTES = 8 << 10

_KEY_FIELDS = (
    "object",
    "id",
    "alg",
    "public_key",
    "thumbprint",
    "status",
    "activated_at",
    "retired_at",
    "revoked_at",
    "jwk",
)


def sha256_digest(b: bytes | str) -> str:
    """'sha256:' + lowercase hex of SHA-256."""
    if isinstance(b, str):
        b = b.encode("utf-8")
    return "sha256:" + hashlib.sha256(b).hexdigest()


@dataclass
class SealKey:
    id: str
    alg: str
    public_key: str
    thumbprint: str
    status: str
    activated_at: str
    retired_at: str | None
    revoked_at: str | None
    jwk: dict[str, str]
    object: str = "seal_key"


@dataclass
class _Entry:
    key: SealKey
    pub: bytes
    activated: int
    retired: int | None
    revoked: int | None
    verifier: Any = None


@dataclass
class KeySet:
    """A parsed, checked seal key set."""

    issuer: str = ""
    updated_at: str = ""
    keys: list[SealKey] = field(default_factory=list)
    _by_id: dict[str, _Entry] = field(default_factory=dict, repr=False)

    @classmethod
    def from_json(cls, data: bytes | str) -> KeySet:
        """Reads keys.json strictly. Raises VerifyError."""
        return parse_key_set(data)

    @classmethod
    def fetch(cls, url: str, *, timeout: float = 5.0) -> KeySet:
        """Fetches keys.json: https only (http on loopback), 5 s, 1 MiB, no redirects."""
        from ._fetch import fetch_key_set

        return fetch_key_set(url, timeout=timeout)

    def lookup(self, key_id: str) -> _Entry | None:
        return self._by_id.get(key_id)

    def valid_at(self, key_id: str, t: int) -> str:
        """'' or seal_key_revoked / seal_key_not_valid_at: activated_at ≤ t < retired_at, never revoked."""
        e = self._by_id[key_id]
        if e.revoked is not None:
            return "seal_key_revoked"
        if t < e.activated or (e.retired is not None and t >= e.retired):
            return "seal_key_not_valid_at"
        return ""

    def active_algs(self, t: int) -> list[str]:
        """The spec-registered algorithms of the keys not revoked and active at t (the key-set rule)."""
        out: list[str] = []
        for k in self.keys:
            e = self._by_id[k.id]
            if k.alg not in SPEC_ALGS or e.revoked is not None or t < e.activated:
                continue
            if e.retired is not None and t >= e.retired:
                continue
            if k.alg not in out:
                out.append(k.alg)
        return out

    def thumbprints(self) -> list[str]:
        return [k.thumbprint for k in self.keys]


def parse_key_set(data: bytes | str) -> KeySet:
    try:
        tree = parse_strict_value(data, True)
    except StrictError as e:
        raise malformed_from(e) from None
    try:
        top = as_obj(tree, "")
    except VerifyError:
        raise malformed("a key set must be a JSON object") from None
    closed(top, ("object", "issuer", "updated_at", "keys"))
    d = Decoder()
    obj = d.s(top, "object")
    ks = KeySet(issuer=d.s(top, "issuer"), updated_at=d.s(top, "updated_at"))
    d.check()
    if obj != "seal_key_set":
        raise schema_field("object", 'must be "seal_key_set"')
    if parse_time(ks.updated_at) is None:
        raise schema_field("updated_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
    lst = top.m.get("keys")
    if not isinstance(lst, list):
        raise schema_field("keys", "must be an array")
    objects: list[str] = []
    for i, v in enumerate(lst):
        o = as_obj(v, f"keys[{i}]")
        closed(o, _KEY_FIELDS)
        objects.append(d.s(o, "object"))
        k = SealKey(
            id=d.s(o, "id"),
            alg=d.s(o, "alg"),
            public_key=d.s(o, "public_key"),
            thumbprint=d.s(o, "thumbprint"),
            status=d.s(o, "status"),
            activated_at=d.s(o, "activated_at"),
            retired_at=d.ns(o, "retired_at"),
            revoked_at=d.ns(o, "revoked_at"),
            jwk={},
        )
        if k.alg == ALG_ED25519:
            jkeys: tuple[str, ...] = ("kty", "crv", "x", "kid", "use")
        else:
            jm = o.m.get("jwk")
            present = jm if isinstance(jm, dict) else {}
            jkeys = ("kty", "kid", "use", *(f for f in ("crv", "x", "alg", "pub") if f in present))
        j = d.obj(o, "jwk", jkeys)
        if j is not None:
            k.jwk = {n: d.s(j, n) for n in jkeys}
        elif d.err is None:
            d.err = schema_field(f"{o.path}.jwk", "must be an object")
        d.check()
        ks.keys.append(k)
    _index(ks, objects)
    return ks


def _index(ks: KeySet, objects: list[str]) -> None:
    ks._by_id = {}
    for i, k in enumerate(ks.keys):
        if objects[i] != "seal_key":
            raise schema_field(f"keys[{i}].object", 'must be "seal_key"')
        if not is_id("sk", k.id):
            raise schema_field(f"keys[{i}].id", "must be an sk_ id")
        if k.id in ks._by_id:
            raise key_rejected(k.id, "listed twice")
        if not _RE_ALG.match(k.alg):
            raise key_rejected(k.id, "unknown alg " + k.alg)
        raw = decode_b64url(k.public_key)
        j = k.jwk
        if k.alg == ALG_ED25519:
            if raw is None or len(raw) != 32:
                raise key_rejected(k.id, "public_key is not 32 bytes of base64url")
            why = unsafe_key(raw)
            if why is not None:
                raise key_rejected(k.id, why)
            if (
                j.get("kty") != "OKP"
                or j.get("crv") != "Ed25519"
                or j.get("x") != k.public_key
                or j.get("kid") != k.id
                or j.get("use") != "sig"
            ):
                raise key_rejected(k.id, "jwk does not match the key")
        else:
            if raw is None or len(raw) == 0 or len(raw) > _MAX_UNKNOWN_KEY_BYTES:
                raise key_rejected(k.id, "public_key is not base64url")
            if j.get("kid") != k.id or j.get("use") != "sig" or not j.get("kty"):
                raise key_rejected(k.id, "jwk does not match the key")
        if k.thumbprint != sha256_digest(raw):
            raise key_rejected(k.id, "thumbprint does not match public_key")
        activated = parse_time(k.activated_at)
        if activated is None:
            raise schema_field(f"keys[{i}].activated_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ")
        retired = revoked = None
        if k.retired_at is not None:
            retired = parse_time(k.retired_at)
            if retired is None:
                raise schema_field(f"keys[{i}].retired_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ or null")
        if k.revoked_at is not None:
            revoked = parse_time(k.revoked_at)
            if revoked is None:
                raise schema_field(f"keys[{i}].revoked_at", "must be YYYY-MM-DDTHH:MM:SS.mmmZ or null")
        ok = (
            (k.status == "active" and retired is None and revoked is None)
            or (k.status == "retired" and retired is not None and revoked is None)
            or (k.status == "revoked" and revoked is not None)
        )
        if not ok:
            raise key_rejected(k.id, f"status {k.status} does not match retired_at and revoked_at")
        ks._by_id[k.id] = _Entry(k, raw, activated, retired, revoked)
    ks.keys = sorted(ks.keys, key=lambda k: k.activated_at)


# --- Ed25519 public key checks (a port of internal/keys) ---

_P = (1 << 255) - 19
_D = 37095705934669439343138083508754565189542113879843219016388785533085940283555
_SQRT_M1 = pow(2, (_P - 1) // 4, _P)
_SMALL = {h for _, h, _ in SMALL_ORDER_KEYS}


def _inv(a: int) -> int:
    return pow(a % _P, _P - 2, _P)


def _add(x1: int, y1: int, x2: int, y2: int) -> tuple[int, int]:
    dxy = _D * x1 * x2 * y1 * y2 % _P
    x3 = (x1 * y2 + y1 * x2) * _inv(1 + dxy) % _P
    y3 = (y1 * y2 + x1 * x2) * _inv((1 - dxy) % _P) % _P
    return x3, y3


def unsafe_key(pub: bytes) -> str | None:
    """Why a 32-byte Ed25519 public key is unsafe (non-canonical, off the curve, small order), or None."""
    if len(pub) != 32:
        return "not 32 bytes"
    sign = pub[31] >> 7
    y = int.from_bytes(bytes(pub[:31]) + bytes([pub[31] & 0x7F]), "little")
    if y >= _P:
        return "non-canonical encoding"
    y2 = y * y % _P
    u = (y2 - 1) % _P
    v = (_D * y2 + 1) % _P
    if v == 0:
        return "not a point on the curve"
    x2 = u * _inv(v) % _P
    x = pow(x2, (_P + 3) // 8, _P)
    if x * x % _P != x2:
        x = x * _SQRT_M1 % _P
        if x * x % _P != x2:
            return "not a point on the curve"
    if x == 0 and sign == 1:
        return "non-canonical encoding"
    if (x & 1) != sign:
        x = _P - x
    px, py = x, y
    for _ in range(3):
        px, py = _add(px, py, px, py)
    if px == 0 and py == 1:
        return "small-order point"
    if pub.hex() in _SMALL:
        return "small-order point"
    return None
