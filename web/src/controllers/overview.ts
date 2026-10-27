// overview.ts: the Overview screen (S25 §3.3, reshaped by console-v1 into a live view): Needs you
// (pending approvals with Approve and Decline), Today (allowed, held, blocked, doesn't match), Live
// activity, Needs attention (repeats folded), Agents connected, Last week and the Chain. An org with
// no actions yet gets one next step and turns into the live view when the first action arrives.
// Numbers come from GET /v1/overview (refetched every 60 s, and a few seconds after activity) and
// move live between refetches from the event stream. While the stream isn't live, the page polls
// every 5 s instead, so it never goes quietly stale.

import { api, ApiError } from '../lib/api.ts';
import { relTime, statusChip } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { number, time } from '../lib/format.ts';
import { applyLive, attentionLine, segments, usageLine, windowWords, type Window } from '../lib/overview.ts';
import { liveCopy as lc } from '../lib/copy-live.ts';
import { agentActive, fold, timesWord, todayCounts } from '../lib/live.ts';
import { phiOn } from '../lib/phi.ts';
import { atLeast, get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { Action, Agent, ApiKey, LedgerCheck, ListResponse, Overview } from '../lib/types.ts';
import { createInbox } from './inbox.ts';
import { createLiveFeed } from './livefeed.ts';
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
const LIVE_ROWS = 20;
const POLL_MS = 5_000;
const SETTLE_MS = 3_000; // a refetch this long after activity (the server caches 10 s)

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

  // ---------- Today ----------
  const tiles = (o: Overview) => {
    const t = todayCounts(o);
    const v = o.verification;
    const witnessed = v.verified + v.mismatch + v.unverifiable;
    const span = `time=${win}`;
    return h('div', { class: 'today' },
      h('div', { class: 'tiles today-tiles' },
        metricTile({ id: 'tile-allowed', label: lc.today.allowed, value: number(t.allowed), sub: lc.today.allowedSub, href: `/app/actions?decision=allow&${span}` }),
        metricTile({ id: 'tile-held', label: lc.today.held, value: number(t.held), sub: t.pending ? fill(lc.today.heldSub, { n: number(t.pending) }) : lc.today.heldNone, href: `/app/actions?decision=require_approval&${span}` }),
        metricTile({ id: 'tile-blocked', label: lc.today.blocked, value: number(t.blocked), sub: lc.today.blockedSub, href: `/app/actions?decision=deny&${span}` }),
        metricTile({ id: 'tile-mismatch', label: lc.today.mismatch, value: number(t.mismatch), sub: t.mismatch ? lc.today.mismatchSub : lc.today.mismatchNone, href: `/app/actions?verdict=MISMATCH&${span}` })),
      witnessed + v.not_witnessed > 0 ? h('div', { class: 'today-checks' }, verificationBar(segments(o), copy.overview.tiles.verification)) : null);
  };

  // ---------- attention, agents ----------
  const attention = (o: Overview) => {
    const lines = fold(o.attention.slice(0, 8).map((it) => ({ ...attentionLine(it, orgName(), new Date(), tz(), time), at: relTime(it.at, tz()) })))
      .map((l) => ({ ...l, title: `${l.title}${timesWord(l.count)}` }));
    return cardSection('ov-attn-t', copy.overview.attention, null, attentionList(lines, copy.overview.attentionNone));
  };
  const agents = (o: Overview) => {
    const now = new Date();
    const rows = o.agents.data.map((a) => h('li', { class: 'agent-strip' },
      h('a', { href: `/app/agents/${a.id}`, class: 'strip-name' }, agentActive(a.last_action_at, now) && a.status === 'active' ? h('span', { class: 'live-dot', title: lc.agents.active }, h('span', { class: 'sr-only' }, `${lc.agents.active}: `)) : null, a.name),
      statusChip(a.status),
      h('div', { class: 'strip-meter' }, usageMeter(a.usage.calls_today, a.budgets.calls_per_day ?? null, `${a.name}, calls today`), h('span', { class: 'sr-only' }, usageLine(a.usage.calls_today, a.budgets.calls_per_day))),
      a.last_action_at ? relTime(a.last_action_at, tz()) : h('span', { class: 't-2 reltime' }, copy.agentsPage.never)));
    return cardSection('ov-agents-t', lc.agents.title, h('a', { href: '/app/agents', class: 'card-link' }, lc.agents.all, icon('chevron-right', 16)),
      rows.length ? h('ul', { class: 'strip' }, ...rows) : h('p', { class: 'card-empty t-2' }, copy.overview.agentsNone));
  };

  // ---------- Live activity ----------
  const feed = createLiveFeed(LIVE_ROWS, tz);
  const liveEmpty = h('p', { class: 'card-empty t-2' }, copy.overview.liveEmpty);
  const pollNote = h('p', { class: 'feed-poll t-footnote t-2', role: 'status', hidden: true }, icon('refresh-cw', 16), lc.feed.paused);
  const liveDot = h('span', { class: 'live-pill', 'aria-hidden': 'true' }, h('span', { class: 'live-dot' }), copy.connection.live);
  const liveCard = cardSection('ov-live-t', h('span', { class: 'feed-title' }, lc.feed.title, liveDot),
    h('a', { href: '/app/actions', class: 'card-link' }, lc.feed.open, icon('chevron-right', 16)), pollNote, feed.el, liveEmpty);
  feed.onChange(() => (liveEmpty.hidden = feed.size() > 0));
  const upsertLive = (a: Action, live: boolean) => feed.upsert(a, live);
  const inbox = createInbox();

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
      phiOn() ? h('p', { class: 'banner banner-phi', role: 'note' }, icon('lock', 16), lc.phi.note) : null,
      caughtHost,
      inbox.el,
      tiles(o),
      h('div', { class: 'ov-grid' },
        h('div', { class: 'ov-main' }, liveCard, attention(o)),
        h('div', { class: 'ov-side' }, agents(o), weekHost, chainCard))));
    body.setAttribute('aria-busy', 'false');
  };
  const refreshTiles = () => {
    if (!data) return;
    const t = body.querySelector('.today');
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
      if (quiet && body.querySelector('.ov .today')) {
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
      return page.data;
    } catch {
      /* the card keeps its empty line */
      return null;
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

  let settle = 0;
  const settleSoon = () => {
    clearTimeout(settle);
    settle = window.setTimeout(() => void load(true), SETTLE_MS);
  };

  // The live view: history, then the stream; while the stream isn't live, a 5 s poll.
  const goLive = async () => {
    if (actionsSlot) actionsSlot.hidden = false;
    await load();
    void inbox.reload();
    void loadHistory();
    void loadChain();
    timer = window.setInterval(() => void load(true), REFRESH_MS);
    const s = stream();
    s.on('action.*', (ev) => {
      const o = ev.data.object as Partial<Action>;
      if (ev.hydrated && o.object === 'action') upsertLive(o as Action, true);
      if (data && applyLive(data, ev, seen)) refreshTiles();
      if (ev.type === 'action.created' || ev.type === 'action.decided') settleSoon();
    });
    s.on('approval.*', (ev) => {
      if (data && applyLive(data, ev, seen)) refreshTiles();
      void inbox.reload();
    });
    s.onResync(() => {
      void load(true);
      void inbox.reload();
      toast(copy.states.resync);
    });
    let poll = 0;
    const polling = (on: boolean) => {
      pollNote.hidden = !on;
      if (on && !poll) {
        poll = window.setInterval(async () => {
          const before = new Set<string>();
          feed.el.querySelectorAll('[data-id]').forEach((li) => before.add(li.getAttribute('data-id')!));
          try {
            const page = await api<ListResponse<Action>>('GET', '/v1/actions', { query: { limit: String(LIVE_ROWS) } });
            const fresh = page.data.filter((a) => !before.has(a.id));
            for (const a of [...page.data].reverse()) upsertLive(a, !before.has(a.id));
            if (fresh.length) void load(true);
          } catch {
            /* the offline banner says why */
          }
          void inbox.reload();
        }, POLL_MS);
      } else if (!on && poll) {
        clearInterval(poll);
        poll = 0;
      }
    };
    s.onState((st) => polling(st !== 'live'));
    // Give the stream a moment to connect before falling back.
    window.setTimeout(() => polling(s.state() !== 'live'), POLL_MS);
  };

  document.addEventListener('dg:ready', async () => {
    setLede();
    // No actions yet → one next step, until the first one arrives.
    let none = false;
    try {
      const page = await api<ListResponse<Action>>('GET', '/v1/actions', { query: { limit: '1' } });
      none = page.data.length === 0;
    } catch {
      none = false;
    }
    if (none) {
      clearTimeout(slow);
      if (actionsSlot) actionsSlot.hidden = true;
      mountEmpty(body, () => {
        void goLive();
      });
      return;
    }
    await goLive();
  }, { once: true });
  window.addEventListener('dg:offline', () => setLede(true));
  window.addEventListener('dg:online', () => setLede(false));
  window.addEventListener('pagehide', () => clearInterval(timer));
}

// ---------- empty: one next step ----------

function mountEmpty(body: HTMLElement, onFirst: () => void): void {
  const e = lc.empty;
  const markHost = h('span', { class: 'empty-mark', 'aria-hidden': 'true' });
  const other = h('div', { class: 'empty-other-body' });
  const details = h('details', { class: 'empty-other' }, h('summary', null, e.other), other);
  let mounted = false;
  details.addEventListener('toggle', () => {
    if (details.open && !mounted) {
      mounted = true;
      mountFirstRun(other);
    }
  });
  body.replaceChildren(h('section', { class: 'card empty-hero', 'aria-labelledby': 'empty-t', 'data-empty': '' },
    markHost,
    h('h2', { class: 't-title-2', id: 'empty-t' }, e.title),
    h('p', { class: 'lede' }, e.lede),
    h('div', { class: 'empty-step' },
      h('p', { class: 'empty-step-label' }, e.step),
      h('div', { class: 'empty-cmd' },
        h('code', { class: 'mono', translate: 'no' }, h('span', { class: 'prompt', 'aria-hidden': 'true' }, '$ '), e.command),
        h('button', { type: 'button', class: 'btn btn-brand', 'data-copy': e.command, 'aria-label': `${e.copy}: ${e.command}` }, icon('copy', 16), e.copy))),
    h('p', { class: 't-footnote t-2 empty-after' }, e.after),
    h('p', { class: 'empty-listen', role: 'status' }, h('span', { class: 'live-dot' }), e.listening),
    details));
  body.setAttribute('aria-busy', 'false');
  mountMirror(markHost, { size: 48, state: 'open' });
  let done = false;
  const first = () => {
    if (done) return;
    done = true;
    clearInterval(poll);
    off();
    onFirst();
  };
  const s = stream();
  const off = s.on('action.*', () => first());
  const poll = window.setInterval(async () => {
    if (s.state() === 'live') return;
    try {
      const page = await api<ListResponse<Action>>('GET', '/v1/actions', { query: { limit: '1' } });
      if (page.data.length) first();
    } catch {
      /* keep waiting */
    }
  }, POLL_MS);
}

// ---------- first run (connecting your own agent) ----------

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
  body.replaceChildren(h('section', { class: 'first-run', 'aria-labelledby': 'fr-t' },
    h('div', { class: 'fr-head' }, h('h2', { class: 't-title-3', id: 'fr-t' }, copy.firstRun.title), h('span', { class: 't-2' }, `· ${copy.firstRun.lede}`)),
    h('ol', { class: 'fr-steps' },
      step(1, copy.firstRun.step1, h('div', { class: 'fr-row' }, create, s1status), admin ? null : h('p', { class: 't-footnote t-2', id: 'fr-viewer' }, copy.firstRun.step1Viewer)),
      step(2, copy.firstRun.step2, step2Note, snip.el),
      step(3, copy.firstRun.step3, h('div', { class: 'fr-row' }, markHost, s3status)))));
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
