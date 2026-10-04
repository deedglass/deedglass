// jsonview.ts: JsonView (S25 §3.5): a collapsible tree of a request or result payload. Keys in
// secondary text; strings, numbers and booleans in mono; "•••" for a redacted value with a
// "masked" label; copy path and copy value on every row. Text only (dom.ts). Nodes past depth 8,
// or past the first 2,000, load when you click.

import { copy, fill } from '../lib/copy.ts';
import { h, icon } from '../lib/dom.ts';
import { visibleControls } from './components.ts';

const MAX_DEPTH = 8;
const MAX_NODES = 2000;
const CHUNK = 200;

/** pathOf: $.params[0].id style paths; keys that aren't identifiers are quoted. */
export function pathOf(parent: string, key: string | number): string {
  if (typeof key === 'number') return `${parent}[${key}]`;
  return /^[A-Za-z_$][\w$]*$/.test(key) ? `${parent}.${key}` : `${parent}[${JSON.stringify(key)}]`;
}

function leaf(v: unknown): HTMLElement {
  if (v === '•••') return h('span', { class: 'jv-masked' }, '•••', h('span', { class: 'masked-label' }, copy.json.masked));
  if (typeof v === 'string') return h('span', { class: 'jv-str mono' }, `"${visibleControls(v)}"`);
  if (typeof v === 'number') return h('span', { class: 'jv-num mono' }, String(v));
  if (typeof v === 'boolean') return h('span', { class: 'jv-bool mono' }, String(v));
  return h('span', { class: 'jv-null mono' }, 'null');
}

function copyButtons(path: string, v: unknown): HTMLElement {
  const val = typeof v === 'string' ? v : JSON.stringify(v);
  return h('span', { class: 'jv-copy' },
    h('button', { type: 'button', class: 'icon-btn', 'data-copy': path, 'aria-label': `${copy.json.copyPath} ${path}`, title: copy.json.copyPath }, icon('link', 16)),
    h('button', { type: 'button', class: 'icon-btn', 'data-copy': val ?? '', 'aria-label': `${copy.json.copyValue} ${path}`, title: copy.json.copyValue }, icon('copy', 16)));
}

export function jsonView(value: unknown, rootLabel = '$'): HTMLElement {
  let nodes = 0;
  const node = (key: string | number | null, v: unknown, path: string, depth: number): HTMLElement => {
    nodes++;
    const keyEl = key === null ? null : h('span', { class: 'jv-key' }, typeof key === 'number' ? String(key) : visibleControls(key), h('span', { class: 't-2' }, ': '));
    if (v === null || typeof v !== 'object') {
      return h('li', { class: 'jv-row' }, h('span', { class: 'jv-line' }, keyEl, leaf(v), copyButtons(path, v)));
    }
    const entries: [string | number, unknown][] = Array.isArray(v) ? v.map((x, i) => [i, x]) : Object.entries(v as Record<string, unknown>);
    const open = depth < 2;
    const kids = h('ul', { class: 'jv-kids' });
    const summary = Array.isArray(v) ? `[${entries.length}]` : `{${entries.length}}`;
    const toggle = h('button', { type: 'button', class: 'jv-toggle', 'aria-expanded': open ? 'true' : 'false' }, icon('chevron-right', 16), keyEl ?? h('span', { class: 'jv-key' }, rootLabel), h('span', { class: 't-2 mono' }, summary));
    let filled = false;
    const fill_ = () => {
      if (filled) return;
      filled = true;
      let i = 0;
      const more = () => {
        const end = Math.min(entries.length, i + CHUNK);
        for (; i < end; i++) {
          const [k, x] = entries[i]!;
          if (depth + 1 > MAX_DEPTH || nodes > MAX_NODES) {
            const lazy = h('li', { class: 'jv-row' }, h('button', { type: 'button', class: 'btn btn-quiet btn-sm' }, fill(copy.json.more, { n: entries.length - i })));
            const start = i;
            lazy.querySelector('button')!.addEventListener('click', () => {
              nodes = 0;
              lazy.remove();
              for (let j = start; j < entries.length; j++) {
                const [kk, xx] = entries[j]!;
                kids.append(node(kk, xx, pathOf(path, kk), 0));
              }
            });
            kids.append(lazy);
            i = entries.length;
            return;
          }
          kids.append(node(k, x, pathOf(path, k), depth + 1));
        }
        if (i < entries.length) {
          const btn = h('button', { type: 'button', class: 'btn btn-quiet btn-sm' }, fill(copy.json.more, { n: entries.length - i }));
          const li = h('li', { class: 'jv-row' }, btn);
          btn.addEventListener('click', () => {
            li.remove();
            more();
          });
          kids.append(li);
        }
      };
      more();
    };
    kids.hidden = !open;
    if (open) fill_();
    toggle.addEventListener('click', () => {
      const now = toggle.getAttribute('aria-expanded') !== 'true';
      toggle.setAttribute('aria-expanded', now ? 'true' : 'false');
      if (now) fill_();
      kids.hidden = !now;
    });
    if (entries.length === 0) return h('li', { class: 'jv-row' }, h('span', { class: 'jv-line' }, keyEl, h('span', { class: 't-2 mono' }, Array.isArray(v) ? '[]' : '{}')));
    return h('li', { class: 'jv-row' }, h('span', { class: 'jv-line' }, toggle, copyButtons(path, v)), kids);
  };
  return h('ul', { class: 'jv' }, node(null, value, '$', 0));
}
