// sentences.ts: one sentence per verdict × level, plus denied, pending and failed (S25 §3.5).
// Verdict sentences are the API's witness.summary, verbatim, with the Console's prefix only.
import { describe, expect, it } from 'vitest';
import { actionSentence, minutesUntil } from '../src/lib/sentences.ts';
import type { ActionDetail, ApprovalDetail } from '../src/lib/types.ts';
import { read, repo } from './helpers.ts';

const fx = (n: string) => JSON.parse(read(repo(`web/test/fixtures/${n}.json`))) as ActionDetail;
const now = new Date('2026-10-09T14:00:00Z');
const w = (level: 'L0' | 'L1' | 'L2', verdict: 'VERIFIED' | 'MISMATCH' | 'UNVERIFIABLE', summary?: string, method = 'postgres.pgoutput') => {
  const a = fx('action-verified-l2');
  a.witness = { object: 'witness', level, verdict, method, required_level: null, detail_digest: null, summary, reasons: [] };
  return a;
};

describe('actionSentence', () => {
  it('MISMATCH is the summary, verbatim, after the word', () => {
    const s = actionSentence(fx('action-mismatch-l2'), now);
    expect(s).toMatchObject({ tone: 'mismatch', text: 'MISMATCH · Postgres changed 301 rows in public.orders. The agent reported 1.' });
  });
  it.each([
    ['L2', 'Postgres committed 1 row in public.orders, as the statement said.', 'postgres.pgoutput', 'Witnessed by Postgres · Postgres committed 1 row in public.orders, as the statement said.'],
    ['L2', 'Committed.', 'other.log', 'Witnessed by the system of record · Committed.'],
    ['L1', 'Read back orders.get: status is "shipped".', 'readback.rest', 'Confirmed by a read-back · Read back orders.get: status is "shipped".'],
    ['L0', 'The tool reported success.', 'reported', "Reported by the tool. Deedglass didn't read it back."],
  ] as const)('VERIFIED %s', (level, summary, method, text) => {
    expect(actionSentence(w(level, 'VERIFIED', summary, method), now)).toMatchObject({ tone: 'verified', text });
  });
  it('UNVERIFIABLE lists the gaps under it', () => {
    const s = actionSentence(fx('action-unverifiable'), now);
    expect(s.tone).toBe('unverifiable');
    expect(s.text.startsWith('UNVERIFIABLE · ')).toBe(true);
    expect(s.gaps.length).toBeGreaterThan(0);
  });
  it('a denial uses the Gate message when there is one, else the rule', () => {
    const d = fx('action-denied');
    expect(actionSentence(d, now)).toMatchObject({ tone: 'denied', text: 'Blocked by policy "refund-limit".' });
    d.decision = { ...d.decision!, message: 'Blocked by policy "block-destructive-sql": DROP is never allowed for agents.' };
    expect(actionSentence(d, now).text).toBe('Blocked by policy "block-destructive-sql": DROP is never allowed for agents.');
  });
  it('pending shows the clock and the countersigns', () => {
    const a = fx('action-denied');
    a.status = 'pending_approval';
    a.state = 'pending_approval';
    a.decision = { ...a.decision!, outcome: 'require_approval' };
    a.approval = { id: 'apr_x', status: 'pending', expires_at: '2026-10-09T14:12:00Z' };
    const ap = { required: 2, countersigned: [{ user: { id: 'u', name: 'Priya Raman' }, at: '', via: 'console', reason: null, deed: 'deed_x' }], expires_at: '2026-10-09T14:12:00Z' } as unknown as ApprovalDetail;
    expect(actionSentence(a, now, ap).text).toBe('Waiting for approval · expires in 12 min · 1 of 2 countersigned');
    expect(actionSentence(a, now).text).toBe('Waiting for approval · expires in 12 min');
  });
  it('failed names the error code and says nothing was witnessed', () => {
    expect(actionSentence(fx('action-failed'), now)).toMatchObject({ tone: 'failed', text: 'The tool returned an error: upstream_error. Nothing was witnessed.' });
  });
  it('minutesUntil', () => {
    expect(minutesUntil('2026-10-09T14:06:00Z', now)).toBe('6 min');
    expect(minutesUntil('2026-10-09T15:05:00Z', now)).toBe('1 h 5 min');
    expect(minutesUntil('2026-10-09T14:00:30Z', now)).toBe('under a minute');
  });
});
