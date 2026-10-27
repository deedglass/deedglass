// inbox.ts: "Needs you", the top of the Overview (console-v1). Every pending approval as a card
// with two big buttons, the four-eyes progress in words and dots, and the expiry. A button opens
// the DecisionSheet (S26: no decision without it, a decline needs a reason, one Idempotency-Key),
// so the inbox adds a shorter path, never a second way to decide. Live: approval events reload it.

import { api } from '../lib/api.ts';
import { fill } from '../lib/copy.ts';
import { liveCopy as c } from '../lib/copy-live.ts';
import { h, icon } from '../lib/dom.ts';
import { effectLine, sortPending, voteCount, votes, type ApprovalFull } from '../lib/models/approval.ts';
import type { ListResponse } from '../lib/types.ts';
import { minutesUntil } from '../lib/sentences.ts';
import { atLeast, get } from '../lib/state.ts';
import { visibleControls } from './components.ts';
import { openDecisionSheet, type Vote } from './decisionsheet.ts';
import { apiErrorToast, toast } from './toast.ts';
import { announce } from './shell.ts';

const SHOWN = 3;

export interface Inbox {
  el: HTMLElement;
  reload: () => Promise<void>;
  count: () => number;
}

function dots(done: number, required: number): HTMLElement {
  const wrap = h('span', { class: 'inbox-dots', 'aria-hidden': 'true' });
  for (let i = 0; i < required; i++) wrap.append(h('span', { class: i < done ? 'dot on' : 'dot' }));
  return wrap;
}

export function createInbox(): Inbox {
  const list = h('ul', { class: 'inbox-list', 'aria-label': c.inbox.title });
  const empty = h('p', { class: 'inbox-empty' }, icon('check', 16), c.inbox.none);
  const more = h('a', { class: 'card-link inbox-more', href: '/app/approvals', hidden: true });
  const badge = h('span', { class: 'inbox-count num', hidden: true });
  const el = h('section', { class: 'inbox', id: 'inbox', 'aria-labelledby': 'inbox-t' },
    h('div', { class: 'inbox-head' }, h('h2', { class: 't-title-3', id: 'inbox-t' }, c.inbox.title, badge), more),
    empty, list);
  let pending: ApprovalFull[] = [];
  let loading: Promise<void> | null = null;

  const decide = async (a: ApprovalFull, vote: Vote, btn: HTMLButtonElement) => {
    btn.setAttribute('aria-busy', 'true');
    let full: ApprovalFull;
    try {
      full = await api<ApprovalFull>('GET', `/v1/approvals/${a.id}`);
    } catch (e) {
      apiErrorToast(e);
      return;
    } finally {
      btn.removeAttribute('aria-busy');
    }
    openDecisionSheet({
      approval: full,
      vote,
      effect: effectLine(full),
      onDone: (res) => {
        const msg = vote === 'deny' ? c.inbox.declined : res.status === 'approved' ? c.inbox.approved : c.inbox.approvedOne;
        toast(msg, { kind: vote === 'deny' ? 'neutral' : 'success' });
        announce(msg);
        void reload();
      },
      onReload: () => void reload(),
      onRace: (m) => {
        toast(m);
        void reload();
      },
    });
  };

  const card = (a: ApprovalFull): HTMLElement => {
    const admin = atLeast(get()?.me.membership.role, 'admin');
    const can = a.you ? a.you.can_decide : admin;
    const why = !admin ? c.inbox.viewer : a.you && !a.you.can_decide ? (a.you.why_not ?? c.inbox.notYou) : a.you?.why_not ?? '';
    const done = voteCount(a);
    const me = get()?.me.user.id;
    const signed = !!me && votes(a).some((v) => v.user.id === me);
    const whyId = `inbox-why-${a.id}`;
    const approve = h('button', { type: 'button', class: 'btn btn-brand btn-lg', 'data-inbox': 'approve', ...(can && admin && !a.you?.why_not ? {} : { disabled: true, 'aria-describedby': whyId }) }, icon('check', 16), c.inbox.approve);
    const decline = h('button', { type: 'button', class: 'btn btn-secondary btn-lg btn-decline', 'data-inbox': 'decline', ...(can && admin ? {} : { disabled: true, 'aria-describedby': whyId }) }, icon('x', 16), c.inbox.decline);
    approve.addEventListener('click', () => void decide(a, 'approve', approve));
    decline.addEventListener('click', () => void decide(a, 'deny', decline));
    const four = a.required > 1
      ? h('p', { class: 'inbox-four num', 'data-four-eyes': '' }, dots(done, a.required), fill(c.inbox.signed, { done, required: a.required }), h('span', { class: 't-2' }, ` · ${c.inbox.fourEyes}`))
      : null;
    return h('li', { class: 'inbox-card', 'data-approval': a.id },
      h('div', { class: 'inbox-top' },
        h('span', { class: 'chip chip-pending' }, icon('contrast', 16), visibleControls(a.tool)),
        h('span', { class: 'inbox-exp t-2 num' }, fill(c.inbox.expires, { mins: minutesUntil(a.expires_at, new Date()) }))),
      h('p', { class: 'inbox-effect' }, visibleControls(effectLine(a) || a.summary)),
      h('p', { class: 'inbox-meta t-2' }, h('a', { href: `/app/agents/${a.agent.id}` }, visibleControls(a.agent.name)), a.rule_id ? h('span', null, ' · ', h('span', { class: 'mono' }, visibleControls(a.rule_id))) : null,
        ' · ', h('a', { href: `/app/approvals/${a.id}` }, c.inbox.open)),
      four,
      signed ? h('p', { class: 'inbox-signed', 'data-you-signed': '' }, icon('stamp', 16), c.inbox.youSigned) : h('div', { class: 'inbox-actions' }, decline, approve),
      why ? h('p', { class: 't-footnote t-2', id: whyId }, why) : null);
  };

  const render = () => {
    const shown = pending.slice(0, SHOWN);
    list.replaceChildren(...shown.map(card));
    empty.hidden = pending.length > 0;
    list.hidden = pending.length === 0;
    el.classList.toggle('is-empty', pending.length === 0);
    badge.hidden = pending.length === 0;
    badge.textContent = String(pending.length);
    more.hidden = pending.length <= SHOWN;
    more.replaceChildren(fill(c.inbox.more, { n: pending.length - SHOWN }), icon('chevron-right', 16));
  };

  const reload = (): Promise<void> => {
    if (loading) return loading;
    loading = (async () => {
      try {
        const page = await api<ListResponse<ApprovalFull>>('GET', '/v1/approvals', { query: { status: 'pending', limit: '50' } });
        pending = sortPending(page.data.filter((a) => a.status === 'pending'));
        render();
      } catch {
        /* keep what is on screen; the offline banner says why */
      } finally {
        loading = null;
      }
    })();
    return loading;
  };

  render();
  return { el, reload, count: () => pending.length };
}
