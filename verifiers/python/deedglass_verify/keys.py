"""Seal key sets: keys.json (DEED_SPEC.md section 10)."""

from __future__ import annotations

import hashlib
import re
from dataclasses import dataclass
from typing import Any

from . import ed25519, mldsa
from .canon import StrictError, parse_strict
from .errors import VerifyError

ALG_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9-]{0,31}")
SK_RE = re.compile(r"sk_[0-7][0-9a-hjkmnp-tv-z]{25}")
TIME_RE = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z")
B64URL_RE = re.compile(r"[A-Za-z0-9_-]*")

#: The algorithms this verifier implements (section 7.4).
IMPLEMENTED = {"Ed25519", "ML-DSA-65"}
#: Public key sizes for the implemented algorithms.
KEY_BYTES = {"Ed25519": 32, "ML-DSA-65": mldsa.PK_BYTES}
#: Members an unimplemented algorithm's jwk may hold (section 10.2).
JWK_MEMBERS = {"kty", "crv", "x", "alg", "pub", "kid", "use"}

#: Thumbprints of the published test keys (testdata/deeds/keys/test-keys.json).
TEST_THUMBPRINTS = {
    "sha256:f6b1d24a4efdc39230905e0c217093f4d1d5e301ac52c9e763a773e864d93f33",
    "sha256:3d3876ffac00eac374b0e6e5735718edfcc7637ace18ef40a3ccc492cf98bf60",
    "sha256:fdfb7b7d83630f6ada1d5000d134792652f8a149a1af90ea5a2dd542651b1112",
}


def b64url_decode(s: str) -> bytes | None:
    """RFC 4648 section 5 without padding; None for anything else, including non-zero trailing bits."""
    if not isinstance(s, str) or not B64URL_RE.fullmatch(s) or len(s) % 4 == 1:
        return None
    import base64

    raw = base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))
    if base64.urlsafe_b64encode(raw).rstrip(b"=").decode() != s:
        return None
    return raw


def valid_time(s: Any) -> bool:
    """The time pattern of section 4 and a real UTC instant."""
    if not isinstance(s, str) or not TIME_RE.fullmatch(s):
        return False
    import datetime

    try:
        datetime.datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")
    except ValueError:
        return False
    return True


@dataclass
class SealKey:
    id: str
    alg: str
    public_key: bytes
    thumbprint: str
    status: str
    activated_at: str
    retired_at: str | None
    revoked_at: str | None

    @property
    def revoked(self) -> bool:
        return self.status == "revoked" or self.revoked_at is not None

    def active_at(self, t: str) -> bool:
        """activated_at <= t < retired_at (open-ended when retired_at is null). Times compare as text."""
        return self.activated_at <= t and (self.retired_at is None or t < self.retired_at)


class KeySet:
    def __init__(self, keys: list[SealKey], issuer: str | None = None) -> None:
        self.keys = {k.id: k for k in keys}
        self.issuer = issuer

    def get(self, key_id: str) -> SealKey | None:
        return self.keys.get(key_id)

    @property
    def has_test_keys(self) -> bool:
        return any(k.thumbprint in TEST_THUMBPRINTS for k in self.keys.values())


def _reject(key: str, detail: str) -> VerifyError:
    return VerifyError("seal_key_rejected", key=key, detail=detail)


def _check_key(k: Any, i: int) -> SealKey:
    if not isinstance(k, dict):
        raise _reject(f"keys[{i}]", "a key that is not an object")
    kid = k.get("id")
    label = kid if isinstance(kid, str) else f"keys[{i}]"
    want = {"object", "id", "alg", "public_key", "thumbprint", "status", "activated_at", "retired_at", "revoked_at", "jwk"}
    if set(k) - want:
        raise _reject(label, f"an unknown member {sorted(set(k) - want)[0]}")
    if set(want) - set(k):
        raise _reject(label, f"a missing member {sorted(set(want) - set(k))[0]}")
    if k["object"] != "seal_key":
        raise _reject(label, "object is not seal_key")
    if not isinstance(kid, str) or not SK_RE.fullmatch(kid):
        raise _reject(label, "an id that is not a seal key id")
    alg = k["alg"]
    if not isinstance(alg, str) or not ALG_RE.fullmatch(alg):
        raise _reject(label, "an alg that doesn't match the pattern")
    pub = b64url_decode(k["public_key"])
    if pub is None or len(pub) == 0:
        raise _reject(label, "a public_key that is not base64url")
    if k["thumbprint"] != "sha256:" + hashlib.sha256(pub).hexdigest():
        raise _reject(label, "a thumbprint that is not the SHA-256 of the public key")
    status = k["status"]
    act, ret, rev = k["activated_at"], k["retired_at"], k["revoked_at"]
    if not valid_time(act) or not (ret is None or valid_time(ret)) or not (rev is None or valid_time(rev)):
        raise _reject(label, "a time that is not valid")
    ok = (
        (status == "active" and ret is None and rev is None)
        or (status == "retired" and ret is not None and rev is None)
        or (status == "revoked" and rev is not None)
    )
    if not ok:
        raise _reject(label, "a status that contradicts its times")
    jwk = k["jwk"]
    if not isinstance(jwk, dict):
        raise _reject(label, "a jwk that is not an object")
    if alg == "Ed25519":
        if jwk != {"kty": "OKP", "crv": "Ed25519", "x": k["public_key"], "kid": kid, "use": "sig"}:
            raise _reject(label, "a jwk that differs from the key")
        try:
            ed25519.check_public_key(pub)
        except ed25519.KeyError_ as e:
            raise _reject(label, str(e)) from None
    elif alg == "ML-DSA-65":
        if jwk != {"kty": "AKP", "alg": "ML-DSA-65", "pub": k["public_key"], "kid": kid, "use": "sig"}:
            raise _reject(label, "a jwk that differs from the key")
        if len(pub) != mldsa.PK_BYTES:
            raise _reject(label, f"an ML-DSA-65 public key of {len(pub)} bytes, not {mldsa.PK_BYTES}")
    else:
        # Kept and never used: check what can be checked without the algorithm.
        if set(jwk) - JWK_MEMBERS or jwk.get("kid") != kid or jwk.get("use") != "sig":
            raise _reject(label, "a jwk that differs from the key")
        if not isinstance(jwk.get("kty"), str) or jwk["kty"] == "":
            raise _reject(label, "a jwk without kty")
    return SealKey(kid, alg, pub, k["thumbprint"], status, act, ret, rev)


def parse_key_set(data: bytes) -> KeySet:
    """Parses and checks a whole key set; any unsafe key refuses the set (seal_key_rejected)."""
    try:
        v = parse_strict(data)
    except StrictError as e:
        raise _reject("keys.json", f"not strict JSON ({e})") from None
    if not isinstance(v, dict) or v.get("object") != "seal_key_set" or not isinstance(v.get("keys"), list):
        raise _reject("keys.json", "not a seal_key_set")
    keys: list[SealKey] = []
    seen: set[str] = set()
    for i, k in enumerate(v["keys"]):
        sk = _check_key(k, i)
        if sk.id in seen:
            raise _reject(sk.id, "an id shared with another key")
        seen.add(sk.id)
        keys.append(sk)
    issuer = v.get("issuer") if isinstance(v.get("issuer"), str) else None
    return KeySet(keys, issuer)
