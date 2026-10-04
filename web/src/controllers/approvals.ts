// approvals.ts: the Approvals inbox (S26 §3.1), designed at 360 px first. Under 1024 px the list and
// the detail are separate screens (the detail has a sticky decision bar above the bottom bar); from
// 1024 px they sit side by side (a 400 px list and the detail). /app/approvals and
// /app/approvals/{apr_id} both mount this controller, and selection changes the URL with
// pushState, so a reload or a shared link lands on the same approval.
//
// Rules that hold here: no decision without the DecisionSheet; Deny always needs a reason; a race
// is never silent; decisions are never queued offline; the headline comes from S22's server-computed
// context, and the agent's arguments are shown as data in a labelled region.

import { api, ApiError } from '../lib/api.ts';
import { relTime, verdictChip } from '../lib/chips.ts';
import { copy, fill } from '../lib/copy.ts';
import { approvalsCopy as c } from '../lib/copy-approvals.ts';
import { h, icon, maybe, replace, type Child } from '../lib/dom.ts';
import { dateTime, number, plural, time } from '../lib/format.ts';
import {
  clockOf, decideState, effectLine, fourEyes, raceSentence, sortPending, sqlOf, voteCount, votes, whereOf,
  type ApprovalFull, type DecideState,
} from '../lib/models/approval.ts';
import { get } from '../lib/state.ts';
import { stream } from '../lib/stream.ts';
import type { Action, ListResponse, Level, Verdict } from '../lib/types.ts';
import { codeBlock, segmented, select } from './components.ts';
import { Countdown } from './countdown.ts';
import { openDecisionSheet, type Vote } from './decisionsheet.ts';
import { jsonView } from './jsonview.ts';
import { emptyState, errorState, notFound, skeleton } from './screens.ts';
import { announce } from './shell.ts';
import { toast } from './toast.ts';
import { mountMirror } from '@verify/ui/mirror.ts';

type Tab = 'pending' | 'history';
const ID_RE = /^apr_[0-7][0-9a-hjkmnp-tv-z]{25}$/;
const tz = () => get()?.org.timezone ?? 'UTC';
const meId = () => get()?.me.user.id ?? null;
const wide = () => matchMedia('(min-width: 1024px)').matches;

interface Follow {
  text: () => Child[]; // a factory: the card and the bar each get their own nodes
  done: boolean;
}

interface Model {
  tab: Tab;
  pending: ApprovalFull[];
  history: ApprovalFull[];
  selected: string | null;
  detail: ApprovalFull | null;
  race: Map<string, string>; // id → the race sentence (decided by someone else while open)
  follow: Map<string, Follow>; // id → follow-through line after my decision
  agent: string;
  status: string;
}

function statusChip(a: ApprovalFull): HTMLElement {
  switch (a.status) {
    case 'approved':
      return h('span', { class: 'chip chip-neutral' }, icon('check', 16), c.filters.approved);
    case 'denied':
      return h('span', { class: 'chip chip-denied' }, icon('ban', 16), c.filters.denied);
    case 'expired':
      return h('span', { class: 'chip chip-outline' }, icon('clock', 16), c.filters.expired);
    case 'cancelled':
      return h('span', { class: 'chip chip-outline' }, icon('circle-slash', 16), c.filters.cancelled);
    default:
      return h('span', { class: 'chip chip-pending' }, icon('contrast', 16), copy.chips.pending);
  }
}

function countdown(a: ApprovalFull, onExpire: () => void, speak = false): HTMLElement {
  // Only the open approval's countdown speaks (once, at 1 minute); the cards stay quiet.
  const cd = Countdown(a.expires_at, { onExpire, announce: speak ? () => announce(fill(c.oneMinute, { tool: a.tool })) : () => {} });
  return cd.el;
}

// ---------- the card ----------

function card(a: ApprovalFull, m: Model, onExpire: () => void): HTMLElement {
  const obo = a.context?.on_behalf_of;
  const fe = fourEyes(a);
  const race = m.race.get(a.id);
  const follow = m.follow.get(a.id);
  const right: Child = a.status === 'pending' && !race && !follow ? h('span', { class: 'apv-count' }, countdown(a, onExpire)) : statusChip(a);
  const el = h('a', {
    class: 'apv-card', href: `/app/approvals/${a.id}`, 'data-id': a.id,
    'aria-current': m.selected === a.id ? 'true' : 'false',
  },
  h('span', { class: 'apv-l1' },
    a.status === 'pending' ? h('span', { class: 'apv-glyph', 'aria-hidden': 'true' }, '◐') : null,
    h('span', { class: 'mono apv-tool', translate: 'no' }, a.tool), right),
  h('span', { class: 'apv-l2' }, effectLine(a)),
  h('span', { class: 'apv-l3' }, h('span', { translate: 'no' }, a.agent.name), obo ? h('span', null, ' · ', h('span', { class: 'mono', translate: 'no' }, obo)) : null),
  h('span', { class: 'apv-l4' }, a.rule_id ? h('span', { class: 'mono', translate: 'no' }, a.rule_id) : null, fe ? h('span', { class: 'apv-fe' }, ` · ${fill(c.fourEyes, { n: voteCount(a), required: a.required })}`) : null),
  race ? h('span', { class: 'apv-note' }, race) : null,
  follow ? h('span', { class: 'apv-note apv-follow' }, ...follow.text()) : null);
  return el;
}

// ---------- the detail ----------

function recentList(a: ApprovalFull): HTMLElement {
  const rec = (a.context?.agent.recent ?? []).slice(0, 10);
  if (!rec.length) return h('p', { class: 't-2' }, c.detail.recentNone);
  return h('ul', { class: 'apv-recent' }, ...rec.map((r) => h('li', null,
    h('a', { href: `/app/actions/${r.action}`, class: 'mono', translate: 'no' }, r.tool),
    h('span', { class: 'apv-recent-v' }, r.verdict ? verdictChip(r.verdict as Verdict, (r.level ?? null) as Level | null) : h('span', { class: 't-2' }, r.status)),
    relTime(r.at, tz()))));
}

function decisionsList(a: ApprovalFull): HTMLElement {
  const v = votes(a);
  const items: Child[] = v.map((x) => h('li', null,
    h('span', { class: 'apv-vote-who' }, icon('check', 16), x.user.name),
    h('span', { class: 't-2' }, `${clockOf(x.at, tz())} · ${x.via}`),
    x.reason ? h('q', { class: 'apv-vote-why' }, x.reason) : null));
  if (a.status === 'denied' && a.denied_by) {
    items.push(h('li', null, h('span', { class: 'apv-vote-who' }, icon('ban', 16), a.denied_by.name), a.decided_at ? h('span', { class: 't-2' }, clockOf(a.decided_at, tz())) : null,
      a.decision_reason ? h('q', { class: 'apv-vote-why' }, a.decision_reason) : null));
  }
  const fe = fourEyes(a);
  return h('div', null,
    fe ? h('p', { class: 'apv-fe-line' }, h('span', { class: 'apv-glyph', 'aria-hidden': 'true' }, '◐'), fe) : null,
    items.length ? h('ul', { class: 'apv-votes' }, ...items) : h('p', { class: 't-2' }, c.detail.decisionsNone));
}

function section(title: string, ...body: Child[]): HTMLElement {
  return h('section', { class: 'apv-sec' }, h('h3', { class: 'apv-sec-t' }, title), ...body);
}

function detailView(a: ApprovalFull, m: Model, act: { decide: (v: Vote) => void; reload: () => void; expire: () => void }): HTMLElement {
  const ctx = a.context;
  const sql = sqlOf(a);
  const obo = ctx?.on_behalf_of;
  const st = m.race.get(a.id) ? ({ kind: 'closed', text: m.race.get(a.id)! } as DecideState) : decideState(a, meId() ? { id: meId()! } : null, new Date(), tz());
  const follow = m.follow.get(a.id);
  const facts: [string, Child][] = [
    [c.detail.agent, h('a', { href: `/app/agents/${a.agent.id}`, translate: 'no' }, a.agent.name)],
    ...(obo ? [[c.detail.onBehalf, h('span', { class: 'mono', translate: 'no' }, obo)] as [string, Child]] : []),
    ...(ctx?.session ? [[c.detail.session, h('span', { class: 'mono', translate: 'no' }, ctx.session)] as [string, Child]] : []),
    [c.detail.action, h('a', { href: `/app/actions/${a.action}`, class: 'mono', translate: 'no' }, a.action)],
    [c.detail.requested, h('span', { class: 'num' }, dateTime(a.created_at, tz()))],
  ];
  const head = h('header', { class: 'apv-head' },
    h('p', { class: 'apv-kicker' }, a.status === 'pending' ? h('span', { class: 'chip chip-pending' }, icon('contrast', 16), copy.chips.pending) : statusChip(a),
      h('span', { class: 'mono', translate: 'no' }, a.tool),
      a.status === 'pending' ? h('span', { class: 'apv-count' }, countdown(a, act.expire, true)) : null),
    h('h2', { class: 't-title-3 apv-effect', id: 'apv-title', tabindex: -1 }, effectLine(a)),
    h('p', { class: 'apv-summary t-2' }, a.summary),
    a.required >= 2 && a.status === 'pending' ? h('p', { class: 'apv-fe-head' }, h('span', { class: 'apv-glyph', 'aria-hidden': 'true' }, '◐'), fourEyes(a)) : null);
  const rule = section(c.detail.rule,
    h('p', { class: 'apv-rule' }, h('span', { class: 'mono', translate: 'no' }, a.rule_id ?? '·'),
      ctx?.policy ? h('span', { class: 't-2' }, ` · ${fill(c.detail.ruleVersion, { policy: ctx.policy.name, version: ctx.policy.version })}`) : null),
    a.reason ? h('blockquote', { class: 'apv-reason' }, a.reason) : null);
  const sqlSec = sql ? section(c.detail.statement,
    codeBlock('sql', sql.statement || '·', c.detail.statement),
    h('dl', { class: 'kv apv-sqlkv' },
      h('dt', null, c.detail.kind), h('dd', { class: 'mono' }, sql.kind.toUpperCase()),
      h('dt', null, c.detail.tables), h('dd', { class: 'mono', translate: 'no' }, sql.tables.join(', ') || '·'),
      h('dt', null, c.detail.where), h('dd', { class: 'mono', translate: 'no' }, (sql.hasWhere && whereOf(sql.statement)) || c.detail.noWhere),
      ...(sql.estimate ? [h('dt', null, 'Estimate'), h('dd', { class: 'num' }, sql.estimate)] : []))) : null;
  const args = section(c.detail.arguments,
    h('div', { class: 'apv-args', role: 'region', 'aria-label': c.detail.arguments, tabindex: 0 }, jsonView(ctx?.args ?? {})),
    h('p', { class: 't-footnote t-2' }, ctx?.args_note ?? c.detail.argsNote));
  const exp = a.status === 'pending'
    ? h('p', { class: 'apv-expiry t-2' }, fill(c.detail.expires, { time: time(a.expires_at, tz()), rel: relative(a.expires_at) }))
    : a.status === 'expired' ? h('p', { class: 'apv-expiry t-2' }, fill(c.expiredAt, { time: time(a.expires_at, tz()) })) : null;
  const bar = decisionBar(a, st, follow, act);
  return h('article', { class: 'apv-detail', 'aria-labelledby': 'apv-title', 'data-approval': a.id },
    h('a', { class: 'btn btn-quiet btn-sm apv-back', href: '/app/approvals' }, icon('chevron-left', 16), c.detail.back),
    head,
    h('dl', { class: 'kv apv-facts' }, ...facts.flatMap(([k, v]) => [h('dt', null, k), h('dd', null, v)])),
    rule, sqlSec, args,
    section(fill(c.detail.recent, { agent: a.agent.name }), recentList(a)),
    section(c.detail.decisions, decisionsList(a)),
    exp, bar);
}

function relative(iso: string): string {
  const ms = new Date(iso).getTime() - Date.now();
  const min = Math.max(0, Math.round(ms / 60000));
  return ms <= 0 ? c.rel.now : min < 1 ? c.rel.underMinute : fill(c.rel.inMin, { n: plural(min, 'min', 'min') });
}

function decisionBar(a: ApprovalFull, st: DecideState, follow: Follow | undefined, act: { decide: (v: Vote) => void }): HTMLElement {
  const bar = h('div', { class: 'apv-bar', role: 'group', 'aria-label': 'Decision' });
  if (follow) {
    bar.append(h('p', { class: 'apv-bar-note' }, ...follow.text()));
    return bar;
  }
  if (st.kind === 'can' || st.kind === 'self') {
    const offline = !navigator.onLine;
    if (st.kind === 'self') bar.append(h('p', { class: 'apv-bar-note' }, st.text));
    if (offline) bar.append(h('p', { class: 'apv-bar-note', role: 'status' }, icon('wifi-off', 16), c.offline));
    const btns = h('div', { class: 'apv-bar-btns' },
      h('button', { type: 'button', class: 'btn btn-danger-outline btn-lg', 'data-vote': 'deny', 'aria-keyshortcuts': 'd', ...(offline ? { disabled: true } : {}), onclick: () => act.decide('deny') }, c.deny),
      st.kind === 'can' ? h('button', { type: 'button', class: 'btn btn-primary btn-lg', 'data-vote': 'approve', 'aria-keyshortcuts': 'a', ...(offline ? { disabled: true } : {}), onclick: () => act.decide('approve') }, c.approve) : null);
    bar.append(btns);
    return bar;
  }
  bar.append(h('p', { class: 'apv-bar-note', role: 'status' }, st.text));
  return bar;
}

// ---------- the screen ----------

export function mountApprovals(): void {
  const root = maybe('apv-root');
  const listHost = maybe('apv-list');
  const detailHost = maybe('apv-detail');
  const tabsHost = maybe('apv-tabs');
  if (!root || !listHost || !detailHost || !tabsHost) return;
  const fromPath = () => {
    const last = location.pathname.split('/').filter(Boolean).pop() ?? '';
    return ID_RE.test(last) ? last : null;
  };
  const params = new URLSearchParams(location.search);
  const m: Model = {
    tab: params.get('tab') === 'history' ? 'history' : 'pending', pending: [], history: [], selected: fromPath(), detail: null,
    race: new Map(), follow: new Map(), agent: params.get('agent') ?? '', status: params.get('status') ?? '',
  };
  let loadedOnce = false;
  let myVotes = new Set<string>(); // approvals I just decided (their events aren't races)

  const syncURL = (push: boolean) => {
    const q = new URLSearchParams();
    if (m.tab === 'history') q.set('tab', 'history');
    if (m.tab === 'history' && m.agent) q.set('agent', m.agent);
    if (m.tab === 'history' && m.status) q.set('status', m.status);
    const path = m.selected ? `/app/approvals/${m.selected}` : '/app/approvals';
    const url = q.toString() ? `${path}?${q}` : path;
    if (url === location.pathname + location.search) return;
    if (push) history.pushState(null, '', url);
    else history.replaceState(null, '', url);
  };

  const listOf = () => (m.tab === 'pending' ? m.pending : m.history);

  const renderTabs = () => {
    const n = m.pending.length;
    const seg = segmented<Tab>(c.tabsLabel, [
      { value: 'pending', label: n ? `${c.tabs.pending} ${number(n)}` : c.tabs.pending },
      { value: 'history', label: c.tabs.history },
    ], m.tab, (v) => {
      m.tab = v;
      syncURL(false);
      renderTabs();
      void load();
    }, 'apv-tab');
    const filters: Child[] = [];
    if (m.tab === 'history') {
      const agents = [...new Map([...m.history, ...m.pending].map((a) => [a.agent.id, a.agent.name])).entries()];
      filters.push(
        select(c.filters.status, [{ value: '', label: c.filters.all }, { value: 'approved', label: c.filters.approved }, { value: 'denied', label: c.filters.denied }, { value: 'expired', label: c.filters.expired }, { value: 'cancelled', label: c.filters.cancelled }], m.status, (v) => { m.status = v; syncURL(false); void load(); }, 'apv-status'),
        select(c.filters.agent, [{ value: '', label: c.filters.any }, ...agents.map(([id, name]) => ({ value: id, label: name }))], m.agent, (v) => { m.agent = v; syncURL(false); void load(); }, 'apv-agent'));
    }
    replace(tabsHost, seg, filters.length ? h('div', { class: 'apv-filters' }, ...filters) : null);
  };

  const renderList = () => {
    const list = listOf();
    if (!list.length) {
      listHost.replaceChildren(m.tab === 'pending'
        ? emptyState(c.empty, 'stamp', undefined, 'https://docs.deedglass.com/approvals')
        : emptyState(m.agent || m.status ? c.historyFilteredEmpty : c.historyEmpty, 'history'));
      return;
    }
    const ul = h('ul', { class: 'apv-cards', 'aria-label': m.tab === 'pending' ? c.tabs.pending : c.tabs.history },
      ...list.map((a) => h('li', null, card(a, m, () => void refreshOne(a.id)))));
    listHost.replaceChildren(ul);
    if (m.tab === 'pending') listHost.append(h('p', { class: 't-footnote t-2 apv-keys' }, c.keysHint));
  };

  const renderDetail = () => {
    root.dataset.selected = m.selected ? 'true' : 'false';
    if (!m.selected) {
      detailHost.replaceChildren(h('div', { class: 'apv-placeholder t-2' }, icon('stamp', 20), h('p', null, c.detail.select)));
      return;
    }
    if (!m.detail || m.detail.id !== m.selected) {
      detailHost.replaceChildren(h('div', { class: 'card card-pad', 'aria-busy': 'true' }, ...[0, 1, 2, 3, 4].map(() => h('span', { class: 'skel skel-line skel-gap' }))));
      return;
    }
    detailHost.replaceChildren(detailView(m.detail, m, { decide, reload: () => void pick(m.selected, false, true), expire: () => void refreshOne(m.detail!.id) }));
  };

  const load = async (quiet = false) => {
    if (!quiet && !loadedOnce) listHost.replaceChildren(skeleton(4));
    try {
      const pend = await api<ListResponse<ApprovalFull>>('GET', '/v1/approvals', { query: { status: 'pending', limit: '100' } });
      m.pending = sortPending(pend.data);
      if (m.tab === 'history') {
        const st = m.status || 'approved,denied,expired,cancelled';
        const hist = await api<ListResponse<ApprovalFull>>('GET', '/v1/approvals', { query: { status: st, limit: '50', agent: m.agent || undefined } });
        m.history = hist.data;
      }
      loadedOnce = true;
      // Keep cards that are mid follow-through or showing a race in place until they settle.
      for (const id of [...m.follow.keys(), ...m.race.keys()]) {
        if (!m.pending.some((x) => x.id === id) && m.detail?.id === id && m.tab === 'pending' && !m.follow.get(id)?.done) m.pending.push(m.detail);
      }
      renderTabs();
      renderList();
      if (!m.selected && wide() && m.tab === 'pending' && m.pending[0]) void pick(m.pending[0].id, false);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) listHost.replaceChildren(errorState(e, () => void load()));
    }
  };

  const pick = async (id: string | null, push: boolean, force = false) => {
    m.selected = id;
    syncURL(push);
    for (const el of listHost.querySelectorAll<HTMLElement>('.apv-card')) el.setAttribute('aria-current', el.dataset.id === id ? 'true' : 'false');
    if (!id) {
      m.detail = null;
      renderDetail();
      return;
    }
    if (force || !m.detail || m.detail.id !== id) {
      const cached = [...m.pending, ...m.history].find((x) => x.id === id);
      m.detail = cached && !force ? cached : null;
      renderDetail();
      try {
        m.detail = await api<ApprovalFull>('GET', `/v1/approvals/${id}`);
      } catch (e) {
        if (e instanceof ApiError && e.status === 404) {
          detailHost.replaceChildren(notFound(c.notFound));
          return;
        }
        detailHost.replaceChildren(errorState(e, () => void pick(id, false, true)));
        return;
      }
    }
    renderDetail();
    if (push && !wide()) {
      scrollTo({ top: 0 });
      maybe('apv-title')?.focus({ preventScroll: true }); // the list column is hidden now
    }
  };

  const refreshOne = async (id: string) => {
    try {
      const a = await api<ApprovalFull>('GET', `/v1/approvals/${id}`);
      if (m.detail?.id === id) m.detail = a;
      m.pending = m.pending.map((x) => (x.id === id ? a : x));
      renderList();
      if (m.selected === id) renderDetail();
    } catch {
      /* the next refresh catches up */
    }
  };

  // ---------- deciding and following through ----------

  const settle = (id: string, ms = 10_000) => {
    setTimeout(() => {
      if (!m.follow.get(id)?.done) m.follow.delete(id);
      if (m.selected !== id) { m.follow.delete(id); m.race.delete(id); }
      m.pending = m.pending.filter((x) => x.id !== id || x.status === 'pending');
      renderList();
      renderTabs();
      if (m.selected === id) renderDetail();
    }, ms);
  };

  const setFollow = (id: string, text: () => Child[], done = false) => {
    m.follow.set(id, { text, done });
    renderList();
    if (m.selected === id) renderDetail();
  };

  const followAction = (ap: ApprovalFull) => {
    const actId = ap.action;
    setFollow(ap.id, () => [h('span', { class: 'apv-glyph', 'aria-hidden': 'true' }, '◐'), c.afterApprove]);
    let finished = false;
    const show = (a: Action) => {
      if (finished) return;
      if (a.state === 'executing') { if (!m.follow.get(ap.id)?.text().some((x) => x === c.running)) announce(c.running); return setFollow(ap.id, () => [h('span', { class: 'apv-glyph', 'aria-hidden': 'true' }, '◐'), c.running]); }
      if (a.state === 'failed' || (a.state === 'sealed' && a.execution?.status === 'failed')) {
        finished = true;
        setFollow(ap.id, () => [icon('circle-alert', 16), c.failedAfter, ' ', h('a', { href: `/app/actions/${actId}` }, c.detail.viewAction)], true);
        settle(ap.id);
        return;
      }
      if (a.state === 'sealed' && a.witness) {
        finished = true;
        const w = a.witness;
        const target = w.verdict === 'VERIFIED' ? 'sealed' : w.verdict === 'MISMATCH' ? 'mismatch' : 'unverifiable';
        let played = false; // Sealed plays once, in the first place it's drawn
        setFollow(ap.id, () => {
          const mark = h('span', { class: 'apv-mark', 'aria-hidden': 'true' });
          const mm = mountMirror(mark, { size: 20, state: played ? target : 'open' });
          if (!played) void mm.set(target, { animate: !matchMedia('(prefers-reduced-motion: reduce)').matches });
          played = true;
          return [mark, `${c.executed} · `, verdictChip(w.verdict, w.level), ` · ${c.sealed}`, h('a', { class: 'apv-view', href: `/app/actions/${actId}` }, c.detail.viewAction)];
        }, true);
        announce(`${c.executed} · ${a.witness.level} · ${a.witness.verdict} · ${c.sealed}`);
        settle(ap.id);
        return;
      }
      if (a.state === 'executed' || a.state === 'witnessing') setFollow(ap.id, () => [h('span', { class: 'apv-glyph', 'aria-hidden': 'true' }, '◐'), `${c.executed} · ${c.witnessing}`]);
    };
    const poll = async () => {
      for (let i = 0; i < 40 && !finished; i++) {
        try {
          show(await api<Action>('GET', `/v1/actions/${actId}`, { query: { wait: '2' } }));
        } catch {
          /* keep going; events may still arrive */
        }
        if (!finished) await new Promise((r) => setTimeout(r, 500));
      }
    };
    void poll();
  };

  const decide = (vote: Vote) => {
    const a = m.detail;
    if (!a) return;
    const st = decideState(a, meId() ? { id: meId()! } : null, new Date(), tz());
    if (st.kind !== 'can' && !(st.kind === 'self' && vote === 'deny')) return;
    openDecisionSheet({
      approval: a, vote, effect: effectLine(a),
      onDone: (res) => {
        myVotes.add(res.id);
        m.detail = res;
        m.pending = m.pending.map((x) => (x.id === res.id ? res : x));
        if (res.status === 'approved') followAction(res);
        else if (res.status === 'denied') {
          setFollow(res.id, () => [icon('ban', 16), c.denied], true);
          settle(res.id);
        } else {
          renderList();
          renderDetail();
        }
        toast(vote === 'approve' ? (res.status === 'approved' ? c.afterApprove : fill(c.fourEyes, { n: voteCount(res), required: res.required })) : c.denied, { kind: 'success' });
      },
      onReload: () => void pick(a.id, false, true),
      onRace: (msg) => {
        m.race.set(a.id, msg);
        void refreshOne(a.id).then(() => {
          renderList();
          renderDetail();
        });
        settle(a.id);
      },
    });
  };

  // ---------- keyboard (desktop): ↑ ↓ move, A and D open the sheet; neither submits ----------

  document.addEventListener('keydown', (e) => {
    if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = e.target as HTMLElement;
    if (t.closest('input, textarea, select, [contenteditable], .sheet, .dialog, .palette, .apv-args')) return;
    if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && !(t === document.body || t.closest('#apv-list'))) return; // let the page scroll
    if (document.querySelector('.scrim')) return;
    const list = listOf();
    const i = list.findIndex((x) => x.id === m.selected);
    if (e.key === 'ArrowDown' || e.key === 'j') {
      const n = list[Math.min(list.length - 1, i + 1)];
      if (n) { e.preventDefault(); void pick(n.id, true).then(() => listHost.querySelector<HTMLElement>(`[data-id="${n.id}"]`)?.focus()); }
    } else if (e.key === 'ArrowUp' || e.key === 'k') {
      const n = list[Math.max(0, i - 1)];
      if (n) { e.preventDefault(); void pick(n.id, true).then(() => listHost.querySelector<HTMLElement>(`[data-id="${n.id}"]`)?.focus()); }
    } else if (e.key === 'a' || e.key === 'A') {
      if (m.detail?.status === 'pending') { e.preventDefault(); decide('approve'); }
    } else if (e.key === 'd' || e.key === 'D') {
      if (m.detail?.status === 'pending') { e.preventDefault(); decide('deny'); }
    }
  });

  listHost.addEventListener('click', (e) => {
    const a = (e.target as HTMLElement).closest<HTMLAnchorElement>('a.apv-card');
    if (!a || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    void pick(a.dataset.id ?? null, true);
  });
  addEventListener('popstate', () => {
    const p = new URLSearchParams(location.search);
    m.tab = p.get('tab') === 'history' ? 'history' : 'pending';
    renderTabs();
    void pick(fromPath(), false);
  });
  const net = () => { if (m.selected) renderDetail(); };
  addEventListener('online', net);
  addEventListener('offline', net);

  document.addEventListener('dg:ready', () => {
    void load();
    if (m.selected) void pick(m.selected, false);
    else renderDetail();
    const live = stream();
    let t = 0;
    live.on('approval.requested', (ev) => {
      const o = ev.data.object as { tool?: string; agent?: string };
      announce(fill(c.arrived, { tool: String(o.tool ?? ''), agent: '' }).replace(' by .', '.'));
      clearTimeout(t);
      t = window.setTimeout(() => void load(true), 250);
    });
    live.on('approval.decided', (ev) => {
      const o = ev.data.object as { id?: string; status?: string };
      const id = String(o.id ?? '');
      if (id && !myVotes.has(id) && o.status && o.status !== 'pending' && (m.pending.some((x) => x.id === id) || m.selected === id)) {
        void api<ApprovalFull>('GET', `/v1/approvals/${id}`).then((a) => {
          m.race.set(id, raceSentence(a));
          m.pending = m.pending.map((x) => (x.id === id ? a : x));
          if (m.detail?.id === id) m.detail = a;
          renderList();
          if (m.selected === id) renderDetail();
          settle(id, 20_000);
        }).catch(() => {});
        return;
      }
      clearTimeout(t);
      t = window.setTimeout(() => void load(true), 250);
    });
    live.onResync(() => void load(true));
  }, { once: true });
  document.addEventListener('dg:load-failed', (e) => listHost.replaceChildren(errorState((e as CustomEvent).detail, () => location.reload())), { once: true });
}
