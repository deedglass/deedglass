// livefeed.ts: Live activity (console-v1). Each agent action as it happens: the tool and the agent,
// a plain summary, the rule decision, the read-back result (Matches, Doesn't match, Can't check)
// and a link to the sealed record. New rows slide in at the top; an existing row updates in place
// as the action moves (decided → executed → witnessed → sealed). Screen readers hear at most one
// arrival every 5 s. The whole row opens the action (a stretched link on the tool).

import { relTime } from '../lib/chips.ts';
import { fill } from '../lib/copy.ts';
import { liveCopy as c } from '../lib/copy-live.ts';
import { h, icon } from '../lib/dom.ts';
import { outcome, plainSummary, recordState } from '../lib/live.ts';
import type { Action } from '../lib/types.ts';
import { actionHref, decisionCell, verdictCell } from './actionrow.ts';
import { visibleControls } from './components.ts';
import { announce } from './shell.ts';

export interface LiveFeed {
  el: HTMLUListElement;
  upsert: (a: Action, live: boolean) => void;
  has: (id: string) => boolean;
  size: () => number;
  onChange: (fn: () => void) => void;
}

const ANNOUNCE_MS = 5000;

function readback(a: Action, live: boolean): HTMLElement {
  if (a.witness || a.state === 'witnessing' || a.state === 'executed') return verdictCell(a, live);
  return h('span', { class: 'feed-nocheck t-2' }, '');
}

function record(a: Action): HTMLElement {
  const r = recordState(a);
  if (r.href) return h('a', { class: 'feed-record', href: r.href, 'data-record': '' }, icon('deed', 16), c.feed.record);
  return h('span', { class: 'feed-record is-wait t-2' }, r.label === 'sealing' ? c.feed.sealing : r.label === 'later' ? c.feed.later : c.feed.noRecord);
}

export function liveRow(a: Action, tz: string, live: boolean): HTMLLIElement {
  const o = outcome(a);
  return h('li', { class: `feed-item tone-${o.tone}${live ? ' arrived' : ''}`, 'data-id': a.id, 'data-state': a.state, ...(live ? { 'data-live': '' } : {}) },
    h('div', { class: 'feed-main' },
      h('p', { class: 'feed-line1' },
        h('a', { href: actionHref(a), class: 'feed-tool mono rowlink', translate: 'no' }, visibleControls(a.tool)),
        h('span', { class: 'feed-agent t-2' }, visibleControls(a.agent.name)),
        h('span', { class: 'feed-time' }, relTime(a.created_at, tz))),
      h('p', { class: 'feed-summary', 'data-summary': '' }, visibleControls(plainSummary(a)))),
    h('div', { class: 'feed-side' },
      h('span', { class: 'feed-decision', 'data-decision': '' }, decisionCell(a)),
      h('span', { class: 'feed-readback', 'data-readback': '' }, readback(a, live)),
      record(a)));
}

export function createLiveFeed(limit: number, tz: () => string): LiveFeed {
  const el = h('ul', { class: 'feed-list', id: 'live-feed', 'aria-label': c.feed.region });
  const fns: (() => void)[] = [];
  let lastSaid = 0;
  const upsert = (a: Action, live: boolean) => {
    const old = el.querySelector<HTMLLIElement>(`[data-id="${a.id}"]`);
    const row = liveRow(a, tz(), live && !old);
    if (old) {
      row.classList.remove('arrived');
      if (old.hasAttribute('data-live')) row.setAttribute('data-live', '');
      if (live) row.classList.add('changed');
      old.replaceWith(row);
    } else {
      // Keep newest first even when history and live rows interleave.
      const t = a.created_at;
      let before: Element | null = null;
      for (const li of Array.from(el.children)) {
        const ct = li.querySelector('time')?.getAttribute('datetime') ?? '';
        if (ct && ct < t) {
          before = li;
          break;
        }
      }
      el.insertBefore(row, before);
      while (el.children.length > limit) el.lastElementChild?.remove();
      if (live && Date.now() - lastSaid > ANNOUNCE_MS) {
        lastSaid = Date.now();
        announce(fill(c.feed.arrived, { tool: a.tool, agent: a.agent.name, summary: plainSummary(a) }));
      }
    }
    fns.forEach((f) => f());
  };
  return { el, upsert, has: (id) => !!el.querySelector(`[data-id="${id}"]`), size: () => el.children.length, onChange: (f) => fns.push(f) };
}
