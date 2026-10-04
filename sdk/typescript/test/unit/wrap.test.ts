import { createHash } from 'node:crypto';
import { describe, expect, it, vi } from 'vitest';
import { ApprovalPendingError, Deedglass, PolicyDeniedError, ToolExecutionError, wrapTool } from '../../src/index.js';
import { asJson, clock, resultPart } from '../../src/wrap.js';
import { canonicalize } from '../../src/verify/jcs.js';
import { action, denied, json, KEY, PENDING, scriptedFetch } from '../helpers.js';

const base = 'http://127.0.0.1:9';
const sealed = action({ status: 'executed', state: 'sealed', deed: 'deed_01j9zw0000000000000000000a', next_action: null });

function setup(...responders: Parameters<typeof scriptedFetch>) {
  const s = scriptedFetch(...responders);
  const warn = vi.fn();
  const c = new Deedglass({ apiKey: KEY, baseUrl: base, fetch: s.fetch, logger: { warn, debug: () => {} } });
  return { s, c, warn };
}

describe('wrapTool (S31 §6)', () => {
  it('authorized → fn runs once and /result gets succeeded with the result', async () => {
    const { s, c } = setup(() => json(200, action()), () => json(200, sealed));
    const fn = vi.fn(async () => ({ ok: true }));
    const onSealed = vi.fn();
    const refund = wrapTool(fn, { client: c, tool: 'billing.refund_customer', onSealed });
    expect(await refund({ customer: 'cus_123', amount: 2000 })).toEqual({ ok: true });
    expect(fn).toHaveBeenCalledTimes(1);
    expect((fn.mock.calls[0] as unknown[])[1]).toMatchObject({ actionId: 'act_01j9zw0000000000000000000a' });
    expect(s.calls[0]!.body).toMatchObject({ mode: 'proxy', connector_type: 'sdk', wait: 0, tool: 'billing.refund_customer' });
    expect(s.calls[1]!.url).toBe(base + '/v1/actions/act_01j9zw0000000000000000000a/result');
    expect(s.calls[1]!.body).toMatchObject({ status: 'succeeded', result: { ok: true } });
    expect(s.calls[1]!.headers['idempotency-key']).toBe('sdkr_act_01j9zw0000000000000000000a');
    expect(onSealed).toHaveBeenCalledTimes(1);
  });

  it('denied → fn never runs; PolicyDeniedError', async () => {
    const { c } = setup(() => denied());
    const fn = vi.fn();
    const err = await wrapTool(fn, { client: c, tool: 'billing.delete_customer' })({ customer: 'cus_123' }).catch((e) => e);
    expect(err).toBeInstanceOf(PolicyDeniedError);
    expect(fn).not.toHaveBeenCalled();
  });

  it('fn throws (default) → unknown + tool_error; ToolExecutionError with cause', async () => {
    const { s, c } = setup(() => json(200, action()), () => json(200, sealed));
    const boom = new Error('card declined for Bearer abcdefgh12345678 and sk_test_' + 'x'.repeat(24));
    const err = await wrapTool(async () => { throw boom; }, { client: c, tool: 'billing.refund_customer' })({ amount: 1 }).catch((e) => e);
    expect(err).toBeInstanceOf(ToolExecutionError);
    expect(err.cause).toBe(boom);
    expect(err.message).toContain('UNVERIFIABLE because it may have run');
    const b = s.calls[1]!.body as Record<string, string>;
    expect(b).toMatchObject({ status: 'unknown', error_code: 'tool_error' });
    expect(b.error_message).toBe('card declined for Bearer [redacted] and sk_test_[redacted]');
  });

  it("fn throws with failureMeans 'not_applied' → failed", async () => {
    const { s, c } = setup(() => json(200, action()), () => json(200, sealed));
    await wrapTool(async () => { throw new Error('bad input'); }, { client: c, tool: 'billing.refund_customer', failureMeans: 'not_applied' })({}).catch(() => {});
    expect(s.calls[1]!.body).toMatchObject({ status: 'failed', error_code: 'tool_error' });
  });

  it("a 300 KiB result sends only result_digest, equal to Go's JCS digest", async () => {
    const { s, c } = setup(() => json(200, action()), () => json(200, sealed));
    const big = { rows: Array.from({ length: 3000 }, (_, i) => ({ i, pad: 'x'.repeat(100) })) };
    await wrapTool(async () => big, { client: c, tool: 'billing.export' })({});
    const b = s.calls[1]!.body as Record<string, unknown>;
    expect(b.result).toBeUndefined();
    expect(b.result_digest).toBe('sha256:' + createHash('sha256').update(canonicalize(big as never)).digest('hex'));
  });

  it('undefined, BigInt and cycles → toResult or the {text} fallback, no crash', async () => {
    const cyc: Record<string, unknown> = { a: 1 };
    cyc.me = cyc;
    for (const [v, want] of [[undefined, { text: 'undefined' }], [10n, { text: '10' }], [cyc, { text: '[object Object]' }]] as const) {
      const { s, c } = setup(() => json(200, action()), () => json(200, sealed));
      await wrapTool(async () => v, { client: c, tool: 'billing.x' })({});
      expect((s.calls[1]!.body as Record<string, unknown>).result).toEqual(want);
    }
    const { s, c } = setup(() => json(200, action()), () => json(200, sealed));
    await wrapTool(async () => 10n, { client: c, tool: 'billing.x', toResult: (r) => ({ n: String(r) }) })({});
    expect((s.calls[1]!.body as Record<string, unknown>).result).toEqual({ n: '10' });
  });

  it('pending, then an identical call after approval: fn runs once, no second action', async () => {
    const pend = action(PENDING);
    const { s, c } = setup(() => json(202, pend), () => json(200, action()), () => json(200, sealed));
    const fn = vi.fn(async () => 'done');
    const refund = wrapTool(fn, { client: c, tool: 'billing.refund_customer' });
    const err = await refund({ amount: 12000 }).catch((e) => e);
    expect(err).toBeInstanceOf(ApprovalPendingError);
    expect(err.approvalId).toBe('apr_01j9zv0000000000000000000a');
    expect(err.message).toContain('Refunds over $50.00 wait for a human. Waiting for approval apr_01j9zv0000000000000000000a. It expires at 10:42 UTC.');
    expect(fn).not.toHaveBeenCalled();
    expect(await refund({ amount: 12000 })).toBe('done');
    expect(fn).toHaveBeenCalledTimes(1);
    expect(s.calls.map((x) => x.method + ' ' + new URL(x.url).pathname)).toEqual([
      'POST /v1/actions',
      'GET /v1/actions/act_01j9zw0000000000000000000a',
      'POST /v1/actions/act_01j9zw0000000000000000000a/result',
    ]);
  });

  it('two identical calls racing after approval: fn runs once (the lock)', async () => {
    const pend = action(PENDING);
    const { s, c } = setup(
      () => json(202, pend),
      () => json(200, action()),
      () => json(200, action()),
      async () => {
        await new Promise((r) => setTimeout(r, 20));
        return json(200, sealed);
      },
    );
    let runs = 0;
    const refund = wrapTool(async () => {
      runs++;
      await new Promise((r) => setTimeout(r, 30));
      return 'done';
    }, { client: c, tool: 'billing.refund_customer' });
    await refund({ amount: 12000 }).catch(() => {});
    const [a, b] = await Promise.all([refund({ amount: 12000 }), refund({ amount: 12000 })]);
    expect([a, b]).toEqual(['done', 'done']);
    expect(runs).toBe(1);
    expect(s.calls.filter((x) => x.url.endsWith('/result')).length).toBe(1);
  });

  it("onPending 'wait' long-polls until authorized, then runs", async () => {
    const pend = action(PENDING);
    const { c } = setup(() => json(202, pend), () => json(200, action()), () => json(200, sealed));
    const fn = vi.fn(async () => 1);
    expect(await wrapTool(fn, { client: c, tool: 'billing.refund_customer', onPending: 'wait' })({})).toBe(1);
  });

  it('a person denies the pending action → PolicyDeniedError, the registry forgets it', async () => {
    const pend = action(PENDING);
    const den = action({ status: 'denied', state: 'sealed', approval: { id: 'apr_x', status: 'denied', expires_at: '2026-10-10T10:42:00.000Z', decision_reason: 'Not this customer.' } });
    const { c } = setup(() => json(202, pend), () => json(200, den));
    const refund = wrapTool(async () => 1, { client: c, tool: 'billing.refund_customer' });
    await refund({}).catch(() => {});
    const err = await refund({}).catch((e) => e);
    expect(err).toBeInstanceOf(PolicyDeniedError);
    expect(err.message).toBe('A person denied this action: Not this customer.');
  });

  it('/result 409 on a retry counts as reported', async () => {
    const { c, warn } = setup(() => json(200, action()), () => json(409, { error: { type: 'invalid_request_error', code: 'conflict', message: 'Already reported.' } }));
    expect(await wrapTool(async () => 1, { client: c, tool: 'billing.x' })({})).toBe(1);
    expect(warn).not.toHaveBeenCalled();
  });

  it('a report that keeps failing warns that the server will seal edge_lost', async () => {
    const { c, warn } = setup(() => json(200, action({ next_action: { type: 'execute_at_edge', deadline: new Date(Date.now() + 1500).toISOString() } })), () => 'network');
    expect(await wrapTool(async () => 1, { client: c, tool: 'billing.x' })({})).toBe(1);
    expect(warn).toHaveBeenCalledWith(expect.stringContaining('(edge_lost)'));
  }, 20_000);

  it('a bad tool name fails at wrap time', () => {
    const { c } = setup(() => json(200, {}));
    expect(() => wrapTool(async () => 1, { client: c, tool: 'Billing.Refund' })).toThrow('isn\'t a Deedglass tool name');
  });

  it('helpers', async () => {
    expect(clock('2026-10-10T10:42:00.000Z')).toBe('10:42 UTC');
    expect(clock('nope')).toBe('its expiry');
    expect(asJson({ a: [1, 'x', null, { b: true }], u: undefined })).toEqual({ a: [1, 'x', null, { b: true }] });
    expect(asJson(new Map())).toBeUndefined();
    expect(await resultPart({ a: 1 })).toEqual({ result: { a: 1 } });
  });
});
