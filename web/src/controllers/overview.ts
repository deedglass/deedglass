// overview.ts: the Overview screen (S25 §3.3): four tiles, Needs attention, Agents, Live actions
// and the Chain card, or the first-run flow when the org has no agents. Numbers come from
// GET /v1/overview (refetched every 60 s) and move live between refetches from the event stream.

import { api, ApiError } from '../lib/api.ts';
import { relTime, statusChip } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { number, time } from '../lib/format.ts';
import { applyLive, attentionLine, blockedLine, delta, segments, sparkLabel, usageLine, waitingLine, windowWords, type Window } from '../lib/overview.ts';
import { atLeast, get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { Action, Agent, ApiKey, LedgerCheck, ListResponse, Overview } from '../lib/types.ts';
import { miniRow, verdictCell } from './actionrow.ts';
import { chainView } from './chain.ts';
import { attentionList, cardSection, metricTile, segmented, snippetTabs, usageMeter, verificationBar } from './components.ts';
import { errorState, permissionState } from './screens.ts';
import { apiErrorToast, toast } from './toast.ts';
import { showKeySheet } from './keysheet.ts';
import { announce } from './shell.ts';
import { mountMirror } from '@verify/ui/mirror.ts';
import { mountCaughtBanner } from './sessions.ts';
import { mountThisWeek } from './weekly.ts';

const REFRESH_MS = 60_000;
const LIVE_ROWS = 10;

function tilesSkeleton(): HTMLElement {
  const t = () => h('div', { class: 'tile', 'aria-hidden': 'true' }, h('span', { class: 'skel skel-line w-16' }), h('span', { class: 'skel skel-metric' }), h('span', { class: 'skel skel-line w-32' }));
  return h('div', { class: 'tiles' }, t(), t(), t(), t());
}

function skeleton(): HTMLElement {
  const block = (rows: number) => h('div', { class: 'card card-pad', 'aria-hidden': 'true' }, ...Array.from({ length: rows }, () => h('span', { class: 'skel skel-line skel-gap' })));
  return h('div', { class: 'ov' }, tilesSkeleton(), h('div', { class: 'ov-grid' }, h('div', { class: 'ov-main' }, block(4)), h('div', { class: 'ov-side' }, block(4))));
}

function windowFromURL(): Window {
  return new URLSearchParams(location.search).get('window') === '7d' ? '7d' : '24h';
}

export function mountOverview(): void {
  const body = maybe('ov-body');
  const lede = maybe('page-lede');
  const actionsSlot = maybe('ov-actions');
  if (!body) return;
  let win: Window = windowFromURL();
  let data: Overview | null = null;
  let lastOK: Date | null = null;
  let seen = new Set<string>();
  let timer = 0;
  let firstRun = false;
  const slow = window.setTimeout(() => {
    if (!data) body.append(h('p', { class: 't-footnote t-2 slow-note', role: 'status' }, copy.feed.slow));
  }, 8000);

  const tz = () => get()?.org.timezone ?? 'UTC';
  const orgName = () => get()?.org.name ?? 'this org';
  const setLede = (offline = false) => {
    if (!lede) return;
    const base = fill(copy.overview.subtitle, { org: orgName(), window: windowWords(win), tz: tz() });
    lede.textContent = offline && lastOK ? `${base} · ${fill(copy.overview.stale, { time: time(lastOK, tz()) })}` : base;
  };

  // ---------- tiles ----------
  const tiles = (o: Overview) => {
    const now = new Date();
    const v = o.verification;
    const witnessed = v.verified + v.mismatch + v.unverifiable;
    const verifTile = h('section', { class: 'tile tile-wide', 'aria-labelledby': 'tile-v-l' },
      h('h2', { class: 'tile-label', id: 'tile-v-l' }, copy.overview.tiles.verification),
      witnessed + v.not_witnessed > 0 ? verificationBar(segments(o), copy.overview.tiles.verification) : h('p', { class: 'tile-sub' }, copy.overview.verificationNone),
      h('p', { class: 'tile-sub levels num' }, `${copy.chips.level.L0} ${number(v.by_level.L0 ?? 0)} · ${copy.chips.level.L1} ${number(v.by_level.L1 ?? 0)} · ${copy.chips.level.L2} ${number(v.by_level.L2 ?? 0)}`));
    return h('div', { class: 'tiles' },
      metricTile({ id: 'tile-a', label: copy.overview.tiles.actions, value: number(o.actions.total), sub: delta(o.actions.total, o.actions.previous, win),
        spark: { values: o.actions.series, label: sparkLabel(copy.overview.sparkActions, o.actions.series, win) }, href: `/app/actions?time=${win}` }),
      metricTile({ id: 'tile-b', label: copy.overview.tiles.blocked, value: number(o.blocked.total), sub: blockedLine(o),
        spark: { values: o.blocked.series, label: sparkLabel(copy.overview.sparkBlocked, o.blocked.series, win) }, href: `/app/actions?decision=deny&time=${win}` }),
      metricTile({ id: 'tile-w', label: copy.overview.tiles.waiting, value: number(o.approvals.pending), sub: waitingLine(o, now), href: '/app/approvals' }),
      verifTile);
  };

  // ---------- attention, agents ----------
  const attention = (o: Overview) => {
    const lines = o.attention.slice(0, 8).map((it) => ({ ...attentionLine(it, orgName(), new Date(), tz(), time), at: relTime(it.at, tz()) }));
    return cardSection('ov-attn-t', copy.overview.attention, null, attentionList(lines, copy.overview.attentionNone));
  };
  const agents = (o: Overview) => {
    const rows = o.agents.data.map((a) => h('li', { class: 'agent-strip' },
      h('a', { href: `/app/agents/${a.id}`, class: 'strip-name' }, a.name),
      statusChip(a.status),
      h('div', { class: 'strip-meter' }, usageMeter(a.usage.calls_today, a.budgets.calls_per_day ?? null, `${a.name}, calls today`), h('span', { class: 'sr-only' }, usageLine(a.usage.calls_today, a.budgets.calls_per_day))),
      a.last_action_at ? relTime(a.last_action_at, tz()) : h('span', { class: 't-2 reltime' }, copy.agentsPage.never)));
    return cardSection('ov-agents-t', copy.overview.agents, h('a', { href: '/app/agents', class: 'card-link' }, copy.overview.agentsAll, icon('chevron-right', 16)),
      rows.length ? h('ul', { class: 'strip' }, ...rows) : h('p', { class: 'card-empty t-2' }, copy.overview.agentsNone));
  };

  // ---------- live actions ----------
  const liveList = h('ul', { class: 'mini', id: 'ov-live', 'aria-live': 'polite', 'aria-relevant': 'additions', 'aria-label': copy.overview.live });
  const liveEmpty = h('p', { class: 'card-empty t-2' }, copy.overview.liveEmpty);
  const liveCard = cardSection('ov-live-t', copy.overview.live, h('a', { href: '/app/actions', class: 'card-link' }, copy.overview.liveOpen, icon('chevron-right', 16)), liveList, liveEmpty);
  const showLive = () => {
    liveEmpty.hidden = liveList.children.length > 0;
  };
  const upsertLive = (a: Action, live: boolean) => {
    const old = liveList.querySelector(`[data-id="${a.id}"]`);
    const row = miniRow(a, tz(), live && !old);
    if (old) {
      if (old.hasAttribute('data-live')) row.setAttribute('data-live', '');
      // The Mirror plays only for a verdict that arrives live (seal.ts rules).
      if (live && a.witness) row.querySelector('.mini-verdict')?.replaceChildren(verdictCell(a, true));
      old.replaceWith(row);
    }
    else {
      liveList.prepend(row);
      while (liveList.children.length > LIVE_ROWS) liveList.lastElementChild?.remove();
    }
    showLive();
  };

  // ---------- chain ----------
  const chainBody = h('div', { class: 'chain', 'aria-live': 'polite' }, h('p', { class: 't-2' }, copy.overview.chainChecking));
  const chainCard = cardSection('ov-chain-t', copy.overview.chain, h('a', { href: '/app/deeds', class: 'card-link' }, copy.nav.deeds, icon('chevron-right', 16)), chainBody);
  const loadChain = async () => {
    const since = new Date(Date.now() - 86_400_000).toISOString();
    try {
      const c = await api<LedgerCheck>('GET', '/v1/ledger/check', { query: { 'issued_at[gte]': since } });
      replace(chainBody, ...chainView(c));
    } catch (e) {
      chainBody.replaceChildren(h('p', { class: 't-2' }, fill(copy.overview.chainError, { message: (e as Error).message })));
    }
  };

  // ---------- render ----------
  // S50: the caught banner (at most 3 watched sessions, each with the free path beside it).
  const caughtHost = h('div', { class: 'ov-caught' });
  void mountCaughtBanner(caughtHost);
  // S52: the This week card (last week's summary, the same one the Monday email sends).
  const weekHost = h('div', { class: 'ov-week' });
  void mountThisWeek(weekHost);
  const render = (o: Overview) => {
    body.replaceChildren(h('div', { class: 'ov' },
      caughtHost,
      tiles(o),
      h('div', { class: 'ov-grid' },
        h('div', { class: 'ov-main' }, attention(o), liveCard),
        h('div', { class: 'ov-side' }, weekHost, agents(o), chainCard))));
    body.setAttribute('aria-busy', 'false');
  };
  const refreshTiles = () => {
    if (!data) return;
    const t = body.querySelector('.tiles');
    t?.replaceWith(tiles(data));
  };

  const load = async (quiet = false) => {
    try {
      const o = await api<Overview>('GET', '/v1/overview', { query: { window: win } });
      clearTimeout(slow);
      data = o;
      seen = new Set();
      lastOK = new Date();
      setLede();
      if (quiet && body.querySelector('.ov .tiles')) {
        refreshTiles();
        body.querySelector('#ov-attn-t')?.closest('.card')?.replaceWith(attention(o));
        body.querySelector('#ov-agents-t')?.closest('.card')?.replaceWith(agents(o));
      } else render(o);
    } catch (e) {
      clearTimeout(slow);
      if (quiet && data) return; // keep what is on screen; the offline banner says why
      if (e instanceof ApiError && e.status === 403) body.replaceChildren(permissionState('Viewer'));
      else if (!(e instanceof ApiError && e.status === 401)) body.replaceChildren(errorState(e, () => void load()));
      body.setAttribute('aria-busy', 'false');
    }
  };

  const loadHistory = async () => {
    try {
      const page = await api<ListResponse<Action>>('GET', '/v1/actions', { query: { limit: String(LIVE_ROWS) } });
      for (const a of [...page.data].reverse()) upsertLive(a, false);
    } catch {
      /* the card keeps its empty line */
    }
  };

  // ---------- window switch ----------
  if (actionsSlot) {
    actionsSlot.replaceChildren(segmented(copy.overview.windowLabel, [
      { value: '24h' as Window, label: copy.overview.windowShort['24h']! },
      { value: '7d' as Window, label: copy.overview.windowShort['7d']! },
    ], win, (v) => {
      win = v;
      const u = new URL(location.href);
      if (v === '24h') u.searchParams.delete('window');
      else u.searchParams.set('window', v);
      history.replaceState(null, '', u.pathname + u.search);
      setLede();
      void load(true);
    }, 'window'));
  }

  document.addEventListener('dg:ready', async () => {
    setLede();
    // First run: no agents yet → the whole body is the onboarding flow.
    try {
      const ag = await api<ListResponse<Agent>>('GET', '/v1/agents', { query: { limit: '1' } });
      firstRun = ag.data.length === 0;
    } catch {
      firstRun = false;
    }
    if (firstRun) {
      clearTimeout(slow);
      if (actionsSlot) actionsSlot.hidden = true;
      mountFirstRun(body);
      return;
    }
    await load();
    void loadHistory();
    void loadChain();
    timer = window.setInterval(() => void load(true), REFRESH_MS);
    const s = stream();
    s.on('action.*', (ev) => {
      const o = ev.data.object as Partial<Action>;
      if (ev.hydrated && o.object === 'action') upsertLive(o as Action, true);
      if (data && applyLive(data, ev, seen)) refreshTiles();
    });
    s.on('approval.*', (ev) => {
      if (data && applyLive(data, ev, seen)) refreshTiles();
    });
    s.onResync(() => {
      void load(true);
      toast(copy.states.resync);
    });
  }, { once: true });
  window.addEventListener('dg:offline', () => setLede(true));
  window.addEventListener('dg:online', () => setLede(false));
  window.addEventListener('pagehide', () => clearInterval(timer));
}

// ---------- first run ----------

function mountFirstRun(body: HTMLElement): void {
  const st = get();
  const admin = atLeast(st?.me.membership.role, 'admin');
  let key = '';
  let agentId = '';
  const snip = snippetTabs(() => ({ server: location.origin, apiKey: key || 'dg_test_REPLACE_ME', connector: 'native' }), 'fr');
  const s1status = h('p', { class: 'fr-status', role: 'status' });
  const s3status = h('p', { class: 'fr-status fr-wait', role: 'status' }, copy.firstRun.step3Waiting);
  const markHost = h('span', { class: 'fr-mark', 'aria-hidden': 'true' });
  const create = h('button', { type: 'button', class: 'btn btn-primary', ...(admin ? {} : { disabled: true, 'aria-describedby': 'fr-viewer' }) }, icon('plus', 16), copy.firstRun.step1Button);
  const step2Note = h('p', { class: 't-footnote t-2' }, copy.firstRun.step2Waiting);
  create.addEventListener('click', async () => {
    create.disabled = true;
    create.setAttribute('aria-busy', 'true');
    try {
      let agent: Agent;
      try {
        agent = await api<Agent>('POST', '/v1/agents', { body: { name: 'demo-agent', labels: { env: 'dev' } } });
      } catch (e) {
        if (!(e instanceof ApiError && e.status === 409)) throw e;
        agent = (await api<ListResponse<Agent>>('GET', '/v1/agents', { query: { limit: '100' } })).data.find((a) => a.name === 'demo-agent')!;
      }
      agentId = agent.id;
      const k = await api<ApiKey>('POST', `/v1/agents/${agent.id}/keys`, { body: { name: 'console', mode: 'test' } });
      showKeySheet(k, () => {
        // The secret stays only in this closure for the snippet; it is never stored.
        step2Note.textContent = copy.firstRun.step2Lede;
        snip.refresh();
      });
      key = k.secret ?? '';
      snip.refresh();
      s1status.replaceChildren(icon('check', 16), fill(copy.firstRun.step1Done, { name: agent.name }));
      create.hidden = true;
    } catch (e) {
      create.disabled = false;
      apiErrorToast(e);
    } finally {
      create.removeAttribute('aria-busy');
    }
  });
  const step = (n: number, title: string, ...kids: Child[]) => h('li', { class: 'fr-step' }, h('span', { class: 'fr-n num', 'aria-hidden': 'true' }, String(n)), h('div', { class: 'fr-body' }, h('h2', { class: 't-headline' }, title), ...kids));
  body.replaceChildren(h('section', { class: 'card first-run', 'aria-labelledby': 'fr-t' },
    h('div', { class: 'fr-head' }, h('h2', { class: 't-title-3', id: 'fr-t' }, copy.firstRun.title), h('span', { class: 't-2' }, `· ${copy.firstRun.lede}`)),
    h('ol', { class: 'fr-steps' },
      step(1, copy.firstRun.step1, h('div', { class: 'fr-row' }, create, s1status), admin ? null : h('p', { class: 't-footnote t-2', id: 'fr-viewer' }, copy.firstRun.step1Viewer)),
      step(2, copy.firstRun.step2, step2Note, snip.el),
      step(3, copy.firstRun.step3, h('div', { class: 'fr-row' }, markHost, s3status)))));
  body.setAttribute('aria-busy', 'false');
  const mirror = mountMirror(markHost, { size: 20, state: 'open' });
  const s = stream();
  let firstAction = '';
  s.on('action.*', (ev) => {
    const o = ev.data.object as Partial<Action>;
    const agent = o.agent?.id;
    if (!agentId || agent !== agentId) return;
    if (!firstAction && ev.type === 'action.created') firstAction = o.id ?? '';
    if (o.id !== firstAction) return;
    if (o.state === 'sealed' || ev.type === 'action.witnessed') {
      const verified = o.witness?.verdict === 'VERIFIED';
      void mirror.set(verified ? 'sealed' : o.witness?.verdict === 'MISMATCH' ? 'mismatch' : 'open', { animate: verified });
      s3status.className = 'fr-status fr-done';
      s3status.replaceChildren(icon('check', 16), h('a', { href: `/app/actions/${o.id}` }, copy.firstRun.step3Done));
      announce(copy.firstRun.step3Done);
    }
  });
}
