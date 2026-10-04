import { describe, expect, it } from 'vitest';
import { Deedglass, InvalidRequestError } from '../../src/index.js';
import { env, isBrowser, runtime } from '../../src/runtime.js';
import { fetchKeys, FetchKeysError, keysURL } from '../../src/verify/index.js';
import { action, json, KEY, readText, scriptedFetch } from '../helpers.js';

const base = 'http://127.0.0.1:9';
const keysJSON = readText('testdata/deeds/keys/test-keys.json');
const deedLine = readText('testdata/deeds/chains/genesis-20.jsonl').split('\n')[0]!;
const deed = JSON.parse(deedLine);

function client(...r: Parameters<typeof scriptedFetch>) {
  const s = scriptedFetch(...r);
  return { s, c: new Deedglass({ apiKey: KEY, baseUrl: base, fetch: s.fetch, maxRetries: 0 }) };
}

describe('resources', () => {
  it('deeds.get and deeds.verify (keys fetched from the server once)', async () => {
    const { s, c } = client(() => json(200, deed), () => new Response(keysJSON, { status: 200 }));
    const d = await c.deeds.get(deed.id);
    const r = await c.deeds.verify(d);
    expect(r.envelope.id).toBe(deed.id);
    expect(s.calls[1]!.url).toBe(base + '/.well-known/deedglass/keys.json');
    await expect(c.deeds.get('nope')).rejects.toThrow("isn't a deed id");
  });

  it('tools.list follows next_cursor and filters by glob', async () => {
    const t = (n: string) => ({ object: 'tool', name: n });
    const { s, c } = client(
      () => json(200, { object: 'list', data: [t('fake.echo'), t('stripe.get_charge')], has_more: true, next_cursor: 'c2' }),
      () => json(200, { object: 'list', data: [t('stripe.create_refund')], has_more: false, next_cursor: null }),
    );
    expect((await c.tools.list({ include: ['stripe.*'], includeHidden: true })).map((x) => x.name)).toEqual(['stripe.get_charge', 'stripe.create_refund']);
    expect(s.calls[1]!.url).toBe(base + '/v1/tools?include_hidden=true&cursor=c2');
  });

  it('actions.get with wait and expand, cancel, and bad ids', async () => {
    const { s, c } = client(() => json(200, action()), () => json(200, action({ status: 'cancelled' })));
    await c.actions.get('act_01j9zw0000000000000000000a', { waitMs: 5000, expand: ['request', 'result'] });
    expect(s.calls[0]!.url).toBe(base + '/v1/actions/act_01j9zw0000000000000000000a?wait=5&expand%5B%5D=request&expand%5B%5D=result');
    expect((await c.actions.cancel('act_01j9zw0000000000000000000a')).status).toBe('cancelled');
    expect(s.calls[1]!.headers['idempotency-key']).toMatch(/^sdk_/);
    await expect(c.actions.get('x')).rejects.toThrow("isn't an action id");
  });

  it('waitUntilSettled gives up at its timeout and returns the last state', async () => {
    const { c } = client(() => json(200, action({ status: 'pending_approval' })));
    const a = await c.actions.waitUntilSettled('act_01j9zw0000000000000000000a', { timeoutMs: 1 });
    expect(a.status).toBe('pending_approval');
  });

  it('intentKey signs act() with org and agent from whoami', async () => {
    const pem = readText('testdata/intents/keys/test1.pem');
    const s = scriptedFetch(
      () => json(200, { object: 'whoami', org: { id: 'org_01k6h7kp6msmzrhdzwtcr3jnkp' }, agent: { id: 'agt_01k633rrmh3bcz2p39gg4vp46q' }, key: {} }),
      () => json(200, action({ status: 'executed' })),
    );
    const c = new Deedglass({ apiKey: KEY, baseUrl: base, fetch: s.fetch, intentKey: { privateKeyPem: pem } });
    await c.act('stripe.refund', { amount: 1 }, { onBehalfOf: 'usr_1', session: 'conv_1' });
    const h = s.calls[1]!.headers;
    expect(h['deedglass-intent']).toMatch(/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{86}$/);
    expect(h['deedglass-on-behalf-of']).toBe('usr_1');
    expect(h['deedglass-session']).toBe('conv_1');
    const intent = JSON.parse(Buffer.from(h['deedglass-intent']!.split('.')[0]!, 'base64url').toString());
    expect(intent).toMatchObject({ v: 'deedglass.intent/1', org: 'org_01k6h7kp6msmzrhdzwtcr3jnkp', tool: 'stripe.refund', on_behalf_of: 'usr_1', session: 'conv_1' });
  });

  it('refuses arrays as args and bad idempotency keys', async () => {
    const { c } = client(() => json(200, {}));
    expect(await c.act('a.b', [] as never).catch((e) => e)).toBeInstanceOf(InvalidRequestError);
    expect((await c.act('ab.b', {}, { idempotencyKey: 'é' }).catch((e) => e)).code).toBe('idempotency_key_invalid');
  });
});

describe('fetchKeys', () => {
  it('https only, http only on this machine', () => {
    expect(keysURL('https://deedglass.com/.well-known/deedglass/keys.json')).not.toBeNull();
    expect(keysURL('http://localhost:8080/k')).not.toBeNull();
    expect(keysURL('http://example.com/k')).toBeNull();
    expect(keysURL('https://u:p@example.com/k')).toBeNull();
    expect(keysURL('::')).toBeNull();
  });

  it('pins the source, omits credentials, refuses redirects; errors for status, size and refusal', async () => {
    let init: RequestInit = {};
    const ok = await fetchKeys('http://127.0.0.1:9/k', { fetch: (async (_u: unknown, i: RequestInit) => ((init = i), new Response(keysJSON))) as never });
    expect(ok.pinned).toBe(true);
    expect(ok.source).toBe('http://127.0.0.1:9/k');
    expect(init).toMatchObject({ credentials: 'omit', redirect: 'error', referrerPolicy: 'no-referrer' });
    await expect(fetchKeys('http://example.com/k')).rejects.toBeInstanceOf(FetchKeysError);
    await expect(fetchKeys('http://127.0.0.1:9/k', { fetch: (async () => new Response('x', { status: 404 })) as never })).rejects.toThrow('it answered 404');
    await expect(fetchKeys('http://127.0.0.1:9/k', { fetch: (async () => new Response('x'.repeat((1 << 20) + 1))) as never })).rejects.toThrow('larger than 1 MiB');
    await expect(fetchKeys('http://127.0.0.1:9/k', { fetch: (async () => { throw new TypeError('redirect'); }) as never })).rejects.toThrow('refused, redirected or blocked');
  });
});

describe('runtime', () => {
  it('names the runtime and reads the environment', () => {
    expect(runtime()).toMatch(/^node\//);
    expect(isBrowser()).toBe(false);
    process.env.DEEDGLASS_TEST_X = 'y';
    expect(env('DEEDGLASS_TEST_X')).toBe('y');
    delete process.env.DEEDGLASS_TEST_X;
    expect(env('DEEDGLASS_TEST_X')).toBeUndefined();
  });
});
