import { describe, expect, it } from 'vitest';
import { ed25519Verify } from '../src/deed.js';
import { fromHex } from '../src/hex.js';
import { SMALL_ORDER_KEYS, TEST_KEY_THUMBPRINTS } from '../src/keys.gen.js';
import { importEd25519 } from '../src/keys.js';
import { encodeB64url, decodeB64url } from '../src/b64url.js';
import { parseKeySet, unsafeKey, VerifyError } from '../src/index.js';
import { readBytes, readText } from './helpers.js';

describe('Ed25519 public keys (attack case A13)', () => {
  it('refuses every small-order and non-canonical encoding, with Go’s reason', () => {
    expect(SMALL_ORDER_KEYS.length).toBe(12);
    for (const k of SMALL_ORDER_KEYS) expect(unsafeKey(fromHex(k.hex)), k.name).toBe(k.refused);
  });

  it('refuses y ≥ p', () => {
    // y = p + 3 (little-endian): not canonical.
    const b = fromHex('f0ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f');
    expect(unsafeKey(b)).toBe('non-canonical encoding');
  });

  it('accepts the RFC 8032 test keys', () => {
    for (const hex of [
      'd75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a',
      '3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c',
      'fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025',
    ]) {
      expect(unsafeKey(fromHex(hex))).toBeNull();
    }
  });

  it('refuses a key set that holds one unsafe key, whole', async () => {
    await expect(parseKeySet(readBytes('testdata/deeds/invalid/small-order-key-in-set/keys.json'))).rejects.toMatchObject({ code: 'seal_key_rejected' });
  });

  it('reads the test key set and its thumbprints', async () => {
    const ks = await parseKeySet(readBytes('testdata/deeds/keys/test-keys.json'));
    expect(ks.keys.map((k) => k.thumbprint).sort()).toEqual([...TEST_KEY_THUMBPRINTS].sort());
    expect(ks.keys.map((k) => k.status)).toEqual(['revoked', 'retired', 'active']);
  });

  it('refuses inconsistent key sets', async () => {
    const good = JSON.parse(readText('testdata/deeds/keys/test-keys.json'));
    const variants: [string, (ks: any) => void, string][] = [
      ['duplicate id', (ks) => ks.keys.push(ks.keys[0]), 'seal_key_rejected'],
      ['wrong thumbprint', (ks) => (ks.keys[0].thumbprint = ks.keys[1].thumbprint), 'seal_key_rejected'],
      ['jwk differs', (ks) => (ks.keys[0].jwk.x = ks.keys[1].public_key), 'seal_key_rejected'],
      ['status does not match', (ks) => (ks.keys[2].status = 'retired'), 'seal_key_rejected'],
      ['unknown alg', (ks) => (ks.keys[0].alg = 'EdDSA'), 'seal_key_rejected'],
      ['extra field', (ks) => (ks.note = 'x'), 'schema_invalid'],
      ['bad time', (ks) => (ks.keys[0].activated_at = '2026-02-30T00:00:00.000Z'), 'schema_invalid'],
      ['not an object', (ks) => (ks.keys = 1), 'schema_invalid'],
    ];
    for (const [name, edit, code] of variants) {
      const ks = structuredClone(good);
      edit(ks);
      await expect(parseKeySet(JSON.stringify(ks)), name).rejects.toMatchObject({ code });
    }
    await expect(parseKeySet('[]')).rejects.toBeInstanceOf(VerifyError);
  });
});

describe('base64url (strict)', () => {
  it('round-trips and refuses other spellings', () => {
    const b = new Uint8Array([0xfb, 0xff, 0x01, 0x02]);
    expect(decodeB64url(encodeB64url(b))).toEqual(b);
    expect(decodeB64url('+/8')).toBeNull(); // standard alphabet
    expect(decodeB64url('AA==')).toBeNull(); // padding
    expect(decodeB64url('AB')).toBeNull(); // non-zero trailing bits
    expect(decodeB64url('A')).toBeNull();
  });
});

describe('Wycheproof Ed25519 (testdata/wycheproof)', () => {
  it('agrees on every vector with a safe public key', async () => {
    const w = JSON.parse(readText('testdata/wycheproof/ed25519_test.json'));
    let n = 0;
    const diffs: string[] = [];
    for (const g of w.testGroups) {
      const pub = fromHex(g.publicKey.pk);
      if (unsafeKey(pub) !== null) continue; // refused at the key set, never verified
      const key = await importEd25519(pub);
      for (const t of g.tests) {
        n++;
        const got = await ed25519Verify(key, fromHex(t.msg), fromHex(t.sig));
        if (got !== (t.result === 'valid')) diffs.push(`tc ${t.tcId} (${t.comment}): got ${got}, want ${t.result}`);
      }
    }
    expect(diffs).toEqual([]);
    expect(n).toBeGreaterThan(100);
  });
});
