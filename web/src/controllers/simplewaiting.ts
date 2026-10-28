// simplewaiting.ts: Waiting for you (DL-143). One calm card per request: who asked, what exactly
// will change (shown before any button), why it waits and how long is left, then Approve and save or
// Decline. Both go through S26's DecisionSheet: nothing is sent without its explicit step, a decline
// always carries a reason, one Idempotency-Key per sheet. After approving, the card follows the
// action through saved → read back → locked, from the server's own states.

import { api, ApiError } from '../lib/api.ts';
import { fill } from '../lib/copy.ts';
import { simpleCopy as sc } from '../lib/copy-simple.ts';
import { h, icon } from '../lib/dom.ts';
import { relative } from '../lib/format.ts';
import { sortPending, type ApprovalFull } from '../lib/models/approval.ts';
import { changeOf, headlineOf, resultOf } from '../lib/models/simple.ts';
import { stream } from '../lib/stream.ts';
import type { Action, ListResponse } from '../lib/types.ts';
import { kv } from './components.ts';
import { Countdown } from './countdown.ts';
import { openDecisionSheet, type Vote } from './decisionsheet.ts';
import { announce } from './shell.ts';
import { emptyBlock, errorBlock, resultPill } from './simplefeed.ts';
import { toast } from './toast.ts';

const c = sc.waiting;

const LABELS = {
  approve: c.approve, deny: c.decline, approving: c.approving, denying: c.declining,
  approveTitle: c.approveTitle, denyTitle: c.declineTitle, approveBody: '{effect}', denyBody: c.declineBody,
  approveReason: c.approveReason, denyReason: c.declineReason,
};

function whereOf(a: ApprovalFull): string {
  return a.context?.action.connector ?? a.tool.split('.')[0] ?? '';
}

export function mountWaiting(host: HTMLElement): void {
  const cards = h('div', { class: 'sv-cards' });
  const decided = new Map<string, HTMLElement>(); // cards that keep showing their follow-through
  let list: ApprovalFull[] = [];

  const card = (a: ApprovalFull): HTMLElement => {
    const ch = changeOf(a);
    const title = headlineOf(a);
    const hid = `sv-ap-${a.id}`;
    const can = a.you ? a.you.can_decide : true;
    const el = h('article', { class: 'sv-card', 'aria-labelledby': hid, 'data-approval': a.id });
    const change = h('section', { class: 'sv-change', 'aria-label': c.willChange },
      h('h3', null, c.willChange),
      ch.rows.length ? kv(ch.rows) : null,
      ch.body ? h(ch.bodyMono ? 'pre' : 'div', { class: `body${ch.bodyMono ? ' mono' : ''}`, tabindex: 0 }, ch.body) : null);
    const cd = Countdown(a.expires_at, { onExpire: () => void refresh() });
    const facts = kv([
      [c.who, a.agent.name + (a.context?.on_behalf_of ? ` · ${a.context.on_behalf_of}` : '')],
      [c.where, whereOf(a)],
      [c.why, a.reason ?? a.context?.policy.reason ?? a.rule_id ?? ''],
      [c.expires, cd.el],
    ], 'kv sv-facts');
    const decide = (vote: Vote) => openDecisionSheet({
      approval: a, vote, effect: title, labels: LABELS,
      onDone: (res) => follow(el, res, vote),
      onReload: () => void refresh(),
      onRace: (msg) => {
        toast(msg || c.raced);
        void refresh();
      },
    });
    const buttons = can
      ? h('div', { class: 'sv-decide' },
        h('button', { type: 'button', class: 'btn btn-primary btn-xl', 'data-decide': 'approve', onclick: () => decide('approve') }, icon('check', 20), c.approve),
        h('button', { type: 'button', class: 'btn btn-danger-outline btn-xl', 'data-decide': 'deny', onclick: () => decide('deny') }, c.decline))
      : h('p', { class: 't-2' }, a.you?.why_not ?? c.notEligible);
    const tech = h('details', { class: 'sv-tech' }, h('summary', null, c.technical),
      kv([[c.tech.tool, h('span', { class: 'mono', translate: 'no' }, a.tool)], [c.tech.rule, a.rule_id ?? ''], [c.tech.approval, h('a', { href: `/app/approvals/${a.id}` }, a.id)]]));
    el.append(
      h('div', { class: 'sv-card-head' },
        h('div', null, h('div', { class: 'meta' }, `${whereOf(a)} · ${relative(a.created_at)}`), h('h2', { id: hid }, title)),
        resultPill('waiting')),
      change, facts, buttons, tech,
      h('p', { class: 'sv-note' }, icon('info', 16), c.nonClinical));
    return el;
  };

  /** follow shows the decision, then the action's own states until it is locked. */
  const follow = (el: HTMLElement, res: ApprovalFull, vote: Vote) => {
    decided.set(res.id, el);
    const head = el.querySelector('.sv-card-head');
    const keep = el.querySelector('.sv-change');
    if (vote === 'deny' || res.status === 'denied') {
      el.replaceChildren(...[head, keep].filter(Boolean) as Node[], h('div', { class: 'sv-outcome', role: 'status' }, h('p', null, c.declinedDone), resultPill('sentBack')));
      announce(c.declinedDone);
      return;
    }
    const steps = c.approvedSteps.map((t, i) => h('li', { 'data-step': i }, h('span', { class: 'mark', 'aria-hidden': 'true' }, icon('check', 16)), fill(t, { where: res.context?.action.connector ?? res.tool.split('.')[0] ?? '' })));
    const ol = h('ol', { class: 'sv-follow', 'aria-label': c.approve }, ...steps);
    const outcome = h('div', { class: 'sv-outcome', role: 'status', 'aria-live': 'polite' });
    el.replaceChildren(...[head, keep].filter(Boolean) as Node[], ol, outcome);
    const mark = (n: number, bad = false) => steps.forEach((s, i) => {
      if (i < n) s.setAttribute('data-done', '');
      if (bad && i === n - 1) s.setAttribute('data-bad', '');
    });
    mark(1);
    let tries = 0;
    const poll = async () => {
      try {
        const act = await api<Action>('GET', `/v1/actions/${res.action}`, { query: { wait: '2' } });
        const r = resultOf(act);
        const executed = act.status === 'executed';
        if (executed) mark(2);
        if (act.witness) mark(3, r === 'problem');
        const done = act.state === 'sealed' || !!act.deed;
        if (done) {
          if (r === 'checked') mark(4);
          else if (act.witness) mark(4, r === 'problem');
          else {
            steps[2]?.remove(); // nothing could read it back: no "matches" step to tick
            mark(3);
          }
          const text = r === 'checked' ? c.approvedDone : r === 'problem' ? c.approvedMismatch : r === 'failed' ? sc.feed.result.failed! : c.approvedNoCheck;
          outcome.replaceChildren(h('p', null, text), act.deed ? h('a', { class: 'btn btn-secondary', href: `/app/simple?show=records&deed=${act.deed}` }, icon('lock', 16), c.seeRecord) : resultPill(r));
          announce(text);
          return;
        }
      } catch {
        /* keep trying a few times */
      }
      if (++tries < 15) setTimeout(() => void poll(), 800);
    };
    void poll();
  };

  const render = () => {
    const live = list.filter((a) => !decided.has(a.id));
    const kept = [...decided.values()];
    if (live.length === 0 && kept.length === 0) {
      host.replaceChildren(emptyBlock(c.empty, 'clock', { label: c.emptyNext, href: '/app/simple' }));
      return;
    }
    const focusId = (document.activeElement?.closest('[data-approval]') as HTMLElement | null)?.dataset.approval;
    cards.replaceChildren(...kept, ...live.map(card));
    host.replaceChildren(cards);
    if (focusId) cards.querySelector<HTMLElement>(`[data-approval="${focusId}"] [data-decide]`)?.focus();
  };

  const refresh = async () => {
    try {
      const page = await api<ListResponse<ApprovalFull>>('GET', '/v1/approvals', { query: { status: 'pending', limit: '50' } });
      const full = await Promise.all(sortPending(page.data).slice(0, 20).map((a) => (a.context ? Promise.resolve(a) : api<ApprovalFull>('GET', `/v1/approvals/${a.id}`).catch(() => a))));
      list = full.filter((a) => a.status === 'pending');
      render();
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return;
      host.replaceChildren(errorBlock(fill(c.loadFailed, { message: e instanceof Error ? e.message : String(e) }), c.retry, () => void refresh()));
    } finally {
      host.setAttribute('aria-busy', 'false');
    }
  };
  void refresh();
  const live = stream();
  let t = 0;
  live.on('approval.*', () => {
    clearTimeout(t);
    t = window.setTimeout(() => void refresh(), 250);
  });
  live.onResync(() => void refresh());
}

