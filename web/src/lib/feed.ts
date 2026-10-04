// feed.ts: the live Actions feed as a pure model (S25 §3.4). The DOM layer (controllers/actions.ts)
// renders `order` from `rows`; this file decides what an event does: insert at the top, update a row
// in place, buffer it behind the "N new actions" pill, or ignore it. No DOM, no timers: tested in
// test/feed.test.ts.

import { matches, type Filters } from './filters.ts';
import type { Action, DeedglassEvent } from './types.ts';

export const FEED_CAP = 500;

export type ApplyResult = 'inserted' | 'updated' | 'buffered' | 'ignored';

export class FeedModel {
  rows = new Map<string, Action>();
  order: string[] = []; // newest first, at most FEED_CAP
  pending = new Set<string>(); // arrived while paused or scrolled away
  private buffered = new Map<string, Action>();
  paused = false;
  atTop = true;

  constructor(private filters: Filters, private readonly now: () => Date = () => new Date()) {}

  setFilters(f: Filters): void {
    this.filters = f;
  }

  /** load replaces the rows with a page from the API (newest first). */
  load(page: Action[]): void {
    this.rows.clear();
    this.order = [];
    this.pending.clear();
    this.buffered.clear();
    this.append(page);
  }

  /** append adds an older page below (Load older actions). */
  append(page: Action[]): void {
    for (const a of page) {
      if (this.rows.has(a.id)) continue;
      this.rows.set(a.id, a);
      this.order.push(a.id);
    }
    this.trim();
  }

  /** live is true when new rows go straight in (not paused, scrolled to the top, no text search). */
  get live(): boolean {
    return !this.paused && this.atTop && !this.filters.q;
  }

  apply(e: DeedglassEvent<Partial<Action>>): ApplyResult {
    if (!e.type.startsWith('action.') && e.type !== 'deed.sealed') return 'ignored';
    const obj = e.data.object as Partial<Action> & { action?: string };
    const id = typeof obj.id === 'string' && obj.id.startsWith('act_') ? obj.id : typeof obj.action === 'string' ? obj.action : '';
    if (!id) return 'ignored';
    const known = this.rows.get(id) ?? this.buffered.get(id);
    if (known) {
      // Thin events (ids only) and non-action objects can't move a row's state.
      if (!e.hydrated || obj.object !== 'action') return 'ignored';
      const next = { ...known, ...(obj as Action) };
      if (this.rows.has(id)) {
        this.rows.set(id, next);
        return 'updated';
      }
      this.buffered.set(id, next);
      return 'buffered';
    }
    if (!e.hydrated || obj.object !== 'action') return 'ignored';
    const a = obj as Action;
    if (!matches(this.filters, a, this.now())) return 'ignored';
    if (!this.live) {
      this.buffered.set(id, a);
      this.pending.add(id);
      return 'buffered';
    }
    this.rows.set(id, a);
    this.order.unshift(id);
    this.trim();
    return 'inserted';
  }

  /** flush shows the buffered rows at the top (the pill's Show, or Resume). */
  flush(): string[] {
    const ids = [...this.pending].filter((id) => this.buffered.has(id));
    // Arrival order: the newest arrived last, so it goes on top.
    for (const id of ids) {
      this.rows.set(id, this.buffered.get(id)!);
      this.order.unshift(id);
    }
    this.pending.clear();
    this.buffered.clear();
    this.trim();
    return ids;
  }

  private trim(): void {
    while (this.order.length > FEED_CAP) {
      const id = this.order.pop()!;
      this.rows.delete(id);
    }
  }
}

/** anchorAfterResync picks the row to keep in view after a refetch: the first visible id that survived. */
export function anchorAfterResync(visible: string[], fresh: string[]): string | null {
  const set = new Set(fresh);
  for (const id of visible) if (set.has(id)) return id;
  return null;
}
