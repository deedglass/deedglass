// Copied from web/verify/src/keys.ts by sdk/typescript/scripts/sync-verify.mjs (S31). Don't edit this copy:
// edit web/verify/src/keys.ts, then run npm run sync-verify. make sdk-sync-verify fails on drift.
// Seal key sets (keys.json): a port of deed.ParseKeySet and keys.ParsePublicKey. Any unsafe key
// (small-order or non-canonical Ed25519, attack case A13) or inconsistent key refuses the whole set
// with seal_key_rejected: a verifier never picks and chooses.

import { decodeB64url } from './b64url.js';
import { keyRejected, malformed, schemaField, UnsupportedError, VerifyError } from './errors.js';
import { toHex } from './hex.js';
import { SMALL_ORDER_KEYS } from './keys.gen.js';
import { digest, subtle } from './sha256.js';
import { asObj, closed, Decoder, isID, parseTime } from './schema.js';
import { type JsonObject, parseStrictValue, StrictError } from './strict.js';
import { malformedFrom } from './strict.js';

export interface SealKey {
  object: 'seal_key';
  id: string;
  alg: string;
  public_key: string;
  thumbprint: string;
  status: string;
  activated_at: string;
  retired_at: string | null;
  revoked_at: string | null;
  jwk: { kty: string; crv: string; x: string; kid: string; use: string };
}

interface KeyEntry {
  key: SealKey;
  pub: Uint8Array;
  activated: number;
  retired: number | null;
  revoked: number | null;
  crypto?: Promise<CryptoKey>;
}

/** A parsed, checked key set. */
export class SealKeySet {
  readonly object = 'seal_key_set';
  issuer = '';
  updated_at = '';
  keys: SealKey[] = [];
  /** @internal */ byId = new Map<string, KeyEntry>();

  lookup(id: string): KeyEntry | undefined {
    return this.byId.get(id);
  }

  /** The validity rule: activated_at ≤ t < retired_at, and never for a revoked key. */
  validAt(id: string, t: number): '' | 'seal_key_revoked' | 'seal_key_not_valid_at' {
    const e = this.byId.get(id)!;
    if (e.revoked !== null) return 'seal_key_revoked';
    if (t < e.activated || (e.retired !== null && t >= e.retired)) return 'seal_key_not_valid_at';
    return '';
  }

  /** The key as a WebCrypto key (imported once). Throws UnsupportedError when Ed25519 is missing. */
  cryptoKey(id: string): Promise<CryptoKey> {
    const e = this.byId.get(id)!;
    e.crypto ??= importEd25519(e.pub);
    return e.crypto;
  }

  /** The same set restricted to these thumbprints (verify --pin). */
  pinned(thumbprints: readonly string[]): SealKeySet {
    const out = new SealKeySet();
    out.issuer = this.issuer;
    out.updated_at = this.updated_at;
    for (const k of this.keys) {
      if (thumbprints.includes(k.thumbprint)) {
        out.keys.push(k);
        out.byId.set(k.id, this.byId.get(k.id)!);
      }
    }
    return out;
  }

  toJSON(): unknown {
    return { object: this.object, issuer: this.issuer, updated_at: this.updated_at, keys: this.keys };
  }
}

export async function importEd25519(pub: Uint8Array): Promise<CryptoKey> {
  try {
    return await subtle().importKey('raw', pub as BufferSource, { name: 'Ed25519' }, false, ['verify']);
  } catch (e) {
    const name = (e as { name?: string }).name;
    if (name === 'NotSupportedError' || name === 'TypeError' || name === 'SyntaxError') throw new UnsupportedError();
    if (e instanceof UnsupportedError) throw e;
    throw new UnsupportedError();
  }
}

/** True when this runtime can verify Ed25519 with WebCrypto. */
export async function ed25519Supported(): Promise<boolean> {
  try {
    await importEd25519(new Uint8Array(BASE_POINT));
    return true;
  } catch {
    return false;
  }
}

// The Ed25519 base point, a safe public key for the feature check.
const BASE_POINT = [
  0x58, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66,
  0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66, 0x66,
];

/** Reads keys.json strictly (port of deed.ParseKeySet). Throws VerifyError. */
export async function parseKeySet(input: string | Uint8Array): Promise<SealKeySet> {
  let tree;
  try {
    tree = parseStrictValue(input, true);
  } catch (e) {
    if (e instanceof StrictError) throw malformedFrom(e);
    throw e;
  }
  let top: { m: JsonObject; path: string };
  try {
    top = asObj(tree, '');
  } catch {
    throw malformed('a key set must be a JSON object');
  }
  closed(top, ['object', 'issuer', 'updated_at', 'keys']);
  const d = new Decoder();
  const ks = new SealKeySet();
  const object = d.s(top, 'object');
  ks.issuer = d.s(top, 'issuer');
  ks.updated_at = d.s(top, 'updated_at');
  d.check();
  if (object !== 'seal_key_set') throw schemaField('object', 'must be "seal_key_set"');
  if (parseTime(ks.updated_at) === null) throw schemaField('updated_at', 'must be YYYY-MM-DDTHH:MM:SS.mmmZ');
  const list = top.m['keys'];
  if (!Array.isArray(list)) throw schemaField('keys', 'must be an array');
  const keys: SealKey[] = [];
  list.forEach((v, i) => {
    const o = asObj(v, `keys[${i}]`);
    closed(o, ['object', 'id', 'alg', 'public_key', 'thumbprint', 'status', 'activated_at', 'retired_at', 'revoked_at', 'jwk']);
    const k = {
      object: d.s(o, 'object'),
      id: d.s(o, 'id'),
      alg: d.s(o, 'alg'),
      public_key: d.s(o, 'public_key'),
      thumbprint: d.s(o, 'thumbprint'),
      status: d.s(o, 'status'),
      activated_at: d.s(o, 'activated_at'),
      retired_at: d.ns(o, 'retired_at'),
      revoked_at: d.ns(o, 'revoked_at'),
    } as SealKey;
    const j = d.obj(o, 'jwk', ['kty', 'crv', 'x', 'kid', 'use']);
    if (j) {
      k.jwk = { kty: d.s(j, 'kty'), crv: d.s(j, 'crv'), x: d.s(j, 'x'), kid: d.s(j, 'kid'), use: d.s(j, 'use') };
    } else if (d.err === null) {
      d.err = schemaField(`${o.path}.jwk`, 'must be an object');
    }
    d.check();
    keys.push(k);
  });
  ks.keys = keys;
  await index(ks);
  return ks;
}

/** Checks every key and builds the lookup table (port of KeySet.index). */
async function index(ks: SealKeySet): Promise<void> {
  ks.byId = new Map();
  for (let i = 0; i < ks.keys.length; i++) {
    const k = ks.keys[i]!;
    if (k.object !== 'seal_key') throw schemaField(`keys[${i}].object`, 'must be "seal_key"');
    if (!isID('sk', k.id)) throw schemaField(`keys[${i}].id`, 'must be an sk_ id');
    if (ks.byId.has(k.id)) throw keyRejected(k.id, 'listed twice');
    if (k.alg !== 'Ed25519') throw keyRejected(k.id, 'unknown alg ' + k.alg);
    const raw = decodeB64url(k.public_key);
    if (raw === null || raw.length !== 32) throw keyRejected(k.id, 'public_key is not 32 bytes of base64url');
    const why = unsafeKey(raw);
    if (why !== null) throw keyRejected(k.id, why);
    if (k.thumbprint !== (await digest(raw))) throw keyRejected(k.id, 'thumbprint does not match public_key');
    const j = k.jwk;
    if (j.kty !== 'OKP' || j.crv !== 'Ed25519' || j.x !== k.public_key || j.kid !== k.id || j.use !== 'sig') {
      throw keyRejected(k.id, 'jwk does not match the key');
    }
    const activated = parseTime(k.activated_at);
    if (activated === null) throw schemaField(`keys[${i}].activated_at`, 'must be YYYY-MM-DDTHH:MM:SS.mmmZ');
    let retired: number | null = null;
    let revoked: number | null = null;
    if (k.retired_at !== null) {
      retired = parseTime(k.retired_at);
      if (retired === null) throw schemaField(`keys[${i}].retired_at`, 'must be YYYY-MM-DDTHH:MM:SS.mmmZ or null');
    }
    if (k.revoked_at !== null) {
      revoked = parseTime(k.revoked_at);
      if (revoked === null) throw schemaField(`keys[${i}].revoked_at`, 'must be YYYY-MM-DDTHH:MM:SS.mmmZ or null');
    }
    const ok =
      (k.status === 'active' && retired === null && revoked === null) ||
      (k.status === 'retired' && retired !== null && revoked === null) ||
      (k.status === 'revoked' && revoked !== null);
    if (!ok) throw keyRejected(k.id, `status ${k.status} does not match retired_at and revoked_at`);
    ks.byId.set(k.id, { key: k, pub: raw, activated, retired, revoked });
  }
  // Stable sort by activated_at, as Go does.
  ks.keys = ks.keys
    .map((k, i) => [k, i] as const)
    .sort((a, b) => (a[0].activated_at < b[0].activated_at ? -1 : a[0].activated_at > b[0].activated_at ? 1 : a[1] - b[1]))
    .map(([k]) => k);
}

// --- Ed25519 public key checks (port of internal/keys with BigInt) ---

const P = (1n << 255n) - 19n;
const D = 37095705934669439343138083508754565189542113879843219016388785533085940283555n;
const SQRT_M1 = modPow(2n, (P - 1n) / 4n, P);

function mod(a: bigint): bigint {
  const r = a % P;
  return r < 0n ? r + P : r;
}

function modPow(b: bigint, e: bigint, m: bigint): bigint {
  let r = 1n;
  b %= m;
  while (e > 0n) {
    if (e & 1n) r = (r * b) % m;
    b = (b * b) % m;
    e >>= 1n;
  }
  return r;
}

function inv(a: bigint): bigint {
  return modPow(mod(a), P - 2n, P);
}

/**
 * Why a 32-byte Ed25519 public key is unsafe, or null when it is safe: a non-canonical encoding
 * (y ≥ p, or x = 0 with the sign bit set), not on the curve, or small order (8·A is the identity).
 * The same rules and words as Go's keys.ParsePublicKey and deed.UnsafeReason.
 */
export function unsafeKey(pub: Uint8Array): string | null {
  if (pub.length !== 32) return 'not 32 bytes';
  const le = Array.from(pub);
  const sign = le[31]! >> 7;
  le[31]! &= 0x7f;
  let y = 0n;
  for (let i = 31; i >= 0; i--) y = (y << 8n) | BigInt(le[i]!);
  if (y >= P) return 'non-canonical encoding';
  const y2 = mod(y * y);
  const u = mod(y2 - 1n);
  const v = mod(D * y2 + 1n);
  if (v === 0n) return 'not a point on the curve';
  const x2 = mod(u * inv(v));
  let x = modPow(x2, (P + 3n) / 8n, P);
  if (mod(x * x) !== x2) {
    x = mod(x * SQRT_M1);
    if (mod(x * x) !== x2) return 'not a point on the curve';
  }
  if (x === 0n && sign === 1) return 'non-canonical encoding';
  if (Number(x & 1n) !== sign) x = P - x;
  let px = x;
  let py = y;
  for (let i = 0; i < 3; i++) [px, py] = add(px, py, px, py);
  if (px === 0n && py === 1n) return 'small-order point';
  // The published list must agree (belt and braces; generated from Go's vectors).
  const hex = toHex(pub);
  if (SMALL_ORDER_KEYS.some((s) => s.hex === hex)) return 'small-order point';
  return null;
}

function add(x1: bigint, y1: bigint, x2: bigint, y2: bigint): [bigint, bigint] {
  const x1y2 = x1 * y2;
  const y1x2 = y1 * x2;
  const y1y2 = y1 * y2;
  const x1x2 = x1 * x2;
  const dxy = mod(D * x1x2 * y1y2);
  const x3 = mod((x1y2 + y1x2) * inv(1n + dxy));
  const y3 = mod((y1y2 + x1x2) * inv(mod(1n - dxy)));
  return [x3, y3];
}

export { VerifyError };
