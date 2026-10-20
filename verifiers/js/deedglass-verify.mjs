// deedglass-verify.mjs: an independent Deedglass verifier in one ES module, written from spec/ alone
// (TY-10). No dependencies. Runs in Node 20+ and in browsers: WebCrypto for SHA-2 and Ed25519 (with a
// BigInt fallback where a browser lacks Ed25519), its own Keccak and ML-DSA-65 (FIPS 204), and
// DecompressionStream for bundles.
//
//   node deedglass-verify.mjs <bundle.tar.gz | deeds.jsonl | deed.json> [--keys keys.json] [--strict] [--json]
//   node deedglass-verify.mjs --vectors <testdata/deeds>
//
//   import { verifyBundle, verifyDeeds, verifyDeed, parseKeySet } from './deedglass-verify.mjs';
//
// Exit 0 when everything verifies, 1 on a failure, 2 when it cannot decide (DEED_SPEC.md §11.3).

export const VERIFIER = 'deedglass-verify (JavaScript) 0.1.0';

const te = new TextEncoder();
const utf8 = (s) => te.encode(s);
const MAX_INT = 9007199254740991;

// ---- errors (§11.3) ------------------------------------------------------------------------------

export const CANNOT_DETERMINE = new Set(['keys_missing', 'unsupported_version', 'unsupported_algorithm']);

const MESSAGES = {
  malformed: ['Deed {seq} is not valid strict JSON ({detail}).', 'Export it again from the issuer; do not edit deeds by hand.'],
  unsupported_version: ["Deed {seq} uses deed format v{v}, which this verifier can't read.", 'Use a verifier that reads that format.'],
  schema_invalid: ['Deed {seq} has an invalid field: {field}.', 'Export it again from the issuer.'],
  id_mismatch: ["Deed {seq}'s id does not match its body.", 'Export it again from the issuer.'],
  body_hash_mismatch: ['Deed {seq} was changed after it was sealed.', 'Get a fresh export from the issuer and compare deed {seq}.'],
  seal_key_mismatch: ['Deed {seq} names two different seal keys.', 'Treat this deed as altered.'],
  seal_key_unknown: ['Deed {seq} names seal key {key}, which is not in the key set.', "Fetch the issuer's keys: --keys <issuer>/.well-known/deedglass/keys.json"],
  seal_key_rejected: ['The key set contains an unsafe key ({key}).', 'Do not use this key set. Fetch it again from the issuer.'],
  seal_key_not_valid_at: ["Deed {seq} was sealed at {issued_at}, outside seal key {key}'s active window.", "Treat this deed as altered, or check you have the issuer's full key set."],
  seal_key_revoked: ['Deed {seq} was sealed by revoked key {key}.', 'Ask the issuer which deeds they re-sealed after the revocation.'],
  seal_invalid: ["Deed {seq}'s seal does not match its contents.", 'Treat this deed as altered.'],
  non_canonical: ['Deed {seq} is not in canonical form (--strict).', 'Re-export it; canonical bytes come straight from the Ledger.'],
  keys_missing: ['Cannot check deed {seq}: no seal keys were given.', 'Add --keys <keys.json>. Issuers publish them at /.well-known/deedglass/keys.json'],
  bundle_invalid: ['{detail} Nothing was extracted.', 'Do not use this file. Ask the issuer for a new export.'],
  bundle_seal_invalid: ["The bundle manifest's seal does not match the manifest.", 'Treat this bundle as altered. Ask the issuer for a new export.'],
  chain_genesis_mismatch: ["Deed 1 does not start this org's chain.", 'Check the org id; the export may belong to another org.'],
  chain_seq_gap: ['Deed {want} is missing. The chain jumps from {prev} to {seq}.', 'A deed was removed or the export is incomplete. Ask the issuer for seq {prev}-{seq}.'],
  chain_seq_out_of_order: ['Deed {seq} appears out of order (after {prev}).', 'Sort by seq, or export again.'],
  chain_prev_hash_mismatch: ['Deed {seq} does not link to deed {prev}.', 'A deed before it was changed or replaced. Get a fresh export.'],
  chain_org_mismatch: ['Deed {seq} belongs to {org}, not {want_org}.', 'Verify each org\'s deeds separately.'],
  chain_time_regression: ['Deed {seq} was sealed before deed {prev}.', 'Treat the chain as altered from deed {seq}.'],
  unsupported_algorithm: ["Deed {seq} is sealed with an algorithm this verifier can't check ({detail}).", 'Check it with a verifier that supports that algorithm.'],
  seal_missing: ['Deed {seq} is missing a seal it must carry ({detail}).', 'Treat this deed as altered, or ask the issuer for the deed as it was sealed.'],
};

export class VerifyError extends Error {
  constructor(code, f = {}) {
    super(code);
    this.code = code;
    this.seq = f.seq ?? null;
    this.field = f.field ?? null;
    this.f = f;
  }
  message2() {
    let [text, next] = MESSAGES[this.code];
    const vals = { ...this.f, seq: this.seq, field: this.field };
    if (this.code === 'chain_seq_gap' && vals.prev === undefined) {
      text = 'The deeds start at deed {seq}, not deed 1.';
      next = 'A deed was removed or the export is incomplete.';
    }
    if (this.seq === null) text = text.replace("Deed {seq}'s", "The deed's").replace('Deed {seq}', 'The deed').replace('deed {seq}', 'the deed');
    const fill = (s) => s.replace(/\{(\w+)\}/g, (_, k) => (vals[k] === undefined || vals[k] === null ? '?' : String(vals[k])));
    return [fill(text), fill(next)];
  }
}

// ---- strict JSON (§6) and JCS (RFC 8785) ------------------------------------------------------------

class StrictError extends Error {}

/** Parses bytes under the strict profile. Objects have no prototype; integers are Numbers. */
export function parseStrict(bytes) {
  let s;
  try {
    s = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
  } catch {
    throw new StrictError('invalid UTF-8');
  }
  let i = 0;
  const ws = () => {
    while (i < s.length && (s[i] === ' ' || s[i] === '\t' || s[i] === '\n' || s[i] === '\r')) i++;
  };
  const bad = (m) => new StrictError(m);
  const str = () => {
    i++; // opening quote
    let out = '';
    for (;;) {
      if (i >= s.length) throw bad('an unterminated string');
      const c = s.charCodeAt(i);
      if (c === 0x22) {
        i++;
        break;
      }
      if (c < 0x20) throw bad('a control character in a string');
      if (c === 0x5c) {
        const e = s[i + 1];
        i += 2;
        if (e === 'u') {
          const hex = s.slice(i, i + 4);
          if (!/^[0-9a-fA-F]{4}$/.test(hex)) throw bad('a bad \\u escape');
          i += 4;
          let cp = parseInt(hex, 16);
          if (cp >= 0xd800 && cp <= 0xdbff) {
            const lo = s.slice(i, i + 6);
            if (!/^\\u[dD][c-fC-F][0-9a-fA-F]{2}$/.test(lo)) throw bad('a lone UTF-16 surrogate');
            i += 6;
            cp = 0x10000 + ((cp - 0xd800) << 10) + (parseInt(lo.slice(2), 16) - 0xdc00);
          } else if (cp >= 0xdc00 && cp <= 0xdfff) {
            throw bad('a lone UTF-16 surrogate');
          }
          out += String.fromCodePoint(cp);
        } else {
          const m = { '"': '"', '\\': '\\', '/': '/', b: '\b', f: '\f', n: '\n', r: '\r', t: '\t' }[e];
          if (m === undefined) throw bad('a bad escape');
          out += m;
        }
        continue;
      }
      out += s[i];
      i++;
    }
    return out;
  };
  const value = (depth) => {
    ws();
    const c = s[i];
    if (c === '{' || c === '[') {
      if (depth > 16) throw bad('nesting deeper than 16 levels');
      i++;
      ws();
      if (c === '{') {
        const o = Object.create(null);
        if (s[i] === '}') {
          i++;
          return o;
        }
        for (;;) {
          ws();
          if (s[i] !== '"') throw bad('an object key that is not a string');
          const k = str();
          if (Object.prototype.hasOwnProperty.call(o, k)) throw bad(`a duplicate member "${k}"`);
          ws();
          if (s[i] !== ':') throw bad('a missing colon');
          i++;
          o[k] = value(depth + 1);
          ws();
          if (s[i] === ',') {
            i++;
            continue;
          }
          if (s[i] === '}') {
            i++;
            return o;
          }
          throw bad('a bad object');
        }
      }
      const a = [];
      if (s[i] === ']') {
        i++;
        return a;
      }
      for (;;) {
        a.push(value(depth + 1));
        ws();
        if (s[i] === ',') {
          i++;
          continue;
        }
        if (s[i] === ']') {
          i++;
          return a;
        }
        throw bad('a bad array');
      }
    }
    if (c === '"') return str();
    if (s.startsWith('true', i)) return (i += 4), true;
    if (s.startsWith('false', i)) return (i += 5), false;
    if (s.startsWith('null', i)) return (i += 4), null;
    const m = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?/.exec(s.slice(i, i + 400));
    if (!m || m[0] === '' || m[0] === '-') throw bad('not JSON');
    if (m[0].startsWith('-')) throw bad('a negative number');
    if (m[2] || m[3]) throw bad('a number with a fraction or an exponent');
    if (m[1].length > 16 || Number(m[1]) > MAX_INT) throw bad('an integer above 2^53-1');
    i += m[0].length;
    return Number(m[1]);
  };
  const v = value(1);
  ws();
  if (i !== s.length) throw bad('data after the value');
  return v;
}

const isObj = (v) => typeof v === 'object' && v !== null && !Array.isArray(v);

function jcsString(s) {
  let out = '"';
  for (const ch of s) {
    const c = ch.codePointAt(0);
    if (ch === '"') out += '\\"';
    else if (ch === '\\') out += '\\\\';
    else if (ch === '\b') out += '\\b';
    else if (ch === '\f') out += '\\f';
    else if (ch === '\n') out += '\\n';
    else if (ch === '\r') out += '\\r';
    else if (ch === '\t') out += '\\t';
    else if (c < 0x20) out += '\\u' + c.toString(16).padStart(4, '0');
    else out += ch;
  }
  return out + '"';
}

/** RFC 8785 text of a strict value (strings, integers, booleans, null, arrays, objects). */
export function jcs(v) {
  if (v === null) return 'null';
  if (v === true) return 'true';
  if (v === false) return 'false';
  if (typeof v === 'number') return String(v);
  if (typeof v === 'string') return jcsString(v);
  if (Array.isArray(v)) return '[' + v.map(jcs).join(',') + ']';
  const keys = Object.keys(v).sort(); // UTF-16 code unit order, as RFC 8785 requires
  return '{' + keys.map((k) => jcsString(k) + ':' + jcs(v[k])).join(',') + '}';
}

// ---- hashing, base64url, hex -----------------------------------------------------------------------

const subtle = globalThis.crypto?.subtle;

async function sha(alg, bytes) {
  return new Uint8Array(await subtle.digest(alg, bytes));
}
const hex = (b) => Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
const sha256hex = async (bytes) => hex(await sha('SHA-256', bytes));
const digestOf = async (bytes) => 'sha256:' + (await sha256hex(bytes));

const B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
const B64I = new Map([...B64].map((c, i) => [c, i]));

/** RFC 4648 §5 without padding; null for anything else, including non-zero trailing bits. */
export function b64urlDecode(s) {
  if (typeof s !== 'string' || s.length % 4 === 1) return null;
  const out = new Uint8Array(Math.floor((s.length * 6) / 8));
  let acc = 0;
  let bits = 0;
  let j = 0;
  for (const c of s) {
    const v = B64I.get(c);
    if (v === undefined) return null;
    acc = (acc << 6) | v;
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      out[j++] = (acc >> bits) & 0xff;
    }
  }
  if ((acc & ((1 << bits) - 1)) !== 0) return null;
  return out;
}

// ---- Ed25519 (RFC 8032 §5.1): key checks in BigInt, signatures by WebCrypto or BigInt ----------------

const P = 2n ** 255n - 19n;
const L = 2n ** 252n + 27742317777372353535851937790883648493n;
const mod = (a, m = P) => ((a % m) + m) % m;
function powmod(b, e, m = P) {
  let r = 1n;
  b = mod(b, m);
  while (e > 0n) {
    if (e & 1n) r = (r * b) % m;
    b = (b * b) % m;
    e >>= 1n;
  }
  return r;
}
const D = mod(-121665n * powmod(121666n, P - 2n));
const SQRT_M1 = powmod(2n, (P - 1n) / 4n);

function recoverX(y, sign) {
  const x2 = mod((y * y - 1n) * powmod(D * y * y + 1n, P - 2n));
  if (x2 === 0n) return sign ? null : 0n;
  let x = powmod(x2, (P + 3n) / 8n);
  if (mod(x * x - x2) !== 0n) x = mod(x * SQRT_M1);
  if (mod(x * x - x2) !== 0n) return null;
  if (Number(x & 1n) !== sign) x = P - x;
  return x;
}
function padd([x1, y1, z1, t1], [x2, y2, z2, t2]) {
  const a = mod((y1 - x1) * (y2 - x2));
  const b = mod((y1 + x1) * (y2 + x2));
  const c = mod(2n * t1 * D * t2);
  const d = mod(2n * z1 * z2);
  const e = b - a, f = d - c, g = d + c, h = b + a;
  return [mod(e * f), mod(g * h), mod(f * g), mod(e * h)];
}
function pmul(s, p) {
  let q = [0n, 1n, 1n, 0n];
  while (s > 0n) {
    if (s & 1n) q = padd(q, p);
    p = padd(p, p);
    s >>= 1n;
  }
  return q;
}
const BY = mod(4n * powmod(5n, P - 2n));
const BX = recoverX(BY, 0);
const BASE = [BX, BY, 1n, mod(BX * BY)];
const leInt = (b) => b.reduceRight((acc, x) => (acc << 8n) | BigInt(x), 0n);

/** Decodes a point; throws a reason for a non-canonical, off-curve or wrongly sized encoding. */
function decodePoint(b) {
  if (b.length !== 32) throw new Error(`a public key of ${b.length} bytes, not 32`);
  let y = leInt(b);
  const sign = Number(y >> 255n);
  y &= (1n << 255n) - 1n;
  if (y >= P) throw new Error('a non-canonical encoding (y >= p)');
  const x = recoverX(y, sign);
  if (x === null) throw new Error(sign && mod(y * y - 1n) === 0n ? 'a non-canonical encoding (x = 0 with the sign bit set)' : 'a point that is not on the curve');
  return [x, y, 1n, mod(x * y)];
}
function encodePoint([x, y, z]) {
  const zi = powmod(z, P - 2n);
  const xa = mod(x * zi);
  let v = mod(y * zi) | ((xa & 1n) << 255n);
  const out = new Uint8Array(32);
  for (let i = 0; i < 32; i++, v >>= 8n) out[i] = Number(v & 0xffn);
  return out;
}

/** §10.2: 32 bytes, canonical, on the curve, and not of small order (8A is the identity). */
function checkEd25519Key(b) {
  const [x, y, z] = pmul(8n, decodePoint(b));
  if (mod(x) === 0n && mod(y - z) === 0n) throw new Error('a small-order point');
}

let webcryptoEd25519 = undefined; // unknown until first use

async function ed25519Verify(pub, msg, sig) {
  if (sig.length !== 64 || pub.length !== 32) return false;
  if (leInt(sig.slice(32)) >= L) return false; // RFC 8032 §5.1.7: refuse S >= L before any engine
  if (webcryptoEd25519 !== false && subtle) {
    try {
      const k = await subtle.importKey('raw', pub, { name: 'Ed25519' }, false, ['verify']);
      webcryptoEd25519 = true;
      return await subtle.verify({ name: 'Ed25519' }, k, sig, msg);
    } catch (e) {
      if (webcryptoEd25519 === true) return false;
      webcryptoEd25519 = false;
    }
  }
  // Cofactorless, comparing encodings: R' = [S]B - [k]A must encode to R.
  let a;
  try {
    a = decodePoint(pub);
  } catch {
    return false;
  }
  const r = sig.slice(0, 32);
  const h = await sha('SHA-512', concat(r, pub, msg));
  const k = mod(leInt(h), L);
  const [x, y, z, t] = pmul(k, a);
  const rp = encodePoint(padd(pmul(leInt(sig.slice(32)), BASE), [mod(-x), y, z, mod(-t)]));
  return rp.every((v, i) => v === r[i]);
}

function concat(...parts) {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let o = 0;
  for (const p of parts) out.set(p, (o += p.length) - p.length);
  return out;
}

// ---- Keccak and SHAKE (FIPS 202) -------------------------------------------------------------------

const RC = [
  [0x00000001, 0x00000000], [0x00008082, 0x00000000], [0x0000808a, 0x80000000], [0x80008000, 0x80000000],
  [0x0000808b, 0x00000000], [0x80000001, 0x00000000], [0x80008081, 0x80000000], [0x00008009, 0x80000000],
  [0x0000008a, 0x00000000], [0x00000088, 0x00000000], [0x80008009, 0x00000000], [0x8000000a, 0x00000000],
  [0x8000808b, 0x00000000], [0x0000008b, 0x80000000], [0x00008089, 0x80000000], [0x00008003, 0x80000000],
  [0x00008002, 0x80000000], [0x00000080, 0x80000000], [0x0000800a, 0x00000000], [0x8000000a, 0x80000000],
  [0x80008081, 0x80000000], [0x00008080, 0x80000000], [0x80000001, 0x00000000], [0x80008008, 0x80000000],
];
const ROT = [0, 1, 62, 28, 27, 36, 44, 6, 55, 20, 3, 10, 43, 25, 39, 41, 45, 15, 21, 8, 18, 2, 61, 56, 14];

/** Keccak-f[1600] over 25 lanes held as 50 uint32 (lo, hi) in s. */
function keccakF(s) {
  const c = new Uint32Array(10);
  const b = new Uint32Array(50);
  for (let round = 0; round < 24; round++) {
    for (let x = 0; x < 5; x++) {
      c[2 * x] = s[2 * x] ^ s[2 * x + 10] ^ s[2 * x + 20] ^ s[2 * x + 30] ^ s[2 * x + 40];
      c[2 * x + 1] = s[2 * x + 1] ^ s[2 * x + 11] ^ s[2 * x + 21] ^ s[2 * x + 31] ^ s[2 * x + 41];
    }
    for (let x = 0; x < 5; x++) {
      const x1 = (x + 1) % 5, x4 = (x + 4) % 5;
      const lo = c[2 * x4] ^ ((c[2 * x1] << 1) | (c[2 * x1 + 1] >>> 31));
      const hi = c[2 * x4 + 1] ^ ((c[2 * x1 + 1] << 1) | (c[2 * x1] >>> 31));
      for (let y = 0; y < 25; y += 5) {
        s[2 * (x + y)] ^= lo;
        s[2 * (x + y) + 1] ^= hi;
      }
    }
    for (let x = 0; x < 5; x++) {
      for (let y = 0; y < 5; y++) {
        const i = x + 5 * y;
        const r = ROT[i];
        let lo = s[2 * i], hi = s[2 * i + 1];
        if (r >= 32) [lo, hi] = [hi, lo];
        const rr = r % 32;
        const nlo = rr ? (lo << rr) | (hi >>> (32 - rr)) : lo;
        const nhi = rr ? (hi << rr) | (lo >>> (32 - rr)) : hi;
        const j = y + 5 * ((2 * x + 3 * y) % 5);
        b[2 * j] = nlo;
        b[2 * j + 1] = nhi;
      }
    }
    for (let y = 0; y < 25; y += 5) {
      for (let x = 0; x < 5; x++) {
        const i = 2 * (x + y), i1 = 2 * (((x + 1) % 5) + y), i2 = 2 * (((x + 2) % 5) + y);
        s[i] = b[i] ^ (~b[i1] & b[i2]);
        s[i + 1] = b[i + 1] ^ (~b[i1 + 1] & b[i2 + 1]);
      }
    }
    s[0] ^= RC[round][0];
    s[1] ^= RC[round][1];
  }
}

/** A SHAKE XOF: absorb the whole input, then squeeze any number of bytes. rate 168 or 136. */
function shake(rate, input) {
  const s = new Uint32Array(50);
  const st = new Uint8Array(s.buffer);
  const padded = new Uint8Array(Math.floor(input.length / rate) * rate + rate);
  padded.set(input);
  padded[input.length] ^= 0x1f;
  padded[padded.length - 1] ^= 0x80;
  for (let o = 0; o < padded.length; o += rate) {
    for (let i = 0; i < rate; i++) st[i] ^= padded[o + i];
    keccakF(s);
  }
  let pos = 0;
  return (n) => {
    const out = new Uint8Array(n);
    for (let i = 0; i < n; i++) {
      if (pos === rate) {
        keccakF(s);
        pos = 0;
      }
      out[i] = st[pos++];
    }
    return out;
  };
}
const shake128 = (input) => shake(168, input);
const shake256 = (input) => shake(136, input);
if (new Uint8Array(new Uint32Array([1]).buffer)[0] !== 1) throw new Error('deedglass-verify needs a little-endian platform');

// ---- ML-DSA-65 (FIPS 204), verification only ---------------------------------------------------------

const Q = 8380417;
const MK = 6, ML = 5, TAU = 49, GAMMA1 = 1 << 19, GAMMA2 = (Q - 1) / 32, BETA = 196, OMEGA = 55, CLEN = 48;
export const MLDSA_PK_BYTES = 32 + MK * 320;
export const MLDSA_SIG_BYTES = CLEN + ML * 640 + OMEGA + MK;

function mulmod(a, b) {
  // a, b < 2^23: the product stays below 2^46, exact in a double.
  return (a * b) % Q;
}
const ZETAS = (() => {
  const z = new Array(256);
  for (let i = 0; i < 256; i++) {
    let r = 0;
    for (let b = 0; b < 8; b++) r |= ((i >> b) & 1) << (7 - b);
    let v = 1;
    for (let k = 0; k < r; k++) v = mulmod(v, 1753);
    z[i] = v;
  }
  return z;
})();
const N_INV = 8347681;

function ntt(a) {
  const w = a.slice();
  let m = 0;
  for (let len = 128; len >= 1; len >>= 1) {
    for (let start = 0; start < 256; start += 2 * len) {
      const z = ZETAS[++m];
      for (let j = start; j < start + len; j++) {
        const t = mulmod(z, w[j + len]);
        w[j + len] = (w[j] - t + Q) % Q;
        w[j] = (w[j] + t) % Q;
      }
    }
  }
  return w;
}
function intt(a) {
  const w = a.slice();
  let m = 256;
  for (let len = 1; len < 256; len <<= 1) {
    for (let start = 0; start < 256; start += 2 * len) {
      const z = Q - ZETAS[--m];
      for (let j = start; j < start + len; j++) {
        const t = w[j];
        w[j] = (t + w[j + len]) % Q;
        w[j + len] = mulmod(z, (t - w[j + len] + Q) % Q);
      }
    }
  }
  return w.map((x) => mulmod(x, N_INV));
}
function rejNTTPoly(seed) {
  const xof = shake128(seed);
  const out = [];
  while (out.length < 256) {
    const b = xof(3);
    const c = b[0] | (b[1] << 8) | ((b[2] & 0x7f) << 16);
    if (c < Q) out.push(c);
  }
  return out;
}
const expandCache = new Map();
function expandA(rho) {
  const key = hex(rho);
  let a = expandCache.get(key);
  if (!a) {
    a = [];
    for (let r = 0; r < MK; r++) {
      a.push([]);
      for (let s = 0; s < ML; s++) a[r].push(rejNTTPoly(concat(rho, new Uint8Array([s, r]))));
    }
    expandCache.set(key, a);
  }
  return a;
}
function sampleInBall(ctilde) {
  const xof = shake256(ctilde);
  const sb = xof(8);
  let signs = 0n;
  for (let i = 7; i >= 0; i--) signs = (signs << 8n) | BigInt(sb[i]);
  const c = new Array(256).fill(0);
  for (let i = 256 - TAU; i < 256; i++) {
    let j;
    do j = xof(1)[0];
    while (j > i);
    c[i] = c[j];
    c[j] = (signs >> BigInt(i + TAU - 256)) & 1n ? Q - 1 : 1;
  }
  return c;
}
function unpack(b, off, bits) {
  const out = new Array(256);
  let acc = 0, n = 0, p = off;
  const mask = (1 << bits) - 1;
  for (let i = 0; i < 256; i++) {
    while (n < bits) {
      acc += b[p++] * 2 ** n;
      n += 8;
    }
    out[i] = acc % (mask + 1);
    acc = Math.floor(acc / (mask + 1));
    n -= bits;
  }
  return out;
}
function hintUnpack(y) {
  const h = Array.from({ length: MK }, () => new Array(256).fill(0));
  let index = 0;
  for (let i = 0; i < MK; i++) {
    const end = y[OMEGA + i];
    if (end < index || end > OMEGA) return null;
    const first = index;
    while (index < end) {
      if (index > first && y[index - 1] >= y[index]) return null;
      h[i][y[index]] = 1;
      index++;
    }
  }
  for (let i = index; i < OMEGA; i++) if (y[i] !== 0) return null;
  return h;
}
function useHint(h, r) {
  const rp = ((r % Q) + Q) % Q;
  let r0 = rp % (2 * GAMMA2);
  if (r0 > GAMMA2) r0 -= 2 * GAMMA2;
  let r1;
  if (rp - r0 === Q - 1) {
    r1 = 0;
    r0 -= 1;
  } else r1 = (rp - r0) / (2 * GAMMA2);
  if (h === 1) return r0 > 0 ? (r1 + 1) % 16 : (r1 + 15) % 16;
  return r1;
}

/** ML-DSA.Verify (FIPS 204 Algorithm 3) for ML-DSA-65 with a context string (empty by default). */
export function mldsa65Verify(pk, msg, sig, ctx = new Uint8Array(0)) {
  if (pk.length !== MLDSA_PK_BYTES || sig.length !== MLDSA_SIG_BYTES || ctx.length > 255) return false;
  const mp = concat(new Uint8Array([0, ctx.length]), ctx, msg);
  const rho = pk.slice(0, 32);
  const t1 = [];
  for (let i = 0; i < MK; i++) t1.push(unpack(pk, 32 + 320 * i, 10));
  const ctilde = sig.slice(0, CLEN);
  const z = [];
  for (let i = 0; i < ML; i++) z.push(unpack(sig, CLEN + 640 * i, 20).map((x) => GAMMA1 - x));
  const h = hintUnpack(sig.slice(CLEN + 640 * ML));
  if (h === null) return false;
  for (const zi of z) for (const x of zi) if (Math.abs(x) >= GAMMA1 - BETA) return false;
  const a = expandA(rho);
  const tr = shake256(pk)(64);
  const mu = shake256(concat(tr, mp))(64);
  const cHat = ntt(sampleInBall(ctilde));
  const zHat = z.map((zi) => ntt(zi.map((x) => ((x % Q) + Q) % Q)));
  const w1 = new Uint8Array(MK * 128);
  for (let i = 0; i < MK; i++) {
    const acc = new Array(256).fill(0);
    for (let j = 0; j < ML; j++) for (let n = 0; n < 256; n++) acc[n] = (acc[n] + mulmod(a[i][j][n], zHat[j][n])) % Q;
    const t1Hat = ntt(t1[i].map((x) => (x * 8192) % Q));
    for (let n = 0; n < 256; n++) acc[n] = (acc[n] - mulmod(cHat[n], t1Hat[n]) + Q) % Q;
    const w = intt(acc);
    for (let n = 0; n < 256; n += 2) w1[i * 128 + n / 2] = useHint(h[i][n], w[n]) | (useHint(h[i][n + 1], w[n + 1]) << 4);
  }
  const c2 = shake256(concat(mu, w1))(CLEN);
  return c2.every((v, i) => v === ctilde[i]);
}

// ---- key sets (§10) --------------------------------------------------------------------------------

const RE_ALG = /^[A-Za-z0-9][A-Za-z0-9-]{0,31}$/;
const RE_TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/;
const idRe = (p) => new RegExp(`^${p}_[0-7][0-9a-hjkmnp-tv-z]{25}$`);
const IDS = Object.fromEntries(['deed', 'org', 'agt', 'act', 'con', 'pol', 'pv', 'apr', 'usr', 'adm', 'sk'].map((p) => [p, idRe(p)]));
const isId = (p, v) => typeof v === 'string' && IDS[p].test(v);
export const IMPLEMENTED = ['Ed25519', 'ML-DSA-65'];
const TEST_THUMBPRINTS = new Set([
  'sha256:f6b1d24a4efdc39230905e0c217093f4d1d5e301ac52c9e763a773e864d93f33',
  'sha256:3d3876ffac00eac374b0e6e5735718edfcc7637ace18ef40a3ccc492cf98bf60',
  'sha256:fdfb7b7d83630f6ada1d5000d134792652f8a149a1af90ea5a2dd542651b1112',
]);

/** The time pattern of §4 and a real UTC instant. Valid times compare correctly as text. */
export function validTime(s) {
  if (typeof s !== 'string' || !RE_TIME.test(s)) return false;
  const [y, mo, d, h, mi, se] = [s.slice(0, 4), s.slice(5, 7), s.slice(8, 10), s.slice(11, 13), s.slice(14, 16), s.slice(17, 19)].map(Number);
  if (mo < 1 || mo > 12 || h > 23 || mi > 59 || se > 59 || d < 1) return false;
  const leap = (y % 4 === 0 && y % 100 !== 0) || y % 400 === 0;
  return d <= [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][mo - 1];
}

const sameJSON = (a, b) => jcs(a) === jcs(b);

/** Parses and checks a whole key set; any unsafe key refuses the set (seal_key_rejected). */
export async function parseKeySet(bytes) {
  const reject = (key, detail) => new VerifyError('seal_key_rejected', { key, detail });
  let v;
  try {
    v = parseStrict(bytes);
  } catch (e) {
    throw reject('keys.json', `not strict JSON (${e.message})`);
  }
  if (!isObj(v) || v.object !== 'seal_key_set' || !Array.isArray(v.keys)) throw reject('keys.json', 'not a seal_key_set');
  const keys = new Map();
  const want = ['object', 'id', 'alg', 'public_key', 'thumbprint', 'status', 'activated_at', 'retired_at', 'revoked_at', 'jwk'];
  for (let i = 0; i < v.keys.length; i++) {
    const k = v.keys[i];
    if (!isObj(k)) throw reject(`keys[${i}]`, 'a key that is not an object');
    const label = typeof k.id === 'string' ? k.id : `keys[${i}]`;
    const extra = Object.keys(k).filter((x) => !want.includes(x));
    if (extra.length || want.some((x) => !(x in k))) throw reject(label, 'members that are not as the format describes');
    if (k.object !== 'seal_key' || !isId('sk', k.id)) throw reject(label, 'an id that is not a seal key id');
    if (typeof k.alg !== 'string' || !RE_ALG.test(k.alg)) throw reject(label, "an alg that doesn't match the pattern");
    const pub = b64urlDecode(k.public_key);
    if (!pub || pub.length === 0) throw reject(label, 'a public_key that is not base64url');
    if (k.thumbprint !== (await digestOf(pub))) throw reject(label, 'a thumbprint that is not the SHA-256 of the public key');
    const { status, activated_at: act, retired_at: ret, revoked_at: rev } = k;
    if (!validTime(act) || !(ret === null || validTime(ret)) || !(rev === null || validTime(rev))) throw reject(label, 'a time that is not valid');
    const ok = (status === 'active' && ret === null && rev === null) || (status === 'retired' && ret !== null && rev === null) || (status === 'revoked' && rev !== null);
    if (!ok) throw reject(label, 'a status that contradicts its times');
    const jwk = k.jwk;
    if (!isObj(jwk)) throw reject(label, 'a jwk that is not an object');
    if (k.alg === 'Ed25519') {
      if (!sameJSON(jwk, { kty: 'OKP', crv: 'Ed25519', x: k.public_key, kid: k.id, use: 'sig' })) throw reject(label, 'a jwk that differs from the key');
      try {
        checkEd25519Key(pub);
      } catch (e) {
        throw reject(label, e.message);
      }
    } else if (k.alg === 'ML-DSA-65') {
      if (!sameJSON(jwk, { kty: 'AKP', alg: 'ML-DSA-65', pub: k.public_key, kid: k.id, use: 'sig' })) throw reject(label, 'a jwk that differs from the key');
      if (pub.length !== MLDSA_PK_BYTES) throw reject(label, `an ML-DSA-65 public key of ${pub.length} bytes`);
    } else {
      const allowed = ['kty', 'crv', 'x', 'alg', 'pub', 'kid', 'use'];
      if (Object.keys(jwk).some((x) => !allowed.includes(x)) || jwk.kid !== k.id || jwk.use !== 'sig') throw reject(label, 'a jwk that differs from the key');
      if (typeof jwk.kty !== 'string' || jwk.kty === '') throw reject(label, 'a jwk without kty');
    }
    if (keys.has(k.id)) throw reject(k.id, 'an id shared with another key');
    keys.set(k.id, { id: k.id, alg: k.alg, pub, thumbprint: k.thumbprint, status, act, ret, rev, revoked: status === 'revoked' || rev !== null });
  }
  return { keys, issuer: typeof v.issuer === 'string' ? v.issuer : null };
}
const activeAt = (k, t) => k.act <= t && (k.ret === null || t < k.ret);

// ---- one deed (§4, §5, §9, §11.1) -------------------------------------------------------------------
//
// §11.1 check 5 names what the closed schema covers but not the order inside it, and the field a
// failure names depends on it. The published vectors (the parity corpora above all) pin it:
// (1) the envelope's members, then its members' types, a v2 envelope's seals completely;
// (2) the body's members, then types in a fixed reading order, each nested object's members when it
// is read; (3) the body's values, in the order of bodyValues; (4) body_hash, then (v1) seal and key id.

const DIGEST = /^sha256:[0-9a-f]{64}$/;
const HEADER_REF = /^[A-Za-z0-9._~:@/+=-]{1,256}$/;
const RULE_ID = /^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/;
const ERROR_CODE = /^[a-z][a-z0-9_]{0,63}$/;
const METHOD = /^[a-z0-9][a-z0-9._-]{0,63}$/;
const CURRENCY = /^[a-z]{3}$/;
const ADMIN_ACTION = /^[a-z][a-z_]{0,30}\.[a-z][a-z_]{0,30}$/;
const REF_TYPE = /^[a-z][a-z_]{0,31}$/;
const REF_ID = /^[a-z][a-z0-9_-]{0,63}$/;
const GENERIC_SEAL = /^[a-z0-9][a-z0-9-]{0,31}:[A-Za-z0-9_-]+$/;
const PREFIX = { Ed25519: 'ed25519:', 'ML-DSA-65': 'ml-dsa-65:' };
const SEAL_CHARS = { Ed25519: 86, 'ML-DSA-65': 4412 };
const TAG_V1 = 'deedglass/deed/v1\n';
const TAGS_V2 = { Ed25519: 'deedglass/deed/v2/ed25519\n', 'ML-DSA-65': 'deedglass/deed/v2/ml-dsa-65\n' };
const ED25519_ALONE_BEFORE = '2035-01-01T00:00:00.000Z';
const MAX_INPUT = 64 * 1024;
const MAX_CANON = { 1: 16384, 2: 24576 };

const ENVELOPE_KEYS = { 1: ['object', 'id', 'body', 'body_hash', 'seal', 'seal_key_id'], 2: ['object', 'id', 'body', 'body_hash', 'seals'] };
const BODY_KEYS = ['v', 'kind', 'deed_id', 'org', 'seq', 'prev_hash', 'issued_at', 'agent', 'on_behalf_of', 'session', 'action', 'policy', 'approval', 'execution', 'witness', 'cost', 'admin', 'amends'];
const AGENT = ['id', 'name'];
const ACTION = ['id', 'tool', 'connector', 'mode', 'request_digest', 'intent_hash', 'intent_signed'];
const POLICY = ['id', 'version_id', 'version_hash', 'mode', 'decision', 'rule_id'];
const APPROVAL = ['id', 'outcome', 'decided_by', 'decided_at', 'deed'];
const EXECUTION = ['status', 'started_at', 'finished_at', 'latency_ms', 'result_digest', 'error_code'];
const WITNESS = ['level', 'verdict', 'method', 'detail_digest'];
const COST = ['amount', 'currency'];
const ADMIN = ['event', 'action', 'actor', 'target', 'reason', 'data_digest'];
const REF = ['type', 'id'];

class Bad extends Error {
  constructor(field) {
    super(field);
    this.field = field;
  }
}
const at = (path, k) => (path ? `${path}.${k}` : k);
const isInt = (v) => typeof v === 'number';
const m = (re, v) => typeof v === 'string' && re.test(v);

function cmpCodePoints(a, b) {
  const x = [...a], y = [...b];
  for (let i = 0; i < Math.min(x.length, y.length); i++) {
    const d = x[i].codePointAt(0) - y[i].codePointAt(0);
    if (d) return d;
  }
  return x.length - y.length;
}
function closed(o, path, keys) {
  if (!isObj(o)) throw new Bad(path || '(top level)');
  const unknown = Object.keys(o).filter((k) => !keys.includes(k)).sort(cmpCodePoints);
  if (unknown.length) throw new Bad(at(path, unknown[0]));
  for (const k of keys) if (!(k in o)) throw new Bad(at(path, k));
  return o;
}
const S = (o, p, k) => {
  if (typeof o[k] !== 'string') throw new Bad(at(p, k));
};
const NS = (o, p, k) => {
  if (o[k] !== null && typeof o[k] !== 'string') throw new Bad(at(p, k));
};
const I = (o, p, k) => {
  if (!isInt(o[k])) throw new Bad(at(p, k));
};
function OBJ(o, p, k, keys) {
  const v = o[k];
  if (v === null) return null;
  if (!isObj(v)) throw new Bad(at(p, k));
  return closed(v, at(p, k), keys);
}
function plain(s, lo, hi, newline = false) {
  let n = 0;
  for (const ch of s) {
    n++;
    const c = ch.codePointAt(0);
    if ((c < 0x20 && !(newline && c === 0x0a)) || (c >= 0x7f && c <= 0x9f)) return false;
  }
  return n >= lo && n <= hi;
}
function sealShape(alg, seal) {
  if (PREFIX[alg] !== undefined) {
    const rest = seal.slice(PREFIX[alg].length);
    return seal.startsWith(PREFIX[alg]) && rest.length === SEAL_CHARS[alg] && /^[A-Za-z0-9_-]+$/.test(rest);
  }
  return GENERIC_SEAL.test(seal) && seal.startsWith(alg.toLowerCase() + ':');
}

function envelopeTypes(e, ver) {
  closed(e, '', ENVELOPE_KEYS[ver]);
  S(e, '', 'id');
  S(e, '', 'body_hash');
  if (ver === 1) {
    S(e, '', 'seal');
    S(e, '', 'seal_key_id');
    return;
  }
  const seals = e.seals;
  if (!Array.isArray(seals) || seals.length < 1 || seals.length > 4) throw new Bad('seals');
  seals.forEach((x, i) => {
    const q = `seals[${i}]`;
    closed(x, q, ['alg', 'seal', 'seal_key_id']);
    for (const k of ['alg', 'seal', 'seal_key_id']) S(x, q, k);
    if (!RE_ALG.test(x.alg)) throw new Bad(q + '.alg');
    if (!sealShape(x.alg, x.seal)) throw new Bad(q + '.seal');
    if (!isId('sk', x.seal_key_id)) throw new Bad(q + '.seal_key_id');
  });
}

function bodyTypes(b, ver) {
  const p = 'body';
  if (!isObj(b)) throw new Bad('body');
  closed(b, p, [...BODY_KEYS, ver === 1 ? 'seal_key_id' : 'seal_keys']);
  I(b, p, 'v');
  for (const k of ['kind', 'deed_id', 'org']) S(b, p, k);
  I(b, p, 'seq');
  S(b, p, 'prev_hash');
  S(b, p, 'issued_at');
  for (const k of ['on_behalf_of', 'session', 'amends']) NS(b, p, k);
  if (ver === 1) S(b, p, 'seal_key_id');
  else {
    const sk = b.seal_keys;
    if (!Array.isArray(sk) || sk.length < 1 || sk.length > 4) throw new Bad(p + '.seal_keys');
    sk.forEach((x, i) => {
      const q = `${p}.seal_keys[${i}]`;
      closed(x, q, ['alg', 'id']);
      S(x, q, 'alg');
      S(x, q, 'id');
    });
  }
  let a = OBJ(b, p, 'agent', AGENT);
  if (a) for (const k of AGENT) S(a, p + '.agent', k);
  a = OBJ(b, p, 'action', ACTION);
  if (a) {
    const q = p + '.action';
    S(a, q, 'id');
    S(a, q, 'tool');
    NS(a, q, 'connector');
    S(a, q, 'mode');
    S(a, q, 'request_digest');
    NS(a, q, 'intent_hash');
    if (typeof a.intent_signed !== 'boolean') throw new Bad(q + '.intent_signed');
  }
  a = OBJ(b, p, 'policy', POLICY);
  if (a) {
    const q = p + '.policy';
    for (const k of ['id', 'version_id', 'version_hash']) NS(a, q, k);
    S(a, q, 'mode');
    S(a, q, 'decision');
    NS(a, q, 'rule_id');
  }
  a = OBJ(b, p, 'approval', APPROVAL);
  if (a) {
    const q = p + '.approval';
    S(a, q, 'id');
    S(a, q, 'outcome');
    NS(a, q, 'decided_at');
    NS(a, q, 'deed');
    if (!Array.isArray(a.decided_by)) throw new Bad(q + '.decided_by');
    a.decided_by.forEach((u, i) => {
      if (typeof u !== 'string') throw new Bad(`${q}.decided_by[${i}]`);
    });
  }
  a = OBJ(b, p, 'execution', EXECUTION);
  if (a) {
    const q = p + '.execution';
    for (const k of ['status', 'started_at', 'finished_at']) S(a, q, k);
    I(a, q, 'latency_ms');
    NS(a, q, 'result_digest');
    NS(a, q, 'error_code');
  }
  a = OBJ(b, p, 'witness', WITNESS);
  if (a) {
    const q = p + '.witness';
    for (const k of ['level', 'verdict', 'method']) S(a, q, k);
    NS(a, q, 'detail_digest');
  }
  a = OBJ(b, p, 'cost', COST);
  if (a) {
    I(a, p + '.cost', 'amount');
    S(a, p + '.cost', 'currency');
  }
  a = OBJ(b, p, 'admin', ADMIN);
  if (a) {
    const q = p + '.admin';
    S(a, q, 'event');
    S(a, q, 'action');
    NS(a, q, 'reason');
    NS(a, q, 'data_digest');
    const actor = closed(a.actor, q + '.actor', REF);
    S(actor, q + '.actor', 'type');
    S(actor, q + '.actor', 'id');
    const t = OBJ(a, q, 'target', REF);
    if (t) {
      S(t, q + '.target', 'type');
      S(t, q + '.target', 'id');
    }
  }
}

function refValues(r, path) {
  if (!m(REF_TYPE, r.type)) throw new Bad(path + '.type');
  if (!m(REF_ID, r.id)) throw new Bad(path + '.id');
}

function bodyValues(b) {
  const p = 'body.';
  const kind = b.kind;
  if (!['action', 'admin', 'amendment'].includes(kind)) throw new Bad(p + 'kind');
  if (!isId('deed', b.deed_id)) throw new Bad(p + 'deed_id');
  if (b.seq < 1 || b.seq > MAX_INT) throw new Bad(p + 'seq');
  if (!m(DIGEST, b.prev_hash)) throw new Bad(p + 'prev_hash');
  if (!validTime(b.issued_at)) throw new Bad(p + 'issued_at');
  if (b.v === 1) {
    if (!isId('sk', b.seal_key_id)) throw new Bad(p + 'seal_key_id');
  } else {
    b.seal_keys.forEach((k, i) => {
      const q = `${p}seal_keys[${i}]`;
      if (!RE_ALG.test(k.alg)) throw new Bad(q + '.alg');
      if (!isId('sk', k.id)) throw new Bad(q + '.id');
      for (const prev of b.seal_keys.slice(0, i)) {
        if (prev.alg === k.alg) throw new Bad(q + '.alg');
        if (prev.id === k.id) throw new Bad(q + '.id');
      }
    });
  }
  if (!isId('org', b.org)) throw new Bad(p + 'org');
  const action = kind === 'action', admin = kind === 'admin', amend = kind === 'amendment';
  if (b.agent === null) {
    if (!admin) throw new Bad(p + 'agent');
  } else {
    if (!isId('agt', b.agent.id)) throw new Bad(p + 'agent.id');
    if (!plain(b.agent.name, 1, 64)) throw new Bad(p + 'agent.name');
  }
  for (const k of ['on_behalf_of', 'session']) {
    if (b[k] === null) continue;
    if (admin || !m(HEADER_REF, b[k])) throw new Bad(p + k);
  }
  const a = b.action;
  if (a === null) {
    if (!admin) throw new Bad(p + 'action');
  } else {
    const q = p + 'action.';
    if (admin) throw new Bad(p + 'action');
    if (!isId('act', a.id)) throw new Bad(q + 'id');
    const n = utf8(a.tool).length;
    if (n === 0 || n > 200 || !plain(a.tool, 1, 200)) throw new Bad(q + 'tool');
    if (a.connector !== null && !isId('con', a.connector)) throw new Bad(q + 'connector');
    if (!['gateway', 'proxy'].includes(a.mode)) throw new Bad(q + 'mode');
    if (!m(DIGEST, a.request_digest)) throw new Bad(q + 'request_digest');
    if (a.intent_hash !== null && !m(DIGEST, a.intent_hash)) throw new Bad(q + 'intent_hash');
    if (a.intent_signed && a.intent_hash === null) throw new Bad(q + 'intent_hash');
  }
  const pol = b.policy;
  if ((pol === null && action) || (pol !== null && !action)) throw new Bad(p + 'policy');
  if (pol !== null) {
    const q = p + 'policy.';
    if (pol.id !== null && !isId('pol', pol.id)) throw new Bad(q + 'id');
    if (pol.version_id !== null && !isId('pv', pol.version_id)) throw new Bad(q + 'version_id');
    if (pol.version_hash !== null && !m(DIGEST, pol.version_hash)) throw new Bad(q + 'version_hash');
    if (pol.version_id !== null && pol.version_hash === null) throw new Bad(q + 'version_hash');
    if (!['enforce', 'shadow'].includes(pol.mode)) throw new Bad(q + 'mode');
    if (!['allow', 'deny', 'require_approval'].includes(pol.decision)) throw new Bad(q + 'decision');
    if (pol.rule_id !== null && !m(RULE_ID, pol.rule_id)) throw new Bad(q + 'rule_id');
  }
  const ap = b.approval;
  if (ap !== null) {
    const q = p + 'approval.';
    if (!action) throw new Bad(p + 'approval');
    if (!isId('apr', ap.id)) throw new Bad(q + 'id');
    if (!['approved', 'denied', 'expired', 'cancelled'].includes(ap.outcome)) throw new Bad(q + 'outcome');
    const db = ap.decided_by;
    db.forEach((u, i) => {
      if (!isId('usr', u) || (i > 0 && !(db[i - 1] < u))) throw new Bad(`${q}decided_by[${i}]`);
    });
    if ((ap.outcome === 'approved' || ap.outcome === 'denied') && db.length === 0) throw new Bad(q + 'decided_by');
    if (ap.outcome === 'expired' && db.length) throw new Bad(q + 'decided_by');
    if (ap.decided_at !== null && !validTime(ap.decided_at)) throw new Bad(q + 'decided_at');
    if (ap.deed !== null && !isId('deed', ap.deed)) throw new Bad(q + 'deed');
  }
  const ex = b.execution;
  if (ex !== null) {
    const q = p + 'execution.';
    if (!action) throw new Bad(p + 'execution');
    if (!['succeeded', 'failed', 'unknown'].includes(ex.status)) throw new Bad(q + 'status');
    if (!validTime(ex.started_at)) throw new Bad(q + 'started_at');
    if (!validTime(ex.finished_at) || ex.finished_at < ex.started_at) throw new Bad(q + 'finished_at');
    if (ex.latency_ms < 0 || ex.latency_ms > MAX_INT) throw new Bad(q + 'latency_ms');
    if (ex.result_digest !== null && !m(DIGEST, ex.result_digest)) throw new Bad(q + 'result_digest');
    if (ex.error_code !== null && !m(ERROR_CODE, ex.error_code)) throw new Bad(q + 'error_code');
  }
  const w = b.witness;
  if ((w === null && amend) || (w !== null && admin)) throw new Bad(p + 'witness');
  if (w !== null) {
    const q = p + 'witness.';
    if (!['L0', 'L1', 'L2'].includes(w.level)) throw new Bad(q + 'level');
    if (!['VERIFIED', 'MISMATCH', 'UNVERIFIABLE'].includes(w.verdict)) throw new Bad(q + 'verdict');
    if (!m(METHOD, w.method)) throw new Bad(q + 'method');
    if (w.detail_digest !== null && !m(DIGEST, w.detail_digest)) throw new Bad(q + 'detail_digest');
  }
  const c = b.cost;
  if (c !== null) {
    if (!action) throw new Bad(p + 'cost');
    if (c.amount < 0 || c.amount > MAX_INT) throw new Bad(p + 'cost.amount');
    if (!m(CURRENCY, c.currency)) throw new Bad(p + 'cost.currency');
  }
  const ad = b.admin;
  if ((ad === null && admin) || (ad !== null && !admin)) throw new Bad(p + 'admin');
  if (ad !== null) {
    const q = p + 'admin.';
    if (!isId('adm', ad.event)) throw new Bad(q + 'event');
    if (!m(ADMIN_ACTION, ad.action)) throw new Bad(q + 'action');
    refValues(ad.actor, q + 'actor');
    if (ad.target !== null) refValues(ad.target, q + 'target');
    if (ad.reason !== null && !plain(ad.reason, 1, 280, true)) throw new Bad(q + 'reason');
    if (ad.data_digest !== null && !m(DIGEST, ad.data_digest)) throw new Bad(q + 'data_digest');
  }
  const am = b.amends;
  if ((am === null && amend) || (am !== null && !amend) || (am !== null && !isId('deed', am))) throw new Bad(p + 'amends');
}

function envelopeValues(e, ver) {
  if (!m(DIGEST, e.body_hash)) throw new Bad('body_hash');
  if (ver === 1) {
    if (!sealShape('Ed25519', e.seal)) throw new Bad('seal');
    if (!isId('sk', e.seal_key_id)) throw new Bad('seal_key_id');
  }
}

export const genesis = (org) => digestOf(utf8('deedglass/genesis\n' + org));

/** Runs the checks of §11.1 in order; resolves with the deed or rejects with the first VerifyError. */
export async function verifyDeed(raw, keys, { strict = false } = {}) {
  if (typeof raw === 'string') raw = utf8(raw);
  if (raw.length > MAX_INPUT) throw new VerifyError('malformed', { detail: 'larger than 64 KiB' });
  let env;
  try {
    env = parseStrict(raw);
  } catch (e) {
    if (e instanceof StrictError) throw new VerifyError('malformed', { detail: e.message });
    throw e;
  }
  if (!isObj(env)) throw new VerifyError('malformed', { detail: 'the top level is not an object' });
  const body = env.body;
  const bseq = isObj(body) && typeof body.seq === 'number' && body.seq > 0 ? body.seq : null;
  const fail = (code, f = {}) => new VerifyError(code, { seq: bseq, deed: isObj(body) ? body.deed_id : undefined, ...f });
  if (env.object !== 'deed') throw new VerifyError('schema_invalid', { field: 'object' });
  if (isObj(body) && typeof env.id === 'string' && typeof body.deed_id === 'string' && env.id !== body.deed_id) throw fail('id_mismatch');
  const v = isObj(body) ? body.v : undefined;
  if (typeof v === 'number' && v !== 1 && v !== 2) throw fail('unsupported_version', { v, detail: `v=${v}` });
  const ver = typeof v === 'number' ? v : 1;
  try {
    envelopeTypes(env, ver);
    bodyTypes(body, ver);
    bodyValues(body);
    envelopeValues(env, ver);
  } catch (e) {
    if (e instanceof Bad) throw fail('schema_invalid', { field: e.field });
    throw e;
  }
  const canon = utf8(jcs(env));
  if (canon.length > MAX_CANON[ver]) throw fail('malformed', { detail: `the canonical form is larger than ${MAX_CANON[ver]} bytes` });
  const bh = await digestOf(utf8(jcs(body)));
  if (bh !== env.body_hash) throw fail('body_hash_mismatch', { expected: env.body_hash, computed: bh });

  let seals;
  if (ver === 1) {
    if (env.seal_key_id !== body.seal_key_id) throw fail('seal_key_mismatch');
    seals = [{ alg: 'Ed25519', seal: env.seal, seal_key_id: env.seal_key_id }];
  } else {
    seals = env.seals;
    for (const sk of body.seal_keys) if (!seals.some((s) => s.seal_key_id === sk.id)) throw fail('seal_missing', { detail: `${sk.alg} ${sk.id}` });
    const want = body.seal_keys.map((k) => k.alg + ' ' + k.id).join('|');
    const got = seals.map((s) => s.alg + ' ' + s.seal_key_id).join('|');
    if (want !== got) throw fail('seal_key_mismatch');
  }
  const issued = body.issued_at;
  for (const s of seals) {
    const { alg, seal_key_id: kid } = s;
    if (!IMPLEMENTED.includes(alg)) throw fail('unsupported_algorithm', { detail: alg });
    const key = keys ? keys.keys.get(kid) : undefined;
    if (!key) throw fail(keys ? 'seal_key_unknown' : 'keys_missing', { key: kid });
    if (key.alg !== alg) throw fail('seal_invalid', { key: kid });
    if (key.revoked) throw fail('seal_key_revoked', { key: kid });
    if (!activeAt(key, issued)) throw fail('seal_key_not_valid_at', { key: kid, issued_at: issued });
    const sig = b64urlDecode(s.seal.slice(s.seal.indexOf(':') + 1));
    const msg = utf8((ver === 1 ? TAG_V1 : TAGS_V2[alg]) + bh);
    const ok = sig !== null && (alg === 'Ed25519' ? await ed25519Verify(key.pub, msg, sig) : mldsa65Verify(key.pub, msg, sig));
    if (!ok) throw fail('seal_invalid', { key: kid });
  }
  // §11.4, the accept policy: by issued_at only, never the verifier's clock.
  const algs = new Set(seals.map((s) => s.alg));
  if (!(algs.has('ML-DSA-65') || (algs.has('Ed25519') && issued < ED25519_ALONE_BEFORE))) throw fail('seal_missing', { detail: 'no seal is accepted alone at ' + issued });
  for (const alg of IMPLEMENTED) {
    if (algs.has(alg)) continue;
    for (const k of keys.keys.values()) if (k.alg === alg && !k.revoked && activeAt(k, issued)) throw fail('seal_missing', { detail: `the key set holds an active ${alg} key` });
  }
  if (strict && (raw.length !== canon.length || raw.some((x, i) => x !== canon[i]))) throw fail('non_canonical');
  return { envelope: env, body, seq: body.seq, org: body.org, deedId: body.deed_id, issuedAt: issued, bodyHash: bh, kind: body.kind, keyIds: seals.map((s) => s.seal_key_id) };
}

// ---- reports, chains and loose deeds (§11.2) -----------------------------------------------------------

export function newReport() {
  return { needsPinned: false, deeds: 0, intact: 0, org: null, first_seq: null, last_seq: null, from: null, to: null, chain: 'not_checked', kinds: {}, keysUsed: {}, bundle: null, failure: null, warnings: [], keys_pinned: false };
}
const resultOf = (rep) => (rep.failure === null ? (rep.needsPinned && !rep.keys_pinned ? 'cannot_determine' : 'valid') : CANNOT_DETERMINE.has(rep.failure.code) ? 'cannot_determine' : 'invalid');
export const exitCode = (rep) => ({ valid: 0, invalid: 1, cannot_determine: 2 })[resultOf(rep)];

function addDeed(rep, d) {
  rep.intact++;
  rep.org ??= d.org;
  rep.first_seq = rep.first_seq === null ? d.seq : Math.min(rep.first_seq, d.seq);
  rep.last_seq = rep.last_seq === null ? d.seq : Math.max(rep.last_seq, d.seq);
  rep.from = rep.from === null || d.issuedAt < rep.from ? d.issuedAt : rep.from;
  rep.to = rep.to === null || d.issuedAt > rep.to ? d.issuedAt : rep.to;
  rep.kinds[d.kind] = (rep.kinds[d.kind] ?? 0) + 1;
  for (const k of d.keyIds) rep.keysUsed[k] = (rep.keysUsed[k] ?? 0) + 1;
}

export function reportJSON(rep) {
  const f = rep.failure;
  const failures = [];
  if (f) {
    const [message, next] = f.message2();
    failures.push({ code: f.code, seq: f.seq, deed: f.f.deed ?? null, field: f.field, message, next });
  }
  return {
    object: 'verification_report', result: resultOf(rep), deeds: rep.deeds, intact: rep.intact, org: rep.org,
    first_seq: rep.first_seq, last_seq: rep.last_seq, from: rep.from, to: rep.to, chain: { status: rep.chain },
    seal_keys: Object.keys(rep.keysUsed).sort().map((id) => ({ id, deeds: rep.keysUsed[id] })), keys_pinned: rep.keys_pinned,
    kinds: Object.fromEntries(Object.entries(rep.kinds).sort()), bundle: rep.bundle, failures, warnings: rep.warnings, verifier: VERIFIER,
  };
}

/** One deed on its own: its checks, and the genesis link when its seq is 1. */
export async function verifyOne(rep, raw, keys, strict = false) {
  rep.deeds++;
  let end = raw.length;
  while (end > 0 && (raw[end - 1] === 0x0a || raw[end - 1] === 0x0d)) end--;
  try {
    const d = await verifyDeed(raw.subarray(0, end), keys, { strict });
    if (d.seq === 1 && d.body.prev_hash !== (await genesis(d.org))) throw new VerifyError('chain_genesis_mismatch', { seq: 1, deed: d.deedId });
    addDeed(rep, d);
  } catch (e) {
    if (!(e instanceof VerifyError)) throw e;
    rep.failure = e;
  }
}

/** Deeds as one org's chain, in input order; obs collects what a bundle manifest is compared with. */
export async function verifyLines(rep, lines, keys, strict = false, obs = null) {
  let prev = null;
  let no = 0;
  for await (const line of lines) {
    no++;
    rep.deeds++;
    try {
      if (line.length === 0) throw new VerifyError('malformed', { detail: `line ${no} is blank` });
      const d = await verifyDeed(line, keys, { strict });
      const fail = (code, f = {}) => new VerifyError(code, { seq: d.seq, deed: d.deedId, ...f });
      if (prev && d.org !== prev.org) throw fail('chain_org_mismatch', { org: d.org, want_org: prev.org });
      if (!prev) {
        if (d.seq !== 1) throw fail('chain_seq_gap', { want: 1 });
        if (d.body.prev_hash !== (await genesis(d.org))) throw fail('chain_genesis_mismatch');
      } else {
        if (d.seq <= prev.seq) throw fail('chain_seq_out_of_order', { prev: prev.seq });
        if (d.seq > prev.seq + 1) throw fail('chain_seq_gap', { prev: prev.seq, want: prev.seq + 1 });
        if (d.body.prev_hash !== prev.bodyHash) throw fail('chain_prev_hash_mismatch', { prev: prev.seq });
        if (d.issuedAt < prev.issuedAt) throw fail('chain_time_regression', { prev: prev.seq });
      }
      prev = d;
      addDeed(rep, d);
      if (obs) {
        obs.count++;
        obs.first_seq ??= d.seq;
        obs.first_prev_hash ??= d.body.prev_hash;
        obs.org = d.org;
        obs.last_seq = d.seq;
        obs.from = obs.from === null || d.issuedAt < obs.from ? d.issuedAt : obs.from;
        obs.to = obs.to === null || d.issuedAt > obs.to ? d.issuedAt : obs.to;
        obs.kinds[d.kind]++;
        obs.head = [d.seq, d.deedId, d.bodyHash];
      }
    } catch (e) {
      if (!(e instanceof VerifyError)) throw e;
      rep.failure = e;
      break;
    }
  }
  rep.chain = rep.failure === null ? 'intact' : rep.failure.code.startsWith('chain_') || rep.intact > 0 ? 'broken' : 'not_checked';
}

/** Splits JSONL bytes into lines (no newline, no trailing CR); a final empty line is dropped. */
export function splitLines(bytes) {
  const out = [];
  let s = 0;
  for (let i = 0; i <= bytes.length; i++) {
    if (i === bytes.length || bytes[i] === 0x0a) {
      let e = i;
      if (e > s && bytes[e - 1] === 0x0d) e--;
      out.push(bytes.subarray(s, e));
      s = i + 1;
    }
  }
  while (out.length && out[out.length - 1].every((b) => b === 0x20 || b === 0x09 || b === 0x0d)) out.pop();
  return out;
}

/** Verifies deeds as one chain and returns the report. */
export async function verifyDeeds(lines, keys, { strict = false } = {}) {
  const rep = newReport();
  await verifyLines(rep, lines, keys, strict);
  return rep;
}

// ---- export bundles (spec/v1/BUNDLE_FORMAT.md) ---------------------------------------------------------

// Edition 2 adds an optional timestamps.json between keys.json and deeds.jsonl (spec/v2/BUNDLE_FORMAT.md
// section 9). It is checked against the manifest like any file; its timestamps are not checked here.
const NAMES = ['README.txt', 'keys.json', 'timestamps.json', 'deeds.jsonl', 'manifest.json', 'manifest.seal.json'];
const LIMITS = { 'README.txt': 64 << 10, 'keys.json': 1 << 20, 'timestamps.json': 1 << 20, 'deeds.jsonl': 8 * 2 ** 30, 'manifest.json': 64 << 10, 'manifest.seal.json': 4 << 10 };
const invalid = (detail) => new VerifyError('bundle_invalid', { detail });

/** A byte stream over the decompressed bundle, refused past the sum of the limits (a gzip bomb). */
class Inflated {
  constructor(input) {
    const src = input instanceof Uint8Array ? new Blob([input]).stream() : input;
    this.reader = src.pipeThrough(new DecompressionStream('gzip')).getReader();
    this.buf = new Uint8Array(0);
    this.off = 0;
    this.total = 0;
    this.cap = Object.values(LIMITS).reduce((a, b) => a + b, 0) + 65536;
  }
  async fill(n) {
    while (this.buf.length - this.off < n) {
      let r;
      try {
        r = await this.reader.read();
      } catch (e) {
        throw invalid(`This bundle is damaged (${e.message}).`);
      }
      if (r.done) return false;
      this.total += r.value.length;
      if (this.total > this.cap) throw invalid('This bundle expands past the size limits.');
      this.buf = concat(this.buf.subarray(this.off), r.value);
      this.off = 0;
    }
    return true;
  }
  async read(n) {
    if (!(await this.fill(n))) throw invalid('This bundle is damaged (unexpected end of data).');
    const out = this.buf.subarray(this.off, this.off + n);
    this.off += n;
    return out;
  }
  async readlines(size, onLine) {
    let left = size;
    let carry = new Uint8Array(0);
    while (left > 0) {
      await this.fill(1);
      const take = Math.min(left, this.buf.length - this.off);
      if (take <= 0) throw invalid('This bundle is damaged (unexpected end of data).');
      const chunk = concat(carry, this.buf.subarray(this.off, this.off + take));
      this.off += take;
      left -= take;
      let s = 0;
      for (let i = 0; i < chunk.length; i++) {
        if (chunk[i] === 0x0a) {
          await onLine(chunk.subarray(s, i + 1));
          s = i + 1;
        }
      }
      carry = chunk.slice(s);
    }
    if (carry.length) await onLine(carry);
  }
  cancel() {
    this.reader.cancel().catch(() => {});
  }
}

const cstr = (b) => {
  const z = b.indexOf(0);
  return new TextDecoder().decode(z < 0 ? b : b.subarray(0, z));
};
function octal(b) {
  if (b[0] & 0x80) {
    let v = 0;
    for (let i = 1; i < b.length; i++) v = v * 256 + b[i];
    return v;
  }
  const s = cstr(b).trim();
  if (!/^[0-7]*$/.test(s)) return NaN;
  return s === '' ? 0 : parseInt(s, 8);
}

/** Reads tar entries in order (USTAR, with PAX and GNU long names) and never writes anything. */
class TarReader {
  constructor(inf) {
    this.inf = inf;
    this.done = false;
    this.pending = null; // an entry read ahead (after keys.json, when there is no timestamps.json)
  }
  async next() {
    let pax = {};
    let longName = null;
    for (;;) {
      if (this.done || !(await this.inf.fill(512))) return null;
      const h = await this.inf.read(512);
      if (h.every((x) => x === 0)) {
        this.done = true;
        return null;
      }
      let sum = 0;
      for (let i = 0; i < 512; i++) sum += i >= 148 && i < 156 ? 32 : h[i];
      if (sum !== octal(h.subarray(148, 156))) throw invalid('This bundle is damaged (a tar header checksum).');
      const type = String.fromCharCode(h[156] || 0x30);
      let size = octal(h.subarray(124, 136));
      if (!Number.isFinite(size)) throw invalid('This bundle is damaged (a tar header).');
      if (pax.size !== undefined) size = Number(pax.size);
      if (type === 'x' || type === 'g' || type === 'L') {
        const data = await this.inf.read(size);
        await this.inf.read((512 - (size % 512)) % 512);
        if (type === 'L') longName = cstr(data);
        else if (type === 'x') pax = parsePax(data);
        continue;
      }
      let name = cstr(h.subarray(0, 100));
      const magic = cstr(h.subarray(257, 263));
      if (magic.startsWith('ustar')) {
        const prefix = cstr(h.subarray(345, 500));
        if (prefix) name = prefix + '/' + name;
      }
      if (longName !== null) name = longName;
      if (pax.path !== undefined) name = pax.path;
      return { name, size, regular: type === '0' };
    }
  }
  async skipPad(size) {
    await this.inf.read((512 - (size % 512)) % 512);
  }
}
function parsePax(data) {
  const out = {};
  const s = new TextDecoder().decode(data);
  let i = 0;
  while (i < s.length) {
    const sp = s.indexOf(' ', i);
    const len = parseInt(s.slice(i, sp), 10);
    if (!(len > 0)) throw invalid('This bundle is damaged (a PAX header).');
    const rec = s.slice(sp + 1, i + len - 1);
    const eq = rec.indexOf('=');
    out[rec.slice(0, eq)] = rec.slice(eq + 1);
    i += len;
  }
  return out;
}

async function bundleEntry(tar, seen, want, optional = null) {
  if (tar.pending) {
    const p = tar.pending;
    tar.pending = null;
    if (p.name !== want) throw invalid(`The files in this bundle are out of order (${p.name}).`);
    return p;
  }
  const e = await tar.next();
  if (e === null) {
    if (want === null || want === 'manifest.seal.json') return null;
    throw invalid(`This bundle has no ${optional || want}.`);
  }
  const n = e.name;
  if (n.includes('/') || n.includes('\\') || n.includes('..') || n.startsWith('.')) throw invalid(`This bundle contains an entry named "${n}".`);
  if (!NAMES.includes(n)) throw invalid(`This bundle contains an unexpected entry "${n}".`);
  if (!e.regular) throw invalid(`${n} in this bundle is not a regular file.`);
  if (seen.has(n)) throw invalid(`This bundle contains ${n} twice.`);
  if (want === null) throw invalid(`This bundle contains an entry after its manifest seal "${n}".`);
  if (n !== want && n !== optional) throw invalid(`The files in this bundle are out of order (${n}).`);
  if (e.size > LIMITS[n]) throw invalid(`${n} in this bundle is larger than its limit.`);
  seen.set(n, e.size);
  return e;
}

const nat = (v) => typeof v === 'number' && v >= 0;
const closedM = (o, keys, what) => {
  if (!isObj(o) || Object.keys(o).length !== keys.length || keys.some((k) => !(k in o))) throw invalid(`The bundle manifest's ${what} is not as the format describes.`);
  return o;
};

async function checkManifest(raw, files, lines, obs) {
  let mf;
  try {
    mf = parseStrict(raw);
  } catch (e) {
    throw invalid(`The bundle manifest is not strict JSON (${e.message}).`);
  }
  if (!isObj(mf) || jcs(mf) !== new TextDecoder().decode(raw)) throw invalid('The bundle manifest is not in canonical form.');
  closedM(mf, ['object', 'v', 'org', 'issuer', 'created_at', 'generator', 'range', 'chain', 'kinds', 'files'], 'top level');
  if (mf.object !== 'deed_bundle_manifest' || mf.v !== 1) throw invalid('The bundle manifest is not a v1 deed_bundle_manifest.');
  if (!isId('org', mf.org) || typeof mf.issuer !== 'string') throw invalid("The bundle manifest's org or issuer is not valid.");
  if (!validTime(mf.created_at)) throw invalid("The bundle manifest's created_at is not valid.");
  const g = closedM(mf.generator, ['name', 'version', 'commit'], 'generator');
  if (!Object.values(g).every((x) => typeof x === 'string')) throw invalid("The bundle manifest's generator is not valid.");
  const r = closedM(mf.range, ['first_seq', 'last_seq', 'count', 'from', 'to'], 'range');
  const c = closedM(mf.chain, ['anchor', 'head'], 'chain');
  const a = closedM(c.anchor, ['kind', 'seq', 'body_hash'], 'anchor');
  const h = closedM(c.head, ['seq', 'deed_id', 'body_hash'], 'head');
  const k = closedM(mf.kinds, ['action', 'admin', 'amendment'], 'kinds');
  const stamped = 'timestamps.json' in files;
  const f = closedM(mf.files, stamped ? ['README.txt', 'keys.json', 'deeds.jsonl', 'timestamps.json'] : ['README.txt', 'keys.json', 'deeds.jsonl'], 'files');
  const fr = closedM(f['README.txt'], ['bytes', 'sha256'], 'files');
  const fk = closedM(f['keys.json'], ['bytes', 'sha256'], 'files');
  const fd = closedM(f['deeds.jsonl'], ['bytes', 'sha256', 'lines'], 'files');
  const listed = [['README.txt', fr], ['keys.json', fk], ['deeds.jsonl', fd]];
  if (stamped) listed.push(['timestamps.json', closedM(f['timestamps.json'], ['bytes', 'sha256'], 'files')]);
  for (const [, e] of listed) if (!nat(e.bytes) || !m(/^[0-9a-f]{64}$/, e.sha256)) throw invalid("The bundle manifest's file list is not valid.");
  if (!nat(fd.lines) || !Object.values(k).every(nat)) throw invalid("The bundle manifest's counts are not valid.");
  if (![r.first_seq, r.last_seq, r.count].every(nat) || !validTime(r.from) || !validTime(r.to)) throw invalid("The bundle manifest's range is not valid.");
  if (r.count !== r.last_seq - r.first_seq + 1 || h.seq !== r.last_seq) throw invalid("The bundle manifest's range does not add up.");
  if (!m(DIGEST, a.body_hash)) throw invalid("The bundle manifest's anchor is not valid.");
  if (r.first_seq === 1) {
    if (a.kind !== 'genesis' || a.seq !== 0 || a.body_hash !== (await genesis(mf.org))) throw invalid("The bundle manifest's anchor is not the org's genesis.");
  } else if (a.kind !== 'deed' || a.seq !== r.first_seq - 1) throw invalid("The bundle manifest's anchor is not valid.");
  if (k.action + k.admin + k.amendment !== r.count) throw invalid("The bundle manifest's kinds do not add up.");
  for (const [name, e] of listed) {
    if (files[name].bytes !== e.bytes || files[name].sha256 !== e.sha256) throw invalid(`${name} differs from the bundle manifest.`);
  }
  if (lines !== fd.lines) throw invalid('deeds.jsonl holds another number of lines than the bundle manifest says.');
  const same =
    obs.count === r.count && obs.first_seq === r.first_seq && obs.last_seq === r.last_seq && obs.from === r.from && obs.to === r.to &&
    obs.kinds.action === k.action && obs.kinds.admin === k.admin && obs.kinds.amendment === k.amendment && obs.org === mf.org &&
    obs.first_prev_hash === a.body_hash && obs.head && obs.head[0] === h.seq && obs.head[1] === h.deed_id && obs.head[2] === h.body_hash;
  if (!same) throw invalid('The bundle manifest does not match the deeds in it.');
  return mf;
}

async function checkManifestSeal(raw, manifestRaw, createdAt, keys) {
  let s;
  try {
    s = parseStrict(raw);
  } catch (e) {
    throw invalid(`The bundle manifest's seal is not strict JSON (${e.message}).`);
  }
  const shape = isObj(s) && jcs(s) === new TextDecoder().decode(raw) && Object.keys(s).length === 4 &&
    s.object === 'deed_bundle_seal' && isId('sk', s.seal_key_id) && m(/^ed25519:[A-Za-z0-9_-]{86}$/, s.seal) && m(DIGEST, s.manifest_hash);
  if (!shape) throw invalid("The bundle manifest's seal is not as the format describes.");
  const bad = new VerifyError('bundle_seal_invalid', { key: s.seal_key_id });
  if (s.manifest_hash !== (await digestOf(manifestRaw))) throw bad;
  const key = keys ? keys.keys.get(s.seal_key_id) : undefined;
  if (!key || key.alg !== 'Ed25519' || key.revoked || !activeAt(key, createdAt)) throw bad;
  const sig = b64urlDecode(s.seal.slice(8));
  if (!sig || !(await ed25519Verify(key.pub, utf8('deedglass/bundle/v1\n' + s.manifest_hash), sig))) throw bad;
  return s.seal_key_id;
}

/**
 * An export bundle: deeds stream through the verifier, then the manifest and its seal are compared.
 * input is the .tar.gz as a Uint8Array or a ReadableStream. Without keys, the bundle's own keys.json is
 * used (not pinned: keys inside a bundle come from the exporter). Resolves with a report.
 */
export async function verifyBundle(input, keys = null, { strict = false, rep = newReport() } = {}) {
  const inf = new Inflated(input);
  try {
    const tar = new TarReader(inf);
    const seen = new Map();
    const files = {};
    const small = async (name) => {
      const e = await bundleEntry(tar, seen, name);
      const data = (await inf.read(e.size)).slice();
      await tar.skipPad(e.size);
      files[name] = { bytes: e.size, sha256: await sha256hex(data) };
      return data;
    };
    let readme, keysBytes;
    try {
      readme = await small('README.txt');
      keysBytes = await small('keys.json');
      const next = await bundleEntry(tar, seen, 'timestamps.json', 'deeds.jsonl');
      if (next.name === 'timestamps.json') {
        const data = (await inf.read(next.size)).slice();
        await tar.skipPad(next.size);
        files['timestamps.json'] = { bytes: next.size, sha256: await sha256hex(data) };
      } else tar.pending = next;
    } catch (e) {
      if (e instanceof VerifyError) return (rep.failure = e), rep;
      throw e;
    }
    void readme;
    if (!keys) {
      try {
        keys = await parseKeySet(keysBytes);
      } catch (e) {
        rep.failure = e;
        return rep;
      }
      rep.warnings.push({ code: 'keys_not_pinned', message: 'Seal keys came from the bundle itself. This shows nothing changed since export, not who sealed it.' });
    }
    rep.keySet = keys;
    const obs = { count: 0, first_seq: null, last_seq: null, from: null, to: null, first_prev_hash: null, org: null, kinds: { action: 0, admin: 0, amendment: 0 }, head: null };
    let nlines = 0;
    try {
      const de = await bundleEntry(tar, seen, 'deeds.jsonl');
      const parts = [];
      const hashParts = [];
      await inf.readlines(de.size, async (line) => {
        hashParts.push(line.slice());
        nlines++;
        parts.push(line[line.length - 1] === 0x0a ? line.slice(0, -1) : line.slice());
      });
      await tar.skipPad(de.size);
      files['deeds.jsonl'] = { bytes: de.size, sha256: await sha256hex(concat(...hashParts)) };
      await verifyLines(rep, parts, keys, strict, obs);
    } catch (e) {
      if (e instanceof VerifyError) return (rep.failure = e), rep;
      throw e;
    }
    if (rep.failure) return rep;
    const st = { manifest: 'mismatch', manifest_seal: 'missing', sealed_by: null };
    rep.bundle = st;
    let mf, manifestRaw, sealRaw = null;
    try {
      const me = await bundleEntry(tar, seen, 'manifest.json');
      manifestRaw = (await inf.read(me.size)).slice();
      await tar.skipPad(me.size);
      const se = await bundleEntry(tar, seen, 'manifest.seal.json');
      if (se) {
        sealRaw = (await inf.read(se.size)).slice();
        await tar.skipPad(se.size);
        await bundleEntry(tar, seen, null);
      }
      mf = await checkManifest(manifestRaw, files, nlines, obs);
    } catch (e) {
      if (e instanceof VerifyError) return (rep.failure = e), rep;
      throw e;
    }
    st.manifest = 'ok';
    if (files['timestamps.json']) {
      rep.warnings.push({ code: 'timestamps_not_checked', message: "This bundle carries timestamps.json; this verifier doesn't check timestamps. Use deedglass verify for them." });
    }
    if (sealRaw === null) {
      rep.warnings.push({ code: 'manifest_unsealed', message: 'The bundle manifest is not sealed.' });
      return rep;
    }
    try {
      st.sealed_by = await checkManifestSeal(sealRaw, manifestRaw, mf.created_at, keys);
      st.manifest_seal = 'ok';
    } catch (e) {
      if (!(e instanceof VerifyError)) throw e;
      if (e.code === 'bundle_seal_invalid') st.manifest_seal = 'invalid';
      rep.failure = e;
    }
    return rep;
  } finally {
    inf.cancel();
  }
}

// ---- the command line (Node) ---------------------------------------------------------------------

function human(rep) {
  const out = [];
  if (!rep.failure && resultOf(rep) === 'cannot_determine') {
    out.push(`○ ${rep.intact} deeds verified against keys from the bundle itself; --strict needs the issuer's keys (--keys).`);
  } else if (!rep.failure) {
    const nk = Object.keys(rep.keysUsed).length;
    const span = rep.from ? ` · ${rep.from.slice(0, 10)} → ${rep.to.slice(0, 10)}` : '';
    out.push(`✓ ${rep.intact} deed${rep.intact === 1 ? '' : 's'}${rep.chain === 'intact' ? ' · chain intact' : ''} · ${nk} seal key${nk === 1 ? '' : 's'}${span}`);
  } else {
    const [msg, next] = rep.failure.message2();
    out.push(`✕ ${msg}`, `  next       ${next}`);
  }
  if (rep.bundle && rep.bundle.manifest === 'ok') out.push('✓ Bundle manifest matches' + (rep.bundle.sealed_by ? ` · sealed by ${rep.bundle.sealed_by}` : ''));
  for (const w of rep.warnings) out.push(`○ ${w.message}`);
  return out.join('\n');
}

/** Verifies one path the way the CLI does; returns the report. */
export async function verifyPath(path, keysPath, strict) {
  const { readFile } = await import('node:fs/promises');
  const rep = newReport();
  rep.needsPinned = strict;
  let keys = null;
  if (keysPath) {
    try {
      keys = await parseKeySet(new Uint8Array(await readFile(keysPath)));
    } catch (e) {
      if (!(e instanceof VerifyError)) throw e;
      rep.failure = e;
      return rep;
    }
    rep.keys_pinned = true;
  }
  const data = new Uint8Array(await readFile(path));
  if (data[0] === 0x1f && data[1] === 0x8b) {
    await verifyBundle(data, keys, { strict, rep });
    keys = rep.keySet ?? keys;
  } else if (path.endsWith('.jsonl')) {
    await verifyLines(rep, splitLines(data), keys, strict);
  } else {
    let s = 0, e = data.length;
    while (s < e && [0x20, 0x09, 0x0a, 0x0d].includes(data[s])) s++;
    while (e > s && [0x20, 0x09, 0x0a, 0x0d].includes(data[e - 1])) e--;
    const lines = splitLines(data.subarray(s, e));
    const looksJsonl = lines.length > 1 && lines.slice(0, 2).every((l) => {
      const t = new TextDecoder().decode(l).trim();
      return t.startsWith('{') && t.endsWith('}');
    });
    if (looksJsonl) await verifyLines(rep, lines, keys, strict);
    else await verifyOne(rep, data, keys, strict);
  }
  if (keys && [...keys.keys.values()].some((k) => TEST_THUMBPRINTS.has(k.thumbprint))) {
    rep.warnings.push({ code: 'test_keys', message: "These deeds were sealed by Deedglass's published test keys. They mean nothing outside tests." });
  }
  return rep;
}

/** Every published vector compared with its expected.json, and the parity corpora (COSE skipped). */
export async function runVectors(root) {
  const { readFile, readdir, access } = await import('node:fs/promises');
  const { join } = await import('node:path');
  const exists = (p) => access(p).then(() => true, () => false);
  const loadKeys = async (p) => {
    try {
      return { keys: await parseKeySet(new Uint8Array(await readFile(p))), err: null };
    } catch (e) {
      if (!(e instanceof VerifyError)) throw e;
      return { keys: null, err: e };
    }
  };
  const outcome = (rep) => (rep.failure ? { valid: false, code: rep.failure.code, seq: rep.failure.seq, field: rep.failure.field } : { valid: true, code: null, seq: null, field: null });
  const diffs = [];
  let total = 0, good = 0, skipped = 0;
  for (const [sub, keysRel] of [['', 'keys/test-keys.json'], ['v2', 'keys.json']]) {
    const dir = sub ? join(root, sub) : root;
    const def = await loadKeys(join(dir, keysRel));
    const cases = [];
    const expected = JSON.parse(await readFile(join(dir, 'expected.json'), 'utf8'));
    for (const rel of Object.keys(expected).sort()) {
      const local = sub && rel.startsWith(sub + '/') ? rel.slice(sub.length + 1) : rel;
      const exp = expected[rel];
      cases.push([join(dir, local), exp, exp.keys ? await loadKeys(join(root, exp.keys)) : def]);
    }
    for (const name of (await readdir(join(dir, 'invalid'))).sort()) {
      const d = join(dir, 'invalid', name);
      const exp = JSON.parse(await readFile(join(d, 'expected.json'), 'utf8'));
      const f = (await exists(join(d, 'deeds.jsonl'))) ? join(d, 'deeds.jsonl') : join(d, 'deed.json');
      cases.push([f, exp, (await exists(join(d, 'keys.json'))) ? await loadKeys(join(d, 'keys.json')) : def]);
    }
    for (const [path, exp, ks] of cases) {
      if (path.endsWith('.cose')) {
        skipped++;
        continue;
      }
      for (const strict of [false, true]) {
        const rep = newReport();
        if (ks.err) rep.failure = ks.err;
        else {
          const data = new Uint8Array(await readFile(path));
          if (path.endsWith('.jsonl')) await verifyLines(rep, splitLines(data), ks.keys, strict);
          else await verifyOne(rep, data, ks.keys, strict);
        }
        const got = outcome(rep);
        let want = { valid: exp.valid, code: exp.code, seq: exp.seq, field: exp.field };
        if (strict && exp.strict_code && exp.valid) want = { valid: false, code: exp.strict_code, seq: got.seq, field: null };
        total++;
        if (JSON.stringify(got) === JSON.stringify(want)) good++;
        else diffs.push(`${path}${strict ? ' --strict' : ''}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
      }
    }
  }
  for (const [corpus, expPath, keysRel] of [
    ['parity/corpus-500.jsonl', 'parity/corpus-500.expected.json', 'keys/test-keys.json'],
    ['v2/parity/corpus-200.jsonl', 'v2/parity/corpus-200.expected.json', 'v2/keys.json'],
  ]) {
    const ks = await loadKeys(join(root, keysRel));
    const expected = JSON.parse(await readFile(join(root, expPath), 'utf8'));
    for (const line of (await readFile(join(root, corpus), 'utf8')).trim().split('\n')) {
      const c = JSON.parse(line);
      const deeds = c.deeds.map((d) => new Uint8Array(Buffer.from(d, 'base64')));
      const rep = newReport();
      if (ks.err) rep.failure = ks.err;
      else if (c.chain) await verifyLines(rep, deeds, ks.keys, c.strict);
      else await verifyOne(rep, deeds[0], ks.keys, c.strict);
      const got = { case: c.case, ...outcome(rep), deeds: rep.deeds };
      total++;
      if (JSON.stringify(got) === JSON.stringify(expected[c.case - 1])) good++;
      else diffs.push(`${corpus} #${c.case} (${c.kind}): got ${JSON.stringify(got)}, want ${JSON.stringify(expected[c.case - 1])}`);
    }
  }
  for (const d of diffs.slice(0, 40)) console.log('✕ ' + d);
  console.log(`${diffs.length ? '✕' : '✓'} ${good}/${total} vector checks match expected.json and the parity corpora (${skipped} COSE vectors skipped: not read by this verifier)`);
  return diffs.length ? 1 : 0;
}

async function main(argv) {
  const args = argv.slice(2);
  const flag = (n) => {
    const i = args.indexOf(n);
    if (i < 0) return undefined;
    const v = args[i + 1];
    args.splice(i, 2);
    return v;
  };
  const bool = (n) => {
    const i = args.indexOf(n);
    if (i >= 0) args.splice(i, 1);
    return i >= 0;
  };
  const vectors = flag('--vectors');
  if (vectors !== undefined) return runVectors(vectors);
  const keysPath = flag('--keys');
  const strict = bool('--strict');
  const json = bool('--json');
  if (args.length !== 1 || args[0].startsWith('-')) {
    console.error('Usage  node deedglass-verify.mjs <bundle.tar.gz | deeds.jsonl | deed.json> [--keys keys.json] [--strict] [--json]\n       node deedglass-verify.mjs --vectors <testdata/deeds>');
    return 2;
  }
  let rep;
  try {
    rep = await verifyPath(args[0], keysPath, strict);
  } catch (e) {
    if (e && e.code === 'ENOENT') {
      console.error(`✕ Can't read ${e.path}: no such file.`);
      return 2;
    }
    throw e;
  }
  console.log(json ? JSON.stringify(reportJSON(rep), null, 2) : human(rep));
  return exitCode(rep);
}

const isMain = typeof process !== 'undefined' && process.argv?.[1] && import.meta.url === (await import('node:url')).pathToFileURL(process.argv[1]).href;
if (isMain) process.exitCode = await main(process.argv);
