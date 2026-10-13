// @vitest-environment happy-dom
// S50: observe mode in the Console. The notes equal internal/observe/present.go word for word, every
// caught view carries the free path (S50 §7), an ordinary session shows no banner, and the helpers
// write what the CLI writes.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { sessionsCopy as c } from '../src/lib/copy-sessions.ts';
import { caughtBanner, homeLine, sessionRow, stateChip, tableLines, undoActions, type Preview, type Session } from '../src/controllers/sessions.ts';

const goPresent = readFileSync(join(process.cwd(), '..', 'internal/observe/present.go'), 'utf8');

const session = (over: Partial<Session> = {}): Session => ({
  object: 'observe_session', id: 'obs_01m4v2rz8t68900dq9ne4vhj6h', connector: { id: 'con_1', name: 'pg-demo' }, label: 'nightly agent', agent: null,
  state: 'sealed', opened_at: '2026-10-17T14:02:11Z', closed_at: '2026-10-17T14:03:40Z', flags: ['caught'], watched_tables: ['public.orders'],
  summary: { transactions: 3, excluded: 0, expired: 0, rows: 602, tables: { 'public.orders': { insert: 0, update: 301, delete: 0, truncate: 0 }, 'public.order_events': { insert: 301, update: 0, delete: 0, truncate: 0 } }, first_commit: null, last_commit: null },
  caught: [{ kind: 'many_rows', line: 'Deedglass caught this: one transaction changed 301 rows in public.orders.', undo_possible: true, undo_free_path: 'deedglass observe show obs_01m4v2rz8t68900dq9ne4vhj6h --undo-sql' }],
  seal: { deed: 'deed_1' }, undo: { action: null, possible: true, free_path: 'x' }, notes: [], observed: c.observed, attribution: c.attribution, ...over,
});

describe('sessions copy', () => {
  it("matches internal/observe/present.go's notes", () => {
    for (const s of [c.observed, c.attribution, c.proSheet]) expect(goPresent).toContain(JSON.stringify(s).slice(1, -1).replace(/\\"/g, '"'));
  });
  it('uses no em dash', () => {
    for (const v of Object.values(c)) if (typeof v === 'string') expect(v).not.toContain('—');
  });
});

describe('sessions views', () => {
  it('writes table lines like the CLI', () => {
    expect(tableLines(session().summary.tables)).toEqual([['public.order_events', '301 inserted'], ['public.orders', '301 updated']]);
    expect(tableLines({ t: { insert: 0, update: 0, delete: 2, truncate: 1 } })).toEqual([['t', '2 deleted, truncated once']]);
  });
  it('a caught banner always carries the free path; an ordinary session shows none', () => {
    const s = session();
    const pv: Preview = { object: 'observe_undo_preview', session: s.id, available: true, summary: 'x', headline: 'x', plan_digest: 'sha256:a', conflicts: 0, excluded: [], sql: 'BEGIN;', refusals: [], pro: false, free_path: 'x', rows: 1 };
    const b = caughtBanner(s.caught, s, pv)!;
    expect(b.textContent).toContain('Deedglass caught this: one transaction changed 301 rows in public.orders.');
    expect(b.querySelector('[data-free-path]')?.textContent).toContain(c.copySQL);
    expect(caughtBanner([], session({ caught: [] }), null)).toBeNull();
    // Without Pro the undo button still shows beside the free path (it opens the Pro sheet).
    const a = undoActions(s, pv);
    expect(a.textContent).toContain(c.undoSession);
    expect(a.querySelector('[data-free-path]')).not.toBeNull();
  });
  it('the home banner drops the repeated prefix', () => {
    expect(homeLine('Deedglass caught this: one transaction changed 301 rows in public.orders.')).toBe('One transaction changed 301 rows in public.orders.');
  });
  it('rows show the caught chip and the state', () => {
    const tr = sessionRow(session());
    expect(tr.textContent).toContain(c.caughtChip);
    expect(tr.textContent).toContain('Sealed');
    expect(sessionRow(session({ caught: [], label: null })).textContent).not.toContain(c.caughtChip);
    expect(stateChip('undone').className).toContain('chip-verified');
  });
});
