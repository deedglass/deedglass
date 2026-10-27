// Signed intents (S31 §3.6): a byte-for-byte port of S09's deedglass.intent/1 signer
// (internal/intent/agent.go). The intent is JCS bytes of at most 2,048; the signature covers
// "deedglass/intent/v1\n" + intent_hash; the header is b64url(intent) "." b64url(signature).

import { canonicalArgs } from './args.js';
import { copy } from './copy.js';
import { DeedglassError, InvalidRequestError } from './errors.js';
import type { JsonValue } from './types.js';
import { encodeB64url } from './verify/b64url.js';
import { toHex } from './verify/hex.js';
import { canonicalize } from './verify/jcs.js';
import { digest } from './verify/sha256.js';

export const INTENT_VERSION: string = 'deedglass.intent/1';
export const INTENT_DOMAIN: string = 'deedglass/intent/v1\n';
export const MAX_INTENT_BYTES: number = 2048;
export const DEFAULT_TTL_MS: number = 60_000;
export const MAX_TTL_MS: number = 300_000;
/** S09's tool pattern for a signed intent (intent.PatternTool). */
export const INTENT_TOOL: RegExp = /^[a-z][a-z0-9-]{1,31}\.[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/;
/** The on-behalf-of and session grammar (intent.PatternRef). */
export const REF: RegExp = /^[A-Za-z0-9._~:@/+=-]{1,256}$/;
const ORG = /^org_[0-7][0-9a-hjkmnp-tv-z]{25}$/;
const AGENT = /^agt_[0-7][0-9a-hjkmnp-tv-z]{25}$/;

export interface IntentKey {
  /** A PKCS#8 PEM Ed25519 private key (what deedglass agent intent keygen writes), or a CryptoKey. */
  privateKeyPem: string | CryptoKey;
  org?: string;
  agent?: string;
  /** Required with a CryptoKey that can't be exported; computed from the PEM otherwise. */
  kid?: string;
}

export interface SignedIntent {
  header: string;
  intentHash: string;
  kid: string;
  expiresAt: number;
}

function subtle(): SubtleCrypto {
  const s = globalThis.crypto?.subtle;
  if (!s) throw new DeedglassError({ type: 'client_error', code: 'ed25519_unsupported', message: copy.ed25519Missing });
  return s;
}

function bad(msg: string, param: string): InvalidRequestError {
  return new InvalidRequestError({ type: 'client_error', code: 'intent_invalid', message: msg, param });
}

export function pemToDer(pem: string): Uint8Array {
  // indexOf, not a lazy regex, which is polynomial on input with many BEGIN lines and no END.
  const begin = '-----BEGIN PRIVATE KEY-----';
  const start = pem.indexOf(begin);
  const end = start < 0 ? -1 : pem.indexOf('-----END PRIVATE KEY-----', start + begin.length);
  if (end < 0 || end === start + begin.length) throw bad(copy.intentKeyBad, 'intentKey');
  const b64 = pem.slice(start + begin.length, end).replace(/\s+/g, '');
  let bin: string;
  try {
    bin = atob(b64);
  } catch {
    throw bad(copy.intentKeyBad, 'intentKey');
  }
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function b64urlToBytes(s: string): Uint8Array {
  const b = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4));
  const out = new Uint8Array(b.length);
  for (let i = 0; i < b.length; i++) out[i] = b.charCodeAt(i);
  return out;
}

/** The key id: hex of the first 16 bytes of SHA-256(raw public key). */
export async function intentKid(publicKeyRaw: Uint8Array): Promise<string> {
  const h = new Uint8Array(await subtle().digest('SHA-256', publicKeyRaw as BufferSource));
  return toHex(h.slice(0, 16));
}

interface Loaded {
  sign: CryptoKey;
  kid: string;
}
const cache = new WeakMap<object, Promise<Loaded>>();

async function load(k: IntentKey): Promise<Loaded> {
  if (typeof k.privateKeyPem !== 'string') {
    if (!k.kid) throw bad('A CryptoKey intent key needs its kid. Pass kid, or pass the PEM instead.', 'intentKey.kid');
    return { sign: k.privateKeyPem, kid: k.kid };
  }
  const der = pemToDer(k.privateKeyPem);
  let pub: Uint8Array;
  let sign: CryptoKey;
  try {
    // A one-time extractable import yields the public key (JWK x); signing uses a non-extractable copy.
    const ext = await subtle().importKey('pkcs8', der as BufferSource, { name: 'Ed25519' }, true, ['sign']);
    const jwk = await subtle().exportKey('jwk', ext);
    if (!jwk.x) throw new Error('no public key');
    pub = b64urlToBytes(jwk.x);
    sign = await subtle().importKey('pkcs8', der as BufferSource, { name: 'Ed25519' }, false, ['sign']);
  } catch (err) {
    if (err instanceof DeedglassError) throw err;
    throw bad(copy.intentKeyBad, 'intentKey');
  }
  const kid = await intentKid(pub);
  if (k.kid && k.kid !== kid) throw bad(`The intent key's kid is ${kid}, not ${k.kid}. Pass the matching kid, or leave it out.`, 'intentKey.kid');
  return { sign, kid };
}

function loaded(k: IntentKey): Promise<Loaded> {
  let p = cache.get(k);
  if (!p) {
    p = load(k);
    cache.set(k, p);
    p.catch(() => cache.delete(k));
  }
  return p;
}

function nonceHex(): string {
  const b = new Uint8Array(16);
  globalThis.crypto.getRandomValues(b);
  return toHex(b);
}

/** Signs one intent. Refusals are InvalidRequestError (code intent_invalid, or big_int for args). */
export async function signIntent(p: {
  key: IntentKey;
  org: string;
  agent: string;
  tool: string;
  args: JsonValue;
  onBehalfOf?: string;
  session?: string;
  ttlMs?: number;
  now?: () => number;
  nonce?: string;
}): Promise<SignedIntent> {
  if (!ORG.test(p.org)) throw bad('org must be an org id like org_01k6h7kp6msmzrhdzwtcr3jnkp.', 'org');
  if (!AGENT.test(p.agent)) throw bad('agent must be an agent id like agt_01k633rrmh3bcz2p39gg4vp46q.', 'agent');
  if (p.tool.length > 200 || !INTENT_TOOL.test(p.tool)) throw bad(copy.badIntentTool(p.tool), 'tool');
  if (p.onBehalfOf !== undefined && !REF.test(p.onBehalfOf)) throw bad(copy.badRef('onBehalfOf'), 'onBehalfOf');
  if (p.session !== undefined && !REF.test(p.session)) throw bad(copy.badRef('session'), 'session');
  const ttl = p.ttlMs ?? DEFAULT_TTL_MS;
  if (!Number.isInteger(ttl) || ttl <= 0 || ttl > MAX_TTL_MS) throw bad('ttlMs must be 1 to 300,000 milliseconds.', 'ttlMs');
  const nonce = p.nonce ?? nonceHex();
  if (!/^[0-9a-f]{32}$/.test(nonce)) throw bad('nonce must be 32 lowercase hex characters (16 bytes).', 'nonce');
  const k = await loaded(p.key);
  const argsDigest = await digest(canonicalArgs(p.args));
  const iat = Math.trunc((p.now ?? Date.now)());
  const intent: Record<string, JsonValue> = {
    v: INTENT_VERSION,
    org: p.org,
    agent: p.agent,
    kid: k.kid,
    tool: p.tool,
    args_digest: argsDigest,
    nonce,
    iat,
    exp: iat + ttl,
  };
  if (p.onBehalfOf !== undefined) intent.on_behalf_of = p.onBehalfOf;
  if (p.session !== undefined) intent.session = p.session;
  const bytes = new TextEncoder().encode(canonicalize(intent));
  if (bytes.length > MAX_INTENT_BYTES) throw bad(`The intent is ${bytes.length} bytes; the limit is 2,048. Shorten on_behalf_of or session.`, 'intent');
  const intentHash = await digest(bytes);
  const sig = new Uint8Array(await subtle().sign({ name: 'Ed25519' }, k.sign, new TextEncoder().encode(INTENT_DOMAIN + intentHash) as BufferSource));
  return { header: encodeB64url(bytes) + '.' + encodeB64url(sig), intentHash, kid: k.kid, expiresAt: iat + ttl };
}

/** A new Ed25519 intent key: the PKCS#8 PEM (keep it secret), the public key to register, its kid. */
export async function generateIntentKey(): Promise<{ privateKeyPem: string; publicKey: string; kid: string }> {
  const pair = (await subtle().generateKey({ name: 'Ed25519' }, true, ['sign', 'verify'])) as CryptoKeyPair;
  const der = new Uint8Array(await subtle().exportKey('pkcs8', pair.privateKey));
  const pub = new Uint8Array(await subtle().exportKey('raw', pair.publicKey));
  let bin = '';
  for (const x of der) bin += String.fromCharCode(x);
  const b64 = btoa(bin).replace(/(.{64})/g, '$1\n').trimEnd();
  return { privateKeyPem: `-----BEGIN PRIVATE KEY-----\n${b64}\n-----END PRIVATE KEY-----\n`, publicKey: 'ed25519:' + encodeB64url(pub), kid: await intentKid(pub) };
}

export { argsDigest } from './args.js';
