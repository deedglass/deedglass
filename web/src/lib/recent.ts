// recent.ts: the palette's Recent group: the last 8 Console pages, in sessionStorage per org,
// cleared on sign-out (S24 §3.8). Only paths and titles; never data.

const KEY = 'deedglass.recent.';
const MAX = 8;

export interface Recent {
  href: string;
  title: string;
}

function storage(): Storage | null {
  try {
    return typeof sessionStorage === 'undefined' ? null : sessionStorage;
  } catch {
    return null;
  }
}

export function recent(org: string): Recent[] {
  try {
    const v = JSON.parse(storage()?.getItem(KEY + org) ?? '[]') as unknown;
    return Array.isArray(v) ? (v as Recent[]).filter((r) => typeof r?.href === 'string' && r.href.startsWith('/app')).slice(0, MAX) : [];
  } catch {
    return [];
  }
}

export function remember(org: string, r: Recent): void {
  try {
    const list = [r, ...recent(org).filter((x) => x.href !== r.href)].slice(0, MAX);
    storage()?.setItem(KEY + org, JSON.stringify(list));
  } catch {
    /* storage blocked: no recents */
  }
}

export function forgetAll(): void {
  try {
    const s = storage();
    if (!s) return;
    for (let i = s.length - 1; i >= 0; i--) {
      const k = s.key(i);
      if (k?.startsWith(KEY)) s.removeItem(k);
    }
  } catch {
    /* nothing to clear */
  }
}
