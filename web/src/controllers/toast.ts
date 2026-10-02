// toast.ts: glass toasts, bottom right (bottom centre on phones), at most 3, newest on top.
// Neutral and success dismiss after 5 s (paused on hover); errors stay until dismissed. Info is
// role="status", errors role="alert" (DESIGN.md toast).

import { copy, fill } from '../lib/copy.ts';
import { h, icon, type Child } from '../lib/dom.ts';

export type ToastKind = 'neutral' | 'success' | 'error';
const ICONS: Record<ToastKind, string> = { neutral: 'info', success: 'check', error: 'circle-alert' };
const MAX = 3;
const DWELL = 5000;

function region(): HTMLElement {
  let r = document.getElementById('toasts');
  if (!r) {
    r = h('section', { id: 'toasts', class: 'toasts', 'aria-label': 'Notifications' });
    document.body.append(r);
  }
  return r;
}

export interface ToastOptions {
  kind?: ToastKind;
  action?: { label: string; href?: string; run?: () => void };
  requestId?: string;
}

export function toast(message: string, o: ToastOptions = {}): () => void {
  const kind = o.kind ?? 'neutral';
  const r = region();
  const dismiss = h('button', { type: 'button', class: 'icon-btn', 'aria-label': copy.toast.dismiss }, icon('x', 16));
  const meta: Child[] = [];
  if (o.action) {
    meta.push(o.action.href ? h('a', { href: o.action.href }, o.action.label) : h('button', { type: 'button', class: 'btn btn-quiet btn-sm', onclick: () => o.action?.run?.() }, o.action.label));
  }
  if (o.requestId) {
    meta.push(h('button', { type: 'button', class: 'idchip', 'data-copy': o.requestId, 'aria-label': copy.toast.copyRequestId, translate: 'no' },
      fill(copy.toast.requestId, { req: o.requestId }), icon('copy', 16)));
  }
  const el = h('div', { class: `toast glass toast-${kind}`, role: kind === 'error' ? 'alert' : 'status' },
    icon(ICONS[kind], 20), h('div', null, h('p', null, message), meta.length ? h('div', { class: 'meta' }, ...meta) : null), dismiss);
  let timer = 0;
  const close = () => {
    clearTimeout(timer);
    el.classList.add('closing');
    setTimeout(() => el.remove(), 130);
  };
  const arm = () => {
    if (kind !== 'error') timer = window.setTimeout(close, DWELL);
  };
  dismiss.addEventListener('click', close);
  el.addEventListener('pointerenter', () => clearTimeout(timer));
  el.addEventListener('pointerleave', arm);
  el.addEventListener('focusin', () => clearTimeout(timer));
  r.append(el);
  while (r.children.length > MAX) r.firstElementChild?.remove();
  arm();
  return close;
}

/** apiErrorToast shows the server's message verbatim (it already ends with a next step) and the request id. */
export function apiErrorToast(err: unknown): void {
  const e = err as { message?: string; requestId?: string };
  toast(e?.message ?? copy.toast.networkError, { kind: 'error', requestId: e?.requestId || undefined });
}
