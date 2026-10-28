// simple.ts: the Simple view's router (DL-143). Three panels on one page, switched with pushState so
// the sidebar, the tabs and the bottom bar change at once and Back works; a reload or a shared link
// lands on the same panel. Each panel mounts the first time it shows.

import { simpleCopy as c } from '../lib/copy-simple.ts';
import { byId } from '../lib/dom.ts';
import { setView } from '../lib/theme.ts';
import { announce } from './shell.ts';
import type { Records } from './simplerecords.ts';

export type Panel = 'activity' | 'waiting' | 'records';
const PANELS: readonly Panel[] = ['activity', 'waiting', 'records'];
const TITLES: Record<Panel, string> = { activity: c.feed.title, waiting: c.waiting.title, records: c.records.title };

export function panelOf(search: string): Panel {
  const v = new URLSearchParams(search).get('show');
  return (PANELS as readonly string[]).includes(v ?? '') ? (v as Panel) : 'activity';
}

export function hrefOf(p: Panel, extra: Record<string, string> = {}): string {
  const q = new URLSearchParams(p === 'activity' ? {} : { show: p });
  for (const [k, v] of Object.entries(extra)) q.set(k, v);
  const qs = q.toString();
  return qs ? `/app/simple?${qs}` : '/app/simple';
}

const mounted = new Set<Panel>();
let records: Records | null = null;

function show(p: Panel, o: { focus: boolean }): void {
  for (const key of PANELS) byId(`sv-${key}`).hidden = key !== p;
  const navKey = p;
  for (const a of document.querySelectorAll<HTMLAnchorElement>('.nav-simple a[data-nav], .bottombar-simple a[data-nav]')) {
    if (a.dataset.nav === navKey) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
  for (const a of document.querySelectorAll<HTMLAnchorElement>('#sv-tabs a[data-show]')) {
    if (a.dataset.show === p) a.setAttribute('aria-current', 'page');
    else a.removeAttribute('aria-current');
  }
  document.body.dataset.screen = p;
  document.title = `${TITLES[p]} · Deedglass`;
  if (!mounted.has(p)) {
    mounted.add(p);
    // Each panel loads on first view, so the page's first paint carries only the one shown.
    if (p === 'activity') void import('./simplefeed.ts').then((m) => m.mountFeed(byId('sv-body-activity')));
    else if (p === 'waiting') void import('./simplewaiting.ts').then((m) => m.mountWaiting(byId('sv-body-waiting')));
    else {
      const deed = new URLSearchParams(location.search).get('deed');
      void import('./simplerecords.ts').then((m) => {
        records = m.mountRecords(byId('sv-body-records'), deed);
      });
    }
  }
  if (o.focus) {
    requestAnimationFrame(() => byId(`sv-h-${p}`).focus());
    announce(TITLES[p]);
  }
}

/** go switches panels in place (used by links inside the panels too). */
export function go(p: Panel, extra: Record<string, string> = {}): void {
  history.pushState({ panel: p }, '', hrefOf(p, extra));
  const had = mounted.has('records');
  show(p, { focus: true });
  if (p === 'records' && extra.deed && had && records) records.open(extra.deed);
  window.scrollTo({ top: 0 });
}

export function mountSimple(): void {
  setView('simple');
  // Links to another panel of this page switch in place.
  document.addEventListener('click', (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const a = (e.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null;
    if (!a) return;
    const u = new URL(a.href, location.href);
    if (u.origin !== location.origin || u.pathname !== '/app/simple') return;
    e.preventDefault();
    const extra: Record<string, string> = {};
    const deed = u.searchParams.get('deed');
    if (deed) extra.deed = deed;
    go(panelOf(u.search), extra);
  });
  window.addEventListener('popstate', () => show(panelOf(location.search), { focus: true }));
  show(panelOf(location.search), { focus: false });
}
