// checklist.ts: CheckList (S26 §3.3), the connector test results and the Postgres readiness panel.
// Rows keep the server's order (S17's). A calm column: passes are quiet, exceptions are spelled out
// with their Fix sentence, the SQL to copy (always under a review header, never run from here) and
// the docs link. Plus the two one-line summaries.

import { formsCopy } from '../lib/copy-forms.ts';
import { fill } from '../lib/copy.ts';
import { h, icon, safeHref } from '../lib/dom.ts';
import { number } from '../lib/format.ts';
import { toast } from './toast.ts';

const C = formsCopy.checks;

export type CheckStatus = 'pass' | 'warn' | 'fail' | 'skip';

export interface Check {
  id: string;
  title: string;
  status: CheckStatus;
  detail?: string;
  fix?: string;
  fix_sql?: string;
  doc_url?: string;
}

export interface ConnectorTest {
  object?: string;
  connector?: string;
  status: 'pass' | 'warn' | 'fail';
  duration_ms?: number;
  checks: Check[];
}

export interface CheckListOptions {
  /** The database name for the SQL header (`Run as a superuser on acme.`). */
  db?: string;
  title?: string;
  /** Prefix for element ids (several lists on one page). */
  idPrefix?: string;
}

/** The brand's status marks (§13.3); skip is a minus sign, never a dash. */
export const MARK: Record<CheckStatus, string> = { pass: '✓', warn: '○', fail: '✕', skip: '−' };

/** sqlFor: what Copy SQL puts on the clipboard: the review header, then the server's fix_sql. */
export function sqlFor(fixSql: string, db?: string): string {
  return `-- ${fill(C.sqlHeader, { db: db && db.trim() ? db.trim() : C.sqlDbFallback })}\n${fixSql}`;
}

const count = (t: ConnectorTest, s: CheckStatus) => t.checks.filter((c) => c.status === s).length;

/** readinessSummary: `Ready for Proven checks · 8 passed · 2 notes · 0 failed` or `Not ready for Proven checks · 2 items to fix`. */
export function readinessSummary(t: ConnectorTest): string {
  const failed = count(t, 'fail');
  if (failed > 0) return failed === 1 ? C.notReadyOne : fill(C.notReady, { n: number(failed) });
  const notes = count(t, 'warn');
  return fill(notes === 1 ? C.readyOne : C.ready, { passed: number(count(t, 'pass')), notes: number(notes), failed: 0 });
}

/** testSummary: `✓ 5 checks passed · 184 ms`, `○ 14 checks passed · 3 notes · 184 ms`, `✕ 1 of 5 checks failed · destination`. */
export function testSummary(t: ConnectorTest): string {
  if (t.checks.length === 0) return C.empty;
  const ms = number(Math.max(0, Math.round(t.duration_ms ?? 0)));
  const failing = t.checks.filter((c) => c.status === 'fail');
  if (failing.length > 0) {
    const total = t.checks.filter((c) => c.status !== 'skip').length;
    return fill(C.failed, { failed: number(failing.length), total: number(total), which: failing.map((c) => c.title || c.id).join(', ') });
  }
  const passed = count(t, 'pass');
  const notes = count(t, 'warn');
  if (notes > 0) return fill(notes === 1 ? C.passedNotesOne : C.passedNotes, { n: number(passed), notes: number(notes), ms });
  return passed === 1 ? fill(C.passedOne, { ms }) : fill(C.passed, { n: number(passed), ms });
}

async function copySql(text: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast(C.sqlCopied, { kind: 'success' });
  } catch {
    toast(C.copyFailed, { kind: 'error' });
  }
}

function row(c: Check, o: CheckListOptions, i: number): HTMLLIElement {
  const word = C[c.status] ?? c.status;
  const sql = c.fix_sql ? sqlFor(c.fix_sql, o.db) : null;
  const titleId = `${o.idPrefix ?? 'cl'}-${i}-t`;
  const docs = c.doc_url ? safeHref(c.doc_url) : null;
  const actions: HTMLElement[] = [];
  if (sql) {
    actions.push(h('button', { type: 'button', class: 'btn btn-secondary btn-sm cl-copy', 'aria-label': fill(C.copySqlFor, { title: c.title }), onclick: () => void copySql(sql) }, icon('copy', 16), C.copySql));
  }
  if (docs) {
    actions.push(h('a', { class: 'btn btn-quiet btn-sm cl-docs', href: docs, target: '_blank', rel: 'noopener noreferrer', 'aria-label': `${fill(C.docsFor, { title: c.title })} ${C.newTab}` }, C.docs, icon('arrow-up-right', 16)));
  }
  return h('li', { class: `cl-row cl-${c.status}`, 'data-check': c.id, 'aria-labelledby': titleId },
    h('span', { class: 'cl-mark', 'aria-hidden': 'true' }, MARK[c.status] ?? '?'),
    h('div', { class: 'cl-body' },
      h('p', { class: 'cl-head', id: titleId },
        h('span', { class: 'cl-title' }, c.title || c.id),
        h('span', { class: 'cl-word' }, word)),
      c.detail ? h('p', { class: 'cl-detail' }, c.detail) : null,
      c.fix ? h('p', { class: 'cl-fix' }, h('span', { class: 'cl-fix-label' }, C.fix), ' ', c.fix) : null,
      actions.length ? h('div', { class: 'cl-actions' }, ...actions) : null,
      sql
        ? h('details', { class: 'cl-sql' },
          h('summary', null, icon('chevron-right', 16, 'cl-chev'), C.showSql),
          h('figure', { class: 'code' },
            h('figcaption', { class: 'code-head' }, h('span', null, C.sqlCaption)),
            h('pre', { tabindex: 0 }, h('span', { class: 'p' }, sql.slice(0, sql.indexOf('\n') + 1)), sql.slice(sql.indexOf('\n') + 1))))
        : null));
}

/** checkList renders a connector_test's checks in the server's order. */
export function checkList(t: ConnectorTest, o: CheckListOptions = {}): HTMLElement {
  const headId = `${o.idPrefix ?? 'cl'}-h`;
  return h('section', { class: `cl cl-status-${t.status}`, ...(o.title ? { 'aria-labelledby': headId } : { 'aria-label': testSummary(t) }) },
    o.title ? h('h3', { class: 't-headline cl-heading', id: headId }, o.title) : null,
    h('ul', { class: 'cl-list', role: 'list' }, ...t.checks.map((c, i) => row(c, o, i))));
}

/** readinessLine: the readiness summary as a line, ✓ quiet when ready, ✕ red when not. */
export function readinessLine(t: ConnectorTest): HTMLElement {
  const fail = t.checks.some((c) => c.status === 'fail');
  return h('p', { class: `readiness ${fail ? 'readiness-fail' : 'readiness-ok'}` },
    h('span', { class: 'readiness-mark', 'aria-hidden': 'true' }, fail ? MARK.fail : MARK.pass),
    h('span', null, readinessSummary(t)));
}
