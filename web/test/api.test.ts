import { describe, expect, it, vi } from 'vitest';
import { api, ApiError, configure, list } from '../src/lib/api.ts';

function json(status: number, body: unknown, headers: Record<string, string> = {}) {
  return new Response(body === null ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });
}
const errBody = (code: string, status = 400) => ({ error: { type: 'invalid_request_error', code, message: `msg ${code}`, param: null, request_id: 'req_01k6', doc_url: `https://docs.deedglass.com/errors/${code}` } });

describe('api', () => {
  it('sends the version, CSRF and one Idempotency-Key reused on a csrf retry', async () => {
    const calls: RequestInit[] = [];
    let csrf = 'old';
    const fetch = vi.fn(async (_u: RequestInfo | URL, init?: RequestInit) => {
      calls.push(init!);
      return calls.length === 1 ? json(403, errBody('csrf_failed')) : json(200, { ok: true });
    });
    const emit = vi.fn();
    configure({ fetch: fetch as never, csrf: () => csrf, refreshCsrf: async () => { csrf = 'new'; }, emit, uuid: () => 'u-1', sleep: async () => {} });
    const out = await api<{ ok: boolean }>('POST', '/v1/org/stop-all', { body: { reason: 'x' } });
    expect(out.ok).toBe(true);
    const h0 = calls[0]!.headers as Record<string, string>;
    const h1 = calls[1]!.headers as Record<string, string>;
    expect(h0['Deedglass-Version']).toBe('2026-10-01');
    expect(h0['Deedglass-CSRF']).toBe('old');
    expect(h1['Deedglass-CSRF']).toBe('new');
    expect(h0['Idempotency-Key']).toBe('u-1');
    expect(h1['Idempotency-Key']).toBe('u-1');
    expect(calls[0]!.redirect).toBe('error');
    expect(calls[0]!.credentials).toBe('same-origin');
  });
  it('keeps the envelope verbatim and emits session-ended on 401', async () => {
    const emit = vi.fn();
    configure({ fetch: (async () => json(401, errBody('session_expired'))) as never, emit });
    const e = (await api('GET', '/v1/me').catch((x: unknown) => x)) as ApiError;
    expect(e).toBeInstanceOf(ApiError);
    expect(e.code).toBe('session_expired');
    expect(e.message).toBe('msg session_expired');
    expect(e.requestId).toBe('req_01k6');
    expect(e.docUrl).toContain('/errors/session_expired');
    expect(emit).toHaveBeenCalledWith('dg:session-ended', e);
  });
  it('waits for Retry-After once on 429', async () => {
    const sleep = vi.fn(async () => {});
    let n = 0;
    configure({ fetch: (async () => (++n === 1 ? json(429, errBody('rate_limited'), { 'Retry-After': '2' }) : json(200, { ok: 1 }))) as never, sleep, emit: () => {} });
    await api('GET', '/v1/agents');
    expect(sleep).toHaveBeenCalledWith(2000);
    expect(n).toBe(2);
  });
  it('reports a network failure as offline', async () => {
    const emit = vi.fn();
    configure({ fetch: (async () => { throw new TypeError('Failed to fetch'); }) as never, emit });
    const e = (await api('GET', '/v1/agents').catch((x: unknown) => x)) as ApiError;
    expect(e.status).toBe(0);
    expect(emit).toHaveBeenCalledWith('dg:offline');
  });
  it('GETs carry no CSRF or Idempotency-Key; list follows next_cursor', async () => {
    const urls: string[] = [];
    const heads: Record<string, string>[] = [];
    configure({
      fetch: (async (u: string, init: RequestInit) => {
        urls.push(u);
        heads.push(init.headers as Record<string, string>);
        return urls.length === 1 ? json(200, { object: 'list', data: [1, 2], has_more: true, next_cursor: 'c2' }) : json(200, { object: 'list', data: [3], has_more: false, next_cursor: null });
      }) as never,
      csrf: () => 'tok',
      emit: () => {},
    });
    const pages: number[][] = [];
    for await (const p of list<number>('/v1/actions', { limit: '2' })) pages.push(p);
    expect(pages).toEqual([[1, 2], [3]]);
    expect(urls).toEqual(['/v1/actions?limit=2', '/v1/actions?limit=2&cursor=c2']);
    expect(heads[0]!['Deedglass-CSRF']).toBeUndefined();
    expect(heads[0]!['Idempotency-Key']).toBeUndefined();
  });
});
