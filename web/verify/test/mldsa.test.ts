import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { parseKeySet } from '../src/index.js';
import { MLDSA65_PUBLIC_KEY_SIZE, MLDSA65_SIGNATURE_SIZE, mldsa65PublicKey, mldsa65Verify, shake128, shake256 } from '../src/mldsa.js';
import { decodeB64url } from '../src/b64url.js';
import { readBytes, readText } from './helpers.js';

// The dependency-free ML-DSA-65 check (ADR-109). The v2 vectors and the v2 parity corpus compare it
// with Go's crypto/mldsa; these cases pin SHAKE against Node's OpenSSL and the refusals.

describe('SHAKE (FIPS 202)', () => {
  for (const n of [0, 1, 135, 136, 137, 167, 168, 169, 1000]) {
    it(`${n} bytes in`, () => {
      const d = new Uint8Array(n).map((_, i) => (i * 31 + 7) & 0xff);
      expect(Buffer.from(shake128().absorb(d).squeeze(500)).toString('hex')).toBe(createHash('shake128', { outputLength: 500 }).update(d).digest('hex'));
      expect(Buffer.from(shake256().absorb(d).squeeze(300)).toString('hex')).toBe(createHash('shake256', { outputLength: 300 }).update(d).digest('hex'));
    });
  }
});

describe('ML-DSA-65 verify', async () => {
  const keys = await parseKeySet(readBytes('testdata/deeds/v2/keys.json'));
  const q = keys.keys.find((k) => k.alg === 'ML-DSA-65')!;
  const pk = mldsa65PublicKey(decodeB64url(q.public_key)!)!;
  const env = JSON.parse(readText('testdata/deeds/v2/valid/03-dual-sealed.json'));
  const seal = env.seals[1].seal as string;
  const sig = decodeB64url(seal.slice('ml-dsa-65:'.length))!;
  const msg = new TextEncoder().encode('deedglass/deed/v2/ml-dsa-65\n' + env.body_hash);

  it('sizes', () => {
    expect(MLDSA65_PUBLIC_KEY_SIZE).toBe(1952);
    expect(MLDSA65_SIGNATURE_SIZE).toBe(3309);
    expect(sig.length).toBe(3309);
  });
  it('accepts the vector seal', () => expect(mldsa65Verify(pk, msg, sig)).toBe(true));
  it('refuses another message, another tag, a flipped bit, a short signature', () => {
    expect(mldsa65Verify(pk, new TextEncoder().encode('deedglass/deed/v2/ed25519\n' + env.body_hash), sig)).toBe(false);
    expect(mldsa65Verify(pk, msg.slice(1), sig)).toBe(false);
    for (const i of [0, 47, 48, 1000, 3247, 3248, 3300]) {
      const bad = sig.slice();
      bad[i] = bad[i]! ^ 1;
      expect(mldsa65Verify(pk, msg, bad)).toBe(false);
    }
    expect(mldsa65Verify(pk, msg, sig.slice(0, 3308))).toBe(false);
  });
  it('refuses a public key of the wrong size', () => expect(mldsa65PublicKey(new Uint8Array(1951))).toBeNull());
});
