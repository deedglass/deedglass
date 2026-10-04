// activation.ts: the typed-confirmation activation sheet (S26 §3.2, S12's copy), shared by the
// Policies screen and Insights' Promote (S28 §3.6). Enforce needs the policy's name typed and a
// reason, Shadow a reason, and a default_allow version a ticked acknowledgement. The server
// enforces every rule regardless. Kept apart from policies.ts so Insights doesn't load the editor.

import { api } from '../lib/api.ts';
import { copy, fill } from '../lib/copy.ts';
import { policiesCopy as c } from '../lib/copy-policies.ts';
import { h, icon, replace, type Child } from '../lib/dom.ts';
import { enforceTitle, headerLine, type Policy, type PolicyVersion } from '../lib/models/policy.ts';
import { get } from '../lib/state.ts';
import { openSheet, type Overlay } from './overlay.ts';
import { toast } from './toast.ts';

export interface ActivateOptions {
  policy: Policy;
  version: PolicyVersion;
  mode: 'enforce' | 'shadow';
  defaultAllow: boolean;
  onDone: () => void;
  /** S28: what the sheet shows before the version row (Insights' Promote: the shadow report). */
  extra?: Child[];
}

/** openActivate: the typed-confirmation activation sheet (S26), reused by Insights' Promote (S28). */
export function openActivate(o: ActivateOptions): Overlay {
  const enforce = o.mode === 'enforce';
  const v = o.version.version;
  const org = get()?.org.name ?? 'this org';
  const typed = enforce ? h('input', { class: 'input mono', id: 'act-name', name: 'confirm', autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', 'aria-describedby': 'act-name-h' }) : null;
  const reason = h('textarea', { class: 'textarea', id: 'act-reason', name: 'reason', rows: 3, maxlength: 280, required: true, 'aria-required': 'true', autocomplete: 'off' });
  const ack = o.defaultAllow ? h('input', { type: 'checkbox', id: 'act-ack', name: 'acknowledge' }) : null;
  const err = h('p', { class: 'error', role: 'alert', hidden: true });
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
  const go = h('button', { type: 'button', class: 'btn btn-primary', disabled: true }, fill(enforce ? c.enforce.button : c.shadow.button, { v }));
  const ready = () => {
    go.disabled = !((!typed || typed.value === o.policy.name) && reason.value.trim().length >= 3 && (!ack || ack.checked));
  };
  for (const el of [typed, reason, ack]) el?.addEventListener('input', ready);
  ack?.addEventListener('change', ready);
  const body: Child[] = [
    h('p', null, enforce ? fill(c.enforce.body, { org }) : c.shadow.body),
    ...(o.extra ?? []),
    h('dl', { class: 'kv act-version' }, h('dt', null, 'Version'), h('dd', { class: 'mono', translate: 'no' }, `${o.policy.name} · ${headerLine(o.version)}`)),
    typed ? h('div', { class: 'field' }, h('label', { class: 'label', for: 'act-name' }, fill(c.enforce.confirm, { name: o.policy.name })), typed, h('p', { class: 'help', id: 'act-name-h' }, copy.stop.mismatchHint)) : null,
    h('div', { class: 'field' }, h('label', { class: 'label', for: 'act-reason' }, c.enforce.reason), reason, h('p', { class: 'help' }, c.enforce.reasonHelp)),
    ack ? h('label', { class: 'check act-ack' }, ack, h('span', null, c.enforce.ack)) : null,
    err,
  ];
  const ov = openSheet({ title: enforce ? enforceTitle(o.policy.name, v) : fill(c.shadow.title, { name: o.policy.name, v }), body, foot: [cancel, go], initialFocus: () => cancel, role: enforce ? 'alertdialog' : 'dialog', kind: 'dialog' });
  cancel.addEventListener('click', ov.close);
  go.addEventListener('click', async () => {
    if (go.disabled) return;
    go.disabled = true;
    go.setAttribute('aria-busy', 'true');
    err.hidden = true;
    try {
      const a = await api<{ deeds?: string[] }>('POST', `/v1/policies/${o.policy.id}/versions/${v}/activate`, {
        body: { mode: o.mode, reason: reason.value.trim(), ...(ack?.checked ? { acknowledge_warnings: ['default_allow'] } : {}) },
      });
      ov.close();
      const deed = a.deeds?.[a.deeds.length - 1];
      toast(fill(enforce ? c.enforce.toast : c.shadow.toast, { name: o.policy.name, v }), { kind: 'success', ...(deed ? { action: { label: c.viewDeed, href: `/app/deeds/${deed}` } } : {}) });
      o.onDone();
    } catch (e) {
      go.removeAttribute('aria-busy');
      replace(err, icon('circle-alert', 16), e instanceof Error ? e.message : String(e));
      err.hidden = false;
      ready();
    }
  });
  return ov;
}

