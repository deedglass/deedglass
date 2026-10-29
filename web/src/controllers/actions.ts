// actions.ts: the Actions feed (S25 §3.4): live, filterable, keyboard-driven, URL-shareable.
// The model is lib/feed.ts; this file renders it, at most once per animation frame. Filters live
// in the URL with the API's names. Text search pauses live insertion (it is the server's).

import { api, ApiError } from '../lib/api.ts';
import { copy, fill, plainVerdict } from '../lib/copy.ts';
import { h, icon, maybe } from '../lib/dom.ts';
import { anchorAfterResync, FeedModel } from '../lib/feed.ts';
import { emptyFilters, filtersToSearch, isFiltered, LEVELS, parseFilters, STATES, toApiQuery, VERDICTS, type Decision, type Filters, type TimeRange } from '../lib/filters.ts';
import { typing } from '../lib/shortcuts.ts';
import { get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { Action, Agent, DeedglassEvent, Level, ListResponse, Verdict } from '../lib/types.ts';
import { feedRow, updateRow } from './actionrow.ts';
import { filterBar, segmented, select, toggles } from './components.ts';
import { openSheet } from './overlay.ts';
import { emptyState, errorState, permissionState } from './screens.ts';
import { announce } from './shell.ts';
import { listKeys } from './table.ts';
import { toast } from './toast.ts';

const PAGE = 50;
const ANNOUNCE_MS = 5000;

const COLS: { key: keyof typeof copy.feed.columns; cls: string; num?: boolean }[] = [
  { key: 'time', cls: 'c-time' }, { key: 'agent', cls: 'c-agent' }, { key: 'tool', cls: 'c-tool' }, { key: 'decision', cls: 'c-decision' },
  { key: 'verdict', cls: 'c-verdict' }, { key: 'latency', cls: 'c-latency', num: true }, { key: 'obo', cls: 'c-obo' },
];

export function skeletonRows(n = 12): HTMLElement {
  return h('div', { class: 'table-frame', 'aria-hidden': 'true' }, ...Array.from({ length: n }, () => h('div', { class: 'skel-row skel-feed' }, ...[0, 1, 2, 3, 4, 5].map(() => h('span', { class: 'skel skel-line' })))));
}

export function mountActions(): void {
  const host = maybe('screen-body');
  const barHost = maybe('feed-filters');
  const pauseHost = maybe('feed-pause');
  if (!host || !barHost) return;
  let filters: Filters = parseFilters(new URLSearchParams(location.search));
  const model = new FeedModel(filters);
  let cursor: string | null = null;
  let loading = false;
  let agents: Agent[] = [];
  let pendingAnnounce = 0;
  let lastAnnounce = 0;
  let announceTimer = 0;
  const tz = () => get()?.org.timezone ?? 'UTC';

  // ---------- DOM: table, pill, older ----------
  const tbody = h('tbody');
  const table = h('table', { class: 'dt feed cards', 'aria-describedby': 'feed-keys' },
    h('thead', null, h('tr', null, ...COLS.map((c) => h('th', { scope: 'col', class: `${c.cls}${c.num ? ' num' : ''}` }, copy.feed.columns[c.key])))),
    tbody);
  const frame = h('div', { class: 'table-frame feed-frame' }, table);
  const top = h('div', { class: 'feed-top', 'aria-hidden': 'true' });
  const pill = h('button', { type: 'button', class: 'feed-pill', hidden: true });
  const older = h('button', { type: 'button', class: 'btn btn-secondary feed-older', hidden: true }, copy.feed.older);
  const end = h('p', { class: 't-footnote t-2 feed-end', hidden: true }, copy.feed.end);
  const keysHint = h('p', { class: 't-footnote t-2 feed-keys hide-sm', id: 'feed-keys' }, copy.feed.keys);
  const qNote = h('p', { class: 'banner banner-readonly feed-qnote', role: 'status', hidden: true }, icon('pause', 16), copy.feed.searchPaused);
  const pauseBtn = h('button', { type: 'button', class: 'btn btn-secondary btn-sm', 'aria-pressed': 'false', 'aria-keyshortcuts': 'p' }, icon('pause', 16), h('span', null, copy.feed.pause));

  const renderAll = () => {
    tbody.replaceChildren(...model.order.map((id) => feedRow(model.rows.get(id)!, tz())));
  };
  const showState = () => {
    if (model.order.length === 0) {
      if (isFiltered(filters)) {
        host.replaceChildren(qNote, emptyState(copy.feed.filteredEmpty, 'search', h('button', { type: 'button', class: 'btn btn-secondary', onclick: () => applyFilters(emptyFilters()) }, copy.feed.clear)));
      } else {
        host.replaceChildren(emptyState(copy.feed.empty, 'activity', h('a', { class: 'btn btn-primary', href: '/app/connect' }, icon('plus', 16), copy.feed.connect), 'https://docs.deedglass.com/quickstart'));
      }
      return;
    }
    if (!frame.isConnected) host.replaceChildren(qNote, top, pill, frame, older, end, keysHint);
  };
  const updatePill = () => {
    const n = model.pending.size;
    pauseBtn.setAttribute('aria-pressed', model.paused ? 'true' : 'false');
    pauseBtn.replaceChildren(icon(model.paused ? 'play' : 'pause', 16), h('span', null, model.paused ? copy.feed.resume : copy.feed.pause));
    if (model.paused) {
      pill.hidden = false;
      pill.replaceChildren(icon('pause', 16), n ? fill(copy.feed.paused, { n }) : copy.feed.pausedNone, h('span', { class: 'pill-act' }, copy.feed.resume));
    } else if (n > 0) {
      pill.hidden = false;
      pill.replaceChildren(icon('arrow-up-right', 16), n === 1 ? copy.feed.newAction : fill(copy.feed.newActions, { n }), h('span', { class: 'pill-act' }, copy.feed.show));
    } else pill.hidden = true;
  };
  const flush = () => {
    const ids = model.flush();
    if (ids.length) {
      for (const id of ids) {
        const a = model.rows.get(id);
        if (a) tbody.prepend(feedRow(a, tz(), true));
      }
      while (tbody.rows.length > model.order.length) tbody.lastElementChild?.remove();
    }
    updatePill();
    showState();
  };
  pill.addEventListener('click', () => {
    model.paused = false;
    flush();
    window.scrollTo({ top: 0, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
  });
  const togglePause = () => {
    model.paused = !model.paused;
    if (!model.paused) flush();
    updatePill();
    announce(model.paused ? copy.feed.pausedNone : copy.feed.resume);
  };
  pauseBtn.addEventListener('click', togglePause);
  pauseHost?.replaceChildren(pauseBtn);

  // At the top of the list or not (new rows go straight in only at the top).
  new IntersectionObserver((es) => {
    const was = model.atTop;
    model.atTop = es[0]?.isIntersecting ?? true;
    if (!was && model.atTop && !model.paused && model.pending.size) flush();
  }).observe(top);

  // ---------- loading ----------
  const load = async (opts: { quiet?: boolean; keepAnchor?: boolean } = {}) => {
    if (loading) return;
    loading = true;
    const visible = opts.keepAnchor ? [...tbody.rows].filter((r) => r.getBoundingClientRect().top >= 0).map((r) => r.dataset.id ?? '') : [];
    if (!opts.quiet) {
      host.setAttribute('aria-busy', 'true');
      host.replaceChildren(skeletonRows());
    }
    const slow = window.setTimeout(() => host.append(h('p', { class: 't-footnote t-2 slow-note', role: 'status' }, copy.feed.slow)), 8000);
    try {
      const page = await api<ListResponse<Action>>('GET', '/v1/actions', { query: { ...toApiQuery(filters, new Date()), limit: String(PAGE) } });
      model.setFilters(filters);
      model.load(page.data);
      cursor = page.has_more ? page.next_cursor : null;
      renderAll();
      if (!frame.isConnected || !opts.quiet) host.replaceChildren(qNote, top, pill, frame, older, end, keysHint);
      older.hidden = !cursor;
      end.hidden = !!cursor || page.data.length === 0;
      qNote.hidden = !filters.q;
      updatePill();
      showState();
      if (opts.keepAnchor) {
        const id = anchorAfterResync(visible, model.order);
        if (id) tbody.querySelector(`[data-id="${id}"]`)?.scrollIntoView({ block: 'start' });
      }
    } catch (e) {
      if (e instanceof ApiError && e.status === 403) host.replaceChildren(permissionState('Viewer'));
      else if (e instanceof ApiError && e.status === 400) {
        toast(e.message, { kind: 'error', requestId: e.requestId });
        applyFilters(emptyFilters());
      } else if (!(e instanceof ApiError && e.status === 401)) {
        if (!opts.quiet || model.order.length === 0) host.replaceChildren(errorState(e, () => void load()));
      }
    } finally {
      clearTimeout(slow);
      host.querySelector('.slow-note')?.remove();
      host.setAttribute('aria-busy', 'false');
      loading = false;
    }
  };
  older.addEventListener('click', async () => {
    if (!cursor) return;
    older.disabled = true;
    try {
      const page = await api<ListResponse<Action>>('GET', '/v1/actions', { query: { ...toApiQuery(filters, new Date()), limit: String(PAGE), cursor } });
      const before = model.order.length;
      model.append(page.data);
      for (const id of model.order.slice(before)) tbody.append(feedRow(model.rows.get(id)!, tz()));
      cursor = page.has_more ? page.next_cursor : null;
      older.hidden = !cursor;
      end.hidden = !!cursor;
    } catch (e) {
      toast((e as Error).message, { kind: 'error' });
    } finally {
      older.disabled = false;
    }
  });

  // ---------- filters ----------
  const applyFilters = (f: Filters) => {
    filters = f;
    const sp = filtersToSearch(f).toString();
    history.replaceState(null, '', sp ? `${location.pathname}?${sp}` : location.pathname);
    renderBar();
    void load();
  };
  const search = h('input', { class: 'input', type: 'search', name: 'q', id: 'feed-q', placeholder: `${copy.feed.search}…`, 'aria-label': copy.feed.searchLabel, autocomplete: 'off', spellcheck: 'false', maxlength: 64, 'aria-keyshortcuts': '/' });
  let qTimer = 0;
  search.addEventListener('input', () => {
    clearTimeout(qTimer);
    qTimer = window.setTimeout(() => {
      const q = search.value.trim();
      if (q.length === 1) return;
      applyFilters({ ...filters, q: q.length >= 2 ? q : '' });
    }, 300);
  });
  const tool = h('input', { class: 'input mono', name: 'tool', id: 'feed-tool', placeholder: copy.feed.toolHint, 'aria-label': copy.feed.tool, autocomplete: 'off', spellcheck: 'false', maxlength: 200 });
  tool.addEventListener('change', () => applyFilters({ ...filters, tool: tool.value.trim() }));
  const moreBtn = h('button', { type: 'button', class: 'btn btn-secondary btn-sm', 'aria-haspopup': 'dialog', 'aria-keyshortcuts': 'f' }, icon('sliders-horizontal', 16), copy.feed.more);
  const clearBtn = h('button', { type: 'button', class: 'btn btn-quiet btn-sm' }, icon('x', 16), copy.feed.clear);
  clearBtn.addEventListener('click', () => applyFilters(emptyFilters()));
  moreBtn.addEventListener('click', () => openMore());
  const bar = h('div');
  const renderBar = () => {
    search.value = filters.q;
    tool.value = filters.tool;
    const agentOpts = [{ value: '', label: copy.feed.agentAny }, ...agents.map((a) => ({ value: a.id, label: a.name }))];
    if (filters.agent && !agents.some((a) => a.id === filters.agent)) agentOpts.push({ value: filters.agent, label: filters.agent });
    const timeOpts: { value: TimeRange; label: string }[] = [
      { value: '', label: copy.feed.timeAny }, { value: '1h', label: '1 h' }, { value: '24h', label: '24 h' }, { value: '7d', label: '7 d' },
      ...(filters.time === 'custom' ? [{ value: 'custom' as TimeRange, label: copy.feed.custom }] : []),
    ];
    const moreCount = filters.state.length + (filters.on_behalf_of ? 1 : 0) + (filters.time === 'custom' ? 1 : 0);
    moreBtn.replaceChildren(icon('sliders-horizontal', 16), moreCount ? `${copy.feed.more} · ${moreCount}` : copy.feed.more);
    clearBtn.hidden = !isFiltered(filters);
    bar.replaceChildren(filterBar({
      search: h('div', { class: 'filter-search' }, icon('search', 16), search),
      controls: [
        select(copy.feed.agent, agentOpts, filters.agent, (v) => applyFilters({ ...filters, agent: v }), 'feed-agent'),
        h('div', { class: 'filter-tool' }, tool),
        select<TimeRange>(copy.feed.time, timeOpts, filters.time, (v) => (v === 'custom' ? openMore() : applyFilters({ ...filters, time: v, from: '', to: '' })), 'feed-time'),
      ],
      segments: [
        segmented<Decision | ''>(copy.feed.decision, [
          { value: '', label: copy.feed.decisionAll }, { value: 'allow', label: copy.chips.allowed }, { value: 'deny', label: copy.chips.denied }, { value: 'require_approval', label: copy.chips.approval },
        ], filters.decision, (v) => applyFilters({ ...filters, decision: v }), 'decision'),
        toggles<Verdict>(copy.feed.verdict, VERDICTS.map((v) => ({ value: v, label: plainVerdict(v) })), filters.verdict, (v) => applyFilters({ ...filters, verdict: v }), 'verdict'),
        toggles<Level>(copy.feed.level, LEVELS.map((v) => ({ value: v, label: copy.chips.level[v] ?? v })), filters.level, (v) => applyFilters({ ...filters, level: v }), 'level'),
      ],
      end: [moreBtn, clearBtn],
    }));
  };
  const openMore = () => {
    const states = h('fieldset', { class: 'field fieldset' }, h('legend', { class: 'label' }, copy.feed.state),
      h('div', { class: 'check-grid' }, ...STATES.map((st) => h('label', { class: 'check' }, h('input', { type: 'checkbox', name: 'state', value: st, ...(filters.state.includes(st) ? { checked: true } : {}) }), h('span', null, st.replace(/_/g, ' '))))));
    const obo = h('input', { class: 'input mono', id: 'more-obo', name: 'on_behalf_of', value: filters.on_behalf_of, autocomplete: 'off', spellcheck: 'false', maxlength: 256 });
    const local = (iso: string) => (iso ? new Date(new Date(iso).getTime() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16) : '');
    const from = h('input', { class: 'input', type: 'datetime-local', id: 'more-from', name: 'from', autocomplete: 'off', value: local(filters.from) });
    const to = h('input', { class: 'input', type: 'datetime-local', id: 'more-to', name: 'to', autocomplete: 'off', value: local(filters.to) });
    const apply = h('button', { type: 'button', class: 'btn btn-primary' }, copy.feed.apply);
    const sheet = openSheet({
      title: copy.feed.more,
      body: [
        states,
        h('div', { class: 'field' }, h('label', { class: 'label', for: 'more-obo' }, copy.feed.onBehalfOf), obo),
        h('div', { class: 'field-row' },
          h('div', { class: 'field' }, h('label', { class: 'label', for: 'more-from' }, copy.feed.from), from),
          h('div', { class: 'field' }, h('label', { class: 'label', for: 'more-to' }, copy.feed.to), to)),
      ],
      foot: [h('button', { type: 'button', class: 'btn btn-secondary', 'data-cancel': '' }, copy.stop.cancel), apply],
    });
    sheet.el.querySelector('[data-cancel]')?.addEventListener('click', () => sheet.close());
    apply.addEventListener('click', () => {
      const picked = [...states.querySelectorAll<HTMLInputElement>('input:checked')].map((i) => i.value);
      const f = { ...filters, state: picked, on_behalf_of: obo.value.trim() };
      if (from.value || to.value) {
        f.time = 'custom';
        f.from = from.value ? new Date(from.value).toISOString() : '';
        f.to = to.value ? new Date(to.value).toISOString() : '';
      } else if (f.time === 'custom') {
        f.time = '';
        f.from = '';
        f.to = '';
      }
      sheet.close();
      applyFilters(parseFilters(filtersToSearch(f)));
    });
  };
  barHost.replaceChildren(bar);
  renderBar();

  // ---------- live ----------
  const onEvent = (e: DeedglassEvent) => {
    const r = model.apply(e as DeedglassEvent<Partial<Action>>);
    if (r === 'inserted') {
      const id = (e.data.object as { id: string }).id;
      tbody.prepend(feedRow(model.rows.get(id)!, tz(), true));
      while (tbody.rows.length > model.order.length) tbody.lastElementChild?.remove();
      showState();
      pendingAnnounce++;
      scheduleAnnounce();
    } else if (r === 'updated') {
      // Update the row in place: only the cells a lifecycle event can change (cheap at 200 events/s).
      const id = (e.data.object as { id: string }).id;
      const old = tbody.querySelector<HTMLTableRowElement>(`tr[data-id="${id}"]`);
      if (old) updateRow(old, model.rows.get(id)!, tz());
    } else if (r === 'buffered') {
      updatePill();
      pendingAnnounce++;
      scheduleAnnounce();
    }
  };
  const scheduleAnnounce = () => {
    if (announceTimer) return;
    const wait = Math.max(0, ANNOUNCE_MS - (Date.now() - lastAnnounce));
    announceTimer = window.setTimeout(() => {
      announceTimer = 0;
      if (pendingAnnounce > 0) announce(pendingAnnounce === 1 ? copy.feed.newAction : fill(copy.feed.announced, { n: pendingAnnounce }));
      pendingAnnounce = 0;
      lastAnnounce = Date.now();
    }, wait);
  };

  // ---------- keys: p pause, / search, f filters, Esc clears the selection ----------
  let lastG = 0;
  window.addEventListener('keydown', (e) => {
    if (e.metaKey || e.ctrlKey || e.altKey || typing(e.target)) return;
    if (document.querySelector('.palette, .sheet, .dialog, .menu')) return;
    if (e.key === 'g') {
      lastG = Date.now();
      return;
    }
    if (Date.now() - lastG < 1200) return; // part of a g-sequence (g p = Approvals)
    if (e.key === '/') {
      e.preventDefault();
      e.stopImmediatePropagation();
      search.focus();
    } else if (e.key === 'p') {
      e.preventDefault();
      togglePause();
    } else if (e.key === 'f') {
      e.preventDefault();
      openMore();
    } else if (e.key === 'Escape') {
      for (const r of tbody.querySelectorAll('[aria-selected="true"]')) r.setAttribute('aria-selected', 'false');
    }
  }, { capture: true });
  listKeys();

  document.addEventListener('dg:ready', async () => {
    try {
      agents = (await api<ListResponse<Agent>>('GET', '/v1/agents', { query: { limit: '100' } })).data;
      renderBar();
    } catch {
      /* the select keeps "Any agent" */
    }
    await load();
    const s = stream();
    s.on('action.*', onEvent);
    s.onResync(() => {
      void load({ quiet: true, keepAnchor: true });
      toast(copy.states.resync);
    });
  }, { once: true });
  document.addEventListener('dg:load-failed', (e) => host.replaceChildren(errorState((e as CustomEvent).detail, () => location.reload())), { once: true });
}
