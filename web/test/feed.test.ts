// feed.ts: the live feed model: insert, update in place, buffer behind the pill, the 500 cap,
// the text-search pause and the resync anchor (S25 §3.4).
import { describe, expect, it } from 'vitest';
import { anchorAfterResync, FEED_CAP, FeedModel } from '../src/lib/feed.ts';
import { emptyFilters } from '../src/lib/filters.ts';
import type { Action, DeedglassEvent } from '../src/lib/types.ts';

let n = 0;
const id = () => `act_01k633rrmh3bcz2p39gg${String(n++).padStart(4, '0')}`;
const action = (o: Partial<Action> = {}): Action => ({
  object: 'action', id: id(), created_at: new Date().toISOString(), livemode: false, agent: { id: 'agt_01k633rrmh3bcz2p39gg4vp46q', name: 'support-bot' },
  tool: 'postgres.execute', connector: null, mode: 'gateway', state: 'received', status: 'processing', decision: null,
  policy: { id: null, version_id: null, mode: 'enforce', decision: null, rule_id: null }, execution: null, witness: null, deed: null, ...o,
});
const ev = (type: string, obj: Partial<Action>, hydrated = true): DeedglassEvent<Partial<Action>> =>
  ({ object: 'event', id: 'evt_x', type, api_version: '2026-10-01', created_at: '', livemode: false, org: 'org_x', hydrated, data: { object: obj } });

describe('FeedModel', () => {
  it('inserts new rows at the top and updates known rows in place', () => {
    const m = new FeedModel(emptyFilters());
    const old = action();
    m.load([old]);
    const a = action();
    expect(m.apply(ev('action.created', a))).toBe('inserted');
    expect(m.order).toEqual([a.id, old.id]);
    expect(m.apply(ev('action.witnessed', { ...a, state: 'sealed', witness: { object: 'witness', level: 'L2', verdict: 'VERIFIED', method: null, required_level: null, detail_digest: null } }))).toBe('updated');
    expect(m.rows.get(a.id)?.witness?.verdict).toBe('VERIFIED');
    expect(m.order).toEqual([a.id, old.id]); // an update never reorders
  });
  it('ignores thin events, foreign objects and actions outside the filters', () => {
    const m = new FeedModel({ ...emptyFilters(), tool: 'stripe.*' });
    m.load([]);
    expect(m.apply(ev('action.created', action(), false))).toBe('ignored');
    expect(m.apply(ev('approval.requested', action()))).toBe('ignored');
    expect(m.apply(ev('action.created', action({ tool: 'postgres.query' })))).toBe('ignored');
    expect(m.apply(ev('action.created', action({ tool: 'stripe.create_refund' })))).toBe('inserted');
  });
  it('buffers while paused or scrolled away, then flushes in arrival order', () => {
    const m = new FeedModel(emptyFilters());
    m.load([]);
    m.paused = true;
    const a = action();
    const b = action();
    expect(m.apply(ev('action.created', a))).toBe('buffered');
    expect(m.apply(ev('action.created', b))).toBe('buffered');
    expect(m.apply(ev('action.decided', { ...a, state: 'denied' }))).toBe('buffered'); // updates a buffered row
    expect(m.pending.size).toBe(2);
    expect(m.order).toEqual([]);
    m.paused = false;
    expect(m.flush()).toEqual([a.id, b.id]);
    expect(m.order).toEqual([b.id, a.id]);
    expect(m.rows.get(a.id)?.state).toBe('denied');
    m.atTop = false;
    expect(m.apply(ev('action.created', action()))).toBe('buffered');
  });
  it('pauses live insertion while searching by text', () => {
    const m = new FeedModel({ ...emptyFilters(), q: 'refund' });
    m.load([]);
    expect(m.live).toBe(false);
    expect(m.apply(ev('action.created', action()))).toBe('ignored'); // matches() is false under q
  });
  it('keeps at most 500 rows, dropping the oldest', () => {
    const m = new FeedModel(emptyFilters());
    m.load(Array.from({ length: FEED_CAP }, () => action()));
    const last = m.order[m.order.length - 1]!;
    m.apply(ev('action.created', action()));
    expect(m.order.length).toBe(FEED_CAP);
    expect(m.rows.has(last)).toBe(false);
  });
  it('resync keeps the first visible row that survived', () => {
    expect(anchorAfterResync(['a', 'b', 'c'], ['x', 'b', 'c'])).toBe('b');
    expect(anchorAfterResync(['a'], ['x'])).toBe(null);
  });
});
