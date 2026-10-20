"""Pure Ed25519 (RFC 8032 section 5.1) verification and key checks, standard library only.

Verification is cofactorless and compares encodings: it computes R' = [S]B - [k]A and accepts
only when encode(R') equals the R bytes of the signature, after refusing S >= L. That refuses
non-canonical R as DEED_SPEC.md section 11.1 check 15 requires. Not constant time: a verifier
handles only public data.
"""

from __future__ import annotations

import hashlib

P = 2**255 - 19
L = 2**252 + 27742317777372353535851937790883648493
D = (-121665 * pow(121666, P - 2, P)) % P
SQRT_M1 = pow(2, (P - 1) // 4, P)

Point = tuple[int, int, int, int]  # extended coordinates (X, Y, Z, T), x = X/Z, y = Y/Z, xy = T/Z


class KeyError_(ValueError):
    """A public key that must be refused (section 10.2)."""


def _recover_x(y: int, sign: int) -> int | None:
    x2 = (y * y - 1) * pow(D * y * y + 1, P - 2, P) % P
    if x2 == 0:
        return None if sign else 0
    x = pow(x2, (P + 3) // 8, P)
    if (x * x - x2) % P != 0:
        x = x * SQRT_M1 % P
    if (x * x - x2) % P != 0:
        return None
    if (x & 1) != sign:
        x = P - x
    return x


def _add(p: Point, q: Point) -> Point:
    x1, y1, z1, t1 = p
    x2, y2, z2, t2 = q
    a = (y1 - x1) * (y2 - x2) % P
    b = (y1 + x1) * (y2 + x2) % P
    c = 2 * t1 * D * t2 % P
    d = 2 * z1 * z2 % P
    e, f, g, h = b - a, d - c, d + c, b + a
    return (e * f % P, g * h % P, f * g % P, e * h % P)


def _mul(s: int, p: Point) -> Point:
    q: Point = (0, 1, 1, 0)
    while s > 0:
        if s & 1:
            q = _add(q, p)
        p = _add(p, p)
        s >>= 1
    return q


def _neg(p: Point) -> Point:
    x, y, z, t = p
    return ((-x) % P, y, z, (-t) % P)


def _encode(p: Point) -> bytes:
    x, y, z, _ = p
    zi = pow(z, P - 2, P)
    x, y = x * zi % P, y * zi % P
    return (y | ((x & 1) << 255)).to_bytes(32, "little")


def _identity(p: Point) -> bool:
    x, y, z, _ = p
    return x % P == 0 and (y - z) % P == 0


_BY = 4 * pow(5, P - 2, P) % P
_BX = _recover_x(_BY, 0)
assert _BX is not None
BASE: Point = (_BX, _BY, 1, _BX * _BY % P)


def decode_point(b: bytes) -> Point:
    """Decodes a 32-byte encoding, refusing non-canonical y, x = 0 with the sign bit, off-curve."""
    if len(b) != 32:
        raise KeyError_(f"a public key of {len(b)} bytes, not 32")
    y = int.from_bytes(b, "little")
    sign = y >> 255
    y &= (1 << 255) - 1
    if y >= P:
        raise KeyError_("a non-canonical encoding (y >= p)")
    x = _recover_x(y, sign)
    if x is None:
        if sign and (y * y - 1) % P == 0:
            raise KeyError_("a non-canonical encoding (x = 0 with the sign bit set)")
        raise KeyError_("a point that is not on the curve")
    return (x, y, 1, x * y % P)


def check_public_key(b: bytes) -> None:
    """Section 10.2: 32 bytes, canonical, on the curve, not of small order (8A is the identity)."""
    a = decode_point(b)
    if _identity(_mul(8, a)):
        raise KeyError_("a small-order point")


def verify(public_key: bytes, message: bytes, sig: bytes) -> bool:
    if len(sig) != 64 or len(public_key) != 32:
        return False
    try:
        a = decode_point(public_key)
    except KeyError_:
        return False
    r = sig[:32]
    s = int.from_bytes(sig[32:], "little")
    if s >= L:
        return False
    k = int.from_bytes(hashlib.sha512(r + public_key + message).digest(), "little") % L
    return _encode(_add(_mul(s, BASE), _neg(_mul(k, a)))) == r
