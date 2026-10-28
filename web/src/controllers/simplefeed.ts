// simplefeed.ts: What agents did (DL-143). Four numbers for today, a filter, and every action as one
// plain sentence (who, what, on which record) with its result word: Checked, Problem found, Waiting
// for you, Stopped by a rule. Live over the event stream. A row opens to say what happened and links
// to every detail in the Detailed view. Results come from the server's verdicts, never from here.

import { api, ApiError } from '../lib/api.ts';
import { fill } from '../lib/copy.ts';
import { simpleCopy as sc } from '../lib/copy-simple.ts';
import { h, icon, replace, type Child } from '../lib/dom.ts';
import { dateTime, number, when } from '../lib/format.ts';
import { ICON, TONE, matchesFilter, placeOf, plainAction, type Filter, type Plain, type Result } from '../lib/models/simple.ts';
import { get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { Action, ActionDetail, ListResponse, Overview } from '../lib/types.ts';
import { kv, segmented } from './components.ts';
import { announce } from './shell.ts';

const c = sc.feed;
const tz = () => get()?.org.timezone ?? 'UTC';
const PAGE = 50;
const ENRICH = 25;

/** resultPill: the result word with its icon and colour pair. */
export function resultPill(r: Result): HTMLElement {
  return h('span', { class: `chip sx-pill chip-${TONE[r]}`, 'data-tone': TONE[r] }, icon(ICON[r], 16), c.result[r]!);
}

/** emptyBlock: the Console's empty state (one sentence that says the next step, one action). */
export function emptyBlock(text: string, ic: string, action?: { label: string; href: string }): HTMLElement {
  return h('div', { class: 'state', role: 'status' }, h('span', { class: 'well' }, icon(ic, 20)), h('p', null, text),
    action ? h('div', { class: 'actions' }, h('a', { class: 'btn btn-primary btn-xl', href: action.href }, action.label)) : null);
}

/** errorBlock: what went wrong, with the server's own next step, and Try again. */
export function errorBlock(message: string, retryLabel: string, retry: () => void): HTMLElement {
  return h('div', { class: 'state state-error', role: 'alert' }, h('span', { class: 'well' }, icon('circle-alert', 20)), h('p', null, message),
    h('div', { class: 'actions' }, h('button', { type: 'button', class: 'btn btn-secondary', onclick: retry }, icon('refresh-cw', 16), retryLabel)));
}

interface Extra { request: unknown; summary: string }

export function mountFeed(host: HTMLElement): void {
  let filter: Filter = 'all';
  let rows: Action[] = [];
  let cursor: string | null = null;
  const extra = new Map<string, Extra>();
  const pending = new Set<string>();
  const openIds = new Set<string>();
  let seen = new Set<string>();

  const stats = h('div', { class: 'sv-stats', 'aria-live': 'off' });
  const stat = (n: number | null, label: string, tone = '') => h('div', { class: 'sv-stat', 'data-tone': tone }, h('div', { class: 'n' }, n === null ? '·' : number(n)), h('div', { class: 'l' }, label));
  const list = h('ul', { class: 'sv-list', 'aria-label': c.title });
  const older = h('div', { class: 'sv-older' }, h('button', { type: 'button', class: 'btn btn-secondary', onclick: () => void load(true) }, c.more));
  const filters = segmented(c.filterLabel, (['all', 'waiting', 'problems', 'stopped'] as Filter[]).map((v) => ({ value: v, label: c.filters[v]! })), filter, (v) => {
    filter = v;
    renderList();
  }, 'simple');
  filters.classList.add('sv-filters');

  const plainOf = (a: Action): Plain => plainAction(a, extra.get(a.id)?.request);

  const row = (a: Action, isNew: boolean): HTMLElement => {
    const p = plainOf(a);
    const id = `sv-more-${a.id}`;
    const open = openIds.has(a.id);
    const btn = h('button', { type: 'button', 'aria-expanded': open ? 'true' : 'false', 'aria-controls': id },
      h('time', { datetime: a.created_at, title: dateTime(a.created_at, tz()) }, when(a.created_at, tz())),
      h('span', null, h('span', { class: 'what' }, p.text), h('span', { class: 'who' }, a.agent.name)),
      resultPill(p.result));
    const panel = h('div', { class: 'sv-more-panel', id });
    panel.hidden = !open;
    if (open) replace(panel, ...details(a, p));
    btn.addEventListener('click', () => {
      const now = !openIds.has(a.id);
      if (now) openIds.add(a.id);
      else openIds.delete(a.id);
      btn.setAttribute('aria-expanded', now ? 'true' : 'false');
      panel.hidden = !now;
      if (now) {
        replace(panel, ...details(a, p));
        void enrich([a], true);
      }
    });
    const li = h('li', { class: 'sv-row', 'data-id': a.id, 'data-result': p.result }, btn, panel);
    if (isNew) li.setAttribute('data-new', '');
    return li;
  };

  const details = (a: Action, p: Plain): Child[] => {
    const ex = extra.get(a.id);
    const happened = ex?.summary || a.decision?.message || a.decision?.reason || a.execution?.error_message || c.result[p.result]!;
    const where = placeOf(null, a.connector ?? a.tool.split('.')[0] ?? '');
    return [
      kv([[c.happened, happened], [c.record, p.record ?? ''], [c.where, where], [c.when, dateTime(a.created_at, tz())], [c.toolLabel, h('span', { class: 'mono', translate: 'no' }, a.tool)]]),
      h('a', { href: `/app/actions/${a.id}`, class: 'btn btn-secondary btn-sm' }, c.allDetails, icon('arrow-right', 16)),
    ];
  };

  const renderList = () => {
    const shown = rows.filter((a) => matchesFilter(plainOf(a).result, filter));
    if (rows.length === 0) {
      host.replaceChildren(stats, emptyBlock(c.empty, 'activity', { label: c.emptyConnect, href: '/app/connect' }));
      return;
    }
    list.replaceChildren(...shown.map((a) => row(a, !seen.has(a.id) && seen.size > 0)));
    seen = new Set(rows.map((a) => a.id));
    older.hidden = !cursor;
    host.replaceChildren(stats, filters, shown.length ? list : emptyBlock(c.emptyFiltered, 'search'), older);
  };

  // GET /v1/actions doesn't carry the arguments; the first rows are read one by one
  // (expand[]=request) until the server adds a summary to the list (WAR_ROOM API line).
  const enrich = async (as: Action[], force = false) => {
    const todo = as.filter((a) => !extra.has(a.id) && !pending.has(a.id)).slice(0, force ? as.length : ENRICH);
    for (const a of todo) pending.add(a.id);
    let i = 0;
    const worker = async () => {
      while (i < todo.length) {
        const a = todo[i++]!;
        try {
          const d = await api<ActionDetail>('GET', `/v1/actions/${a.id}`, { query: { 'expand[]': 'request' } });
          extra.set(a.id, { request: d.request ?? null, summary: d.witness?.summary ?? '' });
        } catch {
          extra.set(a.id, { request: null, summary: '' });
        } finally {
          pending.delete(a.id);
        }
      }
    };
    await Promise.all([worker(), worker(), worker(), worker()]);
    if (todo.length) renderList();
  };

  const loadStats = async () => {
    try {
      const o = await api<Overview>('GET', '/v1/overview', { query: { window: '24h' } });
      stats.replaceChildren(stat(o.actions.total, c.stats.total), stat(o.verification.verified, c.stats.checked),
        stat(o.approvals.pending, c.stats.waiting, o.approvals.pending ? 'pending' : ''), stat(o.verification.mismatch, c.stats.problems, o.verification.mismatch ? 'mismatch' : ''));
    } catch {
      stats.replaceChildren(stat(null, c.stats.total), stat(null, c.stats.checked), stat(null, c.stats.waiting), stat(null, c.stats.problems));
    }
  };

  const load = async (more = false) => {
    try {
      const page = await api<ListResponse<Action>>('GET', '/v1/actions', { query: { limit: String(PAGE), cursor: more && cursor ? cursor : undefined } });
      rows = more ? [...rows, ...page.data] : page.data;
      cursor = page.has_more ? page.next_cursor : null;
      renderList();
      void enrich(page.data);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return;
      host.replaceChildren(errorBlock(fill(c.loadFailed, { message: e instanceof Error ? e.message : String(e) }), c.retry, () => void boot()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };

  const refreshTop = async () => {
    try {
      const page = await api<ListResponse<Action>>('GET', '/v1/actions', { query: { limit: String(PAGE) } });
      const known = new Map(rows.map((a) => [a.id, a]));
      const fresh = page.data.filter((a) => !known.has(a.id));
      const updated = page.data.map((a) => a);
      const tail = rows.filter((a) => !page.data.some((b) => b.id === a.id));
      rows = [...updated, ...tail];
      for (const a of page.data) {
        const old = known.get(a.id);
        if (old && (old.state !== a.state || old.status !== a.status)) extra.delete(a.id); // read the summary again
      }
      renderList();
      void enrich(page.data);
      if (fresh[0]) announce(fill(c.newOne, { what: plainOf(fresh[0]).text }));
    } catch {
      /* the stream's next event tries again */
    }
  };

  const boot = async () => {
    host.setAttribute('aria-busy', 'true');
    await Promise.all([loadStats(), load()]);
  };
  void boot();

  const live = stream();
  let t = 0;
  const soon = () => {
    clearTimeout(t);
    t = window.setTimeout(() => {
      void refreshTop();
      void loadStats();
    }, 300);
  };
  live.on('action.*', soon);
  live.on('approval.*', soon);
  live.onResync(soon);
}
