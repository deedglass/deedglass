// table.ts: the Table component's behaviour (S24 §3.6): sortable headers (aria-sort), j/k to move
// between rows, Enter to open the selected row's link. Card mode under 640 px is CSS (.dt.cards).

import { typing } from '../lib/shortcuts.ts';

/** sortTable sorts a table body by one column's data-sort (or text) value. */
export function sortTable(table: HTMLTableElement, col: number, dir: 'ascending' | 'descending'): void {
  const body = table.tBodies[0];
  if (!body) return;
  const rows = [...body.rows];
  const key = (r: HTMLTableRowElement) => {
    const c = r.cells[col];
    return c?.getAttribute('data-sort') ?? c?.textContent?.trim() ?? '';
  };
  rows.sort((a, b) => {
    const x = key(a);
    const y = key(b);
    const nx = Number(x);
    const ny = Number(y);
    const cmp = x !== '' && y !== '' && Number.isFinite(nx) && Number.isFinite(ny) ? nx - ny : x.localeCompare(y);
    return dir === 'ascending' ? cmp : -cmp;
  });
  body.append(...rows);
}

export function wireTable(table: HTMLTableElement): void {
  const heads = [...table.querySelectorAll<HTMLTableCellElement>('thead th')];
  heads.forEach((th, i) => {
    const btn = th.querySelector('button');
    if (!btn) return;
    btn.addEventListener('click', () => {
      const dir = th.getAttribute('aria-sort') === 'ascending' ? 'descending' : 'ascending';
      for (const o of heads) o.removeAttribute('aria-sort');
      th.setAttribute('aria-sort', dir);
      sortTable(table, i, dir);
    });
  });
}

/** listKeys: j/k move a selection through [data-row] rows on the page; Enter opens it. */
export function listKeys(): void {
  document.addEventListener('keydown', (e) => {
    if (e.metaKey || e.ctrlKey || e.altKey || typing(e.target)) return;
    if (document.querySelector('.palette, .sheet, .dialog, .menu')) return;
    if (e.key !== 'j' && e.key !== 'k' && e.key !== 'Enter') return;
    const rows = [...document.querySelectorAll<HTMLElement>('[data-row]')];
    if (rows.length === 0) return;
    const cur = rows.findIndex((r) => r.getAttribute('aria-selected') === 'true');
    if (e.key === 'Enter') {
      if (cur < 0 || document.activeElement !== rows[cur]) return;
      const a = rows[cur]!.querySelector<HTMLAnchorElement>('a[href]');
      if (a) {
        e.preventDefault();
        a.click();
      }
      return;
    }
    e.preventDefault();
    const next = e.key === 'j' ? Math.min(rows.length - 1, cur + 1) : Math.max(0, cur < 0 ? 0 : cur - 1);
    rows[cur]?.setAttribute('aria-selected', 'false');
    const r = rows[next]!;
    r.setAttribute('aria-selected', 'true');
    r.tabIndex = -1;
    r.focus({ preventScroll: false });
  });
}
