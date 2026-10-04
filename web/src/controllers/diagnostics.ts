// diagnostics.ts: DiagnosticsList (S26 §3.2), the policy lint results under the editor. One row per
// diagnostic: severity as an icon and a word (never colour alone), `Line 9, col 5 · message hint`,
// `Apply fix` when policyfix.ts can derive one, and `Docs` to docs.deedglass.com. A row reveals its
// line in the editor. Clean: `✓ No problems · 16 rules · 1 budget`, only from a real lint result.

import { fill } from '../lib/copy.ts';
import { editorCopy as c } from '../lib/copy-editor.ts';
import { h, icon, safeHref } from '../lib/dom.ts';
import { quickFix, type PolicyDiagnostic } from '../lib/policyfix.ts';

const MAX_ROWS = 50;

export interface DiagnosticsOptions {
  onReveal?: (line: number) => void;
  onFix?: (d: PolicyDiagnostic) => void;
  /** The lint result's counts. With them, an empty list reads as clean; without, it shows nothing. */
  counts?: { rules: number; budgets: number } | null;
  /** Read-only editors offer no fixes. */
  readOnly?: boolean;
  /** Show the `1 error · 2 warnings` line (off when a status line beside the editor says it). */
  summary?: boolean;
}

const n = (x: number) => x.toLocaleString('en-US');

/** plural: `1 rule` / `12 rules`. */
export function countText(x: number, one: string, many: string): string {
  return x === 1 ? one : fill(many, { n: n(x) });
}

/** cleanText: `No problems · 12 rules · 2 budgets` (the ✓ is drawn beside it). */
export function cleanText(rules: number, budgets: number): string {
  return fill(c.clean, { rules: countText(rules, c.ruleOne, c.rules), budgets: countText(budgets, c.budgetOne, c.budgets) });
}

/** summaryText: `1 error · 2 warnings`, or null when there are none. */
export function summaryText(d: readonly PolicyDiagnostic[]): string | null {
  const e = d.filter((x) => x.severity === 'error').length;
  const w = d.length - e;
  const parts: string[] = [];
  if (e) parts.push(countText(e, c.errorOne, c.errors));
  if (w) parts.push(countText(w, c.warningOne, c.warnings));
  return parts.length ? parts.join(' · ') : null;
}

/** locationText: `Line 9, col 5`, `Line 9`, or '' for a file-level diagnostic. */
export function locationText(d: PolicyDiagnostic): string {
  if (!d.line) return '';
  return d.column ? fill(c.at, { line: d.line, col: d.column }) : fill(c.atLine, { line: d.line });
}

/** rowText: `Line 9, col 5 · Unknown key "desicion" in rule "cap-refunds". Did you mean "decision"?` */
export function rowText(d: PolicyDiagnostic): string {
  const msg = [d.message, d.hint].filter(Boolean).join(' ');
  const at = locationText(d);
  return at ? `${at} · ${msg}` : msg;
}

function row(d: PolicyDiagnostic, o: DiagnosticsOptions): HTMLLIElement {
  const sev = d.severity === 'error' ? 'error' : 'warning';
  const sevEl = h('span', { class: `dl-sev dl-sev-${sev}` }, icon(sev === 'error' ? 'x' : 'circle', 16), c.severity[sev] ?? sev);
  const at = locationText(d);
  const msg = [d.message, d.hint].filter(Boolean).join(' ');
  const body: (Node | string)[] = at ? [h('span', { class: 'dl-loc mono' }, at), h('span', { class: 'dl-dot', 'aria-hidden': 'true' }, ' · '), msg] : [msg];
  const line = d.line ?? null;
  const reveal = line && o.onReveal ? () => o.onReveal!(line) : null;
  const main = reveal
    ? h('button', { type: 'button', class: 'dl-main', title: fill(c.reveal, { line: line! }), onclick: reveal }, ...body)
    : h('span', { class: 'dl-main' }, ...body);
  const fix = !o.readOnly && o.onFix ? quickFix(d) : null;
  const actions = h('span', { class: 'dl-actions' },
    fix ? h('button', {
      type: 'button', class: 'btn btn-secondary btn-sm', 'aria-label': fill(c.applyFixLabel, { line: d.line ?? '', replace: fix.replace }),
      onclick: () => o.onFix!(d),
    }, c.applyFix) : null,
    d.doc_url && safeHref(d.doc_url) ? h('a', {
      class: 'btn btn-quiet btn-sm dl-docs', href: d.doc_url, target: '_blank', rel: 'noopener noreferrer',
      'aria-label': fill(c.docsLabel, { code: d.code }),
    }, c.docs, icon('arrow-up-right', 16)) : null);
  const li = h('li', { class: `dl-row dl-${sev}`, 'data-line': line ?? '' }, sevEl, main, actions.childNodes.length ? actions : null);
  if (reveal) {
    // The whole row reveals the line; its own buttons and links keep their meaning.
    li.addEventListener('click', (e) => {
      if ((e.target as Element).closest('button, a')) return;
      reveal();
    });
  }
  return li;
}

/** diagnosticsList: the list, its summary line, or the clean line. */
export function diagnosticsList(d: readonly PolicyDiagnostic[], o: DiagnosticsOptions = {}): HTMLElement {
  if (d.length === 0) {
    if (!o.counts) return h('div', { class: 'dl dl-empty' });
    return h('div', { class: 'dl dl-clean' },
      h('p', { class: 'dl-clean-line' }, h('span', { class: 'dl-glyph', 'aria-hidden': 'true' }, '✓'), cleanText(o.counts.rules, o.counts.budgets)));
  }
  const shown = d.slice(0, MAX_ROWS);
  return h('div', { class: 'dl' },
    o.summary === false ? null : h('p', { class: 'dl-summary t-footnote' }, summaryText(d)),
    h('ul', { class: 'dl-list' }, ...shown.map((x) => row(x, o))),
    d.length > MAX_ROWS ? h('p', { class: 'dl-more t-footnote t-2' }, fill(c.more, { n: n(d.length - MAX_ROWS) })) : null);
}
