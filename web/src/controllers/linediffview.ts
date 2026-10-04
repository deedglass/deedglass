// linediffview.ts: LineDiff (S26 §3.2 Versions): a unified view of two policy versions. Old and new
// line numbers, a `+`/`−` gutter with "added"/"removed" for screen readers, three lines of context,
// `@@ lines 37 to 44 @@` hunk headers. The tints are neutral (accent at 10 % for added, the alt
// surface for removed), never green and red: a policy diff isn't a verdict. The frame scrolls
// sideways as a focusable region, so a long line never widens the page.

import { fill } from '../lib/copy.ts';
import { editorCopy as c } from '../lib/copy-editor.ts';
import { h } from '../lib/dom.ts';
import type { Hunk, LineDiff } from '../lib/linediff.ts';

export interface LineDiffOptions {
  /** Version labels for the region's name, e.g. v3 and v4. */
  from?: string;
  to?: string;
}

/** hunkHeader: `@@ lines 37 to 44 @@` over the new version's lines (the old ones for a pure removal). */
export function hunkHeader(k: Hunk): string {
  const [start, count] = k.newLines > 0 ? [k.newStart, k.newLines] : [k.oldStart, k.oldLines];
  if (count <= 1) return fill(c.hunkOne, { line: start });
  return fill(c.hunk, { from: start, to: start + count - 1 });
}

export function lineDiffView(d: LineDiff, o: LineDiffOptions = {}): HTMLElement {
  const label = o.from && o.to ? fill(c.diffLabel, { from: o.from, to: o.to }) : c.diffLabelPlain;
  const head = h('p', { class: 'ld-summary t-footnote t-2' }, d.identical ? c.diffSame : fill(c.diffSummary, { added: d.added.toLocaleString('en-US'), removed: d.removed.toLocaleString('en-US') }));
  const note = d.crlf ? h('p', { class: 'ld-note t-footnote t-2' }, c.diffCrlf) : null;
  if (d.identical) return h('div', { class: 'ld-wrap' }, head, note);
  const body = h('tbody');
  for (const k of d.hunks) {
    body.append(h('tr', { class: 'ld-hunk' }, h('td', { colspan: '4' }, hunkHeader(k))));
    for (const l of k.lines) {
      const sign = l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : '';
      const sr = l.kind === 'add' ? c.added : l.kind === 'del' ? c.removed : '';
      body.append(h('tr', { class: `ld-row ld-${l.kind}` },
        h('td', { class: 'ld-n' }, l.old ?? ''),
        h('td', { class: 'ld-n' }, l.new ?? ''),
        h('td', { class: 'ld-sign' }, sign ? h('span', { 'aria-hidden': 'true' }, sign) : null, sr ? h('span', { class: 'sr-only' }, sr) : null),
        h('td', { class: 'ld-text' }, l.text)));
    }
  }
  const table = h('table', { class: 'ld' },
    h('thead', { class: 'sr-only' }, h('tr', null,
      h('th', { scope: 'col' }, c.diffCols.old), h('th', { scope: 'col' }, c.diffCols.new),
      h('th', { scope: 'col' }, c.diffCols.change), h('th', { scope: 'col' }, c.diffCols.text))),
    body);
  return h('div', { class: 'ld-wrap' }, head, note,
    h('div', { class: 'ld-frame', role: 'region', tabindex: '0', 'aria-label': label, translate: 'no' }, table));
}
