// overview.ts: the tiles' words, the attention sentences (kinds and data in, words out), the
// Verification bar's segments and the live counters between refetches (S25 §3.3).
import { describe, expect, it } from 'vitest';
import { applyLive, attentionLine, blockedLine, delta, pct, refHref, segments, sparkLabel, waitingLine } from '../src/lib/overview.ts';
import type { AttentionItem, DeedglassEvent, Overview } from '../src/lib/types.ts';
import { read, repo } from './helpers.ts';

const now = new Date('2026-10-09T14:00:00Z');
const base = (): Overview => JSON.parse(read(repo('web/test/fixtures/overview.json')));
const item = (kind: AttentionItem['kind'], data: Record<string, unknown>, ref: string | null = null): AttentionItem => ({ object: 'attention_item', kind, severity: 'high', at: '2026-10-09T13:41:09Z', ref, data });
const t = (iso: string) => iso.slice(11, 16);

describe('tiles', () => {
  it('delta is neutral words, with the window', () => {
    expect(delta(1204, 1115, '24h')).toBe('+8% vs previous 24 hours');
    expect(delta(90, 100, '7d')).toBe('−10% vs previous 7 days');
    expect(delta(5, 0, '24h')).toBe('None in the previous 24 hours');
    expect(delta(0, 0, '24h')).toBe('Same as the previous 24 hours');
  });
  it('blocked and waiting lines', () => {
    const o = base();
    o.actions.total = 1204;
    o.blocked.total = 37;
    expect(pct(37, 1204)).toBe('3.1');
    expect(blockedLine(o)).toBe('3.1% of actions · policy, budget and Stop');
    o.approvals = { pending: 2, expiring_soon: 1, next_expires_at: '2026-10-09T14:06:00Z' };
    expect(waitingLine(o, now)).toBe('1 expires in 6 min');
    o.approvals = { pending: 0, expiring_soon: 0, next_expires_at: null };
    expect(waitingLine(o, now)).toBe('None waiting');
  });
  it('sparkline labels state their window', () => {
    expect(sparkLabel('Actions', [12, 61, 30], '24h')).toBe('Actions per hour, last 24 hours: min 12, max 61');
    expect(sparkLabel('Actions', [1], '7d')).toBe('Actions per 6 hours, last 7 days: min 1, max 1');
  });
  it('segments link to the pre-filtered feed', () => {
    const s = segments(base());
    expect(s.map((x) => x.key)).toEqual(['VERIFIED', 'MISMATCH', 'UNVERIFIABLE', 'not_witnessed']);
    expect(s[1]!.href).toBe('/app/actions?verdict=MISMATCH&time=24h');
    expect(s.reduce((a, x) => a + x.share, 0)).toBeCloseTo(1);
  });
});

describe('attention', () => {
  it.each([
    [item('mismatch', { tool: 'postgres.execute', agent: 'support-bot', summary: 'Postgres changed 301 rows in public.orders. The agent reported 1.' }, 'act_01k633rrmh3bcz2p39gg4vp46q'), "Doesn't match · postgres.execute · support-bot", 'Postgres changed 301 rows in public.orders. The agent reported 1.'],
    [item('approval_expiring', { tool: 'stripe.create_refund', agent: 'support-bot', expires_at: '2026-10-09T14:06:00Z' }, 'apr_01k633rrmh3bcz2p39gg4vp46q'), 'Approval for stripe.create_refund expires in 6 min', 'support-bot'],
    [item('slot_lagging', { connector: 'postgres', lag_bytes: 412 * 1024 * 1024 }), 'Checks for postgres are 412 MB behind. Proven verdicts will arrive late.', ''],
    [item('budget_near', { agent: 'support-bot', budget: 'calls_per_day', used: 4210, limit: 5000 }), 'support-bot used 4,210 of 5,000 calls today.', ''],
    [item('org_stopped', { since: '2026-10-09T14:05:00Z', by: 'Priya Raman', reason: 'Investigating' }), 'All agents in Acme are stopped · since 14:05 · by Priya Raman', '“Investigating”'],
    [item('chain_broken', { seq: 812, message: 'Deed 812 was changed after it was sealed.' }), 'Chain broken at record 812', 'Deed 812 was changed after it was sealed.'],
  ])('%#', (it_, title, detail) => {
    const l = attentionLine(it_, 'Acme', now, 'UTC', t);
    expect(l.title).toBe(title);
    expect(l.detail).toBe(detail);
  });
  it('links only well-formed refs', () => {
    expect(refHref('act_01k633rrmh3bcz2p39gg4vp46q')).toBe('/app/actions/act_01k633rrmh3bcz2p39gg4vp46q');
    expect(refHref('act_<script>')).toBeNull();
    expect(refHref('javascript:alert(1)')).toBeNull();
    expect(refHref(null)).toBeNull();
  });
});

describe('applyLive', () => {
  const ev = (type: string, obj: Record<string, unknown>): DeedglassEvent => ({ object: 'event', id: 'evt', type, api_version: '', created_at: '', livemode: false, org: '', hydrated: true, data: { object: obj } });
  it('moves the counters once per event', () => {
    const o = base();
    const seen = new Set<string>();
    const a0 = o.actions.total;
    const nw = o.verification.not_witnessed;
    expect(applyLive(o, ev('action.created', { id: 'act_1' }), seen)).toBe(true);
    expect(applyLive(o, ev('action.created', { id: 'act_1' }), seen)).toBe(false); // seen twice
    expect(o.actions.total).toBe(a0 + 1);
    expect(applyLive(o, ev('action.decided', { id: 'act_1', decision: { outcome: 'deny' } }), seen)).toBe(true);
    expect(applyLive(o, ev('action.decided', { id: 'act_2', decision: { outcome: 'allow' } }), seen)).toBe(false);
    const v = o.verification.mismatch;
    expect(applyLive(o, ev('action.witnessed', { id: 'act_3', witness: { verdict: 'MISMATCH', level: 'L2' } }), seen)).toBe(true);
    expect(o.verification.mismatch).toBe(v + 1);
    expect(o.verification.not_witnessed).toBe(nw); // +1 created, −1 witnessed
    const p = o.approvals.pending;
    applyLive(o, ev('approval.requested', { id: 'apr_1' }), seen);
    applyLive(o, ev('approval.decided', { id: 'apr_1' }), seen);
    expect(o.approvals.pending).toBe(p);
  });
});

describe('UsageMeter thresholds', async () => {
  const { meterLevel } = await import('../src/controllers/components.ts');
  it.each([
    [0, 5000, 'ok'], [3999, 5000, 'ok'], [4000, 5000, 'near'], [4999, 5000, 'near'], [5000, 5000, 'at'], [6000, 5000, 'at'], [10, undefined, 'none'], [10, 0, 'none'],
  ] as const)('%i of %s → %s', (used, limit, want) => expect(meterLevel(used, limit)).toBe(want));
});
