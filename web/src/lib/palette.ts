// palette.ts: the command palette's pure parts (S24 §3.8): id detection and fuzzy scoring. The
// overlay that uses them is src/controllers/palette.ts.

/** The id prefixes the palette jumps to, and the screen each opens. */
export const ID_SCREENS: Record<string, string> = {
  act: 'actions', deed: 'deeds', agt: 'agents', apr: 'approvals', pol: 'policies', pv: 'policies', con: 'connectors',
  key: 'agents', sug: 'insights', anm: 'insights',
};

export const ID_RE = /^(act|deed|agt|apr|pol|pv|con|key|sug|anm)_[0-9a-hjkmnp-tv-z]{26}$/;

/** idTarget returns the Console path a pasted id opens, or null. Keys, versions, suggestions and
 * anomalies open their screen with the id as a query, because they have no page of their own. */
export function idTarget(raw: string): string | null {
  const id = raw.trim().toLowerCase();
  const m = ID_RE.exec(id);
  if (!m) return null;
  const prefix = m[1]!;
  const screen = ID_SCREENS[prefix]!;
  if (prefix === 'key' || prefix === 'pv' || prefix === 'sug' || prefix === 'anm') return `/app/${screen}?id=${id}`;
  return `/app/${screen}/${id}`;
}

/**
 * score is a subsequence match: every query character must appear in order. It rewards matches
 * at word starts and consecutive runs, and an exact prefix most. Higher is better; -1 is no match.
 */
export function score(query: string, text: string): number {
  const q = query.trim().toLowerCase();
  const t = text.toLowerCase();
  if (!q) return 0;
  if (t === q) return 1000;
  let s = 0;
  let ti = 0;
  let run = 0;
  for (let qi = 0; qi < q.length; qi++) {
    const c = q[qi]!;
    let found = -1;
    for (let j = ti; j < t.length; j++) {
      if (t[j] === c) {
        found = j;
        break;
      }
    }
    if (found < 0) return -1;
    const wordStart = found === 0 || /[\s\-_./›]/.test(t[found - 1]!);
    run = found === ti && qi > 0 ? run + 1 : 0;
    s += 1 + (wordStart ? 8 : 0) + run * 4 - Math.min(found - ti, 6) * 0.5;
    ti = found + 1;
  }
  if (t.startsWith(q)) s += 50;
  return s - t.length * 0.05;
}

export interface Item {
  id: string;
  label: string;
  group: string;
  keywords?: string;
  href?: string;
  run?: () => void;
  hint?: string;
  icon?: string;
}

/** rank filters and orders items for a query; an empty query keeps the given order. */
export function rank(query: string, items: readonly Item[], limit = 50): Item[] {
  if (!query.trim()) return items.slice(0, limit);
  return items
    .map((it, i) => ({ it, i, s: Math.max(score(query, it.label), it.keywords ? score(query, it.keywords) - 5 : -1) }))
    .filter((x) => x.s >= 0)
    .sort((a, b) => b.s - a.s || a.i - b.i)
    .slice(0, limit)
    .map((x) => x.it);
}
