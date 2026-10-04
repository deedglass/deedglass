import { readdirSync } from 'node:fs';
import { createPrivateKey, createPublicKey } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { argsDigest, generateIntentKey, intentKid, signIntent } from '../../src/index.js';
import { readText, repo } from '../helpers.js';

interface Vector {
  header: string;
  sign: { key: string; org: string; agent: string; tool: string; args: string; on_behalf_of?: string; session?: string; now: string; ttl_ms: number; nonce: string };
  expected: { intent_hash: string; args_digest: string; kid: string; expires_at: string; intent_bytes: number };
}

const keys = JSON.parse(readText('testdata/intents/keys.json')) as { keys: Record<string, { pem: string; kid: string }> };
const pem = (name: string) => readText('testdata/intents/' + keys.keys[name]!.pem);

describe('signed intents reproduce testdata/intents/valid byte for byte (S09)', () => {
  const files = readdirSync(repo('testdata/intents/valid')).filter((f) => f.endsWith('.json'));
  it('has the 8 valid vectors', () => expect(files.length).toBe(8));
  for (const f of files) {
    it(f, async () => {
      const v = JSON.parse(readText('testdata/intents/valid/' + f)) as Vector;
      const s = v.sign;
      const args = JSON.parse(s.args);
      const got = await signIntent({
        key: { privateKeyPem: pem(s.key) },
        org: s.org,
        agent: s.agent,
        tool: s.tool,
        args,
        ...(s.on_behalf_of ? { onBehalfOf: s.on_behalf_of } : {}),
        ...(s.session ? { session: s.session } : {}),
        ttlMs: s.ttl_ms,
        now: () => Date.parse(s.now),
        nonce: s.nonce,
      });
      expect(got.header).toBe(v.header);
      expect(got.intentHash).toBe(v.expected.intent_hash);
      expect(got.kid).toBe(v.expected.kid);
      expect(new Date(got.expiresAt).toISOString()).toBe(v.expected.expires_at);
      expect(await argsDigest(args)).toBe(v.expected.args_digest);
    });
  }

  it('the S09 cross-check literals', async () => {
    const v = JSON.parse(readText('testdata/intents/valid/v01-refund-obo.json')) as Vector;
    expect(v.expected.kid).toBe('21fe31dfa154a261626bf854046fd227');
    expect(v.expected.args_digest).toBe('sha256:f7411a7cf4706333c9ba80c85aab7d8b92363ad3077a288dadc26bef42d89d36');
    expect(v.expected.intent_hash).toBe('sha256:87c73e450a53f0269e33f7adb8430a6f89714606f9aae38bc732536fef7e3089');
    expect(v.header.split('.')[1]).toBe('TQAgBevx6iwlDM2Z7px4mrRiCjdbrAX3GCacB_gMAQbvPagJqCsJUWYz7xS7VuQqyV2rOK71uyRHvym2nqjJCA');
  });

  it('refuses what S09 refuses', async () => {
    const base = { key: { privateKeyPem: pem('test1') }, org: 'org_01k6h7kp6msmzrhdzwtcr3jnkp', agent: 'agt_01k633rrmh3bcz2p39gg4vp46q', tool: 'stripe.refund', args: {} };
    const bigInt = await signIntent({ ...base, args: { n: 9007199254740992 } }).catch((e) => e);
    expect(bigInt.code).toBe('big_int');
    expect(bigInt.message).toContain('Send it as a string');
    expect((await signIntent({ ...base, ttlMs: 300_001 }).catch((e) => e)).param).toBe('ttlMs');
    expect((await signIntent({ ...base, tool: 'Stripe.Refund' }).catch((e) => e)).message).toContain("can't be signed");
    expect((await signIntent({ ...base, org: 'org_bad' }).catch((e) => e)).param).toBe('org');
    expect((await signIntent({ ...base, onBehalfOf: 'a b' }).catch((e) => e)).param).toBe('onBehalfOf');
    expect((await signIntent({ ...base, nonce: 'XYZ' }).catch((e) => e)).param).toBe('nonce');
    // The field limits keep an intent under 2,048 bytes; the largest one still signs.
    expect((await signIntent({ ...base, onBehalfOf: 'u'.repeat(256), session: 's'.repeat(256), tool: 'stripe.' + 'r'.repeat(120) })).header).toContain('.');
    expect((await signIntent({ ...base, key: { privateKeyPem: 'not a pem' } }).catch((e) => e)).param).toBe('intentKey');
    expect((await signIntent({ ...base, key: { privateKeyPem: pem('test1'), kid: 'ffff' } }).catch((e) => e)).param).toBe('intentKey.kid');
    let deep: unknown = 1;
    for (let i = 0; i < 40; i++) deep = [deep];
    expect((await signIntent({ ...base, args: { deep } as never }).catch((e) => e)).message).toContain('32 levels');
  });

  it('generateIntentKey round-trips: the public key and kid match the PEM, and it signs', async () => {
    const k = await generateIntentKey();
    expect(k.publicKey).toMatch(/^ed25519:[A-Za-z0-9_-]{43}$/);
    const raw = createPublicKey(createPrivateKey(k.privateKeyPem)).export({ format: 'der', type: 'spki' }).subarray(-32);
    expect('ed25519:' + Buffer.from(raw).toString('base64url')).toBe(k.publicKey);
    expect(await intentKid(new Uint8Array(raw))).toBe(k.kid);
    const s = await signIntent({ key: { privateKeyPem: k.privateKeyPem }, org: 'org_01k6h7kp6msmzrhdzwtcr3jnkp', agent: 'agt_01k633rrmh3bcz2p39gg4vp46q', tool: 'billing.refund_customer', args: { a: 1 } });
    expect(s.kid).toBe(k.kid);
    expect(s.header.split('.').length).toBe(2);
  });
});
