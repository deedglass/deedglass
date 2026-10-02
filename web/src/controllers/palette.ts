// The command palette overlay (S24 §3.8): ⌘K / Ctrl+K or "/", a glass dialog 20vh from the top
// (full screen under 640 px), ARIA combobox + listbox with aria-activedescendant. Pasting an id
// jumps straight to it. Nothing here changes state on its own: Stop all opens its typed sheet.

import { api } from '../lib/api.ts';
import { copy, fill } from '../lib/copy.ts';
import { h, icon } from '../lib/dom.ts';
import { DESTS, SETTINGS_SECTIONS } from '../lib/nav.ts';
import { idTarget, rank, type Item } from '../lib/palette.ts';
import { recent } from '../lib/recent.ts';
import { get as getState } from '../lib/state.ts';
import type { Agent, ListResponse } from '../lib/types.ts';

export interface PaletteHooks {
  stopAll: () => void;
  toggleTheme: () => void;
  signOut: () => void;
  copyOrgId: () => void;
}

let agentsCache: Agent[] | null = null;
let isOpen = false;

async function agents(): Promise<Agent[]> {
  if (agentsCache) return agentsCache;
  try {
    const page = await api<ListResponse<Agent>>('GET', '/v1/agents', { query: { limit: '100' } });
    agentsCache = page.data;
  } catch {
    agentsCache = [];
  }
  return agentsCache;
}

function baseItems(hooks: PaletteHooks): Item[] {
  const items: Item[] = [];
  for (const d of DESTS) items.push({ id: `go-${d.key}`, label: d.label, group: 'goto', href: d.href, hint: d.go, keywords: `go ${d.key}`, icon: d.icon });
  for (const s of SETTINGS_SECTIONS) items.push({ id: `set-${s.key}`, label: `Settings › ${s.label}`, group: 'goto', href: `/app/settings/${s.key}`, icon: 'settings' });
  items.push(
    { id: 'act-create-agent', label: copy.palette.createAgent, group: 'actions', href: '/app/agents?new=1', icon: 'plus' },
    { id: 'act-stop-agent', label: copy.palette.stopAgent, group: 'actions', href: '/app/agents?stop=1', icon: 'stop' },
    { id: 'act-stop-all', label: copy.palette.stopAll, group: 'actions', run: hooks.stopAll, icon: 'stop' },
    { id: 'act-theme', label: copy.palette.toggleTheme, group: 'actions', run: hooks.toggleTheme, icon: 'moon' },
    { id: 'act-org-id', label: copy.palette.copyOrgId, group: 'actions', run: hooks.copyOrgId, icon: 'copy' },
    { id: 'act-sign-out', label: copy.palette.signOut, group: 'actions', run: hooks.signOut, icon: 'log-out' },
  );
  return items;
}

const GROUP_ICON: Record<string, string> = { jump: 'arrow-up-right', goto: 'arrow-right', actions: 'command', agents: 'bot', recent: 'clock' };

export function openPalette(hooks: PaletteHooks): void {
  if (isOpen) return;
  isOpen = true;
  const t0 = performance.now();
  const opener = document.activeElement as HTMLElement | null;
  const input = h('input', {
    type: 'text', role: 'combobox', 'aria-expanded': 'true', 'aria-controls': 'pal-list', 'aria-autocomplete': 'list',
    'aria-label': copy.palette.placeholder, placeholder: copy.palette.placeholderHint, autocomplete: 'off', spellcheck: 'false', autocapitalize: 'off',
  });
  const list = h('div', { id: 'pal-list', role: 'listbox', 'aria-label': copy.palette.title });
  const scrim = h('div', { class: 'scrim' });
  const panel = h('div', { class: 'palette glass', role: 'dialog', 'aria-modal': 'true', 'aria-label': copy.palette.title },
    h('div', { class: 'p-input' }, icon('search', 20), input, h('kbd', { class: 'kbd' }, 'Esc')),
    list,
    h('div', { class: 'p-foot hide-sm' }, h('span', null, copy.palette.hint)));
  let items: Item[] = [];
  let active = 0;
  const all = (): Item[] => {
    const st = getState();
    const rec = st ? recent(st.org.id).map((r, i) => ({ id: `rec-${i}`, label: r.title, group: 'recent', href: r.href })) : [];
    const ags = (agentsCache ?? []).map((a) => ({ id: `agt-${a.id}`, label: a.name, group: 'agents', href: `/app/agents/${a.id}`, keywords: a.id }));
    return [...rec, ...baseItems(hooks), ...ags];
  };
  const render = () => {
    const q = input.value;
    const jump = idTarget(q);
    items = jump ? [{ id: 'jump', label: fill(copy.palette.jumpTo, { id: q.trim().toLowerCase() }), group: 'jump', href: jump }] : [];
    const ranked = rank(q, all(), 40);
    if (!q.trim()) {
      // Empty query: recent first, then go-to, then actions; agents only when searched.
      items.push(...ranked.filter((i) => i.group !== 'agents'));
    } else {
      // Keep each group together, ordered by its best match.
      const groups = new Map<string, Item[]>();
      for (const it of ranked) {
        const g = groups.get(it.group) ?? [];
        g.push(it);
        groups.set(it.group, g);
      }
      for (const g of groups.values()) items.push(...g);
    }
    active = Math.min(active, Math.max(0, items.length - 1));
    list.replaceChildren();
    if (items.length === 0) {
      list.append(h('div', { class: 'p-empty', role: 'presentation' }, copy.palette.empty));
      input.removeAttribute('aria-activedescendant');
      return;
    }
    let group = '';
    items.forEach((it, i) => {
      if (it.group !== group) {
        group = it.group;
        list.append(h('div', { class: 'group', role: 'presentation' }, copy.palette.groups[group] ?? group));
      }
      const opt = h('div', { id: `pal-${i}`, role: 'option', 'aria-selected': i === active ? 'true' : 'false' },
        icon(it.icon ?? GROUP_ICON[it.group] ?? 'arrow-right', 16), h('span', null, it.label),
        it.hint ? h('span', { class: 'hint', 'aria-hidden': 'true' }, ...it.hint.split(' ').map((k) => h('kbd', { class: 'kbd' }, k))) : null);
      opt.addEventListener('pointermove', () => select(i));
      opt.addEventListener('click', () => choose(i));
      list.append(opt);
    });
    input.setAttribute('aria-activedescendant', `pal-${active}`);
  };
  const select = (i: number) => {
    if (i === active) return;
    list.querySelector(`#pal-${active}`)?.setAttribute('aria-selected', 'false');
    active = i;
    const el = list.querySelector(`#pal-${active}`);
    el?.setAttribute('aria-selected', 'true');
    el?.scrollIntoView({ block: 'nearest' });
    input.setAttribute('aria-activedescendant', `pal-${active}`);
  };
  const close = (refocus = true) => {
    isOpen = false;
    scrim.remove();
    panel.remove();
    document.removeEventListener('keydown', key, true);
    for (const id of ['shell-root', 'bottombar']) {
      const el = document.getElementById(id);
      if (el) el.inert = false;
    }
    if (refocus) opener?.focus?.();
  };
  const choose = (i: number) => {
    const it = items[i];
    if (!it) return;
    close(!it.href);
    if (it.href) location.assign(it.href);
    else it.run?.();
  };
  const key = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      select((active + 1) % Math.max(1, items.length));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      select((active - 1 + items.length) % Math.max(1, items.length));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      choose(active);
    } else if (e.key === 'Tab') {
      e.preventDefault(); // focus stays in the input: the list is driven by the arrows
    }
  };
  input.addEventListener('input', () => {
    active = 0;
    render();
  });
  input.addEventListener('paste', () => setTimeout(() => {
    const jump = idTarget(input.value);
    if (jump) {
      close(false);
      location.assign(jump);
    }
  }));
  scrim.addEventListener('click', () => close());
  document.addEventListener('keydown', key, true);
  for (const id of ['shell-root', 'bottombar']) {
    const el = document.getElementById(id);
    if (el) el.inert = true;
  }
  document.body.append(scrim, panel);
  render();
  input.focus();
  if (import.meta.env.DEV) performance.measure('palette-open', { start: t0 });
  void agents().then(() => {
    if (isOpen && input.value.trim()) render();
  });
}
