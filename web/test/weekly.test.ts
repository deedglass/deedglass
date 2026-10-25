// @vitest-environment happy-dom
// S52: the This week card and the Clients page. The card's lines equal internal/weekly's words on the
// Go golden (internal/weekly/testdata/week.json and week.txt), every caught item shows its free path,
// the Pro line appears only on a caught session without undo_session, and a client row is one row.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { clientsCopy as c, weekCopy as w } from '../src/lib/copy-clients.ts';
import { chainLine, checkLine, clientRow, freeLine, itemLine, monthKey, numbersLine, proState, renderWeek, type ClientRow, type Summary, type WeeklyView } from '../src/controllers/weekly.ts';

const dir = join(process.cwd(), '..', 'internal/weekly/testdata');
const summary = JSON.parse(readFileSync(join(dir, 'week.json'), 'utf8')) as Summary;
const goText = readFileSync(join(dir, 'week.txt'), 'utf8').split('\n');

describe('the card writes what internal/weekly writes', () => {
  it('numbers, Check and chain lines equal the Go golden', () => {
    expect(goText[1]).toBe(numbersLine(summary));
    expect(goText[2]).toBe(checkLine(summary));
    expect(goText).toContain(chainLine(summary));
    expect(numbersLine({ ...summary, actions: { total: 0, allowed: 0, waited_for_a_person: 0, blocked_by_rules: 0 } })).toBe('No agent actions this week.');
  });
  it('caught items: the Go golden lists each sentence and its free path', () => {
    for (const it of summary.caught) {
      expect(goText.some((l) => l.trim() === itemLine(it))).toBe(true);
      expect(goText.some((l) => l.trim().startsWith(freeLine(it)))).toBe(true);
    }
  });
});

describe('the This week card', () => {
  const view = (over: Partial<WeeklyView> = {}): WeeklyView => ({ object: 'weekly_view', summary, email: { on: true, receives: true, owner: true, mailer: true }, pro_undo_line: true, ...over });
  it('shows every caught item with its free path, and the Pro line only on a caught session without undo_session', () => {
    const b = document.createElement('div');
    renderWeek(b, view());
    expect(b.querySelectorAll('[data-free-path]').length).toBe(Math.min(3, summary.caught.length));
    expect(b.textContent).toContain('Undo the whole session in one step');
    renderWeek(b, view({ pro_undo_line: false }));
    expect(b.textContent).not.toContain('Undo the whole session in one step');
    expect(b.textContent).toContain(w.emailOn);
  });
  it('a quiet week says so; no mailer says the card is the summary', () => {
    const b = document.createElement('div');
    renderWeek(b, view({ summary: { ...summary, caught: [], caught_total: 0 }, email: { on: true, receives: false, owner: true, mailer: false } }));
    expect(b.textContent).toContain(w.nothing);
    expect(b.textContent).toContain(w.emailNoMailer);
    expect(b.querySelector('.wk-toggle')).toBeNull();
  });
});

describe('the Clients page', () => {
  const row = (over: Partial<ClientRow> = {}): ClientRow => ({ object: 'client', org: 'org_01m4v2rz8t68900dq9ne4vhj6h', name: 'Acme', slug: 'acme', role: 'owner', actions: 412,
    doesnt_match: 0, caught: 1, waiting: 2, stopped: false, chain: 'intact', last_activity: '2026-10-18T09:00:00Z', ...over });
  it('one row per client, numbers right-aligned, chips at the end', () => {
    const tr = clientRow(row());
    expect(tr.querySelectorAll('td.num').length).toBe(3);
    expect(tr.textContent).toContain('412');
    expect(tr.textContent).toContain('1 caught');
    expect(tr.textContent).toContain(c.chain.intact);
    const quiet = clientRow(row({ actions: 0, caught: 0, waiting: 0, last_activity: null, chain: 'no_records' }));
    expect(quiet.textContent).toContain(c.noActivity);
    expect(quiet.textContent).toContain(c.chain.no_records);
  });
  it('without the Agency plan: the explanation, the free line and the plan page, nothing locked or blurred', () => {
    const p = proState();
    expect(p.textContent).toContain(c.proFree);
    expect(p.querySelector('a')?.getAttribute('href')).toBe('https://deedglass.com/pricing');
    expect(p.innerHTML).not.toMatch(/blur|lock/);
  });
  it('copy: no em dash, no exclamation mark', () => {
    for (const v of [...Object.values(c), ...Object.values(w)]) if (typeof v === 'string') expect(v).not.toMatch(/—|!/);
    expect(monthKey(new Date('2026-11-03T00:00:00Z'))).toBe('2026-10');
  });
});
