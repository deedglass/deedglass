// sessions.ts: observe mode in the Console (S50 §5): the Sessions list, the session page (header
// line, attribution note, transactions with Leave out, the diff as a captioned table, the record
// line, the undo area with Undo this session (Pro) and Copy undo SQL (free) side by side), the Pro
// sheet and the caught banner on Overview. A caught view always shows the free path (S50 §7).

import { api, ApiError } from '../lib/api.ts';
import { idChip, relTime } from '../lib/chips.ts';
import { fill } from '../lib/copy.ts';
import { sessionsCopy as c } from '../lib/copy-sessions.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { number, time } from '../lib/format.ts';
import { atLeast, get } from '../lib/state.ts';
import { openSheet } from './overlay.ts';
import { emptyState, errorState, notFound, permissionState, skeleton } from './screens.ts';
import { toast } from './toast.ts';
import { announce } from './shell.ts';

// ---------- the API's shapes (internal/observeapi) ----------

export interface Counts { insert: number; update: number; delete: number; truncate: number }
export interface CaughtItem { kind: string; session?: string; frames?: string[]; table?: string; rows?: number; line: string; undo_possible: boolean; undo_free_path: string }
export interface Cell { column: string; before: string | null; after: string | null; changed: boolean }
export interface DiffRow { op: string; table: string; key: Record<string, string>; cells: Cell[] }
export interface Frame {
  id: string; commit_time: string | null; commit_lsn: string; xid: number; tables: Record<string, Counts>; changes: number;
  excluded: boolean; expired: boolean; flags: string[]; diff: { rows: DiffRow[]; total: number; truncated: boolean } | null;
}
export interface Session {
  object: 'observe_session'; id: string; connector: { id: string; name: string }; label: string | null; agent: string | null; state: string;
  opened_at: string; closed_at: string | null; flags: string[]; watched_tables: string[];
  summary: { transactions: number; excluded: number; expired: number; rows: number; tables: Record<string, Counts>; first_commit: string | null; last_commit: string | null };
  caught: CaughtItem[]; seal: { deed: string } | null; undo: { action: string | null; possible: boolean; free_path: string }; notes: string[];
  observed: string; attribution: string; transactions?: { object: 'list'; total: number; data: Frame[] };
}
export interface Preview {
  object: 'observe_undo_preview'; session: string; available: boolean; summary: string; headline: string; plan_digest: string | null;
  conflicts: number; excluded: string[]; sql: string | null; refusals: string[]; pro: boolean; free_path: string; rows: number;
}
interface List<T> { object: 'list'; data: T[]; has_more: boolean; next_cursor: string | null }

const tz = () => get()?.org.timezone ?? 'UTC';
const canAct = () => atLeast(get()?.me.membership.role, 'admin');

export function windowText(s: Session): string {
  const o = time(s.opened_at, tz());
  return s.closed_at ? `${o}–${time(s.closed_at, tz())}` : `${o}–now`;
}

/** tableLines: one [table, "301 updated"] per table, sorted. Matches observe.TableLines. */
export function tableLines(tables: Record<string, Counts>): [string, string][] {
  return Object.keys(tables).sort().flatMap((t) => {
    const x = tables[t]!;
    const ops: string[] = [];
    if (x.update) ops.push(`${number(x.update)} updated`);
    if (x.insert) ops.push(`${number(x.insert)} inserted`);
    if (x.delete) ops.push(`${number(x.delete)} deleted`);
    if (x.truncate) ops.push(x.truncate === 1 ? 'truncated once' : `truncated ${number(x.truncate)} times`);
    return ops.length ? [[t, ops.join(', ')] as [string, string]] : [];
  });
}

export function stateChip(state: string): HTMLElement {
  const cls = state === 'undone' ? 'chip-verified' : state === 'open' || state === 'closing' || state === 'undoing' ? 'chip-pending' : 'chip-neutral';
  return h('span', { class: `chip ${cls}` }, c.states[state] ?? state);
}

export const caughtChip = (): HTMLElement => h('span', { class: 'chip chip-caught' }, icon('triangle-alert', 16), c.caughtChip);

// ---------- the free path and the Pro sheet ----------

async function copySQL(id: string, btn?: HTMLButtonElement): Promise<void> {
  btn?.setAttribute('aria-busy', 'true');
  try {
    const pv = await api<Preview>('GET', `/v1/observe/sessions/${id}/undo_preview`, { query: { allow_triggers: 'true' } });
    if (!pv.sql) {
      toast(fill(c.sqlRefused, { reason: pv.refusals[0] ?? c.noTransactions }));
      return;
    }
    await navigator.clipboard.writeText(pv.sql);
    toast(c.copied);
  } catch (e) {
    toast((e as Error).message);
  } finally {
    btn?.removeAttribute('aria-busy');
  }
}

export function proSheet(id: string): void {
  const copyBtn = h('button', { type: 'button', class: 'btn btn-secondary' }, icon('copy', 16), c.copySQL) as HTMLButtonElement;
  copyBtn.addEventListener('click', () => void copySQL(id, copyBtn));
  openSheet({ title: c.proTitle, body: [h('p', null, c.proSheet)],
    foot: [h('a', { class: 'btn btn-primary', href: c.proURL }, c.seePro, icon('arrow-up-right', 16)), copyBtn] });
}

/** freeAndPro: the two ways back side by side; the free one is always there. */
export function undoActions(s: Session, pv: Preview | null): HTMLElement {
  const copyBtn = h('button', { type: 'button', class: 'btn btn-secondary', 'data-free-path': '' }, icon('copy', 16), c.copySQL) as HTMLButtonElement;
  copyBtn.addEventListener('click', () => void copySQL(s.id, copyBtn));
  const undoBtn = h('button', { type: 'button', class: 'btn btn-primary' }, c.undoSession, h('span', { class: 'pro-tag' }, `· ${c.undoPro}`)) as HTMLButtonElement;
  undoBtn.disabled = !canAct() || s.state !== 'sealed' || (pv !== null && pv.pro && !pv.available);
  undoBtn.addEventListener('click', () => {
    if (!pv || !pv.pro) proSheet(s.id);
    else undoSheet(s, pv);
  });
  return h('div', { class: 'obs-undo-actions' }, undoBtn, copyBtn);
}

function undoSheet(s: Session, pv: Preview): void {
  const reason = h('textarea', { class: 'input', id: 'obs-reason', name: 'reason', rows: 3, maxlength: 280, placeholder: c.reasonPlaceholder });
  const err = h('p', { class: 'error', role: 'alert', hidden: true });
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, c.undoConfirm) as HTMLButtonElement;
  const sheet = openSheet({ title: c.undoSession,
    body: [h('p', null, pv.summary), h('div', { class: 'field' }, h('label', { class: 'label', for: 'obs-reason' }, c.reasonLabel), reason), err],
    foot: [go], initialFocus: () => reason });
  go.addEventListener('click', async () => {
    err.hidden = true;
    go.disabled = true;
    try {
      const a = await api<{ id: string }>('POST', `/v1/observe/sessions/${s.id}/undo`, { body: { plan_digest: pv.plan_digest, reason: (reason as HTMLTextAreaElement).value } });
      sheet.close();
      toast(fill(c.undoSent, { action: a.id }));
      announce(fill(c.undoSent, { action: a.id }));
      location.reload();
    } catch (e) {
      err.hidden = false;
      replace(err, icon('circle-alert', 16), (e as Error).message);
      go.disabled = false;
    }
  });
}

// ---------- the caught banner (Session page and Overview) ----------

export function caughtBanner(items: CaughtItem[], s: Session | null, pv: Preview | null): HTMLElement | null {
  if (!items.length) return null;
  return h('section', { class: 'card obs-caught', role: 'status', 'aria-labelledby': 'obs-caught-t' },
    h('h2', { class: 'sr-only', id: 'obs-caught-t' }, c.caughtTitle),
    h('div', { class: 'obs-caught-lines' }, ...items.map((it) => h('p', { class: 'obs-caught-line' }, icon('triangle-alert', 16), it.line))),
    s ? undoActions(s, pv) : null);
}

/** homeLine: the line without its "Deedglass caught this: " prefix (the banner's heading says it). */
export function homeLine(line: string): string {
  const rest = line.replace(/^Deedglass caught this: /, '');
  return rest.charAt(0).toUpperCase() + rest.slice(1);
}

/** mountCaughtBanner: Overview's banner, the three newest caught sessions, each with both ways back. */
export async function mountCaughtBanner(host: HTMLElement): Promise<void> {
  try {
    const page = await api<List<Session>>('GET', '/v1/observe/sessions', { query: { caught: 'true', limit: '3' } });
    if (!page.data.length) {
      host.replaceChildren();
      return;
    }
    host.replaceChildren(h('section', { class: 'card obs-caught obs-caught-home', 'aria-labelledby': 'obs-home-t' },
      h('h2', { class: 't-footnote obs-caught-head', id: 'obs-home-t' }, icon('triangle-alert', 16), c.caughtTitle),
      h('ul', { class: 'obs-caught-list' }, ...page.data.map((s) => h('li', { class: 'obs-caught-item' },
        h('p', { class: 'obs-caught-line' }, h('span', { class: 'obs-caught-label' }, s.label ?? s.id), ' · ', homeLine(s.caught[0]?.line ?? '')),
        h('div', { class: 'obs-undo-actions' },
          h('a', { class: 'btn btn-quiet', href: `/app/sessions/${s.id}` }, c.caughtOpen, icon('chevron-right', 16)),
          (() => { const b = h('button', { type: 'button', class: 'btn btn-quiet', 'data-free-path': '' }, icon('copy', 16), c.copySQL) as HTMLButtonElement; b.addEventListener('click', () => void copySQL(s.id, b)); return b; })()))))));
  } catch {
    host.replaceChildren(); // the banner is extra: Overview never fails for it
  }
}

// ---------- the list ----------

function startSheet(onDone: (id: string) => void): void {
  const sel = h('select', { class: 'input', id: 'obs-con', name: 'connector' });
  const label = h('input', { class: 'input', id: 'obs-label', name: 'label', maxlength: 80, autocomplete: 'off' });
  const err = h('p', { class: 'error', role: 'alert', hidden: true });
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, icon('eye', 16), c.startButton) as HTMLButtonElement;
  const sheet = openSheet({ title: c.startTitle, body: [h('p', null, c.startLede),
    h('div', { class: 'field' }, h('label', { class: 'label', for: 'obs-con' }, c.connector), sel),
    h('div', { class: 'field' }, h('label', { class: 'label', for: 'obs-label' }, c.label), label), err], foot: [go] });
  void api<List<{ id: string; name: string; type: string; config?: { witness?: { observe?: { enabled?: boolean } } } }>>('GET', '/v1/connectors').then((l) => {
    const ok = l.data.filter((x) => x.type === 'postgres' && x.config?.witness?.observe?.enabled);
    if (!ok.length) {
      replace(err, c.noConnector);
      err.hidden = false;
      go.disabled = true;
    }
    replace(sel, ...ok.map((x) => h('option', { value: x.name }, x.name)));
  }).catch(() => undefined);
  go.addEventListener('click', async () => {
    go.disabled = true;
    try {
      const body: Record<string, string> = { connector: (sel as HTMLSelectElement).value };
      const l = (label as HTMLInputElement).value.trim();
      if (l) body.label = l;
      const s = await api<Session>('POST', '/v1/observe/sessions', { body });
      sheet.close();
      onDone(s.id);
    } catch (e) {
      err.hidden = false;
      replace(err, icon('circle-alert', 16), (e as Error).message);
      go.disabled = false;
    }
  });
}

export function sessionRow(s: Session): HTMLElement {
  const tables = tableLines(s.summary.tables).map(([t]) => t);
  return h('tr', { 'data-row': '' },
    h('td', { 'data-label': c.columns.label }, h('a', { class: 'rowlink', href: `/app/sessions/${s.id}` }, s.label ?? s.id)),
    h('td', { 'data-label': c.columns.connector }, h('span', { class: 'mono', translate: 'no' }, s.connector.name)),
    h('td', { class: 'num', 'data-label': c.columns.window }, windowText(s)),
    h('td', { class: 'num', 'data-label': c.columns.transactions }, `${number(s.summary.transactions)} ${s.summary.transactions === 1 ? 'transaction' : 'transactions'}`),
    h('td', { 'data-label': c.columns.tables, 'data-card': 'hide' }, h('span', { class: 'mono obs-tables', translate: 'no' }, tables.join(', '))),
    h('td', { 'data-label': c.columns.state }, h('span', { class: 'obs-chips' }, s.caught.length ? caughtChip() : null, stateChip(s.state))));
}

export function mountSessions(): void {
  const host = maybe('screen-body');
  const actions = maybe('sessions-actions');
  if (!host) return;
  const load = async () => {
    host.setAttribute('aria-busy', 'true');
    host.replaceChildren(skeleton());
    try {
      const page = await api<List<Session>>('GET', '/v1/observe/sessions', { query: { limit: '50' } });
      if (!page.data.length) {
        host.replaceChildren(emptyState(c.empty, 'eye', canAct() ? startButton() : undefined, c.docs));
      } else {
        const cols = c.columns;
        host.replaceChildren(h('div', { class: 'table-frame' }, h('table', { class: 'dt cards obs-list' },
          h('caption', { class: 'sr-only' }, c.lede),
          h('thead', null, h('tr', null, ...[cols.label, cols.connector, cols.window, cols.transactions, cols.tables, cols.state].map((l, i) =>
            h('th', { scope: 'col', class: i === 2 || i === 3 ? 'num' : '' }, l)))),
          h('tbody', null, ...page.data.map(sessionRow)))));
      }
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) host.replaceChildren(permissionState('Viewer'));
      else host.replaceChildren(errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  const startButton = () => {
    const b = h('button', { type: 'button', class: 'btn btn-primary' }, icon('eye', 16), c.start);
    b.addEventListener('click', () => startSheet((id) => { location.href = `/app/sessions/${id}`; }));
    return b;
  };
  document.addEventListener('dg:ready', () => {
    if (actions && canAct()) actions.replaceChildren(startButton());
    void load();
  }, { once: true });
}

// ---------- the session page ----------

function diffTable(f: Frame): HTMLElement | null {
  if (!f.diff || !f.diff.rows.length) return null;
  const cols = [...new Set(f.diff.rows.flatMap((r) => r.cells.map((x) => x.column)))].slice(0, 6);
  const cellText = (x: Cell | undefined, op: string) => {
    if (!x) return '';
    if (op === 'update') return `${x.before ?? 'NULL'} → ${x.after ?? 'NULL'}`;
    return (op === 'delete' ? x.before : x.after) ?? 'NULL';
  };
  return h('div', { class: 'obs-diff-scroll' }, h('table', { class: 'dt obs-diff' },
    h('caption', null, fill(c.diffCaption, { id: f.id, n: number(f.diff.rows.length) })),
    h('thead', null, h('tr', null, h('th', { scope: 'col' }, 'Op'), h('th', { scope: 'col' }, 'Key'), ...cols.map((col) => h('th', { scope: 'col' }, col)))),
    h('tbody', null, ...f.diff.rows.map((r) => h('tr', null,
      h('td', null, r.op),
      h('td', { class: 'mono', translate: 'no' }, Object.entries(r.key).map(([k, v]) => `${k}=${v}`).join(' ')),
      ...cols.map((col) => { const x = r.cells.find((y) => y.column === col); return h('td', { class: `mono${x?.changed ? ' obs-changed' : ''}`, translate: 'no' }, cellText(x, r.op)); }))))));
}

function frameRow(s: Session, f: Frame, onChange: () => void): HTMLElement {
  const tables = tableLines(f.tables).map(([t, ops]) => `${t} ${ops}`).join(' · ');
  const editable = canAct() && s.state === 'sealed' && !f.expired;
  let ctl: Child = null;
  if (f.expired) ctl = h('span', { class: 'chip chip-outline' }, c.expired);
  else if (editable) {
    const b = h('button', { type: 'button', class: 'btn btn-quiet btn-sm', 'aria-pressed': f.excluded ? 'true' : 'false' }, f.excluded ? c.putBack : c.leaveOut) as HTMLButtonElement;
    b.addEventListener('click', async () => {
      b.disabled = true;
      try {
        await api('POST', `/v1/observe/sessions/${s.id}/frames/${f.id}/exclude`, { body: { excluded: !f.excluded } });
        onChange();
      } catch (e) {
        toast((e as Error).message);
        b.disabled = false;
      }
    });
    ctl = b;
  } else if (f.excluded) ctl = h('span', { class: 'chip chip-outline' }, c.leftOut);
  return h('li', { class: `obs-frame${f.excluded ? ' obs-frame-out' : ''}` },
    h('div', { class: 'obs-frame-head' },
      f.commit_time ? relTime(f.commit_time, tz()) : h('span', { class: 't-2' }, '–'),
      h('span', { class: 'mono obs-frame-tables', translate: 'no' }, tables),
      h('span', { class: 'obs-frame-id' }, idChip(f.id)),
      ctl),
    diffTable(f));
}

export function mountSession(): void {
  const host = maybe('screen-body');
  const titleEl = maybe('page-title');
  if (!host) return;
  const id = location.pathname.split('/').filter(Boolean).pop() ?? '';
  const live = h('p', { class: 'sr-only', 'aria-live': 'polite' });
  let lastState = '';
  const load = async () => {
    try {
      const s = await api<Session>('GET', `/v1/observe/sessions/${id}`);
      let pv: Preview | null = null;
      if (s.state === 'sealed' || s.state === 'undone' || s.state === 'undoing') {
        pv = await api<Preview>('GET', `/v1/observe/sessions/${id}/undo_preview`).catch(() => null);
      }
      render(s, pv);
      if (lastState && lastState !== s.state) live.textContent = c.states[s.state] ?? s.state;
      lastState = s.state;
      if (s.state === 'open' || s.state === 'closing' || s.state === 'undoing') window.setTimeout(() => void load(), 3000);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) host.replaceChildren(notFound(e.message));
      else host.replaceChildren(errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  const render = (s: Session, pv: Preview | null) => {
    if (titleEl) titleEl.textContent = s.label ?? s.id;
    const counts = [`${number(s.summary.transactions)} ${s.summary.transactions === 1 ? 'transaction' : 'transactions'}`,
      ...tableLines(s.summary.tables).map(([t, ops]) => `${t} ${ops}`)].join(' · ');
    const head = h('div', { class: 'obs-head' },
      h('p', { class: 'obs-headline' }, h('span', { class: 'mono', translate: 'no' }, s.id), ' · ', windowText(s), ' · ',
        h('span', { class: 'mono', translate: 'no' }, s.connector.name), ' · ', stateChip(s.state)),
      h('p', { class: 'obs-counts num' }, counts),
      h('p', { class: 't-footnote t-2' }, c.observed));
    const closeBtn = s.state === 'open' && canAct() ? (() => {
      const b = h('button', { type: 'button', class: 'btn btn-secondary' }, c.close) as HTMLButtonElement;
      b.addEventListener('click', async () => { b.disabled = true; await api('POST', `/v1/observe/sessions/${s.id}/close`, { body: {} }).catch((e) => toast((e as Error).message)); void load(); });
      return b;
    })() : null;
    const frames = s.transactions?.data ?? [];
    const conflicts = pv && pv.conflicts > 0 ? h('p', { class: 'obs-conflict', role: 'alert' }, icon('circle-alert', 16),
      pv.conflicts === 1 ? c.conflictsOne : fill(c.conflicts, { n: number(pv.conflicts) })) : null;
    const undoArea = h('section', { class: 'card card-pad obs-undo', 'aria-labelledby': 'obs-undo-t' },
      h('h2', { class: 't-headline', id: 'obs-undo-t' }, c.undoTitle),
      pv ? h('p', null, pv.summary) : null,
      s.undo.action && s.state === 'undone' ? h('p', { class: 'obs-undone' }, icon('check', 16), fill(c.undoneBy, { action: s.undo.action })) : null,
      s.undo.action && s.state === 'undoing' ? h('p', null, fill(c.undoingBy, { action: s.undo.action })) : null,
      conflicts,
      pv && !pv.sql && pv.refusals.length ? h('p', { class: 'obs-conflict' }, fill(c.sqlRefused, { reason: pv.refusals[0]! })) : null,
      undoActions(s, pv));
    replace(host,
      live,
      head,
      closeBtn,
      s.state === 'closing' ? h('p', { class: 't-2', role: 'status' }, c.closing) : null,
      caughtBanner(s.caught, null, null),
      ...s.notes.map((n) => h('p', { class: 'obs-note' }, icon('info', 16), n)),
      h('section', { class: 'card card-pad', 'aria-labelledby': 'obs-tx-t' },
        h('h2', { class: 't-headline', id: 'obs-tx-t' }, c.transactions),
        frames.length ? h('ul', { class: 'obs-frames', 'aria-label': c.transactionsCaption }, ...frames.map((f) => frameRow(s, f, () => void load())))
          : h('p', { class: 'card-empty t-2' }, c.noTransactions)),
      h('p', { class: 'obs-record' }, icon('deed', 16), s.seal ? fill(c.record, { deed: s.seal.deed }) : c.recordPending),
      s.state === 'open' || s.state === 'closing' ? null : undoArea);
  };
  document.addEventListener('dg:ready', () => {
    host.replaceChildren(skeleton());
    void load();
  }, { once: true });
}
