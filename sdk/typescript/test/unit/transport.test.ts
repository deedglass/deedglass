import { inspect } from 'node:util';
import { afterEach, describe, expect, it } from 'vitest';
import {
  APIError,
  AuthenticationError,
  BudgetExceededError,
  ConnectionError,
  Deedglass,
  DeedglassError,
  InvalidRequestError,
  isDeedglassError,
  PolicyDeniedError,
  RateLimitError,
  TimeoutError,
  API_VERSION,
} from '../../src/index.js';
import { backoffMs, retryAfterMs } from '../../src/http.js';
import { action, denied, json, KEY, PENDING, scriptedFetch } from '../helpers.js';

const base = 'http://127.0.0.1:9';
const executed = action({ mode: 'gateway', status: 'executed', state: 'sealed', deed: 'deed_01j9zw0000000000000000000a', next_action: null, execution: { status: 'succeeded', result: { ok: true } } });

function client(f: typeof fetch, extra: Record<string, unknown> = {}) {
  return new Deedglass({ apiKey: KEY, baseUrl: base, fetch: f, ...extra });
}

describe('transport and client (S31 §6)', () => {
  it('200 executed carries the version and an Idempotency-Key', async () => {
    const s = scriptedFetch(() => json(200, executed));
    const a = await client(s.fetch).act('billing.refund_customer', { customer: 'cus_123', amount: 2000 });
    expect(a.status).toBe('executed');
    expect(a.deed).toMatch(/^deed_/);
    expect(a.meta.requestId).toBe('req_test');
    const h = s.calls[0]!.headers;
    expect(h['deedglass-version']).toBe(API_VERSION);
    expect(h['idempotency-key']).toMatch(/^sdk_[0-9a-f-]{36}$/);
    expect(h['authorization']).toBe('Bearer ' + KEY);
    expect(h['user-agent']).toMatch(/^deedglass-sdk-typescript\/0\.14\.0 \(node\//);
    expect(s.calls[0]!.body).toMatchObject({ tool: 'billing.refund_customer', mode: 'gateway', wait: 10, context: { client: { name: '@deedglass/sdk', version: '0.14.1' }, transport: 'sdk' } });
  });

  it('callId becomes context.call_id; an id outside the API rule is left out (S32)', async () => {
    const s = scriptedFetch(() => json(200, action()), () => json(200, action()));
    const c = client(s.fetch);
    await c.act('billing.refund_customer', { amount: 1 }, { callId: 'toolu_01AbC' });
    await c.act('billing.refund_customer', { amount: 1 }, { callId: 'has a space' });
    expect((s.calls[0]!.body as { context: { call_id?: string } }).context.call_id).toBe('toolu_01AbC');
    expect((s.calls[1]!.body as { context: object }).context).not.toHaveProperty('call_id');
  });

  it('202 pending without wait returns pending; with wait it long-polls until executed', async () => {
    const pend = action({ ...PENDING, mode: 'gateway' });
    let s = scriptedFetch(() => json(202, pend));
    expect((await client(s.fetch).act('billing.refund_customer', { amount: 12000 })).status).toBe('pending_approval');
    s = scriptedFetch(() => json(202, pend), () => json(200, pend), () => json(200, executed));
    const a = await client(s.fetch).act('billing.refund_customer', { amount: 12000 }, { approval: { wait: true, timeoutMs: 60_000 } });
    expect(a.status).toBe('executed');
    expect(s.calls[1]!.method).toBe('GET');
    expect(s.calls[1]!.url).toContain('/v1/actions/act_01j9zw0000000000000000000a?wait=25');
  });

  it('403 policy_denied → PolicyDeniedError with the message verbatim and the sealed action', async () => {
    const s = scriptedFetch(() => denied());
    const err = await client(s.fetch).act('billing.delete_customer', { customer: 'cus_123' }).catch((e) => e);
    expect(err).toBeInstanceOf(PolicyDeniedError);
    expect(err.message).toBe('Blocked by policy "no-deletes": Agents never delete customers.');
    expect(err.action.deed).toMatch(/^deed_/);
    expect(err.docUrl).toBe('https://docs.deedglass.com/errors/policy_denied');
    expect(s.calls.length).toBe(1);
  });

  it('429 budget_exceeded is never retried; rate_limited is retried after Retry-After', async () => {
    let s = scriptedFetch(() => json(429, { error: { type: 'rate_limit_error', code: 'budget_exceeded', message: 'Budget used.' } }, { 'retry-after': '2' }));
    const err = await client(s.fetch).act('billing.refund_customer', {}).catch((e) => e);
    expect(err).toBeInstanceOf(BudgetExceededError);
    expect(err.retryAfterMs).toBe(2000);
    expect(s.calls.length).toBe(1);
    s = scriptedFetch(() => json(429, { error: { type: 'rate_limit_error', code: 'rate_limited', message: 'Slow down.' } }, { 'retry-after': '2' }), () => json(200, executed));
    const t0 = Date.now();
    await client(s.fetch).act('billing.refund_customer', {});
    expect(Date.now() - t0).toBeGreaterThanOrEqual(1900);
    expect(s.calls.length).toBe(2);
    s = scriptedFetch(() => json(429, { error: { type: 'rate_limit_error', code: 'rate_limited', message: 'Slow down.' } }, { 'retry-after': '0' }));
    expect(await client(s.fetch, { maxRetries: 0 }).act('billing.refund_customer', {}).catch((e) => e)).toBeInstanceOf(RateLimitError);
  });

  it('503, 503, 200 on POST: three attempts with the same Idempotency-Key', async () => {
    const s = scriptedFetch(() => json(503, { error: { type: 'api_error', code: 'service_unavailable', message: 'Down.' } }), () => json(503, {}), () => json(200, executed));
    await client(s.fetch).act('billing.refund_customer', {}, { idempotencyKey: 'mine-1' });
    expect(s.calls.length).toBe(3);
    expect(new Set(s.calls.map((c) => c.headers['idempotency-key']))).toEqual(new Set(['mine-1']));
    const s2 = scriptedFetch(() => json(500, { error: { type: 'api_error', code: 'internal_error', message: 'Oops.' } }));
    expect(await client(s2.fetch, { maxRetries: 0 }).act('billing.refund_customer', {}).catch((e) => e)).toBeInstanceOf(APIError);
  });

  it('500 is never retried (the server replays a stored 5xx under the same key; Python parity, S35)', async () => {
    const s = scriptedFetch(() => json(500, { error: { type: 'api_error', code: 'internal_error', message: 'Oops.' } }), () => json(200, executed));
    expect(await client(s.fetch).act('billing.refund_customer', {}).catch((e) => e)).toBeInstanceOf(APIError);
    expect(s.calls.length).toBe(1);
  });

  it('409 idempotency_key_in_use is retried with the same key; another 409 is not', async () => {
    const busy = () => json(409, { error: { type: 'invalid_request_error', code: 'idempotency_key_in_use', message: 'Busy.' } });
    const s = scriptedFetch(busy, () => json(200, executed));
    await client(s.fetch).act('billing.refund_customer', {}, { idempotencyKey: 'mine-2' });
    expect(s.calls.length).toBe(2);
    const s2 = scriptedFetch(() => json(409, { error: { type: 'invalid_request_error', code: 'idempotency_key_reused', message: 'Reused.' } }), () => json(200, executed));
    expect((await client(s2.fetch).act('billing.refund_customer', {}).catch((e) => e)).code).toBe('idempotency_key_reused');
    expect(s2.calls.length).toBe(1);
  });

  it('network errors × 3 → ConnectionError after maxRetries', async () => {
    const s = scriptedFetch(() => 'network');
    const err = await client(s.fetch).act('billing.refund_customer', {}).catch((e) => e);
    expect(err).toBeInstanceOf(ConnectionError);
    expect(s.calls.length).toBe(3);
    expect(err.message).toContain('127.0.0.1:9');
  });

  it('timeoutMs 50 with a silent server → TimeoutError at about 50 ms, no retry beyond it', async () => {
    const s = scriptedFetch(() => 'hang');
    const t0 = Date.now();
    const err = await client(s.fetch, { timeoutMs: 50 }).actions.get('act_01j9zw0000000000000000000a').catch((e) => e);
    expect(err).toBeInstanceOf(TimeoutError);
    expect(Date.now() - t0).toBeLessThan(1000);
    expect(s.calls.length).toBe(1);
    expect(err.message).toContain('act_01j9zw0000000000000000000a in Console › Actions');
  });

  it('Deedglass-Idempotent-Replayed: true → meta.replayed', async () => {
    const s = scriptedFetch(() => json(200, executed, { 'deedglass-idempotent-replayed': 'true' }));
    expect((await client(s.fetch).act('billing.refund_customer', {})).meta.replayed).toBe(true);
  });

  it('an unknown error code is still a DeedglassError with that code', async () => {
    const s = scriptedFetch(() => json(409, { error: { type: 'brand_new_error', code: 'brand_new', message: 'New.' } }));
    const err = await client(s.fetch).act('billing.refund_customer', {}).catch((e) => e);
    expect(isDeedglassError(err)).toBe(true);
    expect(err.code).toBe('brand_new');
    expect(err).toBeInstanceOf(DeedglassError);
  });

  it('a bad or missing key fails before any fetch', () => {
    const s = scriptedFetch(() => json(200, {}));
    for (const k of ['sk_live_' + 'x'.repeat(24), '', 'dg_test_short']) {
      expect(() => new Deedglass({ apiKey: k, baseUrl: base, fetch: s.fetch })).toThrow(AuthenticationError);
    }
    const prev = process.env.DEEDGLASS_API_KEY;
    delete process.env.DEEDGLASS_API_KEY;
    try {
      expect(() => new Deedglass({ baseUrl: base, fetch: s.fetch })).toThrow('No API key. Set DEEDGLASS_API_KEY');
    } finally {
      if (prev !== undefined) process.env.DEEDGLASS_API_KEY = prev;
    }
    expect(s.calls.length).toBe(0);
  });

  it('reads DEEDGLASS_API_KEY and DEEDGLASS_SERVER', async () => {
    process.env.DEEDGLASS_API_KEY = KEY;
    process.env.DEEDGLASS_SERVER = 'http://127.0.0.1:7/';
    try {
      const s = scriptedFetch(() => json(200, { object: 'whoami', org: { id: 'org_x' }, agent: null, key: {} }));
      const c = new Deedglass({ fetch: s.fetch });
      expect(c.baseUrl).toBe('http://127.0.0.1:7');
      await c.whoami();
      await c.whoami(); // cached
      expect(s.calls.length).toBe(1);
    } finally {
      delete process.env.DEEDGLASS_API_KEY;
      delete process.env.DEEDGLASS_SERVER;
    }
  });

  it('the key never appears in inspect, JSON, toString or any error message', async () => {
    const s = scriptedFetch(() => json(401, { error: { type: 'authentication_error', code: 'invalid_api_key', message: 'The API key isn\'t valid.' } }), () => 'network');
    const c = client(s.fetch, { maxRetries: 0 });
    const outs = [inspect(c), JSON.stringify(c), String(c), inspect(c, { showHidden: true, depth: 5 })];
    for (const o of outs) expect(o).not.toContain(KEY.slice(17));
    expect(String(c)).toContain("apiKey: 'dg_test_Ab12Cd34_…'");
    const errs = [await c.whoami().catch((e) => e), await c.act('billing.x', {}).catch((e) => e), await c.act('Bad Name', {}).catch((e) => e)];
    for (const e of errs) {
      expect(e).toBeInstanceOf(DeedglassError);
      expect(e.message + inspect(e)).not.toContain(KEY.slice(17));
    }
  });

  it('window and document → browser_not_supported unless dangerouslyAllowBrowser', () => {
    const g = globalThis as Record<string, unknown>;
    g.window = {};
    g.document = {};
    try {
      expect(() => new Deedglass({ apiKey: KEY, baseUrl: base })).toThrow(/doesn't run in a browser/);
      expect(new Deedglass({ apiKey: KEY, baseUrl: base, dangerouslyAllowBrowser: true })).toBeInstanceOf(Deedglass);
    } finally {
      delete g.window;
      delete g.document;
    }
  });

  it('refuses bad tool names, refs and args before any request', async () => {
    const s = scriptedFetch(() => json(200, executed));
    const c = client(s.fetch);
    const e1 = await c.act('Billing.Refund', {}).catch((e) => e);
    expect(e1.message).toBe('"Billing.Refund" isn\'t a Deedglass tool name. Use "connector.tool", for example "billing.refund_customer".');
    expect(await c.act('billing.refund', {}, { onBehalfOf: 'has space' }).catch((e) => e)).toBeInstanceOf(InvalidRequestError);
    const big = await c.act('billing.refund', { n: 2 ** 53 }).catch((e) => e);
    expect(big.code).toBe('big_int');
    expect(big.message).toContain('Send it as a string');
    for (const bad of [{ f: () => 1 }, { n: NaN }, { d: new Date() }, { b: 10n }]) expect(await c.act('billing.refund', bad as never).catch((e) => e)).toBeInstanceOf(InvalidRequestError);
    const cyc: Record<string, unknown> = {};
    cyc.self = cyc;
    expect((await c.act('billing.refund', cyc as never).catch((e) => e)).message).toContain('cycle');
    expect(await c.act('billing.refund', { big: 'x'.repeat(300_000) }).catch((e) => e)).toBeInstanceOf(InvalidRequestError);
    expect(s.calls.length).toBe(0);
  });

  it('retry timing helpers', () => {
    expect(backoffMs(0, () => 0)).toBe(375);
    expect(backoffMs(2, () => 1)).toBe(2500);
    expect(retryAfterMs(new Headers({ 'retry-after': '1.5' }))).toBe(1500);
    expect(retryAfterMs(new Headers())).toBe(0);
    expect(retryAfterMs(new Headers({ 'retry-after': new Date(Date.now() + 3000).toUTCString() }))).toBeGreaterThan(1000);
  });

  it('a 2xx that isn\'t JSON is an APIError', async () => {
    const s = scriptedFetch(() => new Response('<html>', { status: 200 }));
    expect(await client(s.fetch).whoami().catch((e) => e)).toBeInstanceOf(APIError);
  });
});

afterEach(() => {
  delete (globalThis as Record<string, unknown>).window;
});
