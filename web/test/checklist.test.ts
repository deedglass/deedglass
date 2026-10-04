// @vitest-environment happy-dom
import { describe, expect, it } from 'vitest';
import pgJson from './fixtures/s26/connector-test-postgres.json';
import { checkList, readinessLine, readinessSummary, sqlFor, testSummary, type ConnectorTest } from '../src/controllers/checklist.ts';

const pg = pgJson as unknown as ConnectorTest;
const t = (statuses: string[], ms = 184, extra: Partial<ConnectorTest['checks'][number]>[] = []): ConnectorTest => ({
  status: 'pass',
  duration_ms: ms,
  checks: statuses.map((s, i) => ({ id: `c${i}`, title: `check ${i}`, status: s as never, ...(extra[i] ?? {}) })),
});

describe('summaries', () => {
  it('testSummary: all pass, notes, failures', () => {
    expect(testSummary(t(['pass', 'pass', 'pass', 'pass', 'pass']))).toBe('✓ 5 checks passed · 184 ms');
    expect(testSummary(t(['pass']))).toBe('✓ 1 check passed · 184 ms');
    expect(testSummary({ ...pg, duration_ms: 184 })).toBe('○ 14 checks passed · 3 notes · 184 ms');
    expect(testSummary(t(['pass', 'fail', 'pass', 'pass', 'pass'], 184, [{}, { id: 'destination', title: 'destination' }]))).toBe('✕ 1 of 5 checks failed · destination');
    expect(testSummary(t(['pass', 'fail', 'skip', 'skip', 'skip'], 0, [{}, { title: 'destination' }]))).toBe('✕ 1 of 2 checks failed · destination');
    expect(testSummary(t([]))).toBe('No checks ran.');
  });
  it('readinessSummary: ready with notes, or the items to fix', () => {
    expect(readinessSummary(t(['pass', 'pass', 'pass', 'pass', 'pass', 'pass', 'pass', 'pass', 'warn', 'warn']))).toBe('Ready for L2 · 8 passed · 2 notes · 0 failed');
    expect(readinessSummary(t(['pass', 'warn']))).toBe('Ready for L2 · 1 passed · 1 note · 0 failed');
    expect(readinessSummary(t(['pass', 'fail', 'warn', 'fail']))).toBe('Not ready for L2 · 2 items to fix');
    expect(readinessSummary(t(['fail']))).toBe('Not ready for L2 · 1 item to fix');
    expect(readinessSummary(pg)).toBe('Ready for L2 · 14 passed · 3 notes · 0 failed');
  });
  it('the SQL to copy always starts with the review header', () => {
    expect(sqlFor('ALTER SYSTEM SET wal_level = logical;', 'acme')).toBe('-- Review before running. Run as a superuser on acme.\nALTER SYSTEM SET wal_level = logical;');
    expect(sqlFor('SELECT 1;')).toBe('-- Review before running. Run as a superuser on this database.\nSELECT 1;');
  });
});

describe('checkList', () => {
  it('keeps the server order and marks each row with a symbol and a word', () => {
    const el = checkList(pg, { db: 'acme', title: 'Readiness' });
    document.body.replaceChildren(el);
    const rows = [...el.querySelectorAll<HTMLElement>('.cl-row')];
    expect(rows.map((r) => r.dataset.check)).toEqual(pg.checks.map((c) => c.id));
    const slot = el.querySelector('[data-check="slot"]')!;
    expect(slot.classList.contains('cl-warn')).toBe(true);
    expect(slot.querySelector('.cl-mark')!.textContent).toBe('○');
    expect(slot.querySelector('.cl-mark')!.getAttribute('aria-hidden')).toBe('true');
    expect(slot.querySelector('.cl-word')!.textContent).toBe('Note');
    expect(el.querySelector('[data-check="version"] .cl-mark')!.textContent).toBe('✓');
    expect(el.querySelector('h3')!.textContent).toBe('Readiness');
  });
  it('a row with fix_sql offers Copy SQL and shows the same SQL with its header in a collapsed block', () => {
    const el = checkList(pg, { db: 'acme' });
    const row = el.querySelector('[data-check="emit_message_acl"]')!;
    expect(row.querySelector('.cl-fix')!.textContent).toContain('Fix');
    expect(row.querySelector('.cl-copy')!.textContent).toBe('Copy SQL');
    const details = row.querySelector('details')!;
    expect(details.open).toBe(false);
    const pre = details.querySelector('pre')!.textContent!;
    expect(pre.startsWith('-- Review before running. Run as a superuser on acme.\nREVOKE EXECUTE')).toBe(true);
    // rows without fix_sql have no SQL controls
    expect(el.querySelector('[data-check="tls"] .cl-copy')).toBeNull();
    expect(el.querySelector('[data-check="tls"] .cl-fix')!.textContent).toContain('sslmode=verify-full');
  });
  it('Copy SQL puts the header and the SQL on the clipboard', async () => {
    let clip = '';
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (s: string) => { clip = s; } } });
    const el = checkList(t(['fail'], 10, [{ fix_sql: 'ALTER SYSTEM SET wal_level = logical;', fix: 'Set wal_level to logical, then restart.' }]), { db: 'acme' });
    document.body.replaceChildren(el);
    el.querySelector<HTMLButtonElement>('.cl-copy')!.click();
    await new Promise((r) => setTimeout(r, 0));
    expect(clip).toBe('-- Review before running. Run as a superuser on acme.\nALTER SYSTEM SET wal_level = logical;');
  });
  it('docs links pass safeHref and open in a new tab; others are dropped', () => {
    const el = checkList(t(['warn', 'warn'], 1, [{ doc_url: 'https://docs.deedglass.com/connectors/postgres#tls' }, { doc_url: 'javascript:alert(1)' }]));
    const links = el.querySelectorAll<HTMLAnchorElement>('a.cl-docs');
    expect(links.length).toBe(1);
    expect(links[0]!.getAttribute('href')).toBe('https://docs.deedglass.com/connectors/postgres#tls');
    expect(links[0]!.getAttribute('rel')).toBe('noopener noreferrer');
    expect(links[0]!.getAttribute('aria-label')).toContain('new tab');
  });
  it('skip uses a minus sign and the word Skipped; readinessLine marks failure', () => {
    const el = checkList(t(['skip']));
    expect(el.querySelector('.cl-mark')!.textContent).toBe('−');
    expect(el.querySelector('.cl-word')!.textContent).toBe('Skipped');
    const line = readinessLine(t(['fail']));
    expect(line.textContent).toBe('✕Not ready for L2 · 1 item to fix');
    expect(line.classList.contains('readiness-fail')).toBe(true);
  });
});
