// overlay.ts: sheets, dialogs, the typed confirmation, menus, popovers and tooltips (S24 §3.6).
// Our own light-dismiss and focus handling (<dialog closedby> isn't in stable Safari): Escape and
// a click on the scrim close, focus is trapped inside and returns to the opener, the rest of the
// page is inert. Positioning is computed in JS for every engine.

import { copy, fill } from '../lib/copy.ts';
import { h, icon, type Child } from '../lib/dom.ts';

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function focusables(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => el.offsetParent !== null || el === document.activeElement);
}

function setInert(on: boolean): void {
  for (const id of ['shell-root', 'bottombar']) {
    const el = document.getElementById(id);
    if (el) el.inert = on;
  }
}

let openCount = 0;

export interface Overlay {
  el: HTMLElement;
  close: () => void;
}

export interface SheetOptions {
  kind?: 'sheet' | 'dialog';
  title: string;
  body: Child[];
  foot?: Child[];
  initialFocus?: () => HTMLElement | null;
  onClose?: () => void;
  role?: 'dialog' | 'alertdialog';
  className?: string;
}

let seq = 0;

/** openSheet shows a sheet (right side; a bottom sheet under 640 px) or a centred dialog. */
export function openSheet(o: SheetOptions): Overlay {
  const opener = document.activeElement as HTMLElement | null;
  const id = `ov${++seq}`;
  const scrim = h('div', { class: 'scrim', 'data-overlay': id });
  const closeBtn = h('button', { type: 'button', class: 'icon-btn', 'aria-label': 'Close' }, icon('x', 20));
  const panel = h('div', {
    class: `${o.kind === 'dialog' ? 'dialog' : 'sheet'} glass${o.className ? ' ' + o.className : ''}`,
    role: o.role ?? 'dialog', 'aria-modal': 'true', 'aria-labelledby': `${id}-t`, 'data-overlay': id,
  },
  h('div', { class: 'sheet-head' }, h('h2', { class: 't-headline', id: `${id}-t` }, o.title), closeBtn),
  h('div', { class: 'sheet-body' }, ...o.body),
  o.foot && o.foot.length ? h('div', { class: 'sheet-foot' }, ...o.foot) : null);
  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    document.removeEventListener('keydown', onKey, true);
    panel.classList.add('closing');
    const done = () => {
      scrim.remove();
      panel.remove();
      if (--openCount === 0) setInert(false);
      opener?.focus?.();
      o.onClose?.();
    };
    if (matchMedia('(prefers-reduced-motion: reduce)').matches || o.kind === 'dialog') done();
    else setTimeout(done, 160);
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
      return;
    }
    if (e.key === 'Tab') {
      const f = focusables(panel);
      if (f.length === 0) return;
      const first = f[0]!;
      const last = f[f.length - 1]!;
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  };
  closeBtn.addEventListener('click', close);
  scrim.addEventListener('click', close);
  document.addEventListener('keydown', onKey, true);
  if (openCount++ === 0) setInert(true);
  document.body.append(scrim, panel);
  requestAnimationFrame(() => (o.initialFocus?.() ?? focusables(panel)[0] ?? panel).focus());
  return { el: panel, close };
}

export interface ConfirmOptions {
  title: string;
  body: string;
  expected?: string; // typed confirmation, case-sensitive
  expectedLabel?: string;
  confirmLabel: string;
  reasonLabel?: string;
  reasonRequired?: boolean;
  danger?: boolean;
  onConfirm: (reason: string) => Promise<void>;
}

/**
 * confirmTyped: the destructive-confirm rules (COPY_CATALOG S08): the button repeats the verb and
 * object, it enables only on an exact (case-sensitive) match, Cancel has the initial focus, Escape
 * closes without acting, the reason says where it goes.
 */
export function confirmTyped(o: ConfirmOptions): Overlay {
  const cid = `c${++seq}`;
  const confirmInput = o.expected
    ? h('input', { class: 'input mono', id: `${cid}-c`, autocomplete: 'off', autocapitalize: 'off', spellcheck: 'false', 'aria-describedby': `${cid}-h` })
    : null;
  const reason = o.reasonLabel
    ? h('textarea', { class: 'textarea', id: `${cid}-r`, maxlength: 280, rows: 3, ...(o.reasonRequired ? { required: true, 'aria-required': 'true' } : {}) })
    : null;
  const err = h('p', { class: 'error', id: `${cid}-e`, role: 'alert' });
  err.hidden = true;
  const cancel = h('button', { type: 'button', class: 'btn btn-secondary' }, copy.stop.cancel);
  const go = h('button', { type: 'button', class: `btn ${o.danger ? 'btn-danger' : 'btn-primary'}`, disabled: true }, o.confirmLabel);
  const ready = () => {
    const typed = !confirmInput || confirmInput.value === o.expected;
    const reasoned = !o.reasonRequired || (reason?.value.trim().length ?? 0) >= 3;
    go.disabled = !(typed && reasoned);
  };
  confirmInput?.addEventListener('input', ready);
  reason?.addEventListener('input', ready);
  const body: Child[] = [h('p', null, o.body)];
  if (confirmInput) {
    body.push(h('div', { class: 'field' },
      h('label', { class: 'label', for: `${cid}-c` }, o.expectedLabel ?? fill(copy.stop.agentConfirm, { name: o.expected ?? '' })),
      confirmInput,
      h('p', { class: 'help', id: `${cid}-h` }, copy.stop.mismatchHint)));
  }
  if (reason && o.reasonLabel) body.push(h('div', { class: 'field' }, h('label', { class: 'label', for: `${cid}-r` }, o.reasonLabel), reason));
  body.push(err);
  const ov = openSheet({ title: o.title, body, foot: [cancel, go], initialFocus: () => cancel, role: o.danger ? 'alertdialog' : 'dialog' });
  cancel.addEventListener('click', ov.close);
  go.addEventListener('click', async () => {
    if (go.disabled) return;
    go.setAttribute('aria-busy', 'true');
    go.disabled = true;
    err.hidden = true;
    try {
      await o.onConfirm(reason?.value.trim() ?? '');
      ov.close();
    } catch (e) {
      err.replaceChildren(icon('circle-alert', 16), h('span', null, e instanceof Error ? e.message : String(e)));
      err.hidden = false;
      go.removeAttribute('aria-busy');
      ready();
    }
  });
  return ov;
}

// ---------- menus, popovers, tooltips ----------

export interface MenuItem {
  label: string;
  icon?: string;
  meta?: string;
  checked?: boolean; // menuitemradio
  run?: () => void;
  href?: string;
  section?: string; // a label row above this item
  divider?: boolean;
}

function place(panel: HTMLElement, anchor: HTMLElement, align: 'start' | 'end' = 'end'): void {
  const r = anchor.getBoundingClientRect();
  const w = panel.offsetWidth;
  const hgt = panel.offsetHeight;
  let left = align === 'end' ? r.right - w : r.left;
  left = Math.max(8, Math.min(left, innerWidth - w - 8));
  let top = r.bottom + 6;
  if (top + hgt > innerHeight - 8 && r.top - hgt - 6 > 8) top = r.top - hgt - 6;
  panel.style.left = `${Math.round(left)}px`;
  panel.style.top = `${Math.round(top)}px`;
}

let openMenuClose: (() => void) | null = null;

/** openMenu: a menu button's menu (arrow keys, Home/End, Escape, Tab closes, click outside closes). */
export function openMenu(anchor: HTMLElement, items: MenuItem[], label: string, align: 'start' | 'end' = 'end'): () => void {
  openMenuClose?.();
  const menu = h('div', { class: 'menu glass', role: 'menu', 'aria-label': label });
  const buttons: HTMLElement[] = [];
  for (const it of items) {
    if (it.divider) menu.append(h('hr'));
    if (it.section) menu.append(h('div', { class: 'menu-label', role: 'presentation' }, it.section));
    const role = it.checked === undefined ? 'menuitem' : 'menuitemradio';
    const content = [it.icon ? icon(it.icon, 16) : null, h('span', null, it.label), it.meta ? h('span', { class: 'meta' }, it.meta) : null,
      role === 'menuitemradio' ? h('span', { class: 'tick' }, icon('check', 16)) : null];
    const el = it.href
      ? h('a', { role, href: it.href, tabindex: '-1' }, ...content)
      : h('button', { type: 'button', role, tabindex: '-1', ...(role === 'menuitemradio' ? { 'aria-checked': it.checked ? 'true' : 'false' } : {}) }, ...content);
    el.addEventListener('click', () => {
      close();
      it.run?.();
    });
    buttons.push(el);
    menu.append(el);
  }
  const close = () => {
    menu.remove();
    anchor.setAttribute('aria-expanded', 'false');
    document.removeEventListener('pointerdown', outside, true);
    document.removeEventListener('keydown', key, true);
    if (openMenuClose === close) openMenuClose = null;
  };
  const outside = (e: Event) => {
    if (!menu.contains(e.target as Node) && !anchor.contains(e.target as Node)) close();
  };
  const key = (e: KeyboardEvent) => {
    const i = buttons.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
      anchor.focus();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      buttons[(i + 1) % buttons.length]?.focus();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      buttons[(i - 1 + buttons.length) % buttons.length]?.focus();
    } else if (e.key === 'Home') {
      e.preventDefault();
      buttons[0]?.focus();
    } else if (e.key === 'End') {
      e.preventDefault();
      buttons[buttons.length - 1]?.focus();
    } else if (e.key === 'Tab') {
      close();
    }
  };
  document.body.append(menu);
  place(menu, anchor, align);
  anchor.setAttribute('aria-expanded', 'true');
  document.addEventListener('pointerdown', outside, true);
  document.addEventListener('keydown', key, true);
  openMenuClose = close;
  const first = buttons.find((b) => b.getAttribute('aria-checked') === 'true') ?? buttons[0];
  first?.focus();
  return close;
}

/** menuButton wires a button to open a menu built on demand. */
export function menuButton(btn: HTMLElement, label: string, build: () => MenuItem[], align: 'start' | 'end' = 'end'): void {
  btn.setAttribute('aria-haspopup', 'menu');
  btn.setAttribute('aria-expanded', 'false');
  btn.addEventListener('click', () => {
    if (btn.getAttribute('aria-expanded') === 'true') {
      openMenuClose?.();
      return;
    }
    openMenu(btn, build(), label, align);
  });
  btn.addEventListener('keydown', (e) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      openMenu(btn, build(), label, align);
    }
  });
}

/** openPopover shows a small explanation anchored to an element (the viewer's Stop all). */
export function openPopover(anchor: HTMLElement, content: Child[]): () => void {
  const id = `pop${++seq}`;
  const pop = h('div', { class: 'popover glass', role: 'status', id }, ...content);
  document.body.append(pop);
  place(pop, anchor);
  anchor.setAttribute('aria-describedby', id);
  const close = () => {
    pop.remove();
    anchor.removeAttribute('aria-describedby');
    document.removeEventListener('pointerdown', outside, true);
    document.removeEventListener('keydown', key, true);
  };
  const outside = (e: Event) => {
    if (!pop.contains(e.target as Node) && e.target !== anchor) close();
  };
  const key = (e: KeyboardEvent) => {
    if (e.key === 'Escape') close();
  };
  setTimeout(() => {
    document.addEventListener('pointerdown', outside, true);
    document.addEventListener('keydown', key, true);
  });
  return close;
}

/** tooltips: any [data-tip] element shows its text on hover or focus after 400 ms (S24 §3.6). */
export function tooltips(root: ParentNode = document): void {
  let timer = 0;
  let tip: HTMLElement | null = null;
  const hide = () => {
    clearTimeout(timer);
    tip?.remove();
    tip = null;
  };
  const show = (el: HTMLElement) => {
    hide();
    const text = el.getAttribute('data-tip');
    if (!text) return;
    timer = window.setTimeout(() => {
      tip = h('div', { class: 'tooltip', role: 'tooltip' }, text);
      document.body.append(tip);
      const r = el.getBoundingClientRect();
      const w = tip.offsetWidth;
      const side = el.getAttribute('data-tip-side');
      if (side === 'right') {
        tip.style.left = `${Math.round(r.right + 8)}px`;
        tip.style.top = `${Math.round(r.top + r.height / 2 - tip.offsetHeight / 2)}px`;
      } else {
        tip.style.left = `${Math.round(Math.max(8, Math.min(r.left + r.width / 2 - w / 2, innerWidth - w - 8)))}px`;
        tip.style.top = `${Math.round(r.bottom + 6)}px`;
      }
    }, 400);
  };
  root.addEventListener('pointerover', (e) => {
    const el = (e.target as HTMLElement).closest?.('[data-tip]') as HTMLElement | null;
    if (el) show(el);
  });
  root.addEventListener('pointerout', (e) => {
    if ((e.target as HTMLElement).closest?.('[data-tip]')) hide();
  });
  root.addEventListener('focusin', (e) => {
    const el = (e.target as HTMLElement).closest?.('[data-tip]') as HTMLElement | null;
    if (el && (el as HTMLElement).matches(':focus-visible')) show(el);
  });
  root.addEventListener('focusout', hide);
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') hide();
  });
}
