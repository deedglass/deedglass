// filters.ts: URL ⇄ filters ⇄ API round trips, and live matching that mirrors the Ledger (S06).
import { describe, expect, it } from 'vitest';
import { emptyFilters, filtersToSearch, globMatch, isFiltered, matches, parseFilters, toApiQuery } from '../src/lib/filters.ts';
import type { Action } from '../src/lib/types.ts';

const now = new Date('2026-10-09T14:00:00Z');
const action = (o: Partial<Action> = {}): Action => ({
  object: 'action', id: 'act_01k633rrmh3bcz2p39gg4vp46q', created_at: '2026-10-09T13:59:00Z', livemode: false,
  agent: { id: 'agt_01k633rrmh3bcz2p39gg4vp46q', name: 'support-bot' }, tool: 'postgres.execute', connector: null, mode: 'gateway',
  state: 'sealed', status: 'executed', on_behalf_of: 'u_4821', decision: { outcome: 'allow', code: null, rule_id: 'r', reason: null },
  policy: { id: null, version_id: null, mode: 'enforce', decision: 'allow', rule_id: 'r' }, execution: null,
  witness: { object: 'witness', level: 'L2', verdict: 'MISMATCH', method: 'postgres.pgoutput', required_level: null, detail_digest: null }, deed: null, ...o,
});

describe('globMatch (the Ledger tool filter: exact, or * as any run, LIKE semantics)', () => {
  it.each([
    ['stripe.*', 'stripe.create_refund', true],
    ['stripe.*', 'stripe.', true],
    ['stripe.*', 'stripes.create', false],
    ['*.undo', 'postgres.undo', true],
    ['*.undo', 'postgres.undone', false],
    ['*', 'anything', true],
    ['postgres.execute', 'postgres.execute', true],
    ['postgres.execute', 'postgres.executeX', false],
    ['post*exec*', 'postgres.execute', true],
    ['post*exec*', 'postgres.query', false],
    ['a*b*c', 'abc', true],
    ['a*b*c', 'acb', false],
    ['github.merge_pr', 'github.merge-pr', false], // "_" is literal, not LIKE's any-char
    ['Stripe.*', 'stripe.create_refund', false], // case-sensitive
    ['*.*', 'postgres.query', true],
  ])('%s vs %s → %s', (p, t, want) => expect(globMatch(p, t)).toBe(want));
});

describe('URL ⇄ filters ⇄ API', () => {
  it('round trips every field and drops junk', () => {
    const sp = new URLSearchParams('agent=agt_01k633rrmh3bcz2p39gg4vp46q&tool=stripe.*&decision=deny&verdict=MISMATCH,UNVERIFIABLE,BOGUS&level=L2&state=sealed&on_behalf_of=u_4821&time=24h&q=refund&evil=1');
    const f = parseFilters(sp);
    expect(f.verdict).toEqual(['MISMATCH', 'UNVERIFIABLE']);
    expect(filtersToSearch(f).toString()).toBe('agent=agt_01k633rrmh3bcz2p39gg4vp46q&tool=stripe.*&decision=deny&verdict=MISMATCH%2CUNVERIFIABLE&level=L2&state=sealed&on_behalf_of=u_4821&time=24h&q=refund');
    expect(parseFilters(filtersToSearch(f))).toEqual(f);
    const q = toApiQuery(f, now);
    expect(q['created_at[gte]']).toBe('2026-10-08T14:00:00.000Z');
    expect(q.verdict).toBe('MISMATCH,UNVERIFIABLE');
    expect(q.q).toBe('refund');
  });
  it('refuses a malformed agent, a one-letter search, a bad decision', () => {
    const f = parseFilters(new URLSearchParams('agent=agt_bad&q=a&decision=maybe'));
    expect(f).toEqual(emptyFilters());
    expect(isFiltered(f)).toBe(false);
  });
  it('a custom range wins over time and sends both bounds', () => {
    const f = parseFilters(new URLSearchParams('time=1h&from=2026-10-01T00:00:00Z&to=2026-10-02T00:00:00Z'));
    expect(f.time).toBe('custom');
    expect(toApiQuery(f, now)).toEqual({ 'created_at[gte]': '2026-10-01T00:00:00.000Z', 'created_at[lt]': '2026-10-02T00:00:00.000Z' });
  });
});

describe('matches (live insertion)', () => {
  it('follows every filter', () => {
    const a = action();
    expect(matches(emptyFilters(), a, now)).toBe(true);
    expect(matches({ ...emptyFilters(), verdict: ['VERIFIED'] }, a, now)).toBe(false);
    expect(matches({ ...emptyFilters(), verdict: ['MISMATCH'], level: ['L2'] }, a, now)).toBe(true);
    expect(matches({ ...emptyFilters(), level: ['L1'] }, a, now)).toBe(false);
    expect(matches({ ...emptyFilters(), decision: 'deny' }, a, now)).toBe(false);
    expect(matches({ ...emptyFilters(), tool: 'postgres.*' }, a, now)).toBe(true);
    expect(matches({ ...emptyFilters(), on_behalf_of: 'u_1' }, a, now)).toBe(false);
    expect(matches({ ...emptyFilters(), time: '1h' }, a, now)).toBe(true);
    expect(matches({ ...emptyFilters(), time: '1h' }, action({ created_at: '2026-10-09T12:00:00Z' }), now)).toBe(false);
    expect(matches({ ...emptyFilters(), verdict: ['MISMATCH'] }, action({ witness: null }), now)).toBe(false);
  });
  it('never inserts live rows while searching by text (the server owns q)', () => {
    expect(matches({ ...emptyFilters(), q: 'post' }, action(), now)).toBe(false);
  });
  it('a custom range is [from, to)', () => {
    const f = { ...emptyFilters(), time: 'custom' as const, from: '2026-10-09T13:00:00.000Z', to: '2026-10-09T13:59:00.000Z' };
    expect(matches(f, action({ created_at: '2026-10-09T13:00:00.000Z' }), now)).toBe(true);
    expect(matches(f, action({ created_at: '2026-10-09T13:59:00.000Z' }), now)).toBe(false);
  });
});
