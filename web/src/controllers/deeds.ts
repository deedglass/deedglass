// deeds.ts: the Deeds list and the deed view (S25 §3.6). The chain card heads the list; the list
// is newest first; the export sheet downloads a bundle and says how to check it offline. The deed
// view shows contract §7's sections, the exact raw JCS (never pretty-printed unless you ask, and
// then labelled), the DeedPanel and ← → by seq.

import { api, ApiError, API_VERSION } from '../lib/api.ts';
import { hashChip, idChip, relTime } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { deedSummary } from '../lib/deedview.ts';
import { h, icon, maybe, replace } from '../lib/dom.ts';
import { number } from '../lib/format.ts';
import { get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { DeedEnvelope, LedgerCheck, ListResponse } from '../lib/types.ts';
import { codeBlock, segmented, visibleControls } from './components.ts';
import { chainView } from './chain.ts';
import { openSheet } from './overlay.ts';
import { emptyState, errorState, permissionState, skeleton } from './screens.ts';
import { listKeys, wireTable } from './table.ts';
import { toast } from './toast.ts';

const tzOf = () => get()?.org.timezone ?? 'UTC';
type Kind = '' | 'action' | 'admin' | 'amendment';

export function kindChip(k: string): HTMLElement {
  const ic = k === 'admin' ? 'settings' : k === 'amendment' ? 'refresh-cw' : 'deed';
  return h('span', { class: `chip ${k === 'amendment' ? 'chip-outline' : 'chip-neutral'}` }, icon(ic, 16), copy.deeds.kind[k] ?? k);
}

export function chainBanner(): { el: HTMLElement; load: () => Promise<void> } {
  const body = h('div', { class: 'chain', 'aria-live': 'polite' }, h('p', { class: 't-2' }, copy.overview.chainChecking));
  const el = h('section', { class: 'card chain-banner', 'aria-label': copy.overview.chain }, body);
  const load = async () => {
    try {
      const c = await api<LedgerCheck>('GET', '/v1/ledger/check', { query: { 'issued_at[gte]': new Date(Date.now() - 86_400_000).toISOString() } });
      replace(body, ...chainView(c));
    } catch (e) {
      replace(body, h('p', { class: 't-2' }, fill(copy.overview.chainError, { message: (e as Error).message })));
    }
  };
  return { el, load };
}

function exportSheet(): void {
  const tz = tzOf();
  const today = new Date();
  const iso = (d: Date) => d.toISOString().slice(0, 10);
  const from = h('input', { class: 'input', type: 'date', id: 'exp-from', name: 'from', autocomplete: 'off', value: iso(new Date(today.getTime() - 7 * 86_400_000)) });
  const to = h('input', { class: 'input', type: 'date', id: 'exp-to', name: 'to', autocomplete: 'off', value: iso(today) });
  const err = h('p', { class: 'error', role: 'alert', hidden: true });
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, icon('download', 16), copy.deeds.exportButton);
  const cmd = h('div', { class: 'export-cmd' });
  const showCmd = (file: string) => replace(cmd, h('p', { class: 't-footnote' }, copy.deeds.exportNote), codeBlock('shell', fill(copy.deeds.exportCommand, { file, origin: location.origin })));
  showCmd('deedglass-<org>-<from>-<to>.tar.gz');
  const sheet = openSheet({
    title: copy.deeds.exportTitle,
    body: [
      h('p', null, fill(copy.deeds.exportLede, { tz })),
      h('div', { class: 'field-row' },
        h('div', { class: 'field' }, h('label', { class: 'label', for: 'exp-from' }, copy.deeds.exportFrom), from),
        h('div', { class: 'field' }, h('label', { class: 'label', for: 'exp-to' }, copy.deeds.exportTo), to)),
      err, cmd,
    ],
    foot: [go],
  });
  void sheet;
  go.addEventListener('click', async () => {
    err.hidden = true;
    go.disabled = true;
    go.setAttribute('aria-busy', 'true');
    try {
      // The dates are the org's days: from 00:00 on "from" to 00:00 after "to", sent as UTC instants.
      const start = zonedMidnight(from.value, tz);
      const end = zonedMidnight(to.value, tz, 1);
      const q = new URLSearchParams({ 'issued_at[gte]': start.toISOString(), 'issued_at[lt]': end.toISOString() });
      const res = await fetch(`/v1/deeds/export?${q}`, { headers: { 'Deedglass-Version': API_VERSION, Accept: 'application/gzip, application/json' }, credentials: 'same-origin', redirect: 'error' });
      if (!res.ok) {
        const body = await res.json().catch(() => null) as { error?: { message?: string } } | null;
        throw new Error(body?.error?.message ?? `The server answered ${res.status}.`);
      }
      const name = /filename="?([^";]+)"?/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? 'deedglass-export.tar.gz';
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = name;
      document.body.append(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 30_000);
      showCmd(name);
    } catch (e) {
      err.hidden = false;
      replace(err, icon('circle-alert', 16), (e as Error).message);
    } finally {
      go.disabled = false;
      go.removeAttribute('aria-busy');
    }
  });
}

/** zonedMidnight: 00:00 of a YYYY-MM-DD day in a time zone, as a Date (plus `addDays`). */
export function zonedMidnight(day: string, tz: string, addDays = 0): Date {
  const [y, m, d] = day.split('-').map(Number) as [number, number, number];
  const guess = new Date(Date.UTC(y, m - 1, d + addDays));
  // The zone's offset at that instant: format it in tz and compare.
  const parts = new Intl.DateTimeFormat('en-US', { timeZone: tz, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }).formatToParts(guess);
  const get_ = (t: string) => Number(parts.find((p) => p.type === t)?.value ?? 0);
  const asUTC = Date.UTC(get_('year'), get_('month') - 1, get_('day'), get_('hour') % 24, get_('minute'), get_('second'));
  return new Date(guess.getTime() - (asUTC - guess.getTime()));
}

export function mountDeeds(): void {
  const host = maybe('screen-body');
  const head = maybe('deeds-actions');
  if (!host) return;
  const banner = chainBanner();
  let kind: Kind = (new URLSearchParams(location.search).get('kind') as Kind) || '';
  if (!['', 'action', 'admin', 'amendment'].includes(kind)) kind = '';
  let cursor: string | null = null;
  const tbody = h('tbody');
  const table = h('table', { class: 'dt cards deeds' },
    h('thead', null, h('tr', null, ...(['seq', 'kind', 'summary', 'issued', 'deed', 'hash'] as const).map((k) => h('th', { scope: 'col', class: k === 'seq' ? 'num' : '' }, copy.deeds.columns[k])))), tbody);
  wireTable(table);
  const older = h('button', { type: 'button', class: 'btn btn-secondary feed-older', hidden: true }, copy.feed.older);
  const row = (d: DeedEnvelope) => {
    const tz = tzOf();
    return h('tr', { 'data-row': '', 'data-id': d.id, 'aria-selected': 'false' },
      h('td', { class: 'num mono', 'data-card': 'hide' }, number(d.body.seq)),
      h('td', { 'data-card': 'hide' }, kindChip(d.body.kind)),
      h('td', null, h('a', { href: `/app/deeds/${d.id}`, class: 'rowlink' }, h('span', { class: 'mono t-2 seq-inline' }, `${number(d.body.seq)} `), visibleControls(deedSummary(d.body)))),
      h('td', { class: 'num' }, relTime(d.body.issued_at, tz)),
      h('td', { 'data-card': 'hide' }, idChip(d.id)),
      h('td', { 'data-card': 'hide' }, hashChip(d.body_hash)));
  };
  const load = async (more = false) => {
    if (!more) {
      host.setAttribute('aria-busy', 'true');
      host.replaceChildren(banner.el, skeleton(12));
    }
    try {
      const q: Record<string, string> = { order: 'desc', limit: '50' };
      if (kind) q.kind = kind;
      if (more && cursor) q.cursor = cursor;
      const page = await api<ListResponse<DeedEnvelope>>('GET', '/v1/deeds', { query: q });
      if (!more) tbody.replaceChildren();
      tbody.append(...page.data.map(row));
      cursor = page.has_more ? page.next_cursor : null;
      older.hidden = !cursor;
      if (!more) {
        if (page.data.length === 0) host.replaceChildren(banner.el, kind ? emptyState(copy.feed.filteredEmpty.replace('actions', 'deeds'), 'search', h('button', { type: 'button', class: 'btn btn-secondary', onclick: () => setKind('') }, copy.feed.clear)) : emptyState(copy.pages.deeds.empty, 'deed'));
        else host.replaceChildren(banner.el, h('div', { class: 'table-frame' }, table), older);
      }
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) host.replaceChildren(permissionState('Viewer'));
      else if (!(e instanceof ApiError && e.status === 401)) host.replaceChildren(banner.el, errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  const setKind = (k: Kind) => {
    kind = k;
    const u = new URL(location.href);
    if (k) u.searchParams.set('kind', k);
    else u.searchParams.delete('kind');
    history.replaceState(null, '', u.pathname + u.search);
    void load();
  };
  older.addEventListener('click', () => void load(true));
  if (head) {
    const exp = h('button', { type: 'button', class: 'btn btn-secondary btn-sm' }, icon('download', 16), copy.deeds.export);
    exp.addEventListener('click', exportSheet);
    head.replaceChildren(segmented<Kind>(copy.deeds.filterKind, [
      { value: '', label: copy.deeds.kindAll }, { value: 'action', label: copy.deeds.kind.action! }, { value: 'admin', label: copy.deeds.kind.admin! }, { value: 'amendment', label: copy.deeds.kind.amendment! },
    ], kind, setKind, 'kind'), exp);
  }
  listKeys();
  document.addEventListener('dg:ready', () => {
    void load();
    void banner.load();
    let t = 0;
    stream().on('deed.sealed', () => {
      clearTimeout(t);
      t = window.setTimeout(() => void load(), 1500);
    });
  }, { once: true });
}

export { toast };
