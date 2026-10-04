// decisionsheet.ts: DecisionSheet (S26 §3.1). The one place a person approves or denies. Nothing
// is sent without this explicit step; a deny always carries a reason (3 to 280 characters); every
// POST carries {reason, context_digest} and one Idempotency-Key per sheet, so a double tap or a
// retry sends once. ⌘/Ctrl+Enter submits. A glass bottom sheet on phones, a dialog from 640 px.

import { api, ApiError } from '../lib/api.ts';
import { fill } from '../lib/copy.ts';
import { approvalsCopy as c } from '../lib/copy-approvals.ts';
import { h, icon } from '../lib/dom.ts';
import { validDenyReason, type ApprovalFull } from '../lib/models/approval.ts';
import { openSheet, type Overlay } from './overlay.ts';

export type Vote = 'approve' | 'deny';

export interface DecisionOptions {
  approval: ApprovalFull;
  vote: Vote;
  effect: string;
  /** the server's answer after a recorded vote */
  onDone: (a: ApprovalFull) => void;
  /** 409 context_changed: the sheet shows S22's sentence and a Reload button that calls this */
  onReload: () => void;
  /** 409 approval_decided (a race): the sheet closes and the page shows the sentence */
  onRace?: (message: string) => void;
}

let seq = 0;

export function openDecisionSheet(o: DecisionOptions): Overlay {
  const id = `ds${++seq}`;
  const a = o.approval;
  const deny = o.vote === 'deny';
  const key = crypto.randomUUID();
  const obo = a.context?.on_behalf_of ? fill(c.oboSuffix, { who: a.context.on_behalf_of }) : '';
  const reason = h('textarea', {
    class: 'textarea', id: `${id}-r`, name: 'reason', rows: 3, maxlength: 280, autocomplete: 'off',
    'aria-describedby': `${id}-h ${id}-e`, ...(deny ? { required: true, 'aria-required': 'true' } : {}),
  });
  const err = h('div', { class: 'error decision-error', id: `${id}-e`, role: 'alert' });
  err.hidden = true;
  const offline = h('p', { class: 'banner banner-readonly', role: 'status' }, icon('wifi-off', 16), c.offline);
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, 'Cancel');
  const go = h('button', { type: 'button', class: `btn ${deny ? 'btn-danger-outline' : 'btn-primary'}`, 'data-decide': o.vote }, deny ? c.deny : c.approve);
  const ready = () => {
    const online = navigator.onLine;
    offline.hidden = online;
    if (go.getAttribute('aria-busy') === 'true') return; // busy: keep focus on the button (aria-disabled), never disabled
    go.disabled = !online || (deny && !validDenyReason(reason.value));
  };
  const body = [
    h('p', { class: 'decision-lede' }, deny ? c.denyBody : fill(c.approveBody, { effect: o.effect, agent: a.agent.name, obo })),
    h('div', { class: 'field' },
      h('label', { class: 'label', for: `${id}-r` }, deny ? c.denyReason : c.approveReason),
      reason,
      h('p', { class: 'help', id: `${id}-h` }, deny ? `${c.denyReasonHint} ` : null, h('span', { class: 'kbd-only' }, c.submitHint))),
    offline,
    err,
  ];
  const ov = openSheet({
    kind: matchMedia('(min-width: 640px)').matches ? 'dialog' : 'sheet',
    title: fill(deny ? c.denyTitle : c.approveTitle, { tool: a.tool }),
    body,
    foot: [cancel, go],
    // A phone keyboard would cover the decision: on touch, Approve opens with focus on Cancel.
    initialFocus: () => (deny || matchMedia('(pointer: fine)').matches ? reason : cancel),
    className: 'decision-sheet',
  });
  const onNet = () => ready();
  window.addEventListener('online', onNet);
  window.addEventListener('offline', onNet);
  const cleanup = () => {
    window.removeEventListener('online', onNet);
    window.removeEventListener('offline', onNet);
  };
  const close = () => {
    cleanup();
    ov.close();
  };
  cancel.addEventListener('click', close);
  reason.addEventListener('input', ready);
  const showError = (msg: string, reload = false) => {
    err.replaceChildren(icon('circle-alert', 16), h('span', null, msg));
    if (reload) {
      const reloadBtn = h('button', { type: 'button', class: 'btn btn-secondary btn-sm', onclick: () => { close(); o.onReload(); } }, icon('refresh-cw', 16), c.reload);
      err.append(reloadBtn);
      go.hidden = true; // send nothing else on a stale context
      requestAnimationFrame(() => reloadBtn.focus());
    }
    err.hidden = false;
  };
  const submit = async () => {
    if (go.getAttribute('aria-busy') === 'true') return; // a double tap sends once
    ready();
    if (go.disabled) return;
    go.setAttribute('aria-busy', 'true');
    go.setAttribute('aria-disabled', 'true');
    go.replaceChildren(h('span', { class: 'spin-glyph', 'aria-hidden': 'true' }, '◐'), deny ? c.denying : c.approving);
    err.hidden = true;
    try {
      const res = await api<ApprovalFull>('POST', `/v1/approvals/${a.id}/${o.vote}`, {
        body: { reason: reason.value.trim(), context_digest: a.context_digest ?? '' },
        idempotencyKey: key,
      });
      reason.value = '';
      close();
      o.onDone(res);
    } catch (e) {
      go.removeAttribute('aria-busy');
      go.removeAttribute('aria-disabled');
      go.replaceChildren(deny ? c.deny : c.approve);
      if (e instanceof ApiError && e.code === 'context_changed') return showError(e.message, true);
      if (e instanceof ApiError && e.code === 'approval_decided' && o.onRace) {
        close();
        o.onRace(e.message);
        return;
      }
      showError(e instanceof Error ? e.message : String(e));
      ready();
      go.focus();
    }
  };
  go.addEventListener('click', () => void submit());
  ov.el.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      void submit();
    }
  });
  ready();
  return { el: ov.el, close };
}
