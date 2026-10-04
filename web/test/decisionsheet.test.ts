// @vitest-environment happy-dom
// S26 §3.1 invariants for DecisionSheet: nothing is sent without the explicit button; a deny needs a
// reason (3 to 280 characters); every POST carries {reason, context_digest} and one Idempotency-Key
// per sheet (a retry reuses it); a stale context shows S22's sentence and Reload and sends nothing else.
import { readFileSync } from 'node:fs';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { configure } from '../src/lib/api.ts';
import type { ApprovalFull } from '../src/lib/models/approval.ts';
import { openDecisionSheet } from '../src/controllers/decisionsheet.ts';

const approval = JSON.parse(readFileSync('test/fixtures/s26/approval.json', 'utf8')) as ApprovalFull;

interface Call { url: string; init: RequestInit }
let calls: Call[] = [];
let answer: () => Response;

beforeEach(() => {
  calls = [];
  document.body.replaceChildren();
  window.matchMedia = ((q: string) => ({ matches: q.includes('min-width'), media: q, addEventListener() {}, removeEventListener() {} })) as never;
  configure({
    fetch: (async (url: string, init: RequestInit) => { calls.push({ url, init }); return answer(); }) as never,
    csrf: () => 'csrf-token',
    emit: () => {},
    uuid: () => 'never-used',
    sleep: async () => {},
  });
});
afterEach(() => vi.restoreAllMocks());

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const tick = () => new Promise((r) => setTimeout(r, 0));

describe('DecisionSheet', () => {
  it('deny stays disabled until the reason has 3 characters, then posts reason and digest once', async () => {
    answer = () => json(200, { ...approval, status: 'denied' });
    const done = vi.fn();
    const ov = openDecisionSheet({ approval, vote: 'deny', effect: 'INSERT ~1 row in public.refunds', onDone: done, onReload: vi.fn() });
    const go = ov.el.querySelector<HTMLButtonElement>('[data-decide="deny"]')!;
    const reason = ov.el.querySelector<HTMLTextAreaElement>('textarea')!;
    expect(go.disabled).toBe(true);
    reason.value = 'no';
    reason.dispatchEvent(new Event('input'));
    expect(go.disabled).toBe(true);
    go.click();
    expect(calls).toHaveLength(0);
    reason.value = 'Refund already issued';
    reason.dispatchEvent(new Event('input'));
    expect(go.disabled).toBe(false);
    go.click();
    go.click(); // a double tap sends once
    await tick();
    await tick();
    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toBe(`/v1/approvals/${approval.id}/deny`);
    expect(JSON.parse(String(calls[0]!.init.body))).toEqual({ reason: 'Refund already issued', context_digest: approval.context_digest });
    expect((calls[0]!.init.headers as Record<string, string>)['Idempotency-Key']).toBeTruthy();
    expect(done).toHaveBeenCalledOnce();
  });

  it('a stale context shows the sentence and Reload, and hides the submit button', async () => {
    answer = () => json(409, { error: { type: 'invalid_request_error', code: 'context_changed', message: 'What you reviewed is out of date. Reload the approval and review it again.', param: null, request_id: 'req_x', doc_url: 'https://docs.deedglass.com/errors/context_changed' } });
    const reload = vi.fn();
    const ov = openDecisionSheet({ approval, vote: 'approve', effect: 'x', onDone: vi.fn(), onReload: reload });
    const go = ov.el.querySelector<HTMLButtonElement>('[data-decide="approve"]')!;
    expect(go.disabled).toBe(false); // approve: the reason is optional
    go.click();
    await tick();
    await tick();
    expect(ov.el.textContent).toContain('What you reviewed is out of date. Reload the approval and review it again.');
    expect(go.hidden).toBe(true);
    const btn = [...ov.el.querySelectorAll('button')].find((b) => b.textContent?.includes('Reload'))!;
    btn.click();
    expect(reload).toHaveBeenCalledOnce();
    expect(calls).toHaveLength(1);
  });

  it('a retry after an error reuses the same Idempotency-Key', async () => {
    let n = 0;
    answer = () => (++n === 1 ? json(500, { error: { type: 'api_error', code: 'internal_error', message: 'Something failed on our side. Try again.', param: null, request_id: 'req_y', doc_url: '' } }) : json(200, { ...approval, status: 'approved' }));
    const ov = openDecisionSheet({ approval, vote: 'approve', effect: 'x', onDone: vi.fn(), onReload: vi.fn() });
    const go = ov.el.querySelector<HTMLButtonElement>('[data-decide="approve"]')!;
    go.click();
    await tick();
    await tick();
    expect(ov.el.textContent).toContain('Something failed on our side.');
    go.click();
    await tick();
    await tick();
    const keys = calls.map((c) => (c.init.headers as Record<string, string>)['Idempotency-Key']);
    expect(keys).toHaveLength(2);
    expect(keys[0]).toBe(keys[1]);
  });
});
