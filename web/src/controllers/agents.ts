// agents.ts: the Agents list and an agent's page (S25 §3.7). The list reads usage through
// GET /v1/agents?expand[]=usage; the detail has tabs Overview · Keys · Budgets · Security · Danger
// zone, synced to ?tab=. Every change goes through the API (which enforces the role); the Console
// only hides or disables what a viewer can't do, with the reason. S08's copy is lifted verbatim.

import { api, ApiError } from '../lib/api.ts';
import { idChip, relTime, statusChip } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { money, number, plural, shortId } from '../lib/format.ts';
import { atLeast, get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { Action, Agent, AgentBudgets, ApiKey, IntentKey, ListResponse, Overview } from '../lib/types.ts';
import { feedRow } from './actionrow.ts';
import { metricTile, segmented, usageMeter, verificationBar, visibleControls } from './components.ts';
import { segments } from '../lib/overview.ts';
import { showKeySheet } from './keysheet.ts';
import { confirmTyped, openSheet } from './overlay.ts';
import { emptyState, errorState, notFound, permissionState, skeleton } from './screens.ts';
import { listKeys, wireTable } from './table.ts';
import { apiErrorToast, toast } from './toast.ts';

const NAME_RE = /^[a-z0-9][a-z0-9-]{1,62}$/;
const NAME_MSG = 'Agent names use lowercase letters, digits and hyphens, 2 to 63 characters, like support-bot.'; // S08, the API's message
const KEY_RE = /^ed25519:[A-Za-z0-9_-]{43}$/;
const tzOf = () => get()?.org.timezone ?? 'UTC';
const isAdmin = () => atLeast(get()?.me.membership.role, 'admin');

export function labelChips(labels: Record<string, string> | undefined): HTMLElement {
  return h('span', { class: 'labels' }, ...Object.entries(labels ?? {}).slice(0, 4).map(([k, v]) => h('span', { class: 'label-chip', translate: 'no' }, visibleControls(`${k}:${v}`))));
}

/** listAgents reads the list with usage; a server without the expansion still answers without it. */
async function listAgents(): Promise<ListResponse<Agent>> {
  try {
    return await api<ListResponse<Agent>>('GET', '/v1/agents', { query: { limit: '100', 'expand[]': 'usage' } });
  } catch (e) {
    if (e instanceof ApiError && e.status === 400) return api<ListResponse<Agent>>('GET', '/v1/agents', { query: { limit: '100' } });
    throw e;
  }
}

function meterFor(a: Agent): Child {
  const used = a.usage?.calls_today ?? 0;
  const limit = a.budgets?.calls_per_day;
  if (!a.usage) return h('span', { class: 't-2' }, '·');
  const at = limit && used >= limit ? fill(copy.agentsPage.meterAt, { used: number(used), limit: number(limit), tz: tzOf() }) : undefined;
  return usageMeter(used, limit ?? null, `${a.name}, calls today`, at);
}

// ---------- create ----------

function parseLabels(s: string): Record<string, string> | string {
  const out: Record<string, string> = {};
  for (const part of s.split(',').map((x) => x.trim()).filter(Boolean)) {
    const i = part.indexOf(':');
    if (i < 1) return copy.agentsPage.labelsHint;
    out[part.slice(0, i).trim()] = part.slice(i + 1).trim();
  }
  return out;
}

function intOrNull(v: string): number | null {
  const t = v.trim();
  if (!t) return null;
  const n = Number(t);
  return Number.isInteger(n) && n >= 0 ? n : NaN;
}

function budgetsFrom(perMin: string, perDay: string, spend: string, currency: string): AgentBudgets | string {
  const b: AgentBudgets = {};
  const m = intOrNull(perMin);
  const d = intOrNull(perDay);
  if (Number.isNaN(m) || Number.isNaN(d)) return 'Budgets are whole numbers, or empty for no limit.';
  if (m !== null) b.calls_per_minute = m;
  if (d !== null) b.calls_per_day = d;
  if (spend.trim()) {
    const v = Number(spend);
    if (!Number.isFinite(v) || v < 0) return 'Spend per day is an amount like 120.00.';
    b.spend_per_day = { amount: Math.round(v * 100), currency: currency || 'usd' };
  }
  return b;
}

function field(id: string, label: string, input: HTMLElement, help?: string): HTMLElement {
  const err = h('p', { class: 'error', id: `${id}-e`, hidden: true });
  input.setAttribute('aria-describedby', `${id}-h ${id}-e`);
  return h('div', { class: 'field', 'data-field': id }, h('label', { class: 'label', for: id }, label), input, help ? h('p', { class: 'help', id: `${id}-h` }, help) : null, err);
}

function showError(root: HTMLElement, id: string, msg: string | null): void {
  const e = root.querySelector<HTMLElement>(`#${id}-e`);
  const i = root.querySelector<HTMLElement>(`#${id}`);
  if (!e || !i) return;
  e.hidden = !msg;
  replace(e, ...(msg ? [icon('circle-alert', 16), msg] : []));
  i.setAttribute('aria-invalid', msg ? 'true' : 'false');
}

export function openCreateAgent(after: (a: Agent) => void): void {
  const name = h('input', { class: 'input mono', id: 'ag-name', name: 'name', autocomplete: 'off', spellcheck: 'false', maxlength: 63, required: true });
  const desc = h('input', { class: 'input', id: 'ag-desc', name: 'description', autocomplete: 'off', maxlength: 280 });
  const labels = h('input', { class: 'input mono', id: 'ag-labels', name: 'labels', autocomplete: 'off', spellcheck: 'false', placeholder: 'env:prod, team:support…' });
  const perMin = h('input', { class: 'input num', id: 'ag-min', name: 'calls_per_minute', inputmode: 'numeric', autocomplete: 'off', placeholder: copy.agentsPage.budgetsNone });
  const perDay = h('input', { class: 'input num', id: 'ag-day', name: 'calls_per_day', inputmode: 'numeric', autocomplete: 'off', placeholder: copy.agentsPage.budgetsNone });
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, copy.agentsPage.create);
  const sheet = openSheet({
    title: copy.agentsPage.createTitle,
    body: [
      field('ag-name', copy.agentsPage.name, name, copy.agentsPage.nameHint),
      field('ag-desc', copy.agentsPage.description, desc),
      field('ag-labels', copy.agentsPage.labels, labels, copy.agentsPage.labelsHint),
      h('div', { class: 'field-row' }, field('ag-min', copy.agentsPage.callsPerMinute, perMin), field('ag-day', copy.agentsPage.callsPerDay, perDay)),
    ],
    foot: [h('button', { type: 'button', class: 'btn btn-secondary', 'data-cancel': '' }, copy.stop.cancel), go],
    initialFocus: () => name,
  });
  sheet.el.querySelector('[data-cancel]')?.addEventListener('click', () => sheet.close());
  name.addEventListener('input', () => showError(sheet.el, 'ag-name', name.value && !NAME_RE.test(name.value) ? NAME_MSG : null));
  go.addEventListener('click', async () => {
    if (!NAME_RE.test(name.value)) return showError(sheet.el, 'ag-name', NAME_MSG);
    const lab = parseLabels(labels.value);
    if (typeof lab === 'string') return showError(sheet.el, 'ag-labels', lab);
    const budgets = budgetsFrom(perMin.value, perDay.value, '', 'usd');
    if (typeof budgets === 'string') return showError(sheet.el, 'ag-day', budgets);
    go.disabled = true;
    try {
      const a = await api<Agent>('POST', '/v1/agents', { body: { name: name.value, description: desc.value.trim() || undefined, labels: lab, budgets } });
      sheet.close();
      toast(fill(copy.agentsPage.created, { name: a.name }), { kind: 'success', action: { label: copy.agentsPage.createKey, href: `/app/agents/${a.id}?tab=keys&new=1` } });
      after(a);
    } catch (e) {
      go.disabled = false;
      if (e instanceof ApiError && e.param) {
        const map: Record<string, string> = { name: 'ag-name', description: 'ag-desc', labels: 'ag-labels' };
        const target = map[e.param.split('.')[0] ?? ''] ?? (e.param.startsWith('budgets') ? 'ag-day' : '');
        if (target) return showError(sheet.el, target, e.message);
      }
      apiErrorToast(e);
    }
  });
}

// ---------- the list ----------

export function mountAgents(): void {
  const host = maybe('screen-body');
  const head = maybe('agents-actions');
  if (!host) return;
  const load = async (quiet = false) => {
    if (!quiet) {
      host.setAttribute('aria-busy', 'true');
      host.replaceChildren(skeleton(8));
    }
    try {
      const page = await listAgents();
      const tz = tzOf();
      const rows = page.data.filter((a) => a.name !== 'console' || (a as { kind?: string }).kind !== 'system');
      if (rows.length === 0) {
        host.replaceChildren(emptyState(copy.agents.empty, 'bot', isAdmin() ? h('button', { type: 'button', class: 'btn btn-primary', onclick: () => openCreateAgent(() => void load()) }, icon('plus', 16), copy.agents.create) : undefined, 'https://docs.deedglass.com/quickstart'));
        return;
      }
      const t = h('table', { class: 'dt cards agents' },
        h('thead', null, h('tr', null, ...(['name', 'status', 'labels', 'calls', 'spend', 'last', 'keys'] as const).map((k) =>
          h('th', { scope: 'col', class: k === 'keys' || k === 'spend' ? 'num' : '' }, k === 'name' || k === 'last' ? h('button', { type: 'button' }, copy.agentsPage.columns[k]) : copy.agentsPage.columns[k])))),
        h('tbody', null, ...rows.map((a) => h('tr', { 'data-row': '', 'data-id': a.id, 'aria-selected': 'false' },
          h('td', { 'data-sort': a.name }, h('a', { href: `/app/agents/${a.id}`, class: 'rowlink', translate: 'no' }, h('span', { class: 'font-medium' }, visibleControls(a.name))), a.description ? h('span', { class: 'agent-desc' }, visibleControls(a.description)) : null),
          h('td', null, statusChip(a.status)),
          h('td', { 'data-card': 'hide' }, labelChips(a.labels)),
          h('td', null, meterFor(a)),
          h('td', { class: 'num', 'data-card': 'hide' }, a.usage?.spend_today ? money(a.usage.spend_today.amount, a.usage.spend_today.currency) : h('span', { class: 't-2' }, '·')),
          h('td', { 'data-sort': a.last_action_at ?? a.last_used_at ?? '' }, a.last_action_at ?? a.last_used_at ? relTime((a.last_action_at ?? a.last_used_at)!, tz) : h('span', { class: 't-2' }, copy.agentsPage.never)),
          h('td', { class: 'num', 'data-card': 'hide' }, number(a.active_keys))))));
      wireTable(t);
      host.replaceChildren(h('div', { class: 'table-frame' }, t), page.has_more ? h('p', { class: 't-footnote t-2 mt-3' }, `Showing the first ${number(rows.length)}.`) : '');
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) host.replaceChildren(permissionState('Viewer'));
      else if (!(e instanceof ApiError && e.status === 401)) host.replaceChildren(errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  listKeys();
  document.addEventListener('dg:ready', () => {
    if (head) {
      const btn = h('button', { type: 'button', class: 'btn btn-primary btn-sm', ...(isAdmin() ? {} : { disabled: true, title: copy.agentsPage.adminOnly, 'aria-describedby': 'ag-why' }) }, icon('plus', 16), copy.agents.create);
      btn.addEventListener('click', () => openCreateAgent(() => void load(true)));
      head.replaceChildren(btn, isAdmin() ? '' : h('span', { class: 'sr-only', id: 'ag-why' }, copy.agentsPage.adminOnly));
      if (new URLSearchParams(location.search).get('new') === '1' && isAdmin()) openCreateAgent(() => void load(true));
    }
    void load();
    let t = 0;
    stream().on('agent.*', () => {
      clearTimeout(t);
      t = window.setTimeout(() => void load(true), 300);
    });
  }, { once: true });
}

// ---------- the detail ----------

type Tab = 'overview' | 'keys' | 'budgets' | 'security' | 'danger';
const TABS: Tab[] = ['overview', 'keys', 'budgets', 'security', 'danger'];

export function mountAgent(): void {
  const id = decodeURIComponent(location.pathname.split('/').pop() ?? '');
  const host = maybe('detail-body');
  if (!host) return;
  let agent: Agent | null = null;
  let tab: Tab = (TABS as string[]).includes(new URLSearchParams(location.search).get('tab') ?? '') ? (new URLSearchParams(location.search).get('tab') as Tab) : 'overview';
  const panel = h('div', { class: 'tabpanel', role: 'tabpanel', id: 'agent-panel', tabindex: 0 });
  const tabsEl = h('div', { class: 'tabs', role: 'tablist', 'aria-label': 'Agent' });

  const header = (a: Agent) => h('header', { class: 'detail-head' },
    h('nav', { class: 'crumbs', 'aria-label': 'Breadcrumb' }, h('a', { href: '/app/agents' }, copy.nav.agents), h('span', { 'aria-hidden': 'true' }, '/'), idChip(a.id)),
    h('div', { class: 'agent-head' }, h('h1', { class: 't-title-2 mono', id: 'page-title', translate: 'no' }, visibleControls(a.name)), h('span', { 'data-agent-status': '' }, statusChip(a.status))),
    a.description ? h('p', { class: 'detail-meta' }, visibleControls(a.description)) : null,
    labelChips(a.labels));

  const renderTabs = () => {
    replace(tabsEl, ...TABS.map((k, i) => {
      const b = h('button', { type: 'button', role: 'tab', id: `tab-${k}`, 'aria-controls': 'agent-panel', 'aria-selected': k === tab ? 'true' : 'false', tabindex: k === tab ? 0 : -1, 'data-section': k }, copy.agentsPage.tabs[k] ?? k);
      b.addEventListener('click', () => select(k));
      b.addEventListener('keydown', (e) => {
        const n = e.key === 'ArrowRight' ? i + 1 : e.key === 'ArrowLeft' ? i - 1 : -9;
        if (n === -9) return;
        e.preventDefault();
        select(TABS[(n + TABS.length) % TABS.length]!, true);
      });
      return b;
    }));
  };
  const select = (k: Tab, focus = false) => {
    tab = k;
    const u = new URL(location.href);
    if (k === 'overview') u.searchParams.delete('tab');
    else u.searchParams.set('tab', k);
    u.searchParams.delete('new');
    history.replaceState(null, '', u.pathname + u.search);
    renderTabs();
    panel.setAttribute('aria-labelledby', `tab-${k}`);
    if (focus) tabsEl.querySelector<HTMLElement>(`#tab-${k}`)?.focus();
    void renderPanel();
  };

  const renderPanel = async () => {
    if (!agent) return;
    const a = agent;
    replace(panel, skeleton(4));
    try {
      if (tab === 'overview') await overviewTab(a, panel);
      else if (tab === 'keys') await keysTab(a, panel);
      else if (tab === 'budgets') budgetsTab(a, panel, (next) => (agent = next));
      else if (tab === 'security') await securityTab(a, panel, (next) => (agent = next));
      else dangerTab(a, panel, (next) => {
        agent = next;
        host.querySelector('[data-agent-status]')?.replaceChildren(statusChip(next.status));
      });
    } catch (e) {
      replace(panel, e instanceof ApiError && e.status === 403 ? permissionState('Admin') : errorState(e, () => void renderPanel()));
    }
  };

  const load = async () => {
    try {
      try {
        agent = await api<Agent>('GET', `/v1/agents/${encodeURIComponent(id)}`, { query: { 'expand[]': 'usage' } });
      } catch (e) {
        if (!(e instanceof ApiError && e.status === 400)) throw e;
        agent = await api<Agent>('GET', `/v1/agents/${encodeURIComponent(id)}`);
      }
      document.title = `${agent.name} · Agents · Deedglass`;
      renderTabs();
      host.replaceChildren(header(agent), tabsEl, panel);
      panel.setAttribute('aria-labelledby', `tab-${tab}`);
      await renderPanel();
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) {
        host.replaceChildren(notFound(e.message));
      } else if (!(e instanceof ApiError && e.status === 401)) host.replaceChildren(errorState(e, () => void load()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  document.addEventListener('dg:ready', () => {
    void load();
    stream().on('agent.*', (ev) => {
      const o = ev.data.object as { id?: string; status?: Agent['status'] };
      if (o.id !== id || !agent) return;
      void api<Agent>('GET', `/v1/agents/${encodeURIComponent(id)}`).then((a) => {
        agent = { ...agent!, ...a };
        host.querySelector('[data-agent-status]')?.replaceChildren(statusChip(a.status));
        if (tab === 'danger') void renderPanel();
      }).catch(() => undefined);
    });
  }, { once: true });
}

async function overviewTab(a: Agent, panel: HTMLElement): Promise<void> {
  const since = new Date(Date.now() - 86_400_000).toISOString();
  const page = await api<ListResponse<Action>>('GET', '/v1/actions', { query: { agent: a.id, 'created_at[gte]': since, limit: '100' } });
  const acts = page.data;
  const span = page.has_more ? `the last ${number(acts.length)} actions` : 'last 24 hours';
  const blocked = acts.filter((x) => (x.decision?.outcome ?? x.policy?.decision) === 'deny').length;
  const v = { verified: 0, mismatch: 0, unverifiable: 0 };
  for (const x of acts) if (x.witness) v[x.witness.verdict.toLowerCase() as keyof typeof v]++;
  const notW = acts.length - v.verified - v.mismatch - v.unverifiable;
  const calls = a.usage?.calls_today;
  const tz = get()?.org.timezone ?? 'UTC';
  const segs = segments({ verification: { ...v, not_witnessed: notW, by_level: { L0: 0, L1: 0, L2: 0 } }, from: since, window: '24h' } as unknown as Overview)
    .map((g) => ({ ...g, href: g.href.replace('/app/actions?', `/app/actions?agent=${a.id}&`) }));
  const mix = h('section', { class: 'tile', 'aria-labelledby': 'at-mix-l' }, h('h2', { class: 'tile-label', id: 'at-mix-l' }, copy.agentsPage.verdictMix),
    acts.length ? verificationBar(segs, copy.agentsPage.verdictMix) : h('p', { class: 'tile-sub' }, copy.overview.verificationNone), h('p', { class: 'tile-sub levels' }, span));
  const tiles = h('div', { class: 'tiles tab-tiles' },
    metricTile({ id: 'at-calls', label: copy.agentsPage.callsToday, value: calls === undefined ? '·' : number(calls), sub: a.budgets?.calls_per_day ? `of ${number(a.budgets.calls_per_day)} · today in ${tz}` : `today in ${tz}` }),
    metricTile({ id: 'at-blocked', label: copy.agentsPage.blocked, value: number(blocked), sub: span, href: `/app/actions?agent=${a.id}&decision=deny&time=24h` }),
    mix);
  const recent = acts.slice(0, 20);
  const table = h('table', { class: 'dt feed cards' },
    h('thead', null, h('tr', null, ...(['time', 'agent', 'tool', 'decision', 'verdict', 'latency', 'obo'] as const).map((k) => h('th', { scope: 'col', class: `c-${k}${k === 'latency' ? ' num' : ''}` }, copy.feed.columns[k])))),
    h('tbody', null, ...recent.map((x) => feedRow(x, tz))));
  replace(panel, tiles, h('h2', { class: 't-headline section-title' }, copy.agentsPage.lastActions),
    recent.length ? h('div', { class: 'table-frame' }, table) : emptyState(copy.feed.empty, 'activity'),
    recent.length ? h('a', { class: 'card-link mt-3', href: `/app/actions?agent=${a.id}` }, copy.overview.liveOpen, icon('chevron-right', 16)) : null);
}

async function keysTab(a: Agent, panel: HTMLElement): Promise<void> {
  const tz = get()?.org.timezone ?? 'UTC';
  const admin = isAdmin();
  const page = await api<ListResponse<ApiKey>>('GET', `/v1/agents/${a.id}/keys`);
  const create = h('button', { type: 'button', class: 'btn btn-primary btn-sm', ...(admin ? {} : { disabled: true, title: copy.agents.noPermission }) }, icon('plus', 16), copy.agentsPage.newKey);
  const reload = () => void keysTab(a, panel);
  create.addEventListener('click', () => openCreateKey(a, reload));
  const rows = page.data.map((k) => {
    const status = k.revoked_at ? copy.agentsPage.keyRevoked : k.expires_at ? copy.agentsPage.keyExpiring : copy.agentsPage.keyActive;
    const roll = h('button', { type: 'button', class: 'btn btn-quiet btn-sm', ...(admin && !k.revoked_at ? {} : { disabled: true }) }, copy.agentsPage.roll);
    const revoke = h('button', { type: 'button', class: 'btn btn-quiet btn-sm', ...(admin && !k.revoked_at ? {} : { disabled: true }) }, copy.agentsPage.revoke);
    roll.addEventListener('click', () => openRoll(k, reload));
    revoke.addEventListener('click', () => {
      confirmTyped({ title: fill(copy.agentsPage.revokeTitle, { name: k.name }), body: copy.agentsPage.revokeBody, confirmLabel: copy.agentsPage.revokeButton, danger: true,
        onConfirm: async () => {
          await api('POST', `/v1/keys/${k.id}/revoke`);
          toast(fill(copy.agentsPage.revoked, { name: k.name }), { kind: 'success' });
          reload();
        } });
    });
    return h('tr', { 'data-row': '', 'aria-selected': 'false' },
      h('td', null, h('span', { class: 'font-medium' }, visibleControls(k.name))),
      h('td', { class: 'mono' }, `${k.prefix}…`),
      h('td', null, h('span', { class: `status ${k.mode === 'live' ? 'status-active' : 'status-test'}` }, icon('key-round', 16), k.mode === 'live' ? copy.agentsPage.keyLive : copy.agentsPage.keyTest)),
      h('td', { class: 'mono t-2', 'data-card': 'hide' }, k.scopes.join(' ')),
      h('td', { class: 'num' }, k.last_used_at ? relTime(k.last_used_at, tz) : h('span', { class: 't-2' }, copy.agentsPage.never)),
      h('td', { class: 'num', 'data-card': 'hide' }, k.expires_at ? relTime(k.expires_at, tz) : h('span', { class: 't-2' }, '·')),
      h('td', null, k.revoked_at ? h('span', { class: 'status status-stopped' }, icon('x', 16), status) : h('span', { class: 'status status-active' }, icon('check', 16), status)),
      h('td', { class: 'num', 'data-card': 'hide' }, h('span', { class: 'key-row-actions' }, roll, revoke)));
  });
  const cols = ['name', 'prefix', 'mode', 'scopes', 'used', 'expires', 'status'] as const;
  replace(panel, h('div', { class: 'section-head mb-3' }, h('p', { class: 't-2 m-0' }, plural(page.data.filter((k) => !k.revoked_at).length, 'active key')), create),
    page.data.length ? h('div', { class: 'table-frame' }, h('table', { class: 'dt cards' },
      h('thead', null, h('tr', null, ...cols.map((c) => h('th', { scope: 'col', class: c === 'used' || c === 'expires' ? 'num' : '' }, copy.agentsPage.keysColumns[c])), h('th', { scope: 'col' }, h('span', { class: 'sr-only' }, 'Actions')))),
      h('tbody', null, ...rows))) : emptyState(copy.agentsPage.keysEmpty, 'key-round'));
  if (new URLSearchParams(location.search).get('new') === '1' && admin) {
    history.replaceState(null, '', `${location.pathname}?tab=keys`);
    openCreateKey(a, reload);
  }
}

function openCreateKey(a: Agent, done: () => void): void {
  const name = h('input', { class: 'input', id: 'k-name', name: 'name', value: 'production', autocomplete: 'off', maxlength: 64 });
  let mode: 'live' | 'test' = 'test';
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, copy.agentsPage.newKey);
  const sheet = openSheet({
    title: `${copy.agentsPage.newKey} · ${a.name}`,
    body: [field('k-name', copy.agentsPage.keyName, name),
      h('div', { class: 'field' }, h('span', { class: 'label', id: 'k-mode-l' }, copy.agentsPage.keyMode),
        segmented<'live' | 'test'>(copy.agentsPage.keyMode, [{ value: 'test', label: copy.agentsPage.keyTest }, { value: 'live', label: copy.agentsPage.keyLive }], mode, (v) => (mode = v), 'mode'))],
    foot: [h('button', { type: 'button', class: 'btn btn-secondary', 'data-cancel': '' }, copy.stop.cancel), go],
    initialFocus: () => name,
  });
  sheet.el.querySelector('[data-cancel]')?.addEventListener('click', () => sheet.close());
  go.addEventListener('click', async () => {
    go.disabled = true;
    try {
      const k = await api<ApiKey>('POST', `/v1/agents/${a.id}/keys`, { body: { name: name.value.trim() || 'key', mode } });
      sheet.close();
      showKeySheet(k, done);
    } catch (e) {
      go.disabled = false;
      if (e instanceof ApiError && e.param === 'name') return showError(sheet.el, 'k-name', e.message);
      apiErrorToast(e);
    }
  });
}

function openRoll(k: ApiKey, done: () => void): void {
  const opts: [string, number][] = [['0s', 0], ['1h', 3600], ['24h', 86400], ['168h', 604800]];
  let pick = 86400;
  const go = h('button', { type: 'button', class: 'btn btn-primary' }, copy.agentsPage.rollButton);
  const sheet = openSheet({
    title: fill(copy.agentsPage.rollTitle, { name: k.name }),
    body: [h('p', null, copy.agentsPage.rollLede),
      segmented<string>(copy.agentsPage.rollLede, opts.map(([k2, s]) => ({ value: String(s), label: copy.agentsPage.rollOverlap[k2] ?? k2 })), String(pick), (v) => (pick = Number(v)), 'overlap'),
      h('p', null, copy.agentsPage.rollAfter)],
    foot: [h('button', { type: 'button', class: 'btn btn-secondary', 'data-cancel': '' }, copy.stop.cancel), go],
  });
  sheet.el.querySelector('[data-cancel]')?.addEventListener('click', () => sheet.close());
  go.addEventListener('click', async () => {
    go.disabled = true;
    try {
      const nk = await api<ApiKey>('POST', `/v1/keys/${k.id}/roll`, { body: { expires_in: pick } });
      sheet.close();
      showKeySheet(nk, done);
    } catch (e) {
      go.disabled = false;
      apiErrorToast(e);
    }
  });
}

function budgetsTab(a: Agent, panel: HTMLElement, set: (a: Agent) => void): void {
  const admin = isAdmin();
  const b = a.budgets ?? {};
  const dis = admin ? {} : { disabled: true };
  const perMin = h('input', { class: 'input num', id: 'b-min', name: 'calls_per_minute', inputmode: 'numeric', value: b.calls_per_minute ?? '', placeholder: copy.agentsPage.budgetsNone, autocomplete: 'off', ...dis });
  const perDay = h('input', { class: 'input num', id: 'b-day', name: 'calls_per_day', inputmode: 'numeric', value: b.calls_per_day ?? '', placeholder: copy.agentsPage.budgetsNone, autocomplete: 'off', ...dis });
  const spend = h('input', { class: 'input num', id: 'b-spend', name: 'spend_per_day', inputmode: 'decimal', value: b.spend_per_day ? (b.spend_per_day.amount / 100).toFixed(2) : '', placeholder: copy.agentsPage.budgetsNone, autocomplete: 'off', ...dis });
  let currency = b.spend_per_day?.currency ?? 'usd';
  const cur = h('select', { class: 'select', id: 'b-cur', name: 'currency', ...dis }, ...['usd', 'eur', 'gbp', 'inr'].map((c) => h('option', { value: c, ...(c === currency ? { selected: true } : {}) }, c.toUpperCase())));
  cur.addEventListener('change', () => (currency = cur.value));
  const save = h('button', { type: 'button', class: 'btn btn-primary', ...dis }, copy.agentsPage.budgetsSave);
  const usage = a.usage ? h('div', { class: 'budget-now' }, usageMeter(a.usage.calls_today, b.calls_per_day ?? null, copy.agentsPage.callsToday)) : null;
  replace(panel, h('p', { class: 't-2' }, copy.agentsPage.budgetsLede), usage,
    h('div', { class: 'form-grid' },
      field('b-min', copy.agentsPage.callsPerMinute, perMin), field('b-day', copy.agentsPage.callsPerDay, perDay),
      field('b-spend', copy.agentsPage.spendPerDay, spend), field('b-cur', copy.agentsPage.currency, h('span', { class: 'select-wrap' }, cur, icon('chevron-down', 16)))),
    h('div', { class: 'form-actions' }, save, admin ? null : h('p', { class: 't-footnote t-2' }, copy.agentsPage.adminOnly)));
  save.addEventListener('click', async () => {
    const budgets = budgetsFrom(perMin.value, perDay.value, spend.value, currency);
    if (typeof budgets === 'string') return showError(panel, 'b-day', budgets);
    save.disabled = true;
    try {
      const next = await api<Agent & { deed?: string }>('PATCH', `/v1/agents/${a.id}`, { body: { budgets } });
      set({ ...a, ...next });
      toast(copy.agentsPage.budgetsSaved, { kind: 'success', action: next.deed ? { label: copy.stop.viewDeed, href: `/app/deeds/${next.deed}` } : undefined });
    } catch (e) {
      if (e instanceof ApiError && e.param?.startsWith('budgets')) showError(panel, e.param.includes('minute') ? 'b-min' : e.param.includes('spend') ? 'b-spend' : 'b-day', e.message);
      else apiErrorToast(e);
    } finally {
      save.disabled = false;
    }
  });
}

async function securityTab(a: Agent, panel: HTMLElement, set: (a: Agent) => void): Promise<void> {
  const admin = isAdmin();
  const keys = await api<ListResponse<IntentKey & { kid: string; public_key: string; created_by: string }>>('GET', `/v1/agents/${a.id}/intent-keys`);
  const tz = get()?.org.timezone ?? 'UTC';
  const mode = segmented<'off' | 'optional' | 'required'>(copy.agentsPage.intentMode, (['off', 'optional', 'required'] as const).map((m) => ({ value: m, label: copy.agentsPage.intentModes[m] ?? m })), a.intent_mode ?? 'off', async (m) => {
    try {
      const next = await api<Agent>('PATCH', `/v1/agents/${a.id}`, { body: { intent_mode: m } });
      set({ ...a, ...next });
      toast(fill(copy.agentsPage.intentSaved, { mode: copy.agentsPage.intentModes[m] ?? m }), { kind: 'success' });
    } catch (e) {
      apiErrorToast(e);
      void securityTab(a, panel, set);
    }
  }, 'intent_mode');
  if (!admin) for (const b of mode.querySelectorAll('button')) b.disabled = true;
  const input = h('input', { class: 'input mono', id: 'ik-key', name: 'public_key', placeholder: 'ed25519:…', autocomplete: 'off', spellcheck: 'false', ...(admin ? {} : { disabled: true }) });
  const add = h('button', { type: 'button', class: 'btn btn-secondary', ...(admin ? {} : { disabled: true }) }, icon('plus', 16), copy.agentsPage.addKey);
  const list = keys.data.length
    ? h('ul', { class: 'reasons' }, ...keys.data.map((k) => {
        const rm = h('button', { type: 'button', class: 'btn btn-quiet btn-sm', ...(admin ? {} : { disabled: true }) }, copy.agentsPage.remove);
        rm.addEventListener('click', async () => {
          try {
            await api('DELETE', `/v1/agents/${a.id}/intent-keys/${encodeURIComponent(k.kid)}`);
            void securityTab(a, panel, set);
          } catch (e) {
            apiErrorToast(e);
          }
        });
        return h('li', { class: 'intent-key' }, icon('key-round', 16), h('span', null, h('span', { class: 'mono' }, `kid ${k.kid.slice(0, 4)}…${k.kid.slice(-4)}`), h('span', { class: 't-2' }, ' · added '), relTime(k.created_at, tz), k.created_by ? h('span', { class: 't-2' }, ` by ${k.created_by}`) : null), rm);
      }))
    : h('p', { class: 't-2' }, copy.intents.keysEmpty.replace('support-bot', a.name));
  add.addEventListener('click', async () => {
    const v = input.value.trim();
    if (!KEY_RE.test(v)) return showError(panel, 'ik-key', copy.agentsPage.addKeyBad);
    try {
      await api('POST', `/v1/agents/${a.id}/intent-keys`, { body: { public_key: v } });
      void securityTab(a, panel, set);
    } catch (e) {
      if (e instanceof ApiError && e.param === 'public_key') showError(panel, 'ik-key', e.message);
      else apiErrorToast(e);
    }
  });
  input.addEventListener('input', () => showError(panel, 'ik-key', input.value && !KEY_RE.test(input.value.trim()) ? copy.agentsPage.addKeyBad : null));
  replace(panel,
    h('section', { class: 'card card-pad sec-block' },
      h('h2', { class: 't-headline' }, copy.intents.title), h('p', { class: 't-2' }, copy.agentsPage.intentExplainer),
      h('div', { class: 'field' }, h('span', { class: 'label' }, copy.agentsPage.intentMode), mode, h('p', { class: 'help' }, copy.agentsPage.intentRequiredHelp))),
    h('section', { class: 'card card-pad sec-block' },
      h('h2', { class: 't-headline' }, copy.agentsPage.intentKeys), list,
      h('div', { class: 'secret' }, field('ik-key', copy.agentsPage.addKeyLabel, input, copy.agentsPage.addKeyHint), h('div', { class: 'add-key' }, add))),
    admin ? null : h('p', { class: 't-footnote t-2' }, copy.agentsPage.adminOnly));
}

function dangerTab(a: Agent, panel: HTMLElement, set: (a: Agent) => void): void {
  const admin = isAdmin();
  const dis = admin ? {} : { disabled: true, title: copy.agents.noPermission };
  const act = async (path: string, body: unknown, msg: string) => {
    const next = await api<Agent>('POST', `/v1/agents/${a.id}/${path}`, { body });
    set({ ...a, ...next });
    toast(fill(msg, { name: a.name }), { kind: 'success' });
    dangerTab({ ...a, ...next }, panel, set);
  };
  const suspend = a.status === 'suspended'
    ? h('button', { type: 'button', class: 'btn btn-secondary', ...dis, onclick: () => void act('resume', {}, copy.agentsPage.resumed).catch(apiErrorToast) }, icon('refresh-cw', 16), copy.agentsPage.resume)
    : h('button', { type: 'button', class: 'btn btn-secondary', ...(a.status === 'stopped' ? { disabled: true } : dis), onclick: () => void act('suspend', {}, copy.agentsPage.suspended).catch(apiErrorToast) }, icon('pause', 16), copy.agentsPage.suspend);
  const stop = a.status === 'stopped'
    ? h('button', { type: 'button', class: 'btn btn-secondary', ...dis, onclick: () => confirmTyped({
        title: fill(copy.agentsPage.startTitle, { name: a.name }), body: copy.agentsPage.startBody, confirmLabel: copy.agentsPage.start,
        reasonLabel: copy.stop.startReason, reasonRequired: true, onConfirm: (reason) => act('start', { reason }, copy.agentsPage.started),
      }) }, icon('play', 16), copy.agentsPage.start)
    : h('button', { type: 'button', class: 'btn btn-danger-outline', ...dis }, icon('stop', 16), copy.agentsPage.stop);
  if (a.status !== 'stopped') {
    stop.addEventListener('click', () => {
      confirmTyped({
        title: fill(copy.stop.agentTitle, { name: a.name }), body: fill(copy.stop.agentBody, { name: a.name }), expected: a.name,
        expectedLabel: fill(copy.stop.agentConfirm, { name: a.name }), confirmLabel: copy.stop.agentButton, reasonLabel: copy.stop.reasonOptional, danger: true,
        onConfirm: async (reason) => {
          await act('stop', reason ? { reason } : {}, copy.stop.agentToast);
        },
      });
    });
  }
  replace(panel, h('section', { class: 'card', 'aria-labelledby': 'dz-t' },
    h('div', { class: 'card-head' }, h('h2', { class: 't-headline', id: 'dz-t' }, copy.agentsPage.danger)),
    h('div', { class: 'danger-row' }, h('div', null, h('p', { class: 'font-medium m-0' }, a.status === 'suspended' ? copy.agentsPage.resume : copy.agentsPage.suspend), h('p', null, copy.agentsPage.suspendLede)), suspend),
    h('div', { class: 'danger-row' }, h('div', null, h('p', { class: 'font-medium m-0' }, a.status === 'stopped' ? copy.agentsPage.start : copy.agentsPage.stop), h('p', null, copy.agentsPage.stopLede)), stop)),
  admin ? null : h('p', { class: 't-footnote t-2 mt-3' }, copy.agents.noPermission));
}

export { shortId };
