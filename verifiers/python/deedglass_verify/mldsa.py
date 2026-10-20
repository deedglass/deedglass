"""ML-DSA-65 signature verification (FIPS 204, August 2024), standard library only.

Only what DEED_SPEC.md section 7.4 needs: pure ML-DSA.Verify (Algorithm 3) with the empty context
string, so the message the internal verifier sees is 0x00 || 0x00 || M. Written from FIPS 204's
algorithms; the hash functions are hashlib's SHAKE128 and SHAKE256. Not constant time: a verifier
handles only public data.
"""

from __future__ import annotations

import hashlib
from functools import lru_cache

Q = 8380417
N = 256
D = 13
K, L = 6, 5
TAU = 49
LAMBDA = 192
GAMMA1 = 1 << 19
GAMMA2 = (Q - 1) // 32
BETA = TAU * 4  # tau * eta, eta = 4
OMEGA = 55

PK_BYTES = 32 + K * 320  # 1,952
SIG_BYTES = LAMBDA // 4 + L * 640 + OMEGA + K  # 3,309


def _brv8(x: int) -> int:
    return int(f"{x:08b}"[::-1], 2)


ZETAS = [pow(1753, _brv8(i), Q) for i in range(256)]
N_INV = pow(256, Q - 2, Q)  # 8347681


def _ntt(a: list[int]) -> list[int]:
    w = list(a)
    m = 0
    ln = 128
    while ln >= 1:
        start = 0
        while start < 256:
            m += 1
            z = ZETAS[m]
            for j in range(start, start + ln):
                t = z * w[j + ln] % Q
                w[j + ln] = (w[j] - t) % Q
                w[j] = (w[j] + t) % Q
            start += 2 * ln
        ln //= 2
    return w


def _intt(a: list[int]) -> list[int]:
    w = list(a)
    m = 256
    ln = 1
    while ln < 256:
        start = 0
        while start < 256:
            m -= 1
            z = Q - ZETAS[m]
            for j in range(start, start + ln):
                t = w[j]
                w[j] = (t + w[j + ln]) % Q
                w[j + ln] = z * (t - w[j + ln]) % Q
            start += 2 * ln
        ln *= 2
    return [x * N_INV % Q for x in w]


def _rej_ntt_poly(seed: bytes) -> list[int]:
    """Algorithm 30 RejNTTPoly: coefficients below q from SHAKE128, three bytes at a time."""
    n = 840
    while True:
        buf = hashlib.shake_128(seed).digest(n)
        out: list[int] = []
        for i in range(0, n - 2, 3):
            c = buf[i] | (buf[i + 1] << 8) | ((buf[i + 2] & 0x7F) << 16)
            if c < Q:
                out.append(c)
                if len(out) == 256:
                    return out
        n *= 2


@lru_cache(maxsize=8)
def _expand_a(rho: bytes) -> tuple[tuple[tuple[int, ...], ...], ...]:
    """Algorithm 32 ExpandA: A-hat[r][s] = RejNTTPoly(rho || s || r)."""
    return tuple(tuple(tuple(_rej_ntt_poly(rho + bytes([s, r]))) for s in range(L)) for r in range(K))


def _sample_in_ball(ctilde: bytes) -> list[int]:
    """Algorithm 29 SampleInBall over the whole commitment hash (lambda/4 bytes)."""
    n = 272
    while True:
        buf = hashlib.shake_256(ctilde).digest(n)
        signs = int.from_bytes(buf[:8], "little")
        pos = 8
        c = [0] * 256
        ok = True
        for i in range(256 - TAU, 256):
            while True:
                if pos >= n:
                    ok = False
                    break
                j = buf[pos]
                pos += 1
                if j <= i:
                    break
            if not ok:
                break
            c[i] = c[j]
            c[j] = Q - 1 if (signs >> (i + TAU - 256)) & 1 else 1
        if ok:
            return c
        n *= 2


def _unpack(b: bytes, bits: int) -> list[int]:
    """SimpleBitUnpack: 256 coefficients of `bits` bits each, least significant bit first."""
    v = int.from_bytes(b, "little")
    mask = (1 << bits) - 1
    return [(v >> (bits * i)) & mask for i in range(256)]


def _hint_unpack(y: bytes) -> list[list[int]] | None:
    """Algorithm 21 HintBitUnpack; None when the encoding is malformed."""
    h = [[0] * 256 for _ in range(K)]
    index = 0
    for i in range(K):
        end = y[OMEGA + i]
        if end < index or end > OMEGA:
            return None
        first = index
        while index < end:
            if index > first and y[index - 1] >= y[index]:
                return None
            h[i][y[index]] = 1
            index += 1
    for i in range(index, OMEGA):
        if y[i] != 0:
            return None
    return h


def _use_hint(h: int, r: int) -> int:
    """Algorithms 36 Decompose and 40 UseHint with gamma2 = (q-1)/32, so m = 16."""
    rp = r % Q
    r0 = rp % (2 * GAMMA2)
    if r0 > GAMMA2:
        r0 -= 2 * GAMMA2
    if rp - r0 == Q - 1:
        r1, r0 = 0, r0 - 1
    else:
        r1 = (rp - r0) // (2 * GAMMA2)
    if h == 1:
        return (r1 + 1) % 16 if r0 > 0 else (r1 - 1) % 16
    return r1


def verify(public_key: bytes, message: bytes, sig: bytes, ctx: bytes = b"") -> bool:
    """ML-DSA.Verify(pk, M, sigma, ctx) for ML-DSA-65."""
    if len(public_key) != PK_BYTES or len(sig) != SIG_BYTES or len(ctx) > 255:
        return False
    mp = bytes([0, len(ctx)]) + ctx + message

    rho = public_key[:32]
    t1 = [_unpack(public_key[32 + 320 * i : 32 + 320 * (i + 1)], 10) for i in range(K)]

    clen = LAMBDA // 4
    ctilde = sig[:clen]
    z = []
    for i in range(L):
        raw = _unpack(sig[clen + 640 * i : clen + 640 * (i + 1)], 20)
        z.append([GAMMA1 - x for x in raw])
    h = _hint_unpack(sig[clen + 640 * L :])
    if h is None:
        return False
    if max(abs(x) for zi in z for x in zi) >= GAMMA1 - BETA:
        return False

    a = _expand_a(rho)
    tr = hashlib.shake_256(public_key).digest(64)
    mu = hashlib.shake_256(tr + mp).digest(64)
    c_hat = _ntt(_sample_in_ball(ctilde))
    z_hat = [_ntt([x % Q for x in zi]) for zi in z]

    w1_bits = 0
    shift = 0
    for i in range(K):
        acc = [0] * 256
        for j in range(L):
            aij = a[i][j]
            zj = z_hat[j]
            for n in range(256):
                acc[n] += aij[n] * zj[n]
        t1_hat = _ntt([(x << D) % Q for x in t1[i]])
        for n in range(256):
            acc[n] = (acc[n] - c_hat[n] * t1_hat[n]) % Q
        w = _intt(acc)
        for n in range(256):
            w1_bits |= _use_hint(h[i][n], w[n]) << shift
            shift += 4
    w1 = w1_bits.to_bytes(K * 128, "little")
    return hashlib.shake_256(mu + w1).digest(clen) == ctilde
