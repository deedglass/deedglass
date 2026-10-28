// chips.ts: chips, ids and times built at run time (live rows, stubs). The markup matches the
// Astro components in src/components, so a row loaded later looks like a row rendered at build.
// Colour never carries meaning alone: every chip has an icon and a word (brand §6.3).

import { copy } from './copy.ts';
import { h, icon } from './dom.ts';
import { dateTime, relative, shortHash, shortId } from './format.ts';
import { STATE_STYLES, stateLabel } from './states.ts';
import type { Level, Verdict } from './types.ts';

const VERDICT: Record<Verdict, { cls: string; icon: string; word: string }> = {
  VERIFIED: { cls: 'chip-verified', icon: 'check', word: copy.chips.verified },
  MISMATCH: { cls: 'chip-mismatch', icon: 'x', word: copy.chips.mismatch },
  UNVERIFIABLE: { cls: 'chip-unverifiable', icon: 'circle-dashed', word: copy.chips.unverifiable },
};

/** verdictChip: [icon] Proven · Matches (plain names; the wire values L2 · VERIFIED are in the title). */
export function verdictChip(verdict: Verdict, level?: Level | null): HTMLSpanElement {
  const v = VERDICT[verdict] ?? VERDICT.UNVERIFIABLE;
  const lv = level ? (copy.chips.level[level] ?? level) : '';
  return h('span', { class: `chip ${v.cls}`, title: level ? `${lv} · ${v.word} (${level} · ${verdict})` : `${v.word} (${verdict})` },
    icon(v.icon, 16), level ? h('span', null, lv, h('span', { class: 'sep' }, ' · '), v.word) : v.word);
}

export type Decision = 'allow' | 'deny' | 'require_approval';

/** decisionChip: Allowed (neutral), Denied (outline, never filled), Approval (pending). */
export function decisionChip(d: Decision): HTMLSpanElement {
  switch (d) {
    case 'allow':
      return h('span', { class: 'chip chip-neutral' }, icon('arrow-right', 16), copy.chips.allowed);
    case 'deny':
      return h('span', { class: 'chip chip-denied' }, icon('ban', 16), copy.chips.denied);
    default:
      return h('span', { class: 'chip chip-pending' }, icon('contrast', 16), copy.chips.approval);
  }
}

/** The 15 lifecycle states (contract §5) and how each reads as a chip. */
export const STATES = STATE_STYLES;

export function stateChip(state: string): HTMLSpanElement {
  const s = STATES[state] ?? { cls: 'chip-outline', icon: 'circle-dashed' };
  return h('span', { class: `chip ${s.cls}` }, icon(s.icon, 16), stateLabel(state));
}

/** statusChip: an agent's or key's lifecycle status (not a verdict). */
export function statusChip(status: 'active' | 'suspended' | 'stopped' | 'test'): HTMLSpanElement {
  const m = {
    active: { icon: 'check', word: copy.agents.active },
    suspended: { icon: 'pause', word: copy.agents.suspended },
    stopped: { icon: 'stop', word: copy.agents.stopped },
    test: { icon: 'key-round', word: copy.agents.test },
  }[status];
  return h('span', { class: `status status-${status}` }, icon(m.icon, 16), m.word);
}

/**
 * copyChip: a value shown shortened that copies in full. Its name is "Copy <full value>" from hidden
 * text and the short form is hidden from the name, so the name never contradicts what's on screen
 * (WCAG 2.5.3, label in name).
 */
export function copyChip(value: string, short: string): HTMLButtonElement {
  return h('button', { type: 'button', class: 'idchip', 'data-copy': value, title: value, translate: 'no' },
    h('span', { class: 'sr-only' }, `Copy ${value}`), h('span', { 'aria-hidden': 'true' }, short), icon('copy', 16));
}

/** idChip: mono, shortened, the full id in the title, click to copy. */
export function idChip(id: string, href?: string): HTMLElement {
  const short = shortId(id);
  if (!href) return copyChip(id, short);
  // Linked: the id is the link; the copy button beside it is the icon alone.
  const btn = h('button', { type: 'button', class: 'idchip idchip-icon', 'data-copy': id, title: `Copy ${id}`, 'aria-label': `Copy ${id}` }, icon('copy', 16));
  return h('span', { class: 'idlink' }, h('a', { href, class: 'mono', translate: 'no', title: id }, short), btn);
}

export function hashChip(hash: string): HTMLButtonElement {
  return copyChip(hash, shortHash(hash));
}

/** relTime: <time> with the relative text and the absolute time (org time zone) in the title. */
export function relTime(iso: string, tz = 'UTC', now = new Date()): HTMLTimeElement {
  return h('time', { class: 'reltime', datetime: iso, title: dateTime(iso, tz), 'data-tz': tz }, relative(iso, now, tz));
}
