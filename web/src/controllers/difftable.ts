// difftable.ts: DiffTable (S25 §3.5, brand §12): expected against observed in two aligned
// columns, values in mono, changed cells tinted with the state colour at 12 %. Meaning never
// rests on colour: an unexpected row carries "●", weight 600 and aria-label "unexpected change";
// a sensitive value is "•••" with a "masked" label. The undo sheet reuses it with the columns
// Now and After undo.

import { copy, fill } from '../lib/copy.ts';
import { showingLine, type DiffModel, type DiffRow } from '../lib/diff.ts';
import { h, icon, type Child } from '../lib/dom.ts';
import { visibleControls } from './components.ts';
import { saveText } from './deedpanel.ts';

function value(v: string | null, masked: boolean): Child {
  if (masked) return h('span', { class: 'masked' }, '•••', h('span', { class: 'masked-label' }, copy.diff.masked));
  if (v === null) return h('span', { class: 't-2' }, 'null');
  return visibleControls(v);
}

export interface DiffTableOptions {
  heads?: [string, string]; // default Expected · Observed
  tone?: 'mismatch' | 'pending';
  limit?: number; // rows rendered (cells); default all the model holds
  pairs?: boolean; // the undo sheet: both columns hold values (Now · After undo); every row changes
  rawJSON?: () => string; // enables "Download diff (JSON)"
  fileName?: string;
}

export function diffRowsTable(rows: DiffRow[], o: DiffTableOptions = {}): HTMLElement {
  const [hExp, hObs] = o.heads ?? [copy.diff.expected, copy.diff.observed];
  const tone = o.tone ?? 'mismatch';
  const body = rows.map((r) => {
    const tr = h('tr', r.unexpected && !o.pairs ? { 'aria-label': copy.diff.unexpected, class: 'diff-unexpected' } : null,
      h('td', { class: 'mono t-2', 'data-label': copy.diff.table }, r.first ? visibleControls(r.table) : ''),
      h('td', { class: 'mono', 'data-label': copy.diff.key }, r.first ? visibleControls(r.key) : ''),
      h('td', { class: 'mono', 'data-label': copy.diff.column }, visibleControls(r.column)),
      h('td', { class: 'mono diff-exp', 'data-label': hExp }, r.unexpected && !o.pairs ? h('span', { class: 't-2', title: 'nothing expected' }, '·', h('span', { class: 'sr-only' }, ' nothing expected')) : value(r.expected, r.masked)),
      h('td', { class: `mono diff-obs${r.unexpected ? ` changed tint-${tone}` : ''}`, 'data-label': hObs },
        r.unexpected ? h('span', { class: 'diff-mark', 'aria-hidden': 'true' }, '●') : null,
        r.unexpected && !o.pairs ? h('span', { class: 'sr-only' }, 'unexpected: ') : null,
        value(r.observed, r.masked),
        r.before !== null && !r.masked && r.op === 'update' ? h('span', { class: 'diff-was' }, fill(copy.diff.was, { value: visibleControls(r.before) })) : null));
    return tr;
  });
  return h('div', { class: 'table-frame diff-frame', role: 'region', 'aria-label': copy.diff.region, tabindex: 0 },
    h('table', { class: 'dt diff cards' },
      h('thead', null, h('tr', null, ...[copy.diff.table, copy.diff.key, copy.diff.column, hExp, hObs].map((t) => h('th', { scope: 'col' }, t)))),
      h('tbody', null, ...body)));
}

/** diffTable: the summary lines, the table, then "Showing 20 of 301 rows" and the download. */
export function diffTable(m: DiffModel, o: DiffTableOptions = {}): HTMLElement {
  const lines = h('dl', { class: 'diff-lines' },
    h('dt', null, copy.diff.expected), h('dd', null, visibleControls(m.expected)),
    h('dt', null, copy.diff.observed), h('dd', { class: m.rows.some((r) => r.unexpected) ? 'diff-observed-bad' : '' }, visibleControls(m.observed)));
  const foot = h('div', { class: 'diff-foot' }, h('p', { class: 'num', 'data-diff-showing': '' }, showingLine(m)));
  if (m.truncated) foot.append(h('p', { class: 't-2' }, fill(copy.diff.kept, { n: m.shown })));
  if (o.rawJSON) {
    const a = h('button', { type: 'button', class: 'btn btn-quiet btn-sm' }, icon('download', 16), copy.diff.download);
    a.addEventListener('click', () => saveText(o.rawJSON!(), o.fileName ?? 'diff.json'));
    foot.append(a);
  }
  return h('div', { class: 'diff-wrap' }, lines, m.rows.length ? diffRowsTable(m.rows, o) : h('p', { class: 't-2' }, copy.diff.none), foot);
}
