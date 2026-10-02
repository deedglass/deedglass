// seal.ts: the Sealed moment's play rules (S24 §3.4): only on a live transition, once per deed id
// per page, at most 3 plays a second across the page; the rest jump to their final state.

export class SealGovernor {
  private played = new Set<string>();
  private stamps: number[] = [];
  constructor(private readonly now: () => number = () => performance.now(), private readonly perSecond = 3) {}

  /** mayPlay answers whether this deed's live seal should animate (and records it). */
  mayPlay(id: string, live: boolean): boolean {
    if (!live || this.played.has(id)) return false;
    this.played.add(id);
    const t = this.now();
    this.stamps = this.stamps.filter((s) => t - s < 1000);
    if (this.stamps.length >= this.perSecond) return false;
    this.stamps.push(t);
    return true;
  }
}
