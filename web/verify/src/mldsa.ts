// ML-DSA-65 signature verification (FIPS 204, Algorithms 3 and 8: pure ML-DSA with the empty
// context string, as RFC 9964 and DEED_SPEC.md §7.4 use it), with no dependency: SHAKE128 and
// SHAKE256 (FIPS 202) on 32-bit lane halves, and the NTT mod q = 8,380,417 in plain numbers, whose
// products stay below 2^53 and so are exact. Verification only: there is no secret in this file and
// nothing here needs to run in constant time. Checked against Go's crypto/mldsa on every vector and
// the v2 parity corpus.

// --- Keccak-f[1600] and SHAKE (FIPS 202) ---

// Round constants, as [low 32 bits, high 32 bits].
const RC: readonly number[] = [
  0x00000001, 0x00000000, 0x00008082, 0x00000000, 0x0000808a, 0x80000000, 0x80008000, 0x80000000,
  0x0000808b, 0x00000000, 0x80000001, 0x00000000, 0x80008081, 0x80000000, 0x00008009, 0x80000000,
  0x0000008a, 0x00000000, 0x00000088, 0x00000000, 0x80008009, 0x00000000, 0x8000000a, 0x00000000,
  0x8000808b, 0x00000000, 0x0000008b, 0x80000000, 0x00008089, 0x80000000, 0x00008003, 0x80000000,
  0x00008002, 0x80000000, 0x00000080, 0x80000000, 0x0000800a, 0x00000000, 0x8000000a, 0x80000000,
  0x80008081, 0x80000000, 0x00008080, 0x80000000, 0x80000001, 0x00000000, 0x80008008, 0x80000000,
];
// Rotation offsets by lane index x + 5y.
const ROT: readonly number[] = [0, 1, 62, 28, 27, 36, 44, 6, 55, 20, 3, 10, 43, 25, 39, 41, 45, 15, 21, 8, 18, 2, 61, 56, 14];
// Where lane x + 5y goes under pi: y + 5((2x + 3y) mod 5).
const PI: readonly number[] = (() => {
  const out: number[] = [];
  for (let i = 0; i < 25; i++) {
    const x = i % 5;
    const y = (i - x) / 5;
    out.push(y + 5 * ((2 * x + 3 * y) % 5));
  }
  return out;
})();

function keccakF(s: Uint32Array): void {
  const c = new Uint32Array(10);
  const b = new Uint32Array(50);
  for (let round = 0; round < 24; round++) {
    for (let x = 0; x < 5; x++) {
      c[2 * x] = s[2 * x]! ^ s[2 * x + 10]! ^ s[2 * x + 20]! ^ s[2 * x + 30]! ^ s[2 * x + 40]!;
      c[2 * x + 1] = s[2 * x + 1]! ^ s[2 * x + 11]! ^ s[2 * x + 21]! ^ s[2 * x + 31]! ^ s[2 * x + 41]!;
    }
    for (let x = 0; x < 5; x++) {
      const p = (x + 4) % 5;
      const n = (x + 1) % 5;
      const lo = c[2 * p]! ^ ((c[2 * n]! << 1) | (c[2 * n + 1]! >>> 31));
      const hi = c[2 * p + 1]! ^ ((c[2 * n + 1]! << 1) | (c[2 * n]! >>> 31));
      for (let y = 0; y < 25; y += 5) {
        s[2 * (x + y)] = s[2 * (x + y)]! ^ lo;
        s[2 * (x + y) + 1] = s[2 * (x + y) + 1]! ^ hi;
      }
    }
    for (let i = 0; i < 25; i++) {
      let lo = s[2 * i]!;
      let hi = s[2 * i + 1]!;
      let r = ROT[i]!;
      if (r >= 32) {
        const t = lo;
        lo = hi;
        hi = t;
        r -= 32;
      }
      const j = PI[i]!;
      if (r === 0) {
        b[2 * j] = lo;
        b[2 * j + 1] = hi;
      } else {
        b[2 * j] = (lo << r) | (hi >>> (32 - r));
        b[2 * j + 1] = (hi << r) | (lo >>> (32 - r));
      }
    }
    for (let y = 0; y < 25; y += 5) {
      for (let x = 0; x < 5; x++) {
        const i = x + y;
        const i1 = ((x + 1) % 5) + y;
        const i2 = ((x + 2) % 5) + y;
        s[2 * i] = b[2 * i]! ^ (~b[2 * i1]! & b[2 * i2]!);
        s[2 * i + 1] = b[2 * i + 1]! ^ (~b[2 * i1 + 1]! & b[2 * i2 + 1]!);
      }
    }
    s[0] = s[0]! ^ RC[2 * round]!;
    s[1] = s[1]! ^ RC[2 * round + 1]!;
  }
}

/** An incremental SHAKE (rate 168 for SHAKE128, 136 for SHAKE256). */
export class Shake {
  private readonly s = new Uint32Array(50);
  private readonly rate: number;
  private pos = 0;
  private squeezing = false;

  constructor(rate: number) {
    this.rate = rate;
  }

  private xorByte(i: number, v: number): void {
    const w = i >> 2;
    this.s[w] = this.s[w]! ^ (v << (8 * (i & 3)));
  }

  private byte(i: number): number {
    return (this.s[i >> 2]! >>> (8 * (i & 3))) & 0xff;
  }

  absorb(data: Uint8Array): this {
    for (let i = 0; i < data.length; i++) {
      this.xorByte(this.pos++, data[i]!);
      if (this.pos === this.rate) {
        keccakF(this.s);
        this.pos = 0;
      }
    }
    return this;
  }

  squeeze(n: number): Uint8Array {
    if (!this.squeezing) {
      this.xorByte(this.pos, 0x1f);
      this.xorByte(this.rate - 1, 0x80);
      keccakF(this.s);
      this.pos = 0;
      this.squeezing = true;
    }
    const out = new Uint8Array(n);
    for (let i = 0; i < n; i++) {
      if (this.pos === this.rate) {
        keccakF(this.s);
        this.pos = 0;
      }
      out[i] = this.byte(this.pos++);
    }
    return out;
  }
}

export const shake128 = (): Shake => new Shake(168);
export const shake256 = (): Shake => new Shake(136);

// --- ML-DSA-65 (FIPS 204) ---

const Q = 8380417;
const D = 13;
const TAU = 49;
const LAMBDA_BYTES = 48; // λ/4 for λ = 192
const GAMMA1 = 1 << 19;
const GAMMA2 = (Q - 1) / 32;
const K = 6;
const L = 5;
const BETA = 196; // τ·η
const OMEGA = 55;

export const MLDSA65_PUBLIC_KEY_SIZE: number = 32 + K * 320; // 1,952
export const MLDSA65_SIGNATURE_SIZE: number = LAMBDA_BYTES + L * 640 + OMEGA + K; // 3,309

const mulq = (a: number, b: number): number => (a * b) % Q;

// zetas[k] = 1753^brv8(k) mod q (FIPS 204, Appendix B).
const ZETAS: Int32Array = (() => {
  const pow = (b: number, e: number): number => {
    let r = 1;
    let x = b;
    while (e > 0) {
      if (e & 1) r = mulq(r, x);
      x = mulq(x, x);
      e >>= 1;
    }
    return r;
  };
  const z = new Int32Array(256);
  for (let k = 0; k < 256; k++) {
    let r = 0;
    for (let i = 0; i < 8; i++) r |= ((k >> i) & 1) << (7 - i);
    z[k] = pow(1753, r);
  }
  return z;
})();

type Poly = Int32Array; // 256 coefficients in [0, q)

function ntt(w: Poly): void {
  let m = 0;
  for (let len = 128; len >= 1; len >>= 1) {
    for (let start = 0; start < 256; start += 2 * len) {
      const z = ZETAS[++m]!;
      for (let j = start; j < start + len; j++) {
        const t = mulq(z, w[j + len]!);
        w[j + len] = (w[j]! - t + Q) % Q;
        w[j] = (w[j]! + t) % Q;
      }
    }
  }
}

function invNtt(w: Poly): void {
  let m = 256;
  for (let len = 1; len < 256; len <<= 1) {
    for (let start = 0; start < 256; start += 2 * len) {
      const z = Q - ZETAS[--m]!;
      for (let j = start; j < start + len; j++) {
        const t = w[j]!;
        w[j] = (t + w[j + len]!) % Q;
        w[j + len] = mulq(z, (t - w[j + len]! + Q) % Q);
      }
    }
  }
  for (let j = 0; j < 256; j++) w[j] = mulq(8347681, w[j]!); // 256^-1 mod q
}

/** RejNTTPoly(ρ || s || r) (Algorithm 30): a uniform polynomial, already in the NTT domain. */
function rejNttPoly(seed: Uint8Array): Poly {
  const x = shake128().absorb(seed);
  const a = new Int32Array(256);
  let j = 0;
  while (j < 256) {
    const b = x.squeeze(3);
    const z = b[0]! | (b[1]! << 8) | ((b[2]! & 0x7f) << 16);
    if (z < Q) a[j++] = z;
  }
  return a;
}

/** SampleInBall(c̃) (Algorithm 29): τ coefficients ±1, the rest 0. */
function sampleInBall(ct: Uint8Array): Poly {
  const x = shake256().absorb(ct);
  const s = x.squeeze(8);
  const c = new Int32Array(256);
  for (let i = 256 - TAU; i < 256; i++) {
    let j = x.squeeze(1)[0]!;
    while (j > i) j = x.squeeze(1)[0]!;
    c[i] = c[j]!;
    const bit = i + TAU - 256;
    c[j] = (s[bit >> 3]! >> (bit & 7)) & 1 ? Q - 1 : 1;
  }
  return c;
}

/** Little-endian bit unpacking of 256 coefficients of `bits` bits each. */
function unpack(b: Uint8Array, off: number, bits: number): number[] {
  const out: number[] = new Array<number>(256);
  let acc = 0;
  let n = 0;
  let p = off;
  const mask = (1 << bits) - 1;
  for (let i = 0; i < 256; i++) {
    while (n < bits) {
      acc += b[p++]! * 2 ** n; // exact: acc stays below 2^28
      n += 8;
    }
    out[i] = acc & mask;
    acc = Math.floor(acc / 2 ** bits);
    n -= bits;
  }
  return out;
}

/** HintBitUnpack (Algorithm 21), or null when the encoding is malformed. */
function hintUnpack(y: Uint8Array, off: number): Uint8Array[] | null {
  const h: Uint8Array[] = [];
  let index = 0;
  for (let i = 0; i < K; i++) {
    const hi = new Uint8Array(256);
    const end = y[off + OMEGA + i]!;
    if (end < index || end > OMEGA) return null;
    const first = index;
    while (index < end) {
      if (index > first && y[off + index - 1]! >= y[off + index]!) return null;
      hi[y[off + index]!] = 1;
      index++;
    }
    h.push(hi);
  }
  for (let i = index; i < OMEGA; i++) if (y[off + i] !== 0) return null;
  return h;
}

/** UseHint (Algorithm 40) for γ2 = (q − 1)/32. */
function useHint(hint: number, r: number): number {
  let r0 = r % (2 * GAMMA2);
  if (r0 > GAMMA2) r0 -= 2 * GAMMA2;
  let r1: number;
  if (r - r0 === Q - 1) {
    r1 = 0;
    r0 -= 1;
  } else {
    r1 = (r - r0) / (2 * GAMMA2);
  }
  if (hint === 0) return r1;
  return r0 > 0 ? (r1 + 1) % 16 : (r1 + 15) % 16;
}

function concat(...parts: Uint8Array[]): Uint8Array {
  let n = 0;
  for (const p of parts) n += p.length;
  const out = new Uint8Array(n);
  let o = 0;
  for (const p of parts) {
    out.set(p, o);
    o += p.length;
  }
  return out;
}

/** A decoded ML-DSA-65 public key, with tr and Â computed once. */
export interface MLDSA65PublicKey {
  t1: Poly[]; // t1 · 2^d in the NTT domain
  a: Poly[][]; // Â
  tr: Uint8Array;
}

/** pkDecode (Algorithm 23) and the per-key work of Verify: tr = H(pk, 64), Â = ExpandA(ρ). */
export function mldsa65PublicKey(pk: Uint8Array): MLDSA65PublicKey | null {
  if (pk.length !== MLDSA65_PUBLIC_KEY_SIZE) return null;
  const rho = pk.subarray(0, 32);
  const t1: Poly[] = [];
  for (let i = 0; i < K; i++) {
    const c = unpack(pk, 32 + 320 * i, 10);
    const p = new Int32Array(256);
    for (let j = 0; j < 256; j++) p[j] = c[j]! * (1 << D); // < q
    ntt(p);
    t1.push(p);
  }
  const a: Poly[][] = [];
  for (let r = 0; r < K; r++) {
    const row: Poly[] = [];
    for (let s = 0; s < L; s++) row.push(rejNttPoly(concat(rho, new Uint8Array([s, r]))));
    a.push(row);
  }
  return { t1, a, tr: shake256().absorb(pk).squeeze(64) };
}

/**
 * ML-DSA.Verify(pk, M, σ, ctx = "") (Algorithms 3 and 8). msg is the message as signed: for a deed
 * seal, the domain tag and body_hash.
 */
export function mldsa65Verify(pk: MLDSA65PublicKey, msg: Uint8Array, sig: Uint8Array): boolean {
  if (sig.length !== MLDSA65_SIGNATURE_SIZE) return false;
  const ct = sig.subarray(0, LAMBDA_BYTES);
  const z: Poly[] = [];
  for (let i = 0; i < L; i++) {
    const c = unpack(sig, LAMBDA_BYTES + 640 * i, 20);
    const p = new Int32Array(256);
    for (let j = 0; j < 256; j++) {
      const v = GAMMA1 - c[j]!; // in (−γ1, γ1]
      if (Math.abs(v) >= GAMMA1 - BETA) return false; // ||z||∞ < γ1 − β
      p[j] = v < 0 ? v + Q : v;
    }
    ntt(p);
    z.push(p);
  }
  const h = hintUnpack(sig, LAMBDA_BYTES + 640 * L);
  if (h === null) return false;
  // μ = H(tr || M', 64) with M' = 0 || |ctx| || ctx || M and the empty context.
  const mu = shake256().absorb(pk.tr).absorb(new Uint8Array([0, 0])).absorb(msg).squeeze(64);
  const c = sampleInBall(ct);
  ntt(c);
  const w1 = new Uint8Array(K * 128);
  for (let i = 0; i < K; i++) {
    const w = new Int32Array(256);
    for (let s = 0; s < L; s++) {
      const a = pk.a[i]![s]!;
      const zs = z[s]!;
      for (let j = 0; j < 256; j++) w[j] = (w[j]! + mulq(a[j]!, zs[j]!)) % Q;
    }
    const t = pk.t1[i]!;
    for (let j = 0; j < 256; j++) w[j] = (w[j]! - mulq(c[j]!, t[j]!) + Q) % Q;
    invNtt(w);
    const hi = h[i]!;
    for (let j = 0; j < 256; j += 2) {
      w1[i * 128 + j / 2] = useHint(hi[j]!, w[j]!) | (useHint(hi[j + 1]!, w[j + 1]!) << 4);
    }
  }
  const ct2 = shake256().absorb(mu).absorb(w1).squeeze(LAMBDA_BYTES);
  let diff = 0;
  for (let i = 0; i < LAMBDA_BYTES; i++) diff |= ct[i]! ^ ct2[i]!;
  return diff === 0;
}
