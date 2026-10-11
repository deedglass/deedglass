// screens.ts: the nine stubs (S24 step 12). Each shows its title, a skeleton in the final table's
// shape while loading, then the first page of its list (S25 and S26 build the full screens), the
// empty state that teaches, an error state with the request id and Retry, or the
// permission state. Never "coming soon".

import { api, ApiError } from '../lib/api.ts';
import { decisionChip, idChip, relTime, stateChip, statusChip, verdictChip, type Decision } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { h, icon, maybe, type Child } from '../lib/dom.ts';
import { number } from '../lib/format.ts';
import { get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { Action, Agent, Approval, ListResponse } from '../lib/types.ts';
import { toast } from './toast.ts';
import { listKeys, wireTable } from './table.ts';

type Row = Record<string, unknown>;

interface Column {
  label: string;
  num?: boolean;
  card?: 'hide' | 'label'; // under 640 px: hidden, or shown with its label
  cell: (r: Row, tz: string) => Child;
  sort?: (r: Row) => string | number;
}

interface Screen {
  path: `/v1/${string}`;
  query?: Record<string, string>;
  columns: Column[];
  href: (r: Row) => string | null;
  empty: string;
  emptyIcon: string;
  emptyAction?: { label: string; href: string; adminOnly?: boolean };
  docs?: string;
  live?: string; // an event pattern that refreshes the list
}

const s = (v: unknown): string => (typeof v === 'string' ? v : '');
const mono = (v: string) => h('span', { class: 'mono', translate: 'no' }, v);

const decisionOf = (a: Action): Decision | null => {
  const d = a.decision?.outcome ?? a.policy.decision;
  return d === 'allow' || d === 'deny' || d === 'require_approval' ? d : null;
};

export const SCREENS: Record<string, Screen> = {
  actions: {
    path: '/v1/actions',
    query: { limit: '25' },
    live: 'action.*',
    columns: [
      { label: 'Tool', cell: (r) => mono(s(r.tool)), sort: (r) => s(r.tool) },
      { label: 'Agent', cell: (r) => s((r as unknown as Action).agent?.name), sort: (r) => s((r as unknown as Action).agent?.name) },
      { label: 'Decision', card: 'hide', cell: (r) => { const d = decisionOf(r as unknown as Action); return d ? decisionChip(d) : stateChip(s(r.state)); } },
      { label: 'Verdict', cell: (r) => { const w = (r as unknown as Action).witness; return w ? verdictChip(w.verdict, w.level) : stateChip(s(r.state)); } },
      { label: 'Action', card: 'hide', cell: (r) => idChip(s(r.id)) },
      { label: 'When', num: true, cell: (r, tz) => relTime(s(r.created_at), tz), sort: (r) => s(r.created_at) },
    ],
    href: (r) => `/app/actions/${s(r.id)}`,
    empty: copy.pages.actions.empty,
    emptyIcon: 'activity',
    docs: 'https://docs.deedglass.com/gate',
  },
  approvals: {
    path: '/v1/approvals',
    query: { status: 'pending', limit: '25' },
    live: 'approval.*',
    columns: [
      { label: 'Tool', cell: (r) => mono(s(r.tool)), sort: (r) => s(r.tool) },
      { label: 'Agent', cell: (r) => s((r as unknown as Approval).agent?.name) },
      { label: 'Rule', card: 'hide', cell: (r) => mono(s(r.rule_id) || '·') },
      { label: 'Waiting for', cell: () => h('span', { class: 'chip chip-pending' }, icon('contrast', 16), copy.chips.pending) },
      { label: 'Expires', num: true, cell: (r, tz) => relTime(s(r.expires_at), tz), sort: (r) => s(r.expires_at) },
    ],
    href: (r) => `/app/approvals/${s(r.id)}`,
    empty: copy.pages.approvals.empty,
    emptyIcon: 'stamp',
    docs: 'https://docs.deedglass.com/approvals',
  },
  agents: {
    path: '/v1/agents',
    query: { limit: '50' },
    live: 'agent.*',
    columns: [
      { label: 'Name', cell: (r) => h('span', { class: 'font-medium' }, s(r.name)), sort: (r) => s(r.name) },
      { label: 'Status', cell: (r) => statusChip(((r as unknown as Agent).status ?? 'active') as 'active') },
      { label: 'Keys', num: true, cell: (r) => number(Number(r.active_keys ?? 0)), sort: (r) => Number(r.active_keys ?? 0) },
      { label: 'Agent', card: 'hide', cell: (r) => idChip(s(r.id)) },
      { label: 'Last call', num: true, cell: (r, tz) => (r.last_used_at ? relTime(s(r.last_used_at), tz) : h('span', { class: 't-2' }, 'Never')), sort: (r) => s(r.last_used_at) },
    ],
    href: (r) => `/app/agents/${s(r.id)}`,
    empty: copy.agents.empty,
    emptyIcon: 'bot',
    emptyAction: { label: copy.agents.create, href: '/app/agents?new=1', adminOnly: true },
    docs: 'https://docs.deedglass.com/quickstart',
  },
  policies: {
    path: '/v1/policies',
    query: { limit: '50' },
    live: 'policy.*',
    columns: [
      { label: 'Policy', cell: (r) => mono(s(r.name)), sort: (r) => s(r.name) },
      { label: 'Mode', cell: (r) => h('span', { class: `chip ${s(r.status) === 'enforced' ? 'chip-neutral' : 'chip-outline'}` }, s(r.status) || 'inactive') },
      { label: 'Active', num: true, cell: (r) => (r.active_version ? `v${String(r.active_version)}` : '·'), sort: (r) => Number(r.active_version ?? 0) },
      { label: 'Latest', num: true, card: 'hide', cell: (r) => `v${String(r.latest_version ?? 1)}` },
      { label: 'Updated', num: true, cell: (r, tz) => relTime(s(r.updated_at), tz), sort: (r) => s(r.updated_at) },
    ],
    href: (r) => `/app/policies/${s(r.id)}`,
    empty: copy.pages.policies.empty,
    emptyIcon: 'scroll-text',
    docs: 'https://docs.deedglass.com/policies',
  },
  connectors: {
    path: '/v1/connectors',
    query: { limit: '50' },
    live: 'decoder.*',
    columns: [
      { label: 'Connector', cell: (r) => mono(s(r.name)), sort: (r) => s(r.name) },
      { label: 'Type', cell: (r) => s(r.type) },
      { label: 'Status', cell: (r) => statusChip(s(r.status) === 'active' ? 'active' : 'suspended') },
      { label: 'Tools', num: true, card: 'hide', cell: (r) => number(Array.isArray(r.tools) ? r.tools.length : 0) },
      { label: 'Updated', num: true, cell: (r, tz) => relTime(s(r.updated_at), tz), sort: (r) => s(r.updated_at) },
    ],
    href: (r) => `/app/connectors/${s(r.id)}`,
    empty: copy.connectors.empty,
    emptyIcon: 'plug',
    docs: 'https://docs.deedglass.com/connectors',
  },
  deeds: {
    path: '/v1/deeds',
    query: { limit: '25' },
    live: 'deed.sealed',
    columns: [
      { label: 'Seq', num: true, cell: (r) => number(Number((r.body as Row | undefined)?.seq ?? 0)), sort: (r) => Number((r.body as Row | undefined)?.seq ?? 0) },
      { label: 'Kind', cell: (r) => s((r.body as Row | undefined)?.kind) },
      { label: 'Record', card: 'hide', cell: (r) => idChip(s(r.id)) },
      { label: 'Sealed', num: true, cell: (r, tz) => relTime(s((r.body as Row | undefined)?.issued_at), tz), sort: (r) => s((r.body as Row | undefined)?.issued_at) },
    ],
    href: (r) => `/app/deeds/${s(r.id)}`,
    empty: copy.pages.deeds.empty,
    emptyIcon: 'deed',
    docs: 'https://docs.deedglass.com/deeds',
  },
};

// ---------- the four states ----------

export function skeleton(rows = 6): HTMLElement {
  const frame = h('div', { class: 'table-frame', 'aria-hidden': 'true' });
  for (let i = 0; i < rows; i++) {
    frame.append(h('div', { class: 'skel-row' }, ...[0.9, 0.6, 0.5, 0.35, 0.45].map(() => h('span', { class: 'skel skel-line' }))));
  }
  return frame;
}

export function emptyState(text: string, ic: string, action?: Child, docs?: string): HTMLElement {
  const [first, ...rest] = text.split(/(?<=\.)\s+/);
  return h('div', { class: 'state', role: 'status' },
    h('span', { class: 'well' }, icon(ic, 20)),
    h('p', { class: 't-headline' }, first ?? text),
    rest.length ? h('p', null, rest.join(' ')) : null,
    action || docs ? h('div', { class: 'actions' }, action ?? null, docs ? h('a', { class: 'btn btn-quiet', href: docs }, 'Read the docs', icon('arrow-up-right', 16)) : null) : null);
}

export function errorState(err: unknown, retry: () => void): HTMLElement {
  const e = err as ApiError;
  const req = e?.requestId || 'unknown';
  return h('div', { class: 'state state-error', role: 'alert' },
    h('span', { class: 'well' }, icon('circle-alert', 20)),
    h('p', { class: 't-headline' }, copy.states.errorTitle),
    h('p', null, e?.message ?? copy.toast.networkError),
    h('p', { class: 't-footnote' }, copy.states.errorBody),
    h('button', { type: 'button', class: 'idchip', 'data-copy': req, translate: 'no', 'aria-label': `Copy ${req}` }, req, icon('copy', 16)),
    h('div', { class: 'actions' }, h('button', { type: 'button', class: 'btn btn-secondary', onclick: retry }, icon('refresh-cw', 16), copy.states.retry)));
}

export function permissionState(role: string): HTMLElement {
  const st = get();
  return h('div', { class: 'state', role: 'status' },
    h('span', { class: 'well' }, icon('lock', 20)),
    h('p', { class: 't-headline' }, copy.states.permissionTitle),
    h('p', null, fill(copy.states.permissionBody, { role, org: st?.org.name ?? 'this org' })));
}

/** notFound: the Console 404, identical for a missing id and another org's id (S24). */
export function notFound(message: string): HTMLElement {
  return h('div', { class: 'state', role: 'status' },
    h('span', { class: 'well' }, icon('search', 20)),
    h('p', { class: 't-headline' }, copy.pages.notFound.title),
    h('p', null, message),
    h('div', { class: 'actions' }, h('a', { class: 'btn btn-secondary', href: '/app' }, copy.pages.notFound.action)));
}

// ---------- the list ----------

function table(sc: Screen, rows: Row[], tz: string): HTMLElement {
  const t = h('table', { class: 'dt cards' },
    h('thead', null, h('tr', null, ...sc.columns.map((c) => h('th', { scope: 'col', class: c.num ? 'num' : '' }, c.sort ? h('button', { type: 'button' }, c.label) : c.label)))),
    h('tbody', null, ...rows.map((r) => {
      const href = sc.href(r);
      const tr = h('tr', { 'data-row': '', 'aria-selected': 'false' }, ...sc.columns.map((c, i) => {
        const cell = c.cell(r, tz);
        const content = i === 0 && href ? h('a', { href, class: 'rowlink', translate: 'no' }, cell) : cell;
        const sortv = c.sort ? String(c.sort(r)) : null;
        return h('td', { class: c.num ? 'num' : '', 'data-label': c.label, ...(c.card ? { 'data-card': c.card } : {}), ...(sortv !== null ? { 'data-sort': sortv } : {}) }, content);
      }));
      return tr;
    })));
  wireTable(t as HTMLTableElement);
  return h('div', { class: 'table-frame' }, t);
}

export function mountScreen(key: string): void {
  const sc = SCREENS[key];
  const host = maybe('screen-body');
  if (!host) return;
  if (!sc) return;
  listKeys();
  let fetching = false;
  const load = async (quiet = false) => {
    if (fetching) return;
    fetching = true;
    if (!quiet) {
      host.setAttribute('aria-busy', 'true');
      host.replaceChildren(skeleton());
    }
    try {
      const page = await api<ListResponse<Row>>('GET', sc.path, { query: sc.query });
      const tz = get()?.org.timezone ?? 'UTC';
      if (page.data.length === 0) {
        const st = get();
        const canAct = !sc.emptyAction?.adminOnly || (st && st.me.membership.role !== 'viewer');
        host.replaceChildren(emptyState(sc.empty, sc.emptyIcon, sc.emptyAction && canAct ? h('a', { class: 'btn btn-primary', href: sc.emptyAction.href }, icon('plus', 16), sc.emptyAction.label) : undefined, sc.docs));
      } else {
        const more = page.has_more ? h('p', { class: 't-footnote t-2 mt-3' }, `Showing the first ${number(page.data.length)}.`) : null;
        host.replaceChildren(table(sc, page.data, tz), more ?? '');
      }
    } catch (e) {
      if (e instanceof ApiError && (e.code === 'insufficient_role' || e.status === 403)) host.replaceChildren(permissionState('Admin'));
      else if (!(e instanceof ApiError && e.status === 401)) host.replaceChildren(errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
      fetching = false;
    }
  };
  document.addEventListener('dg:ready', () => {
    void load();
    if (sc.live) {
      let t = 0;
      const live = stream();
      live.on(sc.live, () => {
        clearTimeout(t);
        t = window.setTimeout(() => void load(true), 300);
      });
      live.onResync(() => {
        void load(true);
        toast(copy.states.resync);
      });
    }
  }, { once: true });
  document.addEventListener('dg:load-failed', (e) => host.replaceChildren(errorState((e as CustomEvent).detail, () => location.reload())), { once: true });
}
