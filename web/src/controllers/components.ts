// components.ts: S25's shared components, built at run time from data (S25 §2): MetricTile,
// Sparkline, VerificationBar, UsageMeter, AttentionList, the FilterBar controls, SnippetTabs and
// CodeBlock. Every one draws from the generated tokens through classes in app.css (no inline
// styles: the CSP refuses them, so widths are SVG attributes). S26 reuses them as they are.

import { copy, fill } from '../lib/copy.ts';
import { h, icon, s, type Child } from '../lib/dom.ts';
import { number } from '../lib/format.ts';
import type { AttentionLine, Segment } from '../lib/overview.ts';
import { CLIENTS, snippet, type Client, type SnippetInput } from '../lib/snippets.ts';

// ---------- Sparkline ----------

/** sparkline: neutral stroke and a faint area; the numbers are in the label (brand §6.2). */
export function sparkline(values: number[], label: string, width = 120, height = 28): SVGSVGElement {
  const max = Math.max(...values, 1);
  const step = width / Math.max(1, values.length - 1);
  const pts = values.map((v, i) => [i * step, height - 2 - (v / max) * (height - 4)] as const);
  const line = pts.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`).join(' ') || `M0 ${height - 2} L${width} ${height - 2}`;
  const area = `${line} L${width} ${height} L0 ${height} Z`;
  return s('svg', { class: 'spark', width, height, viewBox: `0 0 ${width} ${height}`, role: 'img', 'aria-label': label, preserveAspectRatio: 'none' },
    s('path', { class: 'area', d: area }), s('path', { d: line }));
}

// ---------- MetricTile ----------

export interface TileOptions {
  id: string;
  label: string;
  value: string;
  sub: string;
  spark?: { values: number[]; label: string };
  extra?: Child;
  href?: string;
}

/** metricTile: caption label, the number in metric type, a neutral footnote, an optional sparkline. */
export function metricTile(o: TileOptions): HTMLElement {
  const label = o.href ? h('a', { href: o.href, class: 'tile-link' }, o.label, icon('chevron-right', 16)) : o.label;
  return h('section', { class: 'tile', 'aria-labelledby': `${o.id}-l` },
    h('h2', { class: 'tile-label', id: `${o.id}-l` }, label),
    h('p', { class: 'tile-value num', 'data-tile-value': o.id }, o.value),
    h('p', { class: 'tile-sub', 'data-tile-sub': o.id }, o.sub),
    o.spark ? h('div', { class: 'tile-spark' }, sparkline(o.spark.values, o.spark.label)) : null,
    o.extra ?? null);
}

// ---------- VerificationBar ----------

const SEG_CLASS: Record<Segment['key'], string> = { VERIFIED: 'seg-verified', MISMATCH: 'seg-mismatch', UNVERIFIABLE: 'seg-unverifiable', not_witnessed: 'seg-neutral' };
const SEG_ICON: Record<Segment['key'], string> = { VERIFIED: 'check', MISMATCH: 'x', UNVERIFIABLE: 'circle-dashed', not_witnessed: 'circle' };

/** verificationBar: one stacked bar in the state colours, with every label and count in text. */
export function verificationBar(segs: Segment[], label: string): HTMLElement {
  const W = 1000;
  let x = 0;
  const bar = s('svg', { class: 'vbar', viewBox: `0 0 ${W} 8`, preserveAspectRatio: 'none', 'aria-hidden': 'true', focusable: 'false' });
  const shown = segs.filter((g) => g.count > 0);
  shown.forEach((g, i) => {
    const w = Math.max(g.share * W, 6);
    const gap = i < shown.length - 1 ? 4 : 0;
    bar.append(s('rect', { class: SEG_CLASS[g.key], x: x.toFixed(1), y: 0, width: Math.max(0, w - gap).toFixed(1), height: 8, rx: 2 }));
    x += w;
  });
  if (shown.length === 0) bar.append(s('rect', { class: 'seg-empty', x: 0, y: 0, width: W, height: 8, rx: 2 }));
  const legend = h('ul', { class: 'vlegend', 'aria-label': label }, ...segs.map((g) =>
    h('li', { class: SEG_CLASS[g.key] },
      g.key === 'not_witnessed'
        ? h('span', { class: 'vl' }, h('span', { class: 'vdot', 'aria-hidden': 'true' }), h('span', { class: 'num' }, number(g.count)), ' ', h('span', { class: 'vword' }, g.label))
        : h('a', { class: 'vl', href: g.href }, icon(SEG_ICON[g.key], 16), h('span', { class: 'num' }, number(g.count)), ' ', h('span', { class: 'vword caps' }, g.label)))));
  return h('div', { class: 'vbar-wrap' }, bar, legend);
}

// ---------- UsageMeter ----------

export type MeterLevel = 'ok' | 'near' | 'at' | 'none';

/** meterLevel: amber from 80 %, red at the limit (S25 §3.7). */
export function meterLevel(used: number, limit: number | undefined | null): MeterLevel {
  if (!limit || limit <= 0) return 'none';
  if (used >= limit) return 'at';
  return used / limit >= 0.8 ? 'near' : 'ok';
}

/** usageMeter: a compact meter with the numbers always in text. */
export function usageMeter(used: number, limit: number | undefined | null, label: string, note?: string): HTMLElement {
  const level = meterLevel(used, limit);
  const text = limit ? fill(copy.usage.of, { used: number(used), limit: number(limit) }) : fill(copy.usage.today, { used: number(used) });
  const wrap = h('div', { class: 'umeter', 'data-level': level },
    h('span', { class: 'umeter-text num' }, text),
    limit ? h('progress', { max: limit, value: Math.min(used, limit), 'aria-label': `${label}: ${text}` }) : h('span', { class: 'sr-only' }, `${label}: ${text}`));
  if (level === 'near' || level === 'at') wrap.append(h('span', { class: 'umeter-note' }, note ?? (level === 'at' ? copy.usage.at : copy.usage.near)));
  return wrap;
}

// ---------- AttentionList ----------

export function attentionList(lines: (AttentionLine & { at?: Child })[], empty: string): HTMLElement {
  if (lines.length === 0) {
    return h('p', { class: 'attn-empty' }, icon('check', 16), empty);
  }
  return h('ul', { class: 'attn' }, ...lines.map((l) => {
    const body: Child[] = [
      h('span', { class: `attn-icon tone-${l.tone}`, 'aria-hidden': 'true' }, icon(l.icon, 16)),
      h('span', { class: 'attn-text' }, h('span', { class: 'attn-title' }, l.title), l.detail ? h('span', { class: 'attn-detail' }, l.detail) : null),
      l.at ?? null,
    ];
    return h('li', null, l.href ? h('a', { class: 'attn-row', href: l.href }, ...body) : h('div', { class: 'attn-row' }, ...body));
  }));
}

// ---------- FilterBar controls ----------

export interface Option<T extends string> { value: T; label: string }

/** segmented: one choice (radio semantics with aria-pressed buttons, as the kit's control). */
export function segmented<T extends string>(label: string, options: Option<T>[], value: T, onChange: (v: T) => void, name = ''): HTMLElement {
  const group = h('div', { class: 'segmented', role: 'group', 'aria-label': label, 'data-filter': name });
  for (const o of options) {
    const b = h('button', { type: 'button', 'aria-pressed': o.value === value ? 'true' : 'false', 'data-value': o.value }, o.label);
    b.addEventListener('click', () => {
      for (const x of group.querySelectorAll('button')) x.setAttribute('aria-pressed', x === b ? 'true' : 'false');
      onChange(o.value);
    });
    group.append(b);
  }
  return group;
}

/** toggles: several choices (multi-select), each an aria-pressed button. */
export function toggles<T extends string>(label: string, options: Option<T>[], values: T[], onChange: (v: T[]) => void, name = ''): HTMLElement {
  const group = h('div', { class: 'segmented', role: 'group', 'aria-label': label, 'data-filter': name });
  const current = new Set(values);
  for (const o of options) {
    const b = h('button', { type: 'button', 'aria-pressed': current.has(o.value) ? 'true' : 'false', 'data-value': o.value }, o.label);
    b.addEventListener('click', () => {
      if (current.has(o.value)) current.delete(o.value);
      else current.add(o.value);
      b.setAttribute('aria-pressed', current.has(o.value) ? 'true' : 'false');
      onChange(options.map((x) => x.value).filter((v) => current.has(v)));
    });
    group.append(b);
  }
  return group;
}

/** select: a labelled native select with the kit's chevron. */
export function select<T extends string>(label: string, options: Option<T>[], value: T, onChange: (v: T) => void, id: string): HTMLElement {
  const el = h('select', { class: 'select', id, name: id, 'aria-label': label }, ...options.map((o) => h('option', { value: o.value, ...(o.value === value ? { selected: true } : {}) }, o.label)));
  el.addEventListener('change', () => onChange(el.value as T));
  return h('span', { class: 'select-wrap' }, el, icon('chevron-down', 16));
}

/** filterBar lays out the filters in a sticky strip (S25 §3.1): the search and the pickers on the
 *  first row, the segmented filters on the second; More filters and Clear sit at the end. */
export function filterBar(parts: { search?: HTMLElement; controls: Child[]; segments?: Child[]; end?: Child[] }): HTMLElement {
  return h('div', { class: 'filterbar', role: 'search' },
    h('div', { class: 'filterbar-row' },
      parts.search ?? null,
      h('div', { class: 'filterbar-controls' }, ...parts.controls),
      parts.end?.length ? h('div', { class: 'filterbar-end' }, ...parts.end) : null),
    parts.segments?.length ? h('div', { class: 'filterbar-row filterbar-segs' }, ...parts.segments) : null);
}

// ---------- CodeBlock and SnippetTabs ----------

export function codeBlock(lang: string, code: string, label = lang): HTMLElement {
  return h('figure', { class: 'code' },
    h('figcaption', { class: 'code-head' }, h('span', null, lang), h('button', { type: 'button', class: 'icon-btn', 'data-copy': code, 'aria-label': `Copy ${label}` }, icon('copy', 16))),
    h('pre', { tabindex: 0 }, code));
}

/** snippetTabs: one tab per MCP client; the panel shows that client's config, filled in. */
export function snippetTabs(input: () => SnippetInput, id = 'snip'): { el: HTMLElement; refresh: () => void } {
  let current: Client = CLIENTS[0]!;
  const panel = h('div', { class: 'tabpanel', role: 'tabpanel', id: `${id}-panel`, tabindex: 0 });
  const tabs = CLIENTS.map((c, i) => {
    const t = h('button', { type: 'button', role: 'tab', id: `${id}-${c}`, 'aria-controls': `${id}-panel`, 'aria-selected': i === 0 ? 'true' : 'false', tabindex: i === 0 ? 0 : -1, 'data-client': c }, copy.snippets.tabs[c] ?? c);
    return t;
  });
  const render = () => {
    const sn = snippet(current, input());
    panel.setAttribute('aria-labelledby', `${id}-${current}`);
    panel.replaceChildren(codeBlock(sn.lang, sn.text, copy.snippets.tabs[current] ?? current), h('p', { class: 't-footnote t-2 snippet-where' }, sn.where));
  };
  const pick = (i: number, focus: boolean) => {
    current = CLIENTS[i]!;
    tabs.forEach((t, j) => {
      t.setAttribute('aria-selected', j === i ? 'true' : 'false');
      t.tabIndex = j === i ? 0 : -1;
    });
    if (focus) tabs[i]!.focus();
    render();
  };
  tabs.forEach((t, i) => {
    t.addEventListener('click', () => pick(i, false));
    t.addEventListener('keydown', (e) => {
      const n = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : -9;
      if (n === -9) return;
      e.preventDefault();
      pick((n + tabs.length) % tabs.length, true);
    });
  });
  render();
  const el = h('div', { class: 'snippets' }, h('div', { class: 'tabs', role: 'tablist', 'aria-label': copy.firstRun.step2 }, ...tabs), panel);
  return { el, refresh: render };
}

// ---------- small pieces ----------

/** cardSection: a card with a header row (title, optional right side) and a body. */
export function cardSection(id: string, title: Child, right: Child | null, ...body: Child[]): HTMLElement {
  return h('section', { class: 'card', 'aria-labelledby': id },
    h('div', { class: 'card-head' }, h('h2', { class: 't-headline', id }, title), right ?? null),
    ...body);
}

/** kv: a definition list from pairs; null values are skipped. */
export function kv(pairs: [string, Child][], cls = 'kv'): HTMLElement {
  const dl = h('dl', { class: cls });
  for (const [k, v] of pairs) {
    if (v === null || v === undefined || v === false || v === '') continue;
    dl.append(h('dt', null, k), h('dd', null, v));
  }
  return dl;
}

export function mono(v: string): HTMLElement {
  return h('span', { class: 'mono', translate: 'no' }, visibleControls(v));
}

/** visibleControls shows bidi and other control characters literally (S25 §7): ⟨U+202E⟩. */
export function visibleControls(v: string): string {
  return v.replace(/[\u0000-\u001f\u007f‎‏‪-‮⁦-⁩]/g, (c) => `⟨U+${c.charCodeAt(0).toString(16).toUpperCase().padStart(4, '0')}⟩`);
}
