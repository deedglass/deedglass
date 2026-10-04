// shell.ts: the app shell's behaviour (S24 §3.7): who is signed in, the org switcher, theme and
// account menus, Stop all and Start all, the banners, the connection line, the Approvals badge,
// the session-ended sheet, keyboard shortcuts and the palette. Roles only shape affordances; the
// API enforces every rule regardless.

import { api, ApiError } from '../lib/api.ts';
import { copy, fill } from '../lib/copy.ts';
import { byId, h, icon, maybe, replace } from '../lib/dom.ts';
import { cap, time } from '../lib/format.ts';
import { DESTS } from '../lib/nav.ts';
import { forgetAll, remember } from '../lib/recent.ts';
import { Sequencer } from '../lib/shortcuts.ts';
import { atLeast, get, load, onChange, refreshOrg, type State } from '../lib/state.ts';
import { stream, type StreamState } from '../lib/stream.ts';
import { getSimHint, getTheme, nextTheme, setSimHint, setTheme, type Theme } from '../lib/theme.ts';
import type { ListResponse, OrgStop } from '../lib/types.ts';
import { wireCopy } from './copyid.ts';
import { confirmTyped, menuButton, openPopover, openSheet, tooltips, type MenuItem } from './overlay.ts';
import { openPalette } from './palette.ts';
import { tickTimes } from './reltime.ts';
import { apiErrorToast, toast } from './toast.ts';

// ---------- live region ----------
export function announce(text: string): void {
  const r = maybe('live');
  if (!r) return;
  r.textContent = '';
  requestAnimationFrame(() => (r.textContent = text));
}

// ---------- theme ----------
const THEME_ICON: Record<Theme, string> = { dark: 'moon', light: 'sun', system: 'monitor' };

function themeItems(): MenuItem[] {
  const cur = getTheme();
  return (['dark', 'light', 'system'] as Theme[]).map((t) => ({
    label: { dark: copy.topbar.themeDark, light: copy.topbar.themeLight, system: copy.topbar.themeSystem }[t],
    icon: THEME_ICON[t], checked: t === cur, run: () => applyTheme(t),
  }));
}

function applyTheme(t: Theme): void {
  setTheme(t);
  const btn = maybe('theme-btn');
  if (btn) replace(btn, icon(THEME_ICON[t], 20));
}

// ---------- sign out ----------
async function signOut(everywhere = false): Promise<void> {
  try {
    await api('POST', '/v1/auth/logout', { body: everywhere ? { everywhere: true } : {} });
  } catch (e) {
    if (!(e instanceof ApiError) || e.status !== 401) {
      apiErrorToast(e);
      return;
    }
  }
  forgetAll();
  setSimHint(false);
  location.assign('/login');
}

// ---------- Stop all / Start all ----------
export function stopAll(): void {
  const s = get();
  if (!s) return;
  if (!atLeast(s.me.membership.role, 'admin')) {
    const btn = maybe('stopall');
    if (btn) openPopover(btn, [h('p', { class: 'm-0' }, fill(copy.stop.viewer, { org: s.org.name }))]);
    return;
  }
  confirmTyped({
    title: fill(copy.stop.allTitle, { org: s.org.name }),
    body: fill(copy.stop.allBody, { org: s.org.name }),
    expected: s.org.slug,
    expectedLabel: fill(copy.stop.allConfirm, { slug: s.org.slug }),
    reasonLabel: copy.stop.reasonOptional,
    confirmLabel: copy.stop.allButton,
    danger: true,
    onConfirm: async (reason) => {
      const out = await api<OrgStop>('POST', '/v1/org/stop-all', { body: reason ? { reason } : {} });
      toast(fill(copy.stop.stoppedToast, { org: s.org.name }), { kind: 'success', action: out.deed ? { label: copy.stop.viewDeed, href: `/app/deeds/${out.deed}` } : undefined });
      announce(fill(copy.stop.stoppedToast, { org: s.org.name }));
      await refreshOrg();
    },
  });
}

export function startAll(): void {
  const s = get();
  if (!s) return;
  confirmTyped({
    title: fill(copy.stop.startTitle, { org: s.org.name }),
    body: copy.stop.startBody,
    reasonLabel: copy.stop.startReason,
    reasonRequired: true,
    confirmLabel: copy.stop.startButton,
    onConfirm: async (reason) => {
      const out = await api<OrgStop>('POST', '/v1/org/start-all', { body: { reason } });
      toast(fill(copy.stop.startedToast, { org: s.org.name }), { kind: 'success', action: out.deed ? { label: copy.stop.viewDeed, href: `/app/deeds/${out.deed}` } : undefined });
      await refreshOrg();
    },
  });
}

function renderStopButton(s: State): void {
  const slot = maybe('stop-slot');
  if (!slot) return;
  const stopped = !!s.org.stop?.stopped;
  const role = s.me.membership.role;
  if (stopped) {
    if (role === 'owner') {
      replace(slot, h('button', { type: 'button', class: 'btn btn-primary btn-sm', id: 'startall', onclick: startAll }, icon('refresh-cw', 16),
        h('span', { class: 'hide-sm' }, copy.topbar.startAll), h('span', { class: 'show-sm' }, 'Start all')));
    } else {
      slot.replaceChildren();
    }
    return;
  }
  const canStop = atLeast(role, 'admin');
  const btn = h('button', {
    type: 'button', class: 'btn btn-danger-outline btn-sm', id: 'stopall', 'aria-label': copy.topbar.stopAll,
    ...(canStop ? {} : { 'aria-disabled': 'true' }),
  }, icon('stop', 16), h('span', { class: 'hide-sm' }, copy.topbar.stopAll), h('span', { class: 'show-sm' }, 'Stop all'));
  btn.addEventListener('click', stopAll);
  replace(slot, btn);
}

// ---------- banners ----------
// S28: a simulated org (deedglass dev simulate) shows a neutral bar on every screen. GET /v1/org's
// settings.simulated sets it; a screen that learns it another way (Insights' settings) calls this.
// S29: until the org loads, the last org's flag (theme.ts getSimHint) draws it at boot, so the
// banner is in place before page content and nothing shifts when GET /v1/org answers.
let simulated = false;
let lastOffline = false;
export function setSimulated(on: boolean): void {
  if (simulated === on) return;
  simulated = on;
  renderBanners(get(), lastOffline);
}

function showsSim(s: State | null): boolean {
  if (!s) return getSimHint();
  const on = simulated || (s.org as { settings?: { simulated?: unknown } | null }).settings?.simulated === true;
  setSimHint(on);
  return on;
}

function renderBanners(s: State | null, offline: boolean): void {
  lastOffline = offline;
  const host = maybe('banners');
  if (!host) return;
  const out: HTMLElement[] = [];
  if (offline) {
    out.push(h('div', { class: 'banner banner-offline', role: 'status' }, icon('wifi-off', 20), h('div', { class: 'body' }, h('p', null, copy.states.offlineBanner))));
  }
  if (s?.org.stop?.stopped) {
    const st = s.org.stop;
    const parts = [fill(copy.stop.stoppedBanner, { org: s.org.name })];
    if (st.stopped_at) parts.push(fill(copy.stop.stoppedSince, { time: time(st.stopped_at, s.org.timezone) }));
    if (st.stopped_by) parts.push(fill(copy.stop.stoppedBy, { who: st.stopped_by }));
    out.push(h('div', { class: 'banner banner-stopped', role: 'status', id: 'banner-stopped' }, icon('stop', 20),
      h('div', { class: 'body' }, h('p', null, h('strong', null, parts.join(' · '))), st.reason ? h('p', { class: 't-2' }, `“${st.reason}”`) : null,
        s.me.membership.role === 'owner' ? null : h('p', { class: 't-2' }, copy.stop.startOwnerOnly))));
  }
  if (showsSim(s)) {
    out.push(h('div', { class: 'banner banner-sim', role: 'note', id: 'banner-simulated' }, icon('flask-conical', 20),
      h('div', { class: 'body' }, h('p', null, copy.states.simulatedBanner))));
  }
  if (s && s.me.membership.role === 'viewer') {
    out.push(h('div', { class: 'banner banner-readonly', role: 'note' }, icon('lock', 20),
      h('div', { class: 'body' }, h('p', null, fill(copy.states.viewerBanner, { org: s.org.name })))));
  }
  host.replaceChildren(...out);
}

// ---------- connection line ----------
function renderConn(st: StreamState, retryIn: number): void {
  const el = maybe('conn');
  if (!el) return;
  el.setAttribute('data-state', st);
  const label = maybe('conn-label');
  const text = st === 'live' ? copy.connection.live
    : st === 'reconnecting' ? fill(copy.connection.reconnecting, { s: Math.max(1, retryIn) })
    : st === 'offline' ? copy.connection.offlineShort
    : st === 'ended' ? copy.connection.ended
    : copy.connection.connecting;
  if (label) label.textContent = text;
  el.setAttribute('title', st === 'offline' ? copy.connection.offline : text);
}

// ---------- approvals badge ----------
let badgeTimer = 0;
async function refreshBadge(): Promise<void> {
  try {
    const page = await api<ListResponse<unknown>>('GET', '/v1/approvals', { query: { status: 'pending', limit: '100' } });
    const n = page.data.length + (page.has_more ? 1 : 0);
    for (const b of document.querySelectorAll<HTMLElement>('[data-badge="approvals"]')) {
      b.textContent = page.has_more ? '99+' : cap(n);
      b.hidden = n === 0;
      b.setAttribute('aria-label', n === 1 ? copy.nav.approvalsBadgeOne : fill(copy.nav.approvalsBadge, { n: page.has_more ? '99+' : n }));
    }
    for (const d of document.querySelectorAll<HTMLElement>('[data-badge-dot="approvals"]')) d.hidden = n === 0;
  } catch {
    /* the badge stays as it was; the page itself reports errors */
  }
}

// ---------- org switcher and account ----------
function orgItems(s: State): MenuItem[] {
  return s.me.orgs.map((o, i) => ({
    label: o.name, meta: copy.topbar.role[o.role] ?? o.role, checked: o.id === s.org.id, section: i === 0 ? copy.topbar.switchOrg : undefined,
    run: async () => {
      if (o.id === s.org.id) return;
      try {
        await api('POST', '/v1/auth/switch-org', { body: { org: o.id } });
        location.assign('/app');
      } catch (e) {
        apiErrorToast(e);
      }
    },
  }));
}

function accountItems(s: State): MenuItem[] {
  const who = s.me.user.name || s.me.user.email;
  return [
    { label: who, icon: 'user', meta: copy.topbar.role[s.me.membership.role], section: s.me.user.email !== who ? s.me.user.email : undefined },
    { label: copy.topbar.theme, icon: THEME_ICON[getTheme()], divider: true, run: () => applyTheme(nextTheme(getTheme())), meta: { dark: copy.topbar.themeDark, light: copy.topbar.themeLight, system: copy.topbar.themeSystem }[getTheme()] },
    { label: copy.topbar.signOut, icon: 'log-out', divider: true, run: () => void signOut(false) },
    { label: copy.topbar.signOutEverywhere, icon: 'log-out', run: () => void signOut(true) },
  ];
}

function initials(name: string, email: string): string {
  const src = name.trim() || email.split('@')[0] || '?';
  const parts = src.split(/[\s._-]+/).filter(Boolean);
  return ((parts[0]?.[0] ?? '') + (parts[1]?.[0] ?? '')).toUpperCase() || '?';
}

function renderIdentity(s: State): void {
  const org = maybe('org-name');
  if (org) org.textContent = s.org.name;
  const av = maybe('avatar');
  if (av) av.textContent = initials(s.me.user.name, s.me.user.email);
  const acc = maybe('account-btn');
  if (acc) acc.setAttribute('aria-label', `${copy.topbar.account}: ${s.me.user.name || s.me.user.email}`);
  renderStopButton(s);
}

// ---------- session ended ----------
let sessionSheetOpen = false;
function sessionEnded(): void {
  if (sessionSheetOpen) return;
  sessionSheetOpen = true;
  const next = encodeURIComponent(location.pathname + location.search);
  const go = h('a', { class: 'btn btn-primary', href: `/login?next=${next}` }, copy.states.signIn);
  openSheet({ kind: 'dialog', title: copy.states.sessionEnded, body: [], foot: [go], initialFocus: () => go, role: 'alertdialog', onClose: () => (sessionSheetOpen = false) });
}

// ---------- the More sheet (phones) ----------
function openMore(): void {
  const cur = document.body.getAttribute('data-screen');
  const links = DESTS.filter((d) => !['overview', 'actions', 'approvals', 'agents'].includes(d.key)).map((d) =>
    h('a', { class: 'nav-item', href: d.href, ...(cur === d.key ? { 'aria-current': 'page' } : {}) }, icon(d.icon, 20), h('span', { class: 'label' }, d.label)));
  const s = get();
  const extra: HTMLElement[] = [];
  if (s) {
    extra.push(h('a', { class: 'nav-item', href: 'https://docs.deedglass.com/' }, icon('external-link', 20), h('span', { class: 'label' }, copy.nav.docs)));
  }
  openSheet({ title: copy.nav.more, body: [h('nav', { class: 'nav', 'aria-label': copy.nav.more }, ...links, ...extra)] });
}

// ---------- boot ----------
export interface ShellOptions {
  title: string;
}

export function bootShell(o: ShellOptions): void {
  // First, in this task: the simulated banner from the last org's flag, then reveal the page (app.css
  // keeps .main hidden until data-shell is set), so the banner never pushes painted content down.
  renderBanners(null, false);
  document.body.setAttribute('data-shell', 'ready');
  wireCopy();
  tooltips();
  tickTimes();

  // The top bar turns to glass once content scrolls under it (no scroll listener).
  const sentinel = maybe('top-sentinel');
  const bar = maybe('topbar');
  if (sentinel && bar && 'IntersectionObserver' in window) {
    new IntersectionObserver(([e]) => {
      const scrolled = !e?.isIntersecting;
      bar.toggleAttribute('data-scrolled', scrolled);
      bar.classList.toggle('glass', scrolled);
    }).observe(sentinel);
  }

  const themeBtn = maybe('theme-btn');
  if (themeBtn) {
    replace(themeBtn, icon(THEME_ICON[getTheme()], 20));
    menuButton(themeBtn, copy.topbar.theme, themeItems);
  }
  const paletteHooks = {
    stopAll,
    toggleTheme: () => applyTheme(nextTheme(getTheme())),
    signOut: () => void signOut(false),
    copyOrgId: () => {
      const s = get();
      if (s) void navigator.clipboard.writeText(s.org.id).then(() => toast(fill(copy.toast.copiedValue, { value: s.org.id }), { kind: 'success' }));
    },
  };
  for (const id of ['search-btn', 'search-btn-sm']) maybe(id)?.addEventListener('click', () => openPalette(paletteHooks));
  maybe('more-btn')?.addEventListener('click', openMore);

  // Keyboard: ⌘K / Ctrl+K and "/" open the palette, "g x" goes, "?" lists them.
  const seq = new Sequencer();
  document.addEventListener('keydown', (e) => {
    if (e.defaultPrevented || document.querySelector('.palette, .sheet, .dialog, .menu')) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k' && !document.querySelector('.palette')) e.preventDefault();
      return;
    }
    const cmd = seq.key(e);
    if (!cmd) return;
    e.preventDefault();
    if (cmd.kind === 'palette') openPalette(paletteHooks);
    else if (cmd.kind === 'go') location.assign(cmd.href);
    else showShortcuts();
  });

  window.addEventListener('dg:session-ended', sessionEnded);
  let offline = !navigator.onLine;
  window.addEventListener('dg:offline', () => {
    offline = true;
    renderBanners(get(), true);
  });
  window.addEventListener('dg:online', () => {
    if (!offline) return;
    offline = false;
    renderBanners(get(), false);
  });

  load()
    .then((s) => {
      remember(s.org.id, { href: location.pathname + location.search, title: o.title });
      const orgBtn = maybe('org-btn');
      if (orgBtn) menuButton(orgBtn, copy.topbar.switchOrg, () => orgItems(get() ?? s), 'start');
      const acc = maybe('account-btn');
      if (acc) menuButton(acc, copy.topbar.account, () => accountItems(get() ?? s));
      onChange((st) => {
        renderIdentity(st);
        renderBanners(st, offline);
      });
      document.body.setAttribute('data-role', s.me.membership.role);
      document.dispatchEvent(new CustomEvent('dg:ready', { detail: s }));

      // Live: the connection line, the badge, and the org's Stop all state.
      const live = stream();
      let tick = 0;
      const show = (st: StreamState) => {
        renderConn(st, live.retryIn());
        clearInterval(tick);
        if (st === 'reconnecting') tick = window.setInterval(() => renderConn('reconnecting', live.retryIn()), 1000);
        if (st === 'offline') {
          offline = true;
          renderBanners(get(), true);
        } else if (offline && st === 'live') {
          offline = false;
          renderBanners(get(), false);
        }
      };
      show(live.state());
      live.onState(show);
      void refreshBadge();
      live.on('approval.*', () => {
        clearTimeout(badgeTimer);
        badgeTimer = window.setTimeout(() => void refreshBadge(), 150);
      });
      live.on('org.*', () => void refreshOrg().catch(() => undefined));
      live.onResync(() => {
        void refreshBadge();
        void refreshOrg().catch(() => undefined);
      });
    })
    .catch((e) => {
      if (e instanceof ApiError && e.status === 401) {
        sessionEnded();
        return;
      }
      document.dispatchEvent(new CustomEvent('dg:load-failed', { detail: e }));
      apiErrorToast(e);
    });
}

// S28: a screen adds its own keys to the ? sheet (Insights: j/k, Enter, a, r).
let pageKeys: { title: string; rows: [string, string][] } | null = null;
export function setPageShortcuts(title: string, rows: [string, string][]): void {
  pageKeys = { title, rows };
}

const keyList = (rows: [string, string][]) => h('dl', { class: 'kv' }, ...rows.flatMap(([k, v]) => [h('dt', null, ...k.split(' ').map((x) => h('kbd', { class: 'kbd' }, x))), h('dd', null, v)]));

function showShortcuts(): void {
  const rows: [string, string][] = [
    ['⌘K', copy.shortcuts.open],
    ['/', copy.shortcuts.open],
    ['?', copy.shortcuts.help],
    ...DESTS.map((d) => [d.go, fill(copy.shortcuts.goTo, { screen: d.label })] as [string, string]),
    ['j k', copy.shortcuts.move],
    ['Enter', copy.shortcuts.enter],
    ['Esc', copy.shortcuts.esc],
  ];
  const page = pageKeys ? [h('h3', { class: 't-footnote t-2 m-0' }, pageKeys.title), keyList(pageKeys.rows)] : [];
  openSheet({ title: copy.shortcuts.title, body: [...page, keyList(rows), h('p', null, copy.shortcuts.note)] });
}

/** byIdSafe: used by pages that know their markup. */
export { byId };
