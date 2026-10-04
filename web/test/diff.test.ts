// diff.ts: S21's L2 evidence as DiffTable rows: the real 301-row hero from the e2e stack, the
// S21 golden fixture, a clean write, a trigger's side effect in another table, a sensitive column,
// and a gap (no frame) that must render no diff at all.
import { describe, expect, it } from 'vitest';
import { diffModel, MASK, showingLine } from '../src/lib/diff.ts';
import type { ActionDetail, WitnessDetail } from '../src/lib/types.ts';
import { read, repo } from './helpers.ts';

const sp = (s: string) => s.replace(/\u00a0/g, ' '); // plural() joins with a no-break space
const fx = (n: string) => JSON.parse(read(repo(`web/test/fixtures/${n}.json`))) as ActionDetail;
const detailWith = (L2: unknown): WitnessDetail => ({ object: 'witness_detail', version: 1, claim: { level: 'L2', verdict: 'MISMATCH', method: 'postgres.pgoutput', required_level: 'L2' }, summary: '', levels: { L0: null, L1: null, L2: L2 as never }, reasons: [], witnessed_at: '' });

describe('diffModel', () => {
  it('the real hero: 1 expected row, 300 unexpected, 20 kept', () => {
    const m = diffModel(fx('action-mismatch-l2').witness!.detail)!;
    expect(sp(m.expected)).toBe('UPDATE 1 row in public.orders (id = 42) · column status');
    expect(sp(m.observed)).toBe('UPDATE 301 rows in public.orders');
    expect(m.rows.length).toBe(20);
    expect(m.rows[0]).toMatchObject({ key: 'id = 42', expected: 'shipped', observed: 'shipped', unexpected: false });
    expect(m.rows.slice(1).every((r) => r.unexpected && r.expected === null)).toBe(true);
    expect(sp(showingLine(m))).toBe('Showing 20 of 301 rows');
  });
  it("S21's golden detail_hero.json renders the same way", () => {
    const l2 = JSON.parse(read(repo('internal/witness/l2/testdata/golden/detail_hero.json')));
    const m = diffModel(detailWith(l2))!;
    expect(m.rows[0]!.before).toBe('paid');
    expect(m.truncated).toBe(true);
    expect(m.changedRows).toBe(301);
  });
  it('a clean write: one expected row, nothing unexpected', () => {
    const m = diffModel(fx('action-verified-l2').witness!.detail)!;
    expect(m.rows.every((r) => !r.unexpected)).toBe(true);
    expect(m.truncated).toBe(false);
    expect(sp(showingLine(m))).toBe('1 row changed');
  });
  it("a trigger's side effect in another table reads as 'plus'", () => {
    const m = diffModel(detailWith({
      verdict: 'MISMATCH', method: 'postgres.pgoutput', redacted: [], rows_shown: 2, rows_more: 0, columns_more: 0, frames: 1,
      expected: { derivation: 'exact_keys', tables: [{ table: 'public.orders', ops: ['update'], rows: 1, keys: [{ id: '42' }], columns: ['status'] }] },
      observed: { total: 2, tables: [{ table: 'public.orders', insert: 0, update: 1, delete: 0 }, { table: 'public.audit_trail', insert: 1, update: 0, delete: 0 }] },
      rows: [
        { table: 'public.orders', op: 'update', key: { id: '42' }, expected: true, cells: [{ column: 'status', before: 'paid', after: 'shipped' }] },
        { table: 'public.audit_trail', op: 'insert', key: { id: '9' }, expected: false, cells: [{ column: 'note', before: null, after: 'shipped 42' }] },
      ],
    }))!;
    expect(sp(m.observed)).toBe('UPDATE 1 row in public.orders · plus INSERT 1 row in public.audit_trail');
    expect(m.rows[1]).toMatchObject({ table: 'public.audit_trail', unexpected: true, observed: 'shipped 42' });
  });
  it('a sensitive column is masked everywhere, before and after', () => {
    const m = diffModel(detailWith({
      verdict: 'MISMATCH', method: 'postgres.pgoutput', redacted: ['public.customers.email'], rows_shown: 1, rows_more: 0, columns_more: 0, frames: 1,
      expected: { derivation: 'exact_keys', tables: [] }, observed: { total: 1, tables: [{ table: 'public.customers', insert: 0, update: 1, delete: 0 }] },
      rows: [{ table: 'public.customers', op: 'update', key: { id: '44' }, expected: false, cells: [{ column: 'email', before: 'a@x.test', after: MASK }] }],
    }))!;
    expect(m.rows[0]).toMatchObject({ masked: true, observed: MASK, before: MASK });
    expect(JSON.stringify(m)).not.toContain('a@x.test');
  });
  it('a gap (no frame, no observed counts) gives no diff', () => {
    expect(diffModel(detailWith(null))).toBeNull();
    expect(diffModel(detailWith({ verdict: 'UNVERIFIABLE', method: 'postgres.pgoutput', rows: [], redacted: [], rows_shown: 0, rows_more: 0, columns_more: 0, frames: 0 }))).toBeNull();
    expect(diffModel(fx('action-unverifiable').witness?.detail)).toBeNull();
    expect(diffModel(undefined)).toBeNull();
  });
});
