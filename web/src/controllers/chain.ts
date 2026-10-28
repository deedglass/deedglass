// chain.ts: the Chain card's body (S25 §3.3), shared by the Overview and the Deeds banner.
import { copy, fill } from '../lib/copy.ts';
import { copyChip } from '../lib/chips.ts';
import { h, icon, type Child } from '../lib/dom.ts';
import { number } from '../lib/format.ts';
import type { LedgerCheck } from '../lib/types.ts';

/** chainView: the Chain card's body (also the Deeds banner). */
export function chainView(c: LedgerCheck): Child[] {
  if (c.status === 'broken' && c.break) {
    return [h('p', { class: 'chain-line is-broken' }, icon('x', 16), fill(copy.overview.chainBroken, { seq: number(c.break.seq ?? 0), sentence: c.break.message }))];
  }
  if (c.checked === 0 || !c.head) return [h('p', { class: 'chain-line t-2' }, icon('circle', 16), copy.overview.chainEmpty)];
  const out: Child[] = [];
  if (c.status === 'incomplete') {
    out.push(h('p', { class: 'chain-line is-incomplete' }, icon('circle-dashed', 16), fill(copy.overview.chainIncomplete, { n: number(c.checked), next: number(c.next_from_seq ?? 0) })));
  } else {
    out.push(h('p', { class: 'chain-line' }, icon('check', 16), fill(copy.overview.chainIntact, { n: number(c.checked), head: number(c.head.seq) })));
  }
  out.push(h('p', { class: 'chain-head' },
    copyChip(c.head.body_hash, `${c.head.body_hash.slice(0, 11)}…${c.head.body_hash.slice(-4)}`)),
  h('p', { class: 't-footnote t-2 chain-note' }, copy.overview.chainNote));
  return out;
}

