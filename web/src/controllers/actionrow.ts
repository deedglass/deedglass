// actionrow.ts: one action as a row, shared by the Actions feed, the Overview's live list and an
// agent's last 20 (S25 §3.4). The whole row is a link through the tool cell (a stretched ::after),
// so middle-click and ⌘-click open a tab. Verdicts always carry their level. The Mirror shows only
// on rows witnessed while the page was open (aurora stays out of history, brand §6.2).

import { mountMirror, type ConsoleMirrorState } from '@verify/ui/mirror.ts';
import { decisionChip, relTime, stateChip, verdictChip } from '../lib/chips.ts';
import { copy } from '../lib/copy.ts';
import { h, icon, type Child } from '../lib/dom.ts';
import { decisionOf } from '../lib/filters.ts';
import { duration } from '../lib/format.ts';
import { SealGovernor } from '../lib/seal.ts';
import type { Action } from '../lib/types.ts';
import { visibleControls } from './components.ts';

export const governor = new SealGovernor();

export function actionHref(a: Pick<Action, 'id'>): string {
  return `/app/actions/${a.id}`;
}

/** verdictCell: [Mirror 16] L2 · VERIFIED, or ◐ witnessing, or "Not executed" (never a bare dash). */
export function verdictCell(a: Action, live = false): HTMLElement {
  const wrap = h('span', { class: 'verdict-cell' });
  if (a.witness && a.state !== 'witnessing') {
    const target: ConsoleMirrorState = a.witness.verdict === 'VERIFIED' ? 'sealed' : a.witness.verdict === 'MISMATCH' ? 'mismatch' : 'unverifiable';
    if (live && (target === 'sealed' || target === 'mismatch')) {
      const host = h('span', { class: 'mark', 'aria-hidden': 'true' });
      const m = mountMirror(host, { size: 16, state: 'open' });
      // The Mirror plays its moment and then leaves the row, so a busy feed doesn't fill with aurora.
      void m.set(target, { animate: target !== 'sealed' || governor.mayPlay(a.deed ?? a.id, true) }).then(() => {
        setTimeout(() => {
          host.classList.add('leaving');
          setTimeout(() => host.remove(), 200);
        }, 1400);
      });
      wrap.append(host);
    }
    wrap.append(verdictChip(a.witness.verdict, a.witness.level));
    return wrap;
  }
  if (a.state === 'failed' || a.execution?.status === 'failed') {
    wrap.append(stateChip('failed'));
    return wrap;
  }
  if (a.state === 'witnessing' || a.state === 'executed') {
    wrap.append(h('span', { class: 'chip chip-pending' }, icon('witness', 16), copy.feed.witnessing));
    return wrap;
  }
  if (a.state === 'executing' || a.state === 'received' || a.state === 'authenticated' || a.state === 'evaluated' || a.state === 'pending_approval') {
    wrap.append(stateChip(a.state));
    return wrap;
  }
  wrap.append(h('span', { class: 't-2 not-executed' }, copy.feed.notExecuted));
  return wrap;
}

export function decisionCell(a: Action): Child {
  const d = decisionOf(a);
  return d ? decisionChip(d) : stateChip(a.state);
}

export function latency(a: Action): string {
  const ms = a.execution?.latency_ms;
  return typeof ms === 'number' ? duration(ms) : '';
}

/** feedRow: the Actions table row (36 px; a card under 640 px). */
export function feedRow(a: Action, tz: string, live = false): HTMLTableRowElement {
  const tr = h('tr', { 'data-row': '', 'data-id': a.id, 'aria-selected': 'false' },
    h('td', { class: 'c-time', 'data-card': 'meta' }, relTime(a.created_at, tz)),
    h('td', { class: 'c-agent', 'data-card': 'meta' }, h('a', { href: `/app/agents/${a.agent.id}`, class: 'agentlink', translate: 'no' }, visibleControls(a.agent.name))),
    h('td', { class: 'c-tool' }, h('a', { href: actionHref(a), class: 'tool rowlink', translate: 'no' }, visibleControls(a.tool))),
    h('td', { class: 'c-decision', 'data-card': 'meta' }, decisionCell(a)),
    h('td', { class: 'c-verdict', 'data-card': 'top' }, verdictCell(a, live)),
    h('td', { class: 'c-latency num', 'data-card': 'hide' }, latency(a)),
    h('td', { class: 'c-obo', 'data-card': 'hide' }, a.on_behalf_of ? h('span', { class: 'mono t-2', translate: 'no' }, visibleControls(a.on_behalf_of)) : ''),
    // Under 640 px the row is this one card (the cells above are hidden): line 1 the tool and the
    // verdict, line 2 agent · decision · time (S25 §3.4).
    h('td', { class: 'c-card' },
      h('span', { class: 'card-l1' }, h('a', { href: actionHref(a), class: 'tool', translate: 'no', tabindex: -1, 'aria-hidden': 'true' }, visibleControls(a.tool)), verdictCell(a)),
      h('span', { class: 'card-l2' }, h('span', null, visibleControls(a.agent.name)), decisionCell(a), relTime(a.created_at, tz))));
  if (live) {
    tr.classList.add('arrived');
    tr.setAttribute('data-live', ''); // arrived while the page was open (kept through updates)
  }
  return tr;
}

/** miniRow: the Overview's compact live row: tool, agent, verdict, time. */
export function miniRow(a: Action, tz: string, live = false): HTMLLIElement {
  const li = h('li', { 'data-id': a.id, class: live ? 'arrived' : '', ...(live ? { 'data-live': '' } : {}) },
    h('a', { href: actionHref(a), class: 'mini-link' },
      h('span', { class: 'mini-tool mono', translate: 'no' }, visibleControls(a.tool)),
      h('span', { class: 'mini-agent t-2' }, visibleControls(a.agent.name))),
    h('span', { class: 'mini-verdict' }, a.witness || a.state === 'witnessing' ? verdictCell(a, live) : decisionCell(a)),
    h('span', { class: 'mini-time' }, relTime(a.created_at, tz)));
  return li;
}

/** updateRow refreshes a feed row in place after a lifecycle event: decision, verdict, latency and
 *  the phone card. The verdict cell gets the live treatment (the Mirror for a witnessed verdict). */
export function updateRow(tr: HTMLTableRowElement, a: Action, tz: string): void {
  tr.classList.remove('arrived');
  tr.classList.add('changed');
  tr.querySelector('.c-decision')?.replaceChildren(decisionCell(a) as Node);
  tr.querySelector('.c-verdict')?.replaceChildren(verdictCell(a, tr.hasAttribute('data-live')));
  const lat = tr.querySelector('.c-latency');
  if (lat) lat.textContent = latency(a);
  const l1 = tr.querySelector('.c-card .card-l1');
  if (l1 && l1.lastElementChild) l1.lastElementChild.replaceWith(verdictCell(a));
  const l2 = tr.querySelector('.c-card .card-l2');
  if (l2 && l2.children[1]) l2.children[1].replaceWith(decisionCell(a) as Node);
  void tz;
}
